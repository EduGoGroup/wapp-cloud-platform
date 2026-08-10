package events

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// summary.go arma el RESUMEN DETERMINISTA del evento (Plan 043 · T3.3; ADR-0029
// E-4 / D-043.6): «en tu último evento esto es lo que ya habías decidido» + la
// lista.
//
// # Determinista quiere decir tres cosas, y las tres son verificables
//
//   - CERO LLM (REQ-21). No hay puerta por la que llamar al clasificador: los
//     constructores reciben DATOS —el estado durable ya leído— y no una
//     dependencia inyectable, así que no existe el parámetro por el que colar
//     una llamada. El test de dependencias del paquete lo fija desde fuera.
//   - CERO RELOJ Y CERO BD. Ninguna función de este archivo recibe context ni
//     conexión: no puede consultar nada, y por eso el mismo estado produce el
//     mismo resumen hoy y el martes.
//   - CERO MAPA EN LA SALIDA. Las respuestas de una encuesta viven en un
//     map[string]string y el recorrido de un mapa Go es aleatorio: se ordenan al
//     normalizar (normalizeAnswers). Sin ese orden, dos corridas sobre el
//     MISMO estado darían dos resúmenes distintos y la fila persistida dejaría de
//     ser comparable consigo misma.
//
// # De dónde sale el estado: de lo DURABLE, no de flow_state.vars
//
// Las LÍNEAS del pedido se leen de `intake_items` (la solicitud `open` del
// contacto), por IntakeLineReader. NO de `flow_state.vars["cart"]`, y la razón es
// exactamente el caso que este archivo existe para atender: al conmutar de evento
// el runtime BORRA el flow_state previo (runtime/events.go, enterEventFlow), así
// que en el instante del rescate —que es cuando el resumen se arma al vuelo— las
// líneas de `vars` ya no están. Un resumen que leyera de ahí saldría vacío justo
// cuando hace falta, y en verde: la fuente existiría, solo que sin datos.
//
// De `vars` se toma UNA cosa, el NIVEL de la sub-máquina, y con los ojos abiertos:
// tampoco sobrevive al salto. Está disponible en el camino de ESCRITURA (el
// abandono ocurre antes del borrado) y ausente en el de LECTURA, donde el resumen
// simplemente no dice «te quedaste…». Un matiz de menos no es una mentira; una
// línea de menos sí.
//
// La forma del nivel se REDECLARA en vez de importar
// internal/flujos/modules/cart: `events` es la capa genérica del evento —despacha
// por `kind`, no conoce módulos— y hacerla depender de UN módulo invertiría esa
// dirección para reutilizar cuatro literales. La duplicación se paga con un test:
// summary_test.go conduce el módulo cart REAL y le da a este decodificador el mapa
// que el módulo produjo, así que un renombre al otro lado sale en rojo aquí.
//
// La ENCUESTA se lee igual, de `survey_results` por SurveyAnswerReader, y NO de
// `vars["answers"]`. Conviene saber por qué, porque la razón no es la que parece:
//
// En el camino de ESCRITURA (el abandono) `vars` está viva —quien resume lo hace
// antes del borrado—, así que leer de ahí funcionaría. Lo que lo tumba es el otro
// camino, que es el que da sentido al plan: **al RESCATAR, el resumen se arma AL
// VUELO y con `vars` nil**, porque tras vencer la inactividad no hay fila
// persistida que leer. Con las respuestas en `vars`, una encuesta rescatada
// resumiría VACÍA — sin error y en verde—, que es el modo de fallo de este
// archivo: la fuente existe y no tiene por dónde entrar.
//
// Por eso las dos mitades del resumen salen de tablas y solo el NIVEL de la
// sub-máquina sale de `vars`: es un matiz que se pierde en el rescate (no hay
// flow_state) y que ninguna decisión del cliente depende de él. Y por eso faltar
// un lector es un ERROR (ver los dos centinelas) y no un resumen vacío.
//
// # Qué NO entra en el resumen
//
// La INDICACIÓN DE TODO EL PEDIDO (cartState.Note, el «déjalo en portería»). El
// contrato del payload está escrito en la migración 0051 —«Estructura, no prosa:
// {"lines":[{sku,label,qty,customization}]}»— y esa nota no está en él: es prosa
// de ENTREGA, la más cercana a decir dónde vive una persona, y la Ola 5 del Plan
// 042 ya la trató como PII al sacarla del payload del webhook (migración 0050).
// El payload de nivel 1 va EN CLARO (ADR-0034): lo que entra aquí es lo que se
// puede agregar por SQL sin identificar a nadie. La personalización de línea sí
// entra —está en el contrato citado, es lo que quien prepara tiene que leer, y su
// pantalla avisa de no escribir datos personales (D-041.19)—.
//
// El resumen tampoco lleva NUNCA un history_id ni el UUID del evento (E-3):
// ninguna función de este archivo recibe un Event, así que no tiene de dónde
// sacarlo.
//
// # Con quién se empareja
//
// Summary.Encode produce el json.RawMessage que Store.AppendSummary persiste con
// entry_kind='summary' y role='system' (T3.4), y Summary.Render produce el texto
// que el cliente LEE al reanudar. Son dos salidas de UN estado, no dos verdades:
// el texto se recompone del mismo valor que se serializó.

