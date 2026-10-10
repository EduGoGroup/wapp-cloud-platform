//go:build pendiente

package admin_test

// triggers_test.go — el alta de reglas de disparo (POST .../triggers) y los ayudantes
// del CRUD. El listado y la baja siguen en triggers_list_test.go y
// triggers_delete_test.go (mismo origen, partido por tema, E-13).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// El puerto lo satisfacen los dos stores reales SIN adaptador.
var (
	_ admin.TriggerStore = (*trigger.MemoryStore)(nil)
	_ admin.TriggerStore = (*trigger.PostgresStore)(nil)
)

const (
	durableFlow = "carrito"
	plainFlow   = "menu"

	msgCheckerFailed = "no se pudo verificar el contenido durable del flujo"
	msgListFailed    = "no se pudieron listar las reglas de disparo"
)

var errInjected = errors.New("fallo inyectado")

// recordingChecker es un DurableFlowChecker que dice durable SOLO para durableFlow
// (o falla, si err no es nil) y apunta cada consulta como "tenant/flujo".
type recordingChecker struct {
	err error

	mu    sync.Mutex
	asked []string
}

func (c *recordingChecker) FlowHasDurableContent(_ context.Context, tenantID, flowID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, tenantID+"/"+flowID)
	if c.err != nil {
		return false, c.err
	}
	return flowID == durableFlow, nil
}

func (c *recordingChecker) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Sorted(slices.Values(c.asked))
}

// faultyStore es un store en memoria al que se le puede hacer fallar cada operación.
type faultyStore struct {
	*trigger.MemoryStore
	insertErr, listErr, deleteErr error
	lists                         int
}

func newFaultyStore() *faultyStore { return &faultyStore{MemoryStore: trigger.NewMemoryStore()} }

func (s *faultyStore) Insert(ctx context.Context, r trigger.Rule) (trigger.Rule, error) {
	if s.insertErr != nil {
		return trigger.Rule{}, s.insertErr
	}
	return s.MemoryStore.Insert(ctx, r)
}

func (s *faultyStore) List(ctx context.Context, tenantID string) ([]trigger.Rule, error) {
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.MemoryStore.List(ctx, tenantID)
}

func (s *faultyStore) Delete(ctx context.Context, tenantID, triggerID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.MemoryStore.Delete(ctx, tenantID, triggerID)
}

// newRule es una regla HABILITADA del tenant del token.
func newRule(kind trigger.Kind, flowID string) trigger.Rule {
	return trigger.Rule{
		TenantID: tokenTenant, Kind: kind, Keyword: string(kind) + "_kw",
		MatchType: trigger.MatchExact, FlowID: flowID, Enabled: true,
	}
}

func eventStart(enabled bool) trigger.Rule {
	r := newRule(trigger.KindEventStart, "")
	r.EventKind, r.Enabled = trigger.EventKindCart, enabled
	return r
}

func seed(t *testing.T, st admin.TriggerStore, r trigger.Rule) trigger.Rule {
	t.Helper()
	saved, err := st.Insert(context.Background(), r)
	if err != nil {
		t.Fatalf("sembrar la regla %+v: %v", r, err)
	}
	return saved
}

func rulesOf(t *testing.T, st admin.TriggerStore, tenantID string) []trigger.Rule {
	t.Helper()
	rules, err := st.List(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("listar las reglas de %s: %v", tenantID, err)
	}
	return rules
}

func create(st admin.TriggerStore, checker admin.DurableFlowChecker, body string) *httptest.ResponseRecorder {
	return serve(admin.CreateTriggerHandler(st, checker), operator(), http.MethodPost, "/api/v1/triggers", body)
}

// decodeObject lee un cuerpo JSON como objeto: las claves AUSENTES son lo que se mira.
func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("el cuerpo no es un objeto JSON (%v): %s", err, raw)
	}
	return got
}

