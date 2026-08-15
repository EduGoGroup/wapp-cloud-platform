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
	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

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

// exchangeFixture arma un ExchangeService sobre un Store en memoria con una
// membresía de tenant y un rol operator asignado.
//
// Fíjate en lo que NO hay: ningún padrón local de usuarios. Desde la Ola 5 del
// Plan 003 de identity, "pertenecer a wApp" es tener membresía y nada más — la
// persona vive en identity-core y wApp no guarda ni una fila suya (design.md
// Ola 5 §2). El fixture lo refleja: el sujeto es un UUID que solo aparece en
// tenant_members y en la asignación de rol.
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
	issuerMgr, verifier := newIdentityPair(t)

	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	userID := seedMember(t, store, testTenant, "operator", []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "messages.send", Effect: domain.EffectAllow},
	})

	svc := mustExchangeSvc(t, verifier, store, contexts)
	return exchangeFixture{svc: svc, issuer: issuerMgr, contexts: contexts, store: store, userID: userID}
}

// newIdentityPair devuelve el emisor de Identity Tokens de prueba y el
// verificador que lo acepta (mismo par de claves).
func newIdentityPair(t *testing.T) (*identityjwt.Manager, *identityjwt.MultiVerifier) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave ES256 de prueba: %v", err)
	}
	issuerMgr, err := identityjwt.NewManager(key, identityIssuer, identityKid)
	if err != nil {
		t.Fatalf("NewManager (identity): %v", err)
	}
	verifier, err := identityjwt.NewMultiVerifier(identityIssuer, map[string]*ecdsa.PublicKey{
		identityKid: &key.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier (identity): %v", err)
	}
	return issuerMgr, verifier
}

// seedMember da de alta un sujeto de wApp como lo hace la realidad tras la Ola
// 5: un UUID de identity con membresía de tenant y un rol asignado. Devuelve ese
// UUID. roleName vacío = miembro sin rol.
func seedMember(t *testing.T, store *memory.Store, tenantID, roleName string, grants []domain.Grant) string {
	t.Helper()
	userID := uuid.NewString()
	if roleName != "" {
		role := store.Roles.Seed(domain.Role{TenantID: ptr(tenantID), Name: roleName}, grants)
		if err := store.Roles.AssignToUser(context.Background(), userID, role.ID, nil); err != nil {
			t.Fatalf("AssignToUser: %v", err)
		}
	}
	store.Memberships.Seed(userID, tenantID)
	return userID
}

