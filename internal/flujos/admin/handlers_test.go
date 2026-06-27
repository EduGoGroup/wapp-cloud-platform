package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// --- dobles ---

// fakeDefinitionStore registra la última llamada a InsertDefinition y devuelve
// una versión/err programables.
type fakeDefinitionStore struct {
	version int
	err     error

	called     bool
	gotTenant  string
	gotFlow    model.Flow
	callsCount int
}

func (f *fakeDefinitionStore) InsertDefinition(_ context.Context, tenantID string, flow model.Flow) (int, error) {
	f.called = true
	f.callsCount++
	f.gotTenant = tenantID
	f.gotFlow = flow
	return f.version, f.err
}

// fakeStarter registra la última llamada a Start y devuelve un Ack/err programables.
type fakeStarter struct {
	ack *cloudlinkv1.Ack
	err error

	called    bool
	gotTenant string
	gotFlow   string
	gotSess   string
	gotTo     string
}

func (f *fakeStarter) Start(_ context.Context, tenantID, flowID, sessionID, contact string) (*cloudlinkv1.Ack, error) {
	f.called = true
	f.gotTenant = tenantID
	f.gotFlow = flowID
	f.gotSess = sessionID
	f.gotTo = contact
	return f.ack, f.err
}

// validFlowJSON es una definición de menú mínima y válida (esquema B).
const validFlowJSON = `{
  "flow_id": "menu-soporte",
  "version": 1,
  "initial": "root",
  "nodes": {
    "root": {"type": "menu", "prompt": "Elige:\n1) A\n2) B", "options": {"1": "a", "2": "b"}},
    "a": {"type": "message", "text": "Elegiste A", "next": null},
    "b": {"type": "message", "text": "Elegiste B", "next": null}
  }
}`

func definitionBody(t *testing.T, tenant string, def string) string {
	t.Helper()
	type req struct {
		TenantID   string          `json:"tenant_id"`
		Definition json.RawMessage `json:"definition"`
	}
	b, err := json.Marshal(req{TenantID: tenant, Definition: json.RawMessage(def)})
	if err != nil {
		t.Fatalf("marshal definitionBody: %v", err)
	}
	return string(b)
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- DefinitionHandler ---

func TestDefinitionHandler_OK(t *testing.T) {
	store := &fakeDefinitionStore{version: 3}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", validFlowJSON))

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("no se llamó a InsertDefinition")
	}
	if store.gotTenant != "tenant-1" {
		t.Fatalf("tenant = %q, quiero tenant-1", store.gotTenant)
	}
	if store.gotFlow.FlowID != "menu-soporte" {
		t.Fatalf("flow_id = %q, quiero menu-soporte", store.gotFlow.FlowID)
	}

	var resp struct {
		FlowID  string `json:"flow_id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v; body=%s", err, rec.Body.String())
	}
	if resp.FlowID != "menu-soporte" || resp.Version != 3 {
		t.Fatalf("resp = %+v, quiero {menu-soporte 3}", resp)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, quiero application/json", ct)
	}
}

func TestDefinitionHandler_MalformedJSON(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows", "{not json")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400", rec.Code)
	}
	if store.called {
		t.Fatal("no debió persistir con JSON malformado")
	}
}

func TestDefinitionHandler_InvalidFlow_MissingInitial(t *testing.T) {
	// initial ausente → ParseAndValidate rechaza con ErrInvalidFlow.
	const noInitial = `{
      "flow_id": "f1",
      "version": 1,
      "nodes": {"root": {"type": "message", "text": "hola", "next": null}}
    }`
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", noInitial))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if store.called {
		t.Fatal("no debió persistir una definición inválida")
	}
}

func TestDefinitionHandler_InvalidFlow_OptionToMissingNode(t *testing.T) {
	// opción que apunta a un nodo inexistente → ErrInvalidFlow.
	const badOption = `{
      "flow_id": "f1",
      "version": 1,
      "initial": "root",
      "nodes": {"root": {"type": "menu", "prompt": "p", "options": {"1": "fantasma"}}}
    }`
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", badOption))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if store.called {
		t.Fatal("no debió persistir una definición inválida")
	}
}

func TestDefinitionHandler_MissingTenant(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		definitionBody(t, "", validFlowJSON))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400", rec.Code)
	}
	if store.called {
		t.Fatal("no debió persistir sin tenant_id")
	}
}

func TestDefinitionHandler_MissingDefinition(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		`{"tenant_id": "tenant-1"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400", rec.Code)
	}
	if store.called {
		t.Fatal("no debió persistir sin definition")
	}
}

func TestDefinitionHandler_MethodNotAllowed(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store), http.MethodGet, "/admin/flows", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, quiero 405", rec.Code)
	}
	if store.called {
		t.Fatal("GET no debió llamar a InsertDefinition")
	}
}

