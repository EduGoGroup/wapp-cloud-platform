package usecase_test

import (
	"context"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// rbacFixture arma un canje sobre un store en memoria y devuelve lo necesario
// para sembrar roles/overrides y leer los grants que acaban en el token.
//
// El RBAC se ejercita por el CANJE, no por un login: desde la Ola 5 del Plan 003
// de identity, el canje es el único momento en que wApp resuelve los grants
// efectivos de una persona. Probarlos por otro camino sería probar un camino que
// ya no existe.
type rbacFixture struct {
	exchangeFixture
	store *memory.Store
}

func newRBACFixture(t *testing.T) rbacFixture {
	t.Helper()
	store := memory.NewStore()
	issuerMgr, verifier := newIdentityPair(t)
	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	svc := mustExchangeSvc(t, verifier, store, contexts)
	return rbacFixture{
		exchangeFixture: exchangeFixture{svc: svc, issuer: issuerMgr, contexts: contexts, store: store},
		store:           store,
	}
}

// grantsOf canjea la identidad del sujeto y devuelve los grants efectivos
// embebidos en el Context Token resultante.
func (f rbacFixture) grantsOf(t *testing.T, userID string) identityrbac.Grants {
	t.Helper()
	token, _ := f.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)
	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	return claims.Grants
}

// TestEffectiveGrants_RoleChain verifica que la herencia de roles
// (parent_role_id) se agrega: un rol hijo hereda los grants del padre.
func TestEffectiveGrants_RoleChain(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	parent := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "base"},
		[]domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}})
	child := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "child", ParentRoleID: ptr(parent.ID)},
		[]domain.Grant{{Pattern: "messages.send", Effect: domain.EffectAllow}})

	userID := uuid.NewString()
	if err := f.store.Roles.AssignToUser(context.Background(), userID, child.ID, nil); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}
	f.store.Memberships.Seed(userID, testTenant)

	grants := f.grantsOf(t, userID)
	if !identityrbac.EvaluateGrants(grants, "messages.send") {
		t.Error("se esperaba el grant propio del hijo")
	}
	if !identityrbac.EvaluateGrants(grants, "contacts.read") {
		t.Error("se esperaba el grant heredado del padre")
	}
}

// TestEffectiveGrants_UserOverrideDeny verifica que un override de usuario con
// effect=deny prevalece sobre un allow del rol (deny-precede-allow del matcher).
func TestEffectiveGrants_UserOverrideDeny(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "wide"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})
	userID := uuid.NewString()
	if err := f.store.Roles.AssignToUser(context.Background(), userID, role.ID, nil); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}
	f.store.Memberships.Seed(userID, testTenant)

	// Override deny sobre flows.delete.
	if err := f.store.Grants.AddUserGrant(context.Background(), userID,
		domain.Grant{Pattern: "flows.delete", Effect: domain.EffectDeny}); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}

	grants := f.grantsOf(t, userID)
	if !identityrbac.EvaluateGrants(grants, "flows.create") {
		t.Error("flows.create debía seguir permitido por flows.*")
	}
	if identityrbac.EvaluateGrants(grants, "flows.delete") {
		t.Error("flows.delete debía estar denegado por el override deny")
	}
}
