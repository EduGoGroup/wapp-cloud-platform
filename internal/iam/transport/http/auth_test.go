package iamhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-shared/auth"
)

const (
	tSecret      = "material-de-firma-hs256-para-los-handlers"
	tIssuer      = "wapp-iam-test"
	tAudience    = "wapp-public-api"
	tTenant      = "11111111-1111-1111-1111-111111111111"
	tEmail       = "op@tenant.example"
	tLoginPhrase = "una-frase-de-acceso-larga"
)

// harness arma el mux con los handlers reales sobre usecases con store en
// memoria (tokens reales), y siembra un usuario activo con rol operator.
type harness struct {
	mux   *http.ServeMux
	store *memory.Store
}

func newHarness(t *testing.T) harness {
	t.Helper()
	store := memory.NewStore()
	jwt := auth.NewJWTManager(tSecret, tIssuer)
	svcJWT := auth.NewServiceJWTManager(tSecret, tIssuer, tAudience)

	authSvc, err := usecase.NewAuthService(store.Users, store.Roles, store.Grants, store.Refresh, store.Audit, jwt, usecase.Config{})
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	m2mSvc, err := usecase.NewM2MService(store.APIKeys, svcJWT, usecase.Config{})
	if err != nil {
		t.Fatalf("NewM2MService: %v", err)
	}
	users, err := usecase.NewUserService(store.Users, store.Roles, store.Grants)
	if err != nil {
		t.Fatalf("NewUserService: %v", err)
	}

	role := store.Roles.Seed(domain.Role{TenantID: ptr(tTenant), Name: "operator"}, []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "messages.send", Effect: domain.EffectAllow},
	})
	if _, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: tTenant, Email: tEmail, Password: tLoginPhrase, RoleIDs: []string{role.ID},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	mux := http.NewServeMux()
	iamhttp.Register(mux, authSvc, m2mSvc, nil)
	return harness{mux: mux, store: store}
}

func ptr(s string) *string { return &s }

// mustJSON deserializa data en v y falla el test si el JSON no parsea.
func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, string(data))
	}
}

// do ejecuta una request JSON contra el mux y devuelve el recorder.
func (h harness) do(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, r)
	return rec
}

func TestLogin_OK(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"`+tLoginPhrase+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Context      struct {
			TenantID string `json:"tenant_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		t.Fatal("tokens vacíos")
	}
	if out.TokenType != "Bearer" || out.Context.TenantID != tTenant {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
}

func TestLogin_BadPassword401(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"wrong"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestLogin_BadJSON400(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login", `{not-json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestLogin_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/auth/login", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestRefresh_EmitsNew(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	login := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"`+tLoginPhrase+`"}`, nil)
	var lr struct {
		RefreshToken string `json:"refresh_token"`
	}
	mustJSON(t, login.Body.Bytes(), &lr)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		`{"refresh_token":"`+lr.RefreshToken+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var rr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	mustJSON(t, rec.Body.Bytes(), &rr)
	if rr.AccessToken == "" || rr.RefreshToken == lr.RefreshToken {
		t.Fatal("refresh no rotó / access vacío")
	}
}

func TestRefresh_Invalid401(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"nope"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestLogout_RevokesRefresh(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	login := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"`+tLoginPhrase+`"}`, nil)
	var lr struct {
		RefreshToken string `json:"refresh_token"`
	}
	mustJSON(t, login.Body.Bytes(), &lr)

	logout := h.do(t, http.MethodPost, "/api/v1/auth/logout",
		`{"refresh_token":"`+lr.RefreshToken+`"}`, nil)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout code = %d, want 204", logout.Code)
	}
	// El refresh ya no sirve.
	after := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		`{"refresh_token":"`+lr.RefreshToken+`"}`, nil)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("refresh tras logout code = %d, want 401", after.Code)
	}
}

func TestVerify_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	login := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"`+tLoginPhrase+`"}`, nil)
	var lr struct {
		AccessToken string `json:"access_token"`
	}
	mustJSON(t, login.Body.Bytes(), &lr)

	// Válido (token en el cuerpo).
	rec := h.do(t, http.MethodPost, "/api/v1/auth/verify", `{"token":"`+lr.AccessToken+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify code = %d, want 200", rec.Code)
	}
	var v struct {
		Valid    bool   `json:"valid"`
		TenantID string `json:"tenant_id"`
	}
	mustJSON(t, rec.Body.Bytes(), &v)
	if !v.Valid || v.TenantID != tTenant {
		t.Fatalf("verify válido inesperado: %+v", v)
	}

	// Inválido → 200 con valid=false (no 401).
	bad := h.do(t, http.MethodPost, "/api/v1/auth/verify", `{"token":"not-a-token"}`, nil)
	if bad.Code != http.StatusOK {
		t.Fatalf("verify(inválido) code = %d, want 200", bad.Code)
	}
	var bv struct {
		Valid bool `json:"valid"`
	}
	mustJSON(t, bad.Body.Bytes(), &bv)
	if bv.Valid {
		t.Fatal("verify(inválido) debe ser valid=false")
	}
}

func TestVerify_HeaderBearer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	login := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+tEmail+`","password":"`+tLoginPhrase+`"}`, nil)
	var lr struct {
		AccessToken string `json:"access_token"`
	}
	mustJSON(t, login.Body.Bytes(), &lr)

	rec := h.do(t, http.MethodGet, "/api/v1/auth/verify", "",
		map[string]string{"Authorization": "Bearer " + lr.AccessToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify(header) code = %d, want 200", rec.Code)
	}
}

func TestServiceToken_FromAPIKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Emite una api-key directamente en el store (vía el usecase).
	keys, err := usecase.NewAPIKeyService(h.store.APIKeys)
	if err != nil {
		t.Fatalf("NewAPIKeyService: %v", err)
	}
	issued, err := keys.IssueAPIKey(context.Background(), in.IssueAPIKeyInput{
		TenantID: tTenant, ClientID: "acme", Scopes: []string{"messages.send"},
	})
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	rec := h.do(t, http.MethodPost, "/api/v1/auth/token", "",
		map[string]string{"X-API-Key": issued.Secret})
	if rec.Code != http.StatusOK {
		t.Fatalf("token code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
	}
	mustJSON(t, rec.Body.Bytes(), &out)
	if out.Token == "" || out.TokenType != "Bearer" {
		t.Fatalf("service token inesperado: %+v", out)
	}
}

func TestServiceToken_BadKey401(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/token", "",
		map[string]string{"X-API-Key": "nope"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
