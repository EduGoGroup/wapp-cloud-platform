package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// APIKeyRepo implementa out.APIKeyRepo sobre public.iam_api_keys. Solo persiste
// el hash del secreto; el secreto en claro NUNCA toca la BD.
type APIKeyRepo struct {
	db *sql.DB
}

// NewAPIKeyRepo construye el repositorio sobre el pool dado.
func NewAPIKeyRepo(db *sql.DB) *APIKeyRepo { return &APIKeyRepo{db: db} }

var _ out.APIKeyRepo = (*APIKeyRepo)(nil)

// keyReadCols es la lista de columnas leída (scopes como CSV vía array_to_string).
const keyReadCols = `
	id::text, tenant_id::text, client_id, key_hash,
	COALESCE(array_to_string(scopes, ','), ''),
	is_active, created_at, last_used_at, expires_at, revoked_at`

// scanAPIKey escanea una fila (con scopes como CSV) a domain.APIKey.
func scanAPIKey(row interface{ Scan(...any) error }) (domain.APIKey, error) {
	var (
		k        domain.APIKey
		scopes   string
		lastUsed sql.NullTime
		expires  sql.NullTime
		revoked  sql.NullTime
	)
	if err := row.Scan(&k.ID, &k.TenantID, &k.ClientID, &k.KeyHash, &scopes,
		&k.IsActive, &k.CreatedAt, &lastUsed, &expires, &revoked); err != nil {
		return domain.APIKey{}, err
	}
	k.Scopes = decodeCSV(scopes)
	k.LastUsedAt = timePtr(lastUsed)
	k.ExpiresAt = timePtr(expires)
	k.RevokedAt = timePtr(revoked)
	return k, nil
}

// Create implementa out.APIKeyRepo.
func (r *APIKeyRepo) Create(ctx context.Context, k domain.APIKey) (domain.APIKey, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO public.iam_api_keys (tenant_id, client_id, key_hash, scopes, is_active, expires_at)
		VALUES ($1, $2, $3, $4::text[], $5, $6)
		RETURNING `+keyReadCols,
		k.TenantID, k.ClientID, k.KeyHash, encodeTextArray(k.Scopes), k.IsActive, nullTime(k.ExpiresAt),
	)
	created, err := scanAPIKey(row)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.APIKey{}, fmt.Errorf("%w: client_id=%s", domain.ErrConflict, k.ClientID)
		}
		return domain.APIKey{}, fmt.Errorf("iam: crear api-key: %w", err)
	}
	return created, nil
}

// GetByHash implementa out.APIKeyRepo.
func (r *APIKeyRepo) GetByHash(ctx context.Context, keyHash string) (domain.APIKey, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+keyReadCols+`
		FROM public.iam_api_keys
		WHERE key_hash = $1
	`, keyHash)
	k, err := scanAPIKey(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.APIKey{}, domain.ErrNotFound
	case err != nil:
		return domain.APIKey{}, fmt.Errorf("iam: leer api-key por hash: %w", err)
	}
	return k, nil
}

// List implementa out.APIKeyRepo.
func (r *APIKeyRepo) List(ctx context.Context, tenantID string) ([]domain.APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+keyReadCols+`
		FROM public.iam_api_keys
		WHERE tenant_id = $1
		ORDER BY created_at ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar api-keys: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.APIKey
	for rows.Next() {
		k, serr := scanAPIKey(rows)
		if serr != nil {
			return nil, fmt.Errorf("iam: escanear api-key: %w", serr)
		}
		res = append(res, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar api-keys: %w", err)
	}
	return res, nil
}

// Revoke implementa out.APIKeyRepo.
func (r *APIKeyRepo) Revoke(ctx context.Context, tenantID, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.iam_api_keys
		SET revoked_at = now(), is_active = false
		WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL
	`, id, tenantID)
	if err != nil {
		return fmt.Errorf("iam: revocar api-key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("iam: filas afectadas revocar api-key: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TouchLastUsed implementa out.APIKeyRepo (telemetría best-effort).
func (r *APIKeyRepo) TouchLastUsed(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.iam_api_keys SET last_used_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("iam: actualizar last_used de api-key: %w", err)
	}
	return nil
}
