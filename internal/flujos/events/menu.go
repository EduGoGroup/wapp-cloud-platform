package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
func (m Menu) Empty() bool { return len(m.Options) == 0 }

// Encode serializa el menú para guardarlo entre dos mensajes.
func (m Menu) Encode() (json.RawMessage, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("events: serializar el menú: %w", err)
	}
	return b, nil
}

// DecodeMenu recupera el menú guardado. Sin nada guardado devuelve ErrNoMenu,
// que no es un fallo sino el caso corriente: este entrante no viene de un menú.
func DecodeMenu(raw []byte) (Menu, error) {
	if len(raw) == 0 {
		return Menu{}, ErrNoMenu
	}
	var m Menu
	if err := json.Unmarshal(raw, &m); err != nil {
		return Menu{}, fmt.Errorf("events: leer el menú guardado: %w", err)
	}
	return m, nil
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
func (m Menu) Resolve(reply string) (Choice, bool) {
	n, ok := parseOptionNumber(reply)
	if !ok {
		return Choice{}, false
	}
	for _, o := range m.Options {
		if o.Number == n {
			return Choice{Action: o.Action, Kind: o.Kind, EventID: o.EventID}, true
		}
	}
	return Choice{}, false
}

// parseOptionNumber extrae el número de una respuesta escrita por un humano.
//
// Tolera lo que un dedo produce sin querer —espacios alrededor y el punto o el
// paréntesis de «2.» / «2)»— y nada más. No intenta entender «el segundo» ni
// «quiero el dos»: eso ya es comprender, y comprender es de otro (y aquí, de
// nadie). Solo dígitos ASCII: un número en otro sistema de numeración no es lo
// que el cliente copió de la lista que le enseñamos.
func parseOptionNumber(reply string) (int, bool) {
	s := strings.TrimSpace(reply)
	s = strings.TrimRight(s, ".)-· ")
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// kindWords son las palabras con que se le habla al cliente de un tipo de
// evento. Tres campos y no uno con reglas de género: el español no compone
// «Empezar un/una X» sin conocer el artículo, y un motorcito de concordancia
// para cuatro tipos es más frágil que escribir las cuatro frases.
type kindWords struct {
	name   string
	start  string
	resume string
}

// kindVocabulario es el vocabulario de los tipos de fábrica (menu|cart|survey|
// media). Es la ÚNICA fuente de lo que el cliente lee: aquí no entra ni un
// identificador, ni un history_id, ni el nombre técnico del módulo.
//
// Las frases de start están redactadas para ser CIERTAS en los dos desenlaces
// que puede tener elegirlas (E-11): que nazca un evento nuevo o que el ejecutor
// conmute hacia el que ya estaba en curso. Por eso ninguna promete «uno nuevo y
// vacío» —«Hacer un pedido» es verdad tanto si el pedido empieza en blanco como
// si el cliente vuelve al que llevaba a medias—, y por eso las de resume dicen
// «que dejaste a medias»: nombran lo que hay, que es lo único que las distingue
// de su gemela cuando las dos aparecen en la misma lista.
var kindVocabulario = map[string]kindWords{
	// cart se llama «pedido» en TODO lo que lee el cliente —el menú, el rescate y
	// la confirmación de cierre—, por decisión de producto de Jhoan (2026-08-09).
	// `cart` sigue siendo el identificador interno del tipo; lo que se unifica es
	// la palabra, para que nadie lea «pedido» al elegir y «carrito» al cerrar.
	"cart":   {name: "pedido", start: "Hacer un pedido", resume: "Retomar el pedido que dejaste a medias"},
	"survey": {name: "encuesta", start: "Responder una encuesta", resume: "Continuar la encuesta que dejaste a medias"},
	"media":  {name: "documentos", start: "Ver los documentos", resume: "Volver a los documentos que dejaste a medias"},
	"menu":   {name: "menú", start: "Ver el menú", resume: "Volver al menú"},
}

// words devuelve el vocabulario de un tipo, con un respaldo para los que aún no
// lo tienen. El respaldo dice el NOMBRE DEL TIPO tal cual, que es lo que el
// diseño permite enseñar (E-3 prohíbe identificadores, no nombres de tipo):
// enchufar un módulo nuevo en el Registry no debe dejar al cliente ante una
// opción en blanco, y una frase algo seca es preferible a un menú mudo.
func words(kind string) kindWords {
	if w, ok := kindVocabulario[kind]; ok {
		return w
	}
	return kindWords{
		name:   kind,
		start:  "Empezar: " + kind,
		resume: "Retomar: " + kind,
	}
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
func KindName(kind string) string { return words(kind).name }

// menuHeader es la pregunta que abre el menú y la instrucción de cómo responder.
// Tutea porque así habla el resto del producto de cara al cliente (el aviso de
// escape del runtime, defaultEscapeMessage, y el ejemplo de Marta del ADR-0029
// §E-9.3): una sola voz por conversación.
const menuHeader = "¿Qué quieres hacer? Responde con el número de la opción:"

// menuFooter deja abierta la puerta al lenguaje natural: el menú es un atajo, no
// una cárcel. Quien escriba otra cosa cae por el camino de siempre.
const menuFooter = "Si prefieres otra cosa, escríbelo y te ayudamos."

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
func (m Menu) Render() string { return m.renderList(menuHeader, menuFooter) }

// renderList es EL componente de render de listas numeradas del despachador
// (D-043.3): cabecera, opciones numeradas y las líneas de cierre que le dé el
// llamante, cada una separada por una línea en blanco.
//
// La prosa entra por parámetro y no se guarda en Menu a propósito: el menú se
// PERSISTE entre dos mensajes de WhatsApp y lo que hay que recordar es a qué
// despacha cada número, no la frase con que se dijo. Así el automensaje de rescate
// (T3.6) y la entrada que ofrece (T3.8) comparten numeración, etiquetas y formato
// con el menú sin que ninguno tenga su propio bucle de render — que es como
// aparecen los menús que numeran distinto que Resolve.
func (m Menu) renderList(header string, tail ...string) string {
	if m.Empty() {
		return ""
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	for _, o := range m.Options {
		b.WriteString(strconv.Itoa(o.Number))
		b.WriteString(". ")
		b.WriteString(optionLabel(o))
		b.WriteString("\n")
	}
	for _, t := range tail {
		if t == "" {
			continue // un cierre vacío no deja una línea en blanco de más
		}
		b.WriteString("\n")
		b.WriteString(t)
	}
	return b.String()
}

// optionLabel es la frase de una opción según lo que hace.
func optionLabel(o MenuOption) string {
	switch o.Action {
	case ActionRescue:
		return rescueEntry(o.Count)
	case ActionResume:
		return words(o.Kind).resume
	default:
		return words(o.Kind).start
	}
}

// rescueEntry es la entrada FINAL de la conversación que ofrece (T3.8): una sola
// línea que resume todo lo que el contacto dejó a medias, cuente lo que cuente.
// Nombra la cantidad y no los tipos porque los tipos ya están arriba, en las
// opciones de empezar, y repetirlos haría leer dos veces la misma palabra con dos
// significados distintos.
func rescueEntry(n int) string {
	return "Retomar algo que dejaste a medias (" + strconv.Itoa(n) + ")"
}

// rescueHeader abre el AUTOMENSAJE de rescate (T3.6): dice que no hay nada en
// curso y cómo retomar lo que quedó.
//
// No dice «evento» ni «carrito»: «evento» es vocabulario nuestro y al cliente no le
// significa nada, y «carrito» está proscrito de cara al cliente (se dice «pedido»,
// decisión de producto de Jhoan). Tutea, como el resto del producto, y no menciona
// ningún identificador (E-3) — lo que se ofrece se nombra por tipo, en las
// opciones.
const rescueHeader = "Pasó un rato sin novedades, así que ahora mismo no tenemos nada en curso. " +
	"Si quieres, puedes retomar lo que dejaste a medias — responde con el número:"

// rescueMore es el cierre «…y N más» de la lista de rescate cuando hay más de los
// que se enseñan (T3.8 · punto 3).
//
// El N es lo que reveló el lote pedido a la BD (topeRescatables + 1), no un
// COUNT(*) de todos: dice «hay al menos uno más», que es lo que el cliente
// necesita saber para no creer que la lista es todo lo que tiene. Contar de verdad
// costaría una segunda consulta para una frase.
func rescueMore(n int) string {
	if n <= 0 {
		return ""
	}
	return "…y " + strconv.Itoa(n) + " más"
}

// tagline es la COLETILLA del camino con clasificador (T3.8 · punto 2): se añade al
// final de la respuesta que atiende la intención del cliente, para que sepa que lo
// que dejó a medias sigue ahí sin que se lo interrumpa con un menú.
//
// Nombra por TIPO y jamás por identificador (E-3), y usa «tu» en vez de artículo
// («tu pedido», «tu encuesta») porque así una sola frase vale para los cuatro tipos
// sin un motorcito de concordancia de género para cuatro palabras.
//
// Sin nada que retomar devuelve la cadena vacía: quien la añade no tiene que
// comprobar nada, y una coletilla que no dice nada no se escribe.
func tagline(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	nombres := make([]string, 0, len(kinds))
	for _, k := range kinds {
		nombres = append(nombres, "tu "+KindName(k))
	}
	sobran := 0
	if len(nombres) > topeRescatables {
		sobran = len(nombres) - topeRescatables
		nombres = nombres[:topeRescatables]
	}
	lista := unirEnEspanol(nombres)
	if sobran > 0 {
		lista += " y algo más"
	}
	if len(kinds) == 1 {
		return "Por cierto, " + lista + " sigue a medias — dime si quieres retomarlo."
	}
	return "Por cierto, " + lista + " siguen a medias — dime si quieres retomarlos."
}

// unirEnEspanol une una lista con comas y una «y» final, como se escribe una
// enumeración en español y no como la escribiría un strings.Join.
func unirEnEspanol(partes []string) string {
	switch len(partes) {
	case 0:
		return ""
	case 1:
		return partes[0]
	default:
		return strings.Join(partes[:len(partes)-1], ", ") + " y " + partes[len(partes)-1]
	}
}
