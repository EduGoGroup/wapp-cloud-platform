// Porta internal/gateway/grpc/auth.go @ cbc5736

package grpc

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// WithAuthenticator inyecta el puerto de autenticación de usuario del IAM (Plan
// 033 · T2.2, ADR-0025). Con él, el gateway atiende UserLogin/UserRefresh/
// UserLogout relayados por el Edge, delegando en el puerto. Sin él (o con nil),
// esas tres peticiones responden UserAuthError{code: "internal", message: "auth no
// disponible"} sin auditar nada (auth no disponible en este despliegue). Mismo
// patrón que WithReceiptSink/WithDiagnosticsSink.
//
// Lo que el gateway promete con el puerto inyectado (R-G9, R3.4.a):
//
//   - La respuesta —UserAuthResponse con el command_id de la petición y el
//     session_id del frame, en el sobre y dentro— vuelve POR EL MISMO STREAM que
//     trajo la petición, con los dos relojes de session.BoundedSend (el ctx del
//     llamante y el plazo propio del Registry). NUNCA se busca el destino en
//     session.Registry: los frames de auth estampan `__wapp_control__`, una clave
//     compartida por todos los Edge, y por ahí la respuesta de una empresa —con sus
//     tokens— salía por el cable de otra (T-5). Sin cable en el connCtx se registra
//     un Error y la respuesta NO se entrega; no hay fallback.
//   - UserLogin: el tenant es IMPLÍCITO del canal mTLS, nunca del mensaje. El login
//     se acota a ese tenant (LoginInput.TenantID) y, tras autenticar, el tenant de
//     la identidad emitida tiene que coincidir con el del canal: si no, responde
//     `tenant_mismatch` y no entrega tokens. Sin identidad mTLS responde
//     `tenant_mismatch` («canal sin identidad») sin llamar al puerto.
//   - UserRefresh: canjea el refresh token (rota el par) con el MISMO guard de
//     tenant cruzado sobre la identidad re-resuelta, y el mismo rechazo sin
//     identidad mTLS.
//   - UserLogout: revoca el refresh token (o todos, con all_sessions) y, si sale
//     bien, responde por la rama Tokens con un UserTokens VACÍO. No exige identidad
//     mTLS.
//   - Un error del puerto se traduce a un código estable, nunca al error crudo ni
//     a un mensaje: ErrInvalidCredentials → `invalid_credentials`, ErrUserInactive
//     → `user_inactive`, ErrRefreshInvalid → `refresh_invalid`, ErrInvalidInput →
//     `invalid_input`; cualquier otro → `internal`.
//   - Éxito de login o refresh: rama Tokens con access_token, refresh_token,
//     token_type y expires_at (segundos unix) tal como los dio el puerto.
//
// 🔒 Ni credenciales ni tokens llegan jamás a un log ni a la auditoría.
func WithAuthenticator(_ in.Authenticator) Option {
	panic(pendiente.Implementar("grpc.WithAuthenticator"))
}

// WithAuthAuditor inyecta el auditor (in.Auditor) que registra los eventos del
// plano de control del Edge. Sin él (o con nil), la auth funciona igual pero no se
// audita (best-effort, como el resto de la auditoría del IAM); un fallo del auditor
// tampoco cambia la respuesta: se registra en debug.
//
// Lo que se registra con él (R-G10), siempre con el tenant del canal, CERO PII
// (ni email, ni password, ni tokens) y, en meta, `edge_id`, `session_id` y
// `channel: "cloudlink"`:
//
//   - `edge.auth.login`, `edge.auth.refresh`, `edge.auth.logout` (recurso
//     `edge.auth`, resultado `ok` o `error`, y `command_id` en meta): acciones de
//     OPERADOR. El actor es el `sub` de la persona —vacío si el fallo fue antes de
//     resolverla, y siempre en el logout— y meta lleva `actor_type: "operator"`.
//   - `edge.session.open` (recurso `edge.session`, resultado `ok`): la apertura de
//     una sesión CloudLink, acción del PROCESO del Edge. El actor es el edge_id (el
//     CN de su certificado mTLS) y meta lleva `actor_type: "daemon"`. Sin identidad
//     mTLS no se registra.
//
// Así «lo hizo el operador X» y «lo hizo el edge Y» se distinguen en la misma
// bitácora sin interpretar el formato del actor.
func WithAuthAuditor(_ in.Auditor) Option {
	panic(pendiente.Implementar("grpc.WithAuthAuditor"))
}
