// Porta internal/tenantvars/memory.go @ 3a21138

package tenantvars

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el Postgres —reemplazo TOTAL en Replace, aislamiento por tenant, orden por
// clave y un updated_at que solo se mueve cuando el valor cambia— para que un test
// de handler contra este store diga algo verdadero sobre producción. Lo comprueba
// la suite tenantvarshelpertest.Contrato, la misma que corre contra Postgres.
//
// Se puede usar desde varias goroutines a la vez: cada método es atómico.
type MemoryStore struct{}

// NewMemoryStore construye un store en memoria vacío (ningún tenant tiene
// variables), listo para usar: marca UpdatedAt con el reloj del proceso mientras
// no se le inyecte otro con SetClock.
func NewMemoryStore() *MemoryStore {
	panic(pendiente.Implementar("tenantvars.NewMemoryStore"))
}

var _ Store = (*MemoryStore)(nil)

// SetClock fija el reloj del store (tests que afirman sobre updated_at): desde la
// llamada, toda variable que Replace dé de alta o cambie de valor lleva en
// UpdatedAt lo que devuelva now en ese momento. No remarca las ya guardadas.
func (m *MemoryStore) SetClock(now func() time.Time) {
	panic(pendiente.Implementar("tenantvars.MemoryStore.SetClock"))
}

// List devuelve las variables del tenant ordenadas por clave (byte a byte). Sin
// variables devuelve un slice vacío, no nil; nunca devuelve error. El slice es
// del llamante: modificarlo no cambia lo guardado.
func (m *MemoryStore) List(ctx context.Context, tenantID string) ([]Variable, error) {
	panic(pendiente.Implementar("tenantvars.MemoryStore.List"))
}

// Replace deja el conjunto del tenant EXACTAMENTE igual a vars (las ausentes se
// borran; con vars vacío o nil el tenant queda sin variables), conservando el
// UpdatedAt de las que no cambiaron de valor y marcando con el reloj las que son
// alta o cambian. Solo toca al tenant pedido y no se queda con el mapa del
// llamante: modificarlo después no cambia lo guardado. Nunca devuelve error.
func (m *MemoryStore) Replace(ctx context.Context, tenantID string, vars map[string]string) error {
	panic(pendiente.Implementar("tenantvars.MemoryStore.Replace"))
}
