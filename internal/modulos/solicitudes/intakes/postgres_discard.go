// Porta internal/intakes/postgres.go @ 64c181a

// postgres_discard.go son los dos caminos por los que una solicitud acaba en
// StatusAbandoned sin pasar por UpdateStatus: el descarte manual del dueño, que deja
// revisión, y el abandono que dispara la muerte del evento padre, que no la deja
// (REQ-32d distingue así los dos `abandoned` del embudo).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Discard implementa Store.Discard: el descarte manual del dueño (T4.8, D-041.18).
// Un intakeID que no es un UUID ⇒ ErrNotFound sin tocar la base. Todo ocurre en UNA
// transacción (postgres.WithTx) y el orden importa:
//
//  1. BLOQUEAR la cabecera y leer su estado y su event_id (FOR UPDATE acotado por
//     tenant): el mismo punto de serialización que UpdateStatus, EnsureShippingLine
//     y ReplaceItems. Sin fila ⇒ ErrNotFound, sin envolver (no existe o no es del
//     tenant: lo mismo, INV-8).
//  2. Si el estado ALMACENADO no está en `discardable` (comparación exacta) ⇒ se sale
//     SIN ESCRIBIR y sin mirar el evento: DiscardOutcome{Status: <estado normalizado>},
//     sin error. El estado explica el rechazo mejor que el evento.
//  3. Si la solicitud declara evento padre y ese evento sigue `open` ⇒ se sale SIN
//     ESCRIBIR: DiscardOutcome{Status, LiveEvent: true}, sin error (DT-043.2). Una
//     solicitud legada sin event_id no tiene evento vivo que mirar: no se consulta.
//  4. Si no: UPDATE a StatusAbandoned (con su `status = ANY(discardable)`, redundante
//     bajo el candado y que va igual: garantiza EN EL PROPIO SQL que este camino no
//     mueve una solicitud desde un estado no descartable), revisión
//     RevisionKindDiscarded con el estado de origen y el total, y CIERRE DEL
//     CONTENEDOR: el evento padre pasa a `cancelled` si seguía `open` (REQ-32e); sin
//     event_id no hay contenedor que cerrar. Devuelve
//     DiscardOutcome{Status: <estado de ORIGEN normalizado>, Discarded: true}.
//
// Los dos rechazos confirman una transacción que no escribió nada; son respuestas
// de negocio, no errores. Descartar dos veces es idempotente: la segunda sale por
// el paso 2.
//
// Errores de la base, envueltos con %w, devolviendo DiscardOutcome{} y con la
// transacción revertida:
//
//   - "intakes: bloquear la solicitud para descartarla: " — falla el bloqueo;
//   - "intakes: comprobar el evento vivo de la solicitud: " — falla la consulta del
//     evento;
//   - "intakes: descartar la solicitud: " — falla el UPDATE, o NO casa ninguna fila
//     (envuelve sql.ErrNoRows): sería la rotura de un invariante, y por eso sale
//     como error y no como un rechazo silencioso;
//   - los de la escritura de la revisión (postgres_revisions.go);
//   - "intakes: cerrar el contenedor de la solicitud descartada: " — falla el cierre
//     del evento.
//
// ⚠️ LEGADO REGISTRADO (Ola 4.5): una solicitud pre-0054 (event_id NULL) pasa la
// guarda, pero su UPDATE revienta contra el CHECK NOT VALID de la 0054. Ese
// comportamiento se conserva tal cual; no se maquilla aquí.
func (p *Postgres) Discard(ctx context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error) {
	panic(pendiente.Implementar("intakes.Postgres.Discard"))
}

// AbandonByEvent implementa Store.AbandonByEvent: deja en StatusAbandoned la
// solicitud StatusOpen del tenant que declara `eventID` como padre (D-043.21). Es la
// dirección contenedor→contenido: la consume el runtime cuando el evento muere
// cancelado.
//
// Es un CAS en UNA sentencia suelta, sin transacción: el `status = open` es el guard
// (una solicitud confirmada no se abandona por aquí) y refresca updated_at. NO
// escribe revisión.
//
//   - 0 filas es ÉXITO IDEMPOTENTE (nil): el reintento de una cancelación a medias
//     tiene que poder terminar aunque el abandono ya esté hecho o no haya solicitud
//     que abandonar;
//   - un eventID que no es un UUID no puede estar en la columna: nil SIN tocar la
//     base (mismo destino que 0 filas);
//   - fallo de la base ⇒ "intakes: abandonar la solicitud del evento <eventID>: "
//     envolviendo la causa.
func (p *Postgres) AbandonByEvent(ctx context.Context, tenantID, eventID string) error {
	panic(pendiente.Implementar("intakes.Postgres.AbandonByEvent"))
}
