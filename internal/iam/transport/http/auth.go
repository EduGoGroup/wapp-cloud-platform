// Package iamhttp expone la superficie HTTP pública de autenticación del módulo
// IAM (Plan 018 · T3): /api/v1/auth/{login,refresh,logout,verify,token}. Es la
// capa de transporte: traduce JSON ⇄ DTOs de los puertos in (Authenticator,
// M2MAuthenticator) y mapea los errores tipados del dominio a códigos HTTP. NO
// contiene lógica de negocio (vive en internal/iam/usecase) ni conoce SQL.
//
// Estas rutas son PÚBLICAS (sin token): login/refresh/verify/token establecen o
// inspeccionan credenciales. La autorización RBAC/scope de las rutas de negocio
// (T4/T5) la aporta el middleware httpapi.Authenticate → RequirePermission.
package iamhttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// AuthHandler sirve los endpoints de autenticación. Depende SOLO de los puertos
// in (interfaces), no de las structs concretas de usecase.
type AuthHandler struct {
	authn in.Authenticator
	m2m   in.M2MAuthenticator
	log   sharedlogger.Logger
}

// NewAuthHandler construye el handler de autenticación.
func NewAuthHandler(authn in.Authenticator, m2m in.M2MAuthenticator, log sharedlogger.Logger) *AuthHandler {
	return &AuthHandler{authn: authn, m2m: m2m, log: log}
}

// Register monta las rutas /api/v1/auth/* en el mux dado (listener público
// :8103). Sigue el idiom del repo: paths planos + verificación de método dentro
// de cada handler.
func Register(mux *http.ServeMux, authn in.Authenticator, m2m in.M2MAuthenticator, log sharedlogger.Logger) {
	h := NewAuthHandler(authn, m2m, log)
	mux.Handle("/api/v1/auth/login", h.Login())
	mux.Handle("/api/v1/auth/refresh", h.Refresh())
	mux.Handle("/api/v1/auth/logout", h.Logout())
	mux.Handle("/api/v1/auth/verify", h.Verify())
	mux.Handle("/api/v1/auth/token", h.ServiceToken())
}

// ---------------------------------------------------------------------------
// DTOs de request/response (wire format estable de /api/v1/auth)
// ---------------------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id,omitempty"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
	AllSessions  bool   `json:"all_sessions,omitempty"`
}

type verifyRequest struct {
	Token string `json:"token,omitempty"`
}

type identityContextDTO struct {
	TenantID string   `json:"tenant_id"`
	UserID   string   `json:"user_id"`
	Roles    []string `json:"roles"`
}

type authResultDTO struct {
	AccessToken  string             `json:"access_token"`
	RefreshToken string             `json:"refresh_token"`
	TokenType    string             `json:"token_type"`
	ExpiresAt    string             `json:"expires_at"`
	Context      identityContextDTO `json:"context"`
}

type verifyResultDTO struct {
	Valid     bool     `json:"valid"`
	TenantID  string   `json:"tenant_id,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

type serviceTokenDTO struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type"`
	ExpiresAt string `json:"expires_at"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// Login autentica email+password y devuelve el par de tokens + contexto. 200 en
// éxito; 400 cuerpo inválido; 401 credenciales/usuario inactivo.
func (h *AuthHandler) Login() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req loginRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		res, err := h.authn.Login(r.Context(), in.LoginInput{
			Email:    req.Email,
			Password: req.Password,
			TenantID: req.TenantID,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toAuthResultDTO(res))
	})
}

// Refresh rota el refresh token y emite un access nuevo. 200 éxito; 400 cuerpo
// inválido; 401 refresh inválido/expirado.
func (h *AuthHandler) Refresh() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req refreshRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		res, err := h.authn.Refresh(r.Context(), in.RefreshInput{RefreshToken: req.RefreshToken})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toAuthResultDTO(res))
	})
}

// Logout revoca el refresh token (o todas las sesiones del usuario si
// all_sessions y el request porta un access válido). Idempotente → 204.
func (h *AuthHandler) Logout() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req logoutRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		out := in.LogoutInput{RefreshToken: req.RefreshToken, AllSessions: req.AllSessions}
		// AllSessions requiere el user_id: se resuelve del access token si viene
		// (Bearer), sin exigirlo (el revocado del refresh puntual no lo necesita).
		if req.AllSessions {
			if tok, ok := bearer(r); ok {
				if v, verr := h.authn.Verify(r.Context(), tok); verr == nil && v.Valid {
					out.UserID = v.Subject
				}
			}
		}
		if err := h.authn.Logout(r.Context(), out); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Verify valida un access token (del cuerpo {token} o del header Authorization)
// y devuelve sus claims. Un token inválido/expirado responde 200 con
// valid=false (design.md §8), NO 401.
func (h *AuthHandler) Verify() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		token := ""
		if r.Method == http.MethodPost && r.Body != nil {
			var req verifyRequest
			// Cuerpo opcional: si no hay JSON válido se cae al header.
			if json.NewDecoder(r.Body).Decode(&req) == nil {
				token = req.Token
			}
		}
		if token == "" {
			if tok, ok := bearer(r); ok {
				token = tok
			}
		}
		if token == "" {
			writeError(w, http.StatusBadRequest, "token requerido (cuerpo {token} o header Authorization)")
			return
		}
		v, err := h.authn.Verify(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo validar el token")
			return
		}
		writeJSON(w, http.StatusOK, toVerifyResultDTO(v))
	})
}

// ServiceToken canjea una api-key (header X-API-Key) por un service token M2M de
// corta vida con los scopes de la api-key. 200 éxito; 401 api-key inválida.
func (h *AuthHandler) ServiceToken() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		key := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if key == "" {
			writeError(w, http.StatusUnauthorized, "X-API-Key requerido")
			return
		}
		si, err := h.m2m.AuthenticateAPIKey(r.Context(), key)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		tok, err := h.m2m.IssueServiceToken(r.Context(), in.IssueServiceTokenInput{
			ClientID: si.ClientID,
			TenantID: si.TenantID,
			Scopes:   si.Scopes,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo emitir el service token")
			return
		}
		writeJSON(w, http.StatusOK, serviceTokenDTO{
			Token:     tok.Token,
			TokenType: "Bearer",
			ExpiresAt: tok.ExpiresAt.UTC().Format(rfc3339),
		})
	})
}
