package usecase

// Parte de exchange_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el montaje del canje (identity de prueba, memoria, espías y dobles rotos), compartido con otros tests del paquete.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
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
