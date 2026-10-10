package admin_test

// handlers_test.go — POST /admin/flows visto desde fuera: código, Content-Type y cuerpo
// LITERAL de cada respuesta, y qué llega al store. Aquí viven también los ayudantes
// HTTP que comparten los tests del paquete. POST /admin/flows/start sigue en
// handlers_start_test.go (mismo origen, partido por tema, E-13).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Los puertos los satisfacen los tipos reales SIN adaptador.
var (
	_ admin.DefinitionStore  = (*store.MemoryRepository)(nil)
	_ admin.DefinitionStore  = (*store.PostgresRepository)(nil)
	_ admin.ModuleTypeSource = (*modules.Registry)(nil)
	_ admin.Starter          = (*runtime.Runtime)(nil)
)

// tokenTenant es el tenant de la Identity que inyectan los tests: el handler lo lee
// del contexto (INV-8), nunca del cuerpo.
const tokenTenant = "ctx-tenant"

const (
	msgMethodNotAllowed = "método no permitido (usar POST)"
	msgAuthRequired     = "autenticación requerida"
	msgInvalidJSON      = "cuerpo JSON inválido"
)

// operator es la Identity de un request que pasó por Authenticate.
func operator() *httpapi.Identity {
	return &httpapi.Identity{TenantID: tokenTenant, Subject: "user-1"}
}

