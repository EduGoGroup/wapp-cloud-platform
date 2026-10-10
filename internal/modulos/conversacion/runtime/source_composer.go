// Porta internal/flujos/runtime/source_composer.go @ e0159171

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
// Es el otro lado de la moneda del IntakeAggregator: allí el presupuesto es UNA
// sentencia y CERO lecturas porque TODO lo caro —leer el hilo, descifrarlo,
// rotular, cifrar— está aquí, fuera del camino del mensaje del cliente.
//
// # 🔴 ESTE FICHERO NO TIENE GATE PROPIO, Y NO PUEDE GANAR UNO (ADR-0044, D-044.28)
//
// Solo se llega aquí desde el cierre de una ventana, y a esa ventana solo llegó lo
// que el gate del agregador dejó pasar: `llm_intake` y nada más. Preguntar aquí otra
// vez sería duplicar la decisión —y preguntar por `api_llm` sería inventarse una
// nueva—: la VÍA por la que se analiza el `source_text` se elige mucho después,
// cuando el worker toma el job. Un tenant de vía local compone, cifra y guarda su
// sobre igual que uno de vía API. Por eso el compositor no recibe ningún resolver de
// derechos.
//
// # EL LITERAL NO SE QUEDA EN NINGÚN OTRO SITIO (REQ-10c)
//
// El texto en claro vive SOLO en memoria, entre la lectura del hilo y el cifrado. No
// entra a `flow_events`, ni a logs, ni a telemetría, ni a un campo nuevo. Los logs de
// este fichero llevan identificadores y NÚMEROS (cuántos mensajes, cuántas entradas
// de contexto, cuántos bytes), nunca contenido; y los errores tampoco lo citan.
//
// # LA REGLA PARA EL PROMPT DE P2 (quien lo edite la copia de aquí)
//
// El `source_text` viene en DOS bloques rotulados y no son intercambiables:
//
//  1. Lo que va entre la cabecera y el pie de CONTEXTO son resúmenes que escribió el
//     sistema y mensajes que el negocio mandó sin que el cliente preguntara. Sirve
//     para RESOLVER REFERENCIAS («sí, esas dos») y para nada más. De ahí NO se extrae
//     ni un ítem, ni una cantidad, ni una fecha, y NINGUNA `evidence` puede citarlo.
//     El automensaje de rescate LISTA PRODUCTOS: si el modelo los extrae, el
//     presupuesto sale con lo que dijimos nosotros.
//  2. Lo que va entre la cabecera y el pie de MENSAJES es el HILO LITERAL. TODO lo que
//     el borrador afirme tiene que salir de aquí, y las `evidence` son subcadenas de
//     ESTE bloque (REQ-13).
//  3. Si el `source_text` no trae bloque de contexto, es que no había: no se inventa
//     uno ni se trata el principio del hilo como si lo fuera.

package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Delimitadores del `source_text`: SON EL CONTRATO CON EL PROMPT DE P2. Se escriben
// con `###` y en MAYÚSCULAS porque tienen que sobrevivir dentro de un prompt junto a
// texto libre de un cliente: un separador que un cliente pueda escribir por accidente
// no separa nada. (Por accidente: a propósito sí puede, ver ComposeSourceText.)
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
//
// La prueba es grepeable: fuera de los comentarios, events.KindSummary y
// events.KindMessageOutOfTurn aparecen en este fichero EXACTAMENTE en estas dos
// líneas. Si aparece una tercera, alguien ha abierto el segundo camino que T1.4
// prohíbe —y dos caminos gemelos que divergen en un dato son la forma clásica de que
// uno de los dos se quede atrás—.
//
// EL RÓTULO NO ES DECORATIVO: el automensaje de rescate LISTA PRODUCTOS. Un LLM que
// lea esa lista sin saber quién la escribió extrae como pedido del cliente lo que
// imprimió nuestro propio automensaje. El rótulo compra la continuidad —el «sí, esas
// dos» del cliente sigue teniendo antecedente— sin comprar el bucle.
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

