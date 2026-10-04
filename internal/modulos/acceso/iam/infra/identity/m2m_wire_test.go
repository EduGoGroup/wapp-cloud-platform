package iamidentity_test

// Parte de m2m_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): fallos del canje (R-I9), identity inalcanzable y el mapeo de códigos; gemelo de m2m_wire.go.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/identity"
)

// --- Fallos del canje (R-I9) ---

// TestM2M_ExchangeFailuresAreMachineCredential (R-I9): los fallos del canje son de la credencial
// de máquina o de identity, nunca de la persona; sin token no se llama a la ruta de negocio.
func TestM2M_ExchangeFailuresAreMachineCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "key_rejected", status: http.StatusUnauthorized, want: domain.ErrMachineCredentialInvalid},
		{name: "empty_key_is_machine_credential", status: http.StatusBadRequest, want: domain.ErrMachineCredentialInvalid},
		{name: "rate_limited", status: http.StatusTooManyRequests, want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.tokenStatus, f.tokenCode = tt.status, "X" })
			err := ensure(f.client(t, newFakeClock()))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, quería %v", err, tt.want)
			}
			if errors.Is(err, domain.ErrInvalidInput) || errors.Is(err, domain.ErrInvalidCredentials) {
				t.Errorf("err = %v: un fallo del canje no es de la persona", err)
			}
			if exchanges, calls := f.snapshot(); exchanges != 1 || len(calls) != 0 {
				t.Errorf("canjes = %d, llamadas = %d; quería 1 y ninguna sin Service Token", exchanges, len(calls))
			}
		})
	}
}

// TestM2M_MalformedExchangeIsAnError (R-I9): un canje sin token o sin vigencia es un error con su
// texto, y no se llama a la ruta de negocio.
func TestM2M_MalformedExchangeIsAnError(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, raw, wantErr string }{
		{name: "without_service_token", raw: `{"status":"ok","expires_in":900}`, wantErr: "iam: identity devolvió un canje sin service token"},
		{name: "access_token_is_not_service_token", raw: `{"access_token":"x","expires_in":900}`, wantErr: "iam: identity devolvió un canje sin service token"},
		{name: "zero_lifetime", raw: `{"service_token":"svc","expires_in":0}`, wantErr: "iam: identity devolvió un service token sin vigencia"},
		{name: "negative_lifetime", raw: `{"service_token":"svc","expires_in":-5}`, wantErr: "iam: identity devolvió un service token sin vigencia"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.tokenRaw = tt.raw })
			err := ensure(f.client(t, newFakeClock()))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, quería «%s»", err, tt.wantErr)
			}
			if _, calls := f.snapshot(); len(calls) != 0 {
				t.Errorf("llamadas = %d, quería ninguna sin Service Token", len(calls))
			}
		})
	}
}

// TestM2M_UnreachableIdentityIsUnavailable (R-I2): un fallo de transporte es
// ErrIdentityUnavailable, también en la ruta pública.
func TestM2M_UnreachableIdentityIsUnavailable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c, err := iamidentity.NewM2M(url, m2mAPIKey, time.Second, iamidentity.WithClock(newFakeClock().Now))
	if err != nil {
		t.Fatalf("NewM2M: %v", err)
	}
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) || errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Errorf("EnsureUser: err = %v, quería ErrIdentityUnavailable", err)
	}
	if _, err := c.Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez"); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("Signup: err = %v, quería ErrIdentityUnavailable", err)
	}
}

// m2mCodeCase es un código de identity y el centinela en que debe traducirse (nil: error opaco).
type m2mCodeCase struct {
	name    string
	status  int
	code    string
	details string
	want    error
}

// runCodeCases lanza cada caso contra un fake nuevo y comprueba la traducción.
func runCodeCases(t *testing.T, cases []m2mCodeCase, call func(c *iamidentity.M2MClient) error) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.respondWith(m2mReply{status: tt.status, code: tt.code, details: tt.details})
			err := call(f.client(t, newFakeClock()))
			if tt.want == nil {
				if err == nil {
					t.Fatal("un código no traducido devolvió nil")
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, quería %v", err, tt.want)
			}
		})
	}
}

