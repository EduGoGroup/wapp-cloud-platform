package runtime

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// Nombres de efecto que el PersistSink reconoce y proyecta. Se replican como
// literales (mismo patrón que "survey_answer") para NO acoplar el sink al paquete
// de cada módulo: son el contrato de nombres de efecto (design.md §3.3/§9.J).
const (
	effSurveyAnswer    = "survey_answer"
	effCartItemAdded   = "item_added"
	effCartClosed      = "cart_closed"
	effCartCancelled   = "cart_cancelled"
	effCartExpired     = "cart_expired"
	orderStatusOpen    = "open"
	orderStatusClosed  = "closed"
	orderStatusCancel  = "cancelled"
	orderStatusExpired = "expired"
)

// PersistSink es la implementación de EventSink que MATERIALIZA cada efecto en el
// outbox append-only flow_events y PROYECTA los efectos de negocio a sus tablas
// tipadas: survey_answer → survey_results (Plan 014/015) y los del carrito
// (Plan 016) → orders/order_items. El outbox flow_events es la bitácora completa
// (base de la telemetría por-paso, Δt entre efectos); la proyección es la vista
// consultable (GROUP BY, joins) que un JSONB no da con índice.
//
// Identidad de la orden (design.md §3.4): UNA orden "open" por (tenant_id,
// contact_id). item_added la asegura (crea si no hay); cart_closed la cierra e
// inserta las líneas; cart_cancelled/cart_expired la transicionan. La fuente de
// verdad de las líneas es cart_closed (no se insertan incrementalmente). La
// idempotencia es HEREDADA de la dedupe por last_wa_message_id del runtime.
type PersistSink struct {
	repo store.Repository
}

// NewPersistSink construye el sink de persistencia con el repositorio dado.
func NewPersistSink(repo store.Repository) *PersistSink {
	return &PersistSink{repo: repo}
}

// Handle persiste el efecto en flow_events (SIEMPRE) y, según su Name, proyecta la
// tabla de negocio correspondiente. Un fallo del INSERT/proyección se devuelve (el
// dispatcher del runtime lo LOGUEA; NO aborta el avance: el estado ya está
// persistido). Menú/encuesta nunca tocan orders (sus efectos caen en el default).
func (s *PersistSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	fe := store.FlowEvent{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		Kind:        eff.Kind,
		Name:        eff.Name,
		Payload:     eff.Payload,
	}
	if err := s.repo.InsertFlowEvent(ctx, fe); err != nil {
		return err
	}

	switch eff.Name {
	case effSurveyAnswer:
		return s.projectSurveyAnswer(ctx, ec, eff)
	case effCartItemAdded:
		return s.ensureOpenOrder(ctx, ec)
	case effCartClosed:
		return s.closeOrder(ctx, ec, eff)
	case effCartCancelled:
		return s.transitionOpenOrder(ctx, ec, orderStatusCancel)
	case effCartExpired:
		return s.transitionOpenOrder(ctx, ec, orderStatusExpired)
	default:
		// Efecto de navegación/telemetría sin proyección tipada (ya quedó en
		// flow_events): menú, category_selected, item_viewed, cart_started, …
		return nil
	}
}

// projectSurveyAnswer proyecta survey_answer → survey_results (misma fila que el
// flush del Plan 014). Aserción de tipo defensiva: claves ausentes o de otro tipo
// → se OMITE la proyección sin panica (el efecto ya quedó en flow_events).
func (s *PersistSink) projectSurveyAnswer(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	qid, ok1 := eff.Payload["question_id"].(string)
	code, ok2 := eff.Payload["answer_code"].(string)
	if !ok1 || !ok2 {
		return nil
	}
	return s.repo.InsertResults(ctx, []store.SurveyResult{{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		QuestionID:  qid,
		AnswerCode:  code,
	}})
}

