package crmpush

// desde_intakes.go — LA SEGUNDA PUERTA del `intake.push`.
//
// La primera es el WebhookSink del motor de flujos, que empuja el CIERRE DEL
// CARRITO y no pasa por intakes.Service (va por el proyector). Ésta es la del
// DUEÑO: las revisiones que escribe desde su consola. Las dos arman el MISMO
// documento con el MISMO constructor (Build) a propósito — duplicar el armado del
// contrato reintroduciría, repartido en dos sitios, exactamente el defecto que
// T4.10 acaba de arreglar en uno.
//
// 🔴 LA DIRECCIÓN DE LA DEPENDENCIA ES LA QUE ES POR UN CICLO. Este paquete importa
// internal/intakes (necesita NormalizeStatus y el tipo Detail); internal/intakes NO
// puede importar éste. Por eso el adaptador vive AQUÍ y satisface por forma
// estructural el puerto que aquel declara (intakes.CRMPusher) — el mismo mecanismo
// con el que integrations.EntitlementsGate satisface a runtime.WebhookGate sin que
// ninguno de los dos importe al otro.

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Satisface el puerto de verdad y no «de palabra»: sin esta línea, renombrar el
// método o cambiarle un parámetro al puerto se descubriría en bootstrap.go, con un
// error que habla de un tipo anónimo, o —peor— no se descubriría hasta que alguien
// notara que el CRM dejó de recibir revisiones.
var _ intakes.CRMPusher = (*RevisionPusher)(nil)

// RevisionPusher traduce una solicitud del dominio a la forma del contrato y la
// encola. Satisface intakes.CRMPusher.
type RevisionPusher struct {
	pusher *Pusher
	log    logger.Logger
}

// NewRevisionPusher construye el adaptador sobre el encolador. Las dos
// dependencias son obligatorias y su ausencia se comprueba al empujar, no aquí: un
// adaptador a medias tiene que APAGAR el empuje, nunca matar la escritura del dueño
// que lo invocó.
func NewRevisionPusher(p *Pusher, log logger.Logger) *RevisionPusher {
	return &RevisionPusher{pusher: p, log: log}
}

// PushRevision implementa intakes.CRMPusher: encola la revisión `revisionNo` de la
// solicitud con su ciclo de vida REAL.
//
// No devuelve error porque el puerto no lo admite, y el puerto no lo admite por una
// razón que este método hace evidente: cuando esto corre, la revisión YA está
// escrita y numerada. Todo fallo se loguea aquí y muere aquí.
//
// El estado va CRUDO a Build, que es quien normaliza (intakes.NormalizeStatus): la
// prohibición de emitir `closed` vive en UN solo sitio y ninguna puerta puede
// saltársela por olvido.
func (r *RevisionPusher) PushRevision(ctx context.Context, tenantID string, d intakes.Detail, revisionNo int) {
	if r == nil || r.pusher == nil || r.log == nil {
		return // adaptador a medias: no empuja, pero tampoco rompe nada
	}
	defer r.contenerPánico(d, revisionNo)

	res, err := r.pusher.Push(ctx, Input{
		TenantID:  tenantID,
		ContactID: d.ContactID,
		IntakeID:  d.ID,
		// CRUDO: lo normaliza Build. Ver el doc del campo en Input.
		LifecycleStatus: d.Status,
		RevisionNo:      revisionNo,
		Items:           líneas(d.Items),
		Total:           d.Total,
	})
	log := r.log.With("tenant", tenantID, "intake_id", d.ID, "revision_no", revisionNo)
	if err != nil {
		log.Error("crmpush: la revisión del dueño no llegó a la cola del puente; "+
			"la revisión SÍ está escrita y no se reintenta el encolado", "error", err)
		return
	}
	if !res.Enqueued {
		return // tenant sin puente CRM activo; crmpush ya lo dejó en debug
	}
	log.Debug("crmpush: revisión encolada para el puente CRM", "outbox_id", res.OutboxID)
}

// contenerPánico es lo que hace ESTRUCTURAL la promesa de la firma sin error, y no
// solo una convención: sin esto, un pánico en el store o en el gate se llevaría por
// delante la respuesta de una corrección que YA está escrita en la base, y el dueño
// la reintentaría creando una revisión de más. Se contiene el ALCANCE del daño, no
// la noticia: el pánico entero queda en Error.
func (r *RevisionPusher) contenerPánico(d intakes.Detail, revisionNo int) {
	p := recover()
	if p == nil {
		return
	}
	r.log.Error("crmpush: pánico empujando la revisión al puente; la revisión YA está escrita",
		"intake_id", d.ID, "revision_no", revisionNo, "panic", fmt.Sprint(p))
}

// líneas traduce las líneas del dominio a las del contrato. La personalización de
// LÍNEA sí viaja (D-041.17): es dato de producción y vive en claro en intake_items.
// La indicación del PEDIDO (customer_note) no está aquí a propósito — la completa el
// worker justo antes del POST, por exposición y no por coste. Ver crmpush.Payload.
func líneas(items []intakes.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		out = append(out, Item{
			SKU:           it.SKU,
			Label:         it.Label,
			Customization: it.Customization,
			Qty:           it.Qty,
			UnitPrice:     it.UnitPrice,
		})
	}
	return out
}
