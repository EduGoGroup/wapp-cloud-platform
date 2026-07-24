package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// setupDatabase abre la conexión a PostgreSQL y corre las migraciones de
// esquema al arrancar. Si la BD no está disponible, devuelve un error claro
// (fail-fast) en lugar de arrancar a medias.
func setupDatabase(ctx context.Context, cfg config.AppConfig, log sharedlogger.Logger) (*sql.DB, error) {
	db, err := postgres.Open(ctx, postgres.Config{DSN: cfg.DB.DSN()})
	if err != nil {
		return nil, fmt.Errorf("base de datos no disponible al arrancar: %w", err)
	}

	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("aplicando migraciones: %w", err), db.Close())
	}
	log.Info("migraciones aplicadas",
		"version", res.Version,
		"content_hash", res.ContentHash,
		"skipped", res.Skipped,
	)

	return db, nil
}

// closeDB cierra el pool de conexiones registrando cualquier error.
func closeDB(db *sql.DB, log sharedlogger.Logger) {
	if err := db.Close(); err != nil {
		log.Error("error cerrando la base de datos", "error", err)
	}
}
