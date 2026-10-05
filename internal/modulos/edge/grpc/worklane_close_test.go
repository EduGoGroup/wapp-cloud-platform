package grpc

// El carril de trabajo (R-G3), parte 3: el cierre en DOS TIEMPOS (seal → drain). Los dobles
// están en worklane_test.go.

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// R-G3: seal cierra la puerta —devolviendo error, no encolando—, también a una sesión que
// todavía no tenía cola; el jobOffline es el ÚNICO que pasa mientras su worker siga vivo, y
// sellar dos veces es inocuo.
func TestWorkLaneSealClosesTheDoor(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobAuth)

	lane.seal()
	lane.seal()

	for _, kind := range []jobKind{jobHeartbeat, jobReceipt, jobAuth, jobDiagnostics, jobLogout} {
		if err := lane.submit("s-1", kind, rec.task("late-"+kind.String())); !errors.Is(err, errLaneSealed) {
			t.Errorf("submit(%s) tras seal = %v, se esperaba errLaneSealed", kind, err)
		}
		if err := lane.submit("s-new", kind, rec.task("new-"+kind.String())); !errors.Is(err, errLaneSealed) {
			t.Errorf("submit(%s) de una sesión NUEVA tras seal = %v, se esperaba errLaneSealed", kind, err)
		}
	}
	if n := queueLen(lane, "s-new"); n != -1 {
		t.Errorf("el sellado dejó crear una cola (y un worker) para una sesión nueva")
	}

	// El worker de s-1 sigue ocupado con el tapón: su cola no está muerta y el offline entra.
	mustSubmit(t, lane, "s-1", jobOffline, rec.task("offline"))

	release()
	waitWorkers(t, lane)

	if got := rec.order(); got != "[offline]" {
		t.Fatalf("ejecutados tras el sellado = %s, se esperaba solo [offline]", got)
	}
}

// El jobOffline de una sesión que aún no tenía cola también entra tras el sellado: se le crea
// la cola ya sellada, su worker lo corre y muere.
func TestWorkLaneOfflineForANewSessionAfterSealStillRuns(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, time.Minute, quietLog())
	lane.seal()

	ran := make(chan struct{})
	mustSubmit(t, lane, "s-new", jobOffline, func(context.Context) { close(ran) })
	await(t, ran, "el offline de la sesión nueva corre")
	waitWorkers(t, lane)

	if err := lane.submit("s-new", jobReceipt, func(context.Context) {}); !errors.Is(err, errLaneSealed) {
		t.Fatalf("submit tras el offline = %v, se esperaba errLaneSealed", err)
	}
}

// El jobOffline ignora el sellado, NO la muerte de su worker: sobre una cola cuyo worker ya
// murió rebota como cualquier otro. Encolarlo ahí sería pérdida muda (nadie sirve esa cola), y
// por eso closeStream encola el MarkOffline ANTES de sellar.
func TestWorkLaneOfflineBouncesIfItsWorkerAlreadyDied(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 4, time.Minute, quietLog())

	ran := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) { close(ran) })
	await(t, ran, "el job previo corre")

	closeLane(t, lane) // cola vacía y sellada: el worker ocioso despierta y muere

	if err := lane.submit("s-1", jobOffline, func(context.Context) {}); !errors.Is(err, errLaneSealed) {
		t.Fatalf("submit(offline) sobre una cola cuyo worker YA murió = %v, se esperaba errLaneSealed", err)
	}
	if n := queueLen(lane, "s-1"); n != 0 {
		t.Fatalf("el offline rebotado quedó en la cola (%d jobs): nadie la sirve", n)
	}
}

// seal despierta a quien estuviera frenado por la cola llena para que reciba errLaneSealed, en
// vez de dejarlo colgado de un carril que ya nadie tiene por qué vaciar. Su job no se ejecuta.
func TestWorkLaneSealWakesABrakedSubmitter(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 1, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobReceipt)
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1")) // cola llena

	var unused atomic.Bool
	braked := brakedSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"), &unused)

	lane.seal()
	// Con el worker TODAVÍA tapado: quien lo despierta tiene que ser el sellado.
	if got := await(t, braked, "el sellado despierta al submit frenado"); !errors.Is(got.err, errLaneSealed) {
		t.Fatalf("submit frenado tras seal = %v, se esperaba errLaneSealed", got.err)
	}

	release()
	waitWorkers(t, lane)
	if got := rec.order(); got != "[r1]" {
		t.Fatalf("ejecutados = %s, se esperaba [r1]: el job rechazado no corre", got)
	}
}