// Composed es el resultado de componer un hilo. Se devuelve entero —y no solo el
// texto— porque los efectos que T1.4 tiene que garantizar son NÚMEROS y hay que
// poder afirmarlos: cuántos mensajes cuenta el hilo (que NO son los del contexto) y
// cuántas entradas de contexto entraron.
type Composed struct {
	// Text es lo que se cifra en el sobre: el bloque de contexto (si hay) seguido
	// del hilo literal, cada uno entre sus delimitadores.
	Text string
	// Context es SOLO el bloque de contexto, ya rotulado, sin sus delimitadores.
	// Vacío si el hilo no traía ninguna entrada de contexto.
	Context string
	// Literal es SOLO el hilo literal, sin delimitadores. Es la región de la que
	// pueden salir `evidence` (REQ-13) y por eso se publica aparte: un test puede
	// afirmar que el texto del resumen NO está aquí dentro.
	Literal string
	// Messages es EL CONTADOR DE VOLUMEN DEL HILO (O5): cuántas entradas `message`
	// entraron. 🔴 El contexto NO suma aquí (REQ-10b (c)) — un resumen no es
	// actividad del cliente. Es el número del que tiene que salir la métrica de
	// volumen: UNO, no dos que se puedan desincronizar.
	Messages int
	// ContextEntries es cuántas entradas de contexto entraron, sumadas LAS DOS
	// CLASES. Existe para operar y para los tests; no alimenta ninguna métrica de
	// volumen del hilo.
	ContextEntries int
}

// Empty dice si no hay nada que valga la pena cifrar: true si y solo si Messages es
// 0. Se mide por MENSAJES, no por longitud del texto: un Composed con contexto y sin
// mensajes tiene Text no vacío y aun así está Empty (ver ComposeAtFlush).
func (c Composed) Empty() bool {
	return c.Messages == 0
}

// ComposeSourceText reparte las entradas del hilo en los dos bloques y arma el
// texto. Es PURA: no lee, no escribe, no cifra y no registra nada.
//
// # El reparto: tres destinos, mirando Kind ANTES que Text
//
//	events.KindMessage           → HILO LITERAL  (lo que se escribió en turno)
//	events.KindSummary           → CONTEXTO      (ADR-0029 E-4, REQ-10b, D-044.3b)
//	events.KindMessageOutOfTurn  → CONTEXTO      (D-044.24)
//	events.KindDecision y cualquier Kind desconocido → NADA (fail-closed)
//
// Las dos clases de contexto son UN solo mecanismo, no dos caminos gemelos: la misma
// entrada, cambiando solo su Kind entre las dos, da la misma salida salvo el rótulo.
// Lo desconocido NO entra como literal del cliente: entrar por defecto al hilo es
// exactamente cómo el automensaje de rescate se convertiría en un pedido. Una entrada
// con Text "" se salta entera, sea del Kind que sea, y no cuenta en ningún contador.
//
// # Las líneas
//
//   - Cada entrada de contexto es una línea "[<rótulo>] <Text>", con el rótulo
//     "resumen del sistema" para KindSummary y "mensaje del negocio fuera de turno"
//     para KindMessageOutOfTurn. El Role de una entrada de contexto no se mira.
//   - Cada mensaje es una línea "<quién>: <Text>", donde <quién> es "cliente" SOLO
//     para events.RoleClient y "negocio" para TODO lo demás (RoleBusiness, RoleSystem,
//     un rol vacío o uno desconocido). La asimetría es deliberada: si hay duda sobre
//     quién habló, la respuesta segura es la que NO convierte texto ajeno en pedido
//     del cliente.
//   - Las líneas de cada bloque van en el ORDEN de las entradas y unidas por "\n".
//
// # El sobre (byte a byte: es el contrato con el prompt de P2)
//
// Las cuatro cabeceras son literales exactos:
//
//	### CONTEXTO PREVIO — NO es lo que el cliente está pidiendo ###
//	### FIN DEL CONTEXTO PREVIO ###
//	### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###
//	### FIN DE LOS MENSAJES ###
//
// Text se arma así:
//
//   - si hay contexto: cabecera de contexto + "\n" + Context + "\n" + pie de contexto +
//     "\n" (el "\n" tras el pie se escribe SIEMPRE que hay contexto, también cuando no
//     hay mensajes detrás);
//   - si hay mensajes: cabecera de mensajes + "\n" + Literal + "\n" + pie de mensajes,
//     SIN salto de línea final;
//   - sin contexto no hay bloque de contexto (ni sus cabeceras); sin mensajes no hay
//     bloque de mensajes; con el hilo vacío (nil o sin entradas útiles) devuelve el
//     Composed cero, con Text "".
//
// Context y Literal son los bloques SIN delimitadores; Messages y ContextEntries
// cuentan las líneas de cada uno.
//
// # Lo que NO hace, y es decisión
//
//   - 🔴 NO deduplica por texto. Un resumen REPITE a propósito lo que el cliente ya
//     dijo; descartar líneas repetidas tiraría el ORIGINAL y destruiría la evidencia
//     que REQ-10b protege. Dos mensajes idénticos son dos líneas y cuentan dos.
//   - 🔴 NO escapa, ni recorta, ni normaliza el Text: va TAL CUAL. Un mensaje que
//     contiene saltos de línea ocupa varias líneas; uno que contiene las propias
//     cabeceras del sobre las deja escritas dentro del bloque de mensajes, precedidas
//     de su "cliente: " (así se comporta el viejo: el sobre NO es a prueba de un
//     cliente que las teclee, y este contrato no lo arregla); un Text de solo espacios —ASCII o
//     Unicode, como U+00A0 o U+2003— NO es "" y entra como una línea más; los dígitos
//     no ASCII no se traducen.
func ComposeSourceText(entries []events.ThreadEntry) Composed {
	var (
		out          Composed
		contextLines []string
		literalLines []string
	)
	for _, e := range entries {
		// 🔴 SE MIRA `Kind` ANTES DE TOCAR `Text`, SIEMPRE: `e.Text` no se usa fuera de
		// los dos brazos del switch.
		label, isContext := contextLabel(e.Kind)
		switch {
		case isContext:
			// EL ÚNICO SITIO QUE PRODUCE CONTEXTO. Las dos clases pasan por aquí.
			if e.Text == "" {
				continue
			}
			contextLines = append(contextLines, "["+label+"] "+e.Text)
			out.ContextEntries++
		case e.Kind == events.KindMessage:
			if e.Text == "" {
				continue
			}
			literalLines = append(literalLines, speakerOf(e.Role)+": "+e.Text)
			out.Messages++
		default:
			// FAIL-CLOSED, y cubre dos casos: `decision` —que es estructura, no prosa—
			// y cualquier `entry_kind` que se invente después de escribir esto. Lo
			// desconocido NO entra como literal del cliente: entrar por defecto al hilo
			// es exactamente cómo el rescate se convertiría en un pedido. Si algún día
			// un grado nuevo debe aportar contexto, se añade su fila a `contextKinds` y
			// no una rama aquí.
			continue
		}
	}
	out.Context = strings.Join(contextLines, "\n")
	out.Literal = strings.Join(literalLines, "\n")

	// ⚠️ Rareza portada tal cual: el Text no se escapa. Un cliente que teclea las
	// cabeceras las deja escritas dentro del bloque de mensajes.
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

// ThreadReader es la lectura del hilo del evento, DESCIFRADA en el borde. Interfaz
// local y estrecha (ISP, mismo patrón que AggregationSettings): la satisface
// *events.Store, que es quien tiene el FieldCipher del hilo. Devuelve las `limit`
// entradas más recientes, en orden cronológico.
type ThreadReader interface {
	ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error)
}

