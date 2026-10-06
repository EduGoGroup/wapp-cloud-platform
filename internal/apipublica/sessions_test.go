//go:build pendiente

package apipublica_test

// sessions_test.go — cubre el contrato de sessions.go (SessionsDeps, MountSessions): el montaje
// condicional y la cadena de D2, D3 y D4, y el listado de D2. La salud derivada de D2 y su
// Alerter van en health_test.go; lo que D3 y D4 responden sin cadena, en sessionadmin_test.go.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

const (
	sessionsTarget = "/api/v1/sessions"
	profileTarget  = "/api/v1/sessions/sess-1/profile"
	statusTarget   = "/api/v1/sessions/sess-1/status"

	patternList    = "GET /api/v1/sessions"
	patternProfile = "POST /api/v1/sessions/{id}/profile"
	patternStatus  = "POST /api/v1/sessions/{id}/status"

	msgListExpired = "el listado de sesiones no respondió a tiempo, reintenta"
	msgListFailed  = "no se pudieron listar las sesiones"
)

// sessionsCara monta D2–D4 con k y d.
func sessionsCara(k apipublica.Common, d apipublica.SessionsDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountSessions(c, k, d)
	return c
}

// listSessions pide D2 con su permiso.
func listSessions(h *apipublicahelpertest.Harness, d apipublica.SessionsDeps) *httptest.ResponseRecorder {
	return h.Call(sessionsCara(h.Common(), d), h.With(tenantA, "sessions.read"), http.MethodGet, sessionsTarget, "")
}

// writeSession pide D3 o D4 con su permiso.
func writeSession(h *apipublicahelpertest.Harness, d apipublica.SessionsDeps, target, body string) *httptest.ResponseRecorder {
	return h.Call(sessionsCara(h.Common(), d), h.With(tenantA, "sessions.write"), http.MethodPost, target, body)
}

// oneSession es una flota de una sola sesión de tenantA.
func oneSession(s fleet.Session) *listerFake {
	return &listerFake{byTenant: map[string][]fleet.Session{tenantA: {s}}}
}

// wantSessionAudit exige EXACTAMENTE un registro de auditoría de D3/D4 con ese resultado y código.
func wantSessionAudit(t *testing.T, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("quedaron %d registros de auditoría, quiero exactamente 1", len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != "sessions.write" || r.Resource != "session" || r.Result != result || r.Meta["status"] != status {
		t.Errorf("registro %+v, quiero tenant %s, action sessions.write, resource session, result %s, status %d",
			r, tenantA, result, status)
	}
}

func TestMountSessions_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	repo, pusher := seededFleet(t), &pusherSpy{}
	cara := sessionsCara(h.Common(), apipublica.SessionsDeps{
		Sessions: repo, SessionProfiles: repo, ProfilePush: pusher, SessionStatus: repo,
	})
	wantPatterns(t, "D2–D4", cara, []string{patternList, patternProfile, patternStatus})
	checkChain(t, h, cara, routeCase{
		id: "D2", method: http.MethodGet, target: sessionsTarget, perm: "sessions.read", want: http.StatusOK,
	})
	checkChain(t, h, cara, routeCase{
		id: "D3", method: http.MethodPost, target: profileTarget, body: `{"profile":"active"}`,
		perm: "sessions.write", resource: "session", want: http.StatusOK,
	})
	if pusher.calls != 1 {
		t.Errorf("PushProfile se llamó %d veces; quiero 1: ni el 401 ni los 403 empujan", pusher.calls)
	}
	checkChain(t, h, cara, routeCase{
		id: "D4", method: http.MethodPost, target: statusTarget, body: loggedOutBody,
		perm: "sessions.write", resource: "session", want: http.StatusOK,
	})
	if s := seededSession(t, repo); s.Profile != fleet.ProfileActive || s.State != fleet.StateLoggedOut {
		t.Errorf("tras D3 y D4 la sesión quedó profile=%q state=%q; quiero active y loggedout", s.Profile, s.State)
	}
}

