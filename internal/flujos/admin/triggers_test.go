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
		"json roto":               `{`,
		"kind desconocido":        `{"kind":"regex","keyword":"x","flow_id":"f"}`,
		"match_type inválido":     `{"kind":"keyword","keyword":"x","flow_id":"f","match_type":"prefix"}`,
		"keyword sin keyword":     `{"kind":"keyword","flow_id":"f"}`,
		"keyword sin flow_id":     `{"kind":"keyword","keyword":"x"}`,
		"fallback sin flow_id":    `{"kind":"fallback"}`,
		"escape sin keyword":      `{"kind":"escape"}`,
		"llm sin keyword":         `{"kind":"llm","flow_id":"carrito"}`,
		"llm sin flow_id":         `{"kind":"llm","keyword":"pedido"}`,
		"llm keyword con mayús":   `{"kind":"llm","keyword":"Pedido","flow_id":"carrito"}`,
		"llm keyword con espacio": `{"kind":"llm","keyword":"quiero pedido","flow_id":"carrito"}`,
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

// TestCreateTrigger_OK_LLM (Plan 029 · T7): una regla kind='llm' con keyword=nombre de
// intención válido y flow_id se acepta (201) y persiste con su kind.
func TestCreateTrigger_OK_LLM(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)
	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"llm","keyword":"pedido","flow_id":"carrito"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("llm code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].Kind != trigger.KindLLM || rules[0].Keyword != "pedido" || rules[0].FlowID != "carrito" {
		t.Fatalf("regla llm no persistió como se esperaba: %+v", rules)
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

// ---------------------------------------------------------------------------
// Plan 043 · T2.1 — kinds event_start / event_stop y la marca derivada
// ---------------------------------------------------------------------------

// TestCreateTrigger_OK_EventStart: un event_start con keyword y event_kind se acepta
// (201), devuelve el event_kind y lo persiste. NO exige flow_id: el despachador del
// menú es un componente del runtime, no una fila de flow_definitions (D-043.3).
func TestCreateTrigger_OK_EventStart(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"event_start","keyword":"carrito","event_kind":"cart"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Kind      string `json:"kind"`
		EventKind string `json:"event_kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != "event_start" || out.EventKind != "cart" {
		t.Fatalf("la respuesta debe traer kind y event_kind, got %+v", out)
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].Kind != trigger.KindEventStart || rules[0].EventKind != "cart" {
		t.Fatalf("event_start no persistió como se esperaba: %+v", rules)
	}
}

// TestCreateTrigger_OK_EventStop: un event_stop solo necesita keyword — corta el
// evento ACTIVO, sea del tipo que sea, así que no lleva event_kind ni flow_id.
func TestCreateTrigger_OK_EventStop(t *testing.T) {
	store := trigger.NewMemoryStore()
	rec := doTrigger(admin.CreateTriggerHandler(store), http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"event_stop","keyword":"parar"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].Kind != trigger.KindEventStop || rules[0].EventKind != "" {
		t.Fatalf("event_stop no persistió como se esperaba: %+v", rules)
	}
}

// TestCreateTrigger_400_EventKindCoherencia: los cuerpos incoherentes con event_kind
// se rechazan con 400 y un mensaje que dice QUÉ falta (no un 400 mudo).
func TestCreateTrigger_400_EventKindCoherencia(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store)
	cases := map[string]struct{ body, wantMsg string }{
		"event_start sin event_kind": {
			`{"kind":"event_start","keyword":"carrito"}`, "event_kind es requerido"},
		"event_start sin keyword": {
			`{"kind":"event_start","event_kind":"cart"}`, "keyword es requerido"},
		"event_stop sin keyword": {
			`{"kind":"event_stop"}`, "keyword es requerido"},
		"event_kind en keyword": {
			`{"kind":"keyword","keyword":"x","flow_id":"f","event_kind":"cart"}`, "event_kind solo es válido"},
		"event_kind en event_stop": {
			`{"kind":"event_stop","keyword":"parar","event_kind":"cart"}`, "event_kind solo es válido"},
		"event_kind con formato inválido": {
			`{"kind":"event_start","keyword":"carrito","event_kind":"Cart Grande"}`, "los valores admitidos son"},
		// El typo del dueño: bien formado, en minúsculas, y aun así no lo atiende
		// ningún módulo. Antes entraba con un 201 y llegaba hasta la BD.
		"event_kind con un typo plausible": {
			`{"kind":"event_start","keyword":"carrito","event_kind":"carrrito"}`, "los valores admitidos son"},
		"event_kind inventado": {
			`{"kind":"event_start","keyword":"pizza","event_kind":"pizza"}`, "menu|cart|survey|media"},
	}
	for name, tc := range cases {
		rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant, tc.body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.wantMsg) {
			t.Fatalf("%s: el error debe explicar el problema (%q), got %q", name, tc.wantMsg, rec.Body.String())
		}
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("ningún cuerpo inválido debe haber persistido, got %+v", rules)
	}
}

// TestCreateTrigger_INV08_TenantDelCuerpoIgnorado: el tenant sale SIEMPRE del token.
// Un cuerpo que intente colar otro tenant_id no se cuela: la regla nace del tenant de
// la Identity y el listado del tenant ajeno no la ve (INV-08).
func TestCreateTrigger_INV08_TenantDelCuerpoIgnorado(t *testing.T) {
	store := trigger.NewMemoryStore()
	rec := doTrigger(admin.CreateTriggerHandler(store), http.MethodPost, "/admin/triggers", ctxTenant,
		`{"tenant_id":"tenant-ajeno","kind":"event_start","keyword":"carrito","event_kind":"cart"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].TenantID != ctxTenant {
		t.Fatalf("la regla debe nacer del tenant del token, got %+v", rules)
	}
	ajenas, err := store.List(context.Background(), "tenant-ajeno")
	if err != nil {
		t.Fatalf("List ajeno: %v", err)
	}
	if len(ajenas) != 0 {
		t.Fatalf("el tenant del cuerpo debe ignorarse por completo, got %+v", ajenas)
	}
}

// TestListTriggers_ShadowedByEventList (MD-043.11): el GET marca las reglas
// kind='fallback' con shadowed_by_event_list para avisar al dueño de que en la
// conversación sin evento manda la lista (D-043.20). Los demás kinds no la llevan.
func TestListTriggers_ShadowedByEventList(t *testing.T) {
	store := trigger.NewMemoryStore()
	seedRules := []trigger.Rule{
		{TenantID: ctxTenant, Kind: trigger.KindFallback, FlowID: "bienvenida", Enabled: true},
		{TenantID: ctxTenant, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true},
		{TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true},
	}
	for _, r := range seedRules {
		if _, err := store.Insert(context.Background(), r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	rec := doTrigger(admin.ListTriggersHandler(store), http.MethodGet, "/admin/triggers", ctxTenant, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		Kind      string `json:"kind"`
		EventKind string `json:"event_kind"`
		Shadowed  bool   `json:"shadowed_by_event_list"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("esperaba 3 reglas, got %+v", out)
	}
	for _, dto := range out {
		if want := dto.Kind == "fallback"; dto.Shadowed != want {
			t.Fatalf("kind=%s shadowed_by_event_list=%v, quiero %v", dto.Kind, dto.Shadowed, want)
		}
		if dto.Kind == "event_start" && dto.EventKind != "cart" {
			t.Fatalf("el listado debe devolver el event_kind, got %+v", dto)
		}
	}
	// La marca es DERIVADA: no se persiste en ninguna parte.
	if !strings.Contains(rec.Body.String(), `"shadowed_by_event_list":true`) {
		t.Fatalf("el fallback debe salir marcado, body=%s", rec.Body.String())
	}
}
