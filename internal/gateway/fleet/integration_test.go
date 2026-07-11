package fleet_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (mismo patrón que lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
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
	ten, err := repo.Create(context.Background(), slug, "Fleet Health Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// TestIntegration_FleetSaveHealth verifica contra Postgres real (migración 0035) la
// persistencia del snapshot de salud y las transiciones de degraded_since (Plan 031
// · T3): SaveHealth NO toca el link state; degraded_since se fija al entrar y se
// limpia al salir; y todo es visible vía List (lo que sirve GET /api/v1/sessions).
func TestIntegration_FleetSaveHealth(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID, sessionID = "edge-health-1", "sess-health-1"

	repo := fleet.NewPostgresRepository(db)
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	// Entra en degradado (socket muerto + motivo).
	want := fleet.HealthSnapshot{
		WhatsappState: "dead", DegradedReason: "dek_load_timeout",
		LastEventAgeS: 1860, DekLoadDurationMs: 10000, IntentCircuit: "open",
		OutboxDepth: 3, BinaryVersion: "v0.9.0", UptimeS: 7200,
	}
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, want); err != nil {
		t.Fatalf("SaveHealth degradado: %v", err)
	}
	s := getSession(t, repo, tenantID, edgeID, sessionID)
	if s.State != fleet.StateOnline {
		t.Fatalf("SaveHealth no debe tocar el link state: got %q", s.State)
	}
	if !snapshotMatches(s, want) || s.DegradedSince.IsZero() || s.LastHealthAt.IsZero() {
		t.Fatalf("snapshot persistido incompleto: %+v", s)
	}
	since := s.DegradedSince

	// Sigue degradado ⇒ degraded_since NO se mueve.
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "degraded", DegradedReason: "ws_dial_timeout",
	}); err != nil {
		t.Fatalf("SaveHealth sigue degradado: %v", err)
	}
	if s = getSession(t, repo, tenantID, edgeID, sessionID); !s.DegradedSince.Equal(since) {
		t.Fatalf("degraded_since no debe moverse si sigue degradado: %v != %v", s.DegradedSince, since)
	}

	// Sale de degradado ⇒ degraded_since se limpia.
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "connected",
	}); err != nil {
		t.Fatalf("SaveHealth sano: %v", err)
	}
	list, err := repo.List(ctx, tenantID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List: got %d filas, want 1", len(list))
	}
	if !list[0].DegradedSince.IsZero() || list[0].DegradedReason != "" || list[0].WhatsappState != "connected" {
		t.Fatalf("al salir de degradado: degraded_since/reason limpios y state connected: %+v", list[0])
	}
}
