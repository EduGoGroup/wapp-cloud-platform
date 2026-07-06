package trigger

import (
	"context"
	"errors"
)

// ErrTriggerNotFound lo devuelven Get/Delete cuando no existe la regla para el
// (tenant_id, trigger_id) dado (equivalente a sql.ErrNoRows en Postgres).
var ErrTriggerNotFound = errors.New("regla de disparo no encontrada")

// Store es el contrato de persistencia de las reglas de disparo (flow_triggers).
// TODAS las operaciones están parametrizadas por tenant_id (INV-8: aislamiento
// por tenant; una regla de otro tenant NUNCA es visible).
//
// (Nombrada Store, no TriggerStore, para evitar el stutter trigger.TriggerStore;
// idiomática como store.Repository.)
//
// Implementaciones: MemoryStore (unit CI-safe) y PostgresStore (integración).
type Store interface {
	// Insert persiste una regla nueva. El repositorio asigna trigger_id
	// (gen_random_uuid, RETURNING) e ignora r.TriggerID del argumento; devuelve
	// la regla persistida con su TriggerID poblado.
	Insert(ctx context.Context, r Rule) (Rule, error)
	// List devuelve todas las reglas del tenant.
	List(ctx context.Context, tenantID string) ([]Rule, error)
	// ListByKind devuelve las reglas del tenant de un kind dado (keyword|fallback|escape)
	// que aplican a la sesión dada: session_id = sessionID O session_id NULL (globales).
	// sessionID vacío ("") ⇒ solo las globales (Plan 020 · T4).
	ListByKind(ctx context.Context, tenantID, sessionID string, k Kind) ([]Rule, error)
	// Get devuelve una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no existe.
	Get(ctx context.Context, tenantID, triggerID string) (Rule, error)
	// Delete borra una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no existía.
	Delete(ctx context.Context, tenantID, triggerID string) error
}
