// Porta internal/iam/usecase/roles.go @ 9a77307

package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// RoleService implementa in.RoleAdmin: la administración de RBAC de una empresa. Añade las tres
// reglas que el repositorio NO puede imponer porque no sabe quién llama:
//
//  1. INV-04 — el tenant sale del CONTEXTO de identidad (el CallerResolver), nunca de un
//     parámetro: ningún Input de in.* tiene campo TenantID.
//  2. Un id que manda el llamante (rol, usuario) se ACOTA antes de tocarlo: si no es visible
//     para su empresa, domain.ErrNotFound. Opaco a propósito —un "prohibido" confirmaría que
//     ese rol o esa persona existen en otra empresa—.
//  3. Las plantillas globales (tenant_id NULL) se leen y se asignan, pero no se modifican: sus
//     grants valen para TODOS los tenants a la vez.
//
// Reglas comunes a TODOS los métodos:
//   - R-U23: sin identidad en el contexto, o con identidad sin empresa (TenantID vacío) ⇒
//     domain.ErrNoTenant y NO se ejecuta nada (ni lectura ni escritura).
//   - Un rol es VISIBLE si es de la empresa del Caller o es una plantilla global; cualquier
//     otro —de otra empresa o inexistente— es domain.ErrNotFound. Un role_id vacío es
//     domain.ErrInvalidInput (envuelto con «role_id vacío»).
//   - Una persona es del tenant si out.MembershipRepo.TenantsOfUser la incluye en la empresa
//     del Caller; si no, domain.ErrNotFound. Un user_id vacío es domain.ErrInvalidInput
//     (envuelto con «user_id vacío»).
//   - Un grant exige patrón no vacío y efecto EXACTAMENTE domain.EffectAllow o EffectDeny; si
//     no, domain.ErrInvalidInput y no se escribe nada (R-U22: un efecto vacío NO vale como
//     allow). La validación del grant va antes que la del rol o la persona.
//   - Los errores de los repositorios se propagan.
//
// Hasta el Plan 047 esto solo existía como repositorio (out.RoleRepo) y como SQL: había con qué
// crear un rol y no había por dónde crearlo. Lo que añade esta capa no es fontanería, son las
// tres reglas de arriba.
type RoleService struct {
	caller  in.CallerResolver
	roles   out.RoleRepo
	grants  out.GrantRepo
	members out.MembershipRepo
}

// compile-time: RoleService satisface el puerto de entrada.
var _ in.RoleAdmin = (*RoleService)(nil)

// NewRoleService construye el servicio (fail-fast, servicio nil y textos literales):
//
//   - caller nil ⇒ "iam: RoleService requiere un CallerResolver (INV-04: el tenant sale del
//     contexto)": sin él la única alternativa sería aceptar el tenant del llamante;
//   - roles, grants o members nil ⇒ "iam: RoleService requiere RoleRepo, GrantRepo y
//     MembershipRepo".
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

// tenantOf resuelve la empresa del llamante. Es el ÚNICO sitio de este fichero donde nace un
// tenant_id: cualquier otro origen sería un parámetro del llamante (INV-04). Sin identidad, o
// con una identidad sin empresa, es domain.ErrNoTenant (R-U23), antes de tocar nada.
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

// visibleRole devuelve el rol si es VISIBLE para el tenant: suyo o plantilla global. Cualquier
// otro caso —incluido el rol de otra empresa— es domain.ErrNotFound, opaco a propósito: un
// «prohibido» confirmaría que ese rol existe en otra empresa.
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

// ownRole devuelve el rol solo si es PROPIO del tenant. Una plantilla global es visible (la
// devuelve visibleRole) pero no editable: sus grants valen para TODOS los tenants a la vez.
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
// Es lo que acota las operaciones sobre PERSONAS, que es donde el aislamiento se escapa con más
// facilidad: iam_user_grants ni siquiera tiene columna de tenant, así que sin esta comprobación
// un administrador podría escribirle overrides a cualquier UUID del grupo con solo teclearlo.
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