// TestMountSessions_EachRouteMountsByItsOwnDependency: sin su dependencia la ruta NO existe (404
// de ruta inexistente, no un 401), y ProfilePush ni monta ni desmonta nada.
func TestMountSessions_EachRouteMountsByItsOwnDependency(t *testing.T) {
	repo := seededFleet(t)
	cases := []struct {
		name string
		deps apipublica.SessionsDeps
		want []string
	}{
		{"no_dependencies_mounts_nothing", apipublica.SessionsDeps{}, nil},
		{"only_sessions", apipublica.SessionsDeps{Sessions: repo}, []string{patternList}},
		{"only_session_profiles_without_push", apipublica.SessionsDeps{SessionProfiles: repo}, []string{patternProfile}},
		{"only_session_status", apipublica.SessionsDeps{SessionStatus: repo}, []string{patternStatus}},
		{"profile_push_alone_mounts_nothing", apipublica.SessionsDeps{ProfilePush: &pusherSpy{}, Alerter: &alerterSpy{}}, nil},
		{"all", apipublica.SessionsDeps{Sessions: repo, SessionProfiles: repo, SessionStatus: repo},
			[]string{patternList, patternProfile, patternStatus}},
	}
	routes := []struct{ pattern, method, target, body, perm string }{
		{patternList, http.MethodGet, sessionsTarget, "", "sessions.read"},
		{patternProfile, http.MethodPost, profileTarget, passiveBody, "sessions.write"},
		{patternStatus, http.MethodPost, statusTarget, `{"state":"offline"}`, "sessions.write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := sessionsCara(h.Common(), tc.deps)
			wantPatterns(t, tc.name, cara, tc.want)
			for _, r := range routes {
				want := http.StatusNotFound
				for _, p := range tc.want {
					if p == r.pattern {
						want = http.StatusOK
					}
				}
				rec := h.Call(cara, h.With(tenantA, r.perm), r.method, r.target, r.body)
				wantCode(t, r.pattern, rec, want)
				if want == http.StatusNotFound && rec.Body.String() != "404 page not found\n" {
					t.Errorf("%s sin su dependencia: cuerpo %q, quiero el 404 de ruta inexistente", r.pattern, rec.Body.String())
				}
			}
		})
	}
}

func TestMountSessions_NilMWPanicsAtMount(t *testing.T) {
	for name, d := range map[string]apipublica.SessionsDeps{
		"with_dependencies":    {Sessions: sessA()},
		"without_dependencies": {},
	} {
		v := recuperar(func() { apipublica.MountSessions(apipublica.Nueva(), apipublica.Common{}, d) })
		if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountSessions") {
			t.Errorf("%s: MountSessions con MW nil: panic = %v; quiero un panic de cableado que nombre MountSessions", name, v)
		}
	}
}

