// aggregator.go — EL AGREGADOR DE VENTANAS del pipeline de captación por LLM
// (Plan 044 · Ola 1 · T1.1, T1.2 y T1.7; design §6.2, D-044.26, D-044.20).
//
// # QUÉ HACE, EN UNA FRASE
//
// Un cliente no pide un presupuesto en un mensaje: lo pide en cinco seguidos. El
// agregador junta esos cinco en UNA ventana —una fila `intake_jobs` en estado
// `aggregating`— y la cierra (`aggregating` → `pending`) cuando le toca, para que
// el pipeline caro corra UNA vez y no cinco.
//
// # 🔴 LA VENTANA DE SILENCIO ES EL CAMINO PRINCIPAL, NO EL RESPALDO (T1.7)
//
// Esto hay que decirlo con estas palabras porque el plan original decía lo
// contrario y la premisa CADUCÓ con el Plan 051:
//
//	LA VENTANA DE SILENCIO ES EL CAMINO PRINCIPAL Y EL ÚNICO GARANTIZADO. EL
//	INTENT, CUANDO LLEGA, SOLO ADELANTA UN FLUSH QUE IBA A OCURRIR IGUAL — NUNCA
//	ES LA CONDICIÓN DE QUE OCURRA.
//
// # 🔧 Y DESDE LA OLA 1.6 EL INTENT NO LLEGA: SE PIDE (D-044.31, T1.6-4)
//
// Este fichero decía que la señal viajaba ADJUNTA al mensaje —el `ClassifiedIntent`
// que el Edge sellaba en el proto— y que podía no llegar nunca por el presupuesto de
// espera del despachador. Eso se acabó: T1.6-1 retiró `ClassifiedIntent` del
// contrato (campo 11 de `IncomingMessage` y campo 5 de `SensitivePayload`, los dos
// `reserved`). **El push MURIÓ**, y murió por el lado del CONTRATO: no hay campo
// donde poner una etiqueta, así que ninguna puede llegar.
//
// ⚠️ Lo que el Edge hace mientras tanto es asunto de T1.6-5 y aún NO está hecho:
// `WAPP_AGENT_INTENT_WAIT_MS` sigue vivo allí (`edge/wapp-edge-agent`), o sea que el
// despachador sigue pagando su presupuesto de espera por una clasificación que ya no
// tiene dónde viajar. Eso NO afecta a este fichero —lo que llegue, llega sin
// etiqueta—, pero sí a la latencia de entrega, y es exactamente el sumando que
// T1.6-5 existe para quitar.
//
// Hoy la señal la PIDE el Cloud, fuera del camino del mensaje: `Observe` encola una
// petición (`AheadRequester`), un pool la resuelve en segundos y la respuesta vuelve
// por `OnClassified`. Todo lo que la premisa de T1.7 decía SIGUE SIENDO CIERTO, y
// más aún: la señal puede no llegar nunca —vía caída, cola llena, catálogo sin
// publicar, respuesta que llega tarde—, así que la ventana de silencio es el camino
// principal y el único garantizado, ahora por más motivos que antes.
//
// Consecuencias que están escritas en el código de este fichero y no solo aquí:
//
//   - el flush por ventana se prueba como camino PRINCIPAL (ver `Sweep`), no como
//     respaldo;
//   - el intent solo mueve la fecha (ver `hintDueNow`): si el proceso se reinicia y
//     la pista se pierde, la ventana se cierra igual por su reloj;
//   - una respuesta que llega DESPUÉS del cierre es inocua: su pista no casa con
//     ninguna ventana viva y el barrido la tira sin abrir ni cerrar nada;
//   - el job NO LLEVA MARCA de por qué se disparó. Los dos caminos producen jobs
//     INDISTINGUIBLES, y eso es deliberado (coherente con D-044.20).
//
// # 🔴 LOS PLAZOS DE LA VENTANA SON LA LATENCIA DE PEOR CASO DEL PIPELINE
//
// Y con el intent ausente —que es un caso normal, no una avería— son la latencia de
// TODOS los casos. Esos números pesan directamente sobre la métrica reina del plan:
// primer borrador en menos de 5 minutos (T6.1).
//
// 🔧 CORREGIDO EN T1.8-1: aquí ponía que el peor caso era `aggregation_window_seconds`
// (45 s) y desde la ventana HÍBRIDA eso es FALSO. El silencio se mide desde el ÚLTIMO
// mensaje, así que una ráfaga larga lo reinicia una y otra vez; quien acota de verdad
// es el TECHO, `aggregation_max_seconds` (120 s por defecto). El peor caso a primer
// borrador es `aggregation_max_seconds` + lo que tarde el pipeline — dos de los cinco
// minutos del presupuesto. Subir CUALQUIERA de los dos es gastarlo, y hay que decirlo
// con el número delante.
//
// # EL PRESUPUESTO DE I/O EN LÍNEA CON EL MENSAJE (D-044.26, INV-02 del Plan 050)
//
// `Observe` es lo ÚNICO que corre en el camino del entrante, y su presupuesto es:
//
//   - EXACTAMENTE 1 sentencia SQL: el `INSERT … ON CONFLICT … DO UPDATE` de
//     `intake.Postgres.OpenOrAppend`, que abre la ventana si no existía y le añade
//     la referencia del mensaje si ya existía;
//   - CERO `SELECT`: ni para encontrar el job abierto (lo resuelve el `ON CONFLICT`
//     contra el índice único parcial), ni para leer `tenant_settings` (la ventana
//     se lee en el barrido, fuera de línea), ni para leer el hilo del evento;
//   - CERO cripto y CERO red. El literal del cliente NO PASA POR AQUÍ: el sobre
//     `source_text_enc/_dek/_kek_id` nace NULL y lo llena el flush (T1.4). No se
//     puede concatenar texto sobre un blob cifrado con una sentencia SQL, así que
//     la salida no es relajar el cifrado: es que el literal no viaje por aquí.
//
// El gate es la feature `llm_intake`, resuelta por el resolver de entitlements
// CACHEADO CON TTL (`internal/entitlements/postgres.go:94-113`) — el mismo que ya
// consulta `thread.go`. 🔴 NO se copia el gate del `WebhookSink`, que hace un
// `SELECT` sin caché a `tenant_integrations`: eso sería justo el I/O que D-044.26
// prohíbe. Y las guardas BARATAS van primero (clave incompleta, sin
// `wa_message_id`, sin store), antes de preguntarle nada a nadie.
//
// # BEST-EFFORT: UN FALLO DE AQUÍ NUNCA TUMBA EL TURNO DEL CLIENTE (INV-10)
//
// `Observe` NO DEVUELVE ERROR y NO marca `ErrMaterializationFailed` (condición
// heredada de T0.4). Un fallo se LOGUEA y el turno sigue, exactamente como hace el
// `WebhookSink`. Perder una ventana de captación es perder un presupuesto
// automático; cortar el turno es dejar al cliente sin respuesta.
//
// # POR QUÉ ESTO NO CUELGA DEL FAN-OUT DE `EventSink` (desviación anotada)
//
// D-044.26 dice «fase `PhaseNotify`», y el ESPÍRITU de esa frase se respeta al pie
// de la letra: esto corre DESPUÉS de toda la proyección, no enriquece el payload de
// nadie y jamás corta un turno. Lo que NO se hace es implementar `EventSink`, y hay
// dos motivos MEDIDOS sobre el código, no de estilo:
//
//  1. `EffectContext` (event_sink.go) NO LLEVA `wa_message_id` — ninguno de sus
//     campos lo lleva, y `source_refs` es precisamente una lista de
//     `wa_message_id`s. Un sink del fan-out no tiene con qué escribirla.
//  2. Un turno puede producir CERO efectos, y entonces `dispatch` no llama a ningún
//     sink. El caso que este plan quiere resolver —una ráfaga de texto libre
//     pidiendo un presupuesto— es exactamente un turno sin efectos de módulo.
//
// Por eso el agregador se alimenta del ENTRANTE (`observeForAggregation`) y no de los
// efectos.
//
// 🔧 **Y POR ESO SE RENOMBRÓ, el 2026-08-22 (decisión de Jhoan tras el barrido de la
// Ola 1): `AggregatorSink` → `IntakeAggregator`.** El nombre viejo es el que fija
// `tasks.md` · T1.1, y prometía un contrato que este tipo NO cumple ni puede cumplir:
// quien leyera «Sink» en un paquete donde `EventSink` es una interfaz de verdad
// (`event_sink.go`) buscaría el fan-out de `dispatch()` y no lo encontraría. Se
// sostuvo un tiempo la lectura «sink = sumidero del entrante», pero un nombre que
// necesita una nota al pie para no engañar es un nombre que hay que cambiar.
//
// Ese puente tiene TRES puntos de llamada —el turno normal, el mensaje que ARRANCA el
// evento y el reinicio por reanudación—, y el censo con su porqué está en el docstring
// de `observeForAggregation`, al final de este fichero. Este párrafo decía «llamado
// desde advanceLiveStep» a secas, que era cierto el día que se escribió y dejó fuera el
// caso que el plan existe para resolver.
package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// IntentIntakeRequest es el ÚNICO nombre de intención que adelanta un flush
// (D-044.20). Lo define la config del tenant (T1.3) y hoy lo devuelve la
// clasificación que el Cloud PIDE (T1.6-4); aquí solo se compara.
const IntentIntakeRequest = "intake_request"

