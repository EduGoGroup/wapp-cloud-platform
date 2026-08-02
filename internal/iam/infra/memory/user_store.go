package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/google/uuid"
)

// UserStore es el doble en memoria de iam_users.
type UserStore struct {
	mu    sync.Mutex
	users map[string]domain.User // id → user
}

// NewUserStore crea un UserStore vacío.
func NewUserStore() *UserStore { return &UserStore{users: make(map[string]domain.User)} }

var _ out.UserRepo = (*UserStore)(nil)

// Create implementa out.UserRepo.
func (s *UserStore) Create(_ context.Context, u domain.User) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.users {
		if ex.TenantID == u.TenantID && ex.Email == u.Email && ex.DeletedAt == nil {
			return domain.User{}, domain.ErrConflict
		}
	}
	u.ID = uuid.NewString()
	now := time.Now()
	u.CreatedAt, u.UpdatedAt = now, now
	s.users[u.ID] = u
	return u, nil
}

// GetByID implementa out.UserRepo.
func (s *UserStore) GetByID(_ context.Context, id string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok || u.DeletedAt != nil {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

// FindByEmail implementa out.UserRepo.
func (s *UserStore) FindByEmail(_ context.Context, email string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Email == email && u.DeletedAt == nil {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

// GetByEmail implementa out.UserRepo.
func (s *UserStore) GetByEmail(_ context.Context, tenantID, email string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.TenantID == tenantID && u.Email == email && u.DeletedAt == nil {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

// List implementa out.UserRepo.
func (s *UserStore) List(_ context.Context, tenantID string) ([]domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.User
	for _, u := range s.users {
		if u.TenantID == tenantID && u.DeletedAt == nil {
			res = append(res, u)
		}
	}
	return res, nil
}

// SoftDelete implementa out.UserRepo.
func (s *UserStore) SoftDelete(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok || u.TenantID != tenantID || u.DeletedAt != nil {
		return domain.ErrNotFound
	}
	now := time.Now()
	u.DeletedAt = &now
	u.IsActive = false
	s.users[id] = u
	return nil
}