// validGrant exige patrón y efecto EXPLÍCITOS. El efecto no toma "allow" por defecto a
// propósito: un efecto vacío que se interpreta como permitir convierte un campo olvidado en un
// permiso concedido.
func validGrant(g domain.Grant) error {
	if g.Pattern == "" {
		return fmt.Errorf("%w: pattern vacío", domain.ErrInvalidInput)
	}
	if g.Effect != domain.EffectAllow && g.Effect != domain.EffectDeny {
		return fmt.Errorf("%w: effect debe ser %q o %q", domain.ErrInvalidInput, domain.EffectAllow, domain.EffectDeny)
	}
	return nil
}

// ListRoles implementa in.RoleAdmin. R-U20: devuelve los roles visibles para la empresa del
// Caller —los suyos más las plantillas globales— y NUNCA los de otra empresa.
func (s *RoleService) ListRoles(ctx context.Context) ([]domain.Role, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return s.roles.List(ctx, c.TenantID)
}

// CreateRole implementa in.RoleAdmin. R-U20: el rol nace SIEMPRE acotado a la empresa del
// Caller (TenantID = la del contexto; por aquí no se crean plantillas globales), con el nombre y
// el padre pedidos. Nombre vacío ⇒ domain.ErrInvalidInput (envuelto con «name vacío»). Un padre
// no vacío tiene que ser visible: uno suyo o una plantilla global; uno de otra empresa ⇒
// domain.ErrNotFound y no se crea nada (heredar de él copiaría sus grants por la cadena).
// domain.ErrConflict si el nombre ya existe en esa empresa (lo decide el repositorio).
func (s *RoleService) CreateRole(ctx context.Context, input in.CreateRoleInput) (domain.Role, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return domain.Role{}, err
	}
	if input.Name == "" {
		return domain.Role{}, fmt.Errorf("%w: name vacío", domain.ErrInvalidInput)
	}
	if input.ParentRoleID != nil && *input.ParentRoleID != "" {
		// El padre se acota igual que todo lo demás: heredar de un rol de otra empresa
		// copiaría sus grants a esta por la cadena de herencia.
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

// AssignRole implementa in.RoleAdmin. R-U20/R-U21: la persona tiene que ser miembro de la
// empresa del Caller y el rol visible para ella (si no, ErrNotFound y no se escribe nada). La
// asignación se escribe SIEMPRE acotada a la empresa del CONTEXTO, nunca global ni con el tenant
// del rol (INV-04), también cuando el rol es una plantilla global: una asignación global valdría
// en TODAS las empresas.
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
	// La diferencia no es cosmética: una asignación global (tenant_id NULL) vale en TODAS las
	// empresas —así la resuelve RoleRepo.RolesOfUser—, así que asignar con el tenant del ROL en
	// vez del tenant del CONTEXTO convertiría cualquier plantilla global en un permiso
	// universal.
	return s.roles.AssignToUser(ctx, input.UserID, input.RoleID, &c.TenantID)
}

// UnassignRole implementa in.RoleAdmin. R-U21: mismas comprobaciones que AssignRole; retira
// solo la asignación acotada a la empresa del Caller. La de esa plantilla en otra empresa (o la
// global) no se toca. Idempotente.
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

// GrantToRole implementa in.RoleAdmin. R-U22: añade el grant a un rol PROPIO de la empresa del
// Caller. Una plantilla global ⇒ domain.ErrGlobalRoleImmutable; un rol de otra empresa ⇒
// domain.ErrNotFound; grant inválido ⇒ domain.ErrInvalidInput. En los tres casos no se escribe
// nada.
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

// RevokeFromRole implementa in.RoleAdmin: quita el grant de un rol propio, con las mismas
// reglas que GrantToRole (R-U22).
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

// GrantToUser implementa in.RoleAdmin. R-U22: iam_user_grants no tiene tenant, así que el
// override se escribe SOLO sobre una persona miembro de la empresa del Caller; a quien no lo es
// ⇒ domain.ErrNotFound y no se escribe nada. Grant inválido ⇒ domain.ErrInvalidInput.
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

// RevokeFromUser implementa in.RoleAdmin: quita el override, con las mismas reglas que
// GrantToUser (R-U22).
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
