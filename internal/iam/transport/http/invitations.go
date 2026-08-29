package iamhttp

// invitations.go — LA PUERTA HTTP DE LAS INVITACIONES (Plan 047 · Ola A · T-A2
// emitir/listar, T-A8 revocar).
//
// Es transporte y nada más: traduce JSON/ruta ⇄ DTOs de in.InvitationAdmin y
// mapea los errores tipados con writeDomainError. Las reglas duras viven en el
// usecase y aquí no se repiten:
//
//   - INV-04 — el tenant sale del CONTEXTO. Fíjate en que el DTO de request no
//     tiene campo `tenant_id`: lo que no se decodifica no se puede colar por el
//     cuerpo. Leerlo del cuerpo es la mutación que el plan declara roja.
//   - El default y el clamp del TTL están en el usecase (usecase/invitations.go),
//     en UN solo sitio. Aquí el `ttl` se lee y se pasa tal cual: una segunda
//     normalización sería una segunda definición del mismo número.
//
// EL MÉTODO NO SE COMPRUEBA AQUÍ, igual que en roles.go: las rutas se montan con
// los patrones método+ruta de Go 1.22 y el 405 lo da el propio http.ServeMux.

import (
	"net/http"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
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
// de claves del JSON del listado. Añadir aquí el hash pone ese test rojo con el
// digest delante — que es exactamente para lo que existe.
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

// instante formatea un *time.Time nullable al wire. nil ⇒ cadena vacía, que con
// el `omitempty` del DTO hace desaparecer la clave: la ausencia se dice no
// mandando el campo, no mandando un cero que parecería el año 1.
func instante(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(rfc3339)
}

// dtoFromInvitation proyecta una domain.Invitation al wire format. `ahora` entra
// como parámetro porque el estado `expired` depende del reloj y no de la fila.
func dtoFromInvitation(inv domain.Invitation, ahora time.Time) invitationDTO {
	dto := invitationDTO{
		ID:         inv.ID,
		Status:     string(inv.Status(ahora)),
		ExpiresAt:  inv.ExpiresAt.UTC().Format(rfc3339),
		RedeemedAt: instante(inv.RedeemedAt),
		RevokedAt:  instante(inv.RevokedAt),
	}
	if inv.RoleID != nil {
		dto.RoleID = *inv.RoleID
	}
	if !inv.CreatedAt.IsZero() {
		dto.CreatedAt = inv.CreatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// InvitationHandler sirve la emisión, el listado y la revocación de las
// invitaciones de la empresa del token.
//
// Va aparte de MembershipHandler aunque comparta los scopes `members.*`: una
// invitación es una membresía EN DIFERIDO —por eso no estrena vocabulario de
// permisos— pero es otro recurso, con otro ciclo de vida y otra tabla.
type InvitationHandler struct {
	invitations in.InvitationAdmin
}

// NewInvitationHandler construye el handler de invitaciones.
func NewInvitationHandler(invitations in.InvitationAdmin) *InvitationHandler {
	return &InvitationHandler{invitations: invitations}
}

// Issue sirve POST /api/v1/invitations: emite una invitación para la empresa del
// token y devuelve el código EN CLARO por única vez.
//
//   - 201 con la invitación y su `token`.
//   - 400 cuerpo JSON mal formado.
//   - 403 el token no trae empresa (D-056.12): sin empresa no hay a qué invitar.
//   - 404 el `role_id` no es visible para esta empresa (incluido el caso «existe,
//     pero es de otra»: no se confirma).
//   - 500 fallo del repositorio o del generador de aleatoriedad.
//
// EL CUERPO ES OPCIONAL y por eso no se usa decodeJSON a secas: una petición sin
// cuerpo es la forma normal de pedir «una invitación con lo de siempre», y
// tratarla como un 400 obligaría a mandar `{}` para no decir nada. Es el mismo
// trato que da IssueEnrollmentCodeHandler, que solo decodifica si hay algo que
// decodificar.
func (h *InvitationHandler) Issue() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req issueInvitationRequest
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSON(w, r, &req) {
				return
			}
		}
		input := in.IssueInvitationInput{TTLSeconds: req.TTLSeconds}
		if rol := strings.TrimSpace(req.RoleID); rol != "" {
			input.RoleID = &rol
		}
		emitida, err := h.invitations.IssueInvitation(r.Context(), input)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, issuedInvitationDTO{
			invitationDTO: dtoFromInvitation(emitida.Invitation, time.Now()),
			Token:         emitida.Token,
		})
	})
}

// List sirve GET /api/v1/invitations: las invitaciones de la empresa del token,
// las más recientes primero. 200 con el arreglo (vacío si no hay ninguna); 403
// si el token no trae empresa; 500 ante fallo del repositorio.
//
// Es LECTURA y por eso se monta con protectRead (sin auditoría), igual que GET
// /api/v1/members.
//
// 🔴 NO DEVUELVE EL TOKEN NI SU DIGEST. Ver invitationDTO.
func (h *InvitationHandler) List() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invitaciones, err := h.invitations.ListInvitations(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		ahora := time.Now()
		out := make([]invitationDTO, 0, len(invitaciones))
		for _, inv := range invitaciones {
			out = append(out, dtoFromInvitation(inv, ahora))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// Revoke sirve DELETE /api/v1/invitations/{id}: anula una invitación VIVA de la
// empresa del token (T-A8), de modo que un canje posterior de ese código falle y
// no escriba ninguna membresía.
//
//   - 204 revocada. También si YA estaba revocada: la baja de algo ya dado de
//     baja es el estado que se pedía, y un error ahí haría que reintentar una
//     revocación pareciera un fallo.
//   - 403 el token no trae empresa.
//   - 404 no existe, o es de OTRA empresa (mismo código a propósito), o el `id`
//     ni siquiera es un UUID.
//   - 409 ya fue CANJEADA. No es lo mismo que «ya revocada» y no se puede fingir
//     que sí: revocar una invitación consumida NO deshace la membresía que el
//     canje escribió, y contestar 204 le diría a quien administra que acaba de
//     retirarle el acceso a alguien que sigue dentro. La baja de esa persona es
//     otra puerta (DELETE /api/v1/members/{user_id}).
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
