// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6
//
// El viejo es un solo fichero de 849 líneas; aquí nace partido por tema (05 E-13):
// cart.go (el Module y su Step), cart_levels.go (los niveles L1–L6 del recorrido),
// cart_notes.go (los niveles de indicación) y cart_navigation.go (reencauces,
// reprompt y localización en el catálogo).

// Package cart es el módulo del carrito conversacional (Plan 016, design.md §4):
// una sub-máquina jerárquica que navega un catálogo de dos niveles
// (categorías → artículos), acumula líneas de pedido y cierra con un resumen.
// Es el tercer módulo del Motor de Flujos tras Menú (Plan 006) y Encuesta
// (Plan 014).
//
// PUREZA (invariante, design.md §2/§4.1): el módulo NO hace I/O. Recibe el
// catálogo ya resuelto y navega en memoria; todo su estado vive en
// Conversation.Vars (JSONB) y lo persiste el engine. DECLARA efectos
// (Result.Effects, ver effects.go) y nunca los ejecuta: los materializa el
// Projector (projection.go), que es un adaptador aparte.
//
// UN SOLO NODO (design.md §9.A): la definición del flujo tiene un único nodo
// {"type":"cart"}; los niveles (categorías/artículos/artículo/cantidad/
// continuar/resumen) y el "volver" se manejan internamente con el estado en
// Vars, sin multiplicar nodos en flow_definitions.
//
// CATÁLOGO EN Step (nota de ejecución, design.md §9): la interfaz Module entrega
// el content resuelto SOLO a Render, no a Step (registry.go). Para que Step
// navegue sin romper la pureza, el catálogo viaja como snapshot en
// Vars["cart_catalog"] (modules.VarContentRaw, misma forma que model.Content.Raw):
// lo siembra el engine antes de cada Step. Así el engine, menú y encuesta NO se
// tocan y el módulo sigue siendo I/O-free.
package cart

