package runtime

// event_lifecycle.go es la MUERTE EXPLÍCITA del evento conversacional (Plan 043 ·
// Ola 4, E-5): el cierre NATURAL cuando su flujo termina (T4.1) y la cancelación
// por id desde la app del dueño (T4.2-core + T4.3). Son los ÚNICOS dos caminos que
// mueven `status` (D-043.5): ni el vencimiento, ni el escape, ni el event_stop
// tocan la fila — esos solo sueltan el puntero, y quien lo diga distinto está
// reintroduciendo el `expired` que E-6 derogó.

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ErrNoEventPlane lo devuelven GetEventForTenant/CancelEventForTenant cuando el
// runtime se construyó sin WithEventStore. NO se disfraza de «no encontrado»: un
// despliegue sin plano de eventos que expone el endpoint de cancelación es un
// error de cableado, y contestarle 404 al dueño escondería exactamente eso.
var ErrNoEventPlane = errors.New("runtime: sin plano de eventos cableado (WithEventStore)")

// closeIfFinished aplica el CIERRE NATURAL (T4.1): si el estado quedó en el
// centinela y la conversación tenía evento activo, transiciona open→closed (el
// guard y el sellado de closed_at los pone el store) y apaga el puntero EN EL
// STRUCT — persistirlo es del llamante, que ya tiene un Save por delante; así el
// cierre no añade una segunda escritura de flow_state.
//
// Tres reglas que no son de estilo:
//
//   - ErrNotOpen es carrera benigna (mismo trato que retireForNew): otro escritor
//     selló la muerte primero —una cancelación desde la app entre el Step y este
//     UPDATE— y el puntero se apaga igual, porque apunta a un muerto.
//   - Un fallo REAL de la transición NO limpia el puntero (un solo hecho, E-8 §4)
//     y NO aborta el turno: se LOGUEA y el estado se persiste con el puntero
//     intacto. El reintento es natural: el estado queda terminal con evento
//     activo, y el siguiente entrante vuelve a pasar por aquí. Abortar perdería
//     el Save del avance —la respuesta al cliente y los efectos del módulo— por
//     no poder sellar una fila que se puede sellar después.
//   - El intake NO se toca: el cierre natural del carrito ya cierra su solicitud
//     por la proyección de cart_closed. Abandonarla aquí pisaría ese hecho;
//     abandonar es SOLO de la cancelación (T4.3).
func (rt *Runtime) closeIfFinished(ctx context.Context, st *model.Conversation) {
	if rt.events == nil || st.EventID == "" || !st.Finished() {
		return
	}
	// Lectura PREVIA (Plan 043 · T5.4, D2 · sitio 5): tras la transición el evento
	// sale de los vivos y ya no se puede releer por aliveByID. Es la ÚNICA lectura
	// que añade esta tarea al camino caliente y solo ocurre en el turno que TERMINA
	// un flujo con evento activo.
	ev, conocido := rt.activeEvent(ctx, store.Key{
		TenantID: st.TenantID, SessionID: st.SessionID, ContactID: st.ContactID,
	}, st.SessionID, st.EventID)

	nuestra := true
	if err := rt.events.TransitionEvent(ctx, st.EventID, events.StatusClosed); err != nil {
		if !errors.Is(err, events.ErrNotOpen) {
			rt.log.Warn("runtime: no se pudo cerrar el evento al terminar su flujo; el puntero se conserva para reintentar",
				"error", err, "session_id", st.SessionID)
			return
		}
		rt.log.Info("runtime: el evento ya no estaba open al terminar su flujo (carrera benigna)",
			"session_id", st.SessionID)
		nuestra = false
	}
	if nuestra && conocido {
		rt.emitEventEffect(ctx, ev, EffectEventClosed)
	}
	st.EventID = ""
}

// GetEventForTenant lee un evento por id acotado al tenant del token (T4.2).
// Delega TODO en el store: el aislamiento vive en el SQL y aquí no se re-decide.
func (rt *Runtime) GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	if rt.events == nil {
		return events.Event{}, ErrNoEventPlane
	}
	return rt.events.GetEventForTenant(ctx, tenantID, eventID)
}

