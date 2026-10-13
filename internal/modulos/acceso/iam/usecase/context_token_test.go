package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
)

// Material compartido por los tests del paquete: vive aquí porque este test se compila
// siempre (los de los ficheros en rojo llevan la etiqueta pendiente y lo reutilizan).
const (
	// testSigningKey es el material HS256 del emisor de Context Tokens de los tests.
	testSigningKey = "hs256-material-de-firma-para-los-tests"
	// testIssuer es el `iss` de los Context Tokens de los tests.
	testIssuer = "wapp-iam-test"
	// testTenant es la empresa A de los tests.
	testTenant = "11111111-1111-1111-1111-111111111111"
)

// stubValidator es un TokenValidator que devuelve lo que se le diga: permite probar las ramas
// que un JWTManager real no produce a voluntad (expirado, fallo inesperado, sin `exp`).
type stubValidator struct {
	claims *sharedjwt.Claims
	err    error
}

func (s stubValidator) ValidateToken(string) (*sharedjwt.Claims, error) { return s.claims, s.err }

// TestTokenTypeBearer_IsTheWireLiteral: el esquema que wApp emite es el literal "Bearer".
func TestTokenTypeBearer_IsTheWireLiteral(t *testing.T) {
	if tokenTypeBearer != "Bearer" {
		t.Fatalf("tokenTypeBearer = %q; quiere el literal \"Bearer\"", tokenTypeBearer)
	}
}

// R-U31: sin validador no se construye, con el texto literal del fail-fast.
func TestNewContextTokenService_RequiresValidator(t *testing.T) {
	svc, err := NewContextTokenService(nil)
	if err == nil {
		t.Fatal("sin validador debería fallar: verificar sin con qué no es verificar")
	}
	if svc != nil {
		t.Errorf("con error devolvió un servicio no nil: %+v", svc)
	}
	if got, want := err.Error(), "iam: ContextTokenService requiere un TokenValidator"; got != want {
		t.Errorf("err = %q; quiere el literal %q", got, want)
	}
}

// Un token válido emitido por wApp sale Valid=true con los claims copiados.
func TestContextTokenService_Verify_ValidTokenCopiesClaims(t *testing.T) {
	issuer := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	var svc *ContextTokenService
	svc, err := NewContextTokenService(issuer)
	if err != nil {
		t.Fatalf("NewContextTokenService: %v", err)
	}
	token, expiresAt, err := issuer.GenerateToken("user-1", testTenant, []string{"operator"},
		identityrbac.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	got, err := svc.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.Valid || got.TenantID != testTenant || got.Subject != "user-1" {
		t.Fatalf("Verify = %+v; quiere Valid con tenant %q y sujeto user-1", got, testTenant)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "operator" {
		t.Errorf("Roles = %v; quiere [operator]", got.Roles)
	}
	if got.ExpiresAt.Unix() != expiresAt.Unix() {
		t.Errorf("ExpiresAt = %v; quiere el exp del token %v", got.ExpiresAt, expiresAt)
	}
}

// Inválido o expirado (también envueltos) es Valid=false SIN error: es la respuesta.
func TestContextTokenService_Verify_InvalidOrExpiredIsAnswerNotError(t *testing.T) {
	cases := []struct {
		name      string
		validator TokenValidator
		token     string
	}{
		{"garbage_with_real_manager", sharedjwt.NewJWTManager(testSigningKey, testIssuer), "not-a-token"},
		{"foreign_signature", sharedjwt.NewJWTManager("otra-clave-distinta-de-la-buena", testIssuer), mustToken(t)},
		{"expired", stubValidator{err: sharedjwt.ErrTokenExpired}, "x"},
		{"wrapped_invalid", stubValidator{err: fmt.Errorf("capa: %w", sharedjwt.ErrInvalidToken)}, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := NewContextTokenService(c.validator)
			if err != nil {
				t.Fatalf("NewContextTokenService: %v", err)
			}
			got, err := svc.Verify(context.Background(), c.token)
			if err != nil {
				t.Fatalf("Verify devolvió error %v; un token que no vale es la respuesta, no un fallo", err)
			}
			if got.Valid {
				t.Fatalf("Verify = %+v; quiere Valid=false", got)
			}
		})
	}
}

// Un error inesperado del validador se propaga, con Valid=false.
func TestContextTokenService_Verify_UnexpectedErrorPropagates(t *testing.T) {
	boom := errors.New("el verificador no tiene claves cargadas")
	svc, err := NewContextTokenService(stubValidator{err: boom})
	if err != nil {
		t.Fatalf("NewContextTokenService: %v", err)
	}
	got, err := svc.Verify(context.Background(), "x")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; quiere el error del validador propagado", err)
	}
	if got.Valid {
		t.Errorf("con error inesperado salió Valid=true: %+v", got)
	}
}

// Sin `exp` en los claims, ExpiresAt queda en cero y no se desreferencia a ciegas.
func TestContextTokenService_Verify_NoExpLeavesZeroExpiry(t *testing.T) {
	svc, err := NewContextTokenService(stubValidator{claims: &sharedjwt.Claims{UserID: "u", TenantID: testTenant}})
	if err != nil {
		t.Fatalf("NewContextTokenService: %v", err)
	}
	got, err := svc.Verify(context.Background(), "x")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.Valid || !got.ExpiresAt.IsZero() {
		t.Fatalf("Verify = %+v; quiere Valid con ExpiresAt cero", got)
	}
}

// mustToken emite un Context Token válido con el emisor de los tests.
func mustToken(t *testing.T) string {
	t.Helper()
	token, _, err := sharedjwt.NewJWTManager(testSigningKey, testIssuer).GenerateToken(
		"user-1", testTenant, []string{"operator"}, identityrbac.Grants{}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return token
}
