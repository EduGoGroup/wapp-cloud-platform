package usecase

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	identityauth "github.com/EduGoGroup/identity-shared/auth"
	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// Material del canje compartido con los tests de los demás ficheros del paquete (active_tenant
// lo usa para R-U18). Vive aquí porque exchange es el primero de los medios en pasar a verde.
const (
	// testTenantB es la empresa B de los tests.
	testTenantB = "22222222-2222-2222-2222-222222222222"
	// testTenantC es la tercera empresa: con ella se puede perder una membresía y seguir en
	// la rama de «varias».
	testTenantC = "33333333-3333-3333-3333-333333333333"
	// testEmail es el correo que viaja en los Identity Tokens de prueba.
	testEmail = "op@tenant.example"
	// identityIssuer es el emisor que estampa identity-core en sus tokens.
	identityIssuer = "identity-core"
	// identityKid es el kid del par de claves de prueba.
	identityKid = "es256-test"
)

// ptr devuelve un puntero a s.
func ptr(s string) *string { return &s }

// exchangeFixture arma un ExchangeService sobre un Store en memoria. La verificación del
// Identity Token es REAL: se emite con el emisor de identity-shared y se comprueba con su
// MultiVerifier sobre la clave pública del par generado aquí.
type exchangeFixture struct {
	svc      *ExchangeService
	issuer   *identityjwt.Manager
	verifier IdentityTokenVerifier
	contexts *sharedjwt.JWTManager
	store    *memory.Store
	// userID es un miembro de testTenant con el rol "operator" (flows.*, messages.send).
	userID string
}

func newExchangeFixture(t *testing.T) exchangeFixture {
	t.Helper()
	store := memory.NewStore()
	issuer, verifier := newIdentityPair(t)
	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	userID := seedMember(t, store, testTenant, "operator", []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "messages.send", Effect: domain.EffectAllow},
	})
	f := exchangeFixture{issuer: issuer, verifier: verifier, contexts: contexts, store: store, userID: userID}
	f.svc = f.serviceWith(t, store.ActiveTenants)
	return f
}

// newIdentityPair devuelve el emisor de Identity Tokens de prueba y el verificador que lo
// acepta (mismo par de claves ES256).
func newIdentityPair(t *testing.T) (*identityjwt.Manager, *identityjwt.MultiVerifier) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave ES256 de prueba: %v", err)
	}
	issuer, err := identityjwt.NewManager(key, identityIssuer, identityKid)
	if err != nil {
		t.Fatalf("NewManager (identity): %v", err)
	}
	verifier, err := identityjwt.NewMultiVerifier(identityIssuer, map[string]*ecdsa.PublicKey{identityKid: &key.PublicKey})
	if err != nil {
		t.Fatalf("NewMultiVerifier (identity): %v", err)
	}
	return issuer, verifier
}

// seedMember siembra un sujeto con membresía en tenantID y, si roleName no es vacío, un rol de
// esa empresa asignado en ella. Devuelve su UUID.
func seedMember(t *testing.T, store *memory.Store, tenantID, roleName string, grants []domain.Grant) string {
	t.Helper()
	userID := uuid.NewString()
	if roleName != "" {
		role := store.Roles.Seed(domain.Role{TenantID: ptr(tenantID), Name: roleName}, grants)
		store.Roles.SeedAssignment(userID, role.ID, ptr(tenantID))
	}
	store.Memberships.Seed(userID, tenantID)
	return userID
}

// serviceWith construye el canje del fixture cambiando SOLO el repositorio de empresa activa.
func (f exchangeFixture) serviceWith(t *testing.T, active out.ActiveTenantRepo) *ExchangeService {
	t.Helper()
	svc, err := NewExchangeService(f.verifier, f.store.Memberships, f.store.Roles, f.store.Grants,
		f.store.Audit, active, f.contexts, Config{})
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	return svc
}

// identityToken emite un Identity Token real y devuelve también su expiración.
func (f exchangeFixture) identityToken(t *testing.T, userID, system string, ttl time.Duration) (string, time.Time) {
	t.Helper()
	token, expiresAt, err := f.issuer.GenerateIdentityToken(identityjwt.IdentityTokenInput{
		UserID: userID, System: system, Email: testEmail, TokenVersion: 1, TTL: ttl,
	})
	if err != nil {
		t.Fatalf("GenerateIdentityToken: %v", err)
	}
	return token, expiresAt
}

