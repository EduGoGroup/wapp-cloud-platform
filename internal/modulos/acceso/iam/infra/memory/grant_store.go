// Porta internal/iam/infra/memory/grant_store.go @ 9a77307 (y removeGrant, de store.go)

package memory

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// GrantStore es el doble en memoria de iam_user_grants. Pasa outhelpertest.ContratoGrantRepo.
type GrantStore struct {
	mu     sync.Mutex
	grants map[string][]domain.Grant // userID → overrides
}

// NewGrantStore crea un GrantStore vacío.
func NewGrantStore() *GrantStore { return &GrantStore{grants: make(map[string][]domain.Grant)} }

var _ out.GrantRepo = (*GrantStore)(nil)

// GrantsOfUser implementa out.GrantRepo. Devuelve una copia.
func (s *GrantStore) GrantsOfUser(_ context.Context, userID string) ([]domain.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Grant(nil), s.grants[userID]...), nil
}

// AddUserGrant implementa out.GrantRepo (idempotente por pattern+effect, como el ON CONFLICT
// (user_id, pattern, effect) DO NOTHING de la tabla).
func (s *GrantStore) AddUserGrant(_ context.Context, userID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.grants[userID] {
		if ex == g {
			return nil
		}
	}
	s.grants[userID] = append(s.grants[userID], g)
	return nil
}

// RemoveUserGrant implementa out.GrantRepo (no-op si no estaba).
func (s *GrantStore) RemoveUserGrant(_ context.Context, userID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[userID] = removeGrant(s.grants[userID], g)
	return nil
}

// removeGrant devuelve la lista sin el grant dado (comparación por valor). La comparten
// GrantStore y RoleStore; vivía en store.go y viene aquí para que este fichero no dependa del
// agregado.
func removeGrant(list []domain.Grant, g domain.Grant) []domain.Grant {
	kept := make([]domain.Grant, 0, len(list))
	for _, ex := range list {
		if ex != g {
			kept = append(kept, ex)
		}
	}
	return kept
}
