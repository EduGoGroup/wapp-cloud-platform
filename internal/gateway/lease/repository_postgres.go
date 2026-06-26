package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresRepository implementa Repository con SQL raw sobre public.leases.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository construye el repositorio sobre el pool dado.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// Upsert inserta o actualiza el lease vigente del Edge (revoked=false). Conserva
// issued_at en la primera emisión.
func (r *PostgresRepository) Upsert(ctx context.Context, s State) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)
		VALUES ($1, $2, $3, $4, false, now(), now())
		ON CONFLICT (tenant_id, edge_id) DO UPDATE
		SET counter = EXCLUDED.counter,
		    expires_at = EXCLUDED.expires_at,
		    revoked = false,
		    updated_at = now()
	`, s.TenantID, s.EdgeID, s.Counter, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("lease: upsert lease: %w", err)
	}
	return nil
}

// MarkRevoked marca el lease del Edge como revocado conservando el counter; crea
// la fila (counter=0) si aún no existía, para que el kill-switch siempre deje
// rastro.
func (r *PostgresRepository) MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)
		VALUES ($1, $2, 0, $3, true, now(), now())
		ON CONFLICT (tenant_id, edge_id) DO UPDATE
		SET revoked = true,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = now()
	`, tenantID, edgeID, expiresAt)
	if err != nil {
		return fmt.Errorf("lease: marcar revocado: %w", err)
	}
	return nil
}

// Get devuelve el estado del lease del Edge, o found=false si no existe.
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID string) (State, bool, error) {
	var s State
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, edge_id, counter, expires_at, revoked, issued_at, updated_at
		FROM public.leases
		WHERE tenant_id = $1 AND edge_id = $2
	`, tenantID, edgeID).Scan(
		&s.TenantID, &s.EdgeID, &s.Counter, &s.ExpiresAt, &s.Revoked, &s.IssuedAt, &s.UpdatedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return State{}, false, nil
	case err != nil:
		return State{}, false, fmt.Errorf("lease: leer lease: %w", err)
	}
	return s, true, nil
}
