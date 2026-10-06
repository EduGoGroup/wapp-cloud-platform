package apipublica_test

// diagnostics_request_test.go — la mitad de diagnostics_test.go que cubre los desenlaces de D5
// (POST /api/v1/sessions/{id}/diagnostics) que NO son el camino feliz: consentimiento, guarda de
// tenant, registro, empuje con su rollback, y los plazos. Partido por tema (E-13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

func TestMountDiagnostics_Request_OptOutIs403(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	store.SetConsent(tenantA, false)
	lister := sessA()
	d := diagDeps(store, requester)
	d.Sessions = lister
	rec := requestDiag(h, d, requestTarget, "")
	wantCode(t, "D5 con opt-out", rec, http.StatusForbidden)
	wantErrorBody(t, "D5 con opt-out", rec, msgOptOut)
	wantNothingEmitted(t, "D5 con opt-out", store, requester)
	if lister.calls != 0 {
		t.Error("se consultó la flota de un tenant que desactivó el diagnóstico")
	}
	wantDiagAudit(t, h, "session", "failure", http.StatusForbidden)
}

// TestMountDiagnostics_Request_ConsentFailures: un consentimiento que no se pudo verificar NO
// abre la capacidad; el plazo vencido es un 504 que se puede reintentar.
func TestMountDiagnostics_Request_ConsentFailures(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	store.consentErr = errors.New("bd caída")
	rec := requestDiag(h, diagDeps(store, requester), requestTarget, "")
	wantCode(t, "D5 con el consentimiento caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "D5 con el consentimiento caído", rec, msgConsentFailed)
	wantNothingEmitted(t, "D5 con el consentimiento caído", store, requester)
	if n := len(logLines(h, msgGuardLog)); n != 0 {
		t.Errorf("un fallo que no es el plazo dejó %d líneas de plazo vencido", n)
	}

	h = apipublicahelpertest.New(t)
	store, requester = newStore(), &requesterSpy{}
	store.blockConsent = true
	d := diagDeps(store, requester)
	d.DBTimeout = 20 * time.Millisecond
	rec = requestDiag(h, d, requestTarget, "")
	wantCode(t, "D5 con el consentimiento vencido", rec, http.StatusGatewayTimeout)
	wantErrorBody(t, "D5 con el consentimiento vencido", rec, msgConsentExpired)
	wantNothingEmitted(t, "D5 con el consentimiento vencido", store, requester)
	wantDeadlineLine(t, h, "diagnostics.consent", "session_id", "sess-a")
	wantDiagAudit(t, h, "session", "failure", http.StatusGatewayTimeout)
}

// wantDeadlineLine exige UNA línea Warn de plazo vencido con esa op, el tenant y el campo dado.
func wantDeadlineLine(t *testing.T, h *apipublicahelpertest.Harness, op, key, value string) {
	t.Helper()
	lines := logLines(h, msgGuardLog)
	if len(lines) != 1 || lines[0].Level != "warn" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel warn", msgGuardLog, lines)
	}
	if f := lines[0].Fields; f["op"] != op || f["tenant_id"] != tenantA || f[key] != value {
		t.Errorf("campos = %v; quiero op %s, tenant_id %s y %s %s", f, op, tenantA, key, value)
	}
}

// TestMountDiagnostics_Request_CrossTenantIs404: la sesión se busca en la flota del tenant DEL
// TOKEN; una ajena (o inexistente) es un 404 que no revela nada, y no se emite.
func TestMountDiagnostics_Request_CrossTenantIs404(t *testing.T) {
	for name, target := range map[string]string{
		"session_of_another_tenant": "/api/v1/sessions/sess-b/diagnostics",
		"unknown_session":           "/api/v1/sessions/no-existe/diagnostics",
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store, requester := newStore(), &requesterSpy{}
			lister := sessA()
			d := diagDeps(store, requester)
			d.Sessions = lister
			rec := requestDiag(h, d, target, "")
			wantCode(t, name, rec, http.StatusNotFound)
			wantErrorBody(t, name, rec, "sesión no encontrada para el tenant")
			wantNothingEmitted(t, name, store, requester)
			if lister.calls != 1 || lister.tenant != tenantA {
				t.Errorf("List(%q) en %d llamadas; quiero el tenant del token en 1", lister.tenant, lister.calls)
			}
			wantDiagAudit(t, h, "session", "failure", http.StatusNotFound)
		})
	}
}

