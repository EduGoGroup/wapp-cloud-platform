//go:build pendiente

package apipublica_test

// eventstelemetry_test.go — cubre el contrato de eventstelemetry.go (EventTelemetryRow,
// EventTelemetryFilter, EventTelemetryReader, EventTelemetryDeps, MountEventTelemetry): G18.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/eventstelemetryhelpertest"
)

const (
	telemetryTarget  = "/api/v1/events/telemetry"
	telemetryPattern = "GET /api/v1/events/telemetry"
	telemetryPerm    = "events_telemetry.read"

	telemetryEmpty      = `{"events":[],"limit":100}`
	telemetryMsgLimit   = "limit inválido: usa un entero positivo"
	telemetryMsgSince   = "since inválido: usa RFC3339 (p. ej. 2026-08-10T00:00:00Z)"
	telemetryMsgCursor  = "cursor inválido: usa el cursor opaco devuelto por la página anterior"
	telemetryMsgFailed  = "no se pudo leer la telemetría de eventos"
	telemetryMsgTooMany = "limit pedido (%s) excede el máximo de 5000: pagina con cursor en vez de pedir más de golpe"
)

var telemetryBase = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// El puerto de G18 lo cumplen el adaptador de la cara y el doble que pasa su suite.
var (
	_ apipublica.EventTelemetryReader = (*apipublica.PostgresEventTelemetryStore)(nil)
	_ apipublica.EventTelemetryReader = (*eventstelemetryhelpertest.Memory)(nil)
)

// telemetryReaderSpy es EventTelemetryReader: devuelve rows o err y apunta lo que recibió.
type telemetryReaderSpy struct {
	rows   []apipublica.EventTelemetryRow
	err    error
	calls  int
	tenant string
	filter apipublica.EventTelemetryFilter
}

func (s *telemetryReaderSpy) ListEventTelemetry(_ context.Context, tenantID string, f apipublica.EventTelemetryFilter) ([]apipublica.EventTelemetryRow, error) {
	s.calls++
	s.tenant, s.filter = tenantID, f
	return s.rows, s.err
}

// telemetryCara monta G18 con k y el lector dado.
func telemetryCara(k apipublica.Common, reader apipublica.EventTelemetryReader) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountEventTelemetry(c, k, apipublica.EventTelemetryDeps{EventTelemetry: reader})
	return c
}

// telemetryCursor codifica raw como lo hace la cara: base64 URL sin relleno. Los tests lo usan
// para fabricar cursores adversarios; un cliente de verdad solo devuelve el que recibió.
func telemetryCursor(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// telemetryRow es una fila de ciclo de vida con un payload mínimo.
func telemetryRow(id int64, name string, at time.Time) apipublica.EventTelemetryRow {
	return apipublica.EventTelemetryRow{ID: id, Name: name, EventKind: "cart", Payload: []byte(`{"history_id":"h","kind":"cart"}`), CreatedAt: at}
}

// telemetryGet llama a G18 como tenantA con el permiso y la query dada (con su «?»).
func telemetryGet(h *apipublicahelpertest.Harness, cara *apipublica.Cara, query string) *httptest.ResponseRecorder {
	return h.Call(cara, h.With(tenantA, telemetryPerm), http.MethodGet, telemetryTarget+query, "")
}

func TestMountEventTelemetry_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := telemetryCara(h.Common(), &telemetryReaderSpy{})
	wantPatterns(t, "G18", cara, []string{telemetryPattern})
	checkChain(t, h, cara, routeCase{id: "G18", method: http.MethodGet, target: telemetryTarget, perm: telemetryPerm, want: http.StatusOK})
}

// TestMountEventTelemetry_WithoutReaderIsNotMounted: sin lector la ruta no existe (404 de ruta
// inexistente, no 500), y montar no exige MW porque no hay cadena que armar.
func TestMountEventTelemetry_WithoutReaderIsNotMounted(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := apipublica.Nueva()
	apipublica.MountEventTelemetry(cara, h.Common(), apipublica.EventTelemetryDeps{})
	wantPatterns(t, "G18 sin lector", cara, nil)
	wantCode(t, "G18 sin lector", telemetryGet(h, cara, ""), http.StatusNotFound)

	if v := recuperar(func() {
		apipublica.MountEventTelemetry(apipublica.Nueva(), apipublica.Common{}, apipublica.EventTelemetryDeps{})
	}); v != nil {
		t.Errorf("MountEventTelemetry sin lector y sin MW: panic %v; quiero que no monte nada y no falle", v)
	}
}

