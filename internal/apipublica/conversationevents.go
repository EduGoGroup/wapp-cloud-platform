// Porta internal/publicapi/conversationevents.go @ 724f3035 (líneas 1-212: todo menos
// formatInstant, que ya nació en instants.go por T-15) y el registro de I18
// (internal/publicapi/publicapi.go @ 724f3035, registerConversationEvents, líneas 794-847; el
// campo ConversationEvents de Deps, líneas 177-181).
//
// conversationevents.go — LA BANDEJA DE EVENTOS CONVERSACIONALES (Plan 043 · T3.9b, REQ-28; mapa
// §2.9, I18): GET /api/v1/conversation-events, el listado por el que el dueño limpia lo que la
// bandeja de solicitudes no alcanza. Es el mínimo que hace EJECUTABLE la limpieza de REQ-26d: un
// evento sin `intake` no lo alcanza POST /api/v1/intakes/discard, y …/cancel opera por id;
// decirle al dueño «ve limpiando» sin darle dónde mirar era una instrucción imposible de seguir.
// La acción de limpieza vive en conversationeventcancel.go (I19).
//
// En el rojo solo existen el puerto, ConversationEventsDeps y MountConversationEvents; el
// handler, la fila del wire (conversationEventDTO en la cara vieja) y el traductor de la query
// son no exportados y nacen con el verde (05 E-4, P6). Sus promesas viven en el comentario de
// MountConversationEvents.

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ConversationEventLister es el puerto de LECTURA de los eventos conversacionales. Lo satisface
// *events.Store del módulo conversación NUEVO.
//
// Es deliberadamente de UNA operación: esta ruta LEE. El tenant lo pone la cara desde la
// Identity del token (INV-8) y nunca viaja en la query: no hay forma de pedirle a este puerto
// los eventos de otro.
type ConversationEventLister interface {
	// ListEvents devuelve una página de los eventos del tenant que casan con el filtro, última
	// actividad primero, con el total de coincidencias del filtro (no de la página).
	ListEvents(ctx context.Context, tenantID string, f events.ListFilter) (events.EventPage, error)
}

// ConversationEventsDeps es lo que I18 necesita. En la cara vieja eran los campos
// ConversationEvents y Entitlements de publicapi.Deps.
type ConversationEventsDeps struct {
	// Events lista los eventos (ConversationEvents en la cara vieja). nil ⇒ la ruta no se monta.
	Events ConversationEventLister
	// Entitlements es el resolver de derechos del módulo acceso NUEVO: sirve al gate de la ruta
	// y al filtro por tipos del handler. nil ⇒ la ruta no se monta.
	Entitlements entitlements.Resolver
}

