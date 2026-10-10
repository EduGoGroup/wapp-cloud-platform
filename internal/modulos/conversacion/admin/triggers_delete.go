// Porta internal/flujos/admin/triggers.go @ 724f3035
//
// Trozo de triggers.go, partido por tema (E-13): la baja de reglas de disparo y la
// puerta de atrás de D-054.8.

package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// blocksLastLiveEventStart implementa la dirección (ii) de T2.7 (D-054.8) — «la
// puerta de atrás»: borrar la ÚLTIMA regla event_start viva de un tenant que YA
// tiene una regla kind='fallback' o kind='keyword' habilitada hacia un flujo
// durable reabriría el mismo corner sin tocar esa regla. rules es el listado
// COMPLETO del tenant (ya tenant-scoped por TriggerStore.List, INV-8); targetID es
// la fila que DeleteTriggerHandler está a punto de borrar.
//
// No hace nada (false, nil) salvo que las TRES condiciones se cumplan a la vez:
// (1) target es una event_start habilitada, (2) ninguna OTRA event_start sigue
// viva sin ella, y (3) existe al menos una regla kind='fallback'/kind='keyword'
// habilitada hacia un flujo durable que dependería de esa red. Con cualquiera de
// las tres en contra, borrar no cambia nada que D-054.8 proteja.
func blocksLastLiveEventStart(ctx context.Context, checker DurableFlowChecker, tenantID, targetID string, rules []trigger.Rule) (bool, error) {
	// otherLiveEventStart era `otrasEventStartVivas` en el viejo.
	var target *trigger.Rule
	otherLiveEventStart := false
	for i := range rules {
		r := rules[i]
		if r.TriggerID == targetID {
			rc := r
			target = &rc
			continue
		}
		if r.Kind == trigger.KindEventStart && r.Enabled {
			otherLiveEventStart = true
		}
	}
	if target == nil || target.Kind != trigger.KindEventStart || !target.Enabled || otherLiveEventStart {
		return false, nil
	}
	for _, r := range rules {
		if !blocksDurableWithoutEventStart(r.Kind) || !r.Enabled {
			continue
		}
		durable, err := durableContent(ctx, checker, tenantID, r.FlowID)
		if err != nil {
			return false, err
		}
		if durable {
			return true, nil
		}
	}
	return false, nil
}

// DeleteTriggerHandler devuelve el handler de DELETE .../triggers/{id}: borra la regla
// {id} (r.PathValue("id")) del tenant del token (INV-8). No mira el método.
//
// Respuestas, en orden; la primera que falla responde y nada se borra:
//
//  1. 401 "autenticación requerida" (sin Identity o con TenantID vacío).
//  2. 400 "trigger id requerido en la ruta" si el id llega vacío.
//  3. 500 "no se pudieron listar las reglas de disparo": SIEMPRE se listan antes las
//     reglas del tenant, también para un id que no existe.
//  4. 422 (Plan 054 · T2.7, D-054.8, dirección ii, «la puerta de atrás») si se cumplen
//     las TRES a la vez: (a) {id} es una regla event_start habilitada del tenant;
//     (b) ninguna OTRA event_start del tenant sigue habilitada; (c) hay alguna regla
//     kind fallback o keyword HABILITADA cuyo flow_id es durable según el checker
//     (consultado con el tenant del token). Texto: "no se puede borrar: es la última
//     regla event_start habilitada del tenant, que tiene una regla kind='fallback' o
//     kind='keyword' hacia un flujo con contenido durable; borrarla dejaría sin
//     respuesta a los entrantes que caigan en esa regla (D-054.8) — deshabilita o borra
//     primero esa regla, o conserva/crea otra regla event_start". Si el checker falla
//     → 500 "no se pudo verificar el contenido durable del flujo". Con (a) o (b) en
//     contra el checker ni se consulta; con checker nil nunca se bloquea.
//  5. 404 "regla de disparo no encontrada" si Delete devuelve
//     trigger.ErrTriggerNotFound (errors.Is): el id no existe o es de OTRO tenant, sin
//     distinguirlos (REQ-D4).
//  6. 500 "no se pudo borrar la regla de disparo" ante otro error de Delete.
//
// Éxito: 204 sin cuerpo.
func DeleteTriggerHandler(store TriggerStore, checker DurableFlowChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}
		triggerID := r.PathValue("id")
		if triggerID == "" {
			http.Error(w, "trigger id requerido en la ruta", http.StatusBadRequest)
			return
		}

		// T2.7 dirección (ii): antes de borrar, comprobar si esta fila es la ÚLTIMA
		// event_start viva que sostiene una regla kind='fallback' o kind='keyword'
		// durable. rules ya está acotado al tenant del token (List, INV-8); un
		// triggerID de otro tenant simplemente no aparece en él, así que
		// blocksLastLiveEventStart no lo bloquea y el store.Delete de abajo sigue
		// siendo quien decide el 404 cross-tenant (REQ-D4) — sin cambio de
		// comportamiento en ese caso.
		rules, err := store.List(r.Context(), id.TenantID)
		if err != nil {
			http.Error(w, "no se pudieron listar las reglas de disparo", http.StatusInternalServerError)
			return
		}
		blocked, err := blocksLastLiveEventStart(r.Context(), checker, id.TenantID, triggerID, rules)
		if err != nil {
			http.Error(w, "no se pudo verificar el contenido durable del flujo", http.StatusInternalServerError)
			return
		}
		if blocked {
			http.Error(w, msg422LastEventStart, http.StatusUnprocessableEntity)
			return
		}

		err = store.Delete(r.Context(), id.TenantID, triggerID)
		switch {
		case errors.Is(err, trigger.ErrTriggerNotFound):
			http.Error(w, "regla de disparo no encontrada", http.StatusNotFound)
		case err != nil:
			http.Error(w, "no se pudo borrar la regla de disparo", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
