package intakehelpertest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Los casos del SEGUNDO PRODUCTOR de jobs. Sus filas están en reanalysisCases (contrato.go).

// incompleteReanalysisRequestText es el texto con el que OpenReanalysis rechaza una petición
// incompleta. Observable y LITERAL: el mismo en intake.Postgres y en MachineMemory. Dice QUÉ
// falta sin volcar la clave de ventana, que lleva el contacto.
const incompleteReanalysisRequestText = "intake: solicitud de re-análisis incompleta (ventana=%t intake=%t dueño=%t)"

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
// su solicitud, con las cuatro columnas de su contexto, sin referencias, sin etapa y reclamable
// ya. Y desde que nace es el job vivo de su evento. La petición trae el sobre VACÍO ENTERO —el
// hilo sin mensajes—, que es legítimo: el job nace igual, con las tres columnas del sobre vacías.
func caseOpenReanalysisRow(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	req := ownerRequest(k)
	if !req.SourceText.Empty() {
		t.Fatalf("la petición del caso trae sobre (%+v); quería el vacío entero", req.SourceText)
	}
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
	if !got.SourceText.Empty() {
		t.Errorf("el sobre del job abierto sin sobre = %+v, quería las tres columnas vacías", got.SourceText)
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

// caseOpenReanalysisRequestBeforeEnvelope: LA PETICIÓN SE MIRA ANTES QUE EL SOBRE. Con las dos
// cosas mal —petición incompleta y sobre a medias—, el error es el de la petición, con su texto,
// y no se abre nada.
func caseOpenReanalysisRequestBeforeEnvelope(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	withoutEvent, withoutIntake, notOwner := ownerRequest(k), ownerRequest(k), ownerRequest(k)
	withoutEvent.Key.EventID = ""
	withoutIntake.IntakeID = ""
	notOwner.Context.RequestedBy = ""
	broken := map[string]intake.ReanalysisRequest{
		"sin evento": withoutEvent, "sin solicitud": withoutIntake, "sin rol": notOwner, "vacía": {},
	}
	envelopes := map[string]intake.SourceText{"completo": envelope("stray")}
	for name, env := range halfEnvelopes() {
		envelopes[name] = env
	}
	for what, req := range broken {
		want := fmt.Sprintf(incompleteReanalysisRequestText, req.Key.Valid(), req.IntakeID != "", req.Context.IsFromOwner())
		for name, env := range envelopes {
			req.SourceText = env
			id, err := m.Store.OpenReanalysis(context.Background(), req)
			if id != "" || err == nil || err.Error() != want {
				t.Errorf("OpenReanalysis con una petición %s y un sobre %s = (%q, %v), quería (\"\", %q)", what, name, id, err, want)
			}
		}
	}
	if id, ok := liveJob(t, m, k); ok {
		t.Errorf("una petición rechazada dejó el job %q", id)
	}
	w.requireUntouched(t, m.Table)
}

// caseOpenReanalysisWithEnvelope es LA CARRERA que T8.40 cierra (D-F7-9, D-F8-13): el job NACE
// con su sobre. Con un sobre completo en la petición, la fila que deja la llamada —leída sin
// ninguna otra llamada en medio— ya está `pending` y con las TRES columnas exactas: no hay un
// instante en que el worker pueda reclamarla sin literal. Y el sobre va a ESA fila y solo a ella:
// una `pending` anterior del mismo evento, que sigue sin sobre, no recibe el texto de otro job.
func caseOpenReanalysisWithEnvelope(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	older := m.Row(t, seedJob(t, m.Table, m.TenantA, intake.StatusPending, onEvent(k)))
	if !older.SourceText.Empty() {
		t.Fatalf("la `pending` anterior se sembró con sobre: %+v", older.SourceText)
	}
	req := ownerRequest(k)
	req.SourceText = envelope("born")

	id, err := m.Store.OpenReanalysis(context.Background(), req)
	if err != nil || id == "" {
		t.Fatalf("OpenReanalysis con su sobre = (%q, %v), quería un id y nil", id, err)
	}
	got := m.Row(t, id)
	want := Row{
		ID: id, Key: k, Status: intake.StatusPending, IntakeID: req.IntakeID, Reanalysis: req.Context,
		SourceText: envelope("born"),
		MessageTS:  got.MessageTS, CreatedAt: got.CreatedAt, NextAttemptAt: got.NextAttemptAt,
	}
	for _, d := range diffRow(got, want) {
		t.Errorf("el job del re-análisis recién abierto con su sobre: %s", d)
	}
	if !got.SourceText.Complete() {
		t.Errorf("el job nació con el sobre %+v, quería las tres piezas desde el INSERT", got.SourceText)
	}
	if got.NextAttemptAt.After(m.Now(t)) {
		t.Errorf("next_attempt_at = %v, posterior al reloj (%v): el job tiene que ser reclamable ya", got.NextAttemptAt, m.Now(t))
	}
	requireSameRow(t, "la `pending` anterior del mismo evento, sin sobre", m.Row(t, older.ID), older)
	w.requireUntouched(t, m.Table)
}

// caseOpenReanalysisHalfEnvelope: COMPLETO O VACÍO ENTERO. Un sobre al que le falta una de sus
// tres piezas se rechaza con error ANTES de escribir: no se abre ningún job —el evento sigue sin
// job vivo— y no queda media fila.
func caseOpenReanalysisHalfEnvelope(t *testing.T, m ReanalysisMontaje) {
	w := seedWitnesses(t, m.Table)
	k := newKey(m.TenantA)
	for name, env := range halfEnvelopes() {
		req := ownerRequest(k)
		req.SourceText = env
		id, err := m.Store.OpenReanalysis(context.Background(), req)
		if err == nil || id != "" {
			t.Errorf("OpenReanalysis con un sobre %s = (%q, %v), quería (\"\", error)", name, id, err)
		}
		if live, ok := liveJob(t, m, k); ok {
			t.Errorf("la petición con un sobre %s, rechazada o no, dejó el job %q", name, live)
		}
	}
	w.requireUntouched(t, m.Table)
}