func TestMountEventTelemetry_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountEventTelemetry(apipublica.Nueva(), apipublica.Common{}, apipublica.EventTelemetryDeps{EventTelemetry: &telemetryReaderSpy{}})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountEventTelemetry") {
		t.Errorf("MountEventTelemetry con MW nil: panic = %v; quiero un panic de cableado que nombre MountEventTelemetry", v)
	}
}

// TestMountEventTelemetry_TenantFromTokenAndDefaults: el tenant es el del token aunque la query
// traiga otro (INV-8); sin parámetros el filtro es solo el límite por defecto más la fila de
// sobra; no hay gate de feature ni auditoría; y sin filas responde [] y no null.
func TestMountEventTelemetry_TenantFromTokenAndDefaults(t *testing.T) {
	for name, rows := range map[string][]apipublica.EventTelemetryRow{"nil": nil, "empty": {}} {
		h := apipublicahelpertest.New(t)
		spy := &telemetryReaderSpy{rows: rows}
		rec := telemetryGet(h, telemetryCara(h.Common(), spy), "?tenant_id="+tenantB)
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, telemetryEmpty)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q, quiero application/json", name, ct)
		}
		if spy.calls != 1 || spy.tenant != tenantA {
			t.Errorf("%s: el puerto recibió %d llamadas con el tenant %q; quiero 1 con el del token, %q", name, spy.calls, spy.tenant, tenantA)
		}
		if want := (apipublica.EventTelemetryFilter{Limit: 101}); !reflect.DeepEqual(spy.filter, want) {
			t.Errorf("%s: el puerto recibió el filtro %+v; quiero %+v (100 por defecto + 1)", name, spy.filter, want)
		}
		if n := len(h.Auditor().Records()); n != 0 {
			t.Errorf("%s: G18 es R y dejó %d registros de auditoría, quiero 0", name, n)
		}
	}
}

// TestMountEventTelemetry_Body: la forma de cada evento, byte a byte: en el orden del puerto, el
// payload tal cual, el instante en UTC con su fracción y sin el tenant.
func TestMountEventTelemetry_Body(t *testing.T) {
	zone := time.FixedZone("-03", -3*3600)
	rows := []apipublica.EventTelemetryRow{
		{ID: 41, Name: "event_started", EventKind: "cart", Payload: []byte(`{"history_id":"h-1","kind":"cart"}`),
			CreatedAt: time.Date(2026, 8, 10, 9, 0, 0, 1234, zone)},
		{ID: 7, Name: "event_closed", EventKind: "", Payload: []byte(`{"z":[1,{"a":null}],"history_id":"h-2"}`),
			CreatedAt: time.Date(2026, 8, 10, 9, 0, 1, 0, zone)},
		{ID: 9007199254740993, Name: "event_switched", EventKind: "survey", Payload: nil,
			CreatedAt: time.Date(2026, 8, 10, 12, 0, 2, 500000000, time.UTC)},
	}
	want := `{"events":[` +
		`{"id":41,"name":"event_started","event_kind":"cart","payload":{"history_id":"h-1","kind":"cart"},"created_at":"2026-08-10T12:00:00.000001234Z"},` +
		`{"id":7,"name":"event_closed","event_kind":"","payload":{"z":[1,{"a":null}],"history_id":"h-2"},"created_at":"2026-08-10T12:00:01Z"},` +
		`{"id":9007199254740993,"name":"event_switched","event_kind":"survey","payload":null,"created_at":"2026-08-10T12:00:02.5Z"}` +
		`],"limit":100}`

	h := apipublicahelpertest.New(t)
	rec := telemetryGet(h, telemetryCara(h.Common(), &telemetryReaderSpy{rows: rows}), "")
	wantCode(t, "G18", rec, http.StatusOK)
	wantExactBody(t, "G18", rec, want)
	if strings.Contains(rec.Body.String(), tenantA) {
		t.Error("G18: la respuesta lleva el tenant; es el del token y no viaja")
	}
}

