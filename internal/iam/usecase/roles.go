package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// RoleService implementa in.RoleAdmin: la administración de RBAC de una empresa.
//
// Hasta el Plan 047 esto solo existía como repositorio (out.RoleRepo) y como
// SQL: había con qué crear un rol y no había por dónde crearlo. Lo que añade
// esta capa no es fontanería, son las tres reglas que el repositorio NO puede
// imponer porque no sabe quién llama:
//
//  1. INV-04 — el tenant sale del CONTEXTO de identidad, nunca de un parámetro.
//     Ningún Input de in.* tiene campo TenantID; el único que hay lo pone el
//     CallerResolver.
//  2. Un id que manda el llamante (rol, usuario) se ACOTA antes de tocarlo: si
//     no es visible para su empresa, domain.ErrNotFound. Opaco a propósito —un
//     "prohibido" confirmaría que ese rol existe en otra empresa.
//  3. Las plantillas globales (tenant_id NULL) se leen y se asignan, pero no se
//     modifican: sus grants valen para TODOS los tenants a la vez.
type RoleService struct {
	caller  in.CallerResolver
	roles   out.RoleRepo
	grants  out.GrantRepo
	members out.MembershipRepo
}

// compile-time: RoleService satisface el puerto de entrada.
var _ in.RoleAdmin = (*RoleService)(nil)

// NewRoleService construye el servicio. Valida deps nil (fail-fast en el
// arranque): sin CallerResolver no habría de dónde sacar el tenant y la única
// alternativa sería aceptarlo del llamante, que es justo lo que INV-04 prohíbe.
func NewRoleService(
	caller in.CallerResolver,
	roles out.RoleRepo,
	grants out.GrantRepo,
	members out.MembershipRepo,
) (*RoleService, error) {
	if caller == nil {
		return nil, errors.New("iam: RoleService requiere un CallerResolver (INV-04: el tenant sale del contexto)")
	}
	if roles == nil || grants == nil || members == nil {
		return nil, errors.New("iam: RoleService requiere RoleRepo, GrantRepo y MembershipRepo")
	}
	return &RoleService{caller: caller, roles: roles, grants: grants, members: members}, nil
}

// tenantOf resuelve la empresa del llamante. Es el ÚNICO sitio de este fichero
// donde nace un tenant_id: cualquier otro origen sería un parámetro del
// llamante.
func (s *RoleService) tenantOf(ctx context.Context) (in.Caller, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok {
		return in.Caller{}, domain.ErrNoTenant
	}
	if c.TenantID == "" {
		return in.Caller{}, domain.ErrNoTenant
	}
	return c, nil
}

// visibleRole devuelve el rol si es VISIBLE para el tenant: suyo o plantilla
// global. Cualquier otro caso —incluido el rol de otra empresa— es
// domain.ErrNotFound.
func (s *RoleService) visibleRole(ctx context.Context, tenantID, roleID string) (domain.Role, error) {
	if roleID == "" {
		return domain.Role{}, fmt.Errorf("%w: role_id vacío", domain.ErrInvalidInput)
	}
	role, err := s.roles.GetByID(ctx, roleID)
	if err != nil {
		return domain.Role{}, err
	}
	if role.TenantID != nil && *role.TenantID != tenantID {
		return domain.Role{}, domain.ErrNotFound
	}
	return role, nil
}

// ownRole devuelve el rol solo si es PROPIO del tenant. Una plantilla global es
// visible (la devuelve visibleRole) pero no editable.
func (s *RoleService) ownRole(ctx context.Context, tenantID, roleID string) (domain.Role, error) {
	role, err := s.visibleRole(ctx, tenantID, roleID)
	if err != nil {
		return domain.Role{}, err
	}
	if role.TenantID == nil {
		return domain.Role{}, domain.ErrGlobalRoleImmutable
	}
	return role, nil
}

// requireMember exige que la persona pertenezca a la empresa del llamante.
//
// Es lo que acota las operaciones sobre PERSONAS, que es donde el aislamiento se
// escapa con más facilidad: iam_user_grants ni siquiera tiene columna de tenant,
// así que sin esta comprobación un administrador podría escribirle overrides a
// cualquier UUID del grupo con solo teclearlo.
func (s *RoleService) requireMember(ctx context.Context, tenantID, userID string) error {
	if userID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	tenants, err := s.members.TenantsOfUser(ctx, userID)
	if err != nil {
		return err
	}
	if !slices.Contains(tenants, tenantID) {
		return domain.ErrNotFound
	}
	return nil
}

