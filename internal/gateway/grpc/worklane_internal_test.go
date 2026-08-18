package gatewaygrpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// laneLog da un logger mudo para los tests que no miran lo que se escribe.
func laneLog() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// laneLogBuf captura lo que escribe el logger desde la goroutine del worker, con
// su propio mutex para que -race no tenga nada que decir.
type laneLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *laneLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *laneLogBuf) contiene(sub string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buf.String(), sub)
}

func (b *laneLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// laneOrden anota QUÉ jobs se ejecutaron, EN QUÉ ORDEN y CUÁNTOS a la vez. Los
// tres datos son el objeto de la Ola 1: sin orden no hay serialización, sin la
// lista no se ve un descarte, y sin el pico no se distingue un carril por sesión
// de una goroutine suelta por job.
type laneOrden struct {
	mu      sync.Mutex
	visto   []string
	enVuelo int
	pico    int
}

func (o *laneOrden) entra(nombre string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.visto = append(o.visto, nombre)
	o.enVuelo++
	if o.enVuelo > o.pico {
		o.pico = o.enVuelo
	}
}

func (o *laneOrden) sale() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.enVuelo--
}

func (o *laneOrden) texto() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return fmt.Sprint(o.visto)
}

func (o *laneOrden) picoEnVuelo() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pico
}

// tarea fabrica el trabajo de un job: se anota al entrar, dura lo que se le pida
// y se descuenta al salir.
func (o *laneOrden) tarea(nombre string, dura time.Duration) func(ctx context.Context) {
	return func(context.Context) {
		o.entra(nombre)
		if dura > 0 {
			time.Sleep(dura)
		}
		o.sale()
	}
}

// mustSubmit encola y aborta el test si el carril rechaza el job. Solo se llama
// desde la goroutine del test (t.Fatalf desde otra sería ilegal).
func mustSubmit(t *testing.T, lane *workLane, sessionID string, kind jobKind, run func(ctx context.Context)) {
	t.Helper()
	if err := lane.submit(sessionID, kind, run); err != nil {
		t.Fatalf("submit(%s, %s): %v", sessionID, kind, err)
	}
}

// esperarN espera a que n jobs hayan entrado, o falla al agotarse el plazo.
func esperarN(t *testing.T, ch <-chan struct{}, n int, plazo time.Duration) {
	t.Helper()
	limite := time.After(plazo)
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-limite:
			t.Fatalf("solo %d de %d jobs entraron en %v", i, n, plazo)
		}
	}
}

// TestWorkLaneMismaSesionSerializaEnOrden (T1.2): dentro de UNA sesión el carril
// es serial. Dos jobs de la misma sesión no se solapan NUNCA —el pico de jobs
// simultáneos es 1— y salen en el orden en que llegaron. Si el carril arrancara
// una goroutine por job (el `go s.route(...)` que este plan viene a evitar), el
// pico sería 3 y el orden dejaría de estar garantizado.
func TestWorkLaneMismaSesionSerializaEnOrden(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	orden := &laneOrden{}

	for _, nombre := range []string{"j1", "j2", "j3"} {
		mustSubmit(t, lane, "s-1", jobReceipt, orden.tarea(nombre, 20*time.Millisecond))
	}
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.picoEnVuelo(); got != 1 {
		t.Fatalf("jobs simultáneos de la MISMA sesión = %d, quiero 1 (el carril es serial)", got)
	}
	if got := orden.texto(); got != "[j1 j2 j3]" {
		t.Fatalf("orden de ejecución = %s, quiero [j1 j2 j3]", got)
	}
}

