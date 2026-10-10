package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// summaryStoreFake es un SummaryStore que devuelve lo que se le dé y apunta lo que le
// piden, para afirmar también lo que NO se consulta.
type summaryStoreFake struct {
	intake    store.Intake
	found     bool
	intakeErr error
	items     []store.IntakeItem
	itemsErr  error
	results   []store.SurveyResult
	resultErr error

	openAsked    []string
	itemsAsked   []string
	resultsAsked []string
}

func (f *summaryStoreFake) GetOpenIntake(_ context.Context, tenantID, contactID string) (store.Intake, bool, error) {
	f.openAsked = append(f.openAsked, tenantID+"|"+contactID)
	return f.intake, f.found, f.intakeErr
}

func (f *summaryStoreFake) ListIntakeItems(_ context.Context, intakeID string) ([]store.IntakeItem, error) {
	f.itemsAsked = append(f.itemsAsked, intakeID)
	return f.items, f.itemsErr
}

func (f *summaryStoreFake) ListResults(_ context.Context, tenantID, contactID, flowID string) ([]store.SurveyResult, error) {
	f.resultsAsked = append(f.resultsAsked, tenantID+"|"+contactID+"|"+flowID)
	return f.results, f.resultErr
}

// Aserciones de compilación: quién satisface el puerto.
var (
	_ SummaryStore = (*store.PostgresRepository)(nil)
	_ SummaryStore = (*store.MemoryRepository)(nil)
	_ SummaryStore = (*summaryStoreFake)(nil)
)

// summaryBirth es el nacimiento del evento de los tests de encuesta. Las filas se fechan
// respecto a él, sobre la MISMA base: lo que se mide es la cota, no un desfase de relojes.
var summaryBirth = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// openIntakeStore es un almacén con una solicitud abierta de `sessionID` y dos líneas.
func openIntakeStore(sessionID string) *summaryStoreFake {
	return &summaryStoreFake{
		found:  true,
		intake: store.Intake{ID: "intake-1", TenantID: "t-1", ContactID: "c-1", SessionID: sessionID, Status: "open"},
		items: []store.IntakeItem{
			{IntakeID: "intake-1", SKU: "COFFEE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
			{IntakeID: "intake-1", SKU: "TEA", Label: "Té", Qty: 1, UnitPrice: 5},
		},
	}
}

func requireAsked(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: consultas = %v, quería %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: consultas = %v, quería %v", what, got, want)
		}
	}
}

// requireSameError exige el error del almacén TAL CUAL: el mismo en la cadena y con el
// mismo texto, es decir, sin un prefijo que lo envuelva.
func requireSameError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, quería el del almacén (%v)", got, want)
	}
	if got.Error() != want.Error() {
		t.Fatalf("error = %q, quería %q sin envolver", got.Error(), want.Error())
	}
}

// TestNewSummarySources_BuildsBothReaders: las dos fuentes salen juntas; media fuente
// deja la mitad de los abandonos sin fila.
func TestNewSummarySources_BuildsBothReaders(t *testing.T) {
	src := NewSummarySources(&summaryStoreFake{})
	if src.Lines == nil {
		t.Error("Lines es nil: el resumen de un pedido fallaría con ErrNoIntakeLineReader")
	}
	if src.Answers == nil {
		t.Error("Answers es nil: el resumen de una encuesta fallaría con ErrNoSurveyAnswerReader")
	}
}

// TestNewSummarySources_DoesNotQueryOnBuild: construir no consulta el almacén.
func TestNewSummarySources_DoesNotQueryOnBuild(t *testing.T) {
	fake := &summaryStoreFake{}
	_ = NewSummarySources(fake)
	if n := len(fake.openAsked) + len(fake.itemsAsked) + len(fake.resultsAsked); n != 0 {
		t.Errorf("construir las fuentes hizo %d consultas, quería 0", n)
	}
}

