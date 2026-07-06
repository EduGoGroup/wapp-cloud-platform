package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// doSessionRole ejecuta el handler de rol vía un mux con el patrón real (para que
// r.PathValue("id") funcione), con una Identity del tenant inyectada en el contexto
// como haría Authenticate. withID=false ejercita el 401.
func doSessionRole(store admin.SessionRoleStore, tenant, sessionID, body string, withID bool) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("POST /admin/sessions/{id}/role", admin.SetSessionRoleHandler(store))
	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+sessionID+"/role", strings.NewReader(body))
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func seedSession(t *testing.T, repo *fleet.MemoryRepository, tenant, session string) {
	t.Helper()
	if err := repo.MarkOnline(context.Background(), tenant, "edge-1", session); err != nil {
		t.Fatalf("seed sesión: %v", err)
	}
}

// TestSetSessionRole_OK_Passive: el dueño fija el rol passive → 200 y persiste.
func TestSetSessionRole_OK_Passive(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionRole(repo, ctxTenant, "sess-1", `{"role":"passive"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SessionID != "sess-1" || out.Role != "passive" {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RolePassive {
		t.Fatalf("no persistió passive: %q", s.Role)
	}
}

// TestSetSessionRole_401_NoIdentity: sin Identity en el contexto → 401.
func TestSetSessionRole_401_NoIdentity(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	rec := doSessionRole(repo, ctxTenant, "sess-1", `{"role":"passive"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

// TestSetSessionRole_400_InvalidBody: JSON roto o rol desconocido → 400.
func TestSetSessionRole_400_InvalidBody(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	for name, body := range map[string]string{
		"json roto":         `{`,
		"rol desconocido":   `{"role":"supervisor"}`,
		"rol vacío":         `{"role":""}`,
		"rol online (typo)": `{"role":"online"}`,
	} {
		rec := doSessionRole(repo, ctxTenant, "sess-1", body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestSetSessionRole_404_CrossTenant: un tenant AJENO no puede tocar la sesión de
// otro (aislamiento multi-tenant, INV-8) → 404 opaco, y la sesión del dueño queda
// intacta (sigue bot).
func TestSetSessionRole_404_CrossTenant(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionRole(repo, "otro-tenant", "sess-1", `{"role":"passive"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	// La sesión del dueño NO cambió (nadie ajeno la mutó).
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RoleBot {
		t.Fatalf("aislamiento roto: la sesión del dueño cambió a %q", s.Role)
	}
}

// TestSetSessionRole_404_Unknown: una sesión inexistente para el tenant → 404.
func TestSetSessionRole_404_Unknown(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	rec := doSessionRole(repo, ctxTenant, "no-existe", `{"role":"bot"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}
