package apipublica_test

// diagnostics_test.go — cubre el contrato de diagnostics.go (DiagnosticsRequester,
// DiagnosticsStore, DiagnosticsDeps, MountDiagnostics): el montaje «las dos o ninguna», la cadena
// de D5 y D6, y el camino feliz de la petición D5. Sus demás desenlaces van en
// diagnostics_request_test.go y la descarga D6 en diagnostics_download_test.go (E-13). Aquí viven
// los dobles que comparten los tres ficheros.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics/diagnosticshelpertest"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

const (
	diagPerm      = "diagnostics.request"
	requestTarget = "/api/v1/sessions/sess-a/diagnostics"
	bundleTarget  = "/api/v1/diagnostics/"

	patternDiagRequest = "POST /api/v1/sessions/{id}/diagnostics"
	patternDiagBundle  = "GET /api/v1/diagnostics/{command_id}"

	msgConsentExpired   = "la verificación del consentimiento no respondió a tiempo, reintenta"
	msgConsentFailed    = "no se pudo verificar el consentimiento"
	msgOptOut           = "el tenant desactivó el diagnóstico remoto (opt-out)"
	msgDiagGuardExpired = "la verificación de la sesión no respondió a tiempo, reintenta"
	msgCreateFailed     = "no se pudo registrar la solicitud"
	msgRollbackFailed   = "diagnóstico: rollback de solicitud tras push fallido falló"
	msgDiagRequested    = "diagnóstico remoto solicitado"
)

// Los puertos de D5 y D6 los cumplen las piezas REALES del módulo edge nuevo.
var (
	_ apipublica.DiagnosticsRequester = (*edgegrpc.Server)(nil)
	_ apipublica.DiagnosticsStore     = (*diagnostics.Postgres)(nil)
	_ apipublica.DiagnosticsStore     = diagnostics.Store(nil)
	_ apipublica.DiagnosticsStore     = (*diagnosticshelpertest.Memoria)(nil)
)

// requesterSpy es DiagnosticsRequester: apunta lo que recibió y devuelve err.
type requesterSpy struct {
	err                       error
	calls                     int
	session, commandID, scope string
	bounded                   bool
	// onCall, si no es nil, corre dentro de RequestDiagnostics, antes de devolver: sirve para
	// simular lo que pasa MIENTRAS se empuja (p. ej. que el cliente se vaya).
	onCall func()
}

var _ apipublica.DiagnosticsRequester = (*requesterSpy)(nil)

func (r *requesterSpy) RequestDiagnostics(ctx context.Context, sessionID, commandID, scope string) error {
	r.calls++
	r.session, r.commandID, r.scope = sessionID, commandID, scope
	_, r.bounded = ctx.Deadline()
	if r.onCall != nil {
		r.onCall()
	}
	return r.err
}

// creation es lo que recibió CreateRequest.
type creation struct {
	tenant, session, commandID, requestedBy string
	expiresAt                               time.Time
}

// storeSpy es DiagnosticsStore sobre el doble en memoria del módulo: delega en él salvo donde el
// test le pide fallar, no contestar (block: espera a que el contexto muera, sin time.Sleep) o
// devolver un bundle fijo, y apunta lo que recibió y el plazo de cada contexto (-1 = sin plazo).
type storeSpy struct {
	*diagnosticshelpertest.Memoria
	consentErr, createErr, deleteErr, getErr error
	blockConsent, blockGet                   bool
	ready                                    *diagnostics.Record
	created                                  []creation
	deleted                                  []string
	gets                                     []string
	remaining                                map[string]time.Duration
	// deleteCtxErr es el ctx.Err() del contexto con el que llegó DeleteRequest.
	deleteCtxErr error
}

var _ apipublica.DiagnosticsStore = (*storeSpy)(nil)

func newStore() *storeSpy {
	return &storeSpy{Memoria: diagnosticshelpertest.NewMemoria(), remaining: map[string]time.Duration{}}
}

func (s *storeSpy) note(ctx context.Context, op string) {
	s.remaining[op] = -1
	if dl, ok := ctx.Deadline(); ok {
		s.remaining[op] = time.Until(dl)
	}
}

