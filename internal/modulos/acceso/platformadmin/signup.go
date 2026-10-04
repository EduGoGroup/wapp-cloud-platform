// Porta internal/platformadmin/signup.go @ 9a77307: el alta pública POST /api/v1/signup, la ÚNICA
// ruta sin autenticar del plano de plataforma. Recibe el puerto AccessRequestStore, no el
// *Repository (D-F2-3).

package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"

	iamdomain "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// signupBodyLimit acota el cuerpo de la ÚNICA ruta pública del plan (A-09): 4 campos de texto no
// necesitan más de un par de KiB; 8 KiB deja margen sin abrir la puerta a que un cuerpo de varios
// MB reserve memoria antes de que la validación lo rechace.
const signupBodyLimit = 8 << 10 // 8 KiB

// signupRequestTimeout acota el contexto de ESTA petición (dos llamadas a identity + un INSERT
// local), independiente del WriteTimeout global del servidor (A-09): sin un deadline propio, un
// identity lento consume el presupuesto entero del servidor, no solo el de esta conexión.
const signupRequestTimeout = 10 * time.Second

// Límites de entrada de una ruta SIN autenticar (A-09). maxPasswordLen es el techo de truncado de
// bcrypt que documenta design.md §5.2: identity ya lo valida, pero fallar aquí evita gastar la
// llamada M2M en un cuerpo que identity iba a rechazar de todos modos.
const (
	maxEmailLen    = 254
	maxNameLen     = 100
	maxPasswordLen = 72
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter != nil && !limiter.Allow(clientIP(r, trustProxy)) {
			http.Error(w, "demasiadas solicitudes desde esta IP", http.StatusTooManyRequests)
			return
		}

		// C-02, defensa en profundidad: el arranque ya evita cablear esta ruta sin cliente M2M,
		// pero la guarda se queda por si alguna vez alguien registra este handler por su cuenta.
		// m2m es SIEMPRE una interfaz (out.IdentityM2MClient), nunca un *iamidentity.M2MClient
		// concreto envuelto en ella: así `== nil` compara un nil de interfaz de verdad, no un
		// puntero nil tipado que Go convertiría en un valor de interfaz no-nil.
		if m2m == nil {
			http.Error(w, "registro no disponible", http.StatusServiceUnavailable)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, signupBodyLimit)
		ctx, cancel := context.WithTimeout(r.Context(), signupRequestTimeout)
		defer cancel()

		var req SignupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validateSignupRequest(&req) {
			http.Error(w, "cuerpo o campos de registro inválidos", http.StatusBadRequest)
			return
		}

		// 1. Registro en identity, o rechazo sin escritura en el caso D (C-01).
		userID, statusCode, errMsg := registerIdentityUser(ctx, req, m2m, log)
		if statusCode != 0 {
			http.Error(w, errMsg, statusCode)
			return
		}

		// 2. Conceder el sistema correspondiente según origin (wapp.bff o wapp.edge). Solo se
		// llega aquí cuando identity devolvió 201 (cuenta nueva, adoptada o reconocida —
		// design.md §5.2 casos A/B/C).
		systemCode := "wapp." + req.Origin
		if _, err := m2m.ReplaceUserSystems(ctx, userID, []string{systemCode}); err != nil {
			if log != nil {
				log.Warn("signup: concesión de sistema falló", "user_id", userID, "system", systemCode, "error", err)
			}
			http.Error(w, "error al configurar aplicaciones", http.StatusBadGateway)
			return
		}

		// 3. Crear solicitud pendiente en la bandeja local (idempotente).
		if err := requests.CreateAccessRequest(ctx, userID, req.Email, req.Origin); err != nil {
			if log != nil {
				log.Warn("signup: creación de access_request falló", "user_id", userID, "error", err)
			}
			http.Error(w, "error al registrar solicitud", http.StatusInternalServerError)
			return
		}

		// 4. Respuesta constante 202 Accepted.
		writeJSON(w, http.StatusAccepted, SignupResponse{
			Message: "Listo. Entra con tu correo y tu clave.",
		})
	})
}

// isEmailPadding dice si r es relleno que se recorta de los bordes del correo: lo que Go llama
// espacio y los dos invisibles que llegan al pegar (D-F2-13; la misma regla que el token de
// invitación, D-F2-11).
func isEmailPadding(r rune) bool {
	return unicode.IsSpace(r) || r == '\u200B' || r == '\uFEFF'
}

