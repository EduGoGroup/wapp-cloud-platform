package publicapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ConversationEventCanceller es el puerto de CANCELACIÓN del evento conversacional
// (Plan 043 · T4.2, REQ-28/D-043.8): la acción de limpieza que le faltaba a la
// bandeja de eventos —su lectura es ConversationEventLister, deliberadamente
// aparte: la satisface *events.Store, mientras que cancelar orquesta tres efectos
// (guard del evento, puntero del motor, solicitud colgante) y eso es del runtime—.
// Lo satisface *runtime.Runtime (internal/flujos/runtime).
//
// El tenant lo pone el handler desde la Identity del token (INV-8) y ACOTA las dos
// operaciones: un id de otro tenant no existe para este puerto (ErrEventNotFound),
// que es lo que permite responder 404 sin consultar nada más.
type ConversationEventCanceller interface {
	// GetEventForTenant lee el evento SOLO si pertenece al tenant. Sin fila para
	// ese tenant+id devuelve events.ErrEventNotFound — también cuando la fila
	// existe bajo otro tenant, que para este puerto es exactamente lo mismo.
	GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
	// CancelEventForTenant cancela con el guard open→cancelled: sella closed_at,
	// limpia el flow_state que apuntara al evento y abandona el contenido vivo que
	// colgara de él — POR EVENTO (D-043.21/T4.5.5): el runtime cierra al hijo por
	// su propio event_id, y ni él ni este handler cargan ids de ningún dominio de
	// contenido. IDEMPOTENTE: sobre un evento ya terminal devuelve la fila tal
	// cual, sin tocarla. Las carreras internas llegan aquí ya resueltas: el
	// retorno es el estado final.
	CancelEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
}

// cancelConversationEventHandler sirve POST /api/v1/conversation-events/{id}/cancel
// (Plan 043 · T4.2): el dueño cierra a mano un evento que el flujo no cerró solo.
//
// UN SOLO criterio de visibilidad, el del listado (ver la nota de
// registerConversationEvents): todo lo que este handler no te deja ver responde el
// MISMO 404 con el MISMO cuerpo —id inexistente, id de otro tenant (nunca 403: un
// 403 confirmaría que el id existe, criterio de T4.2; la conclusión de la Ola 3 de
// que el cross-tenant era imposible valía para el LISTADO, donde no hay id que
// probar — aquí el {id} es explícito y el 404 es obligatorio) y evento de un tipo
// cuya feature el tenant no tiene (un tipo que no ves en el listado no existe para
// ti; cancelarlo sería «cerrar los que no ve», que la nota prohíbe)—.
//
// El fallo de AllowedKinds se PROPAGA como 5xx, igual que en el listado: decide
// CONTENIDO, no acceso, y disfrazarlo de 404 diría «ese evento no existe» cuando
// la verdad es «no pude mirar qué tipos ves».
func cancelConversationEventHandler(canceller ConversationEventCanceller,
	feats entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		// El id es la identidad TÉCNICA (UUID, columna UUID en BD). Un id que no
		// puede existir recibe el mismo 404 que uno que no existe —es literalmente
		// eso— y de paso evita que el cast de Postgres convierta un typo en 500
		// (mismo motivo que el contact_id del listado).
		eventID := r.PathValue("id")
		if uuid.Validate(eventID) != nil {
			writeEventNotFound(w)
			return
		}

		ev, err := canceller.GetEventForTenant(r.Context(), id.TenantID, eventID)
		switch {
		case errors.Is(err, events.ErrEventNotFound):
			writeEventNotFound(w)
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo leer el evento")
			return
		}

		// El MISMO filtro por tipos del listado (events.AllowedKinds, mismo mapa
		// tipo→feature del despachador), aplicado sobre el evento concreto.
		kinds, err := events.AllowedKinds(r.Context(), feats, id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron resolver los tipos habilitados del plan")
			return
		}
		if !slices.Contains(kinds, ev.Kind) {
			writeEventNotFound(w)
			return
		}

		out, err := canceller.CancelEventForTenant(r.Context(), id.TenantID, eventID)
		switch {
		case errors.Is(err, events.ErrEventNotFound):
			// Inalcanzable en la práctica (nada borra eventos, INV-09), pero si el
			// puerto lo dice, la respuesta honesta sigue siendo el mismo 404.
			writeEventNotFound(w)
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo cancelar el evento")
			return
		}

		// La MISMA proyección que una fila del listado (conversationEventDTO), para
		// que la pantalla que pinta la bandeja pueda pintar la respuesta sin otro
		// contrato. `stale` viaja false: la marca «vencido» es una pregunta sobre
		// eventos ABIERTOS y este acaba de dejar de serlo (o ya lo había dejado).
		// `content_state`/`content_ref` viajan OMITIDOS: son derivados del join del
		// LISTADO (D-043.22) y esta respuesta no los recalcula — quien quiera el
		// contenido tras cancelar vuelve a la bandeja, que es su fuente.
		writeJSON(w, http.StatusOK, toConversationEventDTO(events.Rescuable{Event: out}))
	})
}

// writeEventNotFound es EL 404 de este endpoint, en singular a propósito: que los
// tres caminos que no ven el evento (no existe, es de otro tenant, es de un tipo
// que el plan no incluye) compartan función es lo que garantiza el cuerpo
// idéntico — no se puede distinguir desde fuera cuál de los tres fue (INV-8).
func writeEventNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "evento no encontrado")
}
