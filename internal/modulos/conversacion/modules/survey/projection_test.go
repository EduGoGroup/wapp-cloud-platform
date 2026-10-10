package survey_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// spyStore captura lo que el proyector manda a InsertResults y devuelve el error fijado.
type spyStore struct {
	calls int
	rows  []store.SurveyResult
	err   error
}

func (s *spyStore) InsertResults(_ context.Context, rows []store.SurveyResult) error {
	s.calls++
	s.rows = append(s.rows, rows...)
	return s.err
}

// Aserciones de compilación: el proyector es un modules.Projector, y tanto el espía
// como el gemelo en memoria del almacén sirven de survey.ResultStore.
var (
	_ modules.Projector  = (*survey.Projector)(nil)
	_ survey.ResultStore = (*spyStore)(nil)
	_ survey.ResultStore = (*store.MemoryRepository)(nil)
)

func surveyMeta() modules.EffectMeta {
	return modules.EffectMeta{
		TenantID:    "tenant-1",
		ContactID:   "contact-opaque",
		SessionID:   "session-1",
		FlowID:      "encuesta",
		FlowVersion: 3,
		EventID:     "event-1",
	}
}

// Handles reconoce solo su efecto, por nombre exacto.
func TestProjector_Handles(t *testing.T) {
	if survey.EffectSurveyAnswer != "survey_answer" {
		t.Fatalf("EffectSurveyAnswer = %q, quiero \"survey_answer\"", survey.EffectSurveyAnswer)
	}
	p := survey.NewProjector(&spyStore{})
	cases := []struct {
		name string
		want bool
	}{
		{name: "survey_answer", want: true},
		{name: "Survey_Answer", want: false},
		{name: " survey_answer ", want: false},
		{name: "item_added", want: false},
		{name: "", want: false},
	}
	for _, tc := range cases {
		if got := p.Handles(tc.name); got != tc.want {
			t.Errorf("Handles(%q) = %v, quiero %v", tc.name, got, tc.want)
		}
	}
}

// La fila declara a su padre: sale con la identidad de la meta (sin SessionID), el
// event_id del turno y la pregunta/respuesta del payload.
func TestProjector_Project_WritesOneRowDeclaringItsEvent(t *testing.T) {
	spy := &spyStore{}
	p := survey.NewProjector(spy)
	eff := modules.Effect{
		Kind:    "persist",
		Name:    survey.EffectSurveyAnswer,
		Payload: map[string]any{"question_id": "q1", "answer_code": "si"},
	}
	if err := p.Project(context.Background(), surveyMeta(), eff); err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := store.SurveyResult{
		TenantID:    "tenant-1",
		ContactID:   "contact-opaque",
		FlowID:      "encuesta",
		FlowVersion: 3,
		QuestionID:  "q1",
		AnswerCode:  "si",
		EventID:     "event-1",
	}
	if len(spy.rows) != 1 || spy.rows[0] != want {
		t.Errorf("filas = %+v, quiero [%+v]", spy.rows, want)
	}
}

// El efecto que Step declara es el que Project materializa: el contrato entre los dos
// ficheros del paquete.
func TestProjector_Project_MaterializesTheEffectStepDeclares(t *testing.T) {
	node := model.Node{Type: model.NodeTypeSurveyQuestion, QuestionID: "q7", Options: map[string]string{"2": "next"}}
	res := survey.New().Step(node, model.Conversation{}, "2")
	if len(res.Effects) != 1 {
		t.Fatalf("Effects = %+v, quiero uno", res.Effects)
	}
	spy := &spyStore{}
	p := survey.NewProjector(spy)
	if !p.Handles(res.Effects[0].Name) {
		t.Fatalf("Handles(%q) = false, quiero true", res.Effects[0].Name)
	}
	if err := p.Project(context.Background(), surveyMeta(), res.Effects[0]); err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(spy.rows) != 1 || spy.rows[0].QuestionID != "q7" || spy.rows[0].AnswerCode != "2" {
		t.Errorf("filas = %+v, quiero la respuesta 2 a q7", spy.rows)
	}
}

// Payload malformado ⇒ se omite: nil y ni una llamada al almacén.
func TestProjector_Project_SkipsMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{name: "nil payload", payload: nil},
		{name: "missing answer_code", payload: map[string]any{"question_id": "q1"}},
		{name: "missing question_id", payload: map[string]any{"answer_code": "1"}},
		{name: "question_id of another type", payload: map[string]any{"question_id": 7, "answer_code": "1"}},
		{name: "answer_code of another type", payload: map[string]any{"question_id": "q1", "answer_code": 1.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyStore{err: errors.New("no debía llamarse")}
			p := survey.NewProjector(spy)
			err := p.Project(context.Background(), surveyMeta(), modules.Effect{Name: survey.EffectSurveyAnswer, Payload: tc.payload})
			if err != nil || spy.calls != 0 {
				t.Errorf("err = %v con %d llamadas al almacén, quiero nil y 0", err, spy.calls)
			}
		})
	}
}

// Rarezas fijadas: Project no mira el nombre del efecto, proyecta cadenas vacías y
// escribe la fila aunque no haya evento (EventID "").
func TestProjector_Project_DoesNotFilterByNameNorEmptiness(t *testing.T) {
	spy := &spyStore{}
	p := survey.NewProjector(spy)
	meta := surveyMeta()
	meta.EventID = ""
	eff := modules.Effect{Name: "otro_efecto", Payload: map[string]any{"question_id": "", "answer_code": ""}}
	if err := p.Project(context.Background(), meta, eff); err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(spy.rows) != 1 {
		t.Fatalf("filas = %+v, quiero una", spy.rows)
	}
	if got := spy.rows[0]; got.QuestionID != "" || got.AnswerCode != "" || got.EventID != "" || got.TenantID != "tenant-1" {
		t.Errorf("fila = %+v, quiero pregunta, respuesta y evento vacíos con la identidad de la meta", got)
	}
}

// El error del almacén sube tal cual, sin envolver.
func TestProjector_Project_ReturnsStoreError(t *testing.T) {
	boom := errors.New("boom")
	p := survey.NewProjector(&spyStore{err: boom})
	eff := modules.Effect{Name: survey.EffectSurveyAnswer, Payload: map[string]any{"question_id": "q1", "answer_code": "1"}}
	//nolint:errorlint // se comprueba identidad: el contrato promete el error SIN envolver.
	if err := p.Project(context.Background(), surveyMeta(), eff); err != boom {
		t.Errorf("error = %v, quiero el del almacén sin envolver", err)
	}
}
