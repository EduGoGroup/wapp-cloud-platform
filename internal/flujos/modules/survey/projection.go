package survey

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// EffectSurveyAnswer es el nombre lógico del efecto que la encuesta DECLARA al
// validar una respuesta (modules.Effect.Name). Es el CONTRATO por el que su proyector
// lo reconoce; Step lo emite con este mismo nombre.
const EffectSurveyAnswer = "survey_answer"

// ResultStore es lo que el proyector de la encuesta necesita del almacén: insertar
// las respuestas EN CLARO en survey_results. Interfaz mínima (ISP).
type ResultStore interface {
	InsertResults(ctx context.Context, rows []store.SurveyResult) error
}

// Projector implementa modules.Projector para survey_answer → survey_results (Plan
// 027 · Ola 3 · T8, cierra H10). Adaptador IMPURO; produce la MISMA fila que producía
// el switch central del PersistSink (la misma que el flush del Plan 014). El Module
// (Render/Step) sigue puro: solo DECLARA el efecto.
type Projector struct{ store ResultStore }

// NewProjector construye el proyector de la encuesta sobre el almacén dado.
func NewProjector(s ResultStore) *Projector { return &Projector{store: s} }

// Handles reconoce el único efecto que la encuesta proyecta a una tabla tipada.
func (Projector) Handles(name string) bool { return name == EffectSurveyAnswer }

// Project materializa survey_answer en survey_results. Aserción de tipo defensiva:
// claves ausentes o de otro tipo ⇒ se OMITE (el efecto ya quedó en flow_events).
func (p *Projector) Project(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	qid, ok1 := eff.Payload["question_id"].(string)
	code, ok2 := eff.Payload["answer_code"].(string)
	if !ok1 || !ok2 {
		return nil
	}
	return p.store.InsertResults(ctx, []store.SurveyResult{{
		TenantID:    meta.TenantID,
		ContactID:   meta.ContactID,
		FlowID:      meta.FlowID,
		FlowVersion: meta.FlowVersion,
		QuestionID:  qid,
		AnswerCode:  code,
	}})
}