// SourceTextWriter es lo ÚNICO que el compositor necesita de `intake_jobs`: dejar
// el sobre. No puede listar, no puede cerrar y no puede abrir ventanas. Lo satisface
// intake.JobStore.
type SourceTextWriter interface {
	PutSourceText(ctx context.Context, k intake.WindowKey, env intake.SourceText) (bool, error)
}

// DefaultThreadLimit acota cuántas entradas del hilo entran al `source_text`: 200. Es
// un techo de TAMAÑO DE PROMPT, no una regla de negocio: el pipeline paga por token y
// un hilo de mil entradas no cabe en ninguna ventana de contexto útil. El recorte
// muerde por el PRINCIPIO del hilo: lo que se pierde es lo más viejo.
//
// Está exportado (T4.6, Plan 044 · Ola 4) porque hay un SEGUNDO lector del mismo hilo
// con la misma pregunta: `/reanalyze` comprueba que HAY material antes de abrir el
// job, y esa comprobación tiene que mirar exactamente las entradas que este
// compositor va a componer. Es la misma constante o son dos verdades.
const DefaultThreadLimit = 200

// SourceTextComposer implementa SourceComposer (aggregator.go): lee el hilo, compone,
// cifra y guarda.
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

// WithThreadLimit fija cuántas entradas del hilo se piden al ThreadReader. Un valor
// <= 0 se ignora (se queda el límite que hubiera: DefaultThreadLimit si es la única
// opción).
func WithThreadLimit(n int) SourceTextComposerOption {
	return func(c *SourceTextComposer) {
		if n > 0 {
			c.limit = n
		}
	}
}

