package iamhttp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

const (
	tIdentityIssuer = "identity-core"
	tIdentityKid    = "es256-test"
)

// exchangeHarness monta el mux con el canje CABLEADO (a diferencia de harness,
// que lo deja apagado para ejercitar el 503). El Identity Token se emite y se
// verifica de verdad con identity-shared.
type exchangeHarness struct {
	harness
	identity *identityjwt.Manager
	userID   string
}

func newExchangeHarness(t *testing.T) exchangeHarness {
	t.Helper()
	store := memory.NewStore()
	jwt := sharedjwt.NewJWTManager(tSecret, tIssuer)
	svcJWT := sharedjwt.NewServiceJWTManager(tSecret, tIssuer, tAudience)

	authSvc, err := usecase.NewAuthService(store.Users, store.Roles, store.Grants, store.Refresh, store.Audit, jwt, jwt, usecase.Config{})
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

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave ES256 de prueba: %v", err)
	}
	identityIssuer, err := identityjwt.NewManager(key, tIdentityIssuer, tIdentityKid)
	if err != nil {
		t.Fatalf("NewManager (identity): %v", err)
	}
	verifier, err := identityjwt.NewMultiVerifier(tIdentityIssuer, map[string]*ecdsa.PublicKey{
		tIdentityKid: &key.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier (identity): %v", err)
	}
	exchangeSvc, err := usecase.NewExchangeService(
		verifier, store.Users, store.Memberships, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{},
	)
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}

	role := store.Roles.Seed(domain.Role{TenantID: ptr(tTenant), Name: "operator"}, []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
	})
	u, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: tTenant, Email: tEmail, Password: tLoginPhrase, RoleIDs: []string{role.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	store.Memberships.Seed(u.ID, tTenant)

	mux := http.NewServeMux()
	iamhttp.Register(mux, authSvc, m2mSvc, exchangeSvc, nil)
	return exchangeHarness{
		harness:  harness{mux: mux, store: store},
		identity: identityIssuer,
		userID:   u.ID,
	}
}

// identityToken emite un Identity Token real para el sujeto y la aplicación dados.
func (h exchangeHarness) identityToken(t *testing.T, userID, system string) string {
	t.Helper()
	token, _, err := h.identity.GenerateIdentityToken(identityjwt.IdentityTokenInput{
		UserID:       userID,
		System:       system,
		Email:        tEmail,
		TokenVersion: 1,
		TTL:          15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("GenerateIdentityToken: %v", err)
	}
	return token
}

func TestExchange_OK(t *testing.T) {
	t.Parallel()
	h := newExchangeHarness(t)
	token := h.identityToken(t, h.userID, usecase.SystemWappBFF)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"`+token+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		ContextToken string `json:"context_token"`
		TokenType    string `json:"token_type"`
		ExpiresAt    string `json:"expires_at"`
		Context      struct {
			TenantID string `json:"tenant_id"`
			UserID   string `json:"user_id"`
		} `json:"context"`
	}
	mustJSON(t, rec.Body.Bytes(), &out)
	if out.ContextToken == "" || out.TokenType != "Bearer" {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
	if out.Context.TenantID != tTenant || out.Context.UserID != h.userID {
		t.Errorf("contexto = %+v, want tenant %s / user %s", out.Context, tTenant, h.userID)
	}
	if _, err := time.Parse(time.RFC3339, out.ExpiresAt); err != nil {
		t.Errorf("expires_at no es RFC3339: %q", out.ExpiresAt)
	}

	// El canje NO emite refresh: el refresh es de identity y vive donde vive la
	// sesión. Que el campo ni siquiera aparezca en el cable es parte del contrato.
	var raw map[string]json.RawMessage
	mustJSON(t, rec.Body.Bytes(), &raw)
	if _, present := raw["refresh_token"]; present {
		t.Error("la respuesta del canje trae refresh_token: el refresh es de identity")
	}
}

func TestExchange_ModoDualApagadoResponde503(t *testing.T) {
	t.Parallel()
	// harness (sin canje cableado) = despliegue sin WAPP_IDENTITY_JWKS_URL.
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"loquesea"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestExchange_TokenNoAceptableResponde401(t *testing.T) {
	t.Parallel()
	h := newExchangeHarness(t)
	tests := []struct {
		name  string
		token string
	}{
		{name: "basura", token: "no.es.un.jwt"}, //nolint:gosec // no es una credencial: es justo una cadena que NO es un JWT
		{name: "aplicación de otro ecosistema", token: h.identityToken(t, h.userID, "edugo.kmp")},
		{name: "sujeto sin migrar", token: h.identityToken(t, "99999999-9999-9999-9999-999999999999", usecase.SystemWappBFF)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"`+tt.token+`"}`, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestExchange_VariosTenantsResponde409(t *testing.T) {
	t.Parallel()
	h := newExchangeHarness(t)
	h.store.Memberships.Seed(h.userID, "22222222-2222-2222-2222-222222222222")
	token := h.identityToken(t, h.userID, usecase.SystemWappBFF)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":"`+token+`"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestExchange_MetodoYCuerpo(t *testing.T) {
	t.Parallel()
	h := newExchangeHarness(t)

	if rec := h.do(t, http.MethodGet, "/api/v1/auth/exchange", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: code = %d, want 405", rec.Code)
	}
	if rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON roto: code = %d, want 400", rec.Code)
	}
	if rec := h.do(t, http.MethodPost, "/api/v1/auth/exchange", `{"identity_token":""}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("token vacío: code = %d, want 400", rec.Code)
	}
}
