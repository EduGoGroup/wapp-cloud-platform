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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
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
	// verifier es el que acepta los tokens de `issuer`. Se guarda para que un
	// test pueda reconstruir el canje cambiando UNA pieza (mustExchangeSvcCon)
	// sin generar otro par de claves, que emitiría tokens que este verificador
	// rechazaría.
	verifier usecase.IdentityTokenVerifier
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
	return exchangeFixture{svc: svc, issuer: issuerMgr, contexts: contexts, store: store, userID: userID, verifier: verifier}
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

// mustExchangeSvcCon reconstruye el canje del fixture cambiando SOLO el
// repositorio de empresa activa. Comparte el resto de dobles para que lo único
// distinto entre este servicio y el del fixture sea la pieza bajo prueba.
func mustExchangeSvcCon(t *testing.T, f exchangeFixture, active out.ActiveTenantRepo) *usecase.ExchangeService {
	t.Helper()
	svc, err := usecase.NewExchangeService(
		f.verifier, f.store.Memberships, f.store.Roles, f.store.Grants, f.store.Audit, active, f.contexts, usecase.Config{},
	)
	if err != nil {
		t.Fatalf("NewExchangeService: %v", err)
	}
	return svc
}

func mustExchangeSvc(t *testing.T, verifier usecase.IdentityTokenVerifier, store *memory.Store, jwt *sharedjwt.JWTManager) *usecase.ExchangeService {
	t.Helper()
	svc, err := usecase.NewExchangeService(
		verifier, store.Memberships, store.Roles, store.Grants, store.Audit, store.ActiveTenants, jwt, usecase.Config{},
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

// ---------------------------------------------------------------------------
// EL CASO «VARIAS EMPRESAS» (Plan 047 · Ola 5 · T5.1, D-047.14)
// ---------------------------------------------------------------------------
//
// 🔧 AQUÍ VIVÍA TestExchange_ConVariosTenantsFallaEnVezDeElegir, cuya cabecera
// decía «está INTACTO a propósito […] que este test no se haya tocado es parte
// del criterio». Tocarlo ES el cambio de T5.1: el canje ya no falla con dos
// membresías, resuelve por la EMPRESA ACTIVA.
//
// 🔴 PERO SU ESPÍRITU SIGUE VIGENTE Y NO SE HA BORRADO: elegir la primera en
// silencio SIGUE PROHIBIDO. Lo que era un único test que exigía un error son
// ahora tres que exigen desenlaces concretos, y el primero
// (TestExchange_ConVariasEmpresasYSinElegirNoEligePorTi) es literalmente la misma
// afirmación con el desenlace nuevo. La mutación que lo demuestra —hacer que
// resolveTenant devuelva tenants[0] con dos membresías y sin empresa activa—
// tiene que ponerlo en ROJO.

// dosEmpresas siembra una persona NUEVA con membresía en dos empresas y un rol
// DISTINTO acotado a cada una (D-056.11: la asignación lleva tenant). Devuelve su
// UUID.
//
// Los roles van ACOTADOS y no globales a propósito: un rol global se resolvería
// para las dos empresas y entonces el test de «los grants de ESA y no los de la
// otra» no podría fallar nunca — sería un assert vacuo con aspecto de cobertura.
func (f exchangeFixture) dosEmpresas(t *testing.T) string {
	t.Helper()
	userID := uuid.NewString()
	ctx := context.Background()

	rolA := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "solo-en-A"},
		[]domain.Grant{{Pattern: "flows.create", Effect: domain.EffectAllow}})
	if err := f.store.Roles.AssignToUser(ctx, userID, rolA.ID, ptr(testTenant)); err != nil {
		t.Fatalf("AssignToUser (A): %v", err)
	}
	rolB := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "solo-en-B"},
		[]domain.Grant{{Pattern: "messages.send", Effect: domain.EffectAllow}})
	if err := f.store.Roles.AssignToUser(ctx, userID, rolB.ID, ptr(testTenantB)); err != nil {
		t.Fatalf("AssignToUser (B): %v", err)
	}

	// Seed y no Add: Add lleva la guarda de «una sola empresa» (MD-055.2) y este
	// es justo el estado que la guarda no dejaría escribir hoy — y que existe en
	// bases reales anteriores a ella.
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	return userID
}

