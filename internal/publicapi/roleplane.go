package publicapi

// roleplane.go — EL MONTAJE DEL PLANO DE ROLES DEL TENANT (Plan 047 · Ola 1.0 ·
// T1.0-4). Los handlers viven en internal/iam/transport/http; aquí solo se
// deciden tres cosas por ruta: su patrón, su scope y si audita.

import (
	"net/http"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Scopes del plano 2 del ADR-0033, tal y como los siembra la migración 0084
// (T1.0-3). Se nombran aquí para que las diez rutas de abajo no repitan literales
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
)

// registerRolePlane monta la administración de roles, grants y membresía de la
// empresa del token (plano 2 del ADR-0033). Hasta esta ola, de las 84 rutas que
// servía este proceso ninguna abría ese plano: las tablas `iam_*` y
// `tenant_members` solo se tocaban por SQL o por la vía del operador.
//
// La cadena es la MISMA que el resto de /api/v1 y no se inventa nada: `protect`
// para las escrituras (Authenticate → RequirePermission → AuditMiddleware) y
// `protectRead` para las dos lecturas (sin auditoría, que es el patrón vigente:
// una lectura no tiene efecto que registrar).
//
// POR QUÉ LAS OPERACIONES SOBRE UNA PERSONA CUELGAN DE /members/{user_id} Y NO DE
// UN /users PROPIO: porque `users` ya es un recurso del OTRO plano
// ('users.provision.any', migración 0060) y porque aquí no se administra a una
// persona —eso vive en identity-core (INV-02)—, sino su PERTENENCIA y sus
// permisos DENTRO de esta empresa. La ruta dice la condición que el usecase
// impone: requireMember. Ojo con la lectura fácil de que "todo lo que cuelga de
// /members es members.*": NO. El prefijo es el sujeto; el scope lo decide la
// OPERACIÓN. Asignarle un rol a un miembro es `roles.write` (es un permiso lo que
// se mueve, y quien puede asignar roles puede asignarse tenant_admin); darlo de
// alta o de baja es `members.write`.
//
// Sin el servicio correspondiente las rutas NO se montan y responden 404 de ruta
// inexistente: es preferible a una administración que existe y contesta 500.
func registerRolePlane(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.Roles != nil {
		h := iamhttp.NewRoleAdminHandler(d.Roles)

		// Catálogo de roles de la empresa (los suyos + las plantillas globales).
		// LECTURA, y por eso sin auditoría.
		mux.Handle("GET /api/v1/roles", protectRead(mw, log,
			scopeRolesRead, h.List()))
		mux.Handle("POST /api/v1/roles", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceRole, h.Create()))

		// Grants DEL ROL. El par (pattern, effect) viaja en el cuerpo al conceder y
		// en la QUERY al revocar: es la identidad de lo que se borra, y un DELETE
		// con cuerpo atraviesa mal proxies y clientes (ver iamhttp.grantFromQuery).
		mux.Handle("POST /api/v1/roles/{id}/grants", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceRoleGrant, h.AddRoleGrant()))
		mux.Handle("DELETE /api/v1/roles/{id}/grants", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceRoleGrant, h.RemoveRoleGrant()))

		// Rol ↔ persona. La asignación queda acotada a la empresa del token, nunca
		// global (ver RoleService.AssignRole): por eso el DELETE lleva el rol en la
		// ruta y no borra la asignación global que pudiera existir.
		mux.Handle("POST /api/v1/members/{user_id}/roles", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceUserRole, h.AssignRole()))
		mux.Handle("DELETE /api/v1/members/{user_id}/roles/{role_id}", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceUserRole, h.UnassignRole()))

		// Overrides de grant de UNA persona (iam_user_grants). Mismo scope que los
		// del rol a propósito, y está razonado en la migración 0084: partirlos en un
		// `grants.write` aparte solo tiene sentido el día que alguien delegue lo uno
		// sin lo otro, y ese día esa separación necesita SU migración.
		mux.Handle("POST /api/v1/members/{user_id}/grants", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceUserGrant, h.AddUserGrant()))
		mux.Handle("DELETE /api/v1/members/{user_id}/grants", protect(mw, auditor, log,
			scopeRolesWrite, auditResourceUserGrant, h.RemoveUserGrant()))
	}

	if d.Members != nil {
		h := iamhttp.NewMembershipHandler(d.Members)

		// Quién está en la empresa. Es la OTRA lectura del plano y la única ruta que
		// consume `members.read`: sin ella ese scope se quedaría sembrado en la
		// migración 0084 y sin un solo consumidor, y la pantalla de miembros de la
		// Ola 1 no tendría backend al que llamar — que es exactamente el hueco por el
		// que nació esta ola (D-047.8).
		mux.Handle("GET /api/v1/members", protectRead(mw, log,
			scopeMembersRead, h.List()))
		mux.Handle("POST /api/v1/members", protect(mw, auditor, log,
			scopeMembersWrite, auditResourceMember, h.Add()))
		mux.Handle("DELETE /api/v1/members/{user_id}", protect(mw, auditor, log,
			scopeMembersWrite, auditResourceMember, h.Remove()))
	}
}
