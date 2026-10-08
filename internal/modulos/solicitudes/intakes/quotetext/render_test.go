package quotetext_test

// render_test.go — el formato del render determinista. Casi todo se afirma POR
// ESTRUCTURA; el único texto completo es el de TestRender_FullText, porque el render
// es lo que lee el cliente y sus piezas fijas son texto observable.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// Aserciones de compilación del contrato de render.go.
var (
	_ func(quotetext.Draft) string = quotetext.Render
	_ func(float64) string         = quotetext.Amount
)

// TestRender_FullText fija byte a byte un pedido con las tres formas de línea: con
// personalización, con cantidad mayor que uno y sin precio.
func TestRender_FullText(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate", Customization: "sin lactosa", Qty: 1, UnitPrice: 2100},
		{SKU: "TEQ", Label: "Tequeños bandeja x30", Qty: 4, UnitPrice: 490},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	const want = "Hola! Te paso el presupuesto:\n" +
		"\n" +
		"• Torta chocolate — $2100\n" +
		"  Incluye: sin lactosa\n" +
		"• 4 × Tequeños bandeja x30 — $490 c/u — $1960\n" +
		"• Envío — precio por confirmar\n" +
		"\n" +
		"Total: $4060\n" +
		"\n" +
		"Cualquier duda me dices y lo ajustamos."

	if got := quotetext.Render(draft); got != want {
		t.Fatalf("el render no es el esperado:\n%q\nse esperaba:\n%q", got, want)
	}
}

func TestRender_EmptyDraft(t *testing.T) {
	const want = "Hola! Te paso el presupuesto:\n\n\nTotal: $0\n\nCualquier duda me dices y lo ajustamos."
	if got := quotetext.Render(quotetext.DraftOf(nil)); got != want {
		t.Fatalf("el render de un borrador sin líneas no es el esperado:\n%q", got)
	}
}

func TestRender_EveryLineWithItsPriceAndTheTotal(t *testing.T) {
	text := quotetext.Render(fusionDraft())

	last := 0
	for _, item := range fusionItems {
		want := "• " + item.Label + " — " + quotetext.Amount(item.UnitPrice) + "\n"
		at := strings.Index(text, want)
		if at < 0 {
			t.Fatalf("falta la línea %q (producto + tamaño + specs + precio):\n%s", want, text)
		}
		if at < last {
			t.Errorf("la línea %q no está en el orden del borrador:\n%s", item.Label, text)
		}
		last = at
	}
	if !strings.Contains(text, "\nTotal: "+quotetext.Amount(fusionTotal)+"\n") {
		t.Errorf("falta el total:\n%s", text)
	}
	if strings.HasSuffix(text, "\n") {
		t.Errorf("el render no acaba en salto de línea:\n%q", text)
	}
}

// TestRender_CustomizationIsShownAndNeverBilled: INV-13, la personalización NO cobra.
func TestRender_CustomizationIsShownAndNeverBilled(t *testing.T) {
	plain := quotetext.Render(quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
	}))
	custom := quotetext.Render(quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate", Customization: "sin lactosa", Qty: 1, UnitPrice: 2100},
	}))

	if strings.Contains(plain, "Incluye") {
		t.Errorf("una línea sin personalización no lleva «Incluye»:\n%s", plain)
	}
	const includes = "  Incluye: sin lactosa\n"
	if !strings.Contains(custom, includes) {
		t.Fatalf("la personalización no aparece en su propia línea:\n%s", custom)
	}
	if strings.Replace(custom, includes, "", 1) != plain {
		t.Errorf("la personalización cambió algo más que su línea (¿un importe?):\n%s", custom)
	}
}

func TestRender_QuantityOneIsNotWritten(t *testing.T) {
	text := quotetext.Render(quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100},
	}))
	if strings.Contains(text, "×") || strings.Contains(text, "c/u") {
		t.Errorf("con cantidad uno no se escribe ni la cantidad ni «c/u»:\n%s", text)
	}
}

// TestRender_PendingPriceIsSaidWithWords: «$0» le prometería al cliente un envío
// gratis que nadie ha decidido.
func TestRender_PendingPriceIsSaidWithWords(t *testing.T) {
	for name, qty := range map[string]int{"quantity one": 1, "quantity three": 3} {
		t.Run(name, func(t *testing.T) {
			text := quotetext.Render(quotetext.DraftOf([]intakes.Item{
				{SKU: "A", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
				{SKU: intakes.ShippingSKU, Label: "Envío", Qty: qty},
			}))
			if strings.Contains(text, quotetext.Amount(0)) {
				t.Errorf("una línea sin precio no puede escribirse como $0:\n%s", text)
			}
			if !strings.Contains(text, "Envío — precio por confirmar\n") {
				t.Errorf("una línea sin precio tiene que decirlo con palabras:\n%s", text)
			}
			if strings.Count(text, "$") != 2 {
				t.Errorf("solo llevan importe la torta y el total:\n%s", text)
			}
		})
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	draft := fusionDraft()
	first := quotetext.Render(draft)
	for i := 0; i < 20; i++ {
		if other := quotetext.Render(draft); other != first {
			t.Fatalf("dos renders del mismo borrador difieren (vuelta %d)", i)
		}
	}
}

func TestAmount(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  string
	}{
		{"zero", 0, "$0"},
		{"integer without thousands separator", 2100, "$2100"},
		{"a million without separators", 1000000, "$1000000"},
		{"one decimal is padded to two, with a comma", 1234.5, "$1234,50"},
		{"two decimals", 1234.56, "$1234,56"},
		{"below one", 0.99, "$0,99"},
		{"a third decimal is rounded away", 12.346, "$12,35"},
		{"rounding to an integer drops the decimals", 2100.004, "$2100"},
		{"rounding up to an integer drops the decimals", 1.999, "$2"},
		{"only a trailing double zero is trimmed", 100.1, "$100,10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := quotetext.Amount(c.value); got != c.want {
				t.Fatalf("Amount(%v) = %q; se esperaba %q", c.value, got, c.want)
			}
		})
	}
}
