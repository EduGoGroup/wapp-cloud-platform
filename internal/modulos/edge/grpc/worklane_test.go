package grpc

// El carril de trabajo (R-G3), parte 1: los dobles de los tres ficheros de test del carril,
// el orden y el aislamiento entre sesiones, los valores materializados, el contexto de cada
// job y su presupuesto. La coalescencia y el freno están en worklane_coalesce_test.go; el
// cierre en dos tiempos, en worklane_close_test.go. worklane.go no tiene exportados: su test
// nace con el verde y prueba el carril POR CONDUCTA, con canales (sin Sleep, T-16).

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// laneRecorder anota QUÉ jobs se ejecutaron, EN QUÉ ORDEN y CUÁNTOS a la vez. Sin orden no
// hay serialización, sin la lista no se ve un descarte, y sin el pico no se distingue un
// carril por sesión de una goroutine suelta por job.
type laneRecorder struct {
	mu       sync.Mutex
	seen     []string
	inFlight int
	peak     int
}

// task fabrica el trabajo de un job: se anota al entrar, cede el procesador (para que dos
// jobs que NO estuvieran serializados se solapen) y se descuenta al salir.
func (r *laneRecorder) task(name string) func(context.Context) {
	return func(context.Context) {
		r.mu.Lock()
		r.seen = append(r.seen, name)
		r.inFlight++
		r.peak = max(r.peak, r.inFlight)
		r.mu.Unlock()

		runtime.Gosched()

		r.mu.Lock()
		r.inFlight--
		r.mu.Unlock()
	}
}

func (r *laneRecorder) order() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprint(r.seen)
}

func (r *laneRecorder) peakInFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// mustSubmit encola y aborta el test si el carril rechaza el job.
func mustSubmit(t *testing.T, lane *workLane, sessionID string, kind jobKind, run func(ctx context.Context)) {
	t.Helper()
	if err := lane.submit(sessionID, kind, run); err != nil {
		t.Fatalf("submit(%s, %s) = %v", sessionID, kind, err)
	}
}

// plugLane tapa el worker de la sesión con un job que no termina hasta que se llama a la
// función devuelta. Vuelve cuando el worker YA está dentro del tapón: a partir de ahí, todo
// lo que se encole en esa sesión se queda en la cola. Soltar dos veces es inocuo.
func plugLane(t *testing.T, lane *workLane, sessionID string, kind jobKind) (release func()) {
	t.Helper()
	entered := make(chan struct{})
	gate := make(chan struct{})
	mustSubmit(t, lane, sessionID, kind, func(context.Context) {
		close(entered)
		<-gate
	})
	await(t, entered, "el worker de "+sessionID+" entra en el tapón")
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release
}

// waitWorkers espera a que mueran todos los workers del carril, o falla.
func waitWorkers(t *testing.T, lane *workLane) {
	t.Helper()
	dead := make(chan struct{})
	go func() {
		lane.wg.Wait()
		close(dead)
	}()
	await(t, dead, "los workers del carril mueren tras el sellado")
}

// closeLane sella el carril y espera a que sus workers vacíen las colas y mueran.
func closeLane(t *testing.T, lane *workLane) {
	t.Helper()
	lane.seal()
	waitWorkers(t, lane)
}

// queueLen dice cuántos jobs quedan ENCOLADOS (sin el que está en vuelo) en la sesión.
func queueLen(lane *workLane, sessionID string) int {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	q, ok := lane.perSess[sessionID]
	if !ok {
		return -1
	}
	return len(q.items)
}

// R-G3: dentro de UNA sesión el carril es serial. Los jobs no se solapan NUNCA —el pico de
// jobs simultáneos es 1— y salen en el orden en que llegaron. Con una goroutine por job, el
// pico subiría y el orden dejaría de estar garantizado.
func TestWorkLaneSameSessionRunsSeriallyInOrder(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 64, time.Minute, quietLog())
	rec := &laneRecorder{}

	want := make([]string, 0, 40)
	for i := range 40 {
		name := fmt.Sprintf("j%02d", i)
		want = append(want, name)
		mustSubmit(t, lane, "s-1", jobKind(i%4)+jobReceipt, rec.task(name)) // receipt, auth, diagnostics, offline
	}
	closeLane(t, lane)

	if got := rec.order(); got != fmt.Sprint(want) {
		t.Fatalf("orden de ejecución = %s, se esperaba el de llegada %v", got, want)
	}
	if peak := rec.peakInFlight(); peak != 1 {
		t.Fatalf("pico de jobs simultáneos de UNA sesión = %d, se esperaba 1", peak)
	}
}

