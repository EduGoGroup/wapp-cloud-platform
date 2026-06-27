package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en store/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

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
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-resolver-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Tenant Resolver Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// seedFleetSession siembra una fila online en fleet_sessions (mismo patrón que
// fleet.PostgresRepository.MarkOnline).
func seedFleetSession(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE SET state = 'online'
	`, tenantID, edgeID, sessionID)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions: %v", err)
	}
}

func TestIntegration_PostgresTenantResolver_Resuelve(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	seedFleetSession(t, db, tenantID, "edge-A", sessionID)

	res := runtime.NewPostgresTenantResolver(db)
	got, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
}

func TestIntegration_PostgresTenantResolver_CeroFilas(t *testing.T) {
	db := openTestDB(t)
	res := runtime.NewPostgresTenantResolver(db)
	_, err := res.ResolveTenant(context.Background(), fmt.Sprintf("inexistente-%d", time.Now().UnixNano()))
	if !errors.Is(err, runtime.ErrTenantNotResolved) {
		t.Fatalf("0 filas debería dar ErrTenantNotResolved, dio: %v", err)
	}
}

// Un mismo session_id bajo dos edge_id del MISMO tenant resuelve sin ambigüedad
// (DISTINCT tenant_id colapsa).
func TestIntegration_PostgresTenantResolver_MismoTenantVariosEdges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	seedFleetSession(t, db, tenantID, "edge-A", sessionID)
	seedFleetSession(t, db, tenantID, "edge-B", sessionID)

	res := runtime.NewPostgresTenantResolver(db)
	got, err := res.ResolveTenant(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("tenant resuelto: got %q, want %q", got, tenantID)
	}
}
