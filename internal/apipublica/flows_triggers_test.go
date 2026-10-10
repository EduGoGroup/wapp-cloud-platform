package apipublica_test

// flows_triggers_test.go — cubre de MountFlows las rutas I11–I13, el CRUD de reglas de disparo:
// que sirven los handlers de conversacion/admin sobre d.Triggers y d.TriggersDurableFlow, con el
// tenant del token. El detalle de cada validación lo prueba el paquete admin; aquí se prueba el
// cableado y lo que se ve desde la cara. Los dobles viven en flows_test.go.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

const (
	flowsDurableFlowID   = "carrito"
	flowsMsgRuleNotFound = "regla de disparo no encontrada"
)

// flowsRuleDTO es lo que la cara devuelve de una regla, con las dos marcas derivadas del listado.
type flowsRuleDTO struct {
	TriggerID           string `json:"trigger_id"`
	Kind                string `json:"kind"`
	Keyword             string `json:"keyword"`
	FlowID              string `json:"flow_id"`
	Enabled             bool   `json:"enabled"`
	ShadowedByEventList bool   `json:"shadowed_by_event_list"`
	FlowNeedsEvent      bool   `json:"flow_needs_event"`
}

// flowsTriggers pide una ruta de reglas con un token del tenant A que trae SOLO perm. checker nil
// se cablea como interfaz nil, que es el valor válido «ningún flujo es durable».
func flowsTriggers(t *testing.T, rules *flowsTriggersSpy, checker *flowsDurableSpy, perm, method, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	d := apipublica.FlowsDeps{Triggers: rules}
	if checker != nil {
		d.TriggersDurableFlow = checker
	}
	return flowsDo(t, d, perm, method, target, body)
}

// flowsDurable es un checker para el que solo flowsDurableFlowID tiene contenido durable.
func flowsDurable() *flowsDurableSpy {
	return &flowsDurableSpy{durable: map[string]bool{flowsDurableFlowID: true}}
}

// flowsWantCheckerTenant exige que al checker solo se le preguntara por el tenant del token.
func flowsWantCheckerTenant(t *testing.T, what string, checker *flowsDurableSpy) {
	t.Helper()
	if checker.calls == 0 {
		t.Errorf("%s: el checker de flujos durables no se consultó; quiero que la cara se lo pase al handler", what)
	}
	for _, tenant := range checker.gotTenants {
		if tenant != tenantA {
			t.Errorf("%s: el checker recibió el tenant %q, quiero el del token (%s)", what, tenant, tenantA)
		}
	}
}

// Los puertos de reglas los cumplen el store y el checker REALES del módulo conversación nuevo.
var (
	_ admin.TriggerStore       = (*trigger.MemoryStore)(nil)
	_ admin.DurableFlowChecker = (*admin.EngineDurableFlowChecker)(nil)
)

// TestMountFlows_TriggerCreate: I11 persiste la regla en el tenant del TOKEN (uno en el cuerpo o
// en la query no cuenta) y devuelve la que dio el store.
func TestMountFlows_TriggerCreate(t *testing.T) {
	rules := &flowsTriggersSpy{}
	other := apipublicahelpertest.TenantB
	h, rec := flowsTriggers(t, rules, nil, flowsPermTriggerCreate, http.MethodPost, flowsTargetTrigger+"?tenant_id="+other,
		`{"tenant_id":"`+other+`","kind":"keyword","keyword":"hola","flow_id":"`+flowsFlowID+`"}`)
	wantCode(t, "I11", rec, http.StatusCreated)
	var created flowsRuleDTO
	wantJSON(t, "I11", rec, &created)
	want := flowsRuleDTO{TriggerID: flowsTriggerID, Kind: "keyword", Keyword: "hola", FlowID: flowsFlowID, Enabled: true}
	if created != want {
		t.Errorf("I11: respuesta %+v, quiero %+v", created, want)
	}
	if len(rules.inserted) != 1 || rules.inserted[0].TenantID != tenantA || rules.inserted[0].Kind != trigger.KindKeyword || rules.inserted[0].FlowID != flowsFlowID {
		t.Errorf("I11: el store recibió %+v; quiero UNA regla keyword hacia %s en el tenant del token (%s)", rules.inserted, flowsFlowID, tenantA)
	}
	flowsWantAudit(t, "I11", h, flowsPermTriggerCreate, flowsTriggerResource, "success", http.StatusCreated)

	rules = &flowsTriggersSpy{}
	h, rec = flowsTriggers(t, rules, nil, flowsPermTriggerCreate, http.MethodPost, flowsTargetTrigger, `{no es json`)
	wantCode(t, "cuerpo ilegible", rec, http.StatusBadRequest)
	flowsWantPlainError(t, "cuerpo ilegible", rec, "cuerpo JSON inválido")
	if len(rules.inserted) != 0 {
		t.Errorf("cuerpo ilegible: el store recibió %d reglas, quiero 0", len(rules.inserted))
	}
	flowsWantAudit(t, "cuerpo ilegible", h, flowsPermTriggerCreate, flowsTriggerResource, "failure", http.StatusBadRequest)
}

