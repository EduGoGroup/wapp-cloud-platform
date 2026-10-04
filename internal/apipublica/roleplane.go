// Porta internal/publicapi/roleplane.go @ 9a77307 (registerRolePlane, B1–B14).
//
// roleplane.go — EL PLANO DE ROLES, MIEMBROS E INVITACIONES DE LA EMPRESA DEL TOKEN (plano 2 del
// ADR-0033; mapa §2.2). Los handlers son los de internal/modulos/acceso/iam/transport/http; aquí
// solo se decide, por ruta, el patrón, el permiso, si audita y con qué recurso.

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
func MountRolePlane(c *Cara, k Common, d RolePlaneDeps) {
	panic(pendiente.Implementar("apipublica.MountRolePlane"))
}
