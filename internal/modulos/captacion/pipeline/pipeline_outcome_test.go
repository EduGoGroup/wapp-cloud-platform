package pipeline_test

// pipeline_outcome_test.go — los DESENLACES de un job tal como los promete RunOnce: el
// cronómetro por los dos caminos, el motivo de muerte, que ningún desenlace sea mudo y que
// todos se escriban aunque el worker se esté apagando. La política (qué causa, cuántos
// intentos, cuánto castigo) está en backoff_test.go.
//
// 🔴 EL AVISO QUE ESTOS TESTS CUSTODIAN: «no cuelgues una señal del desenlace FELIZ de una
// operación que, en el caso que te importa, FRACASA». La etapa que muere por timeout es la
// que más falta hace medir, y el `Retry` que afecta 0 filas es un NO-ERROR: sin línea
// propia, un job que se movió no dejaría ni rastro.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

var errEdgeDown = errors.New("el Edge no contesta")

// TestOutcome_EachStageThatRunsLeavesItsLineWithElapsed: una línea por etapa con `job_id`,
// `stage`, `elapsed_ms` e `intento`, y el `elapsed_ms` es el del reloj inyectado.
func TestOutcome_EachStageThatRunsLeavesItsLineWithElapsed(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.script = []step{{takes: 21 * time.Second}}
	r.p3.script = []step{{takes: 27 * time.Second}}
	r.p4.script = []step{{takes: 3 * time.Second}}
	r.match.script = []step{{takes: 4 * time.Millisecond}}
	r.draft.script = []step{{takes: 9 * time.Millisecond}}
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	want := map[string]int64{
		intake.StageP2: 21000, intake.StageP3: 27000, intake.StageP4: 3000,
		intake.StageMatch: 4, intake.StageDraft: 9,
	}
	lines := r.log.find("pipeline: etapa completada")
	if len(lines) != len(want) {
		t.Fatalf("se esperaba UNA línea por etapa (5), hay %d:\n%s", len(lines), r.log.dump())
	}
	for _, l := range lines {
		stage := fmt.Sprint(l.fields["stage"])
		if l.level != "INFO" || l.fields["job_id"] != id || l.fields["intento"] != 1 {
			t.Errorf("la línea de %q salió %+v; se esperaba un INFO con su job y el intento 1", stage, l)
		}
		if got := l.fields["elapsed_ms"]; got != want[stage] {
			t.Errorf("la etapa %q publicó elapsed_ms=%v, se esperaba %d", stage, got, want[stage])
		}
		delete(want, stage)
	}
	if len(want) != 0 {
		t.Errorf("faltan las líneas de %v", want)
	}
}

// TestOutcome_TheMatchElapsedIncludesItsTwoReads: el cronómetro de `match` arranca ANTES
// de leer el catálogo: si un día el SELECT del documento es el cuello, su `elapsed_ms`
// tiene que enseñarlo.
func TestOutcome_TheMatchElapsedIncludesItsTwoReads(t *testing.T) {
	r := newParts(t)
	index, err := r.catalogs.Obtener(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("leer el índice de prueba: %v", err)
	}
	slow := &tenantSpy{index: index, onAsk: func() { r.clock.Advance(450 * time.Millisecond) }}
	w, err := pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft, slow,
		r.decrypter, pipeline.Config{}, pipeline.WithClock(r.clock.Now), pipeline.WithShippingZones(slow))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	r.w = w
	r.match.script = []step{{takes: 2 * time.Millisecond}}
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	measured := false
	for _, l := range r.log.find("pipeline: etapa completada") {
		if l.fields["stage"] != intake.StageMatch {
			continue
		}
		measured = true
		if got := l.fields["elapsed_ms"]; got != int64(902) {
			t.Fatalf("match publicó elapsed_ms=%v; con dos lecturas de 450 ms y 2 ms de etapa se esperaban 902", got)
		}
	}
	if !measured {
		t.Fatalf("no hay línea de la etapa match:\n%s", r.log.dump())
	}
}

