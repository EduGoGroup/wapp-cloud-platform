// Porta internal/iam/transport/http/roles.go @ 9a77307

package iamhttp

// roles.go — LA PUERTA HTTP DEL PLANO DE ROLES DEL TENANT (Plan 047 · Ola 1.0 ·
// T1.0-4, plano 2 del ADR-0033): /api/v1/roles y /api/v1/members.
//
// Es transporte y NADA más: traduce JSON/ruta/query ⇄ DTOs de los puertos in
// (in.RoleAdmin, in.MembershipAdmin) y mapea los errores tipados del dominio a
// códigos HTTP con writeDomainError. Las tres reglas duras viven en el usecase y
// aquí no se repiten ni se pueden saltar:
//
//   - INV-04 — el tenant sale del CONTEXTO de identidad. Ningún cuerpo de request de este
//     fichero tiene campo `tenant_id`: lo que no se decodifica no se puede colar.
//   - Un recurso de otra empresa se contesta 404, NUNCA 403: un "prohibido" confirmaría que
//     ese rol o esa persona existen en otra empresa; el 404 no dice nada.
//   - Las plantillas globales se leen y se asignan pero no se editan
//     (domain.ErrGlobalRoleImmutable → 422).
//
// EL MÉTODO NO SE COMPRUEBA AQUÍ, A PROPÓSITO. Estas rutas se montan con los
// patrones método+ruta de Go 1.22, y es el propio http.ServeMux quien devuelve el 405. Un
// `if r.Method != ...` dentro del handler sería código muerto. Las rutas de auth.go sí lo
// comprueban porque se montan por PATH pelado.
//
// R-H7 (común a todos los handlers de este fichero y de invitations.go): todo error del puerto
// sale por writeDomainError, con su código y su texto de diseño §5 (403 sin empresa, 404 ajeno o
// inexistente, 409, 422, 503/502 de identity, 500 el resto); un cuerpo JSON roto es 400
// «cuerpo JSON inválido» sin llamar al puerto; un comodín de ruta vacío es 400 «<nombre>
// requerido en la ruta» (p. ej. «id requerido en la ruta») sin llamar al puerto. Los instantes
// salen en RFC 3339 UTC.

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ---------------------------------------------------------------------------
// Administración de roles y grants (in.RoleAdmin)
// ---------------------------------------------------------------------------

// RoleAdminHandler sirve la administración de RBAC de la empresa del token:
// listar y crear roles, asignarlos a sus miembros y conceder o revocar grants
// —tanto los del rol como los overrides de una persona—. Depende SOLO del puerto in.RoleAdmin.
type RoleAdminHandler struct{}

// NewRoleAdminHandler construye el handler del plano de roles.
func NewRoleAdminHandler(roles in.RoleAdmin) *RoleAdminHandler {
	panic(pendiente.Implementar("iamhttp.NewRoleAdminHandler"))
}

// List sirve GET /api/v1/roles: los roles VISIBLES para la empresa del token (los suyos más las
// plantillas globales), en el orden del puerto. 200 con un ARRAY (vacío `[]`, nunca `null`).
// Cada rol: `role_id`, `name`, `global` (DERIVADA: true si el rol no tiene tenant) y, solo si
// existen, `tenant_id`, `parent_role_id` y `created_at`.
func (h *RoleAdminHandler) List() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.List"))
}

// Create sirve POST /api/v1/roles con `{"name","parent_role_id"}`: crea un rol CUSTOM de la
// empresa del token. Los dos campos llegan al puerto RECORTADOS; `parent_role_id` vacío (o solo
// espacios) es rol raíz (ParentRoleID nil). 201 con el rol en la forma de List; 404 padre no
// visible; 409 nombre repetido; 403 sin empresa.
func (h *RoleAdminHandler) Create() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.Create"))
}

// AddRoleGrant sirve POST /api/v1/roles/{id}/grants con `{"pattern","effect"}`: el puerto
// recibe RoleGrantInput{RoleID: id, Grant} con pattern y effect RECORTADOS y sin validar (lo
// valida el usecase: un grant inválido es 400 por domain.ErrInvalidInput). 204; 404 rol no
// visible; 422 plantilla global.
func (h *RoleAdminHandler) AddRoleGrant() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.AddRoleGrant"))
}

