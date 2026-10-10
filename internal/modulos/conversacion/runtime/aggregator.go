// Porta internal/flujos/runtime/aggregator.go @ e0159171

// aggregator.go — EL AGREGADOR DE VENTANAS del pipeline de captación por LLM
// (Plan 044 · Ola 1 · T1.1, T1.2 y T1.7; design §6.2, D-044.26, D-044.20).
//
// Un cliente no pide un presupuesto en un mensaje: lo pide en cinco seguidos. El
// agregador junta esos cinco en UNA ventana —una fila `intake_jobs` en estado
// `aggregating`— y la cierra (`aggregating` → `pending`) cuando le toca, para que el
// pipeline caro corra UNA vez y no cinco.
//
// # Las ocho reglas (diseno.md §4.1), y dónde las promete este contrato
//
//	AG-1  Observe: 1 sentencia SQL, 0 SELECT, 0 cripto, 0 red; guardas baratas primero ... Observe
//	AG-2  Observe no devuelve error; un fallo se loguea y el turno sigue (INV-10) ......... Observe
//	AG-3  la ventana de silencio es el camino PRINCIPAL; el intent solo adelanta ......... Sweep, OnClassified
//	AG-4  la pista: anotar bajo candado Y DESPUÉS avisar (no bloqueante, buffer 1) ....... OnClassified
//	AG-5  `seen` NO se borra al cerrar la ventana ...................................... Observe
//	AG-6  sin timer por ventana; el plazo se recalcula en cada barrido; cierre idempotente Sweep, RecoverAtBoot, Run
//	AG-7  tres llamantes del puente con el motor ....................................... (abajo: ola de `incoming`)
//	AG-8  sin compositor → noop; sin AheadRequester → siempre por reloj ................. WithSourceComposer, WithAheadRequester
//
// # AG-7 · El puente con el motor NO está en este contrato
//
// En el viejo este fichero termina con observeForAggregation (aggregator.go:962), un
// método NO exportado del Runtime: el ÚNICO puente entre el motor y el agregador.
// Nace en el verde y se prueba con el Runtime (ola de `incoming`), no aquí. Lo que
// promete, para quien lo escriba:
//
//   - Tiene TRES puntos de llamada, EXCLUYENTES entre sí (por entrante corre como
//     mucho uno): (1) el TURNO NORMAL sobre una conversación viva, junto a la escritura
//     del hilo del turno; (2) EL MENSAJE QUE ARRANCA EL EVENTO —el «quiero presupuesto
//     de X» que abre la ráfaga—, con el event_id recién nacido; (3) el REINICIO POR
//     REANUDACIÓN consumado (el mensaje con el que el cliente reabre un pedido caducado).
//   - 🔴 La invariante que los gobierna: TODO MENSAJE QUE ENTRA EN `source_refs` TIENE
//     SU LITERAL EN EL HILO. Los caminos que deciden NO escribir el literal del turno
//     (el corte por fallo del sink durable, el cupo anti-loop agotado, la elección
//     numérica en el despachador, el salto por tipo sobre conversación viva) NO
//     observan.
//   - Es nil-safe (sin agregador o sin mensaje no hace nada) y no devuelve error.
//   - Construye el IncomingRef con la tupla (tenant, sesión, contacto, evento), el
//     wa_message_id y el texto del entrante, y MessageTS = el ts_unix del mensaje en
//     UTC; si ts_unix no es > 0, el reloj del runtime (WithClock): se prefiere un
//     instante escrito y explicable a un NULL.
//
// # E-13
//
// El viejo mide 987 líneas. Aquí está partido por tema, solo moviendo declaraciones y
// con el sufijo del origen; cada parte con su test:
//
//   - aggregator.go: los tipos, los puertos, las opciones y el constructor.
//   - aggregator_observe.go: Observe (lo que corre en línea con el mensaje).
//   - aggregator_hint.go: OnClassified y las pistas de adelanto.
//   - aggregator_sweep.go: Sweep, RecoverAtBoot, los plazos y el cierre.
//   - aggregator_run.go: Run (el tick, el despertador y la parada).
//   - aggregator_bridge.go: observeForAggregation, el puente con el motor (AG-7).

package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// IntentIntakeRequest es el ÚNICO nombre de intención que adelanta un flush
// (D-044.20). Lo define la config del tenant (T1.3) y lo devuelve la clasificación
// que el Cloud PIDE (T1.6-4); aquí solo se compara. Literal observable.
const IntentIntakeRequest = "intake_request"