// NewSourceTextComposer construye el compositor. Nunca devuelve nil. El límite de
// entradas nace en DefaultThreadLimit y las opciones se aplican en orden.
//
// cipher es el MISMO stack de claves que cifra el hilo, los contactos y los datos del
// comprador (keyring versionado del Plan 012). Un segundo cipher sería una segunda
// rotación que gestionar.
//
// Con CUALQUIER dependencia a nil (log, thread, jobs o cipher) el compositor es un
// no-op seguro (ver ComposeAtFlush): el job se queda en `pending` con el sobre a
// NULL, que es una forma legítima en la 0072. No lee ni escribe nada al construir.
func NewSourceTextComposer(log logger.Logger, thread ThreadReader, jobs SourceTextWriter,
	cipher *crypto.FieldCipher, opts ...SourceTextComposerOption) *SourceTextComposer {
	c := &SourceTextComposer{log: log, thread: thread, jobs: jobs, cipher: cipher, limit: DefaultThreadLimit}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ComposeAtFlush implementa SourceComposer. El agregador la llama UNA vez por
// ventana, DESPUÉS de que la transición `aggregating → pending` haya tenido éxito y
// FUERA del camino del entrante. Devolver error NO reabre la ventana ni corta nada:
// el llamante lo LOGUEA y el job se queda en `pending` con el sobre vacío.
//
// Los pasos, en orden, todos con el ctx recibido:
//
//  1. No-op: sobre un receptor nil, o un compositor construido con log, thread, jobs o
//     cipher a nil, devuelve nil sin leer, escribir ni loguear.
//  2. Clave incompleta (intake.WindowKey.Valid() == false): devuelve el error de texto
//     "compositor: clave de ventana incompleta", sin leer el hilo.
//  3. Lee el hilo UNA vez: ListThread(ctx, key.EventID, límite). Si falla, devuelve
//     "compositor: leer el hilo del evento <event_id>: <error>" (envuelve el error) y
//     no escribe nada.
//  4. Compone con ComposeSourceText. 🔴 Con CERO MENSAJES no se escribe NADA, ni
//     siquiera si hubo contexto: un `source_text` hecho SOLO de contexto es un prompt
//     donde lo único que hay son productos que listamos NOSOTROS y ninguna frase del
//     cliente que los contradiga (el accidente que D-044.24 describe). Devuelve nil y
//     deja UN aviso en Warn —no es una avería: el tenant no tiene el hilo escrito, o
//     la ventana se abrió con media sin texto—, de mensaje literal
//     "compositor: la ventana cerró sin una sola línea del hilo; el sobre se queda vacío"
//     y claves "tenant_id", "session_id", "event_id" y "entradas_de_contexto".
//  5. Cifra Composed.Text ENTERO (contexto y mensajes) con el cipher. Si falla,
//     devuelve "compositor: cifrar el literal de la ventana del evento <event_id>:
//     <error>" (envuelve el error) y no escribe nada.
//  6. Guarda el sobre de TRES piezas —intake.SourceText{Enc, DEK, KEKID}, las que
//     devolvió el cipher— con PutSourceText(ctx, key, sobre), UNA vez. Descifrarlo con
//     el mismo keyring devuelve exactamente Composed.Text. Si falla, devuelve
//     "compositor: guardar el literal de la ventana del evento <event_id>: <error>"
//     (envuelve el error).
//  7. Si PutSourceText contesta false sin error (la fila ya tenía sobre, o la ventana
//     no está en `pending`), NO es un error —es idempotencia— y devuelve nil, dejando
//     en Debug "compositor: la ventana ya tenía literal; no se sobrescribe" con las
//     claves "tenant_id" y "event_id".
//  8. Si escribió, devuelve nil y deja en Debug "compositor: literal compuesto y
//     cifrado" con las claves "tenant_id", "session_id", "event_id", "mensajes"
//     (Composed.Messages), "contexto" (Composed.ContextEntries) y "bytes" (el largo en
//     bytes de Composed.Text).
//
// 🔴 NINGUNA línea de log, en ningún nivel, y NINGÚN error llevan contenido del hilo
// (REQ-10c): solo identificadores y números.
//
// ⚠️ DEUDA CONOCIDA, D-F7-9 (no se arregla ni se promete aquí; el verde de F8-05 lo
// portó tal cual, y el arreglo sigue pendiente, en su propio commit): este método corre DESPUÉS del cierre, en una segunda sentencia y sin
// atomicidad con él, y PutSourceText exige que la fila esté ya en `pending`. Entre las
// dos, el job es visible para el worker SIN sobre. Ver IntakeAggregator.Sweep.
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
		// es una decisión y no un atajo (ver el paso 4 del contrato). El sobre se queda
		// NULL —forma legítima en la 0072— y el worker verá un job sin literal. Es Warn
		// y no Error: no hay nada roto.
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

	written, err := c.jobs.PutSourceText(ctx, key, intake.SourceText{Enc: enc, DEK: dek, KEKID: kekID})
	if err != nil {
		return fmt.Errorf("compositor: guardar el literal de la ventana del evento %s: %w", key.EventID, err)
	}
	if !written {
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

// El compositor satisface el hueco que declara el agregador, comprobado en
// compilación. Es lo que impide que un cambio de firma de SourceComposer deje esto
// colgando.
var _ SourceComposer = (*SourceTextComposer)(nil)