// TestWorkLaneSesionesDistintasCorrenEnParalelo (T1.2): el aislamiento entre
// sesiones es la razón de ser del carril. Con una cola única por stream, el job
// bloqueado de s-a impediría entrar al de s-b y este test no terminaría nunca.
func TestWorkLaneSesionesDistintasCorrenEnParalelo(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, 3*time.Second, laneLog())

	dentro := make(chan struct{}, 2)
	soltar := make(chan struct{})
	bloquear := func(context.Context) {
		dentro <- struct{}{}
		<-soltar
	}

	mustSubmit(t, lane, "s-a", jobReceipt, bloquear)
	mustSubmit(t, lane, "s-b", jobReceipt, bloquear)

	// Las dos tienen que estar DENTRO a la vez.
	esperarN(t, dentro, 2, 2*time.Second)
	close(soltar)

	lane.seal()
	lane.drain(3 * time.Second)
}

// TestWorkLaneCoalesceHeartbeatsYSoloCorreElUltimo (T1.4) es la prueba POR
// MUTACIÓN del fallo silencioso de este plan. Con el worker ocupado se encolan
// HB1, HB2 y HB3 de la misma sesión; al soltarlo debe correr EXACTAMENTE UNO, y
// tiene que ser el ÚLTIMO.
//
// Por qué discrimina, que es lo único que hace válido a este test:
//   - Con la política correcta (el nuevo SUSTITUYE al pendiente): [hb3]. Verde.
//   - Con la política invertida («descartar el nuevo si ya hay uno pendiente»):
//     [hb1]. El recuento sigue siendo 1 y todo parece correcto —hay fila, hay
//     timestamp, nadie loguea nada—, pero la salud persistida es la del PASADO.
//     El test cae por la identidad, no por el recuento: por eso afirma cuál.
//   - Sin coalescencia ninguna: [hb1 hb2 hb3]. Cae por el recuento.
func TestWorkLaneCoalesceHeartbeatsYSoloCorreElUltimo(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	orden := &laneOrden{}

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon // el worker está ocupado: todo lo que llegue se queda en la cola

	for _, nombre := range []string{"hb1", "hb2", "hb3"} {
		mustSubmit(t, lane, "s-1", jobHeartbeat, orden.tarea(nombre, 0))
	}

	close(soltar)
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.texto(); got != "[hb3]" {
		t.Fatalf("heartbeats ejecutados = %s, quiero exactamente [hb3]; "+
			"[hb1] significa que se descarta el NUEVO y se persiste salud del pasado", got)
	}
}

// TestWorkLaneElHeartbeatCoalescidoConservaSuPosicion (T1.4): la sustitución es
// EN SITIO. El heartbeat nuevo ocupa la posición del viejo, no se reencola al
// final — reencolarlo dejaría la salud detrás de receipts que llegaron DESPUÉS.
// Con la implementación errónea el orden sería [receipt hb-nuevo].
func TestWorkLaneElHeartbeatCoalescidoConservaSuPosicion(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	orden := &laneOrden{}

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon

	mustSubmit(t, lane, "s-1", jobHeartbeat, orden.tarea("hb-viejo", 0))
	mustSubmit(t, lane, "s-1", jobReceipt, orden.tarea("receipt", 0))
	mustSubmit(t, lane, "s-1", jobHeartbeat, orden.tarea("hb-nuevo", 0))

	close(soltar)
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.texto(); got != "[hb-nuevo receipt]" {
		t.Fatalf("orden = %s, quiero [hb-nuevo receipt] (sustitución en sitio, sin reordenar)", got)
	}
}

// TestWorkLaneReceiptsNoSeCoalescen (T1.4): la coalescencia es SOLO del
// heartbeat. Tres receipts seguidos son tres hechos distintos y se ejecutan los
// tres; si alguien extendiera la sustitución a los demás tipos, este test cae.
func TestWorkLaneReceiptsNoSeCoalescen(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	orden := &laneOrden{}

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon

	for _, nombre := range []string{"r1", "r2", "r3"} {
		mustSubmit(t, lane, "s-1", jobReceipt, orden.tarea(nombre, 0))
	}

	close(soltar)
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.texto(); got != "[r1 r2 r3]" {
		t.Fatalf("receipts ejecutados = %s, quiero [r1 r2 r3]: un receipt no se coalesce jamás", got)
	}
}

