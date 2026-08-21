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

// TestIntegration_FleetPerfilPersisteYRoleYaNoExiste verifica contra Postgres real
// dos cosas que solo se pueden afirmar con la BD delante:
//
//  1. Una sesión que nace por MarkOnline NO nombra la columna profile ⇒ cae al
//     DEFAULT de la 0063 y nace PASIVA (D-07). Es el cambio de comportamiento
//     respecto de la 0025 (que traía DEFAULT 'bot'), y está aquí para que nadie lo
//     revierta sin darse cuenta.
//  2. SetProfile persiste el perfil EN LA COLUMNA, ida y vuelta, y 🔴 la columna
//     `role` YA NO EXISTE (0064). Esto último no es adorno: mientras el esquema la
//     conserve, un `SELECT role` sigue funcionando y el retiro estaría a medias sin
//     que ningún test lo notara. Se afirma contra information_schema, no contra Go.
func TestIntegration_FleetPerfilPersisteYRoleYaNoExiste(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const edgeID, sessionID = "edge-profile-1", "sess-profile-1"

	repo := fleet.NewPostgresRepository(db)
	if err := repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get tras MarkOnline: found=%v err=%v", found, err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("una sesión recién registrada debe nacer pasiva (0063, D-07): got %q", s.Profile)
	}
	// Y contra la fila cruda (T3.1, criterio (a)): Get lee con
	// COALESCE(profile,'passive') — exactamente el valor esperado aquí — así que
	// por sí solo no distingue «la columna nació 'passive'» de «la columna quedó
	// rara y el COALESCE la tapó». El SELECT sin red confirma que el DEFAULT de la
	// 0063 escribió de verdad.
	var perfilNacimiento string
	if err := db.QueryRowContext(ctx, `
		SELECT profile FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID).Scan(&perfilNacimiento); err != nil {
		t.Fatalf("leer la fila cruda tras MarkOnline: %v", err)
	}
	if perfilNacimiento != string(fleet.ProfilePassive) {
		t.Fatalf("la fila en BD tras MarkOnline debe traer profile='passive': got %q", perfilNacimiento)
	}

	fasePerfilIdaYVuelta(ctx, t, db, repo, tenantID, edgeID, sessionID)
	faseRoleYaNoExiste(ctx, t, db)
}

// fasePerfilIdaYVuelta comprueba que SetProfile persiste EN LA COLUMNA los tres
// saltos active→passive→active (el tercero descarta un one-way), afirmando cada uno
// por las DOS vías: el Session que devuelve Get y la fila cruda. Va extraída y
// NOMBRADA —no como closure inline— porque gocyclo imputa los FuncLit anidados a la
// función madre y no bajaría de su umbral; es el mismo molde que faseWorkerDesconocido.
func fasePerfilIdaYVuelta(
	ctx context.Context, t *testing.T, db *sql.DB,
	repo *fleet.PostgresRepository, tenantID, edgeID, sessionID string,
) {
	t.Helper()
	for _, perfil := range []fleet.Profile{
		fleet.ProfileActive,
		fleet.ProfilePassive,
		fleet.ProfileActive, // y vuelve, para descartar un one-way
	} {
		foundP, err := repo.SetProfile(ctx, tenantID, sessionID, perfil)
		if err != nil || !foundP {
			t.Fatalf("SetProfile(%q): found=%v err=%v", perfil, foundP, err)
		}
		s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
		if err != nil || !found {
			t.Fatalf("Get tras SetProfile(%q): found=%v err=%v", perfil, found, err)
		}
		if s.Profile != perfil {
			t.Fatalf("SetProfile(%q) debe dejar profile=%q: got %q", perfil, perfil, s.Profile)
		}
		// Y contra la fila cruda: que el Session salga bien no prueba que la columna
		// se escribiera —lo probaría igual si el perfil se derivara al leer—.
		var perfilBD string
		if err := db.QueryRowContext(ctx, `
			SELECT profile FROM public.fleet_sessions
			WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		`, tenantID, edgeID, sessionID).Scan(&perfilBD); err != nil {
			t.Fatalf("leer la fila cruda tras SetProfile(%q): %v", perfil, err)
		}
		if perfilBD != string(perfil) {
			t.Fatalf("la fila en BD tras SetProfile(%q): profile=%q", perfil, perfilBD)
		}
	}
}

// faseRoleYaNoExiste afirma contra information_schema que la columna legada `role`
// se retiró de verdad (0064). No es adorno: mientras el esquema la conserve, un
// `SELECT role` sigue funcionando y el retiro estaría a medias sin que ningún test
// lo notara.
func faseRoleYaNoExiste(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var columnasRole int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'fleet_sessions' AND column_name = 'role'
	`).Scan(&columnasRole); err != nil {
		t.Fatalf("consultar information_schema por la columna role: %v", err)
	}
	if columnasRole != 0 {
		t.Fatal("fleet_sessions.role sigue existiendo: la 0064 no se aplicó")
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

	fila := filaWorker{
		repo: fleet.NewPostgresRepository(db), tenantID: tenantID,
		edgeID: edgeID, sessionID: sessionID,
	}
	if err := fila.repo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	// Las tres fases son una SECUENCIA sobre la MISMA fila: la 2 pisa lo que dejó la 1
	// y la 3 comprueba que se BORRA justo lo que escribió la 2. Por eso van en este
	// orden, SIN t.Parallel, y una fase rota corta aquí la corrida: seguir sería medir
	// contra un estado que nadie llegó a escribir.
	if !t.Run("1) el Edge no sabe nada del worker", func(t *testing.T) {
		faseWorkerDesconocido(ctx, t, fila)
	}) {
		t.Fatal("fase 1 rota: las fases 2 y 3 leen la fila que ella deja")
	}
	if !t.Run("2) el Edge sí lo sabe (0 MEDIDO incluido)", func(t *testing.T) {
		faseWorkerConocido(ctx, t, fila)
	}) {
		t.Fatal("fase 2 rota: la fase 3 comprueba que se borra justo lo que ella escribe")
	}
	t.Run("3) parte RANCIO: lo anterior se BORRA", func(t *testing.T) {
		faseWorkerRancio(ctx, t, fila)
	})
}

