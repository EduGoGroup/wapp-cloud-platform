// Porta internal/integrations/worker.go @ 36d5a04

package integrations

import (
	"context"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// El worker es el ÚNICO sitio del paquete que importa net/http (este fichero y
// worker_delivery.go, que es su trozo): el store y el sink que encola
// (runtime.WebhookSink) NUNCA hacen POST — es la garantía estructural de INV-02
// (design.md D-042.4, handoff §7.2).
//
// Está partido en tres por tamaño (E-13), solo moviendo declaraciones:
//   - worker.go: los puertos, la configuración, el constructor y el ciclo de Run.
//   - worker_delivery.go: la entrega de una fila (completar, firmar, POST).
//   - worker_failure.go: el fallo (backoff o dead), el claim perdido y la parada.

// BuyerDataReader es lo mínimo que el worker necesita del dominio de solicitudes
// para completar `buyer_data` justo antes del POST (D-042.9: "el builder la
// descifra... dentro del worker, nunca en línea con el mensaje"). Lo satisface
// *intakes.PostgresBuyerData; interfaz mínima (ISP, patrón flowEventStore/
// ProjectionStore de este mismo repo) para poder testear el worker con un fake.
type BuyerDataReader interface {
	GetBuyerData(ctx context.Context, intakeID string) (intakes.BuyerData, bool, error)
}

// CustomerNoteReader es lo mínimo que el worker necesita para completar
// `customer_note` justo antes del POST. Lo satisface *intakes.Postgres.
//
// Es el TERCER campo que se completa aquí en vez de congelarse en la plantilla,
// y el único de los tres que llegó por PII y no por coste: la indicación del
// cliente («dejarlo en portería, calle Mayor 14») es texto libre suyo, y
// congelarla en webhook_outbox.payload la dejaba en claro en una tabla que
// además sobrevive a la entrega — la tercera puerta que destapó el arreglo del
// defecto A2 del Plan 041. Mismo camino que buyer_data: fuera de lo persistido,
// dentro de lo que se arma en memoria (INV-02).
type CustomerNoteReader interface {
	GetCustomerNote(ctx context.Context, tenantID, intakeID string) (string, bool, error)
}

// TenantVariablesReader es lo mínimo que el worker necesita de tenantvars.Store
// para completar `variables{}` (D-042.11: snapshot al momento de la ENTREGA, no
// del push — decisión 2026-08-07, prevalece INV-02). Interfaz mínima (ISP): el
// worker solo lee, nunca escribe (Replace es del CRUD de tenant-variables, otro
// consumidor).
type TenantVariablesReader interface {
	List(ctx context.Context, tenantID string) ([]tenantvars.Variable, error)
}

// WorkerConfig son los parámetros de D-042.4, todos con default si vienen <= 0
// (nunca un poll a 0 ni un tope de intentos nulo por accidente).
type WorkerConfig struct {
	// PollInterval es la cadencia del loop de reclamo. Default 5s (WAPP_WEBHOOK_POLL_INTERVAL).
	PollInterval time.Duration
	// MaxAttempts es el tope de intentos antes de pasar a dead. Default 10 (WAPP_WEBHOOK_MAX_ATTEMPTS).
	MaxAttempts int
	// Timeout es el timeout HTTP de CADA entrega. Default 10s (WAPP_WEBHOOK_TIMEOUT).
	Timeout time.Duration
	// BatchSize es cuántas filas reclama cada vuelta del poll. Default 20: no hay
	// env para esto en D-042.4 (no lo pide), valor conservador fijo en código.
	BatchSize int
	// ClaimLease es cuánto puede estar una entrega reclamada ('delivering') antes
	// de que se la considere huérfana y otro worker pueda rescatarla (Plan 042 ·
	// Ola 3.1, migración 0049). Es también la cadencia del rescate.
	//
	// DEFAULT DERIVADO, no un número suelto: 3× Timeout, con piso de 1 minuto. El
	// razonamiento es que una entrega legítima NO puede durar más que su propio
	// timeout HTTP (lo impone el context de deliver), así que a 3× el margen ya
	// cubre las dos escrituras a la base que la rodean y cualquier pausa razonable
	// del proceso. Ponerlo por debajo del Timeout sería el bug de vuelta: se
	// rescatarían entregas vivas. Como BatchSize, no tiene env porque D-042.4 no
	// lo pide; quien lo necesite lo fija en código.
	//
	// Se deriva del Timeout YA con su default: con Timeout 0 son 3×10s = 30s y gana
	// el piso de 1 minuto. El derivado supera siempre al Timeout.
	ClaimLease time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	// Se calcula DESPUÉS de Timeout a propósito: el lease se deriva de él.
	if c.ClaimLease <= 0 {
		c.ClaimLease = max(3*c.Timeout, time.Minute)
	}
	return c
}

// Worker es el entregador en proceso del puente CRM (D-042.4): loop de poll,
// claim con SKIP LOCKED, POST firmado, backoff exponencial con jitter, y
// recuperación de huérfanos al arrancar. Es la primera goroutine de polling de
// larga vida de este repo (no hay molde previo que copiar — confirmado antes de
// escribir esto): el patrón de apagado es el mismo `select` sobre ctx.Done() que
// ya usa serveAndWait en el arranque, adaptado a un ticker.
//
// Hay UNO por proceso (reglas.md §3): con más de una réplica del proceso, lo que
// reparte el trabajo es el claim del almacén, no este tipo.
type Worker struct {
	store    Store
	buyer    BuyerDataReader
	notes    CustomerNoteReader
	tenvars  TenantVariablesReader
	http     *http.Client
	log      logger.Logger
	cfg      WorkerConfig
	onRecord func(status string)
	now      func() time.Time
}

// WorkerOption ajusta un Worker al construirlo (NewWorker). Hoy solo existe
// WithClock. Es NUEVA: el constructor viejo no tenía opciones.
type WorkerOption func(*Worker)

// WithClock inyecta el reloj del worker. Sustituye a los dos time.Now() del
// fichero viejo (worker.go:247 y :460): de él salen el instante que se firma y
// viaja en X-Wapp-Timestamp (now().Unix()) y la base sobre la que se reprograma
// un intento fallido (now() + backoff). Con now == nil la opción no hace nada; sin
// la opción, el reloj es time.Now.
//
// NO gobierna la cadencia del poll ni la del rescate (son tickers del runtime) ni
// el timeout HTTP de cada entrega.
func WithClock(now func() time.Time) WorkerOption {
	return func(w *Worker) {
		if now != nil {
			w.now = now
		}
	}
}

// NewWorker construye el worker. onRecord es el callback de métricas (T3.4,
// visibilidad de dead) — mismo patrón desacoplado que receipts.NewSink: este
// paquete NUNCA importa internal/platform/metrics, el llamante (el arranque)
// pasa mtx.WebhookDelivery directo. onRecord puede ser nil (tests): entonces no
// se cuenta nada y nada entra en pánico.
//
// cfg se completa con sus defaults (ver WorkerConfig): cada campo <= 0 toma el
// suyo, y un valor positivo se respeta tal cual. El cliente HTTP es uno propio del
// worker, sin timeout global: el plazo de cada entrega lo pone cfg.Timeout. No
// llama al almacén ni arranca nada: eso es Run.
//
// onRecord recibe EXACTAMENTE uno de cuatro valores, y uno por entrega resuelta
// (R-14, R6.4.d; es la etiqueta `status` de wapp_webhook_deliveries_total, de
// cardinalidad FIJA y sin tenant):
//
//   - «delivered» — 2xx del puente y la fila cerrada;
//   - «failed» — el intento falló y la entrega volverá a intentarse;
//   - «dead» — el intento falló y era el último (MaxAttempts);
//   - «claim_lost» — este worker perdió el claim antes de poder cerrar la fila
//     (Plan 042 · Ola 3.1). No es un fallo de entrega ni un éxito: es este proceso
//     llegando tarde. Se cuenta aparte porque un valor distinto de cero significa
//     que el lease se está quedando corto para la carga real, y eso se corrige
//     subiendo ClaimLease, no reintentando.
//
// Si el cierre de la fila falla por otra causa, no se cuenta nada.
func NewWorker(store Store, buyer BuyerDataReader, notes CustomerNoteReader, tenvars TenantVariablesReader, log logger.Logger, cfg WorkerConfig, onRecord func(status string), opts ...WorkerOption) *Worker {
	w := &Worker{
		store:    store,
		buyer:    buyer,
		notes:    notes,
		tenvars:  tenvars,
		http:     &http.Client{},
		log:      log,
		cfg:      cfg.withDefaults(),
		onRecord: onRecord,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Run bloquea hasta que ctx se cancele (D-042.4). Se arranca con `go worker.Run(ctx)`
// sobre el MISMO ctx derivado de signal.NotifyContext que cierra el resto del
// proceso — un solo Ctrl+C también para el worker, sin un segundo mecanismo de
// shutdown.
//
// DOS RELOJES (Plan 042 · Ola 3.1). El rápido reclama y entrega
// (PollInterval, 5s). El lento rescata entregas cuyo claim venció (ClaimLease).
// Van separados porque responden preguntas distintas: el poll pregunta "¿hay algo
// que entregar?" y el rescate "¿alguien murió a medias?". Meter el rescate dentro
// del poll costaría un UPDATE de rango cada 5 segundos para responder que no.
//
// El rescate corre además de forma PERIÓDICA, no solo al arrancar como decía el
// D-042.4 original: con una sola instancia el arranque ERA el rescate, pero con
// más de una réplica —que es lo que SKIP LOCKED existe para permitir— el trabajo
// de la que muere tiene que recogerlo una viva, sin esperar a que un humano
// reinicie algo.
//
// # El ciclo
//
// Al arrancar, y ANTES de esperar a ningún reloj: un rescate
// (Store.RecoverOrphanDeliveries con ClaimLease) y, después, un primer poll. Luego
// un poll cada PollInterval y un rescate cada ClaimLease, hasta que ctx se cancele:
// entonces deja en INFO «webhook worker: apagando (contexto cancelado)» y vuelve.
//
// Un rescate que recupera algo lo dice en WARN («webhook worker: entregas con el
// claim vencido devueltas a pending», con `count` y `lease`) y no en INFO a
// propósito: en régimen normal esto no rescata nada, así que una línea aquí
// significa que un worker murió a medias o que el lease se quedó corto. Si no
// recupera nada, no dice nada. Si falla: ERROR «webhook worker: rescatar entregas
// con el claim vencido» (con `error`), y el worker sigue.
//
// Un poll reclama un lote (Store.ClaimWebhookBatch con BatchSize) y entrega cada
// fila de forma SECUENCIAL, en el orden en que las devolvió el almacén: D-042.4 no
// pide concurrencia dentro de un lote (el paralelismo real, si hiciera falta,
// vendría de correr varias réplicas del proceso). Si el reclamo falla: ERROR
// «webhook worker: reclamar lote» (con `error`), y el worker sigue en el
// siguiente tick.
//
// # La entrega de una fila
//
// Completa el payload, firma y hace el POST. La plantilla encolada NO lleva
// buyer_data, customer_note ni variables{}: los tres se leen AHORA, justo antes
// del POST, y nunca se escriben de vuelta en la fila (INV-02; D-042.9, D-042.11).
// Consecuencia deliberada: lo que llega al puente es lo del instante de la
// ENTREGA —si el dueño corrigió la nota o cambió una variable entre el push y un
// reintento, se entrega lo corregido—, y TODO LO DEMÁS de la plantilla (`items`,
// `total`, `timestamp`…) llega tal como se encoló: un reintento no lo refresca.
// El cuerpo entregado es la plantilla MÁS exactamente esos tres campos: ni uno
// perdido, ni uno inventado, ni uno reescrito.
//
//   - buyer_data: BuyerDataReader.GetBuyerData con el `intake_id` de la plantilla.
//     Sin fila, o si la plantilla no trae `intake_id` (o no es una cadena): `{}`.
//   - customer_note: CustomerNoteReader.GetCustomerNote con el tenant DE LA FILA
//     (nunca el del cuerpo del payload) y ese `intake_id`. Sin fila o sin
//     `intake_id`: cadena vacía.
//   - variables: TenantVariablesReader.List del tenant de la fila, como objeto
//     clave → valor. Sin variables: `{}`.
//
// El destino es el vigente del tenant de la fila: GetTenantIntegration y
// GetTenantSecret del almacén. Si el tenant ya no tiene integración habilitada
// (borrada o apagada después de encolar), o le falta la URL o el secreto, no hay
// POST.
//
// El POST va a EndpointURL con Content-Type «application/json» y tres cabeceras:
// «X-Wapp-Signature» (sigv1.SignatureHeader de sigv1.Sign(secreto, instante,
// cuerpo), sobre el cuerpo EXACTO que se envía), «X-Wapp-Timestamp» (ese instante:
// los segundos Unix del reloj del worker, en decimal) y «X-Wapp-Delivery» (el id
// de la fila, en decimal). Su plazo es Timeout.
//
// Una respuesta 2xx (200–299) cierra la fila con Store.MarkWebhookDelivered,
// presentando el claim TAL COMO lo devolvió el almacén, y cuenta «delivered».
//
// # Los fallos
//
// Un error en cualquier paso ANTES del POST (parseo, lectura, tenant sin
// integración) se trata como fallo de entrega igual que un POST fallido: mismo
// camino de backoff/dead, un solo lugar que decide. El motivo acaba en last_error
// y es uno de estos textos, byte a byte (los que acaban en «: » llevan detrás la
// causa):
//
//   - «plantilla del payload no es JSON válido: »
//   - «plantilla del payload no es un objeto JSON» — un `null`, que sí es JSON
//     válido; el viejo entraba en pánico (única diferencia de textos con él)
//   - «leer buyer_data de <intake_id>: »
//   - «leer la indicación del cliente de <intake_id>: » — nunca cita la nota
//   - «leer tenant_variables de <tenant>: »
//   - «re-serializar el payload completo: »
//   - «leer integración de <tenant>: »
//   - «tenant <tenant> ya no tiene integración webhook habilitada» — sin fila,
//     apagada, con events_adapter distinto de «webhook» o sin endpoint
//   - «leer secreto de <tenant>: »
//   - «tenant <tenant> no tiene secreto de firma configurado»
//   - «construir request: »
//   - «POST: » — no hubo respuesta (red, DNS, plazo vencido)
//   - «respuesta <código> del puente» — cualquier código fuera de 200–299
//
// El orden de las lecturas es buyer_data, customer_note, variables y destino: el
// motivo que queda es el del PRIMER paso que falla. Ningún motivo lleva el
// secreto, la firma ni el cuerpo de la respuesta.
//
// El intento que acaba de fallar es Attempts+1 (Attempts es "intentos ya
// consumidos ANTES de este"). Si ese número llega a MaxAttempts, la fila se cierra
// con Store.MarkWebhookDead, se cuenta «dead» y queda un ERROR «webhook worker:
// entrega DEAD (reintentos agotados)» (con `outbox_id`, `tenant`, `attempts` y
// `reason`). Si no, Store.MarkWebhookFailed la reprograma para reloj + backoff y
// se cuenta «failed».
//
// El backoff es D-042.4 literal: 30s × 2^(intento−1), con tope de 1h, y un jitter
// de ±20 % (factor en [0.800, 1.199]) que DISPERSA de verdad: su fuente es
// crypto/rand y no el reloj, cuya resolución (1 µs en darwin/arm64) lo colapsaba a
// dos valores. Sin fuente de azar, el backoff va sin jitter antes que no ir.
//
// # El claim perdido
//
// Si al cerrar la fila (delivered, failed o dead) el almacén devuelve ErrClaimLost
// —envuelto: se reconoce con errors.Is—, NO es un fallo de entrega: la fila tiene
// otro dueño que la va a resolver, y este worker solo tiene que apartarse. Se
// cuenta «claim_lost» (y nada más), y se avisa en WARN, no en ERROR: «webhook
// worker: el claim expiró antes de cerrar la entrega; la resolverá quien la
// reclamó después» (con `outbox_id`, `tenant`, `transicion` —«delivered», «failed»
// o «dead»— y `lease`). Es WARN porque tiene una consecuencia visible para el
// cliente: si pasa DESPUÉS de un POST con 2xx, el puente ya recibió la entrega y
// la va a recibir otra vez (at-least-once, el receptor debe deduplicar por
// intake_id + revision_no — contrato wapp-crm-v1 §3).
//
// Cualquier otro error al cerrar la fila no cuenta nada y deja un ERROR «webhook
// worker: marcar delivered», «webhook worker: marcar failed» o «webhook worker:
// marcar dead» (con `error` y `outbox_id`). La fila queda en `delivering` y la
// recoge el rescate cuando venza su lease.
//
// # La parada no es un error (D-F6-7)
//
// Contexto cancelado → Run vuelve SIN loguear a ERROR, y SIN dar por fallido lo
// que la parada cortó. Con ctx ya cancelado:
//
//   - un fallo del almacén —el rescate, el reclamo o cualquiera de los tres
//     cierres— no se registra en ERROR: no es una avería, es el proceso apagándose;
//   - una entrega que no llegó a buen fin (el POST cortado, una lectura abortada)
//     NO cuenta como intento: no se llama a MarkWebhookFailed ni a MarkWebhookDead
//     y no se toca la métrica;
//   - las filas del lote que aún no se habían empezado no se intentan.
//
// En los tres casos la fila queda en `delivering` con su claim y la rescata el
// lease. (El worker viejo sí registraba esos fallos —worker.go:209, :225, :283,
// :450 y :463—, y un proceso que se para no puede prometer «cero ERROR».) Con el
// contexto vivo, esos mismos fallos SÍ van a ERROR, como arriba.
func (w *Worker) Run(ctx context.Context) {
	w.recoverOrphans(ctx)

	poll := time.NewTicker(w.cfg.PollInterval)
	defer poll.Stop()
	reclaim := time.NewTicker(w.cfg.ClaimLease)
	defer reclaim.Stop()

	w.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("webhook worker: apagando (contexto cancelado)")
			return
		case <-reclaim.C:
			w.recoverOrphans(ctx)
		case <-poll.C:
			w.pollOnce(ctx)
		}
	}
}

// record llama a onRecord si está inyectado (nil-safe, patrón de todo el
// paquete metrics).
func (w *Worker) record(status string) {
	if w.onRecord != nil {
		w.onRecord(status)
	}
}

// recoverOrphans devuelve a pending las entregas cuyo claim venció. Se avisa a
// nivel WARN y no INFO a propósito: en régimen normal esto no rescata nada, así
// que una línea aquí significa que un worker murió a medias o que el lease se
// quedó corto — las dos cosas que alguien querría ver en el log.
func (w *Worker) recoverOrphans(ctx context.Context) {
	n, err := w.store.RecoverOrphanDeliveries(ctx, w.cfg.ClaimLease)
	if err != nil {
		w.logStoreError(ctx, "webhook worker: rescatar entregas con el claim vencido", "error", err)
		return
	}
	if n > 0 {
		w.log.Warn("webhook worker: entregas con el claim vencido devueltas a pending",
			"count", n, "lease", w.cfg.ClaimLease)
	}
}

// pollOnce reclama un lote y entrega cada fila de forma SECUENCIAL: D-042.4 no
// pide concurrencia dentro de un lote (el paralelismo real, si hiciera falta,
// vendría de correr varias réplicas del proceso — para eso está SKIP LOCKED, no
// para goroutines dentro de un mismo poll).
//
// Si el contexto se cancela a mitad de lote, las filas que quedan NO se intentan
// (D-F6-7): siguen en `delivering` con su claim y las rescata el lease. Empezar
// una entrega con el proceso apagándose solo podría acabar en un POST cortado.
func (w *Worker) pollOnce(ctx context.Context) {
	batch, err := w.store.ClaimWebhookBatch(ctx, w.cfg.BatchSize)
	if err != nil {
		w.logStoreError(ctx, "webhook worker: reclamar lote", "error", err)
		return
	}
	for _, item := range batch {
		if ctx.Err() != nil {
			return
		}
		w.deliver(ctx, item)
	}
}