func (s *storeSpy) ConsentEnabled(ctx context.Context, tenantID string) (bool, error) {
	s.note(ctx, "consent")
	if s.blockConsent {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if s.consentErr != nil {
		return false, s.consentErr
	}
	return s.Memoria.ConsentEnabled(ctx, tenantID)
}

func (s *storeSpy) CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error {
	s.note(ctx, "create")
	s.created = append(s.created, creation{tenantID, sessionID, commandID, requestedBy, expiresAt})
	if s.createErr != nil {
		return s.createErr
	}
	return s.Memoria.CreateRequest(ctx, tenantID, sessionID, commandID, requestedBy, expiresAt)
}

func (s *storeSpy) DeleteRequest(ctx context.Context, tenantID, commandID string) error {
	s.note(ctx, "delete")
	s.deleteCtxErr = ctx.Err()
	s.deleted = append(s.deleted, tenantID+"/"+commandID)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Memoria.DeleteRequest(ctx, tenantID, commandID)
}

func (s *storeSpy) GetBundle(ctx context.Context, tenantID, commandID string) (diagnostics.Record, error) {
	s.note(ctx, "get")
	s.gets = append(s.gets, tenantID+"/"+commandID)
	switch {
	case s.blockGet:
		<-ctx.Done()
		return diagnostics.Record{}, ctx.Err()
	case s.getErr != nil:
		return diagnostics.Record{}, s.getErr
	case s.ready != nil:
		return *s.ready, nil
	}
	return s.Memoria.GetBundle(ctx, tenantID, commandID)
}

// diagDeps son las tres dependencias de D5/D6 con la flota de siempre (sess-a es de tenantA).
func diagDeps(store apipublica.DiagnosticsStore, requester apipublica.DiagnosticsRequester) apipublica.DiagnosticsDeps {
	return apipublica.DiagnosticsDeps{Diagnostics: store, DiagnosticsRequester: requester, Sessions: sessA()}
}

// diagCara monta D5 y D6 con k y d.
func diagCara(k apipublica.Common, d apipublica.DiagnosticsDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountDiagnostics(c, k, d)
	return c
}

// requestDiag pide D5 sobre target como tenantA, con el permiso y el cuerpo dado.
func requestDiag(h *apipublicahelpertest.Harness, d apipublica.DiagnosticsDeps, target, body string) *httptest.ResponseRecorder {
	return h.Call(diagCara(h.Common(), d), h.With(tenantA, diagPerm), http.MethodPost, target, body)
}

// wantDiagAudit exige EXACTAMENTE un registro de auditoría de D5/D6 con ese recurso y desenlace.
func wantDiagAudit(t *testing.T, h *apipublicahelpertest.Harness, resource, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("quedaron %d registros de auditoría, quiero exactamente 1", len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Actor != subject || r.Action != diagPerm || r.Resource != resource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("registro %+v, quiero tenant %s, actor %s, action %s, resource %s, result %s, status %d",
			r, tenantA, subject, diagPerm, resource, result, status)
	}
}

// wantNothingEmitted exige que la petición cortara ANTES de registrar o emitir nada.
func wantNothingEmitted(t *testing.T, what string, store *storeSpy, requester *requesterSpy) {
	t.Helper()
	if len(store.created) != 0 || requester.calls != 0 || len(store.deleted) != 0 {
		t.Errorf("%s: %d solicitudes registradas, %d emitidas y %d borradas; quiero 0, 0 y 0",
			what, len(store.created), requester.calls, len(store.deleted))
	}
}

func TestMountDiagnostics_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	store.ready = &diagnostics.Record{CommandID: "cmd-listo", SessionID: "sess-a"}
	cara := diagCara(h.Common(), diagDeps(store, requester))
	wantPatterns(t, "D5 y D6", cara, []string{patternDiagRequest, patternDiagBundle})
	checkChain(t, h, cara, routeCase{
		id: "D5", method: http.MethodPost, target: requestTarget,
		perm: diagPerm, resource: "session", want: http.StatusAccepted,
	})
	// D6 es una LECTURA con cadena W: deja su registro, con recurso "diagnostics".
	checkChain(t, h, cara, routeCase{
		id: "D6", method: http.MethodGet, target: bundleTarget + "cmd-listo",
		perm: diagPerm, resource: "diagnostics", want: http.StatusOK,
	})
	if requester.calls != 1 || len(store.gets) != 1 {
		t.Errorf("%d emisiones y %d lecturas; quiero 1 y 1: ni el 401 ni los 403 llegan al handler", requester.calls, len(store.gets))
	}
}

