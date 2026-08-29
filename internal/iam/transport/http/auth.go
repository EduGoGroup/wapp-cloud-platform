// Package iamhttp expone la superficie HTTP pública de autenticación del
// listener :8103: /api/v1/auth/{verify,exchange}. Es la capa de transporte:
// traduce JSON ⇄ DTOs de los puertos in (TokenVerifier, Exchanger) y mapea los
// errores tipados del dominio a códigos HTTP. NO contiene lógica de negocio
// (vive en internal/iam/usecase) ni conoce SQL.
//
// Las DOS rutas que quedan tras la Ola 5 del Plan 003 de identity son las dos
// caras del Context Token de wApp: `exchange` lo emite a cambio de un Identity
// Token del SSO, y `verify` lo inspecciona. Aquí ya NO se validan contraseñas ni
// se emiten refresh: /login, /refresh, /logout y /token murieron con el IAM
// propio (REQ-A1), y lo que responden ahora es 404.
//
// Son rutas PÚBLICAS (sin token previo): establecen o inspeccionan credenciales.
// La autorización RBAC de las rutas de negocio la aporta el middleware
// httpapi.Authenticate → RequirePermission.
package iamhttp

import (
	"encoding/json"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// AuthHandler sirve los endpoints de autenticación. Depende SOLO de los puertos
// in (interfaces), no de las structs concretas de usecase.
type AuthHandler struct {
	// verifier inspecciona los Context Tokens que wApp emite. No sale a
	// identity: ese token lo firmó wApp.
	verifier in.TokenVerifier
	// exchange canjea Identity Tokens de identity-core por Context Tokens de
	// wApp (identity Plan 003 · T3.1). Es nil cuando el modo dual está apagado
	// (WAPP_IDENTITY_JWKS_URL vacía): el endpoint existe igual y responde 503.
	exchange in.Exchanger
	log      sharedlogger.Logger
}

// NewAuthHandler construye el handler de autenticación.
func NewAuthHandler(verifier in.TokenVerifier, exchange in.Exchanger, log sharedlogger.Logger) *AuthHandler {
	return &AuthHandler{verifier: verifier, exchange: exchange, log: log}
}

// Register monta las rutas /api/v1/auth/* en el mux dado (listener público
// :8103). Sigue el idiom del repo: paths planos + verificación de método dentro
// de cada handler.
func Register(mux *http.ServeMux, verifier in.TokenVerifier, exchange in.Exchanger, log sharedlogger.Logger) {
	h := NewAuthHandler(verifier, exchange, log)
	mux.Handle("/api/v1/auth/verify", h.Verify())
	mux.Handle("/api/v1/auth/exchange", h.Exchange())
}

// ---------------------------------------------------------------------------
// DTOs de request/response (wire format estable de /api/v1/auth)
// ---------------------------------------------------------------------------

type verifyRequest struct {
	Token string `json:"token,omitempty"`
}

type exchangeRequest struct {
	IdentityToken string `json:"identity_token"`
}

// exchangeResultDTO es la salida del canje: SOLO el Context Token. No lleva
// refresh a propósito (identity Plan 003 · design.md Ola 3 §3): el refresh es de
// identity y vive donde vive la sesión.
type exchangeResultDTO struct {
	ContextToken string             `json:"context_token"`
	TokenType    string             `json:"token_type"`
	ExpiresAt    string             `json:"expires_at"`
	Context      identityContextDTO `json:"context"`
}

type identityContextDTO struct {
	TenantID string   `json:"tenant_id"`
	UserID   string   `json:"user_id"`
	Roles    []string `json:"roles"`
}

type verifyResultDTO struct {
	Valid     bool     `json:"valid"`
	TenantID  string   `json:"tenant_id,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

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
		v, err := h.verifier.Verify(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo validar el token")
			return
		}
		writeJSON(w, http.StatusOK, toVerifyResultDTO(v))
	})
}

// Exchange canjea un Identity Token de identity-core por un Context Token de
// wApp (identity Plan 003 · T3.1). 200 éxito; 400 cuerpo inválido; 401 token no
// aceptable o sujeto sin migrar; 503 con el modo dual apagado o identity
// inalcanzable.
//
// 🔧 El 409 de «sujeto con más de un tenant» YA NO EXISTE (Plan 047 · Ola 5 ·
// T5.1, D-047.14): varias membresías dejaron de ser un fallo. El canje resuelve
// con la empresa ACTIVA que esa persona eligió, y si no ha elegido ninguna
// válida devuelve 200 con un token SIN empresa — el mismo del caso «cero»— para
// que la consola pinte el selector.
//
// Es la puerta por la que entra la identidad del SSO: aquí NO se validan
// credenciales (eso ya es de identity) ni se emite refresh (eso es de la sesión
// que identity custodia).
func (h *AuthHandler) Exchange() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if h.exchange == nil {
			// La misma puerta que dejó la Ola 1, ahora con alguien detrás: sin
			// WAPP_IDENTITY_JWKS_URL no hay verificador y no hay canje posible.
			writeError(w, http.StatusServiceUnavailable, "modo dual apagado: identity no está configurado en este despliegue")
			return
		}
		var req exchangeRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		res, err := h.exchange.Exchange(r.Context(), in.ExchangeInput{IdentityToken: req.IdentityToken})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, exchangeResultDTO{
			ContextToken: res.ContextToken,
			TokenType:    "Bearer",
			ExpiresAt:    res.ExpiresAt.UTC().Format(rfc3339),
			Context: identityContextDTO{
				TenantID: res.Context.TenantID,
				UserID:   res.Context.UserID,
				Roles:    res.Context.Roles,
			},
		})
	})
}
