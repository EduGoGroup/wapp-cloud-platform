// Package out declara los puertos DE SALIDA del módulo IAM: las interfaces de
// repositorio que los usecases necesitan para persistir/leer estado. Son
// contratos PUROS (context + tipos de dominio); las implementaciones viven en
// infra/postgres (SQL raw) e infra/memory (tests). Todas las operaciones
// acotadas a un tenant reciben el tenant_id del CONTEXTO de identidad (nunca lo
// inventan): la regla de aislamiento multi-tenant (INV-8) se cumple en el
// usecase pasando el tenant del token y en el repo con `WHERE tenant_id = $1`.
//
// Convención de errores (verdad de campo del repo): "no encontrado" que es un
// resultado normal se expresa con `found bool`; "no encontrado" que es fallo de
// negocio se expresa devolviendo domain.ErrNotFound (errors.Is). La violación de
// unicidad se mapea a domain.ErrConflict.
package out

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

// IdentityClient habla con identity-api, el SSO del grupo (identity Plan 003 ·
// Ola 3). Es el ÚNICO puerto de este módulo que sale del proceso hacia otro
// servicio: los demás son almacenamiento.
//
// A partir de la delegación, las credenciales de una persona se validan AHÍ y no
// aquí. Lo que wApp sigue decidiendo —el tenant y los grants— viaja en el
// Context Token que se emite después, con el canje.
type IdentityClient interface {
	// Login autentica a la persona contra identity para una aplicación concreta
	// (`system`: wapp.bff o wapp.edge). El System Gate de identity puede negar el
	// acceso a esa aplicación aunque las credenciales sean correctas.
	Login(ctx context.Context, email, password, system string) (domain.IdentitySession, error)
	// Refresh rota la sesión en identity a partir del refresh presentado. La
	// aplicación NO se manda: sale de la fila de la sesión, y mandarla sortearía
	// el System Gate.
	Refresh(ctx context.Context, refreshToken string) (domain.IdentitySession, error)
	// Logout revoca en identity la sesión de ESE refresh. Idempotente: revocar
	// una sesión que ya no existe no es error.
	Logout(ctx context.Context, refreshToken string) error
	// LogoutAll revoca TODAS las sesiones de la persona que acredita el Identity
	// Token presentado, en todas sus aplicaciones.
	LogoutAll(ctx context.Context, identityToken string) error
}

// IdentityM2MClient habla con identity-api como MÁQUINA, no como persona (Plan
// 056 · T2.4). Es HERMANO de IdentityClient y no una ampliación suya, y la
// diferencia no es de comodidad: aquel presenta las credenciales de alguien y
// devuelve su sesión; este presenta la credencial de wApp —una API key que se
// CANJEA, nunca se presenta (identity ADR-0025)— y opera sobre el padrón global
// del grupo en nombre del ecosistema `wapp`.
//
// El canje del Service Token y su caché son asunto de la implementación: ningún
// usecase pasa tokens por aquí. Lo único que hace falta configurar es la API
// key (WAPP_IDENTITY_API_KEY).
//
// 🔴 El orden importa y no es negociable: una cuenta recién nacida —por Signup o
// por EnsureUser— NO puede entrar a ninguna aplicación hasta que
// ReplaceUserSystems le abra el System Gate, porque identity lo evalúa en el
// login ANTES de emitir token y contesta 403 (identity ADR-0020, «el acceso de
// onboarding se escribe, no se infiere»).
type IdentityM2MClient interface {
	// EnsureUser asegura la persona en el padrón GLOBAL de identity por su
	// correo: la crea si no existe y la devuelve si ya existía (create-or-attach).
	// Es idempotente y NUNCA modifica una cuenta que ya estaba —nombre y
	// apellido solo se escriben en el alta—. La cuenta nace SIN contraseña
	// utilizable: identity veta por escrito que un ecosistema fije la credencial
	// de nadie.
	//
	// ⚠️ NO está acotado por ecosistema: con este scope se puede asegurar
	// —y por tanto descubrir— cualquier correo del grupo.
	EnsureUser(ctx context.Context, email, firstName, lastName string) (domain.IdentityUser, error)
	// UserSystemsClient aporta el par LEER/DECLARAR accesos. Va embebido y no
	// declarado aquí para que quien solo necesite ese par pueda pedirlo sin
	// heredar de paso la capacidad de crear cuentas en el padrón del grupo (ver
	// su propia documentación, justo debajo).
	UserSystemsClient
	// Signup registra a la persona en identity CON SU PROPIA contraseña y
	// devuelve el UUID de su cuenta. Es la única vía por la que un hash
	// utilizable entra en identity, y por eso la clave la escribe su dueño y no
	// pasa por ninguna base de wApp.
	//
	// ⚠️ La ruta de identity es PÚBLICA (no lleva el Service Token) y se expone
	// aquí solo para que el alta viva en una pieza. El 201 es AMBIGUO a
	// propósito —cuenta creada, adoptada o reconocida dan el mismo cuerpo— y
	// devuelve SIEMPRE el id real (identity ADR-0027).
	Signup(ctx context.Context, email, password, firstName, lastName string) (userID string, err error)
}

