package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/crmpush"
)

// WebhookSink es la PUERTA CONVERSACIONAL del puente CRM (Plan 042 · Ola 3 ·
// T3.2/T2.4): traduce el cierre de carrito a la forma del contrato y se lo pasa a
// crmpush, que es quien evalúa el gate y hace el INSERT en webhook_outbox.
//
// 🔄 LA REGLA YA NO VIVE AQUÍ (Plan 044 · Ola 4 · Tanda 2). Hasta esta tarea el
// gate, la plantilla del contrato y el encolado estaban dentro de este EventSink, y
// eso los hacía inalcanzables para cualquier llamante sin un modules.Effect en la
// mano — las rutas HTTP de internal/publicapi, que empujan la MISMA solicitud al
// MISMO puente, no tenían forma de reusarlos. Lo que queda en este archivo es lo
// único que es de verdad del motor: sacar del EffectContext y del efecto los datos
// que el contrato pide (ver effectInput). El resto está en
// internal/integrations/crmpush.
//
// NUNCA hace POST — ese es el worker (internal/integrations), que corre en otra
// goroutina y completa buyer_data/variables{}/customer_note justo antes de entregar
// (D-042.9/D-042.11). Ni este archivo ni crmpush importan net/http a propósito: es
// la garantía estructural de que el sink jamás bloquea el mensaje entrante con una
// llamada de red.
//
// Contrato de Handle (heredado de EventSink): no bloquea indefinidamente (un solo
// INSERT), NUNCA filtra PII/credenciales, y NUNCA aborta el avance del flujo
// (devuelve nil siempre; un error de encolado se loguea y sigue).
type WebhookSink struct {
	log    logger.Logger
	pusher *crmpush.Pusher
	// deliverEffect es el efecto que este sink entrega al puente (hoy cart_closed).
	// Se INYECTA (no se hardcodea el literal de un módulo, Plan 027 · Ola 3 · T8):
	// main.go lo cablea con cart.EffectCartClosed.
	deliverEffect string
	// tieneDependencias distingue el sink de producción del que construyen los tests
	// unitarios que solo ejercitan el camino "no entrega". Se guarda al construir
	// porque crmpush.NewPusher devuelve un *Pusher aunque le den nils, y su Push es
	// un no-op seguro: el sink necesita saberlo para no loguear un encolado que no
	// ocurrió.
	tieneDependencias bool
}

// WebhookQueuer es lo mínimo que el sink necesita del store de integraciones
// (interfaz local, ISP — mismo patrón que flowEventStore/ProjectionStore de este
// repo): un solo INSERT. Lo satisface *integrations.Postgres.
//
// Se conserva aquí —en vez de exigirle a bootstrap.go el tipo de crmpush— porque es
// la firma que este paquete publica en su cableado desde el Plan 042; crmpush.Queuer
// es idéntica y Go la satisface sin adaptador.
type WebhookQueuer = crmpush.Queuer

// WebhookGate decide si un tenant tiene el puente CRM activo AHORA MISMO (D-042.8:
// entitlements.Has(tenant,"crm_bridge") + tenant_integrations con
// events_adapter='webhook' y enabled=true — mecánica ADR-0022 ya implementada). La
// implementación real vive en el wiring (integrations.EntitlementsGate).
type WebhookGate = crmpush.Gate

// NewWebhookSink construye el sink real. sender/gate pueden ser nil en tests
// unitarios que solo ejercitan el camino "no entrega" (nav, gate cerrado).
func NewWebhookSink(log logger.Logger, deliverEffect string, sender WebhookQueuer, gate WebhookGate) *WebhookSink {
	return &WebhookSink{
		log:               log,
		pusher:            crmpush.NewPusher(log, sender, gate),
		deliverEffect:     deliverEffect,
		tieneDependencias: sender != nil && gate != nil,
	}
}

// Phase implementa PhasedSink: este sink corre en PhaseNotify, DESPUÉS de toda la
// proyección (Plan 042 · Ola 3.1). No es una preferencia, es un requisito de
// corrección: `intake_id` no existe hasta que cart.Projector.closeIntake lo genera
// y lo anota en eff.Payload, y este sink lo LEE (ver effectInput).
// Declararlo aquí hace que el orden lo garantice el runtime y no el orden de las
// llamadas a WithEventSink en bootstrap.go, que era lo único que lo sostenía antes.
func (*WebhookSink) Phase() SinkPhase { return PhaseNotify }

