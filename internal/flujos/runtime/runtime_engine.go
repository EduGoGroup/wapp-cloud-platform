package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva
// para la clave (T3 lo mapea a HTTP 409). Se inspecciona con errors.Is.
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

// Runtime orquesta el motor de flujos vivo (design.md §6): inicio por API
// (Start) y avance por entrante (HandleIncoming/OnIncoming). Serializa por
// conversación con un single-flight en memoria (keyedMutex) y persiste el
// estado ANTES de enviar (orden Save-antes-de-SendText, design.md §6).
//
// Las dependencias se inyectan: el store (durabilidad), el engine (máquina de
// estados pura), el Sender (salida hacia el Gateway), el TenantResolver
// (session_id → tenant_id, design.md §10.A) y el contact.Resolver (identidad
// del contacto: JID/ref → contact_id opaco y contact_id → destino enviable,
// Plan 010, design.md §4-§6). Es seguro para uso concurrente.
type Runtime struct {
	store    store.Repository
	engine   *engine.Engine
	sender   Sender
	resolver TenantResolver
	contacts contact.Resolver
	log      logger.Logger
	locks    *keyedMutex
}

// New construye el Runtime con sus dependencias.
func New(repo store.Repository, eng *engine.Engine, sender Sender, resolver TenantResolver, contacts contact.Resolver, log logger.Logger) *Runtime {
	return &Runtime{
		store:    repo,
		engine:   eng,
		sender:   sender,
		resolver: resolver,
		contacts: contacts,
		log:      log,
		locks:    newKeyedMutex(),
	}
}

// Start abre una conversación por API (design.md §6, decisión C): bajo el
// single-flight de la clave, si ya existe estado → ErrConversationExists; si
// no, fija la versión vigente, renderiza el nodo inicial (el menú), persiste y
// envía. Devuelve el último Ack del envío (el del último texto emitido) o nil
// si no hubo salidas.
func (rt *Runtime) Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	// Resuelve la ref del admin a un contact_id OPACO antes de clavar la key: el
	// motor opera por contact_id, no por el JID/ref crudo (Plan 010, design.md §6).
	contactID, err := rt.contacts.Resolve(ctx, tenantID, []contact.Ref{ref}, "")
	if err != nil {
		return nil, fmt.Errorf("runtime: resolver contacto: %w", err)
	}
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	unlock := rt.locks.lock(key)
	defer unlock()

	exists, err := rt.store.Exists(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("runtime: comprobar existencia: %w", err)
	}
	if exists {
		return nil, ErrConversationExists
	}

	def, err := rt.store.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		return nil, fmt.Errorf("runtime: definición vigente: %w", err)
	}

	st := model.Conversation{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	st, outs, err := rt.engine.Enter(ctx, def, st)
	if err != nil {
		return nil, fmt.Errorf("runtime: enter: %w", err)
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	return rt.send(ctx, sessionID, to, outs)
}

// HandleIncoming avanza una conversación EXISTENTE con un entrante (design.md
// §6). Resuelve el tenant, serializa por clave y:
//   - si no hay estado vivo → lo IGNORA (return nil; decisión C: un entrante no
//     inicia flujo);
//   - si el wa_message_id coincide con el último procesado → idempotencia
//     (return nil, no reenvía; design.md §10.G);
//   - en otro caso avanza con engine.Step sobre la versión con la que arrancó
//     (Conversation.FlowVersion), persiste y envía.
//
// Orden: persiste el estado ANTES de enviar (design.md §6). Tradeoff aceptado
// en este corte: si el SendText falla, el paso NO se reenvía porque el estado
// ya avanzó (preferimos no duplicar el avance a costa de un texto perdido).
func (rt *Runtime) HandleIncoming(ctx context.Context, sessionID string, m *cloudlinkv1.IncomingMessage) error {
	tenantID, err := rt.resolver.ResolveTenant(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("runtime: resolver tenant: %w", err)
	}
	// Resuelve la identidad enriquecida del entrante (from_pn/from_lid, con
	// fallback al JID crudo) a un contact_id OPACO antes de clavar la key: así el
	// mismo contacto casa el MISMO estado aunque el JID llegue como número o LID
	// (Plan 010, design.md §5, §6).
	refs := contact.RefsFrom(m.GetFromPn(), m.GetFromLid(), m.GetFrom())
	contactID, err := rt.contacts.Resolve(ctx, tenantID, refs, m.GetPushName())
	if err != nil {
		return fmt.Errorf("runtime: resolver contacto: %w", err)
	}
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	unlock := rt.locks.lock(key)
	defer unlock()

	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: cargar estado: %w", err)
	}
	if !ok {
		// Sin estado vivo → se ignora (no inicia flujo, decisión C).
		return nil
	}
	if st.LastWaMessageID != "" && st.LastWaMessageID == m.GetWaMessageId() {
		// Re-entrega del mismo mensaje → no avanzar ni reenviar (idempotencia).
		return nil
	}

	def, err := rt.store.GetDefinition(ctx, tenantID, st.FlowID, st.FlowVersion)
	if err != nil {
		return fmt.Errorf("runtime: definición en curso (v%d): %w", st.FlowVersion, err)
	}

	st, outs, effects, err := rt.engine.Step(ctx, def, st, engine.Input{Text: m.GetText()})
	if err != nil {
		return fmt.Errorf("runtime: step: %w", err)
	}
	// Efectos declarados por el módulo (Plan 015, segunda costura). En T0 nadie
	// los emite (siempre vacío); el dispatch al EventSink/persistencia es T2.
	_ = effects
	st.LastWaMessageID = m.GetWaMessageId()
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar estado: %w", err)
	}
	// Flush ÚNICO de resultados de encuesta al terminar la conversación (Plan
	// 014 §7, §10.G): los módulos son PUROS y solo anotan las respuestas en
	// Vars["answers"]; el runtime —único con store/tenant/contact— las vuelca a
	// survey_results cuando el flujo llega a Finished(). Va DESPUÉS del Save (el
	// estado ya está persistido) y respeta el orden Save-antes-de-Send. La
	// idempotencia es HEREDADA de la dedupe por last_wa_message_id (reprocesar el
	// mismo entrante corta antes del Step y no re-ejecuta el flush).
	rt.flushSurveyResults(ctx, st, sessionID)
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, outs)
	return err
}

