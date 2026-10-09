// Porta internal/intake/pipeline/pipeline.go @ 56097aa

// Package pipeline es EL WORKER del pipeline de presupuestos (Plan 044 · Ola 2 · T2.5): el
// llamante de las etapas. Reclama un job `pending`, descifra su literal, encadena
// P2 → P3 → P4 → match → draft y lo termina — o lo devuelve a la cola castigado, o lo
// mata con su causa escrita.
//
// Está partido por tema (E-13, D-F7-6): este fichero trae los puertos, la configuración y
// el constructor; `pipeline_loop.go`, el bucle (Run, Wake, los drenajes);
// `pipeline_chain.go`, la cadena de un job; `pipeline_outcome.go`, los desenlaces.
// `backoff.go` es la política de reintentos y `slot.go`, el aforo por Edge.
//
// # LO QUE ESTE WORKER ES, Y LO QUE NO
//
//   - NO es un planificador ni una cola con prioridades: el ADR-0046 los descarta por
//     escrito. Reclama de uno en uno, en el orden que fija el `ORDER BY` del reclamo.
//   - UNA instancia procesa UN job a la vez (W = 1), y en el proceso hay UNA sola
//     goroutine `Run` (R-01, I-CP-4). 🔴 Lanzar una segunda NO DA ERROR: el reclamo usa
//     `FOR UPDATE SKIP LOCKED` y las dos convivirían, pero el aforo y el reparto de
//     papeles con el flanco a READY están pensados para una. Nunca un segundo `Run`.
//   - TIENE AFORO POR EDGE (WithCapacity): dos cadenas de lote del MISMO Edge no se
//     solapan. El aforo acota la espera de un turno interactivo a UNA llamada de lote, no
//     a cero.
//   - NO TIENE INTERRUPTOR DE CONFIGURACIÓN (R-02). El único gate del pipeline es la
//     feature `llm_intake`, y vive fuera: sin ella no hay ventana ni job, y el worker gira
//     en vacío. Config{} es la configuración de producción, sin variables de entorno, a
//     propósito.
//   - NO EMPUJA AL CRM (R-07): eso es de la etapa `draft`, y solo en el re-análisis que
//     pidió la dueña. El worker le pasa el job tal cual lo reclamó.
//   - NO ANCLA LOS ADJUNTOS: `DraftInput.Media` viaja en CERO. La heurística de `anclaje`
//     sigue sin llamante de producción, porque nadie lee los `media refs` del hilo con sus
//     instantes. Es un hueco NOMBRADO, no un olvido.
//   - NO RELLENA LA VÍA DEL ANÁLISIS: `DraftInput.Analysis` viaja en CERO. Quien sabe por
//     qué vía corrió el job es el selector, que no lo publica por ningún puerto.
//   - NO escribe el aviso de degradación al dueño: lo escribe el decorador del selector de
//     vía, que ve TODAS las vías y no solo el pipeline.
//
// # 🔴 LA CARRERA DEL SOBRE SE PORTA TAL CUAL (D-F7-9; el arreglo es de F8)
//
// El agregador cierra la ventana (el job ya es `pending` y reclamable) y DESPUÉS compone y
// escribe el sobre del literal. Si un tic o un flanco a READY cae en ese hueco, el worker
// reclama un job cuyo sobre AÚN no se escribió: lo trata igual que el sobre que nunca
// llegará —`failed`, sin reintento, con el texto «el compositor del flush no llegó a
// escribir el sobre»—. Este worker NO se defiende de eso, a propósito: hoy «sobre aún no
// escrito» y «sobre que nunca llegará» son indistinguibles en la fila. La causa (que
// cierre y sobre no son un solo acto) la arregla F8, con el agregador.
//
// # 🔴 LOS FALLOS DE ESTA GOROUTINE SON MUDOS (R-13, D-11)
//
// Run no devuelve error ni lo publica: lo que falla queda en el log y nada más. No se le
// añade supervisión aquí: es un frente con dueño.
//
// # 🔴 EL AVISO QUE ESTE PAQUETE TIENE PRESENTE EN CADA LOG
//
// «No cuelgues una señal, una métrica o una guarda del DESENLACE FELIZ de una operación
// que, en el caso que te importa, FRACASA.» Aquí el caso que importa ES el infeliz: el
// `elapsed_ms` de una etapa se mide ANTES de saber si salió bien y se emite por los DOS
// caminos, y cada desenlace silencioso de la máquina —un reencolado que afecta 0 filas,
// un fallo que no aplica— tiene su propia línea, porque si no, un job atascado en
// `processing` no dejaría ni una.
//
// # LO QUE NUNCA SALE POR EL LOG NI POR EL MOTIVO DE MUERTE
//
// Ni una palabra del cliente (ADR-0034): ni el literal, ni el artefacto persistido, ni el
// documento del catálogo. Los errores de este paquete están escritos para no citarlos.
package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Decrypter (antes `Descifrador`) es lo ÚNICO que el worker necesita del stack de claves:
// abrir el sobre de tres piezas. Lo satisface `*crypto.FieldCipher`, el MISMO que lo cerró
// en el compositor del flush.
//
// Es una interfaz local y estrecha y no el tipo concreto: así el worker no arrastra el
// keyring entero a sus tests, y sobre todo así se puede probar QUÉ HACE con un descifrado
// que falla — que es un caso real (KEK retirada del keyring, KMS caído).
type Decrypter interface {
	Decrypt(valueEnc, valueDEK []byte, keyID string) (string, error)
}