const (
	// defaultIntentConfidence es el umbral por defecto de `confidence` para que un
	// `intake_request` adelante el flush (ver WithIntentConfidence: es un default de
	// PLATAFORMA y no una config por tenant, por D-044.26).
	defaultIntentConfidence = 0.7
	// defaultSweepInterval es cada cuánto barre el cierre de ventanas. Sin broker
	// (ADR-0003): es un ticker de Go, ni cron ni cola externa.
	//
	// Fija el GRANO del cierre, no su plazo: una ventana de 45 s se cierra entre los
	// 45 y los 45+5 s. Y fija también el peor caso del adelanto por intent cuando el
	// despertador no llega —como mucho un tick—, que es la contrapartida aceptada de
	// que el adelanto NO se ejecute en línea con el mensaje (D-044.26: en línea solo
	// cabe UNA sentencia, y esa ya está gastada en abrir/ampliar la ventana).
	defaultSweepInterval = 5 * time.Second
	// defaultSweepBatch acota cuántas ventanas mira un barrido. Es un techo de
	// trabajo por pasada, no un límite de negocio: lo que no entra sale en el
	// siguiente tick, y las más viejas van primero.
	defaultSweepBatch = 200
)

// IntentHint es lo ÚNICO que la política de disparo mira de una clasificación: su
// nombre y su confianza.
//
// 🔴 NO LLEVA `params` A PROPÓSITO (D-044.20), y la garantía es el TIPO: tiene DOS
// campos y ninguno más. La política se dispara con la SEÑAL, no con los ítems: no lee
// params, no espera una lista de productos y no cambia de comportamiento según lo que
// el intent traiga dentro. Quien descompone en ítems es el pipeline P2–P4, aguas
// abajo y sobre el texto acumulado. Si alguien añade un campo aquí para «mejorar el
// disparo», está deshaciendo D-044.20.
type IntentHint struct {
	Name       string
	Confidence float64
}

// IncomingRef es lo mínimo que el agregador necesita de UN entrante.
//
// 🔴 LO QUE ENTRA A `intake_jobs` SON REFERENCIAS OPACAS Y NUNCA CONTENIDO
// (D-044.26). Un `wa_message_id` no es PII; el texto sí, y por eso el literal NO
// viaja a ninguna sentencia SQL desde aquí: el sobre del `source_text` nace NULL y se
// llena AL FLUSH (T1.4).
type IncomingRef struct {
	// Key es la tupla de la ventana: tenant, sesión, contacto y evento VIVO.
	Key intake.WindowKey
	// WaMessageID es el identificador opaco del entrante. Es la primera referencia
	// que entra a `source_refs` y, además, la clave con la que el agregador descarta
	// un mismo mensaje observado dos veces.
	WaMessageID string
	// MediaRefs son las referencias de audio/foto del mensaje, SIN DESCARGAR NADA
	// (T1.1). Entran a `source_refs` detrás del WaMessageID, en su orden. Hoy llegan
	// vacías: el entrante de CloudLink no trae un identificador de media separado del
	// `wa_message_id`, y una foto entra por su propio `wa_message_id`.
	MediaRefs []string
	// MessageTS es el instante del mensaje del CLIENTE (`ts_unix` del entrante), no
	// el reloj del servidor. Es la BASE DE FECHAS del presupuesto (D-044.9): «para el
	// jueves» se resuelve contra este instante. Solo cuenta si este entrante ABRE la
	// ventana; NO decide ningún plazo de cierre.
	MessageTS time.Time
	// Text es el literal del mensaje del cliente, y existe para UNA cosa: alimentar
	// la petición de clasificación (T1.6-4).
	//
	// 🔴 NO SE PERSISTE Y NO SE LOGUEA. No entra en el intake.Append, no toca ninguna
	// sentencia SQL y no aparece en un solo campo de log. Vacío es normal: un mensaje
	// de solo media no tiene texto que clasificar, y entonces no se pide nada.
	Text string
}

// AggregationSettings es lo mínimo que el barrido necesita de la config del tenant.
// Interfaz local (ISP): la satisfacen los dos repositorios de conversacion/store.
//
// ⚠️ Se consulta SOLO desde el barrido. Llamarla desde Observe sería el `SELECT` en
// línea con el mensaje que D-044.26 prohíbe.
type AggregationSettings interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
}

// SourceComposer ES EL PUNTO DE EXTENSIÓN DE T1.4: al cerrar una ventana alguien
// compone el literal (`source_text`) leyendo el hilo del evento y guarda su sobre. El
// compositor real es *SourceTextComposer (source_composer.go) y lo cablea el arranque
// con WithSourceComposer. Lo que vive AQUÍ es el hueco y el MOMENTO en que se llama
// (ver Sweep).
type SourceComposer interface {
	// ComposeAtFlush compone y persiste el `source_text` de la ventana recién
	// cerrada. Devolver error NO reabre la ventana ni corta nada: el job queda en
	// `pending` con el sobre vacío.
	ComposeAtFlush(ctx context.Context, key intake.WindowKey) error
}

