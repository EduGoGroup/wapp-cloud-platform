// Package domain define las entidades PURAS del módulo IAM (Plan 018 · T2) y sus
// errores tipados. No conoce SQL ni HTTP: es el núcleo hexagonal que consumen
// los usecases y que los adaptadores (infra/postgres, infra/memory) materializan.
//
// Convención del repo (verdad de campo): los identificadores UUID viajan como
// `string` (nunca uuid.UUID); en Postgres se leen casteados a texto (`::text`).
// Los campos nullable de BD se modelan como punteros (nil = ausente/NULL).
// CERO PII y CERO material de la doble llave (DEK/lease) viven aquí (ADR-0007/0009).
package domain

import "time"

// Effect es el efecto de un grant en la evaluación RBAC glob (design.md §5).
// deny precede a allow; el default es DENY (lo aplica auth.EvaluateGrants).
type Effect string

const (
	// EffectAllow concede el patrón.
	EffectAllow Effect = "allow"
	// EffectDeny niega el patrón (precede a cualquier allow al evaluar).
	EffectDeny Effect = "deny"
)

// User es un operador del tenant que se autentica contra la Plataforma Cloud
// (tabla public.iam_users, migración 0014). El email es dato OPERATIVO del
// tenant EN CLARO (permite el login por email), NO un contacto WhatsApp
// (design.md §4, INV-5). PasswordHash es bcrypt (wapp-shared/auth); NUNCA la
// contraseña en claro. DeletedAt nil = usuario activo (soft-delete).
type User struct {
	ID           string
	TenantID     string
	Email        string
	PasswordHash string
	IsActive     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// Role es un rol RBAC (tabla public.iam_roles, migración 0015). TenantID nil =
// PLANTILLA global canónica (tenant_admin/operator/viewer sembrados en T1),
// referenciable por cualquier tenant; TenantID set = rol custom del tenant.
// ParentRoleID modela la herencia de grants (cadena, auth.ResolveRoleChain).
type Role struct {
	ID           string
	TenantID     *string // nil = plantilla global
	Name         string
	ParentRoleID *string // nil = raíz
	CreatedAt    time.Time
}

// Grant es un patrón de permiso glob `recurso.accion` con su efecto
// (public.iam_role_grants / public.iam_user_grants). Es la unidad que se agrega
// (rol + cadena ⊕ overrides de usuario) para formar los grants EFECTIVOS que se
// embeben en el token al emitir (design.md §5). CERO PII.
type Grant struct {
	Pattern string
	Effect  Effect
}

// RefreshToken es un refresh token OPACO persistido (tabla
// public.iam_refresh_tokens, migración 0017). SOLO se guarda el TokenHash
// (SHA256, wapp-shared/auth), NUNCA el token en claro. RevokedAt nil = vigente;
// set = revocado (logout). ExpiresAt marca el vencimiento natural.
type RefreshToken struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// APIKey es una credencial M2M de terceros de la API pública (tabla
// public.iam_api_keys, migración 0018). SOLO se guarda el KeyHash (SHA256) del
// secreto, NUNCA el secreto en claro (se devuelve UNA vez al emitir, design.md
// §8). Scopes[] gobierna los permisos M2M (glob recurso.accion). RevokedAt/
// ExpiresAt/LastUsedAt nil = sin revocar / sin caducidad / nunca usada.
type APIKey struct {
	ID         string
	TenantID   string
	ClientID   string
	KeyHash    string
	Scopes     []string
	IsActive   bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// AuditEvent es una fila de la bitácora append-only de auditoría (tabla
// public.audit_events, migración 0019). REGLA DURA (INV-5): CERO PII. Actor y
// Resource son identidades OPACAS (UUID de user/client/recurso), NUNCA email,
// número/JID de contacto ni contenido de mensajes. Meta transporta contexto NO
// sensible (endpoint, método, código). TenantID nil = evento pre-auth (p.ej.
// login fallido sin tenant resuelto).
type AuditEvent struct {
	ID       int64
	TenantID *string
	Actor    string
	Action   string
	Resource string
	Result   string
	Meta     map[string]any
	At       time.Time
}

// IdentityContext es la identidad multi-tenant PLANA de wApp (Decisión C): solo
// {TenantID, UserID, Roles}. La devuelve el login/refresh para que el cliente
// conozca su contexto; los grants efectivos ya viajan en el access token.
type IdentityContext struct {
	TenantID string
	UserID   string
	Roles    []string
}

// AuthResult es el resultado de un login/refresh: el par de tokens y el
// contexto de identidad. RefreshToken es el token OPACO en CLARO, entregado UNA
// vez al cliente (en BD solo vive su hash). ExpiresAt es la expiración del
// AccessToken. TokenType es siempre "Bearer".
type AuthResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time
	Context      IdentityContext
}
