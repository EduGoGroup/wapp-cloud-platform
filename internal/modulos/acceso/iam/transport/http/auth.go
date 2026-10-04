// Porta internal/iam/transport/http/auth.go @ 9a77307

// Package iamhttp expone la superficie HTTP del IAM del módulo acceso en el listener público
// :8103: /api/v1/auth/{verify,exchange} (este fichero), la elección de empresa
// (active_tenant.go), el canje de invitaciones (canje.go), la emisión y revocación de
// invitaciones (invitations.go) y el plano de roles y miembros del tenant (roles.go). Es la capa
// de transporte: traduce JSON ⇄ DTOs de los puertos in y mapea los errores tipados del dominio a
// códigos HTTP (http.go, writeDomainError). NO contiene lógica de negocio (vive en
// iam/usecase) ni conoce SQL; importa solo iam/domain e iam/ports/in.
//
// Las DOS rutas de este fichero son las dos caras del Context Token de wApp: `exchange` lo emite
// a cambio de un Identity Token del SSO, y `verify` lo inspecciona. Aquí ya NO se validan
// contraseñas ni se emiten refresh: /login, /refresh, /logout y /token murieron con el IAM
// propio (REQ-A1), y lo que responden ahora es 404.
//
// Son rutas PÚBLICAS (sin token previo): establecen o inspeccionan credenciales. La
// autorización RBAC de las rutas de negocio la aporta el middleware httpapi.Authenticate →
// RequirePermission.
package iamhttp

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// AuthHandler sirve los endpoints de autenticación. Depende SOLO de los puertos in
// (in.TokenVerifier, in.Exchanger), no de las structs concretas de usecase.
type AuthHandler struct{}

// NewAuthHandler construye el handler de autenticación. exchange puede ser nil: es el modo dual
// APAGADO (sin WAPP_IDENTITY_JWKS_URL), y entonces Exchange responde 503. log puede ser nil.
func NewAuthHandler(verifier in.TokenVerifier, exchange in.Exchanger, log sharedlogger.Logger) *AuthHandler {
	panic(pendiente.Implementar("iamhttp.NewAuthHandler"))
}

// Register monta en mux EXACTAMENTE dos rutas, por PATH pelado (sin verbo: el método lo
// comprueba cada handler): /api/v1/auth/verify (Verify) y /api/v1/auth/exchange (Exchange).
//
// R-H3: el IAM viejo NO está en el cable. /api/v1/auth/login, /refresh, /logout y /token no se
// montan, así que un mux con solo esto responde 404 —no 401: un 401 diría que la ruta sigue ahí
// y ha rechazado la credencial—.
func Register(mux *http.ServeMux, verifier in.TokenVerifier, exchange in.Exchanger, log sharedlogger.Logger) {
	panic(pendiente.Implementar("iamhttp.Register"))
}

// Verify valida un Context Token y devuelve sus claims (R-H3).
//
//   - Método: POST o GET; cualquier otro ⇒ 405 «método no permitido».
//   - El token sale del cuerpo `{"token":"…"}` (solo en POST; el cuerpo es OPCIONAL y un JSON
//     inválido no es error: se cae al header) o, si ahí no hay, de `Authorization: Bearer …`.
//   - Sin token por ninguna de las dos vías ⇒ 400 «token requerido (cuerpo {token} o header
//     Authorization)», sin llamar al puerto.
//   - El puerto falla ⇒ 500 «no se pudo validar el token».
//   - Token inválido o expirado ⇒ 200 con `{"valid":false}` y NADA más (no 401, design.md §8):
//     los claims solo se serializan con valid=true.
//   - Token válido ⇒ 200 con `valid`, `tenant_id`, `subject`, `roles` y `expires_at` (RFC 3339
//     en UTC; omitido si el instante es cero).
func (h *AuthHandler) Verify() http.Handler {
	panic(pendiente.Implementar("iamhttp.AuthHandler.Verify"))
}

// Exchange canjea un Identity Token de identity-core por un Context Token de wApp (identity
// Plan 003 · T3.1). Aquí NO se validan credenciales (eso ya es de identity) ni se emite refresh.
//
// R-H6, LOS DESENLACES:
//   - Método distinto de POST ⇒ 405 «método no permitido».
//   - Modo dual apagado (exchange nil) ⇒ 503 «modo dual apagado: identity no está configurado
//     en este despliegue», antes de mirar el cuerpo.
//   - JSON roto ⇒ 400 «cuerpo JSON inválido».
//   - Los errores del puerto salen por writeDomainError: token no aceptable
//     (domain.ErrIdentityTokenInvalid / ErrIdentityTokenExpiring) o sujeto sin migrar ⇒ 401;
//     domain.ErrInvalidInput ⇒ 400; identity inalcanzable ⇒ 503.
//   - Éxito ⇒ 200 con `{"context_token","token_type":"Bearer","expires_at","context":
//     {"tenant_id","user_id","roles"}}` (expires_at en RFC 3339 UTC) y SIN `refresh_token`: el
//     refresh es de identity.
//   - Sin empresa (cero membresías, o varias sin elegida válida) ⇒ 200 con el contexto que dé el
//     puerto, `tenant_id` vacío. El 409 de «varias empresas» YA NO EXISTE (D-047.14).
//
// R-H5: el cuerpo tiene UN campo, `identity_token`, y nada más. Un `tenant_id` en el cuerpo no
// tiene dónde aterrizar: el puerto recibe exactamente in.ExchangeInput{IdentityToken: …} (INV-8;
// la empresa se elige en POST /api/v1/auth/active-tenant).
func (h *AuthHandler) Exchange() http.Handler {
	panic(pendiente.Implementar("iamhttp.AuthHandler.Exchange"))
}
