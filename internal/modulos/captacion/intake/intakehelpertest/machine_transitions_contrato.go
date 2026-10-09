package intakehelpertest

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// processingJob siembra un job «con todo» en `processing` y devuelve su id y su fila.
func processingJob(t *testing.T, m MachineMontaje, mutate ...func(*Row)) (string, Row) {
	t.Helper()
	id := seedJob(t, m.Table, m.TenantA, intake.StatusProcessing, append([]func(*Row){loaded}, mutate...)...)
	return id, m.Row(t, id)
}

// requireApplied exige que la transición aplicara: (true, nil).
func requireApplied(t *testing.T, what string, ok bool, err error) {
	t.Helper()
	if err != nil || !ok {
		t.Fatalf("%s = (%v, %v), quería (true, nil)", what, ok, err)
	}
}

// caseSaveStageMerges: guardar una etapa mueve `stage` a ella y AÑADE su artefacto sin tocar
// los de las etapas anteriores (fusiona, no sustituye). Avanzar saltando etapas vale: la guarda
// solo impide retroceder. Nada más de la fila cambia.
func caseSaveStageMerges(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id, before := processingJob(t, m)
	ctx := context.Background()

	p3 := artifact(intake.StageP3, "third")
	ok, err := m.Store.SaveStage(ctx, id, p3)
	requireApplied(t, "SaveStage(p3)", ok, err)
	want := before
	want.Stage = intake.StageP3
	want.Artifacts = maps.Clone(before.Artifacts)
	want.Artifacts[intake.StageP3] = p3.Payload
	afterP3 := m.Row(t, id)
	requireWrittenRow(t, "el job tras guardar p3", afterP3, want)

	m.Advance(t)
	draft := artifact(intake.StageDraft, "last")
	ok, err = m.Store.SaveStage(ctx, id, draft)
	requireApplied(t, "SaveStage(draft) saltando p4 y match", ok, err)
	want = afterP3
	want.Stage = intake.StageDraft
	want.Artifacts = maps.Clone(afterP3.Artifacts)
	want.Artifacts[intake.StageDraft] = draft.Payload
	requireWrittenRow(t, "el job tras guardar draft", m.Row(t, id), want)
	w.requireUntouched(t, m.Table)
}

// caseSaveStageRepeats: repetir la etapa ACTUAL vale —un job que volvió a la cola a mitad de
// una etapa la vuelve a producir— y reemplaza SOLO el artefacto de esa etapa. Vale también la
// primera etapa de un job que aún no tiene ninguna.
func caseSaveStageRepeats(t *testing.T, m MachineMontaje) {
	id, before := processingJob(t, m)
	again := artifact(intake.StageP2, "again")
	ok, err := m.Store.SaveStage(context.Background(), id, again)
	requireApplied(t, "SaveStage(p2) sobre un job en p2", ok, err)
	want := before
	want.Artifacts = map[string]json.RawMessage{intake.StageP2: again.Payload}
	requireWrittenRow(t, "el job tras repetir p2", m.Row(t, id), want)

	fresh := seedJob(t, m.Table, m.TenantA, intake.StatusProcessing)
	freshBefore := m.Row(t, fresh)
	first := artifact(intake.StageP2, "first")
	ok, err = m.Store.SaveStage(context.Background(), fresh, first)
	requireApplied(t, "SaveStage(p2) sobre un job sin etapa", ok, err)
	want = freshBefore
	want.Stage = intake.StageP2
	want.Artifacts = map[string]json.RawMessage{intake.StageP2: first.Payload}
	requireWrittenRow(t, "el job sin etapa tras guardar p2", m.Row(t, fresh), want)
}