// featureIntakeAggregation es el gate del agregador: la MISMA feature `llm_intake`
// que abre el productor de filas `message` del hilo (thread.go). No es casualidad
// ni ahorro: el agregador es el LECTOR de ese hilo, así que encender uno sin el
// otro deja media función construida. Un tenant sin la feature produce CERO jobs.
//
// 🔴 Y ES EL ÚNICO GATE DE ESTE CAMINO: aquí NO se consulta `api_llm` (ADR-0044,
// D-044.28). La vía —local o API— es una configuración DENTRO del nivel, y
// decidirla no es asunto de la ventana: un tenant de vía local abre ventana,
// compone su `source_text` cifrado y deja su job en `pending` exactamente igual.
// Quien venga a añadir aquí una segunda pregunta al resolver, que lea antes el
// docstring de entitlements.FeatureAPILLM: lo que busca no se decide en este
// fichero. Lo vigila via_local_sin_api_llm_test.go con un resolver espía.
const featureIntakeAggregation = entitlements.FeatureLLMIntake

const (
	// defaultIntentConfidence es el umbral por defecto de `confidence` para que un
	// `intake_request` adelante el flush.
	//
	// 🔴 ES UN DEFAULT DE PLATAFORMA Y NO UNA CONFIG POR TENANT, y el motivo es
	// D-044.26: leer un umbral por tenant EN LÍNEA con el mensaje sería el `SELECT`
	// que esta ola prohíbe. Se inyecta al construir (WithIntentConfidence) para que
	// el despliegue pueda moverlo; el refinamiento por tenant, si algún día hace
	// falta, tiene que llegar por una vía que NO lea en línea (config cacheada al
	// estilo del resolver de entitlements), no colgando un SELECT de `Observe`.
	//
	// ⚠️ Errar por lo BAJO aquí no rompe nada: un intent que adelanta de más cierra
	// una ventana antes de tiempo y el cliente puede abrir otra sobre el mismo
	// evento (el índice de la 0072 es PARCIAL a propósito). Errar por lo ALTO
	// tampoco: la ventana se cierra igual por silencio, que es el camino garantizado.
	defaultIntentConfidence = 0.7
	// defaultSweepInterval es cada cuánto barre el cierre de ventanas. Sin broker
	// (ADR-0003): es un ticker de Go, ni cron ni cola externa.
	//
	// Fija el GRANO del cierre, no su plazo: una ventana de 45 s se cierra entre los
	// 45 y los 45+5 s. Y fija también qué significa «flush inmediato» para el intent
	// —como mucho un tick—, que es la contrapartida aceptada de que el adelanto NO
	// se ejecute en línea con el mensaje (D-044.26: en línea solo cabe UNA sentencia,
	// y esa ya está gastada en abrir/ampliar la ventana).
	defaultSweepInterval = 5 * time.Second
	// defaultSweepBatch acota cuántas ventanas mira un barrido. Es un techo de
	// trabajo por pasada, no un límite de negocio: lo que no entra sale en el
	// siguiente tick, y las más viejas van primero.
	defaultSweepBatch = 200
)

// IntentHint es lo ÚNICO que la política de disparo mira de una clasificación: su
// nombre y su confianza.
//
// 🔴 NO LLEVA `params` A PROPÓSITO (D-044.20). La política se dispara con la SEÑAL,
// no con los ítems: no lee params, no espera una lista de productos y no cambia de
// comportamiento según lo que el intent traiga dentro. Un `intake_request` con
// `params` vacío —que es la forma FIJADA del intent, T1.3— dispara EXACTAMENTE
// igual, y el job resultante es indistinguible. Quien descompone en ítems es el
// pipeline P2–P4 de la Ola 2, aguas abajo y sobre el texto acumulado.
//
// Si algún día alguien añade un campo aquí para «mejorar el disparo», está
// deshaciendo D-044.20.
type IntentHint struct {
	Name       string
	Confidence float64
}