// TestMountDiagnostics_BothOrNeither: con las tres dependencias existen las dos rutas; si falta
// CUALQUIERA, ninguna (404 de ruta inexistente, no un 401 ni un 500).
func TestMountDiagnostics_BothOrNeither(t *testing.T) {
	all := diagDeps(newStore(), &requesterSpy{})
	cases := []struct {
		name string
		deps apipublica.DiagnosticsDeps
	}{
		{"no_dependencies", apipublica.DiagnosticsDeps{BundleTTL: time.Hour, DBTimeout: time.Second}},
		{"without_store", apipublica.DiagnosticsDeps{DiagnosticsRequester: all.DiagnosticsRequester, Sessions: all.Sessions}},
		{"without_requester", apipublica.DiagnosticsDeps{Diagnostics: all.Diagnostics, Sessions: all.Sessions}},
		{"without_sessions", apipublica.DiagnosticsDeps{Diagnostics: all.Diagnostics, DiagnosticsRequester: all.DiagnosticsRequester}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := diagCara(h.Common(), tc.deps)
			wantPatterns(t, tc.name, cara, nil)
			for _, r := range []struct{ method, target string }{
				{http.MethodPost, requestTarget}, {http.MethodGet, bundleTarget + "cmd-1"},
			} {
				rec := h.Call(cara, h.With(tenantA, diagPerm), r.method, r.target, "")
				wantCode(t, r.method+" "+r.target, rec, http.StatusNotFound)
				if rec.Body.String() != "404 page not found\n" {
					t.Errorf("%s %s: cuerpo %q, quiero el 404 de ruta inexistente", r.method, r.target, rec.Body.String())
				}
			}
		})
	}
	h := apipublicahelpertest.New(t)
	wantPatterns(t, "con las tres", diagCara(h.Common(), all), []string{patternDiagRequest, patternDiagBundle})
}

func TestMountDiagnostics_NilMWPanicsAtMount(t *testing.T) {
	for name, d := range map[string]apipublica.DiagnosticsDeps{
		"with_dependencies":    diagDeps(newStore(), &requesterSpy{}),
		"without_dependencies": {},
	} {
		v := recuperar(func() { apipublica.MountDiagnostics(apipublica.Nueva(), apipublica.Common{}, d) })
		if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountDiagnostics") {
			t.Errorf("%s: MountDiagnostics con MW nil: panic = %v; quiero un panic de cableado que nombre MountDiagnostics", name, v)
		}
	}
}

