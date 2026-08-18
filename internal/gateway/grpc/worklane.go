package gatewaygrpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// El carril de trabajo del gateway (Plan 050 · Ola 1 · ADR-0040). El bucle Recv
// del stream CloudLink deja de hacer el trabajo pesado inline y lo SUELTA aquí:
// una cola propia POR SESIÓN, servida por una goroutine propia, de modo que un
// receipt lento de la sesión A no bloquee el heartbeat de la sesión B.
//
// Por qué una cola de slice + sync.Cond y NO un `chan job` (design.md §2,
// D-050.1): un canal guarda COPIAS POR VALOR, así que el job ya encolado no se
// puede alcanzar ni mutar — y la coalescencia de heartbeats (D-050.4) es
// exactamente una sustitución EN SITIO del job pendiente, conservando su
// posición en la cola. Sobre un canal sería imposible; sobre un slice bajo mutex
// es trivial, igual que el freno con tope de REQ-050.4.
//
// Las cuatro reglas de concurrencia que esta implementación NO relaja:
//  1. Un solo mutex (lane.mu) cubre TODO: items, hbAt, sealing y la mutación de
//     run. Mutar el run de un job que el worker podría estar leyendo sin ese
//     mutex es una data race.
//  2. El worker hace «pop + limpiar hbAt» en el MISMO tramo crítico. Soltar el
//     mutex en medio perdería en silencio una sustitución que llegue justo ahí.
//  3. Cola llena y sin heartbeat coalescible ⇒ se espera en notFull. FRENAR, no
//     descartar y no crecer sin techo (REQ-050.4: el backpressure nativo de
//     HTTP/2 llega hasta el Edge; una cola infinita lo anula en silencio y una
//     que descarta convierte una degradación visible en pérdida muda).
//  4. Cierre en DOS TIEMPOS (seal → drain), nunca un close a secas: seal deja de
//     aceptar submit —devolviendo error, no encolando—; después se drena con
//     presupuesto. El jobOffline es el ÚNICO que ignora el `sealing` de su cola,
//     y la exención llega SOLO hasta ahí: NO le exime del `done` que el worker se
//     pone a sí mismo justo antes de morir, y que enqueue comprueba PRIMERO —para
//     él también—. Sobre una cola cuyo worker ya murió, un jobOffline rebota con
//     errLaneSealed igual que cualquier otro job. Por eso closeStream lo encola
//     ANTES de sellar y no después (ver connect.go).
//
// ⚠️ Corregido el 2026-08-18: esta línea decía «Esta pieza todavía NO está
// cableada a Connect: la conexión es de T1.6-T1.11». **Ya lo está**: Connect
// construye su carril (`newWorkLane` en connect.go) y `route` le suelta el
// trabajo. El comentario se quedó atrás cuando T1.6-T1.11 se implementaron, y un
// comentario que dice «esto no está conectado» sobre código que sí lo está es
// exactamente el patrón de fallo que este repo ya ha pagado dos veces.

// jobKind clasifica la unidad de trabajo. Solo jobHeartbeat se coalesce
// (D-050.4); receipts, auth, diagnósticos, logout y offline no se coalescen ni
// se descartan jamás. jobOffline es el MarkOffline del cierre (D-050.2): se
// encola como ÚLTIMO job de su sesión para que no adelante a un SaveHealth en
// vuelo y deje la flota mostrando «online» un Edge que ya se fue.
//
// ⚠️ MATIZ AÑADIDO EL 2026-08-18 (Plan 050 · Ola 1): que el jobOffline sea el
// ÚLTIMO job de su sesión es una garantía POR CONSTRUCCIÓN, no POR MECANISMO.
// Nada en este fichero impide encolar algo detrás de él: la exención del
// `sealing` que tiene le permitiría, de hecho, entrar y salir cuando quisiera.
// Lo que lo sostiene vive en connect.go: `route` tiene un ÚNICO llamante en
// producción —el bucle `Recv` de Connect— y `closeStream` encola el jobOffline
// cuando ese bucle YA RETORNÓ, así que no queda nadie que pueda encolar después.
// Un segundo llamante de `route` (una goroutine de push, un reintento, un test
// que lo cablee en producción) rompe la garantía en silencio. Ver el invariante
// escrito sobre `route` en connect.go antes de añadir uno.
type jobKind uint8