// isBareAddress dice si raw es la dirección de parsed y nada más: sin nombre visible, sin ángulos
// y sin comentario (D-F2-13). Se compara con la forma canónica que net/mail escribe para esa
// dirección, de modo que una parte local entre comillas («"a b"@x.com»), que es una dirección
// legal y no un adorno, sigue valiendo.
func isBareAddress(raw string, parsed *mail.Address) bool {
	if parsed.Name != "" {
		return false
	}
	return "<"+raw+">" == (&mail.Address{Address: parsed.Address}).String()
}

// validateSignupRequest normaliza los campos de req EN SITIO y dice si el alta es válida.
func validateSignupRequest(req *SignupRequest) bool {
	// El correo se normaliza a minúsculas y sin espacios (A-09): identity y la bandeja local usan
	// el correo como clave "humana", y sin esto "Ana@X.com" y "ana@x.com" producen dos filas
	// distintas.
	//
	// D-F2-13 (Jhoan, 2026-10-04), y aquí el nuevo se APARTA del viejo: de los bordes se recortan
	// también U+200B y U+FEFF, que TrimSpace deja y que quien pega el correo no ve; y lo que se
	// acepta es una dirección PELADA. net/mail está pensado para cabeceras y da por buena «Ana
	// <ana@x.com>» o «ana@x.com (Ana)»: un formulario de alta que recibe eso contesta 400 en vez
	// de guardar el adorno como si fuera el correo.
	req.Email = strings.ToLower(strings.TrimFunc(req.Email, isEmailPadding))
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Origin = strings.TrimSpace(req.Origin)

	if req.Email == "" || req.Password == "" || req.FirstName == "" || req.LastName == "" {
		return false
	}
	addr, err := mail.ParseAddress(req.Email)
	if err != nil || !isBareAddress(req.Email, addr) {
		return false
	}
	if len(req.Email) > maxEmailLen || len(req.FirstName) > maxNameLen ||
		len(req.LastName) > maxNameLen || len(req.Password) > maxPasswordLen {
		return false
	}
	return req.Origin == "bff" || req.Origin == "edge"
}

// registerIdentityUser llama a identity/auth/signup y traduce su resultado: el userID, o el
// código y el texto con que responder.
//
// 🔴 C-01: el caso D (ErrEmailTaken, ADR-0027 — correo con otra clave, o cuenta
// bloqueada/inactiva) es el 409 de identity, y design.md §5.2 es explícito: "no crea ni toca
// nada". wApp respeta la misma frontera y NO cae a EnsureUser con el M2M propio: eso adoptaría la
// cuenta de un tercero sin que nadie se haya autenticado (EnsureUser "NO está acotado por
// ecosistema: con este scope se puede asegurar —y por tanto descubrir— cualquier correo del
// grupo"), y el paso siguiente de la ruta feliz (ReplaceUserSystems, DECLARATIVO) le revocaría a
// esa persona cualquier aplicación que no fuera el origin de quien mandó la petición.
func registerIdentityUser(ctx context.Context, req SignupRequest, m2m out.IdentityM2MClient, log sharedlogger.Logger) (string, int, string) {
	userID, err := m2m.Signup(ctx, req.Email, req.Password, req.FirstName, req.LastName)
	switch {
	case errors.Is(err, iamdomain.ErrPasswordPolicy):
		return "", http.StatusBadRequest, "la contraseña no cumple la política de seguridad (mínimo 12 caracteres)"
	case errors.Is(err, iamdomain.ErrEmailTaken):
		return "", http.StatusConflict, "ese correo ya tiene cuenta: entra con tu clave"
	case errors.Is(err, iamdomain.ErrIdentityUnavailable):
		return "", http.StatusBadGateway, "servicio de identidad no disponible"
	case err != nil:
		if log != nil {
			// A-12: CERO PII. El error de m2m nombra la operación y el código HTTP, nunca el
			// correo ni la contraseña.
			log.Warn("signup: registro en identity falló", "error", err)
		}
		return "", http.StatusInternalServerError, "error al procesar registro"
	default:
		return userID, 0, ""
	}
}

// clientIP resuelve la IP que consume el rate-limit. Con trustProxy=false (el default, A-06a) las
// cabeceras X-Forwarded-For/X-Real-IP se IGNORAN: las pone el cliente, y sin un proxy de
// confianza delante que las sobrescriba, cada petición podría estrenar cubo con una IP falsa
// distinta. Sin proxy de confianza, la única clave que un cliente no puede falsificar es la IP de
// socket (r.RemoteAddr).
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			return strings.TrimSpace(parts[0])
		}
		if rip := r.Header.Get("X-Real-IP"); rip != "" {
			return strings.TrimSpace(rip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