// caseSaveStageNeverGoesBack: la máquina NO RETROCEDE. Guardar una etapa anterior a la actual
// devuelve (false, nil) y no escribe ni la etapa ni el artefacto, desde cualquier etapa.
func caseSaveStageNeverGoesBack(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	stages := []string{intake.StageP2, intake.StageP3, intake.StageP4, intake.StageMatch, intake.StageDraft}
	for i, current := range stages[1:] {
		id, before := processingJob(t, m, func(r *Row) {
			r.Stage = current
			r.Artifacts = map[string]json.RawMessage{current: artifact(current, "current").Payload}
		})
		m.Advance(t)
		for _, earlier := range stages[:i+1] {
			ok, err := m.Store.SaveStage(context.Background(), id, artifact(earlier, "stale"))
			if err != nil || ok {
				t.Errorf("SaveStage(%s) sobre un job en %s = (%v, %v), quería (false, nil)", earlier, current, ok, err)
			}
		}
		requireSameRow(t, "el job en "+current+" tras intentar retroceder", m.Row(t, id), before)
	}
	w.requireUntouched(t, m.Table)
}

// caseSaveStageInvalidArtifact: un artefacto inválido JAMÁS se persiste. Se mira la FILA, no el
// valor de retorno. El que muerde es el objeto JSON válido SIN `version`, que la base aceptaría.
func caseSaveStageInvalidArtifact(t *testing.T, m MachineMontaje) {
	id, before := processingJob(t, m)
	m.Advance(t)
	invalid := map[string]intake.Artifact{
		"objeto sin version": {Stage: intake.StageP3, Payload: json.RawMessage(`{"ideas":[]}`)},
		"version cero":       {Stage: intake.StageP3, Payload: json.RawMessage(`{"version":0}`)},
		"un array":           {Stage: intake.StageP3, Payload: json.RawMessage(`[{"version":1}]`)},
		"JSON roto":          {Stage: intake.StageP3, Payload: json.RawMessage(`{"version":1`)},
		"payload vacío":      {Stage: intake.StageP3},
		"etapa desconocida":  {Stage: "p5", Payload: json.RawMessage(`{"version":1}`)},
		"etapa vacía":        {Payload: json.RawMessage(`{"version":1}`)},
	}
	for name, a := range invalid {
		ok, err := m.Store.SaveStage(context.Background(), id, a)
		if err == nil || ok {
			t.Errorf("SaveStage con %s = (%v, %v), quería (false, error)", name, ok, err)
		}
		requireSameRow(t, "el job tras un artefacto inválido ("+name+")", m.Row(t, id), before)
	}
}

func caseSaveStageNotProcessing(t *testing.T, m MachineMontaje) {
	requireNoEffectOutsideProcessing(t, m, "SaveStage")
}

// caseRelease: soltar devuelve el job a `pending` SIN castigo y sin tocar nada más —ni el sobre,
// ni los artefactos, ni la etapa, ni los intentos, ni la marca—, así que es reclamable en el
// acto y vuelve con todo lo que tenía.
func caseRelease(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id, before := processingJob(t, m)
	ok, err := m.Store.Release(context.Background(), id)
	requireApplied(t, "Release", ok, err)
	want := before
	want.Status = intake.StatusPending
	released := m.Row(t, id)
	requireWrittenRow(t, "el job soltado", released, want)

	requireClaimMatchesRow(t, claimOf(t, m, id), released)
	w.requireUntouched(t, m.Table)
}

func caseReleaseNotProcessing(t *testing.T, m MachineMontaje) {
	requireNoEffectOutsideProcessing(t, m, "Release")
}

// caseRetry: reencolar devuelve el job a `pending` COBRÁNDOLE el intento y EMPUJANDO la marca
// hasta el instante dado, las tres cosas a la vez; no escribe `error` ni toca nada más. Con la
// marca en el futuro el reclamo normal no lo ve; el reclamo por evento sí, y trae los intentos
// ya cobrados.
func caseRetry(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	// Parte de 7 y no de 0: el resultado tiene que ser «uno más», no «uno».
	id, before := processingJob(t, m, func(r *Row) { r.Attempts = 7 })
	next := base(t, m.Table).Add(time.Hour)
	ok, err := m.Store.Retry(context.Background(), id, next)
	requireApplied(t, "Retry", ok, err)

	want := before
	want.Status = intake.StatusPending
	want.Attempts = 8
	want.NextAttemptAt = next
	retried := m.Row(t, id)
	requireWrittenRow(t, "el job reencolado", retried, want)

	requireNothingToClaim(t, m, "el job reencolado tiene la marca en el futuro")
	job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA)
	if err != nil || !ok || job.ID != id {
		t.Fatalf("ClaimNextIgnoringBackoff tras Retry = (%q, %v, %v), quería llevarse %s", job.ID, ok, err, id)
	}
	requireClaimMatchesRow(t, job, retried)
	w.requireUntouched(t, m.Table)
}

