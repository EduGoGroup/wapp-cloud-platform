// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de events.go (E-13): EL reloj de conversación aplicado al entrante que llega con un
// evento ACTIVO (EV-6, RT-15) y la suelta por inactividad. Solo se movieron declaraciones.

// eventClock es EL reloj de conversación (ADR-0029 E-6) aplicado al entrante que
// llega con un evento ACTIVO. Devuelve si el turno debe tratarse como arranque nuevo.
//
// Dos caminos y ninguna columna nueva:
//
//   - Dentro de la ventana ⇒ Touch: la interacción estampa last_activity_at := now()
//     (T3.1). Por eso un chat que dura meses no vence nunca: el plazo se mide contra
//     la ÚLTIMA interacción, no contra el nacimiento del evento. Un plazo absoluto
//     desde created_at mataría al cliente fiel, que es justo el que no debe morir.
//   - Vencido el silencio ⇒ la conversación se SUELTA (T3.2, REQ-09): el entrante no
//     se ata al evento anterior y entra por el resolver de disparos. La fila del
//     evento NO se toca: sigue `open`, con su solicitud y sus líneas, rescatable
//     diciendo su tipo. «Suspendido» sigue siendo DERIVADO — no hay estado `expired`
//     ni nada que escribir para representarlo.
//
// El evento no encontrado entre los vivos (cerrado desde la app del dueño mientras
// el puntero seguía apuntándolo) NO vence nada y NO refresca nada: la conversación
// avanza y el puntero se corregirá solo en el siguiente salto. Sin plano de eventos
// cableado (events nil) esto no existe: no-regresión total (INV-6).
func (rt *Runtime) eventClock(ctx context.Context, tenantID string, key store.Key, eventID string) (bool, error) {
	if rt.events == nil {
		return false, nil
	}
	ev, alive, err := rt.aliveByID(ctx, tenantID, key.SessionID, key.ContactID, eventID)
	if err != nil {
		return false, err
	}
	if !alive {
		rt.log.Debug("runtime: el evento activo ya no está vivo; su reloj no gobierna este entrante",
			"session_id", key.SessionID)
		return false, nil
	}
	ttl, err := rt.eventInactivityTTL(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if rt.events.IsSuspended(ev, ttl) {
		if rerr := rt.releaseForNewConversation(ctx, key); rerr != nil {
			return true, rerr
		}
		rt.emitEventEffect(ctx, ev, EffectEventInactivityExpired)
		return true, nil
	}
	if terr := rt.events.Touch(ctx, ev.ID); terr != nil {
		return false, fmt.Errorf("runtime: refrescar el reloj del evento activo: %w", terr)
	}
	return false, nil
}

// releaseForNewConversation suelta la conversación cuyo evento activo lleva más
// tiempo callado que el silencio que el tenant tolera (T3.2, REQ-09).
//
// Se borra el flow_state entero y no solo su columna event_id, y la diferencia es la
// que decide si el cliente recibe respuesta: lo que sigue es el camino de una
// conversación NUEVA (resolver de disparos → despachador), y ese camino arranca
// flujos con startLocked, que rechaza con ErrConversationExists si queda un estado
// vivo. Dejar la fila con el puntero apagado convertiría en silencio cualquier
// palabra clave dicha tras el vencimiento. Lo que se borra es el PUNTERO DE NODO del
// motor, nunca datos de negocio — la misma regla que enterEventFlow.
//
// Y lo que NO se hace es la mitad que importa: no se llama a TransitionEvent, no se
// toca `status` y no se vacía ninguna solicitud. El evento sigue `open` con sus
// líneas para que un humano —o el propio cliente— lo retome (INV-09, D-041.18).
func (rt *Runtime) releaseForNewConversation(ctx context.Context, key store.Key) error {
	if err := rt.store.Delete(ctx, key); err != nil {
		return fmt.Errorf("runtime: soltar la conversación por inactividad del evento: %w", err)
	}
	rt.autoreplyStreaks.Close(key, rt.now()) // fin de episodio (Plan 049, ver streak.go).
	return nil
}

// eventInactivityTTL lee EL reloj de conversación del tenant (E-6). Un fallo de
// settings NO se traga: a diferencia del TTL conversacional —donde no vencer es el
// lado seguro— aquí el valor decide si se CIERRA un evento del cliente, y cerrar por
// un fallo transitorio de lectura sería destruir un pedido por un timeout.
func (rt *Runtime) eventInactivityTTL(ctx context.Context, tenantID string) (time.Duration, error) {
	settings, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("runtime: leer el TTL de inactividad del evento: %w", err)
	}
	return settings.EventInactivityTTL, nil
}
