package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// doTrigger ejecuta h con una Identity de operador (tenant) ya inyectada en el
// contexto (como haría Authenticate en producción). withID=false ejercita el 401.
func doTrigger(h http.Handler, method, target, tenant, body string, withID bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateTrigger_OK_Keyword(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"keyword","keyword":"Pedido","match_type":"exact","flow_id":"carrito","priority":5}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		TriggerID string `json:"trigger_id"`
		Kind      string `json:"kind"`
		Keyword   string `json:"keyword"`
		MatchType string `json:"match_type"`
		FlowID    string `json:"flow_id"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TriggerID == "" || out.Kind != "keyword" || out.FlowID != "carrito" || !out.Enabled {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
	// Persistido bajo el tenant del token (no del cuerpo).
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].TenantID != ctxTenant {
		t.Fatalf("no persistió bajo el tenant del token: %+v", rules)
	}
}

func TestCreateTrigger_401_NoIdentity(t *testing.T) {
	store := trigger.NewMemoryStore()
	rec := doTrigger(admin.CreateTriggerHandler(store), http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"keyword","keyword":"x","flow_id":"f"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

func TestCreateTrigger_400_InvalidBody(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)
	cases := map[string]string{
		"json roto":            `{`,
		"kind desconocido":     `{"kind":"regex","keyword":"x","flow_id":"f"}`,
		"match_type inválido":  `{"kind":"keyword","keyword":"x","flow_id":"f","match_type":"prefix"}`,
		"keyword sin keyword":  `{"kind":"keyword","flow_id":"f"}`,
		"keyword sin flow_id":  `{"kind":"keyword","keyword":"x"}`,
		"fallback sin flow_id": `{"kind":"fallback"}`,
		"escape sin keyword":   `{"kind":"escape"}`,
	}
	for name, body := range cases {
		rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant, body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateTrigger_OK_FallbackAndEscape(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)
	// fallback: sin keyword, con flow_id.
	if rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"menu"}`, true); rec.Code != http.StatusCreated {
		t.Fatalf("fallback code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	// escape: con keyword, sin flow_id.
	if rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"escape","keyword":"SALIR"}`, true); rec.Code != http.StatusCreated {
		t.Fatalf("escape code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateTrigger_OK_EscapeWithMessage: un escape con message se acepta (201),
// lo devuelve en la respuesta y lo persiste (Plan 019 · T4b).
func TestCreateTrigger_OK_EscapeWithMessage(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"escape","keyword":"SALIR","message":"Hasta pronto"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Message != "Hasta pronto" {
		t.Fatalf("respuesta debe traer el message, got %q", out.Message)
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].Message != "Hasta pronto" {
		t.Fatalf("message no persistió: %+v", rules)
	}
}

// TestCreateTrigger_400_MessageOnNonEscape: message en kind ≠ escape se rechaza (400).
func TestCreateTrigger_400_MessageOnNonEscape(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"keyword","keyword":"pedido","flow_id":"carrito","message":"nope"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListTriggers_OK_TenantScoped(t *testing.T) {
	store := trigger.NewMemoryStore()
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: "otro-tenant", Kind: trigger.KindKeyword, Keyword: "hola",
		MatchType: trigger.MatchExact, FlowID: "x", Enabled: true,
	}); err != nil {
		t.Fatalf("seed otro: %v", err)
	}

	rec := doTrigger(admin.ListTriggersHandler(store), http.MethodGet, "/admin/triggers", ctxTenant, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		Keyword string `json:"keyword"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Keyword != "pedido" {
		t.Fatalf("lista = %+v, quiero solo [pedido] (aislamiento por tenant)", out)
	}
}

func TestDeleteTrigger_OK_And_CrossTenant404(t *testing.T) {
	store := trigger.NewMemoryStore()
	created, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// El handler lee r.PathValue("id"): se enruta por un mux con el patrón real.
	mux := http.NewServeMux()
	mux.Handle("DELETE /admin/triggers/{id}", admin.DeleteTriggerHandler(store))
	del := func(tenant string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/admin/triggers/"+created.TriggerID, nil)
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Un tenant DISTINTO no puede borrar la regla → 404 (no filtra existencia).
	if rec := del("otro-tenant"); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	// El dueño la borra → 204.
	if rec := del(ctxTenant); rec.Code != http.StatusNoContent {
		t.Fatalf("delete code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
	// Ya no existe → 404.
	if rec := del(ctxTenant); rec.Code != http.StatusNotFound {
		t.Fatalf("delete inexistente code=%d, quiero 404", rec.Code)
	}
}
