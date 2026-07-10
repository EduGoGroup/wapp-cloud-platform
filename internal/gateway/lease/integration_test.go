package lease_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en T1/T3).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7): la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7)", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
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

// seedTenant crea un tenant con slug único y devuelve su UUID.
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Lease/Fleet Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

func TestIntegration_LeasePersistIssueRenewRevoke(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID = "edge-int-1"

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	repo := lease.NewPostgresRepository(db)
	mgr, err := lease.NewManager(priv, repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := mgr.IssueInitial(ctx, tenantID, edgeID); err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}
	st, found, err := repo.Get(ctx, tenantID, edgeID)
	if err != nil || !found {
		t.Fatalf("Get inicial: found=%v err=%v", found, err)
	}
	if st.Counter != 1 || st.Revoked {
		t.Fatalf("estado inicial inesperado: %+v", st)
	}

	if _, err := mgr.Renew(ctx, tenantID, edgeID, 41); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	st, _, err = repo.Get(ctx, tenantID, edgeID)
	if err != nil {
		t.Fatalf("Get renovado: %v", err)
	}
	if st.Counter != 42 {
		t.Fatalf("counter renovado: got %d, want 42", st.Counter)
	}

	if _, err := mgr.Revoke(ctx, tenantID, edgeID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	st, _, err = repo.Get(ctx, tenantID, edgeID)
	if err != nil {
		t.Fatalf("Get revocado: %v", err)
	}
	if !st.Revoked {
		t.Fatal("el lease debería quedar revocado en BD")
	}
	// La revocación conserva el counter (no lo baja a 0).
	if st.Counter != 42 {
		t.Fatalf("counter tras revoke: got %d, want 42 (conservado)", st.Counter)
	}
}

func TestIntegration_FleetPersistOnlineOffline(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const (
		edgeID    = "edge-int-2"
		sessionID = "sess-int-2"
	)

	repo := fleet.NewPostgresRepository(db)
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get online: found=%v err=%v", found, err)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("estado: got %q, want online", s.State)
	}

	if err := repo.MarkOffline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	s, _, err = repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil {
		t.Fatalf("Get offline: %v", err)
	}
	if s.State != fleet.StateOffline {
		t.Fatalf("estado: got %q, want offline", s.State)
	}

	list, err := repo.List(ctx, tenantID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List: got %d, want 1", len(list))
	}
}

func TestIntegration_Migrate0003Idempotent(t *testing.T) {
	db := openTestDB(t) // ya migró una vez
	ctx := context.Background()

	for _, table := range []string{"leases", "fleet_sessions"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, table).Scan(&exists); err != nil {
			t.Fatalf("comprobando %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("la tabla public.%s debería existir tras migrar", table)
		}
	}

	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("re-migración: %v", err)
	}
	if !res.Skipped {
		t.Fatal("la re-migración debería marcarse Skipped (idempotencia con 0003)")
	}
	if res.Version != migrations.SchemaVersion {
		t.Fatalf("versión: got %q, want %q", res.Version, migrations.SchemaVersion)
	}
}