// SummaryLine es una línea YA DECIDIDA del pedido, con su etiqueta y su precio
// COPIADOS en el momento de decidirla.
//
// Copiados y no referenciados: el catálogo cambia, y un resumen que resolviera el
// precio al leerlo le enseñaría al cliente una cifra que él nunca aceptó. Es la
// misma razón por la que intake_items copia label y unit_price.
//
// Las etiquetas json son las del contrato del payload de la migración 0051 y, a
// la vez, los nombres de las columnas de `intake_items`, que es de donde salen
// estas líneas: el resumen no traduce nombres por el camino.
type SummaryLine struct {
	// SKU es el código de negocio de la línea. Con variante lleva el sufijo
	// "#<code>" que el carrito le pone ("TORTA-CHOC#V2"): el resumen no lo
	// interpreta, lo copia.
	SKU string `json:"sku"`
	// Label es lo que el cliente LEYÓ al elegir, ya con la variante pegada si la
	// hubo. Es lo único de la línea que se le enseña de vuelta.
	Label string `json:"label"`
	// Qty es la cantidad decidida.
	Qty int `json:"qty"`
	// UnitPrice es el precio unitario del momento de agregar.
	UnitPrice float64 `json:"unit_price"`
	// Customization es la indicación de ESA línea (el «sin cebolla»). Va con
	// omitempty: una línea sin indicación serializa exactamente como antes de que
	// el campo existiera.
	Customization string `json:"customization,omitempty"`
}

// total es el importe de la línea. Las indicaciones NO lo tocan (INV-13): sale de
// qty × unit_price y de nada más.
func (l SummaryLine) total() float64 { return float64(l.Qty) * l.UnitPrice }

// SummaryAnswer es una pregunta YA respondida de la encuesta: el id de la
// pregunta y el código de la opción elegida (design.md §10.D, answer_code = la
// clave de Options).
//
// Códigos, no prosa: aquí no entra ni el texto de la pregunta ni el de la opción.
// El texto vive en la definición del flujo —que puede editarse— y el estado
// durable no lo guarda; guardar aquí una copia sería inventar una segunda verdad
// sobre lo que se preguntó.
type SummaryAnswer struct {
	QuestionID string `json:"question_id"`
	AnswerCode string `json:"answer_code"`
}

// CartState es lo que el resumen necesita del pedido: las líneas decididas y el
// nivel de la sub-máquina donde se quedó. Sus dos campos vienen de DOS SITIOS
// distintos —las líneas de `intake_items` (durable), el nivel de `flow_state.vars`
// (efímero)— y por eso el tipo los junta aquí y no los busca él.
//
// Es un subconjunto DELIBERADO del sub-estado del carrito: no trae la categoría
// en foco, ni la página, ni el contador del checklist del comprador, porque nada
// de eso es una decisión que se le pueda devolver al cliente.
type CartState struct {
	// Level es el nivel de la sub-máquina (categories | articles | article |
	// variant | quantity | continue | summary | …). Se guarda tal cual en el
	// payload —es dato interno de análisis— y solo se TRADUCE al renderizar.
	// Vacío es normal: en el rescate ya no existe.
	Level string
	// Lines son las líneas ya decididas, en el orden en que se agregaron.
	Lines []SummaryLine
}

