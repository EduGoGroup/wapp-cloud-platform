package usecase_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	identityauth "github.com/EduGoGroup/identity-shared/auth"
	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

const (
	// identityIssuer es el emisor que estampa identity-core en sus tokens.
	identityIssuer = "identity-core"
	identityKid    = "es256-test"
)

// exchangeFixture arma un ExchangeService sobre un Store en memoria con un
// usuario activo, su membresía de tenant y un rol operator.
//
// La verificación del Identity Token es REAL: se emite con el emisor de
// identity-shared y se comprueba con su MultiVerifier sobre la clave pública del
// par generado aquí. Es el mismo camino que en producción; lo único simulado es
// de dónde salen las claves.
type exchangeFixture struct {
	svc      *usecase.ExchangeService
	issuer   *identityjwt.Manager
	contexts *sharedjwt.JWTManager
	store    *memory.Store
	userID   string
}

func newExchangeFixture(t *testing.T) exchangeFixture {
	t.Helper()
	store := memory.NewStore()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave ES256 de prueba: %v", err)
	}
	identityIssuerMgr, err := identityjwt.NewManager(key, identityIssuer, identityKid)
	if err != nil {
		t.Fatalf("NewManager (identity): %v", err)
	}
	verifier, err := identityjwt.NewMultiVerifier(identityIssuer, map[string]*ecdsa.PublicKey{
		identityKid: &key.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier (identity): %v", err)
	}

	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	users := mustUserSvc(t, store)
	role := store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "operator"}, []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "messages.send", Effect: domain.EffectAllow},
	})
	u, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: testTenant, Email: testEmail, Password: testLoginPhrase, RoleIDs: []string{role.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	store.Memberships.Seed(u.ID, testTenant)

	svc := mustExchangeSvc(t, verifier, store, contexts)
	return exchangeFixture{svc: svc, issuer: identityIssuerMgr, contexts: contexts, store: store, userID: u.ID}
}

func mustExchangeSvc(t *testing.T, verifier usecase.IdentityTokenVerifier, store *memory.Store, jwt *sharedjwt.JWTManager) *usecase.ExchangeService {
	t.Helper()
	svc, err := usecase.NewExchangeService(
		verifier, store.Users, store.Memberships, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{},
	)
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	return svc
}

// identityToken emite un Identity Token real para el sujeto/aplicación/TTL dados
// y devuelve el token junto con su instante de expiración.
func (f exchangeFixture) identityToken(t *testing.T, userID, system string, ttl time.Duration) (string, time.Time) {
	t.Helper()
	token, expiresAt, err := f.issuer.GenerateIdentityToken(identityjwt.IdentityTokenInput{
		UserID:       userID,
		System:       system,
		Email:        testEmail,
		TokenVersion: 1,
		TTL:          ttl,
	})
	if err != nil {
		t.Fatalf("GenerateIdentityToken: %v", err)
	}
	return token, expiresAt
}

// contextExp lee el `exp` del Context Token emitido validándolo de verdad (el
// dato que importa es el que viaja firmado, no el que devuelve el usecase).
func (f exchangeFixture) contextExp(t *testing.T, contextToken string) time.Time {
	t.Helper()
	claims, err := f.contexts.ValidateToken(contextToken)
	if err != nil {
		t.Fatalf("validando el context token emitido: %v", err)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("el context token no lleva exp")
	}
	return claims.ExpiresAt.Time
}

func TestExchange_CanjeaIdentidadPorContexto(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.ContextToken == "" {
		t.Fatal("context token vacío")
	}
	if res.Context.TenantID != testTenant {
		t.Errorf("tenant = %q, want %q (sale de tenant_members, no del token)", res.Context.TenantID, testTenant)
	}
	if res.Context.UserID != f.userID {
		t.Errorf("user = %q, want %q (el `sub` del identity token)", res.Context.UserID, f.userID)
	}

	// El Context Token lleva el contexto de negocio que el Identity Token no
	// tiene: tenant y grants efectivos de wApp.
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	if claims.TenantID != testTenant || claims.UserID != f.userID {
		t.Errorf("claims del context token = %+v", claims)
	}
	if len(claims.Grants.Allow) == 0 {
		t.Error("el context token viaja sin grants: el RBAC de wApp no se resolvió")
	}
}