// CancelEventForTenant es la cancelación EXPLÍCITA desde la app del dueño
// (T4.2-core + T4.3): guard open→cancelled, solicitud a `abandoned`, puntero de la
// conversación apagado. Devuelve la fila ya terminal — la que el endpoint enseña.
//
// Idempotente sobre terminales por DOS capas que se refuerzan: el fetch temprano
// devuelve la fila TAL CUAL sin tocar nada (la segunda llamada no cambia ni
// closed_at ni vuelve a abandonar), y si aun así dos cancelaciones corren a la
// vez, el compare-and-swap del store deja que una gane y la otra re-lea (el mismo
// patrón de carrera benigna de retireForNew).
//
// El keyedMutex de la conversación se toma ANTES de escribir: la limpieza del
// flow_state comparte fila con HandleIncoming (Load→Save), y sin el single-flight
// un avance concurrente podría re-escribir el puntero recién apagado. Se toma
// DESPUÉS del fetch —la clave sale de la propia fila— y el hueco que eso deja lo
// cubre el guard: si el estado del evento cambió entre medias, la transición
// pierde limpia con ErrNotOpen.
//
// Orden del hecho (E-8 §4, precedente retireForNew): transición → abandono →
// puntero. Si el abandono falla, el error SE PROPAGA con el evento ya cancelado
// (costura conocida: reintentar la cancelación NO reintenta el abandono, porque
// la segunda llamada entra por la rama idempotente). Si falla la limpieza del
// puntero, también se propaga: el puntero colgando es autocorregible —eventClock
// trata un evento no-vivo como benigno— pero el llamante debe saber que el
// criterio «flow_state.event_id queda NULL» no se cumplió en ESTA llamada.
func (rt *Runtime) CancelEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	if rt.events == nil {
		return events.Event{}, ErrNoEventPlane
	}
	ev, err := rt.events.GetEventForTenant(ctx, tenantID, eventID)
	if err != nil {
		return events.Event{}, err
	}
	if !ev.Alive() {
		// Ya terminal: la FILA DEL EVENTO no se toca —closed_at es el de la primera
		// muerte, no el de este reintento—, pero sus dos efectos colaterales sí se
		// COMPLETAN si quedaron a medias. Ver repairCancelled.
		return rt.repairCancelled(ctx, tenantID, ev)
	}
	key := store.Key{TenantID: ev.TenantID, SessionID: ev.SessionID, ContactID: ev.ContactID}
	unlock := rt.locks.lock(key)
	defer unlock()
	if err := rt.cancelAndAbandon(ctx, tenantID, ev); err != nil {
		if errors.Is(err, events.ErrNotOpen) {
			// Carrera benigna: otro escritor selló la muerte entre el fetch y el
			// UPDATE. Re-leer y devolver lo que quedó es exactamente la rama
			// idempotente, solo que ganada por otro.
			return rt.events.GetEventForTenant(ctx, tenantID, eventID)
		}
		return events.Event{}, fmt.Errorf("runtime: cancelar el evento: %w", err)
	}
	if err := rt.releaseStateFrom(ctx, key, eventID); err != nil {
		return events.Event{}, err
	}
	// Re-fetch y no un mutate local: la fila que se devuelve lleva el closed_at
	// REAL que selló el store, no uno reconstruido aquí que podría discrepar.
	return rt.events.GetEventForTenant(ctx, tenantID, eventID)
}

// repairCancelled es la rama IDEMPOTENTE del cancel sobre un evento ya terminal, y
// además la REPARACIÓN de su costura de fallo parcial (Plan 043 · Ola 4).
//
// El orden del hecho es transición → abandono → puntero, así que un fallo a mitad
// deja el evento `cancelled` con la solicitud todavía `open` y/o el puntero puesto.
// Antes esta rama devolvía la fila tal cual y ahí se acababa: el reintento del dueño
// recorría el camino idempotente sin reparar nada, y la solicitud huérfana TAMPOCO
// era descartable a mano —`POST /api/v1/intakes/discard` la salta con `live_event`
// porque su guarda mira el `cart` del flow_state y no el estado del evento
// (`intakes/postgres.go:hasLiveCartTx`, aproximación cuya sustitución el Plan 041
// dejó anotada a nombre de T4.3)—. Con las dos puertas cerradas, la única salida era
// un reconciliador. Reintentar el cancel es esa salida, y no hace falta nada más.
//
// SOLO para `cancelled`. Un evento `closed` (cierre natural) JAMÁS abandona su
// solicitud: la cerró la proyección de cart_closed y abandonarla aquí pisaría ese
// hecho —es la misma regla que closeIfFinished respeta arriba—.
//
// Las dos reparaciones son seguras de repetir: AbandonByEvent es idempotente por
// contrato (T4.5.5a, D-043.21 — cero filas tocadas es éxito, así que «¿hay algo que
// reparar?» se DELEGA en la propia llamada en vez de leerse de una columna del
// evento, que ya no existe) y releaseStateFrom no toca un puntero que ya mira a
// otro sitio. Sobre una cancelación que salió bien, este camino no escribe nada.
func (rt *Runtime) repairCancelled(ctx context.Context, tenantID string, ev events.Event) (events.Event, error) {
	if ev.Status != events.StatusCancelled {
		return ev, nil
	}
	if rt.intakes != nil {
		if err := rt.intakes.AbandonByEvent(ctx, tenantID, ev.ID); err != nil {
			return events.Event{}, fmt.Errorf("runtime: completar el abandono de la solicitud del evento cancelado: %w", err)
		}
	}
	key := store.Key{TenantID: ev.TenantID, SessionID: ev.SessionID, ContactID: ev.ContactID}
	unlock := rt.locks.lock(key)
	defer unlock()
	if err := rt.releaseStateFrom(ctx, key, ev.ID); err != nil {
		return events.Event{}, err
	}
	return ev, nil
}

// releaseStateFrom apaga flow_state.event_id SI la conversación del evento seguía
// apuntándolo (mismo gesto que stopEvent: st.EventID="" + Save). La clave sale de
// la propia fila del evento (tenant, sesión, contacto) — el evento SABE de qué
// conversación es, y por eso no hace falta SQL nuevo para encontrarla.
//
// Que apunte a OTRO evento (o a ninguno) es normal y no toca nada: la conversación
// siguió su vida —saltó de evento, venció, se escapó— y su puntero ya no es asunto
// de esta cancelación.
func (rt *Runtime) releaseStateFrom(ctx context.Context, key store.Key, eventID string) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: leer el estado al cancelar su evento: %w", err)
	}
	if !ok || st.EventID != eventID {
		return nil
	}
	st.EventID = ""
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: apagar el puntero del evento cancelado: %w", err)
	}
	return nil
}
