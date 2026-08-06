package cart

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// Estados del ciclo de vida de una solicitud (public.intakes). Viven aquí porque la
// proyección del carrito es su dueña (design.md §3.4).
const (
	intakeStatusOpen      = "open"
	intakeStatusClosed    = "closed"
	intakeStatusCancelled = "cancelled"
	intakeStatusExpired   = "expired"
)

// ProjectionStore es lo que el proyector del carrito necesita del almacén para
// materializar sus efectos en intakes/intake_items. Interfaz mínima (ISP) que
// satisface *store.PostgresRepository / *MemoryRepository.
type ProjectionStore interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	UpsertIntake(ctx context.Context, o store.Intake) error
	MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error
	CloseIntake(ctx context.Context, in store.IntakeClose) error
}

// Projector implementa modules.Projector para los efectos del carrito (Plan 027 ·
// Ola 3 · T8, cierra H10): item_added asegura la solicitud "open" (+refresca TTL),
// cart_closed cierra atómicamente solicitud+líneas, y cart_cancelled/cart_expired
// transicionan la solicitud. Es un adaptador IMPURO; produce EXACTAMENTE las mismas
// filas que producía el switch central del PersistSink (retrofit por efectos, §9.D).
type Projector struct {
	store ProjectionStore
	now   func() time.Time
}

// NewProjector construye el proyector del carrito sobre el almacén dado.
func NewProjector(s ProjectionStore) *Projector {
	return &Projector{store: s, now: time.Now}
}

// Handles reconoce los efectos que el carrito PROYECTA a tablas tipadas. Los efectos
// de navegación/telemetría (cart_started, category_selected, …) NO se proyectan (ya
// quedan en flow_events por el sink) y devuelven false.
func (Projector) Handles(name string) bool {
	switch name {
	case EffectItemAdded, EffectCartClosed, EffectCartCancelled, EffectCartExpired:
		return true
	default:
		return false
	}
}

// Project materializa el efecto del carrito. El sink solo lo llama para los efectos
// cuyo Handles devolvió true.
func (p *Projector) Project(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	switch eff.Name {
	case EffectItemAdded:
		return p.ensureOpenIntake(ctx, meta)
	case EffectCartClosed:
		return p.closeIntake(ctx, meta, eff)
	case EffectCartCancelled:
		return p.transitionOpenIntake(ctx, meta, intakeStatusCancelled)
	case EffectCartExpired:
		return p.transitionOpenIntake(ctx, meta, intakeStatusExpired)
	default:
		return nil
	}
}

// ensureOpenIntake garantiza UNA solicitud "open" para (tenant, contact) al primer
// item_added (design.md §3.4) y FIJA/REFRESCA su TTL (expires_at = now +
// tenant_settings.order_ttl). Idempotente por identidad de negocio: si ya hay abierta
// NO crea otra, pero la "toca" (refresca expires_at) para que el pedido activo no
// venza mientras el usuario sigue agregando. La evaluación del vencimiento es
// perezosa (al reanudar, en la ResumePolicy); aquí solo se fija la marca.
func (p *Projector) ensureOpenIntake(ctx context.Context, meta modules.EffectMeta) error {
	settings, err := p.store.GetTenantSettings(ctx, meta.TenantID)
	if err != nil {
		return err
	}
	expiresAt := p.now().Add(settings.OrderTTL)
	existing, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if found {
		existing.ExpiresAt = expiresAt
		return p.store.UpsertIntake(ctx, existing)
	}
	return p.store.UpsertIntake(ctx, store.Intake{
		ID:        uuid.NewString(),
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Status:    intakeStatusOpen,
		ExpiresAt: expiresAt,
	})
}

// closeIntake proyecta cart_closed: cierra ATÓMICAMENTE la solicitud abierta (o crea una
// "closed" coherente) con el total del payload e inserta TODAS las líneas (fuente de
// verdad). Delega en store.CloseIntake (una transacción, Plan 027 · Ola 1 · T4).
func (p *Projector) closeIntake(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	return p.store.CloseIntake(ctx, store.IntakeClose{
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Total:     modules.AsFloat(eff.Payload["total"]),
		Items:     cartItems(eff.Payload),
	})
}

// transitionOpenIntake lleva la solicitud "open" a cancelled/expired (design.md §3.4).
// Sin solicitud abierta es un no-op sin error (idempotente / nada que transicionar).
func (p *Projector) transitionOpenIntake(ctx context.Context, meta modules.EffectMeta, status string) error {
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return p.store.MarkIntakeStatus(ctx, intake.ID, status, intake.Total)
}

// cartItems extrae las líneas del payload de cart_closed a []store.IntakeItem. Tolera
// ambas formas de la lista: el camino en-proceso ([]map[string]any que construye el
// módulo) y el round-trip JSON ([]any de map[string]any). Ítems mal formados se
// omiten sin panica. El IntakeID lo fija store.CloseIntake.
func cartItems(payload map[string]any) []store.IntakeItem {
	var out []store.IntakeItem
	switch items := payload["items"].(type) {
	case []map[string]any:
		for _, m := range items {
			out = append(out, intakeItemFromMap(m))
		}
	case []any:
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, intakeItemFromMap(m))
			}
		}
	}
	return out
}

func intakeItemFromMap(m map[string]any) store.IntakeItem {
	return store.IntakeItem{
		SKU:       modules.AsString(m["sku"]),
		Label:     modules.AsString(m["label"]),
		Qty:       modules.AsInt(m["qty"]),
		UnitPrice: modules.AsFloat(m["unit_price"]),
	}
}
