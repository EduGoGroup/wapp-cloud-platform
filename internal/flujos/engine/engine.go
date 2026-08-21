// Package engine es el núcleo PURO de la máquina de estados: dadas una
// definición y un estado, evalúa el nodo actual y produce el estado siguiente
// y las salidas. No conoce transporte ni base de datos (design.md §3).
//
// El engine delega en el módulo registrado para cada tipo de nodo (los nodos
// "menu" los maneja modules/menu: render, validación de opción, transición y
// reprompt acotado). Los nodos "message" son triviales y los encadena el propio
// engine: emite el Text y sigue por Next hasta llegar a un menú (que espera
// input) o a Next == nil (que termina el flujo, marcando CurrentNode con el
// centinela model.NodeTerminal).
package engine

import (
	"context"
	"fmt"
	"sort"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// Input es la entrada normalizada del usuario. En este corte, el texto del
// IncomingMessage.
type Input struct {
	Text string
}

// Output es una orden de respuesta: texto a enviar por SendText o —si Media no es
// nil— un adjunto a despachar por SendMedia.
type Output struct {
	Text string
	// Media, si no es nil, es un adjunto DECLARADO por un módulo de SALIDA (p. ej.
	// "media") que el runtime debe presignar y despachar por Sender.SendMedia en
	// vez de SendText (Plan 017 §9.C, seam consumido en T4). El engine lo transporta
	// OPACO: no lo interpreta (igual que hoy transporta Text). Es un tipo de model
	// (neutral) para no importar el paquete del módulo (dirección hexagonal).
	Media *model.MediaRef
}

// Engine es la máquina de estados. Mantiene el registro de módulos para delegar
// el manejo de cada tipo de nodo. Es inmutable tras construirse y seguro para
// uso concurrente (no guarda estado por conversación; el estado lo lleva el
// Conversation que recibe cada llamada).
type Engine struct {
	reg *modules.Registry
	// content resuelve el model.Content de cada nodo ANTES del Render (Plan 015,
	// T1). Nunca es nil: New lo inicializa al adapter estático (PURO) si no se
	// inyecta otro con WithContentSource, de modo que el engine sigue testeable
	// sin BD y el observable es idéntico al placeholder inline de T0.
	content content.Source
}

// Option configura el Engine al construirlo (patrón functional-options, igual
// que gatewaygrpc.Server).
type Option func(*Engine)

// WithContentSource inyecta la fuente de contenido (puerto ContentSource). Sin
// ella, New usa el adapter estático (PURO) por defecto: contenido copiado del
// propio nodo, sin I/O.
func WithContentSource(src content.Source) Option { return func(e *Engine) { e.content = src } }

// New construye el engine con el registro de módulos ya poblado. La fuente de
// contenido es OPCIONAL (WithContentSource); por defecto es el adapter estático.
func New(reg *modules.Registry, opts ...Option) *Engine {
	e := &Engine{reg: reg}
	for _, opt := range opts {
		opt(e)
	}
	// La fuente de contenido nunca es nil: estática (PURA) por defecto (T1).
	if e.content == nil {
		e.content = content.NewStatic()
	}
	return e
}

// FlowProducesDurableContent decide si f exige un evento padre para arrancar
// (design.md D-054.5): es un OR sobre f.Nodes (map[string]model.Node,
// model.go:95) que resuelve cada n.Type contra el Registry y devuelve true en
// cuanto ALGÚN nodo resuelve a un módulo cuyo ProducesDurableContent() es
// true. Un tipo de nodo NO registrado en e.reg cuenta como NO durable —no hay
// módulo que pueda materializar nada—, así que un flujo con un tipo
// desconocido no bloquea el arranque por esta guarda (D-054.5, §1).
//
// Vive en el Engine, no en el Runtime ni en una Option nueva. runtime.Runtime
// declara `engine *engine.Engine` (tipo CONCRETO, no una interfaz) y el
// Registry es un campo PRIVADO de Engine, inyectado una única vez en New(reg,
// opts...): el Runtime no tiene forma de preguntarle nada al Registry por su
// cuenta (un grep de "Registry" sobre runtime_engine.go no devuelve ni una
// coincidencia). Exponer el predicado aquí evita DOS caminos alternativos, los
// dos descartados a propósito:
//
//   - Una Option nueva en Runtime (p. ej. WithRegistry) para pasarle el
//     Registry por fuera: sería una TERCERA Option variádica cuya omisión
//     compila, pasa `go vet` y pasa el lint sin dejar ni una señal roja —
//     exactamente el modo de fallo que dejó WithOpeningBuilder sin cablear en
//     bootstrap.go durante meses (T1 de este mismo plan) y que costó dos
//     comandas perdidas en UAT (hallazgos #001/#003). Este plan existe para
//     cerrar esa clase de defecto, no para añadir un ejemplar nuevo.
//   - Duplicar el Registry como campo propio de Runtime: Runtime YA recibe el
//     *engine.Engine completo por parámetro posicional obligatorio del
//     constructor (no por Option), así que el Registry viaja gratis dentro de
//     él. Un segundo puntero al mismo Registry en Runtime sería estado
//     redundante que podría desincronizarse del que usa el propio Engine para
//     Render/Step.
//
// startLocked (runtime/start.go), el ÚNICO embudo por el que pasan las cuatro
// puertas de arranque, consulta este método a través de rt.engine —que ya
// tiene inyectado— sin que bootstrap.go necesite una sola línea nueva de
// wiring.
func (e *Engine) FlowProducesDurableContent(f model.Flow) bool {
	for _, n := range f.Nodes {
		if m, ok := e.reg.Get(n.Type); ok && m.ProducesDurableContent() {
			return true
		}
	}
	return false
}

// NodeProducesDurableContent es FlowProducesDurableContent acotado a UN tipo de
// nodo (Plan 054 · T3, D-054.4): el fan-out best-effort del ADR-0003 gana una
// excepción acotada SOLO para los efectos que declaró un módulo durable, y cada
// llamante de runtime.dispatch (startLocked, advanceLiveStep, prepareResume,
// restartableOnStart) conoce el nodo/módulo que produjo el LOTE de efectos que
// va a despachar antes de llamarlo —Step/EnterPrimed/Restart procesan UN nodo
// por turno, así que todo efecto de un mismo lote comparte el mismo módulo—, sin
// tener que reconstruir un model.Flow de un solo nodo para reusar
// FlowProducesDurableContent. Mismo criterio exacto: un tipo no registrado en
// e.reg cuenta como NO durable (D-054.3(a): «no hay módulo que pueda producir
// nada»).
func (e *Engine) NodeProducesDurableContent(nodeType string) bool {
	m, ok := e.reg.Get(nodeType)
	return ok && m.ProducesDurableContent()
}

// DurableNodeType nombra a QUIÉN culpar cuando FlowProducesDurableContent(f) da
// true: el tipo del primer nodo (en orden determinista por clave — f.Nodes es un
// map y su iteración no lo es) que resuelve a un módulo durable. Devuelve "" si
// ninguno lo es (mismo criterio exacto que FlowProducesDurableContent; es un
// segundo recorrido sobre el MISMO Registry, no una fuente de verdad nueva).
//
// Existe SOLO para diagnóstico (Plan 054 · D-054.6, T2.4): el WARN de la
// degradación necesita decir qué nodo obligó al rechazo, y la guarda de D-054.5 ya
// contestó "¿hace falta evento?" con el OR de FlowProducesDurableContent — esto
// responde "¿de cuál nodo?" para el operador que lee el log. Con varios nodos
// durables se reporta uno cualquiera (determinista): saber que el flujo exige
// evento ya basta para corregirlo, no hace falta enumerarlos todos en cada línea.
func (e *Engine) DurableNodeType(f model.Flow) string {
	ids := make([]string, 0, len(f.Nodes))
	for id := range f.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := f.Nodes[id]
		if m, ok := e.reg.Get(n.Type); ok && m.ProducesDurableContent() {
			return n.Type
		}
	}
	return ""
}

