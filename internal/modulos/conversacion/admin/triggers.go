// Porta internal/flujos/admin/triggers.go @ 724f3035
//
// El alta y lo que comparte el CRUD de reglas de disparo. El listado sigue en
// triggers_list.go y la baja en triggers_delete.go (mismo origen, partido por tema,
// E-13).

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intentNameRe es el formato del NOMBRE de intención de una regla kind='llm' (Plan
// 029 · T7): el MISMO contrato que valida wapp-shared/intents para los nombres del
// catálogo (van a flow_triggers.keyword y al enum del schema del clasificador). Un
// keyword de una regla llm que no lo cumpla no podría casar jamás una intención real.
var intentNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// validEventKinds es la lista CERRADA de tipos de evento que acepta el CRUD, en el
// orden estable de trigger.FactoryEventKinds(). Se materializa una vez para que el
// mensaje de error enseñe siempre los mismos valores en el mismo orden.
//
// El cierre es deliberado y sustituye a una validación de FORMA que aceptaba
// cualquier identificador en minúsculas: con ella, un `carrrito` mal escrito entraba
// con un 201 y viajaba hasta una fila de conversation_events para parir un evento que
// ningún módulo atiende, sin error en ningún punto. El argumento de la migración 0052
// —que enchufar un módulo no cueste una migración— queda intacto: ampliar una lista
// en Go tampoco cuesta una migración.
var validEventKinds = strings.Join(trigger.FactoryEventKinds(), "|")

// kindSpec declara qué campos exige cada kind. Es la ÚNICA lista de kinds válidos del
// CRUD: dar de alta uno es añadir una fila aquí, no un case nuevo en varios switches
// (así fue como el kind y sus campos obligatorios pudieron divergir hasta ahora).
type kindSpec struct {
	needsKeyword bool
	needsFlowID  bool
	// needsEventKind hace doble trabajo: marca event_kind como OBLIGATORIO y, por
	// negación, como PROHIBIDO en todos los demás kinds (una regla que no pare eventos
	// deja la columna NULL, que es el caso de siempre).
	needsEventKind bool
	// allowsEventKind ADMITE event_kind sin exigirlo (Plan 043 · T5.3, D-043.9). Solo
	// lo lleva `llm`, y significa DOS cosas a la vez, en SECUENCIA sobre el mismo
	// campo (Plan 054 · F2b, D-A — decisión de Jhoan 2026-08-12, sustituye a
	// CONTRATO-OLA5 D1): primero acota el scoping —«esta intención pertenece a este
	// tipo de evento», para que el resolver la descarte cuando el activo es otro—
	// y, ya elegida la regla, SÍ convierte el disparo en una puerta de nacimiento:
	// una regla llm que casa con event_kind poblado devuelve Action=StartEvent
	// (ver config_resolver.go). Sin event_kind, byte a byte como siempre
	// (Action=Start, sin evento).
	allowsEventKind bool
}

// kindSpecs mapea cada kind válido a sus campos obligatorios. event_start NO exige
// flow_id a propósito (D-043.3): el despachador del menú es un componente del runtime,
// no una fila de flow_definitions, así que un event_start de event_kind='menu' no tiene
// flujo al que apuntar; cart/survey sí pueden traerlo y se respeta. event_stop no lleva
// event_kind porque corta el evento ACTIVO, sea del tipo que sea (D-043.2). llm ADMITE
// (no exige) event_kind desde Plan 043 · T5.3/D-043.9: acota el scoping por evento
// activo Y, desde el Plan 054 · F2b, pare el evento cuando la regla ganadora lo trae
// (config_resolver.go) — las dos lecturas del mismo campo, no dos campos en pugna.
var kindSpecs = map[trigger.Kind]kindSpec{
	trigger.KindKeyword:    {needsKeyword: true, needsFlowID: true},
	trigger.KindFallback:   {needsFlowID: true},
	trigger.KindEscape:     {needsKeyword: true},
	trigger.KindLLM:        {needsKeyword: true, needsFlowID: true, allowsEventKind: true},
	trigger.KindEventStart: {needsKeyword: true, needsEventKind: true},
	trigger.KindEventStop:  {needsKeyword: true},
}

// validKinds es la lista de kinds que se le enseña al llamante en el 400. Se mantiene a
// mano (y no se deriva de kindSpecs) porque el recorrido de un map no tiene orden y un
// mensaje de error que cambia de forma entre peticiones es un mal mensaje de error.
const validKinds = "keyword|fallback|escape|llm|event_start|event_stop"

// TriggerStore es el subconjunto de trigger.Store que consumen los handlers del CRUD
// de reglas de disparo. Lo satisfacen *trigger.PostgresStore y *trigger.MemoryStore.
// TODAS las operaciones se acotan al tenant del token (INV-8).
type TriggerStore interface {
	Insert(ctx context.Context, r trigger.Rule) (trigger.Rule, error)
	List(ctx context.Context, tenantID string) ([]trigger.Rule, error)
	Delete(ctx context.Context, tenantID, triggerID string) error
}

// triggerRequest es el cuerpo JSON de POST .../triggers. El tenant_id NO viaja
// aquí (INV-8): sale del token. enabled es *bool para distinguir "omitido"
// (default true, como la columna) de un false explícito.
type triggerRequest struct {
	Kind      string `json:"kind"`
	Keyword   string `json:"keyword"`
	MatchType string `json:"match_type"`
	FlowID    string `json:"flow_id"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
	// Message es el aviso de escape configurable (Plan 019 · T4b). Solo válido para
	// kind=escape; si llega en keyword/fallback el cuerpo se rechaza (400).
	Message string `json:"message"`
	// SessionID acota la regla a una sesión concreta (Plan 020 · T4). Opcional; si se
	// omite (o vacío) la regla es GLOBAL del tenant (aplica a todas las sesiones).
	SessionID string `json:"session_id"`
	// EventKind es el TIPO de evento conversacional. Para kind=event_start es el que
	// arranca o conmuta la regla (Plan 043 · D-043.2), OBLIGATORIO. Para kind=llm es
	// OPCIONAL y significa el tipo de evento al que pertenece la intención (Plan 043 ·
	// T5.3, D-043.9): acota el scoping en config_resolver.go y, si la regla gana,
	// PARE ese evento (Plan 054 · F2b, D-A) — sin event_kind la regla llm sigue sin
	// arrancar nada por sí sola (Action=Start, sin evento). En cualquier otro kind el
	// cuerpo se rechaza (400).
	EventKind string `json:"event_kind"`
}

// triggerDTO es la proyección pública de una regla (respuesta de create/list).
// keyword/flow_id se omiten cuando están vacíos (fallback no tiene keyword; escape
// no tiene flow_id).
type triggerDTO struct {
	TriggerID string `json:"trigger_id"`
	Kind      string `json:"kind"`
	Keyword   string `json:"keyword,omitempty"`
	MatchType string `json:"match_type"`
	FlowID    string `json:"flow_id,omitempty"`
	Priority  int    `json:"priority"`
	Enabled   bool   `json:"enabled"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	EventKind string `json:"event_kind,omitempty"`
	// ShadowedByEventList es una marca DERIVADA (no se persiste, no hay columna): avisa
	// al dueño de que esta regla kind='fallback' ya NO se emite en la conversación sin
	// evento elegido, porque ahí manda la lista que ofrece (Plan 043 · D-043.20,
	// REQ-27b, MD-043.11). No dice que la regla esté apagada: fuera de ese punto —con
	// decisión Start, con conversación viva, y en el caso vacío en que la lista queda
	// sin una sola opción— el fallback conserva EXACTAMENTE su comportamiento del Plan
	// 019. Se omite cuando es false para no ensuciar la respuesta de los demás kinds.
	ShadowedByEventList bool `json:"shadowed_by_event_list,omitempty"`
	// FlowNeedsEvent es una marca DERIVADA hermana de ShadowedByEventList (Plan 054 ·
	// D-054.6, T2.6): avisa de que esta regla kind='keyword'/'fallback' apunta a un
	// flow_id cuya definición VIGENTE tiene algún nodo con contenido durable
	// (engine.FlowProducesDurableContent, F1). Esas dos son las ÚNICAS puertas que
	// pueden intentar arrancar ese flujo SIN evento padre (event_start y la intención
	// llm con event_kind SIEMPRE traen uno, D-054.5): si esta regla dispara,
	// startLocked la rechaza con ErrDurableFlowNeedsEvent y el runtime DEGRADA a la
	// oferta del despachador (D-054.3(b)) en vez de arrancarla directo. No dice que la
	// regla esté rota — el contacto sigue atendido por la oferta, salvo que ADEMÁS
	// incumpla D-054.8 (T2.7), combinación que el CRUD ya rechaza al guardarla.
	//
	// A diferencia de ShadowedByEventList —una comparación PURA de r.Kind, sin ctx ni
	// store (H3 del contrato de entrada de este frente)— esta marca necesita RESOLVER
	// la definición del flujo, así que no se puede calcar el patrón: listDTO gana ctx
	// y un DurableFlowChecker. Se omite cuando es false.
	FlowNeedsEvent bool `json:"flow_needs_event,omitempty"`
}

// dtoFromRule proyecta una trigger.Rule al DTO de respuesta.
func dtoFromRule(r trigger.Rule) triggerDTO {
	return triggerDTO{
		TriggerID: r.TriggerID,
		Kind:      string(r.Kind),
		Keyword:   r.Keyword,
		MatchType: string(r.MatchType),
		FlowID:    r.FlowID,
		Priority:  r.Priority,
		Enabled:   r.Enabled,
		Message:   r.Message,
		SessionID: r.SessionID,
		EventKind: r.EventKind,
	}
}

// ruleFromRequest valida el cuerpo (REQ-D5) y construye la Rule con el tenant del
// token. Devuelve un mensaje de error (no vacío) si el cuerpo es incoherente:
//   - kind ∉ kindSpecs (hoy keyword|fallback|escape|llm|event_start|event_stop)
//   - match_type ∉ {exact,contains} (vacío → default exact)
//   - falta un campo que el kind exige (keyword / flow_id / event_kind, ver kindSpecs)
//   - kind=llm con keyword que no cumple el formato de NOMBRE de intención
//   - event_kind presente en kind ∉ {event_start, llm} (Plan 043 · T5.3: llm lo
//     ADMITE para el scoping por evento activo, pero no lo exige)
//   - message presente en kind ≠ escape (el aviso solo aplica al escape, T4b)
func ruleFromRequest(tenantID string, req triggerRequest) (trigger.Rule, string) {
	kind := trigger.Kind(strings.TrimSpace(req.Kind))
	if _, known := kindSpecs[kind]; !known {
		return trigger.Rule{}, "kind inválido (usar " + validKinds + ")"
	}

	matchType := trigger.MatchExact
	if mt := strings.TrimSpace(req.MatchType); mt != "" {
		matchType = trigger.MatchType(mt)
		switch matchType {
		case trigger.MatchExact, trigger.MatchContains:
		default:
			return trigger.Rule{}, "match_type inválido (usar exact|contains)"
		}
	}

	keyword := strings.TrimSpace(req.Keyword)
	flowID := strings.TrimSpace(req.FlowID)
	eventKind := strings.TrimSpace(req.EventKind)
	if msg := requiredFieldsByKind(kind, keyword, flowID, eventKind); msg != "" {
		return trigger.Rule{}, msg
	}

	message := strings.TrimSpace(req.Message)
	if message != "" && kind != trigger.KindEscape {
		return trigger.Rule{}, "message solo es válido para kind escape"
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	return trigger.Rule{
		TenantID:  tenantID,
		Kind:      kind,
		Keyword:   keyword,
		MatchType: matchType,
		FlowID:    flowID,
		Priority:  req.Priority,
		Enabled:   enabled,
		Message:   message,
		SessionID: strings.TrimSpace(req.SessionID),
		EventKind: eventKind,
	}, ""
}

// requiredFieldsByKind valida los campos según el kind (extraído de ruleFromRequest
// para acotar su complejidad ciclomática) leyendo kindSpecs: qué campos exige y, en el
// caso de event_kind, en qué kinds está además PROHIBIDO. Para llm, el keyword debe ser
// un nombre de intención válido (casa flow_triggers.keyword con el enum del
// clasificador). El llamante ya comprobó que el kind existe. Devuelve "" si todo está bien.
func requiredFieldsByKind(kind trigger.Kind, keyword, flowID, eventKind string) string {
	spec := kindSpecs[kind]
	if spec.needsKeyword && keyword == "" {
		return "keyword es requerido para kind " + string(kind)
	}
	if spec.needsFlowID && flowID == "" {
		return "flow_id es requerido para kind " + string(kind)
	}
	if spec.needsEventKind && eventKind == "" {
		return "event_kind es requerido para kind event_start (el tipo de evento que arranca: " + validEventKinds + ")"
	}
	if eventKind != "" && !spec.needsEventKind && !spec.allowsEventKind {
		return "event_kind solo es válido para kind event_start o llm"
	}
	if eventKind != "" && !trigger.IsFactoryEventKind(eventKind) {
		return "event_kind inválido: los valores admitidos son " + validEventKinds
	}
	if kind == trigger.KindLLM && !intentNameRe.MatchString(keyword) {
		return "keyword de kind llm debe ser un nombre de intención válido (^[a-z][a-z0-9_]{1,63}$)"
	}
	return ""
}

// durableContent es el único punto que invoca checker: nil-safe (fail-open, ver el
// docstring de DurableFlowChecker) para que un llamante que no ejercita contenido
// durable (producción con el adapter siempre cableado; tests que pasan nil a
// propósito) no tenga que ocuparse de un caso especial.
//
// En el viejo vivía en durable_flow.go; aquí nace junto a quienes lo llaman —las dos
// direcciones de T2.7 y la marca del listado—, porque en durable_flow.go nadie lo usa.
func durableContent(ctx context.Context, checker DurableFlowChecker, tenantID, flowID string) (bool, error) {
	if checker == nil {
		return false, nil
	}
	return checker.FlowHasDurableContent(ctx, tenantID, flowID)
}

// tenantHasLiveEventStart reporta si rules trae alguna regla kind='event_start'
// HABILITADA. Es el predicado exacto de REQ-054.5/D-054.8, y su alcance está
// verificado contra el código, no supuesto:
//
//   - Es la MISMA fuente que alimenta la oferta del despachador
//     (events.TriggerKindOffer.OfferedKinds, internal/modulos/conversacion/events/kinds.go:46-63)
//     — lee EXCLUSIVAMENTE reglas kind='event_start' habilitadas. openWithOffer /
//     buildOpeningOffer (internal/modulos/conversacion/runtime/incoming.go:506-553) son el ÚNICO
//     mecanismo de rescate del corner MD-054.2 (fallback → oferta vacía → flujo
//     durable rechazado → sin dónde degradar), y ese mecanismo no consulta nada
//     más: por eso este predicado tampoco.
//   - La QUINTA puerta del nacimiento (Plan 054 · F2b, T2.8: una regla kind='llm'
//     con event_kind, config_resolver.go:79-111) TAMBIÉN produce Action=StartEvent,
//     pero NO se cuenta aquí a propósito: solo se sirve cuando una intención YA
//     CLASIFICADA casa esa regla, ANTES de que el resolver llegue siquiera a
//     evaluar Fallback (config_resolver.go, Resolve, pasos 1→2→3) — nunca participa
//     en la construcción de la oferta (kinds.go no la consulta) y por tanto nunca
//     rescata al entrante que NO se clasificó o no casó ninguna regla llm, que es
//     EXACTAMENTE el que cae a Fallback y dispara este corner
//     (incoming.go:679-682 ya lo deja escrito: «un tenant con un fallback/keyword
//     durable y sin ningún event_start vivo se queda SIN RESPUESTA»). Contar la
//     llm aquí no cerraría más el agujero: lo REABRIRÍA para un tenant configurado
//     solo con una regla llm y cero event_start, que seguiría mudo ante cualquier
//     entrante no clasificado.
func tenantHasLiveEventStart(rules []trigger.Rule) bool {
	for _, r := range rules {
		if r.Kind == trigger.KindEventStart && r.Enabled {
			return true
		}
	}
	return false
}

// blocksDurableWithoutEventStart es la MISMA lista de kinds que pueden intentar
// arrancar un flujo SIN evento padre (D-054.5): keyword y fallback. event_start y
// la intención llm con event_kind SIEMPRE traen uno (ver el docstring de
// FlowNeedsEvent, arriba en este fichero), así que ninguno de los dos participa
// aquí. Un solo lugar para esta lista: blocksStartWithoutEventStart (alta,
// dirección i) y blocksLastLiveEventStart (baja, dirección ii) la comparten en vez
// de repetir el mismo par de comparaciones cada una por su lado — la primera
// versión de T2.7 solo miraba KindFallback en los dos sitios a la vez y por eso
// dejó kind='keyword' sin cubrir en NINGUNO de los dos: no había una lista común
// que generalizar, había dos copias del mismo `==` que había que encontrar y
// cambiar a la vez. Con esta función, extender la validación a un kind nuevo el
// día de mañana es tocar un solo sitio.
func blocksDurableWithoutEventStart(k trigger.Kind) bool {
	return k == trigger.KindFallback || k == trigger.KindKeyword
}

// msg422DurableWithoutNet (`msg422DurableSinRed` en el viejo) es el motivo del 422 de
// la dirección (i) de T2.7 (alta de un kind='keyword'/'fallback' durable sin red de
// event_start). msg422LastEventStart (`msg422UltimaEventStart` en el viejo) es el de la
// dirección (ii) (baja de la última event_start que sostiene esa red).
// Se nombran como constantes para que los dos handlers y sus tests citen el MISMO
// texto.
const (
	msg422DurableWithoutNet = "no se puede crear: el flujo de destino tiene contenido durable (p. ej. carrito o encuesta) " +
		"y el tenant no tiene ninguna regla event_start habilitada; sin una, un entrante que caiga en esta regla " +
		"(kind='fallback' o kind='keyword') se quedaría sin respuesta (D-054.8) — crea antes una regla event_start"
	msg422LastEventStart = "no se puede borrar: es la última regla event_start habilitada del tenant, que tiene una " +
		"regla kind='fallback' o kind='keyword' hacia un flujo con contenido durable; borrarla dejaría sin respuesta a " +
		"los entrantes que caigan en esa regla (D-054.8) — deshabilita o borra primero esa regla, o conserva/crea otra " +
		"regla event_start"
)

// blocksStartWithoutEventStart implementa la dirección (i) de T2.7 (D-054.8,
// cierra MD-054.2): un alta kind='fallback' o kind='keyword' hacia un flujo con
// contenido durable, sin ninguna regla event_start habilitada en el tenant, se
// rechaza en tiempo de CONFIGURACIÓN — es el mismo corner que degradeDurableStart
// deja anotado y sin resolver en tiempo de EJECUCIÓN (runtime/incoming.go:679-682).
// El hueco gemelo (T2.7 solo miraba kind='fallback', dejando kind='keyword' con el
// mismo silencio por la puerta de al lado) se cierra generalizando esta misma
// función en vez de duplicarla.
//
// Solo se evalúa para blocksDurableWithoutEventStart(rule.Kind) (el llamante ya lo
// comprobó); esta función solo decide SI bloquea y con qué motivo.
func blocksStartWithoutEventStart(ctx context.Context, triggers TriggerStore, checker DurableFlowChecker, tenantID, flowID string) (bool, error) {
	durable, err := durableContent(ctx, checker, tenantID, flowID)
	if err != nil {
		return false, err
	}
	if !durable {
		return false, nil
	}
	existing, err := triggers.List(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return !tenantHasLiveEventStart(existing), nil
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			http.Error(w, "autenticación requerida", http.StatusUnauthorized)
			return
		}

		var req triggerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}

		rule, msg := ruleFromRequest(id.TenantID, req)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		if blocksDurableWithoutEventStart(rule.Kind) {
			blocked, err := blocksStartWithoutEventStart(r.Context(), store, checker, id.TenantID, rule.FlowID)
			if err != nil {
				http.Error(w, "no se pudo verificar el contenido durable del flujo", http.StatusInternalServerError)
				return
			}
			if blocked {
				http.Error(w, msg422DurableWithoutNet, http.StatusUnprocessableEntity)
				return
			}
		}

		created, err := store.Insert(r.Context(), rule)
		if err != nil {
			http.Error(w, "no se pudo crear la regla de disparo", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, dtoFromRule(created))
	})
}
