// Porta internal/iam/usecase/roles.go @ 9a77307

package usecase

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type RoleService struct{}

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
	panic(pendiente.Implementar("usecase.NewRoleService"))
}

// ListRoles implementa in.RoleAdmin. R-U20: devuelve los roles visibles para la empresa del
// Caller —los suyos más las plantillas globales— y NUNCA los de otra empresa.
func (s *RoleService) ListRoles(ctx context.Context) ([]domain.Role, error) {
	panic(pendiente.Implementar("usecase.RoleService.ListRoles"))
}

// CreateRole implementa in.RoleAdmin. R-U20: el rol nace SIEMPRE acotado a la empresa del
// Caller (TenantID = la del contexto; por aquí no se crean plantillas globales), con el nombre y
// el padre pedidos. Nombre vacío ⇒ domain.ErrInvalidInput (envuelto con «name vacío»). Un padre
// no vacío tiene que ser visible: uno suyo o una plantilla global; uno de otra empresa ⇒
// domain.ErrNotFound y no se crea nada (heredar de él copiaría sus grants por la cadena).
// domain.ErrConflict si el nombre ya existe en esa empresa (lo decide el repositorio).
func (s *RoleService) CreateRole(ctx context.Context, input in.CreateRoleInput) (domain.Role, error) {
	panic(pendiente.Implementar("usecase.RoleService.CreateRole"))
}

// AssignRole implementa in.RoleAdmin. R-U20/R-U21: la persona tiene que ser miembro de la
// empresa del Caller y el rol visible para ella (si no, ErrNotFound y no se escribe nada). La
// asignación se escribe SIEMPRE acotada a la empresa del CONTEXTO, nunca global ni con el tenant
// del rol (INV-04), también cuando el rol es una plantilla global: una asignación global valdría
// en TODAS las empresas.
func (s *RoleService) AssignRole(ctx context.Context, input in.RoleAssignmentInput) error {
	panic(pendiente.Implementar("usecase.RoleService.AssignRole"))
}

// UnassignRole implementa in.RoleAdmin. R-U21: mismas comprobaciones que AssignRole; retira
// solo la asignación acotada a la empresa del Caller. La de esa plantilla en otra empresa (o la
// global) no se toca. Idempotente.
func (s *RoleService) UnassignRole(ctx context.Context, input in.RoleAssignmentInput) error {
	panic(pendiente.Implementar("usecase.RoleService.UnassignRole"))
}

// GrantToRole implementa in.RoleAdmin. R-U22: añade el grant a un rol PROPIO de la empresa del
// Caller. Una plantilla global ⇒ domain.ErrGlobalRoleImmutable; un rol de otra empresa ⇒
// domain.ErrNotFound; grant inválido ⇒ domain.ErrInvalidInput. En los tres casos no se escribe
// nada.
func (s *RoleService) GrantToRole(ctx context.Context, input in.RoleGrantInput) error {
	panic(pendiente.Implementar("usecase.RoleService.GrantToRole"))
}

// RevokeFromRole implementa in.RoleAdmin: quita el grant de un rol propio, con las mismas
// reglas que GrantToRole (R-U22).
func (s *RoleService) RevokeFromRole(ctx context.Context, input in.RoleGrantInput) error {
	panic(pendiente.Implementar("usecase.RoleService.RevokeFromRole"))
}

// GrantToUser implementa in.RoleAdmin. R-U22: iam_user_grants no tiene tenant, así que el
// override se escribe SOLO sobre una persona miembro de la empresa del Caller; a quien no lo es
// ⇒ domain.ErrNotFound y no se escribe nada. Grant inválido ⇒ domain.ErrInvalidInput.
func (s *RoleService) GrantToUser(ctx context.Context, input in.UserGrantInput) error {
	panic(pendiente.Implementar("usecase.RoleService.GrantToUser"))
}

// RevokeFromUser implementa in.RoleAdmin: quita el override, con las mismas reglas que
// GrantToUser (R-U22).
func (s *RoleService) RevokeFromUser(ctx context.Context, input in.UserGrantInput) error {
	panic(pendiente.Implementar("usecase.RoleService.RevokeFromUser"))
}