func TestCreateTriggerHandler_Created(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want map[string]any // sin trigger_id
	}{
		{"keyword with defaults", `{"kind":"keyword","keyword":"hola","flow_id":"menu"}`,
			map[string]any{"kind": "keyword", "keyword": "hola", "match_type": "exact", "flow_id": "menu", "priority": 0.0, "enabled": true}},
		{"text fields are trimmed and explicit values kept",
			`{"kind":" keyword ","keyword":" hola ","match_type":" contains ","flow_id":" menu ","priority":5,"enabled":false,"session_id":" s1 "}`,
			map[string]any{"kind": "keyword", "keyword": "hola", "match_type": "contains", "flow_id": "menu", "priority": 5.0,
				"enabled": false, "session_id": "s1"}},
		{"tenant_id and trigger_id of the body are ignored",
			`{"tenant_id":"otro","trigger_id":"elegido","kind":"keyword","keyword":"hola","flow_id":"menu"}`,
			map[string]any{"kind": "keyword", "keyword": "hola", "match_type": "exact", "flow_id": "menu", "priority": 0.0, "enabled": true}},
		{"fallback has no keyword and no derived marks", `{"kind":"fallback","flow_id":"menu"}`,
			map[string]any{"kind": "fallback", "match_type": "exact", "flow_id": "menu", "priority": 0.0, "enabled": true}},
		{"escape with message and no flow", `{"kind":"escape","keyword":"salir","message":" Hasta luego "}`,
			map[string]any{"kind": "escape", "keyword": "salir", "match_type": "exact", "priority": 0.0, "enabled": true, "message": "Hasta luego"}},
		{"llm with a two-letter intent name", `{"kind":"llm","keyword":"ab","flow_id":"menu"}`,
			map[string]any{"kind": "llm", "keyword": "ab", "match_type": "exact", "flow_id": "menu", "priority": 0.0, "enabled": true}},
		{"llm admits event_kind", `{"kind":"llm","keyword":"hacer_pedido","flow_id":"carrito","event_kind":" cart "}`,
			map[string]any{"kind": "llm", "keyword": "hacer_pedido", "match_type": "exact", "flow_id": "carrito", "priority": 0.0,
				"enabled": true, "event_kind": "cart"}},
		{"event_start does not need flow_id", `{"kind":"event_start","keyword":"menu","event_kind":"menu"}`,
			map[string]any{"kind": "event_start", "keyword": "menu", "match_type": "exact", "priority": 0.0, "enabled": true, "event_kind": "menu"}},
		{"event_start admits flow_id", `{"kind":"event_start","keyword":"carrito","event_kind":"cart","flow_id":"carrito"}`,
			map[string]any{"kind": "event_start", "keyword": "carrito", "match_type": "exact", "flow_id": "carrito", "priority": 0.0,
				"enabled": true, "event_kind": "cart"}},
		{"event_stop only needs keyword", `{"kind":"event_stop","keyword":"parar"}`,
			map[string]any{"kind": "event_stop", "keyword": "parar", "match_type": "exact", "priority": 0.0, "enabled": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := trigger.NewMemoryStore()

			rec := create(st, nil, tc.body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("código = %d, quiero 201 (cuerpo: %q)", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, quiero application/json", ct)
			}
			rules := rulesOf(t, st, tokenTenant)
			if len(rules) != 1 {
				t.Fatalf("reglas del tenant del token = %d, quiero 1 (%+v)", len(rules), rules)
			}
			got := decodeObject(t, rec.Body.Bytes())
			if id, ok := got["trigger_id"].(string); !ok || id == "" || id != rules[0].TriggerID {
				t.Errorf("trigger_id = %v, quiero el que asignó el store (%q)", got["trigger_id"], rules[0].TriggerID)
			}
			delete(got, "trigger_id")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("cuerpo = %v, quiero %v", got, tc.want)
			}
			if len(rulesOf(t, st, "otro")) != 0 {
				t.Error("la regla se guardó para el tenant del cuerpo")
			}
		})
	}
}