// validGrant exige patrón y efecto EXPLÍCITOS. El efecto no toma "allow" por
// defecto a propósito: un efecto vacío que se interpreta como permitir convierte
// un campo olvidado en un permiso concedido.
func validGrant(g domain.Grant) error {
	if g.Pattern == "" {
		return fmt.Errorf("%w: pattern vacío", domain.ErrInvalidInput)
	}
	if g.Effect != domain.EffectAllow && g.Effect != domain.EffectDeny {
		return fmt.Errorf("%w: effect debe ser %q o %q", domain.ErrInvalidInput, domain.EffectAllow, domain.EffectDeny)
	}
	return nil
}

// ListRoles implementa in.RoleAdmin.
func (s *RoleService) ListRoles(ctx context.Context) ([]domain.Role, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return s.roles.List(ctx, c.TenantID)
}

// CreateRole implementa in.RoleAdmin. El rol nace SIEMPRE acotado a la empresa
// del llamante: por aquí no se crean plantillas globales.
func (s *RoleService) CreateRole(ctx context.Context, input in.CreateRoleInput) (domain.Role, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return domain.Role{}, err
	}
	if input.Name == "" {
		return domain.Role{}, fmt.Errorf("%w: name vacío", domain.ErrInvalidInput)
	}
	if input.ParentRoleID != nil && *input.ParentRoleID != "" {
		// El padre se acota igual que todo lo demás: heredar de un rol de otra
		// empresa copiaría sus grants a esta por la cadena de herencia.
		if _, perr := s.visibleRole(ctx, c.TenantID, *input.ParentRoleID); perr != nil {
			return domain.Role{}, perr
		}
	}
	return s.roles.Create(ctx, domain.Role{
		TenantID:     &c.TenantID,
		Name:         input.Name,
		ParentRoleID: input.ParentRoleID,
	})
}

// AssignRole implementa in.RoleAdmin.
//
// La asignación se acota SIEMPRE a la empresa del llamante y nunca es global.
// La diferencia no es cosmética: una asignación global (tenant_id NULL) vale en
// TODAS las empresas —así la resuelve RoleRepo.RolesOfUser—, así que asignar con
// el tenant del ROL en vez del tenant del CONTEXTO convertiría cualquier
// plantilla global en un permiso universal.
func (s *RoleService) AssignRole(ctx context.Context, input in.RoleAssignmentInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := s.requireMember(ctx, c.TenantID, input.UserID); err != nil {
		return err
	}
	if _, err := s.visibleRole(ctx, c.TenantID, input.RoleID); err != nil {
		return err
	}
	return s.roles.AssignToUser(ctx, input.UserID, input.RoleID, &c.TenantID)
}

// UnassignRole implementa in.RoleAdmin. Retira solo la asignación acotada a la
// empresa del llamante; la global, si la hubiera, no se toca desde aquí.
func (s *RoleService) UnassignRole(ctx context.Context, input in.RoleAssignmentInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := s.requireMember(ctx, c.TenantID, input.UserID); err != nil {
		return err
	}
	if _, err := s.visibleRole(ctx, c.TenantID, input.RoleID); err != nil {
		return err
	}
	return s.roles.UnassignFromUser(ctx, input.UserID, input.RoleID, &c.TenantID)
}

// GrantToRole implementa in.RoleAdmin.
func (s *RoleService) GrantToRole(ctx context.Context, input in.RoleGrantInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := validGrant(input.Grant); err != nil {
		return err
	}
	role, err := s.ownRole(ctx, c.TenantID, input.RoleID)
	if err != nil {
		return err
	}
	return s.roles.AddGrant(ctx, role.ID, input.Grant)
}

// RevokeFromRole implementa in.RoleAdmin.
func (s *RoleService) RevokeFromRole(ctx context.Context, input in.RoleGrantInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := validGrant(input.Grant); err != nil {
		return err
	}
	role, err := s.ownRole(ctx, c.TenantID, input.RoleID)
	if err != nil {
		return err
	}
	return s.roles.RemoveGrant(ctx, role.ID, input.Grant)
}

// GrantToUser implementa in.RoleAdmin.
func (s *RoleService) GrantToUser(ctx context.Context, input in.UserGrantInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := validGrant(input.Grant); err != nil {
		return err
	}
	if err := s.requireMember(ctx, c.TenantID, input.UserID); err != nil {
		return err
	}
	return s.grants.AddUserGrant(ctx, input.UserID, input.Grant)
}

// RevokeFromUser implementa in.RoleAdmin.
func (s *RoleService) RevokeFromUser(ctx context.Context, input in.UserGrantInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := validGrant(input.Grant); err != nil {
		return err
	}
	if err := s.requireMember(ctx, c.TenantID, input.UserID); err != nil {
		return err
	}
	return s.grants.RemoveUserGrant(ctx, input.UserID, input.Grant)
}