// TestMountSessions_ListBodyByteForByte: los 25 campos, con su nombre y en su orden; los
// instantes en UTC; los punteros con un 0 MEDIDO viajan como 0 y el desglose, clave a clave.
func TestMountSessions_ListBodyByteForByte(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	zero, one, two, five := int64(0), int64(1), int64(2), int64(5)
	h := apipublicahelpertest.New(t)
	rec := listSessions(h, apipublica.SessionsDeps{
		Health: apipublica.HealthRules{Now: func() time.Time { return now }},
		Sessions: oneSession(fleet.Session{
			TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a",
			State: fleet.StateOnline, Profile: fleet.ProfileActive, SelfPn: "34600111222",
			// Un instante en otra zona: el cuerpo lo publica en UTC.
			LastConnectedAt: time.Date(2026, 7, 11, 8, 50, 0, 0, time.FixedZone("-03", -3*3600)),
			LastSeenAt:      now.Add(-time.Minute),
			WhatsappState:   "dead", DegradedReason: "dek_load_timeout",
			DegradedSince: now.Add(-6 * time.Minute), LastHealthAt: now.Add(-30 * time.Second),
			LastEventAgeS: 42, OutboxDepth: 3, BinaryVersion: "v0.9.0", UptimeS: 3600,
			DekLoadDurationMs: 250, IntentCircuit: "open", WorkerTaskset: "solapada",
			IntentP50Ms: &zero, IntentOmittedByReason: map[string]int64{"presupuesto": 2, "fastlane": 7},
			StuckHeads: &one, StuckHeadPolls: &five, FailedSealDispatch: &zero, FailedSealBudget: &two,
		}),
	})
	wantCode(t, "D2", rec, http.StatusOK)
	const want = `[{"session_id":"sess-a","edge_id":"edge-a","state":"online","self_pn":"34600111222",` +
		`"last_connected_at":"2026-07-11T11:50:00Z","last_seen_at":"2026-07-11T11:59:00Z","profile":"active",` +
		`"health":"degraded","whatsapp_state":"dead","degraded_reason":"dek_load_timeout",` +
		`"degraded_since":"2026-07-11T11:54:00Z","last_health_at":"2026-07-11T11:59:30Z","last_event_age_s":42,` +
		`"outbox_depth":3,"binary_version":"v0.9.0","uptime_s":3600,"dek_load_duration_ms":250,` +
		`"intent_circuit":"open","worker_taskset":"solapada","intent_p50_ms":0,` +
		`"intent_omitted_by_reason":{"fastlane":7,"presupuesto":2},"stuck_heads":1,"stuck_head_polls":5,` +
		`"failed_seal_dispatch":0,"failed_seal_budget":2}]`
	if got := rec.Body.String(); got != want {
		t.Errorf("D2: cuerpo\n  %s\nquiero (byte a byte)\n  %s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("D2: Content-Type %q, quiero application/json", ct)
	}
	if n := len(h.Auditor().Records()); n != 0 {
		t.Errorf("D2 es lectura: dejó %d registros de auditoría, quiero 0", n)
	}
}

// TestMountSessions_UnknownFieldsAreOmitted: lo que no se sabe NO viaja (ausencia ≠ cero), salvo
// profile, que va siempre; y un tenant sin sesiones es `[]`, nunca `null`.
func TestMountSessions_UnknownFieldsAreOmitted(t *testing.T) {
	cases := []struct {
		name   string
		lister *listerFake
		want   string
	}{
		{"an_old_edge_reports_only_the_link",
			oneSession(fleet.Session{TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a", State: fleet.StateOffline, Profile: fleet.ProfilePassive}),
			`[{"session_id":"sess-a","edge_id":"edge-a","state":"offline","profile":"passive"}]`},
		{"profile_is_never_omitted",
			oneSession(fleet.Session{SessionID: "sess-a"}),
			`[{"session_id":"sess-a","edge_id":"","state":"","profile":""}]`},
		{"an_empty_breakdown_is_omitted",
			oneSession(fleet.Session{SessionID: "sess-a", Profile: fleet.ProfileActive, IntentOmittedByReason: map[string]int64{}}),
			`[{"session_id":"sess-a","edge_id":"","state":"","profile":"active"}]`},
		{"no_sessions_is_an_empty_array", &listerFake{}, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := listSessions(apipublicahelpertest.New(t), apipublica.SessionsDeps{Sessions: tc.lister})
			wantCode(t, "D2", rec, http.StatusOK)
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("D2: cuerpo %s, quiero %s", got, tc.want)
			}
		})
	}
}

// TestMountSessions_ListsOnlyTheTokenTenant: el tenant es el del token (INV-8) y las filas salen
// en el orden de List.
func TestMountSessions_ListsOnlyTheTokenTenant(t *testing.T) {
	h := apipublicahelpertest.New(t)
	lister := sessA()
	rec := h.Call(sessionsCara(h.Common(), apipublica.SessionsDeps{Sessions: lister}),
		h.With(tenantA, "sessions.read"), http.MethodGet, sessionsTarget+"?tenant_id="+tenantB, "")
	wantCode(t, "D2", rec, http.StatusOK)
	var rows []struct {
		SessionID string `json:"session_id"`
	}
	wantJSON(t, "D2", rec, &rows)
	if len(rows) != 2 || rows[0].SessionID != "sess-otra" || rows[1].SessionID != "sess-a" {
		t.Errorf("D2: filas %+v; quiero sess-otra y sess-a, en ese orden, y nunca sess-b", rows)
	}
	if lister.calls != 1 || lister.tenant != tenantA {
		t.Errorf("List(%q) en %d llamadas; quiero el tenant del token %q en 1", lister.tenant, lister.calls, tenantA)
	}
}

func TestMountSessions_ListErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := listSessions(h, apipublica.SessionsDeps{Sessions: &listerFake{err: errors.New("bd caída")}})
	wantCode(t, "D2 con la flota caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "D2 con la flota caída", rec, msgListFailed)
	if n := len(logLines(h, msgGuardLog)); n != 0 {
		t.Errorf("un fallo que no es el plazo dejó %d líneas de plazo vencido", n)
	}
}

// TestMountSessions_ListDeadlineIs504: una flota que no contesta se corta con DBTimeout, y eso es
// un 504 legible (transitorio), no un 500.
func TestMountSessions_ListDeadlineIs504(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := listSessions(h, apipublica.SessionsDeps{Sessions: &listerFake{block: true}, DBTimeout: 20 * time.Millisecond})
	wantCode(t, "D2 con el listado vencido", rec, http.StatusGatewayTimeout)
	wantErrorBody(t, "D2 con el listado vencido", rec, msgListExpired)
	lines := logLines(h, msgGuardLog)
	if len(lines) != 1 || lines[0].Level != "warn" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel warn", msgGuardLog, lines)
	}
	if f := lines[0].Fields; f["op"] != "sessions.list" || f["tenant_id"] != tenantA {
		t.Errorf("campos = %v; quiero op sessions.list y tenant_id", f)
	}
}

