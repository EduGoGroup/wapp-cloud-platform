package apipublica

// instants_test.go — cubre instants.go, que no exporta nada: por eso es un test INTERNO
// (package apipublica) y nace con su fichero (05 E-4, P6). Una aserción por promesa del
// comentario: el cero viaja vacío, la zona se normaliza a UTC y el formato es RFC3339 sin
// fracción de segundo.

import (
	"testing"
	"time"
)

func TestFormatInstant(t *testing.T) {
	madrid := time.FixedZone("CEST", 2*60*60)

	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero value travels empty", time.Time{}, ""},
		{"utc instant keeps its wall clock", time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC), "2026-10-08T09:30:00Z"},
		{"other zone is normalized to utc", time.Date(2026, 10, 8, 1, 30, 0, 0, madrid), "2026-10-07T23:30:00Z"},
		{"sub-second precision is dropped", time.Date(2026, 10, 8, 9, 30, 0, 987654321, time.UTC), "2026-10-08T09:30:00Z"},
		// El cero «desplazado» sigue siendo el cero: IsZero mira el instante, no la zona.
		{"zero value in another zone travels empty", time.Time{}.In(madrid), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatInstant(tc.in); got != tc.want {
				t.Errorf("formatInstant(%v) = %q; se esperaba %q", tc.in, got, tc.want)
			}
		})
	}
}
