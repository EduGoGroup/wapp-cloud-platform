// Porta internal/iam/infra/postgres/roles.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// RoleRepo implementa out.RoleRepo sobre public.iam_roles, iam_role_grants e iam_user_roles.
type RoleRepo struct {
	db *sql.DB
}

// NewRoleRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewRoleRepo(db *sql.DB) *RoleRepo { return &RoleRepo{db: db} }

var _ out.RoleRepo = (*RoleRepo)(nil)

const roleCols = `id::text, tenant_id::text, name, parent_role_id::text, created_at`

// rowScanner es lo que scanRole y scanInvitation necesitan de una fila: *sql.Row y *sql.Rows lo
// cumplen (el viejo lo escribía en línea, interface{ Scan(...any) error }).
type rowScanner interface{ Scan(dest ...any) error }

// scanRole escanea una fila de iam_roles (en el orden de roleCols) a domain.Role. tenant_id
// NULL ⇒ TenantID nil (plantilla global); parent_role_id NULL ⇒ ParentRoleID nil.
func scanRole(row rowScanner) (domain.Role, error) {
	var (
		r      domain.Role
		tenant sql.NullString
		parent sql.NullString
	)
	if err := row.Scan(&r.ID, &tenant, &r.Name, &parent, &r.CreatedAt); err != nil {
		return domain.Role{}, err
	}
	r.TenantID = strPtr(tenant)
	r.ParentRoleID = strPtr(parent)
	return r, nil
}

