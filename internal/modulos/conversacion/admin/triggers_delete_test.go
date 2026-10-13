package admin_test

// triggers_delete_test.go — la baja de reglas (DELETE .../triggers/{id}) y la puerta
// de atrás de D-054.8. Trozo de triggers_test.go, partido por tema (E-13).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// deleteRule ejecuta el handler con {id} ya resuelto, como lo dejaría el mux.
func deleteRule(h http.Handler, id *httpapi.Identity, method, triggerID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/triggers/x", strings.NewReader(""))
	req.SetPathValue("id", triggerID)
	if id != nil {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func hasRule(t *testing.T, st admin.TriggerStore, tenantID, triggerID string) bool {
	t.Helper()
	for _, r := range rulesOf(t, st, tenantID) {
		if r.TriggerID == triggerID {
			return true
		}
	}
	return false
}

func TestDeleteTriggerHandler_Deletes(t *testing.T) {
	t.Parallel()
	st := trigger.NewMemoryStore()
	target := seed(t, st, newRule(trigger.KindKeyword, plainFlow))
	other := seed(t, st, newRule(trigger.KindEscape, ""))

	rec := deleteRule(admin.DeleteTriggerHandler(st, nil), operator(), http.MethodDelete, target.TriggerID)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("código = %d, cuerpo = %q; quiero 204 sin cuerpo", rec.Code, rec.Body.String())
	}
	if hasRule(t, st, tokenTenant, target.TriggerID) {
		t.Error("la regla borrada sigue en el store")
	}
	if !hasRule(t, st, tokenTenant, other.TriggerID) {
		t.Error("se borró una regla que no era la pedida")
	}
}

func TestDeleteTriggerHandler_Rejections(t *testing.T) {
	t.Parallel()
	st := trigger.NewMemoryStore()
	foreign := newRule(trigger.KindKeyword, plainFlow)
	foreign.TenantID = "otro-tenant"
	foreign = seed(t, st, foreign)
	cases := []struct {
		name       string
		id         *httpapi.Identity
		triggerID  string
		wantStatus int
		wantMsg    string
	}{
		{"no identity", nil, foreign.TriggerID, http.StatusUnauthorized, msgAuthRequired},
		{"identity without tenant", &httpapi.Identity{Subject: "user-1"}, foreign.TriggerID, http.StatusUnauthorized, msgAuthRequired},
		{"authentication is checked before the id", nil, "", http.StatusUnauthorized, msgAuthRequired},
		{"empty id", operator(), "", http.StatusBadRequest, "trigger id requerido en la ruta"},
		{"unknown id", operator(), "no-existe", http.StatusNotFound, "regla de disparo no encontrada"},
		{"rule of another tenant looks unknown", operator(), foreign.TriggerID, http.StatusNotFound, "regla de disparo no encontrada"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := deleteRule(admin.DeleteTriggerHandler(st, &recordingChecker{}), tc.id, http.MethodDelete, tc.triggerID)
			wantPlainError(t, rec, tc.wantStatus, tc.wantMsg)
			if !hasRule(t, st, "otro-tenant", foreign.TriggerID) {
				t.Error("se borró la regla de otro tenant")
			}
		})
	}
}

