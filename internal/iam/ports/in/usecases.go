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

// TokenVerifier inspecciona un Context Token de wApp. Es un puerto PROPIO y no
// un método suelto de Authenticator porque son dos superficies con vidas
// distintas: el canje delegó fuera el login/refresh/logout, pero el token que
// wApp emite lo sigue firmando wApp y solo wApp puede decir si vale.
//
// Lo consume /api/v1/auth/verify (el listener público) y lo satisfacen tanto
// ContextTokenService como DelegatedAuthService.
type TokenVerifier interface {
	// Verify valida un Context Token y devuelve sus claims. Un token inválido o
	// expirado devuelve Valid=false SIN error: no es un fallo de la operación,
	// es su respuesta.
	Verify(ctx context.Context, accessToken string) (VerifyResult, error)
}

// Authenticator es el puerto de autenticación de usuario: lo consume el gateway
// CloudLink, que relaya el login/refresh/logout del operador del Edge, y lo
// implementa DelegatedAuthService contra identity-core.
type Authenticator interface {
	// Login acredita a la persona y devuelve el par de tokens con su contexto de
	// negocio. Devuelve domain.ErrInvalidCredentials/ErrUserInactive en fallo.
	Login(ctx context.Context, in LoginInput) (domain.AuthResult, error)
	// Refresh rota la sesión y RE-RESUELVE los grants, de modo que un cambio de
	// rol entra sin volver a pedir la contraseña. domain.ErrRefreshInvalid en fallo.
	Refresh(ctx context.Context, in RefreshInput) (domain.AuthResult, error)
	// Logout revoca la sesión (o todas, con AllSessions). Idempotente.
	Logout(ctx context.Context, in LogoutInput) error
	TokenVerifier
}

// ---------------------------------------------------------------------------
// Canje de Identity Token por Context Token (identity Plan 003 · T3.1)
// ---------------------------------------------------------------------------

// ExchangeInput porta el Identity Token que emitió identity-core. Es la ÚNICA
// entrada del canje: el tenant no se pide ni se acepta del cliente (se resuelve
// de la membresía, INV-8) y el sujeto sale del `sub` firmado, nunca del cuerpo.
type ExchangeInput struct {
	IdentityToken string
}

// ExchangeResult es el Context Token de wApp emitido a cambio. NO lleva refresh:
// el refresh es de identity y vive donde vive la sesión (identity ADR-0003). Su
// ExpiresAt está acotado por el `exp` del Identity Token de origen.
type ExchangeResult struct {
	ContextToken string
	ExpiresAt    time.Time
	Context      domain.IdentityContext
}

// Exchanger canjea la identidad que emite el SSO del grupo por el contexto de
// negocio de wApp. Es la frontera entre los dos mundos: identity acredita QUIÉN
// es la persona y wApp le pone encima SU tenant y SUS grants.
type Exchanger interface {
	// Exchange valida el Identity Token, resuelve tenant y grants efectivos del
	// sujeto y emite el Context Token. Devuelve domain.ErrIdentityTokenInvalid,
	// ErrIdentityTokenExpiring, ErrMultipleTenants, ErrUserInactive o
	// ErrIdentityUnavailable en fallo. Cero membresías emite un token sin tenant (D-056.12).
	Exchange(ctx context.Context, in ExchangeInput) (ExchangeResult, error)
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
