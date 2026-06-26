package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// createSchemaVersionTable es el DDL idempotente de la tabla de tracking. Vive
// en el schema public para sobrevivir a recreaciones parciales. execution_id se
// genera en cada INSERT (gen_random_uuid, nativo en PostgreSQL 16) y prueba que
// la migración realmente se ejecutó.
const createSchemaVersionTable = `
CREATE TABLE IF NOT EXISTS public.schema_version (
    id           SERIAL PRIMARY KEY,
    version      VARCHAR(20)  NOT NULL,
    content_hash VARCHAR(64)  NOT NULL,
    execution_id UUID         NOT NULL DEFAULT gen_random_uuid(),
    applied_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    applied_by   VARCHAR(100) NOT NULL DEFAULT 'migrator',
    description  TEXT
)`

// schemaRecord es el último registro de public.schema_version.
type schemaRecord struct {
	Version     string
	ContentHash string
	ExecutionID string
}

// ensureSchemaVersionTable crea la tabla de tracking si no existe.
func ensureSchemaVersionTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createSchemaVersionTable); err != nil {
		return fmt.Errorf("creando schema_version: %w", err)
	}
	return nil
}

// readSchemaVersion lee el último registro de schema_version. Devuelve
// sql.ErrNoRows envuelto si la tabla está vacía.
func readSchemaVersion(ctx context.Context, db *sql.DB) (schemaRecord, error) {
	var r schemaRecord
	err := db.QueryRowContext(ctx, `
		SELECT version, content_hash, execution_id::text
		FROM public.schema_version
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&r.Version, &r.ContentHash, &r.ExecutionID)
	if err != nil {
		return schemaRecord{}, fmt.Errorf("leyendo schema_version: %w", err)
	}
	return r, nil
}

// writeSchemaVersion inserta un registro de versión. execution_id lo genera
// PostgreSQL.
func writeSchemaVersion(ctx context.Context, db *sql.DB, version, contentHash, description string) (schemaRecord, error) {
	var r schemaRecord
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.schema_version (version, content_hash, description)
		VALUES ($1, $2, $3)
		RETURNING version, content_hash, execution_id::text
	`, version, contentHash, description).Scan(&r.Version, &r.ContentHash, &r.ExecutionID)
	if err != nil {
		return schemaRecord{}, fmt.Errorf("registrando schema_version: %w", err)
	}
	return r, nil
}

// isUpToDate indica si la BD ya está en la versión y hash esperados.
func isUpToDate(rec schemaRecord, version, contentHash string) bool {
	return rec.Version == version && rec.ContentHash == contentHash
}

// noRows devuelve true si el error corresponde a "sin registros".
func noRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
