package diagnostics_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

func seedTenant(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-diag-%d", time.Now().UnixNano())
	ten, err := repo.Create(ctx, slug, "Diagnostics Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// TestIntegration_Diagnostics_Consent verifica el consentimiento default ON (opt-out)
// contra Postgres real (migración 0036): sin fila ⇒ consentido; enabled=FALSE ⇒ off.
func TestIntegration_Diagnostics_Consent(t *testing.T) {
	db := openTestDB(t)
	store := diagnostics.NewPostgres(db)
	ctx := context.Background()
	tid := seedTenant(ctx, t, db)

	ok, err := store.ConsentEnabled(ctx, tid)
	if err != nil || !ok {
		t.Fatalf("default debe ser ON: ok=%v err=%v", ok, err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.tenant_diagnostics_consent (tenant_id, enabled) VALUES ($1, false)`, tid); err != nil {
		t.Fatalf("opt-out: %v", err)
	}
	off, err := store.ConsentEnabled(ctx, tid)
	if err != nil {
		t.Fatalf("ConsentEnabled tras opt-out: %v", err)
	}
	if off {
		t.Fatal("tras opt-out debe ser OFF")
	}
}

// TestIntegration_Diagnostics_Ciclo ejercita solicitud ⇒ bundle ⇒ descarga, con
// correlación por command_id + (tenant, sesión) y aislamiento por tenant (INV-8).
func TestIntegration_Diagnostics_Ciclo(t *testing.T) {
	db := openTestDB(t)
	store := diagnostics.NewPostgres(db)
	ctx := context.Background()
	tid := seedTenant(ctx, t, db)
	other := seedTenant(ctx, t, db)

	cmd, err := diagnostics.NewCommandID()
	if err != nil {
		t.Fatalf("NewCommandID: %v", err)
	}
	if err := store.CreateRequest(ctx, tid, "sess-1", cmd, "user-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := store.GetBundle(ctx, tid, cmd); !errors.Is(err, diagnostics.ErrPending) {
		t.Fatalf("esperaba ErrPending, got %v", err)
	}
	// Bundle de otra sesión no correlaciona (mismatch).
	mism, err := store.SaveBundle(ctx, tid, "sess-otra", cmd, diagnostics.Bundle{})
	if err != nil {
		t.Fatalf("SaveBundle mismatch: %v", err)
	}
	if mism {
		t.Fatal("no debió correlacionar una sesión distinta")
	}
	// Bundle correcto ⇒ ready.
	found, err := store.SaveBundle(ctx, tid, "sess-1", cmd, diagnostics.Bundle{LogTail: "log", GoroutineDump: "dump", SubsystemsJSON: `{"x":1}`})
	if err != nil || !found {
		t.Fatalf("SaveBundle found=%v err=%v", found, err)
	}
	rec, err := store.GetBundle(ctx, tid, cmd)
	if err != nil || rec.Bundle.LogTail != "log" {
		t.Fatalf("descarga ready: rec=%+v err=%v", rec, err)
	}
	// Otro tenant no lo ve (INV-8).
	if _, err := store.GetBundle(ctx, other, cmd); !errors.Is(err, diagnostics.ErrNotFound) {
		t.Fatalf("cross-tenant esperaba ErrNotFound, got %v", err)
	}
}

// TestIntegration_Diagnostics_Expiracion verifica el 410 + borrado perezoso de una
// solicitud vencida.
func TestIntegration_Diagnostics_Expiracion(t *testing.T) {
	db := openTestDB(t)
	store := diagnostics.NewPostgres(db)
	ctx := context.Background()
	tid := seedTenant(ctx, t, db)

	cmd, err := diagnostics.NewCommandID()
	if err != nil {
		t.Fatalf("NewCommandID: %v", err)
	}
	// Ya vencida al crearse.
	if err := store.CreateRequest(ctx, tid, "sess-1", cmd, "user-1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := store.GetBundle(ctx, tid, cmd); !errors.Is(err, diagnostics.ErrExpired) {
		t.Fatalf("esperaba ErrExpired, got %v", err)
	}
	if _, err := store.GetBundle(ctx, tid, cmd); !errors.Is(err, diagnostics.ErrNotFound) {
		t.Fatalf("tras expirar+borrar esperaba ErrNotFound, got %v", err)
	}
}