// Enter posiciona la conversación en el nodo inicial del flujo y produce su
// render, encadenando nodos "message" hasta el primer "menu" o el fin
// (design.md §6, Start). No muta el estado recibido (devuelve uno nuevo).
//
// ctx se propaga hacia la resolución de contenido del render (Plan 015): en T0
// la resolución es un placeholder inline y no lo usa todavía.
func (e *Engine) Enter(ctx context.Context, def model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	st.FlowID = def.FlowID
	st.FlowVersion = def.Version
	st.CurrentNode = def.Initial
	return e.renderFrom(ctx, def, st)
}

// EnterPrimed es como Enter pero, si el nodo inicial es interactivo, su módulo
// implementa la capacidad modules.Primer y hay intent_params sembrados en st.Vars,
// deja que el módulo PRE-CARGUE su estado (Plan 029 · T8, design.md §4.c): p. ej. el
// carrito agrega la línea del producto pedido y salta a la confirmación en vez de
// mostrar el listado vacío. En CUALQUIER otro caso (sin params, sin Primer, o el
// módulo no consume la señal) equivale EXACTAMENTE a Enter (no-regresión total): por
// eso el arranque por API —que nunca siembra params— es idéntico al de siempre.
//
// A diferencia de Enter, devuelve los []modules.Effect que el pre-carga DECLARÓ (p.
// ej. item_added), para que el runtime los despache por el mismo fan-out que un Step.
// No muta el estado recibido salvo por reasignación de campos (devuelve el nuevo).
func (e *Engine) EnterPrimed(ctx context.Context, def model.Flow, st model.Conversation) (model.Conversation, []Output, []modules.Effect, error) {
	st.FlowID = def.FlowID
	st.FlowVersion = def.Version
	st.CurrentNode = def.Initial
	if outs, effects, handled, err := e.tryPrime(ctx, def, &st); err != nil {
		return st, nil, nil, err
	} else if handled {
		// El módulo consumió la señal y produjo su propio estado/pantalla. El carrito
		// es de un solo nodo (Next==nil): permanece en el nodo inicial esperando input.
		return st, outs, effects, nil
	}
	// 🔒 AQUÍ, Y NO EN CADA FIN DE PASO (Plan 046 · Ola 4 · T4.3, REQ-18).
	//
	// Llegar a esta línea significa que la señal de intención NO la consumió nadie, y
	// las SEIS ramas por las que se llega pasan todas por aquí: sin VarIntentParams,
	// nodo desconocido, módulo no registrado, módulo sin capacidad Primer, contenido no
	// resoluble, y el propio Prime devolviendo handled=false. En todas ellas las claves
	// seguían intactas en Vars, y de aquí caían al renderFrom y al Save del runtime:
	// el TEXTO DEL CLIENTE, en claro y para siempre, en el JSONB de public.flow_state.
	//
	// Este es el ÚNICO productor de esas claves y su consumo ocurre entero dentro de
	// EnterPrimed, así que barrer aquí cierra el 100 % de la fuga. Un barrido «al
	// finalizar cada paso» sería no-op en todos los pasos menos el primero: un Step
	// posterior no las siembra ni las lee.
	st.Vars = modules.StripIntentSignal(st.Vars)
	st, outs, err := e.renderFrom(ctx, def, st)
	return st, outs, nil, err
}