// IdeasStage (antes `EtapaIdeas`) es P2. Lo satisface `*stages.P2`.
type IdeasStage interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error)
}

// SpecsStage (antes `EtapaEspecificaciones`) es P3. Lo satisface `*stages.P3`.
type SpecsStage interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want) (*stages.P3Artifact, error)
}

// NormalizationStage (antes `EtapaNormalizacion`) es P4. Lo satisface `*stages.P4`.
type NormalizationStage interface {
	Run(ctx context.Context, job intake.ClaimedJob, literal string,
		items []llm.ItemSpec, delivery *llm.Hint) (*llm.Quantities, error)
}

// MatchStage (antes `EtapaMatch`) es `match`, la primera etapa que NO habla con un modelo
// en su camino normal. Lo satisface `*stages.Match`.
type MatchStage interface {
	Run(ctx context.Context, job intake.ClaimedJob, in stages.MatchInput) (*stages.MatchArtifact, error)
}

// DraftStage (antes `EtapaDraft`) es `draft`, la última: la que escribe FUERA de
// `intake_jobs` y devuelve el `intake_id` con el que se cierra el job. Lo satisface
// `*stages.Draft`.
type DraftStage interface {
	Run(ctx context.Context, job intake.ClaimedJob, in stages.DraftInput) (*stages.DraftArtifact, error)
}

// Catalogs (antes `Catalogos`) es de dónde sale el índice del catálogo del tenant. Lo
// satisface `*indice.Cache`.
//
// 🔴 ES UN PUERTO DEL WORKER Y NO DE LA ETAPA, Y ESA FRONTERA ES EL CRITERIO. El worker lo
// consulta UNA VEZ POR JOB y le pasa el `*indice.Indice` ya construido a `match`, que
// busca por cada ítem; el índice no tiene con qué leer nada, así que no hay forma de que
// el ítem número 7 de un pedido dispare un SELECT.
type Catalogs interface {
	Obtener(ctx context.Context, tenantID string) (*indice.Indice, error)
}

// ShippingZones (antes `ZonasDeEnvio`) son las zonas de `tenant_settings.shipping_zones`
// del tenant. Lo satisfacen `*intakes.Postgres` y `*intakes.MemoryStore`.
//
// Se lee también UNA VEZ POR JOB y por el mismo motivo que el catálogo: la etapa `match`
// construye la línea de envío y no debe poder consultar la base.
type ShippingZones interface {
	ShippingZones(ctx context.Context, tenantID string) ([]intakes.ShippingZone, error)
}

// Las CINCO etapas de producción y las dos fuentes por job satisfacen sus puertos,
// comprobado en compilación.
var (
	_ IdeasStage         = (*stages.P2)(nil)
	_ SpecsStage         = (*stages.P3)(nil)
	_ NormalizationStage = (*stages.P4)(nil)
	_ MatchStage         = (*stages.Match)(nil)
	_ DraftStage         = (*stages.Draft)(nil)
	_ Catalogs           = (*indice.Cache)(nil)
	_ ShippingZones      = (*intakes.Postgres)(nil)
	_ ShippingZones      = (*intakes.MemoryStore)(nil)
)

