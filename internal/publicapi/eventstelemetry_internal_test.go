package publicapi

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// openEventTelemetryExplainDB abre la BD de integración PROPIA de T6.5 (nunca
// wapp_test). Vive en un archivo aparte (white-box, package publicapi) porque
// necesita la constante NO exportada eventTelemetryQuery — mismo contrato de
// skip que el resto de *_integration_test.go del repo.
func openEventTelemetryExplainDB(t *testing.T) *sql.DB {
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

// TestListEventTelemetry_UsaElIndiceParcial es la DEFENSA contra la
// degradación silenciosa que la migración 0056 advierte en su cabecera: corre
// EXPLAIN sobre eventTelemetryQuery TAL CUAL —la constante que
// ListEventTelemetry ejecuta en producción, no una copia a mano que podría
// divergir en silencio— y afirma que el plan usa flow_events_tenant_event_idx.
//
// `SET LOCAL enable_seqscan = off` es DELIBERADO y no un truco para forzar un
// resultado: en una tabla de prueba con un puñado de filas, el planificador
// prefiere Seq Scan por COSTE (la tabla es minúscula) aunque el índice parcial
// SÍ APLIQUE — eso es una decisión de cardinalidad correcta del planificador,
// no la degradación que este test quiere detectar. Lo que hay que probar es
// APLICABILIDAD, no preferencia de coste sobre una tabla de juguete: con
// Seq Scan apagado, Postgres tiene que elegir ALGÚN índice, y el punto exacto
// de esta prueba es CUÁL. Confirmado a mano contra esta misma BD (T6.5,
// informe de la tarea): con el predicado CORRECTO (escapado) elige
// flow_events_tenant_event_idx; con el predicado roto (sin escapar) el parcial
// deja de aplicar y el planificador cae a flow_events_scan_idx (0009) — la
// prueba de que el índice nuevo específicamente dejó de estar disponible, no
// que "algún" índice desapareció.
//
// ⚠️ REVISIÓN (refutación del revisor, 2026-08-11): afirmar SOLO que el nombre
// del índice sale en el plan verifica APLICABILIDAD, no UTILIDAD, y eso deja
// pasar una degradación real. Mutación medida: cambiar la clave de la 0056 de
// `(tenant_id, created_at, id)` a `(tenant_id)` —dejando el MISMO predicado
// parcial— mantenía este test EN VERDE y, sobre 200.000 filas sembradas, subía
// la consulta de 58 a 2.594 buffers (44,7×) para el tenant grande porque el
// plan pasaba a Bitmap Heap Scan + `Filter: created_at >= …` + `Sort`. Por eso
// ahora se afirman TRES propiedades y no una:
//
//  1. el plan usa flow_events_tenant_event_idx (aplicabilidad — lo de antes);
//  2. el plan NO lleva un nodo `Sort` (el índice entrega el ORDEN del keyset;
//     si `created_at, id` salen de la clave, Postgres tiene que ordenar);
//  3. `created_at` viaja en el `Index Cond`, no en un `Filter` (el índice ACOTA
//     la ventana temporal; si solo indexara `tenant_id`, `since` degradaría a
//     filtro sobre todas las filas event_* del tenant).
//
// Las tres son independientes del tamaño de la tabla, así que siguen valiendo
// en una BD de CI recién migrada. Medido: con la 0056 correcta el plan es
// `Index Scan … Index Cond: (tenant_id = … AND created_at >= …)` sin `Sort`.
func TestListEventTelemetry_UsaElIndiceParcial(t *testing.T) {
	db := openEventTelemetryExplainDB(t)
	tenant := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO public.flow_events (tenant_id, contact_id, flow_id, flow_version, kind, name, payload)
		VALUES ($1, 'contact-opaco', 'flow-1', 1, 'event', 'event_started', '{"kind":"cart"}'::jsonb)
	`, tenant); err != nil {
		t.Fatalf("sembrar fila de prueba: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("abrir transacción: %v", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			t.Logf("rollback de la transacción de EXPLAIN: %v", rerr)
		}
	}()
	if _, err := tx.Exec("SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("SET LOCAL enable_seqscan=off: %v", err)
	}

	// `since` viaja NO NULO a propósito: con los cinco placeholders a NULL el
	// plan no tiene ventana temporal que acotar y las propiedades (2) y (3) no
	// se pueden observar — que es justo el agujero por el que pasaba la
	// mutación de la clave del índice.
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query("EXPLAIN (FORMAT TEXT) "+eventTelemetryQuery, tenant, since, nil, nil, 100)
	if err != nil {
		t.Fatalf("EXPLAIN sobre eventTelemetryQuery: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas de EXPLAIN: %v", cerr)
		}
	}()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if serr := rows.Scan(&line); serr != nil {
			t.Fatalf("escanear línea de EXPLAIN: %v", serr)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("iterar EXPLAIN: %v", rerr)
	}

	got := plan.String()
	t.Logf("EXPLAIN real de eventTelemetryQuery (enable_seqscan=off):\n%s", got)

	// (1) APLICABILIDAD: el predicado de la consulta implica el del índice parcial.
	if !strings.Contains(got, "flow_events_tenant_event_idx") {
		t.Fatalf("el plan NO usa el índice parcial flow_events_tenant_event_idx (migración 0056):\n%s", got)
	}
	// (2) ORDEN GRATIS: `created_at, id` están en la CLAVE, así que el keyset no
	// paga un Sort. Un índice que solo lleve `tenant_id` sigue siendo aplicable
	// y pasaría (1), pero obliga a ordenar toda la página.
	if strings.Contains(got, "Sort") {
		t.Fatalf("el plan ORDENA a mano: el índice no está entregando (created_at, id) desde su clave "+
			"— el keyset de la paginación deja de ser gratis:\n%s", got)
	}
	// (3) VENTANA ACOTADA POR EL ÍNDICE: `since` tiene que entrar por Index Cond,
	// no quedarse como Filter/Recheck sobre todas las filas event_* del tenant.
	if !strings.Contains(got, "Index Cond") || !strings.Contains(indexCondOf(got), "created_at") {
		t.Fatalf("`created_at` NO viaja en el Index Cond: el índice no acota la ventana temporal, "+
			"solo el tenant (mide 44,7x mas buffers sobre 200k filas):\n%s", got)
	}
}

// indexCondOf devuelve la concatenación de las líneas `Index Cond:` del plan.
// Se mira SOLO ahí a propósito: `created_at` aparece también en el Sort Key y en
// el Filter, y buscarlo en el plan entero haría pasar justo el plan degradado
// que la propiedad (3) existe para cazar.
func indexCondOf(plan string) string {
	var out strings.Builder
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Index Cond:") {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	return out.String()
}
