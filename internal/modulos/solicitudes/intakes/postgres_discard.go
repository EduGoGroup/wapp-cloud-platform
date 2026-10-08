// Porta internal/intakes/postgres.go @ 64c181a

// postgres_discard.go son los dos caminos por los que una solicitud acaba en
// StatusAbandoned sin pasar por UpdateStatus: el descarte manual del dueño, que deja
// revisión, y el abandono que dispara la muerte del evento padre, que no la deja
// (REQ-32d distingue así los dos `abandoned` del embudo).

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
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
	if _, err := uuid.Parse(intakeID); err != nil {
		return DiscardOutcome{}, ErrNotFound
	}

	// Fuera de la clausura: WithTx puede REEJECUTARLA ante un deadlock y vale el
	// resultado del intento que confirmó (mismo criterio que UpdateStatus).
	var out DiscardOutcome
	err := postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		var (
			stored  string
			eventID sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
			SELECT status, event_id::text
			FROM public.intakes
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, intakeID).Scan(&stored, &eventID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound // no es del tenant o no existe: lo mismo (INV-8)
		case err != nil:
			return fmt.Errorf("intakes: bloquear la solicitud para descartarla: %w", err)
		}

		out = DiscardOutcome{Status: NormalizeStatus(stored)}
		if !slices.Contains(discardable, stored) {
			return nil
		}

		live, err := hasLiveEventTx(ctx, tx, eventID.String)
		if err != nil {
			return err
		}
		if live {
			out.LiveEvent = true
			return nil
		}

		head, err := scanIntake(tx.QueryRowContext(ctx, `
			UPDATE public.intakes
			SET status = $3, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND status = ANY($4)
			RETURNING `+intakeCols,
			tenantID, intakeID, StatusAbandoned, discardable))
		if err != nil {
			return fmt.Errorf("intakes: descartar la solicitud: %w", err)
		}

		// memoryDiscardedRevision es el discardedRevision del viejo: la MISMA foto que
		// escribe el MemoryStore, para que los dos almacenes no puedan divergir.
		rev, err := memoryDiscardedRevision(intakeID, stored, head.Total)
		if err != nil {
			return err
		}
		if _, err := p.insertRevisionOnce(ctx, tx, rev); err != nil {
			return err
		}
		// El descarte CIERRA SU CONTENEDOR (REQ-32e; D-043.15(1) re-expresada por
		// D-043.21): el evento a cancelar es el que ESTA solicitud declara.
		if err := cancelContainerTx(ctx, tx, eventID.String); err != nil {
			return err
		}
		out.Discarded = true
		return nil
	})
	if err != nil {
		return DiscardOutcome{}, err
	}
	return out, nil
}

// hasLiveEventTx pregunta lo que la guarda `live_event` siempre quiso preguntar
// (DT-043.2 SALDADA, Ola 4.5 · T4.5.5(c)): «¿está `open` el evento que ESTA
// solicitud declara?» — el criterio REAL, sobre `intakes.event_id` (D-043.21), en
// vez de la aproximación por el `cart` del `flow_state` que vivió aquí desde el
// Plan 041 (hasLiveCartTx, escrita cuando la tabla de eventos no existía).
//
// Lo que la sustitución arregla, medido: (a) el pedido huérfano de un evento ya
// `cancelled` (el callejón del journal 2026-08-10) YA NO rebota con `live_event` —
// su evento no está `open` y el descarte procede; (b) un carrito NUEVO del mismo
// contacto ya no frena el descarte de una solicitud vieja suya: solo importa el
// evento de ESTA solicitud, no la conversación de la sesión.
//
// eventID == "" es una solicitud LEGADA (pre-0054, sin padre declarado): no hay
// evento vivo que mirar ⇒ descartable. No es una concesión: sin ligadura no existe
// la conversación que la guarda protege.
func hasLiveEventTx(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var live bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM public.conversation_events e
			WHERE e.id = $1 AND e.status = 'open'
		)
	`, eventID).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("intakes: comprobar el evento vivo de la solicitud: %w", err)
	}
	return live, nil
}

// cancelContainerTx cierra el CONTENEDOR de una solicitud descartada: transición
// open→cancelled + closed_at sobre public.conversation_events (REQ-32e del Plan
// 041; D-043.15(1) re-expresada por D-043.21 — el evento es `WHERE id = event_id
// de la propia solicitud`, ya no `WHERE intake_id`, columna que murió en la 0054).
//
// VIVE AQUÍ, en el dominio de solicitudes y con SQL directo, a conciencia: es la
// dirección contenido→contenedor del par de E-8 («evento y pedido son la misma
// cosa»), y su disparador es el descarte del dueño — una puerta de ESTE dominio.
// La dirección inversa (contenedor→contenido: cancelar el evento abandona su
// solicitud) es del runtime vía AbandonByEvent. El SQL calca la semántica de
// transitionSQL (flujos/events/store.go): compare-and-swap con `AND
// status='open'`, sin transición de vuelta y sin pisar una muerte ya sellada.
//
// 0 filas NO es error (REQ-32e: «si no hay evento ligado, o ya está terminal, el
// descarte sigue siendo éxito»). De hecho, con la guarda de hasLiveEventTx en la
// MISMA transacción, un evento aún `open` frena el descarte antes de llegar aquí
// (`live_event`); este UPDATE es la garantía EN EL SQL de que un descarte
// consumado jamás deja detrás un contenedor rescatable (INV-17), estén como estén
// la guarda o sus llamantes el día de mañana.
func cancelContainerTx(ctx context.Context, tx *sql.Tx, eventID string) error {
	if eventID == "" {
		return nil // solicitud legada sin padre declarado: no hay contenedor que cerrar
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.conversation_events
		SET status = 'cancelled', closed_at = now()
		WHERE id = $1 AND status = 'open'
	`, eventID); err != nil {
		return fmt.Errorf("intakes: cerrar el contenedor de la solicitud descartada: %w", err)
	}
	return nil
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
	if !isUUID(eventID) {
		return nil
	}
	if _, err := p.db.ExecContext(ctx, `
		UPDATE public.intakes
		SET status = $3, updated_at = now()
		WHERE tenant_id = $1 AND event_id = $2 AND status = $4
	`, tenantID, eventID, StatusAbandoned, StatusOpen); err != nil {
		return fmt.Errorf("intakes: abandonar la solicitud del evento %s: %w", eventID, err)
	}
	return nil
}
