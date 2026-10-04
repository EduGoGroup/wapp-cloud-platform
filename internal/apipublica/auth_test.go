package apipublica_test

// auth_test.go — cubre el contrato de auth.go (AuthDeps, MountAuth): A1–A7, los patrones, las
// condiciones de montaje, el limitador propio del alta y los fallos de cableado.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin/platformadminhelpertest"
)

// verifierFake es in.TokenVerifier: da por válido cualquier token y apunta el último.
type verifierFake struct{ callLog }

var _ in.TokenVerifier = (*verifierFake)(nil)

func (f *verifierFake) Verify(_ context.Context, token string) (in.VerifyResult, error) {
	f.add("Verify %s", token)
	return in.VerifyResult{Valid: true, TenantID: tenantA, Subject: subject, ExpiresAt: fixedNow}, nil
}

// exchangerFake es in.Exchanger: canjea cualquier Identity Token por un contexto de tenantA.
type exchangerFake struct{ callLog }

var _ in.Exchanger = (*exchangerFake)(nil)

func (f *exchangerFake) Exchange(_ context.Context, input in.ExchangeInput) (in.ExchangeResult, error) {
	f.add("Exchange %s", input.IdentityToken)
	return in.ExchangeResult{ContextToken: "ctx-token", ExpiresAt: fixedNow,
		Context: domain.IdentityContext{TenantID: tenantA, UserID: subject}}, nil
}

// redeemerFake es in.InvitationRedeemer: apunta el token y devuelve err.
type redeemerFake struct {
	callLog
	err error
}

var _ in.InvitationRedeemer = (*redeemerFake)(nil)

func (f *redeemerFake) RedeemInvitation(_ context.Context, token string) error {
	f.add("RedeemInvitation %s", token)
	return f.err
}

// activeTenantFake es in.ActiveTenantSelector e in.TenantLister a la vez, como el servicio real.
type activeTenantFake struct {
	callLog
	mu     sync.Mutex
	active string
}

var (
	_ in.ActiveTenantSelector = (*activeTenantFake)(nil)
	_ in.TenantLister         = (*activeTenantFake)(nil)
)

func (f *activeTenantFake) SelectActiveTenant(_ context.Context, tenantID string) error {
	f.add("SelectActiveTenant %s", tenantID)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = tenantID
	return nil
}

func (f *activeTenantFake) TenantsOfCaller(context.Context) ([]domain.UserTenant, string, error) {
	f.add("TenantsOfCaller")
	f.mu.Lock()
	defer f.mu.Unlock()
	return []domain.UserTenant{{ID: tenantA, DisplayName: "Empresa A"}, {ID: apipublicahelpertest.TenantB, DisplayName: "Empresa B"}}, f.active, nil
}

var authPatterns = []string{
	"/api/v1/auth/verify",
	"/api/v1/auth/exchange",
	"/api/v1/auth/whoami",
	"POST /api/v1/invitations/accept",
	"POST /api/v1/auth/active-tenant",
	"GET /api/v1/auth/tenants",
	"POST /api/v1/signup",
}

// authFakes son los dobles de una cara con A1–A7 montadas.
type authFakes struct {
	verifier  *verifierFake
	exchanger *exchangerFake
	redeemer  *redeemerFake
	active    *activeTenantFake
}

// authCara monta A1–A7 con todos sus puertos (A7 en la rama sin M2M).
func authCara(k apipublica.Common) (*apipublica.Cara, authFakes) {
	f := authFakes{&verifierFake{}, &exchangerFake{}, &redeemerFake{}, &activeTenantFake{}}
	c := apipublica.Nueva()
	apipublica.MountAuth(c, k, apipublica.AuthDeps{
		Verifier: f.verifier, Exchanger: f.exchanger, Redeemer: f.redeemer,
		TenantSelector: f.active, TenantLister: f.active,
		SignupRequests: platformadminhelpertest.NewFake(),
	})
	return c, f
}

func TestMountAuth_Patterns(t *testing.T) {
	h := apipublicahelpertest.New(t)
	full, _ := authCara(h.Common())
	wantPatterns(t, "todas", full, authPatterns)

	only := apipublica.Nueva()
	apipublica.MountAuth(only, h.Common(), apipublica.AuthDeps{Verifier: &verifierFake{}})
	wantPatterns(t, "solo Verifier", only, []string{
		"/api/v1/auth/verify", "/api/v1/auth/exchange", "/api/v1/auth/whoami", "POST /api/v1/signup",
	})
}

