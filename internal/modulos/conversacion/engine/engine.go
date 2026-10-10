// Porta internal/flujos/engine/engine.go @ 42117b5

// Package engine es el núcleo PURO de la máquina de estados: dadas una
// definición y un estado, evalúa el nodo actual y produce el estado siguiente
// y las salidas. No conoce transporte ni base de datos (design.md §3), y tampoco
// tiene logger, reloj ni métricas: lo único que hace I/O es lo que se le inyecta
// (la fuente de contenido y el resolutor de consultas), y lo único que deja ver
// hacia fuera es el observador de consulta.go.
//
// El engine delega en el módulo registrado para cada tipo de nodo (los nodos
// "menu" los maneja modules/menu: render, validación de opción, transición y
// reprompt acotado). Los nodos "message" son triviales y los encadena el propio
// engine: emite el Text y sigue por Next hasta llegar a un menú (que espera
// input) o a Next == nil (que termina el flujo, marcando CurrentNode con el
// centinela model.NodeTerminal).
//
// Todo error que devuelve envuelve model.ErrInvalidFlow, con estos textos (el
// prefijo es el del centinela, «definición de flujo inválida: »):
//
//	nodo actual %q no existe en la definición                  (Step)
//	nodo actual %q de tipo %q no espera entrada                (Step)
//	nodo %q no existe en la definición                         (render)
//	nodo %q: tipo desconocido %q                               (render)
//	resolver contenido de %q: <causa>                          (render)
//	emitir media de %q: <causa>                                (render)
//	cadena de mensajes demasiado larga (¿ciclo?) desde %q      (render)
//
// En los dos que llevan <causa>, la causa también se alcanza con errors.Is.
package engine

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Input es la entrada normalizada del usuario. En este corte, el texto del
// IncomingMessage. El engine lo entrega al módulo TAL CUAL: no recorta ni
// normaliza (eso es asunto de cada módulo).
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
// Conversation que recibe cada llamada): la misma llamada con la misma entrada
// da el mismo resultado, la haga quien la haga y las veces que la haga.
type Engine struct{}

// Option configura el Engine al construirlo (patrón functional-options, igual
// que gatewaygrpc.Server). Las opciones se aplican en el orden en que se pasan.
type Option func(*Engine)

// WithContentSource inyecta la fuente de contenido (puerto ContentSource). Sin
// ella —o con una fuente nil—, New usa el adapter estático (PURO) por defecto:
// contenido copiado del propio nodo, sin I/O. Si se pasa más de una vez, manda la
// última.
func WithContentSource(src content.Source) Option {
	panic(pendiente.Implementar("engine.WithContentSource"))
}

// New construye el engine con el registro de módulos ya poblado. La fuente de
// contenido es OPCIONAL (WithContentSource); por defecto es el adapter estático,
// con el que un nodo se renderiza byte a byte con su propio Prompt.
func New(reg *modules.Registry, opts ...Option) *Engine {
	panic(pendiente.Implementar("engine.New"))
}

// FlowProducesDurableContent decide si f exige un evento padre para arrancar
// (design.md D-054.5): es un OR sobre f.Nodes (map[string]model.Node) que
// resuelve cada n.Type contra el Registry y devuelve true en cuanto ALGÚN nodo
// —no solo el inicial— resuelve a un módulo cuyo ProducesDurableContent() es
// true. Un tipo de nodo NO registrado en el Registry cuenta como NO durable —no
// hay módulo que pueda materializar nada—, así que un flujo con un tipo
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
	panic(pendiente.Implementar("engine.Engine.FlowProducesDurableContent"))
}

// NodeProducesDurableContent es FlowProducesDurableContent acotado a UN tipo de
// nodo (Plan 054 · T3, D-054.4): el fan-out best-effort del ADR-0003 gana una
// excepción acotada SOLO para los efectos que declaró un módulo durable, y cada
// llamante de runtime.dispatch (startLocked, advanceLiveStep, prepareResume,
// restartableOnStart) conoce el nodo/módulo que produjo el LOTE de efectos que
// va a despachar antes de llamarlo —Step/EnterPrimed/Restart procesan UN nodo
// por turno, así que todo efecto de un mismo lote comparte el mismo módulo—, sin
// tener que reconstruir un model.Flow de un solo nodo para reusar
// FlowProducesDurableContent. Mismo criterio exacto: un tipo no registrado
// cuenta como NO durable (D-054.3(a): «no hay módulo que pueda producir
// nada»).
func (e *Engine) NodeProducesDurableContent(nodeType string) bool {
	panic(pendiente.Implementar("engine.Engine.NodeProducesDurableContent"))
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
	panic(pendiente.Implementar("engine.Engine.DurableNodeType"))
}