// UserSystemsClient es la MITAD de IdentityM2MClient que gobierna a qué
// aplicaciones de wApp puede entrar una persona: leer el conjunto vigente y
// declararlo entero. Lo satisface el mismo adaptador (iamidentity.M2MClient).
//
// Es un puerto PROPIO y no un par de métodos sueltos del cliente entero porque
// la administración de membresía (Plan 047 · Ola B) necesita exactamente estas
// dos operaciones y NINGUNA de las otras dos: dar de alta a alguien en una
// empresa no puede crear cuentas en el padrón global del grupo (EnsureUser) ni
// registrar contraseñas (Signup). Con el cliente completo como dependencia esa
// frontera sería una convención que alguien puede ampliar sin querer; como
// puerto separado la impone el compilador.
type UserSystemsClient interface {
	// GetUserSystems devuelve las aplicaciones a las que la persona puede entrar
	// HOY dentro del ecosistema de wApp. NUNCA es nil: sin ninguna es un arreglo
	// vacío, y ese desenlace es DISTINTO de que la persona no exista (identity
	// dto/user_systems_dto.go:57 lo declara como contrato, no como cortesía).
	//
	// 🔴 ES LO QUE HACE POSIBLE UNA UNIÓN EN VEZ DE UN REEMPLAZO CIEGO. El PUT de
	// aquí abajo es declarativo: sin leer antes, añadir una aplicación revoca
	// todas las demás. Aproximar esta lectura con una tabla local —«¿le
	// aprobamos algo antes?»— es lo que se hacía hasta esta ola y está
	// documentado como estructuralmente equivocado: una tabla de wApp no sabe
	// qué escribió identity.
	//
	// ⚠️ El conjunto está ACOTADO AL ECOSISTEMA de la credencial (identity
	// ADR-0016): NO enumera los accesos que otro ecosistema le haya dado a la
	// misma persona. Lejos de ser una limitación, es lo que hace segura la
	// unión — lo que no se ve tampoco se pisa, porque el PUT hermano revoca
	// dentro del mismo ecosistema y solo ahí.
	//
	// 🔧 Consume el scope `identity.users.systems.read`, que es DISTINTO del de
	// escritura (`...manage`): una credencial M2M que hoy solo escribe recibe 403
	// aquí hasta que un operador le añada el de lectura.
	//
	// domain.ErrNotFound si la persona no está en el padrón de identity.
	GetUserSystems(ctx context.Context, userID string) ([]string, error)
	// ReplaceUserSystems declara el conjunto COMPLETO de aplicaciones a las que
	// esa persona puede entrar dentro del ecosistema de wApp. Es DECLARATIVO, no
	// aditivo: lo que no aparece queda REVOCADO, y un conjunto vacío es legítimo
	// («ninguna»). Devuelve el conjunto vigente y el diff aplicado.
	//
	// Falla con domain.ErrSystemNotAllowed si alguna clave no es del ecosistema
	// de wApp o no existe, y entonces no se escribió NADA (atómico); con
	// domain.ErrNotFound si la persona no existe en identity.
	ReplaceUserSystems(ctx context.Context, userID string, systems []string) (domain.IdentitySystemsDiff, error)
}

