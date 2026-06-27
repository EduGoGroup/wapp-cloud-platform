package runtime

import (
	"context"
	"errors"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

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
// estados pura), el Sender (salida hacia el Gateway) y el TenantResolver
// (session_id → tenant_id, design.md §10.A). Es seguro para uso concurrente.
type Runtime struct {
	store    store.Repository
	engine   *engine.Engine
	sender   Sender
	resolver TenantResolver
	log      logger.Logger
	locks    *keyedMutex
}

// New construye el Runtime con sus dependencias.
func New(repo store.Repository, eng *engine.Engine, sender Sender, resolver TenantResolver, log logger.Logger) *Runtime {
	return &Runtime{
		store:    repo,
		engine:   eng,
		sender:   sender,
		resolver: resolver,
		log:      log,
		locks:    newKeyedMutex(),
	}
}

// Start abre una conversación por API (design.md §6, decisión C): bajo el
// single-flight de la clave, si ya existe estado → ErrConversationExists; si
// no, fija la versión vigente, renderiza el nodo inicial (el menú), persiste y
// envía. Devuelve el último Ack del envío (el del último texto emitido) o nil
// si no hubo salidas.
func (rt *Runtime) Start(ctx context.Context, tenantID, flowID, sessionID, contact string) (*cloudlinkv1.Ack, error) {
	key := store.Key{TenantID: tenantID, SessionID: sessionID, Contact: contact}
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

	st := model.Conversation{TenantID: tenantID, SessionID: sessionID, Contact: contact}
	st, outs, err := rt.engine.Enter(def, st)
	if err != nil {
		return nil, fmt.Errorf("runtime: enter: %w", err)
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	return rt.send(ctx, sessionID, contact, outs)
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
	key := store.Key{TenantID: tenantID, SessionID: sessionID, Contact: m.GetFrom()}
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

	st, outs, err := rt.engine.Step(def, st, engine.Input{Text: m.GetText()})
	if err != nil {
		return fmt.Errorf("runtime: step: %w", err)
	}
	st.LastWaMessageID = m.GetWaMessageId()
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar estado: %w", err)
	}
	_, err = rt.send(ctx, sessionID, m.GetFrom(), outs)
	return err
}

// OnIncoming es el wrapper que T5 asigna a (*gatewaygrpc.Server).OnIncoming
// (func(sessionID string, m *cloudlinkv1.IncomingMessage), sin error). Llama a
// HandleIncoming y LOGUEA el error sin propagarlo ni panickear.
func (rt *Runtime) OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage) {
	if err := rt.HandleIncoming(context.Background(), sessionID, m); err != nil {
		rt.log.Error("runtime: procesar entrante",
			"error", err,
			"session_id", sessionID,
			"wa_message_id", m.GetWaMessageId(),
		)
	}
}

// send empuja cada salida por el Sender en orden y devuelve el último Ack. Ante
// el primer error de envío corta y lo devuelve (con el último Ack logrado).
func (rt *Runtime) send(ctx context.Context, sessionID, contact string, outs []engine.Output) (*cloudlinkv1.Ack, error) {
	var last *cloudlinkv1.Ack
	for _, out := range outs {
		ack, err := rt.sender.SendText(ctx, sessionID, contact, out.Text)
		if err != nil {
			return last, fmt.Errorf("runtime: enviar texto: %w", err)
		}
		last = ack
	}
	return last, nil
}