// tryPrime intenta la pre-carga del nodo inicial (EnterPrimed). Devuelve handled=true
// solo si hay intent_params en Vars, el nodo tiene un módulo con capacidad Primer y
// ese módulo consumió la señal. Un error de resolución de contenido NO aborta: degrada
// a handled=false ⇒ renderFrom resuelve el contenido por su camino uniforme (si falla,
// devuelve el mismo error una sola vez).
func (e *Engine) tryPrime(ctx context.Context, def model.Flow, st *model.Conversation) ([]Output, []modules.Effect, bool, error) {
	if _, ok := st.Vars[modules.VarIntentParams]; !ok {
		return nil, nil, false, nil
	}
	node, ok := def.Nodes[st.CurrentNode]
	if !ok {
		return nil, nil, false, nil
	}
	mod, ok := e.reg.Get(node.Type)
	if !ok {
		return nil, nil, false, nil
	}
	primer, ok := mod.(modules.Primer)
	if !ok {
		return nil, nil, false, nil
	}
	content, err := e.content.Resolve(ctx, st.TenantID, node)
	if err != nil {
		//nolint:nilerr // degradación intencional: sin content, renderFrom lo resuelve por su
		// camino uniforme y devuelve el mismo error una sola vez (no se pre-carga, no se aborta).
		return nil, nil, false, nil
	}
	res, handled := primer.Prime(node, content, st.Vars)
	if !handled {
		return nil, nil, false, nil
	}
	st.Vars = res.Vars
	return toOutputs(res.Outputs), res.Effects, true, nil
}