// exchange canjea un Identity Token de 15 min de SystemWappBFF para userID con svc.
func (f exchangeFixture) exchange(t *testing.T, svc *ExchangeService, userID string) (in.ExchangeResult, error) {
	t.Helper()
	token, _ := f.identityToken(t, userID, SystemWappBFF, 15*time.Minute)
	return svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
}

// claims valida de verdad el Context Token emitido: lo que importa es lo que viaja firmado.
func (f exchangeFixture) claims(t *testing.T, contextToken string) *sharedjwt.Claims {
	t.Helper()
	claims, err := f.contexts.ValidateToken(contextToken)
	if err != nil {
		t.Fatalf("validando el context token emitido: %v", err)
	}
	return claims
}

// twoCompanies siembra una persona con membresía en A y B y un rol distinto ACOTADO a cada una
// (solo-en-A: a.read; solo-en-B: b.read). Devuelve su UUID.
func (f exchangeFixture) twoCompanies(t *testing.T) string {
	t.Helper()
	userID := uuid.NewString()
	roleA := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "only-in-a"},
		[]domain.Grant{{Pattern: "a.read", Effect: domain.EffectAllow}})
	roleB := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "only-in-b"},
		[]domain.Grant{{Pattern: "b.read", Effect: domain.EffectAllow}})
	f.store.Roles.SeedAssignment(userID, roleA.ID, ptr(testTenant))
	f.store.Roles.SeedAssignment(userID, roleB.ID, ptr(testTenantB))
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	return userID
}

// activeTenantSpy envuelve un repositorio y CUENTA las lecturas: afirma que un camino NO
// consulta la empresa activa.
type activeTenantSpy struct {
	inner out.ActiveTenantRepo
	reads int
}

func (s *activeTenantSpy) ActiveTenantOf(ctx context.Context, userID string) (string, bool, error) {
	s.reads++
	return s.inner.ActiveTenantOf(ctx, userID)
}

func (s *activeTenantSpy) SetActiveTenant(ctx context.Context, userID, tenantID string) error {
	return s.inner.SetActiveTenant(ctx, userID, tenantID)
}

// brokenActiveTenant es el repositorio de empresa activa que siempre falla.
type brokenActiveTenant struct{ err error }

func (b brokenActiveTenant) ActiveTenantOf(context.Context, string) (string, bool, error) {
	return "", false, b.err
}
func (b brokenActiveTenant) SetActiveTenant(context.Context, string, string) error { return b.err }

// brokenMemberships falla al leer las membresías de alguien.
type brokenMemberships struct {
	out.MembershipRepo
	err error
}

func (b brokenMemberships) TenantsOfUser(context.Context, string) ([]string, error) {
	return nil, b.err
}

// stubIdentityVerifier es el doble propio del verificador de Identity Tokens: devuelve lo que
// se le diga y anota con qué aplicaciones se le preguntó.
type stubIdentityVerifier struct {
	claims  *identityjwt.Claims
	err     error
	systems []string
}

func (s *stubIdentityVerifier) ValidateIdentityToken(_, expectedSystem string) (*identityjwt.Claims, error) {
	s.systems = append(s.systems, expectedSystem)
	return s.claims, s.err
}

// newStubbedExchange monta un canje sobre un Store vacío con el verificador dado.
func newStubbedExchange(t *testing.T, verifier IdentityTokenVerifier) (*ExchangeService, *memory.Store) {
	t.Helper()
	store := memory.NewStore()
	svc, err := NewExchangeService(verifier, store.Memberships, store.Roles, store.Grants, store.Audit,
		store.ActiveTenants, sharedjwt.NewJWTManager(testSigningKey, testIssuer), Config{})
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	return svc, store
}

// ---------------------------------------------------------------------------
// R-U9 · constructor
// ---------------------------------------------------------------------------

