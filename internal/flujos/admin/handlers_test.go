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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ctxTenant es el tenant que las pruebas inyectan como Identity del token (Plan
// 018 · T4): el handler lo lee de IdentityFromContext (INV-8), NO del cuerpo.
const ctxTenant = "ctx-tenant"

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
	gotRef    contact.Ref
}

func (f *fakeStarter) Start(_ context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	f.called = true
	f.gotTenant = tenantID
	f.gotFlow = flowID
	f.gotSess = sessionID
	f.gotRef = ref
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

// do ejecuta el handler con una Identity de operador (tenant=ctxTenant) ya
// inyectada en el contexto, como haría el middleware Authenticate en producción.
func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	return doAs(h, ctxTenant, method, target, body)
}

// doAs es como do pero con un tenant explícito; tenant="" simula un request SIN
// identidad (no pasó por Authenticate) para ejercitar el 401.
func doAs(h http.Handler, tenant, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if tenant != "" {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- DefinitionHandler ---

func TestDefinitionHandler_OK(t *testing.T) {
	store := &fakeDefinitionStore{version: 3}
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", validFlowJSON))

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("no se llamó a InsertDefinition")
	}
	// El tenant sale del TOKEN (INV-8), no del cuerpo: el body trae "tenant-1"
	// pero el handler usa el de la Identity inyectada (ctxTenant).
	if store.gotTenant != ctxTenant {
		t.Fatalf("tenant = %q, quiero %q (del token)", store.gotTenant, ctxTenant)
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
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows", "{not json")

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
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
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
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", badOption))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if store.called {
		t.Fatal("no debió persistir una definición inválida")
	}
}

// TestDefinitionHandler_NoIdentity: sin Identity en el contexto (request que no
// pasó por Authenticate) el handler responde 401 y NO persiste (INV-8).
func TestDefinitionHandler_NoIdentity(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := doAs(admin.DefinitionHandler(store, nil), "", http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", validFlowJSON))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, quiero 401", rec.Code)
	}
	if store.called {
		t.Fatal("no debió persistir sin identidad")
	}
}

func TestDefinitionHandler_MissingDefinition(t *testing.T) {
	store := &fakeDefinitionStore{}
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
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
	rec := do(admin.DefinitionHandler(store, nil), http.MethodGet, "/admin/flows", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, quiero 405", rec.Code)
	}
	if store.called {
		t.Fatal("GET no debió llamar a InsertDefinition")
	}
}

func TestDefinitionHandler_StoreError(t *testing.T) {
	store := &fakeDefinitionStore{err: errors.New("boom")}
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", validFlowJSON))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, quiero 500", rec.Code)
	}
}

// fakeModuleTypes satisface admin.ModuleTypeSource devolviendo una lista fija de
// tipos de módulo (como haría *modules.Registry.Types()).
type fakeModuleTypes struct{ types []string }

func (f fakeModuleTypes) Types() []string { return f.types }

// cartFlowJSON usa un nodo de tipo de MÓDULO ("cart"), no core. Su validación es
// laxa: el módulo valida el contenido en runtime.
const cartFlowJSON = `{
  "flow_id": "tienda",
  "version": 1,
  "initial": "root",
  "nodes": {
    "root": {"type": "cart"}
  }
}`