// TestMountFlows_TriggerCreateUsesTheDurableChecker: la regla hacia un flujo durable se rechaza
// con 422 solo si la cara le pasó el checker al handler; sin checker ningún flujo es durable.
func TestMountFlows_TriggerCreateUsesTheDurableChecker(t *testing.T) {
	body := `{"kind":"fallback","flow_id":"` + flowsDurableFlowID + `"}`

	rules, checker := &flowsTriggersSpy{}, flowsDurable()
	h, rec := flowsTriggers(t, rules, checker, flowsPermTriggerCreate, http.MethodPost, flowsTargetTrigger, body)
	wantCode(t, "flujo durable sin event_start", rec, http.StatusUnprocessableEntity)
	if len(rules.inserted) != 0 {
		t.Errorf("flujo durable sin event_start: el store recibió %d reglas, quiero 0", len(rules.inserted))
	}
	flowsWantCheckerTenant(t, "flujo durable sin event_start", checker)
	flowsWantAudit(t, "flujo durable sin event_start", h, flowsPermTriggerCreate, flowsTriggerResource, "failure", http.StatusUnprocessableEntity)

	rules = &flowsTriggersSpy{}
	_, rec = flowsTriggers(t, rules, nil, flowsPermTriggerCreate, http.MethodPost, flowsTargetTrigger, body)
	wantCode(t, "sin checker", rec, http.StatusCreated)
	if len(rules.inserted) != 1 {
		t.Errorf("sin checker: el store recibió %d reglas, quiero 1", len(rules.inserted))
	}
}

// TestMountFlows_TriggerList: I12 lista las reglas del tenant del token, con las marcas derivadas
// (la de flujo durable sale del checker que la cara pasa al handler), y `[]` si no hay ninguna.
func TestMountFlows_TriggerList(t *testing.T) {
	rules := &flowsTriggersSpy{rules: []trigger.Rule{
		{TenantID: tenantA, TriggerID: "r-1", Kind: trigger.KindKeyword, Keyword: "comprar", MatchType: trigger.MatchExact, FlowID: flowsDurableFlowID, Enabled: true},
		{TenantID: tenantA, TriggerID: "r-2", Kind: trigger.KindFallback, MatchType: trigger.MatchExact, FlowID: flowsFlowID, Enabled: true},
	}}
	checker := flowsDurable()
	h, rec := flowsTriggers(t, rules, checker, flowsPermTriggerRead, http.MethodGet, flowsTargetTrigger+"?tenant_id="+apipublicahelpertest.TenantB, "")
	wantCode(t, "I12", rec, http.StatusOK)
	var listed []flowsRuleDTO
	wantJSON(t, "I12", rec, &listed)
	want := []flowsRuleDTO{
		{TriggerID: "r-1", Kind: "keyword", Keyword: "comprar", FlowID: flowsDurableFlowID, Enabled: true, FlowNeedsEvent: true},
		{TriggerID: "r-2", Kind: "fallback", FlowID: flowsFlowID, Enabled: true, ShadowedByEventList: true},
	}
	if len(listed) != len(want) || listed[0] != want[0] || listed[1] != want[1] {
		t.Errorf("I12: listado %+v, quiero %+v", listed, want)
	}
	if rules.gotListTenant != tenantA {
		t.Errorf("I12: el store listó el tenant %q, quiero el del token (%s)", rules.gotListTenant, tenantA)
	}
	flowsWantCheckerTenant(t, "I12", checker)
	if n := len(h.Auditor().Records()); n != 0 {
		t.Errorf("I12: una lectura dejó %d registros de auditoría, quiero 0", n)
	}

	_, rec = flowsTriggers(t, &flowsTriggersSpy{}, nil, flowsPermTriggerRead, http.MethodGet, flowsTargetTrigger, "")
	wantCode(t, "sin reglas", rec, http.StatusOK)
	var empty []flowsRuleDTO
	wantJSON(t, "sin reglas", rec, &empty)
	if empty == nil || len(empty) != 0 {
		t.Errorf("sin reglas: cuerpo %q, quiero un arreglo vacío y no null", rec.Body.String())
	}
}

