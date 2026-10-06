//go:build pendiente

package apipublica_test

// sessionadmin_test.go — cubre el contrato de sessionadmin.go (SessionProfileStore,
// ProfilePusher, SessionStatusStore, SetSessionProfileHandler, SetSessionStatusHandler) llamando
// a los handlers DIRECTAMENTE, sin la cadena de la cara: así los servirá también :8100 (J16 y
// J17). Su montaje en la cara pública (D3 y D4, con cadena y auditoría) lo cubre sessions_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/filtercfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

const (
	msgNoIdentity     = "autenticación requerida"
	msgNoSessionID    = "session id requerido en la ruta"
	msgBadJSON        = "cuerpo JSON inválido"
	msgBadProfile     = "profile inválido (usar active|passive)"
	msgBadState       = "state inválido (usar offline|loggedout)"
	msgNoSession      = "sesión no encontrada"
	msgProfileFailed  = "no se pudo fijar el perfil de la sesión"
	msgStateFailed    = "no se pudo fijar el estado de la sesión"
	msgProfilePushLog = "sessions: push de perfil best-effort falló (persistido; reconcilia al conectar)"

	passiveBody   = `{"profile":"passive"}`
	loggedOutBody = `{"state":"loggedout"}`
)

// Los puertos los cumplen las piezas REALES del módulo edge nuevo: sin estas líneas, los dobles
// de abajo probarían un contrato que nadie implementa.
var (
	_ apipublica.SessionProfileStore = fleet.Repository(nil)
	_ apipublica.SessionProfileStore = (*fleet.PostgresRepository)(nil)
	_ apipublica.SessionStatusStore  = fleet.Repository(nil)
	_ apipublica.SessionStatusStore  = (*fleet.PostgresRepository)(nil)
	_ apipublica.ProfilePusher       = (*filtercfg.Pusher)(nil)
)

// profileStoreFake es SessionProfileStore: devuelve found/err y apunta lo que recibió.
type profileStoreFake struct {
	found   bool
	err     error
	calls   int
	tenant  string
	session string
	profile fleet.Profile
}

var _ apipublica.SessionProfileStore = (*profileStoreFake)(nil)

func (f *profileStoreFake) SetProfile(_ context.Context, tenantID, sessionID string, profile fleet.Profile) (bool, error) {
	f.calls++
	f.tenant, f.session, f.profile = tenantID, sessionID, profile
	return f.found, f.err
}

// statusStoreFake es SessionStatusStore: devuelve found/err y apunta lo que recibió.
type statusStoreFake struct {
	found   bool
	err     error
	calls   int
	tenant  string
	session string
	state   fleet.State
}

var _ apipublica.SessionStatusStore = (*statusStoreFake)(nil)

func (f *statusStoreFake) SetState(_ context.Context, tenantID, sessionID string, state fleet.State) (bool, error) {
	f.calls++
	f.tenant, f.session, f.state = tenantID, sessionID, state
	return f.found, f.err
}

// pusherSpy es ProfilePusher: apunta lo que le pidieron y CON QUÉ CONTEXTO (si venía muerto, su
// plazo y si conserva la Identity de la petición), y falla a voluntad.
type pusherSpy struct {
	err         error
	calls       int
	tenant      string
	session     string
	profile     fleet.Profile
	ctxErr      error
	bounded     bool
	remaining   time.Duration
	tenantInCtx string
}

var _ apipublica.ProfilePusher = (*pusherSpy)(nil)

func (p *pusherSpy) PushProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) error {
	p.calls++
	p.tenant, p.session, p.profile = tenantID, sessionID, profile
	p.ctxErr = ctx.Err()
	if dl, ok := ctx.Deadline(); ok {
		p.bounded, p.remaining = true, time.Until(dl)
	}
	if id, ok := httpapi.IdentityFromContext(ctx); ok {
		p.tenantInCtx = id.TenantID
	}
	return p.err
}