// TestOpenIntakeLines_ReturnsLinesOfItsSession: el camino normal. Se pide la solicitud por
// (tenant, contacto), las líneas por el id de la solicitud, y cada línea se copia entera
// y en orden — la personalización incluida (D-041.17).
func TestOpenIntakeLines_ReturnsLinesOfItsSession(t *testing.T) {
	fake := openIntakeStore("s-1")

	lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-1", "c-1")
	if err != nil {
		t.Fatalf("OpenIntakeLines devolvió %v", err)
	}

	requireAsked(t, "GetOpenIntake", fake.openAsked, "t-1|c-1")
	requireAsked(t, "ListIntakeItems", fake.itemsAsked, "intake-1")
	want := []events.SummaryLine{
		{SKU: "COFFEE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
		{SKU: "TEA", Label: "Té", Qty: 1, UnitPrice: 5},
	}
	if len(lines) != len(want) {
		t.Fatalf("llegaron %d líneas, quería %d: %+v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("línea %d = %+v, quería %+v", i, lines[i], want[i])
		}
	}
}

// TestOpenIntakeLines_OtherSessionIsNotSummarized es REQ-18: la solicitud abierta del
// contacto es de otra sesión, así que no hay nada que resumir, sin error y sin leer
// sus líneas.
func TestOpenIntakeLines_OtherSessionIsNotSummarized(t *testing.T) {
	fake := openIntakeStore("s-A")

	lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-B", "c-1")
	if err != nil {
		t.Fatalf("OpenIntakeLines devolvió %v; un pedido de otra sesión no es un error", err)
	}
	if len(lines) != 0 {
		t.Errorf("el pedido de otra sesión NO se resume (REQ-18); llegaron %d líneas: %+v", len(lines), lines)
	}
	requireAsked(t, "ListIntakeItems", fake.itemsAsked)
}

// TestOpenIntakeLines_NoOpenIntakeIsNotAnError: no tener pedido abierto es lo normal.
func TestOpenIntakeLines_NoOpenIntakeIsNotAnError(t *testing.T) {
	fake := &summaryStoreFake{found: false}

	lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-1", "c-none")
	if err != nil {
		t.Fatalf("OpenIntakeLines devolvió %v; sin solicitud abierta no es un error", err)
	}
	if len(lines) != 0 {
		t.Errorf("sin solicitud abierta llegaron %d líneas: %+v", len(lines), lines)
	}
	requireAsked(t, "ListIntakeItems", fake.itemsAsked)
}

// TestOpenIntakeLines_IntakeWithoutItemsIsEmpty: una solicitud de su sesión sin líneas da
// una lista vacía, sin error.
func TestOpenIntakeLines_IntakeWithoutItemsIsEmpty(t *testing.T) {
	fake := openIntakeStore("s-1")
	fake.items = nil

	lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-1", "c-1")
	if err != nil {
		t.Fatalf("OpenIntakeLines devolvió %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("una solicitud sin líneas dio %d: %+v", len(lines), lines)
	}
}

// TestOpenIntakeLines_PropagatesStoreErrors: los dos fallos del almacén salen tal cual,
// sin envolver y sin líneas; si falla la solicitud, las líneas ni se piden.
func TestOpenIntakeLines_PropagatesStoreErrors(t *testing.T) {
	errIntake := errors.New("intake read failed")
	errItems := errors.New("items read failed")

	t.Run("GetOpenIntake fails", func(t *testing.T) {
		fake := openIntakeStore("s-1")
		fake.intakeErr = errIntake

		lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-1", "c-1")
		requireSameError(t, err, errIntake)
		if lines != nil {
			t.Errorf("con error llegaron líneas: %+v", lines)
		}
		requireAsked(t, "ListIntakeItems", fake.itemsAsked)
	})

	t.Run("ListIntakeItems fails", func(t *testing.T) {
		fake := openIntakeStore("s-1")
		fake.itemsErr = errItems

		lines, err := NewSummarySources(fake).Lines.OpenIntakeLines(context.Background(), "t-1", "s-1", "c-1")
		requireSameError(t, err, errItems)
		if lines != nil {
			t.Errorf("con error llegaron líneas: %+v", lines)
		}
	})
}

// surveyEvent es el evento de encuesta cuyo nacimiento acota las respuestas.
func surveyEvent() events.Event {
	return events.Event{
		ID: "event-now", TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "survey",
		FlowID: "flow-survey", FlowVersion: 2, CreatedAt: summaryBirth,
	}
}

// surveyRow es una fila de survey_results escrita `offset` después (o antes, si es
// negativo) del nacimiento del evento.
func surveyRow(questionID, answerCode string, offset time.Duration) store.SurveyResult {
	return store.SurveyResult{
		TenantID: "t-1", ContactID: "c-1", FlowID: "flow-survey", FlowVersion: 2,
		QuestionID: questionID, AnswerCode: answerCode, CreatedAt: summaryBirth.Add(offset),
	}
}

func requireAnswers(t *testing.T, got []events.SummaryAnswer, want ...events.SummaryAnswer) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("respuestas = %+v, quería %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("respuestas = %+v, quería %+v", got, want)
		}
	}
}