// TestWorkLaneElLogoutNoSeCoalesceNiLoBorraUnLatidoPosterior (T1.7, decisión de
// Jhoan del 2026-08-18) es el candado del jobLogout.
//
// El fallo que cierra: hasta hoy markLoggedOut se encolaba como jobHeartbeat, así
// que un latido NORMAL posterior lo SUSTITUÍA en sitio (la coalescencia de
// D-050.4) y la sesión zombi ni se marcaba `loggedout` ni dejaba de renovar su
// lease — las dos cosas que el Plan 020 · T3 prohíbe. Y era el caso normal, no el
// raro: el Edge sigue latiendo después de anunciar el logout.
//
// Por qué discrimina, que es lo único que lo hace válido:
//   - Con jobLogout exento (lo correcto): [logout hb]. Verde.
//   - Si alguien lo vuelve coalescible con el heartbeat: el hb sustituiría al
//     logout y saldría [hb]. Cae por el recuento Y por la identidad.
//   - Si alguien lo reencolara al final en vez de en sitio: [hb logout]. Cae por
//     el orden, que es la otra mitad de la garantía (misma cola de la sesión ⇒
//     FIFO frente al trabajo en vuelo).
func TestWorkLaneElLogoutNoSeCoalesceNiLoBorraUnLatidoPosterior(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	orden := &laneOrden{}

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon // el worker está tapado: todo lo que llegue se queda en la cola

	mustSubmit(t, lane, "s-1", jobLogout, orden.tarea("logout", 0))
	mustSubmit(t, lane, "s-1", jobHeartbeat, orden.tarea("hb", 0))

	close(soltar)
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.texto(); got != "[logout hb]" {
		t.Fatalf("ejecutados = %s, quiero exactamente [logout hb]. "+
			"[hb] significa que el latido posterior BORRÓ el logout (jobLogout volvió a ser coalescible) "+
			"y la sesión zombi seguiría renovando su lease; [hb logout] significa que el logout perdió su "+
			"posición en la cola de su sesión", got)
	}
}

