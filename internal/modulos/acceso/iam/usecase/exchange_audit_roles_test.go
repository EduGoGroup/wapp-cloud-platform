package usecase

// Parte de exchange_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): sesión, auditoría e identity caído (R-U7) y roles y grants (R-U8).

import (
	"context"
	"errors"
	"fmt"
	"testing"

	identityauth "github.com/EduGoGroup/identity-shared/auth"
	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// ---------------------------------------------------------------------------
// R-U7 · sin sesión propia, identity caído no es rechazo, bitácora
// ---------------------------------------------------------------------------

func TestExchange_DoesNotOpenSessionInWapp(t *testing.T) {
	f := newExchangeFixture(t)
	res, err := f.exchange(t, f.svc, f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if c := f.claims(t, res.ContextToken); c.TokenUse != sharedjwt.TokenUseAccess {
		t.Fatalf("token_use = %q; quiere %q", c.TokenUse, sharedjwt.TokenUseAccess)
	}
	events := f.store.Audit.Events()
	if len(events) != 1 {
		t.Fatalf("el canje dejó %d eventos; quiere exactamente 1 (su línea de bitácora)", len(events))
	}
	e := events[0]
	if e.Action != "auth.exchange" || e.Resource != "auth" || e.Result != "ok" || e.Actor != f.userID ||
		e.TenantID == nil || *e.TenantID != testTenant {
		t.Fatalf("evento = %+v; quiere auth.exchange/auth/ok del sujeto en su tenant", e)
	}
}

func TestExchange_TenantlessSuccessIsAuditedWithNullTenant(t *testing.T) {
	f := newExchangeFixture(t)
	if _, err := f.exchange(t, f.svc, uuid.NewString()); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	events := f.store.Audit.Events()
	if len(events) != 1 || events[0].Result != "ok" || events[0].TenantID != nil {
		t.Fatalf("eventos = %+v; quiere uno «ok» con tenant NULL", events)
	}
}

func TestExchange_RejectedTokenLeavesErrorAuditLine(t *testing.T) {
	svc, store := newStubbedExchange(t, &stubIdentityVerifier{err: identityauth.ErrInvalidToken})
	if _, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "x"}); err == nil {
		t.Fatal("un token rechazado no puede canjearse")
	}
	events := store.Audit.Events()
	if len(events) != 1 || events[0].Result != "error" || events[0].Actor != "unknown" || events[0].Action != "auth.exchange" {
		t.Fatalf("eventos = %+v; quiere una línea auth.exchange «error» con actor «unknown»", events)
	}
}

func TestExchange_AuditFailureDoesNotAbort(t *testing.T) {
	f := newExchangeFixture(t)
	svc, err := NewExchangeService(f.verifier, f.store.Memberships, f.store.Roles, f.store.Grants,
		failingAuditRepo{err: errors.New("bitácora caída")}, f.store.ActiveTenants, f.contexts, Config{})
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	if res, err := f.exchange(t, svc, f.userID); err != nil || res.ContextToken == "" {
		t.Fatalf("= %+v, %v; la bitácora es best-effort y no aborta el canje", res, err)
	}
}

func TestExchange_IdentityDownIsNotARejectedCredential(t *testing.T) {
	svc, _ := newStubbedExchange(t, &stubIdentityVerifier{err: fmt.Errorf("%w: sin claves frescas", identityauth.ErrJWKSUnavailable)})
	_, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "token.que.no.se.puede.juzgar"}) //nolint:gosec // token de mentira de un test
	if !errors.Is(err, domain.ErrIdentityUnavailable) || errors.Is(err, domain.ErrIdentityTokenInvalid) {
		t.Fatalf("err = %v; quiere ErrIdentityUnavailable y no ErrIdentityTokenInvalid", err)
	}
}

// ---------------------------------------------------------------------------
// R-U8 · grants efectivos
// ---------------------------------------------------------------------------

func TestExchange_RolesScopedByTenant(t *testing.T) {
	f := newExchangeFixture(t)
	global := f.store.Roles.Seed(domain.Role{Name: "global-role"}, []domain.Grant{{Pattern: "global.read", Effect: domain.EffectAllow}})
	f.store.Roles.SeedAssignment(f.userID, global.ID, nil)
	other := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "other-role"},
		[]domain.Grant{{Pattern: "other.admin", Effect: domain.EffectAllow}})
	f.store.Roles.SeedAssignment(f.userID, other.ID, ptr(testTenantB))

	res, err := f.exchange(t, f.svc, f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	g := f.claims(t, res.ContextToken).Grants
	if !identityrbac.EvaluateGrants(g, "global.read") || !identityrbac.EvaluateGrants(g, "flows.read") {
		t.Errorf("grants = %+v; faltan los del rol global o los del rol de su empresa", g)
	}
	if identityrbac.EvaluateGrants(g, "other.admin") {
		t.Errorf("grants = %+v; se coló el de un rol acotado a OTRA empresa", g)
	}
}

func TestExchange_RoleChainAggregatesParentGrants(t *testing.T) {
	f := newExchangeFixture(t)
	parent := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "base"},
		[]domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}})
	child := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "child", ParentRoleID: ptr(parent.ID)},
		[]domain.Grant{{Pattern: "intakes.read", Effect: domain.EffectAllow}})
	userID := uuid.NewString()
	f.store.Roles.SeedAssignment(userID, child.ID, ptr(testTenant))
	f.store.Memberships.Seed(userID, testTenant)

	res, err := f.exchange(t, f.svc, userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	g := f.claims(t, res.ContextToken).Grants
	if !identityrbac.EvaluateGrants(g, "intakes.read") || !identityrbac.EvaluateGrants(g, "contacts.read") {
		t.Fatalf("grants = %+v; quiere el propio del hijo y el heredado del padre", g)
	}
}

func TestExchange_UserDenyOverridePrecedesAllow(t *testing.T) {
	f := newExchangeFixture(t)
	if err := f.store.Grants.AddUserGrant(context.Background(), f.userID,
		domain.Grant{Pattern: "flows.delete", Effect: domain.EffectDeny}); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}
	res, err := f.exchange(t, f.svc, f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	g := f.claims(t, res.ContextToken).Grants
	if !identityrbac.EvaluateGrants(g, "flows.create") {
		t.Error("flows.create debía seguir permitido por flows.*")
	}
	if identityrbac.EvaluateGrants(g, "flows.delete") {
		t.Error("flows.delete debía estar denegado por el override deny")
	}
}
