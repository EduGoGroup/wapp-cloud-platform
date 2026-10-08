package intakehelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// onEvent devuelve el mutador que cuelga el job de esa clave de ventana (mismo evento).
func onEvent(k intake.WindowKey) func(*Row) {
	return func(r *Row) { r.Key = k }
}

// createdAt devuelve el mutador que fija el instante de creación.
func createdAt(when time.Time) func(*Row) {
	return func(r *Row) { r.CreatedAt, r.UpdatedAt = when, when }
}

// ownerRequest devuelve la petición de re-análisis del dueño sobre esa clave, completa.
func ownerRequest(k intake.WindowKey) intake.ReanalysisRequest {
	return intake.ReanalysisRequest{
		Key: k, IntakeID: uuid.NewString(),
		Context: intake.Reanalysis{RequestedBy: intake.RequestedByOwner, Via: "local", Source: "event_thread", From: 2},
	}
}

// liveJob pregunta por el job vivo del evento de la clave, o falla el test.
func liveJob(t *testing.T, m ReanalysisMontaje, k intake.WindowKey) (string, bool) {
	t.Helper()
	id, ok, err := m.Store.LiveJobOfEvent(context.Background(), k.TenantID, k.EventID)
	if err != nil {
		t.Fatalf("LiveJobOfEvent(%q, %q): error inesperado %v", k.TenantID, k.EventID, err)
	}
	if !ok && id != "" {
		t.Errorf("LiveJobOfEvent devolvió el id %q junto a ok=false", id)
	}
	return id, ok
}

// caseLiveJobNone: un evento sin jobs, o con todos sus jobs TERMINADOS, no tiene job vivo. No
// es un error: es exactamente el evento que se puede re-analizar.
func caseLiveJobNone(t *testing.T, m ReanalysisMontaje) {
	k := newKey(m.TenantA)
	if id, ok := liveJob(t, m, k); ok {
		t.Errorf("un evento sin jobs tiene el job vivo %q", id)
	}
	terminal := []string{intake.StatusDone, intake.StatusFailed}
	seeded := make([]Row, 0, len(terminal))
	for _, status := range terminal {
		seeded = append(seeded, m.Row(t, seedJob(t, m.Table, m.TenantA, status, loaded, onEvent(k))))
	}
	if id, ok := liveJob(t, m, k); ok {
		t.Errorf("un evento con solo jobs terminales tiene el job vivo %q", id)
	}
	for _, want := range seeded {
		requireSameRow(t, "preguntar por el job vivo: el job "+want.Status, m.Row(t, want.ID), want)
	}
}

// caseLiveJobEachStatus: los TRES estados no terminales cuentan como vivos —la ventana abierta,
// la cerrada y la que un worker está corriendo—, y la pregunta no toca la fila.
func caseLiveJobEachStatus(t *testing.T, m ReanalysisMontaje) {
	for _, status := range []string{intake.StatusAggregating, intake.StatusPending, intake.StatusProcessing} {
		k := newKey(m.TenantA)
		id := seedJob(t, m.Table, m.TenantA, status, loaded, onEvent(k))
		before := m.Row(t, id)
		m.Advance(t)
		if got, ok := liveJob(t, m, k); !ok || got != id {
			t.Errorf("job vivo de un evento con un job %s = (%q, %v), quería (%q, true)", status, got, ok, id)
		}
		requireSameRow(t, "preguntar por el job vivo: el job "+status, m.Row(t, id), before)
	}
}

// caseLiveJobByEventAndTenant: la pregunta es por EVENTO y por TENANT. El job vivo del pipeline
// normal todavía no tiene `intake_id` y se ve igual; el del mismo evento en OTRO tenant y el de
// otro evento del mismo tenant no cuentan.
func caseLiveJobByEventAndTenant(t *testing.T, m ReanalysisMontaje) {
	k := newKey(m.TenantA)
	sameEventOtherTenant := k
	sameEventOtherTenant.TenantID = m.TenantB
	seedJob(t, m.Table, m.TenantB, intake.StatusPending, onEvent(sameEventOtherTenant))
	seedJob(t, m.Table, m.TenantA, intake.StatusPending, loaded) // otro evento del mismo tenant
	if id, ok := liveJob(t, m, k); ok {
		t.Errorf("el evento no tiene jobs en su tenant y devuelve el job vivo %q", id)
	}

	// Sin borrador: es el job que abre el agregador mientras el cliente escribe.
	mine := seedJob(t, m.Table, m.TenantA, intake.StatusAggregating, onEvent(k))
	if m.Row(t, mine).IntakeID != "" {
		t.Fatal("el job sembrado sin borrador tiene intake_id")
	}
	if got, ok := liveJob(t, m, k); !ok || got != mine {
		t.Errorf("job vivo = (%q, %v), quería (%q, true): un job sin intake_id también cuenta", got, ok, mine)
	}
}