// TestWorkLaneColaLlenaFrenaSinPerderNiCrecer (T1.13) fija REQ-050.4. Con
// queueCap=2 y el worker parado, el TERCER submit tiene que quedarse bloqueado:
//   - si descartara (select … default), volvería enseguida y este test cae en el
//     primer select;
//   - si la cola creciera sin tope (o fuese un canal sin buffer acotado),
//     volvería enseguida igual y cae en el mismo sitio;
//   - y al soltar el worker no puede haberse perdido ninguno: se ejecutan los
//     tres, en orden.
func TestWorkLaneColaLlenaFrenaSinPerderNiCrecer(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 2, 2*time.Second, laneLog())
	orden := &laneOrden{}

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon // el worker ya sacó el tapón de la cola: la cola queda vacía

	mustSubmit(t, lane, "s-1", jobReceipt, orden.tarea("r1", 0))
	mustSubmit(t, lane, "s-1", jobReceipt, orden.tarea("r2", 0))

	// La cola está al tope (queueCap=2): el tercero TIENE que frenar al llamante.
	tercero := make(chan error, 1)
	go func() {
		tercero <- lane.submit("s-1", jobReceipt, orden.tarea("r3", 0))
	}()
	select {
	case err := <-tercero:
		t.Fatalf("el tercer submit volvió sin frenar (err=%v): la cola descartó o creció", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(soltar)
	if err := <-tercero; err != nil {
		t.Fatalf("el submit frenado acabó en error: %v", err)
	}
	lane.seal()
	lane.drain(3 * time.Second)

	if got := orden.texto(); got != "[r1 r2 r3]" {
		t.Fatalf("procesados = %s, quiero [r1 r2 r3]: frenar significa no perder nada", got)
	}
}

// laneCtxKey identifica el valor que el test cuelga de lane.base para comprobar
// que el ctx del job DESCIENDE de base y no de cualquier otro contexto.
type laneCtxKey struct{}

// TestWorkLaneElJobCorreSobreUnStreamYaMuerto (T1.5) afirma el EFECTO, no la
// invocación, que es donde este criterio se gana o se pierde: sin
// context.WithoutCancel, `context.WithTimeout(streamCtx, budget)` sobre un ctx
// ya cancelado TAMBIÉN invoca el closure — solo que con un ctx muerto. Un test
// que se conformara con «se ejecutó» no distinguiría una implementación de la
// otra.
//
// Aquí el stream se cancela ANTES de encolar (que es cuando esto importa: marcar
// offline a un Edge cuyo stream ya no existe) y el job afirma tres cosas:
// heredó el valor de lane.base, su ctx.Err() es nil, y su escritura LLEGA a
// destino. Con base = streamCtx a secas, la primera comprobación de ctx.Err()
// dispara y no se escribe nada.
func TestWorkLaneElJobCorreSobreUnStreamYaMuerto(t *testing.T) {
	t.Parallel()
	streamCtx, cancelStream := context.WithCancel(context.Background())
	base := context.WithValue(context.WithoutCancel(streamCtx), laneCtxKey{}, "vivo")
	lane := newWorkLane(base, 4, 2*time.Second, laneLog())

	cancelStream()

	heredado := make(chan any, 1)
	errJob := make(chan error, 1)
	destino := make(chan string, 1)

	mustSubmit(t, lane, "s-1", jobOffline, func(ctx context.Context) {
		heredado <- ctx.Value(laneCtxKey{})
		if err := ctx.Err(); err != nil {
			errJob <- err
			return
		}
		select {
		case destino <- "offline":
			errJob <- nil
		case <-ctx.Done():
			errJob <- ctx.Err()
		}
	})

	if v := <-heredado; v != "vivo" {
		t.Fatalf("el ctx del job no desciende de lane.base (valor heredado = %v)", v)
	}
	if err := <-errJob; err != nil {
		t.Fatalf("el job corrió con el ctx MUERTO (%v): falta context.WithoutCancel sobre el streamCtx", err)
	}
	select {
	case got := <-destino:
		if got != "offline" {
			t.Fatalf("escritura del job = %q, quiero %q", got, "offline")
		}
	default:
		t.Fatal("el job no llegó a escribir en destino: se invocó, pero el efecto no ocurrió")
	}

	lane.seal()
	lane.drain(3 * time.Second)
}

// TestWorkLaneElPresupuestoCancelaAlJobYElCarrilSigue (T1.5) es la otra mitad:
// el reloj existe y se cumple. Un job que dura más que el presupuesto recibe un
// ctx cancelado con DeadlineExceeded, el carril NO se queda ahí colgado —el
// siguiente job de la misma sesión corre— y el que se rindió DEJA RASTRO: sin
// ese aviso, un heartbeat que no renovó el lease pasaría por renovado.
func TestWorkLaneElPresupuestoCancelaAlJobYElCarrilSigue(t *testing.T) {
	t.Parallel()
	buf := &laneLogBuf{}
	lane := newWorkLane(context.Background(), 4, 60*time.Millisecond, logger.New(logger.WithWriter(buf)))

	lento := make(chan error, 1)
	mustSubmit(t, lane, "s-1", jobHeartbeat, func(ctx context.Context) {
		<-ctx.Done()
		lento <- ctx.Err()
	})
	siguiente := make(chan struct{}, 1)
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		siguiente <- struct{}{}
	})

	if err := <-lento; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx del job que se pasó del presupuesto = %v, quiero DeadlineExceeded", err)
	}
	select {
	case <-siguiente:
	case <-time.After(3 * time.Second):
		t.Fatal("el carril no siguió con el job siguiente tras el que se rindió")
	}

	lane.seal()
	lane.drain(3 * time.Second)

	if !buf.contiene("se rindió") {
		t.Fatalf("el job que agotó su presupuesto no dejó rastro en el log: %q", buf.String())
	}
}

