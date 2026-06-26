package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-shared/health"
)

func TestHealthHandler_Healthy(t *testing.T) {
	checker := NewHealthChecker()
	handler := HealthHandler(checker)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("cuerpo JSON inválido: %v", err)
	}

	if body.Status != health.StatusHealthy {
		t.Errorf("status JSON: got %q, want %q", body.Status, health.StatusHealthy)
	}

	self, ok := body.Checks["self"]
	if !ok {
		t.Fatalf("falta el check \"self\" en la respuesta")
	}
	if self.Status != health.StatusHealthy {
		t.Errorf("check self: got %q, want %q", self.Status, health.StatusHealthy)
	}
}
