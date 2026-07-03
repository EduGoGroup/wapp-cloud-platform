package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// PersistSink es la implementación de EventSink que MATERIALIZA cada efecto en el
// outbox append-only flow_events y, para el efecto de negocio "survey_answer",
// PROYECTA la misma fila a survey_results (Plan 015 · T2/T3). survey_results se
// conserva como proyección para el GROUP BY answer_code (Plan 014 §10.D); es la
// MISMA fila que producía el flush del Plan 014, ahora materializada por-respuesta.
//
// Desde T3 este sink es la ÚNICA vía de survey_results: el módulo survey declara
// un Effect{persist,survey_answer} por respuesta válida y main cablea este sink
// (WithEventSink), retirado ya el flush viejo (sin doble escritura).
type PersistSink struct {
	repo store.Repository
}

// NewPersistSink construye el sink de persistencia con el repositorio dado.
func NewPersistSink(repo store.Repository) *PersistSink {
	return &PersistSink{repo: repo}
}

// Handle persiste el efecto en flow_events y, si es survey_answer, lo proyecta a
// survey_results. Un fallo del INSERT se devuelve (el dispatcher lo LOGUEA; no
// aborta el avance del flujo). La proyección es defensiva: si el Payload no trae
// question_id/answer_code como string, se OMITE (no panica) — el efecto queda en
// flow_events pero no en survey_results.
func (s *PersistSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	fe := store.FlowEvent{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		Kind:        eff.Kind,
		Name:        eff.Name,
		Payload:     eff.Payload,
	}
	if err := s.repo.InsertFlowEvent(ctx, fe); err != nil {
		return err
	}

	// Proyección de negocio: survey_answer → survey_results (misma fila que el
	// flush del Plan 014). Aserción de tipo defensiva: claves ausentes o de otro
	// tipo → se omite la proyección sin panica.
	if eff.Name != "survey_answer" {
		return nil
	}
	qid, ok1 := eff.Payload["question_id"].(string)
	code, ok2 := eff.Payload["answer_code"].(string)
	if !ok1 || !ok2 {
		return nil
	}
	return s.repo.InsertResults(ctx, []store.SurveyResult{{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		QuestionID:  qid,
		AnswerCode:  code,
	}})
}