// IncomingRef es lo mínimo que el agregador necesita de UN entrante.
//
// 🔴 LO QUE ENTRA A `intake_jobs` SIGUEN SIENDO REFERENCIAS OPACAS Y NUNCA
// CONTENIDO (D-044.26). Un `wa_message_id` no es PII; el texto sí, y por eso el
// literal NO viaja a ninguna sentencia SQL desde aquí: el sobre
// `source_text_enc/_dek/_kek_id` sigue naciendo NULL y llenándose AL FLUSH (T1.4).
//
// 🔧 DESDE T1.6-4 ESTE TIPO SÍ LLEVA `Text`, y la contradicción es aparente: el
// campo no es para persistir, es para PREGUNTAR. Con el push muerto (D-044.31) la
// única forma de tener una señal es pedir la clasificación, y no se puede clasificar
// un texto que no se tiene. Lo que se hace con él está acotado y dicho en el
// docstring del campo.
type IncomingRef struct {
	Key intake.WindowKey
	// WaMessageID es el identificador opaco del entrante. Es la primera referencia
	// que entra a `source_refs` y, además, la clave con la que el agregador
	// descarta un mismo mensaje observado dos veces.
	WaMessageID string
	// MediaRefs son las referencias de audio/foto del mensaje, SIN DESCARGAR NADA
	// (T1.1). Hoy llegan vacías: el entrante de CloudLink no trae todavía un
	// identificador de media separado del `wa_message_id`. Se deja el campo porque
	// el criterio de T1.1 habla de «3 mensajes + 1 foto ⇒ 4 refs» y la foto entra
	// hoy por su propio `wa_message_id`.
	MediaRefs []string
	// MessageTS es el instante del mensaje del CLIENTE (`ts_unix` del entrante), no
	// el reloj del servidor. Es la BASE DE FECHAS del presupuesto (D-044.9): «para
	// el jueves» se resuelve contra este instante. Solo cuenta si este entrante ABRE
	// la ventana.
	MessageTS time.Time
	// Text es el literal del mensaje del cliente, y existe para UNA cosa: alimentar
	// la petición de clasificación (T1.6-4).
	//
	// 🔴 NO SE PERSISTE Y NO SE LOGUEA. No entra en `intake.Append`, no toca ninguna
	// sentencia SQL y no aparece en un solo campo de log de este fichero. Vive en la
	// cola del pool mientras dura la inferencia y muere con ella. Si algún día alguien
	// lo mete en el Append, ha roto D-044.26 (y el presupuesto de I/O de `Observe`,
	// que sigue siendo UNA sentencia y CERO lecturas).
	//
	// Vacío es normal: un mensaje de solo media no tiene texto que clasificar, y
	// entonces no se pide nada.
	Text string
}

// AggregationSettings es lo mínimo que el barrido necesita de la config del
// tenant. Interfaz local (ISP, mismo patrón que WebhookQueuer): lo satisface
// *store.PostgresRepository y *store.MemoryRepository.
//
// ⚠️ Se consulta SOLO desde el barrido. Llamarla desde `Observe` sería el `SELECT`
// en línea con el mensaje que D-044.26 prohíbe.
type AggregationSettings interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
}

// SourceComposer ES EL PUNTO DE EXTENSIÓN DE T1.4, y el nombre es el contrato.
//
// Al cerrar una ventana hay que componer el literal (`source_text`) leyendo el hilo
// del evento (`conversation_event_messages`, la fuente canónica según D-043.13),
// DESCIFRARLO en el borde con el KeyProvider (REQ-10c), rotular las DOS clases de
// contexto —`entry_kind='summary'` y los salientes FUERA DE TURNO (D-044.3b +
// D-044.24)— y guardar el sobre de tres piezas
// (`source_text_enc` / `source_text_dek` / `source_text_kek_id`).
//
// ✅ YA ESTÁ IMPLEMENTADO (2026-08-22, T1.4): el compositor real es
// *SourceTextComposer (source_composer.go), y lo cablea bootstrap.go con
// WithSourceComposer. Lo que sigue viviendo AQUÍ es solo el hueco y el MOMENTO en
// que se llama: DESPUÉS de que la transición `aggregating → pending` haya tenido
// éxito, y FUERA del camino del entrante.
//
// El `noopSourceComposer` de abajo NO se retira: es el default de construcción, y
// es el que corre en los tests del agregador que no montan el compositor. Un
// agregador sin compositor cierra ventanas con el sobre a NULL — la forma que la
// migración 0072 permite a propósito (las tres columnas son NULLables).
type SourceComposer interface {
	// ComposeAtFlush compone y persiste el `source_text` de la ventana recién
	// cerrada. Devolver error NO reabre la ventana ni corta nada: el job queda en
	// `pending` con el sobre vacío y el worker de la Ola 2 decidirá qué hacer.
	ComposeAtFlush(ctx context.Context, key intake.WindowKey) error
}

// noopSourceComposer es la implementación VACÍA y DOCUMENTADA: el DEFAULT de
// construcción del agregador. No es un olvido ni un stub sin dueño — desde T1.4 el
// compositor real existe (source_composer.go) y producción lo inyecta con
// WithSourceComposer. Esto es lo que corre cuando nadie lo hace: el sobre se queda
// a NULL, que es una forma legítima en la 0072.
//
// ⚠️ SI ESTO CORRE EN PRODUCCIÓN, EL PIPELINE SE QUEDA SIN TEXTO Y NO HAY ERROR.
// El fallo sería MUDO: ventanas que cierran, jobs `pending` y un worker de la Ola 2
// recibiendo el sobre vacío. El cable de bootstrap.go es la única cosa que lo
// impide, y por eso se dice aquí y allí.
type noopSourceComposer struct{}

func (noopSourceComposer) ComposeAtFlush(context.Context, intake.WindowKey) error { return nil }

// AheadRequester PIDE la clasificación que antes llegaba adjunta al mensaje (T1.6-4,
// D-044.31). Lo satisface *intakeahead.Pool.
//
// La firma es corta y las tres cosas que NO tiene son el contrato:
//
//   - **no devuelve error**, porque corre en línea con el mensaje y no hay nada que
//     el agregador pueda hacer con un fallo salvo tragárselo (INV-10);
//   - **no devuelve la clasificación**, porque tarda segundos y esperarla aquí sería
//     justo lo que REQ-35 prohíbe — la respuesta vuelve por `OnClassified`;
//   - **no acepta `ctx`**, y eso es lo más importante de las tres: el contexto del
//     turno se cancela en milisegundos y la inferencia dura segundos, así que
//     pasárselo mataría toda petición sin dar un solo error. La firma impide el error
//     en vez de advertirlo.
//
// El puerto vive AQUÍ y no en el paquete que lo implementa por la regla de siempre
// (ISP, mismo patrón que AggregationSettings): el agregador declara lo que necesita y
// no se ata a quién se lo da.
type AheadRequester interface {
	Request(key intake.WindowKey, text string)
}

