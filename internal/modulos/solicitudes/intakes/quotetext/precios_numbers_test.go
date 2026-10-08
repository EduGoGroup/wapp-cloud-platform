package quotetext_test

// precios_numbers_test.go — parte de precios_test.go: cómo LEE el verificador los
// números del texto (marca de dinero, separadores, números ilegibles, los que el
// borrador ya traía) y la pareja escritor ↔ lector (Render y Amount contra Verify).
//
// El corpus lleva casos adversarios a propósito (T-15): separadores repetidos,
// dígitos que no son ASCII y espacios Unicode. Un corpus solo de casos felices es
// ciego a un mutante realista del lector.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// TestVerify_MoneyMark fija qué hace que un número sea un IMPORTE. Cada caso reescribe
// el precio de la segunda torta del caso Fusión; si la marca no cuenta, el 2950 queda
// desnudo, por debajo del techo, y lo que se nota es que FALTA ese precio (C1).
func TestVerify_MoneyMark(t *testing.T) {
	cases := []struct {
		name   string
		amount string
		marked bool
	}{
		{"dollar sign", "$2950", true},
		{"dollar sign and one space", "$ 2950", true},
		{"dollar sign and a tab", "$\t2950", true},
		{"currency word", "2950 pesos", true},
		{"currency word, singular", "2950 peso", true},
		{"currency word, glued", "2950pesos", true},
		{"currency word, upper case", "2950 PESOS", true},
		{"clp", "2950 clp", true},
		{"usd, upper case", "2950 USD", true},
		{"bs", "2950 bs", true},
		{"soles", "2950 soles", true},
		{"dolares without accent", "2950 dolares", true},
		{"dólares with accent", "2950 dólares", true},
		{"dólar, upper case with accent", "2950 DÓLAR", true},
		{"both marks at once", "$2950 pesos", true},

		{"sol in singular is not a currency word", "2950 sol", false},
		{"no mark at all", "2950", false},
		{"dollar sign after the number", "2950 $", false},
		{"currency word before the number", "USD 2950", false},
		{"dollar sign and two spaces", "$  2950", false},
		{"currency word after two spaces", "2950  pesos", false},
		{"dollar sign and a no-break space", "$\u00a02950", false},
		{"currency word after a no-break space", "2950\u00a0pesos", false},
		{"currency word after a thin space", "2950\u2009pesos", false},
		{"dollar sign and a zero-width space", "$\u200b2950", false},
		{"fullwidth digits are not a number", "$\uff12\uff19\uff15\uff10", false},
		{"arabic-indic digits are not a number", "$\u0662\u0669\u0665\u0660", false},
		{"fullwidth dollar sign is not a money mark", "\uff042950", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := strings.Replace(modelText, "$2950", c.amount, 1)
			verdict := quotetext.Verify(fusionDraft(), text)
			if c.marked && !verdict.OK {
				t.Fatalf("%q no se leyó como importe: motivo=%q detalle=%q", c.amount, verdict.Reason, verdict.Detail)
			}
			if !c.marked && verdict.Reason != quotetext.ReasonMissingUnitPrice {
				t.Fatalf("%q: motivo = %q (OK=%v); sin marca válida, lo que falta es el precio de la línea",
					c.amount, verdict.Reason, verdict.OK)
			}
		})
	}
}

func TestVerify_OnlyNonASCIIDigitsIsATextWithoutAmounts(t *testing.T) {
	text := "Torta $\uff12\uff11\uff10\uff10. Total $\uff12\uff11\uff10\uff10"
	draft := quotetext.DraftOf([]intakes.Item{{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100}})

	verdict := quotetext.Verify(draft, text)
	if verdict.OK || verdict.Reason != quotetext.ReasonTextWithoutAmounts {
		t.Fatalf("motivo = %q (OK=%v); los dígitos que no son ASCII no son números", verdict.Reason, verdict.OK)
	}
}

// verifyLiteral verifica un pedido de UNA línea de precio `price` contra un texto que
// dice ese precio y el total con el literal dado.
func verifyLiteral(price float64, literal string) quotetext.Verdict {
	draft := quotetext.DraftOf([]intakes.Item{{SKU: "A", Label: "Cosa", Qty: 1, UnitPrice: price}})
	return quotetext.Verify(draft, "Cosa $"+literal+" y en total $"+literal)
}

