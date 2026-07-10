package modules

import "context"

// EffectMeta lleva la identidad SIN PII de la conversación que produjo un efecto
// (espejo de runtime.EffectContext, declarado en el paquete NEUTRAL de plugins para
// que los proyectores de cada módulo no importen el runtime). ContactID es OPACO
// (contacts.contact_id, Plan 010 / ADR-0010); NUNCA número/JID en claro.
type EffectMeta struct {
	TenantID    string
	ContactID   string
	SessionID   string
	FlowID      string
	FlowVersion int
}

// Projector es la capacidad IMPURA de un módulo para MATERIALIZAR sus efectos en
// tablas tipadas (Plan 027 · Ola 3 · T8, cierra H10). Reemplaza el switch central
// del PersistSink: cada módulo aporta su proyector —wired con el store— y el sink lo
// ejecuta genéricamente tras InsertFlowEvent, sin conocer los efectos de ningún
// módulo. El Module PURO (Render/Step) sigue sin I/O; el proyector es un adaptador
// APARTE que sí toca la BD. Añadir un módulo con proyección NO obliga a editar el
// sink (OCP).
type Projector interface {
	// Handles indica si este proyector materializa el efecto con ese Name lógico.
	Handles(effectName string) bool
	// Project materializa el efecto en las tablas del módulo. El sink solo lo llama
	// para efectos cuyo Handles devolvió true.
	Project(ctx context.Context, meta EffectMeta, eff Effect) error
}

// ResumePolicy es la capacidad IMPURA de un módulo para gobernar la REANUDACIÓN de
// una conversación en uno de sus nodos (Plan 027 · Ola 3 · T8, cierra H9): reinicio
// por estado terminal o por expiración de un recurso externo (p. ej. la orden del
// carrito por TTL) y siembra de Vars de navegación. El runtime la consulta por tipo
// de nodo REGISTRADO, sacando del engine el conocimiento cart-específico. El Module
// sigue puro; la política es un adaptador que lee el store.
type ResumePolicy interface {
	// Restart decide si, al reanudar una conversación cuyo sub-estado es `vars`, hay
	// que reiniciarla de cero. Devuelve el aviso a anteponer al render fresco ("" si
	// ninguno) y los efectos a SINTETIZAR (p. ej. cart_expired) para que el runtime
	// los despache por el fan-out (coherencia BD↔conversación).
	Restart(ctx context.Context, tenantID, contactID string, vars map[string]any) (restart bool, notice string, effects []Effect, err error)
	// Seed inyecta en `vars` lo que el módulo necesita en navegación normal (p. ej.
	// el page_size del tenant). Solo se llama cuando NO hay reinicio; muta el mapa
	// (el runtime garantiza que no es nil).
	Seed(ctx context.Context, tenantID string, vars map[string]any) error
}