func TestMountDiagnostics_Request_OK(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	d := diagDeps(store, requester)
	rec := requestDiag(h, d, requestTarget, `{"scope":"logs","tenant_id":"`+tenantB+`"}`)

	wantCode(t, "D5", rec, http.StatusAccepted)
	if len(store.created) != 1 {
		t.Fatalf("CreateRequest se llamó %d veces, quiero 1", len(store.created))
	}
	c := store.created[0]
	if c.tenant != tenantA || c.session != "sess-a" || c.commandID == "" || c.requestedBy != subject {
		t.Errorf("CreateRequest(%+v); quiero el tenant del token, sess-a, un command_id y el subject del token", c)
	}
	want := `{"command_id":"` + c.commandID + `","session_id":"sess-a","status":"pending","expires_at":"` +
		c.expiresAt.UTC().Format(time.RFC3339) + `"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("D5: cuerpo %s, quiero %s", got, want)
	}
	if requester.calls != 1 || requester.session != "sess-a" || requester.commandID != c.commandID || requester.scope != "logs" {
		t.Errorf("RequestDiagnostics(%q, %q, %q) en %d llamadas; quiero (sess-a, %q, logs) en 1",
			requester.session, requester.commandID, requester.scope, requester.calls, c.commandID)
	}
	// La solicitud queda PENDIENTE y sin rollback: el bundle aún tiene dónde correlacionarse.
	if _, err := store.Memoria.GetBundle(context.Background(), tenantA, c.commandID); !errors.Is(err, diagnostics.ErrPending) {
		t.Errorf("tras el 202 la solicitud está en %v, quiero ErrPending", err)
	}
	if len(store.deleted) != 0 {
		t.Errorf("el camino feliz borró %v: el rollback es solo del empuje fallido", store.deleted)
	}
	wantDiagAudit(t, h, "session", "success", http.StatusAccepted)
}

// TestMountDiagnostics_Request_LeavesAnOperationalTrace: quién, qué sesión y qué command_id.
func TestMountDiagnostics_Request_LeavesAnOperationalTrace(t *testing.T) {
	h := apipublicahelpertest.New(t)
	requester := &requesterSpy{}
	wantCode(t, "D5", requestDiag(h, diagDeps(newStore(), requester), requestTarget, `{"scope":"logs"}`), http.StatusAccepted)
	lines := logLines(h, msgDiagRequested)
	if len(lines) != 1 || lines[0].Level != "info" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel info", msgDiagRequested, lines)
	}
	if f := lines[0].Fields; f["tenant_id"] != tenantA || f["subject"] != subject || f["session_id"] != "sess-a" ||
		f["command_id"] != requester.commandID || f["scope"] != "logs" {
		t.Errorf("campos = %v; quiero tenant_id, subject, session_id, command_id y scope", f)
	}
}

// TestMountDiagnostics_Request_Scope: el cuerpo es opcional y el scope cae a "full".
func TestMountDiagnostics_Request_Scope(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"empty_body_is_full":   {"", "full"},
		"empty_object_is_full": {"{}", "full"},
		"blank_scope_is_full":  {`{"scope":"   "}`, "full"},
		"scope_is_trimmed":     {`{"scope":"  logs "}`, "logs"},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			requester := &requesterSpy{}
			wantCode(t, name, requestDiag(h, diagDeps(newStore(), requester), requestTarget, tc.body), http.StatusAccepted)
			if requester.scope != tc.want {
				t.Errorf("scope emitido %q, quiero %q", requester.scope, tc.want)
			}
		})
	}
}

// TestMountDiagnostics_Request_BadJSONIs400: y va DESPUÉS del consentimiento y de la guarda.
func TestMountDiagnostics_Request_BadJSONIs400(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, requester := newStore(), &requesterSpy{}
	rec := requestDiag(h, diagDeps(store, requester), requestTarget, `{no es json`)
	wantCode(t, "D5 con cuerpo roto", rec, http.StatusBadRequest)
	wantErrorBody(t, "D5 con cuerpo roto", rec, "cuerpo JSON inválido")
	wantNothingEmitted(t, "D5 con cuerpo roto", store, requester)
	wantDiagAudit(t, h, "session", "failure", http.StatusBadRequest)

	h = apipublicahelpertest.New(t)
	rec = requestDiag(h, diagDeps(newStore(), requester), "/api/v1/sessions/sess-b/diagnostics", `{no es json`)
	wantCode(t, "D5 con cuerpo roto sobre una sesión ajena", rec, http.StatusNotFound)
}

// TestMountDiagnostics_Request_TTL: la retención es BundleTTL, y 30 min si llega a <= 0.
func TestMountDiagnostics_Request_TTL(t *testing.T) {
	for name, tc := range map[string]struct{ ttl, want time.Duration }{
		"zero_is_30_min":     {0, 30 * time.Minute},
		"negative_is_30_min": {-time.Second, 30 * time.Minute},
		"wired_ttl_is_used":  {10 * time.Minute, 10 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newStore()
			d := diagDeps(store, &requesterSpy{})
			d.BundleTTL = tc.ttl
			before := time.Now()
			wantCode(t, name, requestDiag(h, d, requestTarget, ""), http.StatusAccepted)
			after := time.Now()
			got := store.created[0].expiresAt
			if got.Before(before.Add(tc.want)) || got.After(after.Add(tc.want)) {
				t.Errorf("expira en %s, quiero ahora + %s (entre %s y %s)", got, tc.want, before.Add(tc.want), after.Add(tc.want))
			}
		})
	}
}