// Handle traduce el efecto que este sink entrega (hoy cart_closed) y se lo pasa a
// crmpush. Cualquier otro efecto (navegación/telemetría) no se entrega — un CRM real
// solo quiere el cierre.
//
// El error de crmpush se LOGUEA y muere aquí: el contrato del EventSink es que
// notificar al puente jamás cuelga la respuesta que el cliente está esperando por
// WhatsApp. La otra puerta del mismo empuje (HTTP) decide distinto, y por eso la
// decisión no está dentro de crmpush.
func (s *WebhookSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if s == nil || s.log == nil {
		return nil
	}
	if eff.Name != s.deliverEffect {
		return nil
	}
	if !s.tieneDependencias {
		return nil // sink construido sin dependencias (tests): no-op seguro
	}

	res, err := s.pusher.Push(ctx, effectInput(ec, eff))
	if err != nil {
		s.log.Error("webhook: no se pudo encolar intake.push", "error", err,
			"tenant", ec.TenantID, "name", eff.Name)
		return nil
	}
	if !res.Enqueued {
		return nil // gate cerrado; crmpush ya lo dejó en debug con su motivo
	}
	s.log.Debug("webhook: intake.push encolado",
		"tenant", ec.TenantID, "outbox_id", res.OutboxID, "intake_id", res.Payload.IntakeID)
	return nil
}

// effectInput saca del EffectContext y del efecto cart_closed los datos que el
// contrato pide. Es la ÚNICA parte del empuje que conoce el motor de flujos, y es
// pura: no toca la BD ni la red (INV-02).
//
// intake_id, revision_no y lifecycle_status salen de eff.Payload — los anota
// cart.Projector.closeIntake DESPUÉS de proyectar, en el MISMO mapa que ve este sink
// (corre después en el fan-out de dispatch(), ver runtime/resume.go): sin esas
// anotaciones no habría forma de correlacionar sin una consulta extra a la BD, que
// el sink NO debe hacer.
//
// 🔴 LOS TRES SON DATOS, NO CONSTANTES, y dos de ellos lo fueron:
//
//   - revision_no fue un literal `1` hasta T4.10 (mitad 1). El puente hace UPSERT
//     por (intake_id, revision_no) y trata como duplicado todo par repetido (manual
//     del integrador §4), así que un número fijo deja al CRM con el primer estado
//     para siempre. AUSENTE ⇒ 0 a propósito: AsInt no distingue «no está» de «vale
//     0», y aquí ese empate juega a favor porque el cero es el único valor que el
//     schema rechaza (`minimum: 1`). Un número FALSO es peor que uno ausente.
//   - lifecycle_status fue un literal `"confirmed"` hasta T4.10 (mitad 2), y
//     acertaba SOLO porque este sink entrega el cierre del carrito. Llega crudo —la
//     clave legada `closed` con la que cart escribe la fila— y lo normaliza
//     crmpush.Build, que es donde vive la prohibición de emitir `closed`.
//
// customer_note NO se lee aunque esté en el efecto (el proyector la necesita para
// escribir intakes.customer_note): la completa el worker justo antes del POST, por
// EXPOSICIÓN y no por coste — congelarla aquí la dejaría en claro en webhook_outbox,
// una tabla que sobrevive a la entrega. Ver el doc de crmpush.Payload.
func effectInput(ec EffectContext, eff modules.Effect) crmpush.Input {
	items := effectItems(eff.Payload)
	lines := make([]crmpush.Item, 0, len(items))
	for _, m := range items {
		lines = append(lines, crmpush.Item{
			SKU:           modules.AsString(m["sku"]),
			Label:         modules.AsString(m["label"]),
			Customization: modules.AsString(m["customization"]),
			Qty:           modules.AsInt(m["qty"]),
			UnitPrice:     modules.AsFloat(m["unit_price"]),
		})
	}
	return crmpush.Input{
		TenantID:        ec.TenantID,
		ContactID:       ec.ContactID,
		IntakeID:        modules.AsString(eff.Payload["intake_id"]),
		LifecycleStatus: modules.AsString(eff.Payload["lifecycle_status"]),
		RevisionNo:      modules.AsInt(eff.Payload["revision_no"]),
		Items:           lines,
		Total:           modules.AsFloat(eff.Payload["total"]),
	}
}

// effectItems extrae la lista de líneas del payload como []map[string]any, tolerando
// el camino en-proceso ([]map[string]any) y el round-trip JSON ([]any de map). Es
// genérico (no conoce el módulo): parsea la forma del payload, no su semántica.
func effectItems(payload map[string]any) []map[string]any {
	switch items := payload["items"].(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}