// ErrNotWired (antes `ErrSinCablear`) es el worker al que le falta una pieza. Como las
// etapas, no nace a medias: un worker sin store reclamaría nil y un worker sin descifrador
// no podría abrir un solo sobre, y las dos cosas se descubrirían en producción.
//
// 🔴 LAS CINCO ETAPAS Y EL CATÁLOGO SON OBLIGATORIOS A PROPÓSITO. Cablear `match` y
// `draft` como opción haría que un worker sin borrador siguiera compilando, pasando los
// tests y terminando jobs en `done` sin `intake_id`.
var ErrNotWired = errors.New("pipeline: el worker necesita log, store, las CINCO etapas, el catálogo y el descifrador")

// CallTimeoutFloor (antes `PlazoPorLlamadaSuelo`) es el plazo que el arranque le pone a
// CADA llamada al modelo de las etapas LLM (`stages.WithCallTimeout`, R-03): 48 s. El
// worker NO lo aplica él: la constante vive aquí porque es la cuenta de este paquete, y la
// leen el cableado de las etapas y el plazo de G7 (T-13).
//
// 🔴 ES UN SUELO CALCULADO, NO UN p99. La condición que un plazo `D` por llamada tiene que
// cumplir son dos desigualdades:
//
//	D − MargenVeredicto > max(P3)          (que la llamada no muera antes de contestar)
//	(D − MargenVeredicto) × 0,8 > p99(P3)  (que una P3 sana no cuente como lenta)
//
// Con `MargenVeredicto` = 7 s y el máximo OBSERVADO de 32 s, la primera pide D > 39 s y la
// segunda —usando ese mismo 32 s en lugar del p99 que no existe— pide D > 47 s. De ahí
// sale 48 s: el número más pequeño que satisface lo que HAY MEDIDO. `p99(P3)` NO ESTÁ
// MEDIDO (hay dos observaciones), así que el número honesto es ≥ 48 s.
//
// LA CONSECUENCIA QUE HAY QUE SABER ANTES DE SUBIRLO: con D = 48 s un ítem puede retener
// la plaza única 41 s, así que 10 ítems son ≈ 6:50 — POR ENCIMA de «< 5 min». Subir D
// empeora esa cuenta y bajarlo mata llamadas sanas.
//
// POR QUÉ NO SE DEJA A CERO «porque el Edge tiene default»: porque el default es la
// avería. Sin deadline el adaptador manda 30 s y el breaker llama lento a todo lo que pase
// de 24 s: una P3 CALIENTE de 27 s ya cuenta como lenta y una FRÍA de 32 s muere.
const CallTimeoutFloor = 48 * time.Second

// Config son las perillas del worker. Todas caen a su valor por defecto con un valor
// `<= 0`, nunca a cero: un techo de intentos a cero mataría el primer job que tropezara y
// una cadencia a cero haría un bucle de CPU al 100 %. Config{} es la configuración de
// producción (R-02): cadencia 5 s, 3 intentos por calidad, 10 por infraestructura,
// castigo de 30 s hasta 5 min.
type Config struct {
	// Cadence (antes `Cadencia`) es cada cuánto se pregunta por trabajo nuevo. Por
	// defecto, DefaultCadence.
	Cadence time.Duration
	// MaxQualityAttempts (antes `MaxIntentosCalidad`) es el techo de intentos cuando la
	// causa es CauseQuality. Por defecto, DefaultMaxQualityAttempts.
	MaxQualityAttempts int
	// MaxInfraAttempts (antes `MaxIntentosInfra`) es el techo de intentos cuando la causa
	// es CauseInfra. Por defecto, DefaultMaxInfraAttempts.
	MaxInfraAttempts int
	// BackoffBase es el primer castigo de la curva exponencial. Por defecto,
	// DefaultBackoffBase.
	BackoffBase time.Duration
	// BackoffCap (antes `BackoffTope`) es el techo de la curva. Por defecto,
	// DefaultBackoffCap.
	BackoffCap time.Duration
}

