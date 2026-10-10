// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Trozo de events.go (E-13): la TERCERA puerta del nacimiento tardío (StartNewOfKind), el
// límite de E-11 —quién retira un evento vencido para dejar sitio a uno nuevo— y el NACIMIENTO
// del evento (EV-2, EV-3 caminos 3 y 4). Solo se movieron declaraciones.

// StartNewOfKind es la TERCERA puerta del nacimiento tardío (EV-2): la elección EXPLÍCITA de
// empezar uno nuevo del tipo kind. Es el único camino con el gesto «nuevo» y, por tanto, el
// único que puede aplicar E-11 (EV-3). flowID es el flujo que arranca ("" = sin flujo);
// activeEventID, el evento activo de la conversación ("" = ninguno).
//
// Devuelve si el turno se CONSUMIÓ:
//
//   - sin plano de eventos, o con kind vacío: (false, nil), sin tocar nada;
//   - hay un vivo de ese tipo DENTRO de su ventana: se conmuta hacia él y no se cierra nada
//     (E-11.3: nadie pierde un pedido en curso por tocar una opción) → (true, nil);
//   - hay un vivo de ese tipo y está VENCIDO: se cancela (event_cancelled), se abandona su
//     solicitud y nace uno nuevo (event_started) → (true, nil). Si la cancelación pierde la
//     carrera (events.ErrNotOpen) se sigue igual; si el abandono falla, el error sube con el
//     evento ya cancelado;
//   - no hay ninguno: nace (event_started) y arranca flowID → (true, nil);
//   - cupo del limitador agotado: (true, nil) sin crear ni enviar nada, y se cuenta
//     `rate_limit`.
//
// 🔴 Se llama con el candado de la conversación YA TOMADO: tomarlo aquí sería auto-deadlock.
//
// El mensaje que la dispara NO entra ni en el hilo del evento ni en la ventana de captación:
// es el número de una lista que pintó la plataforma, no lo que el cliente quiere pedir.
func (rt *Runtime) StartNewOfKind(ctx context.Context, key store.Key, sessionID, kind, flowID, activeEventID string) (bool, error) {
	// ⚠️ DESCARTA el `event_id` que beginEvent devuelve desde el Plan 044, y es DELIBERADO:
	// esta puerta es la ELECCIÓN EN EL DESPACHADOR («1) Hacer un pedido»), y ese número no
	// es el pedido —es la respuesta a una lista que le pintamos nosotros—. Meterlo en la
	// ventana de captación anclaría el `message_ts` del presupuesto (D-044.9) en un «1» y
	// dejaría ese «1» como primera referencia de `source_refs`. La ventana la abre el primer
	// mensaje en el que el cliente dice lo que quiere, que es el turno siguiente.
	dec := trigger.Decision{Action: trigger.StartEvent, EventKind: kind, FlowID: flowID}
	// openingTurn{}: el «1» que el contacto pulsó NO abre el hilo, por la MISMA razón
	// por la que no abre la ventana (ver el ⚠️ de arriba). Es la respuesta a una lista
	// que pintamos nosotros, no lo que el cliente quiere.
	_, consumed, err := rt.beginEvent(ctx, key, sessionID, dec, gestureNew, activeEventID, openingTurn{})
	return consumed, err
}

