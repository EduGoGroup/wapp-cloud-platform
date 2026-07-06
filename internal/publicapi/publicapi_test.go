package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/auth"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	keyAFull    = "key-a-full"    // tenantA, scopes flows.* + messages.send
	keyARead    = "key-a-read"    // tenantA, scope flows.read (sin escritura)
	keyBFull    = "key-b-full"    // tenantB, scopes flows.* + messages.send
	keyAContent = "key-a-content" // tenantA, scopes media.upload + content.*
	keyBContent = "key-b-content" // tenantB, scopes media.upload + content.*

	keyASessions = "key-a-sessions" // tenantA, scope sessions.read
	keyBSessions = "key-b-sessions" // tenantB, scope sessions.read
)

// apiKeys mapea api-key → identidad M2M (tenant + scopes), como haría el store IAM.
func apiKeys() map[string]in.ServiceIdentity {
	return map[string]in.ServiceIdentity{
		keyAFull:     {TenantID: tenantA, ClientID: "crm-a", Scopes: []string{"flows.*", "messages.send"}},
		keyARead:     {TenantID: tenantA, ClientID: "ro-a", Scopes: []string{"flows.read"}},
		keyBFull:     {TenantID: tenantB, ClientID: "crm-b", Scopes: []string{"flows.*", "messages.send"}},
		keyAContent:  {TenantID: tenantA, ClientID: "cms-a", Scopes: []string{"media.upload", "content.write", "content.read"}},
		keyBContent:  {TenantID: tenantB, ClientID: "cms-b", Scopes: []string{"media.upload", "content.write", "content.read"}},
		keyASessions: {TenantID: tenantA, ClientID: "guardian-a", Scopes: []string{"sessions.read"}},
		keyBSessions: {TenantID: tenantB, ClientID: "guardian-b", Scopes: []string{"sessions.read"}},
	}
}

// --- Fakes M2M / auditor ---

type fakeM2M struct{ keys map[string]in.ServiceIdentity }

func (f fakeM2M) AuthenticateAPIKey(_ context.Context, rawKey string) (in.ServiceIdentity, error) {
	si, ok := f.keys[rawKey]
	if !ok {
		return in.ServiceIdentity{}, errors.New("api-key inválida")
	}
	return si, nil
}

func (f fakeM2M) VerifyServiceToken(_ context.Context, _ string) (in.ServiceIdentity, error) {
	return in.ServiceIdentity{}, errors.New("no aplica")
}

func (f fakeM2M) AuthorizeScope(scopes []string, required string) bool {
	for _, s := range scopes {
		if auth.PermissionMatches(s, required) {
			return true
		}
	}
	return false
}

type noopAuditor struct{}

func (noopAuditor) Record(_ context.Context, _ in.AuditInput) error { return nil }

// --- Fakes de negocio ---

type fakeSender struct {
	ack     *cloudlinkv1.Ack
	err     error
	called  bool
	gotSess string
	gotTo   string
	gotText string
}

func (f *fakeSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	f.called = true
	f.gotSess, f.gotTo, f.gotText = sessionID, to, text
	return f.ack, f.err
}

type fakeSessions struct{ byTenant map[string][]fleet.Session }

func (f fakeSessions) List(_ context.Context, tenantID string) ([]fleet.Session, error) {
	return f.byTenant[tenantID], nil
}

type fakeStarter struct {
	ack       *cloudlinkv1.Ack
	err       error
	called    bool
	gotTenant string
	gotFlow   string
	gotSess   string
	gotRef    contact.Ref
}

func (f *fakeStarter) Start(_ context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	f.called = true
	f.gotTenant, f.gotFlow, f.gotSess, f.gotRef = tenantID, flowID, sessionID, ref
	return f.ack, f.err
}

// --- Harness ---

func newAPI(d publicapi.Deps, keys map[string]in.ServiceIdentity) *http.ServeMux {
	mw := httpapi.NewMiddleware(auth.NewJWTManager("secret-hs256-de-test", "wapp-test"), fakeM2M{keys: keys}, nil)
	mux := http.NewServeMux()
	publicapi.Register(mux, d, mw, noopAuditor{}, nil)
	return mux
}

// call ejecuta una request contra el mux con la api-key dada (vacía = sin auth).
func call(mux *http.ServeMux, apiKey, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func okAck() *cloudlinkv1.Ack {
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}
}

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

// flowCreateBody arma el cuerpo {definition} de POST /api/v1/flows.
func flowCreateBody(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"definition": json.RawMessage(validFlowJSON)})
	if err != nil {
		t.Fatalf("marshal flowCreateBody: %v", err)
	}
	return string(body)
}

// seedFlow publica validFlowJSON para tenantID en un MemoryRepository.
func seedFlow(t *testing.T, repo *flowstore.MemoryRepository, tenantID string) {
	t.Helper()
	f, err := model.ParseAndValidate([]byte(validFlowJSON))
	if err != nil {
		t.Fatalf("parse validFlowJSON: %v", err)
	}
	if _, err := repo.InsertDefinition(context.Background(), tenantID, f); err != nil {
		t.Fatalf("seed InsertDefinition: %v", err)
	}
}

// ============================ /api/v1/messages ============================

