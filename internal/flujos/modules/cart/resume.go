package cart

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ResumeStore es lo que la política de reanudación del carrito necesita LEER del
// almacén: los ajustes del tenant (page_size). Interfaz mínima (ISP) que
// satisface *store.PostgresRepository / *MemoryRepository.
//
// Hasta T4.7 también leía la solicitud abierta, para decidir si había VENCIDO.
// D-041.16 derogó ese reloj —nada vence por tiempo— y con él la única razón por
// la que esta política tocaba public.intakes: hoy la reanudación se decide solo
// con el sub-estado del carrito, que ya viene en Vars.
type ResumeStore interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
}

// ResumePolicy implementa modules.ResumePolicy para el carrito (Plan 027 · Ola 3 ·
// T8, cierra H9): auto-reinicio tras nivel terminal + siembra del page_size del
// tenant. Es un adaptador IMPURO (lee la BD); el Module (Render/Step) sigue PURO.
// Extrae del runtime toda la lógica cart-específica de reanudación (antes
// prepareCart/resumeCart/cartRestartable/cartTerminal literales).
type ResumePolicy struct {
	store ResumeStore
}

// NewResumePolicy construye la política sobre el almacén dado.
func NewResumePolicy(s ResumeStore) *ResumePolicy {
	return &ResumePolicy{store: s}
}

// Restart decide el reinicio del carrito: SOLO si la sub-máquina quedó en nivel
// terminal (cerrado/cancelado). En navegación normal devuelve restart=false.
//
// El otro criterio que vivía aquí —la solicitud abierta VENCIDA por TTL, que
// sintetizaba cart_expired y avisaba «tu pedido anterior expiró»— quedó DEROGADO
// por D-041.16 (T4.7): los objetos de negocio no mueren por tiempo, mueren por
// acción humana. Un carrito armado el lunes sigue ahí el miércoles, con sus
// líneas, y el pedido que nadie rescata lo descarta el dueño a mano (D-041.18),
// nunca un reloj. Por eso esta política ya no lee la solicitud, ya no consulta el
// tiempo y ya no sintetiza efectos: la firma conserva los dos huecos porque el
// puerto es genérico (otra política sí podría usarlos), no porque el carrito los
// llene.
func (p *ResumePolicy) Restart(_ context.Context, _, _ string, vars map[string]any) (bool, string, []modules.Effect, error) {
	return isTerminal(vars), "", nil, nil
}

// Seed inyecta en Vars la config del tenant que el módulo PURO necesita y no puede
// leer: el page_size de la paginación (tenant_settings.page_size, default 5,
// design.md §9.E) y el CHECKLIST del comprador (buyer_fields, T4.5 · D-041.13). El
// runtime lo llama solo en navegación normal del carrito, así que el checklist se
// relee en cada mensaje: cambiarlo en la config alcanza a las conversaciones vivas
// sin migrar su estado.
//
// Lo que se siembra es CONFIGURACIÓN —claves y etiquetas de lo que se va a pedir—,
// nunca respuestas: Vars acaba en public.flow_state, JSONB en claro, y lo que el
// cliente conteste no pasa por ahí (ver buyer.go).
func (p *ResumePolicy) Seed(ctx context.Context, tenantID string, vars map[string]any) error {
	settings, err := p.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("cart: config de tenant (page_size, buyer_fields): %w", err)
	}
	vars[VarPageSize] = settings.PageSize
	vars[VarBuyerFields] = settings.BuyerFields
	return nil
}

// isTerminal dice si la sub-máquina del carrito quedó en un nivel terminal (pedido
// confirmado o cancelado). Reusa loadState (misma FORMA que el módulo), sin literales
// duplicados en el runtime.
func isTerminal(vars map[string]any) bool {
	lvl := loadState(vars).Level
	return lvl == LevelClosed || lvl == LevelCancelled
}