// reuseOrRetire decide, para un evento YA VIVO del tipo pedido, si se conmuta hacia
// él (true) o si el gesto del cliente lo retira para dejar sitio a uno nuevo (false).
// Es donde vive el límite de E-11 y por eso está aislada: la condición «vencido» es
// DERIVADA (E-6) y se LEE, nunca se persiste.
func (rt *Runtime) reuseOrRetire(ctx context.Context, tenantID string, ev events.Event, g eventGesture) (bool, error) {
	if g == gestureGoTo {
		return true, nil
	}
	ttl, err := rt.eventInactivityTTL(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if !rt.events.IsSuspended(ev, ttl) {
		// E-11.3: vivo y DENTRO de su ventana ⇒ se conmuta, no se cierra. Nadie pierde
		// un pedido en curso por tocar una opción del menú.
		return true, nil
	}
	return false, rt.retireForNew(ctx, tenantID, ev)
}

// retireForNew aplica E-11: el cliente eligió empezar de nuevo sobre un evento
// VENCIDO suyo, así que ese acto deliberado lo cierra. El evento pasa a `cancelled`
// y su solicitud a `abandoned` — un SOLO hecho (E-8 §4): si el evento no se pudo
// cerrar, la solicitud no se toca.
//
// El pedido NO se borra (INV-09): sigue en la bandeja y sigue exportable.
func (rt *Runtime) retireForNew(ctx context.Context, tenantID string, ev events.Event) error {
	if err := rt.cancelAndAbandon(ctx, tenantID, ev); err != nil {
		if errors.Is(err, events.ErrNotOpen) {
			// Otro escritor lo cerró entre el SELECT y el UPDATE: el tipo quedó libre,
			// que es justo lo que queríamos. Carrera benigna.
			rt.log.Info("runtime: el evento vencido ya no estaba open al retirarlo (carrera benigna)",
				"event_kind", ev.Kind)
			return nil
		}
		return fmt.Errorf("runtime: cerrar el evento vencido para empezar otro (E-11): %w", err)
	}
	return nil
}

// cancelAndAbandon es la muerte explícita por cancelación, COMPARTIDA por sus dos
// puertas (E-11 en retireForNew y el cancel por id de T4.2/T4.3): transición
// open→cancelled con el guard del store y, DESPUÉS, el abandono de la solicitud
// que colgara. El orden es el hecho único de E-8 §4: si el evento no se pudo
// cerrar, la solicitud no se toca; si el abandono falla, el error se PROPAGA con
// el evento ya cancelado (costura conocida — el llamante no puede deshacer la
// transición, y reintentar el cancel la recorre idempotente).
//
// El error de la transición sale TAL CUAL (events.ErrNotOpen incluido): qué
// significa perder la carrera lo decide cada llamante — para E-11 es «el tipo
// quedó libre», para el cancel por id es «ya estaba muerto: re-lee y devuélvelo».
//
// El pedido JAMÁS se borra (INV-09): `abandoned` lo deja en la bandeja, intacto y
// exportable.
func (rt *Runtime) cancelAndAbandon(ctx context.Context, tenantID string, ev events.Event) error {
	if err := rt.events.TransitionEvent(ctx, ev.ID, events.StatusCancelled); err != nil {
		return err
	}
	// La muerte está sellada en el UPDATE. El abandono de la solicitud es su
	// consecuencia y puede fallar propagando el error (costura conocida, E-8 §4): el
	// evento SIGUE cancelado, así que la telemetría va aquí y no después (Plan 043 ·
	// T5.4, D2 · sitio 6).
	rt.emitEventEffect(ctx, ev, EffectEventCancelled)
	if rt.intakes == nil {
		rt.log.Warn("runtime: evento cancelado sin IntakeAbandoner cableado; si tenía solicitud, quedó sin abandonar",
			"tenant_id", tenantID, "event_kind", ev.Kind)
		return nil
	}
	// Por el event_id del propio evento (D-043.21): el runtime ya no sabe —ni debe
	// saber— si este evento parió solicitud. «No había ninguna» es cero filas y
	// éxito idempotente; preguntarlo antes sería una segunda consulta para decidir
	// lo que el UPDATE decide solo.
	if err := rt.intakes.AbandonByEvent(ctx, tenantID, ev.ID); err != nil {
		return fmt.Errorf("runtime: abandonar la solicitud del evento cancelado: %w", err)
	}
	return nil
}

// birthEvent crea el evento y arranca su flujo. El orden es deliberado: la fila
// nace ANTES de hablarle al cliente, de modo que no exista una respuesta que
// mencione un evento que no está en la base.
//
// El texto de esa primera respuesta lo produce el flujo del tenant y NO lleva el
// history_id (E-3): el identificador legible existe para que un humano hable de
// «ese pedido» en la bandeja, no para que el cliente lo memorice.
//
// 🔴 La coletilla se resuelve ANTES del CreateEvent, y ese orden es un ARREGLO, no
// una casualidad: la coletilla habla de «lo que dejaste a medias ANTES de esto», y
// un evento recién nacido no tiene contenido que lo excluya, así que cumple el
// predicado de rescatable (sin fila en `event_content` o con contenido `alive`,
// D-043.22 — events/store.go, rescuableWhere). Resolviéndola después,
// `ListRescuable` devolvía el evento que se acababa de crear y el cliente leía «tu
// pedido sigue a medias» sobre el pedido que acababa de abrir. Mirando antes no hay
// nada que excluir, y el componente que LEE no necesita aprender la noción de «el
// actual» —que es justo lo que el Frente B evitó a propósito—. Lo fija
// TestTagline_ElEventoQueAcabaDeNacerNoSeAnunciaASiMismo.
//
// FlowVersion viaja REAL desde esta ola (T4.5.6, D-043.21): la fila congela el
// flujo Y SU VERSIÓN con los que nació, que es lo que su COMMENT prometía y toda
// fila incumplía naciendo con 0 (birthEvent omitía el campo). La versión no viene
// en la decisión (trigger.Decision solo trae el flow_id) sino del flujo VIGENTE
// que este mismo camino va a arrancar — ver flowVersionFor.
//
// Devuelve el ID del evento RECIÉN CREADO (Plan 044): es el instante exacto en que ese
// id empieza a existir —el `INSERT` de CreateEvent—, y la ventana de captación lo
// necesita para anclar el mensaje que abrió el evento. Vale "" en DOS casos: la carrera
// benigna del `ErrAliveExists`, que es un éxito sin fila propia (otro entrante parió el
// evento entre nuestro SELECT y nuestro INSERT y aquí no se llegó a tener su id en la
// mano), y el ARRANQUE CORTADO por el sink durable — ahí el id sí se tiene y aun así no
// sube, porque el turno no dejó literal en el hilo (ver el `if cutOff`, abajo).
//
// `opening` solo hace de correo: baja hasta startLocked, que es quien escribe el hilo
// del turno de apertura (Plan 044 · T1.4). Esta función no lo mira.
func (rt *Runtime) birthEvent(ctx context.Context, key store.Key, sessionID string, dec trigger.Decision, opening openingTurn) (string, error) {
	tagline := rt.taglineFor(ctx, key.TenantID, sessionID, key.ContactID, dec.IntentName)
	flowVersion, err := rt.flowVersionFor(ctx, key.TenantID, dec.FlowID)
	if err != nil {
		return "", err
	}
	ev, err := rt.events.CreateEvent(ctx, events.NewEvent{
		TenantID:    key.TenantID,
		SessionID:   sessionID,
		ContactID:   key.ContactID,
		Kind:        dec.EventKind,
		FlowID:      dec.FlowID,
		FlowVersion: flowVersion,
	})
	if err != nil {
		if errors.Is(err, events.ErrAliveExists) {
			// Carrera con otro entrante de la misma conversación: alguien lo parió entre
			// nuestro SELECT y nuestro INSERT. No es un fallo — es exactamente la regla
			// que el índice único parcial existe para imponer.
			rt.log.Info("runtime: el evento ya existía al crearlo (carrera benigna)",
				"session_id", sessionID, "event_kind", dec.EventKind)
			return "", nil
		}
		return "", fmt.Errorf("runtime: crear evento de tipo %q: %w", dec.EventKind, err)
	}
	rt.emitEventEffect(ctx, ev, EffectEventStarted)
	cutOff, eerr := rt.enterEventFlow(ctx, key, sessionID, ev.ID, dec.EventKind, dec.FlowID, dec.Params, dec.IntentName, tagline, opening)
	if cutOff {
		// 🔴 EL ARRANQUE SE CORTÓ POR EL SINK DURABLE (D-054.4), Y ENTONCES EL ID **NO
		// SUBE** (Plan 044, corrección del 2026-08-22). El evento EXISTE —la fila se creó
		// arriba y sigue `open`—, pero su turno no ocurrió: startLocked volvió sin guardar
		// estado y sin escribir una sola fila de hilo, y al cliente se le dijo que
		// reintentara.
		//
		// Devolver "" es lo que hace cumplir la invariante del plan sin tocar el llamante:
		// TODO MENSAJE QUE ENTRA EN `source_refs` TIENE SU LITERAL EN EL HILO. Con "" la
		// clave de ventana queda incompleta, `intake.WindowKey.Valid()` da false y
		// `Observe` descarta el arranque sola — que es justo el trato que ya reciben el
		// cupo anti-loop agotado y la carrera del ErrAliveExists (ver la cabecera de
		// beginEvent, que enumera los casos del ""). Es el MISMO corte que advanceLiveStep
		// hace en su rama del sink durable: si el turno se cortó, no se observa.
		//
		// Y no se pierde nada: el mensaje con el que el cliente reintente sí abrirá su
		// ventana, y lo hará sobre este mismo evento vivo.
		return "", eerr
	}
	return ev.ID, eerr
}

// flowVersionFor resuelve la VERSIÓN VIGENTE del flujo que va a abrir un evento
// (T4.5.6): la que startLocked va a arrancar vía LatestDefinition, así que es la
// única verdad disponible en este camino — la decisión del resolver trae el
// flow_id sin versión, a propósito (la versión es estado del catálogo, no regla).
//
// flowID vacío es el tipo SIN flujo (el `menu`, D-043.3): ahí la versión 0 es la
// verdad, no una omisión — no hay flujo que congelar.
//
// Costura asumida y documentada: startLocked vuelve a leer LatestDefinition al
// arrancar, y entre las dos lecturas alguien puede publicar una versión nueva.
// Es la misma carrera benigna de cualquier doble lectura del catálogo; la fila
// congela la versión con la que el evento NACIÓ según este camino, y discrepar
// por una publicación concurrente no rompe nada que un humano vaya a leer mal.
// Un error de la lectura SÍ aborta el nacimiento: mejor no parir el evento que
// parirlo mintiendo (y el flujo que viene detrás fallaría igual con ese error).
func (rt *Runtime) flowVersionFor(ctx context.Context, tenantID, flowID string) (int, error) {
	if flowID == "" {
		return 0, nil
	}
	def, err := rt.store.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		return 0, fmt.Errorf("runtime: resolver la versión del flujo %q al parir el evento: %w", flowID, err)
	}
	return def.Version, nil
}
