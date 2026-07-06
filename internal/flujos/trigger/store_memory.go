package trigger

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// MemoryStore es una implementación en memoria de TriggerStore, segura para
// concurrencia. Pensada para unit tests CI-safe (sin BD) y para dobles del
// runtime. Imita la semántica de PostgresStore: asigna trigger_id en Insert y
// filtra SIEMPRE por tenant_id.
type MemoryStore struct {
	mu    sync.Mutex
	rules map[string]Rule // trigger_id → Rule (trigger_id es UUID global único)
}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rules: make(map[string]Rule)}
}

// Insert asigna un trigger_id nuevo e ignora r.TriggerID del argumento.
func (s *MemoryStore) Insert(_ context.Context, r Rule) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.TriggerID = uuid.NewString()
	s.rules[r.TriggerID] = r
	return r, nil
}

// List devuelve todas las reglas del tenant (sin filtro de kind ni sesión: es la
// vista de administración), ordenadas de forma estable por trigger_id para dar un
// orden determinista al llamante.
func (s *MemoryStore) List(_ context.Context, tenantID string) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter(tenantID, func(Rule) bool { return true }), nil
}

// ListByKind devuelve las reglas del tenant de un kind dado aplicables a la sesión:
// SessionID == sessionID (específica) O SessionID == "" (global). sessionID vacío
// ⇒ solo las globales (Plan 020 · T4).
func (s *MemoryStore) ListByKind(_ context.Context, tenantID, sessionID string, k Kind) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter(tenantID, func(r Rule) bool {
		return r.Kind == k && (r.SessionID == sessionID || r.SessionID == "")
	}), nil
}

// filter recoge las reglas del tenant que satisfacen keep, en orden determinista
// por trigger_id. Requiere el mutex tomado.
func (s *MemoryStore) filter(tenantID string, keep func(Rule) bool) []Rule {
	out := make([]Rule, 0)
	for _, r := range s.rules {
		if r.TenantID != tenantID || !keep(r) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TriggerID < out[j].TriggerID })
	return out
}

// Get devuelve la regla del tenant por trigger_id; ErrTriggerNotFound si no
// existe o pertenece a otro tenant (INV-8).
func (s *MemoryStore) Get(_ context.Context, tenantID, triggerID string) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rules[triggerID]
	if !ok || r.TenantID != tenantID {
		return Rule{}, ErrTriggerNotFound
	}
	return r, nil
}

// Delete borra la regla del tenant por trigger_id; ErrTriggerNotFound si no
// existe o pertenece a otro tenant (INV-8).
func (s *MemoryStore) Delete(_ context.Context, tenantID, triggerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rules[triggerID]
	if !ok || r.TenantID != tenantID {
		return ErrTriggerNotFound
	}
	delete(s.rules, triggerID)
	return nil
}
