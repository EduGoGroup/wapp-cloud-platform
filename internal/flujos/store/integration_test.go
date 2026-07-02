package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada.
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

// seedTenant crea un tenant con slug único y devuelve su UUID (FK de flow_*).
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-flows-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Flows Store Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

func TestIntegration_FlowStatePersistAndUpsert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := store.NewPostgresRepository(db)

	// contact_id es UUID (contacts.contact_id): la key opera por el id opaco ya
	// resuelto (Plan 010 T1), no por el JID crudo.
	key := store.Key{TenantID: tenantID, SessionID: "sess-1", ContactID: "11111111-1111-1111-1111-111111111111"}

	if exists, err := repo.Exists(ctx, key); err != nil || exists {
		t.Fatalf("Exists inicial: exists=%v err=%v", exists, err)
	}
	if _, found, err := repo.Load(ctx, key); err != nil || found {
		t.Fatalf("Load inicial: found=%v err=%v", found, err)
	}

	st := model.Conversation{
		TenantID:        tenantID,
		SessionID:       "sess-1",
		ContactID:       "11111111-1111-1111-1111-111111111111",
		FlowID:          "menu-soporte",
		FlowVersion:     1,
		CurrentNode:     "root",
		Vars:            map[string]any{"reprompt": float64(1), "nombre": "Ana"},
		LastWaMessageID: "wamid.AAA",
	}
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, found, err := repo.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load tras Save: found=%v err=%v", found, err)
	}
	// vars JSONB ida y vuelta.
	if got.Vars["reprompt"] != float64(1) || got.Vars["nombre"] != "Ana" {
		t.Fatalf("vars JSONB no coinciden: %+v", got.Vars)
	}
	if got.CurrentNode != "root" || got.LastWaMessageID != "wamid.AAA" {
		t.Fatalf("estado leído inesperado: %+v", got)
	}

	// UPSERT: misma clave avanza de nodo, reemplaza vars y cambia la marca de idempotencia.
	st.CurrentNode = "fin"
	st.Vars = map[string]any{"reprompt": float64(0)}
	st.LastWaMessageID = "wamid.BBB"
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save upsert: %v", err)
	}
	up, _, err := repo.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load tras upsert: %v", err)
	}
	assertUpserted(t, up)
}

// assertUpserted comprueba el estado tras el UPSERT (extraído para acotar la
// complejidad ciclomática del test).
func assertUpserted(t *testing.T, up model.Conversation) {
	t.Helper()
	if up.CurrentNode != "fin" || up.LastWaMessageID != "wamid.BBB" {
		t.Fatalf("upsert no aplicó: %+v", up)
	}
	if up.Vars["nombre"] != nil {
		t.Fatalf("upsert no reemplazó vars: %+v", up.Vars)
	}
}

// TestIntegration_FlowStateTerminalSentinelRoundtrip es el test de regresión del
// bug que el e2e real destapó: cuando una conversación llega a un nodo terminal,
// CurrentNode = model.NodeTerminal se persiste en la columna TEXT
// flow_state.current_node. El centinela viejo llevaba un byte nulo 0x00 y
// PostgreSQL lo rechazaba ("invalid byte sequence for encoding UTF8", SQLSTATE
// 22021); el MemoryRepository (mapas Go) lo toleraba y enmascaró el fallo. Este
// test HABRÍA FALLADO con el centinela viejo y PASA con el actual (imprimible).
func TestIntegration_FlowStateTerminalSentinelRoundtrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := store.NewPostgresRepository(db)

	key := store.Key{TenantID: tenantID, SessionID: "sess-terminal", ContactID: "22222222-2222-2222-2222-222222222222"}

	st := model.Conversation{
		TenantID:    tenantID,
		SessionID:   key.SessionID,
		ContactID:   key.ContactID,
		FlowID:      "menu-soporte",
		FlowVersion: 1,
		CurrentNode: model.NodeTerminal, // <- el centinela que rompía PostgreSQL
		Vars:        map[string]any{"reprompt": float64(0)},
	}
	// Save NO debe fallar por encoding: el centinela tiene que ser TEXT-safe.
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save con CurrentNode=NodeTerminal falló (¿centinela con byte nulo?): %v", err)
	}

	got, found, err := repo.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load tras Save: found=%v err=%v", found, err)
	}
	if got.CurrentNode != model.NodeTerminal {
		t.Fatalf("centinela terminal no hizo round-trip: got %q, want %q", got.CurrentNode, model.NodeTerminal)
	}
	if !got.Finished() {
		t.Fatalf("estado leído debería estar Finished(): %+v", got)
	}
}

