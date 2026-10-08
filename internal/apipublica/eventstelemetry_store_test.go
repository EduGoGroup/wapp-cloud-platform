//go:build pendiente

package apipublica_test

// eventstelemetry_store_test.go — cubre el contrato de eventstelemetry_store.go
// (PostgresEventTelemetryStore, NewPostgresEventTelemetryStore, ListEventTelemetry) contra un
// driver de mentira: la sentencia y sus argumentos tal como llegan al driver, el mapeo de las
// filas y los cuatro errores. Lo que el SQL HACE (qué filas elige, en qué orden) no se puede ver
// aquí: lo prueba eventstelemetryhelpertest.Contrato contra Postgres en test/procesos.

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// telemetryQuery es la consulta de G18, LITERAL: la misma cadena, carácter a carácter, que
// eventTelemetryQuery en la cara vieja (internal/publicapi/eventstelemetry_store.go). Está
// escrita dos veces a propósito: quien toque la del adaptador tiene que tocar esta, y leer por
// qué no debe (el predicado es el del índice parcial de la migración 0056).
const telemetryQuery = `
SELECT id, name, payload->>'kind' AS event_kind, payload, created_at
  FROM public.flow_events
 WHERE tenant_id = $1
   AND name LIKE 'event\_%'
   AND ($2::timestamptz IS NULL OR created_at >= $2)
   AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4))
 ORDER BY created_at, id
 LIMIT $5
`

// telemetryList llama al adaptador con un contexto vivo.
func telemetryList(store *apipublica.PostgresEventTelemetryStore, tenant string, f apipublica.EventTelemetryFilter) ([]apipublica.EventTelemetryRow, error) {
	return store.ListEventTelemetry(context.Background(), tenant, f)
}

// TestNewPostgresEventTelemetryStore: devuelve siempre un store distinto de nil, sin tocar la
// base (ni siquiera necesita una).
func TestNewPostgresEventTelemetryStore(t *testing.T) {
	if apipublica.NewPostgresEventTelemetryStore(nil) == nil {
		t.Error("NewPostgresEventTelemetryStore(nil) = nil; quiero un store")
	}
	fake, db := openTelemetryFakeDB(t)
	store := apipublica.NewPostgresEventTelemetryStore(db)
	if store == nil {
		t.Fatal("NewPostgresEventTelemetryStore(db) = nil; quiero un store")
	}
	var _ apipublica.EventTelemetryReader = store
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el store mandó %d sentencias a la base; quiero 0", n)
	}
}