// adminCall sirve un POST a /admin/sessions/{id}/<leaf> con h montado en un mux con el patrón
// real (para que r.PathValue("id") funcione). tenant vacío = sin Identity en el contexto.
func adminCall(h http.Handler, leaf, tenant, sessionID, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("POST /admin/sessions/{id}/"+leaf, h)
	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+sessionID+"/"+leaf, strings.NewReader(body))
	if tenant != "" {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: subject}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func setProfile(store apipublica.SessionProfileStore, pusher apipublica.ProfilePusher, log sharedlogger.Logger, tenant, body string) *httptest.ResponseRecorder {
	return adminCall(apipublica.SetSessionProfileHandler(store, pusher, log), "profile", tenant, "sess-1", body)
}

func setStatus(store apipublica.SessionStatusStore, tenant, body string) *httptest.ResponseRecorder {
	return adminCall(apipublica.SetSessionStatusHandler(store), "status", tenant, "sess-1", body)
}

// wantPlain exige el código y el cuerpo en TEXTO PLANO de http.Error: lo observable del handler
// viejo, que no usa el JSON {"error"} del resto de la cara.
func wantPlain(t *testing.T, what string, rec *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	wantCode(t, what, rec, code)
	if got := rec.Body.String(); got != msg+"\n" {
		t.Errorf("%s: cuerpo %q, quiero el texto plano %q", what, got, msg+"\n")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("%s: Content-Type %q, quiero text/plain; charset=utf-8", what, ct)
	}
}

// wantJSONBody exige el 200 con el cuerpo JSON exacto y su Content-Type.
func wantJSONBody(t *testing.T, what string, rec *httptest.ResponseRecorder, body string) {
	t.Helper()
	wantCode(t, what, rec, http.StatusOK)
	if got := rec.Body.String(); got != body {
		t.Errorf("%s: cuerpo %s, quiero %s", what, got, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q, quiero application/json", what, ct)
	}
}

// seededFleet es el doble del módulo edge con sess-1 de tenantA conectada (nace pasiva y online).
func seededFleet(t *testing.T) *fleethelpertest.Memoria {
	t.Helper()
	repo := fleethelpertest.NewMemoria()
	if err := repo.MarkOnline(t.Context(), tenantA, "edge-1", "sess-1"); err != nil {
		t.Fatalf("sembrando sess-1: %v", err)
	}
	return repo
}

func seededSession(t *testing.T, repo *fleethelpertest.Memoria) fleet.Session {
	t.Helper()
	s, found, err := repo.Get(t.Context(), tenantA, "edge-1", "sess-1")
	if err != nil || !found {
		t.Fatalf("leyendo sess-1: found=%v err=%v", found, err)
	}
	return s
}

func TestSetSessionProfileHandler_OK(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"passive", passiveBody, "passive"},
		{"active", `{"profile":"active"}`, "active"},
		{"surrounding_spaces_are_trimmed", `{"profile":"  active "}`, "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, pusher := &profileStoreFake{found: true}, &pusherSpy{}
			rec := setProfile(store, pusher, nil, tenantA, tc.body)
			wantJSONBody(t, "perfil", rec, `{"session_id":"sess-1","profile":"`+tc.want+`"}`)
			if store.calls != 1 || store.tenant != tenantA || store.session != "sess-1" || string(store.profile) != tc.want {
				t.Errorf("SetProfile(%q, %q, %q) en %d llamadas; quiero el tenant de la Identity, sess-1 y %q en 1",
					store.tenant, store.session, store.profile, store.calls, tc.want)
			}
			if pusher.calls != 1 || pusher.tenant != tenantA || pusher.session != "sess-1" || string(pusher.profile) != tc.want {
				t.Errorf("PushProfile(%q, %q, %q) en %d llamadas; quiero (%q, sess-1, %q) en 1",
					pusher.tenant, pusher.session, pusher.profile, pusher.calls, tenantA, tc.want)
			}
		})
	}
}

// TestSetSessionProfileHandler_WithTheModuleFake: contra el doble que pasa la suite del puerto
// fleet, el perfil queda escrito, y la sesión de otro tenant ni se toca ni se revela.
func TestSetSessionProfileHandler_WithTheModuleFake(t *testing.T) {
	repo, pusher := seededFleet(t), &pusherSpy{}
	rec := setProfile(repo, pusher, nil, tenantB, `{"profile":"active"}`)
	wantPlain(t, "perfil de la sesión de otro tenant", rec, http.StatusNotFound, msgNoSession)
	if got := seededSession(t, repo).Profile; got != fleet.ProfilePassive {
		t.Errorf("otro tenant cambió el perfil a %q", got)
	}
	if pusher.calls != 0 {
		t.Errorf("un 404 empujó %d veces: se empujaría un cambio que no se persistió", pusher.calls)
	}

	wantJSONBody(t, "perfil", setProfile(repo, pusher, nil, tenantA, `{"profile":"active"}`), `{"session_id":"sess-1","profile":"active"}`)
	if got := seededSession(t, repo).Profile; got != fleet.ProfileActive {
		t.Errorf("perfil persistido %q, quiero active", got)
	}
}