// TestMountEventTelemetry_Limit: el límite se valida y no se recorta: mal formado es 400, por
// encima de la cota es 422 y en ninguno de los dos se consulta el puerto. Con entradas
// adversarias.
func TestMountEventTelemetry_Limit(t *testing.T) {
	const maxInt64 = "9223372036854775807"
	cases := []struct {
		query string
		code  int
		limit int    // el aplicado, si 200
		msg   string // el error, si no
	}{
		{"", http.StatusOK, 100, ""},
		{"?limit=", http.StatusOK, 100, ""},
		{"?limit=1", http.StatusOK, 1, ""},
		{"?limit=2", http.StatusOK, 2, ""},
		{"?limit=007", http.StatusOK, 7, ""},
		{"?limit=%2B7", http.StatusOK, 7, ""},
		{"?limit=4999", http.StatusOK, 4999, ""},
		{"?limit=5000", http.StatusOK, 5000, ""},
		{"?limit=5&limit=abc", http.StatusOK, 5, ""},
		{"?limit=5001", http.StatusUnprocessableEntity, 0, fmt.Sprintf(telemetryMsgTooMany, "5001")},
		{"?limit=" + maxInt64, http.StatusUnprocessableEntity, 0, fmt.Sprintf(telemetryMsgTooMany, maxInt64)},
		{"?limit=0", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=-1", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=-5001", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=abc", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=1.5", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=1e3", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=0x10", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=1_000", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=%D9%A3", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=%2010", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=10%20", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=+7", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=99999999999999999999", http.StatusBadRequest, 0, telemetryMsgLimit},
		{"?limit=abc&limit=5", http.StatusBadRequest, 0, telemetryMsgLimit},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{}
			rec := telemetryGet(h, telemetryCara(h.Common(), spy), tc.query)
			wantCode(t, tc.query, rec, tc.code)
			if tc.code != http.StatusOK {
				wantErrorBody(t, tc.query, rec, tc.msg)
				if spy.calls != 0 {
					t.Errorf("%q: el puerto recibió %d llamadas; quiero 0", tc.query, spy.calls)
				}
				return
			}
			wantExactBody(t, tc.query, rec, fmt.Sprintf(`{"events":[],"limit":%d}`, tc.limit))
			if spy.calls != 1 || spy.filter.Limit != tc.limit+1 {
				t.Errorf("%q: %d llamadas con Limit %d; quiero 1 con %d (el límite + 1)", tc.query, spy.calls, spy.filter.Limit, tc.limit+1)
			}
		})
	}
}

// TestMountEventTelemetry_Since: `since` es RFC3339 y llega al puerto como instante; uno
// ilegible se dice (400) en vez de ignorarse. Con entradas adversarias.
func TestMountEventTelemetry_Since(t *testing.T) {
	august := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	valid := []struct {
		query string
		want  time.Time
	}{
		{"?since=", time.Time{}},
		{"?since=2026-08-01T00:00:00Z", august},
		{"?since=2026-08-01T02:00:00%2B02:00", august},
		{"?since=2026-07-31T21:00:00-03:00", august},
		{"?since=2026-08-01T00:00:00.123456789Z", august.Add(123456789)},
		{"?since=2026-08-01T00:00:00Z&since=ayer", august},
		// El instante cero de Go es «sin filtro» para el puerto: equivale a no mandarlo.
		{"?since=0001-01-01T00:00:00Z", time.Time{}},
	}
	for _, tc := range valid {
		t.Run(tc.query, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{}
			wantCode(t, tc.query, telemetryGet(h, telemetryCara(h.Common(), spy), tc.query), http.StatusOK)
			if !spy.filter.Since.Equal(tc.want) || spy.filter.Since.IsZero() != tc.want.IsZero() {
				t.Errorf("%q: el puerto recibió Since %v; quiero %v", tc.query, spy.filter.Since, tc.want)
			}
			if !spy.filter.CursorAt.IsZero() || spy.filter.CursorID != 0 || spy.filter.Limit != 101 {
				t.Errorf("%q: el filtro %+v trae algo más que Since y el límite por defecto", tc.query, spy.filter)
			}
		})
	}
	invalid := []string{
		"?since=ayer",
		"?since=2026-08-01",
		"?since=2026-08-01T00:00:00",
		"?since=2026-08-01T00:00:00+02:00", // el «+» sin escapar llega como espacio
		"?since=2026-08-01%2000:00:00Z",
		"?since=2026-08-01T00:00:00Z%20",
		"?since=%202026-08-01T00:00:00Z",
		"?since=1785542400",
		"?since=2026-13-01T00:00:00Z",
		"?since=2026-02-30T00:00:00Z",
		"?since=2026-08-01T24:00:00Z",
		"?since=10000-01-01T00:00:00Z",
		"?since=2026-08-01T00:00:00Z%7C7",
		"?since=%D9%A2%D9%A0%D9%A2%D9%A6-08-01T00:00:00Z",
		"?since=ayer&since=2026-08-01T00:00:00Z",
	}
	for _, query := range invalid {
		t.Run(query, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{}
			rec := telemetryGet(h, telemetryCara(h.Common(), spy), query)
			wantCode(t, query, rec, http.StatusBadRequest)
			wantErrorBody(t, query, rec, telemetryMsgSince)
			if spy.calls != 0 {
				t.Errorf("%q: el puerto recibió %d llamadas; quiero 0", query, spy.calls)
			}
		})
	}
}

