package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
)

// TestLogSink_Handle_NoPersisteNiPanica comprueba que el sink DEFAULT (log-only)
// no recibe store, no persiste y no panica ante un efecto con payload de negocio.
func TestLogSink_Handle_NoPersisteNiPanica(t *testing.T) {
	sink := runtime.NewLogSink(discardLogger())
	ec := runtime.EffectContext{TenantID: testTenant, ContactID: "c-opaco", FlowID: testFlow, FlowVersion: 1}
	eff := modules.Effect{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer_code": "a"}}

	if err := sink.Handle(context.Background(), ec, eff); err != nil {
		t.Fatalf("LogSink.Handle no debería fallar: %v", err)
	}
}
