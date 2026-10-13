package apipublica_test

// health_test.go — cubre el contrato de health.go (HealthRules, Alerter, NoopAlerter) por donde
// se observa: el campo "health" de GET /api/v1/sessions (D2) y las llamadas al Alerter. La tabla
// fina de la derivación (umbrales estrictos, precedencia) está en health_derive_test.go, que
// nació con el verde. El reloj va inyectado, salvo en el caso que prueba justo eso.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

const msgAlertFailed = "alerting de salud falló (best-effort)"

var healthNow = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return healthNow }

// alert es una llamada al Alerter.
type alert struct{ tenant, session, state string }

// alerterSpy es Alerter: apunta cada llamada y falla a voluntad.
type alerterSpy struct {
	mu    sync.Mutex
	err   error
	calls []alert
}

var (
	_ apipublica.Alerter = (*alerterSpy)(nil)
	_ apipublica.Alerter = apipublica.NoopAlerter{}
)

func (a *alerterSpy) Alert(_ context.Context, tenantID, sessionID, derivedState string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, alert{tenantID, sessionID, derivedState})
	return a.err
}

// healthOf lista con rules una flota de una sesión y devuelve su campo "health" ("" si se omite).
func healthOf(t *testing.T, rules apipublica.HealthRules, s fleet.Session) string {
	t.Helper()
	s.SessionID = "sess-a"
	rec := listSessions(apipublicahelpertest.New(t), apipublica.SessionsDeps{Sessions: oneSession(s), Health: rules})
	wantCode(t, "D2", rec, http.StatusOK)
	var rows []map[string]any
	wantJSON(t, "D2", rec, &rows)
	if len(rows) != 1 {
		t.Fatalf("D2: %d filas, quiero 1", len(rows))
	}
	raw, present := rows[0]["health"]
	if !present {
		return ""
	}
	health, isString := raw.(string)
	if !isString || health == "" {
		t.Errorf(`D2: "health" = %v; una salud sin etiqueta se OMITE, no viaja vacía ni con otro tipo`, raw)
	}
	return health
}

func TestHealthRules_DeriveTheHealthField(t *testing.T) {
	given := apipublica.HealthRules{DegradedAfter: 10 * time.Minute, StaleAfter: 4 * time.Minute, Now: fixedClock}
	defaults := apipublica.HealthRules{Now: fixedClock}
	negative := apipublica.HealthRules{DegradedAfter: -time.Hour, StaleAfter: -time.Hour, Now: fixedClock}
	ago := func(d time.Duration) time.Time { return healthNow.Add(-d) }
	cases := []struct {
		name  string
		rules apipublica.HealthRules
		s     fleet.Session
		want  string
	}{
		{"healthy_fresh_health", given, fleet.Session{LastHealthAt: ago(10 * time.Second)}, ""},
		{"sustained_degraded", given, fleet.Session{DegradedSince: ago(11 * time.Minute), LastHealthAt: ago(time.Second)}, "degraded"},
		{"recent_degraded_is_not_labelled_yet", given, fleet.Session{DegradedSince: ago(9 * time.Minute), LastHealthAt: ago(time.Second)}, ""},
		{"stale_health", given, fleet.Session{LastHealthAt: ago(5 * time.Minute)}, "stale"},
		{"stale_wins_over_degraded", given, fleet.Session{DegradedSince: ago(time.Hour), LastHealthAt: ago(5 * time.Minute)}, "stale"},
		{"an_old_edge_without_health_is_never_labelled", given, fleet.Session{State: fleet.StateOnline}, ""},
		{"zero_rules_default_to_five_minutes_degraded", defaults, fleet.Session{DegradedSince: ago(6 * time.Minute), LastHealthAt: ago(10 * time.Second)}, "degraded"},
		{"zero_rules_do_not_degrade_before_five_minutes", defaults, fleet.Session{DegradedSince: ago(4 * time.Minute), LastHealthAt: ago(10 * time.Second)}, ""},
		{"zero_rules_default_to_two_minutes_stale", defaults, fleet.Session{LastHealthAt: ago(3 * time.Minute)}, "stale"},
		{"zero_rules_are_not_stale_before_two_minutes", defaults, fleet.Session{LastHealthAt: ago(time.Minute)}, ""},
		{"negative_thresholds_fall_to_the_defaults", negative, fleet.Session{LastHealthAt: ago(time.Minute)}, ""},
		{"negative_thresholds_still_derive", negative, fleet.Session{LastHealthAt: ago(3 * time.Minute)}, "stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthOf(t, tc.rules, tc.s); got != tc.want {
				t.Errorf("health = %q, quiero %q", got, tc.want)
			}
		})
	}
}

// TestHealthRules_NilNowIsTheRealClock: sin reloj inyectado manda time.Now. Los márgenes son de
// una hora, así que no depende de cuánto tarde el test.
func TestHealthRules_NilNowIsTheRealClock(t *testing.T) {
	rules := apipublica.HealthRules{}
	if got := healthOf(t, rules, fleet.Session{LastHealthAt: time.Now().Add(-time.Hour)}); got != "stale" {
		t.Errorf("salud de hace una hora con el reloj real: health = %q, quiero stale", got)
	}
	if got := healthOf(t, rules, fleet.Session{LastHealthAt: time.Now().Add(time.Hour)}); got != "" {
		t.Errorf("salud recién reportada con el reloj real: health = %q, quiero omitido", got)
	}
}

