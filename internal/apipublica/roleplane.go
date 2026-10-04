// Porta internal/publicapi/roleplane.go @ 9a77307 (registerRolePlane, B1–B14).
//
// roleplane.go — EL PLANO DE ROLES, MIEMBROS E INVITACIONES DE LA EMPRESA DEL TOKEN (plano 2 del
// ADR-0033; mapa §2.2). Los handlers son los de internal/modulos/acceso/iam/transport/http; aquí
// solo se decide, por ruta, el patrón, el permiso, si audita y con qué recurso.

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/transport/http"
)

// Scopes del plano 2 del ADR-0033, tal y como los siembra la migración 0084
// (T1.0-3). Se nombran aquí para que las rutas de abajo no repitan literales
// y para que se vea de un vistazo el reparto: `roles.*` gobierna QUÉ PUEDE HACER
// la gente (roles, asignaciones y grants) y `members.*` gobierna QUIÉN ESTÁ en la
// empresa. Son dos preguntas distintas y por eso son dos permisos distintos.
//
// 🔴 Los cuatro tienen que casar EXACTAMENTE con la migración: tenant_admin los
// alcanza por su glob '*' y viewer solo los `.read`, así que un nombre inventado
// aquí no daría un error — daría un 403 a la dueña de la empresa.
const (
	scopeRolesRead    = "roles.read"
	scopeRolesWrite   = "roles.write"
	scopeMembersRead  = "members.read"
	scopeMembersWrite = "members.write"
)

// Recursos de la bitácora de auditoría. AuditMiddleware graba `action` = el
// scope, así que las seis escrituras de rol comparten action ("roles.write") y lo
// único que las distingue en la bitácora es este literal: por eso NO se comparte
// uno genérico. CERO PII: son etiquetas fijas, nunca ids ni nombres.
const (
	auditResourceRole      = "role"
	auditResourceRoleGrant = "role_grant"
	auditResourceUserRole  = "user_role"
	auditResourceUserGrant = "user_grant"
	auditResourceMember    = "member"
	// La invitación es su PROPIO recurso aunque comparta el action `members.write`
	// con el alta y la baja: sin este literal, emitir un código y meter a alguien
	// en la empresa dejarían la MISMA línea en la bitácora, y son dos hechos
	// distintos —uno crea un secreto con caducidad, el otro escribe una membresía—.
	auditResourceInvitation = "invitation"
)

// RolePlaneDeps son los tres servicios del plano, puertos de entrada del módulo acceso NUEVO.
// Cada uno enciende su grupo de rutas; ninguna ausencia es un error.
type RolePlaneDeps struct {
	// Roles enciende B1–B8 (roles, grants de rol, roles y grants de una persona).
	Roles in.RoleAdmin
	// Members enciende B9–B11 (listado, alta y baja de miembros).
	Members in.MembershipAdmin
	// Invitations enciende B12–B14 (listado, emisión y revocación de invitaciones).
	Invitations in.InvitationAdmin
}