// R-G3: el aislamiento entre sesiones es la razón de ser del carril. Con el job de s-a
// bloqueado, los de las demás sesiones corren; con una cola única por stream no entrarían.
func TestWorkLaneDifferentSessionsRunInParallel(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, time.Minute, quietLog())
	release := plugLane(t, lane, "s-a", jobReceipt)

	for _, sid := range []string{"s-b", "s-c", "s-d"} {
		ran := make(chan struct{})
		mustSubmit(t, lane, sid, jobHeartbeat, func(context.Context) { close(ran) })
		await(t, ran, "el job de "+sid+" corre con s-a bloqueada")
	}

	release()
	closeLane(t, lane)
}

// R-G3: un submit sin trabajo se rechaza en el llamante. Encolarlo reventaría dentro del
// worker, en otra goroutine y sin nada que permita saber de dónde vino.
func TestWorkLaneSubmitWithoutRunIsAnError(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, time.Minute, quietLog())

	if err := lane.submit("s-1", jobReceipt, nil); !errors.Is(err, errNilRun) {
		t.Fatalf("submit(nil) = %v, se esperaba errNilRun", err)
	}
	if n := queueLen(lane, "s-1"); n != -1 {
		t.Fatalf("el submit rechazado dejó una cola para la sesión (%d jobs)", n)
	}

	// El carril sigue sirviendo.
	ran := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) { close(ran) })
	await(t, ran, "el job válido posterior corre")
	closeLane(t, lane)
}

// El carril nunca queda sin tope ni sin reloj: un queueCap 0 bloquearía para siempre al
// primer submit y un budget 0 entregaría a cada job un contexto ya vencido.
func TestNewWorkLaneMaterializesItsValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		queueCap   int
		budget     time.Duration
		wantCap    int
		wantBudget time.Duration
	}{
		{name: "zero", queueCap: 0, budget: 0, wantCap: 1, wantBudget: 5 * time.Second},
		{name: "negative", queueCap: -7, budget: -time.Second, wantCap: 1, wantBudget: 5 * time.Second},
		{name: "smallest valid", queueCap: 1, budget: 1, wantCap: 1, wantBudget: 1},
		{name: "kept", queueCap: 9, budget: time.Hour, wantCap: 9, wantBudget: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lane := newWorkLane(context.Background(), tc.queueCap, tc.budget, quietLog())
			if lane.queueCap != tc.wantCap {
				t.Errorf("queueCap = %d, se esperaba %d", lane.queueCap, tc.wantCap)
			}
			if lane.budget != tc.wantBudget {
				t.Errorf("budget = %v, se esperaba %v", lane.budget, tc.wantBudget)
			}
			closeLane(t, lane)
		})
	}
}

// Sin contexto base y sin logger el carril funciona igual: el job corre con un contexto vivo
// y con el reloj por defecto (5 s), y lo que tenga que avisar se pierde en silencio.
func TestNewWorkLaneWithNilBaseAndLogStillWorks(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(nil, 0, 0, nil) //nolint:staticcheck // el base nil es justo el caso que se afirma

	type seen struct {
		err      error
		left     time.Duration
		deadline bool
	}
	got := make(chan seen, 1)
	mustSubmit(t, lane, "s-1", jobReceipt, func(ctx context.Context) {
		dl, ok := ctx.Deadline()
		got <- seen{err: ctx.Err(), left: time.Until(dl), deadline: ok}
	})
	s := await(t, got, "el job corre sobre el carril por defecto")
	if s.err != nil || !s.deadline {
		t.Fatalf("ctx del job = (err=%v, deadline=%v), se esperaba vivo y con reloj", s.err, s.deadline)
	}
	if s.left <= 2500*time.Millisecond || s.left > 5*time.Second {
		t.Errorf("al job le quedaban %v, se esperaba el presupuesto por defecto de 5s", s.left)
	}

	// Un job que agota su reloj avisa por el logger: con uno nil no puede reventar.
	short := newWorkLane(context.Background(), 1, time.Nanosecond, nil)
	expired := make(chan struct{})
	mustSubmit(t, short, "s-1", jobReceipt, func(ctx context.Context) {
		<-ctx.Done()
		close(expired)
	})
	await(t, expired, "el job agota su presupuesto")

	closeLane(t, lane)
	closeLane(t, short)
}

