// Porta internal/integrations/crmpush/desde_intakes.go @ 36d5a04

package crmpush

// desde_intakes.go — LA SEGUNDA PUERTA del `intake.push`.
//
// La primera es el sink de webhooks del motor de flujos, que empuja el CIERRE DEL
// CARRITO y no pasa por intakes.Service (va por el proyector). Ésta es la del
// DUEÑO: las revisiones que escribe desde su consola. Las dos arman el MISMO
// documento con el MISMO constructor (Build) a propósito — duplicar el armado del
// contrato reintroduciría, repartido en dos sitios, exactamente el defecto que
// T4.10 arregló en uno (R-12).
//
// 🔴 LA DIRECCIÓN DE LA DEPENDENCIA ES LA QUE ES POR UN CICLO. Este paquete importa
// `intakes` (necesita NormalizeStatus y el tipo Detail); `intakes` NO puede importar
// éste. Por eso el adaptador vive AQUÍ y satisface por forma estructural el puerto
// que aquel declara (intakes.CRMPusher).
//
// R-03: este adaptador NO empuja nada por sí solo. Solo encola cuando alguien llama
// a PushRevision —hoy, quien pide el re-empuje de una revisión al Service—; el
// productor de `intake.push` del pipeline normal sigue siendo el sink del carrito.

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Satisface el puerto de verdad y no «de palabra»: sin esta línea, renombrar el
// método o cambiarle un parámetro al puerto se descubriría en el arranque, con un
// error que habla de un tipo anónimo, o —peor— no se descubriría hasta que alguien
// notara que el CRM dejó de recibir revisiones.
var _ intakes.CRMPusher = (*RevisionPusher)(nil)

// RevisionPusher traduce una solicitud del dominio a la forma del contrato y la
// encola. Satisface intakes.CRMPusher.
type RevisionPusher struct{}

// NewRevisionPusher construye el adaptador sobre el encolador y nunca devuelve nil.
// Las dos dependencias son obligatorias y su ausencia se comprueba al empujar, no
// aquí: un adaptador a medias tiene que APAGAR el empuje, nunca matar la escritura
// del dueño que lo invocó.
func NewRevisionPusher(p *Pusher, log logger.Logger) *RevisionPusher {
	panic(pendiente.Implementar("crmpush.NewRevisionPusher"))
}

// PushRevision implementa intakes.CRMPusher: encola la revisión `revisionNo` de la
// solicitud con su ciclo de vida REAL, pasando por Pusher.Push (el mismo gate y el
// mismo Build que la otra puerta, R-13).
//
// No devuelve error porque el puerto no lo admite, y el puerto no lo admite por una
// razón que este método hace evidente: cuando esto corre, la revisión YA está
// escrita y numerada. Todo fallo se loguea aquí y muere aquí. Promete:
//
//   - Adaptador a medias (receptor nil, sin Pusher o sin log): no empuja y no rompe
//     nada.
//   - Traduce: el tenant es el argumento; el contacto, el id, el estado y el total
//     son los de `d`; las líneas cruzan en su orden con sus cinco campos (SKU,
//     etiqueta, personalización, cantidad y precio unitario) sin tocar el dinero.
//     La personalización de LÍNEA sí viaja (D-041.17); la indicación del PEDIDO
//     (customer_note) no — la completa el worker. `event_history_id` no se rellena.
//   - R-12: el número de revisión es `revisionNo` y el estado es el de `d`, CRUDO.
//     Ninguno se sustituye por una constante. Quien normaliza es Build: la
//     prohibición de emitir `closed` vive en UN solo sitio y ninguna puerta puede
//     saltársela por olvido.
//   - Tenant sin puente CRM activo: no encola y no loguea nada propio (Push ya lo
//     dejó en Debug).
//   - Encolado: lo deja en Debug con `crmpush: revisión encolada para el puente CRM`
//     y las claves `tenant`, `intake_id`, `revision_no` y `outbox_id`.
//   - Push devuelve error: lo deja en Error con `crmpush: la revisión del dueño no
//     llegó a la cola del puente; la revisión SÍ está escrita y no se reintenta el
//     encolado` y las claves `tenant`, `intake_id`, `revision_no` y `error`. No se
//     reintenta.
//   - Un PÁNICO del almacén o del gate se contiene: no llega al llamante, y queda en
//     Error con `crmpush: pánico empujando la revisión al puente; la revisión YA
//     está escrita` y las claves `intake_id`, `revision_no` y `panic` (el valor del
//     pánico como texto). Es lo que hace ESTRUCTURAL la promesa de la firma sin
//     error: sin eso, un pánico se llevaría por delante la respuesta de una
//     corrección que YA está escrita en la base, y el dueño la reintentaría creando
//     una revisión de más. Se contiene el ALCANCE del daño, no la noticia.
func (r *RevisionPusher) PushRevision(ctx context.Context, tenantID string, d intakes.Detail, revisionNo int) {
	panic(pendiente.Implementar("crmpush.RevisionPusher.PushRevision"))
}