// MountRolePlane registra en c las rutas B1–B14 con los patrones EXACTOS del mapa §2.2 (byte a
// byte, incluidos los nombres de comodín {id}, {user_id} y {role_id}: son la etiqueta `route`
// de las métricas y el nombre con el que el handler lee r.PathValue). Cadena W o R según
// Common; permiso y recurso de auditoría por ruta:
//
//	B1  GET    /api/v1/roles                              R roles.read
//	B2  POST   /api/v1/roles                              W roles.write   (role)
//	B3  POST   /api/v1/roles/{id}/grants                  W roles.write   (role_grant)
//	B4  DELETE /api/v1/roles/{id}/grants                  W roles.write   (role_grant)
//	B5  POST   /api/v1/members/{user_id}/roles            W roles.write   (user_role)
//	B6  DELETE /api/v1/members/{user_id}/roles/{role_id}  W roles.write   (user_role)
//	B7  POST   /api/v1/members/{user_id}/grants           W roles.write   (user_grant)
//	B8  DELETE /api/v1/members/{user_id}/grants           W roles.write   (user_grant)
//	B9  GET    /api/v1/members                            R members.read
//	B10 POST   /api/v1/members                            W members.write (member)
//	B11 DELETE /api/v1/members/{user_id}                  W members.write (member)
//	B12 GET    /api/v1/invitations                        R members.read
//	B13 POST   /api/v1/invitations                        W members.write (invitation)
//	B14 DELETE /api/v1/invitations/{id}                   W members.write (invitation)
//
// Los permisos casan EXACTAMENTE con la migración 0084 (tenant_admin los alcanza por su glob '*'
// y viewer solo los `.read`): un nombre distinto no daría error, daría 403 a la dueña. Los
// recursos son etiquetas fijas, CERO PII; las seis escrituras de rol comparten action
// (roles.write) y solo el recurso las distingue en la bitácora, y la invitación tiene recurso
// propio aunque comparta members.write con el alta (son dos hechos distintos).
//
// Montaje: B1–B8 solo si d.Roles no es nil (iamhttp.NewRoleAdminHandler); B9–B11 solo si
// d.Members no es nil (iamhttp.NewMembershipHandler); B12–B14 solo si d.Invitations no es nil
// (iamhttp.NewInvitationHandler). Un grupo sin su servicio NO existe: sus patrones no se
// registran y responden 404 de ruta inexistente —mejor que una administración que existe y
// contesta 500—.
//
// 🔴 B10 se monta SIEMPRE que haya d.Members, tenga o no el despliegue cliente M2M de identity
// (RX.2.e): sin M2M el usecase devuelve domain.ErrIdentityNotConfigured y el handler responde
// 503 {"error":"identity_no_configurado"}, NUNCA 404 —un 404 mandaría a depurar el router cuando
// lo que falta es configuración—, mientras B9 sigue en 200. Ese 503 lo produce el usecase, no
// una rama de aquí.
//
// Los errores del dominio que devuelven los servicios llegan al cliente con el mapeo de
// iamhttp: domain.ErrInvalidInput ⇒ 400, ErrNoTenant ⇒ 403, ErrNotFound ⇒ 404 (también el
// recurso de OTRA empresa: nunca 403, que confirmaría que existe), ErrConflict ⇒ 409,
// ErrGlobalRoleImmutable ⇒ 422, ErrIdentityNotConfigured ⇒ 503. Un método que el patrón no
// admite sobre un camino montado es 405 con Allow (lo da el ServeMux, no un `if r.Method`).
//
// Fallo de cableado: k.MW nil con al menos un servicio no nil hace panic AL MONTAR (ver
// Common). Con los tres nil no registra nada.
//
// POR QUÉ LAS OPERACIONES SOBRE UNA PERSONA CUELGAN DE /members/{user_id} Y NO DE UN /users
// PROPIO: porque `users` ya es un recurso del OTRO plano ('users.provision.any', migración 0060)
// y porque aquí no se administra a una persona —eso vive en identity-core (INV-02)—, sino su
// PERTENENCIA y sus permisos DENTRO de esta empresa. Ojo con la lectura fácil de que «todo lo
// que cuelga de /members es members.*»: NO. El prefijo es el sujeto; el scope lo decide la
// OPERACIÓN. Asignarle un rol a un miembro es `roles.write` (es un permiso lo que se mueve, y
// quien puede asignar roles puede asignarse tenant_admin); darlo de alta o de baja es
// `members.write`.
func MountRolePlane(c *Cara, k Common, d RolePlaneDeps) {
	if d.Roles == nil && d.Members == nil && d.Invitations == nil {
		return
	}
	mustHaveMW(k, "MountRolePlane")
	mountRoles(c, k, d.Roles)
	mountMembers(c, k, d.Members)
	mountInvitations(c, k, d.Invitations)
}

