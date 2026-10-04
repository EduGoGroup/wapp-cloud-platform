// Porta internal/platformadmin/signup.go @ 9a77307: el alta pública POST /api/v1/signup, la ÚNICA
// ruta sin autenticar del plano de plataforma. Recibe el puerto AccessRequestStore, no el
// *Repository (D-F2-3).

package platformadmin

import (
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// ErrSignupNotAvailable es el centinela de «el servicio de registro M2M no está configurado».
// Texto literal del viejo; hoy ningún código lo devuelve (SignupHandler contesta el 503 sin él),
// pero es contrato exportado y se conserva.
var ErrSignupNotAvailable = errors.New("platformadmin: servicio de registro no disponible")

// SignupRequest es el cuerpo JSON de POST /api/v1/signup.
type SignupRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Origin    string `json:"origin"`
}

// SignupResponse es la respuesta de éxito, CONSTANTE (REQ-056.8): no dice si la cuenta era nueva.
type SignupResponse struct {
	Message string `json:"message"`
}

// SignupHandler devuelve el handler de POST /api/v1/signup. Los textos de error son observables
// (el BFF y wapp-ctl los enseñan en texto plano) y van literales. En este orden, cortando en el
// primero que falle:
//
//  1. Rate-limit por IP (si limiter no es nil): agotado ⇒ 429 «demasiadas solicitudes desde esta
//     IP». La clave (R-A9, A-06a): sin trustProxy, SIEMPRE la IP de socket (el host de RemoteAddr,
//     o RemoteAddr entero si no lleva puerto): X-Forwarded-For y X-Real-IP los pone el cliente y
//     no estrenan cubo. Con trustProxy, la primera IP de X-Forwarded-For (recortada); si no hay,
//     X-Real-IP (recortada); si tampoco, la de socket.
//  2. m2m nil ⇒ 503 «registro no disponible», sin pánico (C-02; m2m es una interfaz: el nil es de
//     verdad).
//  3. El cuerpo se acota a 8 KiB ANTES de decodificar (A-09) y la petición entera a 10 s (las
//     llamadas a identity y al almacén heredan ese plazo). Un cuerpo por encima del límite, que no
//     es JSON o con campos inválidos ⇒ 400 «cuerpo o campos de registro inválidos», sin llamar a
//     identity. Campos: el correo se normaliza a minúsculas y sin espacios alrededor (A-09: «Ana@X.com»
//     y «ana@x.com» son la MISMA solicitud); nombre, apellido y origin se recortan; correo,
//     contraseña, nombre y apellido son obligatorios; el correo tiene que ser una dirección que
//     acepte net/mail; topes de 254 bytes el correo, 100 cada nombre y 72 la contraseña (el
//     truncado de bcrypt); origin "bff" o "edge".
//  4. Registro en identity (m2m.Signup, con la contraseña tal cual): domain.ErrPasswordPolicy ⇒
//     400 «la contraseña no cumple la política de seguridad (mínimo 12 caracteres)»;
//     domain.ErrEmailTaken ⇒ 409 «ese correo ya tiene cuenta: entra con tu clave» y NADA más
//     (C-01, R-A8: no se adopta la cuenta con EnsureUser ni se le tocan las aplicaciones);
//     domain.ErrIdentityUnavailable ⇒ 502 «servicio de identidad no disponible»; otro ⇒ 500
//     «error al procesar registro».
//  5. Concede UNA sola aplicación, la de su origen: ReplaceUserSystems(userID, ["wapp."+origin]),
//     sin leer las que tuviera (este camino REEMPLAZA; la unión es del alta de miembros). Un fallo
//     ⇒ 502 «error al configurar aplicaciones».
//  6. Siembra la solicitud pendiente con requests.CreateAccessRequest(userID, correo normalizado,
//     origin) (idempotente). Un fallo ⇒ 500 «error al registrar solicitud».
//  7. 202 SignupResponse{"Listo. Entra con tu correo y tu clave."}.
//
// Los fallos de 4 (el genérico), 5 y 6 se registran en log (si no es nil) como Warn, y NINGÚN log
// lleva el correo ni la contraseña (A-12): como mucho el user_id, el system y el error.
//
// No comprueba el método: el patrón "POST /api/v1/signup" del ServeMux ya responde 405 a otro.
func SignupHandler(requests AccessRequestStore, m2m out.IdentityM2MClient, limiter *ratelimit.Limiter, trustProxy bool, log sharedlogger.Logger) http.Handler {
	panic(pendiente.Implementar("platformadmin.SignupHandler"))
}
