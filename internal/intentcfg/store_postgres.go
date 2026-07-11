package intentcfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PostgresStore persiste el blob de intents en public.intent_configs.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore construye el store sobre el *sql.DB ya abierto.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Get lee la config del tenant; ErrNotFound si no existe fila. El blob se lee como
// texto crudo del JSONB (se devuelve verbatim al consumidor).
func (s *PostgresStore) Get(ctx context.Context, tenantID string) (Config, error) {
	var c Config
	err := s.db.QueryRowContext(ctx, `
		SELECT version, config::text, updated_at
		FROM public.intent_configs
		WHERE tenant_id = $1
	`, tenantID).Scan(&c.Version, &c.Blob, &c.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Config{}, fmt.Errorf("%w: tenant=%s", ErrNotFound, tenantID)
	case err != nil:
		return Config{}, fmt.Errorf("intentcfg: leer config: %w", err)
	}
	return c, nil
}

// Upsert persiste (o reemplaza) el blob del tenant con la version de entidad dada.
func (s *PostgresStore) Upsert(ctx context.Context, tenantID, version string, blob []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO public.intent_configs (tenant_id, version, config, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (tenant_id) DO UPDATE
		SET version = EXCLUDED.version, config = EXCLUDED.config, updated_at = now()
	`, tenantID, version, blob)
	if err != nil {
		return fmt.Errorf("intentcfg: upsert config: %w", err)
	}
	return nil
}
