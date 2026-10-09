// Porta internal/flujos/trigger/store_memory.go @ c0c0c03

package trigger

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MemoryStore es una implementación en memoria de TriggerStore, segura para
// concurrencia. Pensada para unit tests CI-safe (sin BD) y para dobles del
// runtime. Imita la semántica de PostgresStore: asigna trigger_id en Insert y
// filtra SIEMPRE por tenant_id. Cumple triggerhelpertest.Contrato, la misma suite
// que PostgresStore.
//
// Donde NO imita a Postgres, y la suite no lo afirma: no valida que tenant_id ni
// trigger_id tengan forma de UUID (Postgres devuelve un error de sintaxis; aquí un
// trigger_id cualquiera que no exista es ErrTriggerNotFound).
type MemoryStore struct{}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	panic(pendiente.Implementar("trigger.NewMemoryStore"))
}

// Insert asigna un trigger_id nuevo (un UUID) e ignora r.TriggerID del argumento.
// Devuelve la regla guardada: la del argumento, campo a campo, con su TriggerID.
// Nunca devuelve error.
func (s *MemoryStore) Insert(_ context.Context, _ Rule) (Rule, error) {
	panic(pendiente.Implementar("trigger.MemoryStore.Insert"))
}

// List devuelve todas las reglas del tenant (sin filtro de kind ni sesión: es la
// vista de administración), ordenadas de forma estable por trigger_id para dar un
// orden determinista al llamante. Sin reglas, un slice vacío no nil.
func (s *MemoryStore) List(_ context.Context, _ string) ([]Rule, error) {
	panic(pendiente.Implementar("trigger.MemoryStore.List"))
}

// ListByKind devuelve las reglas del tenant de un kind dado aplicables a la sesión:
// SessionID == sessionID (específica) O SessionID == "" (global). sessionID vacío
// ⇒ solo las globales (Plan 020 · T4). Ordenadas por trigger_id; sin reglas, un
// slice vacío no nil.
func (s *MemoryStore) ListByKind(_ context.Context, _, _ string, _ Kind) ([]Rule, error) {
	panic(pendiente.Implementar("trigger.MemoryStore.ListByKind"))
}

// Get devuelve la regla del tenant por trigger_id; ErrTriggerNotFound si no
// existe o pertenece a otro tenant (INV-8).
func (s *MemoryStore) Get(_ context.Context, _, _ string) (Rule, error) {
	panic(pendiente.Implementar("trigger.MemoryStore.Get"))
}

// Delete borra la regla del tenant por trigger_id; ErrTriggerNotFound si no
// existe o pertenece a otro tenant (INV-8), y en ese caso no borra nada.
func (s *MemoryStore) Delete(_ context.Context, _, _ string) error {
	panic(pendiente.Implementar("trigger.MemoryStore.Delete"))
}