func mustExchangeSvc(t *testing.T, verifier usecase.IdentityTokenVerifier, store *memory.Store, jwt *sharedjwt.JWTManager) *usecase.ExchangeService {
	t.Helper()
	svc, err := usecase.NewExchangeService(
		verifier, store.Memberships, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{},
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

func TestExchange_LasTresAplicacionesDeWappSeCanjean(t *testing.T) {
	t.Parallel()
	for _, system := range []string{usecase.SystemWappBFF, usecase.SystemWappEdge, usecase.SystemWappPlatform} {
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

// TestExchange_SujetoSinEmpresaCanjeaSinTenant es el caso "cero" de D-056.12
// (Plan 056 · T3.3). SUSTITUYE a TestExchange_SujetoDesconocidoEsUsuarioNoMigrado,
// que exigía ErrUserNotMigrated: ese 401 era justo lo que impedía a alguien
// recién registrado entrar a VER que su acceso está en revisión.
//
// Lo que se comprueba no es solo que no falle, sino la forma exacta del token:
// válido, sin tenant y sin un solo grant. "Entrar y no poder hacer nada" ES el
func assertTenantlessClaims(t *testing.T, claims *sharedjwt.Claims, sinEmpresa string) {
	t.Helper()
	if claims.TenantID != "" {
		t.Errorf("tenant_id del token = %q, want vacío", claims.TenantID)
	}
	if claims.UserID != sinEmpresa || claims.Subject != sinEmpresa {
		t.Errorf("sujeto del token = %q/%q, want %q", claims.UserID, claims.Subject, sinEmpresa)
	}
	if len(claims.Roles) != 0 {
		t.Errorf("roles = %v, want vacío", claims.Roles)
	}
	if len(claims.Grants.Allow) != 0 || len(claims.Grants.Deny) != 0 {
		t.Errorf("grants = %+v, want vacíos: un token sin empresa no autoriza NADA", claims.Grants)
	}
	if identityrbac.EvaluateGrants(claims.Grants, "flows.read") {
		t.Error("un token sin empresa evaluó allow: el estado de espera no está cerrado")
	}
	if claims.TokenUse != sharedjwt.TokenUseAccess {
		t.Errorf("token_use = %q, want %q", claims.TokenUse, sharedjwt.TokenUseAccess)
	}
}

// TestExchange_SujetoSinEmpresaCanjeaSinTenant es el caso "cero" de D-056.12
// (Plan 056 · T3.3). SUSTITUYE a TestExchange_SujetoDesconocidoEsUsuarioNoMigrado,
// que exigía ErrUserNotMigrated: ese 401 era justo lo que impedía a alguien
// recién registrado entrar a VER que su acceso está en revisión.
func TestExchange_SujetoSinEmpresaCanjeaSinTenant(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	sinEmpresa := "99999999-9999-9999-9999-999999999999"
	token, _ := f.identityToken(t, sinEmpresa, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v (cero membresías dejó de ser un error, D-056.12)", err)
	}
	if res.ContextToken == "" {
		t.Fatal("context token vacío: sin token no hay pantalla de espera que pintar")
	}
	if res.Context.TenantID != "" {
		t.Errorf("tenant del contexto = %q, want vacío", res.Context.TenantID)
	}
	if res.Context.UserID != sinEmpresa {
		t.Errorf("user = %q, want %q", res.Context.UserID, sinEmpresa)
	}

	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token sin tenant: %v", err)
	}
	assertTenantlessClaims(t, claims, sinEmpresa)

	events := f.store.Audit.Events()
	if len(events) != 1 {
		t.Fatalf("el canje dejó %d eventos, quiero exactamente 1", len(events))
	}
	if events[0].Action != "auth.exchange" || events[0].Result != "ok" {
		t.Fatalf("evento inesperado: %+v (el canje sin empresa es un éxito, no un fallo)", events[0])
	}
	if events[0].TenantID != nil {
		t.Errorf("tenant del evento = %v, want NULL", *events[0].TenantID)
	}
}

// TestExchange_SinMembresiaNoHeredaLosGrantsDeSusRoles es la MITAD DE SEGURIDAD
// del caso "cero". Sustituye a TestExchange_SinMembresiaEsUsuarioNoMigrado
// conservando su siembra exacta —rol asignado, cero membresías—, porque esa
// combinación es justo la que se vuelve peligrosa cuando el canje deja de
// cortar: si los grants se resolvieran igual, saldría un token CON permisos y
// SIN tenant al que acotarlos.
//
// La regla no cambia, solo cambia dónde se aplica: tener permisos no es
// pertenecer.
func TestExchange_SinMembresiaNoHeredaLosGrantsDeSusRoles(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	huerfano := uuid.NewString()
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "sin-membresia"},
		[]domain.Grant{{Pattern: "flows.read", Effect: domain.EffectAllow}})
	if err := f.store.Roles.AssignToUser(context.Background(), huerfano, role.ID, nil); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}
	token, _ := f.identityToken(t, huerfano, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	if claims.TenantID != "" {
		t.Errorf("tenant_id = %q, want vacío (no hay fila en tenant_members)", claims.TenantID)
	}
	if identityrbac.EvaluateGrants(claims.Grants, "flows.read") {
		t.Fatal("el token sin membresía heredó flows.read de su rol: permisos sin empresa a la que acotarlos")
	}
	if len(claims.Roles) != 0 {
		t.Errorf("roles = %v, want vacío: sin empresa no se anuncian roles", claims.Roles)
	}
}

func assertOperatorIdentity(t *testing.T, claims *sharedjwt.Claims, userID string) {
	t.Helper()
	if claims.TenantID != testTenant {
		t.Errorf("tenant_id = %q, want %q", claims.TenantID, testTenant)
	}
	if claims.UserID != userID || claims.Subject != userID {
		t.Errorf("user_id/sub = %q/%q, want %q", claims.UserID, claims.Subject, userID)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "operator" {
		t.Errorf("roles = %v, want [operator]", claims.Roles)
	}
	if len(claims.Grants.Allow) != 2 {
		t.Errorf("allow = %v, want los 2 grants del rol operator", claims.Grants.Allow)
	}
	if !identityrbac.EvaluateGrants(claims.Grants, "flows.create") ||
		!identityrbac.EvaluateGrants(claims.Grants, "messages.send") {
		t.Errorf("los grants del rol operator no sobrevivieron: %+v", claims.Grants)
	}
	if len(claims.Grants.Deny) != 0 {
		t.Errorf("deny = %v, want vacío", claims.Grants.Deny)
	}
}

func assertOperatorTimestamps(t *testing.T, claims *sharedjwt.Claims, identityExp time.Time) {
	t.Helper()
	if claims.TokenUse != sharedjwt.TokenUseAccess {
		t.Errorf("token_use = %q, want %q", claims.TokenUse, sharedjwt.TokenUseAccess)
	}
	if claims.Issuer != testIssuer {
		t.Errorf("iss = %q, want %q", claims.Issuer, testIssuer)
	}
	if claims.ID == "" {
		t.Error("jti vacío: el token perdió su identificador")
	}
	if claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		t.Fatalf("iat/nbf/exp = %v/%v/%v, ninguno puede faltar", claims.IssuedAt, claims.NotBefore, claims.ExpiresAt)
	}
	if claims.ExpiresAt.Unix() > identityExp.Unix() {
		t.Errorf("context.exp=%d > identity.exp=%d", claims.ExpiresAt.Unix(), identityExp.Unix())
	}
}

// TestExchange_ConUnaMembresiaElTokenNoCambia es la REGRESIÓN DE RANGO CERO de
// T3.3 (🔴 del plan): el caso de 1 membresía —el único que existía en
// producción— tiene que salir idéntico campo a campo tras abrir el caso "cero".
func TestExchange_ConUnaMembresiaElTokenNoCambia(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t) // el fixture siembra 1 membresía y el rol operator
	token, identityExp := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}

	assertOperatorIdentity(t, claims, f.userID)
	assertOperatorTimestamps(t, claims, identityExp)

	if res.ExpiresAt.Unix() != claims.ExpiresAt.Unix() {
		t.Errorf("ExpiresAt devuelto (%d) != exp del token (%d)", res.ExpiresAt.Unix(), claims.ExpiresAt.Unix())
	}
	if res.Context.TenantID != testTenant || res.Context.UserID != f.userID {
		t.Errorf("contexto devuelto = %+v", res.Context)
	}
	if len(res.Context.Roles) != 1 || res.Context.Roles[0] != "operator" {
		t.Errorf("roles del contexto devuelto = %v, want [operator]", res.Context.Roles)
	}
}

