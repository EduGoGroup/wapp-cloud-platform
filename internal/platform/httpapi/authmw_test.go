package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
)

const (
	mwSecret = "material-de-firma-hs256-para-el-middleware"
	mwIssuer = "wapp-iam-test"
	mwTenant = "11111111-1111-1111-1111-111111111111"
	mwUser   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

// userToken firma un access token de usuario con los grants dados.
func userToken(t *testing.T, jwt *sharedjwt.JWTManager, grants identityrbac.Grants) string {
	t.Helper()
	tok, _, err := jwt.GenerateToken(mwUser, mwTenant, []string{"operator"}, grants, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok
}

func newMW() (*httpapi.Middleware, *sharedjwt.JWTManager) {
	jwt := sharedjwt.NewJWTManager(mwSecret, mwIssuer)
	return httpapi.NewMiddleware(jwt, nil), jwt
}

// captureIdentity es un handler terminal que guarda la Identity del contexto y
// responde 200 (para verificar la inyección).
func captureIdentity(dst *httpapi.Identity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := httpapi.IdentityFromContext(r.Context()); ok {
			*dst = id
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthenticate_NoToken401(t *testing.T) {
	t.Parallel()
	mw, _ := newMW()
	rec := httptest.NewRecorder()
	mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next no debe ejecutarse sin credencial")
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestAuthenticate_ValidUserToken_InjectsIdentity(t *testing.T) {
	t.Parallel()
	mw, jwt := newMW()
	tok := userToken(t, jwt, identityrbac.Grants{Allow: []string{"flows.*"}})

	var got httpapi.Identity
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mw.Authenticate(captureIdentity(&got)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got.TenantID != mwTenant {
		t.Errorf("tenant del token = %q, want %q", got.TenantID, mwTenant)
	}
	if got.Subject != mwUser {
		t.Errorf("subject = %q, want %q", got.Subject, mwUser)
	}
}

// TestAuthenticate_APIKeyYaNoAutentica fija la retirada del plano M2M (identity
// Plan 003 · design.md Ola 5 §7). Se verifica por AUSENCIA: la cabecera que
// antes bastaba para entrar ya no abre nada, ni siquiera con un valor que el
// middleware viejo habría aceptado. Sin este caso, reintroducir la rama
// X-API-Key no rompería ningún test.
func TestAuthenticate_APIKeyYaNoAutentica(t *testing.T) {
	t.Parallel()
	mw, _ := newMW()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "valid-key")
	rec := httptest.NewRecorder()
	mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next no debe ejecutarse: el plano M2M se retiró")
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (X-API-Key ya no es una credencial de wApp)", rec.Code)
	}
}

func TestRequirePermission_AllowedAndDenied(t *testing.T) {
	t.Parallel()
	mw, jwt := newMW()

	// Usuario con grant flows.* → allow flows.create, deny messages.send.
	tok := userToken(t, jwt, identityrbac.Grants{Allow: []string{"flows.*"}})

	run := func(perm string) int {
		final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		h := mw.Authenticate(mw.RequirePermission(perm)(final))
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := run("flows.create"); code != http.StatusOK {
		t.Errorf("flows.create: code = %d, want 200 (grant flows.*)", code)
	}
	if code := run("messages.send"); code != http.StatusForbidden {
		t.Errorf("messages.send: code = %d, want 403 (default DENY)", code)
	}
}

func TestRequirePermission_NoIdentity401(t *testing.T) {
	t.Parallel()
	mw, _ := newMW()
	// Sin Authenticate delante: no hay Identity en el contexto.
	h := mw.RequirePermission("flows.create")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next no debe ejecutarse sin identidad")
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
