// source_composer.go — EL COMPOSITOR DEL `source_text` (Plan 044 · Ola 1 · T1.4;
// REQ-10b, REQ-10c, D-044.3b, D-044.24, D-044.26).
//
// # QUÉ HACE, EN UNA FRASE
//
// Cuando el agregador cierra una ventana, alguien tiene que convertir el HILO DEL
// EVENTO —que está cifrado en `conversation_event_messages`— en el texto que verá
// el pipeline P2–P4. Eso es esto: leer, descifrar en el borde, SEPARAR EL CONTEXTO
// DEL HILO LITERAL, y volver a cifrarlo en el sobre de `intake_jobs`.
//
// # 🔴 CORRE AL FLUSH, NUNCA EN LÍNEA CON EL ENTRANTE (D-044.26)
//
// Es el otro lado de la moneda del `IntakeAggregator`: allí el presupuesto es UNA
// sentencia y CERO lecturas porque TODO lo caro —leer el hilo, descifrarlo,
// rotular, cifrar— está aquí, fuera del camino del mensaje del cliente. El
// precedente de la casa es el mismo: el `WebhookSink` no descifra, descifra el
// worker (`integrations/worker.go`, D-042.9/D-042.11).
//
// # 🔴 ESTE FICHERO NO TIENE GATE PROPIO, Y NO PUEDE GANAR UNO (ADR-0044, D-044.28)
//
// Solo se llega aquí desde `closeWindow`, y a esa ventana solo llegó lo que el gate
// del agregador dejó pasar: `llm_intake` y nada más. Preguntar aquí otra vez sería
// duplicar la decisión —y preguntar por `api_llm` sería inventarse una nueva—: la
// VÍA por la que se analiza el `source_text` se elige mucho después, cuando el
// worker toma el job, no cuando se compone el texto. Un tenant de vía local
// compone, cifra y guarda su sobre igual que uno de vía API.
//
// # LAS DOS CLASES DE CONTEXTO SON UN SOLO MECANISMO, Y SE PUEDE COMPROBAR
//
// El hilo trae hasta cuatro `entry_kind` y este fichero los reparte en TRES
// destinos, no en cuatro caminos:
//
//	message              → HILO LITERAL   (lo que el cliente escribió / se le contestó en turno)
//	summary              → CONTEXTO       (ADR-0029 E-4, REQ-10b, D-044.3b)
//	message_out_of_turn  → CONTEXTO       (D-044.24)
//	decision (y lo desconocido) → NADA    (fail-closed, ver abajo)
//
// 🔑 LAS DOS CLASES DE CONTEXTO NO TIENEN CADA UNA SU RAMA. Comparten UNA:
// `contextLabel` decide si una entrada es contexto y con qué rótulo entra, y hay UN
// solo sitio en `ComposeSourceText` que produce líneas de contexto.
//
// La prueba es grepeable y por eso se escribe con el comando delante:
//
//	grep -n "KindSummary\|KindMessageOutOfTurn" source_composer.go | grep -v "^[0-9]*://"
//
// tiene que dar EXACTAMENTE DOS líneas de código, las dos DENTRO de la tabla
// `contextKinds` (este párrafo las nombra, pero es un comentario y el `grep -v` lo
// descarta). Si aparece una tercera, alguien ha abierto el segundo camino que el
// enunciado de T1.4 prohíbe —y dos caminos gemelos que divergen en un dato son la
// forma clásica de que uno de los dos se quede atrás—.
//
// # POR QUÉ EL RÓTULO NO ES DECORATIVO
//
// EL AUTOMENSAJE DE RESCATE LISTA PRODUCTOS (`internal/intakes/revalidate.go`, y el
// resumen que manda `sendResumeSummary`). Un LLM que lea esa lista sin saber quién
// la escribió extrae como pedido del cliente lo que imprimió nuestro propio
// automensaje: pediría dos tortas porque nosotros dijimos «tenías dos tortas». El
// rótulo compra la continuidad —el «sí, esas dos» del cliente sigue teniendo
// antecedente— sin comprar el bucle.
//
// # EL LITERAL NO SE QUEDA EN NINGÚN OTRO SITIO (REQ-10c)
//
// El texto en claro vive SOLO en memoria, entre `ListThread` y `Encrypt`. No entra
// a `flow_events`, ni a logs, ni a telemetría, ni a un campo nuevo. Los logs de
// este fichero llevan identificadores y NÚMEROS (cuántos mensajes, cuántas entradas
// de contexto, cuántos bytes), nunca contenido; y los errores tampoco lo citan.
package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ---------------------------------------------------------------------------
// LOS RÓTULOS. SON EL CONTRATO CON EL PROMPT DE P2.
// ---------------------------------------------------------------------------

