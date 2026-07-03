package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-shared/auth"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// Identity es la identidad autenticada que Authenticate inyecta en el contexto
// del request. Es la representación PLANA multi-tenant de wApp (Decisión C): el
// tenant SIEMPRE sale del token (INV-8), nunca del cuerpo. Un mismo tipo cubre
// las dos caras del middleware:
//
//   - Usuario (JWT Bearer): Subject = user_id, Roles + Grants efectivos (RBAC glob).
//   - Servicio M2M (X-API-Key o service token): Subject = client_id, Scopes;
//     IsService = true.
//
// Los consumidores (T4/T5) la leen con IdentityFromContext y autorizan con
// RequirePermission; NO deben re-derivar el tenant de otra fuente.
type Identity struct {
	// TenantID es el tenant al que se acota TODA la operación (del token, INV-8).
	TenantID string
	// Subject es el user_id (usuario) o el client_id (M2M).
	Subject string
	// Roles son los roles del usuario (vacío en M2M).
	Roles []string
	// Grants son los permisos efectivos del usuario, ya resueltos al emitir el
	// token (vacío en M2M).
	Grants auth.Grants
	// Scopes son los permisos concedidos a la api-key/service token (vacío en
	// usuario).
	Scopes []string
	// IsService distingue la identidad M2M (true) de la de usuario (false).
	IsService bool
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
// (incluidos los grants efectivos). Lo satisface *auth.JWTManager de
// wapp-shared/auth. Se usa directamente la primitiva de validación (no el
// usecase Verify) porque el middleware necesita los GRANTS para autorizar, y
// VerifyResult los omite a propósito (design.md §7).
type UserTokenValidator interface {
	ValidateToken(token string) (*auth.Claims, error)
}

// ServiceAuthenticator resuelve identidades M2M (api-key o service token) y
// autoriza scopes. Lo satisface *usecase.M2MService (superset de
// in.M2MAuthenticator).
type ServiceAuthenticator interface {
	AuthenticateAPIKey(ctx context.Context, rawKey string) (in.ServiceIdentity, error)
	VerifyServiceToken(ctx context.Context, token string) (in.ServiceIdentity, error)
	AuthorizeScope(scopes []string, required string) bool
}

// Middleware agrupa la autenticación (Bearer usuario | X-API-Key | service
// token) y la autorización RBAC/scope de la API pública. Es REUTILIZABLE: T4
// envuelve /admin/* y T5 las rutas /api/v1 con Authenticate → RequirePermission.
type Middleware struct {
	users UserTokenValidator
	svc   ServiceAuthenticator
	log   sharedlogger.Logger
}

// NewMiddleware construye el middleware con el validador de tokens de usuario y
// el autenticador M2M. El logger es opcional (puede ser nil: los rechazos se
// registran a Debug si está presente; JAMÁS se loguea el token ni el secreto).
func NewMiddleware(users UserTokenValidator, svc ServiceAuthenticator, log sharedlogger.Logger) *Middleware {
	return &Middleware{users: users, svc: svc, log: log}
}

// Authenticate resuelve la identidad del request y la inyecta en el contexto.
// Distingue: X-API-Key → AuthenticateAPIKey; Authorization: Bearer <jwt> →
// primero como token de usuario (ValidateToken), y si no es de usuario
// (token_use=service o firma no válida) cae a service token
// (VerifyServiceToken). Sin credencial válida responde 401 y NO llama a next.
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
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		si, err := m.svc.AuthenticateAPIKey(r.Context(), key)
		if err != nil {
			return Identity{}, false
		}
		return serviceIdentity(si), true
	}

	tok, ok := bearerToken(r)
	if !ok {
		return Identity{}, false
	}

	// Token de usuario primero: si valida y NO es un service token, es un usuario
	// con sus grants efectivos embebidos.
	if claims, err := m.users.ValidateToken(tok); err == nil && claims.TokenUse != auth.TokenUseService {
		return Identity{
			TenantID: claims.TenantID,
			Subject:  claims.UserID,
			Roles:    claims.Roles,
			Grants:   claims.Grants,
		}, true
	}

	// Si no era de usuario, se prueba como service token M2M.
	if si, err := m.svc.VerifyServiceToken(r.Context(), tok); err == nil {
		return serviceIdentity(si), true
	}

	return Identity{}, false
}

// RequirePermission devuelve un middleware que exige el permiso `recurso.accion`
// (glob RBAC). Para usuario evalúa los grants con auth.EvaluateGrants
// (default DENY, deny precede allow); para M2M evalúa el scope con
// AuthorizeScope. Debe montarse DESPUÉS de Authenticate (necesita la Identity en
// el contexto): 401 si no hay identidad, 403 si el permiso no se cumple.
func (m *Middleware) RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := IdentityFromContext(r.Context())
			if !ok {
				m.deny(r, http.StatusUnauthorized)
				writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
				return
			}

			var allowed bool
			if id.IsService {
				allowed = m.svc.AuthorizeScope(id.Scopes, perm)
			} else {
				allowed = auth.EvaluateGrants(id.Grants, perm)
			}
			if !allowed {
				m.deny(r, http.StatusForbidden)
				writeAuthError(w, http.StatusForbidden, "permiso denegado")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WhoAmIHandler devuelve la Identity autenticada del contexto (tenant, subject,
// roles/scopes). Es el ejemplo de referencia de cómo un handler protegido lee la
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
			"tenant_id":  id.TenantID,
			"subject":    id.Subject,
			"roles":      id.Roles,
			"scopes":     id.Scopes,
			"is_service": id.IsService,
		})
	})
}

// serviceIdentity mapea una identidad M2M del IAM a la Identity del contexto.
func serviceIdentity(si in.ServiceIdentity) Identity {
	return Identity{
		TenantID:  si.TenantID,
		Subject:   si.ClientID,
		Scopes:    si.Scopes,
		IsService: true,
	}
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
