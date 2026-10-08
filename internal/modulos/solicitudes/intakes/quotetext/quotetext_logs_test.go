//go:build pendiente

package quotetext_test

// quotetext_logs_test.go — parte de quotetext_test.go: lo que Suggest deja en el log,
// y lo que NUNCA deja (el texto que redactó el modelo).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// TestSuggest_HeaderTotalMismatchIsLoggedAndTheSumWins: manda la suma de las líneas,
// que es lo que el cliente puede comprobar a mano, y la discrepancia se dice.
func TestSuggest_HeaderTotalMismatchIsLoggedAndTheSumWins(t *testing.T) {
	const warning = "quotetext: el total de la cabecera no es la suma de las líneas; manda la suma"
	for name, c := range map[string]struct {
		header float64
		warns  bool
	}{
		"mismatch":           {9999, true},
		"match":              {fusionTotal, false},
		"within half a cent": {fusionTotal + 0.004, false},
		"beyond half a cent": {fusionTotal + 0.006, true},
	} {
		t.Run(name, func(t *testing.T) {
			store := intakes.NewMemoryStore()
			store.Add(testTenant, intakes.Intake{
				ID: testIntake, Status: intakes.StatusPendingApproval, Total: c.header,
			}, fusionItems...)
			scene := newSceneOn(t, store, store, nil)

			out := scene.suggest(t)

			if !strings.Contains(out.Text, "Total: $5540\n") {
				t.Errorf("el total del texto no es la suma de las líneas:\n%s", out.Text)
			}
			logs := scene.logs.String()
			if got := strings.Contains(logs, warning); got != c.warns {
				t.Fatalf("aviso presente = %v; se esperaba %v. Log:\n%s", got, c.warns, logs)
			}
			if c.warns && !containsAll(logs, "tenant_id="+testTenant, "intake_id="+testIntake,
				"total_cabecera=", "suma_lineas=5540") {
				t.Errorf("al aviso le faltan claves:\n%s", logs)
			}
		})
	}
}

// containsAll dice si el texto contiene todos los fragmentos.
func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

// TestSuggest_LogsNeverQuoteTheModelText: cada caída deja un aviso con la solicitud y
// el motivo, y NINGUNO cita lo que redactó el modelo (INV-6).
func TestSuggest_LogsNeverQuoteTheModelText(t *testing.T) {
	responses := map[string]json.RawMessage{
		quotetext.ReasonForeignAmount:    p5Artifact(t, "ZAFIRO "+strings.Replace(modelText, "$2950", "$3000", 1)),
		quotetext.ReasonUnreadableOutput: json.RawMessage(`{"version":1,"texto":"ZAFIRO $2100"}`),
		quotetext.ReasonUnreadableText:   p5Artifact(t, "ZAFIRO\x00 $2100"),
	}
	for reason, response := range responses {
		t.Run(reason, func(t *testing.T) {
			scene := newScene(t, response).withSampleHistory(t)

			wantDeterministic(t, scene.suggest(t), reason)

			logs := scene.logs.String()
			if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "intake_id="+testIntake) {
				t.Errorf("la caída no dejó un aviso con la solicitud:\n%s", logs)
			}
			if strings.Contains(logs, "ZAFIRO") {
				t.Errorf("el log cita el texto del modelo:\n%s", logs)
			}
		})
	}
}

func TestSuggest_VerifierRejectionIsLoggedWithItsReason(t *testing.T) {
	scene := newScene(t, p5Artifact(t, swappedText)).withSampleHistory(t)

	wantDeterministic(t, scene.suggest(t), quotetext.ReasonAmountsOutOfPlace)

	if logs := scene.logs.String(); !containsAll(logs,
		"quotetext: el texto del modelo NO cuadra con las líneas (INV-2); sale el texto determinista",
		"motivo="+quotetext.ReasonAmountsOutOfPlace, "detalle=") {
		t.Errorf("el rechazo del verificador no se avisó con su motivo y su detalle:\n%s", logs)
	}
}
