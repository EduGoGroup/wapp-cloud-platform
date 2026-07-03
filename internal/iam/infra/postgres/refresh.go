package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// RefreshRepo implementa out.RefreshRepo sobre public.iam_refresh_tokens. Solo
// persiste el hash del token; el token en claro NUNCA toca la BD.
type RefreshRepo struct {
	db *sql.DB
}

// NewRefreshRepo construye el repositorio sobre el pool dado.
func NewRefreshRepo(db *sql.DB) *RefreshRepo { return &RefreshRepo{db: db} }

var _ out.RefreshRepo = (*RefreshRepo)(nil)

// Save implementa out.RefreshRepo.
func (r *RefreshRepo) Save(ctx context.Context, rt domain.RefreshToken) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.iam_refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, rt.UserID, rt.TokenHash, rt.ExpiresAt)
	if err != nil {
		return fmt.Errorf("iam: guardar refresh: %w", err)
	}
	return nil
}

// GetByHash implementa out.RefreshRepo.
func (r *RefreshRepo) GetByHash(ctx context.Context, tokenHash string) (domain.RefreshToken, error) {
	var (
		rt      domain.RefreshToken
		revoked sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, user_id::text, token_hash, expires_at, revoked_at, created_at
		FROM public.iam_refresh_tokens
		WHERE token_hash = $1
	`, tokenHash).Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.ExpiresAt, &revoked, &rt.CreatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.RefreshToken{}, domain.ErrNotFound
	case err != nil:
		return domain.RefreshToken{}, fmt.Errorf("iam: leer refresh: %w", err)
	}
	rt.RevokedAt = timePtr(revoked)
	return rt, nil
}

// Revoke implementa out.RefreshRepo (idempotente: no-op si no existe/ya revocado).
func (r *RefreshRepo) Revoke(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.iam_refresh_tokens
		SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash)
	if err != nil {
		return fmt.Errorf("iam: revocar refresh: %w", err)
	}
	return nil
}

// RevokeAllForUser implementa out.RefreshRepo.
func (r *RefreshRepo) RevokeAllForUser(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.iam_refresh_tokens
		SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return fmt.Errorf("iam: revocar refresh de usuario: %w", err)
	}
	return nil
}
