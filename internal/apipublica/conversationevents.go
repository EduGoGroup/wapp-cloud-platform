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
// En el rojo solo existían el puerto, ConversationEventsDeps y MountConversationEvents; el
// handler, la fila del wire (conversationEventsDTO; conversationEventDTO en la cara vieja) y el
// traductor de la query son no exportados y nacieron con el verde (05 E-4, P6). Sus promesas
// viven en el comentario de MountConversationEvents.

package apipublica

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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
	// Sin el resolver de features NADA se monta, y la ruta exige además su propia
	// dependencia: mejor un 404 de ruta inexistente que una bandeja que se abre sin poder
	// comprobar el plan.
	if d.Events == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountConversationEvents")
	anyKind := entitlements.RequireAnyFeature(d.Entitlements, events.KindFeatures()...)
	c.Handle("GET /api/v1/conversation-events", protectRead(k,
		"intakes.read", anyKind(conversationEventsListHandler(d.Events, d.Entitlements))))
}

// conversationEventsDTO (conversationEventDTO en la cara vieja) es la proyección al wire de un
// evento conversacional.
//
// contact_id viaja OPACO TAL CUAL está en BD (INV-01 / ADR-0017): es un
// identificador sin número, sin JID y sin nombre, y esta capa no lo descifra ni lo
// enriquece. tenant_id NO viaja: siempre es el del token.
//
// `stale` es la marca DERIVADA «vencido» (D-043.18/E-10): el evento lleva sin
// tocarse más de lo que el tenant tolera. Se recalcula al leer y NO es una columna
// de la tabla — dos lecturas del mismo evento con el TTL cambiado en medio dicen
// cosas distintas, y eso es correcto. Lo que la marca NO hace es sacar al evento de
// la lista (INV-19): un vencido de 15 días se sigue viendo, y se sigue ofreciendo,
// hasta que un humano lo cierre.
//
// `closed_at` viaja vacío mientras el evento siga abierto, que es el caso normal de
// esta bandeja. Va siempre (sin omitempty) por la misma razón que el resto: quien
// pinta la pantalla no tiene que adivinar si la clave falta porque no hay fecha o
// porque este servidor todavía no la publica.
//
// `content_state`/`content_ref` son el CONTENIDO del evento (D-043.21/22): qué
// produjo la conversación (`content_ref`, hoy el id de la solicitud — es lo que le
// permite al dueño navegar evento→solicitud) y en qué está (`content_state`, el
// vocabulario genérico de la vista `event_content`: alive | settled | discarded).
// Son DERIVADOS del join con la vista al leer, nunca almacenados — y aquí sí van
// con omitempty, al revés que `closed_at`, porque la AUSENCIA es el dato: un
// evento sin fila en la vista no produjo contenido (`content=none`), y publicar
// `"content_state": ""` diría «hay contenido en un estado vacío», que es mentira.
//
// Lo que NO está aquí es tan deliberado como lo que sí: ni una línea del historial
// (que es texto del cliente, cifrado, ADR-0034 nivel 2), ni el resumen, ni el
// flow_id. Esta es la lista por la que se LIMPIA, no la que se lee para saber qué
// dijo nadie.
type conversationEventsDTO struct {
	ID             string `json:"id"`
	HistoryID      string `json:"history_id"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	ContactID      string `json:"contact_id"`
	SessionID      string `json:"session_id"`
	ContentState   string `json:"content_state,omitempty"`
	ContentRef     string `json:"content_ref,omitempty"`
	Stale          bool   `json:"stale"`
	CreatedAt      string `json:"created_at"`
	LastActivityAt string `json:"last_activity_at"`
	ClosedAt       string `json:"closed_at"`
}

// conversationEventsListResponse (conversationEventListResponse en la cara vieja) es el contrato
// de GET /api/v1/conversation-events: la página más el TOTAL de coincidencias del filtro, igual
// que la bandeja de solicitudes. El total no es adorno: es lo único que le dice a quien limpia
// cuántos le quedan.
type conversationEventsListResponse struct {
	Events   []conversationEventsDTO `json:"events"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Total    int                     `json:"total"`
}

