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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Module implementa modules.Module para el tipo de nodo "cart", y sus capacidades
// opcionales modules.Primer (prime.go) y modules.NodeValidator (validate.go). Es un
// valor inmutable tras New: no guarda estado por conversación.
type Module struct{}

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
	panic(pendiente.Implementar("cart.WithLogger"))
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
	panic(pendiente.Implementar("cart.WithMatchHook"))
}

// WithPageSize fija el tamaño de página de los niveles de lista: el que usa Render,
// y el que usa Step cuando el runtime no sembró VarPageSize. Un valor <= 0 se
// ignora (se mantiene el que hubiera).
func WithPageSize(n int) Option {
	panic(pendiente.Implementar("cart.WithPageSize"))
}

// New crea el módulo Carrito con el tamaño de página por defecto (DefaultPageSize,
// design.md §9.E), sin logger y sin observador, y le aplica las opciones.
func New(opts ...Option) Module {
	panic(pendiente.Implementar("cart.New"))
}

// Type devuelve el identificador del tipo de nodo manejado: NodeTypeCart.
func (Module) Type() string {
	panic(pendiente.Implementar("cart.Module.Type"))
}

// WaitsForInput indica que el carrito es interactivo: se renderiza y detiene el
// flujo esperando la entrada del usuario (igual que menú/encuesta). Siempre true.
func (Module) WaitsForInput() bool {
	panic(pendiente.Implementar("cart.Module.WaitsForInput"))
}

// ProducesDurableContent es true: el carrito proyecta a intakes / intake_items /
// intake_buyer_data (design.md D-054.3(a)). Es lo que hace que un flujo con un nodo
// cart NO pueda arrancarse por un camino sin evento padre —p. ej. un flujo de solo
// `cart` disparado por `keyword`—: el INSERT de la solicitud reventaría contra el
// NOT NULL de intakes.event_id y la comanda se perdería en silencio (hallazgos #001
// y #003 del Plan 054, dos comandas perdidas en UAT). Quien pregunta es
// engine.FlowProducesDurableContent, antes de guardar nada.
func (Module) ProducesDurableContent() bool {
	panic(pendiente.Implementar("cart.Module.ProducesDurableContent"))
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
	panic(pendiente.Implementar("cart.Module.Render"))
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
	panic(pendiente.Implementar("cart.Module.Step"))
}
