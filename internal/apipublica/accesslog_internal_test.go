package apipublica

// accesslog_internal_test.go — los dos 200 IMPLÍCITOS del access-log (chain.go): el del handler
// que escribe el cuerpo sin llamar a WriteHeader y el del que no escribe nada. Ningún handler de
// la cara hace ninguna de las dos cosas (writeJSON pone siempre el código), así que por una ruta
// montada no se alcanzan: se afirman sobre accessLog a pelo, con el logger de
// writedeadline_test.go.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// accessLogStatusOf sirve una petición por accessLog y devuelve el `status` de su única línea.
func accessLogStatusOf(t *testing.T, what string, inner http.HandlerFunc) any {
	t.Helper()
	log := &writeDeadlineLog{}
	rec := httptest.NewRecorder()
	accessLog(log, inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/algo", nil))
	if len(log.lines) != 1 || log.lines[0].level != "info" || log.lines[0].msg != "petición pública" {
		t.Fatalf("%s: líneas = %+v; quiero una sola, info «petición pública»", what, log.lines)
	}
	return log.lines[0].fields["status"]
}

// TestAccessLog_ImplicitOK: la línea dice el código que el cliente recibe también cuando el
// handler no lo puso. Un Write sin WriteHeader es un 200 (net/http), y el WriteHeader TARDÍO que
// viniera después ya no lo cambia: se apunta el primero. Un handler mudo es un 200 vacío.
func TestAccessLog_ImplicitOK(t *testing.T) {
	t.Run("write_without_header", func(t *testing.T) {
		got := accessLogStatusOf(t, "Write sin WriteHeader", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("hola")); err != nil {
				t.Errorf("Write: %v", err)
			}
			// net/http ignora este código (la cabecera ya salió con el 200): el log también.
			w.WriteHeader(http.StatusTeapot)
		})
		if got != http.StatusOK {
			t.Errorf("status = %v, quiero %d (el 200 implícito del primer Write)", got, http.StatusOK)
		}
	})
	t.Run("silent_handler", func(t *testing.T) {
		got := accessLogStatusOf(t, "handler mudo", func(http.ResponseWriter, *http.Request) {})
		if got != http.StatusOK {
			t.Errorf("status = %v, quiero %d (el 200 vacío que emite el servidor)", got, http.StatusOK)
		}
	})
}
