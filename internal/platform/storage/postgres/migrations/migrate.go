package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Result describe el desenlace de Migrate.
type Result struct {
	// Version es la versión de esquema vigente tras la operación.
	Version string
	// ContentHash es el hash de contenido vigente tras la operación.
	ContentHash string
	// ExecutionID es el UUID del registro escrito; vacío si Skipped.
	ExecutionID string
	// Skipped es true si la BD ya estaba al día y no se aplicó nada.
	Skipped bool
}

// Migrate aplica la estructura embebida sobre db de forma idempotente.
//
// Crea (si no existe) la tabla de tracking public.schema_version, compara la
// versión/hash registrados con los esperados y, si difieren, ejecuta todos los
// scripts de structure/ en orden alfabético (DDL idempotente con IF NOT EXISTS)
// y registra la nueva versión. Si ya está al día, no toca la BD (Skipped=true).
func Migrate(ctx context.Context, db *sql.DB) (Result, error) {
	if err := ensureSchemaVersionTable(ctx, db); err != nil {
		return Result{}, err
	}

	expectedHash := ComputeFilesHash()

	rec, err := readSchemaVersion(ctx, db)
	switch {
	case err == nil && isUpToDate(rec, SchemaVersion, expectedHash):
		return Result{
			Version:     rec.Version,
			ContentHash: rec.ContentHash,
			ExecutionID: rec.ExecutionID,
			Skipped:     true,
		}, nil
	case err != nil && !noRows(err):
		return Result{}, err
	}

	if err := applyStructure(ctx, db); err != nil {
		return Result{}, err
	}

	written, err := writeSchemaVersion(ctx, db, SchemaVersion, expectedHash, "migración de estructura")
	if err != nil {
		return Result{}, err
	}

	return Result{
		Version:     written.Version,
		ContentHash: written.ContentHash,
		ExecutionID: written.ExecutionID,
	}, nil
}

// Status devuelve el estado registrado sin modificar la BD. Skipped indica si
// coincide con la versión/hash esperados.
func Status(ctx context.Context, db *sql.DB) (Result, error) {
	rec, err := readSchemaVersion(ctx, db)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Version:     rec.Version,
		ContentHash: rec.ContentHash,
		ExecutionID: rec.ExecutionID,
		Skipped:     isUpToDate(rec, SchemaVersion, ComputeFilesHash()),
	}, nil
}

// applyStructure ejecuta en orden alfabético todos los archivos .sql embebidos.
func applyStructure(ctx context.Context, db *sql.DB) error {
	entries, err := fs.ReadDir(structureFS, structureDir)
	if err != nil {
		return fmt.Errorf("listando structure/: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		content, readErr := structureFS.ReadFile(structureDir + "/" + name)
		if readErr != nil {
			return fmt.Errorf("leyendo %s: %w", name, readErr)
		}
		if _, execErr := db.ExecContext(ctx, string(content)); execErr != nil {
			return fmt.Errorf("ejecutando %s: %w", name, execErr)
		}
	}
	return nil
}
