package apipublica

// eventstelemetry_store_internal_test.go — los auxiliares NO exportados de
// eventstelemetry_store.go, que son lo que el adaptador hace sin base: armar los argumentos de la
// consulta y mapear una fila (el «test de mapeo» de TX.16). Es un test INTERNO (package
// apipublica) y nace con el verde (05 E-4, P6).

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// TestEventTelemetryNullableTime: el cero es NULL (nil sin tipo, no un time.Time en cero) y
// cualquier otro instante viaja tal cual, con su zona y sus nanosegundos.
func TestEventTelemetryNullableTime(t *testing.T) {
	if got := eventTelemetryNullableTime(time.Time{}); got != nil {
		t.Errorf("el instante cero da %#v; quiero nil (NULL)", got)
	}
	// El cero dicho en otra zona sigue siendo el cero.
	if got := eventTelemetryNullableTime(time.Time{}.In(time.FixedZone("+05", 5*3600))); got != nil {
		t.Errorf("el instante cero en otra zona da %#v; quiero nil (NULL)", got)
	}
	for _, at := range []time.Time{
		time.Date(2026, 8, 10, 12, 0, 0, 123456789, time.FixedZone("-03", -3*3600)),
		time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC), // un nanosegundo después del cero
		time.Unix(0, 0),
	} {
		got, ok := eventTelemetryNullableTime(at).(time.Time)
		if !ok || got != at {
			t.Errorf("%v da %#v; quiero el mismo time.Time", at, got)
		}
	}
}

// TestEventTelemetryArgs: cinco argumentos en el orden de los placeholders; el cursor viaja
// entero o no viaja.
func TestEventTelemetryArgs(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cursorAt := time.Date(2026, 8, 10, 12, 0, 0, 7, time.UTC)
	cases := []struct {
		name   string
		filter EventTelemetryFilter
		want   []any
	}{
		{"zero filter", EventTelemetryFilter{}, []any{"tenant-a", nil, nil, nil, 0}},
		{"limit", EventTelemetryFilter{Limit: 101}, []any{"tenant-a", nil, nil, nil, 101}},
		{"since", EventTelemetryFilter{Since: since, Limit: 2}, []any{"tenant-a", since, nil, nil, 2}},
		{"cursor", EventTelemetryFilter{CursorAt: cursorAt, CursorID: 77, Limit: 2}, []any{"tenant-a", nil, cursorAt, int64(77), 2}},
		{"cursor with id zero is not null", EventTelemetryFilter{CursorAt: cursorAt, Limit: 2}, []any{"tenant-a", nil, cursorAt, int64(0), 2}},
		{"id without instant is null", EventTelemetryFilter{CursorID: 77, Limit: 2}, []any{"tenant-a", nil, nil, nil, 2}},
		{"since does not turn the cursor on", EventTelemetryFilter{Since: since, CursorID: 77, Limit: 2}, []any{"tenant-a", since, nil, nil, 2}},
		{"everything", EventTelemetryFilter{Since: since, CursorAt: cursorAt, CursorID: -1, Limit: 5001}, []any{"tenant-a", since, cursorAt, int64(-1), 5001}},
	}
	for _, tc := range cases {
		if got := eventTelemetryArgs("tenant-a", tc.filter); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: argumentos %#v; quiero %#v", tc.name, got, tc.want)
		}
	}
}

// telemetryScannerFake es un eventTelemetryScanner: rellena los destinos con sus valores, por
// posición y exigiendo el tipo de destino de cada columna, o falla con err.
type telemetryScannerFake struct {
	id        int64
	name      string
	eventKind sql.NullString
	payload   []byte
	createdAt time.Time
	err       error
}

func (s telemetryScannerFake) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != 5 {
		return fmt.Errorf("Scan con %d destinos; quiero 5", len(dest))
	}
	id, ok0 := dest[0].(*int64)
	name, ok1 := dest[1].(*string)
	eventKind, ok2 := dest[2].(*sql.NullString)
	payload, ok3 := dest[3].(*[]byte)
	createdAt, ok4 := dest[4].(*time.Time)
	if !ok0 || !ok1 || !ok2 || !ok3 || !ok4 {
		return fmt.Errorf("destinos %T, %T, %T, %T, %T; quiero *int64, *string, *sql.NullString, *[]byte, *time.Time",
			dest[0], dest[1], dest[2], dest[3], dest[4])
	}
	*id, *name, *eventKind, *payload, *createdAt = s.id, s.name, s.eventKind, s.payload, s.createdAt
	return nil
}

// TestScanEventTelemetryRow: las cinco columnas del SELECT, en su orden y con su tipo, van cada
// una a su campo; event_kind NULL queda vacío; y un fallo de Scan sale tal cual con la fila en
// cero.
func TestScanEventTelemetryRow(t *testing.T) {
	at := time.Date(2026, 8, 10, 12, 0, 0, 123456000, time.UTC)
	cases := []struct {
		name string
		scan telemetryScannerFake
		want EventTelemetryRow
	}{
		{"every column", telemetryScannerFake{id: 42, name: "event_started", eventKind: sql.NullString{String: "cart", Valid: true}, payload: []byte(`{"kind":"cart"}`), createdAt: at},
			EventTelemetryRow{ID: 42, Name: "event_started", EventKind: "cart", Payload: []byte(`{"kind":"cart"}`), CreatedAt: at}},
		{"null event kind is empty", telemetryScannerFake{id: 7, name: "event_closed", payload: []byte(`{}`), createdAt: at},
			EventTelemetryRow{ID: 7, Name: "event_closed", EventKind: "", Payload: []byte(`{}`), CreatedAt: at}},
		{"name and kind are not swapped", telemetryScannerFake{id: 1, name: "kind-like", eventKind: sql.NullString{String: "name-like", Valid: true}, payload: []byte(`1`), createdAt: at},
			EventTelemetryRow{ID: 1, Name: "kind-like", EventKind: "name-like", Payload: []byte(`1`), CreatedAt: at}},
	}
	for _, tc := range cases {
		got, err := scanEventTelemetryRow(tc.scan)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: (%+v, %v); quiero (%+v, nil)", tc.name, got, err, tc.want)
		}
	}

	cause := errors.New("columna ilegible")
	got, err := scanEventTelemetryRow(telemetryScannerFake{id: 9, name: "event_started", err: cause})
	if !errors.Is(err, cause) || !reflect.DeepEqual(got, EventTelemetryRow{}) {
		t.Errorf("Scan que falla: (%+v, %v); quiero la fila en cero y la causa", got, err)
	}
}