// caseLiveJobNewest: si hay más de un job vivo del evento, se devuelve el MÁS RECIENTE, y los
// terminales —aunque sean posteriores— no cuentan.
func caseLiveJobNewest(t *testing.T, m ReanalysisMontaje) {
	now := base(t, m.Table)
	k := newKey(m.TenantA)
	seedJob(t, m.Table, m.TenantA, intake.StatusPending, onEvent(k), createdAt(now.Add(-3*time.Hour)))
	newest := seedJob(t, m.Table, m.TenantA, intake.StatusProcessing, onEvent(k), createdAt(now.Add(-time.Hour)))
	seedJob(t, m.Table, m.TenantA, intake.StatusAggregating, onEvent(k), createdAt(now.Add(-2*time.Hour)))
	seedJob(t, m.Table, m.TenantA, intake.StatusDone, onEvent(k), createdAt(now.Add(-time.Minute)))
	if got, ok := liveJob(t, m, k); !ok || got != newest {
		t.Errorf("job vivo = (%q, %v), quería el más reciente de los no terminales (%q)", got, ok, newest)
	}
}

// caseLiveJobBadCall: sin tenant o sin evento no es «no hay ninguno», es una llamada mal hecha:
// dejarla pasar convertiría la guarda de concurrencia en un sí incondicional.
func caseLiveJobBadCall(t *testing.T, m ReanalysisMontaje) {
	k := newKey(m.TenantA)
	seedJob(t, m.Table, m.TenantA, intake.StatusPending, onEvent(k))
	for name, args := range map[string][2]string{
		"sin tenant": {"", k.EventID}, "sin evento": {k.TenantID, ""}, "sin nada": {"", ""},
	} {
		id, ok, err := m.Store.LiveJobOfEvent(context.Background(), args[0], args[1])
		if err == nil || ok || id != "" {
			t.Errorf("LiveJobOfEvent %s = (%q, %v, %v), quería (\"\", false, error)", name, id, ok, err)
		}
	}
}

// caseOpenReanalysisRow: el job del re-análisis nace `pending` —no `aggregating`—, colgado de
// su solicitud, con las cuatro columnas de su contexto, sin referencias, sin sobre, sin etapa y
// reclamable ya. Y desde que nace es el job vivo de su evento.
func caseOpenReanalysisRow(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	req := ownerRequest(k)
	id, err := m.Store.OpenReanalysis(context.Background(), req)
	if err != nil || id == "" {
		t.Fatalf("OpenReanalysis = (%q, %v), quería un id y nil", id, err)
	}
	got := m.Row(t, id)
	want := Row{
		ID: id, Key: k, Status: intake.StatusPending, IntakeID: req.IntakeID, Reanalysis: req.Context,
		MessageTS: got.MessageTS, CreatedAt: got.CreatedAt, NextAttemptAt: got.NextAttemptAt,
	}
	for _, d := range diffRow(got, want) {
		t.Errorf("el job del re-análisis recién abierto: %s", d)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("(created_at, updated_at) = (%v, %v), quería las dos puestas", got.CreatedAt, got.UpdatedAt)
	}
	if got.NextAttemptAt.After(m.Now(t)) {
		t.Errorf("next_attempt_at = %v, posterior al reloj (%v): el job tiene que ser reclamable ya", got.NextAttemptAt, m.Now(t))
	}
	if live, ok := liveJob(t, m, k); !ok || live != id {
		t.Errorf("job vivo del evento = (%q, %v), quería el recién abierto (%q)", live, ok, id)
	}
	w.requireUntouched(t, m.Table)
}

// caseOpenReanalysisInheritsTS: `message_ts` es la BASE DE FECHAS de P4, así que el re-análisis
// la HEREDA del primer job del evento que la tenga —el creado antes—, no del último ni del
// reloj de hoy. Un job más antiguo SIN instante se salta; los de otro tenant y otro evento no
// cuentan. Los jobs anteriores no se tocan.
func caseOpenReanalysisInheritsTS(t *testing.T, m ReanalysisMontaje) {
	now := base(t, m.Table)
	k := newKey(m.TenantA)
	otherTenant := k
	otherTenant.TenantID = m.TenantB
	ts := func(when time.Time) func(*Row) { return func(r *Row) { r.MessageTS = when } }

	previous := []string{
		seedJob(t, m.Table, m.TenantA, intake.StatusDone, onEvent(k), createdAt(now.Add(-72*time.Hour))), // sin instante
		seedJob(t, m.Table, m.TenantA, intake.StatusDone, onEvent(k), createdAt(now.Add(-48*time.Hour)), ts(firstMessageTS)),
		seedJob(t, m.Table, m.TenantA, intake.StatusFailed, onEvent(k), createdAt(now.Add(-24*time.Hour)), ts(firstMessageTS.Add(24*time.Hour))),
		seedJob(t, m.Table, m.TenantB, intake.StatusDone, onEvent(otherTenant), createdAt(now.Add(-96*time.Hour)), ts(firstMessageTS.Add(-time.Hour))),
		seedJob(t, m.Table, m.TenantA, intake.StatusDone, createdAt(now.Add(-96*time.Hour)), ts(firstMessageTS.Add(-2*time.Hour))),
	}
	before := make([]Row, 0, len(previous))
	for _, id := range previous {
		before = append(before, m.Row(t, id))
	}

	id, err := m.Store.OpenReanalysis(context.Background(), ownerRequest(k))
	if err != nil {
		t.Fatalf("OpenReanalysis: error inesperado %v", err)
	}
	if got := m.Row(t, id).MessageTS; !got.Equal(firstMessageTS) {
		t.Errorf("message_ts del re-análisis = %v, quería el del primer job del evento que lo tiene (%v)", got, firstMessageTS)
	}
	for _, want := range before {
		requireSameRow(t, "un job anterior, tras abrir el re-análisis", m.Row(t, want.ID), want)
	}
}

