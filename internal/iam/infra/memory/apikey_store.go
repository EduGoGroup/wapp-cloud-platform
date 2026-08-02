package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/google/uuid"
)

// APIKeyStore es el doble en memoria de iam_api_keys.
type APIKeyStore struct {
	mu   sync.Mutex
	keys map[string]domain.APIKey // id → api-key
}

// NewAPIKeyStore crea un APIKeyStore vacío.
func NewAPIKeyStore() *APIKeyStore { return &APIKeyStore{keys: make(map[string]domain.APIKey)} }

var _ out.APIKeyRepo = (*APIKeyStore)(nil)

// Create implementa out.APIKeyRepo.
func (s *APIKeyStore) Create(_ context.Context, k domain.APIKey) (domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.keys {
		if ex.ClientID == k.ClientID || ex.KeyHash == k.KeyHash {
			return domain.APIKey{}, domain.ErrConflict
		}
	}
	k.ID = uuid.NewString()
	k.CreatedAt = time.Now()
	s.keys[k.ID] = k
	return k, nil
}

// GetByHash implementa out.APIKeyRepo.
func (s *APIKeyStore) GetByHash(_ context.Context, keyHash string) (domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.KeyHash == keyHash {
			return k, nil
		}
	}
	return domain.APIKey{}, domain.ErrNotFound
}

// List implementa out.APIKeyRepo.
func (s *APIKeyStore) List(_ context.Context, tenantID string) ([]domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.APIKey
	for _, k := range s.keys {
		if k.TenantID == tenantID {
			res = append(res, k)
		}
	}
	return res, nil
}

// Revoke implementa out.APIKeyRepo.
func (s *APIKeyStore) Revoke(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok || k.TenantID != tenantID {
		return domain.ErrNotFound
	}
	now := time.Now()
	k.RevokedAt = &now
	k.IsActive = false
	s.keys[id] = k
	return nil
}

// TouchLastUsed implementa out.APIKeyRepo.
func (s *APIKeyStore) TouchLastUsed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok {
		return nil
	}
	now := time.Now()
	k.LastUsedAt = &now
	s.keys[id] = k
	return nil
}