// D-054.8, dirección (ii): no se puede retirar la última event_start viva mientras un
// keyword o un fallback HABILITADO dependa de ella para no dejar mudo al entrante.
func TestDeleteTriggerHandler_LastLiveEventStart(t *testing.T) {
	t.Parallel()
	const msgBlocked = "no se puede borrar: es la última regla event_start habilitada del tenant, que tiene una " +
		"regla kind='fallback' o kind='keyword' hacia un flujo con contenido durable; borrarla dejaría sin respuesta a " +
		"los entrantes que caigan en esa regla (D-054.8) — deshabilita o borra primero esa regla, o conserva/crea otra " +
		"regla event_start"
	disabled := func(r trigger.Rule) trigger.Rule { r.Enabled = false; return r }
	durableFallback := newRule(trigger.KindFallback, durableFlow)
	durableKeyword := newRule(trigger.KindKeyword, durableFlow)
	cases := []struct {
		name       string
		target     trigger.Rule
		others     []trigger.Rule
		noChecker  bool
		wantStatus int
		wantAsked  bool
	}{
		{"last event_start with a durable fallback", eventStart(true), []trigger.Rule{durableFallback},
			false, http.StatusUnprocessableEntity, true},
		{"last event_start with a durable keyword", eventStart(true), []trigger.Rule{durableKeyword},
			false, http.StatusUnprocessableEntity, true},
		{"the other event_start is disabled", eventStart(true), []trigger.Rule{durableFallback, eventStart(false)},
			false, http.StatusUnprocessableEntity, true},
		{"another event_start is still live", eventStart(true), []trigger.Rule{durableFallback, eventStart(true)},
			false, http.StatusNoContent, false},
		{"target event_start is already disabled", eventStart(false), []trigger.Rule{durableFallback},
			false, http.StatusNoContent, false},
		{"target is not an event_start", durableFallback, []trigger.Rule{eventStart(true)},
			false, http.StatusNoContent, false},
		{"the durable fallback is disabled", eventStart(true), []trigger.Rule{disabled(durableFallback)},
			false, http.StatusNoContent, false},
		{"the fallback points to a plain flow", eventStart(true), []trigger.Rule{newRule(trigger.KindFallback, plainFlow)},
			false, http.StatusNoContent, true},
		{"only an llm rule points to the durable flow", eventStart(true), []trigger.Rule{newRule(trigger.KindLLM, durableFlow)},
			false, http.StatusNoContent, false},
		{"nil checker never blocks", eventStart(true), []trigger.Rule{durableFallback},
			true, http.StatusNoContent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := trigger.NewMemoryStore()
			target := seed(t, st, tc.target)
			for _, r := range tc.others {
				seed(t, st, r)
			}
			checker := &recordingChecker{}
			var port admin.DurableFlowChecker
			if !tc.noChecker {
				port = checker
			}

			rec := deleteRule(admin.DeleteTriggerHandler(st, port), operator(), http.MethodDelete, target.TriggerID)

			blocked := tc.wantStatus == http.StatusUnprocessableEntity
			if blocked {
				wantPlainError(t, rec, tc.wantStatus, msgBlocked)
			} else if rec.Code != tc.wantStatus {
				t.Fatalf("código = %d, quiero %d (cuerpo: %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if kept := hasRule(t, st, tokenTenant, target.TriggerID); kept != blocked {
				t.Errorf("la regla sigue en el store = %t, quiero %t", kept, blocked)
			}
			asked := checker.calls()
			if (len(asked) > 0) != tc.wantAsked {
				t.Errorf("consultas al checker = %v, quiero que pregunte = %t", asked, tc.wantAsked)
			}
			for _, call := range asked {
				if !strings.HasPrefix(call, tokenTenant+"/") {
					t.Errorf("el checker se consultó con %q, quiero el tenant del token", call)
				}
			}
		})
	}
}

func TestDeleteTriggerHandler_Failures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		breakStore func(*faultyStore)
		checkerErr error
		unknownID  bool
		wantStatus int
		wantMsg    string
	}{
		{"listing fails", func(s *faultyStore) { s.listErr = errInjected }, nil, false,
			http.StatusInternalServerError, msgListFailed},
		{"listing fails even for an unknown id", func(s *faultyStore) { s.listErr = errInjected }, nil, true,
			http.StatusInternalServerError, msgListFailed},
		{"checker fails", func(*faultyStore) {}, errInjected, false,
			http.StatusInternalServerError, msgCheckerFailed},
		{"delete fails", func(s *faultyStore) { s.deleteErr = errInjected }, nil, false,
			http.StatusInternalServerError, "no se pudo borrar la regla de disparo"},
		{"delete reports a wrapped not-found", func(s *faultyStore) { s.deleteErr = fmt.Errorf("borrar: %w", trigger.ErrTriggerNotFound) },
			nil, false, http.StatusNotFound, "regla de disparo no encontrada"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newFaultyStore()
			// La última event_start viva junto a un fallback durable: el único camino
			// que llega a preguntar al checker.
			target := seed(t, st, eventStart(true))
			seed(t, st, newRule(trigger.KindFallback, durableFlow))
			tc.breakStore(st)
			id := target.TriggerID
			if tc.unknownID {
				id = "no-existe"
			}
			// Sin fallo inyectado va SIN checker: así la baja no se bloquea con el 422 y
			// el caso llega hasta el Delete del store.
			var port admin.DurableFlowChecker
			if tc.checkerErr != nil {
				port = &recordingChecker{err: tc.checkerErr}
			}

			rec := deleteRule(admin.DeleteTriggerHandler(st, port), operator(), http.MethodDelete, id)

			wantPlainError(t, rec, tc.wantStatus, tc.wantMsg)
		})
	}
}
