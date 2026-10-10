package apipublica_test

// flows_test.go — cubre el contrato de flows.go (FlowsStore, FlowsDeps, MountFlows): el montaje y
// su condición, las cadenas de las siete rutas, y I1–I3 (publicar, listar y leer definiciones).
// Aquí viven además los dobles y los auxiliares del área. El arranque de una conversación (I4)
// está en flows_start_test.go y las reglas de disparo (I11–I13) en flows_triggers_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

const (
	flowsPatternCreate = "POST /api/v1/flows"
	flowsPatternList   = "GET /api/v1/flows"
	flowsPatternGet    = "GET /api/v1/flows/{id}"
	flowsPatternStart  = "POST /api/v1/flows/{id}/start"

	flowsPatternTriggerCreate = "POST /api/v1/triggers"
	flowsPatternTriggerList   = "GET /api/v1/triggers"
	flowsPatternTriggerDelete = "DELETE /api/v1/triggers/{id}"

	flowsPermCreate = "flows.create"
	flowsPermRead   = "flows.read"
	flowsPermStart  = "flows.start"
	flowsResource   = "flow"

	flowsPermTriggerCreate = "triggers.create"
	flowsPermTriggerRead   = "triggers.read"
	flowsPermTriggerDelete = "triggers.delete"
	flowsTriggerResource   = "trigger"

	flowsFlowID        = "menu-soporte"
	flowsTarget        = "/api/v1/flows"
	flowsTargetOne     = flowsTarget + "/" + flowsFlowID
	flowsTargetStart   = flowsTargetOne + "/start"
	flowsTriggerID     = "7c1f0a52-3b64-4c8e-9d2a-5e6f7a8b9c0d"
	flowsTargetTrigger = "/api/v1/triggers"
	flowsTargetRule    = flowsTargetTrigger + "/" + flowsTriggerID

	flowsStartBody   = `{"session_id":"sess-a","contact":"+1 555 123-4567"}`
	flowsTriggerBody = `{"kind":"keyword","keyword":"hola","flow_id":"` + flowsFlowID + `"}`

	// flowsDefinition es una definición válida mínima, con solo tipos de nodo core.
	flowsDefinition = `{"flow_id":"` + flowsFlowID + `","version":1,"initial":"root","nodes":{` +
		`"root":{"type":"menu","prompt":"Elige:\n1) A","options":{"1":"a"}},` +
		`"a":{"type":"message","text":"Elegiste A","next":null}}}`
	flowsCreateBody = `{"definition":` + flowsDefinition + `}`
)

// Los puertos de la cara los cumplen los adaptadores REALES del módulo conversación nuevo, y el
// store de definiciones de la cara sirve tal cual a admin.DefinitionHandler (I1).
var (
	_ apipublica.FlowsStore = (*store.MemoryRepository)(nil)
	_ apipublica.FlowsStore = (*store.PostgresRepository)(nil)
	_ admin.DefinitionStore = apipublica.FlowsStore(nil)
)

// flowsStoreSpy es FlowsStore: contesta lo sembrado (o su error) y apunta lo que recibió.
type flowsStoreSpy struct {
	version   int
	insertErr error
	flow      model.Flow
	latestErr error
	summaries []store.FlowSummary
	listErr   error

	inserts, latests, lists int
	gotTenant, gotFlowID    string
	gotInserted             model.Flow
}

var _ apipublica.FlowsStore = (*flowsStoreSpy)(nil)

func (s *flowsStoreSpy) InsertDefinition(_ context.Context, tenantID string, f model.Flow) (int, error) {
	s.inserts++
	s.gotTenant, s.gotInserted = tenantID, f
	return s.version, s.insertErr
}

func (s *flowsStoreSpy) LatestDefinition(_ context.Context, tenantID, flowID string) (model.Flow, error) {
	s.latests++
	s.gotTenant, s.gotFlowID = tenantID, flowID
	return s.flow, s.latestErr
}

func (s *flowsStoreSpy) ListDefinitions(_ context.Context, tenantID string) ([]store.FlowSummary, error) {
	s.lists++
	s.gotTenant = tenantID
	return s.summaries, s.listErr
}