// TestDefinitionHandler_ModuleTypeAccepted: con el Registry (mods) que declara
// "cart", un flujo con nodo cart PASA el alta (follow-up Plan 016). Sin mods, el
// mismo flujo se rechaza como tipo desconocido (400).
func TestDefinitionHandler_ModuleTypeAccepted(t *testing.T) {
	store := &fakeDefinitionStore{version: 1}
	mods := fakeModuleTypes{types: []string{"cart"}}
	rec := do(admin.DefinitionHandler(store, mods), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", cartFlowJSON))
	if rec.Code != http.StatusCreated {
		t.Fatalf("con Registry(cart) el flujo cart debe validar: code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("no se persistió el flujo cart")
	}
}

func TestDefinitionHandler_ModuleTypeRejectedWithoutRegistry(t *testing.T) {
	store := &fakeDefinitionStore{version: 1}
	rec := do(admin.DefinitionHandler(store, nil), http.MethodPost, "/admin/flows",
		definitionBody(t, "tenant-1", cartFlowJSON))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sin Registry, el tipo cart es desconocido: code = %d, quiero 400", rec.Code)
	}
	if store.called {
		t.Fatal("no debió persistir un flujo con tipo desconocido")
	}
}

// --- StartHandler ---

// validStartBody usa el alias `contact` plano (compat §10.F) con un número real
// (debe normalizar a phone_e164).
const validStartBody = `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1","contact":"573001112233"}`

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
	// El tenant sale del TOKEN (INV-8): el body trae "t1" pero se usa ctxTenant.
	if starter.gotTenant != ctxTenant || starter.gotFlow != "menu-soporte" || starter.gotSess != "s1" {
		t.Fatalf("Start recibió args inesperados: %+v", starter)
	}
	// El alias `contact` plano se interpreta como phone_e164 normalizado (§10.F).
	if starter.gotRef.Kind != contact.KindPhoneE164 || starter.gotRef.Value != "573001112233" {
		t.Fatalf("ref recibida = %+v, quiero {phone_e164 573001112233}", starter.gotRef)
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

func TestStartHandler_ContactRef(t *testing.T) {
	starter := &fakeStarter{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}}
	body := `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1","contact_ref":{"kind":"wa_lid","value":"88887777@lid"}}`
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	// wa_lid se normaliza (se descarta el servidor @lid).
	if starter.gotRef.Kind != contact.KindWALID || starter.gotRef.Value != "88887777" {
		t.Fatalf("ref recibida = %+v, quiero {wa_lid 88887777}", starter.gotRef)
	}
}

func TestStartHandler_ContactRefPrevaleceSobreAlias(t *testing.T) {
	starter := &fakeStarter{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}}
	// Vienen ambos: contact_ref (wa_lid) debe ganar al alias `contact` (phone).
	body := `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1","contact":"573001112233","contact_ref":{"kind":"wa_lid","value":"88887777"}}`
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if starter.gotRef.Kind != contact.KindWALID || starter.gotRef.Value != "88887777" {
		t.Fatalf("ref recibida = %+v, quiero {wa_lid 88887777}", starter.gotRef)
	}
}

func TestStartHandler_ContactRefInvalida(t *testing.T) {
	starter := &fakeStarter{}
	body := `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1","contact_ref":{"kind":"email","value":"a@b.c"}}`
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if starter.called {
		t.Fatal("kind desconocido no debió llamar a Start")
	}
}

func TestStartHandler_SinContactNiRef(t *testing.T) {
	starter := &fakeStarter{}
	body := `{"tenant_id":"t1","flow_id":"menu-soporte","session_id":"s1"}`
	rec := do(admin.StartHandler(starter), http.MethodPost, "/admin/flows/start", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, quiero 400", rec.Code)
	}
	if starter.called {
		t.Fatal("sin identidad de contacto no debió llamar a Start")
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

// TestStartHandler_NoIdentity: sin Identity en el contexto → 401 y no arranca.
func TestStartHandler_NoIdentity(t *testing.T) {
	starter := &fakeStarter{}
	rec := doAs(admin.StartHandler(starter), "", http.MethodPost, "/admin/flows/start", validStartBody)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, quiero 401", rec.Code)
	}
	if starter.called {
		t.Fatal("sin identidad no debió llamar a Start")
	}
}

func TestStartHandler_MissingField(t *testing.T) {
	// El tenant_id ya NO viaja en el cuerpo (sale del token, INV-8): solo se
	// validan flow_id/session_id/contact.
	cases := map[string]string{
		"sin flow_id":    `{"session_id":"s","contact":"c"}`,
		"sin session_id": `{"flow_id":"f","contact":"c"}`,
		"sin contact":    `{"flow_id":"f","session_id":"s"}`,
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
	admin.Register(mux, store, starter, nil)

	// Register monta los handlers "desnudos"; en producción cmd/server los envuelve
	// con Authenticate. Aquí simulamos esa capa inyectando una Identity de operador
	// para que el tenant (INV-8) esté disponible en el contexto.
	srv := httptest.NewServer(injectIdentity("t1", mux))
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

// injectIdentity simula el middleware Authenticate: mete una Identity de operador
// (tenant del token) en el contexto de cada request antes de llegar al handler.
func injectIdentity(tenant string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpapi.WithIdentity(r.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
