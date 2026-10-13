package apipublica

// intakes_filter_internal_test.go — los dos auxiliares de intakes_filter.go, vistos sin pasar por
// una ruta (05 E-4, P6: nacen con el verde). Las promesas de la query entera las prueba
// intakes_filter_test.go a través de G1; aquí va la regla que desde fuera se diagnostica mal: qué
// le pasa a una fecha según sea suelta o un instante, y según sea `from` o `to`.

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// TestParseFilterTime: una fecha suelta es medianoche UTC, y con endOfDay es la medianoche
// SIGUIENTE (el rango es [from, to): sin eso `to=2026-08-06` no traería nada del día 6). Un
// RFC 3339 se respeta tal cual, con o sin endOfDay. Lo demás es un error, nunca «sin cota».
func TestParseFilterTime(t *testing.T) {
	utc := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	cases := []struct {
		raw      string
		endOfDay bool
		want     time.Time
		fails    bool
	}{
		{raw: "", endOfDay: false},
		{raw: "", endOfDay: true},
		{raw: "2026-08-06", endOfDay: false, want: utc(2026, 8, 6, 0)},
		{raw: "2026-08-06", endOfDay: true, want: utc(2026, 8, 7, 0)},
		// El día siguiente cruza el mes, el año y el 29 de febrero.
		{raw: "2026-08-31", endOfDay: true, want: utc(2026, 9, 1, 0)},
		{raw: "2026-12-31", endOfDay: true, want: utc(2027, 1, 1, 0)},
		{raw: "2028-02-28", endOfDay: true, want: utc(2028, 2, 29, 0)},
		{raw: "2026-02-28", endOfDay: true, want: utc(2026, 3, 1, 0)},
		// Quien escribe un instante sabe lo que pide: no se le suma nada.
		{raw: "2026-08-06T10:00:00Z", endOfDay: true, want: utc(2026, 8, 6, 10)},
		{raw: "2026-08-06T10:00:00-03:00", endOfDay: false, want: utc(2026, 8, 6, 13)},
		{raw: "2026-08-06T00:00:00+14:00", endOfDay: true, want: utc(2026, 8, 5, 10)},
		{raw: "2026-02-29", fails: true},
		{raw: "2026-8-6", fails: true},
		{raw: "06-08-2026", fails: true},
		{raw: "2026-08-06 ", fails: true},
		{raw: " 2026-08-06", fails: true},
		{raw: "2026-08-06T10:00:00", fails: true},
		{raw: "2026-08-06 10:00:00Z", fails: true},
		{raw: "٢٠٢٦-٠٨-٠٦", fails: true},
		{raw: "hoy", fails: true},
		{raw: "0", fails: true},
	}
	for _, tc := range cases {
		got, err := parseFilterTime(tc.raw, tc.endOfDay)
		if tc.fails {
			if err == nil {
				t.Errorf("parseFilterTime(%q, %v) = %s sin error; quiero un error", tc.raw, tc.endOfDay, got)
			}
			continue
		}
		if err != nil || !got.Equal(tc.want) || got.IsZero() != tc.want.IsZero() {
			t.Errorf("parseFilterTime(%q, %v) = %s, %v; quiero %s", tc.raw, tc.endOfDay, got, err, tc.want)
		}
	}
}

// TestParseIntakeFilter: la firma que comparten la bandeja, el export y el resumen. Con todo
// bien el mensaje es vacío; con un valor malo el filtro vuelve en CERO —no a medio rellenar— y
// el mensaje dice cuál.
func TestParseIntakeFilter(t *testing.T) {
	parse := func(query string) (string, []string, bool, int, int) {
		f, msg := parseIntakeFilter(httptest.NewRequest(http.MethodGet, "/api/v1/intakes"+query, nil))
		return msg, f.Statuses, f.Orphan, f.Page, f.PageSize
	}

	msg, statuses, orphan, page, size := parse("?status=closed&status=open&orphan=T&page=2")
	if msg != "" || !slices.Equal(statuses, []string{"confirmed", "open"}) || !orphan || page != 2 || size != 50 {
		t.Errorf("query buena: msg %q, estados %q, orphan %v, página %d/%d", msg, statuses, orphan, page, size)
	}

	for query, want := range map[string]string{
		"?status=open&from=x":   "from inválido: usa YYYY-MM-DD o RFC3339",
		"?status=open&to=x":     "to inválido: usa YYYY-MM-DD o RFC3339",
		"?status=open&status=x": "status desconocido",
		"?status=open&sort=x":   "sort desconocido: usa newest u oldest",
		"?status=open&orphan=x": "orphan inválido: usa true o false",
	} {
		msg, statuses, orphan, page, size := parse(query)
		if msg != want {
			t.Errorf("%s: mensaje %q, quiero %q", query, msg, want)
		}
		if statuses != nil || orphan || page != 0 || size != 0 {
			t.Errorf("%s: con error el filtro trae estados %q, orphan %v, página %d/%d; quiero el cero", query, statuses, orphan, page, size)
		}
	}
}
