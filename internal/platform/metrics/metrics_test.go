package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape devuelve el cuerpo de /metrics del registry propio.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.PromHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestInstrumentHTTP_CountsRequestsAndLogin verifica que /metrics expone los
// contadores y que la ruta de login deriva el acuse ok/fallo del status.
func TestInstrumentHTTP_CountsRequestsAndLogin(t *testing.T) {
	m := New()

	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // login fallido
	}))
	mux.Handle("/api/v1/flows/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h := m.InstrumentHTTP("public", mux)

	// Login fallido.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil))
	// Ruta con {id}: el patrón (no el valor) debe ser la etiqueta.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/flows/abc-123", nil))

	body := scrape(t, m)

	if !strings.Contains(body, "wapp_http_requests_total") {
		t.Fatal("falta wapp_http_requests_total en /metrics")
	}
	if !strings.Contains(body, `wapp_auth_logins_total{result="failure"} 1`) {
		t.Errorf("no se contó el login fallido:\n%s", body)
	}
	// CERO PII: la etiqueta route usa el PATRÓN {id}, nunca el valor real "abc-123".
	if strings.Contains(body, "abc-123") {
		t.Error("la métrica NO debe exponer el valor real del path (PII/cardinalidad)")
	}
	if !strings.Contains(body, `/api/v1/flows/{id}`) {
		t.Errorf("la etiqueta route debería ser el patrón /api/v1/flows/{id}:\n%s", body)
	}
}

// TestRateLimitAndReceiptCounters verifica los contadores auxiliares.
func TestRateLimitAndReceiptCounters(t *testing.T) {
	m := New()
	m.RateLimitHit("public")
	m.RateLimitHit("login")
	m.Receipt("delivered")
	m.Receipt("read")
	m.Receipt("delivered")

	body := scrape(t, m)
	for _, want := range []string{
		`wapp_ratelimit_hits_total{scope="public"} 1`,
		`wapp_ratelimit_hits_total{scope="login"} 1`,
		`wapp_receipts_total{status="delivered"} 2`,
		`wapp_receipts_total{status="read"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en /metrics:\n%s", want, body)
		}
	}
}

// TestNilSafe garantiza que los métodos sobre un *Metrics nil no rompen.
func TestNilSafe(t *testing.T) {
	var m *Metrics
	m.RateLimitHit("public")
	m.Receipt("read")
	h := m.InstrumentHTTP("public", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil metrics no debería alterar el handler: got %d", rec.Code)
	}
}