// R-G3: drain ESPERA al trabajo. El job en vuelo y lo que quedaba encolado terminan antes de
// que drain vuelva, y no hay aviso de abandono.
func TestWorkLaneDrainWaitsForTheWork(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	lane := newWorkLane(context.Background(), 4, time.Minute, log)

	entered := make(chan struct{})
	gate := make(chan struct{})
	var finished atomic.Int32
	mustSubmit(t, lane, "s-1", jobAuth, func(context.Context) {
		close(entered)
		<-gate
		for range 200 { // sigue «trabajando» un rato después de que drain haya empezado
			runtime.Gosched()
		}
		finished.Add(1)
	})
	await(t, entered, "el job en vuelo arranca")
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) { finished.Add(1) })
	mustSubmit(t, lane, "s-1", jobOffline, func(context.Context) { finished.Add(1) })

	lane.seal()
	close(gate)
	lane.drain(watchdog)

	if n := finished.Load(); n != 3 {
		t.Fatalf("drain volvió con %d de 3 jobs terminados: no esperó al trabajo", n)
	}
	if logs.contains("drenaje abandonado") {
		t.Fatalf("drain avisó de abandono sin haberse agotado: %s", logs.String())
	}
}

// R-G3: si el presupuesto del drenaje se agota, lo que queda se abandona CON AVISO —cuántos
// y de qué tipo—. El recuento es de lo ENCOLADO: el job en vuelo no cuenta. Y «sin drenar» no
// es «perdidos»: el worker abandonado sigue vivo y acaba ejecutándolos.
func TestWorkLaneDrainGivesUpSayingWhatIsLeft(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	lane := newWorkLane(context.Background(), 8, time.Minute, log)
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobAuth)
	releaseOther := plugLane(t, lane, "s-2", jobAuth)

	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb"))
	mustSubmit(t, lane, "s-2", jobOffline, rec.task("offline"))

	lane.seal()
	lane.drain(time.Nanosecond) // el plazo es el contrato: con los workers tapados, vence

	for _, want := range []string{
		"carril: drenaje abandonado por presupuesto; los jobs que quedan en cola se ejecutarán DIFERIDOS, no se pierden",
		"jobs_sin_drenar=4", "receipt:2", "heartbeat:1", "offline:1", "budget=1ns",
	} {
		if !logs.contains(want) {
			t.Errorf("el aviso del drenaje abandonado no contiene %q: %s", want, logs.String())
		}
	}
	if logs.contains("auth:") {
		t.Errorf("el recuento incluyó los jobs EN VUELO: %s", logs.String())
	}

	release()
	releaseOther()
	waitWorkers(t, lane)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seen) != 4 {
		t.Fatalf("tras el abandono corrieron %v, se esperaban los 4 jobs DIFERIDOS", rec.seen)
	}
}

// Un presupuesto de drenaje no positivo no es «no esperes»: cae al presupuesto del carril.
func TestWorkLaneDrainWithoutBudgetUsesTheLaneBudget(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	lane := newWorkLane(context.Background(), 4, 3*time.Millisecond, log)

	entered := make(chan struct{})
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	mustSubmit(t, lane, "s-1", jobOffline, func(context.Context) {
		close(entered)
		<-gate // sordo a su ctx a propósito: quien se rinde aquí es el drenaje
	})
	await(t, entered, "el job en vuelo arranca")

	lane.seal()
	lane.drain(0)

	if !logs.contains("drenaje abandonado") || !logs.contains("jobs_sin_drenar=0") || !logs.contains("budget=3ms") {
		t.Fatalf("drain(0) no se rindió con el presupuesto del carril (3ms): %s", logs.String())
	}
}
