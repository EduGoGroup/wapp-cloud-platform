package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// RoleRepo implementa out.RoleRepo sobre public.iam_roles, iam_role_grants e
// iam_user_roles.
type RoleRepo struct {
	db *sql.DB
}

// NewRoleRepo construye el repositorio sobre el pool dado.
func NewRoleRepo(db *sql.DB) *RoleRepo { return &RoleRepo{db: db} }

var _ out.RoleRepo = (*RoleRepo)(nil)

const roleCols = `id::text, tenant_id::text, name, parent_role_id::text, created_at`

// scanRole escanea una fila de iam_roles a domain.Role.
func scanRole(row interface{ Scan(...any) error }) (domain.Role, error) {
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

// Create implementa out.RoleRepo (rol custom del tenant).
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

// GetByID implementa out.RoleRepo.
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

// List implementa out.RoleRepo (roles del tenant + plantillas globales).
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

// ParentOf implementa out.RoleRepo. ok=false si el rol no existe o no tiene padre.
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

// GrantsOf implementa out.RoleRepo.
func (r *RoleRepo) GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error) {
	return queryGrants(ctx, r.db, `
		SELECT pattern, effect FROM public.iam_role_grants WHERE role_id = $1
	`, roleID)
}

// AddGrant implementa out.RoleRepo (idempotente por (role_id, pattern, effect)).
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

// RemoveGrant implementa out.RoleRepo.
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

// RolesOfUser implementa out.RoleRepo.
func (r *RoleRepo) RolesOfUser(ctx context.Context, userID string) ([]domain.Role, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id::text, r.tenant_id::text, r.name, r.parent_role_id::text, r.created_at
		FROM public.iam_roles r
		JOIN public.iam_user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar roles de usuario: %w", err)
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
			return nil, fmt.Errorf("iam: escanear rol de usuario: %w", serr)
		}
		res = append(res, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar roles de usuario: %w", err)
	}
	return res, nil
}

// AssignToUser implementa out.RoleRepo (idempotente por PK compuesta).
func (r *RoleRepo) AssignToUser(ctx context.Context, userID, roleID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, role_id) DO NOTHING
	`, userID, roleID)
	if err != nil {
		return fmt.Errorf("iam: asignar rol a usuario: %w", err)
	}
	return nil
}

// UnassignFromUser implementa out.RoleRepo.
func (r *RoleRepo) UnassignFromUser(ctx context.Context, userID, roleID string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM public.iam_user_roles WHERE user_id = $1 AND role_id = $2
	`, userID, roleID)
	if err != nil {
		return fmt.Errorf("iam: quitar rol de usuario: %w", err)
	}
	return nil
}

// queryGrants ejecuta una consulta que devuelve (pattern, effect) y la mapea a
// []domain.Grant. Compartido por RoleRepo.GrantsOf y GrantRepo.GrantsOfUser.
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