// TestExchange_ConVariasEmpresasYSinElegirNoEligePorTi es el HEREDERO DIRECTO del
// test custodio, con el desenlace nuevo: sin empresa activa guardada, el canje
// NO se inventa una — emite el token SIN empresa, exactamente el del caso «cero»
// (D-056.12), y la consola pinta ahí su selector.
//
// 🔴 LO QUE DE VERDAD VIGILA es la mitad negativa: que NO salga testTenant, que
// es la PRIMERA membresía sembrada. Un `return tenants[0]` en esa rama pasaría
// cualquier test que solo comprobara «no hay error».
func TestExchange_ConVariasEmpresasYSinElegirNoEligePorTi(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	userID := f.dosEmpresas(t)
	token, _ := f.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v (dos membresías dejaron de ser un error, D-047.14)", err)
	}
	if res.Context.TenantID == testTenant {
		t.Fatalf("el canje eligió la PRIMERA empresa (%q) en silencio: eso sigue estando prohibido", testTenant)
	}
	if res.Context.TenantID != "" {
		t.Fatalf("tenant = %q, want vacío: sin empresa activa no hay empresa", res.Context.TenantID)
	}

	// El dato que importa es el que viaja FIRMADO, no el del DTO.
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	assertTenantlessClaims(t, claims, userID)
}

