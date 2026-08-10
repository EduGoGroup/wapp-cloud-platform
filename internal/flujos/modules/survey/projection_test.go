package survey_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// almacénEspía captura lo que el proyector manda a InsertResults.
type almacénEspía struct{ filas []store.SurveyResult }

func (a *almacénEspía) InsertResults(_ context.Context, rows []store.SurveyResult) error {
	a.filas = append(a.filas, rows...)
	return nil
}

// TestProjector_LaFilaDeclaraASuPadre (T4.5.3, D-043.21): la respuesta proyectada
// sale con el event_id de la EffectMeta — TRAZABILIDAD que sustituye la
// correlación por timestamp del resumen (runtime/summary_sources.go, que queda
// como fallback del legado; ese paquete no se toca desde aquí).
func TestProjector_LaFilaDeclaraASuPadre(t *testing.T) {
	spy := &almacénEspía{}
	p := survey.NewProjector(spy)

	meta := modules.EffectMeta{
		TenantID: "t45i-tenant", ContactID: "t45i-contacto",
		FlowID: "encuesta", FlowVersion: 3, EventID: "t45i-evento",
	}
	eff := modules.Effect{
		Name:    survey.EffectSurveyAnswer,
		Payload: map[string]any{"question_id": "q1", "answer_code": "si"},
	}
	if err := p.Project(context.Background(), meta, eff); err != nil {
		t.Fatalf("Project: %v", err)
	}

	if len(spy.filas) != 1 {
		t.Fatalf("filas=%d, quiero 1", len(spy.filas))
	}
	got := spy.filas[0]
	if got.EventID != "t45i-evento" {
		t.Fatalf("EventID=%q, quiero t45i-evento: el hijo declara a su padre", got.EventID)
	}
	if got.QuestionID != "q1" || got.AnswerCode != "si" || got.FlowVersion != 3 {
		t.Fatalf("fila inesperada: %+v", got)
	}

	// La aserción defensiva sigue intacta: payload malformado ⇒ se omite sin fila.
	if err := p.Project(context.Background(), meta, modules.Effect{
		Name: survey.EffectSurveyAnswer, Payload: map[string]any{"question_id": 7},
	}); err != nil {
		t.Fatalf("Project defensivo: %v", err)
	}
	if len(spy.filas) != 1 {
		t.Fatalf("el payload malformado escribió una fila: %+v", spy.filas)
	}
}
