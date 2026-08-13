package admin_test

// triggers_llm_eventkind_test.go — Plan 043 · T5.3, D-043.9. Ensancha el CRUD de
// reglas de disparo para que kind='llm' ADMITA event_kind (allowsEventKind) sin
// exigirlo, y fija que las demás clases lo SIGAN rechazando (el hueco de
// validación que se abriría si "ensanchar" hubiera significado "aflojar para
// todos").

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// TestCreateTrigger_LLM_EventKind_Admitido: una regla kind='llm' con event_kind
// puesto se crea (201) y el event_kind persiste tal cual.
func TestCreateTrigger_LLM_EventKind_Admitido(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store, nil)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"llm","keyword":"pedir_encuesta","flow_id":"encuesta","event_kind":"survey"}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Kind      string `json:"kind"`
		EventKind string `json:"event_kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("json inválido: %v", err)
	}
	if out.Kind != "llm" || out.EventKind != "survey" {
		t.Fatalf("respuesta inesperada: %+v", out)
	}

	rules, err := store.List(t.Context(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].Kind != trigger.KindLLM || rules[0].EventKind != "survey" {
		t.Fatalf("la regla llm con event_kind no persistió como se esperaba: %+v", rules)
	}
}

// TestCreateTrigger_LLM_SinEventKind_SigueValidando: una regla llm SIN event_kind
// (el caso de siempre, Plan 029 · T7) sigue creándose igual: allowsEventKind
// admite, no exige. Retrocompatibilidad byte a byte.
func TestCreateTrigger_LLM_SinEventKind_SigueValidando(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store, nil)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"llm","keyword":"pedir_catalogo","flow_id":"catalogo"}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(t.Context(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].EventKind != "" {
		t.Fatalf("una regla llm sin event_kind debe persistir con EventKind vacío: %+v", rules)
	}
}

// TestCreateTrigger_LLM_EventKind_VocabularioCerrado: el vocabulario cerrado de
// trigger.IsFactoryEventKind se aplica IGUAL a llm que a event_start: un
// event_kind inventado sigue siendo 400, aunque la clase que lo admite ya no sea
// solo event_start.
func TestCreateTrigger_LLM_EventKind_VocabularioCerrado(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store, nil)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"llm","keyword":"pedir_pizza","flow_id":"pizza","event_kind":"pizza"}`, true)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "los valores admitidos son") {
		t.Fatalf("el error debe explicar el vocabulario cerrado, got %q", rec.Body.String())
	}
}

// TestCreateTrigger_400_EventKind_OtrasClasesSiguenRechazando: el ensanchamiento
// de D-043.9 es SOLO para llm (allowsEventKind). keyword, fallback y escape NO lo
// admiten y deben seguir devolviendo 400 con el mismo mensaje que antes de esta
// tarea — si esto se pone verde sin la guarda, ensanchar el CRUD para llm se
// habría convertido en un agujero de validación para todas las clases.
func TestCreateTrigger_400_EventKind_OtrasClasesSiguenRechazando(t *testing.T) {
	store := trigger.NewMemoryStore()
	h := admin.CreateTriggerHandler(store, nil)
	cases := map[string]string{
		"keyword":  `{"kind":"keyword","keyword":"x","flow_id":"f","event_kind":"cart"}`,
		"fallback": `{"kind":"fallback","flow_id":"f","event_kind":"cart"}`,
		"escape":   `{"kind":"escape","keyword":"salir","event_kind":"cart"}`,
	}
	for name, body := range cases {
		rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant, body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "event_kind solo es válido") {
			t.Fatalf("%s: mensaje inesperado: %q", name, rec.Body.String())
		}
	}
}
