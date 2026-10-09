package pipeline_test

// pipeline_loop_drain_test.go — los dos drenajes de pipeline_loop.go: Drain (por tic, con
// el backoff como freno) y DrainAwake (por flanco a READY, con el conjunto de vistos como
// freno). Trozo de pipeline_loop_test.go (E-13).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
)

var edgeOne = pipeline.Slot{TenantID: tenantID, EdgeID: "edge-1"}

// TestDrain_ProcessesTheWholeBacklogAndCountsIt: un backlog no tarda `n × cadencia`.
func TestDrain_ProcessesTheWholeBacklogAndCountsIt(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	ids := []string{r.seed("job-a"), r.seed("job-b"), r.seed("job-c")}

	if n := r.drain(context.Background()); n != 3 {
		t.Fatalf("Drain procesó %d jobs, se esperaban 3", n)
	}
	for _, id := range ids {
		if got := r.row(t, id).Status; got != intake.StatusDone {
			t.Errorf("%s quedó %q, se esperaba done", id, got)
		}
	}
	if n := r.drain(context.Background()); n != 0 {
		t.Fatalf("con la cola vacía Drain procesó %d", n)
	}
}

// TestDrain_AStumblingJobIsCountedOnceAndTheBackoffEndsTheLoop: la terminación de Drain
// descansa ENTERA en el backoff. El job que tropieza cuenta una vez, sale con la marca en
// el futuro y no vuelve a reclamarse en la misma pasada; vencida la marca, vuelve solo.
func TestDrain_AStumblingJobIsCountedOnceAndTheBackoffEndsTheLoop(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	id := r.seed("")

	if n := r.drain(context.Background()); n != 1 {
		t.Fatalf("Drain procesó %d, se esperaba 1", n)
	}
	if n := r.drain(context.Background()); n != 0 {
		t.Fatalf("el job castigado NO debe ser reclamable antes de su marca; se reclamaron %d", n)
	}
	if got := r.p2.count(); got != 1 {
		t.Fatalf("P2 se llamó %d veces; el backoff no está conteniendo nada", got)
	}
	if got := r.store.closesOf(opRelease); len(got) != 0 {
		t.Fatalf("un tropiezo se devolvió con Release (%d): sin castigo, Drain giraría para siempre", len(got))
	}

	r.clock.Advance(2 * pipeline.DefaultBackoffBase)
	if n := r.drain(context.Background()); n != 1 {
		t.Fatalf("vencida la marca el job debe volver a reclamarse; se reclamaron %d", n)
	}
	if got := r.row(t, id).Attempts; got != 2 {
		t.Fatalf("attempts = %d tras dos tropiezos, se esperaban 2", got)
	}
}

// TestDrain_APoisonedJobDoesNotBlockTheNextOne: el job sin sobre —el MÁS VIEJO, el que
// ganaría el orden del reclamo para siempre— muere, y el siguiente del MISMO tenant sale
// adelante en la MISMA pasada. No hace falta un candado para bloquear una cola: basta con
// no salir nunca de ella.
func TestDrain_APoisonedJobDoesNotBlockTheNextOne(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	poisoned := r.healthyRow("poisoned")
	poisoned.SourceText = intake.SourceText{}
	poisoned.CreatedAt = r.clock.Now().Add(-time.Hour)
	r.mem.Seed(poisoned)
	healthy := r.seed("healthy")

	if n := r.drain(context.Background()); n != 2 {
		t.Fatalf("Drain procesó %d jobs, se esperaban 2", n)
	}
	if row := r.row(t, "poisoned"); row.Status != intake.StatusFailed || row.Attempts != 0 {
		t.Fatalf("el envenenado quedó (%q, attempts=%d), se esperaba failed sin un solo reintento", row.Status, row.Attempts)
	}
	if got := r.row(t, healthy).Status; got != intake.StatusDone {
		t.Fatalf("el job SANO del mismo tenant quedó %q, se esperaba done", got)
	}
	if got := r.p2.count(); got != 1 {
		t.Fatalf("solo el job sano llama al modelo: P2 se llamó %d veces", got)
	}
}

// TestDrain_CancelledContext_ClaimsNothing: con el ctx ya muerto no se reclama ni un job.
func TestDrain_CancelledContext_ClaimsNothing(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if n := r.drain(ctx); n != 0 {
		t.Fatalf("Drain procesó %d jobs con el ctx cancelado", n)
	}
	if got := r.mem.Claims(); got != 0 {
		t.Fatalf("se reclamó %d veces con el ctx cancelado", got)
	}
	if got := r.row(t, id).Status; got != intake.StatusPending {
		t.Fatalf("el job quedó %q, se esperaba pending", got)
	}
}