func TestSetSessionProfileHandler_BadRequest(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"not_json", `{`, msgBadJSON},
		{"empty_body", ``, msgBadJSON},
		{"wrong_type", `{"profile":7}`, msgBadJSON},
		{"empty_profile", `{"profile":""}`, msgBadProfile},
		{"missing_profile", `{}`, msgBadProfile},
		{"bot_is_the_old_vocabulary", `{"profile":"bot"}`, msgBadProfile},
		{"unknown_profile", `{"profile":"supervisor"}`, msgBadProfile},
		{"a_state_is_not_a_profile", `{"profile":"online"}`, msgBadProfile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, pusher := &profileStoreFake{found: true}, &pusherSpy{}
			wantPlain(t, "perfil", setProfile(store, pusher, nil, tenantA, tc.body), http.StatusBadRequest, tc.want)
			if store.calls != 0 || pusher.calls != 0 {
				t.Errorf("un 400 persistió (%d) o empujó (%d); quiero 0 y 0", store.calls, pusher.calls)
			}
		})
	}
}

func TestSetSessionProfileHandler_NoIdentityIs401(t *testing.T) {
	store, pusher := &profileStoreFake{found: true}, &pusherSpy{}
	wantPlain(t, "perfil sin Identity", setProfile(store, pusher, nil, "", passiveBody), http.StatusUnauthorized, msgNoIdentity)

	// Una Identity sin tenant (persona sin empresa) tampoco puede acotar la operación.
	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/sess-1/profile", strings.NewReader(passiveBody))
	req.SetPathValue("id", "sess-1")
	req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{Subject: subject}))
	rec := httptest.NewRecorder()
	apipublica.SetSessionProfileHandler(store, pusher, nil).ServeHTTP(rec, req)
	wantPlain(t, "perfil con Identity sin tenant", rec, http.StatusUnauthorized, msgNoIdentity)
	if store.calls != 0 || pusher.calls != 0 {
		t.Errorf("un 401 persistió (%d) o empujó (%d); quiero 0 y 0", store.calls, pusher.calls)
	}
}

// TestSessionAdminHandlers_MissingPathIDIs400: montado en una ruta sin {id}, ninguno escribe.
func TestSessionAdminHandlers_MissingPathIDIs400(t *testing.T) {
	profiles, statuses := &profileStoreFake{found: true}, &statusStoreFake{found: true}
	handlers := map[string]http.Handler{
		passiveBody:   apipublica.SetSessionProfileHandler(profiles, nil, nil),
		loggedOutBody: apipublica.SetSessionStatusHandler(statuses),
	}
	for body, h := range handlers {
		req := httptest.NewRequest(http.MethodPost, "/sin-id", strings.NewReader(body))
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenantA, Subject: subject}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		wantPlain(t, "ruta sin {id} con "+body, rec, http.StatusBadRequest, msgNoSessionID)
	}
	if profiles.calls != 0 || statuses.calls != 0 {
		t.Errorf("sin id en la ruta se persistió: perfil %d, estado %d", profiles.calls, statuses.calls)
	}
}

func TestSetSessionProfileHandler_StoreOutcomes(t *testing.T) {
	cases := []struct {
		name  string
		store *profileStoreFake
		code  int
		want  string
	}{
		{"not_found_is_404", &profileStoreFake{found: false}, http.StatusNotFound, msgNoSession},
		{"invalid_profile_sentinel_is_400", &profileStoreFake{err: fleet.ErrInvalidProfile}, http.StatusBadRequest, msgBadProfile},
		{"wrapped_sentinel_is_400", &profileStoreFake{err: fmt.Errorf("fleet: %w", fleet.ErrInvalidProfile)}, http.StatusBadRequest, msgBadProfile},
		{"any_other_error_is_500", &profileStoreFake{found: true, err: errors.New("bd caída")}, http.StatusInternalServerError, msgProfileFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pusher := &pusherSpy{}
			wantPlain(t, "perfil", setProfile(tc.store, pusher, nil, tenantA, passiveBody), tc.code, tc.want)
			if pusher.calls != 0 {
				t.Errorf("se empujó %d veces un cambio que no se persistió", pusher.calls)
			}
		})
	}
}

