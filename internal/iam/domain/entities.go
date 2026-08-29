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

// RolTransversalID es el id FIJO del rol `platform_admin`, sembrado por la
// migración 0059_platform_admin.sql. Es el ÚNICO rol cuya ASIGNACIÓN puede ir
// con ámbito global (public.iam_user_roles.tenant_id NULL), porque es el único
// que por diseño actúa sobre empresas que no son la suya (ADR-0039).
//
// 🔴 EL DISCRIMINADOR ES EL ID Y NO EL NOMBRE, y la diferencia es de seguridad,
// no de estilo. `iam_roles` solo impone unicidad de nombre ENTRE PLANTILLAS
// GLOBALES (índice parcial iam_roles_global_name_uidx, 0015:36-37); los roles
// propios de un tenant se controlan con iam_roles_tenant_name_uidx, que no
// choca con aquél. Es decir: cualquier administradora puede crear en SU empresa
// un rol llamado `platform_admin` (RoleService.Create escribe con
// TenantID = el del llamante y la BD le asigna un id nuevo con
// gen_random_uuid). Una guarda que mirase el NOMBRE le daría a ese rol
// falsificado el privilegio de asignarse global. El id, en cambio, no se puede
// pedir: ninguna vía de producto lo elige, solo lo escribe la migración.
//
// 🔴 Y SI ALGÚN DÍA NO COINCIDIERA con el id sembrado en una base concreta, el
// fallo es FAIL-CLOSED: se rechazaría la asignación global del platform_admin
// —ruidoso y reparable— en vez de dejar pasar la de un rol de empresa.
const RolTransversalID = "10000000-0000-0000-0000-000000000004"

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

// Membership es la pertenencia de una persona a una empresa (tabla
// public.tenant_members, migración 0037). Es el vínculo de NEGOCIO que se queda
// en wApp: identity dice QUIÉN es la persona y esta fila a QUÉ empresa pertenece.
//
// 🔴 SON LAS TRES COLUMNAS DE LA TABLA Y NI UNA MÁS, y esa es toda la entidad.
// No trae nombre ni correo, y no es un recorte pendiente de completar: la
// persona vive en identity-core (INV-02), en otra base, y `user_id` no tiene FK
// que cruzar. Rellenar esos campos saliendo a identity al listar convertiría una
// lectura del propio tenant en una consulta al padrón del grupo — decisión de
// producto, no de esta capa. CERO PII.
type Membership struct {
	// UserID es el UUID de la cuenta en identity. Es un identificador OPACO.
	UserID string
	// TenantID es la empresa. Redundante en un listado acotado a una sola
	// empresa, y aun así va en la entidad: es la mitad de la clave primaria, y
	// omitirla obligaría a reconstruirla desde el contexto en cada consumidor.
	TenantID string
	// CreatedAt es cuándo entró en la empresa (columna created_at). Es lo que
	// permite ordenar el listado de forma estable y lo único que hay que
	// enseñarle a la dueña además del id.
	CreatedAt time.Time
}

// UserTenant es UNA de las empresas del sujeto, vista DESDE el sujeto: lo mínimo
// que un selector de empresa necesita para pintarse (Plan 047 · Ola 5 · T5.1).
//
// ⚠️ NO es domain.Membership con otro nombre, y la diferencia es la dirección.
// Membership mira desde la EMPRESA («quién está aquí dentro»: user_id, tenant_id,
// created_at) y por eso no trae nombres — la persona vive en identity. Ésta mira
// desde la PERSONA («entre qué empresas puedo elegir») y por eso SÍ trae el
// nombre: la empresa no vive en identity, vive en public.tenants, en esta misma
// base, a un JOIN de distancia. No hay ninguna salida al padrón del grupo aquí.
//
// 🔴 NO LLEVA `Active`, y es deliberado. Cuál es la activa NO es una propiedad de
// la empresa —dos personas distintas tienen activas empresas distintas—, así que
// meterla aquí la ataría al sujeto que la consultó. El dato viaja APARTE, en el
// segundo valor de retorno de in.TenantLister, que además admite el «ninguna» sin
// tener que inventar un elemento.
//
// CERO PII: un UUID y el nombre COMERCIAL de una empresa de la que quien pregunta
// ya es miembro. Ni correo, ni teléfono, ni nombre de persona.
type UserTenant struct {
	// ID es la empresa (public.tenants.id). Es lo que el selector manda de vuelta
	// a POST /api/v1/auth/active-tenant.
	ID string
	// DisplayName es el nombre legible (public.tenants.display_name), el mismo
	// que teclea el operador de plataforma al dar de alta la empresa. Existe
	// porque un selector de UUIDs es inservible: hasta hoy la consola pintaba el
	// id crudo.
	DisplayName string
}

// Grant es un patrón de permiso glob `recurso.accion` con su efecto
// (public.iam_role_grants / public.iam_user_grants). Es la unidad que se agrega
// (rol + cadena ⊕ overrides de usuario) para formar los grants EFECTIVOS que se
// embeben en el token al emitir (design.md §5). CERO PII.
type Grant struct {
	Pattern string
	Effect  Effect
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

// IdentitySession es la sesión que identity-core abre para una persona: lo que
// devuelve su login o su refresh (identity Plan 003 · Ola 3).
//
// El IdentityToken NO se persiste NUNCA en wApp: vive solo el instante
// server-side que dura el canje por un Context Token. Lo que se conserva y se
// entrega al cliente es el RefreshToken, que es de identity y solo identity
// puede rotar o revocar.
type IdentitySession struct {
	SessionID     string
	IdentityToken string
	RefreshToken  string
	// ExpiresAt es la expiración del IdentityToken, la que acota al Context
	// Token que salga de canjearlo.
	ExpiresAt time.Time
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

// IdentityUser es la persona tal y como la deja el padrón GLOBAL de identity
// tras un `POST /api/v1/users/ensure` (create-or-attach, identity Plan 003 ·
// T3.4).
//
// No trae nombre ni apellido, y esa ausencia es de identity, no un recorte de
// wApp: `iam.users` no tiene columna de ecosistema, así que devolverlos
// convertiría el endpoint en un directorio del grupo consultable por correo
// (identity dto/user_dto.go:44-56).
type IdentityUser struct {
	// ID es el UUID de la cuenta en identity. Es lo único que sirve para el
	// paso siguiente (`PUT /users/{id}/systems`).
	ID string
	// Email es el correo NORMALIZADO con el que quedó la cuenta —minúsculas y
	// sin espacios en los extremos—, que puede no ser el texto que se mandó.
	Email string
	// Created dice si ESTA llamada creó la cuenta. Falso cuando ya existía: el
	// alta es create-or-attach y una cuenta preexistente NO se modifica.
	Created bool
}

// IdentitySystemsDiff es lo que devuelve el `PUT /api/v1/users/{id}/systems`:
// el conjunto vigente más el diff que esa llamada aplicó (identity Plan 003 ·
// T3.8).
//
// El diff es lo que hace observable la idempotencia: repetir el mismo PUT
// devuelve Granted y Revoked VACÍOS sin tener que leer la base de identity.
//
// ⚠️ Systems está acotado al ecosistema de la credencial de wApp: NO enumera
// los accesos que otro ecosistema le haya dado a la misma persona.
type IdentitySystemsDiff struct {
	// Systems es el conjunto vigente tras la llamada, ordenado. Nunca nil.
	Systems []string
	// Granted son las claves que GANARON acceso en esta llamada.
	Granted []string
	// Revoked son las claves que lo PERDIERON en esta llamada.
	Revoked []string
}