type laneCtxKey struct{}

// D-050.5: el job corre sobre un contexto que SOBREVIVE al stream. El stream se cancela ANTES
// de encolar (que es cuando importa: marcar offline a un Edge cuyo stream ya no existe) y el
// job hereda los valores de la base, ve un ctx vivo y trae el reloj del presupuesto.
func TestWorkLaneJobRunsOnAContextThatOutlivesTheStream(t *testing.T) {
	t.Parallel()
	streamCtx, cancelStream := context.WithCancel(context.Background())
	base := context.WithValue(context.WithoutCancel(streamCtx), laneCtxKey{}, "alive")
	lane := newWorkLane(base, 4, time.Hour, quietLog())
	cancelStream()

	type seen struct {
		value any
		err   error
		left  time.Duration
	}
	got := make(chan seen, 1)
	mustSubmit(t, lane, "s-1", jobOffline, func(ctx context.Context) {
		dl, _ := ctx.Deadline()
		got <- seen{value: ctx.Value(laneCtxKey{}), err: ctx.Err(), left: time.Until(dl)}
	})

	s := await(t, got, "el job offline corre")
	if s.value != "alive" {
		t.Errorf("el ctx del job no desciende de la base del carril (valor heredado = %v)", s.value)
	}
	if s.err != nil {
		t.Errorf("el job corrió con el ctx MUERTO (%v): el carril no puede atarse al stream", s.err)
	}
	if s.left <= 30*time.Minute || s.left > time.Hour {
		t.Errorf("al job le quedaban %v, se esperaba el presupuesto de 1h", s.left)
	}
	closeLane(t, lane)
}

// R-G3: el presupuesto por job existe y se cumple. Un job que dura más recibe un ctx vencido
// con DeadlineExceeded, el carril NO se queda ahí —el siguiente job de la misma sesión corre—
// y el que se rindió DEJA RASTRO: sin ese aviso, un heartbeat que no renovó el lease pasaría
// por renovado. El plazo es el contrato, y por eso el job espera a SU ctx, no a un reloj.
func TestWorkLaneBudgetCancelsTheJobAndTheLaneGoesOn(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	lane := newWorkLane(context.Background(), 4, time.Millisecond, log)

	slow := make(chan error, 1)
	mustSubmit(t, lane, "s-slow", jobHeartbeat, func(ctx context.Context) {
		<-ctx.Done()
		slow <- ctx.Err()
	})
	next := make(chan struct{})
	mustSubmit(t, lane, "s-slow", jobReceipt, func(context.Context) { close(next) })

	if err := await(t, slow, "el job lento ve vencer su ctx"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx del job que se pasó del presupuesto = %v, se esperaba DeadlineExceeded", err)
	}
	await(t, next, "el carril sigue con el job siguiente tras el que se rindió")
	closeLane(t, lane)

	for _, want := range []string{
		"carril: el job se rindió, no terminó dentro de su presupuesto",
		"session_id=s-slow", "kind=heartbeat", "budget=1ms",
	} {
		if !logs.contains(want) {
			t.Errorf("el rastro del job que se rindió no contiene %q: %s", want, logs.String())
		}
	}
}

// El aviso es SOLO para el job que se rinde: uno que termina dentro de su presupuesto no
// escribe nada.
func TestWorkLaneJobWithinBudgetLeavesNoTrace(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	lane := newWorkLane(context.Background(), 4, time.Hour, log)

	mustSubmit(t, lane, "s-1", jobHeartbeat, func(context.Context) {})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {})
	closeLane(t, lane)

	if out := logs.String(); out != "" {
		t.Fatalf("un carril sano escribió en el log: %s", out)
	}
}

// Los nombres de los tipos de job son texto de log estable.
func TestJobKindNames(t *testing.T) {
	t.Parallel()
	cases := map[jobKind]string{
		jobHeartbeat:   "heartbeat",
		jobReceipt:     "receipt",
		jobAuth:        "auth",
		jobDiagnostics: "diagnostics",
		jobOffline:     "offline",
		jobLogout:      "logout",
		jobKind(200):   "desconocido",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("jobKind(%d).String() = %q, se esperaba %q", uint8(kind), got, want)
		}
	}
}