// Summary es el resumen determinista de un evento, listo para las dos salidas:
// Encode (la fila que se persiste) y Render (lo que el cliente lee).
//
// Se construye SOLO con los Build*: los constructores fijan Kind, y un Summary
// armado a mano con Kind vacío renderiza un hueco donde debería decir «pedido».
type Summary struct {
	// Kind es el tipo del evento resumido (cart | survey | menu | …). Es la clave
	// del vocabulario con que se le habla al cliente (KindName): «pedido», nunca
	// «carrito» ni «cart».
	Kind string `json:"kind"`
	// Level es el nivel de la sub-máquina del carrito. Vacío en el resto de tipos.
	Level string `json:"level,omitempty"`
	// Lines son las líneas decididas (solo cart).
	Lines []SummaryLine `json:"lines,omitempty"`
	// Answers son las preguntas respondidas (solo survey), ordenadas por
	// QuestionID.
	Answers []SummaryAnswer `json:"answers,omitempty"`
}

// summaryVarsKeyCart es la clave de flow_state.vars bajo la que el módulo cart
// serializa su sub-estado. Es la del otro lado (cart.stateVarKey) y aquí se
// redeclara por lo dicho arriba; el test la fija conduciendo el módulo real.
//
// Es la ÚNICA clave de vars que este archivo conoce, y de ella solo lee el nivel:
// todo lo demás sale de fuentes durables.
const summaryVarsKeyCart = "cart"

// Niveles de la sub-máquina del carrito, traducidos a lo que el cliente lee.
//
// Faltan a propósito los dos terminales (closed, cancelled): en un pedido ya
// confirmado o ya cancelado no hay un «te quedaste» que sea cierto, y decirlo
// sería contarle al cliente que dejó a medias algo que terminó. Un nivel que no
// esté en el mapa —uno nuevo, o el vacío— simplemente no imprime esa línea: el
// resumen pierde un matiz, que es mejor que enseñar jerga interna.
var summaryCartLevelWords = map[string]string{
	"categories":      "eligiendo una categoría",
	"articles":        "eligiendo un artículo",
	"article":         "mirando un artículo",
	"variant":         "eligiendo una presentación",
	"quantity":        "eligiendo la cantidad",
	"continue":        "decidiendo si agregar algo más",
	"summary":         "revisando el resumen del pedido",
	"item_note_scope": "escribiendo una indicación",
	"item_note":       "escribiendo una indicación",
	"order_note":      "escribiendo una indicación para todo el pedido",
	"buyer_data":      "completando tus datos",
}

// IntakeLineReader lee las LÍNEAS YA DECIDIDAS del pedido abierto de una
// conversación. Es la fuente DURABLE del resumen del carrito y el único puerto
// impuro de este archivo.
//
// La clave es la del EVENTO —(tenant, sesión, contacto)— y no la del pedido, a
// propósito. Hoy el almacén resuelve la solicitud abierta por (tenant, contacto)
// SIN sesión (store.GetOpenIntake), mientras que un evento es de una sesión
// concreta (REQ-18: el mismo contacto por dos números del mismo tenant son dos
// conversaciones). Pedir aquí la clave completa deja esa asimetría VISIBLE en la
// costura, donde el adaptador tiene que decidir qué hace con la sesión, en vez de
// enterrada en un resumen que un día mezclaría dos pedidos.
//
// Devuelve la lista vacía —no un error— cuando no hay pedido abierto: no haberlo
// es lo normal.
type IntakeLineReader interface {
	OpenIntakeLines(ctx context.Context, tenantID, sessionID, contactID string) ([]SummaryLine, error)
}