// Step evalúa el nodo actual con la entrada del usuario (design.md §3):
//   - nodo "menu": delega en el módulo; si transiciona, renderiza el destino
//     encadenando "message" como en Enter; si no, emite el reprompt/ayuda y
//     permanece.
//   - un módulo de un solo nodo (p. ej. "cart") puede declarar el FIN de su
//     flujo apuntando Result.Next al centinela model.NodeTerminal (hallazgo
//     #24, Plan 043 · Ola 6): el engine lo reconoce sin buscarlo en def.Nodes y
//     termina con el Outputs que el propio Step ya produjo.
//   - conversación terminada (centinela): ignora la entrada (salida neutra).
//
// No muta el estado recibido.
//
// Devuelve además los []modules.Effect que el módulo DECLARÓ para que el runtime
// los despache (Plan 015, segunda costura). En T0 ningún módulo emite efectos:
// el slice llega siempre vacío y los returns tempranos/error devuelven nil. ctx
// se propaga al render del destino (resolución de contenido, T1).
func (e *Engine) Step(ctx context.Context, def model.Flow, st model.Conversation, in Input) (model.Conversation, []Output, []modules.Effect, error) {
	if st.Finished() {
		// Nodo terminal: la entrada se ignora, sin salida (documentado §6).
		return st, nil, nil, nil
	}

	node, ok := def.Nodes[st.CurrentNode]
	if !ok {
		return st, nil, nil, fmt.Errorf("%w: nodo actual %q no existe en la definición", model.ErrInvalidFlow, st.CurrentNode)
	}

	// Delegación genérica: cualquier módulo interactivo (menu, survey_question,
	// …) recorre la misma ruta. Si el nodo actual no tiene módulo registrado o
	// su módulo no espera input (p. ej. "message"), es un estado inconsistente:
	// tras un Enter/renderFrom el estado siempre queda en un nodo interactivo o
	// en el centinela.
	mod, ok := e.reg.Get(node.Type)
	if !ok || !mod.WaitsForInput() {
		return st, nil, nil, fmt.Errorf("%w: nodo actual %q de tipo %q no espera entrada", model.ErrInvalidFlow, st.CurrentNode, node.Type)
	}
	// Best-effort: resuelve el contenido del nodo y EXPONE su blob crudo
	// (Content.Raw) en Vars ANTES del Step, para que un módulo cuya sub-máquina
	// navega en Step —que NO recibe el content resuelto, a diferencia de Render—
	// pueda leer datos de dominio sin hacer I/O (Plan 015/016, design.md §4.1).
	// Genérico: el engine no conoce el dominio (cart parsea su catálogo desde ahí).
	// Un error de resolución NO aborta el Step (se degrada: el módulo verá Raw
	// ausente); Raw nil (static: menú/encuesta) no siembra nada ⇒ sin regresión.
	if resolved, rerr := e.content.Resolve(ctx, st.TenantID, node); rerr == nil && resolved.Raw != nil {
		if st.Vars == nil {
			st.Vars = map[string]any{}
		}
		st.Vars[modules.VarContentRaw] = resolved.Raw
	}
	res := mod.Step(node, st, in.Text)
	st.Vars = res.Vars
	if res.Next != nil {
		if *res.Next == model.NodeTerminal {
			// FIN DE FLUJO declarado por el propio módulo (hallazgo #24, Plan 043 ·
			// Ola 6): un módulo de UN SOLO NODO que espera input siempre (WaitsForInput
			// == true, p. ej. "cart") no puede terminar por la cadena normal de
			// renderFrom —no hay un SEGUNDO nodo al que apuntar, y de haberlo,
			// renderFrom llamaría a Render, no reusaría el Outputs de ESTE Step—. El
			// módulo señala el fin apuntando Result.Next al centinela él mismo; el
			// engine lo reconoce AQUÍ, sin buscarlo en def.Nodes (está reservado,
			// model.Validate lo rechaza como id real), y termina con la pantalla que
			// el propio módulo ya produjo. closeIfFinished (runtime/event_lifecycle.go)
			// ve st.Finished()==true en este mismo turno y cierra el evento que posea
			// el flujo — el mecanismo NO cambia, solo gana quien puede activarlo.
			st.CurrentNode = model.NodeTerminal
			// El DESENLACE viaja junto al centinela (hallazgo #29, Plan 043 · Ola 6):
			// el módulo declara CÓMO terminó y el engine lo sella en el estado, que es
			// lo que closeIfFinished lee para traducirlo al `status` del evento. Se
			// escribe SOLO en esta rama —el fin de flujo declarado por el módulo— y
			// nunca en la transición normal ni en el reprompt: un flujo que sigue vivo
			// no tiene desenlace, y sellarle uno «por si acaso» dejaría un valor que
			// mentiría en cuanto la conversación siguiera por otro lado.
			//
			// res.Outcome == OutcomeUndeclared BORRA la clave (ver SetOutcome), así que
			// un módulo que declara el fin sin declarar el desenlace deja el estado byte
			// a byte como antes de esta ola.
			st.SetOutcome(res.Outcome)
			return st, toOutputs(res.Outputs), res.Effects, nil
		}
		// Transición válida: renderiza el destino (encadenando messages).
		st.CurrentNode = *res.Next
		st2, outs, err := e.renderFrom(ctx, def, st)
		return st2, outs, res.Effects, err
	}
	// Permanece: reprompt o ayuda.
	return st, toOutputs(res.Outputs), res.Effects, nil
}

