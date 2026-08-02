package iamidentity_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
)

const (
	testEmail    = "op@tenant.example"
	testPassword = "una-frase-de-acceso-larga" //nolint:gosec // credencial de mentira de un test
	testSystem   = "wapp.edge"
	testRefresh  = "rft_una-cadena-opaca-de-identity"
)

// fakeIdentity levanta un servidor que imita el contrato REAL de identity-api:
// sus rutas, sus nombres de campo (`identity_token`, `expires_in`) y sus códigos.
// Registra lo que recibe para poder afirmar sobre la petición, no solo sobre la
// respuesta.
type fakeIdentity struct {
	srv *httptest.Server

	lastPath   string
	lastBody   map[string]any
	lastBearer string

	status int
	code   string
	body   string
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	f := &fakeIdentity{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		f.lastBearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.lastBody = nil
		if r.Body != nil {
			var payload map[string]any
			if json.NewDecoder(r.Body).Decode(&payload) == nil {
				f.lastBody = payload
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if f.status >= http.StatusBadRequest {
			w.WriteHeader(f.status)
			if _, err := w.Write([]byte(`{"error":"x","code":"` + f.code + `"}`)); err != nil {
				t.Errorf("escribiendo el error de prueba: %v", err)
			}
			return
		}
		if f.status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(f.status)
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Errorf("escribiendo la respuesta de prueba: %v", err)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdentity) client(t *testing.T) *iamidentity.Client {
	t.Helper()
	c, err := iamidentity.New(f.srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// sessionJSON es la respuesta de login/refresh de identity, con SUS nombres.
func sessionJSON(identityToken, refreshToken string, expiresIn int) string {
	return `{"status":"ok","session_id":"sess-1","system":"` + testSystem +
		`","identity_token":"` + identityToken + `","refresh_token":"` + refreshToken +
		`","expires_in":` + strconv.Itoa(expiresIn) + `}`
}

func TestLogin_MandaElSystemEnElCuerpoYLeeIdentityToken(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.body = sessionJSON("id.tok.en", testRefresh, 900)

	session, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if f.lastPath != "/api/v1/auth/login" {
		t.Errorf("path = %q, want /api/v1/auth/login", f.lastPath)
	}
	// El `system` viaja en el CUERPO: es lo que somete el login al System Gate de
	// la aplicación correcta.
	if got := f.lastBody["system"]; got != testSystem {
		t.Errorf("system del cuerpo = %v, want %s", got, testSystem)
	}
	if session.IdentityToken != "id.tok.en" || session.RefreshToken != testRefresh {
		t.Errorf("sesión = %+v", session)
	}
	// `expires_in` (segundos) se traduce a instante absoluto.
	if remaining := time.Until(session.ExpiresAt); remaining > 15*time.Minute || remaining < 14*time.Minute {
		t.Errorf("expiración derivada de expires_in fuera de rango: quedan %s", remaining)
	}
}

func TestLogin_TraduceLosCodigosDeIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{name: "credenciales inválidas", status: http.StatusUnauthorized, code: "INVALID_CREDENTIALS", want: domain.ErrInvalidCredentials},
		// El System Gate: la contraseña es correcta, pero esta persona no tiene
		// concedida ESTA aplicación. No es "contraseña incorrecta".
		{name: "system gate denegado", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrUserInactive},
		{name: "petición inválida", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "identity saturado", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrIdentityUnavailable},
		{name: "identity indispuesto", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeIdentity(t)
			f.status, f.code = tt.status, tt.code
			_, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRefresh_NoMandaElSystem(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.body = sessionJSON("id.tok.en2", "rft_rotado", 900)

	session, err := f.client(t).Refresh(context.Background(), testRefresh)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if f.lastPath != "/api/v1/auth/refresh" {
		t.Errorf("path = %q, want /api/v1/auth/refresh", f.lastPath)
	}
	// La aplicación sale de la fila de la sesión en identity: mandarla desde aquí
	// sortearía el System Gate.
	if _, present := f.lastBody["system"]; present {
		t.Error("el refresh no debe mandar `system`")
	}
	if session.RefreshToken != "rft_rotado" {
		t.Errorf("el refresh no se rotó: %+v", session)
	}
}

func TestRefresh_UnRefreshQuemadoNoEsCredencialInvalida(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.status, f.code = http.StatusUnauthorized, "INVALID_REFRESH_TOKEN"

	_, err := f.client(t).Refresh(context.Background(), testRefresh)
	if !errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("err = %v, want ErrRefreshInvalid", err)
	}
}

func TestLogout_EsIdempotente(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.status = http.StatusNoContent

	if err := f.client(t).Logout(context.Background(), testRefresh); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if f.lastPath != "/api/v1/auth/logout" {
		t.Errorf("path = %q, want /api/v1/auth/logout", f.lastPath)
	}
	if got := f.lastBody["refresh_token"]; got != testRefresh {
		t.Errorf("refresh_token del cuerpo = %v", got)
	}
}

func TestLogoutAll_PresentaElIdentityTokenComoPortador(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.body = `{"status":"ok","token_version":2,"revoked_sessions":3}`

	if err := f.client(t).LogoutAll(context.Background(), "id.tok.en"); err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	if f.lastPath != "/api/v1/auth/logout-all" {
		t.Errorf("path = %q, want /api/v1/auth/logout-all", f.lastPath)
	}
	// El titular sale del token, NUNCA de un cuerpo: es lo que evita que el proto
	// del Edge tenga que transportar un user_id.
	if f.lastBearer != "id.tok.en" {
		t.Errorf("bearer = %q, want el identity token", f.lastBearer)
	}
	if len(f.lastBody) != 0 {
		t.Errorf("logout-all no lleva cuerpo, got %v", f.lastBody)
	}
}

func TestClient_IdentityInalcanzableNoEsCredencialRechazada(t *testing.T) {
	t.Parallel()
	// Servidor cerrado antes de usarlo: la conexión falla en el transporte.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c, err := iamidentity.New(url, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Login(context.Background(), testEmail, testPassword, testSystem); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, want ErrIdentityUnavailable", err)
	}
}

func TestNew_ExigeUnaURLUsable(t *testing.T) {
	t.Parallel()
	for _, url := range []string{"", "  ", "localhost:8200", "ftp://identity"} {
		if _, err := iamidentity.New(url, time.Second); err == nil {
			t.Errorf("New(%q) debería fallar: no hay identity por defecto al que mandar contraseñas", url)
		}
	}
	// La barra final se normaliza (no debe producir //api/v1/...).
	if _, err := iamidentity.New("http://localhost:8200/", time.Second); err != nil {
		t.Errorf("New con barra final: %v", err)
	}
}