func TestIntegration_FlowDefinitionVersioning(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := store.NewPostgresRepository(db)

	if _, err := repo.LatestDefinition(ctx, tenantID, "menu"); err == nil {
		t.Fatal("LatestDefinition sin definiciones debería fallar (ErrDefinitionNotFound)")
	}

	v1, err := repo.InsertDefinition(ctx, tenantID, sampleFlow("menu"))
	if err != nil {
		t.Fatalf("InsertDefinition v1: %v", err)
	}
	v2, err := repo.InsertDefinition(ctx, tenantID, sampleFlow("menu"))
	if err != nil {
		t.Fatalf("InsertDefinition v2: %v", err)
	}
	if v1 != 1 || v2 != 2 {
		t.Fatalf("versiones asignadas: got v1=%d v2=%d, want 1 y 2", v1, v2)
	}

	latest, err := repo.LatestDefinition(ctx, tenantID, "menu")
	if err != nil {
		t.Fatalf("LatestDefinition: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("LatestDefinition versión: got %d, want 2", latest.Version)
	}
	if latest.FlowID != "menu" || latest.Initial != "root" || len(latest.Nodes) != 2 {
		t.Fatalf("definición leída inesperada: %+v", latest)
	}
}

func TestIntegration_Migrate0004Idempotent(t *testing.T) {
	db := openTestDB(t) // ya migró una vez
	ctx := context.Background()

	for _, table := range []string{"flow_definitions", "flow_state", "contacts"} {
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
		t.Fatal("la re-migración debería marcarse Skipped (idempotencia con 0004)")
	}
	if res.Version != migrations.SchemaVersion {
		t.Fatalf("versión: got %q, want %q", res.Version, migrations.SchemaVersion)
	}
	if res.Version != "0.7.0" {
		t.Fatalf("SchemaVersion: got %q, want 0.7.0", res.Version)
	}
}

// TestIntegration_SurveyResultsPersistAndAggregate valida el TRAMO T2 del Plan
// 014: InsertResults escribe filas EN CLARO en survey_results (answer_code sin
// cifrar, design.md §10.D) y un GROUP BY agrega correctamente por opción. Es la
// prueba de que la tabla sirve su propósito de negocio (agregación de encuesta).
func TestIntegration_SurveyResultsPersistAndAggregate(t *testing.T) {
	db := openTestDB(t) // migra incl. 0008_survey_results
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)

	// tenant_id/contact_id como TEXT opaco (la tabla no tiene FK; el runtime pasa
	// el contact_id ya resuelto). Aislamos por un flow_id único de este test.
	flowID := fmt.Sprintf("encuesta-%d", time.Now().UnixNano())
	tenantID := "tenant-survey-t2"
	rows := []store.SurveyResult{
		{TenantID: tenantID, ContactID: "c-1", FlowID: flowID, FlowVersion: 1, QuestionID: "q1", AnswerCode: "si"},
		{TenantID: tenantID, ContactID: "c-2", FlowID: flowID, FlowVersion: 1, QuestionID: "q1", AnswerCode: "si"},
		{TenantID: tenantID, ContactID: "c-3", FlowID: flowID, FlowVersion: 1, QuestionID: "q1", AnswerCode: "no"},
	}
	if err := repo.InsertResults(ctx, rows); err != nil {
		t.Fatalf("InsertResults: %v", err)
	}

	// len==0 es no-op (no debe escribir ni fallar).
	if err := repo.InsertResults(ctx, nil); err != nil {
		t.Fatalf("InsertResults nil (no-op): %v", err)
	}

	got := aggregateAnswers(t, db, flowID)
	if got["si"] != 2 || got["no"] != 1 {
		t.Fatalf("agregación por answer_code inesperada: %+v", got)
	}
}

// aggregateAnswers ejecuta el GROUP BY de negocio (respuestas por opción) para el
// flow dado y devuelve answer_code → conteo.
func aggregateAnswers(t *testing.T, db *sql.DB, flowID string) map[string]int {
	t.Helper()
	agg, err := db.QueryContext(context.Background(), `
		SELECT answer_code, COUNT(*)
		FROM survey_results
		WHERE flow_id = $1
		GROUP BY answer_code
	`, flowID)
	if err != nil {
		t.Fatalf("GROUP BY survey_results: %v", err)
	}
	defer func() {
		if cerr := agg.Close(); cerr != nil {
			t.Logf("cerrando rows: %v", cerr)
		}
	}()
	out := make(map[string]int)
	for agg.Next() {
		var code string
		var n int
		if err := agg.Scan(&code, &n); err != nil {
			t.Fatalf("scan agregación: %v", err)
		}
		out[code] = n
	}
	if err := agg.Err(); err != nil {
		t.Fatalf("iterando agregación: %v", err)
	}
	return out
}