func TestCreateTriggerHandler_Rejections(t *testing.T) {
	t.Parallel()
	const (
		msgKind      = "kind inválido (usar keyword|fallback|escape|llm|event_start|event_stop)"
		msgMatchType = "match_type inválido (usar exact|contains)"
		msgNeedsKind = "event_kind es requerido para kind event_start (el tipo de evento que arranca: menu|cart|survey|media)"
		msgOnlyKind  = "event_kind solo es válido para kind event_start o llm"
		msgBadKind   = "event_kind inválido: los valores admitidos son menu|cart|survey|media"
		msgIntent    = "keyword de kind llm debe ser un nombre de intención válido (^[a-z][a-z0-9_]{1,63}$)"
		msgMessage   = "message solo es válido para kind escape"
	)
	needsKeyword := func(kind string) string { return "keyword es requerido para kind " + kind }
	needsFlow := func(kind string) string { return "flow_id es requerido para kind " + kind }
	long := "a" + strings.Repeat("b", 64) // 65 caracteres: uno más de los que admite el nombre
	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed json", `{`, msgInvalidJSON},
		{"unknown kind", `{"kind":"regex","keyword":"x","flow_id":"f"}`, msgKind},
		{"empty kind", `{"keyword":"x","flow_id":"f"}`, msgKind},
		{"kind is checked before match_type", `{"kind":"regex","match_type":"prefix"}`, msgKind},
		{"unknown match_type", `{"kind":"keyword","keyword":"x","flow_id":"f","match_type":"prefix"}`, msgMatchType},
		{"match_type is checked before the fields", `{"kind":"keyword","match_type":"prefix"}`, msgMatchType},
		{"keyword without keyword", `{"kind":"keyword","flow_id":"f"}`, needsKeyword("keyword")},
		{"keyword with blank keyword", `{"kind":"keyword","keyword":"   ","flow_id":"f"}`, needsKeyword("keyword")},
		{"escape without keyword", `{"kind":"escape"}`, needsKeyword("escape")},
		{"llm without keyword", `{"kind":"llm","flow_id":"f"}`, needsKeyword("llm")},
		{"event_start without keyword", `{"kind":"event_start","event_kind":"cart"}`, needsKeyword("event_start")},
		{"event_stop without keyword", `{"kind":"event_stop"}`, needsKeyword("event_stop")},
		{"keyword without flow_id", `{"kind":"keyword","keyword":"x"}`, needsFlow("keyword")},
		{"fallback without flow_id", `{"kind":"fallback"}`, needsFlow("fallback")},
		{"llm without flow_id", `{"kind":"llm","keyword":"pedido"}`, needsFlow("llm")},
		{"event_start without event_kind", `{"kind":"event_start","keyword":"carrito"}`, msgNeedsKind},
		{"event_kind on keyword", `{"kind":"keyword","keyword":"x","flow_id":"f","event_kind":"cart"}`, msgOnlyKind},
		{"event_kind on fallback", `{"kind":"fallback","flow_id":"f","event_kind":"cart"}`, msgOnlyKind},
		{"event_kind on escape", `{"kind":"escape","keyword":"salir","event_kind":"cart"}`, msgOnlyKind},
		{"event_kind on event_stop", `{"kind":"event_stop","keyword":"parar","event_kind":"cart"}`, msgOnlyKind},
		{"forbidden event_kind is reported before its vocabulary", `{"kind":"keyword","keyword":"x","flow_id":"f","event_kind":"carrrito"}`,
			msgOnlyKind},
		{"event_start with a plausible typo", `{"kind":"event_start","keyword":"carrito","event_kind":"carrrito"}`, msgBadKind},
		{"event_start with a malformed event_kind", `{"kind":"event_start","keyword":"carrito","event_kind":"Cart Grande"}`, msgBadKind},
		{"llm with an unknown event_kind", `{"kind":"llm","keyword":"pedido","flow_id":"f","event_kind":"pizza"}`, msgBadKind},
		{"event_kind vocabulary is checked before the intent name", `{"kind":"llm","keyword":"Pedido","flow_id":"f","event_kind":"pizza"}`,
			msgBadKind},
		{"llm keyword with uppercase", `{"kind":"llm","keyword":"Pedido","flow_id":"f"}`, msgIntent},
		{"llm keyword with a space", `{"kind":"llm","keyword":"quiero pedido","flow_id":"f"}`, msgIntent},
		{"llm keyword of one letter", `{"kind":"llm","keyword":"a","flow_id":"f"}`, msgIntent},
		{"llm keyword too long", `{"kind":"llm","keyword":"` + long + `","flow_id":"f"}`, msgIntent},
		{"message on keyword", `{"kind":"keyword","keyword":"x","flow_id":"f","message":"hola"}`, msgMessage},
		{"message on event_stop", `{"kind":"event_stop","keyword":"parar","message":"hola"}`, msgMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := trigger.NewMemoryStore()
			wantPlainError(t, create(st, nil, tc.body), http.StatusBadRequest, tc.want)
			if rules := rulesOf(t, st, tokenTenant); len(rules) != 0 {
				t.Errorf("un cuerpo rechazado dejó reglas guardadas: %+v", rules)
			}
		})
	}
}

