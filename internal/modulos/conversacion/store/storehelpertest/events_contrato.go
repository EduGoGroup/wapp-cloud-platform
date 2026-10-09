package storehelpertest

import (
	"testing"

	"github.com/google/uuid"
)

// Los casos de las dos tablas append-only: FlowEventStore (flow_events) y SurveyResultStore
// (survey_results).

// caseInsertFlowEvent: cada efecto se añade al final con sus siete campos, en el orden en que
// llegó, y solo al outbox de su tenant. El nombre es texto libre (migración 0009).
func caseInsertFlowEvent(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	witness := FlowEvent{TenantID: m.TenantB, ContactID: contact, FlowID: flowMenu, FlowVersion: 1,
		Kind: "event", Name: "ajeno", Payload: map[string]any{"k": "v"}}
	if err := m.Store.InsertFlowEvent(ctx, witness); err != nil {
		t.Fatalf("InsertFlowEvent del testigo: %v", err)
	}
	before := take(t, m)

	events := []FlowEvent{
		{TenantID: m.TenantA, ContactID: contact, FlowID: flowMenu, FlowVersion: 2, Kind: "persist",
			Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer": "si", "n": 2}},
		{TenantID: m.TenantA, ContactID: uuid.NewString(), FlowID: flowOrder, FlowVersion: 5, Kind: "event",
			Name: "event_custom/ñandú", Payload: map[string]any{"nested": map[string]any{"ok": true}}},
		{TenantID: m.TenantA, ContactID: contact, FlowID: flowMenu, FlowVersion: 2, Kind: "persist",
			Name: "survey_answer", Payload: map[string]any{"question_id": "q2", "answer": "no"}},
	}
	for i, ev := range events {
		if err := m.Store.InsertFlowEvent(ctx, ev); err != nil {
			t.Fatalf("InsertFlowEvent nº %d: %v", i, err)
		}
	}

	requireSameFlowEvents(t, "efectos del tenant", m.FlowEvents(t, m.TenantA), events)
	requireRestUntouched(t, "InsertFlowEvent", before, take(t, m), forgetFlowEvents(m.TenantA))
}

// caseInsertFlowEventPayload: un efecto sin payload se guarda con el cuerpo vacío, y el payload
// que se guarda es una copia: mutar el mapa del llamante después no cambia lo guardado.
func caseInsertFlowEventPayload(t *testing.T, m Montaje) {
	contact := uuid.NewString()
	empty := FlowEvent{TenantID: m.TenantA, ContactID: contact, FlowID: flowMenu, FlowVersion: 1,
		Kind: "event", Name: "sin_payload"}
	payload := map[string]any{"answer": "si"}
	withPayload := FlowEvent{TenantID: m.TenantA, ContactID: contact, FlowID: flowMenu, FlowVersion: 1,
		Kind: "persist", Name: "survey_answer", Payload: payload}
	for _, ev := range []FlowEvent{empty, withPayload} {
		if err := m.Store.InsertFlowEvent(ctx, ev); err != nil {
			t.Fatalf("InsertFlowEvent(%s): %v", ev.Name, err)
		}
	}
	payload["answer"] = "mutado tras guardar"
	payload["extra"] = 1

	got := m.FlowEvents(t, m.TenantA)
	if len(got) != 2 {
		t.Fatalf("%d efectos, quería 2: %+v", len(got), got)
	}
	if len(got[0].Payload) != 0 {
		t.Errorf("el efecto sin payload se guardó con %v, quería el cuerpo vacío", got[0].Payload)
	}
	if want := map[string]any{"answer": "si"}; !sameMap(t, got[1].Payload, want) {
		t.Errorf("payload guardado = %v tras mutar el del llamante, quería %v", got[1].Payload, want)
	}
}

// answer es una respuesta de encuesta de ese contacto en ese flujo, ligada a ese evento.
func answer(tenant, contact, flowID, question, code, eventID string) SurveyResult {
	return SurveyResult{TenantID: tenant, ContactID: contact, FlowID: flowID, FlowVersion: 1,
		QuestionID: question, AnswerCode: code, EventID: eventID}
}

// mustInsertResults guarda la tanda o falla el test.
func mustInsertResults(t *testing.T, m Montaje, rows ...SurveyResult) {
	t.Helper()
	if err := m.Store.InsertResults(ctx, rows); err != nil {
		t.Fatalf("InsertResults (%d filas): %v", len(rows), err)
	}
}

// caseInsertResultsEmpty: una tanda vacía —nil o sin filas— es un no-op: ni error ni escritura.
func caseInsertResultsEmpty(t *testing.T, m Montaje) {
	mustInsertResults(t, m, answer(m.TenantA, uuid.NewString(), flowMenu, "q1", "si", m.NewEvent(t, m.TenantA)))
	before := take(t, m)
	if err := m.Store.InsertResults(ctx, nil); err != nil {
		t.Errorf("InsertResults(nil) = %v, quería nil", err)
	}
	if err := m.Store.InsertResults(ctx, []SurveyResult{}); err != nil {
		t.Errorf("InsertResults de una tanda sin filas = %v, quería nil", err)
	}
	requireUntouched(t, "InsertResults vacío", before, take(t, m))
}

// caseInsertResults: una tanda se guarda entera, en su orden, detrás de lo que hubiera
// (append-only), cada fila con sus siete campos —el evento incluido— y fechada por la
// implementación. Un mismo evento puede ser el padre de varias respuestas.
func caseInsertResults(t *testing.T, m Montaje) {
	contact, event := uuid.NewString(), m.NewEvent(t, m.TenantA)
	mustInsertResults(t, m, answer(m.TenantB, contact, flowMenu, "q1", "ajena", m.NewEvent(t, m.TenantB)))
	first := make([]SurveyResult, 0, 4) // las tres de la tanda y, al final, la posterior.
	first = append(first,
		answer(m.TenantA, contact, flowMenu, "q1", "si", event),
		answer(m.TenantA, contact, flowMenu, "q2", "no", event),
		answer(m.TenantA, uuid.NewString(), flowMenu, "q1", "si", event),
	)
	first[1].FlowVersion = 4
	t0 := m.Now(t)
	mustInsertResults(t, m, first...)
	t1 := m.Now(t)
	stored := m.SurveyResults(t, m.TenantA)
	if len(stored) != len(first) {
		t.Fatalf("%d respuestas tras la primera tanda, quería %d: %+v", len(stored), len(first), stored)
	}
	for i := range stored {
		requireStamped(t, "InsertResults: CreatedAt", stored[i].CreatedAt, t0, t1)
		first[i].CreatedAt = stored[i].CreatedAt
	}
	requireSameSurveyResults(t, "primera tanda", stored, first)

	m.Advance(t)
	before := take(t, m)
	later := answer(m.TenantA, contact, flowOrder, "q9", "tal vez", m.NewEvent(t, m.TenantA))
	mustInsertResults(t, m, later)

	stored = m.SurveyResults(t, m.TenantA)
	if len(stored) != len(first)+1 {
		t.Fatalf("%d respuestas tras la segunda tanda, quería %d: %+v", len(stored), len(first)+1, stored)
	}
	last := stored[len(stored)-1]
	if !last.CreatedAt.After(first[0].CreatedAt) {
		t.Errorf("la respuesta posterior lleva CreatedAt %v, quería posterior a %v", last.CreatedAt, first[0].CreatedAt)
	}
	later.CreatedAt = last.CreatedAt
	requireSameSurveyResults(t, "las dos tandas", stored, append(first, later))
	requireRestUntouched(t, "InsertResults", before, take(t, m), forgetSurveyResults(m.TenantA))
}

// caseListResults: ListResults devuelve lo que ESE contacto respondió en ESE flujo de ESE tenant,
// en orden cronológico —y, dentro de una misma tanda, en el orden en que se escribió—, con la
// versión del flujo y la fecha de cada fila. Sin respuestas, la lista vacía sin error.
//
// No se mira el EventID de lo que devuelve: el adaptador Postgres no lo lee en esta consulta.
func caseListResults(t *testing.T, m Montaje) {
	contact, other := uuid.NewString(), uuid.NewString()
	if got, err := m.Store.ListResults(ctx, m.TenantA, contact, flowMenu); err != nil || len(got) != 0 {
		t.Fatalf("ListResults sin respuestas = (%+v, %v), quería vacía sin error", got, err)
	}

	eventA, eventB := m.NewEvent(t, m.TenantA), m.NewEvent(t, m.TenantB)
	// Una tanda con las tres cosas que NO son suyas intercaladas: otro contacto, otro flujo y otro
	// tenant con el mismo contacto y el mismo flujo.
	mustInsertResults(t, m,
		answer(m.TenantA, contact, flowMenu, "q1", "a", eventA),
		answer(m.TenantA, other, flowMenu, "q1", "de otro contacto", eventA),
		answer(m.TenantA, contact, flowMenu, "q2", "b", eventA),
		answer(m.TenantA, contact, flowOrder, "q1", "de otro flujo", eventA),
		answer(m.TenantB, contact, flowMenu, "q1", "de otro tenant", eventB),
	)
	m.Advance(t)
	// La misma pregunta respondida otra vez, más tarde y con otra versión del flujo: sale al final.
	again := answer(m.TenantA, contact, flowMenu, "q1", "c", m.NewEvent(t, m.TenantA))
	again.FlowVersion = 2
	mustInsertResults(t, m, again)

	got, err := m.Store.ListResults(ctx, m.TenantA, contact, flowMenu)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	want := []SurveyResult{
		answer(m.TenantA, contact, flowMenu, "q1", "a", ""),
		answer(m.TenantA, contact, flowMenu, "q2", "b", ""),
		again,
	}
	if len(got) != len(want) {
		t.Fatalf("ListResults = %d respuestas, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !sameAnswer(got[i], want[i]) {
			t.Errorf("ListResults[%d] = %+v, quería %+v", i, got[i], want[i])
		}
		if got[i].CreatedAt.IsZero() {
			t.Errorf("ListResults[%d] trae CreatedAt cero", i)
		}
		if i > 0 && got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Errorf("ListResults no es cronológico: [%d] = %v es anterior a [%d] = %v",
				i, got[i].CreatedAt, i-1, got[i-1].CreatedAt)
		}
	}
	if !got[2].CreatedAt.After(got[1].CreatedAt) {
		t.Errorf("la respuesta posterior lleva CreatedAt %v, quería posterior a %v", got[2].CreatedAt, got[1].CreatedAt)
	}
	if got, err := m.Store.ListResults(ctx, m.TenantB, other, flowMenu); err != nil || len(got) != 0 {
		t.Errorf("ListResults de un contacto sin respuestas en ese tenant = (%+v, %v), quería vacía sin error", got, err)
	}
}