// TestVerify_Separators es la regla de los separadores, que es lo único interesante
// del lector. Un literal que se lee como otro número no rompe nada: cae por C3.
func TestVerify_Separators(t *testing.T) {
	cases := []struct {
		name    string
		price   float64
		literal string
		same    bool
	}{
		{"plain", 2100, "2100", true},
		{"dot as thousands", 2100, "2.100", true},
		{"comma as thousands", 2100, "2,100", true},
		{"dot thousands, comma decimals", 2100, "2.100,00", true},
		{"comma thousands, dot decimals", 2100, "2,100.00", true},
		{"comma decimals", 2100, "2100,00", true},
		{"dot decimals", 2100, "2100.00", true},
		{"a single decimal digit", 2100, "2100,0", true},
		{"trailing dot of the sentence", 2100, "2100.", true},
		{"trailing comma of the sentence", 2100, "2100,", true},
		{"trailing dot after decimals", 1234.5, "1234,50.", true},
		{"trailing mixed punctuation", 2100, "2100.,", true},
		{"several thousands groups with dots", 1234567, "1.234.567", true},
		{"several thousands groups with commas", 1234567, "1,234,567", true},
		{"several groups and decimals", 1234567, "1.234.567,00", true},
		{"several comma groups and dot decimals", 1234567, "1,234,567.00", true},
		{"decimals below one", 0.99, "0,99", true},
		{"dot decimals below one", 0.99, "0.99", true},
		{"one decimal digit means tenths", 2100.5, "2100,5", true},
		{"thousands and one decimal digit", 2100.5, "2.100,5", true},
		{"comma thousands and one decimal digit", 2100.5, "2,100.5", true},

		// Adversarios: separadores repetidos. Un separador repetido NO es decimal.
		{"repeated dot is thousands", 2100, "2..100", true},
		{"repeated comma is thousands", 2100, "2,,100", true},
		{"a dot between every digit is thousands", 2100, "2.1.0.0", true},
		{"repeated dot before real decimals", 2100.5, "2..100,5", true},
		{"dot then comma glued: the last one is the decimal", 2100, "2.,100", false},
		{"comma then dot glued: the last one is the decimal", 2100, "2,.100", false},

		// Lo que la regla lee como OTRO número.
		{"three digits after the only separator are thousands", 2100.5, "2100,500", false},
		{"two digits after the only separator are decimals", 2100, "21,00", false},
		{"en-US reading of a es-CL price", 2100, "2.10", false},
		{"three decimals are thousands", 12.35, "12,345", false},
		{"one cent off is another amount", 2100, "2100,01", false},
		{"forty cents off is another amount", 2100, "2100,40", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict := verifyLiteral(c.price, c.literal)
			if c.same && !verdict.OK {
				t.Fatalf("%q no se leyó como %v: motivo=%q detalle=%q", c.literal, c.price, verdict.Reason, verdict.Detail)
			}
			if !c.same && verdict.Reason != quotetext.ReasonForeignAmount {
				t.Fatalf("%q contra %v: motivo = %q (OK=%v); se esperaba un importe ajeno",
					c.literal, c.price, verdict.Reason, verdict.OK)
			}
		})
	}
}

// TestVerify_ThousandsSeparatorsInARealText: el modelo puede escribir «$2.100» y eso
// NO puede tumbar un texto correcto en es-CL.
func TestVerify_ThousandsSeparatorsInARealText(t *testing.T) {
	text := strings.NewReplacer(
		"$2100", "$2.100", "$2950", "$2.950", "$5540", "$5.540",
	).Replace(modelText)
	if v := quotetext.Verify(fusionDraft(), text); !v.OK {
		t.Fatalf("un texto con separador de miles se rechazó: motivo=%q detalle=%q", v.Reason, v.Detail)
	}
}

// TestVerify_UnreadableNumber es la lección cara del plan: lo que viene del modelo se
// valida ANTES de convertirlo. Un número imposible da un ERROR DE DATO nombrado, no un
// pánico ni un cero silencioso, y no se salta aunque venga sin marca de dinero.
func TestVerify_UnreadableNumber(t *testing.T) {
	const overflowDetail = "quotetext: número ilegible: 400 caracteres, separadores no interpretables"
	cases := []struct {
		name   string
		text   string
		detail string
	}{
		{"a marked number that overflows", "Total $" + strings.Repeat("9", 400), overflowDetail},
		{"a naked number that overflows", modelText + "\nPedido " + strings.Repeat("9", 400), overflowDetail},
		{"the length is the one of the cleaned literal",
			"Total $" + strings.Repeat("9.999", 100) + ".", overflowDetail},
		{"a number that stops being finite in cents", "Total $1" + strings.Repeat("0", 308),
			"quotetext: número ilegible: el valor no es finito"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict := quotetext.Verify(fusionDraft(), c.text)
			if verdict.OK || verdict.Reason != quotetext.ReasonUnreadableNumber {
				t.Fatalf("motivo = %q (OK=%v); se esperaba %q", verdict.Reason, verdict.OK, quotetext.ReasonUnreadableNumber)
			}
			if verdict.Detail != c.detail {
				t.Errorf("detalle = %q; se esperaba %q", verdict.Detail, c.detail)
			}
		})
	}
}

