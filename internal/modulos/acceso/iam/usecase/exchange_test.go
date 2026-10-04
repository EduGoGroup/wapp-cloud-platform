package usecase

import (
	"context"
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