// SurveyAnswerReader lee las RESPUESTAS YA DADAS de la encuesta de un evento. Es
// el hermano de IntakeLineReader y la fuente durable de la otra mitad del resumen.
//
// # Por qué recibe el Event entero y no tres identificadores
//
// Porque acotar «las respuestas de ESTA encuesta» necesita más que la conversación,
// y la tabla lo demuestra: `survey_results` guarda (tenant, contacto, flow_id,
// flow_version, question_id, answer_code, created_at) y —desde la 0054 (Plan 043 ·
// Ola 4.5, D-043.21)— también `event_id`, que escribe el proyector del módulo
// survey. Pero las filas anteriores a esa migración lo llevan NULL, así que el
// adaptador sigue necesitando separar la encuesta de hoy de la del mes pasado
// TAMBIÉN para el legado. El Event trae lo que hace falta para las dos vías —su
// propio ID (la preferida, para fila nueva), FlowID y FlowVersion, que congela al
// nacer, y CreatedAt como cota inferior del fallback por timestamp— y de paso la
// sesión, que la tabla no tiene y que REQ-18 exige no mezclar. Ese trabajo es del
// adaptador; aquí se le da todo lo que necesita para hacerlo bien.
//
// Pasar el Event y no seis parámetros también evita que esta firma —que gobierna a
// dos frentes— tenga que crecer en cuanto el adaptador descubra que le falta un
// dato. `Event` es un tipo de este paquete: el puerto sigue sin saber nada del store.
//
// # Contrato del orden
//
// Devuelve las respuestas **en el orden en que se dieron** (que es como la tabla las
// acumula: es append-only, `id BIGSERIAL`). Si una pregunta aparece varias veces
// —el cliente la rehízo, o hubo reintento—, **gana la última**, y de eso se encarga
// LoadSummary: el adaptador no necesita `DISTINCT ON`. Ordenar para el payload es
// también cosa de aquí, no suya.
//
// Devuelve la lista vacía —no un error— cuando aún no ha respondido nada.
type SurveyAnswerReader interface {
	SurveyAnswers(ctx context.Context, ev Event) ([]SummaryAnswer, error)
}

// SummarySources son las fuentes durables de las que se arma un resumen, una por
// tipo de evento que acumula decisiones.
//
// Van juntas en un struct y no sueltas en la firma para que cablearlo sea un sitio
// y no una cuenta de parámetros: cuando un tipo nuevo traiga su propia fuente, se
// añade un campo aquí y las firmas de LoadSummary y PersistSummary no se mueven —
// que es justo lo que le pasó a este archivo la primera vez, con dos frentes
// esperando la firma.
type SummarySources struct {
	// Lines lee las líneas del pedido abierto (kind cart).
	Lines IntakeLineReader
	// Answers lee las respuestas ya dadas (kind survey).
	Answers SurveyAnswerReader
}

// Errores de fuente ausente. Los dos dicen lo mismo y por el mismo motivo: NO
// PODER LEER no es LEER Y NO ENCONTRAR. Devolver un resumen vacío cuando falta el
// lector borraría del historial justo lo que el cliente sí había decidido, y lo
// haría en silencio y en verde — que es como se descubrió que la encuesta llevaba
// toda la ola escribiendo cero filas.
var (
	ErrNoIntakeLineReader   = errors.New("events: sin IntakeLineReader no se puede resumir un pedido (leer y no encontrar no es lo mismo que no poder leer)")
	ErrNoSurveyAnswerReader = errors.New("events: sin SurveyAnswerReader no se puede resumir una encuesta (leer y no encontrar no es lo mismo que no poder leer)")
)

// LoadSummary arma el resumen de un evento leyendo la fuente DURABLE que
// corresponde a su tipo. Es LA puerta que cablea T3.4 y la que usa el rescate.
//
// vars es el flow_state.vars de la conversación y aporta lo único que no es
// durable: el nivel de la sub-máquina del carrito, en el camino de escritura.
//
// **`vars` nil es un caso de primera clase, no una degradación**: es el del
// RESCATE, donde el resumen se arma AL VUELO porque no hay fila persistida que
// leer, y donde el flow_state previo ya no existe. Por eso ninguna DECISIÓN puede
// salir de `vars` —solo el nivel, que es un matiz— y por eso las dos fuentes de
// SummarySources leen de tablas. Un resumen que necesitara `vars` para decir lo
// que el cliente decidió saldría vacío justo en el escenario que da sentido al
// plan, sin error y en verde.
//
// Un tipo que no acumula decisiones —media, o cualquier módulo que se enchufe
// mañana al Registry— devuelve un resumen VACÍO, no uno inventado: Empty() lo
// dice, y quien persiste debe preguntarlo.
func LoadSummary(ctx context.Context, src SummarySources, ev Event, vars map[string]any) (Summary, error) {
	switch ev.Kind {
	case "cart":
		if src.Lines == nil {
			return Summary{}, ErrNoIntakeLineReader
		}
		lines, err := src.Lines.OpenIntakeLines(ctx, ev.TenantID, ev.SessionID, ev.ContactID)
		if err != nil {
			return Summary{}, fmt.Errorf("events: leer las líneas del pedido para el resumen: %w", err)
		}
		return BuildCartSummary(CartState{Level: CartLevelFromVars(vars), Lines: lines}), nil
	case "survey":
		if src.Answers == nil {
			return Summary{}, ErrNoSurveyAnswerReader
		}
		answers, err := src.Answers.SurveyAnswers(ctx, ev)
		if err != nil {
			return Summary{}, fmt.Errorf("events: leer las respuestas de la encuesta para el resumen: %w", err)
		}
		return BuildSurveySummary(normalizeAnswers(answers)), nil
	case "menu":
		return BuildMenuSummary(), nil
	default:
		return Summary{Kind: ev.Kind}, nil
	}
}

