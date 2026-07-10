package cart

import (
	"context"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// expiryNotice antecede al reinicio del carrito cuando la orden abierta venció por
// TTL (design.md §4.3/§9.H). Vive aquí (no en el runtime) porque es cart-específico.
const expiryNotice = "⌛ Tu pedido anterior expiró. Empezamos de nuevo."

// ResumeStore es lo que la política de reanudación del carrito necesita LEER del
// almacén: la orden abierta (para el TTL) y los ajustes del tenant (page_size).
// Interfaz mínima (ISP) que satisface *store.PostgresRepository / *MemoryRepository.
type ResumeStore interface {
	GetOpenOrder(ctx context.Context, tenantID, contactID string) (store.Order, bool, error)
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
}

// ResumePolicy implementa modules.ResumePolicy para el carrito (Plan 027 · Ola 3 ·
// T8, cierra H9): TTL perezoso + auto-reinicio tras nivel terminal o expiración +
// siembra del page_size del tenant. Es un adaptador IMPURO (lee la BD); el Module
// (Render/Step) sigue PURO. Extrae del runtime toda la lógica cart-específica de
// reanudación (antes prepareCart/resumeCart/cartRestartable/cartTerminal literales).
type ResumePolicy struct {
	store ResumeStore
	now   func() time.Time
}

// NewResumePolicy construye la política sobre el almacén dado.
func NewResumePolicy(s ResumeStore) *ResumePolicy {
	return &ResumePolicy{store: s, now: time.Now}
}

// Restart decide el reinicio del carrito: si la orden abierta venció por TTL o la
// sub-máquina quedó en nivel terminal (cerrado/cancelado). Ante vencimiento sintetiza
// el efecto cart_expired (para que el proyector transicione la orden a "expired",
// coherencia BD↔conversación) y devuelve el aviso. Un carrito terminal NO tiene
// orden "open" (el cierre/cancelación ya la transicionó), así que ambos criterios no
// colisionan. En navegación normal devuelve restart=false.
func (p *ResumePolicy) Restart(ctx context.Context, tenantID, contactID string, vars map[string]any) (bool, string, []modules.Effect, error) {
	order, found, err := p.store.GetOpenOrder(ctx, tenantID, contactID)
	if err != nil {
		return false, "", nil, fmt.Errorf("cart: orden abierta: %w", err)
	}
	expired := found && orderExpired(order, p.now())
	terminal := isTerminal(vars)
	if !expired && !terminal {
		return false, "", nil, nil
	}
	if expired {
		return true, expiryNotice, []modules.Effect{event(EffectCartExpired, map[string]any{})}, nil
	}
	return true, "", nil, nil
}

// Seed inyecta en Vars el page_size REAL del tenant (tenant_settings.page_size,
// default 5) para que el módulo pagine con la config del tenant sin hacer I/O
// (design.md §9.E). El runtime lo llama solo en navegación normal del carrito.
func (p *ResumePolicy) Seed(ctx context.Context, tenantID string, vars map[string]any) error {
	settings, err := p.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("cart: config de tenant (page_size): %w", err)
	}
	vars[VarPageSize] = settings.PageSize
	return nil
}

// isTerminal dice si la sub-máquina del carrito quedó en un nivel terminal (pedido
// confirmado o cancelado). Reusa loadState (misma FORMA que el módulo), sin literales
// duplicados en el runtime.
func isTerminal(vars map[string]any) bool {
	lvl := loadState(vars).Level
	return lvl == LevelClosed || lvl == LevelCancelled
}

// orderExpired indica si una orden tiene TTL fijado (expires_at no-cero) y ya venció
// respecto a now. Sin expires_at (zero) NUNCA expira.
func orderExpired(o store.Order, now time.Time) bool {
	return !o.ExpiresAt.IsZero() && o.ExpiresAt.Before(now)
}