// TestMountEventTelemetry_Cursor: el cursor es base64 URL sin relleno de «instante|id»; uno
// legible llega al puerto como (CursorAt, CursorID) y cualquier otra cosa es 400. Con cursores
// adversarios: base64 roto, campos de más, ids fuera de rango, fechas imposibles.
func TestMountEventTelemetry_Cursor(t *testing.T) {
	const ts = "2026-08-10T12:00:00Z"
	valid := []struct {
		name string
		raw  string
		at   time.Time
		id   int64
	}{
		{"whole second", ts + "|7", telemetryBase, 7},
		{"nanoseconds", "2026-08-10T12:00:00.123456789Z|42", telemetryBase.Add(123456789), 42},
		{"other zone", "2026-08-10T14:00:00+02:00|1", telemetryBase, 1},
		{"zero id", ts + "|0", telemetryBase, 0},
		{"negative id is accepted", ts + "|-5", telemetryBase, -5},
		{"explicit plus sign", ts + "|+5", telemetryBase, 5},
		{"largest id", ts + "|9223372036854775807", telemetryBase, 9223372036854775807},
		{"smallest id", ts + "|-9223372036854775808", telemetryBase, -9223372036854775808},
		{"latest year", "9999-12-31T23:59:59Z|1", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 1},
		// El instante cero se acepta y llega en cero: para el puerto es «sin cursor».
		{"zero instant", "0001-01-01T00:00:00Z|9", time.Time{}, 9},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{}
			wantCode(t, tc.name, telemetryGet(h, telemetryCara(h.Common(), spy), "?cursor="+telemetryCursor(tc.raw)), http.StatusOK)
			if !spy.filter.CursorAt.Equal(tc.at) || spy.filter.CursorAt.IsZero() != tc.at.IsZero() || spy.filter.CursorID != tc.id {
				t.Errorf("cursor %q: el puerto recibió (%v, %d); quiero (%v, %d)", tc.raw, spy.filter.CursorAt, spy.filter.CursorID, tc.at, tc.id)
			}
			if !spy.filter.Since.IsZero() || spy.filter.Limit != 101 {
				t.Errorf("cursor %q: el filtro %+v trae algo más que el cursor y el límite por defecto", tc.raw, spy.filter)
			}
		})
	}

	t.Run("an empty cursor is no cursor", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		spy := &telemetryReaderSpy{}
		wantCode(t, "cursor vacío", telemetryGet(h, telemetryCara(h.Common(), spy), "?cursor="), http.StatusOK)
		if want := (apipublica.EventTelemetryFilter{Limit: 101}); !reflect.DeepEqual(spy.filter, want) {
			t.Errorf("cursor vacío: filtro %+v; quiero %+v", spy.filter, want)
		}
	})

	good := telemetryCursor(ts + "|7")
	invalid := map[string]string{
		"not base64":                    "no-es-base64-valido!!",
		"padded base64":                 base64.URLEncoding.EncodeToString([]byte(ts + "|77")),
		"standard alphabet":             base64.RawStdEncoding.EncodeToString([]byte("\xfb\xff|" + ts)),
		"truncated base64":              good[:len(good)-3] + "=",
		"leading space":                 "%20" + good,
		"trailing space":                good + "%20",
		"twice encoded":                 telemetryCursor(good),
		"bytes that are not text":       telemetryCursor("\xff\xfe\x00"),
		"no separator":                  telemetryCursor(ts),
		"only the separator":            telemetryCursor("|"),
		"empty instant":                 telemetryCursor("|7"),
		"empty id":                      telemetryCursor(ts + "|"),
		"one field too many":            telemetryCursor(ts + "|7|8"),
		"trailing separator":            telemetryCursor(ts + "|7|"),
		"fields swapped":                telemetryCursor("7|" + ts),
		"another separator":             telemetryCursor(ts + ",7"),
		"decimal id":                    telemetryCursor(ts + "|7.5"),
		"hex id":                        telemetryCursor(ts + "|0x7"),
		"id with a space":               telemetryCursor(ts + "| 7"),
		"id with non ascii digits":      telemetryCursor(ts + "|٣"),
		"id over int64":                 telemetryCursor(ts + "|9223372036854775808"),
		"id under int64":                telemetryCursor(ts + "|-9223372036854775809"),
		"instant without zone":          telemetryCursor("2026-08-10T12:00:00|7"),
		"instant with a space":          telemetryCursor("2026-08-10 12:00:00Z|7"),
		"date only":                     telemetryCursor("2026-08-10|7"),
		"unix seconds":                  telemetryCursor("1786363200|7"),
		"month 13":                      telemetryCursor("2026-13-10T12:00:00Z|7"),
		"february 30":                   telemetryCursor("2026-02-30T12:00:00Z|7"),
		"hour 24":                       telemetryCursor("2026-08-10T24:00:00Z|7"),
		"year 10000":                    telemetryCursor("10000-01-01T00:00:00Z|7"),
		"negative year":                 telemetryCursor("-0001-01-01T00:00:00Z|7"),
		"instant with surrounding text": telemetryCursor("x" + ts + "|7"),
	}
	for name, cursor := range invalid {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{}
			rec := telemetryGet(h, telemetryCara(h.Common(), spy), "?cursor="+cursor)
			wantCode(t, name, rec, http.StatusBadRequest)
			wantErrorBody(t, name, rec, telemetryMsgCursor)
			if spy.calls != 0 {
				t.Errorf("%s: el puerto recibió %d llamadas; quiero 0", name, spy.calls)
			}
		})
	}
}