// TestVerify_NumbersOfTheDraftAreForgiven es la contraparte de C4: los números que YA
// estaban en el borrador —los que el prompt le pide al modelo que copie— no pueden
// tumbar un texto correcto aunque superen el techo. Cada caso lleva su CONTROL: sin
// él, pasaría también con C4 desactivada.
func TestVerify_NumbersOfTheDraftAreForgiven(t *testing.T) {
	cases := []struct {
		name    string
		item    intakes.Item
		text    string
		foreign string
	}{
		{"a number of the label",
			intakes.Item{SKU: "TEQ", Label: "Tequeños congelados paquete x30", Qty: 1, UnitPrice: 12},
			"Hola! El paquete x30 de tequeños te queda en $12. Total $12", " y el x40 sale igual"},
		{"a number of the customization",
			intakes.Item{SKU: "T", Label: "Torta", Customization: "vela con el número 50", Qty: 1, UnitPrice: 12},
			"La torta con la vela del 50 te queda en $12. Total $12", " Para 60 personas no alcanza."},
		{"the quantity",
			intakes.Item{SKU: "G", Label: "Galleta", Qty: 500, UnitPrice: 0.1},
			"Las 500 galletas a $0,10 cada una son $50. Total $50", " Si quieres 600 me avisas."},
		{"both numbers of a range in the label",
			intakes.Item{SKU: "T", Label: "Torta 25-30 porciones", Qty: 1, UnitPrice: 20},
			"La torta de 25-30 porciones te queda en $20. Total $20", " Para 35 es otra."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			draft := quotetext.DraftOf([]intakes.Item{c.item})
			if v := quotetext.Verify(draft, c.text); !v.OK {
				t.Fatalf("un número del borrador tumbó un texto correcto: motivo=%q detalle=%q", v.Reason, v.Detail)
			}
			if v := quotetext.Verify(draft, c.text+c.foreign); v.Reason != quotetext.ReasonForeignNumber {
				t.Fatalf("motivo = %q (OK=%v); un número grande ajeno al borrador tiene que caer por C4", v.Reason, v.OK)
			}
		})
	}
}

// TestVerify_DraftNumberWithAMoneyMarkIsStillForeign: el perdón es para los números
// DESNUDOS. Un número de la etiqueta escrito como importe no sale de ninguna línea, y
// cae por C3 con su diagnóstico, no por la secuencia.
func TestVerify_DraftNumberWithAMoneyMarkIsStillForeign(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "TEQ", Label: "Tequeños congelados paquete x30", Qty: 1, UnitPrice: 12},
	})
	verdict := quotetext.Verify(draft, "El paquete x30 te queda en $12, o $30 con salsa. Total $12")
	if verdict.OK || verdict.Reason != quotetext.ReasonForeignAmount {
		t.Fatalf("motivo = %q (OK=%v); un número del borrador con marca de dinero es un importe ajeno",
			verdict.Reason, verdict.OK)
	}
}

// TestVerify_UnreadableNumberInTheDraftGrantsNothing: un número ilegible en una
// etiqueta no da permiso y tampoco es un error.
func TestVerify_UnreadableNumberInTheDraftGrantsNothing(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Cosa ref " + strings.Repeat("9", 400), Qty: 1, UnitPrice: 12},
	})
	if v := quotetext.Verify(draft, "La cosa te queda en $12. Total $12"); !v.OK {
		t.Fatalf("un número ilegible en la etiqueta tumbó un texto correcto: motivo=%q detalle=%q", v.Reason, v.Detail)
	}
}