// Delimitadores del `source_text`. Se escriben con `###` y en MAYÚSCULAS porque
// tienen que sobrevivir dentro de un prompt junto a texto libre de un cliente: un
// separador que un cliente pueda escribir por accidente no separa nada.
const (
	// sourceContextHeader abre el bloque de CONTEXTO: lo que el sistema resumió y lo
	// que el negocio dijo por su cuenta. Todo lo que hay aquí dentro es antecedente,
	// NO es lo que el cliente está pidiendo.
	sourceContextHeader = "### CONTEXTO PREVIO — NO es lo que el cliente está pidiendo ###"
	sourceContextFooter = "### FIN DEL CONTEXTO PREVIO ###"
	// sourceLiteralHeader abre el HILO LITERAL: lo que se escribió de verdad en esta
	// conversación, en orden. Es el ÚNICO bloque del que puede salir una `evidence`.
	sourceLiteralHeader = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###"
	sourceLiteralFooter = "### FIN DE LOS MENSAJES ###"
)

// contextKinds ES EL MECANISMO ÚNICO de las dos clases de contexto: la clave dice
// QUÉ es contexto y el valor con qué rótulo entra. Las dos clases no son dos
// caminos, son dos FILAS de esta tabla — y añadir una tercera clase de contexto el
// día que exista es añadir una fila, no escribir una rama.
var contextKinds = map[events.EntryKind]string{
	events.KindSummary:          "resumen del sistema",
	events.KindMessageOutOfTurn: "mensaje del negocio fuera de turno",
}

// contextLabel responde LA pregunta —¿esta entrada es contexto, y cómo se rotula?—
// para las dos clases a la vez. Es la función por la que pasan ambas.
func contextLabel(kind events.EntryKind) (string, bool) {
	label, ok := contextKinds[kind]
	return label, ok
}

// speakerOf traduce el rol a la palabra que lee el LLM en el hilo literal.
//
// 🔴 SOLO `client` es «cliente»; TODO lo demás —incluido un `system` que no
// debería aparecer nunca con entry_kind='message'— cae a «negocio». La asimetría es
// deliberada y es la misma que gobierna el resto del fichero: si hay duda sobre
// quién habló, la respuesta segura es la que NO convierte texto ajeno en pedido del
// cliente.
func speakerOf(role events.Role) string {
	if role == events.RoleClient {
		return "cliente"
	}
	return "negocio"
}

// ---------------------------------------------------------------------------
// 📌 TODO(Plan 044 · Ola 2 — EL PROMPT DE P2). LA REGLA, ESCRITA DONDE SE VA A NECESITAR.
// ---------------------------------------------------------------------------
//
// T1.4 pide documentar en el prompt de P2 que el bloque de contexto va rotulado. El
// prompt de P2 vive en el módulo `llm` de `wapp-shared`, que es OTRO REPO y OTRO
// RELEASE, y esta ola NO lo consume siquiera (D-044.25: «la Ola 1 NO necesita el
// release de llm/v0.1.0»). Así que la regla se deja escrita aquí, que es el punto
// del cloud donde la Ola 2 compondrá el prompt a partir de este `source_text`, y
// quien la lleve allí la copia de aquí:
//
//	EL `source_text` VIENE EN DOS BLOQUES ROTULADOS Y NO SON INTERCAMBIABLES.
//
//	 1. Lo que va entre sourceContextHeader y sourceContextFooter es CONTEXTO:
//	    resúmenes que escribió el sistema y mensajes que el negocio mandó sin que el
//	    cliente preguntara. Sirve para RESOLVER REFERENCIAS («sí, esas dos») y para
//	    nada más. De ahí NO se extrae ni un ítem, ni una cantidad, ni una fecha, y
//	    NINGUNA `evidence` puede citarlo. El automensaje de rescate LISTA PRODUCTOS:
//	    si el modelo los extrae, el presupuesto sale con lo que dijimos nosotros.
//	 2. Lo que va entre sourceLiteralHeader y sourceLiteralFooter es el HILO
//	    LITERAL. TODO lo que el borrador afirme tiene que salir de aquí, y las
//	    `evidence` son subcadenas de ESTE bloque (REQ-13).
//	 3. Si el `source_text` no trae bloque de contexto, es que no había: no se
//	    inventa uno ni se trata el principio del hilo como si lo fuera.
//
// El día que se toque `wapp-shared/llm`, esta nota se retira de aquí y se convierte
// en las líneas del prompt. Mientras tanto NO se edita ese repo desde este plan.

