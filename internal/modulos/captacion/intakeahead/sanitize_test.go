package intakeahead

// sanitize_test.go — LA REGLA DEL SANEO, sobre la función que la aplica. El contrato de
// Request ya recorre desde fuera sus dos desenlaces (la evidencia tumba, los params se
// cuentan: intakeahead_evidence_test.go); lo que desde allí NO se ve es QUÉ params
// sobreviven, porque los params no salen del paquete. Eso es lo único que se fija aquí.

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// sanitizeInput es la entrada del prompt de los casos: `consulta` declara `tema` y
// `lugar`; `intake_request` no declara ninguno (D-044.20).
func sanitizeInput() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text: "A qué HORA abren   en Chacao",
		Catalog: []llm.IntentSpec{
			{Name: "intake_request", Params: []string{}},
			{Name: "consulta", Params: []string{"tema", "lugar"}},
		},
		UnknownLabel: "desconocido",
	}
}

// TestSanitize_KeepsOnlyWhatTheClientWrote: la clasificación se modifica IN SITU —lo que
// sale de aquí es lo único que el resto del pipeline puede ver— y el número devuelto es
// el de los params que se cayeron.
func TestSanitize_KeepsOnlyWhatTheClientWrote(t *testing.T) {
	cases := []struct {
		name         string
		intent       string
		evidence     string
		params       map[string]string
		wantParams   map[string]string
		wantDropped  int
		wantEvidence bool
	}{
		{"declared values in the text survive", "consulta", "a qué hora abren",
			map[string]string{"tema": "hora", "lugar": "CHACAO"},
			map[string]string{"tema": "hora", "lugar": "CHACAO"}, 0, true},
		{"declared value not in the text falls alone", "consulta", "a qué hora abren",
			map[string]string{"tema": "hora", "lugar": "Altamira"},
			map[string]string{"tema": "hora"}, 1, true},
		{"empty value means not said and stays", "consulta", "a qué hora abren",
			map[string]string{"tema": "", "lugar": "chacao"},
			map[string]string{"tema": "", "lugar": "chacao"}, 0, true},
		{"undeclared key falls even with its value in the text", "consulta", "a qué hora abren",
			map[string]string{"tema": "hora", "ciudad": "Chacao"},
			map[string]string{"tema": "hora"}, 1, true},
		{"undeclared key falls even when empty", "consulta", "a qué hora abren",
			map[string]string{"ciudad": ""}, map[string]string{}, 1, true},
		{"an intent that declares none keeps none", "intake_request", "hora abren",
			map[string]string{"tema": "hora", "lugar": "chacao"}, map[string]string{}, 2, true},
		{"the unknown label declares none", "desconocido", "hora",
			map[string]string{"tema": "hora"}, map[string]string{}, 1, true},
		{"params are sanitized even when the evidence is invented", "consulta", "a qué hora cierran",
			map[string]string{"tema": "hora", "lugar": "Altamira"},
			map[string]string{"tema": "hora"}, 1, false},
		{"empty evidence does not hold", "desconocido", "", nil, nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &llm.Classification{Intent: tc.intent, Evidence: tc.evidence, Params: tc.params}

			evidenceOK, dropped := sanitize(c, sanitizeInput())

			if evidenceOK != tc.wantEvidence || dropped != tc.wantDropped {
				t.Errorf("sanitize = (%v, %d), quiero (%v, %d)", evidenceOK, dropped, tc.wantEvidence, tc.wantDropped)
			}
			if !reflect.DeepEqual(c.Params, tc.wantParams) {
				t.Errorf("params tras el saneo = %v, quiero %v", c.Params, tc.wantParams)
			}
		})
	}
}

// TestSanitize_NilClassification: sin clasificación no hay evidencia que se sostenga ni
// params que contar.
func TestSanitize_NilClassification(t *testing.T) {
	if evidenceOK, dropped := sanitize(nil, sanitizeInput()); evidenceOK || dropped != 0 {
		t.Errorf("sanitize(nil) = (%v, %d), quiero (false, 0)", evidenceOK, dropped)
	}
}