// noopSourceComposer es la implementación VACÍA y DOCUMENTADA: el DEFAULT de
// construcción del agregador (AG-8). No es un olvido ni un stub sin dueño: el
// compositor real existe (source_composer.go) y producción lo inyecta con
// WithSourceComposer. Esto es lo que corre cuando nadie lo hace: el sobre se queda
// a NULL, que es una forma legítima en la 0072.
//
// ⚠️ SI ESTO CORRE EN PRODUCCIÓN, EL PIPELINE SE QUEDA SIN TEXTO Y NO HAY ERROR.
// El fallo sería MUDO: ventanas que cierran, jobs `pending` y un worker recibiendo
// el sobre vacío. El cable del arranque es la única cosa que lo impide.
type noopSourceComposer struct{}

func (noopSourceComposer) ComposeAtFlush(context.Context, intake.WindowKey) error { return nil }

// AheadRequester PIDE la clasificación que antes llegaba adjunta al mensaje (T1.6-4,
// D-044.31: el push murió, hoy es pull). Lo satisface el pool de clasificación
// adelantada de captación.
//
// La firma es corta y las tres cosas que NO tiene son el contrato:
//
//   - **no devuelve error**, porque corre en línea con el mensaje y no hay nada que el
//     agregador pueda hacer con un fallo salvo tragárselo (INV-10);
//   - **no devuelve la clasificación**, porque tarda segundos y esperarla aquí sería
//     justo lo que REQ-35 prohíbe — la respuesta vuelve por OnClassified;
//   - **no acepta `ctx`**: el contexto del turno se cancela en milisegundos y la
//     inferencia dura segundos, así que pasárselo mataría toda petición sin dar un solo
//     error. La firma impide el error en vez de advertirlo.
//
// Request tiene que volver enseguida (es un encolado, no una llamada al modelo).
type AheadRequester interface {
	Request(key intake.WindowKey, text string)
}

// IntakeAggregator acumula entrantes en ventanas y las cierra. Es seguro para uso
// concurrente: Observe corre desde la goroutine de cada entrante, OnClassified desde
// la del pool de clasificación y Sweep desde la de Run.
//
// NO es un EventSink y no cuelga del fan-out de efectos (en el viejo se llamó
// AggregatorSink hasta el 2026-08-22): EffectContext no lleva `wa_message_id`, y un
// turno puede producir CERO efectos —una ráfaga de texto libre pidiendo un
// presupuesto es justo eso—. Se alimenta del ENTRANTE.
//
// El estado de una ventana vive en `intake_jobs`, NO en este proceso: aquí no hay un
// mapa de ventanas que salvar (hallazgo 1 de F8). Lo que SÍ vive en memoria, y muere
// con el proceso sin perder ningún job, son las PISTAS de adelanto (ver OnClassified)
// y la memoria del último mensaje visto por tupla (ver Observe).
type IntakeAggregator struct {
	log      logger.Logger
	jobs     intake.JobStore
	settings AggregationSettings
	ents     entitlements.Resolver
	compose  SourceComposer
	// ahead es quien PIDE la clasificación (T1.6-4). nil ⇒ no se pide nada y la
	// ventana cierra siempre por su reloj, que es el camino garantizado de T1.7.
	ahead AheadRequester

	// now es el reloj INYECTABLE, mismo patrón que el del Runtime (WithClock) y por
	// el mismo motivo: sin él, un test de la ventana tendría que dormir de verdad 45
	// segundos y el caso «silencio ⇒ flush a los N s» no se podría cubrir.
	now func() time.Time

	sweepEvery      time.Duration
	sweepBatch      int
	intentThreshold float64

	mu sync.Mutex
	// dueNow son las ventanas que un intent ha ADELANTADO. Es una PISTA en memoria,
	// no un estado: si el proceso muere con pistas dentro, no se pierde ningún job
	// —la ventana sigue en `aggregating` en la base y el barrido la cierra por su
	// reloj—. Que la pista sea perecedera es coherente con T1.7 y no un descuido:
	// el intent adelanta, no decide. La protege `mu`.
	dueNow map[intake.WindowKey]struct{}
	// wake es EL DESPERTADOR DEL BARRIDO (Plan 044 · T1.8-1 criterio (h),
	// D-044.43): un canal con BUFFER 1 que `hintDueNow` toca y que `Run` escucha junto
	// al ticker. Vive fuera del candado a propósito — se escribe con un envío NO
	// BLOQUEANTE, así que no necesita `mu` y no puede bloquear a quien avisa.
	//
	// 🔴 POR QUÉ EXISTE: el intent es un EVENTO que YA LLEGÓ, y hasta esa tarea su
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
	wake chan struct{}
	// seen recuerda el último `wa_message_id` observado por ventana, para que un
	// mismo entrante observado dos veces no duplique su referencia. Es una red
	// SECUNDARIA: la primera es el dedupe PERSISTENTE de ingesta (Plan 028 · T6), que
	// corta el reenvío del outbox del Edge antes de llegar aquí. La protege `mu`.
	//
	// 🔴 NO se borra al cerrar la ventana (AG-5, trampa T-10): ver el final de
	// aggregator_observe.go.
	seen map[intake.WindowKey]string
}