func TestNoopAlerter_DiscardsTheAlert(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, c := range []context.Context{t.Context(), ctx} {
		if err := (apipublica.NoopAlerter{}).Alert(c, tenantA, "sess-a", "degraded"); err != nil {
			t.Errorf("NoopAlerter.Alert = %v, quiero nil siempre", err)
		}
	}
	if err := (apipublica.NoopAlerter{}).Alert(t.Context(), "", "", ""); err != nil {
		t.Errorf("NoopAlerter.Alert sin argumentos = %v, quiero nil", err)
	}
}

// mixedFleet: una sesión sana, una degradada y una rancia, las tres de tenantA.
func mixedFleet() *listerFake {
	return &listerFake{byTenant: map[string][]fleet.Session{tenantA: {
		{SessionID: "sess-ok", LastHealthAt: healthNow.Add(-time.Second)},
		{SessionID: "sess-deg", DegradedSince: healthNow.Add(-time.Hour), LastHealthAt: healthNow.Add(-time.Second)},
		{SessionID: "sess-stale", LastHealthAt: healthNow.Add(-time.Hour)},
	}}}
}

// TestAlerter_CalledOncePerDerivedSession: el seam del alerting push queda vivo. Una vez por
// sesión con salud derivada, con el tenant del token; la sana no lo invoca.
func TestAlerter_CalledOncePerDerivedSession(t *testing.T) {
	spy := &alerterSpy{}
	rec := listSessions(apipublicahelpertest.New(t), apipublica.SessionsDeps{
		Sessions: mixedFleet(), Health: apipublica.HealthRules{Now: fixedClock}, Alerter: spy,
	})
	wantCode(t, "D2", rec, http.StatusOK)
	want := []alert{{tenantA, "sess-deg", "degraded"}, {tenantA, "sess-stale", "stale"}}
	if len(spy.calls) != len(want) || spy.calls[0] != want[0] || spy.calls[1] != want[1] {
		t.Errorf("llamadas al Alerter = %+v, quiero %+v", spy.calls, want)
	}
}

// TestAlerter_FailureDoesNotChangeTheResponse: es best-effort. La salud ya va en la respuesta; el
// fallo queda en una línea Debug.
func TestAlerter_FailureDoesNotChangeTheResponse(t *testing.T) {
	deps := func(a apipublica.Alerter) apipublica.SessionsDeps {
		return apipublica.SessionsDeps{Sessions: mixedFleet(), Health: apipublica.HealthRules{Now: fixedClock}, Alerter: a}
	}
	quiet := listSessions(apipublicahelpertest.New(t), deps(&alerterSpy{}))

	h := apipublicahelpertest.New(t)
	alertErr := errors.New("webhook caído")
	rec := listSessions(h, deps(&alerterSpy{err: alertErr}))
	wantCode(t, "D2 con el Alerter caído", rec, http.StatusOK)
	if rec.Body.String() != quiet.Body.String() {
		t.Errorf("el fallo del Alerter cambió el cuerpo:\n  %s\nquiero\n  %s", rec.Body.String(), quiet.Body.String())
	}
	lines := logLines(h, msgAlertFailed)
	if len(lines) != 2 || lines[0].Level != "debug" {
		t.Fatalf("línea %q: %+v; quiero dos (una por sesión con salud derivada), de nivel debug", msgAlertFailed, lines)
	}
	f := lines[0].Fields
	if err, ok := f["error"].(error); !ok || !errors.Is(err, alertErr) || f["session_id"] != "sess-deg" || f["estado"] != "degraded" {
		t.Errorf("campos = %v; quiero session_id sess-deg, estado degraded y el error del Alerter", f)
	}

	// Sin logger, lo mismo y sin panic.
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	rec = h.Call(sessionsCara(k, deps(&alerterSpy{err: alertErr})), h.With(tenantA, "sessions.read"), http.MethodGet, sessionsTarget, "")
	wantCode(t, "D2 con el Alerter caído y sin logger", rec, http.StatusOK)
}

// TestAlerter_NilFallsBackToNoop: sin Alerter cableado la salud se publica igual.
func TestAlerter_NilFallsBackToNoop(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := listSessions(h, apipublica.SessionsDeps{Sessions: mixedFleet(), Health: apipublica.HealthRules{Now: fixedClock}})
	wantCode(t, "D2 sin Alerter", rec, http.StatusOK)
	var rows []struct {
		SessionID string `json:"session_id"`
		Health    string `json:"health"`
	}
	wantJSON(t, "D2 sin Alerter", rec, &rows)
	if len(rows) != 3 || rows[0].Health != "" || rows[1].Health != "degraded" || rows[2].Health != "stale" {
		t.Errorf("D2 sin Alerter: filas %+v; quiero sana, degraded y stale", rows)
	}
	if n := len(logLines(h, msgAlertFailed)); n != 0 {
		t.Errorf("sin Alerter quedaron %d líneas de alerting fallido, quiero 0", n)
	}
}