// TestSetSessionProfileHandler_PushFailureKeepsThe200: el empuje es best-effort. Su fallo no
// cambia el código ni deshace la escritura; queda dicho en una línea Warn.
func TestSetSessionProfileHandler_PushFailureKeepsThe200(t *testing.T) {
	h := apipublicahelpertest.New(t)
	repo := seededFleet(t)
	if _, err := repo.SetProfile(t.Context(), tenantA, "sess-1", fleet.ProfileActive); err != nil {
		t.Fatalf("sembrando el perfil: %v", err)
	}
	pushErr := errors.New("el Edge no está conectado")
	pusher := &pusherSpy{err: pushErr}

	rec := setProfile(repo, pusher, h.Log(), tenantA, passiveBody)
	wantJSONBody(t, "perfil con el empuje fallido", rec, `{"session_id":"sess-1","profile":"passive"}`)
	if pusher.calls != 1 {
		t.Errorf("PushProfile se llamó %d veces, quiero 1", pusher.calls)
	}
	if got := seededSession(t, repo).Profile; got != fleet.ProfilePassive {
		t.Errorf("el empuje fallido se llevó por delante la escritura: perfil %q", got)
	}
	lines := logLines(h, msgProfilePushLog)
	if len(lines) != 1 || lines[0].Level != "warn" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel warn", msgProfilePushLog, lines)
	}
	f := lines[0].Fields
	if err, ok := f["error"].(error); !ok || !errors.Is(err, pushErr) || f["tenant_id"] != tenantA || f["session_id"] != "sess-1" || f["profile"] != "passive" {
		t.Errorf("campos = %v; quiero tenant_id, session_id sess-1, profile passive y el error del empuje", f)
	}
}

// TestSetSessionProfileHandler_NilPusherAndNilLog: nil es un no-op válido en los dos.
func TestSetSessionProfileHandler_NilPusherAndNilLog(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := setProfile(&profileStoreFake{found: true}, nil, h.Log(), tenantA, passiveBody)
	wantJSONBody(t, "perfil sin pusher", rec, `{"session_id":"sess-1","profile":"passive"}`)
	if n := len(h.Log().Entries()); n != 0 {
		t.Errorf("sin pusher quedaron %d líneas de log, quiero 0", n)
	}
	rec = setProfile(&profileStoreFake{found: true}, &pusherSpy{err: errors.New("x")}, nil, tenantA, passiveBody)
	wantJSONBody(t, "perfil con el empuje fallido y sin logger", rec, `{"session_id":"sess-1","profile":"passive"}`)
}

// TestSetSessionProfileHandler_PushSurvivesClientAbort es la corrección del code review del
// 2026-08-21: con el contexto de la petición, un cliente que abortaba el POST se llevaba por
// delante el empuje y dejaba el perfil PERSISTIDO con la sesión viva sin enterarse. Se ejerce con
// la petición ya abortada ANTES de entrar al handler, que es el caso límite.
func TestSetSessionProfileHandler_PushSurvivesClientAbort(t *testing.T) {
	store, pusher := &profileStoreFake{found: true}, &pusherSpy{}
	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/sess-1/profile", strings.NewReader(passiveBody))
	req.SetPathValue("id", "sess-1")
	ctx, cancel := context.WithCancel(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenantA, Subject: subject}))
	cancel() // el cliente se fue
	rec := httptest.NewRecorder()
	apipublica.SetSessionProfileHandler(store, pusher, nil).ServeHTTP(rec, req.WithContext(ctx))

	wantCode(t, "perfil con el cliente ido", rec, http.StatusOK)
	if pusher.calls != 1 {
		t.Fatalf("PushProfile se llamó %d veces, quiero 1: el aborto no puede perder el empuje", pusher.calls)
	}
	if pusher.ctxErr != nil {
		t.Errorf("el empuje recibió un contexto MUERTO (%v): falta soltar la cancelación de la petición", pusher.ctxErr)
	}
	if !pusher.bounded || pusher.remaining > 5*time.Second || pusher.remaining < 4*time.Second {
		t.Errorf("el empuje vio plazo=%v restante=%v; quiero un plazo propio de ~5 s", pusher.bounded, pusher.remaining)
	}
	if pusher.tenantInCtx != tenantA {
		t.Errorf("el contexto del empuje perdió los valores de la petición (tenant %q)", pusher.tenantInCtx)
	}
}

