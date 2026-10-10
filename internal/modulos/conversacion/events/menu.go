// Porta internal/flujos/events/menu.go @ 9d5a4b6

package events

import (
	"encoding/json"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrNoMenu lo devuelve DecodeMenu cuando no hay nada guardado. Es distinto de
// «lo guardado no se entiende»: no haber presentado menú es lo NORMAL (la
// mayoría de los entrantes no vienen de un menú), y quien pregunta necesita
// poder distinguir ese caso del de un estado corrupto sin mirar el texto del
// error.
var ErrNoMenu = errors.New("events: no hay menú guardado para esta conversación")

// MenuAction es lo que PIDE el cliente al elegir una opción. Son DOS y no una
// porque son dos peticiones distintas —«quiero hacer un pedido» y «quiero volver
// a lo que dejé a medias»— y el menú tiene que transmitirlas sin ambigüedad.
//
// Ojo con lo que NO significan: no son dos operaciones de BD ya decididas. Un
// tipo que ya tiene un evento vivo aparece en el menú por partida DOBLE (E-9.3,
// el ejemplo de Marta: «1) Hacer un pedido» convive con «4) Retomar algo que
// dejaste a medias»), y qué ocurre entonces al elegir la primera lo resuelve el
// ejecutor con la norma del ADR-0029 · Enmienda 6 / E-11 — no este paquete.
type MenuAction string

const (
	// ActionResume es volver a un evento que el contacto YA tiene vivo: el
	// ejecutor conmuta flow_state.event_id a Choice.EventID. No nace nada y
	// ningún status cambia (T2.2).
	ActionResume MenuAction = "resume"
	// ActionRescue es pedir la LISTA de lo que se dejó a medias: la entrada final
	// de la conversación que ofrece (T3.8), «Retomar algo que dejaste a medias
	// (N)». No retoma nada por sí sola —para eso está ActionResume, que ya sabe
	// QUÉ evento—: lo que pide es que se le enseñe el automensaje de rescate, con
	// sus opciones numeradas, y ahí elige.
	//
	// Existe porque la entrada de conversación COLAPSA los rescatables en una sola
	// línea (D-043.17): enumerarlos ahí mezclaría «lo que puedes empezar» con «lo
	// que dejaste», que es la lista que el despachador ya enseña cuando toca.
	ActionRescue MenuAction = "rescue"
	// ActionStart es pedir el tipo Choice.Kind.
	//
	// Que el tipo ya tenga un evento vivo NO es un error ni un caso raro: es el
	// caso central que resuelve E-11 en el ejecutor (si el vivo está suspendido
	// lo cierra y crea el nuevo; si está dentro de su ventana conmuta hacia él,
	// para que nadie pierda un pedido en curso). Por eso el menú lo ofrece
	// SIEMPRE: ocultarlo sería la salida (iii) del design §MD, la que quitaba al
	// cliente la posibilidad de empezar limpio, y no es la que se eligió.
	ActionStart MenuAction = "start"
)

// MenuOption es una opción numerada del menú, ya resuelta a lo que hay que
// hacer si el cliente la elige.
//
// Las etiquetas JSON son cortas a propósito: esta estructura se PERSISTE entre
// dos mensajes de WhatsApp (el cliente responde el número en el mensaje
// siguiente) y viaja en una columna JSONB, no en una API pública.
//
// Lo que NO lleva es el texto renderizado. La frase se recompone al mostrar; lo
// que hay que recordar entre mensajes es a qué despacha cada número, y guardar
// además la prosa invitaría a resolver comparando textos.
type MenuOption struct {
	// Number es el número que el cliente teclea. Empieza en 1 y es denso.
	Number int `json:"n"`
	// Action distingue retomar de empezar. Ver MenuAction.
	Action MenuAction `json:"a"`
	// Kind es el tipo de evento (menu|cart|survey|media). Se puebla SIEMPRE,
	// también al retomar: quien confirma la elección la nombra por tipo, no por
	// identificador (E-3). Vacío con ActionRescue, que no habla de un tipo sino
	// de «lo que dejaste», sea lo que sea.
	Kind string `json:"k"`
	// EventID es el evento vivo que se retoma. Vacío con ActionStart.
	EventID string `json:"e,omitempty"`
	// Count acompaña a ActionRescue: cuántos hay para retomar. Solo compone la
	// frase —el «(2)» del final— y no decide nada; quien resuelve la elección
	// vuelve a preguntar por la lista, porque entre los dos mensajes el cliente
	// pudo haber cerrado uno.
	Count int `json:"c,omitempty"`
}

// Menu es el menú numérico dinámico del despachador: lo que se le enseña al
// cliente y, a la vez, lo que hace falta para entender su respuesta.
//
// Es un VALOR, no una sesión en memoria: se construye, se renderiza, se guarda
// (Encode) y se recupera al mensaje siguiente (DecodeMenu). El proceso que
// resuelve el número puede no ser el que lo mostró.
type Menu struct {
	// Options son las opciones en el orden en que se enseñan.
	Options []MenuOption `json:"options"`
	// Unfiltered avisa de que el menú se armó SIN filtrar por features porque
	// el tenant no tiene ninguna (la taxonomía del Plan 040 no está sembrada
	// para él). No se persiste (`json:"-"`) porque es una propiedad de CÓMO se
	// construyó este menú, no de lo que hay que recordar para resolverlo: tras
	// un DecodeMenu no significaría nada. Está para que quien lo construye lo
	// registre en el momento, que es cuando el dato es cierto.
	Unfiltered bool `json:"-"`
}

// Empty reporta si el menú no tiene ninguna opción que ofrecer. Un menú vacío no
// se envía: ver Render.
func (m Menu) Empty() bool {
	panic(pendiente.Implementar("events.Menu.Empty"))
}

// Encode serializa el menú para guardarlo entre dos mensajes.
//
// La forma, literal: {"options":[{"n":1,"a":"start","k":"cart"},{"n":2,"a":"resume","k":"cart","e":"<id>"},
// {"n":3,"a":"rescue","k":"","c":2}]}. Unfiltered no viaja. Un menú sin opciones serializa
// {"options":null}.
//
// Texto de error (literal): "events: serializar el menú: %w".
func (m Menu) Encode() (json.RawMessage, error) {
	panic(pendiente.Implementar("events.Menu.Encode"))
}

// DecodeMenu recupera el menú guardado. Sin nada guardado devuelve ErrNoMenu,
// que no es un fallo sino el caso corriente: este entrante no viene de un menú.
//
// raw vacío o nil ⇒ ErrNoMenu, a secas. Lo que no es JSON de un menú ⇒
// "events: leer el menú guardado: %w", que NO casa con ErrNoMenu. El menú recuperado trae
// Unfiltered a false siempre.
func DecodeMenu(raw []byte) (Menu, error) {
	panic(pendiente.Implementar("events.DecodeMenu"))
}

// Choice es la elección ya resuelta: qué hay que hacer y sobre qué. El llamante
// ejecuta; el despachador solo decide.
type Choice struct {
	// Action es retomar o empezar.
	Action MenuAction
	// Kind es el tipo elegido, poblado en las dos acciones.
	Kind string
	// EventID es el evento a retomar; vacío con ActionStart.
	EventID string
}

// Resolve interpreta la respuesta del cliente contra ESTE menú.
//
// Es la puerta que despacha SIN CLASIFICADOR (T2.3): un número no se comprende,
// se lee. No consulta la BD, no llama a nadie y no depende del reloj, así que la
// misma respuesta sobre el mismo menú da siempre la misma elección.
//
// El segundo retorno es false cuando la respuesta no es un número de este menú
// (texto libre, un número fuera de rango, vacío). Eso NO es un error: significa
// «esto no era una elección», y el llamante sigue su camino normal (resolver de
// triggers, módulo activo, clasificador).
//
// Lo que tolera: espacios alrededor y, al final, cualquier mezcla de «.», «)», «-», «·» y espacio
// ("2.", "2)", " 2 "). Solo dígitos ASCII; cero, un negativo o un número que no está entre los
// Number del menú no resuelven. Se busca por el NÚMERO de la opción, no por su posición. Count no
// viaja en la Choice.
func (m Menu) Resolve(reply string) (Choice, bool) {
	panic(pendiente.Implementar("events.Menu.Resolve"))
}

// KindName es el nombre de un tipo de evento en español, para nombrarlo delante
// del cliente. Es lo que hay que decir en vez del identificador cuando se
// confirma una elección, se cierra un evento (T2.4) o se ofrece rescatarlo
// (T3.6): «cerré tu pedido», nunca «carrito_001» ni «cart-2026-08-09-1830».
//
// UNA palabra por tipo, la misma en todas partes: la que devuelve esta función
// es la misma que aparece en las frases del menú. Que el cliente eligiera
// «Hacer un pedido» y luego leyera «cerré tu carrito» le haría preguntarse si
// son dos cosas distintas.
//
// El vocabulario, literal: cart → "pedido", survey → "encuesta", media → "documentos", menu →
// "menú". Un tipo sin vocabulario se nombra por el propio tipo, tal cual.
func KindName(kind string) string {
	panic(pendiente.Implementar("events.KindName"))
}

// Render arma el texto que se le manda al cliente por WhatsApp.
//
// Dos garantías que el test fija sobre la cadena resultante: cada opción se
// nombra por TIPO (nunca por identificador — ni el UUID ni el history_id
// aparecen por ninguna parte, E-3) y la numeración que se muestra es la misma
// que Resolve entiende.
//
// Un menú vacío devuelve la cadena vacía en vez de una pregunta sin respuestas
// posibles: así, un llamante que no comprobó Empty no puede mandar un menú
// hueco. El caso vacío lo atiende quien llama, no este render.
//
// El texto, literal: la cabecera "¿Qué quieres hacer? Responde con el número de la opción:", una
// línea en blanco, una línea "N. etiqueta" por opción (con el Number de la opción, no su
// posición), una línea en blanco y el cierre "Si prefieres otra cosa, escríbelo y te ayudamos.".
//
// Las etiquetas, literales. ActionStart: cart → "Hacer un pedido", survey → "Responder una
// encuesta", media → "Ver los documentos", menu → "Ver el menú"; otro tipo → "Empezar: <tipo>".
// ActionResume: cart → "Retomar el pedido que dejaste a medias", survey → "Continuar la encuesta
// que dejaste a medias", media → "Volver a los documentos que dejaste a medias", menu → "Volver
// al menú"; otro tipo → "Retomar: <tipo>". ActionRescue: "Retomar algo que dejaste a medias (N)"
// con N = Count.
func (m Menu) Render() string {
	panic(pendiente.Implementar("events.Menu.Render"))
}
