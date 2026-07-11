package intentcfg_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
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

// TestIntegration_IntentConfig_UpsertGet valida el roundtrip contra la tabla real
// intent_configs (migración 0033): ErrNotFound sin fila, Upsert+Get devuelve el
// blob y la version, y un segundo Upsert reemplaza. tenant_id es TEXT (no exige un
// tenant real), coherente con el aislamiento por tenant_id.
func TestIntegration_IntentConfig_UpsertGet(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	s := intentcfg.NewPostgresStore(db)
	tid := "tenant-intentcfg-integ"

	// Limpieza previa por si una corrida anterior dejó la fila (tabla persistente).
	if _, err := db.ExecContext(ctx, `DELETE FROM public.intent_configs WHERE tenant_id=$1`, tid); err != nil {
		t.Fatalf("limpieza previa: %v", err)
	}

	if _, err := s.Get(ctx, tid); !errors.Is(err, intentcfg.ErrNotFound) {
		t.Fatalf("Get sin fila: err=%v, quería ErrNotFound", err)
	}

	blob := []byte(`{"version":"v1","intents":[{"name":"x"}]}`)
	if err := s.Upsert(ctx, tid, "hash-1", blob); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(ctx, tid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != "hash-1" || !bytes.Equal(got.Blob, blob) || got.UpdatedAt.IsZero() {
		t.Fatalf("config recuperada inesperada: %+v", got)
	}

	newBlob := []byte(`{"version":"v2","intents":[{"name":"y"}]}`)
	if err := s.Upsert(ctx, tid, "hash-2", newBlob); err != nil {
		t.Fatalf("Upsert reemplazo: %v", err)
	}
	got2, err := s.Get(ctx, tid)
	if err != nil {
		t.Fatalf("Get tras reemplazo: %v", err)
	}
	if got2.Version != "hash-2" || !bytes.Equal(got2.Blob, newBlob) {
		t.Fatalf("Upsert no reemplazó: %+v", got2)
	}

	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM public.intent_configs WHERE tenant_id=$1`, tid); err != nil {
			t.Logf("limpieza final: %v", err)
		}
	})
}