func TestCreateTriggerHandler_RequiresIdentity(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]*httpapi.Identity{"no identity": nil, "identity without tenant": {Subject: "user-1"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			st := trigger.NewMemoryStore()
			rec := serve(admin.CreateTriggerHandler(st, nil), id, http.MethodPost, "/api/v1/triggers", `{`)
			wantPlainError(t, rec, http.StatusUnauthorized, msgAuthRequired)
		})
	}
}

// D-054.8, dirección (i): un keyword o un fallback hacia un flujo durable necesita una
// event_start HABILITADA del propio tenant, o el entrante se quedaría sin respuesta.
func TestCreateTriggerHandler_DurableFlowNeedsALiveEventStart(t *testing.T) {
	t.Parallel()
	const msgBlocked = "no se puede crear: el flujo de destino tiene contenido durable (p. ej. carrito o encuesta) " +
		"y el tenant no tiene ninguna regla event_start habilitada; sin una, un entrante que caiga en esta regla " +
		"(kind='fallback' o kind='keyword') se quedaría sin respuesta (D-054.8) — crea antes una regla event_start"
	llmWithEvent := newRule(trigger.KindLLM, durableFlow)
	llmWithEvent.EventKind = trigger.EventKindCart
	foreign := eventStart(true)
	foreign.TenantID = "otro-tenant"
	cases := []struct {
		name       string
		flowID     string
		existing   []trigger.Rule
		noChecker  bool
		wantStatus int
	}{
		{"durable flow and no rules", durableFlow, nil, false, http.StatusUnprocessableEntity},
		{"durable flow and a disabled event_start", durableFlow, []trigger.Rule{eventStart(false)}, false, http.StatusUnprocessableEntity},
		{"durable flow and only an llm rule with event_kind", durableFlow, []trigger.Rule{llmWithEvent}, false, http.StatusUnprocessableEntity},
		{"durable flow and another tenant's event_start", durableFlow, []trigger.Rule{foreign}, false, http.StatusUnprocessableEntity},
		{"durable flow and a live event_start", durableFlow, []trigger.Rule{eventStart(true)}, false, http.StatusCreated},
		{"plain flow", plainFlow, nil, false, http.StatusCreated},
		{"nil checker means nothing is durable", durableFlow, nil, true, http.StatusCreated},
	}
	for _, kind := range []string{"keyword", "fallback"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				st := trigger.NewMemoryStore()
				for _, r := range tc.existing {
					seed(t, st, r)
				}
				checker := &recordingChecker{}
				var port admin.DurableFlowChecker
				if !tc.noChecker {
					port = checker
				}

				rec := create(st, port, `{"kind":"`+kind+`","keyword":"hola","flow_id":" `+tc.flowID+` "}`)

				wantRules := 0
				for _, r := range tc.existing {
					if r.TenantID == tokenTenant {
						wantRules++
					}
				}
				if tc.wantStatus == http.StatusUnprocessableEntity {
					wantPlainError(t, rec, tc.wantStatus, msgBlocked)
				} else {
					wantRules++
					if rec.Code != tc.wantStatus {
						t.Fatalf("código = %d, quiero %d (cuerpo: %q)", rec.Code, tc.wantStatus, rec.Body.String())
					}
				}
				if got := len(rulesOf(t, st, tokenTenant)); got != wantRules {
					t.Errorf("reglas del tenant tras la petición = %d, quiero %d", got, wantRules)
				}
				if want := []string{tokenTenant + "/" + tc.flowID}; !tc.noChecker && !slices.Equal(checker.calls(), want) {
					t.Errorf("consultas al checker = %v, quiero %v (tenant del token, flow_id recortado)", checker.calls(), want)
				}
			})
		}
	}
}

