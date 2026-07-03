package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// EffectContext lleva la identidad de la conversación que produjo un efecto, sin
// PII (Plan 015 · T2). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID en claro.
type EffectContext struct {
	TenantID  string
	ContactID string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	// SessionID identifica la sesión de WhatsApp que produjo el efecto; el
	// PersistSink lo persiste como orders.session_id (metadato de trazabilidad,
	// Plan 016 · design.md §3.4). No es PII.
	SessionID   string
	FlowID      string
	FlowVersion int
}

// EventSink es el puerto por el que el runtime despacha cada Effect que un módulo
// DECLARA (modules.Effect) al avanzar una conversación (Plan 015 · T2, segunda
// costura del refactor hexagonal). Es el análogo de gatewaygrpc.ReceiptSink: el
// runtime lo invoca en fan-out EN PROCESO (ADR-0003, sin broker) y el default es
// log-only (LogSink).
//
// Contrato de Handle:
//   - NO debe bloquear de forma indefinida (el runtime lo llama en su goroutine
//     de HandleIncoming, tras el Save del estado).
//   - NUNCA debe filtrar PII ni credenciales (ContactID es opaco; el Payload es
//     dato de negocio, no PII).
//   - Un error se LOGUEA (lo hace el dispatcher del runtime) y NO aborta el
//     avance del flujo: el estado ya quedó persistido antes del dispatch.
type EventSink interface {
	Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error
}