// AggregatorOption configura el agregador al construirlo.
type AggregatorOption func(*IntakeAggregator)

// WithAggregatorClock inyecta el reloj con el que el barrido decide si un plazo
// venció. nil se ignora (se queda time.Now). Los tests lo inyectan SIEMPRE: sin él,
// «silencio ⇒ flush a los 45 s» solo se podría probar durmiendo 45 segundos.
func WithAggregatorClock(now func() time.Time) AggregatorOption {
	return func(s *IntakeAggregator) {
		if now != nil {
			s.now = now
		}
	}
}

// WithSweepInterval fija cada cuánto barre Run. Un valor <= 0 se ignora y queda el
// default de plataforma: 5 segundos.
//
// El intervalo fija el GRANO del cierre, no su plazo: una ventana de 45 s se cierra
// entre los 45 y los 45+5 s.
func WithSweepInterval(d time.Duration) AggregatorOption {
	return func(s *IntakeAggregator) {
		if d > 0 {
			s.sweepEvery = d
		}
	}
}

// WithSweepBatch fija cuántas ventanas vivas pide un barrido al almacén. Un valor
// <= 0 se ignora y queda el default de plataforma: 200.
//
// Es un TECHO DE TRABAJO POR PASADA, no un límite de negocio: lo que no entra sale en
// la pasada siguiente (las más antiguas van primero) y no se pierde nada.
func WithSweepBatch(n int) AggregatorOption {
	return func(s *IntakeAggregator) {
		if n > 0 {
			s.sweepBatch = n
		}
	}
}

// WithIntentConfidence fija el umbral de confianza a partir del cual un
// IntentIntakeRequest adelanta el flush. Un valor <= 0 se ignora y queda el default de
// plataforma: 0.7. El valor inyectado SUSTITUYE al default, no se suma a él.
//
// 🔴 Es un default de PLATAFORMA y no una config por tenant, por D-044.26: leer un
// umbral por tenant en línea con el mensaje sería el `SELECT` prohibido. Errar por lo
// bajo no rompe nada (cierra una ventana antes de tiempo y el cliente puede abrir
// otra); errar por lo alto tampoco (la ventana cierra igual por silencio).
func WithIntentConfidence(threshold float64) AggregatorOption {
	return func(s *IntakeAggregator) {
		if threshold > 0 {
			s.intentThreshold = threshold
		}
	}
}

// WithSourceComposer inyecta el compositor del literal (T1.4). nil se ignora.
//
// AG-8: SIN esta opción el agregador usa un compositor VACÍO y documentado: las
// ventanas cierran igual y el job queda `pending` con el sobre a NULL (jobs sin
// texto), la forma que la migración 0072 permite a propósito.
//
// ⚠️ Si ese vacío corre en producción, el pipeline se queda sin texto Y NO HAY ERROR:
// el cable del arranque es lo único que lo impide (trampa T-4 de la fase: es del
// candado de cableado).
func WithSourceComposer(c SourceComposer) AggregatorOption {
	return func(s *IntakeAggregator) {
		if c != nil {
			s.compose = c
		}
	}
}

// WithAheadRequester inyecta quien PIDE la clasificación (T1.6-4). nil se ignora.
//
// AG-8: SIN esta opción el agregador no pide nada, así que no adelanta NUNCA por su
// cuenta y toda ventana cierra por su reloj — que es una forma legítima y el camino
// garantizado de T1.7, no una avería.
func WithAheadRequester(a AheadRequester) AggregatorOption {
	return func(s *IntakeAggregator) {
		if a != nil {
			s.ahead = a
		}
	}
}

// NewIntakeAggregator construye el agregador. Nunca devuelve nil. Las opciones se
// aplican en orden sobre los defaults: reloj time.Now, barrido cada 5 s, 200 ventanas
// por pasada, umbral 0.7, compositor vacío y sin AheadRequester. No lee ni escribe
// nada al construir, ni arranca ninguna goroutine (eso es Run).
//
// Qué pasa con cada dependencia a nil:
//
//   - log, jobs o ents nil: Observe es un no-op seguro y Sweep devuelve 0;
//   - settings nil: NO apaga nada. Observe funciona igual (no la usa) y el barrido
//     cierra con los plazos de plataforma (45 s / 120 s). (El comentario del viejo,
//     aggregator.go:457-460, dice que «con cualquiera de los tres a nil Observe es un
//     no-op»; su código, :576 y :877, no trata así a settings. Manda el código.)
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
		wake: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}
