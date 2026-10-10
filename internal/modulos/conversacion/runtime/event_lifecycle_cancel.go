// Porta internal/flujos/runtime/event_lifecycle.go @ e0159171

package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de event_lifecycle.go (E-13): la cancelación EXPLÍCITA por id desde la app del dueño
// (RT-13) —CancelEventForTenant—, su rama idempotente y de reparación (repairCancelled) y el
// apagado del puntero de la conversación (releaseStateFrom). Solo se movieron declaraciones.

// CancelEventForTenant es la cancelación EXPLÍCITA desde la app del dueño (RT-13; Plan 043 ·
// T4.2/T4.3). Devuelve la fila YA terminal, releída del almacén: su closed_at es el que selló
// el almacén, no uno reconstruido.
//
// Errores previos a tocar nada: sin plano de eventos, ErrNoEventPlane; evento inexistente o
// de otro tenant, el error del almacén tal cual (events.ErrEventNotFound).
//
// # Evento VIVO
//
// Orden del hecho (E-8 §4): candado → transición → abandono → puntero.
//
//  1. Toma el candado de la conversación del evento (la clave sale de la propia fila): la
//     limpieza del puntero comparte fila con el entrante.
//  2. Transiciona open → cancelled con el guard del almacén.
//  3. Emite event_cancelled a los sinks (RT-14), con kind = "event" y el evento en su
//     contexto. Va antes del abandono: la muerte ya está sellada.
//  4. Abandona la solicitud que declare ese evento (IntakeAbandoner.AbandonByEvent). Sin
//     abandonador cableado: WARN y se sigue.
//  5. Apaga el puntero ACTIVO del estado SOLO si seguía apuntando a ESTE evento; si apunta a
//     otro, o no hay estado, no escribe nada. El resto del estado se conserva.
//  6. Relee y devuelve el evento.
//
// Errores de ese camino:
//
//   - La transición pierde la carrera (events.ErrNotOpen): benigna. No abandona ni toca el
//     puntero; relee y devuelve lo que quedó, sin error.
//   - Otro fallo de la transición: «runtime: cancelar el evento: …», y nada más se toca.
//   - El abandono falla: el error SUBE (con ese mismo prefijo) con el evento ya cancelado
//     —costura conocida— y el puntero sin apagar. Reintentar la cancelación lo repara (rama
//     terminal, abajo).
//   - Falla la limpieza del puntero: el error sube; el evento ya está cancelado y su
//     solicitud abandonada.
//
// # Evento ya TERMINAL (idempotencia y reparación)
//
// La fila del evento NO se toca: no hay transición, closed_at es el de la primera muerte y no
// se emite ningún efecto.
//
//   - `closed`: se devuelve tal cual, sin abandonar nada ni tocar el estado. Un fin natural
//     jamás abandona su solicitud.
//   - `cancelled`: se COMPLETAN sus dos consecuencias por si quedaron a medias —el abandono
//     (idempotente: cero filas es éxito) y, bajo el candado, el apagado del puntero si aún
//     apuntaba a él— y se devuelve la fila. Sobre una cancelación que salió bien no escribe
//     nada. Si el abandono vuelve a fallar: «runtime: completar el abandono de la solicitud
//     del evento cancelado: …».
//
// # Lo que el cliente ve después (RT-14, T62)
//
// Con el evento cancelado, el tipo queda libre: el siguiente entrante del contacto que dispare
// ese tipo abre un evento NUEVO en el acto, sin esperar a ningún reloj. El menú de salida o el
// menú del despachador que hubiera pendientes sobre el evento cancelado caducan solos, por su
// sello.
func (rt *Runtime) CancelEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	// Por qué es idempotente y por qué el candado va donde va:
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
// SOLO para `cancelled`. Un evento `closed` (fin natural) JAMÁS abandona su
// solicitud: la cerró la proyección de cart_closed y abandonarla aquí pisaría ese
// hecho —es la misma regla que closeIfFinished respeta arriba—.
//
// 🔴 EL #29 LE AÑADE POBLACIÓN, Y SE ACEPTA A PROPÓSITO (mirado con lupa, no
// heredado): desde el hallazgo #29 hay una SEGUNDA forma de llegar a `cancelled` —la
// clienta cancelando dentro de su pedido, vía closeIfFinished—, así que un cancel por
// id sobre uno de esos eventos ya NO cae en el return temprano de arriba y entra aquí.
// Se deja entrar, por tres hechos comprobados:
//
//   - En el estado asentado no escribe NADA. AbandonByEvent lleva `AND status = 'open'`
//     en su WHERE (intakes/postgres.go) y la solicitud de ese pedido ya está
//     `cancelled` por la proyección: cero filas, que por contrato es éxito. Y
//     releaseStateFrom no toca un puntero que ya apagó closeIfFinished en el mismo Save.
//   - Cuando SÍ escribe, escribe lo que hay que escribir. El fan-out que proyecta
//     cart_cancelled es BEST-EFFORT (dispatch loguea y sigue): si su sink falla, la
//     solicitud se queda `open` colgando de un evento ya `cancelled` — el huérfano
//     exacto para el que se construyó esta reparación, solo que llegando por otra
//     puerta. Antes del #29 ese huérfano no tenía NINGUNA salida (el evento quedaba
//     `closed` y el return temprano lo dejaba pasar de largo); ahora reintentar el
//     cancel lo repara, igual que repara el de E-8 §4.
//   - La contrapartida es una ventana estrecha y se nombra en vez de esconderse: el
//     dispatch corre DESPUÉS del Save, así que durante esos milisegundos la solicitud
//     sigue `open`. Un cancel desde la app que caiga justo ahí la dejaría en
//     `abandoned` en vez de `cancelled`. Las dos son terminales, ninguna borra nada
//     (INV-09) y el pedido sigue en la bandeja; distinguirlas exigiría que el evento
//     recordara POR QUÉ murió, que es dato nuevo en la fila y materia del Plan 053.
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
