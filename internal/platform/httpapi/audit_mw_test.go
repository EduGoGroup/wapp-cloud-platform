package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// fakeAuditor captura el último evento registrado por AuditMiddleware.
type fakeAuditor struct {
	calls int
	last  in.AuditInput
	err   error
}

func (f *fakeAuditor) Record(_ context.Context, ev in.AuditInput) error {
	f.calls++
	f.last = ev
	return f.err
}

// serveAudited monta AuditMiddleware sobre un handler que responde `status`, con
// una Identity de operador inyectada, y devuelve el auditor para inspección.
func serveAudited(status int, withID bool) *fakeAuditor {
	aud := &fakeAuditor{}
	h := httpapi.AuditMiddleware(aud, "flows.create", "flow", nil)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/admin/flows", nil)
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(),
			httpapi.Identity{TenantID: "t-1", Subject: "user-9"}))
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return aud
}

func TestAuditMiddleware_Success(t *testing.T) {
	aud := serveAudited(http.StatusCreated, true)
	if aud.calls != 1 {
		t.Fatalf("Record llamado %d veces, want 1", aud.calls)
	}
	ev := aud.last
	if ev.TenantID != "t-1" || ev.Actor != "user-9" {
		t.Fatalf("identidad: got tenant=%q actor=%q, want t-1/user-9", ev.TenantID, ev.Actor)
	}
	if ev.Action != "flows.create" || ev.Resource != "flow" {
		t.Fatalf("acción/recurso: got %q/%q", ev.Action, ev.Resource)
	}
	if ev.Result != "success" {
		t.Fatalf("result = %q, want success (status 201)", ev.Result)
	}
	if got := ev.Meta["status"]; got != http.StatusCreated {
		t.Fatalf("meta.status = %v, want %d", got, http.StatusCreated)
	}
}

func TestAuditMiddleware_Failure(t *testing.T) {
	aud := serveAudited(http.StatusBadRequest, true)
	if aud.last.Result != "failure" {
		t.Fatalf("result = %q, want failure (status 400)", aud.last.Result)
	}
}

// TestAuditMiddleware_TargetTenant verifica que si un handler de plataforma
// publica el target_tenant_id, este se incluye en meta (D-056.9 / T2.5).
func TestAuditMiddleware_TargetTenant(t *testing.T) {
	aud := &fakeAuditor{}
	h := httpapi.AuditMiddleware(aud, "tenants.revoke.any", "tenant", nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpapi.SetAuditTargetTenant(r.Context(), "target-tenant-123")
			w.WriteHeader(http.StatusOK)
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", nil)
	req = req.WithContext(httpapi.WithIdentity(req.Context(),
		httpapi.Identity{TenantID: "platform-tenant", Subject: "platform-operator"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if aud.calls != 1 {
		t.Fatalf("Record llamado %d veces, want 1", aud.calls)
	}
	ev := aud.last
	if ev.Meta["target_tenant_id"] != "target-tenant-123" {
		t.Fatalf("meta.target_tenant_id = %v, want target-tenant-123", ev.Meta["target_tenant_id"])
	}
}