// IntakeAggregator acumula entrantes en ventanas y las cierra. Es seguro para uso
// concurrente: `Observe` corre desde la goroutine de cada entrante.
type IntakeAggregator struct {
	log      logger.Logger
	jobs     intake.JobStore
	settings AggregationSettings
	ents     entitlements.Resolver
	compose  SourceComposer
	// ahead es quien PIDE la clasificación (T1.6-4). nil ⇒ no se pide nada y la
	// ventana cierra siempre por su reloj, que es el camino garantizado de T1.7.
	ahead AheadRequester

	// now es el reloj INYECTABLE, mismo patrón que rt.now/WithClock (Plan 029 · T9)
	// y por el mismo motivo: sin él, un test de la ventana tendría que dormir de
	// verdad 45 segundos y el caso «silencio ⇒ flush a los N s» no se podría cubrir.
	now func() time.Time

	sweepEvery      time.Duration
	sweepBatch      int
	intentThreshold float64

	mu sync.Mutex
	// dueNow son las ventanas que un intent ha ADELANTADO. Es una PISTA en memoria,
	// no un estado: si el proceso muere con pistas dentro, no se pierde ningún job
	// —la ventana sigue en `aggregating` en la base y el barrido la cierra por su
	// reloj—. Que la pista sea perecedera es coherente con T1.7 y no un descuido:
	// el intent adelanta, no decide.
	dueNow map[intake.WindowKey]struct{}
	// despertar es EL DESPERTADOR DEL BARRIDO (Plan 044 · T1.8-1 criterio (h),
	// D-044.43): un canal con BUFFER 1 que `hintDueNow` toca y que `Run` escucha junto
	// al ticker. Vive fuera del candado a propósito — se escribe con un envío NO
	// BLOQUEANTE, así que no necesita `s.mu` y no puede bloquear a quien avisa.
	//
	// 🔴 POR QUÉ EXISTE: el intent es un EVENTO que YA LLEGÓ, y hasta esta tarea su
	// efecto esperaba al siguiente tick del barrido — hasta 5 s de espera ciega después
	// de saber lo que había que saber. Un reloj que SUSTITUYE a un evento es el
	// antipatrón que esta casa rechaza; el tick de 5 s se queda porque hace la otra
	// mitad del trabajo, que sí es de reloj: vigilar los DOS plazos (45/120 s), que no
	// tienen evento que los anuncie.
	//
	// 🔴 POR QUÉ NO SE PIERDE NINGÚN DESPERTAR, que es la pregunta que este patrón
	// siempre invita a hacer. El escritor descarta el aviso si el buffer está lleno; y
	// que esté lleno significa que hay un aviso PENDIENTE que nadie ha recibido todavía.
	// Ese aviso se recibirá en un `select` POSTERIOR al descarte, y al recibirlo `Sweep`
	// hace una pasada COMPLETA: `takeHints` se lleva TODAS las pistas acumuladas y
	// `ListAggregating` vuelve a mirar la tabla entera. La pista nunca se queda
	// esperando a un aviso que ya se consumió. Y si aun así se perdiera, la ventana
	// cierra igual por su reloj: el aviso es un ADELANTO, no la verdad durable.
	//
	// nil es una degradación silenciosa (un canal nil nunca está listo en un `select`)
	// y por eso lo construye SIEMPRE el constructor, nunca una opción.
	despertar chan struct{}
	// seen recuerda el último `wa_message_id` observado por ventana, para que un
	// mismo entrante observado dos veces no duplique su referencia. Es una red
	// SECUNDARIA: la primera es `duplicateIngest` (dedupe PERSISTENTE de ingesta,
	// Plan 028 · T6), que corta el reenvío del outbox del Edge antes de llegar aquí.
	seen map[intake.WindowKey]string
}

// AggregatorOption configura el agregador al construirlo.
type AggregatorOption func(*IntakeAggregator)

// WithAggregatorClock inyecta el reloj (tests deterministas). nil se ignora.
func WithAggregatorClock(now func() time.Time) AggregatorOption {
	return func(s *IntakeAggregator) {
		if now != nil {
			s.now = now
		}
	}
}

// WithSweepInterval fija cada cuánto barre el cierre de ventanas. <=0 se ignora.
//
// Consumidor: TestRun_ElTickerCierraLaVentanaSinQueNadieLlameASweep, que es además el
// ÚNICO test que ejerce Run (el camino que corre en producción, donde nadie llama a
// Sweep a mano). Si algún día se queda sin llamante, vuelve a ser superficie exportada
// sin consumidor — que es lo que D-044.23 condena.
func WithSweepInterval(d time.Duration) AggregatorOption {
	return func(s *IntakeAggregator) {
		if d > 0 {
			s.sweepEvery = d
		}
	}
}

// WithSweepBatch fija cuántas ventanas mira un barrido. <=0 se ignora.
//
// Consumidor: TestTechoDeTrabajoPorPasada_ElBarridoNoMiraMasDeLoQueSeLeDijo, que fija
// que el batch es un TECHO POR PASADA y no un límite de negocio: lo que no entra sale
// en el tick siguiente.
func WithSweepBatch(n int) AggregatorOption {
	return func(s *IntakeAggregator) {
		if n > 0 {
			s.sweepBatch = n
		}
	}
}

// WithIntentConfidence fija el umbral de `confidence` a partir del cual un
// `intake_request` adelanta el flush. <=0 se ignora (ver defaultIntentConfidence).
//
// Consumidor: TestUmbralDeConfianzaInyectado_MandaSobreElDefaultDePlataforma, que fija
// lo único que esta opción promete —el umbral inyectado SUSTITUYE al default de
// plataforma, no se suma a él— con una confianza elegida entre los dos números.
func WithIntentConfidence(min float64) AggregatorOption {
	return func(s *IntakeAggregator) {
		if min > 0 {
			s.intentThreshold = min
		}
	}
}

// WithSourceComposer inyecta el compositor del literal (T1.4). Sin él corre el
// noop documentado.
func WithSourceComposer(c SourceComposer) AggregatorOption {
	return func(s *IntakeAggregator) {
		if c != nil {
			s.compose = c
		}
	}
}

// WithAheadRequester inyecta quien PIDE la clasificación (T1.6-4). Sin él el
// agregador no adelanta NUNCA y toda ventana cierra por su reloj — que es una forma
// legítima y el camino garantizado de T1.7, no una avería.
func WithAheadRequester(a AheadRequester) AggregatorOption {
	return func(s *IntakeAggregator) {
		if a != nil {
			s.ahead = a
		}
	}
}

