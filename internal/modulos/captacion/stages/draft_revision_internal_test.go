package stages

// draft_revision_internal_test.go — LA PREGUNTA DE UNA LÍNEA, sobre la función que la
// decide. El contrato recorre desde fuera las dos preguntas y sus textos
// (TestDraftRun_SuggestedQuestions); lo que desde allí no se distingue es la PRECEDENCIA:
// el envío se resuelve entero por su precio y nunca llega a la rama de la variante. Un
// envío con `variant_options` no lo produce hoy el match, y por eso el caso se fija aquí,
// sobre el auxiliar, y no con un artefacto inventado desde fuera.

import (
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// TestLineQuestion_ShippingNeverAsksForAVariant: la pregunta por la variante es de «una
// línea que no sea el envío». Con precio, el envío no pregunta nada; sin precio, pregunta
// la zona — tenga o no presentaciones y rango.
func TestLineQuestion_ShippingNeverAsksForAVariant(t *testing.T) {
	price := 300.0
	options := []VariantOption{{SKU: "X#1", Label: "1", Price: 1}, {SKU: "X#2", Label: "2", Price: 2}}
	cases := []struct {
		name   string
		line   Line
		want   string
		wantOK bool
	}{
		{"priced shipping with options asks nothing",
			Line{Kind: KindShipping, Label: "Envío", UnitPrice: &price, VariantOptions: options}, "", false},
		{"priced shipping with options and a range asks nothing",
			Line{Kind: KindShipping, Label: "Envío", UnitPrice: &price, VariantOptions: options,
				Range: &llm.Range{Min: 1, Max: 2}}, "", false},
		{"unpriced shipping with options asks for the zone, not the size",
			Line{Kind: KindShipping, Label: "Envío", VariantOptions: options,
				Range: &llm.Range{Min: 1, Max: 2}}, questionShippingZone, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := lineQuestion(c.line)
			if got != c.want || ok != c.wantOK {
				t.Fatalf("lineQuestion = (%q, %v); se esperaba (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}