// ---------------------------------------------------------------------------
// LA COMPOSICIÓN (función PURA)
// ---------------------------------------------------------------------------

// Composed es el resultado de componer un hilo. Se devuelve entero —y no solo el
// texto— porque los efectos que T1.4 tiene que garantizar son NÚMEROS y hay que
// poder afirmarlos: cuántos mensajes cuenta el hilo (que NO son los del contexto) y
// cuántas entradas de contexto entraron.
type Composed struct {
	// Text es lo que se cifra en el sobre: el bloque de contexto (si hay) seguido
	// del hilo literal.
	Text string
	// Context es SOLO el bloque de contexto, ya rotulado, sin sus delimitadores.
	// Vacío si el hilo no traía ninguna entrada de contexto.
	Context string
	// Literal es SOLO el hilo literal, sin delimitadores. Es la región de la que
	// pueden salir `evidence` (REQ-13) y por eso se publica aparte: un test puede
	// afirmar que el texto del resumen NO está aquí dentro.
	Literal string
	// Messages es EL CONTADOR DE VOLUMEN DEL HILO: cuántas entradas `message`
	// entraron. 🔴 El contexto NO suma aquí (REQ-10b (c)) — un resumen no es
	// actividad del cliente. Es el número del que tiene que salir la métrica de
	// volumen de la O5 cuando exista: UNO, no dos que se puedan desincronizar.
	Messages int
	// ContextEntries es cuántas entradas de contexto entraron, sumadas LAS DOS
	// CLASES. Existe para operar y para los tests; no alimenta ninguna métrica de
	// volumen del hilo.
	ContextEntries int
}

// Empty dice si no hay nada que valga la pena cifrar. Se mide por MENSAJES, no por
// longitud del texto: ver la guarda de ComposeAtFlush.
func (c Composed) Empty() bool { return c.Messages == 0 }

// ComposeSourceText reparte las entradas del hilo en los dos bloques y arma el
// texto. Es PURA: no lee, no escribe, no cifra y no registra nada — así el reparto,
// que es la regla de negocio de T1.4, se puede probar sin montar nada.
//
// 🔴 SE MIRA `Kind` ANTES DE TOCAR `Text`, SIEMPRE. Es el criterio literal de la
// tarea («el agregador nunca lee el cuerpo sin mirar antes entry_kind») y es
// estructural aquí: `e.Text` no se usa fuera de los dos brazos del switch.
//
// 🔴 NO HAY DEDUPLICACIÓN POR TEXTO, Y ES UNA DECISIÓN. Un resumen REPITE a
// propósito lo que el cliente ya dijo; una implementación que descartara líneas
// repetidas tiraría el ORIGINAL —el resumen suele ir primero, porque se inyecta al
// cambiar de evento— y destruiría justo la evidencia que REQ-10b protege. La
// deduplicación de la ráfaga existe, pero es por `wa_message_id` y vive en
// `Observe` (`alreadySeen`) y en el dedupe persistente de ingesta (Plan 028 · T6):
// mira identificadores, no contenido, así que ninguna entrada de contexto puede
// entrar en ella — las entradas de contexto no pasan por `Observe`.
func ComposeSourceText(entries []events.ThreadEntry) Composed {
	var (
		out      Composed
		contexto []string
		literal  []string
	)
	for _, e := range entries {
		label, esContexto := contextLabel(e.Kind)
		switch {
		case esContexto:
			// EL ÚNICO SITIO QUE PRODUCE CONTEXTO. Las dos clases pasan por aquí.
			if e.Text == "" {
				continue
			}
			contexto = append(contexto, "["+label+"] "+e.Text)
			out.ContextEntries++
		case e.Kind == events.KindMessage:
			if e.Text == "" {
				continue
			}
			literal = append(literal, speakerOf(e.Role)+": "+e.Text)
			out.Messages++
		default:
			// FAIL-CLOSED, y cubre dos casos: `decision` —que es estructura, no prosa,
			// y ya viene con Text vacío— y cualquier `entry_kind` que se invente
			// después de escribir esto. Lo desconocido NO entra como literal del
			// cliente: entrar por defecto al hilo es exactamente cómo el rescate se
			// convertiría en un pedido. Si algún día un grado nuevo debe aportar
			// contexto, se añade su fila a `contextKinds` y no una rama aquí.
			continue
		}
	}
	out.Context = strings.Join(contexto, "\n")
	out.Literal = strings.Join(literal, "\n")

	var b strings.Builder
	if out.Context != "" {
		b.WriteString(sourceContextHeader)
		b.WriteString("\n")
		b.WriteString(out.Context)
		b.WriteString("\n")
		b.WriteString(sourceContextFooter)
		b.WriteString("\n")
	}
	if out.Literal != "" {
		b.WriteString(sourceLiteralHeader)
		b.WriteString("\n")
		b.WriteString(out.Literal)
		b.WriteString("\n")
		b.WriteString(sourceLiteralFooter)
	}
	out.Text = b.String()
	return out
}

