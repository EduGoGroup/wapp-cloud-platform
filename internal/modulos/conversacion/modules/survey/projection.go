// Porta internal/flujos/modules/survey/projection.go @ 42117b5

package survey

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
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

// NewProjector construye el proyector de la encuesta sobre el almacén dado. No
// valida el almacén: con uno nil, Project revienta al proyectar.
func NewProjector(s ResultStore) *Projector { return &Projector{store: s} }

// Handles reconoce el único efecto que la encuesta proyecta a una tabla tipada:
// true solo para EffectSurveyAnswer, exacto (sin recortar ni plegar mayúsculas).
func (Projector) Handles(name string) bool { return name == EffectSurveyAnswer }

// Project materializa survey_answer en survey_results. Aserción de tipo defensiva:
// claves ausentes o de otro tipo ⇒ se OMITE (el efecto ya quedó en flow_events).
//
// La fila DECLARA A SU PADRE (D-043.21, T4.5.3): event_id = meta.EventID, el evento
// conversacional vivo del turno. Es TRAZABILIDAD, no «contenido que puede morir»
// (D-043.18) — la encuesta no entra en la vista event_content. Con esto, la
// correlación por timestamp que hace el resumen de encuesta
// (runtime/summary_sources.go, paquete ajeno: no se toca aquí) queda como FALLBACK
// del legado (filas pre-0054 con event_id NULL); la vía preferida es event_id.
//
// Escribe UNA fila por llamada, con TenantID, ContactID, FlowID, FlowVersion y
// EventID de la meta (SessionID no viaja) y question_id/answer_code del Payload,
// tal cual vengan —también vacíos—. El error del almacén sube sin envolver; la
// omisión por payload malformado devuelve nil sin tocar el almacén. Project NO
// mira eff.Name ni eff.Kind: filtrar por Handles es cosa del sink que lo llama.
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
		EventID:     meta.EventID,
	}})
}
