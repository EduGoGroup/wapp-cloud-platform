package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-shared/auth"
)

const testAudience = "wapp-public-api"

func m2mFixture(t *testing.T) (*usecase.M2MService, *usecase.APIKeyService) {
	t.Helper()
	store := memory.NewStore()
	svcJWT := auth.NewServiceJWTManager(testSigningKey, testIssuer, testAudience)
	m2m, err := usecase.NewM2MService(store.APIKeys, svcJWT, usecase.Config{})
	if err != nil {
		t.Fatalf("NewM2MService: %v", err)
	}
	keys, err := usecase.NewAPIKeyService(store.APIKeys)
	if err != nil {
		t.Fatalf("NewAPIKeyService: %v", err)
	}
	return m2m, keys
}

func TestAPIKey_IssueAuthenticateAndScope(t *testing.T) {
	t.Parallel()
	m2m, keys := m2mFixture(t)
	ctx := context.Background()

	issued, err := keys.IssueAPIKey(ctx, in.IssueAPIKeyInput{
		TenantID: testTenant, ClientID: "acme", Scopes: []string{"messages.send", "flows.*"},
	})
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if issued.Secret == "" {
		t.Fatal("el secreto debe devolverse una vez")
	}
	if issued.APIKey.KeyHash == issued.Secret {
		t.Fatal("en la entidad solo debe vivir el hash, no el secreto")
	}

	// Autenticar con el secreto en claro.
	ident, err := m2m.AuthenticateAPIKey(ctx, issued.Secret)
	if err != nil {
		t.Fatalf("AuthenticateAPIKey: %v", err)
	}
	if ident.TenantID != testTenant || ident.ClientID != "acme" {
		t.Fatalf("identidad inesperada: %+v", ident)
	}

	// Autorización por scope (glob).
	if !m2m.AuthorizeScope(ident.Scopes, "messages.send") {
		t.Error("messages.send debía estar permitido")
	}
	if !m2m.AuthorizeScope(ident.Scopes, "flows.create") {
		t.Error("flows.create debía estar permitido por flows.*")
	}
	if m2m.AuthorizeScope(ident.Scopes, "leases.revoke") {
		t.Error("leases.revoke NO debía estar permitido")
	}

	// Secreto inválido → ErrAPIKeyInvalid.
	if _, err := m2m.AuthenticateAPIKey(ctx, "secreto-que-no-existe"); !errors.Is(err, domain.ErrAPIKeyInvalid) {
		t.Fatalf("se esperaba ErrAPIKeyInvalid, got %v", err)
	}
}

func TestServiceToken_IssueAndVerify(t *testing.T) {
	t.Parallel()
	m2m, _ := m2mFixture(t)
	ctx := context.Background()

	tok, err := m2m.IssueServiceToken(ctx, in.IssueServiceTokenInput{
		ClientID: "acme", TenantID: testTenant, Scopes: []string{"messages.send"},
	})
	if err != nil {
		t.Fatalf("IssueServiceToken: %v", err)
	}
	ident, err := m2m.VerifyServiceToken(ctx, tok.Token)
	if err != nil {
		t.Fatalf("VerifyServiceToken: %v", err)
	}
	if ident.TenantID != testTenant || ident.ClientID != "acme" {
		t.Fatalf("identidad de service token inesperada: %+v", ident)
	}
	if !m2m.AuthorizeScope(ident.Scopes, "messages.send") {
		t.Error("scope messages.send esperado en el service token")
	}
}