// MembershipRepo persiste la membresía usuario↔tenant (tabla
// public.tenant_members, migración 0037). Es el vínculo de NEGOCIO que se queda
// en wApp cuando los usuarios pasan a identity (identity ADR-0001, INV-1):
// identity dice QUIÉN es la persona y esta tabla a QUÉ tenant pertenece.
//
// Dejó de ser SOLO LECTURA en el Plan 047 · Ola 1.0 (T1.0-2): hasta entonces la
// única alta la escribía el operador al aprobar un access-request
// (platformadmin.executeApprovalTx) y no había forma de darla de alta desde el
// plano de administración del propio tenant.
type MembershipRepo interface {
	// TenantsOfUser devuelve los tenants de los que el usuario es miembro, en
	// orden estable. Una lista VACÍA no es error: significa que ese usuario no
	// tiene membresía en wApp (quien lo llame decide qué hacer con eso).
	TenantsOfUser(ctx context.Context, userID string) ([]string, error)
	// MembersOf devuelve los miembros de UN tenant, en orden estable
	// (created_at, user_id). Es la lectura INVERSA de TenantsOfUser y la sirve el
	// índice que la migración 0037 creó exactamente para ella
	// (idx_tenant_members_tenant): la PK (user_id, tenant_id) sirve al acceso por
	// usuario —el del canje—, este índice al de administración por tenant.
	//
	// Una lista VACÍA no es error: una empresa sin miembros es un estado
	// legítimo (los tenants nacen antes que su gente).
	//
	// ⚠️ Devuelve lo que la TABLA guarda —id opaco y fecha de alta— y nada más.
	// El nombre y el correo viven en identity (INV-02) y este puerto no sale a
	// buscarlos: hacerlo convertiría el listado de una empresa en una consulta al
	// padrón del grupo.
	MembersOf(ctx context.Context, tenantID string) ([]domain.Membership, error)
	// Add da de alta la membresía (userID, tenantID). Es IDEMPOTENTE: repetirla
	// no duplica ni falla. NO asigna rol: eso es otra decisión y tiene su propia
	// puerta (in.RoleAdmin.AssignRole).
	//
	// 🔴 Devuelve domain.ErrConflict si el usuario ya es miembro de OTRO tenant.
	// Esa guarda NO es una regla de negocio de la administración de empresas: es
	// lo que mantiene canjeable el token de esa persona. El canje resuelve el
	// tenant por esta tabla y con más de una membresía falla con
	// domain.ErrMultipleTenants (usecase/exchange.go:resolveTenant), así que una
	// segunda membresía no le añade una empresa a nadie — le rompe el login.
	// Es la deuda MD-055.2 y se levanta cuando el canje sepa elegir, no antes.
	//
	// La escritura la comparte con la vía del operador en el adaptador
	// (iampostgres.GrantTenantAccess): este puerto es PURO y no conoce
	// transacciones, y la del operador necesita pasar la suya.
	Add(ctx context.Context, userID, tenantID string) error
	// Remove da de baja la membresía (userID, tenantID). No-op si no existía.
	//
	// ⚠️ NO toca los roles ni los grants de esa persona: las filas de
	// iam_user_roles acotadas a ese tenant sobreviven. No es un descuido — el
	// canje ya ignora los permisos de quien no tiene tenant (resolveGrants
	// devuelve cero grants sin membresía), así que la baja deja a la persona sin
	// poder operar aunque su asignación siga escrita, y readmitirla no obliga a
	// reconstruir su rol.
	Remove(ctx context.Context, userID, tenantID string) error
}

