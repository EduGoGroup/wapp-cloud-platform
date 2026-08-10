// event_pair_pg_test.go — las DOS direcciones del par contenedor↔contenido de E-8
// contra Postgres REAL (Plan 043 · Ola 4.5 · T4.5.5, D-043.21):
//
//   - contenido→contenedor: descartar la solicitud cancela el evento que ella
//     declara (cancelContainerTx, REQ-32e / D-043.15(1) re-expresada);
//   - contenedor→contenido: cancelar el evento abandona su solicitud
//     (AbandonByEvent).
//
// Es un test de CAJA BLANCA a propósito: cancelContainerTx no se exporta —ningún
// paquete ajeno tiene por qué cerrar contenedores— y con la guarda `live_event`
// delante (misma transacción) el camino público Discard solo la alcanza con el
// evento ya terminal (0 filas). Aquí se fija la mecánica del CAS por sí misma:
// que exista, que selle closed_at y que no pise una muerte ajena.
package intakes

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// abrirParPG abre la BD (mismo contrato de salto que openTestDB del paquete
// intakes_test, que desde aquí no se alcanza) y siembra tenant + evento en
// `estadoEvento` + solicitud en `estadoIntake` que lo declara (prefijos t45i-,
// t.Cleanup en orden LIFO: solicitud → tenant, con el evento por CASCADE).
func abrirParPG(t *testing.T, estadoIntake, estadoEvento string) (db *sql.DB, tenantID, intakeID, eventID string) {
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
	var err error
	db, err = postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible (%v): se omiten", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}

	tenantID = uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenants (id, slug, display_name)
		VALUES ($1, $2, 'Ola 4.5 par')
	`, tenantID, "t45i-"+tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})

	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
		VALUES ($1, 't45i-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        't45i-' || gen_random_uuid(), $2, 'flujo-w45', 1,
		        CASE WHEN $2 = 'open' THEN NULL ELSE now() END)
		RETURNING id::text
	`, tenantID, estadoEvento).Scan(&eventID); err != nil {
		t.Fatalf("sembrando evento: %v", err)
	}

	intakeID = uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at)
		VALUES ($1, $2, $3, 't45i-sess-par', $4, 18000, $5, now(), now())
	`, intakeID, tenantID, uuid.NewString(), estadoIntake, eventID); err != nil {
		t.Fatalf("sembrando solicitud: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM public.intakes WHERE id = $1`, intakeID); err != nil {
			t.Logf("limpiando solicitud: %v", err)
		}
	})
	return db, tenantID, intakeID, eventID
}

// eventoPG lee (status, closed_at IS NOT NULL) del evento.
func eventoPG(t *testing.T, db *sql.DB, eventID string) (string, bool) {
	t.Helper()
	var (
		status   string
		closedAt sql.NullTime
	)
	if err := db.QueryRow(`SELECT status, closed_at FROM public.conversation_events WHERE id = $1`,
		eventID).Scan(&status, &closedAt); err != nil {
		t.Fatalf("leyendo evento: %v", err)
	}
	return status, closedAt.Valid
}

