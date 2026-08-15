package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/google/uuid"
)

type userRoleAssignment struct {
	roleID   string
	tenantID *string
}

// RoleStore es el doble en memoria de iam_roles/iam_role_grants/iam_user_roles.
type RoleStore struct {
	mu        sync.Mutex
	roles     map[string]domain.Role
	grants    map[string][]domain.Grant       // roleID → grants
	userRoles map[string][]userRoleAssignment // userID → assignments
}

// NewRoleStore crea un RoleStore vacío.
func NewRoleStore() *RoleStore {
	return &RoleStore{
		roles:     make(map[string]domain.Role),
		grants:    make(map[string][]domain.Grant),
		userRoles: make(map[string][]userRoleAssignment),
	}
}

var _ out.RoleRepo = (*RoleStore)(nil)

// Seed inserta un rol con sus grants directamente (para sembrar plantillas
// globales o cadenas con parent en tests). ID vacío → se asigna uno. Devuelve el
// rol insertado.
func (s *RoleStore) Seed(r domain.Role, grants []domain.Grant) domain.Role {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	s.roles[r.ID] = r
	if len(grants) > 0 {
		s.grants[r.ID] = append(s.grants[r.ID], grants...)
	}
	return r
}

// Create implementa out.RoleRepo.
func (s *RoleStore) Create(_ context.Context, r domain.Role) (domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.roles {
		if ex.Name != r.Name {
			continue
		}
		if ex.TenantID == nil && r.TenantID == nil {
			return domain.Role{}, domain.ErrConflict
		}
		if ex.TenantID != nil && r.TenantID != nil && *ex.TenantID == *r.TenantID {
			return domain.Role{}, domain.ErrConflict
		}
	}
	r.ID = uuid.NewString()
	r.CreatedAt = time.Now()
	s.roles[r.ID] = r
	return r, nil
}

// GetByID implementa out.RoleRepo.
func (s *RoleStore) GetByID(_ context.Context, id string) (domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok {
		return domain.Role{}, domain.ErrNotFound
	}
	return r, nil
}

// List implementa out.RoleRepo (roles del tenant + plantillas globales).
func (s *RoleStore) List(_ context.Context, tenantID string) ([]domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.Role
	for _, r := range s.roles {
		if r.TenantID == nil || *r.TenantID == tenantID {
			res = append(res, r)
		}
	}
	return res, nil
}

// ParentOf implementa out.RoleRepo.
func (s *RoleStore) ParentOf(_ context.Context, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok || r.ParentRoleID == nil || *r.ParentRoleID == "" {
		return "", false, nil
	}
	return *r.ParentRoleID, true, nil
}

// GrantsOf implementa out.RoleRepo.
func (s *RoleStore) GrantsOf(_ context.Context, roleID string) ([]domain.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Grant(nil), s.grants[roleID]...), nil
}

// AddGrant implementa out.RoleRepo (idempotente por pattern+effect).
func (s *RoleStore) AddGrant(_ context.Context, roleID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.grants[roleID] {
		if ex == g {
			return nil
		}
	}
	s.grants[roleID] = append(s.grants[roleID], g)
	return nil
}

// RemoveGrant implementa out.RoleRepo.
func (s *RoleStore) RemoveGrant(_ context.Context, roleID string, g domain.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[roleID] = removeGrant(s.grants[roleID], g)
	return nil
}

// RolesOfUser implementa out.RoleRepo.
func (s *RoleStore) RolesOfUser(_ context.Context, userID, tenantID string) ([]domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.Role
	seen := make(map[string]bool)

	// Primero los asignados específicamente al tenant
	if tenantID != "" {
		for _, a := range s.userRoles[userID] {
			if a.tenantID != nil && *a.tenantID == tenantID {
				if r, ok := s.roles[a.roleID]; ok && !seen[r.ID] {
					seen[r.ID] = true
					res = append(res, r)
				}
			}
		}
	}
	// Luego los asignados globalmente (tenantID == nil)
	for _, a := range s.userRoles[userID] {
		if a.tenantID == nil {
			if r, ok := s.roles[a.roleID]; ok && !seen[r.ID] {
				seen[r.ID] = true
				res = append(res, r)
			}
		}
	}
	return res, nil
}

// AssignToUser implementa out.RoleRepo, opcionalmente acotado a un tenant
// (D-056.11): tenantID nil asigna GLOBAL; tenantID no nil acota la
// asignación a esa empresa. Idempotente por (roleID, tenantID), igual que las
// dos UNIQUE de iam_user_roles que emula.
func (s *RoleStore) AssignToUser(_ context.Context, userID, roleID string, tenantID *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.userRoles[userID] {
		if a.roleID == roleID && samePtrValue(a.tenantID, tenantID) {
			return nil
		}
	}
	var t *string
	if tenantID != nil {
		v := *tenantID
		t = &v
	}
	s.userRoles[userID] = append(s.userRoles[userID], userRoleAssignment{roleID: roleID, tenantID: t})
	return nil
}

// samePtrValue compara dos *string por VALOR: nil == nil, y dos no-nil son
// iguales si sus valores lo son (nunca por dirección de memoria).
func samePtrValue(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// UnassignFromUser implementa out.RoleRepo.
func (s *RoleStore) UnassignFromUser(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]userRoleAssignment, 0, len(s.userRoles[userID]))
	for _, a := range s.userRoles[userID] {
		if a.roleID != roleID {
			filtered = append(filtered, a)
		}
	}
	s.userRoles[userID] = filtered
	return nil
}