// Create implementa out.RoleRepo (rol custom del tenant, o plantilla global con TenantID nil): la
// fila que la base escribió (id y created_at reales). Nombre repetido en el mismo ámbito →
// error que envuelve domain.ErrConflict ("…: rol=<nombre>"); cualquier otro fallo de la base →
// "iam: crear rol: …", nunca ErrConflict.
func (r *RoleRepo) Create(ctx context.Context, role domain.Role) (domain.Role, error) {
	var (
		created domain.Role
		tenant  sql.NullString
		parent  sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO public.iam_roles (tenant_id, name, parent_role_id)
		VALUES ($1, $2, $3)
		RETURNING `+roleCols,
		nullString(role.TenantID), role.Name, nullString(role.ParentRoleID),
	).Scan(&created.ID, &tenant, &created.Name, &parent, &created.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Role{}, fmt.Errorf("%w: rol=%s", domain.ErrConflict, role.Name)
		}
		return domain.Role{}, fmt.Errorf("iam: crear rol: %w", err)
	}
	created.TenantID = strPtr(tenant)
	created.ParentRoleID = strPtr(parent)
	return created, nil
}

// GetByID implementa out.RoleRepo. Inexistente → domain.ErrNotFound; un fallo de la base NO es
// ausencia: sale envuelto con "iam: leer rol: …".
func (r *RoleRepo) GetByID(ctx context.Context, id string) (domain.Role, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+roleCols+` FROM public.iam_roles WHERE id = $1`, id)
	role, err := scanRole(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Role{}, domain.ErrNotFound
	case err != nil:
		return domain.Role{}, fmt.Errorf("iam: leer rol: %w", err)
	}
	return role, nil
}

// List implementa out.RoleRepo: los roles del tenant más las plantillas globales (tenant_id
// NULL), nunca los de otra empresa, por `created_at`. Fallo de la base → (nil, "iam: listar
// roles: …").
func (r *RoleRepo) List(ctx context.Context, tenantID string) ([]domain.Role, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+roleCols+`
		FROM public.iam_roles
		WHERE tenant_id = $1 OR tenant_id IS NULL
		ORDER BY created_at ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar roles: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.Role
	for rows.Next() {
		role, serr := scanRole(rows)
		if serr != nil {
			return nil, fmt.Errorf("iam: escanear rol: %w", serr)
		}
		res = append(res, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar roles: %w", err)
	}
	return res, nil
}

// ParentOf implementa out.RoleRepo. ok=false si el rol no existe o no tiene padre; un fallo de
// la base NO es «sin padre»: sale ("", false, err) con "iam: leer parent de rol: …".
func (r *RoleRepo) ParentOf(ctx context.Context, id string) (string, bool, error) {
	var parent sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT parent_role_id::text FROM public.iam_roles WHERE id = $1
	`, id).Scan(&parent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("iam: leer parent de rol: %w", err)
	}
	if !parent.Valid || parent.String == "" {
		return "", false, nil
	}
	return parent.String, true, nil
}

// GrantsOf implementa out.RoleRepo: los grants (pattern, effect) del rol. Fallo de la base →
// (nil, "iam: leer grants: …").
func (r *RoleRepo) GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error) {
	return queryGrants(ctx, r.db, `
		SELECT pattern, effect FROM public.iam_role_grants WHERE role_id = $1
	`, roleID)
}

// AddGrant implementa out.RoleRepo (idempotente por (role_id, pattern, effect)). Fallo de la base
// → "iam: añadir grant a rol: …".
func (r *RoleRepo) AddGrant(ctx context.Context, roleID string, g domain.Grant) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.iam_role_grants (role_id, pattern, effect)
		VALUES ($1, $2, $3)
		ON CONFLICT (role_id, pattern, effect) DO NOTHING
	`, roleID, g.Pattern, string(g.Effect))
	if err != nil {
		return fmt.Errorf("iam: añadir grant a rol: %w", err)
	}
	return nil
}

// RemoveGrant implementa out.RoleRepo (no-op si no estaba). Fallo de la base → "iam: quitar
// grant de rol: …".
func (r *RoleRepo) RemoveGrant(ctx context.Context, roleID string, g domain.Grant) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM public.iam_role_grants
		WHERE role_id = $1 AND pattern = $2 AND effect = $3
	`, roleID, g.Pattern, string(g.Effect))
	if err != nil {
		return fmt.Errorf("iam: quitar grant de rol: %w", err)
	}
	return nil
}

// RolesOfUser implementa out.RoleRepo. Con tenantID, los roles asignados al usuario acotados a
// ese tenant o globales, primero los acotados y sin repetir un rol asignado de las dos formas
// (T1.1b); con tenantID "", solo los globales. Fallo de la base → (nil, "iam: listar roles de
// usuario: …").
func (r *RoleRepo) RolesOfUser(ctx context.Context, userID, tenantID string) ([]domain.Role, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if tenantID != "" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT r.id::text, r.tenant_id::text, r.name, r.parent_role_id::text, r.created_at
			FROM public.iam_roles r
			JOIN public.iam_user_roles ur ON ur.role_id = r.id
			WHERE ur.user_id = $1 AND (ur.tenant_id = $2 OR ur.tenant_id IS NULL)
			ORDER BY (ur.tenant_id IS NOT NULL) DESC, r.created_at ASC
		`, userID, tenantID)
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT r.id::text, r.tenant_id::text, r.name, r.parent_role_id::text, r.created_at
			FROM public.iam_roles r
			JOIN public.iam_user_roles ur ON ur.role_id = r.id
			WHERE ur.user_id = $1 AND ur.tenant_id IS NULL
			ORDER BY r.created_at ASC
		`, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("iam: listar roles de usuario: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.Role
	seen := make(map[string]bool)
	for rows.Next() {
		role, serr := scanRole(rows)
		if serr != nil {
			return nil, fmt.Errorf("iam: escanear rol de usuario: %w", serr)
		}
		if seen[role.ID] {
			continue // Deduplica si el mismo rol estaba asignado acotado y global
		}
		seen[role.ID] = true
		res = append(res, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar roles de usuario: %w", err)
	}
	return res, nil
}

// validateAssignmentScope (era validarAmbitoDeAsignacion) decide si una asignación de rol puede ir
// con ámbito GLOBAL (public.iam_user_roles.tenant_id NULL). Es la guarda de T5.6 (Plan 047 ·
// Ola 5) y la comparten las DOS vías que escriben en esa tabla: RoleRepo.AssignToUser y
// GrantTenantAccess.
//
// LA REGLA, en una frase: el ámbito global es del rol transversal y de ningún otro. Lo
// transversal se reconoce por el id fijo que siembra la migración 0059
// (domain.TransversalRoleID) — el porqué de que sea el id y no el nombre está escrito ahí, y se
// resume en que el nombre lo puede falsificar cualquier tenant creando un rol propio que se llame
// igual.
//
// 🔴 POR QUÉ VIVE EN EL REPOSITORIO Y NO EN EL CASO DE USO. Porque el hueco está aquí:
// AssignToUser aceptaba `nil` sin preguntar y escribía NULL, así que lo único que impedía el
// desastre era que ningún llamante se hubiera equivocado todavía. Eso no es una guarda, es una
// costumbre — y la fila mala que hay en la base de UAT (un tenant_admin con tenant_id NULL, que
// ninguna migración siembra y ningún camino de producto produce) demuestra que la costumbre ya se
// rompió una vez, por SQL directo.
//
// El string vacío cuenta como global igual que el nil: es la forma que toma «sin empresa» cuando
// el tenant viaja por valor, y dejarla fuera abriría la misma puerta con otra llave.
func validateAssignmentScope(roleID string, tenantID *string) error {
	global := tenantID == nil || *tenantID == ""
	if !global || roleID == domain.TransversalRoleID {
		return nil
	}
	return fmt.Errorf("%w: rol=%s", domain.ErrRoleScopeInvalid, roleID)
}

// AssignToUser implementa out.RoleRepo (idempotente). tenantID no nil acota la fila a esa
// empresa (D-056.11); nil (o "") la asigna con ámbito GLOBAL.
//
// 🔒 GUARDA DE ÁMBITO (Plan 047 · Ola 5 · T5.6): el ámbito global es del rol transversal
// (domain.TransversalRoleID, por su id y no por su nombre, T-10) y de ningún otro. Cualquier otro
// rol con tenantID nil o "" → error que envuelve domain.ErrRoleScopeInvalid ("…: rol=<id>"), y
// la base NO se toca: el rechazo va antes del INSERT y no depende de poder deshacer la fila. Un
// fallo de la base → "iam: asignar rol a usuario: …".
//
// Idempotente por los índices únicos de iam_user_roles — desde 0060 ya NO hay PK: UNIQUE
// (user_id, role_id, tenant_id) más UNIQUE parcial (user_id, role_id) WHERE tenant_id IS NULL. El
// ON CONFLICT va SIN target a propósito: un índice PARCIAL no entra en la inferencia por columnas
// a secas (haría falta repetir su WHERE), así que la forma sin target es la única que cubre los
// dos índices a la vez — mismo patrón que 0059_platform_admin.sql:52-57.
func (r *RoleRepo) AssignToUser(ctx context.Context, userID, roleID string, tenantID *string) error {
	// GUARDA DE ÁMBITO (T5.6): un rol de empresa con tenant_id NULL sería un rol que vale en
	// TODAS las empresas del titular. Va ANTES del INSERT y no después, para que el rechazo no
	// dependa de que la fila se pueda deshacer.
	if err := validateAssignmentScope(roleID, tenantID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, userID, roleID, nullString(tenantID))
	if err != nil {
		return fmt.Errorf("iam: asignar rol a usuario: %w", err)
	}
	return nil
}

// UnassignFromUser implementa out.RoleRepo, SIMÉTRICO a AssignToUser: tenantID nil retira la
// asignación GLOBAL y no nil solo la acotada a esa empresa. NO lleva la guarda de ámbito:
// retirar una asignación global de un rol de empresa (la fila mala que la guarda ya no deja
// escribir) tiene que seguir siendo posible. Fallo de la base → "iam: quitar rol de usuario: …".
//
// El `tenant_id IS NOT DISTINCT FROM $3` es lo que hace las dos ramas UNA: `=` nunca casa contra
// NULL, así que con la comparación normal el caso global —que es el que escribe AssignToUser con
// nil— no borraría nada.
func (r *RoleRepo) UnassignFromUser(ctx context.Context, userID, roleID string, tenantID *string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM public.iam_user_roles
		WHERE user_id = $1 AND role_id = $2 AND tenant_id IS NOT DISTINCT FROM $3
	`, userID, roleID, nullString(tenantID))
	if err != nil {
		return fmt.Errorf("iam: quitar rol de usuario: %w", err)
	}
	return nil
}

// queryGrants ejecuta una consulta que devuelve (pattern, effect) y la mapea a []domain.Grant.
// Compartido por RoleRepo.GrantsOf y GrantRepo.GrantsOfUser.
func queryGrants(ctx context.Context, db *sql.DB, query string, args ...any) ([]domain.Grant, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("iam: leer grants: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.Grant
	for rows.Next() {
		var (
			pattern string
			effect  string
		)
		if serr := rows.Scan(&pattern, &effect); serr != nil {
			return nil, fmt.Errorf("iam: escanear grant: %w", serr)
		}
		res = append(res, domain.Grant{Pattern: pattern, Effect: domain.Effect(effect)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar grants: %w", err)
	}
	return res, nil
}
