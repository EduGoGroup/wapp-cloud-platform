package iamidentity_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/identity"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// El test es EXTERNO (package iamidentity_test): prueba el cliente por su contrato, contra un
// httptest.Server que imita identity-api. Sin red real.

var _ out.IdentityClient = (*iamidentity.Client)(nil)

const (
	testEmail    = "op@tenant.example"
	testPassword = "una-frase-de-acceso-larga" //nolint:gosec // credencial de mentira de un test
	testSystem   = "wapp.edge"
	testRefresh  = "rft_una-cadena-opaca-de-identity"
)

// personRequest es lo que el fake de identity vio de UNA petición.
type personRequest struct {
	method string
	path   string
	bearer string
	// rawBody es el cuerpo tal cual llegó ("" si no hubo).
	rawBody string
	body    map[string]any
}

// fakeIdentity imita el contrato REAL de identity-api para personas: sus rutas, sus nombres de
// campo (`identity_token`, `expires_in`) y sus códigos. Registra cada petición para poder afirmar
// sobre lo que salió al cable, no solo sobre la respuesta.
type fakeIdentity struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []personRequest
	status   int
	code     string
	body     string
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	f := &fakeIdentity{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("leyendo el cuerpo de prueba: %v", err)
		}
		req := personRequest{
			method:  r.Method,
			path:    r.URL.Path,
			bearer:  strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			rawBody: string(raw),
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req.body); err != nil {
				t.Errorf("el cliente mandó un cuerpo que no es JSON: %q", raw)
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		status, code, body := f.status, f.code, f.body
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case status >= http.StatusBadRequest:
			w.WriteHeader(status)
			writeBody(t, w, `{"error":"x","code":"`+code+`"}`)
		case status == http.StatusNoContent:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(status)
			writeBody(t, w, body)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// writeBody escribe el cuerpo de prueba y marca el test si no pudo.
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("escribiendo la respuesta de prueba: %v", err)
	}
}

func (f *fakeIdentity) respond(status int, code, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.code, f.body = status, code, body
}

func (f *fakeIdentity) seen() []personRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]personRequest(nil), f.requests...)
}

// last devuelve la única petición que se esperaba, o corta el test.
func (f *fakeIdentity) last(t *testing.T) personRequest {
	t.Helper()
	reqs := f.seen()
	if len(reqs) != 1 {
		t.Fatalf("peticiones a identity = %d, quería exactamente 1", len(reqs))
	}
	return reqs[0]
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

// TestLogin_SendsSystemInBodyAndReadsIdentityToken (R-I1): el `system` viaja en el cuerpo, se
// lee `identity_token` (no `access_token`) y `expires_in` se vuelve instante absoluto.
func TestLogin_SendsSystemInBodyAndReadsIdentityToken(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusOK, "", sessionJSON("id.tok.en", testRefresh, 900))

	before := time.Now()
	session, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	req := f.last(t)
	if req.method != http.MethodPost || req.path != "/api/v1/auth/login" {
		t.Errorf("petición = %s %s, quería POST /api/v1/auth/login", req.method, req.path)
	}
	if req.body["system"] != testSystem || req.body["email"] != testEmail || req.body["password"] != testPassword {
		t.Errorf("cuerpo del login = %v, quería email, password y system=%s", req.body, testSystem)
	}
	if session.SessionID != "sess-1" || session.IdentityToken != "id.tok.en" || session.RefreshToken != testRefresh {
		t.Errorf("sesión = %+v, no es la que devolvió identity", session)
	}
	lo, hi := before.Add(900*time.Second), time.Now().Add(900*time.Second)
	if session.ExpiresAt.Before(lo) || session.ExpiresAt.After(hi) {
		t.Errorf("ExpiresAt = %v, quería ahora + 900 s (entre %v y %v)", session.ExpiresAt, lo, hi)
	}
}

