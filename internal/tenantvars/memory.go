package tenantvars

import (
	"context"
	"slices"
	"sync"
	"time"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el Postgres —reemplazo TOTAL en Replace, aislamiento por tenant, orden por
// clave y un updated_at que solo se mueve cuando el valor cambia— para que un test
// de handler contra este store diga algo verdadero sobre producción.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]map[string]Variable // tenant → clave → variable
	now  func() time.Time
}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]map[string]Variable{}, now: time.Now}
}

// SetClock fija el reloj del store (tests que afirman sobre updated_at).
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// List devuelve las variables del tenant ordenadas por clave.
func (m *MemoryStore) List(_ context.Context, tenantID string) ([]Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Variable, 0, len(m.rows[tenantID]))
	for _, v := range m.rows[tenantID] {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Variable) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}

// Replace deja el conjunto del tenant EXACTAMENTE igual a vars (las ausentes se
// borran), conservando el updated_at de las que no cambiaron de valor.
func (m *MemoryStore) Replace(_ context.Context, tenantID string, vars map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.rows[tenantID]
	next := make(map[string]Variable, len(vars))
	for k, v := range vars {
		if old, ok := prev[k]; ok && old.Value == v {
			next[k] = old // mismo valor ⇒ la marca de cambio NO se mueve
			continue
		}
		next[k] = Variable{Key: k, Value: v, UpdatedAt: m.now()}
	}
	m.rows[tenantID] = next
	return nil
}
