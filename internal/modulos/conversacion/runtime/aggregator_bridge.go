// Porta internal/flujos/runtime/aggregator.go @ e0159171

package runtime

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Trozo de aggregator.go (E-13): el puente entre el motor y el agregador (AG-7). Lo que
// promete está en la cabecera de aggregator.go; sus tres llamantes se prueban a través
// del Runtime en incoming_aggregation_test.go.

// observeForAggregation es el ÚNICO puente entre el motor y el agregador.
//
// # LOS TRES PUNTOS DE LLAMADA, Y POR QUÉ SON TRES Y NO UNO
//
// Nació con uno solo —el turno normal— y eso dejaba fuera, en silencio, JUSTO el
// mensaje que el plan viene a resolver. Hoy son tres, y son EXCLUYENTES entre sí: por
// cada entrante corre como mucho uno.
//
//  1. El TURNO NORMAL sobre una conversación viva. Va justo al lado de la escritura del
//     hilo del turno y por el mismo motivo: ahí el turno YA está persistido, se conoce
//     el evento que estaba vivo MIENTRAS se produjo el turno (T4.5.1) y el hilo literal
//     que T1.4 leerá acaba de escribirse.
//  2. EL MENSAJE QUE ARRANCA EL EVENTO. Es el «quiero presupuesto de X» que abre la
//     ráfaga, y no llegaba a `source_refs`: la ventana la abría el mensaje SIGUIENTE,
//     con lo que el `message_ts` —la base de fechas, D-044.9— quedaba anclado al
//     segundo. Ahí el `event_id` acaba de nacer (o de recuperarse del vivo).
//  3. El REINICIO POR REANUDACIÓN, y SOLO en su rama de reinicio consumado — el mensaje
//     con el que el cliente reabre un pedido caducado. Ese turno se consume ahí y nunca
//     llega al (1).
//
// 🔴 LOS CAMINOS QUE DELIBERADAMENTE NO OBSERVAN llevan su porqué escrito EN EL SITIO,
// no aquí: el corte por fallo del sink durable, el cupo anti-loop agotado, la elección
// numérica en el despachador y el salto por tipo sobre conversación viva.
//
// 🔴 LA INVARIANTE QUE GOBIERNA A LOS PRIMEROS, Y QUE NO SE PUEDE ROMPER SIN ROMPER EL
// PLAN: TODO MENSAJE QUE ENTRA EN `source_refs` TIENE SU LITERAL EN EL HILO. Los cortes
// por sink durable deciden NO escribir el literal de ese turno, así que su referencia
// tampoco puede existir — quedaría colgando y el `source_text` saldría incompleto SIN
// dar error, que es el peor modo de fallo posible. Al revés vale igual: cualquier
// camino nuevo que observe tiene que escribir su literal por el mismo acto.
//
// Best-effort integral: nil-safe de arriba abajo y sin devolver error.
//
// Lo que viaja es el TEXTO, que es lo que el Cloud necesita para PEDIR la clasificación
// (T1.6-4; el intent adjunto murió con D-044.31). El derecho sigue siendo `llm_intake`
// y solo ese —lo comprueba Observe—: exigir además `llm_intent` haría que un tenant con
// el pipeline contratado dependiera de una segunda feature para algo que la ventana de
// silencio hace igual sin señal.
func (rt *Runtime) observeForAggregation(ctx context.Context, tenantID, sessionID, contactID, eventID string, m *cloudlinkv1.IncomingMessage) {
	if rt.aggregator == nil || m == nil {
		return
	}
	var ts time.Time
	if m.GetTsUnix() > 0 {
		ts = time.Unix(m.GetTsUnix(), 0).UTC()
	} else {
		// Sin ts del cliente se usa el reloj del runtime (inyectable, WithClock).
		// Es peor base de fechas que el ts real, pero se prefiere un instante escrito
		// y explicable a un NULL.
		ts = rt.now()
	}
	rt.aggregator.Observe(ctx, IncomingRef{
		Key: intake.WindowKey{
			TenantID:  tenantID,
			SessionID: sessionID,
			ContactID: contactID,
			EventID:   eventID,
		},
		WaMessageID: m.GetWaMessageId(),
		MessageTS:   ts,
		Text:        m.GetText(),
	})
}
