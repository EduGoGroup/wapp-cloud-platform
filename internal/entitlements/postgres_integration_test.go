package entitlements_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
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

func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-ent-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Entitlements Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// TestIntegration_Entitlements_Resolucion ejercita las 4 reglas de resolución de
// ADR-0022 contra la BD real (migración 0032): plan NULL⇒basic, plan pro, override
// que activa, override que desactiva (gana sobre el plan). Cada caso usa un tenant
// distinto para no cruzarse con la caché por (tenant, feature).
func TestIntegration_Entitlements_Resolucion(t *testing.T) {
	db := openTestDB(t)
	res := entitlements.NewPostgres(db)

	t.Run("plan NULL ⇒ basic (sin llm_intent)", func(t *testing.T) {
		tid := seedTenant(t, db)
		assertHas(t, res, tid, false)
	})

	t.Run("plan pro ⇒ con llm_intent", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='pro' WHERE id=$1`, tid)
		assertHas(t, res, tid, true)
	})

	t.Run("override enabled=true gana sobre plan basic", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,true)`,
			tid, entitlements.FeatureLLMIntent)
		assertHas(t, res, tid, true)
	})

	t.Run("override enabled=false gana sobre plan pro", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='pro' WHERE id=$1`, tid)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,false)`,
			tid, entitlements.FeatureLLMIntent)
		assertHas(t, res, tid, false)
	})
}

func assertHas(t *testing.T, res entitlements.Resolver, tenantID string, want bool) {
	t.Helper()
	got, err := res.Has(context.Background(), tenantID, entitlements.FeatureLLMIntent)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if got != want {
		t.Fatalf("Has(%s, llm_intent) = %v, quería %v", tenantID, got, want)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
