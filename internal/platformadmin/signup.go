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

	iamdomain "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// ErrSignupNotAvailable se devuelve cuando el servicio de registro M2M no está configurado.
var ErrSignupNotAvailable = errors.New("platformadmin: servicio de registro no disponible")

// signupBodyLimit acota el cuerpo de la ÚNICA ruta pública del plan (A-09): 4
// campos de texto no necesitan más de un par de KiB; 8 KiB deja margen sin
// abrir la puerta a que un cuerpo de varios MB reserve memoria antes de que la
// validación lo rechace.
const signupBodyLimit = 8 << 10 // 8 KiB

// signupRequestTimeout acota el contexto de ESTA petición (dos llamadas a
// identity + un INSERT local), independiente del WriteTimeout global del
// servidor (A-09): sin un deadline propio, un identity lento consume el
// presupuesto entero del servidor, no solo el de esta conexión.
const signupRequestTimeout = 10 * time.Second

// Límites de entrada de una ruta SIN autenticar (A-09). maxPasswordLen es el
// techo de truncado de bcrypt que documenta design.md §5.2: identity ya lo
// valida, pero fallar aquí evita gastar la llamada M2M en un cuerpo que
// identity iba a rechazar de todos modos.
const (
	maxEmailLen    = 254
	maxNameLen     = 100
	maxPasswordLen = 72
)

// SignupRequest es el cuerpo JSON para POST /api/v1/signup.
type SignupRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Origin    string `json:"origin"`
}

// SignupResponse es la respuesta de éxito constante (REQ-056.8).
type SignupResponse struct {
	Message string `json:"message"`
}

func validateSignupRequest(req *SignupRequest) bool {
	// El correo se normaliza a minúsculas y sin espacios (A-09): identity y la
	// bandeja local usan el correo como clave "humana", y sin esto
	// "Ana@X.com" y "ana@x.com" producen dos filas distintas.
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Origin = strings.TrimSpace(req.Origin)

	if req.Email == "" || req.Password == "" || req.FirstName == "" || req.LastName == "" {
		return false
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return false
	}
	if len(req.Email) > maxEmailLen || len(req.FirstName) > maxNameLen ||
		len(req.LastName) > maxNameLen || len(req.Password) > maxPasswordLen {
		return false
	}
	return req.Origin == "bff" || req.Origin == "edge"
}

// registerIdentityUser llama a identity/auth/signup y traduce su resultado.
//
// 🔴 C-01: el caso D (ErrEmailTaken, ADR-0027 — correo con otra clave, o
// cuenta bloqueada/inactiva) es el 409 de identity, y design.md §5.2 es
// explícito: "no crea ni toca nada". wApp respeta la misma frontera y NO cae a
// EnsureUser con el M2M propio: eso adoptaría la cuenta de un tercero sin que
// nadie se haya autenticado (EnsureUser "NO está acotado por ecosistema: con
// este scope se puede asegurar —y por tanto descubrir— cualquier correo del
// grupo", su propio doc-comment), y el paso siguiente de la ruta feliz
// (ReplaceUserSystems, DECLARATIVO) le revocaría a esa persona cualquier
// aplicación que no fuera el origin de quien mandó la petición.
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
			// A-12: CERO PII. El error de m2m nombra la operación y el código
			// HTTP, nunca el correo ni la contraseña.
			log.Warn("signup: registro en identity falló", "error", err)
		}
		return "", http.StatusInternalServerError, "error al procesar registro"
	default:
		return userID, 0, ""
	}
}

// SignupHandler implementa la ruta pública POST /api/v1/signup con rate-limit
// por IP. trustProxy gobierna si clientIP confía en X-Forwarded-For/X-Real-IP
// (A-06a): en `false` (el default) esas cabeceras se ignoran y la clave es
// siempre la IP de socket, que un cliente no puede falsificar.
//
// Sin comprobación de método: el registro es "POST /api/v1/signup"
// (internal/bootstrap/http.go), y el ServeMux de Go 1.22 ya responde 405 por
// su cuenta ante cualquier otro método sobre ese patrón -- un `if r.Method !=
// http.MethodPost` aquí dentro sería código muerto que engaña sobre quién
// decide el 405.
func SignupHandler(repo *Repository, m2m out.IdentityM2MClient, limiter *ratelimit.Limiter, trustProxy bool, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter != nil && !limiter.Allow(clientIP(r, trustProxy)) {
			http.Error(w, "demasiadas solicitudes desde esta IP", http.StatusTooManyRequests)
			return
		}

		// C-02, defensa en profundidad: bootstrap ya evita cablear esta ruta
		// sin cliente M2M (ver internal/bootstrap/http.go), pero la guarda se
		// queda por si alguna vez alguien registra este handler por su
		// cuenta. m2m es SIEMPRE una interfaz (out.IdentityM2MClient), nunca
		// un *iamidentity.M2MClient concreto envuelto en ella: así `== nil`
		// compara un nil de interfaz de verdad, no un puntero nil tipado que
		// Go convertiría en un valor de interfaz no-nil.
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

		// 2. Conceder el sistema correspondiente según origin (wapp.bff o
		// wapp.edge). Solo se llega aquí cuando identity devolvió 201 (cuenta
		// nueva, adoptada o reconocida — design.md §5.2 casos A/B/C).
		systemCode := "wapp." + req.Origin
		if _, err := m2m.ReplaceUserSystems(ctx, userID, []string{systemCode}); err != nil {
			if log != nil {
				log.Warn("signup: concesión de sistema falló", "user_id", userID, "system", systemCode, "error", err)
			}
			http.Error(w, "error al configurar aplicaciones", http.StatusBadGateway)
			return
		}

		// 3. Crear solicitud pendiente en la bandeja local (idempotente)
		if err := repo.CreateAccessRequest(ctx, userID, req.Email, req.Origin); err != nil {
			if log != nil {
				log.Warn("signup: creación de access_request falló", "user_id", userID, "error", err)
			}
			http.Error(w, "error al registrar solicitud", http.StatusInternalServerError)
			return
		}

		// 4. Respuesta constante 202 Accepted
		writeJSON(w, http.StatusAccepted, SignupResponse{
			Message: "Listo. Entra con tu correo y tu clave.",
		})
	})
}

// clientIP resuelve la IP que consume el rate-limit. Con trustProxy=false (el
// default, A-06a) las cabeceras X-Forwarded-For/X-Real-IP se IGNORAN: las pone
// el cliente, y sin un proxy de confianza delante que las sobrescriba, cada
// petición podría estrenar cubo con una IP falsa distinta. Sin proxy de
// confianza, la única clave que un cliente no puede falsificar es la IP de
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