// caseRetryPastMark: la marca se escribe TAL CUAL, también si ya pasó. Una marca pasada (y no
// cero) no se sube al reloj de la implementación: queda exacta en la fila y, como ya venció, el
// reclamo normal se lleva el job en el acto. Quien decide cuánto se espera es el llamante.
func caseRetryPastMark(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id, before := processingJob(t, m)
	next := base(t, m.Table).Add(-time.Hour)
	ok, err := m.Store.Retry(context.Background(), id, next)
	requireApplied(t, "Retry con la marca en el pasado", ok, err)

	want := before
	want.Status = intake.StatusPending
	want.Attempts = before.Attempts + 1
	want.NextAttemptAt = next
	retried := m.Row(t, id)
	requireWrittenRow(t, "el job reencolado con la marca en el pasado", retried, want)
	if !retried.NextAttemptAt.Equal(next) {
		t.Errorf("NextAttemptAt = %v, quería la marca dada sin tocar (%v)", retried.NextAttemptAt, next)
	}

	requireClaimMatchesRow(t, claimOf(t, m, id), retried)
	w.requireUntouched(t, m.Table)
}

// caseRetryZeroInstant: una marca cero es el año 1 —el pasado— y el backoff sería un no-op
// silencioso. Se rechaza con error y el job sigue tomado, intacto.
func caseRetryZeroInstant(t *testing.T, m MachineMontaje) {
	id, before := processingJob(t, m)
	m.Advance(t)
	if ok, err := m.Store.Retry(context.Background(), id, time.Time{}); err == nil || ok {
		t.Errorf("Retry con el instante cero = (%v, %v), quería (false, error)", ok, err)
	}
	requireSameRow(t, "el job tras un Retry sin marca", m.Row(t, id), before)
}

func caseRetryNotProcessing(t *testing.T, m MachineMontaje) {
	requireNoEffectOutsideProcessing(t, m, "Retry")
}

// caseFinish: terminar lleva el job a `done`, escribe el borrador y VACÍA LAS TRES piezas del
// sobre en el mismo paso (INV-13). Lo demás sobrevive: referencias, artefactos, etapa, instante
// del primer mensaje.
func caseFinish(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id, before := processingJob(t, m)
	if !before.SourceText.Complete() {
		t.Fatalf("el job sembrado no tiene el sobre completo (%+v): el vaciado no probaría nada", before.SourceText)
	}
	intakeID := uuid.NewString()
	ok, err := m.Store.Finish(context.Background(), id, intakeID)
	requireApplied(t, "Finish", ok, err)

	want := before
	want.Status = intake.StatusDone
	want.IntakeID = intakeID
	want.SourceText = intake.SourceText{}
	requireWrittenRow(t, "el job terminado", m.Row(t, id), want)
	w.requireUntouched(t, m.Table)
}

// caseFinishKeepsIntake: un borrador vacío deja la columna como estaba. Es el job del
// re-análisis, que nace sabiendo a quién sirve.
func caseFinishKeepsIntake(t *testing.T, m MachineMontaje) {
	id, before := processingJob(t, m)
	if before.IntakeID == "" {
		t.Fatal("el job sembrado no tiene borrador: el caso no probaría que se conserva")
	}
	ok, err := m.Store.Finish(context.Background(), id, "")
	requireApplied(t, "Finish sin borrador", ok, err)
	want := before
	want.Status = intake.StatusDone
	want.SourceText = intake.SourceText{}
	requireWrittenRow(t, "el job terminado sin borrador nuevo", m.Row(t, id), want)

	// Y sobre un job que no tenía ninguno, sigue sin tenerlo.
	bare := seedJob(t, m.Table, m.TenantA, intake.StatusProcessing)
	ok, err = m.Store.Finish(context.Background(), bare, "")
	requireApplied(t, "Finish sin borrador sobre un job sin borrador", ok, err)
	if got := m.Row(t, bare); got.Status != intake.StatusDone || got.IntakeID != "" {
		t.Errorf("job sin borrador terminado = (status %q, intake_id %q), quería (done, \"\")", got.Status, got.IntakeID)
	}
}