// ---------------------------------------------------------------------------
// EL COMPOSITOR CABLEADO
// ---------------------------------------------------------------------------

// ThreadReader es la lectura del hilo del evento, DESCIFRADA en el borde. Interfaz
// local y estrecha (ISP, mismo patrón que AggregationSettings): la satisface
// *events.Store, que es quien tiene el FieldCipher del hilo.
type ThreadReader interface {
	ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error)
}

// SourceTextWriter es lo ÚNICO que el compositor necesita de `intake_jobs`: dejar
// el sobre. No puede listar, no puede cerrar y no puede abrir ventanas. Lo satisface
// intake.JobStore.
type SourceTextWriter interface {
	PutSourceText(ctx context.Context, k intake.WindowKey, env intake.SourceText) (bool, error)
}

// DefaultThreadLimit acota cuántas entradas del hilo entran al `source_text`. Es un
// techo de TAMAÑO DE PROMPT, no una regla de negocio: el pipeline paga por token y
// un hilo de mil entradas no cabe en ninguna ventana de contexto útil. El recorte
// muerde por el PRINCIPIO del hilo (ver listThreadSQL): lo que se pierde es lo más
// viejo.
//
// 🔧 SE EXPORTÓ CON T4.6 (Plan 044 · Ola 4), y el motivo es que apareció un SEGUNDO
// lector del mismo hilo con la misma pregunta: `/reanalyze` comprueba que HAY
// material antes de abrir el job, y esa comprobación tiene que mirar exactamente las
// entradas que este compositor va a componer. Con dos constantes, un hilo largo
// podría pasar la comprobación y componerse vacío —o al revés— y el desenlace sería
// un job sin sobre que muere sin que nadie sepa por qué. Es la misma constante o son
// dos verdades.
const DefaultThreadLimit = 200

// SourceTextComposer implementa SourceComposer (aggregator.go): lee el hilo,
// compone, cifra y guarda.
type SourceTextComposer struct {
	log    logger.Logger
	thread ThreadReader
	jobs   SourceTextWriter
	// cipher es el MISMO stack de claves que cifra el hilo, los contactos y los
	// datos del comprador (keyring versionado del Plan 012). Un segundo cipher sería
	// una segunda rotación que gestionar.
	cipher *crypto.FieldCipher
	limit  int
}

// SourceTextComposerOption configura el compositor al construirlo.
type SourceTextComposerOption func(*SourceTextComposer)

// WithThreadLimit fija cuántas entradas del hilo entran al literal. <=0 se ignora.
func WithThreadLimit(n int) SourceTextComposerOption {
	return func(c *SourceTextComposer) {
		if n > 0 {
			c.limit = n
		}
	}
}