// Los kinds que siempre traen evento (o que no arrancan nada) ni preguntan: con un
// checker que falla, entran igual.
func TestCreateTriggerHandler_OtherKindsNeverAskTheChecker(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"escape":      `{"kind":"escape","keyword":"salir"}`,
		"llm":         `{"kind":"llm","keyword":"hacer_pedido","flow_id":"carrito"}`,
		"event_start": `{"kind":"event_start","keyword":"carrito","event_kind":"cart","flow_id":"carrito"}`,
		"event_stop":  `{"kind":"event_stop","keyword":"parar"}`,
	}
	for kind, body := range bodies {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			checker := &recordingChecker{err: errInjected}
			rec := create(trigger.NewMemoryStore(), checker, body)
			if rec.Code != http.StatusCreated {
				t.Errorf("código = %d, quiero 201 (cuerpo: %q)", rec.Code, rec.Body.String())
			}
			if asked := checker.calls(); len(asked) != 0 {
				t.Errorf("consultas al checker = %v, quiero ninguna", asked)
			}
		})
	}
}

func TestCreateTriggerHandler_Failures(t *testing.T) {
	t.Parallel()
	const keywordTo = `{"kind":"keyword","keyword":"hola","flow_id":"`
	cases := []struct {
		name       string
		store      func() *faultyStore
		checkerErr error
		flowID     string
		wantStatus int
		wantMsg    string
	}{
		{"checker fails", newFaultyStore, errInjected, durableFlow, http.StatusInternalServerError, msgCheckerFailed},
		{"listing fails while checking a durable flow", func() *faultyStore { s := newFaultyStore(); s.listErr = errInjected; return s },
			nil, durableFlow, http.StatusInternalServerError, msgCheckerFailed},
		{"insert fails", func() *faultyStore { s := newFaultyStore(); s.insertErr = errInjected; return s },
			nil, plainFlow, http.StatusInternalServerError, "no se pudo crear la regla de disparo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := tc.store()
			rec := create(st, &recordingChecker{err: tc.checkerErr}, keywordTo+tc.flowID+`"}`)
			wantPlainError(t, rec, tc.wantStatus, tc.wantMsg)
		})
	}

	// Con un flujo NO durable las reglas ni se listan: un listado roto no estorba.
	t.Run("plain flow never lists", func(t *testing.T) {
		t.Parallel()
		st := newFaultyStore()
		st.listErr = errInjected
		rec := create(st, &recordingChecker{}, keywordTo+plainFlow+`"}`)
		if rec.Code != http.StatusCreated || st.lists != 0 {
			t.Errorf("código = %d con %d listados, quiero 201 y 0", rec.Code, st.lists)
		}
	})
}

// Ninguno de los tres handlers mira el método: lo acota el patrón de la ruta.
func TestTriggerHandlers_DoNotCheckTheMethod(t *testing.T) {
	t.Parallel()
	st := trigger.NewMemoryStore()
	target := seed(t, st, newRule(trigger.KindEscape, ""))

	rec := serve(admin.CreateTriggerHandler(st, nil), operator(), http.MethodPut, "/api/v1/triggers", `{"kind":"event_stop","keyword":"parar"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("alta con PUT: código = %d, quiero 201", rec.Code)
	}
	rec = serve(admin.ListTriggersHandler(st, nil), operator(), http.MethodPost, "/api/v1/triggers", "")
	if rec.Code != http.StatusOK {
		t.Errorf("listado con POST: código = %d, quiero 200", rec.Code)
	}
	rec = deleteRule(admin.DeleteTriggerHandler(st, nil), operator(), http.MethodGet, target.TriggerID)
	if rec.Code != http.StatusNoContent {
		t.Errorf("baja con GET: código = %d, quiero 204", rec.Code)
	}
}