// flowsStarterSpy es admin.Starter: devuelve ack o err y apunta la llamada, y si su contexto
// traía plazo.
type flowsStarterSpy struct {
	ack *cloudlinkv1.Ack
	err error

	calls                            int
	gotTenant, gotFlowID, gotSession string
	gotRef                           contact.Ref
	hasDeadline                      bool
}

var _ admin.Starter = (*flowsStarterSpy)(nil)

func (s *flowsStarterSpy) Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	s.calls++
	s.gotTenant, s.gotFlowID, s.gotSession, s.gotRef = tenantID, flowID, sessionID, ref
	_, s.hasDeadline = ctx.Deadline()
	return s.ack, s.err
}

// flowsTriggersSpy es admin.TriggerStore: lista lo sembrado, devuelve la regla que le insertan
// con un id fijo y apunta lo que recibió.
type flowsTriggersSpy struct {
	rules     []trigger.Rule
	deleteErr error

	inserted                   []trigger.Rule
	gotListTenant              string
	gotDelTenant, gotDelRuleID string
	deletes                    int
}

var _ admin.TriggerStore = (*flowsTriggersSpy)(nil)

func (s *flowsTriggersSpy) Insert(_ context.Context, r trigger.Rule) (trigger.Rule, error) {
	s.inserted = append(s.inserted, r)
	r.TriggerID = flowsTriggerID
	return r, nil
}

func (s *flowsTriggersSpy) List(_ context.Context, tenantID string) ([]trigger.Rule, error) {
	s.gotListTenant = tenantID
	return s.rules, nil
}

func (s *flowsTriggersSpy) Delete(_ context.Context, tenantID, triggerID string) error {
	s.deletes++
	s.gotDelTenant, s.gotDelRuleID = tenantID, triggerID
	return s.deleteErr
}

// flowsDurableSpy es admin.DurableFlowChecker: dice que son durables los flujos de durable y
// apunta con qué tenant se le preguntó.
type flowsDurableSpy struct {
	durable    map[string]bool
	calls      int
	gotTenants []string
}

var _ admin.DurableFlowChecker = (*flowsDurableSpy)(nil)

func (s *flowsDurableSpy) FlowHasDurableContent(_ context.Context, tenantID, flowID string) (bool, error) {
	s.calls++
	s.gotTenants = append(s.gotTenants, tenantID)
	return s.durable[flowID], nil
}

// flowsModulesSpy es admin.ModuleTypeSource y cuenta cuántas veces se le piden los tipos.
type flowsModulesSpy struct{ calls int }

var _ admin.ModuleTypeSource = (*flowsModulesSpy)(nil)

func (s *flowsModulesSpy) Types() []string {
	s.calls++
	return []string{"cart"}
}

// flowsCara monta las rutas del área con k y d.
func flowsCara(k apipublica.Common, d apipublica.FlowsDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountFlows(c, k, d)
	return c
}

// flowsDo sirve UNA petición con un token del tenant A que trae SOLO perm, y devuelve también el
// banco para mirar la auditoría.
func flowsDo(t *testing.T, d apipublica.FlowsDeps, perm, method, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	return h, h.Call(flowsCara(h.Common(), d), h.With(tenantA, perm), method, target, body)
}

// flowsWantAudit exige EXACTAMENTE un registro de auditoría con esa acción, ese recurso, ese
// resultado y ese código.
func flowsWantAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, perm, resource, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: quedaron %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != perm || r.Resource != resource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, perm, resource, result, status)
	}
}

// flowsWantPlainError exige el cuerpo de error de los handlers de conversacion/admin (I1 e
// I11–I13): texto plano, el mensaje y un salto de línea, no el {"error":…} del resto de la cara.
func flowsWantPlainError(t *testing.T, what string, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	if got := rec.Body.String(); got != msg+"\n" {
		t.Errorf("%s: cuerpo %q, quiero el texto plano %q", what, got, msg+"\n")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("%s: Content-Type %q, quiero text/plain", what, got)
	}
}

