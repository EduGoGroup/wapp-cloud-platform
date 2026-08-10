package publicapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ConversationEventLister es el puerto de LECTURA de los eventos conversacionales
// que consume la API pública (Plan 043 · T3.9b, REQ-28). Lo satisface
// *events.Store. Es deliberadamente de una sola operación: esta ruta LEE, y la
// acción de limpieza es POST …/cancel, que llega con su propia tarea (T4.2).
//
// El tenant lo pone el handler desde la Identity del token (INV-8) y nunca viaja
// en la query: no hay forma de pedirle a este puerto los eventos de otro.
type ConversationEventLister interface {
	ListEvents(ctx context.Context, tenantID string, f events.ListFilter) (events.EventPage, error)
}

// conversationEventDTO es la proyección al wire de un evento conversacional.
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
type conversationEventDTO struct {
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

// conversationEventListResponse es el contrato de GET /api/v1/conversation-events:
// la página más el TOTAL de coincidencias del filtro, igual que la bandeja de
// solicitudes. El total no es adorno: es lo único que le dice a quien limpia
// cuántos le quedan.
type conversationEventListResponse struct {
	Events   []conversationEventDTO `json:"events"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
	Total    int                    `json:"total"`
}

// listConversationEventsHandler sirve GET /api/v1/conversation-events: los eventos
// del tenant del token (INV-8), última actividad primero, con filtros
// status/kind/content/stale/contact_id y paginación page/page_size (default 50,
// máx 200).
//
// Es el mínimo que hace EJECUTABLE la limpieza de REQ-26d: un evento sin `intake`
// no lo alcanza POST /api/v1/intakes/discard, y …/cancel opera por id — decirle al
// dueño «ve limpiando» sin darle dónde mirar era una instrucción imposible de
// seguir. De ahí que el filtro que importa sea `content=none`.
func listConversationEventsHandler(lister ConversationEventLister,
	feats entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		filter, msg := parseConversationEventFilter(r)
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

		out := make([]conversationEventDTO, 0, len(page.Events))
		for _, ev := range page.Events {
			out = append(out, toConversationEventDTO(ev))
		}
		writeJSON(w, http.StatusOK, conversationEventListResponse{
			Events: out, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
		})
	})
}

// parseConversationEventFilter traduce la query al filtro del store. Un valor
// desconocido se DICE (mensaje de 400) en vez de ignorarse: devolver la bandeja
// entera ante `status=abiertos` sería peor que un error, porque quien la lee
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
func parseConversationEventFilter(r *http.Request) (events.ListFilter, string) {
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
	contacto := q.Get("contact_id")
	if contacto != "" && uuid.Validate(contacto) != nil {
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
		ContactID: contacto,
		Page:      parseIntQuery(r, "page", 1),
		PageSize:  parseIntQuery(r, "page_size", events.DefaultPageSize),
	}.Normalized(), ""
}

// toConversationEventDTO proyecta un evento con su marca al wire. Los instantes
// van en RFC3339 UTC; el cero de time.Time (evento sin cerrar) viaja como cadena
// vacía y no como «0001-01-01», que es una fecha que no significa nada.
func toConversationEventDTO(ev events.Rescuable) conversationEventDTO {
	return conversationEventDTO{
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

// formatInstant devuelve el instante en RFC3339 UTC, o vacío si es el cero.
func formatInstant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
