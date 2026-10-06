package apipublica

// limits_test.go — cubre limits.go, que no exporta nada: test INTERNO que nace con el verde de
// su fichero (05 E-4, P6). Lo que se fija es la FORMA única del 413 (REQ-16): la frase nombra la
// cifra, en bytes y sin redondear, y `max_bytes` la repite para que la UI no parsee prosa.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTooLargeMessage_NamesSubjectAndBytes(t *testing.T) {
	cases := []struct {
		subject string
		max     int64
		want    string
	}{
		{"la config", 65536, "la config excede el tamaño máximo de 65536 bytes"},
		{"el archivo", 5 << 20, "el archivo excede el tamaño máximo de 5242880 bytes"},
		{"el cuerpo", 0, "el cuerpo excede el tamaño máximo de 0 bytes"},
	}
	for _, tc := range cases {
		if got := tooLargeMessage(tc.subject, tc.max); got != tc.want {
			t.Errorf("tooLargeMessage(%q, %d) = %q, quiero %q", tc.subject, tc.max, got, tc.want)
		}
	}
}

// TestTooLarge_BodyCarriesTheSameFigure: la frase y el campo dicen el MISMO número.
func TestTooLarge_BodyCarriesTheSameFigure(t *testing.T) {
	got := tooLarge("el documento", 1048576)
	want := tooLargeBody{Error: "el documento excede el tamaño máximo de 1048576 bytes", MaxBytes: 1048576}
	if got != want {
		t.Errorf("tooLarge = %+v, quiero %+v", got, want)
	}
}

// TestWriteTooLarge: 413, JSON, `error` primero y `max_bytes` como número.
func TestWriteTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTooLarge(rec, "el contenido", 262144)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("código %d, quiero 413", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, quiero application/json", ct)
	}
	wantBody(t, "writeTooLarge", rec.Body.String(),
		`{"error":"el contenido excede el tamaño máximo de 262144 bytes","max_bytes":262144}`)
}