func TestMessages_OK_WithScope(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	d := publicapi.Deps{
		Sender:   sender,
		Sessions: fakeSessions{byTenant: map[string][]fleet.Session{tenantA: {{TenantID: tenantA, SessionID: "sess-a"}}}},
	}
	mux := newAPI(d, apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !sender.called || sender.gotSess != "sess-a" || sender.gotText != "hola" {
		t.Fatalf("SendText no invocado correctamente: %+v", sender)
	}
}

func TestMessages_401_NoToken(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	mux := newAPI(publicapi.Deps{Sender: sender, Sessions: fakeSessions{}}, apiKeys())

	rec := call(mux, "", http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
	if sender.called {
		t.Fatal("no debió enviar sin autenticación")
	}
}

func TestMessages_403_NoScope(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	d := publicapi.Deps{
		Sender:   sender,
		Sessions: fakeSessions{byTenant: map[string][]fleet.Session{tenantA: {{TenantID: tenantA, SessionID: "sess-a"}}}},
	}
	mux := newAPI(d, apiKeys())

	// keyARead solo tiene flows.read → messages.send denegado.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if sender.called {
		t.Fatal("no debió enviar sin scope")
	}
}

func TestMessages_CrossTenant_Isolation_404(t *testing.T) {
	sender := &fakeSender{ack: okAck()}
	// tenantA solo tiene sess-a; sess-b es de tenantB.
	d := publicapi.Deps{
		Sender: sender,
		Sessions: fakeSessions{byTenant: map[string][]fleet.Session{
			tenantA: {{TenantID: tenantA, SessionID: "sess-a"}},
			tenantB: {{TenantID: tenantB, SessionID: "sess-b"}},
		}},
	}
	mux := newAPI(d, apiKeys())

	// api-key del tenant A intenta enviar por una sesión del tenant B → 404 (INV-8).
	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-b","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (aislamiento); body=%s", rec.Code, rec.Body.String())
	}
	if sender.called {
		t.Fatal("no debió enviar por una sesión de otro tenant")
	}
}

// ============================ /api/v1/flows (create) ============================

func TestFlowsCreate_OK_WithScope(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Flows: repo}, apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/flows", flowCreateBody(t))

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	// Persistido BAJO tenantA (del token), verificable por lectura.
	if _, err := repo.LatestDefinition(context.Background(), tenantA, "menu-soporte"); err != nil {
		t.Fatalf("no se persistió bajo tenantA: %v", err)
	}
	if _, err := repo.LatestDefinition(context.Background(), tenantB, "menu-soporte"); err == nil {
		t.Fatal("no debió persistirse bajo tenantB")
	}
}

func TestFlowsCreate_403_NoScope(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	mux := newAPI(publicapi.Deps{Flows: repo}, apiKeys())

	// keyARead: flows.read NO cubre flows.create.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/flows", flowCreateBody(t))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ============================ /api/v1/flows (list/get) ============================

func TestFlowsList_OK_And_TenantIsolation(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	seedFlow(t, repo, tenantA)
	mux := newAPI(publicapi.Deps{Flows: repo}, apiKeys())

	// tenantA ve su flujo.
	rec := call(mux, keyAFull, http.MethodGet, "/api/v1/flows", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var listA []struct {
		FlowID  string `json:"flow_id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listA); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listA) != 1 || listA[0].FlowID != "menu-soporte" {
		t.Fatalf("lista A = %+v, quiero [menu-soporte]", listA)
	}

	// tenantB NO ve el flujo de A (aislamiento).
	recB := call(mux, keyBFull, http.MethodGet, "/api/v1/flows", "")
	var listB []any
	if err := json.Unmarshal(recB.Body.Bytes(), &listB); err != nil {
		t.Fatalf("unmarshal B: %v", err)
	}
	if len(listB) != 0 {
		t.Fatalf("tenantB no debe ver flujos ajenos: %+v", listB)
	}
}

func TestFlowsGet_OK_NotFound_CrossTenant(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	seedFlow(t, repo, tenantA)
	mux := newAPI(publicapi.Deps{Flows: repo}, apiKeys())

	// A lee su flujo.
	if rec := call(mux, keyAFull, http.MethodGet, "/api/v1/flows/menu-soporte", ""); rec.Code != http.StatusOK {
		t.Fatalf("get A code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	// flujo inexistente → 404.
	if rec := call(mux, keyAFull, http.MethodGet, "/api/v1/flows/no-existe", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get inexistente code=%d, quiero 404", rec.Code)
	}
	// B intenta leer el flujo de A → 404 (el store filtra por tenant).
	if rec := call(mux, keyBFull, http.MethodGet, "/api/v1/flows/menu-soporte", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get code=%d, quiero 404", rec.Code)
	}
}

// ============================ /api/v1/flows/{id}/start ============================

func TestFlowsStart_OK_WithScope(t *testing.T) {
	starter := &fakeStarter{ack: okAck()}
	mux := newAPI(publicapi.Deps{Starter: starter}, apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/flows/menu-soporte/start",
		`{"session_id":"sess-a","contact":"+15551234567"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !starter.called || starter.gotTenant != tenantA || starter.gotFlow != "menu-soporte" || starter.gotSess != "sess-a" {
		t.Fatalf("Start no invocado con (tenantA, menu-soporte, sess-a): %+v", starter)
	}
}

func TestFlowsStart_403_NoScope(t *testing.T) {
	starter := &fakeStarter{ack: okAck()}
	mux := newAPI(publicapi.Deps{Starter: starter}, apiKeys())

	// keyARead: flows.read NO cubre flows.start.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/flows/menu-soporte/start",
		`{"session_id":"sess-a","contact":"+15551234567"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if starter.called {
		t.Fatal("no debió arrancar sin scope")
	}
}
