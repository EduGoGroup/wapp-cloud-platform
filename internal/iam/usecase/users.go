package usecase

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-shared/auth"
)

// UserService implementa in.UserManager: alta/consulta/baja de usuarios y su
// relación con roles y overrides de grants. TODO recibe el tenant_id del
// contexto de identidad y verifica el aislamiento (un usecase de tenant A no
// puede tocar recursos de tenant B: se traduce a domain.ErrNotFound).
type UserService struct {
	users  out.UserRepo
	roles  out.RoleRepo
	grants out.GrantRepo
}

// compile-time: UserService satisface el puerto de entrada.
var _ in.UserManager = (*UserService)(nil)

// NewUserService construye el servicio de usuarios. Valida deps nil.
func NewUserService(users out.UserRepo, roles out.RoleRepo, grants out.GrantRepo) (*UserService, error) {
	if users == nil || roles == nil || grants == nil {
		return nil, errors.New("iam: UserService requiere users/roles/grants")
	}
	return &UserService{users: users, roles: roles, grants: grants}, nil
}

// CreateUser da de alta un operador del tenant: hashea la contraseña con bcrypt
// (nunca la persiste en claro) y asigna los roles de arranque. Devuelve
// domain.ErrConflict si (tenant_id, email) ya existe.
func (s *UserService) CreateUser(ctx context.Context, req in.CreateUserInput) (domain.User, error) {
	if req.TenantID == "" || req.Email == "" || req.Password == "" {
		return domain.User{}, domain.ErrInvalidInput
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return domain.User{}, err
	}
	created, err := s.users.Create(ctx, domain.User{
		TenantID:     req.TenantID,
		Email:        req.Email,
		PasswordHash: hash,
		IsActive:     true,
	})
	if err != nil {
		return domain.User{}, err
	}
	for _, roleID := range req.RoleIDs {
		if aerr := s.roles.AssignToUser(ctx, created.ID, roleID); aerr != nil {
			return domain.User{}, aerr
		}
	}
	return created, nil
}

// GetUser devuelve un usuario del tenant. Aislamiento: si el usuario existe pero
// pertenece a otro tenant, se comporta como no encontrado.
func (s *UserService) GetUser(ctx context.Context, tenantID, id string) (domain.User, error) {
	u, err := s.users.GetByID(ctx, id)
	if err != nil {
		return domain.User{}, err
	}
	if u.TenantID != tenantID {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

// ListUsers devuelve los usuarios del tenant.
func (s *UserService) ListUsers(ctx context.Context, tenantID string) ([]domain.User, error) {
	return s.users.List(ctx, tenantID)
}

// DeleteUser da de baja (soft-delete) un usuario del tenant.
func (s *UserService) DeleteUser(ctx context.Context, tenantID, id string) error {
	return s.users.SoftDelete(ctx, tenantID, id)
}

// AssignRole asigna un rol a un usuario del tenant (verifica pertenencia).
func (s *UserService) AssignRole(ctx context.Context, tenantID, userID, roleID string) error {
	if _, err := s.GetUser(ctx, tenantID, userID); err != nil {
		return err
	}
	return s.roles.AssignToUser(ctx, userID, roleID)
}

// UnassignRole retira un rol de un usuario del tenant (verifica pertenencia).
func (s *UserService) UnassignRole(ctx context.Context, tenantID, userID, roleID string) error {
	if _, err := s.GetUser(ctx, tenantID, userID); err != nil {
		return err
	}
	return s.roles.UnassignFromUser(ctx, userID, roleID)
}

// AddUserGrant añade un override de grant a un usuario del tenant.
func (s *UserService) AddUserGrant(ctx context.Context, tenantID, userID string, g domain.Grant) error {
	if _, err := s.GetUser(ctx, tenantID, userID); err != nil {
		return err
	}
	return s.grants.AddUserGrant(ctx, userID, g)
}

// RemoveUserGrant elimina un override de grant de un usuario del tenant.
func (s *UserService) RemoveUserGrant(ctx context.Context, tenantID, userID string, g domain.Grant) error {
	if _, err := s.GetUser(ctx, tenantID, userID); err != nil {
		return err
	}
	return s.grants.RemoveUserGrant(ctx, userID, g)
}