// NewIntakeAggregator construye el agregador. `jobs`, `settings` y `ents` pueden ser
// nil en tests que solo ejerciten el camino «no acumula»: con cualquiera de los
// tres a nil, `Observe` es un no-op seguro (mismo criterio nil-safe que el resto
// de piezas opcionales del runtime).
func NewIntakeAggregator(log logger.Logger, jobs intake.JobStore, settings AggregationSettings,
	ents entitlements.Resolver, opts ...AggregatorOption) *IntakeAggregator {
	s := &IntakeAggregator{
		log:             log,
		jobs:            jobs,
		settings:        settings,
		ents:            ents,
		compose:         noopSourceComposer{},
		now:             time.Now,
		sweepEvery:      defaultSweepInterval,
		sweepBatch:      defaultSweepBatch,
		intentThreshold: defaultIntentConfidence,
		dueNow:          make(map[intake.WindowKey]struct{}),
		seen:            make(map[intake.WindowKey]string),
		// BUFFER 1, ni 0 ni N. Con 0 el envío no bloqueante fallaría siempre que el
		// barrido no estuviera parado justo en el `select` —o sea, casi siempre— y el
		// despertador no despertaría nada. Con N se apilarían N avisos para hacer N
		// barridos completos que verían lo mismo: un aviso pendiente ya significa «hay
		// trabajo sin mirar», y más de uno no significa más.
		despertar: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Observe mete UN entrante en su ventana. Es lo ÚNICO de este fichero que corre en
// línea con el mensaje del cliente, y su presupuesto está en la cabecera: 1
// sentencia, 0 lecturas, 0 cripto, 0 red.
//
// NO devuelve error a propósito (INV-10): cualquier fallo se loguea y el turno
// sigue.
func (s *IntakeAggregator) Observe(ctx context.Context, ref IncomingRef) {
	if !s.acceptable(ref) {
		return
	}
	// GATE, y va DESPUÉS de las guardas baratas (patrón thread.go): sin la feature
	// no se escribe nada, y un fallo del resolver se trata como «no» (fail-closed,
	// mismo criterio que entitlements.Resolver y que buildSignal). La caché con TTL
	// hace que esto no sea una consulta por mensaje.
	has, err := s.ents.Has(ctx, ref.Key.TenantID, featureIntakeAggregation)
	if err != nil {
		s.log.Warn("agregador: no se pudo resolver la feature llm_intake; el entrante no entra en ninguna ventana",
			"error", err, "tenant_id", ref.Key.TenantID, "session_id", ref.Key.SessionID)
		return
	}
	if !has {
		return
	}
	if s.alreadySeen(ref) {
		return
	}
	refs := append([]string{ref.WaMessageID}, ref.MediaRefs...)
	// LA SENTENCIA. Una, y ninguna lectura.
	if err := s.jobs.OpenOrAppend(ctx, intake.Append{
		Key:       ref.Key,
		MessageTS: ref.MessageTS,
		Refs:      refs,
	}); err != nil {
		s.log.Error("agregador: no se pudo abrir/ampliar la ventana de captación; el turno sigue",
			"error", err, "tenant_id", ref.Key.TenantID, "session_id", ref.Key.SessionID,
			"wa_message_id", ref.WaMessageID)
		return
	}
	// EL ADELANTO (T1.2, reescrito por T1.6-4). Es lo último y NO ES UNA LLAMADA AL
	// MODELO: es un encolado que vuelve en nanosegundos. La inferencia —segundos— la
	// hace un pool aparte y su respuesta entra por OnClassified. El cierre lo sigue
	// ejecutando el barrido, fuera del camino del mensaje (D-044.26 intacta).
	s.requestAhead(ref)
}

// requestAhead pide la clasificación de este entrante. Va DESPUÉS de que la ventana
// exista de verdad: preguntar por una ventana que el INSERT no llegó a abrir sería
// gastar una inferencia para anotar una pista que no casa con nada.
//
// Sin texto no se pide: un mensaje de solo media no tiene nada que clasificar, y es
// además uno de los motivos SANOS que REQ-38 saca del canal de avisos.
func (s *IntakeAggregator) requestAhead(ref IncomingRef) {
	if s.ahead == nil || ref.Text == "" {
		return
	}
	s.ahead.Request(ref.Key, ref.Text)
}

// OnClassified recibe la clasificación que se pidió y aplica la política de disparo
// (T1.6-4). Es el reemplazo EXACTO de lo que antes hacía la etiqueta adjunta.
//
// 🔴 LA POLÍTICA VIVE AQUÍ Y NO EN QUIEN CLASIFICA, y es deliberado: el pool sabe
// hablar con un modelo, no sabe qué significa `intake_request` ni qué umbral tiene
// esta plataforma. Repartir la decisión entre los dos es como acaban diciendo cosas
// distintas.
//
// Por debajo del umbral NO PASA NADA: ni pista, ni log, ni aviso al dueño. Un
// «desconocido» con confianza baja es el sistema funcionando —REQ-38 lo nombra entre
// los motivos SANOS—, y contarlo como incidencia mataría el canal de avisos.
//
// Que la ventana ya haya cerrado cuando esto llega es INOCUO y no se comprueba:
// `hintDueNow` anota en memoria y el barrido solo mira las pistas de las ventanas que
// siguen `aggregating`. Comprobarlo aquí costaría un SELECT para no hacer nada.
func (s *IntakeAggregator) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	if s == nil {
		return
	}
	if !s.intentTriggers(&IntentHint{Name: intent, Confidence: confidence}) {
		return
	}
	s.hintDueNow(key)
}

// acceptable agrupa las GUARDAS BARATAS: las que no cuestan ni una consulta y por
// eso van primero (patrón thread.go). Una clave incompleta no es «una ventana
// rara»: las cuatro columnas son NOT NULL en la 0072 y el INSERT reventaría.
func (s *IntakeAggregator) acceptable(ref IncomingRef) bool {
	switch {
	case s == nil, s.log == nil, s.jobs == nil, s.ents == nil:
		return false
	case ref.WaMessageID == "":
		// Un entrante sin identificador no puede aportar una referencia opaca, y
		// `source_refs` es justo eso. No se inventa una.
		return false
	case !ref.Key.Valid():
		// Sin evento vivo no hay ventana: `intake_jobs.event_id` es NOT NULL y la
		// fuente del literal (el hilo) cuelga del evento. Un saludo suelto —el
		// LIMBO, sin evento— no abre nada, y es correcto.
		return false
	}
	return true
}

// alreadySeen descarta el MISMO `wa_message_id` observado dos veces sobre la misma
// ventana. Red SECUNDARIA (la primera es duplicateIngest, Plan 028 · T6): sin ella,
// un doble Observe duplicaría la referencia dentro de `source_refs` porque el
// `DO UPDATE` concatena a ciegas y no puede comprobar nada (no lee).
func (s *IntakeAggregator) alreadySeen(ref IncomingRef) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[ref.Key] == ref.WaMessageID {
		return true
	}
	s.seen[ref.Key] = ref.WaMessageID
	return false
}

// intentTriggers aplica D-044.20 ENTERA: mira `Name` y `Confidence` y NADA MÁS del
// intent. No lee `params`, no espera una lista de productos y no cambia de
// comportamiento según lo que el intent traiga dentro.
func (s *IntakeAggregator) intentTriggers(hint *IntentHint) bool {
	if hint == nil {
		return false
	}
	return hint.Name == IntentIntakeRequest && hint.Confidence >= s.intentThreshold
}

