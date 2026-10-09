package intakehelpertest

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// caseClaimEmptyQueue: la cola vacía es el estado normal del worker. No es un error, y no hay
// job a medias junto al false.
func caseClaimEmptyQueue(t *testing.T, m MachineMontaje) {
	requireNothingToClaim(t, m, "cola vacía")
	job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA)
	if err != nil || ok || job.ID != "" {
		t.Errorf("ClaimNextIgnoringBackoff con la cola vacía = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", job, ok, err)
	}
}

// caseClaimReturnsEverything: el reclamo pasa el job a `processing` y devuelve DE UNA VEZ todo
// lo que el worker necesita —clave, etapa, instante del primer mensaje, referencias, sobre,
// artefactos, intentos consumidos y contexto del re-análisis—, sin tocar nada más de la fila.
// Es lo que hace gratis la reanudación.
func caseClaimReturnsEverything(t *testing.T, m MachineMontaje) {
	w := seedWitnesses(t, m.Table)
	id := seedJob(t, m.Table, m.TenantA, intake.StatusPending, loaded)
	before := m.Row(t, id)

	job := claimOf(t, m, id)
	requireClaimMatchesRow(t, job, before)

	want := before
	want.Status = intake.StatusProcessing
	requireWrittenRow(t, "el job reclamado", m.Row(t, id), want)
	w.requireUntouched(t, m.Table)
}

// caseClaimZeroValues: un job que nunca corrió y sin sobre —el compositor no llegó a
// escribirlo— se reclama igual, y lo que en la tabla es NULL llega como valor cero: etapa "",
// instante cero, sobre vacío, ningún artefacto y contexto de re-análisis cero (pipeline normal).
func caseClaimZeroValues(t *testing.T, m MachineMontaje) {
	id := seedJob(t, m.Table, m.TenantA, intake.StatusPending)
	job := claimOf(t, m, id)
	if job.Stage != "" || !job.MessageTS.IsZero() || job.Attempts != 0 {
		t.Errorf("(Stage, MessageTS, Attempts) = (%q, %v, %d), quería (\"\", cero, 0)", job.Stage, job.MessageTS, job.Attempts)
	}
	if len(job.SourceRefs) != 0 || len(job.Artifacts) != 0 {
		t.Errorf("(SourceRefs, Artifacts) = (%v, %v), quería los dos vacíos", job.SourceRefs, artifactsText(job.Artifacts))
	}
	if job.SourceText.Complete() || len(job.SourceText.Enc) != 0 || len(job.SourceText.DEK) != 0 || job.SourceText.KEKID != "" {
		t.Errorf("SourceText = %+v, quería el sobre vacío", job.SourceText)
	}
	if job.Reanalysis != (intake.Reanalysis{}) || job.Reanalysis.IsFromOwner() {
		t.Errorf("Reanalysis = %+v, quería el valor cero (pipeline normal)", job.Reanalysis)
	}
}

// caseClaimOnlyPending: solo se reclama lo que está en `pending`. Una ventana abierta, un job
// tomado y los dos terminales no se tocan, aunque su marca esté vencida.
func caseClaimOnlyPending(t *testing.T, m MachineMontaje) {
	statuses := []string{intake.StatusAggregating, intake.StatusProcessing, intake.StatusDone, intake.StatusFailed}
	before := make([]Row, 0, len(statuses))
	for _, status := range statuses {
		before = append(before, m.Row(t, seedJob(t, m.Table, m.TenantA, status, loaded)))
	}
	m.Advance(t)
	requireNothingToClaim(t, m, "sin ningún job pending")
	for _, want := range before {
		requireSameRow(t, "un job "+want.Status+" tras el reclamo", m.Row(t, want.ID), want)
	}
}

// caseClaimTwice: «doble-claim pierde uno». Un job reclamado ya no está en `pending`, así que
// el segundo reclamo no lo encuentra y la fila no se vuelve a tocar.
func caseClaimTwice(t *testing.T, m MachineMontaje) {
	id := seedJob(t, m.Table, m.TenantA, intake.StatusPending, loaded)
	claimOf(t, m, id)
	claimed := m.Row(t, id)
	m.Advance(t)
	requireNothingToClaim(t, m, "el único job ya está reclamado")
	requireSameRow(t, "el job ya reclamado, tras el segundo reclamo", m.Row(t, id), claimed)
}