// TestDrain_StopsAsSoonAsTheContextDies: el ctx muere durante el primer job; ese job
// llega a su desenlace y el SEGUNDO no se reclama.
func TestDrain_StopsAsSoonAsTheContextDies(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	first := r.seed("job-a")
	r.clock.Advance(time.Second)
	second := r.seed("job-b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.draft.around = func(intake.ClaimedJob) func() { return cancel }

	if n := r.drain(ctx); n != 1 {
		t.Fatalf("Drain procesó %d jobs, se esperaba 1", n)
	}
	if got := r.row(t, first).Status; got != intake.StatusDone {
		t.Errorf("el job en curso quedó %q; su desenlace se escribe aunque el ctx muera", got)
	}
	if got := r.row(t, second).Status; got != intake.StatusPending {
		t.Errorf("el segundo quedó %q; con el ctx muerto no se reclama", got)
	}
	if got := r.mem.Claims(); got != 1 {
		t.Errorf("se reclamó %d veces, se esperaba 1", got)
	}
}

// TestDrain_ClaimFailure_LogsAtErrorAndReturnsWhatItProcessed: la base deja de contestar
// a mitad del backlog.
func TestDrain_ClaimFailure_LogsAtErrorAndReturnsWhatItProcessed(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.seed("job-a")
	second := r.seed("job-b")
	down := errors.New("la base no contesta")
	r.draft.around = func(intake.ClaimedJob) func() {
		return func() { r.store.breakOp(opClaim, down) }
	}

	if n := r.drain(context.Background()); n != 1 {
		t.Fatalf("Drain procesó %d jobs antes del fallo, se esperaba 1", n)
	}
	line := r.log.one(t, "ERROR", "pipeline: no se pudo reclamar trabajo")
	if got, ok := line.fields["error"].(error); !ok || !errors.Is(got, down) {
		t.Errorf("la línea lleva error=%v, se esperaba el del store", line.fields["error"])
	}
	if got := r.row(t, second).Status; got != intake.StatusPending {
		t.Errorf("el job no reclamado quedó %q, se esperaba pending", got)
	}
}

// TestDrainAwake_IgnoresTheBackoffAndTakesOnlyThatTenant: el reclamo del flanco no mira la
// marca, y es por TENANT (`intake_jobs` no tiene `edge_id`): el job de otro tenant no se
// toca. El `edge_id` viaja al log.
func TestDrainAwake_IgnoresTheBackoffAndTakesOnlyThatTenant(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	far := r.clock.Now().Add(10 * time.Minute)
	mine := r.healthyRow("mine")
	mine.NextAttemptAt = far
	r.mem.Seed(mine)
	other := r.healthyRow("other")
	other.Key.TenantID = "tenant-2"
	other.NextAttemptAt = far
	r.mem.Seed(other)

	if n := r.drainAwake(context.Background()); n != 1 {
		t.Fatalf("DrainAwake procesó %d jobs, se esperaba 1", n)
	}
	if got := r.row(t, "mine").Status; got != intake.StatusDone {
		t.Errorf("el job del tenant del flanco quedó %q, se esperaba done", got)
	}
	if got := r.row(t, "other").Status; got != intake.StatusPending {
		t.Errorf("el job de OTRO tenant quedó %q; el flanco no es suyo", got)
	}
	for _, tenant := range r.store.awakeTenants() {
		if tenant != tenantID {
			t.Errorf("se reclamó por evento para %q, se esperaba solo %q", tenant, tenantID)
		}
	}
	if got := r.mem.Claims(); got != 2 {
		t.Errorf("se reclamó %d veces, se esperaban 2 (el job y la cola vacía)", got)
	}
	line := r.log.one(t, "INFO", "se reanudan sus jobs sin esperar al backoff")
	if line.fields["tenant_id"] != tenantID || line.fields["edge_id"] != "edge-1" {
		t.Errorf("el aviso lleva %v; se esperaban el tenant y el Edge del flanco", line.fields)
	}
}

// TestDrainAwake_ProcessesEachJobOncePerEdge: el job falla SIEMPRE y vuelve a la cola
// castigado, que es justo lo que el reclamo del flanco ignora. Sin el conjunto de vistos
// consumiría su techo entero de intentos en un solo flanco y moriría. Con él: UNA pasada,
// UN intento cobrado, y la segunda vez que sale se devuelve SIN castigo.
func TestDrainAwake_ProcessesEachJobOncePerEdge(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	id := r.seed("")

	if n := r.drainAwake(context.Background()); n != 1 {
		t.Fatalf("el flanco procesó %d jobs; con uno solo en la cola tenía que ser 1", n)
	}
	if got := r.p2.count(); got != 1 {
		t.Fatalf("P2 se llamó %d veces en UN flanco; el evento vale una pasada por job", got)
	}
	row := r.row(t, id)
	if row.Status != intake.StatusPending || row.Attempts != 1 {
		t.Fatalf("el job quedó (%q, attempts=%d), se esperaba pending con UN intento cobrado", row.Status, row.Attempts)
	}

	retries, releases := r.store.closesOf(opRetry), r.store.closesOf(opRelease)
	if len(retries) != 1 || len(releases) != 1 {
		t.Fatalf("hubo %d Retry y %d Release, se esperaba 1 y 1:\n%s", len(retries), len(releases), r.log.dump())
	}
	if !row.NextAttemptAt.Equal(retries[0].mark) {
		t.Errorf("la marca es %s y su tropiezo la dejó en %s: la devolución sin castigo no la mueve", row.NextAttemptAt, retries[0].mark)
	}
	line := r.log.one(t, "INFO", "job devuelto a la cola SIN castigo")
	if line.fields["motivo"] != "ya procesado en este mismo flanco a READY" || line.fields["job_id"] != id {
		t.Errorf("la devolución lleva %v; se esperaban su job y su motivo", line.fields)
	}
}

// TestDrainAwake_TheRepeatedJobEndsTheEdge: el job repetido cierra el flanco aunque quede
// otro detrás… que ya se procesó: los dos pasan UNA vez.
func TestDrainAwake_TheRepeatedJobEndsTheEdge(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
	r.seed("job-a")
	r.clock.Advance(time.Second)
	r.seed("job-b")

	if n := r.drainAwake(context.Background()); n != 2 {
		t.Fatalf("el flanco procesó %d jobs, se esperaban 2", n)
	}
	if got := r.p2.count(); got != 2 {
		t.Fatalf("P2 se llamó %d veces; cada job pasa UNA vez por flanco", got)
	}
	for _, id := range []string{"job-a", "job-b"} {
		if row := r.row(t, id); row.Status != intake.StatusPending || row.Attempts != 1 {
			t.Errorf("%s quedó (%q, attempts=%d), se esperaba pending con un intento", id, row.Status, row.Attempts)
		}
	}
}

// TestDrainAwake_ClaimFailure_LogsAtErrorWithTheEdge: el fallo del reclamo por evento se
// dice con la dirección del flanco.
func TestDrainAwake_ClaimFailure_LogsAtErrorWithTheEdge(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.seed("")
	down := errors.New("la base no contesta")
	r.store.breakOp(opAwake, down)

	if n := r.drainAwake(context.Background()); n != 0 {
		t.Fatalf("DrainAwake procesó %d jobs con el reclamo roto", n)
	}
	line := r.log.one(t, "ERROR", "pipeline: no se pudo reclamar trabajo tras el flanco a READY")
	line.requireKeys(t, "tenant_id", "edge_id", "error")
	if line.fields["edge_id"] != "edge-1" {
		t.Errorf("edge_id = %v, se esperaba edge-1", line.fields["edge_id"])
	}
}

// TestDrainAwake_CancelledContext_ClaimsNothing: con el ctx muerto el flanco no reclama.
func TestDrainAwake_CancelledContext_ClaimsNothing(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.seed("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if n := r.drainAwake(ctx); n != 0 {
		t.Fatalf("DrainAwake procesó %d jobs con el ctx cancelado", n)
	}
	if got := r.mem.Claims(); got != 0 {
		t.Fatalf("se reclamó %d veces con el ctx cancelado", got)
	}
}

// TestDrainAwake_ReleaseThatFailsOrDoesNotApply_IsSaid: la devolución sin castigo tampoco
// es muda.
func TestDrainAwake_ReleaseThatFailsOrDoesNotApply_IsSaid(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*rig)
		level   string
		message string
	}{
		{"the write fails", func(r *rig) { r.store.breakOp(opRelease, errors.New("la base no contesta")) },
			"ERROR", "no se pudo devolver el job a la cola; queda en processing"},
		{"the write does not apply", func(r *rig) { r.store.loseOp(opRelease) },
			"INFO", "la devolución no aplicó (el job ya no estaba en processing)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			r.p2.script = []step{{err: errors.New("el Edge no contesta")}}
			r.seed("")
			c.prepare(r)

			r.drainAwake(context.Background())

			r.log.one(t, c.level, c.message).requireKeys(t, "job_id", "motivo")
			if got := r.log.find("job devuelto a la cola SIN castigo"); len(got) != 0 {
				t.Errorf("la devolución no ocurrió y aun así se anunció:\n%s", r.log.dump())
			}
		})
	}
}
