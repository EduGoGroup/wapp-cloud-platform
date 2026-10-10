// Porta internal/flujos/runtime/summary_sources.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// SummaryStore es lo que el adaptador del resumen necesita del almacén de flujos, y
// nada más (ISP, igual que FlowStore): la solicitud abierta, sus líneas y los
// resultados de encuesta. Lo satisfacen *store.PostgresRepository y
// *store.MemoryRepository sin cambios.
type SummaryStore interface {
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	ListIntakeItems(ctx context.Context, intakeID string) ([]store.IntakeItem, error)
	ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]store.SurveyResult, error)
}

// NewSummarySources arma las dos fuentes durables del resumen sobre un mismo almacén
// (Plan 043 · T3.4). Es lo que el arranque pasa a WithSummarySources.
//
// Se construyen juntas porque juntas se usan: events.LoadSummary falla si le falta el
// lector del tipo que le toca, así que media fuente deja la mitad de los abandonos
// escribiendo un WARN en vez de una fila. Por eso Lines y Answers salen SIEMPRE los
// dos, no nil, y leen del mismo almacén `s`. No valida `s` ni lo consulta al
// construir: un almacén nil se descubre al leer.
//
// Los dos lectores son adaptadores no exportados (intakeLines y surveyAnswers); lo que
// prometen se promete aquí, que es la única puerta por la que se llega a ellos.
//
// # Lines · OpenIntakeLines(ctx, tenantID, sessionID, contactID)
//
// Lee las líneas YA DECIDIDAS del pedido abierto de una conversación:
//
//   - Pide la solicitud abierta con GetOpenIntake(tenantID, contactID), SIN sesión
//     (hay una solicitud abierta por contacto, design.md §3.4).
//   - Si GetOpenIntake falla, devuelve (nil, ese mismo error), sin envolver y sin
//     leer líneas.
//   - Sin solicitud abierta devuelve (nil, nil) sin leer líneas: no es un fallo, es
//     que no hay nada que resumir (la mayoría de las conversaciones no tienen pedido
//     abierto).
//   - 🔴 Filtro por SESIÓN (REQ-18), que no es opcional y es lo primero que se pierde
//     al copiar esto: si la solicitud abierta es de OTRA sesión, devuelve (nil, nil)
//     sin leer líneas. Un evento es de (tenant, SESIÓN, contacto); sin la
//     comparación, dos sesiones del mismo tenant hablando con la misma persona se
//     resumirían el mismo pedido.
//   - Si es de su sesión, lee las líneas con ListIntakeItems(el ID de esa solicitud).
//     Si falla, devuelve (nil, ese mismo error), sin envolver.
//   - Devuelve una events.SummaryLine por línea, EN EL ORDEN del almacén, copiando
//     SKU, Label, Qty, UnitPrice y Customization (la personalización viaja, D-041.17:
//     quien prepara el pedido tiene que leer el «sin azúcar»). Una solicitud sin
//     líneas da una lista de largo 0, sin error.
//
// # Answers · SurveyAnswers(ctx, ev)
//
// Lee las respuestas de ESTA pasada de la encuesta:
//
//   - Pide los resultados con ListResults(ev.TenantID, ev.ContactID, ev.FlowID). Si
//     falla, devuelve (nil, ese mismo error), sin envolver.
//   - 🔴 Cota por fecha: DESCARTA las filas cuyo CreatedAt es ANTERIOR a
//     ev.CreatedAt, y conserva las demás —también la del MISMO instante: el corte es
//     estricto—. Es el fallback del LEGADO: las filas anteriores a la migración 0054
//     no tienen event_id, así que para no devolver lo que la misma persona respondió
//     el mes pasado se usa el nacimiento del evento, que es tardío y por tanto
//     posterior a cualquier pasada anterior.
//   - Devuelve una events.SummaryAnswer por fila conservada, EN EL ORDEN del almacén,
//     con QuestionID y AnswerCode. No reduce a una por pregunta ni ordena: eso lo
//     hace events.LoadSummary. Sin filas, lista de largo 0, sin error.
//   - NO filtra por event_id, ni por sesión, ni por versión del flujo, aunque la fila
//     los traiga: migrar este lector a la vía preferida por `event_id` es del plan
//     que vuelva a tocar el resumen de encuesta. La limitación queda ACOTADA, no
//     eliminada: en el legado dos sesiones del mismo tenant respondiendo el MISMO
//     flujo a la vez no se distinguen, y por esta fuente REQ-18 no se satisface del
//     todo. Taparlo aquí sería fingir una precisión que esas filas no dan.
//
// ⚠️ La cota compara DOS marcas de tiempo y ambas tienen que venir del MISMO reloj. En
// producción las dos las pone la base. En un test, el CreatedAt del evento y el de las
// filas se fijan los dos a mano, sobre la misma base: un evento fechado con un reloj y
// filas fechadas con otro miden el desfase de los relojes y no la cota.
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
// descarta lo escrito ANTES de que este evento existiera. El resto del porqué (el
// mismo reloj para las dos marcas, la limitación acotada del legado) está en el
// contrato de NewSummarySources.
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
