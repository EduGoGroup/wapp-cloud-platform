// Package out declara los puertos DE SALIDA del módulo IAM: las interfaces de
// repositorio que los usecases necesitan para persistir/leer estado. Son
// contratos PUROS (context + tipos de dominio); las implementaciones viven en
// infra/postgres (SQL raw) e infra/memory (tests). Todas las operaciones
// acotadas a un tenant reciben el tenant_id del CONTEXTO de identidad (nunca lo
// inventan): la regla de aislamiento multi-tenant (INV-8) se cumple en el
// usecase pasando el tenant del token y en el repo con `WHERE tenant_id = $1`.
//
// Convención de errores (verdad de campo del repo): "no encontrado" que es un
// resultado normal se expresa con `found bool`; "no encontrado" que es fallo de
// negocio se expresa devolviendo domain.ErrNotFound (errors.Is). La violación de
// unicidad se mapea a domain.ErrConflict.
package out

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

// IdentityClient habla con identity-api, el SSO del grupo (identity Plan 003 ·
// Ola 3). Es el ÚNICO puerto de este módulo que sale del proceso hacia otro
// servicio: los demás son almacenamiento.
//
// A partir de la delegación, las credenciales de una persona se validan AHÍ y no
// aquí. Lo que wApp sigue decidiendo —el tenant y los grants— viaja en el
// Context Token que se emite después, con el canje.
type IdentityClient interface {
	// Login autentica a la persona contra identity para una aplicación concreta
	// (`system`: wapp.bff o wapp.edge). El System Gate de identity puede negar el
	// acceso a esa aplicación aunque las credenciales sean correctas.
	Login(ctx context.Context, email, password, system string) (domain.IdentitySession, error)
	// Refresh rota la sesión en identity a partir del refresh presentado. La
	// aplicación NO se manda: sale de la fila de la sesión, y mandarla sortearía
	// el System Gate.
	Refresh(ctx context.Context, refreshToken string) (domain.IdentitySession, error)
	// Logout revoca en identity la sesión de ESE refresh. Idempotente: revocar
	// una sesión que ya no existe no es error.
	Logout(ctx context.Context, refreshToken string) error
	// LogoutAll revoca TODAS las sesiones de la persona que acredita el Identity
	// Token presentado, en todas sus aplicaciones.
	LogoutAll(ctx context.Context, identityToken string) error
}

// MembershipRepo lee la membresía usuario↔tenant (tabla public.tenant_members,
// migración 0037). Es el vínculo de NEGOCIO que se queda en wApp cuando los
// usuarios pasan a identity (identity ADR-0001, INV-1): identity dice QUIÉN es
// la persona y esta tabla a QUÉ tenant pertenece. Solo lectura: la escritura la
// hacen la migración y la administración de tenants, no el plano de auth.
type MembershipRepo interface {
	// TenantsOfUser devuelve los tenants de los que el usuario es miembro, en
	// orden estable. Una lista VACÍA no es error: significa que ese usuario no
	// tiene membresía en wApp (quien lo llame decide qué hacer con eso).
	TenantsOfUser(ctx context.Context, userID string) ([]string, error)
}

// RoleRepo persiste roles, sus grants y la asignación usuario↔rol (tablas
// iam_roles, iam_role_grants, iam_user_roles).
type RoleRepo interface {
	// Create inserta un rol custom del tenant y devuelve la fila con ID asignado.
	// Devuelve domain.ErrConflict si el nombre ya existe para el tenant.
	Create(ctx context.Context, r domain.Role) (domain.Role, error)
	// GetByID busca un rol por PK (global o de tenant). domain.ErrNotFound si no.
	GetByID(ctx context.Context, id string) (domain.Role, error)
	// List devuelve los roles VISIBLES para el tenant: sus roles custom más las
	// plantillas globales (tenant_id NULL).
	List(ctx context.Context, tenantID string) ([]domain.Role, error)
	// ParentOf resuelve el parent_role_id de un rol para la cadena de herencia
	// (auth.ResolveRoleChain). ok=false si el rol no tiene padre (raíz).
	ParentOf(ctx context.Context, id string) (parentID string, ok bool, err error)
	// GrantsOf devuelve los grants directos de UN rol (sin herencia).
	GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error)
	// AddGrant añade un grant al rol (idempotente por (role_id, pattern, effect)).
	AddGrant(ctx context.Context, roleID string, g domain.Grant) error
	// RemoveGrant elimina un grant del rol (no-op si no existía).
	RemoveGrant(ctx context.Context, roleID string, g domain.Grant) error
	// RolesOfUser devuelve los roles ASIGNADOS directamente a un usuario.
	RolesOfUser(ctx context.Context, userID string) ([]domain.Role, error)
	// AssignToUser asigna un rol a un usuario (idempotente por PK compuesta).
	AssignToUser(ctx context.Context, userID, roleID string) error
	// UnassignFromUser retira un rol de un usuario (no-op si no estaba).
	UnassignFromUser(ctx context.Context, userID, roleID string) error
}

// GrantRepo persiste los overrides de grants por usuario (tabla
// iam_user_grants) que se mergean sobre los del rol al emitir el token.
type GrantRepo interface {
	// GrantsOfUser devuelve los overrides de grants de un usuario.
	GrantsOfUser(ctx context.Context, userID string) ([]domain.Grant, error)
	// AddUserGrant añade un override (idempotente por (user_id, pattern, effect)).
	AddUserGrant(ctx context.Context, userID string, g domain.Grant) error
	// RemoveUserGrant elimina un override (no-op si no existía).
	RemoveUserGrant(ctx context.Context, userID string, g domain.Grant) error
}

// AuditRepo persiste la bitácora de auditoría (tabla audit_events). CERO PII.
type AuditRepo interface {
	// Record inserta un evento de auditoría append-only.
	Record(ctx context.Context, e domain.AuditEvent) error
	// List devuelve los eventos del tenant, más recientes primero, paginados.
	List(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}
