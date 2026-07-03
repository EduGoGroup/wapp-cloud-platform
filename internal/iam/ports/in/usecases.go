// Package in declara los puertos DE ENTRADA del módulo IAM: las interfaces de
// caso de uso que la capa de transporte (T3, endpoints /api/v1 + middleware)
// invocará, más sus DTOs de entrada/salida. Es el CONTRATO estable entre el
// dominio y el transporte: T3 depende de estas interfaces, no de las structs
// concretas de usecase.
//
// Regla dura (INV-8): toda operación acotada a un tenant recibe el tenant_id del
// CONTEXTO de identidad (del token), nunca del cuerpo del request. Los DTOs que
// llevan TenantID esperan ese valor ya resuelto por el middleware.
package in

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

// ---------------------------------------------------------------------------
// Autenticación de usuario (login/refresh/logout/verify) — design.md §8
// ---------------------------------------------------------------------------

// LoginInput es la credencial de login. TenantID es OPCIONAL: si se conoce el
// tenant (subdominio/cabecera) el login se acota a él; si va vacío se resuelve
// el usuario por email de forma global. Password viaja en claro SOLO en tránsito
// (TLS); nunca se persiste (se compara contra el hash bcrypt).
type LoginInput struct {
	Email    string
	Password string
	TenantID string // opcional
}

// RefreshInput porta el refresh token OPACO en claro que el cliente recibió.
type RefreshInput struct {
	RefreshToken string
}

// LogoutInput revoca sesión. RefreshToken revoca ese token; si AllSessions es
// true y UserID va informado, revoca todos los refresh del usuario.
type LogoutInput struct {
	RefreshToken string
	UserID       string
	AllSessions  bool
}

// VerifyResult es el resultado de validar un access token. Valid=false (sin
// error) si el token es inválido o expiró; los campos restantes solo son
// significativos con Valid=true.
type VerifyResult struct {
	Valid     bool
	TenantID  string
	Subject   string
	Roles     []string
	ExpiresAt time.Time
}

// Authenticator es el puerto de autenticación de usuario.
type Authenticator interface {
	// Login valida email+password, resuelve los grants EFECTIVOS al emitir
	// (cadena de roles ⊕ overrides), firma el access token y persiste el refresh.
	// Devuelve domain.ErrInvalidCredentials/ErrUserInactive en fallo.
	Login(ctx context.Context, in LoginInput) (domain.AuthResult, error)
	// Refresh valida el refresh (hash, no revocado, no expirado), RE-RESUELVE los
	// grants, emite un access nuevo y ROTA el refresh. domain.ErrRefreshInvalid en
	// fallo.
	Refresh(ctx context.Context, in RefreshInput) (domain.AuthResult, error)
	// Logout revoca el/los refresh token(s). Idempotente.
	Logout(ctx context.Context, in LogoutInput) error
	// Verify valida un access token y devuelve sus claims (Valid=false si no vale).
	Verify(ctx context.Context, accessToken string) (VerifyResult, error)
}

// ---------------------------------------------------------------------------
// Autenticación M2M (api-key / service token) — design.md §7/§8
// ---------------------------------------------------------------------------

// ServiceIdentity es la identidad M2M resuelta desde una api-key o un service
// token: el tenant al que se acota y los scopes concedidos.
type ServiceIdentity struct {
	TenantID string
	ClientID string
	Scopes   []string
}

// IssueServiceTokenInput pide un service token para un cliente M2M ya validado.
type IssueServiceTokenInput struct {
	ClientID string
	TenantID string
	Scopes   []string
}

// ServiceTokenResult es el service token firmado y su expiración.
type ServiceTokenResult struct {
	Token     string
	ExpiresAt time.Time
}

