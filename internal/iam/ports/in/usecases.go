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
	// ErrIdentityTokenExpiring, ErrUserInactive o ErrIdentityUnavailable en
	// fallo.
	//
	// NINGÚN número de membresías es un fallo: cero emite un token sin tenant
	// (D-056.12) y varias resuelven por la empresa ACTIVA, que también emite un
	// token sin tenant cuando no hay elección válida (Plan 047 · Ola 5 · T5.1,
	// D-047.14).
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

// ---------------------------------------------------------------------------
// Administración de roles, grants y membresías (Plan 047 · Ola 1.0 · T1.0-1/2)
// ---------------------------------------------------------------------------

// Caller es QUIÉN está llamando, resuelto del contexto de identidad. Es el
// origen ÚNICO del tenant en los usecases de administración (INV-04): fíjate en
// que ningún Input de abajo tiene campo TenantID, y esa ausencia es la regla
// escrita en el tipo — lo que no existe no se puede leer del cuerpo por
// descuido.
type Caller struct {
	// TenantID es la empresa a la que se acota TODA la operación. Vacío es un
	// caso legítimo del canje (D-056.12: token sin empresa todavía) y los
	// usecases lo rechazan con domain.ErrNoTenant.
	TenantID string
	// UserID es el sujeto que opera (el `sub` que acreditó identity).
	UserID string
}

// CallerResolver extrae el Caller del contexto del request. Es un puerto y no
// una llamada directa a httpapi.IdentityFromContext por dirección de
// dependencias: internal/platform/httpapi ya importa este paquete, y que el
// usecase importara el transporte invertiría la flecha del módulo.
//
// El transporte lo satisface con una línea sobre lo que ya tiene:
//
//	in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
//		id, ok := httpapi.IdentityFromContext(ctx)
//		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
//	})
type CallerResolver interface {
	// Caller devuelve la identidad del contexto. ok=false si el request no pasó
	// por el middleware de autenticación.
	Caller(ctx context.Context) (Caller, bool)
}

// CallerResolverFunc adapta una función al puerto CallerResolver.
type CallerResolverFunc func(ctx context.Context) (Caller, bool)

// Caller implementa CallerResolver.
func (f CallerResolverFunc) Caller(ctx context.Context) (Caller, bool) { return f(ctx) }

// CreateRoleInput es el alta de un rol CUSTOM del tenant. No lleva tenant: el
// rol nace acotado a la empresa del Caller, siempre.
type CreateRoleInput struct {
	// Name es el nombre del rol dentro del tenant (único por tenant).
	Name string
	// ParentRoleID es el rol del que hereda grants (opcional). Debe ser visible
	// para el tenant del Caller: uno suyo o una plantilla global.
	ParentRoleID *string
}

// RoleAssignmentInput asigna o retira un rol a una persona DEL TENANT del
// Caller. UserID es el UUID de identity (aquí no hay padrón local).
type RoleAssignmentInput struct {
	UserID string
	RoleID string
}

// RoleGrantInput concede o revoca un grant sobre un ROL del tenant.
type RoleGrantInput struct {
	RoleID string
	Grant  domain.Grant
}

// UserGrantInput concede o revoca un override de grant sobre una PERSONA.
//
// ⚠️ iam_user_grants no tiene columna de tenant: el override es del usuario, no
// de la pareja (usuario, empresa). Lo que lo mantiene acotado es que el usecase
// exige que esa persona sea miembro de la empresa del Caller, más la guarda de
// una sola empresa por usuario (out.MembershipRepo.Add).
type UserGrantInput struct {
	UserID string
	Grant  domain.Grant
}

// RoleAdmin es la administración de RBAC del tenant: lo que hasta el Plan 047
// solo existía como repositorio y no tenía por dónde ejercerse.
//
// TODAS sus operaciones se acotan al tenant del Caller. Un recurso de otra
// empresa se contesta con domain.ErrNotFound —no con "prohibido"— para no
// filtrar qué existe fuera.
type RoleAdmin interface {
	// ListRoles devuelve los roles visibles: los del tenant más las plantillas
	// globales.
	ListRoles(ctx context.Context) ([]domain.Role, error)
	// CreateRole crea un rol custom del tenant. domain.ErrConflict si el nombre
	// ya existe en esa empresa.
	CreateRole(ctx context.Context, input CreateRoleInput) (domain.Role, error)
	// AssignRole asigna un rol visible a un miembro del tenant. La asignación
	// queda SIEMPRE acotada a la empresa del Caller, nunca global.
	AssignRole(ctx context.Context, input RoleAssignmentInput) error
	// UnassignRole retira esa asignación acotada al tenant. Idempotente.
	UnassignRole(ctx context.Context, input RoleAssignmentInput) error
	// GrantToRole añade un grant a un rol PROPIO del tenant. Las plantillas
	// globales se rechazan con domain.ErrGlobalRoleImmutable.
	GrantToRole(ctx context.Context, input RoleGrantInput) error
	// RevokeFromRole quita un grant de un rol propio del tenant.
	RevokeFromRole(ctx context.Context, input RoleGrantInput) error
	// GrantToUser añade un override de grant a un miembro del tenant.
	GrantToUser(ctx context.Context, input UserGrantInput) error
	// RevokeFromUser quita un override de grant a un miembro del tenant.
	RevokeFromUser(ctx context.Context, input UserGrantInput) error
}