// TestM2M_EnsureUser_MapsCodes: 401 (tras el reintento) y 403 son de la credencial de wApp.
func TestM2M_EnsureUser_MapsCodes(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, ensure)
}

// TestM2M_GetUserSystems_HasItsOwnCodeMapping (R-I7): el 403 SYSTEM_ACCESS_DENIED aquí es de la
// credencial, no «esa aplicación no es tuya» como en el PUT.
func TestM2M_GetUserSystems_HasItsOwnCodeMapping(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "not_in_registry", status: http.StatusNotFound, code: "NOT_FOUND", want: domain.ErrNotFound},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "ecosystem_403_is_credential_here", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrMachineCredentialInvalid},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "RATE_LIMITED", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, func(c *iamidentity.M2MClient) error {
		_, err := c.GetUserSystems(context.Background(), m2mUserID)
		return err
	})
}

// TestM2M_ReplaceUserSystems_MapsCodes (R-I6): los DOS 403 se separan por el code.
func TestM2M_ReplaceUserSystems_MapsCodes(t *testing.T) {
	t.Parallel()
	runCodeCases(t, []m2mCodeCase{
		{name: "ecosystem_boundary", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrSystemNotAllowed},
		{name: "forbidden_scope", status: http.StatusForbidden, code: "FORBIDDEN", want: domain.ErrMachineCredentialInvalid},
		{name: "unauthorized_after_retry", status: http.StatusUnauthorized, code: "UNAUTHORIZED", want: domain.ErrMachineCredentialInvalid},
		{name: "user_not_found", status: http.StatusNotFound, code: "NOT_FOUND", want: domain.ErrNotFound},
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, func(c *iamidentity.M2MClient) error {
		_, err := c.ReplaceUserSystems(context.Background(), m2mUserID, []string{"edugo.kmp"})
		return err
	})
}

// TestM2M_Signup_MapsCodes (R-I8): el 400 con details.password es la política de contraseña, y
// el motivo de identity viaja en el texto.
func TestM2M_Signup_MapsCodes(t *testing.T) {
	t.Parallel()
	signup := func(c *iamidentity.M2MClient) error {
		_, err := c.Signup(context.Background(), m2mEmail, "una-frase-de-acceso-larga", "Ana", "Pérez")
		return err
	}
	runCodeCases(t, []m2mCodeCase{
		{name: "email_taken", status: http.StatusConflict, code: "CONFLICT", want: domain.ErrEmailTaken},
		{name: "other_bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", details: `{"email":"invalid shape"}`, want: domain.ErrInvalidInput},
		{name: "rate_limited", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrRateLimited},
		{name: "internal_error", status: http.StatusInternalServerError, code: "INTERNAL", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
		{name: "unmapped_is_opaque", status: http.StatusTeapot, code: "TEAPOT"},
	}, signup)

	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusBadRequest, code: "INVALID_REQUEST", details: `{"password":"must be at least 12 characters long"}`})
	err := signup(f.client(t, newFakeClock()))
	if !errors.Is(err, domain.ErrPasswordPolicy) || errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quería ErrPasswordPolicy (y no ErrInvalidInput)", err)
	}
	if !strings.Contains(err.Error(), "must be at least 12 characters long") {
		t.Errorf("err = %q, quería el motivo de identity en el texto", err.Error())
	}
}

// TestM2M_BusinessCallsPresentBearerScheme (R-I4): la ruta de negocio recibe el Service Token con
// el esquema Bearer, literal. El fake recorta el prefijo para leer el token, así que sin esta
// afirmación una cabecera sin esquema pasaría por buena (mutante vivo de F2-05).
func TestM2M_BusinessCallsPresentBearerScheme(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	if err := ensure(f.client(t, newFakeClock())); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	_, calls := f.snapshot()
	if len(calls) == 0 {
		t.Fatal("la ruta de negocio no recibió ninguna llamada")
	}
	for _, c := range calls {
		if c.bearer == "" || c.authorization != "Bearer "+c.bearer {
			t.Errorf("Authorization = %q, quería \"Bearer <service_token>\"", c.authorization)
		}
	}
}