// TestMountSessions_ListClock: qué plazo recibe List. Se mira el Deadline, sin esperar a nada.
func TestMountSessions_ListClock(t *testing.T) {
	const slack = 500 * time.Millisecond
	cases := []struct {
		name            string
		dbTimeout, want time.Duration
	}{
		{"zero_falls_to_default", 0, 1500 * time.Millisecond},
		{"negative_falls_to_default", -time.Second, 1500 * time.Millisecond},
		{"given", 5 * time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := sessA()
			rec := listSessions(apipublicahelpertest.New(t), apipublica.SessionsDeps{Sessions: lister, DBTimeout: tc.dbTimeout})
			wantCode(t, "D2", rec, http.StatusOK)
			if !lister.bounded || lister.remaining > tc.want || lister.remaining < tc.want-slack {
				t.Errorf("List vio plazo=%v restante=%v; quiero un plazo de ~%v", lister.bounded, lister.remaining, tc.want)
			}
		})
	}
}

// TestMountSessions_NilLogServesTheSame: sin logger, las mismas respuestas y ningún panic.
func TestMountSessions_NilLogServesTheSame(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	list := func(d apipublica.SessionsDeps) *httptest.ResponseRecorder {
		return h.Call(sessionsCara(k, d), h.With(tenantA, "sessions.read"), http.MethodGet, sessionsTarget, "")
	}
	wantCode(t, "D2 sin logger", list(apipublica.SessionsDeps{Sessions: sessA()}), http.StatusOK)
	wantCode(t, "D2 sin logger, listado vencido",
		list(apipublica.SessionsDeps{Sessions: &listerFake{block: true}, DBTimeout: 20 * time.Millisecond}), http.StatusGatewayTimeout)
	rec := h.Call(sessionsCara(k, apipublica.SessionsDeps{SessionProfiles: seededFleet(t), ProfilePush: &pusherSpy{err: errors.New("x")}}),
		h.With(tenantA, "sessions.write"), http.MethodPost, profileTarget, passiveBody)
	wantCode(t, "D3 sin logger, empuje fallido", rec, http.StatusOK)
}

