//go:build pendiente

package iamhttp

// Una aserción por promesa de R-H3, R-H5 y R-H6 (auth.go), con dobles de los puertos: el
// transporte no decide desenlaces, solo los traduce.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// fakeVerifier devuelve siempre el mismo resultado y guarda el token recibido.
type fakeVerifier struct {
	res      in.VerifyResult
	err      error
	received string
	calls    int
}

var _ in.TokenVerifier = (*fakeVerifier)(nil)

func (v *fakeVerifier) Verify(_ context.Context, token string) (in.VerifyResult, error) {
	v.calls++
	v.received = token
	return v.res, v.err
}

// fakeExchanger devuelve siempre el mismo resultado y guarda la entrada recibida.
type fakeExchanger struct {
	res      in.ExchangeResult
	err      error
	received in.ExchangeInput
	calls    int
}

var _ in.Exchanger = (*fakeExchanger)(nil)

func (e *fakeExchanger) Exchange(_ context.Context, input in.ExchangeInput) (in.ExchangeResult, error) {
	e.calls++
	e.received = input
	return e.res, e.err
}

// authMux monta Register sobre un mux nuevo. exchange nil ⇒ modo dual apagado.
func authMux(v in.TokenVerifier, e in.Exchanger) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, v, e, nil)
	return mux
}

// R-H3: el IAM viejo no está en el cable (prueba por AUSENCIA: 404, no 401).
func TestRegister_OldIAMRoutesAreGone(t *testing.T) {
	mux := authMux(&fakeVerifier{}, &fakeExchanger{})
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout", "/api/v1/auth/token"} {
		t.Run(path, func(t *testing.T) {
			if rec := serve(t, mux, http.MethodPost, path, `{"x":"y"}`); rec.Code != http.StatusNotFound {
				t.Errorf("%s: código = %d; se esperaba 404 (la ruta debía DESAPARECER, no rechazar)", path, rec.Code)
			}
		})
	}
}