// TestPostgresEventTelemetryStore_QueryAndArgs: UNA sentencia, la consulta literal, con sus cinco
// argumentos en orden; el cero de Since y de CursorAt viaja como NULL, y el id del cursor solo
// viaja con su instante.
func TestPostgresEventTelemetryStore_QueryAndArgs(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cursorAt := time.Date(2026, 8, 10, 12, 0, 0, 123456789, time.FixedZone("-03", -3*3600))
	cases := []struct {
		name   string
		tenant string
		filter apipublica.EventTelemetryFilter
		want   []driver.Value
	}{
		{"only the limit", "tenant-a", apipublica.EventTelemetryFilter{Limit: 101},
			[]driver.Value{"tenant-a", nil, nil, nil, int64(101)}},
		{"since", "tenant-a", apipublica.EventTelemetryFilter{Since: since, Limit: 3},
			[]driver.Value{"tenant-a", since, nil, nil, int64(3)}},
		{"cursor", "tenant-b", apipublica.EventTelemetryFilter{CursorAt: cursorAt, CursorID: 77, Limit: 3},
			[]driver.Value{"tenant-b", nil, cursorAt, int64(77), int64(3)}},
		{"cursor with id zero", "tenant-a", apipublica.EventTelemetryFilter{CursorAt: cursorAt, CursorID: 0, Limit: 3},
			[]driver.Value{"tenant-a", nil, cursorAt, int64(0), int64(3)}},
		{"cursor with a negative id", "tenant-a", apipublica.EventTelemetryFilter{CursorAt: cursorAt, CursorID: -5, Limit: 3},
			[]driver.Value{"tenant-a", nil, cursorAt, int64(-5), int64(3)}},
		{"cursor id without its instant travels as null", "tenant-a", apipublica.EventTelemetryFilter{CursorID: 77, Limit: 3},
			[]driver.Value{"tenant-a", nil, nil, nil, int64(3)}},
		{"since and cursor", "tenant-a", apipublica.EventTelemetryFilter{Since: since, CursorAt: cursorAt, CursorID: 9, Limit: 5001},
			[]driver.Value{"tenant-a", since, cursorAt, int64(9), int64(5001)}},
		{"since equal to the cursor instant", "tenant-a", apipublica.EventTelemetryFilter{Since: cursorAt, CursorAt: since, CursorID: 1, Limit: 1},
			[]driver.Value{"tenant-a", cursorAt, since, int64(1), int64(1)}},
		{"empty tenant and limit zero", "", apipublica.EventTelemetryFilter{Limit: 0},
			[]driver.Value{"", nil, nil, nil, int64(0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, db := openTelemetryFakeDB(t)
			fake.answer()
			if _, err := telemetryList(apipublica.NewPostgresEventTelemetryStore(db), tc.tenant, tc.filter); err != nil {
				t.Fatalf("ListEventTelemetry: %v", err)
			}
			stmts := fake.statements()
			if len(stmts) != 1 {
				t.Fatalf("llegaron %d sentencias al driver; quiero 1", len(stmts))
			}
			if stmts[0].query != telemetryQuery {
				t.Errorf("consulta\n%s\nquiero (carácter a carácter)\n%s", stmts[0].query, telemetryQuery)
			}
			if !reflect.DeepEqual(stmts[0].args, tc.want) {
				t.Errorf("argumentos %#v; quiero %#v", stmts[0].args, tc.want)
			}
		})
	}
}

// TestPostgresEventTelemetryStore_MapsRows: cada fila sale con sus cinco columnas en su campo,
// en el orden de la consulta; event_kind NULL es vacío y el payload llega tal cual.
func TestPostgresEventTelemetryStore_MapsRows(t *testing.T) {
	at := time.Date(2026, 8, 10, 12, 0, 0, 123456000, time.UTC)
	fake, db := openTelemetryFakeDB(t)
	fake.answer(
		[]driver.Value{int64(42), "event_started", "cart", []byte(`{"kind": "cart", "history_id": "h-1"}`), at},
		[]driver.Value{int64(7), "event_closed", nil, []byte(`{}`), at.Add(time.Second)},
		[]driver.Value{int64(9007199254740993), "event_", "", []byte(`{"kind": ""}`), at.Add(2 * time.Second)},
	)
	got, err := telemetryList(apipublica.NewPostgresEventTelemetryStore(db), "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListEventTelemetry: %v", err)
	}
	want := []apipublica.EventTelemetryRow{
		{ID: 42, Name: "event_started", EventKind: "cart", Payload: []byte(`{"kind": "cart", "history_id": "h-1"}`), CreatedAt: at},
		{ID: 7, Name: "event_closed", EventKind: "", Payload: []byte(`{}`), CreatedAt: at.Add(time.Second)},
		{ID: 9007199254740993, Name: "event_", EventKind: "", Payload: []byte(`{"kind": ""}`), CreatedAt: at.Add(2 * time.Second)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filas\n  %+v\nquiero\n  %+v", got, want)
	}
}

// TestPostgresEventTelemetryStore_NoRows: sin filas, una lista vacía (no nil) y error nil.
func TestPostgresEventTelemetryStore_NoRows(t *testing.T) {
	fake, db := openTelemetryFakeDB(t)
	fake.answer()
	got, err := telemetryList(apipublica.NewPostgresEventTelemetryStore(db), "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("sin filas: (%#v, %v); quiero una lista vacía no nil y error nil", got, err)
	}
}

// TestPostgresEventTelemetryStore_Errors: los cuatro fallos, cada uno con su texto, envolviendo
// la causa y con CERO filas aunque ya se hubiera leído alguna.
func TestPostgresEventTelemetryStore_Errors(t *testing.T) {
	cause := errors.New("causa del driver")
	closeCause := errors.New("causa del cierre")
	goodRow := []driver.Value{int64(1), "event_started", "cart", []byte(`{}`), time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	badRow := []driver.Value{"no-es-un-id", "event_started", "cart", []byte(`{}`), time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	cases := []struct {
		name    string
		arrange func(f *telemetryFakeDB)
		prefix  string
		is      error // la causa que errors.Is debe encontrar; nil = no se mira
		isNot   error // la que NO debe aparecer
	}{
		{"the query fails", func(f *telemetryFakeDB) { f.err = cause },
			"apipublica: consultar telemetría de eventos: ", cause, nil},
		{"a row cannot be scanned", func(f *telemetryFakeDB) { f.answer(goodRow, badRow) },
			"apipublica: escanear fila de telemetría de eventos: ", nil, nil},
		{"the iteration breaks after a row", func(f *telemetryFakeDB) { f.answer(goodRow); f.rowsErr = cause },
			"apipublica: iterar telemetría de eventos: ", cause, nil},
		{"closing the rows fails", func(f *telemetryFakeDB) { f.answer(goodRow); f.closeErr = closeCause },
			"apipublica: cerrar filas de telemetría de eventos: ", closeCause, nil},
		{"a scan error is not replaced by the close error", func(f *telemetryFakeDB) { f.answer(badRow); f.closeErr = closeCause },
			"apipublica: escanear fila de telemetría de eventos: ", nil, closeCause},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, db := openTelemetryFakeDB(t)
			tc.arrange(fake)
			got, err := telemetryList(apipublica.NewPostgresEventTelemetryStore(db), "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
			if err == nil || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("error %v; quiero uno que empiece por %q", err, tc.prefix)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error %v: no envuelve su causa (%v)", err, tc.is)
			}
			if tc.isNot != nil && errors.Is(err, tc.isNot) {
				t.Errorf("error %v: lleva el del cierre (%v), que no debía pisar al primero", err, tc.isNot)
			}
			if got != nil {
				t.Errorf("con error devolvió %d filas (%+v); quiero nil", len(got), got)
			}
		})
	}
}

// TestPostgresEventTelemetryStore_CancelledContext: con el contexto cancelado es un error de
// consulta que envuelve el del contexto, y la sentencia ni llega al driver.
func TestPostgresEventTelemetryStore_CancelledContext(t *testing.T) {
	fake, db := openTelemetryFakeDB(t)
	fake.answer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := apipublica.NewPostgresEventTelemetryStore(db).ListEventTelemetry(ctx, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "apipublica: consultar telemetría de eventos: ") || got != nil {
		t.Errorf("contexto cancelado: (%v, %v); quiero nil y un error de consulta que envuelva context.Canceled", got, err)
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("contexto cancelado: llegaron %d sentencias al driver; quiero 0", n)
	}
}
