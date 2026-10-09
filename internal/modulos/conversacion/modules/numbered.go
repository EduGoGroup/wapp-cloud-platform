// Porta internal/flujos/modules/numbered.go @ c0c0c03

package modules

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// CloneVars copia el mapa de variables para mantener la PUREZA de los módulos (no
// mutar el Vars de entrada). nil → mapa nuevo, vacío y escribible. La copia es de
// un nivel: los valores se comparten. Extraído de menú/encuesta, que lo duplicaban
// byte-a-byte (Plan 027 · Ola 2 · T9, cierra H12).
func CloneVars(in map[string]any) map[string]any {
	panic(pendiente.Implementar("modules.CloneVars"))
}

// GetInt lee un entero de Vars tolerando el tipo que deja un round-trip por JSON
// (float64) además de int/int64. Valor ausente/de otro tipo → 0. Extraído de
// menú/encuesta (Plan 027 · Ola 2 · T9, cierra H12).
func GetInt(vars map[string]any, key string) int {
	panic(pendiente.Implementar("modules.GetInt"))
}

// ExitMenuVar es la clave de Conversation.Vars donde vive el TEXTO de la pantalla
// que hay que re-emitir si el cliente elige «seguir intentando» en el MENÚ DE SALIDA
// (Plan 043 · T5.2, D-043.10). Presente y no vacío ⇔ el último mensaje enviado fue el
// menú de salida.
//
// Vive EXACTAMENTE UN TURNO: quien lo lee lo borra. Un menú de salida que sobreviviera
// convertiría cualquier «2» posterior en una salida del evento, y el menú es un atajo,
// no una cárcel (misma norma que el menú del despachador del runtime).
const ExitMenuVar = "event_exit_menu"

// ExitMenuEventVar es la clave donde el módulo deja el EVENTO sobre el que armó el
// menú de salida. Sin ella el marcador solo dice «hay un menú armado», y eso NO basta
// para cumplir la promesa de ExitMenuVar: hay un camino del runtime que CONSERVA las
// Vars al cambiar de evento —saveMenuState, el que deja el menú del despachador
// pendiente sin borrar el estado previo—, y por él el marcador sobrevivía al cambio
// de contexto. Medido: con el menú de salida armado en el carrito, decir la palabra
// del despachador y contestar luego un número que la lista NO ofrece hacía que ese
// número desactivara el evento `menu` recién abierto («Listo, dejamos el menú por
// ahora»).
//
// Es la MISMA guarda que el menú del despachador se pone a sí mismo (menuChoice del
// runtime: «solo se consulta mientras el evento ACTIVO es el propio menú»): un
// marcador pendiente pertenece al evento para el que se pintó y a ningún otro.
const ExitMenuEventVar = "event_exit_menu_event_id"

// Las tres opciones del MENÚ DE SALIDA (D-043.10). Numéricas puras: cero LLM (REQ-12).
const (
	ExitMenuKeepTrying = "1"
	ExitMenuStop       = "2"
	ExitMenuDispatcher = "3"
)

// MaxReprompts es el número de intentos inválidos consecutivos tras el cual el
// reprompt acotado escala (design.md §10.E). Vive aquí para que un módulo NUEVO no
// tenga que redeclararlo; menu.MaxReprompts y survey.MaxReprompts se conservan
// (son parte de su superficie pública y sus tests los citan).
const MaxReprompts = 3