// BuildCartSummary arma el resumen del pedido: las líneas decididas —con su
// etiqueta y su precio tal como se copiaron— más el nivel donde se quedó.
//
// Un carrito SIN líneas devuelve un resumen vacío aunque traiga nivel. No es un
// descuido: el resumen existe para devolverle al cliente lo que YA HABÍA
// DECIDIDO, y estar mirando la lista de categorías no es una decisión. Sin esa
// regla, abandonar un carrito recién abierto escribiría una fila que no dice
// nada y un automensaje que no ofrece nada.
//
// Copia las líneas: el resumen no comparte memoria con el estado del que salió,
// así que nadie puede cambiarlo por detrás después de haberlo construido.
func BuildCartSummary(st CartState) Summary {
	if len(st.Lines) == 0 {
		return Summary{Kind: "cart"}
	}
	return Summary{Kind: "cart", Level: st.Level, Lines: slices.Clone(st.Lines)}
}

// BuildSurveySummary arma el resumen de la encuesta: las preguntas ya
// respondidas, en el orden estable que fija normalizeAnswers.
func BuildSurveySummary(answers []SummaryAnswer) Summary {
	if len(answers) == 0 {
		return Summary{Kind: "survey"}
	}
	return Summary{Kind: "survey", Answers: slices.Clone(answers)}
}

// BuildMenuSummary devuelve el resumen del menú, que está VACÍO: LÍMITE v1
// DOCUMENTADO (T3.3, D-043.6).
//
// El menú no acumula estado valioso. Lo que guarda entre dos mensajes es a qué
// despacha cada número (events.Menu), y eso caduca en cuanto el cliente
// contesta: no es una decisión que se le pueda devolver dentro de una semana
// («ya habías elegido la opción 2» no significa nada sin la lista que la
// acompañaba, y esa lista se rearma distinta cada vez según lo que el tenant
// ofrezca hoy). La elección que SÍ importa —el tipo que pidió— ya quedó en el
// evento que nació de ella.
//
// Existe como constructor propio, y no disuelto en el default de BuildSummary,
// porque el límite tiene que ser visible donde se busca: quien venga a preguntar
// «¿y el menú?» encuentra aquí la respuesta y su porqué.
func BuildMenuSummary() Summary { return Summary{Kind: "menu"} }

// CartLevelFromVars lee de flow_state.vars el NIVEL de la sub-máquina del carrito
// —lo único que el resumen toma de ahí— y devuelve "" si no lo encuentra.
//
// Devuelve "" y no un error ante cualquier cosa que no entienda —clave ausente,
// nil, un tipo raro, JSON que no casa—, incluido el caso normal del rescate, donde
// el flow_state previo ya se borró. Es deliberado: el nivel es un MATIZ («te
// quedaste eligiendo la cantidad») y perderlo no puede tumbar un resumen que sí
// tiene las líneas, ni la conversación.
//
// Tolera el round-trip JSONB (map[string]any) igual que lo hace el módulo.
func CartLevelFromVars(vars map[string]any) string {
	raw, ok := vars[summaryVarsKeyCart]
	if !ok || raw == nil {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var st struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return ""
	}
	return st.Level
}

