package lease_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"

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

// TestIntegration_RevokeSurvivesManagerRestart demuestra REQ-055.4: revoca
// con un primer *Manager, construye un SEGUNDO *Manager desde cero sobre el
// MISMO PostgresRepository/misma fila de public.leases (simulando
// `systemctl restart` de la Plataforma Cloud), y comprueba que la
// revocación sigue vigente al pedir IssueInitial en el proceso "reiniciado".
func TestIntegration_RevokeSurvivesManagerRestart(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID = "edge-int-restart"

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}

	repo1 := lease.NewPostgresRepository(db)
	mgr1, err := lease.NewManager(priv, repo1)
	if err != nil {
		t.Fatalf("NewManager (primer proceso): %v", err)
	}

	if _, err := mgr1.IssueInitial(ctx, tenantID, edgeID); err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}
	if _, err := mgr1.Revoke(ctx, tenantID, edgeID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	var revokedInDB bool
	if err := db.QueryRowContext(ctx,
		`SELECT revoked FROM public.leases WHERE tenant_id = $1 AND edge_id = $2`,
		tenantID, edgeID,
	).Scan(&revokedInDB); err != nil {
		t.Fatalf("SELECT revoked tras Revoke: %v", err)
	}
	if !revokedInDB {
		t.Fatal("revoked debería ser true en BD tras Revoke")
	}

	// Simula el reinicio de la nube: un *Manager y un *PostgresRepository
	// NUEVOS, misma clave y mismo *sql.DB, misma fila.
	repo2 := lease.NewPostgresRepository(db)
	mgr2, err := lease.NewManager(priv, repo2)
	if err != nil {
		t.Fatalf("NewManager (segundo proceso, tras 'reinicio'): %v", err)
	}

	lu, err := mgr2.IssueInitial(ctx, tenantID, edgeID)
	if err != nil {
		t.Fatalf("IssueInitial en el Manager 'reiniciado': %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("IssueInitial tras 'reiniciar' la nube debería seguir devolviendo revocado")
	}

	v := cllease.NewValidator(mgr2.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser false: el 'reinicio' de la nube no debe des-revocar")
	}

	if err := db.QueryRowContext(ctx,
		`SELECT revoked FROM public.leases WHERE tenant_id = $1 AND edge_id = $2`,
		tenantID, edgeID,
	).Scan(&revokedInDB); err != nil {
		t.Fatalf("SELECT revoked tras 'reinicio': %v", err)
	}
	if !revokedInDB {
		t.Fatal("revoked debería seguir true en BD tras el 'reinicio' + IssueInitial")
	}
}

// TestIntegration_UpsertNoResucitaLeaseRevocado ataca contra Postgres real el
// TERCER sitio del defecto (T2.1): el `ON CONFLICT DO UPDATE` de Upsert ya no
// lleva `revoked = false`. Ningún otro test lo cubre: con la guarda de
// wasRevoked puesta, el camino de producción nunca llega a Upsert sobre una
// fila revocada, así que reintroducir esa línea del SQL no rompería nada más.
// Se pone rojo si: vuelve `revoked = false` al SET del ON CONFLICT.
func TestIntegration_UpsertNoResucitaLeaseRevocado(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID = "edge-int-upsert-revocado"

	repo := lease.NewPostgresRepository(db)

	if err := repo.Upsert(ctx, lease.State{
		TenantID: tenantID, EdgeID: edgeID, Counter: 1, ExpiresAt: time.Now().Add(time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("Upsert inicial: %v", err)
	}
	if err := repo.MarkRevoked(ctx, tenantID, edgeID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}

	if err := repo.Upsert(ctx, lease.State{
		TenantID: tenantID, EdgeID: edgeID, Counter: 2, ExpiresAt: time.Now().Add(time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("Upsert tras revocar: %v", err)
	}

	st, found, err := repo.Get(ctx, tenantID, edgeID)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !st.Revoked {
		t.Fatal("el ON CONFLICT de Upsert NO debe des-revocar la fila (D-055.1 · T2.1)")
	}
	if st.Counter != 2 {
		t.Fatalf("Upsert sí debe actualizar el counter: got %d, want 2", st.Counter)
	}
}

// TestIntegration_TenantRevocationColumnPersists cubre T3.1 contra Postgres
// real: la columna public.tenants.revoked_at existe, es NULL por defecto en un
// tenant recién creado, y MarkTenantRevoked/RestoreTenant la mueven de verdad
// en BD (no solo en el repo en memoria).
func TestIntegration_TenantRevocationColumnPersists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	repo := lease.NewPostgresRepository(db)

	revoked, err := repo.TenantRevoked(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantRevoked (tenant recién creado): %v", err)
	}
	if revoked {
		t.Fatal("un tenant recién creado NO debería estar revocado (revoked_at nace NULL)")
	}

	var revokedAtNull bool
	if err := db.QueryRowContext(ctx,
		`SELECT revoked_at IS NULL FROM public.tenants WHERE id = $1`, tenantID,
	).Scan(&revokedAtNull); err != nil {
		t.Fatalf("SELECT revoked_at: %v", err)
	}
	if !revokedAtNull {
		t.Fatal("revoked_at debería ser NULL en un tenant recién creado")
	}

	if err := repo.MarkTenantRevoked(ctx, tenantID); err != nil {
		t.Fatalf("MarkTenantRevoked: %v", err)
	}
	revoked, err = repo.TenantRevoked(ctx, tenantID)
	if err != nil || !revoked {
		t.Fatalf("TenantRevoked tras MarkTenantRevoked: revoked=%v err=%v", revoked, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT revoked_at IS NULL FROM public.tenants WHERE id = $1`, tenantID,
	).Scan(&revokedAtNull); err != nil {
		t.Fatalf("SELECT revoked_at tras marcar: %v", err)
	}
	if revokedAtNull {
		t.Fatal("revoked_at debería estar poblado tras MarkTenantRevoked")
	}

	if err := repo.RestoreTenant(ctx, tenantID); err != nil {
		t.Fatalf("RestoreTenant: %v", err)
	}
	revoked, err = repo.TenantRevoked(ctx, tenantID)
	if err != nil || revoked {
		t.Fatalf("TenantRevoked tras RestoreTenant: revoked=%v err=%v (debería ser false)", revoked, err)
	}
}

// TestIntegration_TenantRevokedGatesNeverSeenEdge repite contra Postgres real
// el criterio de aceptación de T3.2 ya cubierto en unitario
// (TestIssueInitialTenantRevokedNeverSeenEdge): tenant revocado + edge_id que
// nunca apareció en public.leases ⇒ IssueInitial no emite vigente.
func TestIntegration_TenantRevokedGatesNeverSeenEdge(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID = "edge-int-tenant-revoked"

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	repo := lease.NewPostgresRepository(db)
	mgr, err := lease.NewManager(priv, repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if err := repo.MarkTenantRevoked(ctx, tenantID); err != nil {
		t.Fatalf("MarkTenantRevoked: %v", err)
	}

	lu, err := mgr.IssueInitial(ctx, tenantID, edgeID)
	if err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("IssueInitial para un edge nunca visto de un tenant revocado debería devolver Revoked=true")
	}

	// No debió persistirse fila de lease para este edge (nace revocada, sin Upsert).
	if _, found, gerr := repo.Get(ctx, tenantID, edgeID); gerr != nil {
		t.Fatalf("Get: %v", gerr)
	} else if found {
		t.Fatal("no debería haberse persistido una fila de lease para un edge que nace revocado por su tenant")
	}
}

// TestIntegration_Migrate0058Idempotent comprueba que la 0058 deja la columna
// y que es REALMENTE re-aplicable. Ojo con el modo de fallo del test obvio: el
// runner es hash-based, así que un segundo Migrate() sin más devuelve
// Skipped=true SIN EJECUTAR NADA -- eso no prueba idempotencia del DDL, solo
// que el hash no cambió (y eso ya lo cubre TestIntegration_MigrateIdempotent
// del paquete postgres). Aquí se fuerza el FULL-REPLAY alterando el hash
// registrado (misma técnica que TestIntegration_FullReplay_ConservaLosDatos),
// que es lo que hace que la 0058 se ejecute OTRA VEZ sobre una BD donde la
// columna YA existe.
// Se pone rojo si: la 0058 pierde el IF NOT EXISTS (el replay fallaría con
// "column revoked_at of relation tenants already exists").
func TestIntegration_Migrate0058Idempotent(t *testing.T) {
	db := openTestDB(t) // ya migró una vez
	ctx := context.Background()

	columnaExiste := func(t *testing.T) bool {
		t.Helper()
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'tenants' AND column_name = 'revoked_at'
		)`).Scan(&exists); err != nil {
			t.Fatalf("comprobando tenants.revoked_at: %v", err)
		}
		return exists
	}

	if !columnaExiste(t) {
		t.Fatal("la columna public.tenants.revoked_at debería existir tras migrar (0058)")
	}

	// Fuerza el replay: hash alterado, versión intacta.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.schema_version SET content_hash = 'hash-alterado-por-0058-test'
		WHERE id = (SELECT id FROM public.schema_version ORDER BY id DESC LIMIT 1)
	`); err != nil {
		t.Fatalf("alterar el hash registrado: %v", err)
	}

	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("full-replay con la 0058 ya aplicada (¿falta IF NOT EXISTS?): %v", err)
	}
	if res.Skipped {
		t.Fatal("con el hash alterado el runner DEBE reaplicar: el test no ha probado nada")
	}
	if res.Version != migrations.SchemaVersion {
		t.Fatalf("versión: got %q, want %q", res.Version, migrations.SchemaVersion)
	}
	if !columnaExiste(t) {
		t.Fatal("la columna revoked_at debería seguir existiendo tras el replay")
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