// TestExchange_ElContextTokenNuncaSobrevivalAlIdentityToken es el test OBLIGATORIO
// de REQ-A2 (identity Plan 003 · design.md Ola 3 §3): identity NUNCA ve un
// Context Token, así que la regla «la visa no dura más que el pasaporte» solo
// puede hacerse cumplir aquí. Sin este caso, la regla es una frase en un
// documento.
func TestExchange_ElContextTokenNuncaSobrevivalAlIdentityToken(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	// Identity Token a punto de expirar: le queda MUCHO menos que el TTL de
	// contexto (15m por defecto), así que el `exp` del canje tiene que acortarse.
	const remaining = 90 * time.Second
	token, identityExp := f.identityToken(t, f.userID, usecase.SystemWappEdge, remaining)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	contextExp := f.contextExp(t, res.ContextToken)
	if contextExp.Unix() > identityExp.Unix() {
		t.Fatalf("el context token sobrevive al identity token: context.exp=%d > identity.exp=%d",
			contextExp.Unix(), identityExp.Unix())
	}
	// Y que de verdad se acortó: si hubiera tomado el TTL de contexto entero, el
	// token duraría 15 minutos y el ≤ de arriba no lo habría detectado por poco.
	if time.Until(contextExp) > usecase.DefaultAccessTTL {
		t.Fatalf("el TTL de contexto no se recortó: quedan %s", time.Until(contextExp))
	}
	if res.ExpiresAt.Unix() != contextExp.Unix() {
		t.Errorf("el ExpiresAt devuelto (%d) no coincide con el del token (%d)",
			res.ExpiresAt.Unix(), contextExp.Unix())
	}
}

func TestExchange_ConIdentidadLargaMandaElTTLDeContexto(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	// Identity Token de 2h: el que acota ahora es el TTL de contexto (15m).
	token, identityExp := f.identityToken(t, f.userID, usecase.SystemWappBFF, 2*time.Hour)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	contextExp := f.contextExp(t, res.ContextToken)
	if contextExp.Unix() > identityExp.Unix() {
		t.Fatalf("context.exp=%d > identity.exp=%d", contextExp.Unix(), identityExp.Unix())
	}
	if remaining := time.Until(contextExp); remaining > usecase.DefaultAccessTTL || remaining < usecase.DefaultAccessTTL-time.Minute {
		t.Fatalf("el context token debería durar ~%s, quedan %s", usecase.DefaultAccessTTL, remaining)
	}
}

func TestExchange_IdentidadCasiVencidaNoSeCanjea(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	// Emitido con el mínimo que admite identity (1m): para cuando se canjea, lo
	// que queda ya no llega al mínimo emitible de un Context Token. Emitir uno
	// más largo que su origen es exactamente lo que la regla prohíbe.
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, time.Minute)

	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrIdentityTokenExpiring) {
		t.Fatalf("err = %v, want ErrIdentityTokenExpiring", err)
	}
}

func TestExchange_AmbasAplicacionesDeWappSeCanjean(t *testing.T) {
	t.Parallel()
	for _, system := range []string{usecase.SystemWappBFF, usecase.SystemWappEdge} {
		t.Run(system, func(t *testing.T) {
			t.Parallel()
			f := newExchangeFixture(t)
			token, _ := f.identityToken(t, f.userID, system, 15*time.Minute)
			if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token}); err != nil {
				t.Fatalf("Exchange para %s: %v", system, err)
			}
		})
	}
}