// serve ejecuta el handler. id nil simula un request SIN identidad.
func serve(h http.Handler, id *httpapi.Identity, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if id != nil {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// wantPlainError comprueba la forma de TODO error del paquete: código, texto plano y
// el mensaje literal con su salto de línea.
func wantPlainError(t *testing.T, rec *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	if rec.Code != code {
		t.Errorf("código = %d, quiero %d (cuerpo: %q)", rec.Code, code, rec.Body.String())
	}
	if got := rec.Body.String(); got != msg+"\n" {
		t.Errorf("cuerpo = %q, quiero %q", got, msg+"\n")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, quiero text/plain; charset=utf-8", ct)
	}
}

// wantJSON comprueba la forma de un éxito con cuerpo: código, JSON y el cuerpo exacto.
func wantJSON(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("código = %d, quiero %d (cuerpo: %q)", rec.Code, code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("cuerpo = %q, quiero %q", got, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, quiero application/json", ct)
	}
}

// ---------------------------------------------------------------------------
// DefinitionHandler
// ---------------------------------------------------------------------------

type fakeDefinitionStore struct {
	version int
	err     error

	calls       int
	gotIdentity httpapi.Identity
	gotTenant   string
	gotFlow     model.Flow
}

func (f *fakeDefinitionStore) InsertDefinition(ctx context.Context, tenantID string, flow model.Flow) (int, error) {
	f.calls++
	f.gotIdentity, _ = httpapi.IdentityFromContext(ctx)
	f.gotTenant, f.gotFlow = tenantID, flow
	return f.version, f.err
}

// fakeModuleTypes es una fuente de tipos SIN validación estructural; cuenta lecturas.
type fakeModuleTypes struct {
	types []string
	calls int
}

func (f *fakeModuleTypes) Types() []string {
	f.calls++
	return f.types
}

// fakeModuleValidator añade la capacidad opcional ValidateModuleNodes.
type fakeModuleValidator struct {
	fakeModuleTypes
	err error
	got []model.Flow
}

func (f *fakeModuleValidator) ValidateModuleNodes(flow model.Flow) error {
	f.got = append(f.got, flow)
	return f.err
}

const (
	menuFlowJSON = `{"flow_id":"menu-soporte","version":1,"initial":"root","nodes":{` +
		`"root":{"type":"menu","prompt":"Elige:\n1) A","options":{"1":"a"}},` +
		`"a":{"type":"message","text":"Elegiste A","next":null}}}`
	noInitialFlowJSON = `{"flow_id":"menu-soporte","version":1,"nodes":{` +
		`"a":{"type":"message","text":"Elegiste A","next":null}}}`
	cartFlowJSON = `{"flow_id":"tienda","version":1,"initial":"root","nodes":{"root":{"type":"cart"}}}`
)

// definitionBody arma el cuerpo con un tenant_id intruso, que el handler debe ignorar.
func definitionBody(def string) string {
	return `{"tenant_id":"tenant-del-cuerpo","definition":` + def + `}`
}

// invalidFlowMessage es el 400 esperado para una definición que el modelo rechaza.
func invalidFlowMessage(t *testing.T, def string, moduleTypes ...string) string {
	t.Helper()
	_, err := model.ParseAndValidate([]byte(def), moduleTypes...)
	if err == nil {
		t.Fatalf("el fixture %s debería ser inválido para el modelo", def)
	}
	return "definición de flujo inválida: " + err.Error()
}

func TestDefinitionHandler_PublishesForTheTokenTenant(t *testing.T) {
	t.Parallel()
	st := &fakeDefinitionStore{version: 7}

	rec := serve(admin.DefinitionHandler(st, nil), operator(), http.MethodPost, "/admin/flows", definitionBody(menuFlowJSON))

	// La versión es la que asignó el store (7), no la del cuerpo (1).
	wantJSON(t, rec, http.StatusCreated, `{"flow_id":"menu-soporte","version":7}`)
	if st.calls != 1 {
		t.Fatalf("InsertDefinition se llamó %d veces, quiero 1", st.calls)
	}
	if st.gotTenant != tokenTenant {
		t.Errorf("tenant persistido = %q, quiero el del token (%q)", st.gotTenant, tokenTenant)
	}
	if st.gotIdentity.TenantID != tokenTenant {
		t.Errorf("el store no recibió el contexto de la petición (identidad = %+v)", st.gotIdentity)
	}
	want, err := model.ParseAndValidate([]byte(menuFlowJSON))
	if err != nil {
		t.Fatalf("fixture inválido: %v", err)
	}
	if st.gotFlow.FlowID != want.FlowID || st.gotFlow.Initial != want.Initial || len(st.gotFlow.Nodes) != len(want.Nodes) {
		t.Errorf("flujo persistido = %+v, quiero el validado %+v", st.gotFlow, want)
	}
}

func TestDefinitionHandler_Rejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		id       *httpapi.Identity
		method   string
		body     string
		wantCode int
		wantMsg  string
	}{
		{"no identity", nil, http.MethodPost, definitionBody(menuFlowJSON),
			http.StatusUnauthorized, msgAuthRequired},
		{"identity without tenant", &httpapi.Identity{Subject: "user-1"}, http.MethodPost, definitionBody(menuFlowJSON),
			http.StatusUnauthorized, msgAuthRequired},
		{"malformed json", operator(), http.MethodPost, `{`,
			http.StatusBadRequest, msgInvalidJSON},
		{"missing definition", operator(), http.MethodPost, `{"tenant_id":"t"}`,
			http.StatusBadRequest, "definition es requerida"},
		{"schema without initial node", operator(), http.MethodPost, definitionBody(noInitialFlowJSON),
			http.StatusBadRequest, invalidFlowMessage(t, noInitialFlowJSON)},
		{"module node type without module source", operator(), http.MethodPost, definitionBody(cartFlowJSON),
			http.StatusBadRequest, invalidFlowMessage(t, cartFlowJSON)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := &fakeDefinitionStore{version: 1}
			rec := serve(admin.DefinitionHandler(st, nil), tc.id, tc.method, "/admin/flows", tc.body)
			wantPlainError(t, rec, tc.wantCode, tc.wantMsg)
			if st.calls != 0 {
				t.Errorf("se persistió una definición rechazada (%d llamadas)", st.calls)
			}
		})
	}
}

// El texto del modelo ya empieza por el de ErrInvalidFlow: el prefijo sale dos veces.
func TestDefinitionHandler_InvalidFlowRepeatsThePrefix(t *testing.T) {
	t.Parallel()
	rec := serve(admin.DefinitionHandler(&fakeDefinitionStore{}, nil), operator(), http.MethodPost, "/admin/flows",
		definitionBody(noInitialFlowJSON))
	const twice = "definición de flujo inválida: definición de flujo inválida: "
	if !strings.HasPrefix(rec.Body.String(), twice) {
		t.Errorf("cuerpo = %q, quiero que empiece por %q", rec.Body.String(), twice)
	}
}

