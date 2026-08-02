package memory

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// GrantStore es el doble en memoria de iam_user_grants.
type GrantStore struct {
	mu     sync.Mutex
	grants map[string][]domain.Grant // userID → overrides
}

// NewGrantStore crea un GrantStore vacío.
func NewGrantStore() *GrantStore { return &GrantStore{grants: make(map[string][]domain.Grant)} }

var _ out.GrantRepo = (*GrantStore)(nil)

// GrantsOfUser implementa out.GrantRepo.
func (s *GrantStore) GrantsOfUser(_ context.Context, userID string) ([]domain.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Grant(nil), s.grants[userID]...), nil
}

// AddUserGrant implementa out.GrantRepo (idempotente).
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

// RemoveUserGrant implementa out.GrantRepo.
func (s *GrantStore) RemoveUserGrant(_ context.Context, userID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[userID] = removeGrant(s.grants[userID], g)
	return nil
}