const (
	jobHeartbeat jobKind = iota
	jobReceipt
	jobAuth
	jobDiagnostics
	jobOffline
	// jobLogout es el markLoggedOut del Plan 020 · T3: el Edge anunció que
	// WhatsApp cerró el device y la sesión pasa a ZOMBIE. Nace el 2026-08-18 como
	// tipo PROPIO —antes viajaba como jobHeartbeat— y está EXENTO de coalescencia:
	// solo jobHeartbeat se coalesce, y esa regla no se toca. Ver la explicación
	// completa (y el enunciado que sustituye) en submitHeartbeat, connect.go.
	//
	// 🔴 Nunca se coalesce ni se descarta, pero SÍ frena con la cola llena y SÍ
	// respeta el `sealing`: la única exención de sellado es la del jobOffline. Es
	// correcto — el logout lo encola el bucle `Recv` con el stream VIVO, así que
	// nunca se encuentra un carril sellado; si alguna vez lo hiciera, submitJob lo
	// grita (no hay pérdida muda).
	jobLogout
)

// String da el nombre estable del tipo de job para los logs (el drenaje
// abandonado tiene que decir CUÁNTOS jobs quedan sin drenar y DE QUÉ TIPO).
//
// ⚠️ Corregido el 2026-08-18: este comentario decía «cuántos jobs PERDIÓ». Es la
// misma mentira que ya se corrigió en drain — los jobs que el drenaje abandona
// NO se pierden, se DIFIEREN: su worker sigue vivo y acaba ejecutándolos. Lo que
// se abandona es la espera, no el trabajo.
func (k jobKind) String() string {
	switch k {
	case jobHeartbeat:
		return "heartbeat"
	case jobReceipt:
		return "receipt"
	case jobAuth:
		return "auth"
	case jobDiagnostics:
		return "diagnostics"
	case jobOffline:
		return "offline"
	case jobLogout:
		return "logout"
	default:
		return "desconocido"
	}
}

// noPendingHeartbeat es el valor de sessQueue.hbAt cuando no hay ningún
// heartbeat pendiente al que sustituir.
const noPendingHeartbeat = -1

var (
	// errLaneSealed lo devuelve submit cuando el carril ya está sellado: el
	// trabajo NO se encoló. Es deliberadamente ruidoso — el fallo que este plan
	// viene a evitar es justamente la pérdida muda.
	errLaneSealed = errors.New("carril: sellado, el job no se encoló")
	// errNilRun protege contra el submit sin trabajo, que reventaría dentro del
	// worker (en otra goroutine y sin contexto para diagnosticarlo).
	errNilRun = errors.New("carril: submit sin función que ejecutar")
)

// job es una unidad de trabajo. Se guarda SIEMPRE por puntero: la coalescencia
// muta run en sitio sobre el job que ya está en la cola.
type job struct {
	kind jobKind
	run  func(ctx context.Context)
}

// sessQueue es la cola FIFO de UNA sesión. Todos sus campos viven bajo
// workLane.mu (regla 1).
type sessQueue struct {
	// items es la cola FIFO. El heartbeat coalescible se muta EN SITIO, sin
	// reordenar: retrasarlo al final lo dejaría detrás de receipts que llegaron
	// después.
	items []*job
	// hbAt es el índice del heartbeat pendiente dentro de items, o
	// noPendingHeartbeat si no hay ninguno.
	hbAt int
	// sealing marca la cola cerrada a nuevos submit, salvo el jobOffline (regla 4).
	sealing bool
	// done lo pone el worker justo antes de morir, con el mutex tomado. Sin él,
	// un submit tardío se encolaría en una cola sin worker: pérdida muda, otra
	// vez. Con él, ese submit devuelve errLaneSealed.
	done bool
}

// workLane es el carril de un stream CloudLink: una cola y una goroutine por
// sesión, con tope, presupuesto por job y cierre en dos tiempos.
type workLane struct {
	// mu cubre TODO: perSess, y de cada sessQueue sus items, hbAt, sealing, done
	// y la mutación de job.run (regla 1).
	mu sync.Mutex
	// notEmpty es donde esperan los workers. Se difunde (Broadcast) porque el
	// cond es único y cada worker sirve a SU sesión: quien despierta re-comprueba
	// su propia cola.
	notEmpty *sync.Cond
	// notFull es donde espera el bucle Recv cuando la cola de la sesión está
	// llena. Es el freno de REQ-050.4.
	notFull  *sync.Cond
	perSess  map[string]*sessQueue
	queueCap int           // WAPP_GATEWAY_WORK_QUEUE, POR SESIÓN (default 64, T1.3)
	budget   time.Duration // WAPP_GATEWAY_WORK_TIMEOUT (default 5s, T1.3)
	// base es el contexto padre de cada job: context.WithoutCancel(streamCtx).
	// El molde ya existe y está probado en onStreamClosed (connect.go), y existe
	// por la misma razón: persistir que un Edge se cayó importa PRECISAMENTE
	// cuando su stream ya murió.
	base context.Context
	log  logger.Logger
	wg   sync.WaitGroup
	// sealed es el sellado a nivel de carril. No basta con el sealing de cada
	// sessQueue: una sesión que aún no tiene cola no tiene dónde llevar la marca,
	// y un submit posterior le crearía una cola —y un worker— después del cierre.
	sealed bool
}