// renderFrom produce la salida desde st.CurrentNode: emite los "message" (y los
// nodos de SALIDA no interactivos, p. ej. "media": Render + adjunto declarado)
// encadenados por Next y se detiene al llegar a un nodo INTERACTIVO (menu/survey/
// cart, cuyo render delega en el módulo) o a Next == nil (marca el fin con el
// centinela NodeTerminal).
//
// El content de cada nodo interactivo lo RESUELVE la fuente inyectada (puerto
// ContentSource, Plan 015 T1) ANTES del Render; por defecto es el adapter estático
// (PURO), cuyo observable es idéntico al render previo.
func (e *Engine) renderFrom(ctx context.Context, def model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	var outs []Output
	// Cota de seguridad ante ciclos message→message no detectables por Validate.
	for guard := 0; guard <= len(def.Nodes); guard++ {
		node, ok := def.Nodes[st.CurrentNode]
		if !ok {
			return st, outs, fmt.Errorf("%w: nodo %q no existe en la definición", model.ErrInvalidFlow, st.CurrentNode)
		}
		// El tipo "message" es trivial y NO es un módulo: su lógica (emitir el
		// texto y encadenar por Next o terminar) vive inline aquí. Cualquier
		// otro tipo se delega al módulo registrado; si es interactivo, se
		// renderiza y el flujo se detiene esperando input.
		if node.Type == model.NodeTypeMessage {
			outs = append(outs, Output{Text: node.Text})
			if node.Next == nil {
				st.CurrentNode = model.NodeTerminal
				return st, outs, nil
			}
			st.CurrentNode = *node.Next
			continue
		}

		mod, ok := e.reg.Get(node.Type)
		if !ok {
			return st, outs, fmt.Errorf("%w: nodo %q: tipo desconocido %q", model.ErrInvalidFlow, st.CurrentNode, node.Type)
		}
		// Resolución de contenido por fuente (Plan 015, T1): la Source inyectada
		// produce el model.Content del nodo ANTES del Render. El default (static,
		// PURO) copia Prompt/Options del propio nodo ⇒ observable idéntico a T0.
		content, err := e.content.Resolve(ctx, st.TenantID, node)
		if err != nil {
			return st, outs, fmt.Errorf("%w: resolver contenido de %q: %w", model.ErrInvalidFlow, st.CurrentNode, err)
		}
		outs = append(outs, toOutputs(mod.Render(node, content))...)
		if mod.WaitsForInput() {
			// Nodo INTERACTIVO (menu, survey, cart): se renderiza y el flujo se
			// detiene aquí esperando la entrada del usuario.
			return st, outs, nil
		}
		// Nodo de SALIDA (emite y avanza), NO interactivo (p. ej. "media", Plan 017
		// §9.A): además del texto de Render puede DECLARAR un adjunto opaco
		// (model.MediaRef) por la capacidad OPCIONAL modules.MediaEmitter; el runtime
		// lo interpreta (presign + SendMedia, §9.C). El engine consulta la CAPACIDAD,
		// no node.Type ⇒ sigue genérico (sin switch por tipo). Tras emitir, encadena
		// por Next (o termina con el centinela) igual que un "message".
		if em, ok := mod.(modules.MediaEmitter); ok {
			ref, merr := em.EmitMedia(node, content)
			if merr != nil {
				return st, outs, fmt.Errorf("%w: emitir media de %q: %w", model.ErrInvalidFlow, st.CurrentNode, merr)
			}
			if ref != nil {
				outs = append(outs, Output{Media: ref})
			}
		}
		if node.Next == nil {
			st.CurrentNode = model.NodeTerminal
			return st, outs, nil
		}
		st.CurrentNode = *node.Next
	}
	return st, outs, fmt.Errorf("%w: cadena de mensajes demasiado larga (¿ciclo?) desde %q", model.ErrInvalidFlow, st.CurrentNode)
}

func toOutputs(texts []string) []Output {
	if len(texts) == 0 {
		return nil
	}
	outs := make([]Output, 0, len(texts))
	for _, t := range texts {
		outs = append(outs, Output{Text: t})
	}
	return outs
}