// TestMountSessions_ProfileThroughTheChain: D3 sirve el handler de sessionadmin.go con el tenant
// DEL TOKEN, el pusher de d.ProfilePush y el logger de Common; sus errores siguen siendo texto
// plano y la auditoría los apunta como fallo.
func TestMountSessions_ProfileThroughTheChain(t *testing.T) {
	t.Run("ok_pushes_and_audits_success", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		repo, pusher := seededFleet(t), &pusherSpy{err: errors.New("el Edge no está conectado")}
		rec := writeSession(h, apipublica.SessionsDeps{SessionProfiles: repo, ProfilePush: pusher}, profileTarget, `{"profile":"active"}`)
		wantJSONBody(t, "D3", rec, `{"session_id":"sess-1","profile":"active"}`)
		if pusher.calls != 1 || pusher.tenant != tenantA || pusher.session != "sess-1" || pusher.profile != fleet.ProfileActive {
			t.Errorf("PushProfile(%q, %q, %q) en %d llamadas; quiero (%q, sess-1, active) en 1",
				pusher.tenant, pusher.session, pusher.profile, pusher.calls, tenantA)
		}
		if n := len(logLines(h, msgProfilePushLog)); n != 1 {
			t.Errorf("el empuje fallido dejó %d líneas en el logger de Common, quiero 1", n)
		}
		wantSessionAudit(t, h, "success", http.StatusOK)
	})
	t.Run("another_tenants_session_is_a_plain_404", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		store, pusher := &profileStoreFake{found: false}, &pusherSpy{}
		rec := writeSession(h, apipublica.SessionsDeps{SessionProfiles: store, ProfilePush: pusher}, profileTarget, passiveBody)
		wantPlain(t, "D3", rec, http.StatusNotFound, msgNoSession)
		if store.tenant != tenantA || pusher.calls != 0 {
			t.Errorf("SetProfile con tenant %q y %d empujes; quiero el del token y 0", store.tenant, pusher.calls)
		}
		wantSessionAudit(t, h, "failure", http.StatusNotFound)
	})
	t.Run("bot_is_a_plain_400", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		rec := writeSession(h, apipublica.SessionsDeps{SessionProfiles: seededFleet(t)}, profileTarget, `{"profile":"bot"}`)
		wantPlain(t, "D3", rec, http.StatusBadRequest, msgBadProfile)
		wantSessionAudit(t, h, "failure", http.StatusBadRequest)
	})
	t.Run("store_failure_is_a_plain_500", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		rec := writeSession(h, apipublica.SessionsDeps{SessionProfiles: &profileStoreFake{err: errors.New("bd caída")}}, profileTarget, passiveBody)
		wantPlain(t, "D3", rec, http.StatusInternalServerError, msgProfileFailed)
		wantSessionAudit(t, h, "failure", http.StatusInternalServerError)
	})
}

// TestMountSessions_StatusThroughTheChain: lo mismo para D4.
func TestMountSessions_StatusThroughTheChain(t *testing.T) {
	cases := []struct {
		name   string
		store  *statusStoreFake
		body   string
		code   int
		plain  string
		result string
	}{
		{"ok_audits_success", &statusStoreFake{found: true}, loggedOutBody, http.StatusOK, "", "success"},
		{"another_tenants_session_is_a_plain_404", &statusStoreFake{found: false}, loggedOutBody, http.StatusNotFound, msgNoSession, "failure"},
		{"online_is_a_plain_400", &statusStoreFake{found: true}, `{"state":"online"}`, http.StatusBadRequest, msgBadState, "failure"},
		{"store_failure_is_a_plain_500", &statusStoreFake{err: errors.New("bd caída")}, loggedOutBody, http.StatusInternalServerError, msgStateFailed, "failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			rec := writeSession(h, apipublica.SessionsDeps{SessionStatus: tc.store}, statusTarget, tc.body)
			if tc.plain == "" {
				wantJSONBody(t, "D4", rec, `{"session_id":"sess-1","state":"loggedout"}`)
				if tc.store.tenant != tenantA || tc.store.session != "sess-1" {
					t.Errorf("SetState(%q, %q); quiero el tenant del token y sess-1", tc.store.tenant, tc.store.session)
				}
			} else {
				wantPlain(t, "D4", rec, tc.code, tc.plain)
			}
			wantSessionAudit(t, h, tc.result, tc.code)
		})
	}
}