// TestVerify_NakedNumbersDoNotSwitchOffTheGenerator: una línea barata NO puede
// convertir cualquier fecha, hora o cantidad del texto en un rechazo. Con el listón en
// el importe más barato, «te llamo en 3 días» caía, y el único rastro era un
// `fallback_reason` en un log: un fallo mudo.
func TestVerify_NakedNumbersDoNotSwitchOffTheGenerator(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "GALLETA", Label: "Galleta decorada", Qty: 1, UnitPrice: 2},
		{SKU: "TORTA", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
	})
	base := "Hola! La galleta te queda en $2 y la torta en $2100. Total $2102."

	harmless := map[string]string{
		"a number of days":      " Te llamo en 3 días.",
		"a date":                " Es para el 30 de agosto.",
		"some portions":         " La torta es de 12 porciones.",
		"a time without dots":   " Te lo llevo a las 1930.",
		"exactly the ceiling":   " Son 2102 en total.",
		"the declared loophole": " Con velas el total sería 2000.",
	}
	for name, tail := range harmless {
		t.Run(name, func(t *testing.T) {
			if v := quotetext.Verify(draft, base+tail); !v.OK {
				t.Fatalf("un número inocuo apagó el generador: motivo=%q detalle=%q", v.Reason, v.Detail)
			}
		})
	}

	t.Run("a naked number just above the ceiling still falls", func(t *testing.T) {
		v := quotetext.Verify(draft, base+" Con decoración extra son 2103.")
		if v.OK || v.Reason != quotetext.ReasonForeignNumber {
			t.Fatalf("motivo=%q (OK=%v); por encima del techo y ajeno al borrador cae por C4", v.Reason, v.OK)
		}
	})
	t.Run("a marked foreign amount still falls", func(t *testing.T) {
		v := quotetext.Verify(draft, base+" Con decoración extra son $3000.")
		if v.OK || v.Reason != quotetext.ReasonForeignAmount {
			t.Fatalf("motivo=%q (OK=%v); un importe marcado ajeno tiene que caer por C3", v.Reason, v.OK)
		}
	})
}

// TestRender_PassesItsOwnVerifier es LA PROPIEDAD del respaldo: pase lo que pase, lo
// que se devuelve cuando el modelo falla cuadra con las líneas.
//
// 🔴 LOS CASOS DE MÁS DE DOS DECIMALES ESTÁN AQUÍ PORQUE FALLARON: Amount escribe
// dinero con dos decimales, y un verificador que comparase contra el valor crudo
// llamaría `importe_ajeno` al propio render. Por eso todo se compara en CÉNTIMOS.
func TestRender_PassesItsOwnVerifier(t *testing.T) {
	cases := map[string][]intakes.Item{
		"the base case": fusionItems,
		"more than two decimals": {
			{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100.005},
			{SKU: "B", Label: "Café", Qty: 3, UnitPrice: 0.333},
		},
		"decimals that pile up when multiplying": {
			{SKU: "A", Label: "Empanada", Qty: 3, UnitPrice: 0.1},
			{SKU: "B", Label: "Jugo", Qty: 7, UnitPrice: 1.15},
		},
		"quantities above one": {
			{SKU: "TEQ", Label: "Tequeños bandeja x30", Qty: 4, UnitPrice: 490},
			{SKU: "EMP", Label: "Empanadas", Qty: 12, UnitPrice: 250},
		},
		"decimals": {
			{SKU: "A", Label: "Torta", Qty: 3, UnitPrice: 1234.5},
			{SKU: "B", Label: "Café", Qty: 1, UnitPrice: 0.99},
		},
		"a pending line": {
			{SKU: "A", Label: "Torta chocolate — 15 porciones", Qty: 1, UnitPrice: 2100},
			{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
		},
		"a label with a number above the price": {
			{SKU: "A", Label: "Bandeja de 3000 mini empanadas", Qty: 1, UnitPrice: 12},
		},
		"a customization with a number above the price": {
			{SKU: "A", Label: "Torta chocolate", Customization: "cartel de 9000 días", Qty: 2, UnitPrice: 2100},
		},
		"a quantity above every amount": {
			{SKU: "G", Label: "Galleta", Qty: 500, UnitPrice: 0.1},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			draft := quotetext.DraftOf(items)
			text := quotetext.Render(draft)
			if v := quotetext.Verify(draft, text); !v.OK {
				t.Fatalf("el render determinista NO pasa su propio verificador:\nmotivo=%q detalle=%q\n---\n%s",
					v.Reason, v.Detail, text)
			}
		})
	}
}

// TestAmount_IsReadBackByTheVerifier ata el escritor con el lector. Sin este test, un
// cambio en el formato de Amount podría dejar de ser legible por el verificador y el
// único síntoma sería que TODO cae al determinista... que es lo que el determinista
// produce, así que nadie lo notaría.
func TestAmount_IsReadBackByTheVerifier(t *testing.T) {
	for _, value := range []float64{1, 12, 490, 2100, 5540, 1234.5, 0.99, 0.1, 1000000, 1234567.89} {
		draft := quotetext.DraftOf([]intakes.Item{{SKU: "A", Label: "Cosa", Qty: 1, UnitPrice: value}})
		text := "Cosa " + quotetext.Amount(value) + ". Total " + quotetext.Amount(value)
		if v := quotetext.Verify(draft, text); !v.OK {
			t.Errorf("Amount(%v) = %q no se relee como el mismo importe: motivo=%q detalle=%q",
				value, quotetext.Amount(value), v.Reason, v.Detail)
		}
	}
}
