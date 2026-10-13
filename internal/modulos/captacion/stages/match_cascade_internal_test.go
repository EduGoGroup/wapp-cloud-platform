package stages

// match_cascade_internal_test.go — EL ORDEN DEL ESCALÓN POR CLAVE, sobre la función que
// lo decide. El contrato lo recorre desde fuera (TestMatchRun_KeySteps), pero «variante
// antes que tag» solo se distingue con un texto que sea las dos cosas a la vez, y ese
// caso se diagnostica mejor aquí que desde un presupuesto entero.

import (
	"testing"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// TestByKey_FromTheMostSpecificToTheLeast: sku, etiqueta, variante, tag; el primero que
// resuelve gana. Cada texto del fixture es DOS claves a la vez, de artículos distintos.
func TestByKey_FromTheMostSpecificToTheLeast(t *testing.T) {
	cat := catalogo.Catalog{Categories: []catalogo.Category{{Code: "1", Label: "Carta", Items: []catalogo.Article{
		// «familiar» es la etiqueta de A y el sku de B.
		{Code: "1", SKU: "A", Label: "Familiar", Price: 1},
		{Code: "2", SKU: "familiar", Label: "Combo", Price: 2},
		// «grande» es la etiqueta de C y la variante de D.
		{Code: "3", SKU: "C", Label: "Grande", Price: 3},
		{Code: "4", SKU: "D", Label: "Pizza", Variants: []catalogo.Variant{
			{Code: "G", Label: "Grande", Price: 4}, {Code: "V", Label: "Vegana", Price: 5},
		}},
		// «vegana» es la variante de D y el tag de E.
		{Code: "5", SKU: "E", Label: "Ensalada", Price: 6, Tags: []string{"vegana", "fresca"}},
	}}}}
	idx, err := indice.Construir(cat, textmatch.Normalize)
	if err != nil {
		t.Fatalf("construir el índice del fixture: %v", err)
	}
	cases := []struct{ name, text, sku, strategy string }{
		{"sku before label", "familiar", "familiar", StrategySKU},
		{"label before variant", "grande", "C", StrategyExact},
		{"variant before tag", "vegana", "D", StrategyVariant},
		{"tag when nothing above answers", "fresca", "E", StrategyTag},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ok := byKey(idx, c.text)
			if !ok || f.match.Articulo.SKU != c.sku || f.provenance.Strategy != c.strategy || f.provenance.Confidence != 1 {
				t.Fatalf("byKey(%q) = %s por %q (ok=%v, confianza %v); se esperaba %s por %q con confianza 1",
					c.text, f.match.Articulo.SKU, f.provenance.Strategy, ok, f.provenance.Confidence, c.sku, c.strategy)
			}
			if (c.strategy == StrategyVariant) != f.match.HayVariante {
				t.Fatalf("HayVariante = %v por %q: solo el escalón de variante la trae resuelta", f.match.HayVariante, c.strategy)
			}
		})
	}
	if _, ok := byKey(idx, "no está en la carta"); ok {
		t.Fatal("un texto que no es ninguna clave no resuelve")
	}
}