// newWorkLane construye el carril. base debe ser context.WithoutCancel(streamCtx)
// (D-050.5); queueCap es el tope POR SESIÓN y budget el presupuesto de CADA job.
//
// Los valores no positivos se materializan en vez de aceptarse: un queueCap 0
// bloquearía para siempre y un budget 0 entregaría a cada job un contexto ya
// vencido. Un log nil degrada a silencio (el llamante real, Connect, siempre
// pasa el del Server).
func newWorkLane(base context.Context, queueCap int, budget time.Duration, log logger.Logger) *workLane { //nolint:contextcheck // el Background() de abajo es el DEFAULT de un base nil, no un descarte del padre: el llamante real pasa context.WithoutCancel(streamCtx) y la no-herencia es justo lo que pide D-050.5.
	if base == nil {
		base = context.Background()
	}
	if queueCap < 1 {
		queueCap = 1
	}
	if budget <= 0 {
		budget = offlinePersistTimeout
	}
	if log == nil {
		log = logger.New(logger.WithWriter(io.Discard))
	}
	l := &workLane{
		perSess:  make(map[string]*sessQueue),
		queueCap: queueCap,
		budget:   budget,
		base:     base,
		log:      log,
	}
	l.notEmpty = sync.NewCond(&l.mu)
	l.notFull = sync.NewCond(&l.mu)
	return l
}

// submit encola run en la cola de sessionID y devuelve error si el carril ya no
// lo acepta. NUNCA descarta trabajo en silencio: si la cola está llena, BLOQUEA
// al llamante hasta que haya sitio (REQ-050.4).
//
// La única excepción a «una cosa encolada es una cosa que se ejecuta» es la
// coalescencia de heartbeats (D-050.4), y es una sustitución, no un descarte: el
// job pendiente se reemplaza por el nuevo conservando su posición.
func (l *workLane) submit(sessionID string, kind jobKind, run func(ctx context.Context)) error {
	if run == nil {
		return errNilRun
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	q, err := l.queueFor(sessionID, kind)
	if err != nil {
		return err
	}
	return l.enqueue(q, kind, run)
}

// queueFor devuelve la cola de la sesión, creándola —y arrancando SU goroutine—
// la primera vez que esa sesión trae trabajo. Se llama con l.mu tomado.
func (l *workLane) queueFor(sessionID string, kind jobKind) (*sessQueue, error) {
	if q, ok := l.perSess[sessionID]; ok {
		return q, nil
	}
	if l.sealed && kind != jobOffline {
		return nil, errLaneSealed
	}
	q := &sessQueue{hbAt: noPendingHeartbeat, sealing: l.sealed}
	l.perSess[sessionID] = q
	l.wg.Add(1)
	go l.worker(sessionID, q)
	return q, nil
}

// enqueue aplica las tres políticas de la cola —sellado, coalescencia y freno—
// en ese orden. Se llama con l.mu tomado; notFull.Wait lo suelta mientras espera.
func (l *workLane) enqueue(q *sessQueue, kind jobKind, run func(ctx context.Context)) error {
	for {
		if q.done || (q.sealing && kind != jobOffline) {
			return errLaneSealed
		}
		// D-050.4: si ya hay un heartbeat sin procesar, el nuevo lo SUSTITUYE en
		// su misma posición. Descartar el nuevo —el error natural— persistiría
		// salud del pasado sin que nada lo delate.
		if kind == jobHeartbeat && q.hbAt != noPendingHeartbeat {
			q.items[q.hbAt].run = run
			return nil
		}
		// El jobOffline no frena: es el último job del cierre y su orden es la
		// garantía de D-050.2. Perderlo (o refusarlo) dejaría la flota mostrando
		// «online» un Edge que ya se fue, que es peor que un item de más en una
		// cola que ya se está drenando. Crecimiento acotado: uno por sesión.
		if kind == jobOffline || len(q.items) < l.queueCap {
			break
		}
		// REQ-050.4: llena y sin sitio donde sustituir ⇒ FRENA. Esto incluye a un
		// heartbeat cuyo hueco ya fue consumido: ahí REQ-050.4 manda sobre
		// REQ-050.5, y está decidido de antemano para que no se improvise.
		l.notFull.Wait()
	}

	q.items = append(q.items, &job{kind: kind, run: run})
	if kind == jobHeartbeat {
		q.hbAt = len(q.items) - 1
	}
	l.notEmpty.Broadcast()
	return nil
}

// worker es LA goroutine de una sesión: saca los jobs de su cola de uno en uno y
// los corre en serie. Muere cuando su cola queda sellada y vacía.
func (l *workLane) worker(sessionID string, q *sessQueue) {
	defer l.wg.Done()
	for {
		j, ok := l.next(q)
		if !ok {
			return
		}
		l.runJob(sessionID, j)
	}
}

// next espera y extrae el siguiente job. Devuelve ok=false cuando la cola quedó
// sellada y vacía, que es la orden de morir del worker.
//
// El «pop + limpiar hbAt» ocurre en UN SOLO tramo crítico (regla 2): si el
// mutex se soltara entre sacar el heartbeat y limpiar su índice, una sustitución
// que llegase en esa ventana escribiría sobre un job ya consumido y se perdería
// en silencio — el fallo exacto que D-050.4 dice temer.
func (l *workLane) next(q *sessQueue) (*job, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for len(q.items) == 0 && !q.sealing {
		l.notEmpty.Wait()
	}
	if len(q.items) == 0 {
		q.done = true
		return nil, false
	}

	j := q.items[0]
	q.items[0] = nil // no retener el closure tras sacarlo de la cola
	q.items = q.items[1:]
	switch {
	case q.hbAt == 0:
		q.hbAt = noPendingHeartbeat
	case q.hbAt > 0:
		q.hbAt--
	}
	l.notFull.Broadcast()
	return j, true
}

// runJob le da a cada job su propio reloj sobre un contexto que SOBREVIVE al
// stream (D-050.5). Usar el ctx del stream a secas cancelaría el trabajo en
// vuelo justo cuando más importa: persistir que un Edge se fue.
//
// Un job que se pasa del presupuesto DEJA RASTRO. En particular, un heartbeat
// que se rinde no debe darse por renovado: sin este aviso, un lease que no se
// renovó parecería renovado.
func (l *workLane) runJob(sessionID string, j *job) {
	ctx, cancel := context.WithTimeout(l.base, l.budget)
	defer cancel()

	j.run(ctx)

	if err := ctx.Err(); err != nil {
		l.log.Warn("carril: el job se rindió, no terminó dentro de su presupuesto",
			"session_id", sessionID, "kind", j.kind.String(),
			"budget", l.budget, "error", err)
	}
}

// seal cierra la puerta a nuevos submit (salvo el jobOffline) y despierta a
// quien esté esperando. Es el PRIMER tiempo del cierre; el segundo es drain.
// Llamarlo dos veces es inocuo.
func (l *workLane) seal() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.sealed {
		return
	}
	l.sealed = true
	for _, q := range l.perSess {
		q.sealing = true
	}
	// notEmpty: los workers ociosos despiertan para morir.
	// notFull: quien estuviera frenado despierta para recibir errLaneSealed en
	// vez de quedarse colgado de un carril que ya nadie va a vaciar.
	l.notEmpty.Broadcast()
	l.notFull.Broadcast()
}

