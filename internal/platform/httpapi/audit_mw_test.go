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
	// T2.5: una ruta que NO publica target_tenant_id (este handler no llama a
	// SetAuditTargetTenant) escribe EXACTAMENTE el mismo meta que antes,
	// comprobado campo a campo -- no solo que "status" esté, sino que sea lo
	// ÚNICO. Sin este assert, un meta con claves de más (una fuga de PII, un
	// campo de depuración olvidado) pasaría igual.
	if len(ev.Meta) != 1 {
		t.Fatalf("len(ev.Meta) = %d, want 1 (solo 'status'); meta = %v", len(ev.Meta), ev.Meta)
	}
}

func TestAuditMiddleware_Failure(t *testing.T) {
	aud := serveAudited(http.StatusBadRequest, true)
	if aud.last.Result != "failure" {
		t.Fatalf("result = %q, want failure (status 400)", aud.last.Result)
	}
}

// TestAuditMiddleware_NilRecorder: sin auditor el middleware es transparente
// (no entra en pánico ni altera la respuesta). M-05: se eliminó en c858e43 al
// añadir TestAuditMiddleware_TargetTenant en su lugar, pero el código que
// prueba (audit_mw.go: `if rec == nil { return }`) sigue vivo y se quedó sin
// cobertura -- ninguno de los tests que sobrevivieron pasa `nil` como
// auditor. Recuperado tal cual, más el caso `log` nil (también contemplado
// por AuditMiddleware) para que ambos "opcionales" del middleware queden
// bajo prueba a la vez.
func TestAuditMiddleware_NilRecorder(t *testing.T) {
	h := httpapi.AuditMiddleware(nil, "x", "y", nil)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
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
