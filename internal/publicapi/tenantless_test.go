package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

type fakeAuditReader struct{}

func (fakeAuditReader) ListAudit(_ context.Context, _ string, _, _ int) ([]domain.AuditEvent, error) {
	return nil, nil
}

// TestTenantlessToken_RechazadoEnRutasDeNegocioYAceptadoEnWhoami cubre el criterio
// autoritativo de T3.3 (Plan 056 / D-056.12):
// Un token emitido con GenerateTenantlessToken (sujeto acreditado pero sin empresa)
// debe ser rechazado con 403 en todas las rutas de negocio (/flows, /messages, /audit),
// y aceptado con 200 en /api/v1/auth/whoami con tenant_id vacío.
func TestTenantlessToken_RechazadoEnRutasDeNegocioYAceptadoEnWhoami(t *testing.T) {
	t.Parallel()

	jwt := sharedjwt.NewJWTManager(tokenSecret, tokenIssuer)
	mw := httpapi.NewMiddleware(jwt, nil)
	mux := http.NewServeMux()

	deps := publicapi.Deps{
		Sender: &fakeSender{ack: okAck()},
		Audit:  fakeAuditReader{},
	}
	publicapi.Register(mux, deps, mw, noopAuditor{}, nil)
	mux.Handle("/api/v1/auth/whoami", mw.Authenticate(httpapi.WhoAmIHandler()))

	const userID = "usr-sin-empresa-1234"
	tenantlessToken, _, err := jwt.GenerateTenantlessToken(userID, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateTenantlessToken: %v", err)
	}

	execReq := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tenantlessToken)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. GET /api/v1/flows -> 403 Forbidden
	t.Run("GET_flows_403", func(t *testing.T) {
		rec := execReq(http.MethodGet, "/api/v1/flows", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET /api/v1/flows: code = %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
	})

	// 2. POST /api/v1/messages -> 403 Forbidden
	t.Run("POST_messages_403", func(t *testing.T) {
		rec := execReq(http.MethodPost, "/api/v1/messages", `{"session_id":"sess-1","to":"+123456789","text":"hola"}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST /api/v1/messages: code = %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
	})

	// 3. GET /api/v1/audit -> 403 Forbidden
	t.Run("GET_audit_403", func(t *testing.T) {
		rec := execReq(http.MethodGet, "/api/v1/audit", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET /api/v1/audit: code = %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
	})

	// 4. GET /api/v1/auth/whoami -> 200 OK con tenant_id vacío
	t.Run("GET_whoami_200_tenantless", func(t *testing.T) {
		rec := execReq(http.MethodGet, "/api/v1/auth/whoami", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/auth/whoami: code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var out struct {
			TenantID string   `json:"tenant_id"`
			Subject  string   `json:"subject"`
			Roles    []string `json:"roles"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal whoami: %v", err)
		}
		if out.TenantID != "" {
			t.Errorf("tenant_id = %q, want vacío", out.TenantID)
		}
		if out.Subject != userID {
			t.Errorf("subject = %q, want %q", out.Subject, userID)
		}
		if len(out.Roles) != 0 {
			t.Errorf("roles = %v, want vacío", out.Roles)
		}
	})
}
