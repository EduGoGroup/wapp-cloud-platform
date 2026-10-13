package apipublica

// response_test.go — cubre response.go, que no exporta nada: por eso es un test INTERNO
// (package apipublica) y nace con el verde de su fichero (05 E-4, P6). Una aserción por promesa
// de los comentarios: el descarte y la devolución del fallo de escritura (incidente del
// 2026-08-06), el 500 ante un valor que no se codifica, el cuerpo canónico {"error": …} y los
// valores por defecto de parseIntQuery.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// brokenWriter es un ResponseWriter cuyo Write falla, como con el deadline de escritura vencido.
type brokenWriter struct {
	header http.Header
	code   int
}

var errBrokenWrite = errors.New("write: deadline vencido")

func (w *brokenWriter) Header() http.Header       { return w.header }
func (w *brokenWriter) WriteHeader(code int)      { w.code = code }
func (w *brokenWriter) Write([]byte) (int, error) { return 0, errBrokenWrite }

func newBrokenWriter() *brokenWriter { return &brokenWriter{header: http.Header{}} }

// unencodable es un valor que encoding/json no sabe codificar (un canal).
func unencodable() map[string]any { return map[string]any{"x": make(chan int)} }

func wantBody(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: cuerpo %q, quiero %q", what, got, want)
	}
}

func TestWriteJSON_WritesCodeTypeAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	if rec.Code != http.StatusCreated {
		t.Errorf("código %d, quiero 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, quiero application/json", ct)
	}
	wantBody(t, "writeJSON", rec.Body.String(), `{"n":1}`)
}

// TestWriteJSON_DiscardsWriteError: el descarte es el contrato; la cabecera sí llega.
func TestWriteJSON_DiscardsWriteError(t *testing.T) {
	w := newBrokenWriter()
	writeJSON(w, http.StatusOK, map[string]int{"n": 1})
	if w.code != http.StatusOK {
		t.Errorf("con el Write roto, la cabecera fue %d, quiero 200", w.code)
	}
}

func TestWriteJSONErr_ReturnsWriteError(t *testing.T) {
	if err := writeJSONErr(newBrokenWriter(), http.StatusOK, map[string]int{"n": 1}); !errors.Is(err, errBrokenWrite) {
		t.Errorf("writeJSONErr con el Write roto devolvió %v, quiero el error del Write", err)
	}
	rec := httptest.NewRecorder()
	if err := writeJSONErr(rec, http.StatusAccepted, []string{"a"}); err != nil {
		t.Errorf("writeJSONErr con escritura sana devolvió %v, quiero nil", err)
	}
	if rec.Code != http.StatusAccepted || rec.Body.String() != `["a"]` {
		t.Errorf("writeJSONErr: %d %q, quiero 202 [\"a\"]", rec.Code, rec.Body.String())
	}
}

func TestWriteJSONErr_UnencodableIs500(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(w http.ResponseWriter) error
	}{
		{"writeJSONErr", func(w http.ResponseWriter) error { return writeJSONErr(w, http.StatusOK, unencodable()) }},
		{"writeJSON", func(w http.ResponseWriter) error { writeJSON(w, http.StatusOK, unencodable()); return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := tc.write(rec)
			if tc.name == "writeJSONErr" && err == nil {
				t.Error("writeJSONErr con un valor que no se codifica devolvió nil, quiero el error de codificación")
			}
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("código %d, quiero 500 (y no la cabecera pedida)", rec.Code)
			}
			wantBody(t, tc.name, rec.Body.String(), "codificando respuesta\n")
		})
	}
}

func TestWriteError_CanonicalBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusForbidden, "permiso denegado")
	if rec.Code != http.StatusForbidden {
		t.Errorf("código %d, quiero 403", rec.Code)
	}
	wantBody(t, "writeError", rec.Body.String(), `{"error":"permiso denegado"}`)
}

func TestErrorBody_OnlyErrorKey(t *testing.T) {
	got := errorBody("x")
	if len(got) != 1 || got["error"] != "x" {
		t.Errorf("errorBody(\"x\") = %v, quiero {error: x}", got)
	}
}

func TestParseIntQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"missing_is_default", "", 7},
		{"empty_is_default", "?n=", 7},
		{"valid", "?n=42", 42},
		{"explicit_zero_is_zero", "?n=0", 0},
		{"negative_is_default", "?n=-1", 7},
		{"decimal_is_default", "?n=1.5", 7},
		{"letters_are_default", "?n=diez", 7},
		{"non_ascii_digits_are_default", "?n=%D9%A3", 7},
		{"fullwidth_digit_is_default", "?n=%EF%BC%91", 7},
		{"overflow_is_default", "?n=99999999999999999999999", 7},
		{"other_key_ignored", "?m=3", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x"+tc.query, nil)
			if got := parseIntQuery(r, "n", 7); got != tc.want {
				t.Errorf("parseIntQuery(%q) = %d, quiero %d", tc.query, got, tc.want)
			}
		})
	}
}
