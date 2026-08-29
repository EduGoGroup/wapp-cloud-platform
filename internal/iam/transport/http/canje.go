package iamhttp

// canje.go — LA PUERTA HTTP DEL CANJE DE UNA INVITACIÓN
// (Plan 047 · Ola A · T-A3, POST /api/v1/invitations/accept).
//
// ------------------------------------------------------------
// EL TOKEN VIAJA EN EL CUERPO, NO EN LA RUTA
// ------------------------------------------------------------
// Decisión de Jhoan del 2026-08-28, y sustituye a la forma `{token}/accept` que
// aún se lee en borradores del plan. Un secreto en la URL no se queda en la URL:
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
// Se garantiza por CONSTRUCCIÓN en tres sitios, y ninguno es un comentario que
// alguien deba recordar:
//
//   (a) EL CUERPO — lo escribe UNA sola sentencia para los dos casos, y quien la
//       elige (codigoIndistinguible) devuelve un `int` y nada más. No es que hoy
//       casualmente devuelvan el mismo texto: es que esa función NO TIENE por
//       dónde devolver un texto. Para que los cuerpos diverjan hay que cambiarle
//       la firma, no despistarse.
//   (b) EL TIEMPO EN LA BASE — los dos caminos hacen UNA sola consulta, la misma,
//       y la ausencia se convierte en un puntero nil que sigue por el mismo
//       código (infra/postgres/canje.go). Un candado sobre el AST cuenta las
//       consultas para que no aparezca una segunda en la rama de la ausencia.
//   (c) EL TIEMPO EN EL DOMINIO — quien decide el veredicto es una función pura
//       sin `context` y sin base de datos (domain.EvaluarCanje): no puede
//       introducir trabajo asimétrico porque no tiene con qué.
//
// ⚠️ LO QUE ESTO **NO** IGUALA, dicho para que nadie lo lea de más: el CÓDIGO DE
// ESTADO sigue siendo distinto (404 frente a 410), porque el criterio de T-A3 lo
// pide así explícitamente. O sea que el endpoint sigue distinguiendo los dos
// casos para quien mire el status; lo que ya no distingue es el cuerpo ni el
// tiempo. Si algún día se quiere cerrar del todo, la conversación es fundir los
// dos códigos en uno —no afinar el mensaje—, y es una decisión de producto: hoy
// el 410 le dice a la UI «pídele otra a tu jefa», que es una frase útil.
//
// Y el 409 sí tiene cuerpo propio: no forma parte del par indistinguible. Ahí
// quien pregunta YA sabe que su token era bueno (o que su propia cuenta tiene un
// problema), así que no hay nada que ocultarle sobre terceros.

import (
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
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

// mensajeInvitacionInservible es el ÚNICO cuerpo de los dos desenlaces que no
// deben poder distinguirse. Está a nivel de paquete y no en línea para que haya
// UNA cadena y no dos que alguien pueda editar por separado.
//
// Su redacción es deliberadamente incapaz de decir POR QUÉ: ni «caducada», ni
// «no encontrada», ni «revocada». No es vaguedad, es el requisito.
const mensajeInvitacionInservible = "esa invitación no se puede usar"

// InvitationRedeemHandler sirve el canje. Es transporte y nada más: no valida el
// token, no consulta nada y no decide desenlaces — traduce JSON ⇄ puerto y
// errores tipados ⇄ códigos HTTP.
type InvitationRedeemHandler struct {
	redeemer in.InvitationRedeemer
}

// NewInvitationRedeemHandler construye el handler sobre el puerto de entrada.
func NewInvitationRedeemHandler(redeemer in.InvitationRedeemer) *InvitationRedeemHandler {
	return &InvitationRedeemHandler{redeemer: redeemer}
}

// Accept sirve POST /api/v1/invitations/accept.
//
// LOS CUATRO DESENLACES DEL CRITERIO DE T-A3:
//   - 204 — canjeada; ya es miembro de la empresa de la invitación.
//   - 409 — conflicto: la invitación ya era terminal (usada o revocada), o esa
//     persona ya es miembro de OTRA empresa (T-A5). En los dos casos la
//     invitación queda INTACTA: sigue usable.
//   - 410 — existía y había vencido.
//   - 404 — no existe ninguna con ese digest. MISMO CUERPO que el 410.
//
// SE MONTA DETRÁS DE `Authenticate` Y SIN `RequirePermission`, que es lo que la
// hace distinta de todas las rutas de roles.go. Quien canjea acaba de
// registrarse: tiene cero membresías, luego su Context Token va SIN empresa y
// SIN un solo grant (D-056.12, exchange.go:resolveTenant/resolveGrants), así que
// cualquier `RequirePermission` le contestaría 403 — a todos, siempre. El
// precedente exacto de este montaje es /api/v1/auth/whoami (bootstrap/http.go),
// la otra ruta de este proceso que un token sin empresa atraviesa.
//
// EL 405 LO DA EL MUX, no este handler: la ruta se registra con el patrón
// método+ruta de Go 1.22, así que un GET ni siquiera llega aquí. Mismo criterio
// que roles.go, y por eso no hay un `if r.Method != http.MethodPost` que sería
// código muerto.
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
		if codigo, indistinguible := codigoIndistinguible(err); indistinguible {
			writeError(w, codigo, mensajeInvitacionInservible)
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

// codigoIndistinguible decide si el error es uno de los DOS desenlaces que
// comparten cuerpo y, si lo es, con qué código sale.
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
func codigoIndistinguible(err error) (int, bool) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, true
	case errors.Is(err, domain.ErrInvitationExpired):
		return http.StatusGone, true
	default:
		return 0, false
	}
}