// drain espera a que los workers terminen, con presupuesto. Es el SEGUNDO tiempo
// del cierre y asume seal() previo: sin sellar, los workers no mueren nunca y el
// presupuesto se agota siempre.
//
// Lo que no quepa en el presupuesto se abandona con un aviso que dice CUÁNTOS
// jobs quedan sin drenar y DE QUÉ TIPO: un abandono silencioso aquí sería el
// mismo defecto que este plan viene a arreglar, con otra cara.
//
// ⚠️ «Sin drenar» NO es «perdidos»: los workers abandonados siguen vivos hasta
// vaciar su cola, así que esos jobs se ejecutan igual —DIFERIDOS, después de que
// drain haya retornado—. Lo que se pierde es la ESPERA, no el trabajo. La fuga
// es acotada (muere con la cola), no indefinida.
func (l *workLane) drain(budget time.Duration) {
	if budget <= 0 {
		budget = l.budget
	}

	finished := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(finished)
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case <-finished:
		return
	case <-timer.C:
	}

	total, porTipo := l.pending()
	l.log.Warn("carril: drenaje abandonado por presupuesto; los jobs que quedan en cola se ejecutarán DIFERIDOS, no se pierden",
		"jobs_sin_drenar", total, "por_tipo", porTipo, "budget", budget)
}

// pending cuenta lo que queda ENCOLADO, en total y por tipo de job.
//
// ⚠️ No incluye el job EN VUELO: el worker ya lo sacó de items antes de correrlo,
// así que este recuento dice lo que queda por EMPEZAR, no lo que queda por
// terminar. Un carril con un job atascado y la cola vacía cuenta 0.
func (l *workLane) pending() (int, map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	total := 0
	porTipo := make(map[string]int, len(l.perSess))
	for _, q := range l.perSess {
		for _, j := range q.items {
			total++
			porTipo[j.kind.String()]++
		}
	}
	return total, porTipo
}
