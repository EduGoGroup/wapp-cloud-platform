package publicapi_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// openEventTelemetryTestDB abre la BD de integración PROPIA de T6.5 (nunca
// wapp_test: hay interbloqueo medido con el otro agente de la Ola 6, que
// trabaja internal/flujos/** a la vez sobre su propia base). Mismo contrato
// que el resto de *_integration_test.go del repo: sin DSN se salta.
func openEventTelemetryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WAPP_TEST_DB_DSN")
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatal("WAPP_TEST_DB_DSN no definido pero WAPP_TEST_REQUIRE_DB exige BD")
		}
		t.Skip("WAPP_TEST_DB_DSN no definido: se omiten los tests de integración con BD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en WAPP_TEST_DB_DSN (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible en WAPP_TEST_DB_DSN (%v): se omiten", err)
	}
	if _, merr := migrations.Migrate(ctx, db); merr != nil {
		t.Fatalf("aplicar migraciones: %v", merr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	return db
}

func insertEventTelemetryRow(t *testing.T, db *sql.DB, tenantID, kind, name, payloadJSON string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`
		INSERT INTO public.flow_events (tenant_id, contact_id, flow_id, flow_version, kind, name, payload)
		VALUES ($1, 'contact-opaco', 'flow-1', 1, $2, $3, $4::jsonb)
		RETURNING id
	`, tenantID, kind, name, payloadJSON).Scan(&id)
	if err != nil {
		t.Fatalf("insertar flow_event de prueba (%s): %v", name, err)
	}
	return id
}

// TestListEventTelemetry_EscapaGuionBajoEnElPredicado fija la enmienda #1
// DESDE EL STORE real contra Postgres: una fila que solo matchea el comodín
// SIN escapar («eventXfoo») no debe aparecer en el resultado.
func TestListEventTelemetry_EscapaGuionBajoEnElPredicado(t *testing.T) {
	db := openEventTelemetryTestDB(t)
	store := publicapi.NewPostgresEventTelemetryStore(db)
	tenant := uuid.NewString()

	insertEventTelemetryRow(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	insertEventTelemetryRow(t, db, tenant, "persist", "eventXfoo", `{"kind":"cart"}`)
	insertEventTelemetryRow(t, db, tenant, "persist", "survey_answer", `{"kind":"survey"}`)

	rows, err := store.ListEventTelemetry(context.Background(), tenant, publicapi.EventTelemetryFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListEventTelemetry: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("quiero exactamente 1 fila (event_started), got %d: %+v", len(rows), rows)
	}
	if rows[0].Name != "event_started" || rows[0].EventKind != "cart" {
		t.Fatalf("fila inesperada: %+v", rows[0])
	}
}

// TestListEventTelemetry_AcotaPorTenant fija INV-8 a nivel de store: un tenant
// nunca ve filas de otro, aunque compartan el mismo `name`.
func TestListEventTelemetry_AcotaPorTenant(t *testing.T) {
	db := openEventTelemetryTestDB(t)
	store := publicapi.NewPostgresEventTelemetryStore(db)
	tenantX := uuid.NewString()
	tenantY := uuid.NewString()

	insertEventTelemetryRow(t, db, tenantX, "event", "event_started", `{"kind":"cart"}`)
	insertEventTelemetryRow(t, db, tenantY, "event", "event_started", `{"kind":"cart"}`)

	rows, err := store.ListEventTelemetry(context.Background(), tenantX, publicapi.EventTelemetryFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListEventTelemetry: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("tenantX debe ver SOLO su fila, got %d: %+v", len(rows), rows)
	}
}

// TestListEventTelemetry_OrdenYKeyset fija la enmienda #3: ORDER BY
// (created_at, id) + keyset. Se insertan filas con created_at EXPLÍCITO fuera
// de orden de inserción (updateando la columna tras el INSERT, porque la
// tabla usa DEFAULT now()) para que un resultado que dependiera del orden
// físico del heap fallara.
func TestListEventTelemetry_OrdenYKeyset(t *testing.T) {
	db := openEventTelemetryTestDB(t)
	store := publicapi.NewPostgresEventTelemetryStore(db)
	tenant := uuid.NewString()

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// Insertadas en orden name c,a,b pero con created_at a,b,c: si el store no
	// ordenara por created_at,id el test fallaría con cualquier permutación
	// que no sea la cronológica.
	idC := insertEventTelemetryRow(t, db, tenant, "event", "event_closed", `{"kind":"cart"}`)
	idA := insertEventTelemetryRow(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	idB := insertEventTelemetryRow(t, db, tenant, "event", "event_switched", `{"kind":"cart"}`)
	setCreatedAt(t, db, idA, base)
	setCreatedAt(t, db, idB, base.Add(time.Hour))
	setCreatedAt(t, db, idC, base.Add(2*time.Hour))

	// Página 1: limit=2.
	page1, err := store.ListEventTelemetry(context.Background(), tenant, publicapi.EventTelemetryFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListEventTelemetry página 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Name != "event_started" || page1[1].Name != "event_switched" {
		t.Fatalf("página 1 fuera de orden: %+v", page1)
	}

	// Página 2 con el cursor de la última fila de la página 1.
	last := page1[len(page1)-1]
	page2, err := store.ListEventTelemetry(context.Background(), tenant, publicapi.EventTelemetryFilter{
		Limit: 2, CursorAt: last.CreatedAt, CursorID: last.ID,
	})
	if err != nil {
		t.Fatalf("ListEventTelemetry página 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Name != "event_closed" {
		t.Fatalf("página 2 inesperada: %+v", page2)
	}
}

func setCreatedAt(t *testing.T, db *sql.DB, id int64, at time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE public.flow_events SET created_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("fijar created_at de la fila %d: %v", id, err)
	}
}

// El test EXPLAIN que defiende el índice parcial (migración 0056) vive en
// eventstelemetry_internal_test.go (package publicapi, no publicapi_test):
// necesita la constante NO exportada eventTelemetryQuery para correr EXPLAIN
// sobre la consulta REAL del store, carácter a carácter — una copia a mano
// aquí podría divergir en silencio de la que corre en producción, que es
// justo la fragilidad que ese test existe para atajar.