func TestDefinitionHandler_StoreError(t *testing.T) {
	store := &fakeDefinitionStore{err: errors.New("boom")}
	rec := do(admin.DefinitionHandler(store), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", validFlowJSON))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, quiero 500", rec.Code)
	}
}

// --- StartHandler ---

const validStartBody = `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1","contact":"c1"}`

func TestStartHandler_OK(t *testing.T) {
	starter := &fakeStarter{ack: &cloudlinkv1.Ack{
		AckedCommandId: "cmd-1",
		Ok:             true,
	}}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !starter.called {
		t.Fatal("no se llamó a Start")
	}
	if starter.gotTenant != "t1" || starter.gotFlow != "menu-soporte" ||
		starter.gotSess != "s1" || starter.gotTo != "c1" {
		t.Fatalf("Start recibió args inesperados: %+v", starter)
	}

	var resp struct {
		AckedCommandID string `json:"acked_command_id"`
		OK             bool   `json:"ok"`
		Error          string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v; body=%s", err, rec.Body.String())
	}
	if resp.AckedCommandID != "cmd-1" || !resp.OK {
		t.Fatalf("resp = %+v, quiero {cmd-1 true}", resp)
	}
}

func TestStartHandler_MalformedJSON(t *testing.T) {
	starter := &fakeStarter{}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", "{not json")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400", rec.Code)
	}
	if starter.called {
		t.Fatal("JSON malformado no debió llamar a Start")
	}
}

func TestStartHandler_MissingField(t *testing.T) {
	cases := map[string]string{
		"sin tenant_id":  `{"flow_id":"f","session_id":"s","contact":"c"}`,
		"sin flow_id":    `{"tenant_id":"t","session_id":"s","contact":"c"}`,
		"sin session_id": `{"tenant_id":"t","flow_id":"f","contact":"c"}`,
		"sin contact":    `{"tenant_id":"t","flow_id":"f","session_id":"s"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			starter := &fakeStarter{}
			rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, quiero 400", rec.Code)
			}
			if starter.called {
				t.Fatal("no debió llamar a Start con campo faltante")
			}
		})
	}
}

func TestStartHandler_ConversationExists(t *testing.T) {
	starter := &fakeStarter{err: runtime.ErrConversationExists}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, quiero 409", rec.Code)
	}
}

func TestStartHandler_SessionOffline(t *testing.T) {
	// envuelto, como lo propaga Runtime.send → registry.Push (errors.Is debe atravesarlo).
	starter := &fakeStarter{err: errWrap(session.ErrSessionOffline)}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, quiero 502", rec.Code)
	}
}

func TestStartHandler_DeadlineExceeded(t *testing.T) {
	starter := &fakeStarter{err: errWrap(context.DeadlineExceeded)}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code = %d, quiero 504", rec.Code)
	}
}

func TestStartHandler_GenericError(t *testing.T) {
	starter := &fakeStarter{err: errors.New("algo más")}
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, quiero 500", rec.Code)
	}
}

func TestStartHandler_MethodNotAllowed(t *testing.T) {
	starter := &fakeStarter{}
	rec := do(admin.StartHandler(starter), http.MethodGet, "/admin/flows/start", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, quiero 405", rec.Code)
	}
	if starter.called {
		t.Fatal("GET no debió llamar a Start")
	}
}

// --- Register ---

func TestRegister_RoutesBothEndpoints(t *testing.T) {
	store := &fakeDefinitionStore{version: 1}
	starter := &fakeStarter{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd", Ok: true}}
	mux := http.NewServeMux()
	admin.Register(mux, store, starter)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/flows", "application/json",
		strings.NewReader(definitionBody(t, "t1", validFlowJSON)))
	if err != nil {
		t.Fatalf("POST /admin/flows: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("/admin/flows code = %d, quiero 201", resp.StatusCode)
	}

	resp2, err := http.Post(srv.URL+"/admin/flows/start", "application/json",
		strings.NewReader(validStartBody))
	if err != nil {
		t.Fatalf("POST /admin/flows/start: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/admin/flows/start code = %d, quiero 200", resp2.StatusCode)
	}
	if !store.called || !starter.called {
		t.Fatal("Register no enrutó ambos endpoints")
	}
}

// errWrap envuelve un error centinela como lo hace el runtime (fmt.Errorf %w),
// para verificar que el handler usa errors.Is y no comparación directa.
func errWrap(target error) error {
	return errors.Join(errors.New("runtime: enviar texto"), target)
}