import (
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// catalogUnavailable es la pantalla que se muestra cuando no hay catálogo con el
// que navegar (Raw ausente / snapshot no sembrado). No debería ocurrir en el
// camino real: el runtime siembra el catálogo antes del primer Step (T2).
const catalogUnavailable = "El catálogo no está disponible en este momento. Intenta más tarde."

// Module implementa modules.Module para el tipo de nodo "cart", y sus capacidades
// opcionales modules.Primer (prime.go) y modules.NodeValidator (validate.go). Es un
// valor inmutable tras New: no guarda estado por conversación.
type Module struct {
	pageSize int
	log      logger.Logger
	// onMatch observa QUÉ escalón de la cascada determinista resolvió la entrada
	// del cliente (Plan 044 · Ola 3.5 · T3.5-1). Es un CALLBACK y no una métrica:
	// el módulo no importa prometheus ni sabe que existe —mismo desacoplo que
	// receipts.Sink y flowruntime.WithReactiveBlockedHook—. Sin él la cascada
	// funciona exactamente igual, en silencio: el hook NO decide, solo observa.
	onMatch func(step, level string)
}

// Option configura el Module al construirlo (patrón functional-options). Las
// opciones se aplican en el orden en que se pasan; si una se repite con un valor
// que no se ignora, manda la última.
type Option func(*Module)

// WithLogger le da al módulo un logger para AVISAR de los campos del catálogo v2
// que el parseo tolerante descartó (Plan 041 · T2.2). Es OPCIONAL y solo
// observabilidad: sin él —o con uno nil, que se ignora— el módulo se comporta
// exactamente igual, las mismas pantallas incluidas, y la pureza no se toca, porque
// un aviso no lee ni escribe nada del mundo.
//
// Con logger, Render y Prime emiten UN Warn por cada catalogo.CatalogWarning del
// catálogo que resuelven, con el mensaje «cart: campo del catálogo v2 descartado» y
// los atributos `categoria`, `sku`, `campo` y `motivo`. Step NO avisa: re-parsea el
// snapshot en cada mensaje del cliente y avisar ahí repetiría el mismo defecto una
// vez por tecleo, hasta enterrar el log del resto de la flota. Un aviso por
// conversación basta para que el dueño se entere de que su catálogo tiene una parte
// ilegible.
func WithLogger(l logger.Logger) Option {
	return func(m *Module) {
		if l != nil {
			m.log = l
		}
	}
}

// WithMatchHook inyecta el observador de la interpretación de lo que el cliente
// ESCRIBIÓ (Plan 044 · Ola 3.5 · T3.5-1): recibe el ESCALÓN que resolvió y el NIVEL
// de la sub-máquina en que se resolvió. Se cablea con la métrica
// `wapp_cart_match_total`; el módulo no importa prometheus ni sabe que existe. Un
// hook nil se ignora.
//
// Los escalones son cinco, y ninguno más:
//
//   - "exact" | "fuzzy" | "ninguno": la cascada determinista del pre-resolutor
//     (preresolutor.go), UNA vez por mensaje en que la cascada llega a correr. Un
//     mensaje resuelto por código exacto (el cliente tecleó "2"), un nivel excluido
//     o la segunda pasada de un turno con consulta NO dejan rastro: se cuenta lo que
//     la cascada hizo, no los turnos.
//   - "troceo" | "troceo_perdido": el troceado de un turno con varios productos
//     (troceo.go), una vez POR TROZO que sí era una petición: entró en el pedido, o
//     no se pudo identificar.
//
// 🔴 Los dos argumentos son de cardinalidad ACOTADA por construcción: el escalón
// sale de ese vocabulario cerrado y el nivel es una de las constantes Level* de
// state.go. Ni el texto del cliente ni la evidencia legible de la comparación salen
// jamás por aquí, y eso es una regla dura, no una recomendación: esto acaba en una
// etiqueta de Prometheus. Los niveles de texto libre (item_note, order_note,
// buyer_data) no avisan NUNCA.
//
// Sin hook el módulo se comporta igual y la pureza no se toca: contar no lee ni
// escribe nada del mundo (mismo razonamiento que WithLogger). El hook NO decide,
// solo observa.
func WithMatchHook(fn func(step, level string)) Option {
	return func(m *Module) {
		if fn != nil {
			m.onMatch = fn
		}
	}
}

// WithPageSize fija el tamaño de página de los niveles de lista: el que usa Render,
// y el que usa Step cuando el runtime no sembró VarPageSize. Un valor <= 0 se
// ignora (se mantiene el que hubiera).
func WithPageSize(n int) Option {
	return func(m *Module) {
		if n > 0 {
			m.pageSize = n
		}
	}
}

// New crea el módulo Carrito con el tamaño de página por defecto (DefaultPageSize,
// design.md §9.E), sin logger y sin observador, y le aplica las opciones.
func New(opts ...Option) Module {
	m := Module{pageSize: DefaultPageSize}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Type devuelve el identificador del tipo de nodo manejado: NodeTypeCart.
func (Module) Type() string {
	return NodeTypeCart
}

// WaitsForInput indica que el carrito es interactivo: se renderiza y detiene el
// flujo esperando la entrada del usuario (igual que menú/encuesta). Siempre true.
func (Module) WaitsForInput() bool {
	return true
}

// ProducesDurableContent es true: el carrito proyecta a intakes / intake_items /
// intake_buyer_data (design.md D-054.3(a)). Es lo que hace que un flujo con un nodo
// cart NO pueda arrancarse por un camino sin evento padre —p. ej. un flujo de solo
// `cart` disparado por `keyword`—: el INSERT de la solicitud reventaría contra el
// NOT NULL de intakes.event_id y la comanda se perdería en silencio (hallazgos #001
// y #003 del Plan 054, dos comandas perdidas en UAT). Quien pregunta es
// engine.FlowProducesDurableContent, antes de guardar nada.
func (Module) ProducesDurableContent() bool {
	return true
}

// Render produce la pantalla de ARRANQUE del carrito: la lista de categorías (L1,
// página 0, con el tamaño de página del Module), siempre UNA pantalla. Recibe el
// catálogo ya resuelto por el engine (model.Content.Raw vía catalogo.ParseCatalog)
// y no mira el nodo. L1 es la raíz: no ofrece «volver».
//
// Si el catálogo no se puede leer (Raw ausente o mal formado) la pantalla es
// «El catálogo no está disponible en este momento. Intenta más tarde.».
//
// El resto de pantallas (tras cada Step) las produce Step en Result.Outputs, porque
// el carrito permanece en el MISMO nodo (Next==nil) mientras navega y el engine no
// vuelve a llamar Render dentro de la sub-máquina — la ÚNICA excepción es el pedido
// terminado (Step fija Next al centinela, hallazgo #24), y ahí tampoco se
// re-renderiza: el engine termina con el Outputs que el propio Step ya produjo.
//
// Render no declara efectos (no puede: devuelve textos) y avisa por el logger de
// los campos descartados del catálogo (ver WithLogger).
func (m Module) Render(_ model.Node, content model.Content) []string {
	cat, err := catalogo.ParseCatalog(content)
	if err != nil {
		return []string{catalogUnavailable}
	}
	m.warnCatalog(cat)
	return []string{screenCategories(cat, cartState{Level: LevelCategories}, m.pageSize)}
}

// warnCatalog vuelca por el log los campos v2 que el parseo tolerante descartó
// (Plan 041 · T2.2). Se llama SOLO donde el catálogo se resuelve al ENTRAR al
// nodo (Render y Prime), no en Step: Step re-parsea el snapshot en cada mensaje
// del cliente y avisar ahí repetiría el mismo defecto una vez por tecleo, hasta
// enterrar el log del resto de la flota. Un aviso por conversación basta para
// que el dueño se entere de que su catálogo tiene una parte ilegible.
func (m Module) warnCatalog(cat catalogo.Catalog) {
	if m.log == nil {
		return
	}
	for _, w := range cat.Warnings {
		m.log.Warn("cart: campo del catálogo v2 descartado",
			"categoria", w.Category, "sku", w.SKU, "campo", w.Field, "motivo", w.Reason)
	}
}

// Step procesa la entrada del usuario sobre el nodo carrito: carga el catálogo
// (snapshot en Vars) y el estado, aplica la transición de la sub-máquina, guarda el
// nuevo estado en Vars y DECLARA los efectos de negocio (design.md §3.3). No mira
// el nodo. NO muta conv.Vars: trabaja sobre una copia, que es la que devuelve.
//
// SIN CATÁLOGO (modules.VarContentRaw ausente, de otro tipo o ilegible) devuelve la
// pantalla «El catálogo no está disponible en este momento. Intenta más tarde.», la
// copia de Vars sin tocar, sin efectos y sin transición.
//
// NAVEGACIÓN. El carrito permanece en el mismo nodo (Next == nil) mientras navega,
// con la pantalla del nuevo nivel en Outputs. Qué acepta cada nivel y qué pantalla
// produce lo dicen cart_levels.go, cart_notes.go, variants.go, buyer.go y
// screens.go; la entrada se recorta (TrimSpace) antes de mirarla. El tamaño de
// página es el que el runtime sembró en VarPageSize y, si no, el del Module; el
// checklist del comprador es el sembrado en VarBuyerFields.
//
// cart_started se declara EXACTAMENTE UNA vez por carrito, en el primer Step (marca
// `started` del estado), delante de los efectos de la transición: la pureza del
// módulo y el contrato de efectos impiden emitirlo en el Enter/Render. Es lo más
// cercano a "al arranque" (design.md §3.3) sin tocar el contrato del engine.
//
// 🔴 EL ORDEN ES EL INVARIANTE (trampa T-11; lo fija orden_consulta_ast_test.go).
// Antes de mutar NADA —antes de `started`, antes del contador de inválidos— Step
// hace dos cosas, en este orden:
//
//  1. El TROCEADO (troceo.go): un turno con VARIOS productos en el nivel de
//     categorías es un pedido entero, no una elección de opción. Va primero: si lo
//     mirara antes el pre-resolutor, casaría UN producto y se tragaría el resto.
//  2. La traducción de la entrada: la cascada determinista (preresolutor.go) y, si
//     no resuelve y el nivel lo admite, la PETICIÓN de consulta (consulta.go).
//
// Cualquiera de los dos puede terminar el turno devolviendo SOLO una petición:
// Result.Query relleno, la copia de Vars sin tocar, sin Outputs, sin Effects, sin
// Next y sin estado guardado. El engine DESCARTA ese Result entero, resuelve la
// consulta, siembra el veredicto en Vars y vuelve a llamar a Step. Si la petición
// saliera por debajo de una mutación, la segunda pasada duplicaría cart_started o
// contaría dos inválidos por un solo mensaje del cliente — y la suite de conducta
// seguiría verde. Con el veredicto ya sembrado (modules.VerdictFrom) Step NO vuelve
// a pedir: su presencia es la señal de «ya preguntaste».
//
// EL CONTADOR DE INVÁLIDOS (Plan 043 · T5.2, D-043.10). Solo cuenta DENTRO de un
// evento (conv.EventID != ""): fuera, el carrito repromptea sin techo y el estado
// no lleva contador, exactamente como siempre (regresión cero). Dentro:
//
//   - Cuentan los inválidos de OPCIÓN, los que responden «Opción no válida.
//     Responde con el número de una de las opciones.» seguido de la pantalla del
//     nivel. El primero y el segundo repromptean así.
//   - El TERCERO consecutivo no repregunta: arma el menú de salida
//     (modules.ArmExitMenu) sobre la pantalla del nivel —la misma que el reprompt
//     venía mostrando, sin el aviso—, y el contador vuelve a cero.
//   - Cualquier turno que NO sea un inválido de opción lo reinicia: una entrada
//     válida, y también —se porta tal cual— una cantidad inválida o una indicación
//     rechazada, que repreguntan por otro camino.
//   - El contador va SELLADO con el evento en que se contó (`reprompts_event_id`).
//     Si el estado llega sellado con OTRO evento (hay caminos del runtime que cambian
//     de evento conservando Vars), arranca de cero: sin eso, dos fallos en un evento
//     armaban el menú de salida al PRIMER fallo del siguiente. Un contador a cero no
//     lleva sello.
//
// FIN DE FLUJO EN EL PEDIDO CONFIRMADO (hallazgo #24, Plan 043 · Ola 6, decisión de
// Jhoan 2026-08-11): "el carrito permanece en el mismo nodo" describe la NAVEGACIÓN,
// no el cierre. Cuando la transición deja la sub-máquina en LevelClosed, Step fija
// Result.Next al centinela model.NodeTerminal: engine.Step lo reconoce y termina SIN
// volver a llamar Render (el Outputs de este mismo Step ya es la pantalla final), y
// el runtime cierra el evento —con su event_closed— en el MISMO turno. Antes de ese
// arreglo Next se quedaba en nil también tras cerrar: el evento que contenía el
// pedido quedaba `open` PARA SIEMPRE, y si la misma conversación pedía otra vez, el
// salto por tipo reusaba ese evento todavía vivo y la segunda solicitud chocaba en
// silencio contra el índice único parcial `intakes_event_id_uidx` (migración 0054).
// De ahí la promesa H24: el SEGUNDO pedido tras confirmar tiene su PROPIO evento
// `cart` y su PROPIA solicitud abierta. Y la de la Ola 6: como la conversación queda
// en el centinela, el turno SIGUIENTE no llega a este módulo —el runtime suelta el
// flow_state terminal y el entrante cae por el camino del contacto sin flujo—, así
// que la clienta NUNCA queda muda y puede pedir de nuevo.
//
// LA MISMA CONDICIÓN, EXTENDIDA AL PEDIDO CANCELADO (hallazgo #29, salida (A),
// decisión de Jhoan 2026-08-11): cuando la transición deja la sub-máquina en
// LevelCancelled, Step también fija Result.Next al centinela. Consecuencia de
// producto ACEPTADA: el «hola» que antes reanudaba el carrito cancelado ahora cae al
// fallback/oferta del tenant como cualquier contacto sin flujo — solo un disparador
// real («carrito») abre un pedido nuevo, y por eso la pantalla de cancelado lo
// NOMBRA.
//
// Y QUÉ EVENTO QUEDA DETRÁS (segunda vuelta del #29): terminar los dos por el mismo
// sitio NO es terminar los dos igual. Result.Outcome declara CUÁL de los dos finales
// se alcanzó —model.OutcomeCompleted al confirmar, model.OutcomeCancelled al
// cancelar— y el runtime lo traduce: cancelar DENTRO del flujo deja el evento en
// `cancelled`, no en `closed` (H29). El módulo dice cómo terminó y ahí acaba su
// responsabilidad; el efecto que declara (cart_closed / cart_cancelled) sigue al
// mismo estado. Mientras se navega, Next es nil y Outcome es el cero.
//
// Un estado LEGADO que llegue ya en LevelClosed o LevelCancelled sin el centinela
// puesto ignora la entrada, re-muestra su pantalla final y vuelve a declarar el fin
// del flujo con su desenlace, sin efectos de cierre. Un `level` desconocido se
// reencauza a la lista de categorías conservando líneas, `started`, la nota del
// pedido y el contador del comprador: son cosas que el cliente ya dijo.
func (m Module) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	vars := modules.CloneVars(conv.Vars)
	cat, err := loadCatalog(vars)
	if err != nil {
		return modules.Result{Vars: vars, Outputs: []string{catalogUnavailable}}
	}
	// page_size REAL del tenant: el runtime lo siembra en Vars[VarPageSize]
	// (tenant_settings.page_size); sin sembrar cae al default del Module (design.md
	// §9.E). Toda la paginación de la sub-máquina (que ocurre en Step) lo respeta;
	// Render (una sola pantalla, al arranque) usa el default del Module.
	size := pageSizeFromVars(vars, m.pageSize)
	// Checklist del comprador REAL del tenant (tenant_settings.buyer_fields, T4.5),
	// sembrado por la misma vía que el page_size. Ausente ⇒ lista vacía ⇒ el carrito
	// no pregunta nada y el recorrido es el de siempre (INV-15).
	fields := loadBuyerFields(vars)
	st := loadState(vars)
	st.inEvent = modules.InEvent(conv)
	// Los inválidos son del EVENTO en que se cometieron: si el estado viene sellado con
	// otro (la conversación cambió de contexto por un camino que conserva Vars), el
	// contador arranca de cero aquí. Sin esto, dos fallos en el carrito armaban el menú
	// de salida al primer fallo del evento siguiente (ver RepromptsEvent).
	if st.RepromptsEvent != conv.EventID {
		st.Reprompts = 0
		st.RepromptsEvent = ""
	}
	// ════════════════════════════════════════════════════════════════════════
	// PRE-RESOLUTOR DETERMINISTA (Plan 044 · Ola 3.5 · T3.5-1)
	// ════════════════════════════════════════════════════════════════════════
	//
	// Traduce lo que el cliente escribió al CÓDIGO canónico que los step* ya
	// entienden (preresolutor.go). Si no resuelve con certeza devuelve el input
	// INTACTO y todo lo de abajo se comporta EXACTAMENTE como antes de esta tarea.
	//
	// 🔴 VA AQUÍ, Y EL SITIO ES LA MITAD DE LA TAREA. Todo lo que hay por encima es
	// LECTURA PURA —clonar Vars, parsear el catálogo, leer page_size, buyer_fields
	// y el cartState, y reiniciar un contador de reprompt que es idempotente—, así
	// que este punto se puede volver a pisar sin efectos duplicados. Todo lo que
	// hay por DEBAJO ya muta: `Started` (que emite cart_started exactamente una
	// vez) y el contador de inválidos (que a los 3 arma el menú de salida). Si el
	// pre-resolutor viviera un renglón más abajo, la segunda pasada que T3.5-2
	// necesita —salir a preguntarle al LLM y volver a entrar— duplicaría el efecto
	// cart_started o contaría dos inválidos por un solo mensaje del cliente.
	// …Y LA CONSULTA SALE DEL MISMO PUNTO (T3.5-2, consulta.go). La segunda pasada
	// que este comentario anticipaba ya existe: si la cascada no resuelve y el
	// nivel admite consulta, el módulo devuelve AQUÍ MISMO un Result que solo lleva
	// la petición —sin efectos, sin estado guardado, sin `Started`— y el engine lo
	// DESCARTA entero antes de volver a llamar con el veredicto sembrado. Por eso
	// las dos cosas viven en la misma línea del método y por encima de toda
	// mutación: el orden es el invariante, y lo fija orden_consulta_ast_test.go.
	// ════════════════════════════════════════════════════════════════════════
	// EL TROCEADO (Plan 044 · Ola 3.5 · T3.5-3), Y VA ANTES QUE EL PRE-RESOLUTOR
	// ════════════════════════════════════════════════════════════════════════
	//
	// Un turno con VARIOS productos («quiero 2 pizzas y una hamburguesa») no es una
	// elección de opción: es un pedido entero. Si lo mirara primero el pre-resolutor,
	// casaría UN producto y se tragaría el resto del mensaje sin dejar rastro — que
	// es exactamente la pérdida medida en campo el 2026-08-17 (troceo.go).
	//
	// Se aparta solo: fuera del nivel de categorías, o con menos de dos peticiones
	// dentro del turno, devuelve ok=false y todo lo de abajo corre byte a byte como
	// el día antes de esta tarea. Y vive aquí arriba por el mismo motivo que la
	// consulta: su primera pasada también puede devolver una PETICIÓN que el engine
	// descarta entera.
	if res, ok := m.chunked(cat, st, vars, input); ok {
		return res
	}
	input, query := m.preresolveOrQuery(cat, st, vars, input)
	if query != nil {
		// Vars va sin tocar (el clon fiel de la entrada) y el engine lo descarta
		// igualmente: se devuelve por disciplina, para que este Result sea legítimo
		// también si algún día alguien llama a Step sin pasar por el engine.
		return modules.Result{Vars: vars, Query: query}
	}
	var effects []modules.Effect
	if !st.Started {
		st.Started = true
		effects = append(effects, event(EffectCartStarted, map[string]any{}))
	}
	newSt, outs, stepEffects := advance(cat, st, input, size, fields)
	effects = append(effects, stepEffects...)
	// Contador: si advance NO pasó por reprompt(), el contador no se movió ⇒ la entrada
	// fue válida ⇒ se reinicia. Evita tocar los 8 sitios de reprompt para reiniciarlo.
	if newSt.Reprompts == st.Reprompts {
		newSt.Reprompts = 0
	}
	// El contador que sobrevive al turno se sella con el evento en que se contó; el que
	// murió (entrada válida, o el tercer inválido que armó el menú) suelta su sello.
	newSt.RepromptsEvent = ""
	if newSt.Reprompts > 0 {
		newSt.RepromptsEvent = conv.EventID
	}
	if newSt.exitScreen != "" {
		outs = modules.ArmExitMenu(vars, conv, newSt.exitScreen).Outputs
		newSt.exitScreen = ""
	}
	storeState(vars, newSt)
	res := modules.Result{Vars: vars, Outputs: outs, Effects: effects}
	if newSt.Level == LevelClosed || newSt.Level == LevelCancelled {
		// Pedido CONFIRMADO o CANCELADO: cierra el flujo (ver la nota grande sobre
		// Step, arriba — #24 y su extensión, #29). Lo que recibe la clienta en el
		// turno siguiente lo decide advanceLive (runtime/incoming.go, rama
		// st.Finished() && st.EventID=="", arreglada por el #28): el flow_state
		// terminal se suelta y el turno cae por el camino del contacto sin flujo, así
		// que la clienta NUNCA queda muda y puede pedir de nuevo.
		term := model.NodeTerminal
		res.Next = &term
		// El DESENLACE (segunda vuelta del #29, decisión de Jhoan 2026-08-11): la
		// condición del centinela es la MISMA para los dos niveles, pero lo que le pasa
		// al evento no lo es. El módulo dice cuál de los dos finales alcanzó y ahí acaba
		// su responsabilidad: traducirlo a `closed`/`cancelled` es del runtime
		// (closeIfFinished), que es el único que conoce el plano de eventos.
		res.Outcome = model.OutcomeCompleted
		if newSt.Level == LevelCancelled {
			res.Outcome = model.OutcomeCancelled
		}
	}
	return res
}