// caseClaimRespectsTheMark: la marca del backoff RETIENE hasta que vence y al vencer SUELTA. Un
// `pending` con la marca en el futuro no se reclama ni se toca; con la marca en el pasado, sí.
func caseClaimRespectsTheMark(t *testing.T, m MachineMontaje) {
	now := base(t, m.Table)
	future := seedJob(t, m.Table, m.TenantA, intake.StatusPending, loaded, at(now.Add(time.Hour), now.Add(-time.Hour)))
	before := m.Row(t, future)
	requireNothingToClaim(t, m, "el único pending tiene la marca en el futuro")
	requireSameRow(t, "el job con la marca en el futuro", m.Row(t, future), before)

	due := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(-time.Second), now))
	claimOf(t, m, due)
	requireSameRow(t, "el job con la marca en el futuro, tras reclamar el vencido", m.Row(t, future), before)
}

// caseClaimOrder: entre dos vencidos gana LA MARCA MÁS ANTIGUA, no el creado antes; y a igual
// marca, desempata el creado antes. Los dos primeros jobs llevan la marca y la creación
// CRUZADAS a propósito: si coincidieran, un orden por creación pasaría el caso sin serlo.
func caseClaimOrder(t *testing.T, m MachineMontaje) {
	now := base(t, m.Table)
	// Misma marca que waitedLongest, creado después: pierde el desempate. Se siembra ANTES que
	// el ganador a propósito: un reclamo que no desempatara por creación devolvería los dos en
	// el orden en que se escribieron, y con el perdedor sembrado después pasaría el caso.
	tieLoser := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(-time.Minute), now.Add(-5*time.Minute)))
	waitedLongest := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(-time.Minute), now.Add(-10*time.Minute)))
	oldestMark := seedJob(t, m.Table, m.TenantB, intake.StatusPending, at(now.Add(-10*time.Minute), now.Add(-time.Minute)))

	for i, want := range []string{oldestMark, waitedLongest, tieLoser} {
		job, ok, err := m.Store.ClaimNext(context.Background())
		if err != nil || !ok {
			t.Fatalf("reclamo %d = (_, %v, %v), quería un job", i+1, ok, err)
		}
		if job.ID != want {
			t.Fatalf("el reclamo %d se llevó %s, quería %s (orden: marca más antigua; a igual marca, creado antes)", i+1, job.ID, want)
		}
	}
}

// caseWakeIgnoresTheMark: el reclamo por evento se lleva el `pending` de su tenant AUNQUE su
// backoff no haya vencido —es todo su propósito—, donde el reclamo normal no lo ve. Devuelve el
// mismo job entero y deja la misma fila que su hermano.
func caseWakeIgnoresTheMark(t *testing.T, m MachineMontaje) {
	now := base(t, m.Table)
	w := seedWitnesses(t, m.Table)
	id := seedJob(t, m.Table, m.TenantA, intake.StatusPending, loaded, at(now.Add(time.Hour), now.Add(-time.Hour)))
	before := m.Row(t, id)
	requireNothingToClaim(t, m, "el backoff del único pending no ha vencido")

	job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA)
	if err != nil || !ok || job.ID != id {
		t.Fatalf("ClaimNextIgnoringBackoff = (%q, %v, %v), quería llevarse %s", job.ID, ok, err, id)
	}
	requireClaimMatchesRow(t, job, before)
	want := before
	want.Status = intake.StatusProcessing
	requireWrittenRow(t, "el job reclamado por evento", m.Row(t, id), want)
	w.requireUntouched(t, m.Table)
}

// caseWakeOnlyItsTenant: un evento LOCAL no tiene un efecto GLOBAL. El job del otro tenant es
// el más antiguo y el de marca más vieja —ganaría por los dos criterios del orden—, y aun así
// el reclamo por evento de A se lleva el de A y no toca el de B.
func caseWakeOnlyItsTenant(t *testing.T, m MachineMontaje) {
	now := base(t, m.Table)
	other := seedJob(t, m.Table, m.TenantB, intake.StatusPending, loaded, at(now.Add(-time.Hour), now.Add(-time.Hour)))
	mine := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(time.Hour), now.Add(-time.Minute)))
	before := m.Row(t, other)

	job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA)
	if err != nil || !ok || job.ID != mine {
		t.Fatalf("ClaimNextIgnoringBackoff(A) = (%q, %v, %v), quería llevarse el job de A (%s)", job.ID, ok, err, mine)
	}
	if job.Key.TenantID != m.TenantA {
		t.Errorf("el job reclamado es del tenant %q, quería %q", job.Key.TenantID, m.TenantA)
	}
	requireSameRow(t, "el job del otro tenant", m.Row(t, other), before)
	if job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA); err != nil || ok {
		t.Errorf("segundo reclamo por evento de A = (%q, %v, %v), quería (\"\", false, nil): no le queda nada", job.ID, ok, err)
	}
	requireSameRow(t, "el job del otro tenant, tras el segundo reclamo", m.Row(t, other), before)
}