// TestOutcome_AStageThatFailsAlsoPublishesItsElapsed: el tropiezo publica cuánto tardó la
// etapa ANTES de fallar, con la causa como CAMPO, el intento, su techo y la marca.
func TestOutcome_AStageThatFailsAlsoPublishesItsElapsed(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p3.script = []step{{err: errEdgeDown, takes: 48 * time.Second}}
	id := r.seed("")
	r.run(t, id, intake.StatusPending)

	line := r.log.one(t, "WARN", "la etapa falló; el job vuelve a la cola con backoff")
	want := map[string]any{
		"job_id": id, "stage": intake.StageP3, "causa": pipeline.CauseInfra,
		"elapsed_ms": int64(48000), "intento": 1, "tope": 10, "error": "el Edge no contesta",
		"next_attempt_at": r.row(t, id).NextAttemptAt.UTC().Format(time.RFC3339),
	}
	for key, value := range want {
		if got := line.fields[key]; got != value {
			t.Errorf("%s = %v, se esperaba %v", key, got, value)
		}
	}
}

// TestOutcome_TheAttemptNumberComesFromTheClaim: el intento es `Attempts + 1` del job
// reclamado, en la línea de la etapa y en la del cierre.
func TestOutcome_TheAttemptNumberComesFromTheClaim(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	row := r.healthyRow("")
	row.Attempts = 4
	id := r.mem.Seed(row)
	r.run(t, id, intake.StatusDone)

	for _, l := range r.log.find("pipeline: etapa completada") {
		if l.fields["intento"] != 5 {
			t.Errorf("la etapa %v dice intento=%v, se esperaba 5", l.fields["stage"], l.fields["intento"])
		}
	}
	if got := r.log.one(t, "INFO", "pipeline: job DONE").fields["intento"]; got != 5 {
		t.Errorf("el cierre dice intento=%v, se esperaba 5", got)
	}
}

// TestOutcome_ExhaustedAttempts_KillTheJobWithItsCauseWritten: agotado el techo, el job
// muere con `causa=… stage=…: agotados los N intentos: …` y un Error «job FAILED».
func TestOutcome_ExhaustedAttempts_KillTheJobWithItsCauseWritten(t *testing.T) {
	r := newRig(t, pipeline.Config{MaxInfraAttempts: 2})
	r.p3.script = []step{{err: errEdgeDown}}
	id := r.seed("")
	r.run(t, id, intake.StatusPending)
	r.clock.Advance(2 * pipeline.DefaultBackoffCap)
	r.run(t, id, intake.StatusFailed)

	row := r.row(t, id)
	if want := "causa=infra stage=p3: agotados los 2 intentos: el Edge no contesta"; row.Error != want {
		t.Errorf("motivo de muerte = %q, se esperaba %q", row.Error, want)
	}
	if row.Attempts != 1 {
		t.Errorf("attempts = %d; el intento que mata no se cobra, se esperaba 1", row.Attempts)
	}
	line := r.log.one(t, "ERROR", "pipeline: job FAILED")
	want := map[string]any{
		"job_id": id, "stage": intake.StageP3, "causa": pipeline.CauseInfra, "intento": 2,
		"error": "agotados los 2 intentos: el Edge no contesta",
	}
	for key, value := range want {
		if got := line.fields[key]; got != value {
			t.Errorf("%s = %v, se esperaba %v", key, got, value)
		}
	}
}

// TestOutcome_AJobThatDiesBeforeAnyStage_SaysNone: `stage=""` obligaría a adivinar si es
// «murió antes de entrar en ninguna» o si alguien se dejó el campo.
func TestOutcome_AJobThatDiesBeforeAnyStage_SaysNone(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	row := r.healthyRow("")
	row.SourceText = intake.SourceText{}
	id := r.mem.Seed(row)
	r.run(t, id, intake.StatusFailed)

	line := r.log.one(t, "ERROR", "pipeline: job FAILED")
	if line.fields["stage"] != "ninguna" || line.fields["causa"] != pipeline.CauseInvalidJob || line.fields["intento"] != 1 {
		t.Errorf("la línea lleva %v; se esperaba stage=ninguna, causa=job_invalido e intento=1", line.fields)
	}
}

