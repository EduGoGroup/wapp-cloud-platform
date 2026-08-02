package memory

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// MembershipStore implementa out.MembershipRepo en memoria (tabla
// tenant_members). Conserva el orden de alta, como el ORDER BY created_at de la
// implementación Postgres.
type MembershipStore struct {
	mu       sync.RWMutex
	byUserID map[string][]string
}

// NewMembershipStore crea el store vacío.
func NewMembershipStore() *MembershipStore {
	return &MembershipStore{byUserID: make(map[string][]string)}
}

var _ out.MembershipRepo = (*MembershipStore)(nil)

// Seed da de alta una membresía (helper de tests). Repetir la misma pareja no
// la duplica, igual que la PK compuesta de la tabla real.
func (s *MembershipStore) Seed(userID, tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.byUserID[userID] {
		if existing == tenantID {
			return
		}
	}
	s.byUserID[userID] = append(s.byUserID[userID], tenantID)
}

// TenantsOfUser implementa out.MembershipRepo.
func (s *MembershipStore) TenantsOfUser(_ context.Context, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tenants := s.byUserID[userID]
	if len(tenants) == 0 {
		return nil, nil
	}
	out := make([]string, len(tenants))
	copy(out, tenants)
	return out, nil
}