func TestMountDiagnostics_Request_GuardFailures(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	d := diagDeps(store, requester)
	d.Sessions = &listerFake{err: errors.New("bd caída")}
	rec := requestDiag(h, d, requestTarget, "")
	wantCode(t, "D5 con la flota caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "D5 con la flota caída", rec, "no se pudo verificar la sesión")
	wantNothingEmitted(t, "D5 con la flota caída", store, requester)

	h = apipublicahelpertest.New(t)
	d.Sessions, d.DBTimeout = &listerFake{block: true}, 20*time.Millisecond
	rec = requestDiag(h, d, requestTarget, "")
	wantCode(t, "D5 con la guarda vencida", rec, http.StatusGatewayTimeout)
	wantErrorBody(t, "D5 con la guarda vencida", rec, msgDiagGuardExpired)
	wantNothingEmitted(t, "D5 con la guarda vencida", store, requester)
	wantDeadlineLine(t, h, "diagnostics.guarda_tenant", "session_id", "sess-a")
}

func TestMountDiagnostics_Request_CreateErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	store.createErr = errors.New("bd caída")
	rec := requestDiag(h, diagDeps(store, requester), requestTarget, "")
	wantCode(t, "D5 sin poder registrar", rec, http.StatusInternalServerError)
	wantErrorBody(t, "D5 sin poder registrar", rec, msgCreateFailed)
	if requester.calls != 0 || len(store.deleted) != 0 {
		t.Errorf("%d emisiones y %d borrados sin solicitud registrada; quiero 0 y 0", requester.calls, len(store.deleted))
	}
}

