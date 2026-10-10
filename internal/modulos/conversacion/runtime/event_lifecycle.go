// Porta internal/flujos/runtime/event_lifecycle.go @ e0159171

package runtime

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// event_lifecycle.go es la MUERTE EXPLÍCITA del evento conversacional (Plan 043 · Ola 4,
// E-5): el cierre NATURAL cuando su flujo termina y la cancelación por id desde la app del
// dueño. Son los ÚNICOS dos caminos que mueven `status` (D-043.5): ni el vencimiento, ni el
// escape, ni el event_stop tocan la fila; esos solo sueltan el puntero.
//
// # El cierre natural (no exportado; nace en el verde y se prueba por HandleIncoming)
//
// LC-1 · Cuándo: el estado quedó terminal (Conversation.Finished) y tiene DUEÑO
// (OwnerEventID). Con el dueño vacío no se transiciona NINGÚN evento, haya o no activo.
//
// LC-2 · A quién: al DUEÑO, nunca al activo (RT-16). Con los dos punteros iguales —el caso
// común— es el gesto de siempre.
//
// LC-3 · A qué estado, según el desenlace que el módulo declaró (Conversation.Outcome):
// model.OutcomeCancelled → events.StatusCancelled y efecto event_cancelled; cualquier otro,
// el vacío incluido → events.StatusClosed y event_closed. El efecto sigue al ESTADO, no al
// camino.
//
// LC-4 · Los punteros, en el MISMO Save del turno (no hay una segunda escritura del estado):
// el dueño se apaga siempre que el cierre se consuma; el activo, SOLO si era el mismo evento.
// Un `menu` activo sobre un `cart` que termina sigue open y activo.
//
// LC-5 · events.ErrNotOpen es carrera benigna: otro escritor selló la muerte primero. Se
// apagan los punteros igual, no se emite efecto y se anuncia a Info «runtime: el evento ya no
// estaba open al terminar su flujo (carrera benigna)».
//
// LC-6 · Un fallo REAL de la transición no aborta el turno y NO limpia ningún puntero: WARN
// «runtime: no se pudo cerrar el evento al terminar su flujo; el puntero se conserva para
// reintentar», el estado se guarda terminal con sus punteros, y el SIGUIENTE entrante
// reintenta el cierre sobre esa misma fila en vez de soltarla.
//
// LC-7 · Si el evento murió pero no se pudo releer antes para su telemetría, no hay efecto y
// se anuncia a WARN «runtime: el evento murió al terminar su flujo pero no se pudo releer
// antes para emitir su efecto de ciclo de vida».
//
// LC-8 · La solicitud NO se toca, tampoco con desenlace cancelado: su estado lo deja la
// proyección del módulo. Abandonar es solo de la cancelación desde la app.
//
// LC-9 · Un flujo de UN solo paso (un `message` sin `next`) cierra su evento en el mismo
// arranque: la fila queda `closed` y el puntero no sobrevive ni un turno.

// ErrNoEventPlane lo devuelven GetEventForTenant y CancelEventForTenant cuando el runtime se
// construyó sin WithEventStore. NO se disfraza de «no encontrado»: un despliegue sin plano de
// eventos que expone el endpoint de cancelación es un error de cableado, y contestarle 404 al
// dueño lo escondería.
var ErrNoEventPlane = errors.New("runtime: sin plano de eventos cableado (WithEventStore)")

// GetEventForTenant lee un evento por id ACOTADO al tenant (Plan 043 · T4.2). Delega todo en
// el almacén, que aísla en el SQL: aquí no se re-decide nada.
//
//   - Sin plano de eventos: ErrNoEventPlane y el evento cero.
//   - Id inexistente o de OTRO tenant: el error del almacén tal cual (events.ErrEventNotFound,
//     la misma respuesta para los dos: no filtra existencia cruzada).
//   - Cualquier otro error del almacén sube sin envolver.
//
// No toma el candado de la conversación ni escribe nada.
func (rt *Runtime) GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	panic(pendiente.Implementar("runtime.Runtime.GetEventForTenant"))
}

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
	panic(pendiente.Implementar("runtime.Runtime.CancelEventForTenant"))
}