// TestSurveyAnswers_AsksByTenantContactAndFlow: los resultados se piden con el tenant, el
// contacto y el flujo DEL EVENTO, y cada fila da su pregunta y su código, en orden.
func TestSurveyAnswers_AsksByTenantContactAndFlow(t *testing.T) {
	fake := &summaryStoreFake{results: []store.SurveyResult{
		surveyRow("q2", "b", time.Minute),
		surveyRow("q1", "a", 2*time.Minute),
	}}

	answers, err := NewSummarySources(fake).Answers.SurveyAnswers(context.Background(), surveyEvent())
	if err != nil {
		t.Fatalf("SurveyAnswers devolvió %v", err)
	}

	requireAsked(t, "ListResults", fake.resultsAsked, "t-1|c-1|flow-survey")
	requireAnswers(t, answers,
		events.SummaryAnswer{QuestionID: "q2", AnswerCode: "b"},
		events.SummaryAnswer{QuestionID: "q1", AnswerCode: "a"},
	)
}

// TestSurveyAnswers_DropsRowsOlderThanTheEvent: la cota por fecha. Lo escrito ANTES de que
// el evento naciera es de otra pasada y se descarta; lo del mismo instante y lo posterior
// se conserva (corte estricto).
func TestSurveyAnswers_DropsRowsOlderThanTheEvent(t *testing.T) {
	fake := &summaryStoreFake{results: []store.SurveyResult{
		surveyRow("q-last-month", "x", -30*24*time.Hour),
		surveyRow("q-just-before", "x", -time.Nanosecond),
		surveyRow("q-same-instant", "a", 0),
		surveyRow("q-after", "b", time.Second),
	}}

	answers, err := NewSummarySources(fake).Answers.SurveyAnswers(context.Background(), surveyEvent())
	if err != nil {
		t.Fatalf("SurveyAnswers devolvió %v", err)
	}

	requireAnswers(t, answers,
		events.SummaryAnswer{QuestionID: "q-same-instant", AnswerCode: "a"},
		events.SummaryAnswer{QuestionID: "q-after", AnswerCode: "b"},
	)
}

// TestSurveyAnswers_DoesNotFilterByEventSessionOrVersion: la limitación del legado, tal
// cual. Una fila posterior al nacimiento se conserva aunque su event_id sea de otro
// evento, esté vacío o su versión de flujo sea otra; y no se reduce a una por pregunta.
func TestSurveyAnswers_DoesNotFilterByEventSessionOrVersion(t *testing.T) {
	otherEvent := surveyRow("q1", "a", time.Minute)
	otherEvent.EventID = "event-other"
	legacy := surveyRow("q1", "b", 2*time.Minute)
	otherVersion := surveyRow("q2", "c", 3*time.Minute)
	otherVersion.FlowVersion = 9
	fake := &summaryStoreFake{results: []store.SurveyResult{otherEvent, legacy, otherVersion}}

	answers, err := NewSummarySources(fake).Answers.SurveyAnswers(context.Background(), surveyEvent())
	if err != nil {
		t.Fatalf("SurveyAnswers devolvió %v", err)
	}

	requireAnswers(t, answers,
		events.SummaryAnswer{QuestionID: "q1", AnswerCode: "a"},
		events.SummaryAnswer{QuestionID: "q1", AnswerCode: "b"},
		events.SummaryAnswer{QuestionID: "q2", AnswerCode: "c"},
	)
}

// TestSurveyAnswers_NoRowsIsEmpty: sin respuestas, lista vacía y sin error.
func TestSurveyAnswers_NoRowsIsEmpty(t *testing.T) {
	answers, err := NewSummarySources(&summaryStoreFake{}).Answers.SurveyAnswers(context.Background(), surveyEvent())
	if err != nil {
		t.Fatalf("SurveyAnswers devolvió %v", err)
	}
	requireAnswers(t, answers)
}

// TestSurveyAnswers_PropagatesStoreError: el fallo del almacén sale tal cual y sin
// respuestas.
func TestSurveyAnswers_PropagatesStoreError(t *testing.T) {
	errResults := errors.New("results read failed")
	fake := &summaryStoreFake{resultErr: errResults, results: []store.SurveyResult{surveyRow("q1", "a", time.Minute)}}

	answers, err := NewSummarySources(fake).Answers.SurveyAnswers(context.Background(), surveyEvent())
	requireSameError(t, err, errResults)
	if answers != nil {
		t.Errorf("con error llegaron respuestas: %+v", answers)
	}
}
