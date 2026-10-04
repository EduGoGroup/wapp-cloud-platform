// Porta internal/iam/usecase/delegated_auth.go @ 9a77307

package usecase

import (
	"context"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DelegatedAuthService implementa in.Authenticator delegando la autenticación en identity-core
// (identity Plan 003 · design.md Ola 3 §6). Quien lo consume —hoy el gateway CloudLink, que
// relaya el login/refresh/logout del operador— no tiene que enterarse de la delegación.
//
// El reparto: identity valida credenciales, custodia la sesión y emite el refresh; wApp canjea
// esa identidad por un Context Token con el tenant y los grants que solo él conoce. El Identity
// Token NO se persiste ni sale al cliente: vive el instante server-side que dura el canje.
type DelegatedAuthService struct{}

// compile-time: el delegado satisface el puerto de autenticación.
var _ in.Authenticator = (*DelegatedAuthService)(nil)

// NewDelegatedAuthService construye el autenticador delegado. log es OPCIONAL (nil = sin
// rastro); todo lo demás es obligatorio y su ausencia se rechaza al arrancar (fail-fast), con
// servicio nil y estos textos literales:
//
//   - identity nil ⇒ "iam: DelegatedAuthService requiere un cliente de identity";
//   - exchange nil ⇒ "iam: DelegatedAuthService requiere el canje (Exchanger)";
//   - validator nil ⇒ "iam: DelegatedAuthService requiere un TokenValidator";
//   - R-U14: system distinto de SystemWappBFF o SystemWappEdge (vacío, de otro ecosistema,
//     "wapp" a secas, incluso SystemWappPlatform) ⇒ "iam: DelegatedAuthService requiere una
//     aplicación de wApp (wapp.bff o wapp.edge)". system es la aplicación con la que se hace
//     el login: lo que somete cada login al System Gate de la aplicación correcta.
func NewDelegatedAuthService(
	identity out.IdentityClient,
	exchange in.Exchanger,
	validator TokenValidator,
	system string,
	log sharedlogger.Logger,
) (*DelegatedAuthService, error) {
	panic(pendiente.Implementar("usecase.NewDelegatedAuthService"))
}

// Login autentica contra identity y canjea la identidad resultante por el Context Token de wApp,
// en un solo movimiento server-side (R-U10).
//
//   - Email o Password vacíos ⇒ domain.ErrInvalidInput, sin llamar a identity.
//   - Llama a identity.Login UNA vez con el correo, la contraseña y el system del constructor;
//     su error (credenciales inválidas, System Gate denegado, identity caído…) se propaga tal
//     cual y NO se canjea nada.
//   - Canjea el IdentityToken de la sesión; un error del canje (p. ej. sujeto sin migrar) se
//     propaga y la persona no entra.
//   - Resultado: AccessToken = el Context Token; RefreshToken = el de identity; TokenType =
//     "Bearer"; ExpiresAt = la del Context Token (ya acotada por la del Identity Token);
//     Context = el del canje. El Identity Token no aparece en el resultado.
//
// LoginInput.TenantID se IGNORA: identity no conoce tenants, y el tenant del resultado sale de
// la membresía en wApp (el gateway puede comprobar después que coincide con el de su canal).
func (s *DelegatedAuthService) Login(ctx context.Context, req in.LoginInput) (domain.AuthResult, error) {
	panic(pendiente.Implementar("usecase.DelegatedAuthService.Login"))
}

// Refresh rota la sesión en identity (identity.Refresh con el refresh presentado) y RE-CANJEA
// (R-U11): los grants se resuelven otra vez, así que un cambio de rol entra en el siguiente
// Context Token sin volver a pedir la contraseña. RefreshToken vacío ⇒ domain.ErrInvalidInput
// sin llamar a nadie; un error de identity o del canje se propaga. El resultado tiene la misma
// forma que el de Login.
func (s *DelegatedAuthService) Refresh(ctx context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	panic(pendiente.Implementar("usecase.DelegatedAuthService.Refresh"))
}

// Logout revoca en identity (R-U11). RefreshToken vacío ⇒ domain.ErrInvalidInput sin llamar a
// nadie.
//
//   - Sin AllSessions: identity.Logout con ESE refresh y nada más. Cierra SOLO la sesión de esta
//     aplicación (modelo Google: cerrar la consola del Edge no cierra la web).
//   - Con AllSessions: el titular de la revocación global sale de un Identity Token, NUNCA de un
//     user_id transportado (LogoutInput.UserID se ignora). Se rota primero el refresh
//     (identity.Refresh) y con el IdentityToken obtenido se llama a identity.LogoutAll. Si el
//     refresh falla (p. ej. ya quemado), ese error sube y NO se intenta el logout-all.
//
// R-U12 · fallo parcial: si el logout-all falla tras la rotación, el refresh presentado ya se
// consumió y las demás sesiones siguen vivas. Se intenta entonces cerrar la sesión recién
// rotada (identity.Logout con el refresh NUEVO) como mitigación best-effort, y el error devuelto
// es SIEMPRE el del logout-all (también si el cierre falla): una revocación global que no
// ocurrió no se disfraza de éxito. La mitigación NO depende del logger: con log nil se intenta
// igual. Con logger, deja rastro del estado real —Warn «revocación global fallida: solo se cerró
// la sesión rotada; las demás siguen vivas» si el cierre funcionó, Error «revocación global
// fallida y la sesión rotada tampoco se pudo cerrar: quedan sesiones vivas» si no—, con el
// refresh rotado TRUNCADO a sus 12 primeros caracteres más «…»: nunca un token entero ni una
// contraseña.
func (s *DelegatedAuthService) Logout(ctx context.Context, req in.LogoutInput) error {
	panic(pendiente.Implementar("usecase.DelegatedAuthService.Logout"))
}

// Verify valida un Context Token de wApp con el validador inyectado, con la MISMA semántica que
// ContextTokenService.Verify (inválido o expirado ⇒ Valid=false sin error). R-U13: no sale a
// identity: el token lo firmó wApp, y la persona ya fue acreditada cuando se emitió.
func (s *DelegatedAuthService) Verify(ctx context.Context, accessToken string) (in.VerifyResult, error) {
	panic(pendiente.Implementar("usecase.DelegatedAuthService.Verify"))
}
