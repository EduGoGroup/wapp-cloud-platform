package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/google/uuid"
)

// RefreshStore es el doble en memoria de iam_refresh_tokens.
type RefreshStore struct {
	mu     sync.Mutex
	tokens map[string]domain.RefreshToken // tokenHash → token
}

// NewRefreshStore crea un RefreshStore vacío.
func NewRefreshStore() *RefreshStore {
	return &RefreshStore{tokens: make(map[string]domain.RefreshToken)}
}

var _ out.RefreshRepo = (*RefreshStore)(nil)

// Save implementa out.RefreshRepo.
func (s *RefreshStore) Save(_ context.Context, rt domain.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt.ID == "" {
		rt.ID = uuid.NewString()
	}
	if rt.CreatedAt.IsZero() {
		rt.CreatedAt = time.Now()
	}
	s.tokens[rt.TokenHash] = rt
	return nil
}

// GetByHash implementa out.RefreshRepo.
func (s *RefreshStore) GetByHash(_ context.Context, tokenHash string) (domain.RefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.tokens[tokenHash]
	if !ok {
		return domain.RefreshToken{}, domain.ErrNotFound
	}
	return rt, nil
}

// Revoke implementa out.RefreshRepo (idempotente: no-op si no existe).
func (s *RefreshStore) Revoke(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.tokens[tokenHash]
	if !ok {
		return nil
	}
	now := time.Now()
	rt.RevokedAt = &now
	s.tokens[tokenHash] = rt
	return nil
}

// Count devuelve cuántos refresh tokens hay persistidos (helper de tests: sirve
// para afirmar que un flujo NO emitió ninguno).
func (s *RefreshStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens)
}

// RevokeAllForUser implementa out.RefreshRepo.
func (s *RefreshStore) RevokeAllForUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for h, rt := range s.tokens {
		if rt.UserID == userID && rt.RevokedAt == nil {
			rt.RevokedAt = &now
			s.tokens[h] = rt
		}
	}
	return nil
}