// TestOutcome_AJobFinishedByAnotherWorker_IsLetGoWithoutWriting: `ErrJobNotProcessing` no
// es un tropiezo: otro terminó el job mientras esta cadena corría. No se intenta Retry ni
// Fail —afectarían 0 filas—, se dice lo que pasó y la cadena se corta.
func TestOutcome_AJobFinishedByAnotherWorker_IsLetGoWithoutWriting(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	// Otro worker lo termina mientras P3 corre: P3 no puede guardar su artefacto y devuelve
	// el centinela (envuelto, como lo devuelven las etapas).
	r.p3.around = func(job intake.ClaimedJob) func() {
		if _, err := r.mem.Finish(context.Background(), job.ID, "otro-borrador"); err != nil {
			t.Errorf("simular al otro worker: %v", err)
		}
		return func() {}
	}
	r.p3.script = []step{{takes: 2 * time.Second}}
	r.run(t, id, intake.StatusDone)

	if got := r.store.closes(); len(got) != 0 {
		t.Fatalf("esta cadena escribió %+v; sobre un job que ya no es suyo no escribe nada", got)
	}
	line := r.log.one(t, "INFO", "el job se movió bajo los pies (ya no estaba en processing); esta cadena lo suelta")
	if line.fields["job_id"] != id || line.fields["stage"] != intake.StageP3 || line.fields["elapsed_ms"] != int64(2000) {
		t.Errorf("la línea lleva %v; se esperaban su job, stage=p3 y elapsed_ms=2000", line.fields)
	}
	if r.p4.count() != 0 || r.draft.count() != 0 {
		t.Error("la cadena siguió tras soltar el job")
	}
	if row := r.row(t, id); row.IntakeID != "otro-borrador" || row.Attempts != 0 {
		t.Errorf("la fila quedó (intake_id=%q, attempts=%d); es del otro worker y no se toca", row.IntakeID, row.Attempts)
	}
	r.log.requireNoErrors(t)
}

// TestOutcome_NoWriteIsSilent: cada escritura de desenlace que FALLA deja un Error «…queda
// en processing», y cada una que NO APLICA (`(false, nil)`, un no-error) deja un Info
// «…no aplicó». Sin ellas, un job atascado no dejaría ni una línea.
func TestOutcome_NoWriteIsSilent(t *testing.T) {
	down := errors.New("la base no contesta")
	stumbles := func(r *rig) { r.p2.script = []step{{err: errEdgeDown}} }
	poisoned := func(r *rig) { r.decrypter.text = "" }
	healthy := func(*rig) {}
	cases := []struct {
		name    string
		op      string
		job     func(*rig)
		fails   bool
		level   string
		message string
		keys    []string
	}{
		{"retry fails", opRetry, stumbles, true, "ERROR",
			"no se pudo reencolar el job con backoff; queda en processing", []string{"job_id", "stage", "causa", "error"}},
		{"retry does not apply", opRetry, stumbles, false, "INFO",
			"el reencolado no aplicó (el job ya no estaba en processing)", []string{"job_id", "stage", "causa"}},
		{"fail fails", opFail, poisoned, true, "ERROR",
			"no se pudo marcar el job como failed; queda en processing", []string{"job_id", "stage", "causa", "error"}},
		{"fail does not apply", opFail, poisoned, false, "INFO",
			"el fallo no aplicó (el job ya no estaba en processing)", []string{"job_id", "stage", "causa"}},
		{"finish fails", opFinish, healthy, true, "ERROR",
			"no se pudo terminar el job; queda en processing", []string{"job_id", "error"}},
		{"finish does not apply", opFinish, healthy, false, "INFO",
			"el cierre no aplicó (el job ya no estaba en processing)", []string{"job_id"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			c.job(r)
			if c.fails {
				r.store.breakOp(c.op, down)
			} else {
				r.store.loseOp(c.op)
			}
			id := r.seed("")
			if n := r.drain(context.Background()); n != 1 {
				t.Fatalf("Drain procesó %d jobs, se esperaba 1: un desenlace que no se escribe no para el worker", n)
			}

			line := r.log.one(t, c.level, c.message)
			line.requireKeys(t, c.keys...)
			if line.fields["job_id"] != id {
				t.Errorf("job_id = %v, se esperaba %q", line.fields["job_id"], id)
			}
			// Y no se anuncia el desenlace que no ocurrió.
			for _, announced := range []string{"job DONE", "job FAILED", "vuelve a la cola con backoff"} {
				if got := r.log.find(announced); len(got) != 0 {
					t.Errorf("se anunció %q y la escritura no ocurrió:\n%s", announced, r.log.dump())
				}
			}
			if got := r.row(t, id).Status; got != intake.StatusProcessing {
				t.Errorf("la fila quedó %q; el doble no la tocó y debía seguir en processing", got)
			}
		})
	}
}