// caseWakeOnlyPendingInOrder: el reclamo por evento ignora la marca para FILTRAR pero la sigue
// usando para ORDENAR (el castigo que vencía antes va primero, y desempata el creado antes), y
// solo toma `pending`: lo que ya está tomado o terminado no se toca.
func caseWakeOnlyPendingInOrder(t *testing.T, m MachineMontaje) {
	now := base(t, m.Table)
	statuses := []string{intake.StatusAggregating, intake.StatusProcessing, intake.StatusDone, intake.StatusFailed}
	others := make([]Row, 0, len(statuses))
	for _, status := range statuses {
		others = append(others, m.Row(t, seedJob(t, m.Table, m.TenantA, status, loaded, at(now.Add(-24*time.Hour), now.Add(-24*time.Hour)))))
	}
	// El perdedor del desempate se siembra ANTES que el ganador (ver caseClaimOrder).
	tieLoser := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(2*time.Hour), now.Add(-5*time.Minute)))
	createdFirst := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(2*time.Hour), now.Add(-10*time.Minute)))
	earliestMark := seedJob(t, m.Table, m.TenantA, intake.StatusPending, at(now.Add(time.Hour), now.Add(-time.Minute)))

	for i, want := range []string{earliestMark, createdFirst, tieLoser} {
		job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA)
		if err != nil || !ok {
			t.Fatalf("reclamo por evento %d = (_, %v, %v), quería un job", i+1, ok, err)
		}
		if job.ID != want {
			t.Fatalf("el reclamo por evento %d se llevó %s, quería %s", i+1, job.ID, want)
		}
	}
	if job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), m.TenantA); err != nil || ok {
		t.Errorf("reclamo por evento sin pending = (%q, %v, %v), quería (\"\", false, nil)", job.ID, ok, err)
	}
	for _, want := range others {
		requireSameRow(t, "un job "+want.Status+" tras los reclamos por evento", m.Row(t, want.ID), want)
	}
}

// caseWakeNothing: los dos desenlaces mudos. Un tenant sin nada que reanudar es (_, false, nil);
// y un tenant VACÍO no es un filtro que case con todo, es una llamada mal hecha: no reclama
// nada aunque haya jobs esperando.
func caseWakeNothing(t *testing.T, m MachineMontaje) {
	id := seedJob(t, m.Table, m.TenantB, intake.StatusPending, loaded)
	before := m.Row(t, id)
	m.Advance(t)
	for name, tenant := range map[string]string{"un tenant sin jobs": m.TenantA, "un tenant vacío": ""} {
		job, ok, err := m.Store.ClaimNextIgnoringBackoff(context.Background(), tenant)
		if err != nil || ok || job.ID != "" {
			t.Errorf("ClaimNextIgnoringBackoff de %s = (%q, %v, %v), quería (\"\", false, nil)", name, job.ID, ok, err)
		}
	}
	requireSameRow(t, "el job pending del otro tenant", m.Row(t, id), before)
}

// requireClaimMatchesRow afirma que el job reclamado trae TODO lo que la fila tenía antes del
// reclamo.
func requireClaimMatchesRow(t *testing.T, job intake.ClaimedJob, row Row) {
	t.Helper()
	if job.ID != row.ID || job.Key != row.Key {
		t.Errorf("(ID, Key) = (%q, %+v), quería (%q, %+v)", job.ID, job.Key, row.ID, row.Key)
	}
	if job.Stage != row.Stage || job.Attempts != row.Attempts {
		t.Errorf("(Stage, Attempts) = (%q, %d), quería (%q, %d)", job.Stage, job.Attempts, row.Stage, row.Attempts)
	}
	if !job.MessageTS.Equal(row.MessageTS) {
		t.Errorf("MessageTS = %v, quería %v", job.MessageTS, row.MessageTS)
	}
	if !slices.Equal(job.SourceRefs, row.SourceRefs) {
		t.Errorf("SourceRefs = %v, quería %v", job.SourceRefs, row.SourceRefs)
	}
	if !bytes.Equal(job.SourceText.Enc, row.SourceText.Enc) || !bytes.Equal(job.SourceText.DEK, row.SourceText.DEK) ||
		job.SourceText.KEKID != row.SourceText.KEKID {
		t.Errorf("SourceText = %+v, quería %+v", job.SourceText, row.SourceText)
	}
	if !sameArtifacts(job.Artifacts, row.Artifacts) {
		t.Errorf("Artifacts = %s, quería %s", artifactsText(job.Artifacts), artifactsText(row.Artifacts))
	}
	if job.Reanalysis != row.Reanalysis {
		t.Errorf("Reanalysis = %+v, quería %+v", job.Reanalysis, row.Reanalysis)
	}
}
