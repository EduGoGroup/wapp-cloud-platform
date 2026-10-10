// Porta internal/flujos/admin/triggers.go @ 724f3035
//
// Trozo de triggers.go, partido por tema (E-13): el listado de reglas de disparo y sus
// dos marcas derivadas.

package admin

import (
	"context"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// listDTO proyecta una regla para la RESPUESTA DEL LISTADO, que es la única que lleva
// las dos marcas derivadas (MD-043.11, D-054.6): el listado es donde el dueño mira su
// configuración, mientras que la respuesta 201 de un alta describe lo que acaba de
// escribir y no es sitio para avisar de nada.
//
// ShadowedByEventList se calcula por kind y no consulta estado alguno: la sombra la
// proyecta la lista de tipos sobre TODA regla de fallback, y si ese tenant acaba en
// el caso vacío —sin ningún event_kind habilitado y sin nada rescatable— el turno
// vuelve a su fallback (D-043.20). Por eso la marca dice «te ensombrece», no «estás
// muerta».
//
// FlowNeedsEvent (T2.6) SÍ necesita resolver la definición del flujo —de ahí que
// listDTO gane ctx y checker (H3 del contrato de entrada)— y solo se calcula para
// keyword/fallback: son las dos puertas que pueden intentar arrancar sin evento
// padre (ver el docstring del campo). Devuelve error si checker falla por algo
// DISTINTO de «flujo inexistente» (ese caso ya lo resuelve durableContent en
// fail-open): el llamante decide si aborta el listado entero o no.
func listDTO(ctx context.Context, checker DurableFlowChecker, r trigger.Rule) (triggerDTO, error) {
	dto := dtoFromRule(r)
	dto.ShadowedByEventList = r.Kind == trigger.KindFallback
	if r.Kind == trigger.KindKeyword || r.Kind == trigger.KindFallback {
		durable, err := durableContent(ctx, checker, r.TenantID, r.FlowID)
		if err != nil {
			return triggerDTO{}, err
		}
		dto.FlowNeedsEvent = durable
	}
	return dto, nil
}

// ListTriggersHandler devuelve el handler de GET .../triggers: lista las reglas del
// tenant del token (INV-8), en el orden en que las da el store. No mira el método.
//
//   - 401 "autenticación requerida" (sin Identity o con TenantID vacío).
//   - 500 "no se pudieron listar las reglas de disparo" si List falla.
//   - 500 "no se pudo resolver el contenido durable de una regla" si el checker falla.
//   - 200 con un arreglo JSON de reglas (`[]`, nunca null, si no hay), con los mismos
//     campos que el 201 del alta más dos marcas DERIVADAS (cero DDL, cero estado), que
//     se omiten cuando son false:
//   - `shadowed_by_event_list`: true en TODA regla kind fallback y solo en ellas
//     (D-043.20, MD-043.11): avisa de que ya no se emite en la conversación sin
//     evento elegido. Se calcula por kind, sin consultar nada.
//   - `flow_needs_event`: true en una regla kind keyword o fallback cuyo flow_id
//     tiene contenido durable según el checker (Plan 054 · D-054.6, T2.6). El
//     checker se consulta con el TenantID y el FlowID DE LA REGLA, y solo para
//     esos dos kinds; con checker nil nunca se marca.
func ListTriggersHandler(store TriggerStore, checker DurableFlowChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}
		rules, err := store.List(r.Context(), id.TenantID)
		if err != nil {
			http.Error(w, "no se pudieron listar las reglas de disparo", http.StatusInternalServerError)
			return
		}
		out := make([]triggerDTO, 0, len(rules))
		for _, rule := range rules {
			dto, err := listDTO(r.Context(), checker, rule)
			if err != nil {
				http.Error(w, "no se pudo resolver el contenido durable de una regla", http.StatusInternalServerError)
				return
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	})
}
