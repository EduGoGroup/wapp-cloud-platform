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
// los dos casos, y quien elige el código devuelve un `int` y nada más (no tiene por dónde
// devolver un texto); (b) el tiempo en la base: los dos caminos hacen UNA sola consulta
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
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InvitationRedeemHandler sirve el canje. Es transporte y nada más: no valida el
// token, no consulta nada y no decide desenlaces — traduce JSON ⇄ puerto
// (in.InvitationRedeemer) y errores tipados ⇄ códigos HTTP.
type InvitationRedeemHandler struct{}

// NewInvitationRedeemHandler construye el handler sobre el puerto de entrada.
func NewInvitationRedeemHandler(redeemer in.InvitationRedeemer) *InvitationRedeemHandler {
	panic(pendiente.Implementar("iamhttp.NewInvitationRedeemHandler"))
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
	panic(pendiente.Implementar("iamhttp.InvitationRedeemHandler.Accept"))
}
