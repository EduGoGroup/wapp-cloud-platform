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
	"net/url"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// ---------------------------------------------------------------------------
// DTOs de request/response (wire format de /api/v1/roles y /api/v1/members)
// ---------------------------------------------------------------------------

// roleDTO es la proyección pública de un rol. `global` es DERIVADA (no hay
// columna): TenantID nil ⇒ plantilla del ecosistema. Se sirve explícita y no se
// deja inferir de la ausencia de `tenant_id` porque es justo la diferencia que
// la UI necesita para no ofrecer "editar grants" sobre algo que responderá 422.
type roleDTO struct {
	RoleID       string `json:"role_id"`
	Name         string `json:"name"`
	TenantID     string `json:"tenant_id,omitempty"`
	ParentRoleID string `json:"parent_role_id,omitempty"`
	Global       bool   `json:"global"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// createRoleRequest es el cuerpo de POST /api/v1/roles. No lleva tenant_id
// (INV-04) y `parent_role_id` vacío significa rol raíz.
type createRoleRequest struct {
	Name         string `json:"name"`
	ParentRoleID string `json:"parent_role_id"`
}

// grantRequest es el cuerpo de las CONCESIONES de grant (a rol o a persona).
// effect es obligatorio y explícito: el usecase rechaza el vacío en vez de
// tomarlo como "allow" (un campo olvidado no debe conceder nada).
type grantRequest struct {
	Pattern string `json:"pattern"`
	Effect  string `json:"effect"`
}

// assignRoleRequest es el cuerpo de POST /api/v1/members/{user_id}/roles. El
// usuario va en la RUTA y el rol en el cuerpo.
type assignRoleRequest struct {
	RoleID string `json:"role_id"`
}

// memberRequest es el cuerpo de POST /api/v1/members: el UUID de identity de la
// persona a la que se le abre la empresa DEL LLAMANTE.
type memberRequest struct {
	UserID string `json:"user_id"`
}

// memberDTO es la proyección pública de una membresía: EXACTAMENTE lo que
// guarda tenant_members.
//
// 🔴 No hay `name` ni `email`, y su ausencia es el contrato, no un hueco por
// rellenar: la persona vive en identity-core (INV-02) y este endpoint no sale a
// buscarla. Ponerlos aquí convertiría «los miembros de mi empresa» en una
// consulta al padrón del grupo, que es una decisión de producto y no de la capa
// de transporte.
type memberDTO struct {
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	CreatedAt string `json:"created_at,omitempty"`
}

// dtoFromMembership proyecta una domain.Membership al wire format.
func dtoFromMembership(m domain.Membership) memberDTO {
	dto := memberDTO{UserID: m.UserID, TenantID: m.TenantID}
	if !m.CreatedAt.IsZero() {
		dto.CreatedAt = m.CreatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// dtoFromRole proyecta un domain.Role al wire format.
func dtoFromRole(r domain.Role) roleDTO {
	dto := roleDTO{RoleID: r.ID, Name: r.Name, Global: r.TenantID == nil}
	if r.TenantID != nil {
		dto.TenantID = *r.TenantID
	}
	if r.ParentRoleID != nil {
		dto.ParentRoleID = *r.ParentRoleID
	}
	if !r.CreatedAt.IsZero() {
		dto.CreatedAt = r.CreatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// grantFromQuery lee el grant de la QUERY STRING, que es por donde viaja en las
// dos revocaciones (DELETE .../grants).
//
// No va en el cuerpo por lo de siempre: un DELETE con cuerpo es legal en HTTP
// pero atraviesa mal proxies y clientes, y aquí el grant es la IDENTIDAD de lo
// que se borra —el par (pattern, effect)—, no un dato accesorio. Tampoco va en
// la ruta: un pattern lleva puntos y asteriscos (`sessions.*`) y meterlo en un
// segmento obligaría a escapar en los dos extremos.
//
// No valida nada: pattern vacío o effect fuera de {allow,deny} los rechaza el
// usecase (validGrant) con domain.ErrInvalidInput → 400. Una segunda validación
// aquí sería una segunda definición de lo que es un grant válido.
func grantFromQuery(q url.Values) domain.Grant {
	return domain.Grant{
		Pattern: strings.TrimSpace(q.Get("pattern")),
		Effect:  domain.Effect(strings.TrimSpace(q.Get("effect"))),
	}
}

// grantFromRequest construye el grant del cuerpo JSON. Mismo criterio que
// grantFromQuery: aquí no se valida, se traduce.
func grantFromRequest(req grantRequest) domain.Grant {
	return domain.Grant{
		Pattern: strings.TrimSpace(req.Pattern),
		Effect:  domain.Effect(strings.TrimSpace(req.Effect)),
	}
}

// pathValue lee un comodín de la ruta y responde 400 si viniera vacío. Con los
// patrones de Go 1.22 un comodín no casa un segmento vacío, así que es una
// guarda de cinturón: existe para que el handler no dependa de esa sutileza del
// mux si algún día se monta por otra vía. Lo usa también invitations.go.
func pathValue(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v := strings.TrimSpace(r.PathValue(name))
	if v == "" {
		writeError(w, http.StatusBadRequest, name+" requerido en la ruta")
		return "", false
	}
	return v, true
}

// ---------------------------------------------------------------------------
// Administración de roles y grants (in.RoleAdmin)
// ---------------------------------------------------------------------------

// RoleAdminHandler sirve la administración de RBAC de la empresa del token:
// listar y crear roles, asignarlos a sus miembros y conceder o revocar grants
// —tanto los del rol como los overrides de una persona—. Depende SOLO del puerto in.RoleAdmin.
type RoleAdminHandler struct {
	roles in.RoleAdmin
}

// NewRoleAdminHandler construye el handler del plano de roles.
func NewRoleAdminHandler(roles in.RoleAdmin) *RoleAdminHandler {
	return &RoleAdminHandler{roles: roles}
}

// List sirve GET /api/v1/roles: los roles VISIBLES para la empresa del token (los suyos más las
// plantillas globales), en el orden del puerto. 200 con un ARRAY (vacío `[]`, nunca `null`).
// Cada rol: `role_id`, `name`, `global` (DERIVADA: true si el rol no tiene tenant) y, solo si
// existen, `tenant_id`, `parent_role_id` y `created_at`.
func (h *RoleAdminHandler) List() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roles, err := h.roles.ListRoles(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out := make([]roleDTO, 0, len(roles))
		for _, role := range roles {
			out = append(out, dtoFromRole(role))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// Create sirve POST /api/v1/roles con `{"name","parent_role_id"}`: crea un rol CUSTOM de la
// empresa del token. Los dos campos llegan al puerto RECORTADOS; `parent_role_id` vacío (o solo
// espacios) es rol raíz (ParentRoleID nil). 201 con el rol en la forma de List; 404 padre no
// visible; 409 nombre repetido; 403 sin empresa.
func (h *RoleAdminHandler) Create() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req createRoleRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		input := in.CreateRoleInput{Name: strings.TrimSpace(req.Name)}
		if parent := strings.TrimSpace(req.ParentRoleID); parent != "" {
			input.ParentRoleID = &parent
		}
		role, err := h.roles.CreateRole(r.Context(), input)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, dtoFromRole(role))
	})
}

// AddRoleGrant sirve POST /api/v1/roles/{id}/grants con `{"pattern","effect"}`: el puerto
// recibe RoleGrantInput{RoleID: id, Grant} con pattern y effect RECORTADOS y sin validar (lo
// valida el usecase: un grant inválido es 400 por domain.ErrInvalidInput). 204; 404 rol no
// visible; 422 plantilla global.
func (h *RoleAdminHandler) AddRoleGrant() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roleID, ok := pathValue(w, r, "id")
		if !ok {
			return
		}
		var req grantRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		err := h.roles.GrantToRole(r.Context(), in.RoleGrantInput{RoleID: roleID, Grant: grantFromRequest(req)})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// RemoveRoleGrant sirve DELETE /api/v1/roles/{id}/grants?pattern=…&effect=…: el grant viaja en
// la QUERY (es la identidad de lo que se borra; un DELETE con cuerpo atraviesa mal los proxies),
// recortado. 204 también si no lo tenía; mismos códigos que AddRoleGrant.
func (h *RoleAdminHandler) RemoveRoleGrant() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roleID, ok := pathValue(w, r, "id")
		if !ok {
			return
		}
		input := in.RoleGrantInput{RoleID: roleID, Grant: grantFromQuery(r.URL.Query())}
		if err := h.roles.RevokeFromRole(r.Context(), input); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// AssignRole sirve POST /api/v1/members/{user_id}/roles con `{"role_id"}` (recortado): el puerto
// recibe RoleAssignmentInput{UserID: user_id, RoleID}. 204 (idempotente); 404 si la persona no
// es miembro o el rol no es visible (mismo código a propósito).
func (h *RoleAdminHandler) AssignRole() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathValue(w, r, "user_id")
		if !ok {
			return
		}
		var req assignRoleRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		input := in.RoleAssignmentInput{UserID: userID, RoleID: strings.TrimSpace(req.RoleID)}
		if err := h.roles.AssignRole(r.Context(), input); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// UnassignRole sirve DELETE /api/v1/members/{user_id}/roles/{role_id}: el puerto recibe
// RoleAssignmentInput con los dos comodines. 204; 404 en los mismos casos que AssignRole.
func (h *RoleAdminHandler) UnassignRole() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathValue(w, r, "user_id")
		if !ok {
			return
		}
		roleID, ok := pathValue(w, r, "role_id")
		if !ok {
			return
		}
		input := in.RoleAssignmentInput{UserID: userID, RoleID: roleID}
		if err := h.roles.UnassignRole(r.Context(), input); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// AddUserGrant sirve POST /api/v1/members/{user_id}/grants con `{"pattern","effect"}`: un
// OVERRIDE de grant a una persona (iam_user_grants no tiene tenant: lo acota que sea miembro).
// El puerto recibe UserGrantInput{UserID: user_id, Grant} recortado. 204; 404 no miembro.
func (h *RoleAdminHandler) AddUserGrant() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathValue(w, r, "user_id")
		if !ok {
			return
		}
		var req grantRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		err := h.roles.GrantToUser(r.Context(), in.UserGrantInput{UserID: userID, Grant: grantFromRequest(req)})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// RemoveUserGrant sirve DELETE /api/v1/members/{user_id}/grants?pattern=…&effect=…: quita ese
// override (grant por QUERY, recortado). 204 también si no lo tenía.
func (h *RoleAdminHandler) RemoveUserGrant() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathValue(w, r, "user_id")
		if !ok {
			return
		}
		input := in.UserGrantInput{UserID: userID, Grant: grantFromQuery(r.URL.Query())}
		if err := h.roles.RevokeFromUser(r.Context(), input); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---------------------------------------------------------------------------
// Administración de membresía (in.MembershipAdmin)
// ---------------------------------------------------------------------------

// MembershipHandler sirve el alta y la baja de personas en la empresa del token. Va aparte de
// RoleAdminHandler porque es OTRO permiso (`members.write` frente a `roles.write`).
type MembershipHandler struct {
	members in.MembershipAdmin
}

// NewMembershipHandler construye el handler de membresía.
func NewMembershipHandler(members in.MembershipAdmin) *MembershipHandler {
	return &MembershipHandler{members: members}
}

// List sirve GET /api/v1/members: quién está en la empresa del token. 200 con un ARRAY (vacío
// `[]`, nunca `null`); cada miembro con EXACTAMENTE `user_id`, `tenant_id` y, si existe,
// `created_at` — ni `name` ni `email`: la persona vive en identity-core (INV-02).
func (h *MembershipHandler) List() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// members: `miembros` en el viejo (E-11).
		members, err := h.members.ListMembers(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out := make([]memberDTO, 0, len(members))
		for _, m := range members {
			out = append(out, dtoFromMembership(m))
		}
		writeJSON(w, http.StatusOK, out)
	})
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req memberRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		input := in.MembershipInput{UserID: strings.TrimSpace(req.UserID)}
		if err := h.members.AddMember(r.Context(), input); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Remove sirve DELETE /api/v1/members/{user_id}: el puerto recibe MembershipInput{UserID}. 204
// (idempotente; el DELETE va acotado al tenant del contexto). No retira roles ni grants.
func (h *MembershipHandler) Remove() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathValue(w, r, "user_id")
		if !ok {
			return
		}
		if err := h.members.RemoveMember(r.Context(), in.MembershipInput{UserID: userID}); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
