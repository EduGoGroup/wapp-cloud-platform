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
	"encoding/json"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// rfc3339 es el formato de los instantes de expiración en el wire. Común a todo el paquete
// (roles.go e invitations.go también lo usan); vivía en http.go y está aquí porque este es su
// primer consumidor (ver la cabecera de http.go).
const rfc3339 = time.RFC3339

// AuthHandler sirve los endpoints de autenticación. Depende SOLO de los puertos in
// (in.TokenVerifier, in.Exchanger), no de las structs concretas de usecase.
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

// NewAuthHandler construye el handler de autenticación. exchange puede ser nil: es el modo dual
// APAGADO (sin WAPP_IDENTITY_JWKS_URL), y entonces Exchange responde 503. log puede ser nil.
func NewAuthHandler(verifier in.TokenVerifier, exchange in.Exchanger, log sharedlogger.Logger) *AuthHandler {
	return &AuthHandler{verifier: verifier, exchange: exchange, log: log}
}

// Register monta en mux EXACTAMENTE dos rutas, por PATH pelado (sin verbo: el método lo
// comprueba cada handler): /api/v1/auth/verify (Verify) y /api/v1/auth/exchange (Exchange).
//
// R-H3: el IAM viejo NO está en el cable. /api/v1/auth/login, /refresh, /logout y /token no se
// montan, así que un mux con solo esto responde 404 —no 401: un 401 diría que la ruta sigue ahí
// y ha rechazado la credencial—.
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

// exchangeRequest es el cuerpo de POST /api/v1/auth/exchange.
//
// 🔴 UN SOLO CAMPO, Y ES INV-8 ESCRITO EN UN TIPO (R-H5): un `tenant_id` aquí viajaría en cada
// re-canje desatendido de los consumidores web (cada ~13 min). La empresa se elige en POST
// /api/v1/auth/active-tenant y se guarda en el servidor. Que siga siendo un campo lo vigila un
// test que compara el conjunto EXACTO de claves JSON del struct (auth_test.go).
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

// toVerifyResultDTO proyecta el VerifyResult del puerto al wire format. Solo
// serializa los campos de identidad cuando Valid=true. (Vivía en http.go: ver su cabecera.)
func toVerifyResultDTO(v in.VerifyResult) verifyResultDTO {
	dto := verifyResultDTO{Valid: v.Valid}
	if v.Valid {
		dto.TenantID = v.TenantID
		dto.Subject = v.Subject
		dto.Roles = v.Roles
		if !v.ExpiresAt.IsZero() {
			dto.ExpiresAt = v.ExpiresAt.UTC().Format(rfc3339)
		}
	}
	return dto
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