// RoleRepo persiste roles, sus grants y la asignación usuario↔rol (tablas
// iam_roles, iam_role_grants, iam_user_roles).
type RoleRepo interface {
	// Create inserta un rol custom del tenant y devuelve la fila con ID asignado.
	// Devuelve domain.ErrConflict si el nombre ya existe para el tenant.
	Create(ctx context.Context, r domain.Role) (domain.Role, error)
	// GetByID busca un rol por PK (global o de tenant). domain.ErrNotFound si no.
	GetByID(ctx context.Context, id string) (domain.Role, error)
	// List devuelve los roles VISIBLES para el tenant: sus roles custom más las
	// plantillas globales (tenant_id NULL).
	List(ctx context.Context, tenantID string) ([]domain.Role, error)
	// ParentOf resuelve el parent_role_id de un rol para la cadena de herencia
	// (auth.ResolveRoleChain). ok=false si el rol no tiene padre (raíz).
	ParentOf(ctx context.Context, id string) (parentID string, ok bool, err error)
	// GrantsOf devuelve los grants directos de UN rol (sin herencia).
	GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error)
	// AddGrant añade un grant al rol (idempotente por (role_id, pattern, effect)).
	AddGrant(ctx context.Context, roleID string, g domain.Grant) error
	// RemoveGrant elimina un grant del rol (no-op si no existía).
	RemoveGrant(ctx context.Context, roleID string, g domain.Grant) error
	// RolesOfUser devuelve los roles ASIGNADOS a un usuario para el tenant dado
	// (o globales si tenant_id es NULL en la asignación).
	RolesOfUser(ctx context.Context, userID, tenantID string) ([]domain.Role, error)
	// AssignToUser asigna un rol a un usuario, opcionalmente acotado a un
	// tenant (D-056.11): tenantID nil asigna GLOBAL, tenantID no nil acota la
	// asignación a esa empresa. Idempotente por los índices únicos de
	// iam_user_roles; ya NO hay PK desde la migración 0060.
	AssignToUser(ctx context.Context, userID, roleID string, tenantID *string) error
	// UnassignFromUser retira un rol de un usuario (no-op si no estaba),
	// SIMÉTRICO a AssignToUser: tenantID nil retira la asignación GLOBAL,
	// tenantID no nil retira solo la acotada a esa empresa.
	//
	// El parámetro se añadió en el Plan 047 · Ola 1.0 y no es simetría estética:
	// sin él, el DELETE borraba TODAS las filas de esa pareja (user, role) sin
	// mirar el tenant, y un administrador de la empresa A podía retirarle a
	// alguien la asignación de un rol GLOBAL que también valía en la empresa B.
	UnassignFromUser(ctx context.Context, userID, roleID string, tenantID *string) error
}

// GrantRepo persiste los overrides de grants por usuario (tabla
// iam_user_grants) que se mergean sobre los del rol al emitir el token.
type GrantRepo interface {
	// GrantsOfUser devuelve los overrides de grants de un usuario.
	GrantsOfUser(ctx context.Context, userID string) ([]domain.Grant, error)
	// AddUserGrant añade un override (idempotente por (user_id, pattern, effect)).
	AddUserGrant(ctx context.Context, userID string, g domain.Grant) error
	// RemoveUserGrant elimina un override (no-op si no existía).
	RemoveUserGrant(ctx context.Context, userID string, g domain.Grant) error
}

// AuditRepo persiste la bitácora de auditoría (tabla audit_events). CERO PII.
type AuditRepo interface {
	// Record inserta un evento de auditoría append-only.
	Record(ctx context.Context, e domain.AuditEvent) error
	// List devuelve los eventos del tenant, más recientes primero, paginados.
	List(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}
