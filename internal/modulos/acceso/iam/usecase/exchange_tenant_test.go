package usecase

// Parte de exchange_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): membresías y empresa activa (R-U3…R-U6).

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// ---------------------------------------------------------------------------
// R-U3 · cero membresías
// ---------------------------------------------------------------------------

func TestExchange_NoMembershipYieldsTenantlessToken(t *testing.T) {
	f := newExchangeFixture(t)
	nobody := uuid.NewString()
	res, err := f.exchange(t, f.svc, nobody)
	if err != nil {
		t.Fatalf("Exchange: %v (cero membresías no es un error, D-056.12)", err)
	}
	if res.Context.TenantID != "" || res.Context.UserID != nobody {
		t.Fatalf("contexto = %+v; quiere sin tenant y sujeto %q", res.Context, nobody)
	}
	claims := f.claims(t, res.ContextToken)
	if claims.TenantID != "" || len(claims.Roles) != 0 || len(claims.Grants.Allow) != 0 || len(claims.Grants.Deny) != 0 {
		t.Fatalf("claims = %+v; quiere token sin empresa, sin roles y sin grants", claims)
	}
	if identityrbac.EvaluateGrants(claims.Grants, "flows.read") {
		t.Error("un token sin empresa evaluó allow: el estado de espera no está cerrado")
	}
}

// Tener permisos no es pertenecer: un rol asignado sin membresía no llega al token.
func TestExchange_NoMembershipDoesNotInheritRoleGrants(t *testing.T) {
	f := newExchangeFixture(t)
	orphan := uuid.NewString()
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "no-membership"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})
	// Ámbito global a propósito: es lo que RolesOfUser resolvería incluso sin tenant.
	f.store.Roles.SeedAssignment(orphan, role.ID, nil)

	res, err := f.exchange(t, f.svc, orphan)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims := f.claims(t, res.ContextToken)
	if claims.TenantID != "" || len(claims.Roles) != 0 || len(claims.Grants.Allow) != 0 {
		t.Fatalf("claims = %+v; un sujeto sin membresía no hereda los grants de sus roles", claims)
	}
}

// ---------------------------------------------------------------------------
// R-U4 · una membresía
// ---------------------------------------------------------------------------

func TestExchange_SingleMembershipTokenIsUnchanged(t *testing.T) {
	f := newExchangeFixture(t)
	token, identityExp := f.identityToken(t, f.userID, SystemWappBFF, 15*time.Minute)
	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	c := f.claims(t, res.ContextToken)
	switch {
	case c.TenantID != testTenant || c.UserID != f.userID || c.Subject != f.userID:
		t.Errorf("identidad = %q/%q/%q; quiere %q y %q", c.TenantID, c.UserID, c.Subject, testTenant, f.userID)
	case !slices.Equal(c.Roles, []string{"operator"}):
		t.Errorf("roles = %v; quiere [operator]", c.Roles)
	case len(c.Grants.Allow) != 2 || len(c.Grants.Deny) != 0:
		t.Errorf("grants = %+v; quiere los 2 allow del rol operator y ningún deny", c.Grants)
	case !slices.Equal(res.Context.Roles, []string{"operator"}) || res.Context.TenantID != testTenant:
		t.Errorf("contexto devuelto = %+v", res.Context)
	}
	checkRegisteredClaims(t, c, identityExp)
}

// checkRegisteredClaims afirma la parte registrada del token de una membresía: token_use
// "access", iss del emisor, iat/nbf/exp presentes y exp no posterior al del Identity Token.
// Separada de TestExchange_SingleMembershipTokenIsUnchanged solo por el límite de gocyclo.
func checkRegisteredClaims(t *testing.T, c *sharedjwt.Claims, identityExp time.Time) {
	t.Helper()
	switch {
	case c.TokenUse != sharedjwt.TokenUseAccess || c.Issuer != testIssuer:
		t.Errorf("token_use/iss = %q/%q; quiere %q/%q", c.TokenUse, c.Issuer, sharedjwt.TokenUseAccess, testIssuer)
	case c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil:
		t.Errorf("iat/nbf/exp = %v/%v/%v; ninguno puede faltar", c.IssuedAt, c.NotBefore, c.ExpiresAt)
	case c.ExpiresAt.Unix() > identityExp.Unix():
		t.Errorf("context.exp=%d > identity.exp=%d", c.ExpiresAt.Unix(), identityExp.Unix())
	}
}