// hintDueNow anota que esa ventana debe cerrarse, y DESPIERTA al barrido para que lo
// haga ya (T1.8-1 (h)). Antes solo anotaba, y el efecto esperaba al siguiente tick.
//
// 🔴 EL ORDEN DE LAS DOS MITADES NO ES INTERCAMBIABLE: primero se anota bajo candado y
// DESPUÉS se avisa. Al revés habría una carrera real —el barrido despertaría, llamaría
// a `takeHints` y encontraría el mapa todavía vacío, y la pista se quedaría esperando
// al tick igual que antes—. Con este orden, cualquiera que reciba el aviso ve ya la
// pista.
//
// El aviso va FUERA del candado y es NO BLOQUEANTE: quien llama a esto es
// `OnClassified`, o sea la goroutine del pool de clasificación al terminar una
// inferencia. No puede quedarse esperando a que el barrido esté escuchando, y tampoco
// puede quedarse esperando a `s.mu` mientras un barrido largo lo tiene tomado.
func (s *IntakeAggregator) hintDueNow(k intake.WindowKey) {
	s.mu.Lock()
	s.dueNow[k] = struct{}{}
	s.mu.Unlock()
	// El `default` es lo que hace que esto no bloquee NUNCA: si ya hay un aviso
	// pendiente, este se descarta y no pasa nada (ver el porqué en el campo `despertar`).
	select {
	case s.despertar <- struct{}{}:
	default:
	}
}

// takeHints se lleva las pistas acumuladas y deja el mapa vacío. Se llevan TODAS
// aunque el barrido no llegue a mirar la ventana de alguna (el `limit` recorta):
// esa ventana no se pierde, se cierra por su reloj en un tick posterior — que es
// justo lo que T1.7 dice que tiene que pasar cuando el adelanto no llega.
func (s *IntakeAggregator) takeHints() map[intake.WindowKey]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dueNow) == 0 {
		return nil
	}
	out := s.dueNow
	s.dueNow = make(map[intake.WindowKey]struct{})
	return out
}

// 🔴 NO HAY UN `forget(key)` AL CERRAR LA VENTANA, Y ESO ES DELIBERADO. Lo hubo, y
// era un defecto: al cerrar la ventana se borraba su entrada de `seen`, con lo que
// una RE-ENTREGA del mismo `wa_message_id` después del flush volvía a pasar la
// guarda y ABRÍA UNA VENTANA NUEVA con el mensaje que ya se había procesado — justo
// lo que T1.7 (b) prohíbe. La entrada se conserva.
//
// Lo que se paga por conservarla, dicho con el número: el mapa guarda UNA entrada
// —dos cadenas cortas— por tupla (tenant, sesión, contacto, evento) vista desde el
// arranque. Un tenant que abriera mil eventos al día suma del orden de 100 KB
// diarios, y el mapa se vacía en cada reinicio, que es lo mismo que acepta
// `passiveAnnounced` en runtime_engine.go. Si algún día el parque hace que eso
// importe, la salida es una caché ACOTADA (LRU por tamaño), no volver a borrar al
// cerrar: borrar al cerrar reabre el defecto.
//
// Y conservarla NO impide reabrir ventana sobre el mismo evento, que es legítimo
// —el índice de la 0072 es PARCIAL a propósito, un cliente puede volver a pedir—:
// `seen` guarda el ÚLTIMO id visto, así que un mensaje DISTINTO pasa la guarda y
// abre la ventana siguiente. Lo único que se descarta es el mismo mensaje dos veces.

// RecoverAtBoot es la RECUPERACIÓN DEL REINICIO (T1.1): las ventanas que llevan
// abiertas más que su plazo pasan a `pending`.
//
// Es literalmente un barrido, y eso es el diseño y no un atajo: el estado de una
// ventana vive en `intake_jobs`, no en este proceso, así que arrancar no es
// «restaurar» nada — es mirar la tabla. Por eso un reinicio en medio de una ráfaga
// no pierde el job (criterio de T1.1) y por eso este agregador no tiene un mapa de
// ventanas en memoria que salvar.
func (s *IntakeAggregator) RecoverAtBoot(ctx context.Context) int {
	n := s.Sweep(ctx)
	if n > 0 {
		s.log.Info("agregador: ventanas vencidas cerradas al arrancar", "jobs", n)
	}
	return n
}

// Run arranca el barrido periódico y bloquea hasta que ctx se cancele. Sin broker
// (ADR-0003): un ticker de Go, ni cron ni cola externa.
//
// ⚠️ QUÉ PASA CON UNA VENTANA ABIERTA SI EL PROCESO MUERE: nada que se pierda. No
// hay `time.AfterFunc` por ventana —y no lo hay a propósito—: un timer vivo en
// memoria es exactamente lo que un despliegue se lleva por delante, y esta casa
// despliega a menudo. El plazo de cada ventana se RECALCULA en cada barrido a
// partir de `message_ts` (o `created_at`), que están en la base. El proceso nuevo
// arranca, barre, y cierra lo que venció mientras no había nadie.
func (s *IntakeAggregator) Run(ctx context.Context) {
	if s == nil || s.jobs == nil {
		return
	}
	s.RecoverAtBoot(ctx)
	t := time.NewTicker(s.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.despertar:
			// EL ADELANTO POR EVENTO (T1.8-1 (h)): un intent seguro acaba de llegar y su
			// ventana se cierra AHORA, no dentro de hasta 5 s. `Sweep` es el mismo de
			// abajo y hace una pasada COMPLETA —se lleva TODAS las pistas y vuelve a
			// mirar la tabla—, que es lo que hace que un aviso descartado por buffer
			// lleno no pierda trabajo.
			s.Sweep(ctx)
		case <-t.C:
			// EL BARRENDERO, y NO se retira con el despertador puesto: los dos plazos de
			// la ventana híbrida (silencio 45 s, techo 120 s) NO TIENEN EVENTO que los
			// anuncie —nadie avisa de que el cliente ha dejado de escribir—, así que
			// alguien tiene que ir a mirar el reloj. Es también quien cierra las ventanas
			// que quedaron vivas de un proceso anterior y quien cubre un aviso perdido.
			s.Sweep(ctx)
		}
	}
}

// Sweep cierra las ventanas a las que les tocó y devuelve cuántas cerró. Es el
// CAMINO PRINCIPAL del disparo (T1.7) y corre FUERA del camino del entrante, que es
// lo que le permite leer `intake_jobs` y `tenant_settings` sin violar D-044.26.
//
// Es idempotente por construcción: quien cierra es el `UPDATE … WHERE
// status='aggregating'` del store, así que dos barridos solapados —o un barrido y
// un adelanto por intent— no pueden producir dos jobs.
func (s *IntakeAggregator) Sweep(ctx context.Context) int {
	if s == nil || s.jobs == nil || s.log == nil {
		return 0
	}
	open, err := s.jobs.ListAggregating(ctx, s.sweepBatch)
	if err != nil {
		s.log.Error("agregador: no se pudieron listar las ventanas vivas", "error", err)
		return 0
	}
	hints := s.takeHints()
	now := s.now()
	// Memo POR PASADA de los plazos de cada tenant: una ráfaga deja N ventanas del
	// mismo tenant y no hace falta preguntar N veces. Se descarta al terminar la
	// pasada a propósito — un cambio de config se ve en el barrido siguiente, no al
	// reiniciar.
	//
	// 🔴 GUARDA LOS DOS PLAZOS JUNTOS Y ES UNA SOLA ENTRADA, no dos memos ni dos
	// lecturas: `GetTenantSettings` devuelve el struct entero, así que separar silencio
	// y techo en dos cachés duplicaría las consultas a `tenant_settings` sin ganar nada
	// —y el presupuesto del barrido está medido (TestVentanaCero_… afirma UNA lectura
	// por pasada y por tenant).
	plazos := make(map[string]plazosDeVentana, 4)
	cerrados := 0
	for _, job := range open {
		if !s.due(ctx, job, hints, plazos, now) {
			continue
		}
		if s.closeWindow(ctx, job) {
			cerrados++
		}
	}
	return cerrados
}

