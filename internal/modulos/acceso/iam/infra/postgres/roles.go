// Porta internal/iam/infra/postgres/roles.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// RoleRepo implementa out.RoleRepo sobre public.iam_roles, iam_role_grants e iam_user_roles.
type RoleRepo struct{}

// NewRoleRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewRoleRepo(db *sql.DB) *RoleRepo {
	panic(pendiente.Implementar("iampostgres.NewRoleRepo"))
}

var _ out.RoleRepo = (*RoleRepo)(nil)

// Create implementa out.RoleRepo (rol custom del tenant, o plantilla global con TenantID nil): la
// fila que la base escribió (id y created_at reales). Nombre repetido en el mismo ámbito →
// error que envuelve domain.ErrConflict ("…: rol=<nombre>"); cualquier otro fallo de la base →
// "iam: crear rol: …", nunca ErrConflict.
func (r *RoleRepo) Create(ctx context.Context, role domain.Role) (domain.Role, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.Create"))
}

// GetByID implementa out.RoleRepo. Inexistente → domain.ErrNotFound; un fallo de la base NO es
// ausencia: sale envuelto con "iam: leer rol: …".
func (r *RoleRepo) GetByID(ctx context.Context, id string) (domain.Role, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.GetByID"))
}

// List implementa out.RoleRepo: los roles del tenant más las plantillas globales (tenant_id
// NULL), nunca los de otra empresa, por `created_at`. Fallo de la base → (nil, "iam: listar
// roles: …").
func (r *RoleRepo) List(ctx context.Context, tenantID string) ([]domain.Role, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.List"))
}

// ParentOf implementa out.RoleRepo. ok=false si el rol no existe o no tiene padre; un fallo de
// la base NO es «sin padre»: sale ("", false, err) con "iam: leer parent de rol: …".
func (r *RoleRepo) ParentOf(ctx context.Context, id string) (string, bool, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.ParentOf"))
}

// GrantsOf implementa out.RoleRepo: los grants (pattern, effect) del rol. Fallo de la base →
// (nil, "iam: leer grants: …").
func (r *RoleRepo) GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.GrantsOf"))
}

// AddGrant implementa out.RoleRepo (idempotente por (role_id, pattern, effect)). Fallo de la base
// → "iam: añadir grant a rol: …".
func (r *RoleRepo) AddGrant(ctx context.Context, roleID string, g domain.Grant) error {
	panic(pendiente.Implementar("iampostgres.RoleRepo.AddGrant"))
}

// RemoveGrant implementa out.RoleRepo (no-op si no estaba). Fallo de la base → "iam: quitar
// grant de rol: …".
func (r *RoleRepo) RemoveGrant(ctx context.Context, roleID string, g domain.Grant) error {
	panic(pendiente.Implementar("iampostgres.RoleRepo.RemoveGrant"))
}

// RolesOfUser implementa out.RoleRepo. Con tenantID, los roles asignados al usuario acotados a
// ese tenant o globales, primero los acotados y sin repetir un rol asignado de las dos formas
// (T1.1b); con tenantID "", solo los globales. Fallo de la base → (nil, "iam: listar roles de
// usuario: …").
func (r *RoleRepo) RolesOfUser(ctx context.Context, userID, tenantID string) ([]domain.Role, error) {
	panic(pendiente.Implementar("iampostgres.RoleRepo.RolesOfUser"))
}

// AssignToUser implementa out.RoleRepo (idempotente). tenantID no nil acota la fila a esa
// empresa (D-056.11); nil (o "") la asigna con ámbito GLOBAL.
//
// 🔒 GUARDA DE ÁMBITO (Plan 047 · Ola 5 · T5.6): el ámbito global es del rol transversal
// (domain.TransversalRoleID, por su id y no por su nombre, T-10) y de ningún otro. Cualquier otro
// rol con tenantID nil o "" → error que envuelve domain.ErrRoleScopeInvalid ("…: rol=<id>"), y
// la base NO se toca: el rechazo va antes del INSERT y no depende de poder deshacer la fila. Un
// fallo de la base → "iam: asignar rol a usuario: …".
func (r *RoleRepo) AssignToUser(ctx context.Context, userID, roleID string, tenantID *string) error {
	panic(pendiente.Implementar("iampostgres.RoleRepo.AssignToUser"))
}

// UnassignFromUser implementa out.RoleRepo, SIMÉTRICO a AssignToUser: tenantID nil retira la
// asignación GLOBAL y no nil solo la acotada a esa empresa. NO lleva la guarda de ámbito:
// retirar una asignación global de un rol de empresa (la fila mala que la guarda ya no deja
// escribir) tiene que seguir siendo posible. Fallo de la base → "iam: quitar rol de usuario: …".
func (r *RoleRepo) UnassignFromUser(ctx context.Context, userID, roleID string, tenantID *string) error {
	panic(pendiente.Implementar("iampostgres.RoleRepo.UnassignFromUser"))
}
