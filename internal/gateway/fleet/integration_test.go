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

// TestIntegration_FleetSaveWorkerHealth verifica contra Postgres real (migración
// 0061) que el bloque del WORKER (Plan 051 · T4.3) conserva la distinción entre
// «no lo sé» y «cero»: sin dato las columnas quedan NULL y se leen como nil (no
// como 0), con dato viajan enteras —incluido un 0 MEDIDO— y el desglose de motivos
// hace ida y vuelta por el JSONB clave a clave, sin agregarse (INV-051.3).
func TestIntegration_FleetSaveWorkerHealth(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID, sessionID = "edge-worker-1", "sess-worker-1"

	repo := fleet.NewPostgresRepository(db)
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	// 1) Edge que no sabe nada del worker (mapa NIL incluido) ⇒ todo NULL/nil.
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "connected", BinaryVersion: "v0.12.0",
	}); err != nil {
		t.Fatalf("SaveHealth sin bloque de worker: %v", err)
	}
	s := getSession(t, repo, tenantID, edgeID, sessionID)
	if s.WorkerTaskset != "" || s.IntentP50Ms != nil || s.IntentOmittedByReason != nil ||
		s.StuckHeads != nil || s.StuckHeadPolls != nil ||
		s.FailedSealDispatch != nil || s.FailedSealBudget != nil {
		t.Fatalf("sin dato del worker todo debe quedar en DESCONOCIDO (NULL⇒nil): %+v", s)
	}

	// 2) Edge que sí lo sabe: el 0 de failed_seal_dispatch es un 0 MEDIDO.
	cero := int64(0)
	p50 := int64(1450)
	heads := int64(3)
	polls := int64(11)
	budget := int64(4)
	razones := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "connected", IntentCircuit: "open",
		WorkerTaskset: "solapada", IntentP50Ms: &p50,
		IntentOmittedByReason: razones, StuckHeads: &heads,
		StuckHeadPolls: &polls, FailedSealDispatch: &cero, FailedSealBudget: &budget,
	}); err != nil {
		t.Fatalf("SaveHealth con bloque de worker: %v", err)
	}
	s = getSession(t, repo, tenantID, edgeID, sessionID)
	if s.WorkerTaskset != "solapada" || s.IntentCircuit != "open" {
		t.Fatalf("taskset/breaker deben verse sin entrar en la máquina: %+v", s)
	}
	if s.IntentP50Ms == nil || *s.IntentP50Ms != 1450 {
		t.Fatalf("intent_p50_ms: %v, want 1450", s.IntentP50Ms)
	}
	if s.FailedSealDispatch == nil || *s.FailedSealDispatch != 0 {
		t.Fatalf("un 0 MEDIDO debe volver como puntero a 0, no como NULL: %v", s.FailedSealDispatch)
	}
	if s.FailedSealBudget == nil || *s.FailedSealBudget != 4 {
		t.Fatalf("failed_seal_budget: %v, want 4", s.FailedSealBudget)
	}
	if s.StuckHeads == nil || *s.StuckHeads != 3 || s.StuckHeadPolls == nil || *s.StuckHeadPolls != 11 {
		t.Fatalf("contadores de cabeza atascada: %+v", s)
	}
	for k, want := range map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1} {
		if got := s.IntentOmittedByReason[k]; got != want {
			t.Fatalf("motivo %q: got %d, want %d", k, got, want)
		}
	}
	if len(s.IntentOmittedByReason) != 3 {
		t.Fatalf("el desglose no puede ganar ni perder claves en el JSONB: %v", s.IntentOmittedByReason)
	}

	// 3) Parte RANCIO (el Edge manda las tres señales a su cero a propósito): el
	// valor anterior se BORRA, no se conserva. Conservar un "solapada" viejo sería
	// publicar una señal de salud inventada.
	if err := repo.SaveHealth(ctx, tenantID, edgeID, sessionID, fleet.HealthSnapshot{
		WhatsappState: "connected",
	}); err != nil {
		t.Fatalf("SaveHealth rancio: %v", err)
	}
	if s = getSession(t, repo, tenantID, edgeID, sessionID); s.WorkerTaskset != "" ||
		s.IntentP50Ms != nil || s.IntentOmittedByReason != nil {
		t.Fatalf("un parte rancio debe volver a DESCONOCIDO: %+v", s)
	}
}