// TestOutcome_WritesSurviveTheCancellationOfTheWorker: con el ctx del llamante, un apagado
// durante una etapa cancelaría también la escritura del desenlace y el job se quedaría en
// `processing` PARA SIEMPRE. Los tres desenlaces se escriben con un ctx VIVO y acotado
// (5 s), y aplican.
func TestOutcome_WritesSurviveTheCancellationOfTheWorker(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		prepare func(r *rig, cancel func())
		status  string
	}{
		{"retry", opRetry, func(r *rig, cancel func()) {
			r.p2.around = func(intake.ClaimedJob) func() { return cancel }
			r.p2.script = []step{{err: fmt.Errorf("p2: pedir las ideas principales: %w", context.Canceled)}}
		}, intake.StatusPending},
		{"fail", opFail, func(r *rig, cancel func()) {
			r.p2.around = func(intake.ClaimedJob) func() { return cancel }
			r.p2.script = []step{{err: fmt.Errorf("p2: %w", stages.ErrNoLiteral)}}
		}, intake.StatusFailed},
		{"finish", opFinish, func(r *rig, cancel func()) {
			r.draft.around = func(intake.ClaimedJob) func() { return cancel }
		}, intake.StatusDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.prepare(r, cancel)
			id := r.seed("")

			if found, err := r.w.RunOnce(ctx); !found || err != nil {
				t.Fatalf("RunOnce = (%v, %v), se esperaba (true, nil)", found, err)
			}
			if ctx.Err() == nil {
				t.Fatal("el ctx del worker sigue vivo: el test no ejercitó el apagado")
			}

			writes := r.store.closesOf(c.op)
			if len(writes) != 1 {
				t.Fatalf("hubo %d escrituras de %s, se esperaba 1:\n%s", len(writes), c.op, r.log.dump())
			}
			w := writes[0]
			if !w.alive {
				t.Error("el desenlace se escribió con un ctx CANCELADO: no habría llegado a la base")
			}
			if !w.hasDeadline || w.budget > 5*time.Second || w.budget < 3*time.Second {
				t.Errorf("el ctx del desenlace trae plazo=%v, presupuesto=%s; se esperaba un plazo de 5 s", w.hasDeadline, w.budget)
			}
			if got := r.row(t, id).Status; got != c.status {
				t.Errorf("el job quedó %q, se esperaba %q: no puede quedarse en processing", got, c.status)
			}
		})
	}
}

// TestOutcome_ClosingWithoutIntakeID_StillClosesAndWarns: con la cadena entera corrida, un
// id vacío cierra el job igual —el cierre conserva lo que hubiera— y la bandeja se
// quedaría sin la solicitud sin que nada fallara. Es una ANOMALÍA y se dice.
func TestOutcome_ClosingWithoutIntakeID_StillClosesAndWarns(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.draft.intakeID = ""
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	line := r.log.one(t, "WARN", "el job termina SIN intake_id; su solicitud no quedará enlazada al job")
	line.requireKeys(t, "job_id", "tenant_id", "donde_mirar")
	finishes := r.store.closesOf(opFinish)
	if len(finishes) != 1 || finishes[0].text != "" {
		t.Errorf("cierres = %+v; se esperaba UNO, con el id vacío", finishes)
	}

	// Y con id, el cierre lo lleva y no hay aviso.
	ok := newRig(t, pipeline.Config{})
	other := ok.seed("")
	ok.run(t, other, intake.StatusDone)
	if got := ok.store.closesOf(opFinish); len(got) != 1 || got[0].text != draftIntakeID || got[0].jobID != other {
		t.Errorf("cierres = %+v; se esperaba UNO, con el id del borrador", got)
	}
	if got := ok.log.find("el job termina SIN intake_id"); len(got) != 0 {
		t.Errorf("con intake_id no hay aviso:\n%s", ok.log.dump())
	}
}
