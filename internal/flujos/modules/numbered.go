package modules

import (
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// CloneVars copia el mapa de variables para mantener la PUREZA de los módulos (no
// mutar el Vars de entrada). nil → mapa nuevo. Extraído de menú/encuesta, que lo
// duplicaban byte-a-byte (Plan 027 · Ola 2 · T9, cierra H12).
func CloneVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// GetInt lee un entero de Vars tolerando el tipo que deja un round-trip por JSON
// (float64) además de int/int64. Valor ausente/de otro tipo → 0. Extraído de
// menú/encuesta (Plan 027 · Ola 2 · T9, cierra H12).
func GetInt(vars map[string]any, key string) int {
	switch v := vars[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// ExitMenuVar es la clave de Conversation.Vars donde vive el TEXTO de la pantalla
// que hay que re-emitir si el cliente elige «seguir intentando» en el MENÚ DE SALIDA
// (Plan 043 · T5.2, D-043.10). Presente y no vacío ⇔ el último mensaje enviado fue el
// menú de salida.
//
// Vive EXACTAMENTE UN TURNO: quien lo lee lo borra. Un menú de salida que sobreviviera
// convertiría cualquier «2» posterior en una salida del evento, y el menú es un atajo,
// no una cárcel (misma norma que el menú del despachador, runtime/events.go:957).
const ExitMenuVar = "event_exit_menu"

// ExitMenuEventVar es la clave donde el módulo deja el EVENTO sobre el que armó el
// menú de salida. Sin ella el marcador solo dice «hay un menú armado», y eso NO basta
// para cumplir la promesa de ExitMenuVar: hay un camino del runtime que CONSERVA las
// Vars al cambiar de evento —saveMenuState (runtime/events.go), el que deja el menú
// del despachador pendiente sin borrar el estado previo—, y por él el marcador
// sobrevivía al cambio de contexto. Medido: con el menú de salida armado en el
// carrito, decir la palabra del despachador y contestar luego un número que la lista
// NO ofrece hacía que ese número desactivara el evento `menu` recién abierto
// («Listo, dejamos el menú por ahora»).
//
// Es la MISMA guarda que el menú del despachador se pone a sí mismo (menuChoice,
// runtime/events.go: «solo se consulta mientras el evento ACTIVO es el propio menú»):
// un marcador pendiente pertenece al evento para el que se pintó y a ningún otro.
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
// SELLO donde ese contador declara el EVENTO en que se cometieron los inválidos.
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
func RepromptEventKey(repromptKey string) string { return repromptKey + "_event_id" }

// RepromptCount devuelve los intentos inválidos contados en vars PARA eventID. Si el
// contador se cargó en otro evento —o el sello es ilegible— vale 0: los intentos son
// del evento en que se cometieron, no de la conversación.
//
// eventID "" (fuera de todo evento) casa con el sello AUSENTE, así que una conversación
// que nunca entró en un evento cuenta exactamente como antes de esta corrección.
func RepromptCount(vars map[string]any, eventID, repromptKey string) int {
	sello, ok := vars[RepromptEventKey(repromptKey)].(string)
	if !ok {
		sello = ""
	}
	if sello != eventID {
		return 0
	}
	return GetInt(vars, repromptKey)
}

// SetRepromptCount escribe el contador SELLADO con el evento en que se está contando.
//
// Con eventID "" el sello no se escribe (y se borra si lo hubiera): fuera de un evento
// el JSONB de flow_state.vars queda byte a byte como antes de esta corrección.
func SetRepromptCount(vars map[string]any, eventID, repromptKey string, n int) {
	vars[repromptKey] = n
	if eventID == "" {
		delete(vars, RepromptEventKey(repromptKey))
		return
	}
	vars[RepromptEventKey(repromptKey)] = eventID
}

// ClearRepromptCount borra el contador y su sello (los dos, o el sello quedaría
// huérfano en el JSONB).
func ClearRepromptCount(vars map[string]any, repromptKey string) {
	delete(vars, repromptKey)
	delete(vars, RepromptEventKey(repromptKey))
}

// InEvent reporta si la conversación tiene un evento conversacional ACTIVO. Es lo
// ÚNICO que un módulo necesita saber del plano de eventos y ya viaja en el estado
// (model.Conversation.EventID, estampado por el runtime en T4.5.1): el paquete
// modules NO importa flujos/events ni recibe ningún callback.
func InEvent(conv model.Conversation) bool { return conv.EventID != "" }

// ExitMenuText compone el menú de salida sobre la pantalla que el módulo iba a
// re-emitir. El orden de las opciones lo fija D-043.10 y no es negociable: el «2»
// (dejarlo por ahora) tiene que quedar entre las dos que NO abandonan.
func ExitMenuText(screen string) string {
	return "Parece que no nos estamos entendiendo. ¿Qué prefieres? Responde con el número:\n" +
		"1) Seguir intentando\n" +
		"2) Dejar esto por ahora\n" +
		"3) Ver el menú\n\n" + screen
}

// ArmExitMenu deja el menú de salida ARMADO en vars —guardando `screen` para poder
// re-emitirla si el cliente elige 1— y devuelve el Result que lo enseña. El módulo
// PERMANECE en el nodo (Next == nil): el menú de salida no transiciona nada.
//
// 🔴 DESVIACIÓN de la firma literal del CONTRATO §D3, y su porqué: el contrato la fija
// como `ArmExitMenu(vars, screen)`, sin `conv`. Esa firma no puede sellar el marcador
// con el evento al que pertenece, y el propio docstring de ExitMenuVar —también
// literal del contrato— promete que el menú «vive exactamente un turno». Sin el sello
// la promesa era falsa por el camino del despachador (ver ExitMenuEventVar). `conv` ya
// está en la mano de los DOS llamantes (NumberedStep y cart.Module.Step lo reciben) y
// ya se lee ahí mismo vía InEvent, así que el coste es cero y no entra ningún import.
func ArmExitMenu(vars map[string]any, conv model.Conversation, screen string) Result {
	vars[ExitMenuVar] = screen
	vars[ExitMenuEventVar] = conv.EventID
	return Result{Vars: vars, Outputs: []string{ExitMenuText(screen)}}
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
	id, ok := vars[ExitMenuEventVar].(string)
	if !ok {
		return ""
	}
	return id
}

// DisarmExitMenu borra la marca (las DOS claves: pantalla y evento) y devuelve la
// pantalla guardada ("" si no había menú armado). Lo llama el RUNTIME; los módulos no
// lo necesitan.
func DisarmExitMenu(vars map[string]any) string {
	// La firma literal del contrato usa `screen, _ := vars[ExitMenuVar].(string)`;
	// aquí se comprueba el `ok` porque este repo activa
	// errcheck.check-type-assertions (.golangci.yml:44), que marca la aserción con
	// blank como error no comprobado. Mismo comportamiento observable: "" si la
	// clave falta o no es string.
	screen, ok := vars[ExitMenuVar].(string)
	if !ok {
		screen = ""
	}
	delete(vars, ExitMenuVar)
	delete(vars, ExitMenuEventVar)
	return screen
}

// NumberedStep resuelve el patrón COMÚN de los nodos de opción numerada
// (menú/encuesta, design.md §3/§10.E), extraído para eliminar la duplicación
// byte-a-byte SIN cambiar la conducta observable (Plan 027 · Ola 2 · T9, cierra
// H12). Clona Vars (pureza), recorta la entrada, la casa contra node.Options y
// aplica la escalera de reprompt acotada:
//
//   - Opción VÁLIDA: borra el contador de reprompt y delega en onValid. El módulo
//     usa onValid para registrar sus efectos PROPIOS (la encuesta anota la respuesta
//     y declara el efecto survey_answer; el menú no hace nada extra) y devolver el
//     Result de transición al target. onValid recibe los Vars ya clonados y sin el
//     contador, la opción elegida (choice = la clave de Options) y el nodo destino.
//   - Opción INVÁLIDA con < maxReprompts intentos: incrementa el contador y re-emite
//     el prompt precedido del aviso (permanece en el nodo).
//   - Al alcanzar maxReprompts: emite el mensaje de ayuda, reinicia el contador y
//     permanece.
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
	vars := CloneVars(conv.Vars)
	trimmed := strings.TrimSpace(input)

	if target, ok := node.Options[trimmed]; ok {
		// Opción válida → el contador se reinicia al transicionar; onValid arma el Result.
		ClearRepromptCount(vars, repromptKey)
		return onValid(vars, trimmed, target)
	}

	// Opción inválida. El contador se lee SELLADO con el evento activo: uno cargado en
	// otro evento vale 0 y esta entrada es el primer fallo de este (ver RepromptEventKey).
	attempts := RepromptCount(vars, conv.EventID, repromptKey) + 1
	if attempts >= maxReprompts {
		// Último intento inválido: el contador se reinicia SIEMPRE (para no spamear) y
		// se permanece en el nodo. Lo que se dice depende de si hay evento vivo:
		//   - DENTRO de un evento (D-043.10): menú de salida numérico. Lo resuelve el
		//     runtime en el turno siguiente (exitMenuChoice); el módulo solo lo arma.
		//   - FUERA: el mensaje de ayuda de siempre, byte a byte (regresión cero).
		ClearRepromptCount(vars, repromptKey)
		if InEvent(conv) {
			return ArmExitMenu(vars, conv, node.Prompt)
		}
		return Result{Vars: vars, Outputs: []string{numberedHelpText(node.Prompt)}}
	}
	SetRepromptCount(vars, conv.EventID, repromptKey, attempts)
	return Result{Vars: vars, Outputs: []string{numberedInvalidText(node.Prompt)}}
}

// numberedInvalidText y numberedHelpText son los avisos del reprompt acotado,
// idénticos byte-a-byte a los que menú/encuesta emitían por separado.
func numberedInvalidText(prompt string) string {
	return "Opción no válida. Responde con el número de una de las opciones.\n\n" + prompt
}

func numberedHelpText(prompt string) string {
	return "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n" + prompt
}
