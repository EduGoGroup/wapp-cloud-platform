package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	iamdomain "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// ErrSignupNotAvailable se devuelve cuando el servicio de registro M2M no está configurado.
var ErrSignupNotAvailable = errors.New("platformadmin: servicio de registro no disponible")

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
	req.Email = strings.TrimSpace(req.Email)
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Origin = strings.TrimSpace(req.Origin)

	if req.Email == "" || req.Password == "" || req.FirstName == "" || req.LastName == "" {
		return false
	}
	return req.Origin == "bff" || req.Origin == "edge"
}

func registerOrEnsureIdentityUser(ctx context.Context, req SignupRequest, m2m out.IdentityM2MClient, log sharedlogger.Logger) (string, int, string) {
	userID, err := m2m.Signup(ctx, req.Email, req.Password, req.FirstName, req.LastName)
	switch {
	case errors.Is(err, iamdomain.ErrPasswordPolicy):
		return "", http.StatusBadRequest, "la contraseña no cumple la política de seguridad (mínimo 12 caracteres)"
	case errors.Is(err, iamdomain.ErrEmailTaken):
		u, ensureErr := m2m.EnsureUser(ctx, req.Email, req.FirstName, req.LastName)
		if ensureErr != nil {
			if log != nil {
				log.Warn("signup: fallback ensure_user falló", "email", req.Email, "error", ensureErr)
			}
			return "", http.StatusBadGateway, "error al procesar registro"
		}
		return u.ID, 0, ""
	case errors.Is(err, iamdomain.ErrIdentityUnavailable):
		return "", http.StatusBadGateway, "servicio de identidad no disponible"
	case err != nil:
		if log != nil {
			log.Warn("signup: registro en identity falló", "error", err)
		}
		return "", http.StatusInternalServerError, "error al procesar registro"
	default:
		return userID, 0, ""
	}
}

// SignupHandler implementa la ruta pública POST /api/v1/signup con rate-limit por IP.
func SignupHandler(repo *Repository, m2m out.IdentityM2MClient, limiter *ratelimit.Limiter, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
			return
		}

		if limiter != nil && !limiter.Allow(clientIP(r)) {
			http.Error(w, "demasiadas solicitudes desde esta IP", http.StatusTooManyRequests)
			return
		}

		if m2m == nil {
			http.Error(w, "registro no disponible", http.StatusServiceUnavailable)
			return
		}

		var req SignupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validateSignupRequest(&req) {
			http.Error(w, "cuerpo o campos de registro inválidos", http.StatusBadRequest)
			return
		}

		// 1. Registro o ensure en identity
		userID, statusCode, errMsg := registerOrEnsureIdentityUser(r.Context(), req, m2m, log)
		if statusCode != 0 {
			http.Error(w, errMsg, statusCode)
			return
		}

		// 2. Conceder el sistema correspondiente según origin (wapp.bff o wapp.edge)
		systemCode := "wapp." + req.Origin
		if _, err := m2m.ReplaceUserSystems(r.Context(), userID, []string{systemCode}); err != nil {
			if log != nil {
				log.Warn("signup: concesión de sistema falló", "user_id", userID, "system", systemCode, "error", err)
			}
			http.Error(w, "error al configurar aplicaciones", http.StatusBadGateway)
			return
		}

		// 3. Crear solicitud pendiente en la bandeja local (idempotente)
		if err := repo.CreateAccessRequest(r.Context(), userID, req.Email, req.Origin); err != nil {
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

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return strings.TrimSpace(rip)
	}
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