// TestMountEventTelemetry_ValidationOrder: limit, since y cursor se validan en ese orden y la
// cota del 422 va después de los tres: con dos fallos responde el primero y un 400 gana al 422.
func TestMountEventTelemetry_ValidationOrder(t *testing.T) {
	cases := []struct {
		query string
		code  int
		msg   string
	}{
		{"?cursor=!!&since=ayer&limit=abc", http.StatusBadRequest, telemetryMsgLimit},
		{"?cursor=!!&since=ayer", http.StatusBadRequest, telemetryMsgSince},
		{"?cursor=!!&since=ayer&limit=5001", http.StatusBadRequest, telemetryMsgSince},
		{"?cursor=!!&limit=5001", http.StatusBadRequest, telemetryMsgCursor},
		{"?since=2026-08-01T00:00:00Z&limit=5001", http.StatusUnprocessableEntity, fmt.Sprintf(telemetryMsgTooMany, "5001")},
	}
	for _, tc := range cases {
		h := apipublicahelpertest.New(t)
		spy := &telemetryReaderSpy{}
		rec := telemetryGet(h, telemetryCara(h.Common(), spy), tc.query)
		wantCode(t, tc.query, rec, tc.code)
		wantErrorBody(t, tc.query, rec, tc.msg)
		if spy.calls != 0 {
			t.Errorf("%q: el puerto recibió %d llamadas; quiero 0", tc.query, spy.calls)
		}
	}
}

