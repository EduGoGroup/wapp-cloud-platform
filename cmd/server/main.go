// Command server es el binario único de la Plataforma Cloud (monolito modular).
//
// Carga la configuración, construye el logger, abre la conexión a PostgreSQL,
// corre las migraciones de esquema al arrancar y levanta un servidor HTTP con
// el endpoint de health (que incluye el check de BD) y graceful shutdown. El
// servidor gRPC del Gateway CloudLink entra en T2.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		stdlog.Fatalf("fallo fatal del arranque: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := setupDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeDB(db, log)

	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(db))
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.HealthHandler(checker))

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("servidor HTTP iniciado", "addr", cfg.HTTPAddr)
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("señal de parada recibida, cerrando")
	case serveErr := <-errCh:
		log.Error("fallo del servidor HTTP", "error", serveErr)
		return serveErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("error durante el shutdown", "error", err)
		return err
	}

	log.Info("servidor detenido limpiamente")
	return nil
}

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