// caseOpenReanalysisWithoutHistory: la solicitud que no nació de un job (la del carrito) no
// tiene de quién heredar: `message_ts` es el reloj de la implementación. Y un From 0 —no había
// revisión anterior— se guarda como «sin valor», no como la revisión cero.
func caseOpenReanalysisWithoutHistory(t *testing.T, m ReanalysisMontaje) {
	k := newKey(m.TenantA)
	req := ownerRequest(k)
	req.Context.From = 0
	earliest := m.Now(t)
	id, err := m.Store.OpenReanalysis(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenReanalysis: error inesperado %v", err)
	}
	latest := m.Now(t)
	got := m.Row(t, id)
	// Postgres guarda microsegundos: el margen de un milisegundo absorbe el redondeo.
	if got.MessageTS.Before(earliest.Add(-time.Millisecond)) || got.MessageTS.After(latest.Add(time.Millisecond)) {
		t.Errorf("message_ts = %v, quería el reloj de la implementación (entre %v y %v)", got.MessageTS, earliest, latest)
	}
	if got.Reanalysis != req.Context || got.Reanalysis.From != 0 {
		t.Errorf("contexto guardado = %+v, quería %+v (From 0)", got.Reanalysis, req.Context)
	}
}

// caseOpenReanalysisTwice: NO es idempotente, y no puede serlo: dos re-análisis del mismo
// pedido son dos actos y dejan dos jobs. Quien impide el duplicado accidental es
// LiveJobOfEvent, que se pregunta antes.
func caseOpenReanalysisTwice(t *testing.T, m ReanalysisMontaje) {
	k := newKey(m.TenantA)
	req := ownerRequest(k)
	first, err := m.Store.OpenReanalysis(context.Background(), req)
	if err != nil {
		t.Fatalf("primer OpenReanalysis: error inesperado %v", err)
	}
	before := m.Row(t, first)
	m.Advance(t)
	second, err := m.Store.OpenReanalysis(context.Background(), req)
	if err != nil {
		t.Fatalf("segundo OpenReanalysis: error inesperado %v", err)
	}
	if second == "" || second == first {
		t.Fatalf("segundo OpenReanalysis devolvió %q, quería un id nuevo distinto de %q", second, first)
	}
	requireSameRow(t, "el primer job, tras abrir el segundo", m.Row(t, first), before)
	if got := m.Row(t, second); got.Status != intake.StatusPending || got.Key != k {
		t.Errorf("segundo job = (status %q, clave %+v), quería (pending, %+v)", got.Status, got.Key, k)
	}
}

// caseOpenReanalysisIncomplete: sin la clave de ventana entera, sin solicitud o sin un contexto
// del DUEÑO, no se abre nada: error, id vacío y ningún job vivo para el evento.
func caseOpenReanalysisIncomplete(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	withoutEvent, withoutIntake, notOwner, anotherRole := ownerRequest(k), ownerRequest(k), ownerRequest(k), ownerRequest(k)
	withoutEvent.Key.EventID = ""
	withoutIntake.IntakeID = ""
	notOwner.Context.RequestedBy = ""
	anotherRole.Context.RequestedBy = "system"
	for name, req := range map[string]intake.ReanalysisRequest{
		"sin evento": withoutEvent, "sin solicitud": withoutIntake,
		"sin rol": notOwner, "con otro rol": anotherRole, "vacía": {},
	} {
		id, err := m.Store.OpenReanalysis(context.Background(), req)
		if err == nil || id != "" {
			t.Errorf("OpenReanalysis con una petición %s = (%q, %v), quería (\"\", error)", name, id, err)
		}
	}
	if id, ok := liveJob(t, m, k); ok {
		t.Errorf("una petición rechazada dejó el job %q", id)
	}
	w.requireUntouched(t, m.Table)
}
