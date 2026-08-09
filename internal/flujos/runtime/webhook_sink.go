package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// WebhookSink es el ENCOLADOR del puente CRM (Plan 042 · Ola 3 · T3.2/T2.4):
// evalúa el gate (feature + integración habilitada) y, si pasa, hace UN INSERT
// en webhook_outbox con el payload PLANTILLA del contrato wapp-crm-v1
// (intake.push). NUNCA hace POST — ese es el worker (internal/integrations),
// que corre en otra goroutina y completa buyer_data/variables{}/customer_note
// justo antes de entregar (D-042.9/D-042.11, decisión 2026-08-07 §8.3: prevalece
// INV-02; customer_note se sumó a esa lista el 2026-08-08 por PII, ver la
// plantilla). Este
// archivo NO importa net/http a propósito: es la garantía estructural de que el
// sink jamás bloquea el mensaje entrante con una llamada de red.
//
// Contrato de Handle (heredado de EventSink): no bloquea indefinidamente
// (un solo INSERT), NUNCA filtra PII/credenciales, y NUNCA aborta el avance del
// flujo (devuelve nil siempre; un error de encolado se loguea y sigue).
type WebhookSink struct {
	log    logger.Logger
	sender WebhookQueuer
	gate   WebhookGate
	// deliverEffect es el efecto que este sink entrega al puente (hoy cart_closed).
	// Se INYECTA (no se hardcodea el literal de un módulo, Plan 027 · Ola 3 · T8):
	// main.go lo cablea con cart.EffectCartClosed.
	deliverEffect string
}

// WebhookQueuer es lo mínimo que el sink necesita del store de integraciones
// (interfaz local, ISP — mismo patrón que flowEventStore/ProjectionStore de este
// repo): un solo INSERT. Lo satisface *integrations.Postgres.
type WebhookQueuer interface {
	EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error)
}

// WebhookGate decide si un tenant tiene el puente CRM activo AHORA MISMO
// (D-042.8: entitlements.Has(tenant,"crm_bridge") + tenant_integrations con
// events_adapter='webhook' y enabled=true — mecánica ADR-0022 ya implementada).
// Interfaz local para que el sink no dependa de los paquetes concretos de
// entitlements/integrations; la implementación real vive en el wiring
// (bootstrap.go) como un adaptador pequeño que combina ambos.
type WebhookGate interface {
	// Enabled responde si el tenant debe recibir el efecto AHORA. Un error se
	// trata como "no" (fail-closed, mismo criterio que entitlements.Resolver): un
	// puente mal configurado no debe colgar el mensaje ni encolar basura.
	Enabled(ctx context.Context, tenantID string) (bool, error)
}

// NewWebhookSink construye el sink real. sender/gate pueden ser nil en tests
// unitarios que solo ejercitan el camino "no entrega" (nav, gate cerrado).
func NewWebhookSink(log logger.Logger, deliverEffect string, sender WebhookQueuer, gate WebhookGate) *WebhookSink {
	return &WebhookSink{log: log, sender: sender, gate: gate, deliverEffect: deliverEffect}
}

// Phase implementa PhasedSink: este sink corre en PhaseNotify, DESPUÉS de toda la
// proyección (Plan 042 · Ola 3.1). No es una preferencia, es un requisito de
// corrección: `intake_id` no existe hasta que cart.Projector.closeIntake lo genera
// y lo anota en eff.Payload, y este sink lo LEE (ver buildIntakePushTemplate).
// Declararlo aquí hace que el orden lo garantice el runtime y no el orden de las
// llamadas a WithEventSink en bootstrap.go, que era lo único que lo sostenía antes.
func (*WebhookSink) Phase() SinkPhase { return PhaseNotify }

// Handle evalúa el gate para el efecto que este sink entrega (hoy cart_closed) y,
// si pasa, arma la plantilla de intake.push y la encola. Cualquier otro efecto
// (navegación/telemetría) no se entrega — un CRM real solo quiere el cierre.
func (s *WebhookSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if s == nil || s.log == nil {
		return nil
	}
	if eff.Name != s.deliverEffect {
		return nil
	}
	if s.sender == nil || s.gate == nil {
		return nil // sink construido sin dependencias (tests): no-op seguro
	}

	enabled, err := s.gate.Enabled(ctx, ec.TenantID)
	if err != nil {
		s.log.Error("webhook: evaluar gate del puente CRM", "error", err, "tenant", ec.TenantID)
		return nil
	}
	if !enabled {
		s.log.Debug("webhook: tenant sin puente CRM activo, no se encola", "tenant", ec.TenantID, "name", eff.Name)
		return nil
	}

	payload := buildIntakePushTemplate(ec, eff, time.Now().UTC())
	body, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("webhook: serializando la plantilla de intake.push", "error", err, "tenant", ec.TenantID)
		return nil
	}

	id, err := s.sender.EnqueueWebhook(ctx, ec.TenantID, "intake.push", body)
	if err != nil {
		s.log.Error("webhook: encolando intake.push", "error", err, "tenant", ec.TenantID)
		return nil
	}
	s.log.Debug("webhook: intake.push encolado", "tenant", ec.TenantID, "outbox_id", id, "intake_id", payload.IntakeID)
	return nil
}