// normalizeAnswers deja las respuestas listas para el resumen: UNA por pregunta —la
// ÚLTIMA— y en orden estable por QuestionID.
//
// Las dos mitades son necesarias y por motivos distintos:
//
//   - **Una por pregunta**: `survey_results` es APPEND-ONLY (`id BIGSERIAL`, sin
//     unicidad por pregunta), así que rehacer una respuesta deja DOS filas. Un
//     resumen que las enseñara ambas le diría al cliente que respondió dos veces lo
//     que respondió una — la misma doble contabilidad que la marca de E-4 evita en
//     el historial. Gana la última porque el contrato del puerto entrega en el orden
//     en que se respondieron: la última es la que el cliente dejó en pie.
//   - **Orden por pregunta**: es lo que hace el resumen comparable consigo mismo.
//     Sin él, dos lecturas del mismo estado podrían serializar el payload en otro
//     orden y la fila persistida dejaría de ser reproducible.
func normalizeAnswers(in []SummaryAnswer) []SummaryAnswer {
	if len(in) == 0 {
		return nil
	}
	ultima := make(map[string]string, len(in))
	for _, a := range in {
		ultima[a.QuestionID] = a.AnswerCode
	}
	out := make([]SummaryAnswer, 0, len(ultima))
	for q, code := range ultima {
		out = append(out, SummaryAnswer{QuestionID: q, AnswerCode: code})
	}
	slices.SortFunc(out, func(a, b SummaryAnswer) int {
		return cmp.Compare(a.QuestionID, b.QuestionID)
	})
	return out
}

// Empty reporta que no hay nada que resumir. Es la pregunta que T3.4 tiene que
// hacer ANTES de persistir: un resumen vacío no se escribe en el historial (sería
// una fila que no dice nada) ni se le manda al cliente.
func (s Summary) Empty() bool { return len(s.Lines) == 0 && len(s.Answers) == 0 }

// Encode serializa el resumen para Store.AppendSummary, que exige
// json.RawMessage justamente para que por esa puerta no entre prosa (ver el doc
// del paquete): lo que sale de aquí es estructura, y por eso puede vivir EN CLARO
// en payload (nivel 1, ADR-0034).
//
// La serialización es estable —campos en orden fijo, sin mapas— así que dos
// resúmenes del mismo estado son el mismo byte.
func (s Summary) Encode() (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("events: serializar el resumen: %w", err)
	}
	return b, nil
}

// Render arma el texto que LEE EL CLIENTE al reanudar («esto es lo que ya habías
// decidido»). Un resumen vacío devuelve la cadena vacía, no un encabezado sin
// nada debajo: así, quien no comprobó Empty tampoco puede mandar un mensaje hueco.
//
// Despacha por CONTENIDO y no por Kind a propósito: lo que hay que enseñar son
// líneas o respuestas, y un tipo nuevo que acumule líneas se renderiza bien sin
// tocar esta función.
func (s Summary) Render() string {
	switch {
	case len(s.Lines) > 0:
		return s.renderLines()
	case len(s.Answers) > 0:
		return s.renderAnswers()
	default:
		return ""
	}
}

// renderLines pinta las líneas decididas con la MISMA forma que la pantalla de
// resumen del propio carrito («Café x2  $5.00», la indicación como sub-línea
// debajo de lo que describe, el TOTAL al final). Que el cliente vea el mismo
// renglón al confirmar y al retomar es lo que le permite reconocer su pedido; dos
// formatos distintos para lo mismo le harían dudar de si es lo mismo.
func (s Summary) renderLines() string {
	var b strings.Builder
	b.WriteString("Esto es lo que ya habías decidido en tu ")
	b.WriteString(KindName(s.Kind))
	b.WriteString(":")

	var total float64
	for _, l := range s.Lines {
		b.WriteString("\n")
		b.WriteString(l.Label)
		b.WriteString(" x")
		b.WriteString(strconv.Itoa(l.Qty))
		b.WriteString("  ")
		b.WriteString(summaryMoney(l.total()))
		if l.Customization != "" {
			b.WriteString("\n   ✏️ ")
			b.WriteString(l.Customization)
		}
		total += l.total()
	}
	b.WriteString("\nTOTAL  ")
	b.WriteString(summaryMoney(total))

	if phrase, ok := summaryCartLevelWords[s.Level]; ok {
		b.WriteString("\nTe quedaste ")
		b.WriteString(phrase)
		b.WriteString(".")
	}
	return b.String()
}