// intakePG lee el status crudo de la solicitud.
func intakePG(t *testing.T, db *sql.DB, intakeID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM public.intakes WHERE id = $1`, intakeID).Scan(&status); err != nil {
		t.Fatalf("leyendo solicitud: %v", err)
	}
	return status
}

// TestCancelContainerTx_DescartarCierraElContenedor: la dirección
// contenido→contenedor. Un intake cuyo evento está `open` deja, al descartarse, el
// evento `cancelled` con closed_at sellado; el segundo intento (CAS `AND
// status='open'`) no toca nada y no es error — la muerte sellada no se pisa
// (calcado de transitionSQL).
func TestCancelContainerTx_DescartarCierraElContenedor(t *testing.T) {
	db, _, _, eventID := abrirParPG(t, StatusOpen, "open")
	ctx := context.Background()

	cierra := func() {
		t.Helper()
		if err := postgres.WithTx(ctx, db, func(tx *sql.Tx) error {
			return cancelContainerTx(ctx, tx, eventID)
		}); err != nil {
			t.Fatalf("cancelContainerTx: %v", err)
		}
	}

	cierra()
	status, closed := eventoPG(t, db, eventID)
	if status != "cancelled" || !closed {
		t.Fatalf("evento=%q closed=%v; quiero cancelled con closed_at sellado", status, closed)
	}

	// Idempotente y sin pisar: repetir no cambia nada ni falla (0 filas = éxito).
	cierra()
	if status, _ := eventoPG(t, db, eventID); status != "cancelled" {
		t.Fatalf("el segundo cierre pisó la muerte: %q", status)
	}

	// Y sin ligadura (solicitud legada) es un no-op explícito.
	if err := postgres.WithTx(ctx, db, func(tx *sql.Tx) error {
		return cancelContainerTx(ctx, tx, "")
	}); err != nil {
		t.Fatalf("cancelContainerTx sin evento: %v", err)
	}
}

// TestPostgres_AbandonByEvent_AbandonaElOpen: la dirección contenedor→contenido.
// El evento (recién cancelado por el runtime) abandona su solicitud `open` por el
// event_id que ELLA declara — sin conocer ningún id de hijo — y el segundo intento
// es éxito idempotente (0 filas): el reintento de una cancelación a medias puede
// terminar.
func TestPostgres_AbandonByEvent_AbandonaElOpen(t *testing.T) {
	db, tenantID, intakeID, eventID := abrirParPG(t, StatusOpen, "cancelled")
	store := NewPostgres(db)
	ctx := context.Background()

	if err := store.AbandonByEvent(ctx, tenantID, eventID); err != nil {
		t.Fatalf("AbandonByEvent: %v", err)
	}
	if got := intakePG(t, db, intakeID); got != StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", got)
	}

	// Idempotente: ya no hay `open` que casar y sigue siendo éxito.
	if err := store.AbandonByEvent(ctx, tenantID, eventID); err != nil {
		t.Fatalf("segundo AbandonByEvent: %v", err)
	}
	if got := intakePG(t, db, intakeID); got != StatusAbandoned {
		t.Fatalf("el reintento movió la solicitud: %q", got)
	}

	// Un evento inexistente (o que no parió contenido) también es éxito: 0 filas.
	if err := store.AbandonByEvent(ctx, tenantID, uuid.NewString()); err != nil {
		t.Fatalf("AbandonByEvent sin contenido: %v", err)
	}
	// Y un id que ni siquiera es UUID no molesta a Postgres: mismo destino.
	if err := store.AbandonByEvent(ctx, tenantID, "no-soy-un-uuid"); err != nil {
		t.Fatalf("AbandonByEvent con id malformado: %v", err)
	}
}

// TestPostgres_AbandonByEvent_NoTocaSettled: el guard `status='open'` va en el
// propio SQL — una solicitud ya resuelta por un humano no se abandona porque su
// evento muera.
func TestPostgres_AbandonByEvent_NoTocaSettled(t *testing.T) {
	db, tenantID, intakeID, eventID := abrirParPG(t, StatusSettled, "cancelled")
	store := NewPostgres(db)

	if err := store.AbandonByEvent(context.Background(), tenantID, eventID); err != nil {
		t.Fatalf("AbandonByEvent: %v", err)
	}
	if got := intakePG(t, db, intakeID); got != StatusSettled {
		t.Fatalf("status=%q; una settled no se abandona (0 filas = éxito sin tocar)", got)
	}
}

// TestPostgres_AbandonByEvent_AisladoPorTenant: el tenant viaja en el WHERE — el
// evento de otro tenant no alcanza una solicitud ajena (INV-8).
func TestPostgres_AbandonByEvent_AisladoPorTenant(t *testing.T) {
	db, _, intakeID, eventID := abrirParPG(t, StatusOpen, "cancelled")
	store := NewPostgres(db)

	if err := store.AbandonByEvent(context.Background(), uuid.NewString(), eventID); err != nil {
		t.Fatalf("AbandonByEvent de tenant ajeno: %v", err)
	}
	if got := intakePG(t, db, intakeID); got != StatusOpen {
		t.Fatalf("status=%q; un tenant ajeno no abandona nada", got)
	}
}