func TestMountAuth_VerifyAndExchangeArePublic(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, f := authCara(h.Common())

	rec := h.Call(c, "", http.MethodPost, "/api/v1/auth/verify", `{"token":"tok-x"}`)
	wantCode(t, "A1 sin token de sesión", rec, http.StatusOK)
	if f.verifier.last() != "Verify tok-x" {
		t.Errorf("A1: el verificador recibió %q, quiero Verify tok-x", f.verifier.last())
	}

	rec = h.Call(c, "", http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"it-1"}`)
	wantCode(t, "A2 sin token de sesión", rec, http.StatusOK)
	var body map[string]any
	wantJSON(t, "A2", rec, &body)
	if body["context_token"] != "ctx-token" || f.exchanger.last() != "Exchange it-1" {
		t.Errorf("A2: cuerpo %v y canje %q; quiero ctx-token y Exchange it-1", body, f.exchanger.last())
	}
}

func TestMountAuth_ExchangeWithoutExchangerIs503(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountAuth(c, h.Common(), apipublica.AuthDeps{Verifier: &verifierFake{}})
	rec := h.Call(c, "", http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"it-1"}`)
	wantCode(t, "A2 en modo dual apagado", rec, http.StatusServiceUnavailable)
	wantErrorBody(t, "A2 en modo dual apagado", rec, "modo dual apagado: identity no está configurado en este despliegue")
}

func TestMountAuth_WhoAmI(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, _ := authCara(h.Common())
	wantCode(t, "A3 sin token", h.Call(c, "", http.MethodGet, "/api/v1/auth/whoami", ""), http.StatusUnauthorized)

	for _, tc := range []struct{ name, token, tenant string }{
		{"tenantless", h.Tenantless("sin-empresa"), ""},
		{"with_tenant", h.With(tenantA), tenantA},
	} {
		rec := h.Call(c, tc.token, http.MethodGet, "/api/v1/auth/whoami", "")
		wantCode(t, "A3 "+tc.name, rec, http.StatusOK)
		var body map[string]any
		wantJSON(t, "A3 "+tc.name, rec, &body)
		if body["tenant_id"] != tc.tenant {
			t.Errorf("A3 %s: tenant_id = %v, quiero %q", tc.name, body["tenant_id"], tc.tenant)
		}
	}
}

// TestMountAuth_AuthenticateOnlyRoutes: A4–A6 exigen token (401 sin él) pero NO permiso: un token
// sin empresa y sin grants llega al puerto.
func TestMountAuth_AuthenticateOnlyRoutes(t *testing.T) {
	cases := []struct {
		id, method, target, body string
		want                     int
		call                     func(f authFakes) string
		wantCall                 string
	}{
		{"A4", http.MethodPost, "/api/v1/invitations/accept", `{"token":"inv-1"}`, http.StatusNoContent,
			func(f authFakes) string { return f.redeemer.last() }, "RedeemInvitation inv-1"},
		{"A5", http.MethodPost, "/api/v1/auth/active-tenant", `{"tenant_id":"` + tenantA + `"}`, http.StatusNoContent,
			func(f authFakes) string { return f.active.last() }, "SelectActiveTenant " + tenantA},
		{"A6", http.MethodGet, "/api/v1/auth/tenants", "", http.StatusOK,
			func(f authFakes) string { return f.active.last() }, "TenantsOfCaller"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			c, f := authCara(h.Common())
			wantCode(t, tc.id+" sin token", h.Call(c, "", tc.method, tc.target, tc.body), http.StatusUnauthorized)
			if got := tc.call(f); got != "" {
				t.Errorf("%s sin token llegó al puerto: %q", tc.id, got)
			}
			wantCode(t, tc.id+" sin empresa ni grants", h.Call(c, h.Tenantless("nuevo"), tc.method, tc.target, tc.body), tc.want)
			if got := tc.call(f); got != tc.wantCall {
				t.Errorf("%s: el puerto recibió %q, quiero %q", tc.id, got, tc.wantCall)
			}
		})
	}
}

func TestMountAuth_TenantListBody(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, f := authCara(h.Common())
	f.active.active = apipublicahelpertest.TenantB
	var body struct {
		Tenants []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Active      bool   `json:"active"`
		} `json:"tenants"`
	}
	wantJSON(t, "A6", h.Call(c, h.Tenantless("dos-empresas"), http.MethodGet, "/api/v1/auth/tenants", ""), &body)
	if len(body.Tenants) != 2 || body.Tenants[0].Active || !body.Tenants[1].Active {
		t.Errorf("A6: %+v, quiero A y B con B activa", body.Tenants)
	}
}

func TestMountAuth_AcceptConflictIs409(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountAuth(c, h.Common(), apipublica.AuthDeps{Verifier: &verifierFake{}, Redeemer: &redeemerFake{err: domain.ErrConflict}})
	rec := h.Call(c, h.Tenantless("nuevo"), http.MethodPost, "/api/v1/invitations/accept", `{"token":"inv-1"}`)
	wantCode(t, "A4 con conflicto", rec, http.StatusConflict)
}