// TestMountFlows_TriggerDelete: I13 borra el {id} de la ruta en el tenant del token; un id que no
// existe (o que es de otro tenant) es 404.
func TestMountFlows_TriggerDelete(t *testing.T) {
	rules := &flowsTriggersSpy{}
	h, rec := flowsTriggers(t, rules, nil, flowsPermTriggerDelete, http.MethodDelete, flowsTargetRule+"?tenant_id="+apipublicahelpertest.TenantB, "")
	wantCode(t, "I13", rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("I13: el 204 lleva cuerpo (%q), quiero ninguno", rec.Body.String())
	}
	if rules.deletes != 1 || rules.gotDelTenant != tenantA || rules.gotDelRuleID != flowsTriggerID {
		t.Errorf("I13: el store recibió %d borrados de (%q, %q); quiero 1 de (%s, %s)", rules.deletes, rules.gotDelTenant, rules.gotDelRuleID, tenantA, flowsTriggerID)
	}
	flowsWantAudit(t, "I13", h, flowsPermTriggerDelete, flowsTriggerResource, "success", http.StatusNoContent)

	rules = &flowsTriggersSpy{deleteErr: trigger.ErrTriggerNotFound}
	h, rec = flowsTriggers(t, rules, nil, flowsPermTriggerDelete, http.MethodDelete, flowsTargetRule, "")
	wantCode(t, "regla ajena o inexistente", rec, http.StatusNotFound)
	flowsWantPlainError(t, "regla ajena o inexistente", rec, flowsMsgRuleNotFound)
	flowsWantAudit(t, "regla ajena o inexistente", h, flowsPermTriggerDelete, flowsTriggerResource, "failure", http.StatusNotFound)
}

// TestMountFlows_TriggerDeleteUsesTheDurableChecker: borrar la última event_start habilitada con
// una regla viva hacia un flujo durable es 422 solo si la cara le pasó el checker al handler.
func TestMountFlows_TriggerDeleteUsesTheDurableChecker(t *testing.T) {
	seeded := func() *flowsTriggersSpy {
		return &flowsTriggersSpy{rules: []trigger.Rule{
			{TenantID: tenantA, TriggerID: flowsTriggerID, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, Enabled: true, EventKind: trigger.EventKindCart},
			{TenantID: tenantA, TriggerID: "r-2", Kind: trigger.KindFallback, MatchType: trigger.MatchExact, FlowID: flowsDurableFlowID, Enabled: true},
		}}
	}

	rules, checker := seeded(), flowsDurable()
	h, rec := flowsTriggers(t, rules, checker, flowsPermTriggerDelete, http.MethodDelete, flowsTargetRule, "")
	wantCode(t, "última event_start", rec, http.StatusUnprocessableEntity)
	if rules.deletes != 0 {
		t.Errorf("última event_start: el store recibió %d borrados, quiero 0", rules.deletes)
	}
	flowsWantCheckerTenant(t, "última event_start", checker)
	flowsWantAudit(t, "última event_start", h, flowsPermTriggerDelete, flowsTriggerResource, "failure", http.StatusUnprocessableEntity)

	rules = seeded()
	_, rec = flowsTriggers(t, rules, nil, flowsPermTriggerDelete, http.MethodDelete, flowsTargetRule, "")
	wantCode(t, "sin checker", rec, http.StatusNoContent)
	if rules.deletes != 1 {
		t.Errorf("sin checker: el store recibió %d borrados, quiero 1", rules.deletes)
	}
}