// TestLogin_SessionWithoutTokensIsAnError: una respuesta 200 sin identity_token o sin
// refresh_token es un error con su texto, nunca una sesión a medias.
func TestLogin_SessionWithoutTokensIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"without_identity_token": sessionJSON("", testRefresh, 900),
		"without_refresh_token":  sessionJSON("id.tok.en", "", 900),
		"access_token_is_not_identity_token": `{"access_token":"id.tok.en","refresh_token":"` +
			testRefresh + `","expires_in":900}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeIdentity(t)
			f.respond(http.StatusOK, "", body)
			_, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
			if err == nil || err.Error() != "iam: identity devolvió una sesión sin tokens" {
				t.Fatalf("err = %v, quería «iam: identity devolvió una sesión sin tokens»", err)
			}
		})
	}
}

// TestLogin_MapsIdentityCodes: cada código de identity sale como su centinela del dominio. El
// 403 del System Gate NO es «contraseña incorrecta».
func TestLogin_MapsIdentityCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{name: "invalid_credentials", status: http.StatusUnauthorized, code: "INVALID_CREDENTIALS", want: domain.ErrInvalidCredentials},
		{name: "system_gate_denied_is_inactive", status: http.StatusForbidden, code: "SYSTEM_ACCESS_DENIED", want: domain.ErrUserInactive},
		{name: "bad_request", status: http.StatusBadRequest, code: "INVALID_REQUEST", want: domain.ErrInvalidInput},
		{name: "rate_limited_is_unavailable", status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS", want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeIdentity(t)
			f.respond(tt.status, tt.code, "")
			_, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, quería %v", err, tt.want)
			}
		})
	}
}

// TestClient_UnmappedStatusIsAnOpaqueError: un código sin traducción sube como error (nunca
// nil) y no se confunde con ninguno de los centinelas de credencial.
func TestClient_UnmappedStatusIsAnOpaqueError(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusInternalServerError, "INTERNAL", "")
	_, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
	if err == nil {
		t.Fatal("un 500 no traducido devolvió nil")
	}
	for _, sentinel := range []error{domain.ErrInvalidCredentials, domain.ErrUserInactive, domain.ErrRefreshInvalid} {
		if errors.Is(err, sentinel) {
			t.Errorf("un 500 no traducido salió como %v", sentinel)
		}
	}
}

// TestClient_UnreadableSuccessBodyIsAnError: un 200 con un cuerpo que no es JSON es un error con
// su prefijo, no una sesión vacía.
func TestClient_UnreadableSuccessBodyIsAnError(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusOK, "", "esto no es json")
	_, err := f.client(t).Refresh(context.Background(), testRefresh)
	if err == nil || !strings.HasPrefix(err.Error(), "iam: respuesta de identity ilegible: ") {
		t.Fatalf("err = %v, quería «iam: respuesta de identity ilegible: …»", err)
	}
}

// TestRefresh_DoesNotSendSystem (R-I1): el refresh manda solo el refresh_token y devuelve la
// sesión rotada.
func TestRefresh_DoesNotSendSystem(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusOK, "", sessionJSON("id.tok.en2", "rft_rotado", 900))

	session, err := f.client(t).Refresh(context.Background(), testRefresh)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	req := f.last(t)
	if req.method != http.MethodPost || req.path != "/api/v1/auth/refresh" {
		t.Errorf("petición = %s %s, quería POST /api/v1/auth/refresh", req.method, req.path)
	}
	if _, present := req.body["system"]; present {
		t.Error("el refresh mandó `system`: sortearía el System Gate")
	}
	if req.body["refresh_token"] != testRefresh {
		t.Errorf("refresh_token del cuerpo = %v, quería %s", req.body["refresh_token"], testRefresh)
	}
	if session.IdentityToken != "id.tok.en2" || session.RefreshToken != "rft_rotado" {
		t.Errorf("sesión = %+v, quería la rotada", session)
	}
}

// TestRefresh_BurnedRefreshIsNotInvalidCredentials (R-I1): el 401 de un refresh habla del
// refresh, no de la contraseña. Vale también para logout y logout-all, que comparten mapeo.
func TestRefresh_BurnedRefreshIsNotInvalidCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "unauthorized_is_refresh_invalid", status: http.StatusUnauthorized, want: domain.ErrRefreshInvalid},
		{name: "bad_request", status: http.StatusBadRequest, want: domain.ErrInvalidInput},
		{name: "rate_limited_is_unavailable", status: http.StatusTooManyRequests, want: domain.ErrIdentityUnavailable},
		{name: "service_unavailable", status: http.StatusServiceUnavailable, want: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeIdentity(t)
			f.respond(tt.status, "INVALID_REFRESH_TOKEN", "")
			c := f.client(t)
			_, err := c.Refresh(context.Background(), testRefresh)
			if !errors.Is(err, tt.want) || errors.Is(err, domain.ErrInvalidCredentials) {
				t.Errorf("Refresh: err = %v, quería %v", err, tt.want)
			}
			if err := c.Logout(context.Background(), testRefresh); !errors.Is(err, tt.want) {
				t.Errorf("Logout: err = %v, quería %v", err, tt.want)
			}
			if err := c.LogoutAll(context.Background(), "id.tok.en"); !errors.Is(err, tt.want) {
				t.Errorf("LogoutAll: err = %v, quería %v", err, tt.want)
			}
		})
	}
}

// TestLogout_IsIdempotent (R-I1): identity contesta 204 haya o no algo que revocar, y las dos
// veces es nil.
func TestLogout_IsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusNoContent, "", "")
	c := f.client(t)

	for i := range 2 {
		if err := c.Logout(context.Background(), testRefresh); err != nil {
			t.Fatalf("Logout #%d: %v", i+1, err)
		}
	}
	for _, req := range f.seen() {
		if req.method != http.MethodPost || req.path != "/api/v1/auth/logout" {
			t.Errorf("petición = %s %s, quería POST /api/v1/auth/logout", req.method, req.path)
		}
		if req.body["refresh_token"] != testRefresh {
			t.Errorf("refresh_token del cuerpo = %v, quería %s", req.body["refresh_token"], testRefresh)
		}
	}
}

// TestLogoutAll_PresentsIdentityTokenAsBearer (R-I1): el titular sale del token portador; la
// petición no lleva cuerpo.
func TestLogoutAll_PresentsIdentityTokenAsBearer(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusOK, "", `{"status":"ok","token_version":2,"revoked_sessions":3}`)

	if err := f.client(t).LogoutAll(context.Background(), "id.tok.en"); err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	req := f.last(t)
	if req.method != http.MethodPost || req.path != "/api/v1/auth/logout-all" {
		t.Errorf("petición = %s %s, quería POST /api/v1/auth/logout-all", req.method, req.path)
	}
	if req.bearer != "id.tok.en" {
		t.Errorf("portador = %q, quería el identity token", req.bearer)
	}
	if req.rawBody != "" {
		t.Errorf("logout-all llevó cuerpo %q: el titular sale del token, no de un cuerpo", req.rawBody)
	}
}

// TestClient_EmptyArgumentsNeverReachTheWire: cualquier argumento vacío es ErrInvalidInput y
// no sale ninguna petición.
func TestClient_EmptyArgumentsNeverReachTheWire(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	c := f.client(t)
	ctx := context.Background()

	calls := map[string]func() error{
		"login_without_email":    func() error { _, err := c.Login(ctx, "", testPassword, testSystem); return err },
		"login_without_password": func() error { _, err := c.Login(ctx, testEmail, "", testSystem); return err },
		"login_without_system":   func() error { _, err := c.Login(ctx, testEmail, testPassword, ""); return err },
		"refresh_without_token":  func() error { _, err := c.Refresh(ctx, ""); return err },
		"logout_without_token":   func() error { return c.Logout(ctx, "") },
		"logout_all_without_token": func() error {
			return c.LogoutAll(ctx, "")
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: err = %v, quería ErrInvalidInput", name, err)
		}
	}
	if reqs := f.seen(); len(reqs) != 0 {
		t.Errorf("salieron %d peticiones con argumentos vacíos, quería 0", len(reqs))
	}
}

// TestClient_UnreachableIdentityIsNotRejectedCredential (R-I2): un fallo de transporte es
// ErrIdentityUnavailable, en las cuatro operaciones.
func TestClient_UnreachableIdentityIsNotRejectedCredential(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // cerrado antes de usarlo: la conexión falla en el transporte

	c, err := iamidentity.New(url, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := c.Login(ctx, testEmail, testPassword, testSystem); !errors.Is(err, domain.ErrIdentityUnavailable) ||
		errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login: err = %v, quería ErrIdentityUnavailable", err)
	}
	if _, err := c.Refresh(ctx, testRefresh); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("Refresh: err = %v, quería ErrIdentityUnavailable", err)
	}
	if err := c.Logout(ctx, testRefresh); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("Logout: err = %v, quería ErrIdentityUnavailable", err)
	}
	if err := c.LogoutAll(ctx, "id.tok.en"); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("LogoutAll: err = %v, quería ErrIdentityUnavailable", err)
	}
}

// TestNew_RequiresUsableURL (R-I2): sin URL o con una que no es http(s) no hay cliente, con sus
// textos literales; la barra final se normaliza.
func TestNew_RequiresUsableURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "empty", url: "", wantErr: "iam: la URL de identity-api no puede estar vacía"},
		{name: "blank", url: "  ", wantErr: "iam: la URL de identity-api no puede estar vacía"},
		{name: "without_scheme", url: "localhost:8200", wantErr: `iam: la URL de identity-api debe ser http(s): "localhost:8200"`},
		{name: "other_scheme", url: "ftp://identity", wantErr: `iam: la URL de identity-api debe ser http(s): "ftp://identity"`},
	}
	for _, tt := range tests {
		c, err := iamidentity.New(tt.url, time.Second)
		if err == nil || err.Error() != tt.wantErr || c != nil {
			t.Errorf("%s: New(%q) = %v, %v; quería nil y «%s»", tt.name, tt.url, c, err, tt.wantErr)
		}
	}

	f := newFakeIdentity(t)
	f.respond(http.StatusNoContent, "", "")
	c, err := iamidentity.New(" "+f.srv.URL+"/ ", 0) // timeout 0: el de por defecto
	if err != nil {
		t.Fatalf("New con barra final y espacios: %v", err)
	}
	if err := c.Logout(context.Background(), testRefresh); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if req := f.last(t); req.path != "/api/v1/auth/logout" {
		t.Errorf("path = %q, quería /api/v1/auth/logout (sin doble barra)", req.path)
	}
}