// RepromptEventKey deriva, de la clave de un contador de reprompt, la clave del
// SELLO donde ese contador declara el EVENTO en que se cometieron los inválidos:
// la del contador más el sufijo "_event_id".
//
// El sello existe por la MISMA razón que ExitMenuEventVar, un escalón más abajo: el
// marcador del menú de salida ya sabe de quién es, pero el CONTADOR que lo arma no
// sabía de nadie. Los contadores son anteriores al plano de eventos y viven en
// Conversation.Vars, y hay caminos del runtime que cambian el evento activo
// CONSERVANDO las Vars (saveMenuState, pointStateAtEvent sin flujo, stopEvent…), así
// que un contador cargado dentro del evento A seguía contando dentro del B: bastaba
// UN inválido en el evento recién nacido para armarle un menú de salida que ArmExitMenu
// sellaba —correctamente— con el evento nuevo, de modo que la guarda de T5.2 lo
// bendecía y el «2» desactivaba el evento equivocado. Medido por sonda.
//
// Se sella en vez de limpiarse en los caminos que cambian de evento a propósito: los
// caminos son varios y crecen con cada ola, y uno nuevo que olvidara limpiar
// reintroduciría el defecto en silencio. La CADUCIDAD POR LECTURA no se puede olvidar:
// un contador que no declara el evento activo vale 0, venga por donde venga.
//
// La clave del sello es adyacente a la del contador (no compartida) para que borrar un
// contador borre su sello sin invalidar el de otro módulo que estuviera contando.
func RepromptEventKey(repromptKey string) string {
	panic(pendiente.Implementar("modules.RepromptEventKey"))
}

// RepromptCount devuelve los intentos inválidos contados en vars PARA eventID. Si el
// contador se cargó en otro evento vale 0: los intentos son del evento en que se
// cometieron, no de la conversación.
//
// eventID "" (fuera de todo evento) casa con el sello AUSENTE, así que una conversación
// que nunca entró en un evento cuenta exactamente como antes de esta corrección. Un
// sello ilegible (que no es string) se lee como ausente: vale 0 dentro de cualquier
// evento y solo cuenta fuera de todos.
func RepromptCount(vars map[string]any, eventID, repromptKey string) int {
	panic(pendiente.Implementar("modules.RepromptCount"))
}

// SetRepromptCount escribe el contador SELLADO con el evento en que se está contando.
// Muta vars.
//
// Con eventID "" el sello no se escribe (y se borra si lo hubiera): fuera de un evento
// el JSONB de flow_state.vars queda byte a byte como antes de esta corrección.
func SetRepromptCount(vars map[string]any, eventID, repromptKey string, n int) {
	panic(pendiente.Implementar("modules.SetRepromptCount"))
}

// ClearRepromptCount borra el contador y su sello (los dos, o el sello quedaría
// huérfano en el JSONB). Muta vars; no toca ninguna otra clave.
func ClearRepromptCount(vars map[string]any, repromptKey string) {
	panic(pendiente.Implementar("modules.ClearRepromptCount"))
}

// InEvent reporta si la conversación tiene un evento conversacional ACTIVO. Es lo
// ÚNICO que un módulo necesita saber del plano de eventos y ya viaja en el estado
// (model.Conversation.EventID, estampado por el runtime en T4.5.1): el paquete
// modules NO importa el paquete de eventos ni recibe ningún callback.
func InEvent(conv model.Conversation) bool {
	panic(pendiente.Implementar("modules.InEvent"))
}

// ExitMenuText compone el menú de salida sobre la pantalla que el módulo iba a
// re-emitir: la pregunta, las tres opciones y, tras una línea en blanco, la pantalla.
// El orden de las opciones lo fija D-043.10 y no es negociable: el «2» (dejarlo por
// ahora) tiene que quedar entre las dos que NO abandonan.
func ExitMenuText(screen string) string {
	panic(pendiente.Implementar("modules.ExitMenuText"))
}