// MembershipInput es el alta o la baja de una persona en la empresa del Caller.
// El tenant NO viaja aquí (INV-04): un administrador solo puede dar de alta en
// SU empresa.
type MembershipInput struct {
	// UserID es el UUID de identity de la persona.
	UserID string
}

// MembershipAdmin administra la pertenencia usuario↔empresa.
type MembershipAdmin interface {
	// ListMembers devuelve los miembros de la empresa del Caller. NO recibe
	// tenant, como ningún método de aquí: la empresa sale del contexto (INV-04) y
	// por eso no hay forma de pedir el padrón de otra.
	//
	// Una lista vacía no es error. Devuelve identificadores OPACOS y la fecha de
	// alta —lo que guarda tenant_members— y NO sale a identity a por el nombre:
	// eso convertiría el listado de una empresa en una consulta al padrón del
	// grupo (INV-02).
	ListMembers(ctx context.Context) ([]domain.Membership, error)
	// AddMember da de alta a la persona en la empresa del Caller. Idempotente.
	// domain.ErrConflict si ya es miembro de OTRA empresa (ver
	// out.MembershipRepo.Add: no le añade una empresa, le rompe el canje).
	AddMember(ctx context.Context, input MembershipInput) error
	// RemoveMember la da de baja de la empresa del Caller. Idempotente.
	RemoveMember(ctx context.Context, input MembershipInput) error
}

// ---------------------------------------------------------------------------
// Invitaciones de un solo uso (Plan 047 · Ola A · T-A2/T-A8, D-047.11)
// ---------------------------------------------------------------------------

// IssueInvitationInput es la emisión de una invitación. Fíjate en lo que NO
// tiene, que es la mitad del contrato:
//
//   - no tiene TenantID (INV-04): la empresa sale del token de quien emite, y lo
//     que no existe en el tipo no se puede colar por el cuerpo;
//   - no tiene correo, ni nombre, ni teléfono (D-047.11): quien emite NO teclea
//     el correo de nadie en ningún momento. Reparte el código por WhatsApp —que
//     es el producto, no un canal prestado— y la nube nunca sabe a quién se lo
//     mandó. Añadir aquí un campo de destinatario metería PII en una tabla que se
//     diseñó para no tener ni una columna de texto.
type IssueInvitationInput struct {
	// RoleID es el rol que se concederá AL CANJEAR. Opcional (nil = alta sin
	// rol). Tiene que ser VISIBLE para la empresa de quien emite: uno suyo o una
	// plantilla global; cualquier otro es domain.ErrNotFound.
	RoleID *string
	// TTLSeconds es la vida de la invitación en segundos. CERO (o ausente)
	// significa «el default», no «que caduque ya»: el servicio aplica 24 h y
	// después el clamp a [60 s, 30 días]. El número lo elige quien emite porque
	// no es lo mismo invitar a alguien que está delante que a alguien que abrirá
	// WhatsApp mañana.
	TTLSeconds int
}

// IssuedInvitation es lo que devuelve la emisión: la fila y el token EN CLARO.
//
// 🔴 Token viaja AQUÍ Y SOLO AQUÍ, una vez. No está en domain.Invitation, no se
// persiste y el listado no puede reconstruirlo: en la tabla vive su SHA-256. Si
// quien emite lo pierde, la respuesta correcta es revocar y emitir otra.
type IssuedInvitation struct {
	Invitation domain.Invitation
	Token      string
}

// InvitationAdmin administra las invitaciones de la empresa del Caller: la vía
// por la que entra alguien a quien la dueña NO PUEDE BUSCAR porque no tiene
// bandeja y, para ella, quien se registra por su cuenta es invisible.
//
// Igual que MembershipAdmin, NINGÚN método recibe tenant: sale del contexto.
type InvitationAdmin interface {
	// IssueInvitation emite una invitación para la empresa del Caller y devuelve
	// el token en claro por única vez. domain.ErrNoTenant si el token no trae
	// empresa; domain.ErrNotFound si el rol pedido no es visible para ella.
	IssueInvitation(ctx context.Context, input IssueInvitationInput) (IssuedInvitation, error)
	// ListInvitations devuelve las invitaciones de la empresa del Caller, las
	// más recientes primero. Una lista vacía no es error.
	ListInvitations(ctx context.Context) ([]domain.Invitation, error)
	// RevokeInvitation anula una invitación VIVA de la empresa del Caller
	// (T-A8), de modo que un canje posterior de ese token falle.
	//
	// Idempotente sobre una ya revocada. domain.ErrNotFound si no existe o es de
	// otra empresa; domain.ErrConflict si ya fue canjeada — revocarla NO deshace
	// la membresía que el canje escribió, así que no se puede fingir que sí.
	RevokeInvitation(ctx context.Context, id string) error
}
