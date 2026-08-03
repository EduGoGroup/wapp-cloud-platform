package usecase_test

import (
	"context"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// TestVerify_ValidAndInvalid es el caso que sobrevivió a auth_test.go: /verify
// mira Context Tokens de wApp, no contraseñas, así que la muerte del IAM propio
// no lo toca. Solo cambió quién lo sirve (ContextTokenService en vez de
// AuthService) y de dónde sale el token (se emite, ya no se hace login).
func TestVerify_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	jwt := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	svc, err := usecase.NewContextTokenService(jwt)
	if err != nil {
		t.Fatalf("NewContextTokenService: %v", err)
	}
	ctx := context.Background()

	userID := uuid.NewString()
	token, _, err := jwt.GenerateToken(userID, testTenant, []string{"operator"},
		identityrbac.Grants{Allow: []string{"flows.*"}}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	v, err := svc.Verify(ctx, token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !v.Valid || v.TenantID != testTenant || v.Subject != userID {
		t.Fatalf("Verify inesperado: %+v", v)
	}

	// Un token que no vale NO es un error de la operación: es su respuesta.
	bad, err := svc.Verify(ctx, "not-a-token")
	if err != nil {
		t.Fatalf("Verify(inválido) no debe devolver error: %v", err)
	}
	if bad.Valid {
		t.Fatal("Verify(inválido) debe ser Valid=false")
	}
}

func TestNewContextTokenService_ExigeValidador(t *testing.T) {
	t.Parallel()
	if _, err := usecase.NewContextTokenService(nil); err == nil {
		t.Error("sin validador debería fallar: verificar sin con qué no es verificar")
	}
}
