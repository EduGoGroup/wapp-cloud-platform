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
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// issueInvitationRequest es el cuerpo de POST /api/v1/invitations. Los dos
// campos son OPCIONALES y un cuerpo vacío (`{}`, o ninguno) es una petición
// válida: invitación sin rol y con la caducidad por defecto.
//
// 🔴 NO HAY CAMPO DE CORREO, NI DE NOMBRE, NI DE TELÉFONO, y su ausencia es el
// contrato (D-047.11): quien emite no teclea el correo de nadie. Reparte el
// código por WhatsApp y la nube nunca sabe a quién se lo mandó — por eso la
// tabla no tiene ni una columna de texto donde algo así quepa.
type issueInvitationRequest struct {
	// RoleID es el rol que se concederá al canjear. Vacío = alta sin rol.
	RoleID string `json:"role_id"`
	// TTLSeconds es la vida en segundos. Se llama `ttl` en el cable, igual que en
	// los códigos de enrolamiento (platformadmin.IssueEnrollmentCodeRequest): las
	// dos emisiones de código de un solo uso de la casa se explican con la misma
	// frase, y un segundo nombre para lo mismo obligaría a recordar cuál va dónde.
	TTLSeconds int `json:"ttl"`
}

// invitationDTO es la proyección pública de una invitación.
//
// 🔴 LO QUE NO ESTÁ AQUÍ ES EL CONTRATO: ni `token` ni `token_hash`. El token en
// claro existió una sola vez —en la respuesta del POST, y de ahí a WhatsApp— y
// el digest no sale nunca, porque es la clave de acceso del canje: quien lo
// tuviera podría buscar la fila por él. domain.Invitation SÍ lleva el TokenHash
// (es la fila), así que la separación no la garantiza el tipo: la garantiza esta
// proyección, y que se mantenga lo vigila un test que compara el conjunto EXACTO
// de claves del JSON del listado.
type invitationDTO struct {
	ID string `json:"id"`
	// Status es DERIVADO, no una columna: pending | redeemed | revoked | expired.
	// Se sirve calculado y no se deja deducir de las tres marcas de tiempo porque
	// la caducidad no tiene escritura que la anuncie —ocurre por el paso del
	// tiempo— y cada consumidor que lo dedujera por su cuenta escribiría otra vez
	// la regla de precedencia (canje > revocación > caducidad).
	Status string `json:"status"`
	// ExpiresAt es cuándo deja de valer. Va SIEMPRE informado.
	ExpiresAt string `json:"expires_at"`
	// RoleID es el rol que concederá al canjearse; ausente = alta sin rol.
	RoleID    string `json:"role_id,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// RedeemedAt y RevokedAt solo aparecen cuando ocurrieron: son las dos
	// escrituras terminales, y su presencia es la que explica el `status`.
	RedeemedAt string `json:"redeemed_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

// issuedInvitationDTO es la respuesta del POST: la invitación MÁS el token en
// claro. Es el ÚNICO sitio de todo el sistema donde ese texto viaja.
type issuedInvitationDTO struct {
	invitationDTO
	// Token es el código opaco que quien emite reparte por WhatsApp. No se
	// persiste (en la tabla vive su SHA-256) y no se puede volver a consultar: si
	// se pierde, se revoca esta y se emite otra.
	Token string `json:"token"`
}

// instant (`instante` en el viejo, E-11) formatea un *time.Time nullable al wire. nil ⇒ cadena
// vacía, que con el `omitempty` del DTO hace desaparecer la clave: la ausencia se dice no
// mandando el campo, no mandando un cero que parecería el año 1.
func instant(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(rfc3339)
}

// dtoFromInvitation proyecta una domain.Invitation al wire format. `now` (`ahora` en el viejo,
// E-11) entra como parámetro porque el estado `expired` depende del reloj y no de la fila.
func dtoFromInvitation(inv domain.Invitation, now time.Time) invitationDTO {
	dto := invitationDTO{
		ID:         inv.ID,
		Status:     string(inv.Status(now)),
		ExpiresAt:  inv.ExpiresAt.UTC().Format(rfc3339),
		RedeemedAt: instant(inv.RedeemedAt),
		RevokedAt:  instant(inv.RevokedAt),
	}
	if inv.RoleID != nil {
		dto.RoleID = *inv.RoleID
	}
	if !inv.CreatedAt.IsZero() {
		dto.CreatedAt = inv.CreatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// InvitationHandler sirve la emisión, el listado y la revocación de las invitaciones de la
// empresa del token. Va aparte de MembershipHandler aunque comparta los scopes `members.*`: una
// invitación es una membresía EN DIFERIDO, pero es otro recurso, con otro ciclo de vida.
type InvitationHandler struct {
	invitations in.InvitationAdmin
}

// NewInvitationHandler construye el handler de invitaciones.
func NewInvitationHandler(invitations in.InvitationAdmin) *InvitationHandler {
	return &InvitationHandler{invitations: invitations}
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EL CUERPO ES OPCIONAL y por eso no se usa decodeJSON a secas: una petición sin
		// cuerpo es la forma normal de pedir «una invitación con lo de siempre», y
		// tratarla como un 400 obligaría a mandar `{}` para no decir nada. Es el mismo
		// trato que da IssueEnrollmentCodeHandler, que solo decodifica si hay algo que
		// decodificar.
		var req issueInvitationRequest
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSON(w, r, &req) {
				return
			}
		}
		input := in.IssueInvitationInput{TTLSeconds: req.TTLSeconds}
		// role e issued: `rol` y `emitida` en el viejo (E-11).
		if role := strings.TrimSpace(req.RoleID); role != "" {
			input.RoleID = &role
		}
		issued, err := h.invitations.IssueInvitation(r.Context(), input)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, issuedInvitationDTO{
			invitationDTO: dtoFromInvitation(issued.Invitation, time.Now()),
			Token:         issued.Token,
		})
	})
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// invitations y now: `invitaciones` y `ahora` en el viejo (E-11).
		invitations, err := h.invitations.ListInvitations(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		now := time.Now()
		out := make([]invitationDTO, 0, len(invitations))
		for _, inv := range invitations {
			out = append(out, dtoFromInvitation(inv, now))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// Revoke sirve DELETE /api/v1/invitations/{id}: el puerto recibe el `id` de la ruta.
//
// R-H8: 204 revocada (también si YA lo estaba); 403 sin empresa; 404 no existe, es de OTRA
// empresa o el id no es un UUID (mismo código); 409 ya fue CANJEADA (revocarla no deshace la
// membresía). Comodín vacío ⇒ 400 «id requerido en la ruta» sin llamar al puerto.
func (h *InvitationHandler) Revoke() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathValue(w, r, "id")
		if !ok {
			return
		}
		if err := h.invitations.RevokeInvitation(r.Context(), id); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
