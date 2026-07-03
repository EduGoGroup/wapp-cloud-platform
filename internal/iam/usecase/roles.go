package usecase

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// RoleService implementa in.RoleManager: alta de roles CUSTOM del tenant y
// gestión de sus grants. Los roles canónicos (tenant_admin/operator/viewer) son
// plantillas globales sembradas en T1 y NO se mutan por aquí (AddGrant/
// RemoveGrant sobre un rol de otro tenant o global se rechaza con ErrNotFound).
type RoleService struct {
	roles out.RoleRepo
}

// compile-time: RoleService satisface el puerto de entrada.
var _ in.RoleManager = (*RoleService)(nil)

// NewRoleService construye el servicio de roles. Valida deps nil.
func NewRoleService(roles out.RoleRepo) (*RoleService, error) {
	if roles == nil {
		return nil, errors.New("iam: RoleService requiere el repositorio de roles")
	}
	return &RoleService{roles: roles}, nil
}

// CreateRole crea un rol custom del tenant y le añade los grants iniciales.
func (s *RoleService) CreateRole(ctx context.Context, req in.CreateRoleInput) (domain.Role, error) {
	if req.TenantID == "" || req.Name == "" {
		return domain.Role{}, domain.ErrInvalidInput
	}
	tenantID := req.TenantID
	created, err := s.roles.Create(ctx, domain.Role{
		TenantID:     &tenantID,
		Name:         req.Name,
		ParentRoleID: req.ParentRoleID,
	})
	if err != nil {
		return domain.Role{}, err
	}
	for _, g := range req.Grants {
		if aerr := s.roles.AddGrant(ctx, created.ID, g); aerr != nil {
			return domain.Role{}, aerr
		}
	}
	return created, nil
}

// ListRoles devuelve los roles visibles para el tenant (custom + plantillas
// globales).
func (s *RoleService) ListRoles(ctx context.Context, tenantID string) ([]domain.Role, error) {
	return s.roles.List(ctx, tenantID)
}

// AddGrant añade un grant a un rol CUSTOM del tenant (verifica pertenencia).
func (s *RoleService) AddGrant(ctx context.Context, tenantID, roleID string, g domain.Grant) error {
	if err := s.ownRole(ctx, tenantID, roleID); err != nil {
		return err
	}
	return s.roles.AddGrant(ctx, roleID, g)
}

// RemoveGrant elimina un grant de un rol CUSTOM del tenant (verifica pertenencia).
func (s *RoleService) RemoveGrant(ctx context.Context, tenantID, roleID string, g domain.Grant) error {
	if err := s.ownRole(ctx, tenantID, roleID); err != nil {
		return err
	}
	return s.roles.RemoveGrant(ctx, roleID, g)
}

// ownRole verifica que el rol exista y sea PROPIEDAD del tenant (no una
// plantilla global ni de otro tenant). Un rol ajeno se traduce a ErrNotFound.
func (s *RoleService) ownRole(ctx context.Context, tenantID, roleID string) error {
	r, err := s.roles.GetByID(ctx, roleID)
	if err != nil {
		return err
	}
	if r.TenantID == nil || *r.TenantID != tenantID {
		return domain.ErrNotFound
	}
	return nil
}
