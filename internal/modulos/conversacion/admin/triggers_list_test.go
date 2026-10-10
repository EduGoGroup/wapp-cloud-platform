//go:build pendiente

package admin_test

// triggers_list_test.go — el listado de reglas (GET .../triggers) y sus dos marcas
// derivadas. Trozo de triggers_test.go, partido por tema (E-13).

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// fixedStore devuelve SIEMPRE las mismas reglas, en ese orden y con el tenant que
// traigan: es lo que deja ver qué toma el handler de la fila y qué del token.
type fixedStore struct {
	admin.TriggerStore
	rules     []trigger.Rule
	gotTenant string
}

func (s *fixedStore) List(_ context.Context, tenantID string) ([]trigger.Rule, error) {
	s.gotTenant = tenantID
	return s.rules, nil
}

func list(st admin.TriggerStore, checker admin.DurableFlowChecker) []byte {
	return serve(admin.ListTriggersHandler(st, checker), operator(), http.MethodGet, "/api/v1/triggers", "").Body.Bytes()
}

// marksByKeyword indexa el listado por keyword y deja, de cada regla, solo las marcas
// derivadas PRESENTES en el JSON.
func marksByKeyword(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("el listado no es un arreglo JSON (%v): %s", err, raw)
	}
	got := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		marks := map[string]any{}
		for _, key := range []string{"shadowed_by_event_list", "flow_needs_event"} {
			if v, ok := row[key]; ok {
				marks[key] = v
			}
		}
		keyword, ok := row["keyword"].(string)
		if !ok {
			t.Fatalf("una regla del listado llegó sin keyword: %v", row)
		}
		got[keyword] = marks
	}
	return got
}

func TestListTriggersHandler_EmptyIsAnEmptyArray(t *testing.T) {
	t.Parallel()
	rec := serve(admin.ListTriggersHandler(trigger.NewMemoryStore(), nil), operator(), http.MethodGet, "/api/v1/triggers", "")
	wantJSON(t, rec, http.StatusOK, `[]`)
}

func TestListTriggersHandler_DerivedMarksAndTenantScope(t *testing.T) {
	t.Parallel()
	named := func(keyword string, kind trigger.Kind, flowID string) trigger.Rule {
		r := newRule(kind, flowID)
		r.Keyword = keyword
		return r
	}
	foreign := named("ajena", trigger.KindFallback, durableFlow)
	foreign.TenantID = "otro-tenant"
	rules := []trigger.Rule{
		named("kw_plain", trigger.KindKeyword, plainFlow),
		named("kw_durable", trigger.KindKeyword, durableFlow),
		named("fb_plain", trigger.KindFallback, plainFlow),
		named("fb_durable", trigger.KindFallback, durableFlow),
		named("llm_durable", trigger.KindLLM, durableFlow),
		named("start_durable", trigger.KindEventStart, durableFlow),
		named("escape", trigger.KindEscape, ""),
		foreign,
	}
	shadowed := map[string]any{"shadowed_by_event_list": true}
	cases := []struct {
		name      string
		noChecker bool
		want      map[string]map[string]any
		wantAsked []string
	}{
		{"with checker", false, map[string]map[string]any{
			"kw_plain": {}, "kw_durable": {"flow_needs_event": true},
			"fb_plain": shadowed, "fb_durable": {"shadowed_by_event_list": true, "flow_needs_event": true},
			"llm_durable": {}, "start_durable": {}, "escape": {},
		}, []string{
			tokenTenant + "/" + durableFlow, tokenTenant + "/" + durableFlow,
			tokenTenant + "/" + plainFlow, tokenTenant + "/" + plainFlow,
		}},
		{"nil checker never marks flow_needs_event", true, map[string]map[string]any{
			"kw_plain": {}, "kw_durable": {}, "fb_plain": shadowed, "fb_durable": shadowed,
			"llm_durable": {}, "start_durable": {}, "escape": {},
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := trigger.NewMemoryStore()
			for _, r := range rules {
				seed(t, st, r)
			}
			checker := &recordingChecker{}
			var port admin.DurableFlowChecker
			if !tc.noChecker {
				port = checker
			}

			got := marksByKeyword(t, list(st, port))

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("marcas por regla = %v, quiero %v", got, tc.want)
			}
			if asked := checker.calls(); !slices.Equal(asked, tc.wantAsked) {
				t.Errorf("consultas al checker = %v, quiero %v (solo keyword y fallback)", asked, tc.wantAsked)
			}
		})
	}
}

// El orden es el del store, y al checker se le pregunta con el tenant DE LA FILA.
func TestListTriggersHandler_KeepsStoreOrderAndAsksWithTheRuleTenant(t *testing.T) {
	t.Parallel()
	row := func(id string) trigger.Rule {
		r := newRule(trigger.KindKeyword, durableFlow)
		r.TriggerID, r.TenantID = id, "tenant-de-la-fila"
		return r
	}
	st := &fixedStore{rules: []trigger.Rule{row("c"), row("a"), row("b")}}
	checker := &recordingChecker{}

	rec := serve(admin.ListTriggersHandler(st, checker), operator(), http.MethodGet, "/api/v1/triggers", "")

	var rows []struct {
		TriggerID string `json:"trigger_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("el listado no es un arreglo JSON (%v): %s", err, rec.Body.String())
	}
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		order = append(order, r.TriggerID)
	}
	if !slices.Equal(order, []string{"c", "a", "b"}) {
		t.Errorf("orden = %v, quiero el del store (c, a, b)", order)
	}
	if st.gotTenant != tokenTenant {
		t.Errorf("se listó el tenant %q, quiero el del token", st.gotTenant)
	}
	want := slices.Repeat([]string{"tenant-de-la-fila/" + durableFlow}, 3)
	if asked := checker.calls(); !slices.Equal(asked, want) {
		t.Errorf("consultas al checker = %v, quiero %v", asked, want)
	}
}

func TestListTriggersHandler_Failures(t *testing.T) {
	t.Parallel()
	broken := newFaultyStore()
	broken.listErr = errInjected
	withKeyword := trigger.NewMemoryStore()
	seed(t, withKeyword, newRule(trigger.KindKeyword, durableFlow))
	cases := []struct {
		name       string
		id         *httpapi.Identity
		store      admin.TriggerStore
		wantStatus int
		wantMsg    string
	}{
		{"no identity", nil, withKeyword, http.StatusUnauthorized, msgAuthRequired},
		{"identity without tenant", &httpapi.Identity{Subject: "user-1"}, withKeyword, http.StatusUnauthorized, msgAuthRequired},
		{"listing fails", operator(), broken, http.StatusInternalServerError, msgListFailed},
		{"checker fails", operator(), withKeyword, http.StatusInternalServerError, "no se pudo resolver el contenido durable de una regla"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := admin.ListTriggersHandler(tc.store, &recordingChecker{err: errInjected})
			wantPlainError(t, serve(h, tc.id, http.MethodGet, "/api/v1/triggers", ""), tc.wantStatus, tc.wantMsg)
		})
	}
}
