package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// UserRepo implementa out.UserRepo sobre public.iam_users.
type UserRepo struct {
	db *sql.DB
}

// NewUserRepo construye el repositorio sobre el pool dado.
func NewUserRepo(db *sql.DB) *UserRepo { return &UserRepo{db: db} }

var _ out.UserRepo = (*UserRepo)(nil)

// scanUser escanea una fila de iam_users a domain.User.
func scanUser(row interface{ Scan(...any) error }) (domain.User, error) {
	var (
		u       domain.User
		deleted sql.NullTime
	)
	if err := row.Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.IsActive,
		&u.CreatedAt, &u.UpdatedAt, &deleted); err != nil {
		return domain.User{}, err
	}
	u.DeletedAt = timePtr(deleted)
	return u, nil
}

const userCols = `id::text, tenant_id::text, email, password_hash, is_active, created_at, updated_at, deleted_at`

// Create implementa out.UserRepo.
func (r *UserRepo) Create(ctx context.Context, u domain.User) (domain.User, error) {
	var created domain.User
	var deleted sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO public.iam_users (tenant_id, email, password_hash, is_active)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userCols,
		u.TenantID, u.Email, u.PasswordHash, u.IsActive,
	).Scan(&created.ID, &created.TenantID, &created.Email, &created.PasswordHash,
		&created.IsActive, &created.CreatedAt, &created.UpdatedAt, &deleted)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.User{}, fmt.Errorf("%w: email=%s", domain.ErrConflict, u.Email)
		}
		return domain.User{}, fmt.Errorf("iam: crear usuario: %w", err)
	}
	created.DeletedAt = timePtr(deleted)
	return created, nil
}

// GetByID implementa out.UserRepo (excluye soft-deleted).
func (r *UserRepo) GetByID(ctx context.Context, id string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+userCols+`
		FROM public.iam_users
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	u, err := scanUser(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.User{}, domain.ErrNotFound
	case err != nil:
		return domain.User{}, fmt.Errorf("iam: leer usuario por id: %w", err)
	}
	return u, nil
}

// FindByEmail implementa out.UserRepo (global, excluye soft-deleted).
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+userCols+`
		FROM public.iam_users
		WHERE email = $1 AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, email)
	u, err := scanUser(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.User{}, domain.ErrNotFound
	case err != nil:
		return domain.User{}, fmt.Errorf("iam: buscar usuario por email: %w", err)
	}
	return u, nil
}

// GetByEmail implementa out.UserRepo (acotado a tenant, excluye soft-deleted).
func (r *UserRepo) GetByEmail(ctx context.Context, tenantID, email string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+userCols+`
		FROM public.iam_users
		WHERE tenant_id = $1 AND email = $2 AND deleted_at IS NULL
	`, tenantID, email)
	u, err := scanUser(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.User{}, domain.ErrNotFound
	case err != nil:
		return domain.User{}, fmt.Errorf("iam: leer usuario por email: %w", err)
	}
	return u, nil
}

// List implementa out.UserRepo (usuarios activos del tenant).
func (r *UserRepo) List(ctx context.Context, tenantID string) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+userCols+`
		FROM public.iam_users
		WHERE tenant_id = $1 AND deleted_at IS NULL
		ORDER BY created_at ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar usuarios: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.User
	for rows.Next() {
		u, serr := scanUser(rows)
		if serr != nil {
			return nil, fmt.Errorf("iam: escanear usuario: %w", serr)
		}
		res = append(res, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar usuarios: %w", err)
	}
	return res, nil
}

// SoftDelete implementa out.UserRepo.
func (r *UserRepo) SoftDelete(ctx context.Context, tenantID, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.iam_users
		SET deleted_at = now(), is_active = false, updated_at = now()
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, id, tenantID)
	if err != nil {
		return fmt.Errorf("iam: baja de usuario: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("iam: filas afectadas baja usuario: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