func TestNewExchangeService_RequiresDependencies(t *testing.T) {
	store := memory.NewStore()
	jwt := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	verifier := &stubIdentityVerifier{}
	const (
		noVerifier = "iam: ExchangeService requiere un verificador de Identity Tokens"
		noRepos    = "iam: ExchangeService requiere todos los repositorios"
		noJWT      = "iam: ExchangeService requiere un JWTManager emisor"
	)
	cases := []struct {
		name    string
		build   func() (*ExchangeService, error)
		wantErr string
	}{
		{"nil_verifier", func() (*ExchangeService, error) {
			return NewExchangeService(nil, store.Memberships, store.Roles, store.Grants, store.Audit, store.ActiveTenants, jwt, Config{})
		}, noVerifier},
		{"nil_memberships", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, nil, store.Roles, store.Grants, store.Audit, store.ActiveTenants, jwt, Config{})
		}, noRepos},
		{"nil_roles", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, store.Memberships, nil, store.Grants, store.Audit, store.ActiveTenants, jwt, Config{})
		}, noRepos},
		{"nil_grants", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, store.Memberships, store.Roles, nil, store.Audit, store.ActiveTenants, jwt, Config{})
		}, noRepos},
		{"nil_audit", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, store.Memberships, store.Roles, store.Grants, nil, store.ActiveTenants, jwt, Config{})
		}, noRepos},
		{"nil_active_tenant_is_not_optional", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, store.Memberships, store.Roles, store.Grants, store.Audit, nil, jwt, Config{})
		}, noRepos},
		{"nil_jwt", func() (*ExchangeService, error) {
			return NewExchangeService(verifier, store.Memberships, store.Roles, store.Grants, store.Audit, store.ActiveTenants, nil, Config{})
		}, noJWT},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := c.build()
			if err == nil || svc != nil {
				t.Fatalf("= %v, %v; quiere servicio nil y error", svc, err)
			}
			if err.Error() != c.wantErr {
				t.Errorf("err = %q; quiere el literal %q", err.Error(), c.wantErr)
			}
		})
	}
	if svc, err := NewExchangeService(verifier, store.Memberships, store.Roles, store.Grants, store.Audit,
		store.ActiveTenants, jwt, Config{}); err != nil || svc == nil {
		t.Fatalf("con todo cableado = %v, %v; quiere un servicio", svc, err)
	}
}

// ---------------------------------------------------------------------------
// Entrada y camino feliz
// ---------------------------------------------------------------------------

func TestExchange_EmptyTokenIsInvalidInput(t *testing.T) {
	f := newExchangeFixture(t)
	if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v; quiere ErrInvalidInput", err)
	}
}

// El sujeto sale del `sub` firmado y el tenant de la membresía; el token lleva los grants.
func TestExchange_IssuesContextTokenForIdentity(t *testing.T) {
	f := newExchangeFixture(t)
	res, err := f.exchange(t, f.svc, f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.Context.TenantID != testTenant || res.Context.UserID != f.userID {
		t.Fatalf("contexto = %+v; quiere tenant %q y sujeto %q", res.Context, testTenant, f.userID)
	}
	claims := f.claims(t, res.ContextToken)
	if claims.TenantID != testTenant || claims.UserID != f.userID {
		t.Errorf("claims del context token = %+v", claims)
	}
	if !identityrbac.EvaluateGrants(claims.Grants, "messages.send") {
		t.Error("el context token viaja sin los grants del rol: el RBAC de wApp no se resolvió")
	}
}

// ---------------------------------------------------------------------------
// R-U1 · la visa no dura más que el pasaporte
// ---------------------------------------------------------------------------

func TestExchange_ContextTokenNeverOutlivesIdentityToken(t *testing.T) {
	f := newExchangeFixture(t)
	token, identityExp := f.identityToken(t, f.userID, SystemWappEdge, 90*time.Second)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	exp := f.claims(t, res.ContextToken).ExpiresAt.Time
	if exp.Unix() > identityExp.Unix() {
		t.Fatalf("el context token sobrevive al identity token: %d > %d", exp.Unix(), identityExp.Unix())
	}
	if res.ExpiresAt.Unix() != exp.Unix() {
		t.Errorf("ExpiresAt devuelto (%d) != exp firmado (%d)", res.ExpiresAt.Unix(), exp.Unix())
	}
}

func TestExchange_LongIdentityUsesContextTTL(t *testing.T) {
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, SystemWappBFF, 2*time.Hour)
	before := time.Now()

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	life := f.claims(t, res.ContextToken).ExpiresAt.Sub(before)
	if life > DefaultAccessTTL+time.Second || life < DefaultAccessTTL-time.Minute {
		t.Fatalf("el context token dura %s; quiere ~%s (manda el TTL de contexto)", life, DefaultAccessTTL)
	}
}