// plazosDeVentana son los DOS números que gobiernan el cierre de un tenant. Van
// juntos en un tipo y no sueltos porque son UNA regla: la ventana cierra por lo que
// llegue antes, así que leer uno sin el otro no responde ninguna pregunta.
type plazosDeVentana struct {
	// silencio es `aggregation_window_seconds` (45 s por defecto): cuánto se espera
	// desde el ÚLTIMO mensaje del cliente.
	silencio time.Duration
	// techo es `aggregation_max_seconds` (120 s por defecto): cuánto se espera como
	// mucho desde que la ventana NACIÓ, pase lo que pase con el silencio.
	techo time.Duration
}

// due decide si a una ventana le tocó. TRES caminos, y los tres se dicen enteros:
//
//  1. el ADELANTO por intent (la pista), que es un atajo y llega por EVENTO;
//  2. el SILENCIO —45 s desde el último mensaje—, que es el camino principal;
//  3. el TECHO —120 s desde que la ventana nació—, que es la red que impide que 2
//     no cierre nunca.
//
// # POR QUÉ HACEN FALTA LOS DOS PLAZOS Y NO UNO (T1.8-1, D-044.43)
//
// Hasta esta tarea había uno solo y anclaba en el PRIMER mensaje, así que:
//
//   - una ráfaga tecleada despacio (t=0, 30, 60) SE PARTÍA EN DOS JOBS: a los 45 s el
//     barrido cerraba lo que hubiera y el tercer mensaje abría otra ventana. El
//     presupuesto salía incompleto y nadie veía un error.
//
// Y la cura obvia —anclar solo en el silencio— arregla eso y rompe lo otro:
//
//   - una conversación que gotea cada 40 s NUNCA alcanza 45 s de silencio, así que su
//     ventana no cerraría JAMÁS. Un job en `aggregating` que nadie recoge es un pedido
//     perdido, otra vez sin error.
//
// ⇒ los dos, y gana el primero que venza. Quitar el techo pone rojo el test del goteo;
// devolver el silencio al ancla vieja (`message_ts`) pone rojo el de la ráfaga lenta.
//
// El orden de evaluación no cambia el resultado —una ventana vencida se cierra haya o
// no pista, y por cualquiera de los dos plazos—, y por eso el job resultante es
// INDISTINGUIBLE por los tres caminos: aquí no se anota en ningún sitio cuál ganó
// (T1.7 (d)).
func (s *IntakeAggregator) due(ctx context.Context, job intake.OpenJob,
	hints map[intake.WindowKey]struct{}, memo map[string]plazosDeVentana, now time.Time) bool {
	if _, adelantada := hints[job.Key]; adelantada {
		return true
	}
	p, ok := memo[job.Key.TenantID]
	if !ok {
		p = s.plazosFor(ctx, job.Key.TenantID)
		memo[job.Key.TenantID] = p
	}
	return s.venceElSilencio(job, p, now) || s.venceElTecho(job, p, now)
}

// venceElSilencio es el plazo desde el ÚLTIMO mensaje del cliente.
//
// 🔴 EL ANCLA ES `LastActivity`, QUE ES `intake_jobs.updated_at`, Y NO `message_ts`.
// `updated_at` lo escribe el `DO UPDATE … updated_at = now()` del UPSERT, o sea el
// reloj de POSTGRES en cada mensaje de la ráfaga; `message_ts` lo pone el Edge con el
// reloj del CLIENTE. Medir un plazo restando dos relojes distintos es un fallo
// permanente y silencioso: con el teléfono adelantado la ventana no cerraría hasta que
// el desfase pasara, y con el reloj atrasado cerraría de inmediato. Ver el docstring de
// intake.OpenJob.
//
// `silencio <= 0` es el override explícito «flush inmediato» del tenant (CHECK >= 0 de
// la 0072): la ventana se cierra en el primer barrido que la vea. No es un valor
// degenerado ni un «sin configurar» — ver el COMMENT de la columna.
func (s *IntakeAggregator) venceElSilencio(job intake.OpenJob, p plazosDeVentana, now time.Time) bool {
	if p.silencio <= 0 {
		return true
	}
	return !now.Before(job.LastActivity.Add(p.silencio))
}

// venceElTecho es el plazo desde que la ventana NACIÓ (`intake_jobs.created_at`, otra
// vez el reloj de Postgres).
//
// 🔴 `techo <= 0` SIGNIFICA «VENCIDO SIEMPRE», IGUAL QUE EL 0 DE LA VENTANA — y NO
// «sin techo». La decisión está razonada en el COMMENT de la columna (0076) y se
// repite aquí porque es donde muerde: leer el 0 como «sin techo» pondría a dos
// columnas vecinas a significar cosas opuestas con el mismo número, y además
// construiría el interruptor del defecto que esta tarea cerró. No existe forma de
// apagar el techo, y es deliberado.
//
// La guarda no es solo por el 0: `Add` de una duración negativa daría un instante en
// el pasado y cerraría igual, pero decirlo explícito es lo que impide que alguien
// «simplifique» la línea y se lleve por delante la lectura del override.
func (s *IntakeAggregator) venceElTecho(job intake.OpenJob, p plazosDeVentana, now time.Time) bool {
	if p.techo <= 0 {
		return true
	}
	return !now.Before(job.CreatedAt.Add(p.techo))
}

// plazosFor resuelve los DOS plazos del tenant con UNA sola lectura de
// `tenant_settings`. Un fallo de settings cae a los DEFAULTS DE PLATAFORMA (45 s / 120
// s) y NO a «no cerrar»: una config ilegible no puede dejar ventanas abiertas para
// siempre, que sería un presupuesto que nunca llega y un job en `aggregating` que nadie
// recoge.
//
// 🔴 SE DEVUELVEN LOS DOS VALORES TAL CUAL, SIN PARCHEAR CEROS. El 0 de cualquiera de
// las dos columnas es un override explícito del tenant, y un `if x == 0 { x = Default }`
// aquí lo apagaría sin que nadie se entere — el defecto que repository_postgres.go
// prohíbe por escrito para la ventana, aplicado ahora a dos columnas en vez de una.
func (s *IntakeAggregator) plazosFor(ctx context.Context, tenantID string) plazosDeVentana {
	porDefecto := plazosDeVentana{
		silencio: store.DefaultAggregationWindow,
		techo:    store.DefaultAggregationMax,
	}
	if s.settings == nil {
		return porDefecto
	}
	cfg, err := s.settings.GetTenantSettings(ctx, tenantID)
	if err != nil {
		s.log.Warn("agregador: no se pudieron leer los plazos de la ventana (aggregation_window_seconds/aggregation_max_seconds); se usan los defaults de plataforma",
			"error", err, "tenant_id", tenantID)
		return porDefecto
	}
	return plazosDeVentana{silencio: cfg.AggregationWindow, techo: cfg.AggregationMax}
}