// flowsFullDeps son las deps con todo cableado para el camino feliz de las siete rutas.
func flowsFullDeps() apipublica.FlowsDeps {
	return apipublica.FlowsDeps{
		Flows:    &flowsStoreSpy{version: 1},
		Starter:  &flowsStarterSpy{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}},
		Triggers: &flowsTriggersSpy{},
	}
}

var (
	flowsAllPatterns = []string{flowsPatternCreate, flowsPatternList, flowsPatternGet, flowsPatternStart,
		flowsPatternTriggerCreate, flowsPatternTriggerList, flowsPatternTriggerDelete}
	flowsUnconditionalPatterns = flowsAllPatterns[:4]
)

func TestMountFlows_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := flowsCara(h.Common(), flowsFullDeps())
	wantPatterns(t, "I1–I4 e I11–I13", cara, flowsAllPatterns)
	for _, rc := range []routeCase{
		{id: "I1", method: http.MethodPost, target: flowsTarget, body: flowsCreateBody, perm: flowsPermCreate, resource: flowsResource, want: http.StatusCreated},
		{id: "I2", method: http.MethodGet, target: flowsTarget, perm: flowsPermRead, want: http.StatusOK},
		{id: "I3", method: http.MethodGet, target: flowsTargetOne, perm: flowsPermRead, want: http.StatusOK},
		{id: "I4", method: http.MethodPost, target: flowsTargetStart, body: flowsStartBody, perm: flowsPermStart, resource: flowsResource, want: http.StatusOK},
		{id: "I11", method: http.MethodPost, target: flowsTargetTrigger, body: flowsTriggerBody, perm: flowsPermTriggerCreate, resource: flowsTriggerResource, want: http.StatusCreated},
		{id: "I12", method: http.MethodGet, target: flowsTargetTrigger, perm: flowsPermTriggerRead, want: http.StatusOK},
		{id: "I13", method: http.MethodDelete, target: flowsTargetRule, perm: flowsPermTriggerDelete, resource: flowsTriggerResource, want: http.StatusNoContent},
	} {
		checkChain(t, h, cara, rc)
	}
}

// TestMountFlows_EachPermissionIsItsOwn: leer flujos no abre ni publicarlos ni arrancarlos, y
// leer reglas no abre crearlas ni borrarlas.
func TestMountFlows_EachPermissionIsItsOwn(t *testing.T) {
	starter := &flowsStarterSpy{ack: &cloudlinkv1.Ack{Ok: true}}
	d := flowsFullDeps()
	d.Starter = starter
	for _, tc := range []struct{ name, perm, method, target, body string }{
		{"read_does_not_create", flowsPermRead, http.MethodPost, flowsTarget, flowsCreateBody},
		{"read_does_not_start", flowsPermRead, http.MethodPost, flowsTargetStart, flowsStartBody},
		{"create_does_not_start", flowsPermCreate, http.MethodPost, flowsTargetStart, flowsStartBody},
		{"trigger_read_does_not_create", flowsPermTriggerRead, http.MethodPost, flowsTargetTrigger, flowsTriggerBody},
		{"trigger_create_does_not_delete", flowsPermTriggerCreate, http.MethodDelete, flowsTargetRule, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, rec := flowsDo(t, d, tc.perm, tc.method, tc.target, tc.body)
			wantCode(t, tc.name, rec, http.StatusForbidden)
			if n := len(h.Auditor().Records()); n != 0 {
				t.Errorf("%s: un 403 dejó %d registros de auditoría, quiero 0", tc.name, n)
			}
		})
	}
	if starter.calls != 0 {
		t.Errorf("el motor arrancó %d conversaciones sin el permiso flows.start, quiero 0", starter.calls)
	}
}