func TestSetSessionStatusHandler_OK(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"loggedout_retires_a_zombie", loggedOutBody, "loggedout"},
		{"offline", `{"state":"offline"}`, "offline"},
		{"surrounding_spaces_are_trimmed", `{"state":" offline  "}`, "offline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &statusStoreFake{found: true}
			wantJSONBody(t, "estado", setStatus(store, tenantA, tc.body), `{"session_id":"sess-1","state":"`+tc.want+`"}`)
			if store.calls != 1 || store.tenant != tenantA || store.session != "sess-1" || string(store.state) != tc.want {
				t.Errorf("SetState(%q, %q, %q) en %d llamadas; quiero el tenant de la Identity, sess-1 y %q en 1",
					store.tenant, store.session, store.state, store.calls, tc.want)
			}
		})
	}
}

func TestSetSessionStatusHandler_WithTheModuleFake(t *testing.T) {
	repo := seededFleet(t)
	wantPlain(t, "estado de la sesión de otro tenant", setStatus(repo, tenantB, loggedOutBody), http.StatusNotFound, msgNoSession)
	if got := seededSession(t, repo).State; got != fleet.StateOnline {
		t.Errorf("otro tenant cambió el estado a %q", got)
	}
	wantJSONBody(t, "estado", setStatus(repo, tenantA, loggedOutBody), `{"session_id":"sess-1","state":"loggedout"}`)
	if got := seededSession(t, repo).State; got != fleet.StateLoggedOut {
		t.Errorf("estado persistido %q, quiero loggedout", got)
	}
}

func TestSetSessionStatusHandler_BadRequest(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"not_json", `{`, msgBadJSON},
		{"empty_body", ``, msgBadJSON},
		{"wrong_type", `{"state":true}`, msgBadJSON},
		{"empty_state", `{"state":""}`, msgBadState},
		{"online_is_derived_not_settable", `{"state":"online"}`, msgBadState},
		{"unknown_state", `{"state":"borrada"}`, msgBadState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &statusStoreFake{found: true}
			wantPlain(t, "estado", setStatus(store, tenantA, tc.body), http.StatusBadRequest, tc.want)
			if store.calls != 0 {
				t.Errorf("un 400 persistió %d veces", store.calls)
			}
		})
	}
}

func TestSetSessionStatusHandler_NoIdentityIs401(t *testing.T) {
	store := &statusStoreFake{found: true}
	wantPlain(t, "estado sin Identity", setStatus(store, "", loggedOutBody), http.StatusUnauthorized, msgNoIdentity)
	if store.calls != 0 {
		t.Errorf("un 401 persistió %d veces", store.calls)
	}
}

func TestSetSessionStatusHandler_StoreOutcomes(t *testing.T) {
	cases := []struct {
		name  string
		store *statusStoreFake
		code  int
		want  string
	}{
		{"not_found_is_404", &statusStoreFake{found: false}, http.StatusNotFound, msgNoSession},
		{"invalid_state_sentinel_is_400", &statusStoreFake{err: fleet.ErrInvalidState}, http.StatusBadRequest, msgBadState},
		{"wrapped_sentinel_is_400", &statusStoreFake{err: fmt.Errorf("fleet: %w", fleet.ErrInvalidState)}, http.StatusBadRequest, msgBadState},
		{"any_other_error_is_500", &statusStoreFake{found: true, err: errors.New("bd caída")}, http.StatusInternalServerError, msgStateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantPlain(t, "estado", setStatus(tc.store, tenantA, loggedOutBody), tc.code, tc.want)
		})
	}
}
