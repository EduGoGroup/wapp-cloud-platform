package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los tests de store.go y de sus trozos nacen VERDES: son declaraciones (tipos, constantes,
// centinelas e interfaces) y dos funciones sin estado. No llevan la etiqueta `pendiente`.

// runtimeFlowStore es la composición que pide el runtime (ISP, H12 · Plan 027 · Ola 2 · T9): solo
// lo que usa, no el Repository entero.
type runtimeFlowStore interface {
	store.ConversationStore
	store.DefinitionReader
	store.IntakeReader
	store.TenantSettingsReader
}

// Aserciones de compilación: los dos adaptadores satisfacen cada interfaz segregada de store.go,
// la composición entera y la que pide el runtime; y DefinitionStore incluye a DefinitionReader.
var (
	_ store.ConversationStore = (*store.MemoryRepository)(nil)
	_ store.DefinitionStore   = (*store.MemoryRepository)(nil)
	_ store.SurveyResultStore = (*store.MemoryRepository)(nil)
	_ store.FlowEventStore    = (*store.MemoryRepository)(nil)
	_ store.Repository        = (*store.MemoryRepository)(nil)
	_ store.Repository        = (*store.PostgresRepository)(nil)
	_ runtimeFlowStore        = (*store.MemoryRepository)(nil)
	_ runtimeFlowStore        = (*store.PostgresRepository)(nil)
	_ runtimeFlowStore        = store.Repository(nil)
	_ store.DefinitionReader  = store.DefinitionStore(nil)
)

// requireEqual afirma que got es want. Lo comparten los tests del paquete.
func requireEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, quería %v", what, got, want)
	}
}

// TestKey_String: la clave se representa como sus tres componentes separadas por «|», en el orden
// tenant, sesión, contacto. Es opaca y estable: sirve de índice de mapa fuera del paquete.
func TestKey_String(t *testing.T) {
	cases := []struct {
		name string
		key  store.Key
		want string
	}{
		{"standard key", store.Key{TenantID: "tenant-1", SessionID: "sess-1", ContactID: "contact-1"}, "tenant-1|sess-1|contact-1"},
		{"empty components", store.Key{}, "||"},
		{"only the session", store.Key{SessionID: "s"}, "|s|"},
		{"components are not escaped", store.Key{TenantID: "a|b", SessionID: "c", ContactID: "d"}, "a|b|c|d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.String(); got != tc.want {
				t.Errorf("Key.String() = %q, quería %q", got, tc.want)
			}
			if got := fmt.Sprint(tc.key); got != tc.want {
				t.Errorf("fmt.Sprint(Key) = %q, quería %q (Key es un fmt.Stringer)", got, tc.want)
			}
		})
	}
}

// TestErrDefinitionNotFound: el centinela tiene su texto literal y se reconoce con errors.Is a
// través de una envoltura.
func TestErrDefinitionNotFound(t *testing.T) {
	if got := store.ErrDefinitionNotFound.Error(); got != "definición de flujo no encontrada" {
		t.Errorf("texto = %q, quería %q", got, "definición de flujo no encontrada")
	}
	wrapped := fmt.Errorf("%w: tenant=%s flow=%s", store.ErrDefinitionNotFound, "t", "f")
	if !errors.Is(wrapped, store.ErrDefinitionNotFound) {
		t.Error("errors.Is no reconoce ErrDefinitionNotFound envuelto")
	}
	if errors.Is(wrapped, store.ErrTenantContentNotFound) {
		t.Error("ErrDefinitionNotFound se confunde con ErrTenantContentNotFound")
	}
}

// TestBaseTypes_CarryTheirFields: los tres tipos de datos de store.go llevan los campos que el
// puerto promete; su valor cero es utilizable.
func TestBaseTypes_CarryTheirFields(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	summary := store.FlowSummary{FlowID: "menu", Version: 2, CreatedAt: at}
	result := store.SurveyResult{TenantID: "t", ContactID: "c", FlowID: "menu", FlowVersion: 2,
		QuestionID: "q1", AnswerCode: "si", EventID: "e", CreatedAt: at}
	event := store.FlowEvent{TenantID: "t", ContactID: "c", FlowID: "menu", FlowVersion: 2,
		Kind: "persist", Name: "survey_answer", Payload: map[string]any{"answer": "si"}}

	if summary.FlowID != "menu" || summary.Version != 2 || !summary.CreatedAt.Equal(at) {
		t.Errorf("FlowSummary = %+v", summary)
	}
	if result.QuestionID != "q1" || result.AnswerCode != "si" || result.EventID != "e" || !result.CreatedAt.Equal(at) {
		t.Errorf("SurveyResult = %+v", result)
	}
	if event.Kind != "persist" || event.Name != "survey_answer" || event.Payload["answer"] != "si" {
		t.Errorf("FlowEvent = %+v", event)
	}
	if (store.FlowEvent{}).Payload != nil {
		t.Error("el FlowEvent cero trae un payload, quería nil (el repositorio lo guarda como {})")
	}
}