// NewSourceTextComposer construye el compositor. Con cualquier dependencia a nil
// es un no-op seguro (mismo criterio nil-safe que el resto del runtime): el job se
// queda en `pending` con el sobre a NULL, que es una forma legítima en la 0072.
func NewSourceTextComposer(log logger.Logger, thread ThreadReader, jobs SourceTextWriter,
	cipher *crypto.FieldCipher, opts ...SourceTextComposerOption) *SourceTextComposer {
	c := &SourceTextComposer{log: log, thread: thread, jobs: jobs, cipher: cipher, limit: DefaultThreadLimit}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ComposeAtFlush implementa SourceComposer. Se llama UNA vez por ventana, DESPUÉS
// de que la transición `aggregating → pending` haya tenido éxito y FUERA del camino
// del entrante.
//
// Devolver error NO reabre la ventana ni corta nada: el llamante lo LOGUEA y el job
// se queda en `pending` con el sobre vacío.
func (c *SourceTextComposer) ComposeAtFlush(ctx context.Context, key intake.WindowKey) error {
	if c == nil || c.log == nil || c.thread == nil || c.jobs == nil || c.cipher == nil {
		return nil
	}
	if !key.Valid() {
		return fmt.Errorf("compositor: clave de ventana incompleta")
	}

	entries, err := c.thread.ListThread(ctx, key.EventID, c.limit)
	if err != nil {
		return fmt.Errorf("compositor: leer el hilo del evento %s: %w", key.EventID, err)
	}

	composed := ComposeSourceText(entries)
	if composed.Empty() {
		// 🔴 CERO MENSAJES ⇒ NO SE ESCRIBE NADA, ni siquiera si hubo contexto, y esto
		// es una decisión y no un atajo: un `source_text` hecho SOLO de contexto es un
		// prompt donde lo único que hay son productos que listamos NOSOTROS y ninguna
		// frase del cliente que los contradiga. Es literalmente el accidente que
		// D-044.24 describe. El sobre se queda NULL —forma legítima en la 0072— y el
		// worker de la Ola 2 verá un job sin literal.
		//
		// El caso normal en que esto ocurre HOY: el tenant no tiene el hilo escrito
		// (T1.6 apagado por falta de la feature) o la ventana se abrió con media/audio
		// sin texto. Por eso es Warn y no Error: no hay nada roto.
		c.log.Warn("compositor: la ventana cerró sin una sola línea del hilo; el sobre se queda vacío",
			"tenant_id", key.TenantID, "session_id", key.SessionID, "event_id", key.EventID,
			"entradas_de_contexto", composed.ContextEntries)
		return nil
	}

	// EL CIFRADO, y es lo último que toca el literal. A partir de aquí solo viajan
	// bytes. El error de Encrypt no se enriquece con nada del texto.
	enc, dek, kekID, err := c.cipher.Encrypt(composed.Text)
	if err != nil {
		return fmt.Errorf("compositor: cifrar el literal de la ventana del evento %s: %w", key.EventID, err)
	}

	escrito, err := c.jobs.PutSourceText(ctx, key, intake.SourceText{Enc: enc, DEK: dek, KEKID: kekID})
	if err != nil {
		return fmt.Errorf("compositor: guardar el literal de la ventana del evento %s: %w", key.EventID, err)
	}
	if !escrito {
		// No había dónde escribir: la fila ya tenía sobre, o la ventana no está en
		// `pending`. No es un error —es idempotencia— pero se dice, porque si pasa
		// siempre significa que alguien está componiendo dos veces.
		c.log.Debug("compositor: la ventana ya tenía literal; no se sobrescribe",
			"tenant_id", key.TenantID, "event_id", key.EventID)
		return nil
	}

	// EL LOG LLEVA NÚMEROS, NUNCA CONTENIDO (REQ-10c). `bytes` es un tamaño, no un
	// texto; `mensajes` y `contexto` son los contadores de Composed.
	c.log.Debug("compositor: literal compuesto y cifrado",
		"tenant_id", key.TenantID, "session_id", key.SessionID, "event_id", key.EventID,
		"mensajes", composed.Messages, "contexto", composed.ContextEntries, "bytes", len(composed.Text))
	return nil
}

// El compositor satisface el hueco que dejó T1.1, comprobado en compilación. Es lo
// que impide que un cambio de firma de SourceComposer deje esto colgando.
var _ SourceComposer = (*SourceTextComposer)(nil)