// renderAnswers dice CUÁNTAS preguntas llevaba respondidas y no cuáles, porque
// cuáles no se puede decir sin mentir: el estado durable guarda ids y códigos
// (question_id → answer_code), y el texto de la pregunta vive en la definición
// del flujo, que pudo editarse desde entonces. Enseñar «p3: b» sería jerga; ir a
// buscar el texto de hoy sería atribuirle al cliente una respuesta a una pregunta
// que quizá no es la que le hicimos. El detalle queda en el payload, que es donde
// sirve (para analizar, no para leer en WhatsApp).
func (s Summary) renderAnswers() string {
	n := len(s.Answers)
	noun := " preguntas de tu "
	if n == 1 {
		noun = " pregunta de tu "
	}
	return "Ya habías respondido " + strconv.Itoa(n) + noun + KindName(s.Kind) + "."
}

// SummaryAppender es lo ÚNICO que PersistSummary necesita del historial: la
// puerta que escribe un resumen. Interfaz estrecha —la satisface *Store— por el
// mismo motivo que RuleLister en kinds.go: quien abandona un evento no tiene por
// qué recibir el CRUD entero del historial, y con una firma así el test la puede
// suplantar sin base de datos.
type SummaryAppender interface {
	AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error)
}

// PersistSummary escribe el resumen del evento que se ABANDONA (T3.4, E-4).
//
// # Cuándo se llama (y cuándo NO) — decisión de Jhoan del 2026-08-09
//
// Se llama en los abandonos REALES del evento activo, que son tres: el SALTO POR
// TIPO, el `event_stop` y el ESCAPE GLOBAL.
//
// NO se llama al vencer la inactividad. REQ-19 lista además el «silencio
// conversacional», y esa lectura quedó imposible con E-9.2 sobre E-6: al vencer
// la ventana el evento NO muere —sigue `open` y rescatable, y lo único que pasa
// es que la conversación suelta el puntero—, así que no hay abandono que resumir.
// CALLARSE NO ES ABANDONAR. Escribir ahí una fila `summary` marcaría como cerrado
// lo que sigue abierto y, peor, la escribiría OTRA VEZ en cada vencimiento
// sucesivo del mismo evento: el historial acabaría contando varios finales de
// algo que nunca terminó.
//
// Y al RESCATAR tampoco se persiste: el texto que el cliente lee se arma al vuelo
// con BuildSummary(...).Render(). Un camino de LECTURA que escribe es un camino
// que puede fallar leyendo, y además duplicaría lo que ya está en la tabla.
//
// # Qué garantiza
//
// El grado y el rol NO son de quien llama: AppendSummary fija entry_kind='summary'
// y role='system' (esa es la MARCA de E-4, la que impide que el analista del hilo
// cuente dos veces la decisión que ya está en la tabla), y el CHECK de grado de la
// BD lo sostiene desde el otro lado.
//
// Un evento sin nada que resumir NO escribe fila: devuelve escrito=false y ningún
// error. Es la regla que impide un historial salpicado de resúmenes vacíos —un
// carrito abierto y abandonado sin agregar nada no decidió nada—, y por eso la
// comprobación vive AQUÍ y no en cada uno de los tres llamadores, que es donde se
// olvidaría.
//
// # Un orden que hay que respetar al cablearlo
//
// Se llama ANTES de borrar el flow_state de la conversación. No por elegancia: el
// nivel de la sub-máquina sale de ahí, y llamar después deja el resumen sin la
// línea del «te quedaste…» sin que nada falle ni avise. Las LÍNEAS no dependen de
// ese orden —son durables—, que es justo por lo que se leen del pedido.
//
// Devuelve el seq asignado y si llegó a escribirse.
func PersistSummary(ctx context.Context, w SummaryAppender, src SummarySources, ev Event, vars map[string]any) (int, bool, error) {
	s, err := LoadSummary(ctx, src, ev, vars)
	if err != nil {
		return 0, false, err
	}
	if s.Empty() {
		return 0, false, nil
	}
	body, err := s.Encode()
	if err != nil {
		return 0, false, err
	}
	seq, err := w.AppendSummary(ctx, ev.ID, body)
	if err != nil {
		return 0, false, fmt.Errorf("events: persistir el resumen del evento %s (tipo %s): %w", ev.ID, ev.Kind, err)
	}
	return seq, true, nil
}

// summaryMoney formatea un importe con el MISMO formato que las pantallas del
// carrito (cart/screens.go: money). Se redeclara por la misma razón que los
// niveles —este paquete no importa el módulo— y el test lo fija contra los
// precios reales del catálogo.
func summaryMoney(v float64) string { return fmt.Sprintf("$%.2f", v) }