// TestMountFlows_TriggersNeedTheirStore: I1–I4 no tienen condición (existen con las deps vacías)
// e I11–I13 van las tres o ninguna según d.Triggers (trampa T-11: sin store, 404 y no 500). El
// checker de flujos durables no entra en la condición.
func TestMountFlows_TriggersNeedTheirStore(t *testing.T) {
	h := apipublicahelpertest.New(t)
	for _, tc := range []struct {
		name string
		deps apipublica.FlowsDeps
		want []string
	}{
		{"empty_deps_mount_the_flow_routes", apipublica.FlowsDeps{}, flowsUnconditionalPatterns},
		{"checker_alone_mounts_no_trigger_route", apipublica.FlowsDeps{TriggersDurableFlow: &flowsDurableSpy{}}, flowsUnconditionalPatterns},
		{"store_without_checker_mounts_all", apipublica.FlowsDeps{Triggers: &flowsTriggersSpy{}}, flowsAllPatterns},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantPatterns(t, tc.name, flowsCara(h.Common(), tc.deps), tc.want)
		})
	}

	cara := flowsCara(h.Common(), apipublica.FlowsDeps{})
	token := h.With(tenantA, flowsPermTriggerCreate, flowsPermTriggerRead, flowsPermTriggerDelete)
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodPost, flowsTargetTrigger, flowsTriggerBody},
		{http.MethodGet, flowsTargetTrigger, ""},
		{http.MethodDelete, flowsTargetRule, ""},
	} {
		wantCode(t, "sin store de reglas: "+tc.method+" "+tc.target, h.Call(cara, token, tc.method, tc.target, tc.body), http.StatusNotFound)
	}
}

func TestMountFlows_OtherMethodIs405(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := flowsCara(h.Common(), flowsFullDeps())
	token := h.With(tenantA, "flows.*", "triggers.*")
	for _, tc := range []struct{ method, target string }{
		{http.MethodDelete, flowsTarget},
		{http.MethodPut, flowsTargetOne},
		{http.MethodGet, flowsTargetStart},
		{http.MethodPut, flowsTargetTrigger},
		{http.MethodGet, flowsTargetRule},
	} {
		wantCode(t, tc.method+" "+tc.target, h.Call(cara, token, tc.method, tc.target, ""), http.StatusMethodNotAllowed)
	}
}

// TestMountFlows_NilMWPanicsAtMount: I1–I4 se montan siempre, así que sin middleware el fallo de
// cableado salta también con las deps vacías.
func TestMountFlows_NilMWPanicsAtMount(t *testing.T) {
	for name, d := range map[string]apipublica.FlowsDeps{"empty_deps": {}, "full_deps": flowsFullDeps()} {
		v := recuperar(func() { apipublica.MountFlows(apipublica.Nueva(), apipublica.Common{}, d) })
		if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountFlows") {
			t.Errorf("%s: MountFlows con MW nil: panic = %v; quiero un panic de cableado que nombre MountFlows", name, v)
		}
	}
}

// TestMountFlows_ModuleTypesAreReadOnceAtMount: los tipos de los módulos se leen al montar y no
// en cada alta; con Modules nil el alta funciona igual (solo tipos core).
func TestMountFlows_ModuleTypesAreReadOnceAtMount(t *testing.T) {
	h := apipublicahelpertest.New(t)
	mods := &flowsModulesSpy{}
	cara := flowsCara(h.Common(), apipublica.FlowsDeps{Flows: &flowsStoreSpy{version: 1}, Modules: mods})
	if mods.calls != 1 {
		t.Errorf("al montar se pidieron los tipos de módulo %d veces, quiero 1", mods.calls)
	}
	token := h.With(tenantA, flowsPermCreate)
	for range 2 {
		wantCode(t, "alta con módulos", h.Call(cara, token, http.MethodPost, flowsTarget, flowsCreateBody), http.StatusCreated)
	}
	if mods.calls != 1 {
		t.Errorf("tras dos altas los tipos de módulo se pidieron %d veces, quiero 1 (se leen al montar)", mods.calls)
	}

	_, rec := flowsDo(t, apipublica.FlowsDeps{Flows: &flowsStoreSpy{version: 1}}, flowsPermCreate, http.MethodPost, flowsTarget, flowsCreateBody)
	wantCode(t, "alta sin Modules", rec, http.StatusCreated)
}