// RemoveRoleGrant sirve DELETE /api/v1/roles/{id}/grants?pattern=…&effect=…: el grant viaja en
// la QUERY (es la identidad de lo que se borra; un DELETE con cuerpo atraviesa mal los proxies),
// recortado. 204 también si no lo tenía; mismos códigos que AddRoleGrant.
func (h *RoleAdminHandler) RemoveRoleGrant() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.RemoveRoleGrant"))
}

// AssignRole sirve POST /api/v1/members/{user_id}/roles con `{"role_id"}` (recortado): el puerto
// recibe RoleAssignmentInput{UserID: user_id, RoleID}. 204 (idempotente); 404 si la persona no
// es miembro o el rol no es visible (mismo código a propósito).
func (h *RoleAdminHandler) AssignRole() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.AssignRole"))
}

// UnassignRole sirve DELETE /api/v1/members/{user_id}/roles/{role_id}: el puerto recibe
// RoleAssignmentInput con los dos comodines. 204; 404 en los mismos casos que AssignRole.
func (h *RoleAdminHandler) UnassignRole() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.UnassignRole"))
}

// AddUserGrant sirve POST /api/v1/members/{user_id}/grants con `{"pattern","effect"}`: un
// OVERRIDE de grant a una persona (iam_user_grants no tiene tenant: lo acota que sea miembro).
// El puerto recibe UserGrantInput{UserID: user_id, Grant} recortado. 204; 404 no miembro.
func (h *RoleAdminHandler) AddUserGrant() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.AddUserGrant"))
}

// RemoveUserGrant sirve DELETE /api/v1/members/{user_id}/grants?pattern=…&effect=…: quita ese
// override (grant por QUERY, recortado). 204 también si no lo tenía.
func (h *RoleAdminHandler) RemoveUserGrant() http.Handler {
	panic(pendiente.Implementar("iamhttp.RoleAdminHandler.RemoveUserGrant"))
}

// ---------------------------------------------------------------------------
// Administración de membresía (in.MembershipAdmin)
// ---------------------------------------------------------------------------

// MembershipHandler sirve el alta y la baja de personas en la empresa del token. Va aparte de
// RoleAdminHandler porque es OTRO permiso (`members.write` frente a `roles.write`).
type MembershipHandler struct{}

// NewMembershipHandler construye el handler de membresía.
func NewMembershipHandler(members in.MembershipAdmin) *MembershipHandler {
	panic(pendiente.Implementar("iamhttp.NewMembershipHandler"))
}

// List sirve GET /api/v1/members: quién está en la empresa del token. 200 con un ARRAY (vacío
// `[]`, nunca `null`); cada miembro con EXACTAMENTE `user_id`, `tenant_id` y, si existe,
// `created_at` — ni `name` ni `email`: la persona vive en identity-core (INV-02).
func (h *MembershipHandler) List() http.Handler {
	panic(pendiente.Implementar("iamhttp.MembershipHandler.List"))
}

// Add sirve POST /api/v1/members con `{"user_id"}` (recortado): da de alta a la persona en la
// empresa del token (el llamante NO elige empresa, INV-04).
//
// R-H7 y R-H9 (sin M2M ⇒ 503 con su cuerpo propio), LOS SEIS DESENLACES (Plan 047 · Ola B):
//
//	alta correcta (con o sin escritura en identity)         204, sin cuerpo
//	falta la credencial M2M (domain.ErrIdentityNotConfigured) 503 {"error":"identity_no_configurado"}
//	identity no contesta (ErrIdentityUnavailable)           503 {"error":"identity no está disponible"}
//	identity no acredita `wapp.bff` (ErrSystemNotAllowed)    502 {"error":"system_no_acreditable"}
//	el UUID no existe en identity (ErrNotFound)             404
//	ya es miembro de otra empresa (ErrConflict)             409
//
// 204 y no 201: es IDEMPOTENTE y no devuelve recurso.
func (h *MembershipHandler) Add() http.Handler {
	panic(pendiente.Implementar("iamhttp.MembershipHandler.Add"))
}

// Remove sirve DELETE /api/v1/members/{user_id}: el puerto recibe MembershipInput{UserID}. 204
// (idempotente; el DELETE va acotado al tenant del contexto). No retira roles ni grants.
func (h *MembershipHandler) Remove() http.Handler {
	panic(pendiente.Implementar("iamhttp.MembershipHandler.Remove"))
}