// «Ni se mira» se mide: con una membresía y una activa que apunta a otra parte, cero lecturas.
func TestExchange_SingleMembershipNeverReadsActiveTenant(t *testing.T) {
	f := newExchangeFixture(t)
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), f.userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	spy := &activeTenantSpy{inner: f.store.ActiveTenants}
	res, err := f.exchange(t, f.serviceWith(t, spy), f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if spy.reads != 0 {
		t.Fatalf("el canje leyó la empresa activa %d veces con UNA membresía; quiere 0", spy.reads)
	}
	if res.Context.TenantID != testTenant {
		t.Fatalf("tenant = %q; quiere %q: con una membresía manda la membresía", res.Context.TenantID, testTenant)
	}
}

// ---------------------------------------------------------------------------
// R-U5 · varias membresías
// ---------------------------------------------------------------------------

func TestExchange_SeveralCompaniesWithoutChoiceDoesNotPickForYou(t *testing.T) {
	f := newExchangeFixture(t)
	userID := f.twoCompanies(t)
	res, err := f.exchange(t, f.svc, userID)
	if err != nil {
		t.Fatalf("Exchange: %v (dos membresías no son un error, D-047.14)", err)
	}
	if res.Context.TenantID != "" {
		t.Fatalf("tenant = %q; quiere vacío: sin empresa activa el canje no elige por ti", res.Context.TenantID)
	}
	if c := f.claims(t, res.ContextToken); c.TenantID != "" || len(c.Grants.Allow) != 0 {
		t.Fatalf("claims = %+v; quiere el token sin empresa y sin grants", c)
	}
}

func TestExchange_LiveActiveTenantScopesTheToken(t *testing.T) {
	f := newExchangeFixture(t)
	userID := f.twoCompanies(t)
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	res, err := f.exchange(t, f.svc, userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	c := f.claims(t, res.ContextToken)
	if c.TenantID != testTenantB || !slices.Equal(c.Roles, []string{"only-in-b"}) {
		t.Fatalf("tenant/roles = %q/%v; quiere %q/[only-in-b]", c.TenantID, c.Roles, testTenantB)
	}
	if !identityrbac.EvaluateGrants(c.Grants, "b.read") || identityrbac.EvaluateGrants(c.Grants, "a.read") {
		t.Fatalf("grants = %+v; quiere los de B y ninguno de A", c.Grants)
	}
}

// Guardar una empresa activa no concede nada: se contrasta al leerla.
func TestExchange_ActiveTenantNoLongerTheirsYieldsNoTenant(t *testing.T) {
	f := newExchangeFixture(t)
	ctx := context.Background()
	userID := f.twoCompanies(t)
	f.store.Memberships.Seed(userID, testTenantC)
	if err := f.store.ActiveTenants.SetActiveTenant(ctx, userID, testTenantC); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	// La baja no toca la empresa activa: le quedan DOS, así que el canje sigue en «varias».
	if err := f.store.Memberships.Remove(ctx, userID, testTenantC); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if active, ok, err := f.store.ActiveTenants.ActiveTenantOf(ctx, userID); err != nil || !ok || active != testTenantC {
		t.Fatalf("la empresa activa debía seguir escrita (%q, %v, %v)", active, ok, err)
	}

	res, err := f.exchange(t, f.svc, userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.Context.TenantID != "" {
		t.Fatalf("tenant = %q; quiere vacío: la empresa activa ya no es suya", res.Context.TenantID)
	}
}

// ---------------------------------------------------------------------------
// R-U6 · un fallo de lectura corta
// ---------------------------------------------------------------------------

// TestExchange_ActiveTenantReadFailureAborts es el invariante cruzado R-U6: si la empresa activa
// no se puede leer, el canje CORTA con ese error; no degrada a «token sin empresa».
func TestExchange_ActiveTenantReadFailureAborts(t *testing.T) {
	f := newExchangeFixture(t)
	userID := f.twoCompanies(t)
	broken := errors.New("la base no contesta")
	res, err := f.exchange(t, f.serviceWith(t, brokenActiveTenant{err: broken}), userID)
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v; quiere %v: un fallo de infraestructura no se lee como «no ha elegido»", err, broken)
	}
	if res.ContextToken != "" {
		t.Errorf("con el fallo se emitió un token: %q", res.ContextToken)
	}
}

func TestExchange_MembershipReadFailureAborts(t *testing.T) {
	f := newExchangeFixture(t)
	broken := errors.New("tenant_members no contesta")
	svc, err := NewExchangeService(f.verifier, brokenMemberships{MembershipRepo: f.store.Memberships, err: broken},
		f.store.Roles, f.store.Grants, f.store.Audit, f.store.ActiveTenants, f.contexts, Config{})
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	if _, err := f.exchange(t, svc, f.userID); !errors.Is(err, broken) {
		t.Fatalf("err = %v; quiere %v", err, broken)
	}
}
