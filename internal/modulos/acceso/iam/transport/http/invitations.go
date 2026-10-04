// Porta internal/iam/transport/http/invitations.go @ 9a77307

package iamhttp

// invitations.go — LA PUERTA HTTP DE LAS INVITACIONES (Plan 047 · Ola A · T-A2
// emitir/listar, T-A8 revocar): POST/GET /api/v1/invitations y DELETE /api/v1/invitations/{id}.
//
// Es transporte y nada más: traduce JSON/ruta ⇄ DTOs de in.InvitationAdmin y
// mapea los errores tipados con writeDomainError (R-H7, ver roles.go). Las reglas duras viven en
// el usecase y aquí no se repiten:
//
//   - INV-04 — el tenant sale del CONTEXTO. El cuerpo de la emisión no tiene campo
//     `tenant_id`: lo que no se decodifica no se puede colar.
//   - El default y el clamp del TTL están en el usecase, en UN solo sitio. Aquí el `ttl` se lee
//     y se pasa tal cual: una segunda normalización sería una segunda definición del número.
//
// EL MÉTODO NO SE COMPRUEBA AQUÍ, igual que en roles.go: el 405 lo da el http.ServeMux.

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InvitationHandler sirve la emisión, el listado y la revocación de las invitaciones de la
// empresa del token. Va aparte de MembershipHandler aunque comparta los scopes `members.*`: una
// invitación es una membresía EN DIFERIDO, pero es otro recurso, con otro ciclo de vida.
type InvitationHandler struct{}

// NewInvitationHandler construye el handler de invitaciones.
func NewInvitationHandler(invitations in.InvitationAdmin) *InvitationHandler {
	panic(pendiente.Implementar("iamhttp.NewInvitationHandler"))
}

// Issue sirve POST /api/v1/invitations con `{"role_id","ttl"}`, los dos OPCIONALES.
//
// R-H8:
//   - EL CUERPO ES OPCIONAL: sin cuerpo (o `{}`) es una petición válida — invitación sin rol y
//     con la caducidad por defecto. Con cuerpo, un JSON roto es 400 sin llamar al puerto.
//   - `role_id` llega RECORTADO; vacío (o solo espacios) ⇒ RoleID nil (alta sin rol).
//   - `ttl` (segundos) llega TAL CUAL a TTLSeconds, sin default ni clamp (son del usecase).
//   - 201 con la invitación y su `token` EN CLARO, por única vez: `id`, `status`, `expires_at`,
//     `token` y, solo si existen, `role_id`, `created_at`, `redeemed_at`, `revoked_at`. Nunca
//     `token_hash`.
//   - Errores por writeDomainError: 403 sin empresa; 404 el rol no es visible; 500 el resto.
func (h *InvitationHandler) Issue() http.Handler {
	panic(pendiente.Implementar("iamhttp.InvitationHandler.Issue"))
}

// List sirve GET /api/v1/invitations: las invitaciones de la empresa del token, en el orden del
// puerto (más recientes primero). 200 con un ARRAY (vacío `[]`, nunca `null`).
//
// R-H8: cada invitación lleva EXACTAMENTE `id`, `status`, `expires_at` y, solo si existen,
// `role_id`, `created_at`, `redeemed_at` y `revoked_at` (RFC 3339 UTC). 🔴 Ni `token` ni
// `token_hash`: el digest es la clave de acceso del canje. `status` es DERIVADO con el reloj del
// servidor al servir (domain.Invitation.Status: canje > revocación > caducidad), no una
// columna. Errores por writeDomainError (403 sin empresa).
func (h *InvitationHandler) List() http.Handler {
	panic(pendiente.Implementar("iamhttp.InvitationHandler.List"))
}

// Revoke sirve DELETE /api/v1/invitations/{id}: el puerto recibe el `id` de la ruta.
//
// R-H8: 204 revocada (también si YA lo estaba); 403 sin empresa; 404 no existe, es de OTRA
// empresa o el id no es un UUID (mismo código); 409 ya fue CANJEADA (revocarla no deshace la
// membresía). Comodín vacío ⇒ 400 «id requerido en la ruta» sin llamar al puerto.
func (h *InvitationHandler) Revoke() http.Handler {
	panic(pendiente.Implementar("iamhttp.InvitationHandler.Revoke"))
}
