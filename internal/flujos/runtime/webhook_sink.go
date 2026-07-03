package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// WebhookSink es el PUNTO DE INYECCIÓN del CRM/POS externo (Plan 016 · T4,
// design.md §9.I) implementado como STUB NO-OP: es un EventSink más del fan-out
// del runtime que, HOY, NO entrega nada por red. Solo LOGUEA que "entregaría" el
// efecto al CRM y, para cart_closed, CONSTRUYE y SERIALIZA el payload que se
// enviaría (para dejar documentado y probado el contrato), pero NO hace POST.
//
// Por qué existe ya (y no el POST real): en 016 TODO va a la BD (PersistSink →
// orders/order_items). El WebhookSink deja el "asiento" del CRM listo para el
// futuro sin comprometer credenciales, red ni durabilidad. Un CRM real se
// enchufa registrando este (u otro) EventSink en main.go SIN tocar el módulo ni
// el flujo (extensibilidad del hexágono del Plan 015).
//
// # Contrato del payload al CRM (zero-knowledge · CERO PII)
//
// La forma JSON que un WebhookSink REAL enviaría al CRM/POS es (design.md §9.I):
//
//	{
//	  "tenant":    "<tenant_id>",          // identificador del tenant
//	  "contact":   "<contact_id opaco>",   // OPACO (Plan 010 / ADR-0010); NUNCA número/JID
//	  "order_id":  "<uuid de la orden>",   // correlación con orders (ver nota abajo)
//	  "items": [ { "sku": "...", "label": "...", "qty": 2, "unit_price": 9.9 }, ... ],
//	  "total":     29.7,
//	  "timestamp": "2026-07-03T10:00:00Z"  // RFC3339 UTC del cierre
//	}
//
// Coherente con el zero-knowledge (ADR-0007/0009): son DATOS DE NEGOCIO, no PII
// ni credenciales. El contacto viaja OPACO (contact_id, jamás el teléfono/JID) y
// no se incluye ningún dato del store cifrado del Edge.
//
// Nota sobre order_id: el fan-out entrega el MISMO modules.Effect a todos los
// sinks; el order_id lo materializa el PersistSink al proyectar orders. Este stub
// lo toma de eff.Payload["order_id"] si el efecto lo llevara (hoy cart_closed no
// lo incluye — design.md §9.J — así que queda vacío). Un WebhookSink real lo
// correlacionaría (efecto que lo transporte, o consulta a orders); DIFERIDO.
//
// # Qué faltaría para hacerlo REAL (TODO DIFERIDO — §9.I / §10, no implementar aquí)
//
//   - POST HTTP saliente al endpoint del CRM (1er cliente HTTP saliente del repo).
//   - OUTBOX DURABLE + reintentos con back-off (la nube no tiene cola propia; la
//     durabilidad la da hoy el outbox del Edge, ADR-0003): un POST best-effort
//     perdería eventos ante un CRM caído.
//   - tenant_integrations: endpoint + CREDENCIALES CIFRADAS por-tenant (Plan
//     011/012). tenant_settings (§9.G) es solo su germen.
//
// Contrato de Handle (heredado de EventSink): no bloquea indefinidamente, NUNCA
// filtra PII/credenciales y NUNCA aborta el avance del flujo (devuelve nil; un
// error de un sink real solo se loguearía). Registrar este sink en el fan-out NO
// cambia el comportamiento observable (menú/encuesta/carrito idénticos): no
// escribe BD, no envía WhatsApp, solo loguea.
type WebhookSink struct {
	log logger.Logger
}

// NewWebhookSink construye el stub del punto de inyección del CRM con el logger
// dado. Se registra como cualquier EventSink: flowruntime.WithEventSink(
// flowruntime.NewWebhookSink(log)) en main.go. Por defecto NO se registra (§9.I).
func NewWebhookSink(log logger.Logger) *WebhookSink {
	return &WebhookSink{log: log}
}

// crmItem es una línea del pedido en el contrato JSON hacia el CRM (§9.I).
type crmItem struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
}

// crmOrderPayload es el cuerpo JSON que un WebhookSink REAL enviaría al CRM/POS al
// cerrar un pedido (§9.I). CERO PII: contact es OPACO, el resto es dato de negocio.
type crmOrderPayload struct {
	Tenant    string    `json:"tenant"`
	Contact   string    `json:"contact"`
	OrderID   string    `json:"order_id"`
	Items     []crmItem `json:"items"`
	Total     float64   `json:"total"`
	Timestamp string    `json:"timestamp"`
}

// Handle es el NO-OP funcional del punto de inyección: NO entrega nada por red y
// NUNCA aborta (devuelve nil siempre). Loguea a Debug que "entregaría" el efecto
// al CRM; para cart_closed, además construye y serializa el payload del contrato
// (§9.I) y lo loguea —son datos de negocio sin PII, no eff.Payload crudo— para
// dejar el contrato verificable sin red.
func (s *WebhookSink) Handle(_ context.Context, ec EffectContext, eff modules.Effect) error {
	if s == nil || s.log == nil {
		return nil
	}
	if eff.Name != effCartClosed {
		// Navegación/telemetría/otros efectos: el punto de inyección no los entrega
		// (un CRM real filtraría por interés). Solo deja rastro de que existe.
		s.log.Debug("webhook (stub): efecto NO entregado al CRM (punto de inyección diferido)",
			"name", eff.Name,
			"tenant", ec.TenantID,
			"contact_id", ec.ContactID,
		)
		return nil
	}

	payload := buildCRMOrderPayload(ec, eff, time.Now().UTC())
	body, err := json.Marshal(payload)
	if err != nil {
		// Un fallo de serialización JAMÁS aborta el flujo: se loguea y sigue.
		s.log.Error("webhook (stub): serializando payload del CRM", "error", err, "tenant", ec.TenantID)
		return nil
	}
	// STUB: NO hay POST. Se loguea el payload que se ENVIARÍA (dato de negocio, sin
	// PII) para dejar el contrato del CRM verificable en campo sin red.
	s.log.Debug("webhook (stub): ENTREGARÍA cart_closed al CRM (no se envía — punto de inyección diferido)",
		"tenant", ec.TenantID,
		"contact_id", ec.ContactID,
		"total", payload.Total,
		"items", len(payload.Items),
		"payload_json", string(body),
	)
	return nil
}

// buildCRMOrderPayload arma el contrato JSON hacia el CRM (§9.I) a partir del
// EffectContext (tenant + contact opaco) y el payload de cart_closed (items +
// total). Reutiliza el parseo tolerante del PersistSink (cartItems/asFloat/
// asString) para casar la misma forma del efecto (in-process y round-trip JSON).
// now se inyecta para poder verificar la serialización de forma determinista.
func buildCRMOrderPayload(ec EffectContext, eff modules.Effect, now time.Time) crmOrderPayload {
	items := cartItems(eff.Payload, "")
	crmItems := make([]crmItem, 0, len(items))
	for _, it := range items {
		crmItems = append(crmItems, crmItem{
			SKU:       it.SKU,
			Label:     it.Label,
			Qty:       it.Qty,
			UnitPrice: it.UnitPrice,
		})
	}
	return crmOrderPayload{
		Tenant:    ec.TenantID,
		Contact:   ec.ContactID,
		OrderID:   asString(eff.Payload["order_id"]),
		Items:     crmItems,
		Total:     asFloat(eff.Payload["total"]),
		Timestamp: now.Format(time.RFC3339),
	}
}