// TestMountFlows_CreateIsTheAdminHandler: I1 sirve admin.DefinitionHandler sobre d.Flows, con el
// tenant del token y la versión que asignó el store.
func TestMountFlows_CreateIsTheAdminHandler(t *testing.T) {
	flows := &flowsStoreSpy{version: 7}
	other := apipublicahelpertest.TenantB
	h, rec := flowsDo(t, apipublica.FlowsDeps{Flows: flows}, flowsPermCreate, http.MethodPost,
		flowsTarget+"?tenant_id="+other, `{"tenant_id":"`+other+`","definition":`+flowsDefinition+`}`)
	wantCode(t, "I1", rec, http.StatusCreated)
	var created struct {
		FlowID  string `json:"flow_id"`
		Version int    `json:"version"`
	}
	wantJSON(t, "I1", rec, &created)
	if created.FlowID != flowsFlowID || created.Version != 7 {
		t.Errorf("I1: respuesta %+v, quiero flow_id %s y la versión 7 que asignó el store", created, flowsFlowID)
	}
	if flows.inserts != 1 || flows.gotTenant != tenantA || flows.gotInserted.FlowID != flowsFlowID {
		t.Errorf("I1: el store recibió %d altas, tenant %q y flujo %q; quiero 1, %s y %s",
			flows.inserts, flows.gotTenant, flows.gotInserted.FlowID, tenantA, flowsFlowID)
	}
	flowsWantAudit(t, "I1", h, flowsPermCreate, flowsResource, "success", http.StatusCreated)

	for _, tc := range []struct {
		name, body string
		insertErr  error
		code       int
		msg        string
		inserts    int
	}{
		{"unreadable_body_is_400", `{no es json`, nil, http.StatusBadRequest, "cuerpo JSON inválido", 0},
		{"missing_definition_is_400", `{}`, nil, http.StatusBadRequest, "definition es requerida", 0},
		{"store_failure_is_500", flowsCreateBody, errors.New("dsn=secreto"), http.StatusInternalServerError, "no se pudo persistir la definición", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flows := &flowsStoreSpy{version: 1, insertErr: tc.insertErr}
			h, rec := flowsDo(t, apipublica.FlowsDeps{Flows: flows}, flowsPermCreate, http.MethodPost, flowsTarget, tc.body)
			wantCode(t, tc.name, rec, tc.code)
			flowsWantPlainError(t, tc.name, rec, tc.msg)
			if flows.inserts != tc.inserts {
				t.Errorf("%s: el store recibió %d altas, quiero %d", tc.name, flows.inserts, tc.inserts)
			}
			flowsWantAudit(t, tc.name, h, flowsPermCreate, flowsResource, "failure", tc.code)
		})
	}

	_, rec = flowsDo(t, apipublica.FlowsDeps{Flows: &flowsStoreSpy{}}, flowsPermCreate, http.MethodPost, flowsTarget,
		`{"definition":{"flow_id":"x","version":1,"initial":"no-existe","nodes":{}}}`)
	wantCode(t, "definición inválida", rec, http.StatusBadRequest)
	if got := rec.Body.String(); !strings.HasPrefix(got, "definición de flujo inválida: ") {
		t.Errorf("definición inválida: cuerpo %q, quiero el prefijo \"definición de flujo inválida: \"", got)
	}
}