// destino resuelve el contact_id a una cadena de destino DIRECCIONABLE por el
// Edge (design.md §10.E): desacopla el envío del JID entrante (doble rol, R4).
func (rt *Runtime) destino(ctx context.Context, tenantID, contactID string) (string, error) {
	dst, err := rt.contacts.Destino(ctx, tenantID, contactID)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver destino: %w", err)
	}
	to, err := dst.Sendable()
	if err != nil {
		return "", fmt.Errorf("runtime: destino no direccionable: %w", err)
	}
	return to, nil
}

// OnIncoming es el wrapper que T5 asigna a (*gatewaygrpc.Server).OnIncoming
// (func(sessionID string, m *cloudlinkv1.IncomingMessage), sin error).
//
// Despacha HandleIncoming en una goroutine y NO bloquea al llamante: el Gateway
// invoca este hook de forma SÍNCRONA dentro del loop Recv del stream del Edge
// (internal/gateway/grpc/server.go, route), y HandleIncoming hace un SendText
// que espera el Ack —Ack que ese MISMO loop Recv debe entregar (deliverAck).
// Procesar inline bloquearía el loop y causaría un deadlock por sesión. La
// serialización por conversación la sigue garantizando el keyedMutex dentro de
// HandleIncoming (cada clave se procesa de a una). Los errores se LOGUEAN sin
// propagarse ni panickear.
func (rt *Runtime) OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage) {
	go func() {
		if err := rt.HandleIncoming(context.Background(), sessionID, m); err != nil {
			rt.log.Error("runtime: procesar entrante",
				"error", err,
				"session_id", sessionID,
				"wa_message_id", m.GetWaMessageId(),
			)
		}
	}()
}

// flushSurveyResults vuelca las respuestas de encuesta acumuladas a
// survey_results cuando la conversación TERMINÓ (Plan 014 §10.G). Un flujo sin
// nodos survey_question (p. ej. un menú puro) deja `answers` vacío → no escribe
// (no-op). Un fallo aquí se LOGUEA pero NO aborta el manejo del entrante: son
// métricas de negocio, no el avance del flujo (el estado ya quedó persistido).
func (rt *Runtime) flushSurveyResults(ctx context.Context, st model.Conversation, sessionID string) {
	if !st.Finished() {
		return
	}
	rows := extractAnswers(st)
	if len(rows) == 0 {
		return
	}
	if err := rt.store.InsertResults(ctx, rows); err != nil {
		rt.log.Error("runtime: flush de resultados de encuesta",
			"error", err,
			"session_id", sessionID,
		)
	}
}

// extractAnswers recolecta las respuestas que el módulo survey anotó en
// Vars["answers"] y las convierte en filas listas para survey_results. Tolera el
// round-trip JSON: `answers` puede ser map[string]string (recién salido del
// módulo en memoria) o map[string]any (revivido de JSONB por Load); los valores
// no-string se ignoran. Sin respuestas → nil. Ordena por QuestionID para una
// salida DETERMINISTA (evita flakiness en los asserts de los tests).
func extractAnswers(conv model.Conversation) []store.SurveyResult {
	answers := asAnswers(conv.Vars["answers"])
	if len(answers) == 0 {
		return nil
	}
	ids := make([]string, 0, len(answers))
	for qid := range answers {
		ids = append(ids, qid)
	}
	sort.Strings(ids)
	rows := make([]store.SurveyResult, 0, len(ids))
	for _, qid := range ids {
		rows = append(rows, store.SurveyResult{
			TenantID:    conv.TenantID,
			ContactID:   conv.ContactID,
			FlowID:      conv.FlowID,
			FlowVersion: conv.FlowVersion,
			QuestionID:  qid,
			AnswerCode:  answers[qid],
		})
	}
	return rows
}

// asAnswers devuelve el mapa de respuestas tolerando el round-trip JSON
// (map[string]any tras Load de JSONB) o el tipo nativo (map[string]string recién
// salido del módulo) o nil, ignorando valores no-string. Réplica del patrón
// `asMap` del módulo survey (allí es no exportado).
func asAnswers(v any) map[string]string {
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]string:
		for k, val := range m {
			out[k] = val
		}
	case map[string]any:
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// send empuja cada salida por el Sender en orden y devuelve el último Ack. Ante
// el primer error de envío corta y lo devuelve (con el último Ack logrado).
func (rt *Runtime) send(ctx context.Context, sessionID, to string, outs []engine.Output) (*cloudlinkv1.Ack, error) {
	var last *cloudlinkv1.Ack
	for _, out := range outs {
		ack, err := rt.sender.SendText(ctx, sessionID, to, out.Text)
		if err != nil {
			return last, fmt.Errorf("runtime: enviar texto: %w", err)
		}
		last = ack
	}
	return last, nil
}