// Con una fuente que declara "cart" el flujo pasa, y la fuente se lee UNA vez, al
// construir el handler.
func TestDefinitionHandler_ModuleTypesAreReadOnceAtConstruction(t *testing.T) {
	t.Parallel()
	st := &fakeDefinitionStore{version: 2}
	mods := &fakeModuleTypes{types: []string{"cart"}}

	h := admin.DefinitionHandler(st, mods)
	if mods.calls != 1 {
		t.Fatalf("Types() se leyó %d veces al construir, quiero 1", mods.calls)
	}
	for range 2 {
		rec := serve(h, operator(), http.MethodPost, "/admin/flows", definitionBody(cartFlowJSON))
		wantJSON(t, rec, http.StatusCreated, `{"flow_id":"tienda","version":2}`)
	}
	if mods.calls != 1 {
		t.Errorf("Types() se leyó %d veces tras dos peticiones, quiero 1", mods.calls)
	}
	if st.calls != 2 {
		t.Errorf("definiciones persistidas = %d, quiero 2", st.calls)
	}
}

func TestDefinitionHandler_ModuleStructuralValidation(t *testing.T) {
	t.Parallel()
	errNoCatalog := errors.New(`nodo "root": cart sin catálogo`)
	cases := []struct {
		name      string
		err       error
		wantCode  int
		wantStore int
	}{
		{"module rejects the node", errNoCatalog, http.StatusBadRequest, 0},
		{"module accepts the node", nil, http.StatusCreated, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := &fakeDefinitionStore{version: 1}
			mods := &fakeModuleValidator{fakeModuleTypes: fakeModuleTypes{types: []string{"cart"}}, err: tc.err}

			rec := serve(admin.DefinitionHandler(st, mods), operator(), http.MethodPost, "/admin/flows", definitionBody(cartFlowJSON))

			if tc.err != nil {
				wantPlainError(t, rec, tc.wantCode, "definición de flujo inválida: "+tc.err.Error())
			} else {
				wantJSON(t, rec, tc.wantCode, `{"flow_id":"tienda","version":1}`)
			}
			if len(mods.got) != 1 || mods.got[0].FlowID != "tienda" {
				t.Errorf("ValidateModuleNodes recibió %+v, quiero el flujo tienda una vez", mods.got)
			}
			if st.calls != tc.wantStore {
				t.Errorf("InsertDefinition se llamó %d veces, quiero %d", st.calls, tc.wantStore)
			}
		})
	}
}

func TestDefinitionHandler_StoreFailure(t *testing.T) {
	t.Parallel()
	st := &fakeDefinitionStore{err: errors.New("boom")}
	rec := serve(admin.DefinitionHandler(st, nil), operator(), http.MethodPost, "/admin/flows", definitionBody(menuFlowJSON))
	wantPlainError(t, rec, http.StatusInternalServerError, "no se pudo persistir la definición")
}

// Los dos handlers miran el método ANTES que la autenticación: sin identidad y con un
// método que no es POST, la respuesta es el 405 y no el 401.
func TestHandlers_MethodIsCheckedBeforeAuthentication(t *testing.T) {
	t.Parallel()
	handlers := map[string]func() (http.Handler, func() int){
		"definition": func() (http.Handler, func() int) {
			st := &fakeDefinitionStore{version: 1}
			return admin.DefinitionHandler(st, nil), func() int { return st.calls }
		},
		"start": func() (http.Handler, func() int) {
			st := &fakeStarter{}
			return admin.StartHandler(st), func() int { return st.calls }
		},
	}
	for name, build := range handlers {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			t.Run(name+"/"+method, func(t *testing.T) {
				t.Parallel()
				h, calls := build()
				wantPlainError(t, serve(h, nil, method, "/admin/flows", `{`), http.StatusMethodNotAllowed, msgMethodNotAllowed)
				if calls() != 0 {
					t.Errorf("el handler llegó a su puerto %d veces con un método rechazado", calls())
				}
			})
		}
	}
}
