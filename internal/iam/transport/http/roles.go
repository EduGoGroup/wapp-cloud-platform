package iamhttp

// roles.go — LA PUERTA HTTP DEL PLANO DE ROLES DEL TENANT (Plan 047 · Ola 1.0 ·
// T1.0-4, plano 2 del ADR-0033).
//
// Es transporte y NADA más: traduce JSON/ruta/query ⇄ DTOs de los puertos in
// (in.RoleAdmin, in.MembershipAdmin) y mapea los errores tipados del dominio a
// códigos HTTP con writeDomainError. Las tres reglas duras viven en el usecase y
// aquí no se repiten ni se pueden saltar:
//
//   - INV-04 — el tenant sale del CONTEXTO de identidad. Fíjate en que ningún
//     DTO de request de este fichero tiene campo `tenant_id`: el usecase lo
//     resuelve por su in.CallerResolver, y lo que no se decodifica no se puede
//     colar por el cuerpo.
//   - Un recurso de otra empresa se contesta 404, NUNCA 403 (visibleRole /
//     requireMember devuelven domain.ErrNotFound). Un "prohibido" confirmaría que
//     ese rol o esa persona existen en otra empresa; el 404 no dice nada.
//   - Las plantillas globales se leen y se asignan pero no se editan
//     (domain.ErrGlobalRoleImmutable → 422): el cuerpo está bien formado, lo que
//     no se puede procesar es lo que pide.
//
// EL MÉTODO NO SE COMPRUEBA AQUÍ, A PROPÓSITO. Estas rutas se montan con los
// patrones método+ruta de Go 1.22 (publicapi.Register), y es el propio
// http.ServeMux quien devuelve el 405 con su cabecera Allow ante un método que
// no está registrado para esa ruta. Un `if r.Method != ...` dentro del handler
// sería código muerto que nunca se ejecuta — y, peor, daría la impresión de que
// el 405 lo garantiza el handler cuando lo garantiza el registro. Las rutas de
// auth.go sí lo comprueban porque se montan por PATH pelado (mux.Handle sin
// verbo) y allí el mux no puede decidirlo.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
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
// mux si algún día se monta por otra vía.
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
// —tanto los del rol como los overrides de una persona—.
//
// Depende SOLO del puerto in.RoleAdmin, como AuthHandler depende de in.
// TokenVerifier: el transporte no conoce ni repositorios ni SQL.
type RoleAdminHandler struct {
	roles in.RoleAdmin
}

// NewRoleAdminHandler construye el handler del plano de roles.
func NewRoleAdminHandler(roles in.RoleAdmin) *RoleAdminHandler {
	return &RoleAdminHandler{roles: roles}
}

// List sirve GET /api/v1/roles: los roles VISIBLES para la empresa del token
// (los suyos más las plantillas globales). 200 con el arreglo (vacío si no hay);
// 403 si el token no trae empresa (D-056.12); 500 ante fallo del repositorio.
//
// Es LECTURA y por eso se monta con protectRead (sin auditoría): no tiene efecto
// y el patrón vigente no audita las lecturas.
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

// Create sirve POST /api/v1/roles: crea un rol CUSTOM de la empresa del token.
// Por aquí no se crean plantillas globales, ni pidiéndolo.
//
//   - 201 con el rol creado.
//   - 400 cuerpo inválido o `name` vacío.
//   - 403 token sin empresa.
//   - 404 el `parent_role_id` no es visible para esta empresa (incluido el caso
//     "existe, pero es de otra": no se confirma).
//   - 409 ya hay un rol con ese nombre en la empresa.
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

// AddRoleGrant sirve POST /api/v1/roles/{id}/grants: añade un grant a un rol
// PROPIO de la empresa. Idempotente (el repositorio lo es).
//
//   - 204 concedido.
//   - 400 cuerpo inválido, pattern vacío o effect fuera de {allow,deny}.
//   - 404 el rol no es visible para esta empresa.
//   - 422 el rol es una plantilla global: visible y asignable, pero no editable
//     (sus grants valen para TODOS los tenants a la vez).
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

// RemoveRoleGrant sirve DELETE /api/v1/roles/{id}/grants?pattern=…&effect=…:
// quita un grant de un rol propio. No-op si no lo tenía (204 igual). Mismos
// códigos que AddRoleGrant; el grant viaja en la query (ver grantFromQuery).
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

// AssignRole sirve POST /api/v1/members/{user_id}/roles: asigna un rol visible a
// un MIEMBRO de la empresa del token, y la asignación queda acotada a esa
// empresa (nunca global).
//
//   - 204 asignado (idempotente).
//   - 400 cuerpo inválido o `role_id` vacío.
//   - 404 la persona no es miembro de esta empresa, o el rol no es visible.
//     Los dos casos comparten código a propósito: distinguirlos diría si ese
//     UUID pertenece a otra empresa.
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

