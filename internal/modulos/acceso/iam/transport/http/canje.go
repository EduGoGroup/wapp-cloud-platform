// Porta internal/iam/transport/http/canje.go @ 9a77307

package iamhttp

// canje.go — LA PUERTA HTTP DEL CANJE DE UNA INVITACIÓN
// (Plan 047 · Ola A · T-A3, POST /api/v1/invitations/accept). El nombre del fichero se conserva
// (T-15).
//
// ------------------------------------------------------------
// EL TOKEN VIAJA EN EL CUERPO, NO EN LA RUTA
// ------------------------------------------------------------
// Decisión de Jhoan del 2026-08-28. Un secreto en la URL no se queda en la URL:
// acaba en el log de acceso de cualquier proxy que haya delante, en la cabecera
// `Referer` de lo que sea que se cargue después, y en el historial del navegador
// de quien lo pegó. El cuerpo de un POST no aparece en ninguno de los tres.
// Aquí mismo, este proceso registra cada petición con su ruta (accessLog): con el
// token en la ruta, el único secreto del flujo quedaría escrito en nuestros
// propios ficheros de registro.
//
// ------------------------------------------------------------
// 🔴 LOS DOS DESENLACES QUE NO SE PUEDEN DISTINGUIR (requisito anti-oráculo)
// ------------------------------------------------------------
// «No existe» (404) y «caducada» (410) tienen que responder el MISMO CUERPO y
// costar el MISMO TIEMPO. Si no, quien tenga una lista de tokens sospechosos
// puede sondearlos uno a uno y averiguar CUÁLES EXISTIERON alguna vez — y de
// paso, cuándo se emiten invitaciones en esta empresa.
//
// Se garantiza por CONSTRUCCIÓN en tres sitios: (a) el cuerpo lo escribe UNA sola sentencia para
// los dos casos, y quien elige el código (indistinguishableCode) devuelve un `int` y nada más (no
// tiene por dónde devolver un texto: para que los cuerpos diverjan hay que cambiarle la firma); (b) el tiempo en la base: los dos caminos hacen UNA sola consulta
// (infra/postgres/canje.go, con su candado AST); (c) el tiempo en el dominio: el veredicto lo da
// una función pura sin `context` ni base (domain.EvaluarCanje).
//
// ⚠️ LO QUE ESTO **NO** IGUALA: el CÓDIGO DE ESTADO sigue siendo distinto (404 frente a 410),
// porque el criterio de T-A3 lo pide así. Fundir los dos códigos sería una decisión de producto:
// hoy el 410 le dice a la UI «pídele otra a tu jefa».
//
// Y el 409 sí tiene cuerpo propio: no forma parte del par indistinguible. Ahí
// quien pregunta YA sabe que su token era bueno (o que su propia cuenta tiene un
// problema), así que no hay nada que ocultarle sobre terceros.

