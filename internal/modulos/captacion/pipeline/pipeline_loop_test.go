//go:build pendiente

package pipeline_test

// pipeline_loop_test.go — el contrato del bucle: Run, Wake y RunOnce. Los dos drenajes
// (Drain, DrainAwake) están en pipeline_loop_drain_test.go.
//
// Ningún test de aquí duerme: el ticker es falso (un tic no vuelve hasta que Run lo
// recoge, y como Run es UNA goroutine, que recoja el tic siguiente prueba que el drenaje
// anterior terminó) y los flancos se esperan con `eventually`.

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
)

// settle espera a que Run haya terminado lo que estuviera drenando: dos tics seguidos.
func settle(t *testing.T, r *rig) {
	t.Helper()
	r.ticker.tick(t)
	r.ticker.tick(t)
}

// TestRun_DrainsAtStartupWithoutWaitingForATick: un job que ya estaba listo no espera a la
// primera cadencia. Aquí no se entrega NINGÚN tic: solo el drenaje del arranque puede
// haberlo terminado.
func TestRun_DrainsAtStartupWithoutWaitingForATick(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.start(t)
	eventually(t, "el job terminado por el drenaje del arranque", func() bool {
		return r.row(t, id).Status == intake.StatusDone
	})
}

// TestRun_EachTick_DrainsTheQueue: lo que llega después del arranque lo recoge el tic.
func TestRun_EachTick_DrainsTheQueue(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.start(t)
	r.ticker.tick(t) // el drenaje del arranque ya pasó, con la cola vacía
	id := r.seed("")
	if got := r.row(t, id).Status; got != intake.StatusPending {
		t.Fatalf("sin tic, el job sembrado tras el arranque está %q; se esperaba pending", got)
	}
	settle(t, r)
	if row := r.row(t, id); row.Status != intake.StatusDone {
		t.Fatalf("tras el tic el job quedó %q (%s), se esperaba done", row.Status, r.log.dump())
	}
}

// TestRun_Wake_ResumesTheTenantJobsWithoutWaitingForTheirBackoff (D-044.43): el job tiene
// la marca a DIEZ MINUTOS; los tics no lo tocan, el flanco a READY sí, y la marca no se
// adelanta: el flanco la IGNORA, no la borra.
func TestRun_Wake_ResumesTheTenantJobsWithoutWaitingForTheirBackoff(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	row := r.healthyRow("job-asleep")
	far := r.clock.Now().Add(10 * time.Minute)
	row.NextAttemptAt = far
	r.mem.Seed(row)

	r.start(t)
	settle(t, r)
	if got := r.row(t, "job-asleep").Status; got != intake.StatusPending {
		t.Fatalf("el job está %q; su backoff no ha vencido y un tic no debía tocarlo", got)
	}
	if got := r.p2.count(); got != 0 {
		t.Fatalf("P2 se llamó %d veces antes del flanco", got)
	}

	r.w.Wake(tenantID, "edge-1")
	eventually(t, "el job reanudado por el flanco a READY", func() bool {
		return r.row(t, "job-asleep").Status == intake.StatusDone
	})

	if got := r.row(t, "job-asleep").NextAttemptAt; !got.Equal(far) {
		t.Errorf("la marca del backoff pasó de %s a %s; el flanco la ignora, no la mueve", far, got)
	}
	line := r.log.one(t, "INFO", "se reanudan sus jobs sin esperar al backoff")
	if line.fields["tenant_id"] != tenantID || line.fields["edge_id"] != "edge-1" {
		t.Errorf("el aviso del flanco lleva %v; se esperaban el tenant y el Edge que lo dispararon", line.fields)
	}
}

// TestRun_ContextCancelled_LogsTheShutdownStopsTheTickerAndReturns: la parada ordenada.
func TestRun_ContextCancelled_LogsTheShutdownStopsTheTickerAndReturns(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	stop := r.start(t)
	r.ticker.tick(t)
	stop() // espera a que Run vuelva; si no vuelve, falla

	r.log.one(t, "INFO", "pipeline: worker apagando (contexto cancelado)")
	if cadences, stops := r.ticker.asked(); len(cadences) != 1 || stops != 1 {
		t.Errorf("el ticker se pidió %d veces y se paró %d; se esperaba 1 y 1", len(cadences), stops)
	}
	r.log.requireNoErrors(t)
}