// Register monta verify y exchange (por path pelado: el 405 lo da el handler).
func TestRegister_MountsVerifyAndExchange(t *testing.T) {
	v, e := &fakeVerifier{res: in.VerifyResult{Valid: false}}, &fakeExchanger{}
	mux := authMux(v, e)
	if rec := serve(t, mux, http.MethodPost, "/api/v1/auth/verify", `{"token":"t"}`); rec.Code != http.StatusOK || v.calls != 1 {
		t.Errorf("verify: código = %d, llamadas = %d; se esperaba 200 y 1", rec.Code, v.calls)
	}
	if rec := serve(t, mux, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"t"}`); rec.Code != http.StatusOK || e.calls != 1 {
		t.Errorf("exchange: código = %d, llamadas = %d; se esperaba 200 y 1", rec.Code, e.calls)
	}
	if rec := serve(t, mux, http.MethodDelete, "/api/v1/auth/exchange", ""); rec.Code != http.StatusMethodNotAllowed ||
		rec.Body.String() != errorBody("método no permitido") {
		t.Errorf("exchange DELETE: %d %s; se esperaba 405 con el cuerpo JSON del handler", rec.Code, rec.Body.String())
	}
}

// R-H3: verify por cuerpo o por Bearer; el cuerpo manda; un JSON roto cae al header.
func TestVerify_TokenSources(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		body    string
		headers []string
		want    string
	}{
		{"post_body", http.MethodPost, `{"token":"from-body"}`, nil, "from-body"},
		{"get_bearer", http.MethodGet, "", []string{"Authorization", "Bearer from-header"}, "from-header"},
		{"body_wins_over_header", http.MethodPost, `{"token":"from-body"}`, []string{"Authorization", "Bearer from-header"}, "from-body"},
		{"broken_body_falls_back_to_header", http.MethodPost, `{`, []string{"Authorization", "Bearer from-header"}, "from-header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &fakeVerifier{}
			rec := serve(t, NewAuthHandler(v, nil, nil).Verify(), c.method, "/api/v1/auth/verify", c.body, c.headers...)
			if rec.Code != http.StatusOK || v.received != c.want {
				t.Errorf("código = %d, token al puerto = %q; se esperaba 200 y %q", rec.Code, v.received, c.want)
			}
		})
	}
}

// R-H3: sin token, 400 con su texto y sin llamar al puerto.
func TestVerify_NoTokenIs400(t *testing.T) {
	v := &fakeVerifier{}
	rec := serve(t, NewAuthHandler(v, nil, nil).Verify(), http.MethodPost, "/api/v1/auth/verify", `{}`)
	want := errorBody("token requerido (cuerpo {token} o header Authorization)")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != want || v.calls != 0 {
		t.Errorf("respuesta = %d %s (llamadas %d); se esperaba 400 %s sin llamar al puerto", rec.Code, rec.Body.String(), v.calls, want)
	}
}

// Verify: método ajeno ⇒ 405; fallo del puerto ⇒ 500 con su texto.
func TestVerify_MethodAndPortFailure(t *testing.T) {
	var h *AuthHandler = NewAuthHandler(&fakeVerifier{err: errors.New("caído")}, nil, nil)
	if rec := serve(t, h.Verify(), http.MethodPut, "/api/v1/auth/verify", `{"token":"t"}`); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: código = %d; se esperaba 405", rec.Code)
	}
	rec := serve(t, h.Verify(), http.MethodPost, "/api/v1/auth/verify", `{"token":"t"}`)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != errorBody("no se pudo validar el token") {
		t.Errorf("respuesta = %d %s; se esperaba 500 %s", rec.Code, rec.Body.String(), errorBody("no se pudo validar el token"))
	}
}

// Verify: inválido ⇒ 200 {"valid":false} y nada más, aunque el puerto traiga claims.
func TestVerify_InvalidIs200WithValidFalseOnly(t *testing.T) {
	v := &fakeVerifier{res: in.VerifyResult{Valid: false, TenantID: tenantA, Subject: "u", Roles: []string{"r"}}}
	rec := serve(t, NewAuthHandler(v, nil, nil).Verify(), http.MethodPost, "/api/v1/auth/verify", `{"token":"t"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"valid":false}` {
		t.Errorf("respuesta = %d %s; se esperaba 200 {\"valid\":false}", rec.Code, rec.Body.String())
	}
}

// Verify: válido ⇒ claims con expires_at RFC 3339 en UTC; instante cero ⇒ sin expires_at.
func TestVerify_ValidSerializesClaims(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	v := &fakeVerifier{res: in.VerifyResult{Valid: true, TenantID: tenantA, Subject: "user-1", Roles: []string{"operator"}, ExpiresAt: exp}}
	rec := serve(t, NewAuthHandler(v, nil, nil).Verify(), http.MethodPost, "/api/v1/auth/verify", `{"token":"t"}`)
	want := `{"valid":true,"tenant_id":"` + tenantA + `","subject":"user-1","roles":["operator"],"expires_at":"2030-01-02T02:04:05Z"}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 200 %s", rec.Code, rec.Body.String(), want)
	}
	v.res.ExpiresAt = time.Time{}
	rec = serve(t, NewAuthHandler(v, nil, nil).Verify(), http.MethodPost, "/api/v1/auth/verify", `{"token":"t"}`)
	if strings.Contains(rec.Body.String(), "expires_at") {
		t.Errorf("instante cero serializado: %s", rec.Body.String())
	}
}

// R-H6: modo dual apagado ⇒ 503 con su texto, aunque el cuerpo esté roto.
func TestExchange_DualModeOffIs503(t *testing.T) {
	rec := serve(t, NewAuthHandler(&fakeVerifier{}, nil, nil).Exchange(), http.MethodPost, "/api/v1/auth/exchange", `{`)
	want := errorBody("modo dual apagado: identity no está configurado en este despliegue")
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 503 %s", rec.Code, rec.Body.String(), want)
	}
}

