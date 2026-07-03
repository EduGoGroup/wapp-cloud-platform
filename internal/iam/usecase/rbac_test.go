package usecase_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-shared/auth"
)

// grantsFromToken hace login y devuelve los grants efectivos embebidos en el
// access token.
func grantsFromToken(t *testing.T, authSvc *usecase.AuthService, email, password string) auth.Grants {
	t.Helper()
	res, err := authSvc.Login(context.Background(), in.LoginInput{Email: email, Password: password})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	jwt := auth.NewJWTManager(testSigningKey, testIssuer)
	claims, err := jwt.ValidateToken(res.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	return claims.Grants
}

// TestEffectiveGrants_RoleChain verifica que la herencia de roles
// (parent_role_id) se agrega: un rol hijo hereda los grants del padre.
func TestEffectiveGrants_RoleChain(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	jwt := auth.NewJWTManager(testSigningKey, testIssuer)
	users := mustUserSvc(t, store)
	authSvc := mustAuthSvc(t, store, jwt)

	parent := store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "base"},
		[]domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}})
	child := store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "child", ParentRoleID: ptr(parent.ID)},
		[]domain.Grant{{Pattern: "messages.send", Effect: domain.EffectAllow}})

	u, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: testTenant, Email: "chain@x.example", Password: testLoginPhrase, RoleIDs: []string{child.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = u

	grants := grantsFromToken(t, authSvc, "chain@x.example", testLoginPhrase)
	if !auth.EvaluateGrants(grants, "messages.send") {
		t.Error("se esperaba el grant propio del hijo")
	}
	if !auth.EvaluateGrants(grants, "contacts.read") {
		t.Error("se esperaba el grant heredado del padre")
	}
}

// TestEffectiveGrants_UserOverrideDeny verifica que un override de usuario con
// effect=deny prevalece sobre un allow del rol (deny-precede-allow del matcher).
func TestEffectiveGrants_UserOverrideDeny(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	jwt := auth.NewJWTManager(testSigningKey, testIssuer)
	users := mustUserSvc(t, store)
	authSvc := mustAuthSvc(t, store, jwt)

	role := store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "wide"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})
	u, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: testTenant, Email: "deny@x.example", Password: testLoginPhrase, RoleIDs: []string{role.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Override deny sobre flows.delete.
	if err := users.AddUserGrant(context.Background(), testTenant, u.ID,
		domain.Grant{Pattern: "flows.delete", Effect: domain.EffectDeny}); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}

	grants := grantsFromToken(t, authSvc, "deny@x.example", testLoginPhrase)
	if !auth.EvaluateGrants(grants, "flows.create") {
		t.Error("flows.create debía seguir permitido por flows.*")
	}
	if auth.EvaluateGrants(grants, "flows.delete") {
		t.Error("flows.delete debía estar denegado por el override deny")
	}
}