// ArmExitMenu deja el menú de salida ARMADO en vars —guardando `screen` en
// ExitMenuVar para poder re-emitirla si el cliente elige 1, y conv.EventID en
// ExitMenuEventVar— y devuelve el Result que lo enseña: ese mismo mapa en Vars y
// ExitMenuText(screen) como única salida. El módulo PERMANECE en el nodo
// (Next == nil): el menú de salida no transiciona nada. Muta vars y conserva sus
// demás claves.
//
// 🔴 DESVIACIÓN de la firma literal del CONTRATO §D3, y su porqué: el contrato la fija
// como `ArmExitMenu(vars, screen)`, sin `conv`. Esa firma no puede sellar el marcador
// con el evento al que pertenece, y el propio docstring de ExitMenuVar —también
// literal del contrato— promete que el menú «vive exactamente un turno». Sin el sello
// la promesa era falsa por el camino del despachador (ver ExitMenuEventVar). `conv` ya
// está en la mano de los DOS llamantes (NumberedStep y cart.Module.Step lo reciben) y
// ya se lee ahí mismo vía InEvent, así que el coste es cero y no entra ningún import.
func ArmExitMenu(vars map[string]any, conv model.Conversation, screen string) Result {
	panic(pendiente.Implementar("modules.ArmExitMenu"))
}

// ExitMenuArmedOn devuelve el evento sobre el que se armó el menú de salida ("" si no
// hay ninguno armado o si el marcador es viejo/ilegible). Lo llama el RUNTIME para
// comprobar que el marcador sigue siendo del evento ACTIVO antes de interpretar 1/2/3.
//
// "" también es lo que devuelve un marcador escrito ANTES de esta corrección y que
// siguiera vivo en un flow_state: al no casar con ningún EventID real, el runtime lo
// desarma y el texto sigue su camino. Degradar hacia «no es del menú de salida» es el
// lado seguro — el otro sería secuestrar un turno ajeno.
func ExitMenuArmedOn(vars map[string]any) string {
	panic(pendiente.Implementar("modules.ExitMenuArmedOn"))
}

// DisarmExitMenu borra la marca (las DOS claves: pantalla y evento) y devuelve la
// pantalla guardada ("" si no había menú armado o si no era un string). Lo llama el
// RUNTIME; los módulos no lo necesitan. Muta vars; no toca ninguna otra clave.
func DisarmExitMenu(vars map[string]any) string {
	panic(pendiente.Implementar("modules.DisarmExitMenu"))
}

// NumberedStep resuelve el patrón COMÚN de los nodos de opción numerada
// (menú/encuesta, design.md §3/§10.E), extraído para eliminar la duplicación
// byte-a-byte SIN cambiar la conducta observable (Plan 027 · Ola 2 · T9, cierra
// H12). Clona Vars (pureza: conv.Vars no se muta), recorta la entrada, la casa
// —exacta— contra las claves de node.Options y aplica la escalera de reprompt
// acotada:
//
//   - Opción VÁLIDA: borra el contador de reprompt (y su sello) y delega en onValid,
//     cuyo Result devuelve tal cual. El módulo usa onValid para registrar sus efectos
//     PROPIOS (la encuesta anota la respuesta y declara el efecto survey_answer; el
//     menú no hace nada extra) y devolver el Result de transición al target. onValid
//     recibe los Vars ya clonados y sin el contador, la opción elegida (choice = la
//     clave de Options, ya recortada) y el nodo destino.
//   - Opción INVÁLIDA con < maxReprompts intentos: incrementa el contador, sellado
//     con el evento activo, y re-emite el prompt precedido del aviso «Opción no
//     válida. Responde con el número de una de las opciones.» (permanece en el nodo).
//   - Al alcanzar maxReprompts: reinicia el contador SIEMPRE y permanece. FUERA de un
//     evento emite el mensaje de ayuda de siempre, «No logré entender tu respuesta.
//     Por favor elige una de las opciones escribiendo solo su número.», seguido del
//     prompt; DENTRO de un evento arma el menú de salida sobre el prompt
//     (ArmExitMenu, D-043.10).
//
// El contador se lee con RepromptCount: uno cargado en OTRO evento vale 0 y la
// entrada inválida es la primera de este.
//
// repromptKey es la clave del contador (propia de cada módulo; no colisionan).
func NumberedStep(
	node model.Node,
	conv model.Conversation,
	input string,
	repromptKey string,
	maxReprompts int,
	onValid func(vars map[string]any, choice, target string) Result,
) Result {
	panic(pendiente.Implementar("modules.NumberedStep"))
}