// TestExchange_CanjeaSinPadronLocalYConservaLosRolesDeWapp es el test de la Ola
// 5 (regla D14: se verifica comportamiento, no texto). Comprueba las DOS mitades
// de la limpieza en un solo movimiento, y cada una falla por su lado:
//
//  1. El sujeto NO existe en ningún padrón local de wApp y aun así canjea. Con
//     el código anterior —que consultaba `iam_users` antes de resolver el
//     tenant— esto devolvía ErrUserNotMigrated. Es la prueba de que el chequeo
//     se fue de verdad, no de que el texto lo diga.
//  2. El Context Token emitido sigue llevando SUS roles y SUS grants. Eso es
//     justo lo que un `DROP TABLE iam_users CASCADE` mal apuntado destruiría en
//     silencio: se llevaría iam_user_roles por delante, el login seguiría en
//     verde y la persona se quedaría sin poder hacer lo que sí podía.
func TestExchange_CanjeaSinPadronLocalYConservaLosRolesDeWapp(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v (un sujeto sin fila local pero con membresía DEBE canjear)", err)
	}

	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	// Los roles, en el token y en el contexto devuelto.
	if len(claims.Roles) != 1 || claims.Roles[0] != "operator" {
		t.Errorf("roles del context token = %v, want [operator] (el RBAC de wApp no sobrevivió)", claims.Roles)
	}
	if len(res.Context.Roles) != 1 || res.Context.Roles[0] != "operator" {
		t.Errorf("roles del contexto devuelto = %v, want [operator]", res.Context.Roles)
	}
	// Y los grants de ese rol, evaluados de verdad con el matcher glob.
	if !identityrbac.EvaluateGrants(claims.Grants, "flows.create") {
		t.Error("se esperaba allow de flows.create (grant flows.* del rol operator)")
	}
	if !identityrbac.EvaluateGrants(claims.Grants, "messages.send") {
		t.Error("se esperaba allow de messages.send")
	}
	if identityrbac.EvaluateGrants(claims.Grants, "leases.revoke") {
		t.Error("no se esperaba allow de leases.revoke (default DENY)")
	}
}