// ensureOpenOrder garantiza que exista UNA orden "open" para (tenant, contact) al
// primer item_added (design.md §3.4) y FIJA/REFRESCA su TTL (design.md §4.3/§9.H):
// expires_at = now + tenant_settings.order_ttl (default 1h si el tenant no tiene
// fila; GetTenantSettings devuelve los defaults sin error). Idempotente por
// identidad de negocio: si ya hay una abierta NO crea otra, pero la "toca"
// (refresca expires_at) en cada item_added para que el pedido activo no venza
// mientras el usuario sigue agregando. La evaluación del vencimiento es PEREZOSA
// (al reanudar, en el runtime); aquí solo se fija la marca.
func (s *PersistSink) ensureOpenOrder(ctx context.Context, ec EffectContext) error {
	settings, err := s.repo.GetTenantSettings(ctx, ec.TenantID)
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(settings.OrderTTL)
	existing, found, err := s.repo.GetOpenOrder(ctx, ec.TenantID, ec.ContactID)
	if err != nil {
		return err
	}
	if found {
		// Touch: refresca expires_at conservando id/created_at/total (UpsertOrder es
		// idempotente por ID; MemoryRepository preserva created_at, Postgres hace
		// ON CONFLICT (id) DO UPDATE).
		existing.ExpiresAt = expiresAt
		return s.repo.UpsertOrder(ctx, existing)
	}
	return s.repo.UpsertOrder(ctx, store.Order{
		ID:        uuid.NewString(),
		TenantID:  ec.TenantID,
		ContactID: ec.ContactID,
		SessionID: ec.SessionID,
		Status:    orderStatusOpen,
		ExpiresAt: expiresAt,
	})
}

// closeOrder proyecta cart_closed: cierra la orden "open" (o crea una "closed"
// coherente si no hubiera abierta) con el total del payload e inserta TODAS las
// líneas (fuente de verdad). Delega en repo.CloseOrder para que la transición de
// estado y la inserción de líneas sean ATÓMICAS (Plan 027 · Ola 1 · T4, cierra H4):
// antes eran GetOpenOrder + MarkOrderStatus/UpsertOrder + InsertOrderItems sueltos,
// y un fallo entre medias dejaba una orden "closed" sin líneas. El orderID lo fija
// CloseOrder, así que las líneas se pasan sin él (cartItems con "" ).
func (s *PersistSink) closeOrder(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	return s.repo.CloseOrder(ctx, store.OrderClose{
		TenantID:  ec.TenantID,
		ContactID: ec.ContactID,
		SessionID: ec.SessionID,
		Total:     asFloat(eff.Payload["total"]),
		Items:     cartItems(eff.Payload, ""),
	})
}

// transitionOpenOrder lleva la orden "open" a cancelled/expired (design.md §3.4).
// Sin orden abierta es un no-op sin error (idempotente / nada que transicionar).
func (s *PersistSink) transitionOpenOrder(ctx context.Context, ec EffectContext, status string) error {
	order, found, err := s.repo.GetOpenOrder(ctx, ec.TenantID, ec.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return s.repo.MarkOrderStatus(ctx, order.ID, status, order.Total)
}

// cartItems extrae las líneas del payload de cart_closed a []store.OrderItem.
// Tolera ambas formas de la lista: el camino en-proceso ([]map[string]any que
// construye el módulo) y el round-trip JSON ([]any de map[string]any). Ítems mal
// formados se omiten sin panica.
func cartItems(payload map[string]any, orderID string) []store.OrderItem {
	var out []store.OrderItem
	switch items := payload["items"].(type) {
	case []map[string]any:
		for _, m := range items {
			out = append(out, orderItemFromMap(m, orderID))
		}
	case []any:
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, orderItemFromMap(m, orderID))
			}
		}
	}
	return out
}

func orderItemFromMap(m map[string]any, orderID string) store.OrderItem {
	return store.OrderItem{
		OrderID:   orderID,
		SKU:       asString(m["sku"]),
		Label:     asString(m["label"]),
		Qty:       asInt(m["qty"]),
		UnitPrice: asFloat(m["unit_price"]),
	}
}

// asFloat/asInt/asString normalizan valores del payload tolerando el tipo nativo
// (in-process) y el round-trip JSON (números como float64). Valor ausente/de otro
// tipo → cero.
func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