// TestExchange_LaEmpresaActivaValidaAcotaElToken es la otra mitad: con una
// elección guardada y VIVA, el token sale acotado a ESA empresa y con LOS GRANTS
// DE ESA EMPRESA.
//
// 🔴 Se comprueba leyendo los CLAIMS, y las dos direcciones: que estén los de B y
// que NO estén los de A. Solo con la primera mitad, un canje que resolviera los
// grants con el tenant equivocado —o con los dos— saldría verde.
func TestExchange_LaEmpresaActivaValidaAcotaElToken(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	userID := f.dosEmpresas(t)
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	token, _ := f.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := f.svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.Context.TenantID != testTenantB {
		t.Fatalf("tenant = %q, want %q (la empresa activa)", res.Context.TenantID, testTenantB)
	}

	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	if claims.TenantID != testTenantB {
		t.Fatalf("tenant_id del token FIRMADO = %q, want %q", claims.TenantID, testTenantB)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "solo-en-B" {
		t.Errorf("roles = %v, want [solo-en-B]: se resolvieron los de la otra empresa", claims.Roles)
	}
	if !identityrbac.EvaluateGrants(claims.Grants, "messages.send") {
		t.Errorf("faltan los grants de la empresa ACTIVA: %+v", claims.Grants)
	}
	if identityrbac.EvaluateGrants(claims.Grants, "flows.create") {
		t.Errorf("el token lleva los grants de la OTRA empresa: %+v", claims.Grants)
	}
}

// TestExchange_LaEmpresaActivaQueYaNoEsSuyaNoDaTenant es EL TEST DEL REQUISITO DE
// SEGURIDAD de T5.1: guardar una empresa activa NO concede nada, y la fila se
// contrasta contra las membresías EN EL MOMENTO DE LEERLA.
//
// 🔴 Fíjate en lo que el test NO hace: no borra la fila de user_active_tenant. Le
// quita la MEMBRESÍA y deja la elección escrita, que es exactamente lo que pasa
// en producción cuando a alguien se le da de baja de una empresa —la baja no toca
// esa tabla, a propósito—. Si la comprobación de lectura desapareciera, esa
// persona seguiría recibiendo tokens acotados a una empresa de la que ya no forma
// parte, CON sus grants, y nadie se enteraría.
//
// ⚠️ SON TRES EMPRESAS Y NO DOS, Y ESO NO ES ADORNO: al quitarle la tercera le
// quedan DOS, que es la única forma de que el canje siga entrando por la rama de
// «varias» y llegue a mirar la empresa activa. Con dos empresas y una baja se
// cae a la rama de UNA membresía, que resuelve por la membresía y ni consulta
// esta tabla — el test saldría rojo por el motivo equivocado (y así salió la
// primera vez que se escribió).
func TestExchange_LaEmpresaActivaQueYaNoEsSuyaNoDaTenant(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	ctx := context.Background()
	userID := f.dosEmpresas(t)
	const tercera = "33333333-3333-3333-3333-333333333333"
	f.store.Memberships.Seed(userID, tercera)
	if err := f.store.ActiveTenants.SetActiveTenant(ctx, userID, tercera); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	// La baja de la TERCERA. La empresa activa NO se toca: sigue diciéndola.
	if err := f.store.Memberships.Remove(ctx, userID, tercera); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Y se comprueba que de verdad quedó escrita, para que este test no pase por
	// haber limpiado lo que dice no limpiar.
	if activo, ok, aerr := f.store.ActiveTenants.ActiveTenantOf(ctx, userID); aerr != nil || !ok || activo != tercera {
		t.Fatalf("la empresa activa se perdió (%q, ok=%v, err=%v): este test exige que SIGA escrita", activo, ok, aerr)
	}
	// Guarda anti-hueco: le tienen que quedar DOS, o el canje no entraría por la
	// rama que este test pretende ejercitar.
	if quedan, qerr := f.store.Memberships.TenantsOfUser(ctx, userID); qerr != nil || len(quedan) != 2 {
		t.Fatalf("le quedan %v (err=%v), quiero DOS: con una sola el canje ni mira la empresa activa", quedan, qerr)
	}

	token, _ := f.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)
	res, err := f.svc.Exchange(ctx, in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.Context.TenantID != "" {
		t.Fatalf("tenant = %q, want vacío: la empresa activa ya no es suya y guardarla NO concede nada",
			res.Context.TenantID)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	// Sin empresa Y SIN GRANTS: si el token saliera sin tenant pero con los
	// grants de la empresa perdida, el fallo sería el mismo con otra cara.
	assertTenantlessClaims(t, claims, userID)
}

// TestExchange_ConUnaSolaMembresiaLaEmpresaActivaNI SE MIRA. Con una membresía el
// canje devuelve esa empresa sin consultar nada: es la regresión de rango cero de
// T5.1 en su forma más incómoda —una empresa activa guardada que apunta a OTRA
// parte— y el desenlace correcto es que NO cambia nada.
//
// Sin este caso, alguien podría "simplificar" resolveTenant consultando la
// empresa activa SIEMPRE, y el camino que hoy corre en producción para todo el
// mundo pasaría a depender de una tabla nueva.
func TestExchange_ConUnaSolaMembresiaLaEmpresaActivaNiSeMira(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t) // 1 membresía en testTenant, rol operator
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), f.userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	// 🔴 «NI SE MIRA» SE MIDE, NO SE DEDUCE. Un espía cuenta las lecturas del
	// repositorio: sin él, este test solo comprobaría el DESENLACE, y un canje que
	// consultara la tabla en cada emisión —pagando una consulta por token para
	// todo el sistema— saldría verde mientras devolviera lo mismo.
	espia := &activeTenantEspia{dentro: f.store.ActiveTenants}
	svc := mustExchangeSvcCon(t, f, espia)
	token, _ := f.identityToken(t, f.userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if espia.lecturas != 0 {
		t.Fatalf("el canje consultó la empresa activa %d vez/veces con UNA sola membresía: "+
			"es el camino que hoy corre en producción para todo el mundo y no puede pagar esa consulta",
			espia.lecturas)
	}
	if res.Context.TenantID != testTenant {
		t.Fatalf("tenant = %q, want %q: con UNA membresía manda la membresía, no la empresa activa",
			res.Context.TenantID, testTenant)
	}
	claims, err := f.contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	assertOperatorIdentity(t, claims, f.userID)
}

// TestExchange_UnFalloLeyendoLaEmpresaActivaNoEsUnTokenSinEmpresa: si el
// repositorio se cae, el canje CORTA. No degrada a «token sin empresa».
//
// 🔴 Es la distinción que el puerto pide y que un `if err != nil { return "", nil }`
// destruiría en silencio: quien pertenece a dos empresas se quedaría operando sin
// ninguna —sin poder hacer nada, y pareciendo que nunca eligió— porque Postgres
// tuvo un mal segundo. Es el modo de fallo que se diagnostica mal: parece un
// problema de la persona y es un problema de la base.
func TestExchange_UnFalloLeyendoLaEmpresaActivaNoEsUnTokenSinEmpresa(t *testing.T) {
	t.Parallel()
	f := newExchangeFixture(t)
	userID := f.dosEmpresas(t)
	roto := errors.New("la base no contesta")
	svc := mustExchangeSvcCon(t, f, activeTenantRoto{err: roto})
	token, _ := f.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)

	_, err := svc.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if !errors.Is(err, roto) {
		t.Fatalf("err = %v, want %v: un fallo de infraestructura NO puede leerse como «no ha elegido»", err, roto)
	}
}

