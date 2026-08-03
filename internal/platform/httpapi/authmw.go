package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// Identity es la identidad autenticada que Authenticate inyecta en el contexto
// del request. Es la representación PLANA multi-tenant de wApp (Decisión C): el
// tenant SIEMPRE sale del token (INV-8), nunca del cuerpo.
//
// Desde la Ola 5 del Plan 003 de identity tiene UNA sola forma —la persona que
// porta un Context Token de wApp— porque el plano M2M se retiró entero: nadie
// tenía credencial y arrastrarlo era conservar un IAM propio sin uso (design.md
// Ola 5 §7). Cablear M2M contra identity, cuando haga falta, es el modelo del
// ADR-0025: se canjea una credencial, no se presenta.
//
// Los consumidores la leen con IdentityFromContext y autorizan con
// RequirePermission; NO deben re-derivar el tenant de otra fuente.
type Identity struct {
	// TenantID es el tenant al que se acota TODA la operación (del token, INV-8).
	TenantID string
	// Subject es el user_id de la persona (el `sub` que acreditó identity).
	Subject string
	// Roles son los roles del usuario EN WAPP (los resuelve el canje).
	Roles []string
	// Grants son los permisos efectivos del usuario, ya resueltos al emitir el
	// token.
	Grants identityrbac.Grants
}

// identityCtxKey es la clave PRIVADA del contexto (evita colisiones entre
// paquetes; solo este paquete puede leer/escribir la Identity).
type identityCtxKey struct{}

// WithIdentity devuelve un contexto derivado que porta la Identity. Lo usa
// Authenticate; expuesto para tests y para composición en T4/T5.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// IdentityFromContext extrae la Identity inyectada por Authenticate. ok=false si
// el request no pasó por el middleware (sin identidad). Los handlers protegidos
// toman el tenant_id de aquí (INV-8), NO del cuerpo.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

// UserTokenValidator valida un access token de USUARIO y devuelve sus claims
// (incluidos los grants efectivos). Lo satisface *sharedjwt.JWTManager de
// wapp-shared/auth. Se usa directamente la primitiva de validación (no el
// usecase Verify) porque el middleware necesita los GRANTS para autorizar, y
// VerifyResult los omite a propósito (design.md §7).
type UserTokenValidator interface {
	ValidateToken(token string) (*sharedjwt.Claims, error)
}

// Middleware agrupa la autenticación (Bearer con Context Token de wApp) y la
// autorización RBAC de la API pública. Es REUTILIZABLE: envuelve /admin/* y las
// rutas /api/v1 con Authenticate → RequirePermission.
type Middleware struct {
	users UserTokenValidator
	log   sharedlogger.Logger
}

// NewMiddleware construye el middleware con el validador de Context Tokens. El
// logger es opcional (puede ser nil: los rechazos se registran a Debug si está
// presente; JAMÁS se loguea el token ni el secreto).
func NewMiddleware(users UserTokenValidator, log sharedlogger.Logger) *Middleware {
	return &Middleware{users: users, log: log}
}

// Authenticate resuelve la identidad del request y la inyecta en el contexto:
// Authorization: Bearer <context-token> validado con ValidateToken. Sin
// credencial válida responde 401 y NO llama a next.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := m.resolve(r)
		if !ok {
			m.deny(r, http.StatusUnauthorized)
			writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// resolve deriva la Identity de las credenciales del request (sin efectos de
// escritura). ok=false si no hay credencial o no valida.
func (m *Middleware) resolve(r *http.Request) (Identity, bool) {
	tok, ok := bearerToken(r)
	if !ok {
		return Identity{}, false
	}

	// El `token_use` se exige explícitamente en vez de darse por hecho: aceptar
	// cualquier token bien firmado sería aceptar también los que un día se
	// emitan para otra cosa.
	claims, err := m.users.ValidateToken(tok)
	if err != nil || claims.TokenUse == sharedjwt.TokenUseService {
		return Identity{}, false
	}
	return Identity{
		TenantID: claims.TenantID,
		Subject:  claims.UserID,
		Roles:    claims.Roles,
		Grants:   claims.Grants,
	}, true
}

// RequirePermission devuelve un middleware que exige el permiso `recurso.accion`
// (glob RBAC): evalúa los grants con identityrbac.EvaluateGrants (default DENY,
// deny precede allow). Debe montarse DESPUÉS de Authenticate (necesita la
// Identity en el contexto): 401 si no hay identidad, 403 si el permiso no se
// cumple.
func (m *Middleware) RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := IdentityFromContext(r.Context())
			if !ok {
				m.deny(r, http.StatusUnauthorized)
				writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
				return
			}

			if !identityrbac.EvaluateGrants(id.Grants, perm) {
				m.deny(r, http.StatusForbidden)
				writeAuthError(w, http.StatusForbidden, "permiso denegado")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WhoAmIHandler devuelve la Identity autenticada del contexto (tenant, subject,
// roles). Es el ejemplo de referencia de cómo un handler protegido lee la
// identidad (IdentityFromContext) sin tocar el cuerpo; se monta detrás de
// Authenticate. Sirve además de humo de extremo a extremo del middleware.
func WhoAmIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id": id.TenantID,
			"subject":   id.Subject,
			"roles":     id.Roles,
		})
	})
}

// bearerToken extrae el token del header Authorization: Bearer <token>. ok=false
// si falta o el esquema no es Bearer.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// deny registra un rechazo de auth a Debug SIN filtrar el token/secreto (solo
// método, ruta y código). No-op si el logger es nil.
func (m *Middleware) deny(r *http.Request, code int) {
	if m.log == nil {
		return
	}
	m.log.Debug("acceso denegado", "method", r.Method, "path", r.URL.Path, "status", code)
}

// writeAuthError responde un error de auth como JSON tipado {error}.
func writeAuthError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeJSON serializa v como JSON con el código dado. Helper compartido por el
// middleware y WhoAmIHandler.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "error codificando respuesta", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}