// mountRoles monta B1–B8 si hay servicio de roles.
func mountRoles(c *Cara, k Common, roles in.RoleAdmin) {
	if roles == nil {
		return
	}
	h := iamhttp.NewRoleAdminHandler(roles)

	// Catálogo de roles de la empresa (los suyos + las plantillas globales).
	// LECTURA, y por eso sin auditoría.
	c.Handle("GET /api/v1/roles", protectRead(k, scopeRolesRead, h.List()))
	c.Handle("POST /api/v1/roles", protect(k, scopeRolesWrite, auditResourceRole, h.Create()))

	// Grants DEL ROL. El par (pattern, effect) viaja en el cuerpo al conceder y
	// en la QUERY al revocar: es la identidad de lo que se borra, y un DELETE
	// con cuerpo atraviesa mal proxies y clientes (ver iamhttp.grantFromQuery).
	c.Handle("POST /api/v1/roles/{id}/grants", protect(k, scopeRolesWrite, auditResourceRoleGrant, h.AddRoleGrant()))
	c.Handle("DELETE /api/v1/roles/{id}/grants", protect(k, scopeRolesWrite, auditResourceRoleGrant, h.RemoveRoleGrant()))

	// Rol ↔ persona. La asignación queda acotada a la empresa del token, nunca
	// global (ver RoleService.AssignRole): por eso el DELETE lleva el rol en la
	// ruta y no borra la asignación global que pudiera existir.
	c.Handle("POST /api/v1/members/{user_id}/roles", protect(k, scopeRolesWrite, auditResourceUserRole, h.AssignRole()))
	c.Handle("DELETE /api/v1/members/{user_id}/roles/{role_id}", protect(k, scopeRolesWrite, auditResourceUserRole, h.UnassignRole()))

	// Overrides de grant de UNA persona (iam_user_grants). Mismo scope que los
	// del rol a propósito, y está razonado en la migración 0084: partirlos en un
	// `grants.write` aparte solo tiene sentido el día que alguien delegue lo uno
	// sin lo otro, y ese día esa separación necesita SU migración.
	c.Handle("POST /api/v1/members/{user_id}/grants", protect(k, scopeRolesWrite, auditResourceUserGrant, h.AddUserGrant()))
	c.Handle("DELETE /api/v1/members/{user_id}/grants", protect(k, scopeRolesWrite, auditResourceUserGrant, h.RemoveUserGrant()))
}

// mountMembers monta B9–B11 si hay servicio de membresía.
func mountMembers(c *Cara, k Common, members in.MembershipAdmin) {
	if members == nil {
		return
	}
	h := iamhttp.NewMembershipHandler(members)

	// Quién está en la empresa. Es la OTRA lectura del plano y la única ruta que
	// consume `members.read`: sin ella ese scope se quedaría sembrado en la
	// migración 0084 y sin un solo consumidor (D-047.8).
	c.Handle("GET /api/v1/members", protectRead(k, scopeMembersRead, h.List()))
	// El alta se monta SIEMPRE que haya administración de membresía, tenga o no
	// este despliegue credencial M2M de identity: sin ella contesta 503 (lo produce
	// el usecase, ver MountRolePlane), nunca 404. Por eso aquí no hay una segunda
	// rama que mantener sincronizada con él.
	c.Handle("POST /api/v1/members", protect(k, scopeMembersWrite, auditResourceMember, h.Add()))
	c.Handle("DELETE /api/v1/members/{user_id}", protect(k, scopeMembersWrite, auditResourceMember, h.Remove()))
}

// mountInvitations monta B12–B14 si hay servicio de invitaciones.
func mountInvitations(c *Cara, k Common, invitations in.InvitationAdmin) {
	if invitations == nil {
		return
	}
	h := iamhttp.NewInvitationHandler(invitations)

	// LA INVITACIÓN DE UN SOLO USO (Plan 047 · Ola A · T-A2 y T-A8, D-047.11).
	// Cierra el hueco que el alta de arriba no puede cubrir: POST /api/v1/members
	// exige que la dueña SEPA el UUID de la persona, y ella no tiene bandeja ni
	// buscador. Aquí emite un código opaco, se lo pasa por WhatsApp y la persona
	// lo canjea después de registrarse ella misma (A4, MountAuth).
	//
	// 🔴 LOS SCOPES SON LOS DE MIEMBROS Y NO UNOS NUEVOS, y es una decisión, no
	// una economía: una invitación es una membresía en diferido, así que quien
	// puede meter a alguien en la empresa puede invitarlo, y quien no, no.
	// Estrenar un `invitations.write` habría dejado a la dueña sin poder emitir
	// hasta que una migración se lo sembrara — la 0085 dice explícitamente que
	// no siembra grants por esto mismo.
	c.Handle("GET /api/v1/invitations", protectRead(k, scopeMembersRead, h.List()))
	c.Handle("POST /api/v1/invitations", protect(k, scopeMembersWrite, auditResourceInvitation, h.Issue()))
	// La revocación es ESCRITURA y se audita: es la única forma de saber
	// después quién cerró una puerta que se había abierto.
	c.Handle("DELETE /api/v1/invitations/{id}", protect(k, scopeMembersWrite, auditResourceInvitation, h.Revoke()))
}
