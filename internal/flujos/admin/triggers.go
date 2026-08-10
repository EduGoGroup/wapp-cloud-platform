package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
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
	// lo lleva `llm`, y significa una cosa muy acotada: «esta intención pertenece a
	// este tipo de evento», para que el resolver la descarte cuando el activo es otro.
	// NO convierte la regla en una puerta de nacimiento: una regla llm sigue
	// devolviendo Action=Start SIN EventKind (ver config_resolver.go).
	allowsEventKind bool
}

// kindSpecs mapea cada kind válido a sus campos obligatorios. event_start NO exige
// flow_id a propósito (D-043.3): el despachador del menú es un componente del runtime,
// no una fila de flow_definitions, así que un event_start de event_kind='menu' no tiene
// flujo al que apuntar; cart/survey sí pueden traerlo y se respeta. event_stop no lleva
// event_kind porque corta el evento ACTIVO, sea del tipo que sea (D-043.2). llm ADMITE
// (no exige) event_kind desde Plan 043 · T5.3/D-043.9: acota el scoping por evento
// activo (config_resolver.go), no crea una puerta de nacimiento nueva.
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

// TriggerStore es el subconjunto de trigger.Store que consumen los handlers CRUD
// de reglas de disparo. Lo satisface *trigger.PostgresStore y *trigger.MemoryStore.
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
	// T5.3, D-043.9): acota el scoping en config_resolver.go, no arranca nada por sí
	// solo. En cualquier otro kind el cuerpo se rechaza (400).
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

// listDTO proyecta una regla para la RESPUESTA DEL LISTADO, que es la única que lleva
// la marca derivada shadowed_by_event_list (MD-043.11): el listado es donde el dueño
// mira su configuración, mientras que la respuesta 201 de un alta describe lo que
// acaba de escribir y no es sitio para avisar de nada.
//
// La marca se calcula por kind y no consulta estado alguno: la sombra la proyecta la
// lista de tipos sobre TODA regla de fallback, y si ese tenant acaba en el caso vacío
// —sin ningún event_kind habilitado y sin nada rescatable— el turno vuelve a su
// fallback (D-043.20). Por eso la marca dice «te ensombrece», no «estás muerta».
func listDTO(r trigger.Rule) triggerDTO {
	dto := dtoFromRule(r)
	dto.ShadowedByEventList = r.Kind == trigger.KindFallback
	return dto
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

// CreateTriggerHandler devuelve el handler de POST .../triggers: decodifica el
// cuerpo, toma el tenant del token (INV-8), valida la coherencia (REQ-D5) y
// persiste la regla. Respuestas:
//
//   - 201 con la regla creada ({trigger_id, kind, …}).
//   - 400 si el JSON es inválido o el cuerpo es incoherente.
//   - 401 sin Identity en el contexto; 500 ante fallo de persistencia.
func CreateTriggerHandler(store TriggerStore) http.Handler {
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

		created, err := store.Insert(r.Context(), rule)
		if err != nil {
			http.Error(w, "no se pudo crear la regla de disparo", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, dtoFromRule(created))
	})
}

// ListTriggersHandler devuelve el handler de GET .../triggers: lista las reglas
// del tenant del token (INV-8). 200 con el arreglo (vacío si no hay); 401 sin
// Identity; 500 ante fallo del store.
//
// Cada regla kind='fallback' sale marcada con shadowed_by_event_list (listDTO): es el
// aviso al dueño de que su fallback ya no se emite en la conversación sin evento
// elegido (D-043.20 / REQ-27b). Es DERIVADO —cero DDL, cero estado— y lo pinta quien
// consuma la API cuando exista una UI de administración (MD-043.11).
func ListTriggersHandler(store TriggerStore) http.Handler {
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
			out = append(out, listDTO(rule))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// DeleteTriggerHandler devuelve el handler de DELETE .../triggers/{id}: borra la
// regla {id} del tenant del token (INV-8). Respuestas:
//
//   - 204 al borrar.
//   - 404 si el id no existe o pertenece a otro tenant (no se filtra existencia, REQ-D4).
//   - 400 si falta el id en la ruta; 401 sin Identity; 500 ante otro fallo.
func DeleteTriggerHandler(store TriggerStore) http.Handler {
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
		err := store.Delete(r.Context(), id.TenantID, triggerID)
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