// TestRun_ContextCancelled_ReturnsWithoutLoggingAtError (D-F9-10): la cancelación corta un
// reclamo a medias, el store devuelve «context canceled» y eso NO es una avería: ni una
// línea a ERROR. Es la única conducta que cambia frente al worker viejo.
func TestRun_ContextCancelled_ReturnsWithoutLoggingAtError(t *testing.T) {
	sites := []struct {
		name string
		op   string
		wake bool
	}{
		{"during the claim of a drain", opClaim, false},
		{"during the claim of a wake", opAwake, true},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.seed("")
			r.store.hangOn(s.op)
			stop := r.start(t)
			if s.wake {
				r.w.Wake(tenantID, "edge-1")
			}
			select {
			case <-r.store.blocked:
			case <-time.After(waitLimit):
				t.Fatalf("el worker no llegó al reclamo (%s) en %v", s.op, waitLimit)
			}
			stop() // cancela con el reclamo a medias y espera a que Run vuelva

			r.log.requireNoErrors(t)
			r.log.one(t, "INFO", "pipeline: worker apagando (contexto cancelado)")
		})
	}
}

// TestRun_ContextAlive_AClaimFailureGoesToErrorAndTheLoopGoesOn: el mismo fallo con el
// contexto VIVO sí es una avería, se dice en ERROR, y no para el bucle: el tic siguiente
// vuelve a preguntar.
func TestRun_ContextAlive_AClaimFailureGoesToErrorAndTheLoopGoesOn(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	down := errors.New("la base no contesta")
	r.store.breakOp(opClaim, down)
	r.start(t)
	r.ticker.tick(t) // el drenaje del arranque ya falló

	// Al menos la del arranque: el tic recién recogido puede haber dejado ya la suya.
	lines := r.log.find("pipeline: no se pudo reclamar trabajo")
	if len(lines) == 0 || lines[0].level != "ERROR" {
		t.Fatalf("se esperaba una línea a ERROR por el reclamo fallido:\n%s", r.log.dump())
	}
	if got, ok := lines[0].fields["error"].(error); !ok || !errors.Is(got, down) {
		t.Errorf("la línea lleva error=%v, se esperaba el del store", lines[0].fields["error"])
	}

	r.store.breakOp(opClaim, nil)
	id := r.seed("")
	settle(t, r)
	if row := r.row(t, id); row.Status != intake.StatusDone {
		t.Fatalf("reparada la base, el tic siguiente debía terminar el job; quedó %q", row.Status)
	}
}

// TestRun_DoesNotLeakGoroutines: Run no lanza goroutines propias. Hoy es una RED PARA EL
// FUTURO: el día que alguien paralelice el fan-out o meta un `go` en un desenlace, esto lo
// dice.
func TestRun_DoesNotLeakGoroutines(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.seed("")
	settled := func(target int) int {
		deadline := time.Now().Add(waitLimit)
		n := runtime.NumGoroutine()
		for n > target && time.Now().Before(deadline) {
			runtime.Gosched()
			n = runtime.NumGoroutine()
		}
		return n
	}
	before := settled(runtime.NumGoroutine())

	stop := r.start(t)
	settle(t, r) // el worker hizo trabajo REAL antes de apagarse
	if got := r.mem.Claims(); got < 2 {
		t.Fatalf("el worker reclamó %d veces; el test no llegó a ejercitarlo", got)
	}
	stop()

	if after := settled(before); after > before {
		t.Fatalf("quedaron %d goroutine(s) de más tras apagar el worker (antes %d, después %d)", after-before, before, after)
	}
}

// TestWake_NeverBlocks_AndDropsWhatDoesNotFit: el hook corre INLINE en el bucle Recv del
// gateway. Con el buzón lleno y NADIE consumiéndolo, Wake vuelve igual: caben 32 avisos y
// el resto se descarta diciéndolo en Debug. Los que cupieron esperan a que Run arranque.
func TestWake_NeverBlocks_AndDropsWhatDoesNotFit(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	const sent, fits = 40, 32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range sent {
			r.w.Wake(tenantID, "edge-1")
		}
	}()
	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("Wake bloqueó con el buzón lleno: pararía el bucle Recv del gateway")
	}

	dropped := r.log.find("aviso de Edge READY descartado (buzón lleno)")
	if len(dropped) != sent-fits {
		t.Fatalf("se descartaron %d avisos, se esperaban %d (caben %d)", len(dropped), sent-fits, fits)
	}
	if dropped[0].level != "DEBUG" || dropped[0].fields["tenant_id"] != tenantID || dropped[0].fields["edge_id"] != "edge-1" {
		t.Errorf("el descarte debe ir a DEBUG con su tenant y su Edge; salió %+v", dropped[0])
	}

	r.start(t)
	served := func() int { return len(r.log.find("se reanudan sus jobs sin esperar al backoff")) }
	eventually(t, "los avisos que cupieron, atendidos", func() bool { return served() >= fits })
	settle(t, r)
	if got := served(); got != fits {
		t.Fatalf("Run atendió %d avisos, se esperaban exactamente %d", got, fits)
	}
}

