// Porta internal/tenantvars/memory.go @ 3a21138

package tenantvars

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el Postgres —reemplazo TOTAL en Replace, aislamiento por tenant, orden por
// clave y un updated_at que solo se mueve cuando el valor cambia— para que un test
// de handler contra este store diga algo verdadero sobre producción. Lo comprueba
// la suite tenantvarshelpertest.Contrato, la misma que corre contra Postgres.
//
// Se puede usar desde varias goroutines a la vez: cada método es atómico.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]map[string]Variable // tenant → clave → variable
	now  func() time.Time
}

// NewMemoryStore construye un store en memoria vacío (ningún tenant tiene
// variables), listo para usar: marca UpdatedAt con el reloj del proceso mientras
// no se le inyecte otro con SetClock.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]map[string]Variable{}, now: time.Now}
}

var _ Store = (*MemoryStore)(nil)

// SetClock fija el reloj del store (tests que afirman sobre updated_at): desde la
// llamada, toda variable que Replace dé de alta o cambie de valor lleva en
// UpdatedAt lo que devuelva now en ese momento. No remarca las ya guardadas.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// List devuelve las variables del tenant ordenadas por clave (byte a byte). Sin
// variables devuelve un slice vacío, no nil; nunca devuelve error. El slice es
// del llamante: modificarlo no cambia lo guardado.
func (m *MemoryStore) List(_ context.Context, tenantID string) ([]Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// make, no var: sin variables se devuelve un slice vacío, como el Postgres.
	out := make([]Variable, 0, len(m.rows[tenantID]))
	for _, v := range m.rows[tenantID] {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Variable) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// Replace deja el conjunto del tenant EXACTAMENTE igual a vars (las ausentes se
// borran; con vars vacío o nil el tenant queda sin variables), conservando el
// UpdatedAt de las que no cambiaron de valor y marcando con el reloj las que son
// alta o cambian. Solo toca al tenant pedido y no se queda con el mapa del
// llamante: modificarlo después no cambia lo guardado. Nunca devuelve error.
func (m *MemoryStore) Replace(_ context.Context, tenantID string, vars map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.rows[tenantID]
	// El conjunto nuevo se arma aparte y sustituye al anterior entero: lo que no viene en
	// vars no pasa a next, y eso es el borrado.
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