// Enter posiciona la conversación en el nodo inicial del flujo y produce su
// render, encadenando nodos "message" hasta el primer "menu" o el fin
// (design.md §6, Start). Fija FlowID, FlowVersion y CurrentNode desde la
// definición (pisando los que trajera el estado) y devuelve un estado nuevo; las
// Vars recibidas las deja como están —Enter NO barre la señal de intención: eso
// es de EnterPrimed—.
//
// El render, que comparte con EnterPrimed y con las transiciones de Step, promete:
//   - un nodo "message" emite su Text y sigue por Next; con Next == nil termina
//     el flujo (CurrentNode = model.NodeTerminal). Es inline: ni consulta el
//     Registry ni resuelve contenido;
//   - cualquier otro tipo se delega al módulo registrado, con el contenido
//     resuelto por la fuente (a la que se le pasan ctx y el TenantID del estado)
//     ANTES del Render. Si el módulo es interactivo, se emite su Render y el flujo
//     se detiene en ese nodo; si es de SALIDA, se emite su Render, después su
//     adjunto si lo declara (modules.MediaEmitter; un adjunto nil no emite nada)
//     y se sigue por Next igual que un "message";
//   - ante un error devuelve, junto al error, el estado en el nodo que falló y
//     las salidas acumuladas hasta él;
//   - la cadena se corta con error tras visitar len(def.Nodes)+1 nodos sin
//     detenerse: una cadena que visita cada nodo una vez nunca lo alcanza.
//
// ctx se propaga hacia la resolución de contenido del render (Plan 015).
func (e *Engine) Enter(ctx context.Context, def model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	panic(pendiente.Implementar("engine.Engine.Enter"))
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
// No muta el estado recibido salvo por reasignación de campos (devuelve el nuevo): el
// mapa de Vars del llamante conserva la señal aunque el estado devuelto no la lleve.
//
// Si el módulo consume la señal (handled), la conversación se queda en el nodo
// inicial con las Vars, las salidas y los efectos que devolvió Prime, sin Render. Ahí
// las Vars son las del módulo TAL CUAL: limpiar la señal es cosa suya (el contrato de
// modules.Primer) y el engine no la barre por él. Al Primer se le entrega el contenido
// ya resuelto del nodo y las Vars con la señal.
//
// Si nadie la consume, la señal (modules.VarIntentParams y modules.VarIntentName) se
// barre de las Vars —las demás claves no se tocan— ANTES del render, los efectos son
// nil y lo demás es Enter, errores incluidos. Vale también cuando solo viaja
// VarIntentName.
func (e *Engine) EnterPrimed(ctx context.Context, def model.Flow, st model.Conversation) (model.Conversation, []Output, []modules.Effect, error) {
	panic(pendiente.Implementar("engine.Engine.EnterPrimed"))
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
// Devuelve además los []modules.Effect que el módulo DECLARÓ para que el runtime
// los despache (Plan 015, segunda costura). ctx se propaga al render del destino
// (resolución de contenido, T1).
//
// Lo que promete, caso por caso:
//   - conversación terminada: devuelve el estado tal cual, sin salidas, sin
//     efectos y sin error; no mira la definición ni llama a ningún módulo;
//   - el nodo actual no existe, su tipo no está registrado o su módulo no espera
//     entrada (un "message", un módulo de salida): error, con el estado sin tocar
//     y salidas y efectos nil;
//   - antes de llamar al módulo resuelve el contenido del nodo y, si trae blob
//     crudo, lo expone en Vars[modules.VarContentRaw]; si no hay blob o la
//     resolución falla, no siembra nada y el Step sigue;
//   - la delegación es GENÉRICA, sin switch por tipo: cualquier módulo interactivo
//     recorre la misma ruta;
//   - si el módulo eleva una Query, se resuelve y se le vuelve a llamar UNA vez
//     (consulta.go): lo que sigue se aplica al Result de la segunda pasada;
//   - las Vars del estado devuelto son SIEMPRE las del Result (unas Vars nil
//     vacían el estado);
//   - permanencia (Next nil): mismo nodo, las salidas y los efectos del módulo;
//   - transición (Next a otro nodo): CurrentNode pasa al destino y las salidas
//     son las del render del destino; las Outputs del Result se descartan y sus
//     efectos se devuelven, también cuando el render del destino falla;
//   - fin declarado por el módulo (Next al centinela, comparado por VALOR): termina
//     con las salidas y los efectos del Result, sin Render, y sella el desenlace;
//   - Result.Outcome solo se sella en el fin declarado: en la permanencia y en la
//     transición se ignora.
//
// 🔴 «No muta el estado recibido» vale para los CAMPOS, no para el mapa de Vars: el
// blob crudo se escribe en el MISMO mapa que trae el estado (solo se crea uno nuevo
// si venía nil), y el desenlace se sella en el mapa que devolvió el módulo, que
// suele ser ese mismo. Es el comportamiento del viejo, portado tal cual.
func (e *Engine) Step(ctx context.Context, def model.Flow, st model.Conversation, in Input) (model.Conversation, []Output, []modules.Effect, error) {
	panic(pendiente.Implementar("engine.Engine.Step"))
}