// R-H6: método, cuerpo y errores del puerto.
func TestExchange_Outcomes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   string
		err    error
		status int
	}{
		{"get_is_405", http.MethodGet, "", nil, http.StatusMethodNotAllowed},
		{"broken_json_is_400", http.MethodPost, `{`, nil, http.StatusBadRequest},
		{"invalid_input_is_400", http.MethodPost, `{"identity_token":""}`, domain.ErrInvalidInput, http.StatusBadRequest},
		{"unacceptable_token_is_401", http.MethodPost, `{"identity_token":"x"}`, domain.ErrIdentityTokenInvalid, http.StatusUnauthorized},
		{"expiring_token_is_401", http.MethodPost, `{"identity_token":"x"}`, domain.ErrIdentityTokenExpiring, http.StatusUnauthorized},
		{"not_migrated_is_401", http.MethodPost, `{"identity_token":"x"}`, domain.ErrUserNotMigrated, http.StatusUnauthorized},
		{"identity_down_is_503", http.MethodPost, `{"identity_token":"x"}`, domain.ErrIdentityUnavailable, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, NewAuthHandler(&fakeVerifier{}, &fakeExchanger{err: c.err}, nil).Exchange(), c.method, "/api/v1/auth/exchange", c.body)
			if rec.Code != c.status {
				t.Errorf("código = %d; se esperaba %d (cuerpo %s)", rec.Code, c.status, rec.Body.String())
			}
		})
	}
}

// R-H6: éxito ⇒ forma exacta, Bearer, RFC 3339 UTC y sin refresh_token.
func TestExchange_SuccessShape(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", -7200))
	e := &fakeExchanger{res: in.ExchangeResult{
		ContextToken: "ctx-token", ExpiresAt: exp,
		Context: domain.IdentityContext{TenantID: tenantA, UserID: "user-1", Roles: []string{"operator"}},
	}}
	rec := serve(t, NewAuthHandler(&fakeVerifier{}, e, nil).Exchange(), http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"id-token"}`)
	want := `{"context_token":"ctx-token","token_type":"Bearer","expires_at":"2030-01-02T05:04:05Z",` +
		`"context":{"tenant_id":"` + tenantA + `","user_id":"user-1","roles":["operator"]}}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 200 %s", rec.Code, rec.Body.String(), want)
	}
	if keys := jsonKeys(t, rec.Body.Bytes()); slices.Contains(keys, "refresh_token") {
		t.Errorf("el canje trae refresh_token: el refresh es de identity (%v)", keys)
	}
}

// R-H6: sin empresa (cero, o varias sin elegida) ⇒ 200 con tenant_id vacío; el 409 ya no existe.
func TestExchange_NoTenantIs200WithEmptyTenant(t *testing.T) {
	e := &fakeExchanger{res: in.ExchangeResult{ContextToken: "ctx", ExpiresAt: time.Unix(1, 0), Context: domain.IdentityContext{UserID: "user-1"}}}
	rec := serve(t, NewAuthHandler(&fakeVerifier{}, e, nil).Exchange(), http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"x"}`)
	var out struct {
		Context struct {
			TenantID *string `json:"tenant_id"`
			UserID   string  `json:"user_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("respuesta = %d %s (err %v); se esperaba 200", rec.Code, rec.Body.String(), err)
	}
	if out.Context.TenantID == nil || *out.Context.TenantID != "" || out.Context.UserID != "user-1" {
		t.Errorf("contexto = %s; se esperaba tenant_id \"\" presente y user_id user-1", rec.Body.String())
	}
}

// R-H5: el token viaja tal cual y un tenant_id del cuerpo no llega al puerto.
func TestExchange_BodyTenantHasNowhereToLand(t *testing.T) {
	e := &fakeExchanger{}
	body := `{"identity_token":"  id-token  ","tenant_id":"` + tenantB + `"}`
	serve(t, NewAuthHandler(&fakeVerifier{}, e, nil).Exchange(), http.MethodPost, "/api/v1/auth/exchange", body)
	if e.received != (in.ExchangeInput{IdentityToken: "  id-token  "}) {
		t.Errorf("el puerto recibió %+v; se esperaba solo el identity_token tal cual", e.received)
	}
}
