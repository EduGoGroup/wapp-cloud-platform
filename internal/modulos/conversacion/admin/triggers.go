// Porta internal/flujos/admin/triggers.go @ 724f3035

package admin

import (
	"context"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// TriggerStore es el subconjunto de trigger.Store que consumen los handlers del CRUD
// de reglas de disparo. Lo satisfacen *trigger.PostgresStore y *trigger.MemoryStore.
// TODAS las operaciones se acotan al tenant del token (INV-8).
type TriggerStore interface {
	Insert(ctx context.Context, r trigger.Rule) (trigger.Rule, error)
	List(ctx context.Context, tenantID string) ([]trigger.Rule, error)
	Delete(ctx context.Context, tenantID, triggerID string) error
}

// CreateTriggerHandler devuelve el handler de POST .../triggers: valida el cuerpo
// (REQ-D5) y persiste la regla para el tenant del token (INV-8). NO mira el método: lo
// acota el patrón de la ruta (se porta como está).
//
// Cuerpo: {kind, keyword, match_type, flow_id, priority, enabled, message, session_id,
// event_kind}. Los campos de texto se recortan de espacios; `match_type` vacío es
// `exact`; `enabled` omitido es true (un false explícito se respeta); `session_id`
// vacío es regla GLOBAL del tenant. Un `tenant_id` o un `trigger_id` del cuerpo se
// ignoran.
//
// Respuestas, en orden; la primera que falla responde y nada se persiste:
//
//  1. 401 "autenticación requerida" (sin Identity o con TenantID vacío).
//  2. 400 "cuerpo JSON inválido".
//  3. 400 con el PRIMERO de estos motivos, en este orden:
//     - "kind inválido (usar keyword|fallback|escape|llm|event_start|event_stop)";
//     - "match_type inválido (usar exact|contains)";
//     - "keyword es requerido para kind <kind>" (keyword, escape, llm, event_start,
//     event_stop);
//     - "flow_id es requerido para kind <kind>" (keyword, fallback, llm). event_start
//     NO lo exige (D-043.3), aunque lo admite;
//     - "event_kind es requerido para kind event_start (el tipo de evento que arranca:
//     menu|cart|survey|media)";
//     - "event_kind solo es válido para kind event_start o llm" (llm lo ADMITE sin
//     exigirlo, Plan 043 · T5.3);
//     - "event_kind inválido: los valores admitidos son menu|cart|survey|media"
//     (vocabulario CERRADO de trigger.FactoryEventKinds: un `carrrito` no entra);
//     - "keyword de kind llm debe ser un nombre de intención válido
//     (^[a-z][a-z0-9_]{1,63}$)" (Plan 029 · T7);
//     - "message solo es válido para kind escape" (Plan 019 · T4b).
//  4. Solo para kind keyword o fallback (los dos que pueden arrancar un flujo SIN
//     evento padre, D-054.5): se pregunta al checker por (tenant del token, flow_id).
//     Si falla → 500 "no se pudo verificar el contenido durable del flujo". Si el
//     flujo es durable se listan las reglas del tenant (el mismo 500 si el listado
//     falla) y, si NINGUNA es kind event_start habilitada → 422 (Plan 054 · T2.7,
//     D-054.8; cierra MD-054.2): "no se puede crear: el flujo de destino tiene
//     contenido durable (p. ej. carrito o encuesta) y el tenant no tiene ninguna regla
//     event_start habilitada; sin una, un entrante que caiga en esta regla
//     (kind='fallback' o kind='keyword') se quedaría sin respuesta (D-054.8) — crea
//     antes una regla event_start". Una event_start deshabilitada no cuenta, ni una
//     regla llm con event_kind. 400 = «el cuerpo no se entiende»; 422 = «se entiende
//     y aun así no se puede guardar». Los demás kinds NI consultan al checker, y con
//     checker nil ningún flujo es durable.
//  5. 500 "no se pudo crear la regla de disparo" si Insert falla.
//
// Éxito: 201 con la regla que devolvió el store: {trigger_id, kind, keyword,
// match_type, flow_id, priority, enabled, message, session_id, event_kind}; keyword,
// flow_id, message, session_id y event_kind se omiten si están vacíos. El 201 NO lleva
// las marcas derivadas del listado.
func CreateTriggerHandler(store TriggerStore, checker DurableFlowChecker) http.Handler {
	panic(pendiente.Implementar("admin.CreateTriggerHandler"))
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
	panic(pendiente.Implementar("admin.ListTriggersHandler"))
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
	panic(pendiente.Implementar("admin.DeleteTriggerHandler"))
}