// filaWorker identifica la fila de flota que comparten las fases de
// TestIntegration_FleetSaveWorkerHealth: todas escriben y leen SIEMPRE la misma.
type filaWorker struct {
	repo      fleet.Repository
	tenantID  string
	edgeID    string
	sessionID string
}

// save persiste el snapshot y falla con el motivo de la fase si el repo protesta.
func (f filaWorker) save(ctx context.Context, t *testing.T, h fleet.HealthSnapshot, motivo string) {
	t.Helper()
	if err := f.repo.SaveHealth(ctx, f.tenantID, f.edgeID, f.sessionID, h); err != nil {
		t.Fatalf("SaveHealth %s: %v", motivo, err)
	}
}

// get relee la fila (falla el test si no está).
func (f filaWorker) get(ctx context.Context, t *testing.T) fleet.Session {
	t.Helper()
	return getSessionCtx(ctx, t, f.repo, f.tenantID, f.edgeID, f.sessionID)
}

// faseWorkerDesconocido: un Edge que no sabe nada del worker (mapa NIL incluido) deja
// todas las columnas en NULL, que se leen como nil y NUNCA como 0.
func faseWorkerDesconocido(ctx context.Context, t *testing.T, f filaWorker) {
	t.Helper()
	f.save(ctx, t, fleet.HealthSnapshot{
		WhatsappState: "connected", BinaryVersion: "v0.12.0",
	}, "sin bloque de worker")
	s := f.get(ctx, t)
	if s.WorkerTaskset != "" || s.IntentP50Ms != nil || s.IntentOmittedByReason != nil ||
		s.StuckHeads != nil || s.StuckHeadPolls != nil ||
		s.FailedSealDispatch != nil || s.FailedSealBudget != nil {
		t.Fatalf("sin dato del worker todo debe quedar en DESCONOCIDO (NULL⇒nil): %+v", s)
	}
}