// TestMountDiagnostics_Request_PushErrorRollsBack: si el empuje falla, la solicitud pendiente se
// BORRA (nunca recibiría bundle) y el error se traduce como el de un envío.
func TestMountDiagnostics_Request_PushErrorRollsBack(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"offline_is_502", fmt.Errorf("%w: %q", session.ErrSessionOffline, "sess-a"), http.StatusBadGateway, `{"error":"` + msgOffline + `"}`},
		{"push_timeout_is_504", fmt.Errorf("%w: %q", session.ErrPushTimeout, "sess-a"), http.StatusGatewayTimeout, `{"error":"` + msgEdgeNotReading + `"}`},
		{"anything_else_is_500", errors.New("otra cosa"), http.StatusInternalServerError, `{"error":"` + msgSendFailed + `"}`},
		{"command_id_of_the_error_travels", withID(context.Canceled), http.StatusGatewayTimeout, `{"error":"` + msgAckTimeout + `","command_id":"cmd-42"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store, requester := newStore(), &requesterSpy{err: tc.err}
			rec := requestDiag(h, diagDeps(store, requester), requestTarget, "")
			wantCode(t, tc.name, rec, tc.code)
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("cuerpo %s, quiero %s", got, tc.body)
			}
			if requester.calls != 1 || requester.commandID == "" {
				t.Fatalf("el empuje debió intentarse una vez con su command_id: %+v", requester)
			}
			if want := tenantA + "/" + requester.commandID; len(store.deleted) != 1 || store.deleted[0] != want {
				t.Errorf("DeleteRequest = %v, quiero solo %s (rollback del tenant del token)", store.deleted, want)
			}
			if _, err := store.Memoria.GetBundle(context.Background(), tenantA, requester.commandID); !errors.Is(err, diagnostics.ErrNotFound) {
				t.Errorf("tras el rollback la solicitud está en %v, quiero ErrNotFound", err)
			}
			if lines := logLines(h, msgSendErrorLog); len(lines) != 1 || lines[0].Fields["status"] != tc.code || lines[0].Fields["session_id"] != "sess-a" {
				t.Errorf("línea %q = %+v; quiero una con status %d y session_id sess-a", msgSendErrorLog, lines, tc.code)
			}
			if n := len(logLines(h, msgDiagRequested)) + len(logLines(h, msgRollbackFailed)); n != 0 {
				t.Errorf("%d líneas de solicitud emitida o de rollback fallido, quiero 0", n)
			}
			wantDiagAudit(t, h, "session", "failure", tc.code)
		})
	}
}

// TestMountDiagnostics_Request_RollbackFailureIsLogged: el rollback es best-effort; si falla se
// avisa y la respuesta sigue siendo la del empuje.
func TestMountDiagnostics_Request_RollbackFailureIsLogged(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{err: session.ErrSessionOffline}
	store.deleteErr = errors.New("bd caída")
	rec := requestDiag(h, diagDeps(store, requester), requestTarget, "")
	wantCode(t, "D5 con el rollback roto", rec, http.StatusBadGateway)
	wantErrorBody(t, "D5 con el rollback roto", rec, msgOffline)
	lines := logLines(h, msgRollbackFailed)
	if len(lines) != 1 || lines[0].Level != "warn" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel warn", msgRollbackFailed, lines)
	}
	f := lines[0].Fields
	if err, ok := f["error"].(error); !ok || !errors.Is(err, store.deleteErr) || f["tenant_id"] != tenantA || f["command_id"] != requester.commandID {
		t.Errorf("campos = %v; quiero tenant_id, command_id y el error del borrado", f)
	}
}

// TestMountDiagnostics_Request_Clocks: DBTimeout acota las DOS lecturas del preflight (<= 0 ⇒
// 1,5 s); registrar y emitir van con el contexto de la petición, sin plazo propio. El rollback
// tiene su reloj aparte: ver TestMountDiagnostics_Request_RollbackSurvivesTheClientLeaving.
func TestMountDiagnostics_Request_Clocks(t *testing.T) {
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms": {0, time.Second, 1500 * time.Millisecond},
		"wired_timeout":        {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store, requester := newStore(), &requesterSpy{}
			lister := sessA()
			d := diagDeps(store, requester)
			d.Sessions, d.DBTimeout = lister, tc.wired
			wantCode(t, name, requestDiag(h, d, requestTarget, ""), http.StatusAccepted)
			for op, got := range map[string]time.Duration{"consent": store.remaining["consent"], "guard": lister.remaining} {
				if got <= tc.floor || got > tc.ceil {
					t.Errorf("%s: al contexto le quedaban %s, quiero entre %s y %s", op, got, tc.floor, tc.ceil)
				}
			}
			if store.remaining["create"] != -1 || requester.bounded {
				t.Errorf("registrar (%s) o emitir (plazo=%v) llevan plazo propio; quiero el contexto de la petición",
					store.remaining["create"], requester.bounded)
			}
		})
	}
}

func TestMountDiagnostics_Request_NilLogServesTheSame(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	token := h.With(tenantA, diagPerm)
	store := newStore()
	store.deleteErr = errors.New("bd caída")
	failing := diagCara(k, diagDeps(store, &requesterSpy{err: session.ErrSessionOffline}))
	wantCode(t, "D5 sin logger y con el rollback roto", h.Call(failing, token, http.MethodPost, requestTarget, ""), http.StatusBadGateway)
	ok := diagCara(k, diagDeps(newStore(), &requesterSpy{}))
	wantCode(t, "D5 sin logger", h.Call(ok, token, http.MethodPost, requestTarget, ""), http.StatusAccepted)
}
