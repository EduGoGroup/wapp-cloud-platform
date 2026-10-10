// Porta internal/publicapi/conversationeventcancel.go @ 724f3035 (126 líneas, entero) y el
// registro de I19 (internal/publicapi/publicapi.go @ 724f3035, registerConversationEvents, líneas
// 794-852; el campo EventCanceller de Deps, líneas 182-187).
//
// conversationeventcancel.go — LA ACCIÓN DE LIMPIEZA DE LA BANDEJA DE EVENTOS (Plan 043 · T4.2,
// REQ-28/D-043.8; mapa §2.9, I19): POST /api/v1/conversation-events/{id}/cancel. El dueño cierra
// a mano un evento que el flujo no cerró solo. Su lectura es I18 (conversationevents.go),
// deliberadamente aparte: aquella la satisface el store, mientras que cancelar orquesta tres
// efectos (guard del evento, puntero del motor, contenido colgante) y eso es del runtime.
//
// En el rojo solo existen el puerto, ConversationEventCancelDeps y
// MountConversationEventCancel; el handler y su 404 único (writeEventNotFound en la cara vieja)
// son no exportados y nacen con el verde (05 E-4, P6). Sus promesas viven en el comentario de
// MountConversationEventCancel.

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ConversationEventCanceller es el puerto de CANCELACIÓN del evento conversacional. Lo satisface
// *runtime.Runtime del módulo conversación NUEVO (ruta directa al runtime, mapa §3).
//
// El tenant lo pone la cara desde la Identity del token (INV-8) y ACOTA las dos operaciones: un
// id de otro tenant no existe para este puerto (events.ErrEventNotFound), que es lo que permite
// responder 404 sin consultar nada más.
type ConversationEventCanceller interface {
	// GetEventForTenant lee el evento SOLO si pertenece al tenant. Sin fila para ese tenant+id
	// devuelve events.ErrEventNotFound —también cuando la fila existe bajo otro tenant, que para
	// este puerto es exactamente lo mismo—.
	GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
	// CancelEventForTenant cancela con el guard open→cancelled: sella closed_at, limpia el
	// flow_state que apuntara al evento y abandona el contenido vivo que colgara de él, POR
	// EVENTO (D-043.21/T4.5.5): ni el runtime ni la cara cargan ids de ningún dominio de
	// contenido. IDEMPOTENTE: sobre un evento ya terminal devuelve la fila tal cual, sin
	// tocarla. Las carreras internas llegan ya resueltas: el retorno es el estado final.
	CancelEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
}

// ConversationEventCancelDeps es lo que I19 necesita. En la cara vieja eran los campos
// EventCanceller y Entitlements de publicapi.Deps.
type ConversationEventCancelDeps struct {
	// Canceller cancela el evento (EventCanceller en la cara vieja). nil ⇒ la ruta no se monta.
	Canceller ConversationEventCanceller
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO que recibe
	// MountConversationEvents: sirve al gate de la ruta y al filtro por tipos del handler. nil ⇒
	// la ruta no se monta.
	Entitlements entitlements.Resolver
}