// faseWorkerConocido: el Edge sí lo sabe, y el 0 de failed_seal_dispatch es un 0 MEDIDO.
func faseWorkerConocido(ctx context.Context, t *testing.T, f filaWorker) {
	t.Helper()
	cero := int64(0)
	p50 := int64(1450)
	heads := int64(3)
	polls := int64(11)
	budget := int64(4)
	razones := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	f.save(ctx, t, fleet.HealthSnapshot{
		WhatsappState: "connected", IntentCircuit: "open",
		WorkerTaskset: "solapada", IntentP50Ms: &p50,
		IntentOmittedByReason: razones, StuckHeads: &heads,
		StuckHeadPolls: &polls, FailedSealDispatch: &cero, FailedSealBudget: &budget,
	}, "con bloque de worker")
	s := f.get(ctx, t)
	if s.WorkerTaskset != "solapada" || s.IntentCircuit != "open" {
		t.Fatalf("taskset/breaker deben verse sin entrar en la máquina: %+v", s)
	}
	requirePunteroInt64(t, s.IntentP50Ms, 1450, "intent_p50_ms: %v, want 1450", s.IntentP50Ms)
	requirePunteroInt64(t, s.FailedSealDispatch, 0,
		"un 0 MEDIDO debe volver como puntero a 0, no como NULL: %v", s.FailedSealDispatch)
	requirePunteroInt64(t, s.FailedSealBudget, 4,
		"failed_seal_budget: %v, want 4", s.FailedSealBudget)
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
}

// faseWorkerRancio: parte RANCIO (el Edge manda las tres señales a su cero a
// propósito). El valor anterior se BORRA, no se conserva: conservar un "solapada"
// viejo sería publicar una señal de salud inventada.
func faseWorkerRancio(ctx context.Context, t *testing.T, f filaWorker) {
	t.Helper()
	f.save(ctx, t, fleet.HealthSnapshot{WhatsappState: "connected"}, "rancio")
	if s := f.get(ctx, t); s.WorkerTaskset != "" ||
		s.IntentP50Ms != nil || s.IntentOmittedByReason != nil {
		t.Fatalf("un parte rancio debe volver a DESCONOCIDO: %+v", s)
	}
}

// TestIntegration_SaludoPendienteYCentinela verifica contra Postgres real el SQL que
// gobierna el aviso de sesión pasiva (Plan 046 · T3.2 (b), migración 0066). Está aquí
// y no en gateway/grpc porque allí PendingGreeting/MarkGreeted se ejercitan con un
// DOBLE (fleetSaludos): ese doble prueba la lógica del emisor, pero no ejecuta ni una
// línea del SQL, así que el filtro de perfil y el centinela no los custodiaba nadie.
//
// Cubre las cuatro propiedades que solo la BD puede afirmar:
//
//  1. una sesión PASIVA con número y sin marcar está pendiente;
//  2. 🔴 una sesión ACTIVA con número y sin marcar NO lo está —el aviso le mentiría en
//     sus tres frases—;
//  3. sin self_pn no hay a quién avisar;
//  4. el centinela `WHERE greeted_at IS NULL` de MarkGreeted hace que la SEGUNDA
//     llamada devuelva marked=false: ahí vive la idempotencia del criterio (c), en la
//     BD y no en memoria, que es lo que impide un mensaje cada 30 s.
func TestIntegration_SaludoPendienteYCentinela(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := fleet.NewPostgresRepository(db)

	fasePendienteSoloSiPasivaYConNumero(ctx, t, repo, tenantID)
	faseCentinelaDeMarkGreeted(ctx, t, repo, tenantID)
}