func TestExchange_ConfiguredAccessTTLIsHonoured(t *testing.T) {
	f := newExchangeFixture(t)
	svc, err := NewExchangeService(f.verifier, f.store.Memberships, f.store.Roles, f.store.Grants, f.store.Audit,
		f.store.ActiveTenants, f.contexts, Config{AccessTTL: 5 * time.Minute})
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	before := time.Now()
	res, err := f.exchange(t, svc, f.userID)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	life := f.claims(t, res.ContextToken).ExpiresAt.Sub(before)
	if life > 5*time.Minute+time.Second || life < 4*time.Minute {
		t.Fatalf("el context token dura %s; quiere ~5m (Config.AccessTTL)", life)
	}
}

func TestExchange_NearlyExpiredIdentityIsNotExchanged(t *testing.T) {
	f := newExchangeFixture(t)
	// El mínimo que admite identity (1 min): al canjear ya queda menos del mínimo emitible.
	token, _ := f.identityToken(t, f.userID, SystemWappBFF, time.Minute)
	if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token}); !errors.Is(err, domain.ErrIdentityTokenExpiring) {
		t.Fatalf("err = %v; quiere ErrIdentityTokenExpiring", err)
	}
}

// ---------------------------------------------------------------------------
// R-U2 · solo las aplicaciones de wApp
// ---------------------------------------------------------------------------

func TestExchange_AllThreeWappSystemsAreAccepted(t *testing.T) {
	for _, system := range []string{SystemWappBFF, SystemWappEdge, SystemWappPlatform} {
		t.Run(system, func(t *testing.T) {
			f := newExchangeFixture(t)
			token, _ := f.identityToken(t, f.userID, system, 15*time.Minute)
			if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token}); err != nil {
				t.Fatalf("Exchange para %s: %v", system, err)
			}
		})
	}
	if SystemWappBFF != "wapp.bff" || SystemWappEdge != "wapp.edge" || SystemWappPlatform != "wapp.platform" {
		t.Errorf("las claves del catálogo cambiaron: %q %q %q", SystemWappBFF, SystemWappEdge, SystemWappPlatform)
	}
}

func TestExchange_ForeignEcosystemIsRejected(t *testing.T) {
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, "edugo.kmp", 15*time.Minute)
	if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token}); !errors.Is(err, domain.ErrIdentityTokenInvalid) {
		t.Fatalf("err = %v; quiere ErrIdentityTokenInvalid", err)
	}
}

// Un rechazo prueba las tres aplicaciones, en el orden del contrato.
func TestExchange_TriesWappSystemsInOrder(t *testing.T) {
	verifier := &stubIdentityVerifier{err: identityauth.ErrInvalidToken}
	svc, _ := newStubbedExchange(t, verifier)
	if _, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "x"}); !errors.Is(err, domain.ErrIdentityTokenInvalid) {
		t.Fatalf("err = %v; quiere ErrIdentityTokenInvalid", err)
	}
	want := []string{SystemWappBFF, SystemWappEdge, SystemWappPlatform}
	if !slices.Equal(verifier.systems, want) {
		t.Errorf("aplicaciones probadas = %v; quiere %v", verifier.systems, want)
	}
}

// Expirado lo está para todas: se corta en el primer intento.
func TestExchange_ExpiredIdentityStopsAtFirstSystem(t *testing.T) {
	verifier := &stubIdentityVerifier{err: fmt.Errorf("verificador: %w", identityauth.ErrTokenExpired)}
	svc, _ := newStubbedExchange(t, verifier)
	if _, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "x"}); !errors.Is(err, domain.ErrIdentityTokenInvalid) {
		t.Fatalf("err = %v; quiere ErrIdentityTokenInvalid", err)
	}
	if len(verifier.systems) != 1 {
		t.Errorf("se probaron %v; quiere solo la primera aplicación", verifier.systems)
	}
}

// Claims sin `sub` o sin `exp` no se aceptan.
func TestExchange_ClaimsWithoutSubjectOrExpAreInvalid(t *testing.T) {
	noSubject := &identityjwt.Claims{System: SystemWappBFF}
	noExp := &identityjwt.Claims{System: SystemWappBFF}
	noExp.Subject = uuid.NewString()
	for name, claims := range map[string]*identityjwt.Claims{"no_subject": noSubject, "no_exp": noExp} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newStubbedExchange(t, &stubIdentityVerifier{claims: claims})
			if _, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "x"}); !errors.Is(err, domain.ErrIdentityTokenInvalid) {
				t.Fatalf("err = %v; quiere ErrIdentityTokenInvalid", err)
			}
		})
	}
}

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