// TestWorkLaneSealCierraLaPuertaYDrainEsperaAlTrabajo (T1.2) cubre el cierre en
// DOS TIEMPOS: seal deja de aceptar submit devolviendo error —no encolando y no
// haciendo panic, que es lo que daría un `close` a secas—; el jobOffline es el
// ÚNICO que ignora ese sellado; y drain espera de verdad al trabajo en vuelo.
//
// ⚠️ El caso que cubre es el del worker OCUPADO: el tapón sigue dentro de su job,
// así que la cola de s-1 NO está `done` y la exención del jobOffline basta. La
// mitad complementaria —cola vacía y worker ocioso, donde el `done` SÍ rebota al
// jobOffline— la prueba TestWorkLaneElJobOfflineRebotaSiSuWorkerYaMurio, y es la
// que explica por qué closeStream encola ANTES de sellar.
func TestWorkLaneSealCierraLaPuertaYDrainEsperaAlTrabajo(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, 2*time.Second, laneLog())

	arranco := make(chan struct{})
	soltar := make(chan struct{})
	var termino atomic.Bool
	mustSubmit(t, lane, "s-1", jobAuth, func(context.Context) {
		close(arranco)
		<-soltar
		termino.Store(true)
	})
	<-arranco

	lane.seal()

	if err := lane.submit("s-1", jobReceipt, func(context.Context) {}); !errors.Is(err, errLaneSealed) {
		t.Fatalf("submit tras seal = %v, quiero errLaneSealed", err)
	}
	if err := lane.submit("s-nueva", jobDiagnostics, func(context.Context) {}); !errors.Is(err, errLaneSealed) {
		t.Fatalf("submit de una sesión NUEVA tras seal = %v, quiero errLaneSealed", err)
	}

	// El jobOffline es la excepción al `sealing`: es el MarkOffline del cierre y
	// su orden es la garantía de que la flota no deje «online» un Edge que ya se
	// fue. Aquí entra porque el worker de s-1 sigue OCUPADO con el tapón —su cola
	// no está `done`—; sobre una cola ya vacía y con el worker muerto NO entraría.
	offlineCorrio := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobOffline, func(context.Context) { close(offlineCorrio) })

	close(soltar)
	lane.drain(3 * time.Second)

	if !termino.Load() {
		t.Fatal("drain no esperó al job que estaba en vuelo")
	}
	select {
	case <-offlineCorrio:
	default:
		t.Fatal("el jobOffline encolado tras el sellado no llegó a ejecutarse")
	}
}

// esperarWorkersMuertos espera, SIN dormir, a que todos los workers del carril
// hayan salido: se cuelga del mismo WaitGroup del que se cuelga drain. Es
// determinista en el caso bueno (vuelve en cuanto el último worker retorna) y
// acotado en el malo (falla con plazo en vez de colgar el paquete de tests).
func esperarWorkersMuertos(t *testing.T, lane *workLane, plazo time.Duration) {
	t.Helper()
	muertos := make(chan struct{})
	go func() {
		lane.wg.Wait()
		close(muertos)
	}()
	select {
	case <-muertos:
	case <-time.After(plazo):
		t.Fatalf("los workers del carril no murieron en %v tras seal()", plazo)
	}
}

