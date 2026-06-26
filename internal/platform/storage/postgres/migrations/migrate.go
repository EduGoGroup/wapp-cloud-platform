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

// migrationLockKey es la clave constante del advisory lock de sesión que
// serializa Migrate. PostgreSQL identifica el lock por este int64; cualquier
// proceso/conexión que use el mismo valor compite por el mismo lock.
//
// Valor derivado de SHA-256("wapp_cloud_migrations") tomando los primeros 8
// bytes como int64 big-endian (sha256[:8] = 0x73 0x6f ... ). Es una constante
// fija del proyecto; no la cambies salvo que quieras un dominio de lock nuevo.
const migrationLockKey int64 = 0x736f0b2a4c1d9e57

// Migrate aplica la estructura embebida sobre db de forma idempotente.
//
// Crea (si no existe) la tabla de tracking public.schema_version, compara la
// versión/hash registrados con los esperados y, si difieren, ejecuta todos los
// scripts de structure/ en orden alfabético (DDL idempotente con IF NOT EXISTS)
// y registra la nueva versión. Si ya está al día, no toca la BD (Skipped=true).
//
// Toda la sección crítica se serializa con un advisory lock de sesión de
// PostgreSQL (pg_advisory_lock) sobre una conexión dedicada del pool. Esto
// evita la race conocida de CREATE TABLE IF NOT EXISTS cuando varias goroutines
// (p.ej. paquetes de test en paralelo) migran a la vez una BD fresca: el lock
// hace que la primera cree el esquema y las demás esperen y vean todo ya
// creado, ejecutando el camino idempotente (Skipped).
func Migrate(ctx context.Context, db *sql.DB) (res Result, err error) {
	// El advisory lock de sesión vive en la conexión que lo adquiere; por eso
	// reservamos una *sql.Conn dedicada y ejecutamos lock + migración + unlock
	// sobre ESA misma conexión (no sobre el pool genérico, que podría repartir
	// las queries en conexiones distintas).
	conn, err := db.Conn(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reservando conexión para migración: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("cerrando conexión de migración: %w", cerr)
		}
	}()

	if _, lerr := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); lerr != nil {
		return Result{}, fmt.Errorf("adquiriendo advisory lock de migración: %w", lerr)
	}
	defer func() {
		// Liberamos el lock en la misma conexión. El unlock implícito ocurre al
		// cerrar la sesión de todas formas, pero lo soltamos explícito para
		// devolverlo al pool sin lock retenido.
		if _, uerr := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey); uerr != nil && err == nil {
			err = fmt.Errorf("liberando advisory lock de migración: %w", uerr)
		}
	}()

	return migrateLocked(ctx, conn)
}

// migrateLocked ejecuta la lógica de migración sobre una conexión que YA tiene
// el advisory lock adquirido. Todas las sub-operaciones usan esa misma conn.
func migrateLocked(ctx context.Context, conn *sql.Conn) (Result, error) {
	if err := ensureSchemaVersionTable(ctx, conn); err != nil {
		return Result{}, err
	}

	expectedHash := ComputeFilesHash()

	rec, err := readSchemaVersion(ctx, conn)
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

	if err := applyStructure(ctx, conn); err != nil {
		return Result{}, err
	}

	written, err := writeSchemaVersion(ctx, conn, SchemaVersion, expectedHash, "migración de estructura")
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
func applyStructure(ctx context.Context, db execer) error {
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