func caseFinishNotProcessing(t *testing.T, m MachineMontaje) {
	requireNoEffectOutsideProcessing(t, m, "Finish")
}

// caseFail: fallar lleva el job a `failed` con su causa y vacía el sobre EXACTAMENTE igual que
// Finish: lo que dispara INV-13 es terminar, no terminar bien. `stage` se conserva —es dónde
// murió— y los artefactos también.
func caseFail(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id, before := processingJob(t, m)
	if !before.SourceText.Complete() {
		t.Fatalf("el job sembrado no tiene el sobre completo (%+v): el vaciado no probaría nada", before.SourceText)
	}
	const reason = "causa=calidad stage=p2: agotados los 3 intentos"
	ok, err := m.Store.Fail(context.Background(), id, reason)
	requireApplied(t, "Fail", ok, err)

	want := before
	want.Status = intake.StatusFailed
	want.Error = reason
	want.SourceText = intake.SourceText{}
	requireWrittenRow(t, "el job fallado", m.Row(t, id), want)
	w.requireUntouched(t, m.Table)
}

// caseFailWithoutReason: un job muerto sin causa no es diagnosticable. Se rechaza con error y
// el job sigue tomado, con su sobre.
func caseFailWithoutReason(t *testing.T, m MachineMontaje) {
	id, before := processingJob(t, m)
	m.Advance(t)
	if ok, err := m.Store.Fail(context.Background(), id, ""); err == nil || ok {
		t.Errorf("Fail sin causa = (%v, %v), quería (false, error)", ok, err)
	}
	requireSameRow(t, "el job tras un Fail sin causa", m.Row(t, id), before)
}

func caseFailNotProcessing(t *testing.T, m MachineMontaje) {
	requireNoEffectOutsideProcessing(t, m, "Fail")
}

// caseTerminalsAbsorb: `done` y `failed` son ABSORBENTES. Llegando a ellos POR LA MÁQUINA
// (Finish y Fail), ninguna de las cinco transiciones aplica después ni ningún reclamo se los
// lleva: la fila queda idéntica, con el sobre vacío.
func caseTerminalsAbsorb(t *testing.T, m MachineMontaje) {
	ctx := context.Background()
	done, _ := processingJob(t, m)
	ok, err := m.Store.Finish(ctx, done, uuid.NewString())
	requireApplied(t, "Finish", ok, err)
	failed, _ := processingJob(t, m)
	ok, err = m.Store.Fail(ctx, failed, "causa=infra stage=p3: sin respuesta")
	requireApplied(t, "Fail", ok, err)

	for _, id := range []string{done, failed} {
		before := m.Row(t, id)
		m.Advance(t)
		for _, tr := range transitions(base(t, m.Table).Add(time.Hour)) {
			if ok, err := tr.run(m.Store, id); err != nil || ok {
				t.Errorf("%s sobre un job %s = (%v, %v), quería (false, nil)", tr.name, before.Status, ok, err)
			}
		}
		requireNothingToClaim(t, m, "solo hay jobs terminales")
		if job, ok, err := m.Store.ClaimNextIgnoringBackoff(ctx, m.TenantA); err != nil || ok {
			t.Errorf("reclamo por evento con solo terminales = (%q, %v, %v), quería (\"\", false, nil)", job.ID, ok, err)
		}
		requireSameRow(t, "el job "+before.Status+" tras todas las transiciones", m.Row(t, id), before)
	}
}

// caseEmptyJobID: una transición sin id de job es una llamada mal hecha: error, y nada tocado.
func caseEmptyJobID(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	_, _ = processingJob(t, m)
	m.Advance(t)
	for _, tr := range transitions(base(t, m.Table).Add(time.Hour)) {
		if ok, err := tr.run(m.Store, ""); err == nil || ok {
			t.Errorf("%s sin id de job = (%v, %v), quería (false, error)", tr.name, ok, err)
		}
	}
	w.requireUntouched(t, m.Table)
}