// TestWake_HalfAnAddress_IsDroppedSilently: una dirección a medias no entra en el buzón ni
// deja log.
func TestWake_HalfAnAddress_IsDroppedSilently(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.w.Wake(tenantID, "")
	r.w.Wake("", "edge-1")
	r.start(t)
	settle(t, r)
	if got := r.log.find("se reanudan sus jobs sin esperar al backoff"); len(got) != 0 {
		t.Errorf("una dirección a medias llegó a Run:\n%s", r.log.dump())
	}
	if got := r.log.at("DEBUG"); len(got) != 0 {
		t.Errorf("descartar una dirección a medias no deja log; hay %d líneas a DEBUG", len(got))
	}
	if got := r.store.awakeTenants(); len(got) != 0 {
		t.Errorf("se reclamó por evento para %v sin ningún flanco válido", got)
	}
}

// TestRunOnce_EmptyQueue_ReturnsFalseWithoutError: el estado normal del worker.
func TestRunOnce_EmptyQueue_ReturnsFalseWithoutError(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	if found, err := r.w.RunOnce(context.Background()); found || err != nil {
		t.Fatalf("RunOnce = (%v, %v) con la cola vacía, se esperaba (false, nil)", found, err)
	}
}

// TestRunOnce_OnlyClaimsWhatIsDue: un job castigado no se reclama antes de su marca (para
// eso está el flanco: ver DrainAwake); vencida, sí.
func TestRunOnce_OnlyClaimsWhatIsDue(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	row := r.healthyRow("")
	row.NextAttemptAt = r.clock.Now().Add(time.Minute)
	id := r.mem.Seed(row)

	if found, err := r.w.RunOnce(context.Background()); found || err != nil {
		t.Fatalf("RunOnce = (%v, %v) antes de la marca, se esperaba (false, nil)", found, err)
	}
	r.clock.Advance(time.Minute)
	if found, err := r.w.RunOnce(context.Background()); !found || err != nil {
		t.Fatalf("RunOnce = (%v, %v) vencida la marca, se esperaba (true, nil)", found, err)
	}
	if row := r.row(t, id); row.Status != intake.StatusDone {
		t.Fatalf("el job quedó %q, se esperaba done", row.Status)
	}
}

// TestRunOnce_ClaimError_IsReturnedAndNothingRuns: el ÚNICO error que sale es el del
// reclamo, tal cual, y RunOnce no lo loguea (lo hace quien drena).
func TestRunOnce_ClaimError_IsReturnedAndNothingRuns(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.seed("")
	down := errors.New("la base no contesta")
	r.store.breakOp(opClaim, down)

	found, err := r.w.RunOnce(context.Background())
	if found || !errors.Is(err, down) {
		t.Fatalf("RunOnce = (%v, %v), se esperaba (false, %v)", found, err, down)
	}
	if got := r.p2.count(); got != 0 {
		t.Errorf("P2 se llamó %d veces sin job reclamado", got)
	}
	r.log.requireNoErrors(t)
}

// TestRunOnce_AJobThatFails_IsNotAnErrorOfTheWorker: los fallos del job se resuelven
// dentro; por fuera sale (true, nil), o un job envenenado pararía el drenaje.
func TestRunOnce_AJobThatFails_IsNotAnErrorOfTheWorker(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	id := r.seed("")

	if found, err := r.w.RunOnce(context.Background()); !found || err != nil {
		t.Fatalf("RunOnce = (%v, %v) con una etapa caída, se esperaba (true, nil)", found, err)
	}
	if row := r.row(t, id); row.Status != intake.StatusPending || row.Attempts != 1 {
		t.Fatalf("el job quedó (%q, attempts=%d), se esperaba pending con el intento cobrado", row.Status, row.Attempts)
	}
}

// TestRunOnce_TakesOneJobAtATime: una vuelta, un job.
func TestRunOnce_TakesOneJobAtATime(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	first := r.seed("job-a")
	r.clock.Advance(time.Second)
	second := r.seed("job-b")

	if found, err := r.w.RunOnce(context.Background()); !found || err != nil {
		t.Fatalf("RunOnce = (%v, %v), se esperaba (true, nil)", found, err)
	}
	if got := r.row(t, first).Status; got != intake.StatusDone {
		t.Errorf("el más antiguo quedó %q, se esperaba done", got)
	}
	if got := r.row(t, second).Status; got != intake.StatusPending {
		t.Errorf("el segundo quedó %q; una vuelta toma UN job", got)
	}
	// El resto del backlog es de Drain y, con flanco, de DrainAwake.
	if n := r.w.Drain(context.Background()); n != 1 {
		t.Errorf("Drain procesó %d, quedaba 1", n)
	}
	if n := r.w.DrainAwake(context.Background(), pipeline.Slot{TenantID: tenantID, EdgeID: "edge-1"}); n != 0 {
		t.Errorf("DrainAwake procesó %d con la cola vacía", n)
	}
}