// MountConversationEventCancel registra en c la ruta I19, "POST
// /api/v1/conversation-events/{id}/cancel", solo si d.Canceller y d.Entitlements son los dos
// distintos de nil. Si falta cualquiera la ruta NO existe (404 de ruta inexistente; no se
// registra ningún patrón). Es INDEPENDIENTE de MountConversationEvents: la cancelación se monta
// sin el listado, y al revés.
//
// Cadena W de Common, permiso "intakes.write", recurso de auditoría "conversation_event", y
// DENTRO de ella —después de la auditoría— el MISMO gate de feature que I18:
// entitlements.RequireAnyFeature(d.Entitlements, events.KindFeatures()...). En orden:
//
//   - 401 sin token y 403 {"error":"permiso denegado"} sin el permiso o con un token sin
//     empresa: ningún registro de auditoría;
//   - 403 {"error":"feature_not_enabled","features":["cart_basic","media","menu","survey"]} si
//     el tenant no tiene NINGUNA feature de tipo (o si el resolver falla al preguntar: falla
//     cerrado). El gate corta ANTES de mirar el id, así que el cuerpo es byte-idéntico para un
//     id que existe y para uno inventado («sin revelar si el evento existe»), y el puerto no se
//     toca. Como el gate va por dentro de la auditoría, este 403 SÍ deja su registro "failure";
//   - UN registro por petición que pasa el permiso: "success" con el 200, "failure" con Meta
//     {"status":<código>} en cualquier 4xx o 5xx. Cancelar es una escritura irreversible.
//
// El PERMISO es `intakes.write` y no un `events.write` nuevo, por la misma razón que el de I18:
// ya está concedido al rol `operator`, y su precedente exacto es POST /api/v1/intakes/discard
// —cancelar un evento ES operar la limpieza de la bandeja—. Lo que sí es propio es el RECURSO de
// auditoría: la bitácora distingue cancelar un evento de descartar una solicitud aunque el
// permiso sea el mismo.
//
// EL HANDLER. Sin cuerpo: lo que venga en él no se lee. El tenant es SIEMPRE el del token
// (INV-8: uno en la query no cuenta). En orden (gana el primero que case):
//
//  1. el {id} no es un UUID (lo que uuid.Validate rechaza) ⇒ 404, sin tocar el puerto. Un id
//     que no puede existir recibe el mismo 404 que uno que no existe —es literalmente eso— y de
//     paso evita que el cast de Postgres convierta un typo en 500;
//  2. d.Canceller.GetEventForTenant(tenant, id): errors.Is(err, events.ErrEventNotFound) ⇒ 404;
//     otro error ⇒ 500 {"error":"no se pudo leer el evento"};
//  3. events.AllowedKinds(d.Entitlements, tenant) falla ⇒ 500 {"error":"no se pudieron resolver
//     los tipos habilitados del plan"}. 🔴 Se PROPAGA como 5xx, igual que en el listado: decide
//     CONTENIDO, no acceso, y disfrazarlo de 404 diría «ese evento no existe» cuando la verdad
//     es «no pude mirar qué tipos ves»;
//  4. el tipo del evento no está entre los que el plan del tenant incluye ⇒ 404;
//  5. d.Canceller.CancelEventForTenant(tenant, id): events.ErrEventNotFound ⇒ 404 (inalcanzable
//     en la práctica —nada borra eventos, INV-09—, pero si el puerto lo dice la respuesta
//     honesta sigue siendo la misma); otro error ⇒ 500 {"error":"no se pudo cancelar el
//     evento"};
//  6. 200 con el evento que DEVOLVIÓ la cancelación.
//
// En los desenlaces 1 a 4 NO se llama a CancelEventForTenant: nada se cancela. Cada operación
// del puerto se llama una vez como mucho, con el contexto de la petición y el id tal cual vino
// en la ruta. Ningún 500 repite el error del puerto ni del resolver.
//
// 🔴 UN SOLO 404, {"error":"evento no encontrado"}, con el MISMO cuerpo en sus cuatro caminos: id
// que no es UUID, id inexistente, id de OTRO tenant (nunca 403: un 403 confirmaría que el id
// existe; aquí el {id} es explícito, así que a diferencia del listado el cross-tenant SÍ es
// alcanzable y el 404 es obligatorio) y evento de un tipo cuya feature el tenant no tiene. Desde
// fuera no se puede distinguir cuál fue (INV-8).
//
// 🔴 VISIBILIDAD Y CANCELABILIDAD SON EL MISMO CRITERIO: el filtro del paso 4 es el de I18
// (events.AllowedKinds, el mismo mapa tipo→feature del despachador) aplicado al evento concreto.
// Un tipo que no ves en el listado no existe para ti, y cancelarlo sería «cerrar los que no
// ve»; un segundo criterio dejaría al dueño viendo eventos que no puede cerrar, o cerrando los
// que no ve.
//
// IDEMPOTENCIA. La cara no la decide: deja pasar la del puerto. Cancelar un evento ya terminal
// (cancelado o cerrado) es 200 con la fila que el puerto devuelve, y la cara llama igual a
// CancelEventForTenant: no adelanta el desenlace mirando el estado que leyó en el paso 2.
//
// EL 200. La MISMA proyección que una fila de I18, para que la pantalla que pinta la bandeja
// pinte la respuesta sin otro contrato. En este orden:
//
//		id, history_id, kind, status, contact_id, session_id, stale, created_at, last_activity_at,
//		closed_at
//
//	  - `stale` viaja SIEMPRE false: la marca «vencido» es una pregunta sobre eventos ABIERTOS y
//	    este acaba de dejar de serlo (o ya lo había dejado);
//	  - `content_state` y `content_ref` viajan OMITIDOS: son derivados del join del LISTADO
//	    (D-043.22) y esta respuesta no los recalcula; quien quiera el contenido tras cancelar
//	    vuelve a la bandeja, que es su fuente;
//	  - los instantes van en RFC3339 UTC al segundo, y uno cero viaja como "". El tenant no viaja.
//
// Rareza portada tal cual: el {id} acepta las cuatro formas de uuid.Validate (canónica, sin
// guiones, entre llaves y con prefijo `urn:uuid:`) y llega al puerto como se escribió.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"} sin tocar
// el puerto.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common), con un mensaje que nombra MountConversationEventCancel. Si falta una dependencia no
// se monta nada y k ni se mira.
func MountConversationEventCancel(c *Cara, k Common, d ConversationEventCancelDeps) {
	panic(pendiente.Implementar("apipublica.MountConversationEventCancel"))
}