// closeWindow ejecuta la transición y, si de verdad la hizo ESTA llamada, invoca el
// punto de extensión de T1.4. Devuelve si cerró.
func (s *IntakeAggregator) closeWindow(ctx context.Context, job intake.OpenJob) bool {
	cerrada, err := s.jobs.CloseWindow(ctx, job.Key)
	if err != nil {
		s.log.Error("agregador: no se pudo cerrar la ventana de captación",
			"error", err, "tenant_id", job.Key.TenantID, "session_id", job.Key.SessionID, "job_id", job.ID)
		return false
	}
	if !cerrada {
		// Otro barrido (u otro proceso) llegó antes. No es un error y no se
		// reintenta: el guard de estado ya garantizó que hay UN job y no dos.
		return false
	}
	// EL PUNTO DE EXTENSIÓN DE T1.4, y el único sitio desde donde se llama. Va
	// DESPUÉS de la transición y su fallo NO la revierte: el job queda `pending` con
	// el sobre a NULL, que es una forma legítima en la 0072.
	if err := s.compose.ComposeAtFlush(ctx, job.Key); err != nil {
		s.log.Error("agregador: la ventana se cerró pero el literal no se pudo componer (T1.4)",
			"error", err, "tenant_id", job.Key.TenantID, "job_id", job.ID)
	}
	s.log.Debug("agregador: ventana cerrada",
		"tenant_id", job.Key.TenantID, "session_id", job.Key.SessionID, "job_id", job.ID)
	return true
}

// observeForAggregation es el ÚNICO puente entre el motor y el agregador. Vive
// aquí, y no en incoming.go, para que todo el 044 quepa en este fichero y la
// retirada —o el cambio— sea de un sitio solo.
//
// # LOS TRES PUNTOS DE LLAMADA, Y POR QUÉ SON TRES Y NO UNO (revisión 2026-08-22)
//
// Nació con uno solo —advanceLiveStep— y eso dejaba fuera, en silencio, JUSTO el
// mensaje que el plan viene a resolver. Hoy son tres, y son EXCLUYENTES entre sí: por
// cada entrante corre como mucho uno.
//
//  1. `advanceLiveStep` (incoming.go) — el TURNO NORMAL sobre una conversación viva.
//     Va justo al lado de persistTurnMessages y por el mismo motivo: ahí el turno YA
//     está persistido, se conoce el `turnEventID` (el evento que estaba vivo MIENTRAS
//     se produjo el turno, T4.5.1) y el hilo literal que T1.4 leerá acaba de escribirse.
//  2. `startFromDecision` (incoming.go) — EL MENSAJE QUE ARRANCA EL EVENTO. Es el
//     «quiero presupuesto de X» que abre la ráfaga, y no llegaba a `source_refs`: la
//     ventana la abría el mensaje SIGUIENTE, con lo que el `message_ts` —la base de
//     fechas, D-044.9— quedaba anclado al segundo. Ahí el `event_id` acaba de nacer
//     (o de recuperarse del vivo) en beginEvent, que es lo que lo hace posible.
//  3. `prepareResume` (resume.go), y SOLO en su rama de reinicio consumado — el
//     mensaje con el que el cliente reabre un pedido caducado. Ese turno se consume
//     ahí y nunca llega al (1).
//
// 🔴 LOS CAMINOS QUE DELIBERADAMENTE NO OBSERVAN llevan su porqué escrito EN EL SITIO,
// no aquí: el corte por fallo del sink durable —en sus TRES puntos: el turno vivo
// (incoming.go), la reanudación (resume.go) y el ARRANQUE (start.go, que lo devuelve, y
// birthEvent en events.go, que lo traduce en un `event_id` "")—, el cupo anti-loop
// agotado (resume.go), la elección numérica en el despachador (StartNewOfKind,
// events.go) y el salto por tipo sobre conversación viva (liveEventSwitch, events.go).
//
// 🔴 LA INVARIANTE QUE GOBIERNA A LOS PRIMEROS, Y QUE NO SE PUEDE ROMPER SIN ROMPER EL
// PLAN: TODO MENSAJE QUE ENTRA EN `source_refs` TIENE SU LITERAL EN EL HILO. Los cortes
// por sink durable deciden NO escribir el literal de ese turno, así que su referencia
// tampoco puede existir — quedaría colgando y el `source_text` saldría incompleto SIN
// dar error, que es el peor modo de fallo posible. Al revés vale igual: cualquier
// camino nuevo que observe tiene que escribir su literal por el mismo acto (es lo que
// hacen persistOpeningTurn en el arranque, persistTurnMessages en el turno vivo y en la
// CONMUTA, y persistTurnMessages con `nil` en la reanudación).
//
// Best-effort integral: nil-safe de arriba abajo y sin devolver error.
//
// 🔧 AQUÍ SE LEÍA EL INTENT ADJUNTO (`m.GetIntent()`), y ya no existe: T1.6-1 lo
// retiró del contrato y D-044.31 cambió el push por el pull. Lo que viaja hoy en su
// lugar es el TEXTO, que es lo que el Cloud necesita para PEDIR la clasificación
// (T1.6-4). El derecho sigue siendo `llm_intake` y solo ese —lo comprueba `Observe`—:
// exigir además `llm_intent` haría que un tenant con el pipeline contratado dependiera
// de una segunda feature para algo que la ventana de silencio hace igual sin señal.
func (rt *Runtime) observeForAggregation(ctx context.Context, tenantID, sessionID, contactID, eventID string, m *cloudlinkv1.IncomingMessage) {
	if rt.aggregator == nil || m == nil {
		return
	}
	var ts time.Time
	if m.GetTsUnix() > 0 {
		ts = time.Unix(m.GetTsUnix(), 0).UTC()
	} else {
		// Sin ts del cliente se usa el reloj del runtime (inyectable, WithClock).
		// Es peor base de fechas que el ts real, pero dejar message_ts a NULL haría
		// que el plazo de la ventana se midiera contra created_at igualmente: se
		// prefiere un instante escrito y explicable a un NULL.
		ts = rt.now()
	}
	rt.aggregator.Observe(ctx, IncomingRef{
		Key: intake.WindowKey{
			TenantID:  tenantID,
			SessionID: sessionID,
			ContactID: contactID,
			EventID:   eventID,
		},
		WaMessageID: m.GetWaMessageId(),
		MessageTS:   ts,
		Text:        m.GetText(),
	})
}
