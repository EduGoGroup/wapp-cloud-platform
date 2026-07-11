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
	}
}

// ruleFromRequest valida el cuerpo (REQ-D5) y construye la Rule con el tenant del
// token. Devuelve un mensaje de error (no vacío) si el cuerpo es incoherente:
//   - kind ∉ {keyword,fallback,escape,llm}
//   - match_type ∉ {exact,contains} (vacío → default exact)
//   - keyword/escape/llm sin keyword
//   - keyword/fallback/llm sin flow_id
//   - kind=llm con keyword que no cumple el formato de NOMBRE de intención
//   - message presente en kind ≠ escape (el aviso solo aplica al escape, T4b)
func ruleFromRequest(tenantID string, req triggerRequest) (trigger.Rule, string) {
	kind := trigger.Kind(strings.TrimSpace(req.Kind))
	switch kind {
	case trigger.KindKeyword, trigger.KindFallback, trigger.KindEscape, trigger.KindLLM:
	default:
		return trigger.Rule{}, "kind inválido (usar keyword|fallback|escape|llm)"
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
	if msg := requiredFieldsByKind(kind, keyword, flowID); msg != "" {
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
	}, ""
}

// requiredFieldsByKind valida los campos obligatorios según el kind (extraído de
// ruleFromRequest para acotar su complejidad ciclomática): keyword para keyword|
// escape|llm; flow_id para keyword|fallback|llm; y, para llm, el keyword debe ser un
// nombre de intención válido (casa flow_triggers.keyword con el enum del clasificador).
// Devuelve "" si todo está bien.
func requiredFieldsByKind(kind trigger.Kind, keyword, flowID string) string {
	needsKeyword := kind == trigger.KindKeyword || kind == trigger.KindEscape || kind == trigger.KindLLM
	if needsKeyword && keyword == "" {
		return "keyword es requerido para kind keyword|escape|llm"
	}
	needsFlow := kind == trigger.KindKeyword || kind == trigger.KindFallback || kind == trigger.KindLLM
	if needsFlow && flowID == "" {
		return "flow_id es requerido para kind keyword|fallback|llm"
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
			out = append(out, dtoFromRule(rule))
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