// fasePendienteSoloSiPasivaYConNumero cubre las propiedades 1, 2 y 3: quién sale
// pendiente y quién no. Va extraída y NOMBRADA por gocyclo (los FuncLit anidados se
// imputan a la función madre), igual que faseWorkerDesconocido.
func fasePendienteSoloSiPasivaYConNumero(
	ctx context.Context, t *testing.T, repo *fleet.PostgresRepository, tenantID string,
) {
	t.Helper()
	const edgeID = "edge-saludo"
	casos := []struct {
		nombre    string
		sessionID string
		selfPn    string
		perfil    fleet.Profile
		pendiente bool
	}{
		{"pasiva con número: hay que avisarla", "s-pasiva", "34600111222", fleet.ProfilePassive, true},
		{"ACTIVA con número: el aviso le mentiría", "s-activa", "34600333444", fleet.ProfileActive, false},
		{"pasiva sin número: no hay a quién avisar", "s-sin-pn", "", fleet.ProfilePassive, false},
	}
	for _, c := range casos {
		if err := repo.MarkOnline(ctx, tenantID, edgeID, c.sessionID); err != nil {
			t.Fatalf("%s: MarkOnline: %v", c.nombre, err)
		}
		if c.selfPn != "" {
			if err := repo.SetSelfPn(ctx, tenantID, edgeID, c.sessionID, c.selfPn); err != nil {
				t.Fatalf("%s: SetSelfPn: %v", c.nombre, err)
			}
		}
		// El perfil se pone EXPLÍCITO aunque el default ya sea 'passive': un caso que
		// pasa por omisión deja de probar lo que dice el día que el default cambie.
		if _, err := repo.SetProfile(ctx, tenantID, c.sessionID, c.perfil); err != nil {
			t.Fatalf("%s: SetProfile: %v", c.nombre, err)
		}

		to, pending, err := repo.PendingGreeting(ctx, tenantID, edgeID, c.sessionID)
		if err != nil {
			t.Fatalf("%s: PendingGreeting: %v", c.nombre, err)
		}
		if pending != c.pendiente {
			t.Fatalf("%s: pending=%v, quiero %v", c.nombre, pending, c.pendiente)
		}
		if c.pendiente && to != c.selfPn {
			t.Fatalf("%s: el número a avisar es %q, quiero %q", c.nombre, to, c.selfPn)
		}
	}
}

// faseCentinelaDeMarkGreeted cubre la propiedad 4: la SEGUNDA llamada no marca. Es la
// idempotencia del criterio (c) de T3.2 afirmada donde de verdad vive —el
// `WHERE greeted_at IS NULL` del UPDATE—, no en la memoria del emisor.
func faseCentinelaDeMarkGreeted(
	ctx context.Context, t *testing.T, repo *fleet.PostgresRepository, tenantID string,
) {
	t.Helper()
	const edgeID, sessionID = "edge-saludo", "s-pasiva"

	marked, err := repo.MarkGreeted(ctx, tenantID, edgeID, sessionID)
	if err != nil || !marked {
		t.Fatalf("la PRIMERA llamada debe marcar: marked=%v err=%v", marked, err)
	}
	// Ya marcada ⇒ deja de estar pendiente. Sin esto, el saludo se repetiría en cada
	// latido aunque el UPDATE hubiera funcionado.
	if _, pending, err := repo.PendingGreeting(ctx, tenantID, edgeID, sessionID); err != nil || pending {
		t.Fatalf("tras marcar no puede seguir pendiente: pending=%v err=%v", pending, err)
	}
	marked, err = repo.MarkGreeted(ctx, tenantID, edgeID, sessionID)
	if err != nil {
		t.Fatalf("la segunda MarkGreeted no debe dar error: %v", err)
	}
	if marked {
		t.Fatal("la SEGUNDA llamada devolvió marked=true: el centinela WHERE greeted_at IS NULL no está en el UPDATE, y el dueño recibiría un mensaje por latido")
	}
}