// MountConversationEvents registra en c la ruta I18, "GET /api/v1/conversation-events", solo si
// d.Events y d.Entitlements son los dos distintos de nil. Si falta cualquiera la ruta NO existe
// (404 de ruta inexistente; no se registra ningún patrón): mejor eso que una bandeja que se abre
// sin poder comprobar el plan. Es INDEPENDIENTE de MountConversationEventCancel: el cableado
// puede traer el listado sin la cancelación (así vivió entre la Ola 3 y la 4) o al revés, y la
// mitad presente funciona.
//
// Cadena R de Common, permiso "intakes.read", y DENTRO de ella el gate de feature
// entitlements.RequireAnyFeature(d.Entitlements, events.KindFeatures()...). En orden: 401 sin
// token; 403 {"error":"permiso denegado"} sin el permiso o con un token sin empresa; 403
// {"error":"feature_not_enabled","features":["cart_basic","media","menu","survey"]} si el tenant
// no tiene NINGUNA de las features de tipo —o si el resolver falla al preguntar: el gate decide
// el ACCESO y falla cerrado—, sin haber consultado el puerto. Es lectura: CERO registros de
// auditoría en todos los desenlaces.
//
// DOS decisiones que conviene no deshacer sin leer esto:
//
// El PERMISO es el de la bandeja de solicitudes, y no un `events.read` nuevo. Un permiso nuevo
// no lo tiene nadie hasta que una migración se lo conceda al rol `operator`, así que estrenarlo
// aquí dejaría la ruta montada y devolviendo 403 a la única persona que la necesita. Además las
// dos bandejas son la misma tarea partida en dos: esta enseña exactamente lo que a la otra se le
// escapa.
//
// La FEATURE no es una: son las de los CUATRO tipos de fábrica, y basta tener UNA (decisión de
// Jhoan, 2026-08-09). Se descartó `cart_basic` —el gate de la bandeja de solicitudes— por lo
// que dejaba fuera: esta lista abarca menu, cart, survey y media, y gatearla con la del carrito
// habría cegado a un tenant de solo encuestas sobre sus PROPIAS encuestas. El gate abre la
// puerta; lo que se ve dentro lo acota events.AllowedKinds en el handler, con el MISMO mapa
// tipo→feature del despachador. Las dos mitades son necesarias: sin la primera un tenant sin
// nada entraría; sin la segunda, entrar por `survey` enseñaría también los carritos.
//
// LA QUERY. Todos los parámetros son opcionales, y uno vacío (`status=`) cuenta como ausente:
//
//   - `status`: open | closed | cancelled. Ausente ⇒ open (lo que se limpia es lo que sigue
//     abierto). Otro valor ⇒ 400 {"error":"status desconocido: usa open, closed o cancelled"};
//   - `content`: any | none | alive. Ausente ⇒ any. Otro valor ⇒ 400 {"error":"content
//     desconocido: usa any, none o alive"}. El filtro que importa es `content=none`;
//   - `contact_id`: el identificador OPACO (UUID) del contacto (ADR-0017). Lo que uuid.Validate
//     rechaza ⇒ 400 {"error":"contact_id inválido: es el identificador opaco (UUID) del
//     contacto"}: sin esa comprobación un typo sería un error de Postgres al castear, y el dueño
//     vería un 500 por haber escrito mal un id;
//   - `stale`: TRI-ESTADO. Ausente ⇒ la marca no filtra (INV-19: informa); verdadero ⇒ solo los
//     vencidos; falso ⇒ solo los que no lo están. Lo que strconv.ParseBool rechaza ⇒ 400
//     {"error":"stale inválido: usa true o false"};
//   - `kind`: llega al filtro TAL CUAL, sin validar (un tipo desconocido es una lista vacía, no
//     un 400);
//   - `page` (por defecto 1) y `page_size` (por defecto 50): NO se validan. Un valor que no es
//     un entero no negativo cae al valor por defecto, `page=0` es la página 1, `page_size=0` es
//     50 y por encima de 200 es 200: pedir 100000 no es un error del llamante sino una petición
//     que el contrato acota.
//
// Un valor desconocido se DICE en vez de ignorarse: devolver la bandeja entera ante
// `status=abiertos` sería peor que un error, porque quien la lee creería estar viendo lo que
// pidió. Si varios parámetros son inválidos gana el primero de este orden: status, content,
// contact_id, stale. En ningún 400 se consulta el resolver (más allá del gate) ni el puerto.
//
// LA LLAMADA AL PUERTO. Tras la query, events.AllowedKinds(d.Entitlements, tenant del token):
// si falla ⇒ 500 {"error":"no se pudieron resolver los tipos habilitados del plan"} SIN llamar
// al puerto. 🔴 Es 500 y NO una lista recortada: aquí se decide el CONTENIDO, y una bandeja que
// dice «no queda nada» cuando la verdad es «no pude mirar» manda al dueño a casa con eventos
// abiertos (es la única asimetría deliberada con el gate, que ante lo mismo responde 403).
//
// El puerto se llama UNA vez, con el contexto de la petición y el tenant del TOKEN (INV-8: un
// `tenant_id` en la query no cuenta), y con un filtro ya NORMALIZADO —el tope de 200 es del
// CONTRATO de esta ruta, así que vale aunque detrás haya otra implementación del puerto—:
// Status, Content, Page y PageSize con sus valores por defecto puestos; Kind y ContactID tal
// cual llegaron; Stale nil si no vino; y Kinds = los tipos que el plan del tenant incluye, en
// orden alfabético (tener `survey` no da derecho a ver los carritos: pedir `kind=cart` sin esa
// feature es una lista vacía, no un 403). Si el puerto falla ⇒ 500 {"error":"no se pudieron
// listar los eventos conversacionales"}, SIN repetir el error.
//
// LA RESPUESTA. 200 {"events":[…],"page","page_size","total"}: `page`, `page_size` y `total`
// son los de la página que DEVOLVIÓ el puerto (el total es el de coincidencias del filtro: lo
// único que le dice a quien limpia cuántos le quedan), y `events` es `[]`, nunca `null`, si no
// hay ninguno. Cada evento lleva, en este orden:
//
//		id, history_id, kind, status, contact_id, session_id, content_state, content_ref, stale,
//		created_at, last_activity_at, closed_at
//
//	  - `contact_id` viaja OPACO tal cual está en BD (INV-01 / ADR-0017): la cara no lo descifra
//	    ni lo enriquece. El tenant NO viaja: siempre es el del token;
//	  - `stale` es la marca DERIVADA «vencido» tal cual la resolvió el puerto: la cara no la
//	    recalcula. Informa y no filtra (INV-19): un vencido se sigue viendo;
//	  - `content_state` y `content_ref` son el CONTENIDO del evento (D-043.21/22), derivados por
//	    el puerto. Van con omitempty porque la AUSENCIA es el dato: un evento que no produjo
//	    contenido no publica ninguna de las dos claves (un `"content_state":""` diría «hay
//	    contenido en un estado vacío», que es mentira);
//	  - los tres instantes van en RFC3339 UTC al segundo; el instante cero viaja como "" y la
//	    clave va SIEMPRE: `closed_at` es "" mientras el evento siga abierto.
//
// Lo que NO está es tan deliberado como lo que sí: ni una línea del historial (texto del
// cliente, cifrado, ADR-0034 nivel 2), ni el resumen, ni el flow_id, ni la versión del flujo.
//
// Rarezas portadas tal cual: `stale` acepta todo lo que acepta strconv.ParseBool (1, t, T,
// TRUE, True, 0, f, F, FALSE, False), aunque el mensaje diga «usa true o false»; y `contact_id`
// acepta las cuatro formas de uuid.Validate (canónica, sin guiones, entre llaves y con prefijo
// `urn:uuid:`) y llega al puerto como se escribió, sin normalizar.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common), con un mensaje que nombra MountConversationEvents. Si falta una dependencia no se
// monta nada y k ni se mira.
func MountConversationEvents(c *Cara, k Common, d ConversationEventsDeps) {
	panic(pendiente.Implementar("apipublica.MountConversationEvents"))
}
