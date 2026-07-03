package iampostgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// GrantRepo implementa out.GrantRepo sobre public.iam_user_grants (overrides
// por usuario que se mergean sobre los del rol al emitir el token).
type GrantRepo struct {
	db *sql.DB
}

// NewGrantRepo construye el repositorio sobre el pool dado.
func NewGrantRepo(db *sql.DB) *GrantRepo { return &GrantRepo{db: db} }

var _ out.GrantRepo = (*GrantRepo)(nil)

// GrantsOfUser implementa out.GrantRepo.
func (r *GrantRepo) GrantsOfUser(ctx context.Context, userID string) ([]domain.Grant, error) {
	return queryGrants(ctx, r.db, `
		SELECT pattern, effect FROM public.iam_user_grants WHERE user_id = $1
	`, userID)
}

// AddUserGrant implementa out.GrantRepo (idempotente por (user_id, pattern, effect)).
func (r *GrantRepo) AddUserGrant(ctx context.Context, userID string, g domain.Grant) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.iam_user_grants (user_id, pattern, effect)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, pattern, effect) DO NOTHING
	`, userID, g.Pattern, string(g.Effect))
	if err != nil {
		return fmt.Errorf("iam: añadir override de grant: %w", err)
	}
	return nil
}

// RemoveUserGrant implementa out.GrantRepo.
func (r *GrantRepo) RemoveUserGrant(ctx context.Context, userID string, g domain.Grant) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM public.iam_user_grants
		WHERE user_id = $1 AND pattern = $2 AND effect = $3
	`, userID, g.Pattern, string(g.Effect))
	if err != nil {
		return fmt.Errorf("iam: quitar override de grant: %w", err)
	}
	return nil
}