// withDefaults (antes `conDefaults`) rellena lo que venga a cero o negativo.
func (c Config) withDefaults() Config {
	if c.Cadence <= 0 {
		c.Cadence = DefaultCadence
	}
	if c.MaxQualityAttempts <= 0 {
		c.MaxQualityAttempts = DefaultMaxQualityAttempts
	}
	if c.MaxInfraAttempts <= 0 {
		c.MaxInfraAttempts = DefaultMaxInfraAttempts
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = DefaultBackoffBase
	}
	if c.BackoffCap <= 0 {
		c.BackoffCap = DefaultBackoffCap
	}
	return c
}

// ceilingOf (antes `topeDe`) devuelve el techo de intentos que le toca a una causa.
// CauseInvalidJob no aparece, y hay que decir por qué: `stumble` lo aparta ANTES de llegar
// aquí —un job inválido muere sin curva—. Si alguien quitara esa guarda, este `return`
// silencioso le daría el techo de infra y volveríamos a los 29 minutos del job `6c5aac22`.
func (c Config) ceilingOf(cause string) int {
	if cause == CauseQuality {
		return c.MaxQualityAttempts
	}
	return c.MaxInfraAttempts
}

// Worker recorre `pending` y encadena las etapas. Una instancia procesa UN job a la vez.
// Se construye con NewWorker; su valor cero no es utilizable.
type Worker struct {
	log   logger.Logger
	store intake.PipelineStore
	p2    IdeasStage
	p3    SpecsStage
	p4    NormalizationStage
	match MatchStage
	draft DraftStage
	// catalogs y zones son las DOS lecturas por job: lo que `match` necesita del tenant y
	// no puede ir a buscar por sí misma. El catálogo es obligatorio —sin índice ningún
	// ítem casa—; las zonas no: un tenant sin zonas configuradas es el caso normal y su
	// borrador sale con «Envío por confirmar».
	catalogs  Catalogs
	zones     ShippingZones
	decrypter Decrypter
	cfg       Config
	now       func() time.Time
	newTicker func(time.Duration) (<-chan time.Time, func())

	// capacity y slots son EL ENTERO de T2.7 y quien sabe a qué plaza apunta un job. Van
	// en pareja y nacen juntos (WithCapacity). Los DOS nil = worker sin aforo, que sigue
	// siendo legal (lo grita el arranque, ver Run).
	capacity *Capacity
	slots    Slots

	// wakes es el DISPARADOR POR EVENTO (D-044.43): por aquí entra «el Edge de este tenant
	// acaba de decir que puede». Tiene buffer y el envío es NO BLOQUEANTE (ver Wake)
	// porque quien lo llena es el bucle Recv del gateway, que no puede esperar a nadie.
	wakes chan Slot
}

// Option (antes `Opcion`) es una perilla del worker que NO es un número: un colaborador.
// Se separan de Config a propósito —Config son las perillas que un operador puede querer
// mover; esto son piezas que se cablean una vez—.
type Option func(*Worker)

// WithCapacity (antes `ConAforo`) le da al worker el entero de T2.7 y quien resuelve la
// dirección de la plaza. Los dos o ninguno: con uno solo a nil, la opción no hace nada y
// el worker sigue sin aforo (y lo grita al arrancar, ver Run).
//
// `slots` lo satisface `*llmvia.Selector`, que es quien sabe la vía del tenant — y por
// tanto quien sabe que por vía API NO HAY PLAZA que tomar. El worker nunca pregunta eso:
// recibe un `ok` y ya.
func WithCapacity(c *Capacity, slots Slots) Option {
	return func(w *Worker) {
		if c == nil || slots == nil {
			return
		}
		w.capacity, w.slots = c, slots
	}
}

