package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// SummaryStore es lo que el adaptador del resumen necesita del almacén de flujos, y
// nada más (ISP, igual que FlowStore): la solicitud abierta y sus líneas. Lo satisface
// *store.PostgresRepository y *store.MemoryRepository sin cambios.
type SummaryStore interface {
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	ListIntakeItems(ctx context.Context, intakeID string) ([]store.IntakeItem, error)
	ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]store.SurveyResult, error)
}

// NewSummarySources arma las dos fuentes durables del resumen sobre un mismo almacén
// (Plan 043 · T3.4). Es lo que el bootstrap pasa a WithSummarySources.
//
// Se construyen juntas porque juntas se usan: LoadSummary falla si le falta el lector
// del tipo que le toca, así que media fuente deja la mitad de los abandonos escribiendo
// un WARN en vez de una fila.
func NewSummarySources(s SummaryStore) events.SummarySources {
	return events.SummarySources{
		Lines:   intakeLines{store: s},
		Answers: surveyAnswers{store: s},
	}
}

// intakeLines lee las líneas YA DECIDIDAS del pedido abierto de una conversación.
type intakeLines struct{ store SummaryStore }

// OpenIntakeLines resuelve la solicitud abierta del contacto y devuelve sus líneas.
//
// El filtro por SESIÓN no es opcional y es lo primero que se pierde al copiar esto
// (REQ-18): GetOpenIntake resuelve por (tenant, contacto) SIN sesión —una solicitud
// abierta por contacto, design.md §3.4— mientras que un evento es de (tenant, SESIÓN,
// contacto). Sin la comparación, dos sesiones del mismo tenant hablando con la misma
// persona se resumirían el mismo pedido, y el resumen del evento de una acabaría
// enseñando lo que la otra armó.
//
// Sin solicitud abierta, o con una de otra sesión, devuelve nil SIN error: no es un
// fallo, es que no hay nada que resumir, y quien llama ya distingue ese caso.
func (a intakeLines) OpenIntakeLines(ctx context.Context, tenantID, sessionID, contactID string) ([]events.SummaryLine, error) {
	in, found, err := a.store.GetOpenIntake(ctx, tenantID, contactID)
	if err != nil || !found {
		return nil, err
	}
	if in.SessionID != sessionID {
		return nil, nil
	}
	items, err := a.store.ListIntakeItems(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := make([]events.SummaryLine, 0, len(items))
	for _, it := range items {
		out = append(out, events.SummaryLine{
			SKU:           it.SKU,
			Label:         it.Label,
			Qty:           it.Qty,
			UnitPrice:     it.UnitPrice,
			Customization: it.Customization,
		})
	}
	return out, nil
}

// surveyAnswers lee las respuestas YA DADAS de la encuesta de un evento.
type surveyAnswers struct{ store SummaryStore }

// SurveyAnswers devuelve las respuestas de ESTA pasada de la encuesta.
//
// La cota por fecha es el FALLBACK DEL LEGADO, no un adorno: survey_results se
// diseñó dos planes antes de que los eventos existieran y no tenía session_id ni
// event_id. Desde la 0054 (Plan 043 · Ola 4.5, D-043.21) la tabla SÍ tiene
// `event_id` —lo escribe el proyector del módulo survey al declarar a su padre—,
// pero las filas anteriores a esa migración lo llevan NULL, así que la consulta por
// (tenant, contacto, flujo) devolvería también lo que la misma persona respondió el
// mes pasado. Para separar tandas cubriendo TAMBIÉN el legado se usa el nacimiento
// del evento —que es TARDÍO, y por tanto posterior a cualquier pasada anterior—: se
// descarta lo escrito ANTES de que este evento existiera. Migrar este lector a la
// vía preferida por `event_id` es del plan que vuelva a tocar el resumen de encuesta
// (anotado en tasks.md del 043, §Ola 4.5 · hallazgos).
//
// ⚠️ La cota compara DOS marcas de tiempo y ambas tienen que venir del MISMO reloj. En
// producción lo hacen —las dos las pone la base—, pero en un test no: el evento se
// fecha con el reloj INYECTADO del banco de pruebas y las filas con el reloj REAL de la
// máquina. Un fixture con fecha futura descarta respuestas legítimas; uno con fecha
// pasada cuela las que producción filtraría. Por eso los tests que ejercitan esto anclan
// su reloj a la hora real en vez de a una fecha inventada — si vas a escribir uno nuevo,
// hazlo igual o estarás midiendo el desfase de los relojes y no la cota.
//
// La limitación de fondo quedó ACOTADA por la 0054, no eliminada: para filas nuevas
// `event_id` da la identidad de pasada exacta, pero para el LEGADO (`event_id` NULL)
// sigue sin haber `session_id` ni identidad de pasada, así que por esta fuente REQ-18
// no se satisface del todo. En el legado lo que se puede acotar es el flujo y el
// instante de nacimiento del evento; dos sesiones del mismo tenant respondiendo el
// MISMO flujo a la vez no se distinguen. Taparlo aquí sería fingir una precisión que
// esas filas no dan.
func (a surveyAnswers) SurveyAnswers(ctx context.Context, ev events.Event) ([]events.SummaryAnswer, error) {
	rows, err := a.store.ListResults(ctx, ev.TenantID, ev.ContactID, ev.FlowID)
	if err != nil {
		return nil, err
	}
	out := make([]events.SummaryAnswer, 0, len(rows))
	for _, r := range rows {
		if r.CreatedAt.Before(ev.CreatedAt) {
			continue
		}
		out = append(out, events.SummaryAnswer{QuestionID: r.QuestionID, AnswerCode: r.AnswerCode})
	}
	return out, nil
}