func TestExchange_LaIdentidadDeOtroEcosistemaNoSeCanjea(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	// Token perfectamente firmado por identity, pero emitido para una aplicación
	// de EduGo: aquí no vale.
	token, _ := f.identityToken(t, f.userID, "edugo.kmp", 15*time.Minute)

	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrIdentityTokenInvalid) {
		t.Fatalf("err = %v, want ErrIdentityTokenInvalid", err)
	}
}

func TestExchange_SujetoDesconocidoEsUsuarioNoMigrado(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, "99999999-9999-9999-9999-999999999999", usecase.SystemWappBFF, 15*time.Minute)

	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrUserNotMigrated) {
		t.Fatalf("err = %v, want ErrUserNotMigrated (no se crea el usuario al vuelo)", err)
	}
}

func TestExchange_SinMembresiaEsUsuarioNoMigrado(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	// Usuario que existe en wApp pero cuya membresía no llegó a tenant_members.
	huerfano, err := mustUserSvc(t, f.store).CreateUser(context.Background(), in.CreateUserInput{
		TenantID: testTenant, Email: "sin-membresia@tenant.example", Password: testLoginPhrase,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _ := f.identityToken(t, huerfano.ID, usecase.SystemWappBFF, 15*time.Minute)

	_, err = f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrUserNotMigrated) {
		t.Fatalf("err = %v, want ErrUserNotMigrated", err)
	}
}

func TestExchange_ConVariosTenantsFallaEnVezDeElegir(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	f.store.Memberships.Seed(f.userID, testTenantB)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrMultipleTenants) {
		t.Fatalf("err = %v, want ErrMultipleTenants (elegir el primero en silencio está prohibido)", err)
	}
}

func TestExchange_UsuarioInactivoNoCanjea(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	if err := mustUserSvc(t, f.store).DeleteUser(context.Background(), testTenant, f.userID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, domain.ErrUserNotMigrated) && !errors.Is(err, domain.ErrUserInactive) {
		t.Fatalf("err = %v, want ErrUserNotMigrated o ErrUserInactive", err)
	}
}

func TestExchange_SinTokenEsEntradaInvalida(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestExchange_NoEmiteRefreshToken(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	if _, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token}); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// El refresh es de identity y vive donde vive la sesión: el canje no debe
	// haber persistido ninguno en wApp.
	if n := f.store.Refresh.Count(); n != 0 {
		t.Fatalf("el canje persistió %d refresh tokens; no debe emitir ninguno", n)
	}
}

// unavailableVerifier simula el verificador sin claves frescas (identity
// inalcanzable). Nunca devuelve claims: el punto del caso es que la
// indisponibilidad NO se contesta como rechazo de la credencial.
type unavailableVerifier struct{}

func (unavailableVerifier) ValidateIdentityToken(_, _ string) (*identityjwt.Claims, error) {
	return nil, fmt.Errorf("%w: sin claves frescas", identityauth.ErrJWKSUnavailable)
}

func TestExchange_IdentityCaidoNoRechazaLaCredencial(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	svc := mustExchangeSvc(t, unavailableVerifier{}, store, sharedjwt.NewJWTManager(testSigningKey, testIssuer))

	//nolint:gosec // no es una credencial: el verificador ni llega a mirarla
	_, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: "token.que.no.se.puede.juzgar"})
	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, want ErrIdentityUnavailable (no ErrIdentityTokenInvalid)", err)
	}
}

func TestNewExchangeService_ExigeSusDependencias(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	jwt := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	if _, err := usecase.NewExchangeService(nil, store.Users, store.Memberships, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{}); err == nil {
		t.Error("sin verificador debería fallar: el modo dual apagado NO se expresa con un servicio a medias")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, store.Users, nil, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{}); err == nil {
		t.Error("sin repositorio de membresías debería fallar")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, store.Users, store.Memberships, store.Roles, store.Grants, store.Audit, nil, usecase.Config{}); err == nil {
		t.Error("sin emisor debería fallar")
	}
}