// TestExchange_ConVariosTenantsFallaEnVezDeElegir es la pata "2 membresías" de
// T3.3, y está INTACTO a propósito: D-056.12 abre el caso "cero", no el caso
// "varias" (INV-056.9). Que este test no se haya tocado es parte del criterio.
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

func TestExchange_SinTokenEsEntradaInvalida(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	_, err := f.svc.Exchange(context.Background(), in.ExchangeInput{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestExchange_NoAbreSesionEnWapp sustituye al viejo TestExchange_NoEmiteRefreshToken,
// que miraba el almacén de refresh de wApp — un almacén que ya no existe (la
// tabla murió en la Ola 5). Lo que se sigue verificando es lo mismo: canjear no
// abre sesión aquí. La sesión la custodia identity, y el único rastro que el
// canje deja en wApp es su línea de auditoría.
func TestExchange_NoAbreSesionEnWapp(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	if claims.TokenUse == sharedjwt.TokenUseService {
		t.Fatalf("el canje emitió un token que no es de usuario: token_use=%q", claims.TokenUse)
	}

	events := f.store.Audit.Events()
	if len(events) != 1 {
		t.Fatalf("el canje dejó %d eventos, quiero exactamente 1 (el del canje)", len(events))
	}
	if events[0].Action != "auth.exchange" || events[0].Result != "ok" {
		t.Fatalf("evento inesperado: %+v", events[0])
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
	if _, err := usecase.NewExchangeService(nil, store.Memberships, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{}); err == nil {
		t.Error("sin verificador debería fallar: el modo dual apagado NO se expresa con un servicio a medias")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, nil, store.Roles, store.Grants, store.Audit, jwt, usecase.Config{}); err == nil {
		t.Error("sin repositorio de membresías debería fallar")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, store.Memberships, store.Roles, store.Grants, store.Audit, nil, usecase.Config{}); err == nil {
		t.Error("sin emisor debería fallar")
	}
}

func TestExchange_RolesAcotadosPorTenant(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)

	// Rol global (sin tenant): concede "global.read"
	roleGlobal := f.store.Roles.Seed(domain.Role{Name: "global-role"}, []domain.Grant{
		{Pattern: "global.read", Effect: domain.EffectAllow},
	})
	if err := f.store.Roles.AssignToUser(context.Background(), f.userID, roleGlobal.ID, nil); err != nil {
		t.Fatalf("AssignToUser global: %v", err)
	}

	// Rol acotado a testTenant: concede "tenant.write"
	tenantID := testTenant
	roleTenant := f.store.Roles.Seed(domain.Role{TenantID: &tenantID, Name: "tenant-role"}, []domain.Grant{
		{Pattern: "tenant.write", Effect: domain.EffectAllow},
	})
	if err := f.store.Roles.AssignToUser(context.Background(), f.userID, roleTenant.ID, &tenantID); err != nil {
		t.Fatalf("AssignToUser (tenant): %v", err)
	}

	// Rol acotado a OTRO tenant: concede "other.admin"
	otherTenantID := "other-tenant-uuid"
	roleOther := f.store.Roles.Seed(domain.Role{TenantID: &otherTenantID, Name: "other-role"}, []domain.Grant{
		{Pattern: "other.admin", Effect: domain.EffectAllow},
	})
	if err := f.store.Roles.AssignToUser(context.Background(), f.userID, roleOther.ID, &otherTenantID); err != nil {
		t.Fatalf("AssignToUser (other tenant): %v", err)
	}

	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)
	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.TenantID != testTenant {
		t.Fatalf("TenantID=%s, want %s", claims.TenantID, testTenant)
	}

	allowMap := make(map[string]bool)
	for _, a := range claims.Grants.Allow {
		allowMap[a] = true
	}

	if !allowMap["global.read"] {
		t.Error("falta grant global.read de rol global")
	}
	if !allowMap["tenant.write"] {
		t.Error("falta grant tenant.write de rol acotado a este tenant")
	}
	if allowMap["other.admin"] {
		t.Error("se coló grant other.admin de un rol acotado a OTRO tenant")
	}
}
