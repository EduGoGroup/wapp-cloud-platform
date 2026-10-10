// Porta internal/flujos/modules/cart/projection.go @ 9d5a4b6 (trozo «cierre»:
// closeIntake y revisionLines; el viejo es un solo fichero y aquí nace partido, 05 E-13)

// projection_close.go es la proyección de cart_closed: el cierre atómico de la
// solicitud con sus líneas, la revisión que deja constancia de lo que el carrito armó,
// la línea de envío y las tres anotaciones que el efecto se lleva de vuelta para el
// sink del CRM (intake_id, revision_no, lifecycle_status).
//
// No exporta nada: lo que promete se ve por Projector.Project (projection.go).

package cart

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// closeIntake proyecta cart_closed: cierra ATÓMICAMENTE la solicitud abierta (o crea una
// "closed" coherente) con el total del payload e inserta TODAS las líneas (fuente de
// verdad). Delega en store.CloseIntake (una transacción, Plan 027 · Ola 1 · T4) y
// después le cuelga la REVISIÓN 1 (ADR-0031 §3): la foto de lo que el carrito armó.
//
// La revisión se escribe FUERA de la transacción del cierre, y eso es una decisión
// consciente con un costo asumido: si su INSERT falla, queda una solicitud cerrada
// y coherente —líneas incluidas— sin su revisión 1, y el error sube al dispatcher,
// que lo LOGUEA sin abortar el avance de la conversación (ver PersistSink.Handle).
// Se acepta porque la verdad de lo vendido es intake_items, no la revisión: perder
// el rastro de la negociación degrada la auditoría, mientras que meter el escritor
// de revisiones dentro de la transacción del motor obligaría al carrito a compartir
// la conexión con otro módulo. Lo que NO ocurre es que se pierdan las dos: el
// cierre ya está confirmado cuando esto se intenta.
func (p *Projector) closeIntake(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	items := cartItems(eff.Payload)
	total := modules.AsFloat(eff.Payload["total"])

	intakeID, err := p.store.CloseIntake(ctx, store.IntakeClose{
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Total:     total,
		// El cierre también declara al padre (D-043.21): en el camino normal solo
		// rellena un NULL legado (el open ya lo estampó ensureOpenIntake), y en la
		// rama que crea la "closed" coherente es el event_id de esa fila nueva.
		EventID: meta.EventID,
		// La indicación del pedido (D-041.19) viaja en la cabecera del efecto y su
		// AUSENCIA da la cadena vacía, igual que la personalización de cada línea: un
		// cierre emitido antes de que el campo existiera —o por un carrito sin
		// indicaciones, que es la mayoría— cierra exactamente igual.
		CustomerNote: modules.AsString(eff.Payload["customer_note"]),
		Items:        items,
	})
	if err != nil {
		return err
	}

	payload, err := intakes.CartRevisionPayload(total, revisionLines(items))
	if err != nil {
		return err
	}
	// 🔴 EL NÚMERO QUE DEVUELVE LA ESCRITURA YA NO SE DESCARTA (T4.10 · D-044.19).
	// El INSERT numera la revisión DENTRO de su propia sentencia
	// (`COALESCE(MAX(revision_no), 0) + 1`, intakes/postgres.go), así que el
	// correlativo que le tocó a ESTE cierre solo se sabe aquí: releerlo después
	// sería una carrera con cualquier otro escritor de la misma solicitud, y una
	// consulta extra en línea con el mensaje (INV-02).
	//
	// Y ya NO es siempre 1: desde T4.0 (D-044.46) el pipeline del 044 cuelga su
	// revisión `interpreted` de la MISMA fila que el carrito dejó en `open` —no crea
	// otra, y no le toca el estado—, así que el cierre normal de ese carrito escribe
	// la 2. Mientras esto se descartaba, el WebhookSink emitía un literal `1` y el
	// puente CRM —que hace UPSERT por (intake_id, revision_no), manual del
	// integrador §4— habría descartado el CIERRE como duplicado de la
	// interpretación: el pedido de verdad no llega al CRM y no hay un solo error en
	// ningún log.
	rev, err := p.revisions.InsertRevision(ctx, intakes.Revision{
		IntakeID:  intakeID,
		Kind:      intakes.RevisionKindCart,
		Payload:   payload,
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		return fmt.Errorf("cart: revisión del cierre de la solicitud %s: %w", intakeID, err)
	}

	// La línea de envío va DESPUÉS de la revisión, y no antes, para que la revisión
	// 1 siga siendo la foto FIEL de lo que armó el cliente: el envío lo pone la
	// plataforma encima, no el carrito. Con ShippingOnlyIfZones, un tenant que no
	// cobra envío cierra exactamente igual que antes de esta tarea — este cierre no
	// pasa por ningún ciclo de aprobación (va directo a `confirmed`), así que una
	// línea «por confirmar» que nadie va a precificar solo sería ruido (D-041.11).
	if err := p.shipping.EnsureShippingLine(ctx, meta.TenantID, intakeID, intakes.ShippingOnlyIfZones); err != nil {
		return fmt.Errorf("cart: línea de envío de la solicitud %s: %w", intakeID, err)
	}

	// Anota el ID recién generado de vuelta en el Payload (mapa compartido, no una
	// copia): el fan-out de PersistSink.Handle ya escribió flow_events con el
	// payload ORIGINAL antes de llegar aquí, así que esto no lo toca. Lo que sí ve
	// esta escritura es el WebhookSink (Plan 042), que corre DESPUÉS en el mismo
	// dispatch() sobre el mismo eff — es la única forma de correlacionar sin que el
	// sink del CRM tenga que volver a consultar la BD.
	eff.Payload["intake_id"] = intakeID
	// El revision_no viaja por la MISMA puerta y por el mismo motivo (T4.10): el
	// contrato wapp-crm-v1 lo declara requerido y es, junto al intake_id, la clave
	// con la que el puente decide si un push trae un estado NUEVO o repite uno que
	// ya conoce. Sale del retorno del INSERT de arriba —el número REAL que la base
	// asignó—, no de contar revisiones ni de suponer que el cierre es la primera.
	eff.Payload["revision_no"] = rev.RevisionNo
	// Y el ESTADO viaja por la MISMA puerta que los otros dos (Plan 044 · Ola 4,
	// T4.10 mitad 2). El sink emitía un literal "confirmed" que acertaba SOLO
	// porque este es el cierre del carrito; en cuanto una segunda puerta empuja el
	// mismo contrato —el re-empuje de una corrección, que devuelve la solicitud a
	// `pending_approval`— ese literal miente igual que mentía el `RevisionNo: 1`.
	//
	// Se anota la clave LEGADA con la que CloseIntake acaba de escribir la fila, sin
	// normalizar: el contrato la normaliza al armarse (intakes.NormalizeStatus, que
	// es el único punto del sistema donde se resuelve ese alias) porque JAMÁS emite
	// `closed`. Normalizar aquí repartiría la regla en dos sitios y dejaría a la
	// puerta HTTP con la posibilidad de saltársela.
	eff.Payload["lifecycle_status"] = intakeStatusClosed
	return nil
}

// revisionLines congela las líneas del cierre en la forma del payload de la
// revisión. No lleva added_at: la revisión ya está fechada entera y el instante de
// cada línea es del carrito, no de la foto.
func revisionLines(items []store.IntakeItem) []intakes.RevisionLine {
	out := make([]intakes.RevisionLine, 0, len(items))
	for _, it := range items {
		out = append(out, intakes.RevisionLine{
			SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	return out
}
