package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// flowEventStore es lo único que el PersistSink necesita del almacén tras extraer la
// proyección a los módulos (Plan 027 · Ola 3 · T8, ISP): el outbox append-only
// flow_events. La proyección tipada la aportan los modules.Projector inyectados.
type flowEventStore interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// DecisionAppender es el puerto ESTRECHO del sink hacia el hilo del evento
// (Plan 043 · Ola 4.5 · T4.5.7a, D-043.23): una fila `decision` por efecto
// estructurado del cliente. Lo satisface *events.Store (AppendDecision) y el
// EventStore del runtime — se declara aparte por ISP: el sink no necesita crear
// eventos ni tocar su ciclo de vida para escribir en su hilo.
type DecisionAppender interface {
	AppendDecision(ctx context.Context, eventID string, payload []byte) error
}

// Nombres de los efectos que este sink reconoce como DECISIÓN del cliente. Son
// RÉPLICAS de los literales que declaran los módulos (la convención de siempre:
// cart/effects.go:11-12 documenta que el PersistSink replica los nombres sin
// importar el módulo), y forman una LISTA CERRADA — la whitelist de D-043.13/23.
const (
	// effectItemAdded (cart.EffectItemAdded): la línea que el cliente AÑADIÓ al
	// pedido, con su sku, cantidad y variante en el payload. Es la decisión
	// arquetípica del carrito.
	effectItemAdded = "item_added"
	// effectNoteAdded (cart.EffectNoteAdded): la personalización que el cliente
	// dictó — la indicación de UNA línea (con su texto y, si partió la línea ×N,
	// el split_from_qty) o la indicación del pedido entero (de la que el payload
	// público solo lleva el LARGO; el literal es PII y no entra en el nivel 1).
	// También es el único efecto que acompaña a un cambio de líneas que no es un
	// alta (el split ×N → ×(N-1)+×1), así que cubre el «quitado/cantidad» de la
	// tabla de D-043.23 tal como el módulo lo emite hoy.
	effectNoteAdded = "note_added"
	// effectSurveyAnswer (survey.EffectSurveyAnswer): la respuesta validada de la
	// encuesta (question_id + answer_code).
	effectSurveyAnswer = "survey_answer"
)

// decisionEffects es la whitelist CERRADA de efectos-decisión (D-043.23): lo que
// el CLIENTE decidió, estructurado, nivel 1 en claro. Lo que queda FUERA, y por
// qué, para que nadie lo «complete» sin pasar por la decisión:
//
//   - cart_started, category_selected, item_viewed: NAVEGACIÓN — mirar no decide.
//   - cart_closed, cart_cancelled, cart_expired y toda apertura: CICLO DE VIDA del
//     evento/solicitud, no una decisión dentro de él (D-043.23 lo dice explícito).
//   - buyer_data_captured: sí es del cliente, pero es DATO PERSONAL con Kind
//     private (nivel 2, D-041.13) — su sitio es la fila cifrada del proyector,
//     jamás una entrada en claro del hilo. La guarda de KindPrivate en Handle lo
//     cortaría igual; no listarlo evita depender solo de esa guarda.
var decisionEffects = map[string]struct{}{
	effectItemAdded:    {},
	effectNoteAdded:    {},
	effectSurveyAnswer: {},
}

// PersistSink es el EventSink que MATERIALIZA cada efecto en el outbox append-only
// flow_events y delega la PROYECCIÓN tipada a los modules.Projector registrados (Plan
// 027 · Ola 3 · T8, cierra H10). Ya NO conoce los efectos de ningún módulo: el switch
// central (survey_answer→survey_results, cart_*→intakes/intake_items) se movió a cada
// módulo (modules/survey, modules/cart). Añadir un módulo con proyección NO obliga a
// tocar este archivo (OCP): basta registrar su Projector en el arranque.
//
// Desde la Ola 4.5 (T4.5.7a) además ALIMENTA EL HILO del evento: los efectos de la
// whitelist decisionEffects, cuando el turno pertenece a un evento vivo
// (ec.EventID), dejan su payload público como fila `decision` vía el
// DecisionAppender. Best-effort: un fallo del hilo NO corta la proyección ni el
// turno (patrón PersistSummary — el error sale para que el dispatcher lo loguee).
//
// El outbox flow_events es la bitácora completa (base de la telemetría por-paso, Δt
// entre efectos); la proyección es la vista consultable (GROUP BY, joins) que un
// JSONB no da con índice. La idempotencia es HEREDADA de la dedupe por
// last_wa_message_id del runtime.
type PersistSink struct {
	repo       flowEventStore
	projectors []modules.Projector
	// decisions es el hilo del evento (T4.5.7a). nil ⇒ no se escribe ninguna fila
	// `decision` (no-regresión total; lo cablea el bootstrap con WithDecisionThread).
	decisions DecisionAppender
}

// NewPersistSink construye el sink con el almacén del outbox y los proyectores por
// módulo. Sin proyectores, solo escribe flow_events (los efectos de negocio no se
// proyectan a sus tablas tipadas).
func NewPersistSink(repo flowEventStore, projectors ...modules.Projector) *PersistSink {
	return &PersistSink{repo: repo, projectors: projectors}
}

