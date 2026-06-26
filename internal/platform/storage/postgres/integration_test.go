package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-shared/health"
)

// dsnEnv es la variable que habilita los tests de integración con BD real.
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta el test si no hay BD configurada.
// CI no levanta PostgreSQL: sin WAPP_TEST_DB_DSN (o si la BD no responde) se
// hace t.Skip en lugar de fallar.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	return db
}

func TestIntegration_MigrateIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	first, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("primera migración: %v", err)
	}
	if first.Version != migrations.SchemaVersion {
		t.Fatalf("versión: got %q, want %q", first.Version, migrations.SchemaVersion)
	}

	// La tabla tenants debe existir tras migrar.
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'tenants'
	)`).Scan(&exists); err != nil {
		t.Fatalf("comprobando tenants: %v", err)
	}
	if !exists {
		t.Fatal("la tabla public.tenants debería existir tras migrar")
	}

	// Re-aplicar no debe romper y debe reportar Skipped (idempotencia).
	second, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("segunda migración: %v", err)
	}
	if !second.Skipped {
		t.Fatal("la segunda migración debería marcarse como Skipped (idempotencia)")
	}
}

func TestIntegration_TenantRepositoryCRUD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migración: %v", err)
	}

	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("acme-%d", time.Now().UnixNano())

	created, err := repo.Create(ctx, slug, "ACME Inc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("el tenant creado debería tener id generado por la BD")
	}
	if created.Slug != slug || created.DisplayName != "ACME Inc" {
		t.Fatalf("tenant creado inesperado: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("created_at/updated_at deberían venir poblados")
	}

	found, err := repo.FindBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}
	if found.ID != created.ID {
		t.Fatalf("FindBySlug devolvió id distinto: %q != %q", found.ID, created.ID)
	}

	if _, err := repo.FindBySlug(ctx, "no-existe-"+slug); err == nil {
		t.Fatal("FindBySlug de slug inexistente debería devolver error")
	}
}

func TestIntegration_HealthCheck(t *testing.T) {
	db := openTestDB(t)

	hc := postgres.NewHealthCheck(db)
	if hc.Name() != "postgres" {
		t.Fatalf("Name: got %q, want postgres", hc.Name())
	}

	res := hc.Check(context.Background())
	if res.Status != health.StatusHealthy {
		t.Fatalf("health: got %q, want healthy (msg: %s)", res.Status, res.Message)
	}
}