// UnassignRole sirve DELETE /api/v1/members/{user_id}/roles/{role_id}: retira la
// asignación ACOTADA a la empresa del token. La global, si la hubiera, no se
// toca desde aquí (out.RoleRepo.UnassignFromUser es simétrico al alta). 204;
// 404 en los mismos dos casos que AssignRole.
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

// AddUserGrant sirve POST /api/v1/members/{user_id}/grants: concede un OVERRIDE
// de grant a una persona, por encima de los de su rol.
//
// Va bajo /members/{user_id} y no bajo un /users propio a propósito:
// iam_user_grants no tiene columna de tenant, y lo único que mantiene el
// override acotado es que esa persona sea miembro de la empresa del llamante
// (requireMember). La ruta dice esa condición en voz alta. 204; 400 grant
// inválido; 404 la persona no es miembro de esta empresa.
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

// RemoveUserGrant sirve DELETE /api/v1/members/{user_id}/grants?pattern=…&effect=…:
// quita ese override. No-op si no lo tenía. Mismos códigos que AddUserGrant.
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

// MembershipHandler sirve el alta y la baja de personas en la empresa del token.
// Va aparte de RoleAdminHandler porque es OTRO permiso (`members.write` frente a
// `roles.write`): quien administra quién está en la empresa no es
// necesariamente quien administra qué puede hacer cada cual.
type MembershipHandler struct {
	members in.MembershipAdmin
}

// NewMembershipHandler construye el handler de membresía.
func NewMembershipHandler(members in.MembershipAdmin) *MembershipHandler {
	return &MembershipHandler{members: members}
}

// List sirve GET /api/v1/members: quién está en la empresa del token. 200 con el
// arreglo (vacío si la empresa aún no tiene a nadie); 403 si el token no trae
// empresa; 500 ante fallo del repositorio.
//
// Es LECTURA: se monta con protectRead y NO se audita, igual que GET
// /api/v1/roles. Y es la única de este par que un `viewer` alcanza — su glob
// '*.read' cubre members.read y ninguno de los .write.
func (h *MembershipHandler) List() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		miembros, err := h.members.ListMembers(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out := make([]memberDTO, 0, len(miembros))
		for _, m := range miembros {
			out = append(out, dtoFromMembership(m))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// Add sirve POST /api/v1/members: da de alta a la persona en la empresa del
// token. El llamante NO elige empresa (INV-04): el cuerpo solo trae `user_id`.
//
//   - 204 y no 201: la operación es IDEMPOTENTE y no devuelve recurso. Repetirla
//     no crea una segunda membresía ni falla, así que un 201 mentiría la mitad
//     de las veces.
//   - 400 cuerpo inválido o `user_id` vacío.
//   - 409 esa persona YA es miembro de OTRA empresa. 🔧 Hasta el 2026-08-29 esto
//     se justificaba con «una segunda membresía rompe el canje de su token»; ya
//     no es cierto —el canje resuelve con varias desde el Plan 047 · Ola 5 · T5.1
//     (empresa activa, D-047.14)—. El 409 se queda porque el lado de la ESCRITURA
//     sigue sin decidirse: su levantamiento es MD-055.2.
//
// LOS SEIS DESENLACES (Plan 047 · Ola B). Desde que el alta acredita también la
// aplicación en identity, un 204 dejó de significar «se escribió una fila» para
// significar «esa persona es miembro Y puede entrar». Los códigos de fallo
// separan de quién es el problema, que es lo único que sirve para actuar:
//
//	alta correcta (con o sin escritura en identity)   204, sin cuerpo
//	falta la credencial M2M de este despliegue        503 {"error":"identity_no_configurado"}
//	identity no contesta (ErrIdentityUnavailable)     503 {"error":"identity no está disponible"}
//	identity no acredita `wapp.bff` (ErrSystemNotAllowed) 502 {"error":"system_no_acreditable"}
//	el UUID no existe en identity (ErrNotFound)       404
//	ya es miembro de otra empresa (ErrConflict)       409
//
// «Con o sin escritura en identity» no es un matiz: si la persona ya tenía la
// aplicación, el alta NO llama al PUT y responde 204 igual. Desde fuera los dos
// caminos son indistinguibles a propósito — el 204 promete un ESTADO, no un
// número de escrituras.
//
// 🔴 El 404 de aquí es el de identity —esa persona no está en el padrón del
// grupo—, no el de una ruta inexistente ni el de un recurso ajeno. Llega porque
// el alta LEE los accesos de la persona antes de escribir, y esa lectura es la
// primera vez que wApp comprueba que el UUID que le pasaron existe: hasta esta
// ola, un UUID inventado producía una fila en tenant_members y un 204.
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

// Remove sirve DELETE /api/v1/members/{user_id}: da de baja a la persona de la
// empresa del token. Idempotente (204 aunque no fuera miembro) y acotada: el
// DELETE lleva el tenant del contexto, así que pasar el UUID de alguien de otra
// empresa no borra nada.
//
// ⚠️ NO retira sus roles ni sus grants (ver out.MembershipRepo.Remove): sin
// membresía el canje ya no le resuelve permisos, y readmitirla no obliga a
// reconstruir su rol.
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