// WithDecisionThread cablea el productor de filas `decision` del hilo (T4.5.7a).
// Devuelve el propio sink para poder encadenarlo en el wiring. Es un método y no
// un parámetro de NewPersistSink para que ningún constructor existente cambie de
// firma (los tests y el bootstrap previos siguen compilando byte a byte).
func (s *PersistSink) WithDecisionThread(a DecisionAppender) *PersistSink {
	s.decisions = a
	return s
}

// Handle persiste el efecto en flow_events, escribe la fila `decision` del hilo si
// toca (ver appendDecision) y, si algún Projector registrado lo reconoce (Handles),
// delega en él la proyección tipada. Un fallo del INSERT del outbox corta (el
// estado del turno ya está persistido y el dispatcher LOGUEA); un fallo del hilo o
// de la proyección NO corta lo demás — se acumulan y salen juntos para el log del
// dispatcher, que jamás aborta el avance. Efectos sin proyector
// (navegación/telemetría) solo quedan en flow_events.
//
// Hay DOS excepciones a "siempre en flow_events", y las dos son de la plataforma,
// no del módulo — si dependieran de que cada módulo se acuerde de no emitir PII,
// bastaría uno nuevo distraído:
//
//   - modules.KindPrivate (Plan 041 · T4.5): el efecto ENTERO lleva datos
//     personales. Se salta el INSERT y va DIRECTO al proyector, que lo cifra.
//   - Effect.PrivateKeys (defecto A2 del cierre del Plan 041): el efecto es
//     público salvo por unas claves. Se escribe SIN ellas (PublicPayload) y el
//     proyector sigue recibiéndolo entero.
//
// Las dos existen por lo mismo: public.flow_events es un outbox append-only, en
// claro y sin poda, así que lo que se escriba ahí sobrevive a la solicitud que lo
// motivó y a cualquier borrado posterior. La fila `decision` del hilo respeta LAS
// MISMAS dos: se escribe el PublicPayload (nunca una clave privada) y un efecto
// KindPrivate no produce decisión en claro.
func (s *PersistSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if eff.Kind != modules.KindPrivate {
		fe := store.FlowEvent{
			TenantID:    ec.TenantID,
			ContactID:   ec.ContactID,
			FlowID:      ec.FlowID,
			FlowVersion: ec.FlowVersion,
			Kind:        eff.Kind,
			Name:        eff.Name,
			Payload:     eff.PublicPayload(),
		}
		if err := s.repo.InsertFlowEvent(ctx, fe); err != nil {
			return err
		}
	}

	// El hilo va ANTES de la proyección a propósito: la decisión es lo que el
	// MÓDULO declaró, y algún proyector ENRIQUECE eff.Payload al materializar
	// (cart anota intake_id en cart_closed, ver SinkPhase) — escribir después
	// colaría en el hilo datos que el cliente no decidió.
	errHilo := s.appendDecision(ctx, ec, eff)

	meta := modules.EffectMeta{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		SessionID:   ec.SessionID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		EventID:     ec.EventID,
	}
	for _, p := range s.projectors {
		if p.Handles(eff.Name) {
			return errors.Join(errHilo, p.Project(ctx, meta, eff))
		}
	}
	return errHilo
}

// appendDecision escribe la fila `decision` del hilo para un efecto de la
// whitelist (T4.5.7a, D-043.23). Las cuatro guardas, en orden de baratura:
//
//   - decisions nil: el hilo no está cableado (no-regresión).
//   - ec.EventID vacío: el turno NO pertenece a un evento vivo — «siempre que haya
//     evento vivo» es literal en D-043.23; una decisión sin evento no tiene hilo
//     en el que vivir, y no se inventa uno.
//   - KindPrivate: el payload es dato personal; el hilo en claro no lo ve NUNCA
//     (misma regla de plataforma que el outbox).
//   - fuera de la whitelist: navegación y ciclo de vida no son decisiones.
//
// El payload es el PublicPayload serializado: estructura en claro de nivel 1, sin
// las claves privadas (la indicación del pedido entra como su LARGO, igual que en
// flow_events — defecto A2 del Plan 041, misma poda, mismo motivo).
func (s *PersistSink) appendDecision(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if s.decisions == nil || ec.EventID == "" || eff.Kind == modules.KindPrivate {
		return nil
	}
	if _, ok := decisionEffects[eff.Name]; !ok {
		return nil
	}
	payload, err := json.Marshal(eff.PublicPayload())
	if err != nil {
		return fmt.Errorf("runtime: serializar la decisión %q para el hilo: %w", eff.Name, err)
	}
	if err := s.decisions.AppendDecision(ctx, ec.EventID, payload); err != nil {
		// Best-effort (patrón PersistSummary): el hilo JAMÁS tumba el turno. El
		// error sube envuelto para que el dispatcher lo loguee y siga.
		return fmt.Errorf("runtime: escribir la decisión %q en el hilo del evento: %w", eff.Name, err)
	}
	return nil
}
