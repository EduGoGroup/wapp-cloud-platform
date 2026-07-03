// Package memory provee implementaciones EN MEMORIA de los puertos out del IAM
// (UserRepo, RoleRepo, GrantRepo, RefreshRepo, APIKeyRepo, AuditRepo), seguras
// para concurrencia. Pensadas para tests unitarios CI-safe de los usecases (sin
// BD), imitando la semántica de la implementación Postgres: unicidad →
// domain.ErrConflict, ausencia → domain.ErrNotFound, filtrado por tenant.
//
// Cada tabla tiene su propio store (no un único tipo): los seis puertos declaran
// métodos homónimos con firmas distintas (Create/GetByID/List/Revoke/GetByHash),
// que Go no permite convivir en un mismo tipo. Store agrega los seis para un
// wiring cómodo en los tests.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/google/uuid"
)

// Store agrega los seis repositorios en memoria para el wiring de tests.
type Store struct {
	Users   *UserStore
	Roles   *RoleStore
	Grants  *GrantStore
	Refresh *RefreshStore
	APIKeys *APIKeyStore
	Audit   *AuditStore
}

// NewStore crea el agregado con los seis repositorios vacíos.
func NewStore() *Store {
	return &Store{
		Users:   NewUserStore(),
		Roles:   NewRoleStore(),
		Grants:  NewGrantStore(),
		Refresh: NewRefreshStore(),
		APIKeys: NewAPIKeyStore(),
		Audit:   NewAuditStore(),
	}
}

// removeGrant devuelve la lista sin el grant dado (comparación por valor).
func removeGrant(list []domain.Grant, g domain.Grant) []domain.Grant {
	out := make([]domain.Grant, 0, len(list))
	for _, ex := range list {
		if ex != g {
			out = append(out, ex)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// UserStore (out.UserRepo)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// RoleStore (out.RoleRepo) — roles + role_grants + user_roles
// ---------------------------------------------------------------------------

// RoleStore es el doble en memoria de iam_roles/iam_role_grants/iam_user_roles.
type RoleStore struct {
	mu        sync.Mutex
	roles     map[string]domain.Role
	grants    map[string][]domain.Grant  // roleID → grants
	userRoles map[string]map[string]bool // userID → set(roleID)
}

// NewRoleStore crea un RoleStore vacío.
func NewRoleStore() *RoleStore {
	return &RoleStore{
		roles:     make(map[string]domain.Role),
		grants:    make(map[string][]domain.Grant),
		userRoles: make(map[string]map[string]bool),
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
func (s *RoleStore) RolesOfUser(_ context.Context, userID string) ([]domain.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []domain.Role
	for roleID := range s.userRoles[userID] {
		if r, ok := s.roles[roleID]; ok {
			res = append(res, r)
		}
	}
	return res, nil
}

// AssignToUser implementa out.RoleRepo (idempotente).
func (s *RoleStore) AssignToUser(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userRoles[userID] == nil {
		s.userRoles[userID] = make(map[string]bool)
	}
	s.userRoles[userID][roleID] = true
	return nil
}

// UnassignFromUser implementa out.RoleRepo.
func (s *RoleStore) UnassignFromUser(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.userRoles[userID], roleID)
	return nil
}

// ---------------------------------------------------------------------------
// GrantStore (out.GrantRepo) — user_grants (overrides)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// RefreshStore (out.RefreshRepo)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// APIKeyStore (out.APIKeyRepo)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// AuditStore (out.AuditRepo)
// ---------------------------------------------------------------------------

// AuditStore es el doble en memoria de audit_events.
type AuditStore struct {
	mu     sync.Mutex
	events []domain.AuditEvent
	seq    int64
}

// NewAuditStore crea un AuditStore vacío.
func NewAuditStore() *AuditStore { return &AuditStore{} }

var _ out.AuditRepo = (*AuditStore)(nil)

// Record implementa out.AuditRepo.
func (s *AuditStore) Record(_ context.Context, e domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.ID = s.seq
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.events = append(s.events, e)
	return nil
}

// List implementa out.AuditRepo (más recientes primero, paginado).
func (s *AuditStore) List(_ context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var filtered []domain.AuditEvent
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.TenantID != nil && *e.TenantID == tenantID {
			filtered = append(filtered, e)
		}
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], nil
}

// Events devuelve todos los eventos registrados (inspección en tests).
func (s *AuditStore) Events() []domain.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AuditEvent(nil), s.events...)
}