// M2MAuthenticator es el puerto de autenticación máquina-a-máquina.
type M2MAuthenticator interface {
	// AuthenticateAPIKey valida el secreto en claro de una api-key (por su hash),
	// verifica que esté activa/no revocada/no expirada, refresca last_used y
	// devuelve la identidad+scopes. domain.ErrAPIKeyInvalid en fallo.
	AuthenticateAPIKey(ctx context.Context, rawKey string) (ServiceIdentity, error)
	// IssueServiceToken firma un service token de corta vida con los scopes dados.
	IssueServiceToken(ctx context.Context, in IssueServiceTokenInput) (ServiceTokenResult, error)
	// VerifyServiceToken valida un service token y devuelve su identidad+scopes.
	VerifyServiceToken(ctx context.Context, token string) (ServiceIdentity, error)
	// AuthorizeScope decide si los scopes cubren el permiso pedido (glob RBAC,
	// mismo matcher que los grants).
	AuthorizeScope(scopes []string, required string) bool
}

// ---------------------------------------------------------------------------
// Gestión de usuarios/roles/api-keys (CRUD mínimo) — design.md §8
// ---------------------------------------------------------------------------

// CreateUserInput crea un operador del tenant. Password se hashea con bcrypt
// (nunca se persiste en claro). RoleIDs asigna roles de arranque (opcional).
type CreateUserInput struct {
	TenantID string
	Email    string
	Password string
	RoleIDs  []string
}

// UserManager gestiona el ciclo de vida de usuarios y su relación con roles.
type UserManager interface {
	CreateUser(ctx context.Context, in CreateUserInput) (domain.User, error)
	GetUser(ctx context.Context, tenantID, id string) (domain.User, error)
	ListUsers(ctx context.Context, tenantID string) ([]domain.User, error)
	DeleteUser(ctx context.Context, tenantID, id string) error
	AssignRole(ctx context.Context, tenantID, userID, roleID string) error
	UnassignRole(ctx context.Context, tenantID, userID, roleID string) error
	AddUserGrant(ctx context.Context, tenantID, userID string, g domain.Grant) error
	RemoveUserGrant(ctx context.Context, tenantID, userID string, g domain.Grant) error
}

// CreateRoleInput crea un rol custom del tenant con sus grants iniciales.
type CreateRoleInput struct {
	TenantID     string
	Name         string
	ParentRoleID *string
	Grants       []domain.Grant
}

// RoleManager gestiona roles custom del tenant y sus grants.
type RoleManager interface {
	CreateRole(ctx context.Context, in CreateRoleInput) (domain.Role, error)
	ListRoles(ctx context.Context, tenantID string) ([]domain.Role, error)
	AddGrant(ctx context.Context, tenantID, roleID string, g domain.Grant) error
	RemoveGrant(ctx context.Context, tenantID, roleID string, g domain.Grant) error
}

// IssueAPIKeyInput emite una api-key M2M. ExpiresAt opcional (nil = sin caducar).
type IssueAPIKeyInput struct {
	TenantID  string
	ClientID  string
	Scopes    []string
	ExpiresAt *time.Time
}

// IssueAPIKeyResult devuelve la api-key creada MÁS el secreto en claro. El
// secreto se devuelve UNA sola vez (en BD solo vive su hash); el caller debe
// entregarlo al cliente y NO persistirlo.
type IssueAPIKeyResult struct {
	APIKey domain.APIKey
	Secret string
}

// APIKeyManager gestiona el ciclo de vida de las api-keys M2M.
type APIKeyManager interface {
	IssueAPIKey(ctx context.Context, in IssueAPIKeyInput) (IssueAPIKeyResult, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]domain.APIKey, error)
	RevokeAPIKey(ctx context.Context, tenantID, id string) error
}

// ---------------------------------------------------------------------------
// Auditoría — design.md §7 (CERO PII)
// ---------------------------------------------------------------------------

// AuditInput registra un evento de auditoría. TenantID vacío = evento pre-auth.
// Actor/Resource deben ser identidades OPACAS (ids), NUNCA email/número ni
// contenido; Meta, contexto NO sensible.
type AuditInput struct {
	TenantID string
	Actor    string
	Action   string
	Resource string
	Result   string
	Meta     map[string]any
}

// Auditor registra y consulta la bitácora de auditoría.
type Auditor interface {
	Record(ctx context.Context, in AuditInput) error
	ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}
