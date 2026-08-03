// Command migrate aplica las migraciones de esquema de la Plataforma Cloud y
// termina. Nada más: no abre listeners HTTP ni gRPC, no arranca el plano de
// control del Edge y no toca WhatsApp.
//
// POR QUÉ EXISTE (identity Plan 003 · design.md Ola 5 §5): hasta ahora el único
// binario era `server`, y su main() no lee argumentos, así que aplicar DDL sobre
// una base exigía levantar el servidor ENTERO contra ella. En la ola que emite
// los `DROP` de las tablas del IAM viejo eso significaba abrir media plataforma
// para hacer un cambio de esquema — efectos que no tienen nada que ver con
// migrar, en el momento de mayor riesgo del plan.
//
// Uso:
//
//	migrate            aplica las migraciones pendientes y sale
//	migrate -status    solo consulta el estado registrado (NO escribe)
//
// La configuración es la MISMA que la del servidor (config.Load, prefijo WAPP_):
// la cadena de conexión sale de las variables de entorno, nunca de un argumento,
// para que no haya dos formas de decir a qué base se apunta.
//
// ⚠️ Contra Neon: el runner serializa con pg_advisory_lock sobre una conexión
// dedicada, así que hay que apuntar al HOST DIRECTO, nunca al `-pooler` — un
// pooler en modo transacción puede repartir el lock y el unlock en sesiones
// distintas.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	stdlog "log"
	"os/signal"
	"syscall"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

func main() {
	statusOnly := flag.Bool("status", false, "consulta el estado del esquema sin aplicar nada")
	flag.Parse()

	if err := run(*statusOnly); err != nil {
		stdlog.Fatalf("migrate: %v", err)
	}
}

func run(statusOnly bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := postgres.Open(ctx, postgres.Config{DSN: cfg.DB.DSN()})
	if err != nil {
		return fmt.Errorf("base de datos no disponible: %w", err)
	}
	defer closeDB(db)

	// Se informa a qué base se ha conectado ANTES de tocarla. Un `DROP` bien
	// escrito sobre la base equivocada sigue siendo un `DROP` sobre la base
	// equivocada, y el DSN se arma de cinco variables distintas.
	target, err := currentDatabase(ctx, db)
	if err != nil {
		return err
	}
	stdlog.Printf("base de datos: %s (host %s:%d, usuario %s)", target, cfg.DB.Host, cfg.DB.Port, cfg.DB.User)

	if statusOnly {
		return reportStatus(ctx, db)
	}
	return apply(ctx, db)
}

// reportStatus imprime lo que la BD tiene registrado y si coincide con lo que
// este binario lleva embebido. NO escribe.
func reportStatus(ctx context.Context, db *sql.DB) error {
	res, err := migrations.Status(ctx, db)
	if err != nil {
		return fmt.Errorf("consultando el estado del esquema: %w", err)
	}
	stdlog.Printf("estado registrado: version=%s content_hash=%s execution_id=%s al_dia=%t",
		res.Version, res.ContentHash, res.ExecutionID, res.Skipped)
	stdlog.Printf("estado embebido:   version=%s content_hash=%s",
		migrations.SchemaVersion, migrations.ComputeFilesHash())
	if !res.Skipped {
		stdlog.Print("hay cambios pendientes: corre `migrate` sin -status para aplicarlos")
	}
	return nil
}

// apply corre el runner y reporta el desenlace. `skipped=true` significa que la
// BD ya estaba al día y no se tocó nada.
func apply(ctx context.Context, db *sql.DB) error {
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		return fmt.Errorf("aplicando migraciones: %w", err)
	}
	stdlog.Printf("migraciones aplicadas: version=%s content_hash=%s execution_id=%s skipped=%t",
		res.Version, res.ContentHash, res.ExecutionID, res.Skipped)
	return nil
}

// currentDatabase pregunta a la propia sesión en qué base está, que es el único
// dato que no se puede malinterpretar.
func currentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	var name string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return "", fmt.Errorf("consultando current_database(): %w", err)
	}
	return name, nil
}

func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		stdlog.Printf("error cerrando la base de datos: %v", err)
	}
}