// TestMountEventTelemetry_AllParametersReachThePort: los tres parámetros juntos llegan al puerto
// cada uno en su campo.
func TestMountEventTelemetry_AllParametersReachThePort(t *testing.T) {
	h := apipublicahelpertest.New(t)
	spy := &telemetryReaderSpy{}
	query := "?limit=3&since=2026-08-01T00:00:00Z&cursor=" + telemetryCursor("2026-08-10T12:00:00.5Z|77")
	wantCode(t, query, telemetryGet(h, telemetryCara(h.Common(), spy), query), http.StatusOK)
	f := spy.filter
	if !f.Since.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) || !f.CursorAt.Equal(telemetryBase.Add(500*time.Millisecond)) || f.CursorID != 77 || f.Limit != 4 {
		t.Errorf("filtro %+v; quiero since 2026-08-01, cursor (12:00:00.5, 77) y Limit 4", f)
	}
}

// TestMountEventTelemetry_NextCursor: next_cursor sale SOLO si el puerto dio más filas que el
// límite; codifica la última fila SERVIDA (no la de sobra), al nanosegundo y en UTC; y devolverlo
// lleva al puerto exactamente a esa fila.
func TestMountEventTelemetry_NextCursor(t *testing.T) {
	zone := time.FixedZone("+05", 5*3600)
	rows := []apipublica.EventTelemetryRow{
		telemetryRow(11, "event_started", telemetryBase),
		telemetryRow(5, "event_switched", time.Date(2026, 8, 10, 17, 0, 1, 7, zone)), // 12:00:01.000000007Z
		telemetryRow(12, "event_closed", telemetryBase.Add(2*time.Second)),
		telemetryRow(13, "event_closed", telemetryBase.Add(3*time.Second)),
	}
	ids := func(t *testing.T, rec *httptest.ResponseRecorder) (got []int64, next string) {
		t.Helper()
		var body struct {
			Events []struct {
				ID int64 `json:"id"`
			} `json:"events"`
			NextCursor *string `json:"next_cursor"`
		}
		wantJSON(t, "página", rec, &body)
		for _, e := range body.Events {
			got = append(got, e.ID)
		}
		if body.NextCursor == nil {
			return got, "(ausente)"
		}
		return got, *body.NextCursor
	}
	secondRowCursor := telemetryCursor("2026-08-10T12:00:01.000000007Z|5")
	cases := []struct {
		name     string
		returned int // cuántas filas da el puerto
		query    string
		wantIDs  []int64
		wantNext string
	}{
		{"fewer rows than the limit", 1, "?limit=2", []int64{11}, "(ausente)"},
		{"exactly the limit", 2, "?limit=2", []int64{11, 5}, "(ausente)"},
		{"one more than the limit", 3, "?limit=2", []int64{11, 5}, secondRowCursor},
		{"a port that returns more than asked", 4, "?limit=2", []int64{11, 5}, secondRowCursor},
		{"limit one", 2, "?limit=1", []int64{11}, telemetryCursor("2026-08-10T12:00:00Z|11")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			spy := &telemetryReaderSpy{rows: rows[:tc.returned]}
			rec := telemetryGet(h, telemetryCara(h.Common(), spy), tc.query)
			wantCode(t, tc.name, rec, http.StatusOK)
			got, next := ids(t, rec)
			if !slices.Equal(got, tc.wantIDs) || next != tc.wantNext {
				t.Errorf("eventos %v y next_cursor %q; quiero %v y %q", got, next, tc.wantIDs, tc.wantNext)
			}
		})
	}

	t.Run("the returned cursor points at the last served row", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		spy := &telemetryReaderSpy{rows: rows[:3]}
		cara := telemetryCara(h.Common(), spy)
		_, next := ids(t, telemetryGet(h, cara, "?limit=2"))
		wantCode(t, "segunda página", telemetryGet(h, cara, "?limit=2&cursor="+next), http.StatusOK)
		if !spy.filter.CursorAt.Equal(rows[1].CreatedAt) || spy.filter.CursorID != 5 || spy.filter.Limit != 3 {
			t.Errorf("segunda página: el puerto recibió (%v, %d) con Limit %d; quiero la fila 5 al nanosegundo (%v) y Limit 3",
				spy.filter.CursorAt, spy.filter.CursorID, spy.filter.Limit, rows[1].CreatedAt)
		}
	})
}