// activeTenantEspia envuelve un repositorio real y CUENTA las lecturas. Sirve
// para afirmar lo que ningún aserto sobre el resultado puede afirmar: que cierto
// camino NO consulta la tabla.
type activeTenantEspia struct {
	dentro   out.ActiveTenantRepo
	lecturas int
}

func (e *activeTenantEspia) ActiveTenantOf(ctx context.Context, userID string) (string, bool, error) {
	e.lecturas++
	return e.dentro.ActiveTenantOf(ctx, userID)
}

func (e *activeTenantEspia) SetActiveTenant(ctx context.Context, userID, tenantID string) error {
	return e.dentro.SetActiveTenant(ctx, userID, tenantID)
}

// activeTenantRoto es el repositorio de empresa activa que siempre falla.
type activeTenantRoto struct{ err error }

func (r activeTenantRoto) ActiveTenantOf(context.Context, string) (string, bool, error) {
	return "", false, r.err
}
func (r activeTenantRoto) SetActiveTenant(context.Context, string, string) error { return r.err }

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
	if _, err := usecase.NewExchangeService(nil, store.Memberships, store.Roles, store.Grants, store.Audit, store.ActiveTenants, jwt, usecase.Config{}); err == nil {
		t.Error("sin verificador debería fallar: el modo dual apagado NO se expresa con un servicio a medias")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, nil, store.Roles, store.Grants, store.Audit, store.ActiveTenants, jwt, usecase.Config{}); err == nil {
		t.Error("sin repositorio de membresías debería fallar")
	}
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, store.Memberships, store.Roles, store.Grants, store.Audit, store.ActiveTenants, nil, usecase.Config{}); err == nil {
		t.Error("sin emisor debería fallar")
	}
	// 🔴 Y sin el repositorio de la EMPRESA ACTIVA también (Plan 047 · Ola 5 ·
	// T5.1). No es simetría por gusto: un servicio construido sin él arrancaría y
	// dejaría a quien tiene dos empresas sin ninguna, EN SILENCIO — el peor modo
	// de fallo de los tres, porque los otros dos se notan en el primer canje.
	if _, err := usecase.NewExchangeService(unavailableVerifier{}, store.Memberships, store.Roles, store.Grants, store.Audit, nil, jwt, usecase.Config{}); err == nil {
		t.Error("sin repositorio de empresa activa debería fallar: no hay modo «sin multi-empresa»")
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