// intakePushTemplate es la PLANTILLA que se encola (webhook_outbox.payload):
// solo los campos baratos y estables del contrato wapp-crm-v1 · intake.push.
// `buyer_data`, `variables` y `customer_note` NO viajan aquí — el worker los
// completa justo antes del POST (D-042.9/D-042.11) — por eso esta struct NO
// tiene esos campos: añadirlos en el sink volvería a violar INV-02
// (cripto/consulta en línea con el mensaje).
//
// customer_note se fue por el MISMO camino, pero por un motivo distinto de los
// otros dos: no es coste, es EXPOSICIÓN. La indicación del cliente («dejarlo en
// portería, calle Mayor 14») es la PII de manual, y el defecto A2 del Plan 041 ya
// la sacó de flow_events por la puerta de KindPrivate. Congelarla en el payload de
// webhook_outbox la dejaba EN CLARO en una segunda tabla que además sobrevive a la
// entrega — la tercera puerta que aquel arreglo destapó y no cerró. El worker la
// lee de public.intakes (columna customer_note, migración 0045) justo antes del
// POST, igual que descifra buyer_data: el contrato wapp-crm-v1 NO cambia, cambia
// CUÁNDO se rellena.
type intakePushTemplate struct {
	ContractVersion string           `json:"contract_version"`
	Verb            string           `json:"verb"`
	Tenant          string           `json:"tenant"`
	Contact         string           `json:"contact"`
	IntakeID        string           `json:"intake_id"`
	LifecycleStatus string           `json:"lifecycle_status"`
	RevisionNo      int              `json:"revision_no"`
	Items           []intakePushItem `json:"items"`
	Total           float64          `json:"total"`
	Timestamp       string           `json:"timestamp"`
	// EventHistoryID es OMITEMPTY a propósito (MD-042.1, resuelto 2026-08-07):
	// es el único campo opcional del contrato — ausente mientras el Plan 043 no
	// exista, y el schema lo declara no-requerido.
	EventHistoryID string `json:"event_history_id,omitempty"`
}

type intakePushItem struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// lifecycleStatusClosed es el estado con el que cart.closeIntake escribe la fila
// (legado, cart/projection.go: intakeStatusClosed = "closed") — este sink no
// importa el paquete cart para no crear un ciclo con runtime; el ciclo de vida
// real tras un cierre de carrito ES SIEMPRE "confirmed" tras normalizar
// (intakes.NormalizeStatus("closed") == "confirmed"), y el contrato PROHÍBE
// emitir "closed" (intake.push.md: "El contrato JAMÁS emite closed"). Se
// hardcodea el resultado ya normalizado en vez de importar internal/intakes
// solo por esta constante.
const lifecycleStatusConfirmed = "confirmed"

// buildIntakePushTemplate arma la plantilla de intake.push (§3 del contrato,
// design.md) a partir del EffectContext y el efecto cart_closed. intake_id sale
// de eff.Payload["intake_id"] — lo anota cart.Projector.closeIntake DESPUÉS de
// proyectar, en el MISMO mapa que ve este sink (corre después en el fan-out de
// dispatch(), ver runtime/resume.go): sin esa anotación no habría forma de
// correlacionar sin una consulta extra a la BD, que el sink NO debe hacer
// (INV-02: cero red/BD adicional en línea con el mensaje más allá del encolado).
func buildIntakePushTemplate(ec EffectContext, eff modules.Effect, now time.Time) intakePushTemplate {
	items := effectItems(eff.Payload)
	pushItems := make([]intakePushItem, 0, len(items))
	for _, m := range items {
		pushItems = append(pushItems, intakePushItem{
			SKU:           modules.AsString(m["sku"]),
			Label:         modules.AsString(m["label"]),
			Customization: modules.AsString(m["customization"]),
			Qty:           modules.AsInt(m["qty"]),
			UnitPrice:     modules.AsFloat(m["unit_price"]),
		})
	}
	return intakePushTemplate{
		ContractVersion: "1",
		Verb:            "intake.push",
		Tenant:          ec.TenantID,
		Contact:         ec.ContactID,
		IntakeID:        modules.AsString(eff.Payload["intake_id"]),
		LifecycleStatus: lifecycleStatusConfirmed,
		RevisionNo:      1, // wApp emite siempre 1 hasta el Plan 044 (revisiones)
		// customer_note NO se lee de eff.Payload aunque esté ahí: el efecto sigue
		// llevándola (el proyector la necesita para escribir intakes.customer_note)
		// y este sink la ignora a propósito. Ver el comentario de intakePushTemplate.
		Items:     pushItems,
		Total:     modules.AsFloat(eff.Payload["total"]),
		Timestamp: now.Format(time.RFC3339),
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