// TestMountEventTelemetry_WithTheMemoryDouble: con el doble que pasa la suite del puerto, cada
// tenant pagina SOLO lo suyo, siguiendo next_cursor hasta que falta, sin repetir ni saltarse
// ninguna fila aunque haya instantes iguales; y `since` acota.
func TestMountEventTelemetry_WithTheMemoryDouble(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	insert := func(tenant, name string, offset time.Duration) int64 {
		t.Helper()
		id, err := mem.Insert(eventstelemetryhelpertest.FlowEvent{TenantID: tenant, Kind: "event", Name: name, Payload: `{"kind":"cart"}`, CreatedAt: telemetryBase.Add(offset)})
		if err != nil {
			t.Fatalf("sembrando %s de %s: %v", name, tenant, err)
		}
		return id
	}
	wantA := make([]int64, 0, 5)
	for _, offset := range []time.Duration{0, time.Second, time.Second, time.Second, 2 * time.Second} {
		wantA = append(wantA, insert(tenantA, "event_started", offset))
		insert(tenantB, "event_started", offset)
		insert(tenantA, "survey_answer", offset)
	}

	h := apipublicahelpertest.New(t)
	cara := telemetryCara(h.Common(), mem)
	walk := func(token, query string) []int64 {
		t.Helper()
		var walked []int64
		cursor := ""
		for page := 0; page <= len(wantA); page++ {
			rec := h.Call(cara, token, http.MethodGet, telemetryTarget+query+cursor, "")
			wantCode(t, query+cursor, rec, http.StatusOK)
			var body struct {
				Events []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"events"`
				NextCursor string `json:"next_cursor"`
			}
			wantJSON(t, query+cursor, rec, &body)
			for _, e := range body.Events {
				walked = append(walked, e.ID)
			}
			if body.NextCursor == "" {
				return walked
			}
			cursor = "&cursor=" + body.NextCursor
		}
		t.Fatalf("%s: el cursor no se acaba; recorrido %v", query, walked)
		return nil
	}

	if got := walk(h.With(tenantA, telemetryPerm), "?limit=2&tenant_id="+tenantB); !slices.Equal(got, wantA) {
		t.Errorf("tenantA en páginas de 2 recorre %v; quiero %v, lo suyo y cada fila una vez", got, wantA)
	}
	if got := walk(h.With(tenantA, telemetryPerm), "?limit=5"); !slices.Equal(got, wantA) {
		t.Errorf("tenantA en una página justa recorre %v; quiero %v", got, wantA)
	}
	if got := walk(h.With(tenantA, telemetryPerm), "?limit=2&since=2026-08-10T12:00:01Z"); !slices.Equal(got, wantA[1:]) {
		t.Errorf("tenantA desde 12:00:01 recorre %v; quiero %v", got, wantA[1:])
	}
	if got := walk(h.With(tenantB, telemetryPerm), "?limit=3"); len(got) != len(wantA) || slices.ContainsFunc(got, func(id int64) bool { return slices.Contains(wantA, id) }) {
		t.Errorf("tenantB recorre %v; quiero sus %d filas y ninguna de tenantA (%v)", got, len(wantA), wantA)
	}
}

// TestMountEventTelemetry_ReaderErrorIs500: el 500 no repite el error del puerto.
func TestMountEventTelemetry_ReaderErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	spy := &telemetryReaderSpy{
		rows: []apipublica.EventTelemetryRow{telemetryRow(1, "event_started", telemetryBase)},
		err:  errors.New("postgres://usuario:secreto@host/bd: conexión rechazada"),
	}
	rec := telemetryGet(h, telemetryCara(h.Common(), spy), "")
	wantCode(t, "G18 con el puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "G18 con el puerto caído", rec, telemetryMsgFailed)
	if spy.tenant != tenantA {
		t.Errorf("el puerto recibió el tenant %q, quiero el del token %q", spy.tenant, tenantA)
	}
}