// TestWorkLaneElJobOfflineRebotaSiSuWorkerYaMurio (T1.6 · T1.11) es la mitad que
// le falta a TestWorkLaneSealCierraLaPuertaYDrainEsperaAlTrabajo, y la que
// convierte el orden de closeStream en algo que un refactor no puede deshacer sin
// darse cuenta.
//
// La exención del jobOffline cubre `sessQueue.sealing`, pero NO cubre
// `sessQueue.done`, que el worker se pone a sí mismo justo antes de morir y que
// enqueue comprueba PRIMERO. Sobre una cola VACÍA con el worker OCIOSO —que es el
// caso NORMAL de cierre de un stream sano— seal() despierta al worker, el worker
// marca done y muere, y a partir de ahí el jobOffline rebota como cualquier otro.
//
// Por eso closeStream encola el MarkOffline ANTES del seal(). Si alguien
// invirtiera ese orden, el otro test seguiría verde —allí el worker está ocupado
// con el tapón, así que su cola nunca llega a done— y producción perdería el
// MarkOffline casi siempre. Este test es el que se pone rojo.
func TestWorkLaneElJobOfflineRebotaSiSuWorkerYaMurio(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, 2*time.Second, laneLog())

	// Cola creada y worker OCIOSO: el job entra, corre y deja la cola vacía; el
	// worker se queda esperando en notEmpty. Es el estado normal de una sesión al
	// caer su stream.
	corrio := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) { close(corrio) })
	<-corrio

	lane.seal()
	esperarWorkersMuertos(t, lane, 3*time.Second)

	if err := lane.submit("s-1", jobOffline, func(context.Context) {}); !errors.Is(err, errLaneSealed) {
		t.Fatalf("submit(jobOffline) sobre una cola cuyo worker YA murió = %v, quiero errLaneSealed. "+
			"El jobOffline ignora `sealing`, no `done`: si esto pasara, el MarkOffline se estaría "+
			"encolando en una cola sin nadie que la sirva, que es pérdida muda", err)
	}

	lane.drain(time.Second)
}

// TestWorkLaneDrainAbandonaDiciendoLoQueQuedaSinDrenar (T1.2): si el presupuesto
// del drenaje se agota, lo que queda se abandona CON AVISO —cuántos y de qué
// tipo—. Un drenaje que se rindiera en silencio sería el mismo defecto que este
// plan viene a arreglar, con otra cara.
//
// El aviso dice «sin drenar», no «perdidos», y la diferencia es real: el worker
// abandonado sigue vivo y acabará ejecutándolos. Lo que se abandona es la espera.
func TestWorkLaneDrainAbandonaDiciendoLoQueQuedaSinDrenar(t *testing.T) {
	t.Parallel()
	buf := &laneLogBuf{}
	lane := newWorkLane(context.Background(), 8, 5*time.Second, logger.New(logger.WithWriter(buf)))

	enTapon := make(chan struct{})
	soltar := make(chan struct{})
	defer close(soltar)

	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(enTapon)
		<-soltar
	})
	<-enTapon
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {})
	mustSubmit(t, lane, "s-1", jobHeartbeat, func(context.Context) {})

	lane.seal()
	lane.drain(50 * time.Millisecond)

	if !buf.contiene("jobs_sin_drenar=2") {
		t.Fatalf("el drenaje abandonado no dijo cuántos jobs quedaban sin drenar: %q", buf.String())
	}
}

// TestWorkLaneSubmitSinRunEsError (T1.2): un submit sin trabajo se rechaza en el
// llamante. Encolarlo reventaría dentro del worker, en otra goroutine y sin
// nada que permita saber de dónde vino.
func TestWorkLaneSubmitSinRunEsError(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 2, time.Second, laneLog())

	if err := lane.submit("s-1", jobDiagnostics, nil); !errors.Is(err, errNilRun) {
		t.Fatalf("submit sin run = %v, quiero errNilRun", err)
	}

	lane.seal()
	lane.drain(time.Second)
}

// TestWorkLaneMaterializaSusValores (T1.2): el carril nunca queda sin tope ni sin
// reloj. Un queueCap 0 bloquearía para siempre al primer submit y un budget 0
// entregaría a cada job un contexto ya vencido — dos formas de romperlo todo en
// silencio desde una variable de entorno mal puesta.
func TestWorkLaneMaterializaSusValores(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.TODO(), 0, 0, nil)

	if lane.queueCap < 1 {
		t.Fatalf("queueCap = %d, quiero >= 1", lane.queueCap)
	}
	if lane.budget <= 0 {
		t.Fatalf("budget = %v, quiero > 0", lane.budget)
	}
	if lane.base == nil || lane.log == nil {
		t.Fatal("base y log tienen que quedar materializados, no nil")
	}

	corrio := make(chan struct{}, 1)
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) { corrio <- struct{}{} })
	lane.seal()
	lane.drain(3 * time.Second)

	select {
	case <-corrio:
	default:
		t.Fatal("el carril con valores por defecto no ejecutó su job")
	}
}
