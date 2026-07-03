package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-shared/auth"
)

const (
	mwSecret   = "material-de-firma-hs256-para-el-middleware"
	mwIssuer   = "wapp-iam-test"
	mwAudience = "wapp-public-api"
	mwTenant   = "11111111-1111-1111-1111-111111111111"
	mwUser     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	mwClient   = "acme-crm"
)

// fakeM2M implementa httpapi.ServiceAuthenticator sin BD: reconoce una única
// api-key ("valid-key") y usa un ServiceJWTManager real para los service tokens.
type fakeM2M struct {
	svcJWT *auth.ServiceJWTManager
}

func (f fakeM2M) AuthenticateAPIKey(_ context.Context, rawKey string) (in.ServiceIdentity, error) {
	if rawKey != "valid-key" {
		return in.ServiceIdentity{}, errors.New("api-key inválida")
	}
	return in.ServiceIdentity{TenantID: mwTenant, ClientID: mwClient, Scopes: []string{"messages.send"}}, nil
}

func (f fakeM2M) VerifyServiceToken(_ context.Context, token string) (in.ServiceIdentity, error) {
	claims, err := f.svcJWT.ValidateServiceToken(token)
	if err != nil {
		return in.ServiceIdentity{}, err
	}
	return in.ServiceIdentity{TenantID: claims.TenantID, ClientID: claims.ClientID, Scopes: claims.Scopes}, nil
}

func (f fakeM2M) AuthorizeScope(scopes []string, required string) bool {
	for _, s := range scopes {
		if auth.PermissionMatches(s, required) {
			return true
		}
	}
	return false
}

// userToken firma un access token de usuario con los grants dados.
func userToken(t *testing.T, jwt *auth.JWTManager, grants auth.Grants) string {
	t.Helper()
	tok, _, err := jwt.GenerateToken(mwUser, mwTenant, []string{"operator"}, grants, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok
}

func newMW() (*httpapi.Middleware, *auth.JWTManager, fakeM2M) {
	jwt := auth.NewJWTManager(mwSecret, mwIssuer)
	svc := fakeM2M{svcJWT: auth.NewServiceJWTManager(mwSecret, mwIssuer, mwAudience)}
	return httpapi.NewMiddleware(jwt, svc, nil), jwt, svc
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
	mw, _, _ := newMW()
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
	mw, jwt, _ := newMW()
	tok := userToken(t, jwt, auth.Grants{Allow: []string{"flows.*"}})

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
	if got.IsService {
		t.Error("un token de usuario no debe marcar IsService")
	}
}

func TestAuthenticate_APIKey_InjectsServiceIdentity(t *testing.T) {
	t.Parallel()
	mw, _, _ := newMW()

	var got httpapi.Identity
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "valid-key")
	rec := httptest.NewRecorder()
	mw.Authenticate(captureIdentity(&got)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !got.IsService || got.Subject != mwClient || got.TenantID != mwTenant {
		t.Fatalf("identidad M2M inesperada: %+v", got)
	}
}

func TestAuthenticate_BadAPIKey401(t *testing.T) {
	t.Parallel()
	mw, _, _ := newMW()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "nope")
	rec := httptest.NewRecorder()
	mw.Authenticate(captureIdentity(new(httpapi.Identity))).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestAuthenticate_ServiceToken_Bearer(t *testing.T) {
	t.Parallel()
	mw, _, svc := newMW()
	tok, _, err := svc.svcJWT.GenerateServiceToken(mwClient, mwTenant, []string{"messages.send"}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateServiceToken: %v", err)
	}
	var got httpapi.Identity
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mw.Authenticate(captureIdentity(&got)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !got.IsService || got.TenantID != mwTenant {
		t.Fatalf("service token no resuelto: %+v", got)
	}
}

func TestRequirePermission_AllowedAndDenied(t *testing.T) {
	t.Parallel()
	mw, jwt, _ := newMW()

	// Usuario con grant flows.* → allow flows.create, deny messages.send.
	tok := userToken(t, jwt, auth.Grants{Allow: []string{"flows.*"}})

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
	mw, _, _ := newMW()
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

func TestRequirePermission_M2MScope(t *testing.T) {
	t.Parallel()
	mw, _, _ := newMW()
	// api-key con scope messages.send: allow messages.send, deny flows.create.
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-API-Key", "valid-key")
		return req
	}
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	recOK := httptest.NewRecorder()
	mw.Authenticate(mw.RequirePermission("messages.send")(final)).ServeHTTP(recOK, newReq())
	if recOK.Code != http.StatusOK {
		t.Errorf("messages.send: code = %d, want 200", recOK.Code)
	}
	recDeny := httptest.NewRecorder()
	mw.Authenticate(mw.RequirePermission("flows.create")(final)).ServeHTTP(recDeny, newReq())
	if recDeny.Code != http.StatusForbidden {
		t.Errorf("flows.create: code = %d, want 403 (fuera de scope)", recDeny.Code)
	}
}