// conversationEventsListHandler (listConversationEventsHandler en la cara vieja) sirve GET
// /api/v1/conversation-events: los eventos del tenant del token (INV-8), última actividad
// primero, con filtros status/kind/content/stale/contact_id y paginación page/page_size (default
// 50, máx 200).
func conversationEventsListHandler(lister ConversationEventLister,
	feats entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		filter, msg := conversationEventsParseFilter(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		// El CONTENIDO se acota a los tipos que el plan del tenant incluye (decisión
		// de Jhoan del 2026-08-09). El gate de la ruta ya pasó —tiene al menos uno—,
		// pero tener `survey` no da derecho a ver los carritos: son cuatro features
		// distintas y esta bandeja las abarca todas.
		//
		// Un fallo al resolverlas es 500 y NO una lista recortada en silencio: ver
		// events.AllowedKinds.
		kinds, err := events.AllowedKinds(r.Context(), feats, id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron resolver los tipos habilitados del plan")
			return
		}
		filter.Kinds = kinds

		page, err := lister.ListEvents(r.Context(), id.TenantID, filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron listar los eventos conversacionales")
			return
		}

		out := make([]conversationEventsDTO, 0, len(page.Events))
		for _, ev := range page.Events {
			out = append(out, conversationEventsToDTO(ev))
		}
		writeJSON(w, http.StatusOK, conversationEventsListResponse{
			Events: out, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
		})
	})
}

// conversationEventsParseFilter (parseConversationEventFilter en la cara vieja) traduce la query
// al filtro del store. Un valor desconocido se DICE (mensaje de 400) en vez de ignorarse:
// devolver la bandeja entera ante `status=abiertos` sería peor que un error, porque quien la lee
// creería estar viendo lo que pidió.
//
// La paginación NO se valida: page_size=100000 no es un error del llamante sino
// una petición que el contrato acota (Normalized la baja a 200). Ese es el mismo
// criterio que la bandeja de solicitudes.
//
// El filtro sale de aquí ya NORMALIZADO, y no se deja para el store: el tope de
// 200 es del CONTRATO de esta ruta, así que tiene que valer aunque quien esté
// detrás sea otra implementación del puerto. El store lo vuelve a aplicar —
// Normalized es idempotente— porque él también tiene que ser seguro si lo llama
// otro.
func conversationEventsParseFilter(r *http.Request) (events.ListFilter, string) {
	q := r.URL.Query()

	status := q.Get("status")
	if status != "" && !events.IsStatus(status) {
		return events.ListFilter{}, "status desconocido: usa open, closed o cancelled"
	}
	content := q.Get("content")
	if content != "" && !events.IsContentFilter(content) {
		return events.ListFilter{}, "content desconocido: usa any, none o alive"
	}

	// El contacto es un UUID OPACO (ADR-0017) y la columna es UUID: sin esta
	// comprobación, `?contact_id=marta` no sería una lista vacía sino un error de
	// Postgres al castear, y el dueño vería un 500 por haber escrito mal un id.
	contactID := q.Get("contact_id")
	if contactID != "" && uuid.Validate(contactID) != nil {
		return events.ListFilter{}, "contact_id inválido: es el identificador opaco (UUID) del contacto"
	}

	// stale es TRI-ESTADO: ausente no es lo mismo que false. Ausente ⇒ la marca no
	// filtra (INV-19: informa); `true` ⇒ solo los vencidos; `false` ⇒ solo los que
	// no lo están.
	var stale *bool
	if raw := q.Get("stale"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return events.ListFilter{}, "stale inválido: usa true o false"
		}
		stale = &v
	}

	return events.ListFilter{
		Status:    events.Status(status),
		Kind:      q.Get("kind"),
		Content:   events.ContentFilter(content),
		Stale:     stale,
		ContactID: contactID,
		Page:      parseIntQuery(r, "page", 1),
		PageSize:  parseIntQuery(r, "page_size", events.DefaultPageSize),
	}.Normalized(), ""
}

// conversationEventsToDTO (toConversationEventDTO en la cara vieja) proyecta un evento con su
// marca al wire. Los instantes van en RFC3339 UTC; el cero de time.Time (evento sin cerrar)
// viaja como cadena vacía y no como «0001-01-01», que es una fecha que no significa nada.
func conversationEventsToDTO(ev events.Rescuable) conversationEventsDTO {
	return conversationEventsDTO{
		ID:             ev.ID,
		HistoryID:      ev.HistoryID,
		Kind:           ev.Kind,
		Status:         string(ev.Status),
		ContactID:      ev.ContactID,
		SessionID:      ev.SessionID,
		ContentState:   ev.ContentState,
		ContentRef:     ev.ContentRef,
		Stale:          ev.Stale,
		CreatedAt:      formatInstant(ev.CreatedAt),
		LastActivityAt: formatInstant(ev.LastActivityAt),
		ClosedAt:       formatInstant(ev.ClosedAt),
	}
}