func TestMountFlows_List(t *testing.T) {
	caracas := time.FixedZone("-04", -4*60*60)
	for _, tc := range []struct {
		name      string
		summaries []store.FlowSummary
		want      string
	}{
		{"no_flows_is_an_empty_array", nil, `[]`},
		{"store_order_and_utc_seconds", []store.FlowSummary{
			{FlowID: "zeta", Version: 4, CreatedAt: time.Date(2026, 3, 1, 20, 30, 15, 999, caracas)},
			{FlowID: "alfa", Version: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		}, `[{"flow_id":"zeta","version":4,"created_at":"2026-03-02T00:30:15Z"},{"flow_id":"alfa","version":1,"created_at":"2026-01-02T03:04:05Z"}]`},
		{"zero_instant_is_omitted", []store.FlowSummary{{FlowID: "sin-fecha", Version: 2}}, `[{"flow_id":"sin-fecha","version":2}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flows := &flowsStoreSpy{summaries: tc.summaries}
			h, rec := flowsDo(t, apipublica.FlowsDeps{Flows: flows}, flowsPermRead, http.MethodGet,
				flowsTarget+"?tenant_id="+apipublicahelpertest.TenantB, "")
			wantCode(t, tc.name, rec, http.StatusOK)
			wantExactBody(t, tc.name, rec, tc.want)
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("%s: Content-Type %q, quiero application/json", tc.name, got)
			}
			if flows.lists != 1 || flows.gotTenant != tenantA {
				t.Errorf("%s: el store recibió %d listados del tenant %q; quiero 1 del tenant del token (%s)", tc.name, flows.lists, flows.gotTenant, tenantA)
			}
			if n := len(h.Auditor().Records()); n != 0 {
				t.Errorf("%s: una lectura dejó %d registros de auditoría, quiero 0", tc.name, n)
			}
		})
	}

	_, rec := flowsDo(t, apipublica.FlowsDeps{Flows: &flowsStoreSpy{listErr: errors.New("dsn=secreto")}}, flowsPermRead, http.MethodGet, flowsTarget, "")
	wantCode(t, "fallo del store", rec, http.StatusInternalServerError)
	wantExactBody(t, "fallo del store", rec, `{"error":"no se pudieron listar los flujos"}`)
}

func TestMountFlows_Get(t *testing.T) {
	next := "fin"
	flow := model.Flow{FlowID: flowsFlowID, Version: 3, Initial: "root", Nodes: map[string]model.Node{
		"root": {Type: "message", Text: "Hola", Next: &next},
	}}
	flows := &flowsStoreSpy{flow: flow}
	h, rec := flowsDo(t, apipublica.FlowsDeps{Flows: flows}, flowsPermRead, http.MethodGet,
		flowsTargetOne+"?tenant_id="+apipublicahelpertest.TenantB, "")
	wantCode(t, "I3", rec, http.StatusOK)
	wantExactBody(t, "I3", rec, `{"flow_id":"`+flowsFlowID+`","version":3,"initial":"root","nodes":{"root":{"type":"message","text":"Hola","next":"fin"}}}`)
	if flows.latests != 1 || flows.gotTenant != tenantA || flows.gotFlowID != flowsFlowID {
		t.Errorf("I3: el store recibió %d lecturas de (%q, %q); quiero 1 de (%s, %s)", flows.latests, flows.gotTenant, flows.gotFlowID, tenantA, flowsFlowID)
	}
	if n := len(h.Auditor().Records()); n != 0 {
		t.Errorf("I3: una lectura dejó %d registros de auditoría, quiero 0", n)
	}

	// El id no se valida ni se normaliza aquí: lo que diga la ruta.
	flows = &flowsStoreSpy{flow: flow}
	_, rec = flowsDo(t, apipublica.FlowsDeps{Flows: flows}, flowsPermRead, http.MethodGet, flowsTarget+"/Menu_SOPORTE.v2", "")
	wantCode(t, "id libre", rec, http.StatusOK)
	if flows.gotFlowID != "Menu_SOPORTE.v2" {
		t.Errorf("id libre: el store recibió el id %q, quiero \"Menu_SOPORTE.v2\"", flows.gotFlowID)
	}

	for _, tc := range []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"not_found_is_404", store.ErrDefinitionNotFound, http.StatusNotFound, "flujo no encontrado"},
		{"wrapped_not_found_is_404", fmt.Errorf("leyendo %q: %w", flowsFlowID, store.ErrDefinitionNotFound), http.StatusNotFound, "flujo no encontrado"},
		{"other_failure_is_500", errors.New("dsn=secreto"), http.StatusInternalServerError, "no se pudo leer el flujo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rec := flowsDo(t, apipublica.FlowsDeps{Flows: &flowsStoreSpy{latestErr: tc.err}}, flowsPermRead, http.MethodGet, flowsTargetOne, "")
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, `{"error":"`+tc.msg+`"}`)
		})
	}
}