// WithShippingZones (antes `ConZonasDeEnvio`) le da al worker de dónde leer las zonas de
// envío del tenant. Con nil la opción no hace nada. Es OPCIÓN y no parámetro obligatorio
// porque su ausencia no inventa nada: sin lector, `match` recibe cero zonas y la línea de
// envío sale «Envío por confirmar» a precio vacío, que es la misma línea de un tenant con
// 0 zonas o con más de una.
//
// 🔴 Y POR ESO MISMO ES OMISIBLE SIN SÍNTOMA: lo único que se pierde al olvidarla es la
// tarifa plana del tenant con UNA zona configurada, y eso no da error. Su red es el Warn
// del arranque (ver Run) y el candado de cableado.
func WithShippingZones(z ShippingZones) Option {
	return func(w *Worker) {
		if z == nil {
			return
		}
		w.zones = z
	}
}

// WithClock inyecta el reloj del worker: con él se miden los `elapsed_ms` de las etapas y
// se calcula la marca del reintento (`next_attempt_at = now() + castigo`). Con nil la
// opción no hace nada; sin ella el reloj es `time.Now`.
//
// Es una costura NUEVA (el viejo dejaba el campo a mano de sus tests internos): no cambia
// la conducta por defecto. El reloj NO gobierna el ticker (WithTicker) ni las fechas de un
// pedido, que salen de `message_ts` dentro de las etapas.
func WithClock(now func() time.Time) Option {
	return func(w *Worker) {
		if now == nil {
			return
		}
		w.now = now
	}
}

// WithTicker inyecta la fábrica del ticker de Run: recibe la cadencia ya resuelta
// (Config.Cadence con su valor por defecto) y devuelve el canal de los tics y la función
// que lo para. Run la llama UNA vez al arrancar y llama a la función de parada al volver.
// Con nil la opción no hace nada; sin ella el ticker es `time.NewTicker`.
//
// Es una costura NUEVA, para probar el bucle sin dormir: no cambia la conducta por
// defecto.
func WithTicker(newTicker func(cadence time.Duration) (ticks <-chan time.Time, stop func())) Option {
	return func(w *Worker) {
		if newTicker == nil {
			return
		}
		w.newTicker = newTicker
	}
}

// wakeBufferSize (antes `capacidadDespertares`) es cuántos flancos a READY caben
// esperando a que el worker vuelva al select. Treinta y dos y no uno: el flanco es raro,
// pero llegan en RÁFAGA cuando el Cloud se reinicia y toda la flota vuelve a latir a la
// vez. Lleno ⇒ se descarta el aviso, y descartarlo es seguro: el ticker sigue barriendo y
// el backoff sigue venciendo. Ver Wake.
const wakeBufferSize = 32

// NewWorker construye el worker. Devuelve ErrNotWired (y worker nil) si `log`, `store`,
// cualquiera de las cinco etapas, `catalogs` o `decrypter` es nil.
//
// Las CINCO etapas van POSICIONALES y EN EL ORDEN DE LA CADENA (`p2 → p3 → p4 → match →
// draft`), detrás el catálogo que `match` consume. No hay riesgo de equivocar el orden:
// los cinco puertos tienen firmas distintas, así que una permutación no compila.
//
// `cfg` se completa con los valores por defecto (ver Config). Las OPCIONES comparten
// forma: cablean algo cuya ausencia NO da error, solo sirve peor (WithCapacity,
// WithShippingZones) o es una costura de test (WithClock, WithTicker). Se aplican en
// orden; la última gana.
//
// El buzón de Wake existe desde aquí: se puede despertar a un worker que aún no corre.
func NewWorker(log logger.Logger, store intake.PipelineStore,
	p2 IdeasStage, p3 SpecsStage, p4 NormalizationStage,
	match MatchStage, draft DraftStage, catalogs Catalogs,
	decrypter Decrypter, cfg Config, opts ...Option) (*Worker, error) {
	if log == nil || store == nil || p2 == nil || p3 == nil || p4 == nil ||
		match == nil || draft == nil || catalogs == nil || decrypter == nil {
		return nil, ErrNotWired
	}
	w := &Worker{
		log: log, store: store, p2: p2, p3: p3, p4: p4,
		match: match, draft: draft, catalogs: catalogs, decrypter: decrypter,
		cfg: cfg.withDefaults(), now: time.Now,
		newTicker: realTicker,
		wakes:     make(chan Slot, wakeBufferSize),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// realTicker es el ticker de producción: `time.NewTicker`.
func realTicker(cadence time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(cadence)
	return t.C, t.Stop
}
