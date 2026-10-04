// Porta internal/iam/usecase/context_token.go @ 9a77307

package usecase

import (
	"context"
	"errors"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// tokenTypeBearer es el único esquema de autorización que emite wApp: el TokenType de todo
// domain.AuthResult (lo usa DelegatedAuthService).
const tokenTypeBearer = "Bearer"

// TokenValidator valida un Context Token de wApp y devuelve sus claims. Lo satisfacen
// *sharedjwt.JWTManager (emisor único) y *sharedjwt.MultiVerifier (selección por `kid`,
// ADR-0019), que es lo que cablea el arranque.
//
// Se declara como interface, y no como el tipo concreto, para que la política de aceptación
// del :8103 sea EXACTAMENTE la misma que la del middleware: se inyecta el mismo verificador en
// los dos sitios y no hay forma de que uno acepte un token que el otro rechace.
type TokenValidator interface {
	ValidateToken(token string) (*sharedjwt.Claims, error)
}

// ContextTokenService implementa in.TokenVerifier: inspecciona los Context Tokens que emite
// wApp. Es lo único que queda del plano de auth propio del :8103 tras la Ola 5, y sobrevive
// por una razón concreta: el Context Token lo firma wApp con su clave, así que identity no
// puede validarlo y nadie más tiene por qué intentarlo.
//
// No valida contraseñas ni emite nada. Quien acredita a la persona es identity-core (identity
// ADR-0001).
type ContextTokenService struct {
	validator TokenValidator
}

// compile-time: satisface el puerto de entrada.
var _ in.TokenVerifier = (*ContextTokenService)(nil)

// NewContextTokenService construye el verificador sobre el validador dado. R-U31: un
// validador nil es error de cableado y se rechaza al arrancar (fail-fast) con
// "iam: ContextTokenService requiere un TokenValidator": verificar sin con qué no es verificar.
func NewContextTokenService(validator TokenValidator) (*ContextTokenService, error) {
	if validator == nil {
		return nil, errors.New("iam: ContextTokenService requiere un TokenValidator")
	}
	return &ContextTokenService{validator: validator}, nil
}

// Verify valida un Context Token con el validador inyectado, sin salir a ningún otro sitio.
//
//   - Token válido ⇒ Valid=true con TenantID, Subject (el user_id), Roles y ExpiresAt (cero si
//     el token no trae `exp`) copiados de los claims; err nil.
//   - Token inválido o expirado (sharedjwt.ErrInvalidToken / ErrTokenExpired, también
//     envueltos) ⇒ Valid=false SIN error: es la respuesta de /verify, no un fallo de la
//     operación.
//   - Cualquier otro error del validador ⇒ Valid=false y ese error, propagado.
func (s *ContextTokenService) Verify(_ context.Context, accessToken string) (in.VerifyResult, error) {
	return verifyWithValidator(s.validator, accessToken)
}

// verifyWithValidator es la semántica de /verify sobre cualquier validador de Context Tokens.
// Vive como función libre porque la comparten este servicio y el delegado
// (DelegatedAuthService.Verify): el token que se mira lo emite wApp en los dos casos, así que
// la política de aceptación tiene que ser exactamente la misma.
func verifyWithValidator(validator TokenValidator, accessToken string) (in.VerifyResult, error) {
	claims, err := validator.ValidateToken(accessToken)
	if err != nil {
		if errors.Is(err, sharedjwt.ErrInvalidToken) || errors.Is(err, sharedjwt.ErrTokenExpired) {
			return in.VerifyResult{Valid: false}, nil
		}
		return in.VerifyResult{Valid: false}, err
	}
	var expiresAt time.Time
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}
	return in.VerifyResult{
		Valid:     true,
		TenantID:  claims.TenantID,
		Subject:   claims.UserID,
		Roles:     claims.Roles,
		ExpiresAt: expiresAt,
	}, nil
}