import (
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// redeemInvitationRequest es el cuerpo de POST /api/v1/invitations/accept.
//
// 🔴 TIENE UN SOLO CAMPO, Y ESO ES INV-04 ESCRITO EN UN TIPO. La empresa a la
// que entra esta persona sale de la FILA de la invitación —la eligió quien la
// emitió, con su propio token— y no puede salir de ningún otro sitio, porque no
// hay ningún otro sitio: un `tenant_id` en el cuerpo no se ignoraría, es que no
// se decodifica. Lo que no existe no se puede colar.
//
// Que siga siendo un solo campo lo vigila un test que compara el conjunto EXACTO
// de claves JSON del struct (canje_test.go), no la buena memoria de quien añada
// el siguiente campo.
type redeemInvitationRequest struct {
	Token string `json:"token"`
}

// unusableInvitationMessage (`mensajeInvitacionInservible` en el viejo, E-11) es el ÚNICO
// cuerpo de los dos desenlaces que no deben poder distinguirse. Está a nivel de paquete y no en
// línea para que haya UNA cadena y no dos que alguien pueda editar por separado.
//
// Su redacción es deliberadamente incapaz de decir POR QUÉ: ni «caducada», ni
// «no encontrada», ni «revocada». No es vaguedad, es el requisito.
const unusableInvitationMessage = "esa invitación no se puede usar"

// InvitationRedeemHandler sirve el canje. Es transporte y nada más: no valida el
// token, no consulta nada y no decide desenlaces — traduce JSON ⇄ puerto
// (in.InvitationRedeemer) y errores tipados ⇄ códigos HTTP.
type InvitationRedeemHandler struct {
	redeemer in.InvitationRedeemer
}

// NewInvitationRedeemHandler construye el handler sobre el puerto de entrada.
func NewInvitationRedeemHandler(redeemer in.InvitationRedeemer) *InvitationRedeemHandler {
	return &InvitationRedeemHandler{redeemer: redeemer}
}

// Accept sirve POST /api/v1/invitations/accept. Se monta detrás de `Authenticate` y SIN
// `RequirePermission` (quien canjea acaba de registrarse: su token va sin empresa ni grants); el
// 405 lo da el mux (patrón método+ruta de Go 1.22).
//
// R-H4, LOS DESENLACES:
//   - 204 SIN cuerpo — canjeada.
//   - 404 (domain.ErrNotFound) y 410 (domain.ErrInvitationExpired) — con BYTES IDÉNTICOS de
//     cuerpo y el mismo Content-Type: `{"error":"esa invitación no se puede usar"}`, un texto
//     que no dice POR QUÉ (ni caducada, ni no encontrada, ni revocada). Solo cambia el código.
//   - 409 (domain.ErrConflict) — voz PROPIA, distinta del par: «esa invitación ya no está
//     disponible, o esta cuenta ya pertenece a una empresa» (funde a propósito «ya usada o
//     anulada» y «ya perteneces a otra empresa»).
//   - 400 «falta el token de invitación» (domain.ErrInvalidInput); 400 «cuerpo JSON inválido»
//     con JSON roto, sin llamar al puerto.
//   - 500 «error interno» — cualquier otro error.
//
// R-H4: el token viaja TAL CUAL al puerto, sin normalizar ni recortar: la normalización vive
// dentro de domain.HashInvitationToken, en el único sitio que hashea.
//
// R-H5: el cuerpo tiene UN campo, `token`. La empresa sale de la FILA de la invitación (INV-04):
// un `tenant_id` en el cuerpo no se ignora, es que no tiene dónde aterrizar.
func (h *InvitationRedeemHandler) Accept() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req redeemInvitationRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		err := h.redeemer.RedeemInvitation(r.Context(), req.Token)

		// 🔴 EL PUNTO DE SALIDA ÚNICO DEL PAR INDISTINGUIBLE. Va ANTES del switch
		// de abajo y se lleva sus dos casos, de modo que el cuerpo de 404 y 410 se
		// escribe en UNA sola línea con el código como única variable. No añadas
		// abajo una rama para ErrNotFound o ErrInvitationExpired: sería código
		// muerto, y el día que dejara de serlo habría dos cuerpos.
		// (code e indistinguishable: `codigo` e `indistinguible` en el viejo, E-11.)
		if code, indistinguishable := indistinguishableCode(err); indistinguishable {
			writeError(w, code, unusableInvitationMessage)
			return
		}

		switch {
		case err == nil:
			// 204 y no 200 con cuerpo: no hay nada que devolver. Lo que la persona
			// necesita después —su empresa y sus grants— no está en esta respuesta,
			// está en su SIGUIENTE Context Token: el que tiene en la mano se emitió
			// sin empresa y sigue sin ella. Tiene que volver a canjear su Identity
			// Token para que resolveTenant encuentre ya la membresía.
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, domain.ErrConflict):
			// Funde DOS causas a propósito: «esa invitación ya se usó o se anuló» y
			// «tú ya perteneces a otra empresa». Separarlas le diría a quien pregunta
			// algo sobre el estado interno de una empresa a la que todavía no
			// pertenece. Quien de verdad necesite distinguirlas —la dueña— lo ve en
			// su listado de invitaciones, que sí es suyo.
			writeError(w, http.StatusConflict, "esa invitación ya no está disponible, o esta cuenta ya pertenece a una empresa")
		case errors.Is(err, domain.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "falta el token de invitación")
		default:
			writeError(w, http.StatusInternalServerError, "error interno")
		}
	})
}

// indistinguishableCode (`codigoIndistinguible` en el viejo, E-11) decide si el error es uno de
// los DOS desenlaces que comparten cuerpo y, si lo es, con qué código sale.
//
// 🔴 DEVUELVE UN `int` Y UN `bool`, Y ESA FIRMA ES LA GARANTÍA. No devuelve
// mensaje porque no debe poder devolverlo: mientras el cuerpo no sea un valor
// que esta función produzca, los dos desenlaces no pueden divergir en el cuerpo
// por mucho que alguien edite las ramas de aquí abajo. Es la diferencia entre
// «hoy coinciden» y «no pueden dejar de coincidir sin cambiar una firma».
//
// No se usa writeDomainError para estos dos, y es el motivo entero de que esta
// función exista: aquel mapea ErrNotFound a «recurso no encontrado» y no tiene
// rama para la caducidad, así que los dos cuerpos saldrían distintos.
func indistinguishableCode(err error) (int, bool) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, true
	case errors.Is(err, domain.ErrInvitationExpired):
		return http.StatusGone, true
	default:
		return 0, false
	}
}