func TestMountAuth_MissingPortsAre404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	tok := h.Tenantless("nuevo")
	cases := []struct {
		name string
		deps apipublica.AuthDeps
	}{
		{"no_redeemer_nor_tenant_ports", apipublica.AuthDeps{Verifier: &verifierFake{}}},
		{"only_selector", apipublica.AuthDeps{Verifier: &verifierFake{}, TenantSelector: &activeTenantFake{}}},
		{"only_lister", apipublica.AuthDeps{Verifier: &verifierFake{}, TenantLister: &activeTenantFake{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := apipublica.Nueva()
			apipublica.MountAuth(c, h.Common(), tc.deps)
			wantCode(t, "A4", h.Call(c, tok, http.MethodPost, "/api/v1/invitations/accept", `{"token":"t"}`), http.StatusNotFound)
			wantCode(t, "A5", h.Call(c, tok, http.MethodPost, "/api/v1/auth/active-tenant", `{"tenant_id":"x"}`), http.StatusNotFound)
			wantCode(t, "A6", h.Call(c, tok, http.MethodGet, "/api/v1/auth/tenants", ""), http.StatusNotFound)
		})
	}
}

// TestMountAuth_NoAuditNoAccessLog: ninguna de las siete deja registro de auditoría ni línea de
// access-log, ni en el camino feliz ni en el 401.
func TestMountAuth_NoAuditNoAccessLog(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, _ := authCara(h.Common())
	tok := h.Tenantless("nuevo")
	h.Call(c, "", http.MethodPost, "/api/v1/auth/verify", `{"token":"x"}`)
	h.Call(c, "", http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"x"}`)
	h.Call(c, "", http.MethodGet, "/api/v1/auth/whoami", "")
	h.Call(c, tok, http.MethodGet, "/api/v1/auth/whoami", "")
	h.Call(c, tok, http.MethodPost, "/api/v1/invitations/accept", `{"token":"x"}`)
	h.Call(c, tok, http.MethodPost, "/api/v1/auth/active-tenant", `{"tenant_id":"x"}`)
	h.Call(c, tok, http.MethodGet, "/api/v1/auth/tenants", "")
	h.Call(c, "", http.MethodPost, "/api/v1/signup", `{}`)
	if n := len(h.Auditor().Records()); n != 0 {
		t.Errorf("A1–A7 dejaron %d registros de auditoría, quiero 0", n)
	}
	if lines := accessLines(h); len(lines) != 0 {
		t.Errorf("A1–A7 dejaron %d líneas de access-log, quiero 0: %+v", len(lines), lines)
	}
}

func TestMountAuth_WiringPanics(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cases := []struct {
		name string
		deps apipublica.AuthDeps
	}{
		{"nil_verifier", apipublica.AuthDeps{}},
		{"m2m_without_signup_requests", apipublica.AuthDeps{Verifier: &verifierFake{}, M2M: &m2mFake{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := recuperar(func() { apipublica.MountAuth(apipublica.Nueva(), h.Common(), tc.deps) })
			if v == nil || esPendiente(v) {
				t.Errorf("MountAuth %s: panic = %v; quiero un panic de cableado al montar (no nil, no pendiente)", tc.name, v)
			}
		})
	}
}

const (
	signupBody   = `{"email":"Ana@X.com","password":"una-clave-larga-1","first_name":"Ana","last_name":"Paz","origin":"bff"}`
	signupUserID = "33333333-3333-3333-3333-333333333333"
	//nolint:gosec // G101: no es una credencial, es el texto literal del aviso que nombra la variable
	signupWarnNoM2M = "POST /api/v1/signup: falta WAPP_IDENTITY_API_KEY; el registro público responde 503 (servicio no disponible)"
)

// m2mFake es out.IdentityM2MClient: el registro siempre crea signupUserID y apunta las llamadas.
type m2mFake struct{ callLog }

var _ out.IdentityM2MClient = (*m2mFake)(nil)

func (f *m2mFake) EnsureUser(_ context.Context, email, _, _ string) (domain.IdentityUser, error) {
	f.add("EnsureUser %s", email)
	return domain.IdentityUser{ID: signupUserID, Email: email}, nil
}

func (f *m2mFake) GetUserSystems(_ context.Context, userID string) ([]string, error) {
	f.add("GetUserSystems %s", userID)
	return nil, nil
}

func (f *m2mFake) ReplaceUserSystems(_ context.Context, userID string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.add("ReplaceUserSystems %s %v", userID, systems)
	return domain.IdentitySystemsDiff{Systems: systems}, nil
}

func (f *m2mFake) Signup(_ context.Context, email, _, _, _ string) (string, error) {
	f.add("Signup %s", email)
	return signupUserID, nil
}

// signupCara monta A1–A7 con el cliente M2M dado (nil = la rama del 503 fijo).
func signupCara(k apipublica.Common, m2m out.IdentityM2MClient) (*apipublica.Cara, *platformadminhelpertest.Fake) {
	requests := platformadminhelpertest.NewFake()
	c := apipublica.Nueva()
	deps := apipublica.AuthDeps{Verifier: &verifierFake{}, SignupRequests: requests}
	if m2m != nil {
		deps.M2M = m2m
	}
	apipublica.MountAuth(c, k, deps)
	return c, requests
}

func pendingRequests(t *testing.T, requests *platformadminhelpertest.Fake) int {
	t.Helper()
	items, err := requests.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	return len(items)
}

func TestMountAuth_SignupWithM2M(t *testing.T) {
	h := apipublicahelpertest.New(t)
	m2m := &m2mFake{}
	c, requests := signupCara(h.Common(), m2m)

	rec := h.Call(c, "", http.MethodPost, "/api/v1/signup", signupBody)
	wantCode(t, "A7 con M2M, sin token de sesión", rec, http.StatusAccepted)
	var body map[string]string
	wantJSON(t, "A7", rec, &body)
	if body["message"] != "Listo. Entra con tu correo y tu clave." {
		t.Errorf("A7: cuerpo %v, quiero el mensaje constante del alta", body)
	}
	if got := pendingRequests(t, requests); got != 1 {
		t.Errorf("A7: %d solicitudes pendientes sembradas, quiero 1", got)
	}
	if m2m.last() != "ReplaceUserSystems "+signupUserID+" [wapp.bff]" {
		t.Errorf("A7: última llamada a identity %q, quiero la concesión de wapp.bff", m2m.last())
	}
}

// TestMountAuth_SignupOwnLimiter: una alta por minuto por IP con ráfaga de 5, en un limitador
// PROPIO de cada MountAuth. Las cinco primeras pasan el limitador (llegan a la validación del
// cuerpo: 400), la sexta es 429. No espera: el limitador repone una ficha por minuto y el test
// dura milisegundos.
func TestMountAuth_SignupOwnLimiter(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, _ := signupCara(h.Common(), &m2mFake{})
	for i := 1; i <= 5; i++ {
		wantCode(t, "A7 dentro de la ráfaga", h.Call(c, "", http.MethodPost, "/api/v1/signup", `{}`), http.StatusBadRequest)
	}
	rec := h.Call(c, "", http.MethodPost, "/api/v1/signup", `{}`)
	wantCode(t, "A7, sexta alta seguida desde la misma IP", rec, http.StatusTooManyRequests)
	if got := rec.Body.String(); got != "demasiadas solicitudes desde esta IP\n" {
		t.Errorf("A7 429: cuerpo %q, quiero el texto literal", got)
	}

	other, _ := signupCara(h.Common(), &m2mFake{})
	wantCode(t, "A7 en otra cara (otro limitador)", h.Call(other, "", http.MethodPost, "/api/v1/signup", `{}`), http.StatusBadRequest)
}

func TestMountAuth_SignupWithoutM2MIsFixed503(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, requests := signupCara(h.Common(), nil)

	for i, body := range []string{signupBody, `{}`, "no es json", "", signupBody, signupBody, signupBody} {
		rec := h.Call(c, "", http.MethodPost, "/api/v1/signup", body)
		wantCode(t, "A7 sin M2M", rec, http.StatusServiceUnavailable)
		if got := rec.Body.String(); got != "registro no disponible\n" {
			t.Errorf("A7 sin M2M (petición %d): cuerpo %q, quiero \"registro no disponible\\n\"", i, got)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("A7 sin M2M: Content-Type %q, quiero el texto plano de http.Error", ct)
		}
	}
	if got := pendingRequests(t, requests); got != 0 {
		t.Errorf("A7 sin M2M sembró %d solicitudes, quiero 0", got)
	}

	warns := 0
	for _, e := range h.Log().Entries() {
		if e.Level == "warn" && e.Msg == signupWarnNoM2M {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("A7 sin M2M: %d avisos al montar con el texto literal, quiero 1 (%+v)", warns, h.Log().Entries())
	}
}

func TestMountAuth_SignupWithoutM2MAndNilLogDoesNotPanic(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c, _ := signupCara(apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}, nil)
	wantCode(t, "A7 sin M2M ni logger", h.Call(c, "", http.MethodPost, "/api/v1/signup", signupBody), http.StatusServiceUnavailable)
}
