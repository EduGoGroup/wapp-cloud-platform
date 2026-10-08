//go:build pendiente

package quotetext_test

// precios_test.go — EL VERIFICADOR (INV-2: el LLM nunca calcula precios): el
// vocabulario, la validación de la salida, la secuencia esperada y las cinco
// condiciones con su orden. La lectura de números vive en precios_numbers_test.go.

import (
	"math"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// Aserciones de compilación del contrato de precios.go.
var (
	_ func(string) error                              = quotetext.ValidateOutput
	_ func(quotetext.Draft, string) quotetext.Verdict = quotetext.Verify
	_ func(quotetext.Draft) []float64                 = quotetext.ExpectedSequence
)

// TestVerifierVocabulary fija los valores que viajan por `fallback_reason` y por el
// log: el identificador se tradujo, el valor NO.
func TestVerifierVocabulary(t *testing.T) {
	if quotetext.MaxTextRunes != 4000 {
		t.Errorf("MaxTextRunes = %d; se esperaba 4000", quotetext.MaxTextRunes)
	}
	values := map[string]string{
		quotetext.ReasonDraftWithoutAmounts: "borrador_sin_importes",
		quotetext.ReasonUnreadableText:      "texto_ilegible",
		quotetext.ReasonUnreadableNumber:    "numero_ilegible",
		quotetext.ReasonTextWithoutAmounts:  "texto_sin_importes",
		quotetext.ReasonMissingUnitPrice:    "falta_precio_de_linea",
		quotetext.ReasonMissingTotal:        "falta_total",
		quotetext.ReasonForeignAmount:       "importe_ajeno",
		quotetext.ReasonForeignNumber:       "numero_ajeno",
		quotetext.ReasonAmountsOutOfPlace:   "importes_fuera_de_sitio",
	}
	if len(values) != 9 {
		t.Fatalf("hay %d motivos distintos; el verificador tiene 9", len(values))
	}
	for got, want := range values {
		if got != want {
			t.Errorf("motivo = %q; el valor del cable es %q", got, want)
		}
	}
}

func TestValidateOutput_AcceptsUsableText(t *testing.T) {
	cases := map[string]string{
		"a real quote":                          modelText,
		"newline, carriage return and tab":      "Torta\t$2100\r\nTotal $2100",
		"exactly the limit, counted in runes":   strings.Repeat("ñ", quotetext.MaxTextRunes),
		"a zero-width space is not a control":   "Torta\u200b $2100",
		"a line separator is not a control":     "Torta $2100\u2028Total $2100",
		"blank edges around real text are fine": "  \n Total $2100 \n ",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if err := quotetext.ValidateOutput(text); err != nil {
				t.Fatalf("se rechazó un texto utilizable: %v", err)
			}
		})
	}
}

func TestValidateOutput_RejectsWithItsReason(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"not utf-8", "Total \xff\xfe $2100", "texto_ilegible: la salida no es UTF-8"},
		{"empty", "", "texto_ilegible: la salida está vacía"},
		{"only ascii blanks", "   \n\t ", "texto_ilegible: la salida está vacía"},
		{"only unicode spaces", "\u00a0\u2003\u3000", "texto_ilegible: la salida está vacía"},
		{"one rune over the limit", strings.Repeat("a", quotetext.MaxTextRunes+1),
			"texto_ilegible: la salida tiene 4001 runas y el tope es 4000"},
		{"null byte", "Total $5540\x00", "texto_ilegible: la salida trae un carácter de control (U+0000)"},
		{"the first control character is the one named", "a\x1bb\x00",
			"texto_ilegible: la salida trae un carácter de control (U+001B)"},
		{"delete", "Total\x7f", "texto_ilegible: la salida trae un carácter de control (U+007F)"},
		{"next line (C1 control)", "Total\u0085$5540",
			"texto_ilegible: la salida trae un carácter de control (U+0085)"},
		{"utf-8 is checked before controls", "\x00\xff", "texto_ilegible: la salida no es UTF-8"},
		{"length is checked before controls", strings.Repeat("a", quotetext.MaxTextRunes+1) + "\x00",
			"texto_ilegible: la salida tiene 4002 runas y el tope es 4000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := quotetext.ValidateOutput(c.text)
			if err == nil {
				t.Fatal("se aceptó un texto que no es utilizable")
			}
			if err.Error() != c.want {
				t.Errorf("error = %q; se esperaba %q", err, c.want)
			}
			if !strings.HasPrefix(err.Error(), quotetext.ReasonUnreadableText+": ") {
				t.Errorf("el error no empieza por el motivo: %q", err)
			}
		})
	}
}

// sameAmounts compara dos secuencias de importes con una holgura muy por debajo del
// céntimo: la secuencia ya viene redondeada.
func sameAmounts(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func TestExpectedSequence(t *testing.T) {
	cases := []struct {
		name  string
		draft quotetext.Draft
		want  []float64
	}{
		{"unit price per line, then the total", fusionDraft(), []float64{2100, 2950, 490, 5540}},
		{"quantity above one adds the line total", quotetext.DraftOf([]intakes.Item{
			{SKU: "TEQ", Label: "Tequeños", Qty: 4, UnitPrice: 490},
		}), []float64{490, 1960, 1960}},
		{"a single line of quantity one expects its number twice", quotetext.DraftOf([]intakes.Item{
			{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100},
		}), []float64{2100, 2100}},
		{"a pending line adds nothing", quotetext.DraftOf([]intakes.Item{
			{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100},
			{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 3},
		}), []float64{2100, 2100}},
		{"amounts are rounded to cents", quotetext.DraftOf([]intakes.Item{
			{SKU: "B", Label: "Café", Qty: 3, UnitPrice: 0.333},
		}), []float64{0.33, 1, 1}},
		{"a line without a positive unit price is skipped", quotetext.Draft{
			Version: quotetext.DraftVersion, Total: 5,
			Lines: []quotetext.Line{
				{Label: "Descuento", Qty: 1, UnitPrice: -5, LineTotal: -5},
				{Label: "Torta", Qty: 1, UnitPrice: 10, LineTotal: 10},
			},
		}, []float64{10, 5}},
		{"a total that is not positive is left out", quotetext.Draft{
			Version: quotetext.DraftVersion,
			Lines:   []quotetext.Line{{Label: "Torta", Qty: 1, UnitPrice: 10, LineTotal: 10}},
		}, []float64{10}},
		{"every line pending", quotetext.DraftOf([]intakes.Item{
			{SKU: "A", Label: "Torta por presupuestar", Qty: 1},
			{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
		}), nil},
		{"no lines", quotetext.DraftOf(nil), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := quotetext.ExpectedSequence(c.draft)
			if !sameAmounts(got, c.want) {
				t.Fatalf("secuencia = %v; se esperaba %v", got, c.want)
			}
		})
	}
}

func TestExpectedSequence_DoesNotMutateTheDraft(t *testing.T) {
	draft := fusionDraft()
	before := quotetext.Render(draft)

	if got := quotetext.ExpectedSequence(draft); len(got) != 4 {
		t.Fatalf("secuencia = %v; el caso Fusión tiene cuatro importes", got)
	}

	if after := quotetext.Render(draft); after != before {
		t.Fatalf("ExpectedSequence mutó el borrador:\n%s", after)
	}
}

func TestVerify_GoodTextPasses(t *testing.T) {
	verdict := quotetext.Verify(fusionDraft(), modelText)
	if !verdict.OK {
		t.Fatalf("el texto bueno se rechazó: motivo=%q detalle=%q", verdict.Reason, verdict.Detail)
	}
	if verdict.Reason != "" || verdict.Detail != "" {
		t.Errorf("un veredicto bueno no trae motivo ni detalle: %+v", verdict)
	}
}

// swappedText son las dos tortas del caso Fusión con los precios INTERCAMBIADOS. Ni
// un número inventado: 2950 y 2100 son los dos precios reales, cambiados de sitio.
const swappedText = "Hola! Te paso el presupuesto:\n" +
	"Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo — $2950\n" +
	"El otro para 25-30 personas, vainilla, ddl y merengue — $2100\n" +
	"Envío — $490\n" +
	"Total $5540"

// TestVerify_Rejections recorre un fallo por cada condición sobre el caso Fusión
// (torta $2100 · torta $2950 · envío $490), con el detalle que sale al log.
//
// Los cuatro últimos casos usan EXCLUSIVAMENTE importes que salen del borrador: no
// hay ni un número inventado, y aun así le mandarían al cliente un precio que no es
// el suyo. Es el hueco que una regla de CONJUNTOS no podía ver.
func TestVerify_Rejections(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		reason string
		detail string
	}{
		{"C1: a unit price is missing",
			strings.Replace(modelText, "Envío — $490", "Envío incluido", 1),
			quotetext.ReasonMissingUnitPrice, "la línea 3 vale $490 y ese importe no está en el texto"},
		{"C2: the total is missing",
			strings.Replace(modelText, "Total $5540", "Te lo dejo así", 1),
			quotetext.ReasonMissingTotal, "el total es $5540 y no está en el texto"},
		{"C3: an invented amount with a money mark",
			modelText + "\nRecargo por decoración $800",
			quotetext.ReasonForeignAmount, "el texto trae el importe $800 y no sale de ninguna línea"},
		{"C3: a price altered by the model",
			strings.Replace(modelText, "$2950", "$3000", 1),
			quotetext.ReasonForeignAmount, "el texto trae el importe $3000 y no sale de ninguna línea"},
		{"C4: a big invented number without a money mark",
			modelText + "\nSi lo quieres para 20 personas seria 6200 en total",
			quotetext.ReasonForeignNumber,
			"el texto trae el número 6200, por encima de lo más caro del pedido ($5540), y no sale del borrador"},
		{"the text states no price at all",
			"Hola! ya te paso el presupuesto, dame un rato",
			quotetext.ReasonTextWithoutAmounts, "el texto no dice ni un precio"},
		{"C5 swap: the prices of the two cakes, exchanged",
			swappedText,
			quotetext.ReasonAmountsOutOfPlace, "el importe nº 1 del texto es $2950 y ahí va $2100"},
		{"C5: an invented charge that reuses an existing amount",
			modelText + "\nSeña por adelantado: $490",
			quotetext.ReasonAmountsOutOfPlace, "el texto dice 5 importes y el presupuesto tiene 4"},
		{"C5: an amount repeated by adding a mention",
			modelText + "\nY el segundo también a $2100",
			quotetext.ReasonAmountsOutOfPlace, "el texto dice 5 importes y el presupuesto tiene 4"},
		{"C1 catches an amount repeated over another one",
			strings.Replace(modelText, "merengue — $2950", "merengue — $2100", 1),
			quotetext.ReasonMissingUnitPrice, "la línea 2 vale $2950 y ese importe no está en el texto"},
		{"C5: correct amounts listed in another order than the draft",
			"Envío $490, la de chocolate $2100 y la de vainilla $2950. Total $5540",
			quotetext.ReasonAmountsOutOfPlace, "el importe nº 1 del texto es $490 y ahí va $2100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict := quotetext.Verify(fusionDraft(), c.text)
			if verdict.OK {
				t.Fatal("el texto se ACEPTÓ y se le habría mandado al cliente")
			}
			if verdict.Reason != c.reason {
				t.Errorf("motivo = %q; se esperaba %q (detalle: %s)", verdict.Reason, c.reason, verdict.Detail)
			}
			if verdict.Detail != c.detail {
				t.Errorf("detalle = %q; se esperaba %q", verdict.Detail, c.detail)
			}
		})
	}
}

// TestVerify_DetailNeverQuotesTheText: el detalle va al log, y lo que redactó el
// modelo no es material para un log (INV-6). Solo números y nombres de campo.
func TestVerify_DetailNeverQuotesTheText(t *testing.T) {
	texts := []string{
		"ZAFIRO la torta $2100 ZAFIRO",
		"ZAFIRO $2100 $2950 $490 ZAFIRO",
		"ZAFIRO $2100 $2950 $490 $5540 y de ZAFIRO $2100",
		"ZAFIRO $777",
		"ZAFIRO 99999 $2100",
		"ZAFIRO $" + strings.Repeat("9", 400),
		"ZAFIRO\x00",
		"ZAFIRO sin precios",
	}
	for _, text := range texts {
		verdict := quotetext.Verify(fusionDraft(), text)
		if verdict.OK {
			t.Fatalf("se aceptó %q: el caso no prueba un rechazo", text)
		}
		if verdict.Detail == "" {
			t.Errorf("un rechazo sin detalle no le sirve a nadie que lea el log (motivo %q)", verdict.Reason)
		}
		if strings.Contains(verdict.Detail, "ZAFIRO") {
			t.Errorf("el detalle cita el texto del modelo: %q", verdict.Detail)
		}
	}
}

// pendingOnlyDraft es un pedido sin ni un importe.
func pendingOnlyDraft() quotetext.Draft {
	return quotetext.DraftOf([]intakes.Item{{SKU: "X", Label: "Torta por presupuestar", Qty: 1}})
}

// TestVerify_FirstFailingCheckWins fija el ORDEN de las comprobaciones: decide qué
// motivo sale cuando fallan varias a la vez.
func TestVerify_FirstFailingCheckWins(t *testing.T) {
	overflow := "$" + strings.Repeat("9", 400)
	cases := []struct {
		name   string
		draft  quotetext.Draft
		text   string
		reason string
	}{
		{"unusable text beats a draft without amounts", pendingOnlyDraft(), "  ",
			quotetext.ReasonUnreadableText},
		{"a draft without amounts beats an unreadable number", pendingOnlyDraft(), "Total " + overflow,
			quotetext.ReasonDraftWithoutAmounts},
		{"a draft without amounts rejects even a plausible text", pendingOnlyDraft(), "Torta $2100. Total $2100",
			quotetext.ReasonDraftWithoutAmounts},
		{"an unreadable number beats a foreign amount that comes earlier", fusionDraft(),
			"Recargo $800 y luego " + overflow, quotetext.ReasonUnreadableNumber},
		{"numbers are judged in text order: naked first", fusionDraft(),
			"Serían 6200, o con recargo $800", quotetext.ReasonForeignNumber},
		{"numbers are judged in text order: marked first", fusionDraft(),
			"Con recargo $800, serían 6200", quotetext.ReasonForeignAmount},
		{"a foreign naked number beats the lack of any price", fusionDraft(),
			"Te va a salir como 9000", quotetext.ReasonForeignNumber},
		{"a missing unit price beats a missing total", fusionDraft(),
			"Torta $2100 y la otra $2950", quotetext.ReasonMissingUnitPrice},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict := quotetext.Verify(c.draft, c.text)
			if verdict.OK || verdict.Reason != c.reason {
				t.Fatalf("motivo = %q (OK=%v); se esperaba %q (detalle: %s)",
					verdict.Reason, verdict.OK, c.reason, verdict.Detail)
			}
		})
	}
}

func TestVerify_DetailOfTheEarlyRejections(t *testing.T) {
	blank := quotetext.Verify(fusionDraft(), "   \n ")
	if err := quotetext.ValidateOutput("   \n "); err == nil || blank.Detail != err.Error() {
		t.Errorf("detalle = %q; se esperaba el texto del error de ValidateOutput (%v)", blank.Detail, err)
	}
	tooLong := quotetext.Verify(fusionDraft(), strings.Repeat("a", quotetext.MaxTextRunes+1))
	if tooLong.Reason != quotetext.ReasonUnreadableText {
		t.Errorf("motivo = %q; un texto por encima del tope es ilegible", tooLong.Reason)
	}
	empty := quotetext.Verify(pendingOnlyDraft(), "Torta $2100. Total $2100")
	if empty.Detail != "ninguna línea del borrador tiene importe" {
		t.Errorf("detalle = %q", empty.Detail)
	}
	first := quotetext.Verify(fusionDraft(), "Solo el envío $490 y el total $5540")
	if first.Detail != "la línea 1 vale $2100 y ese importe no está en el texto" {
		t.Errorf("detalle = %q; C1 nombra la PRIMERA línea que falta, en el orden del borrador", first.Detail)
	}
}

// TestVerify_PendingLineNeedsNoPriceButForbidsOne: el envío sin precio no tiene
// unitario que exigir (C1 lo salta), pero si el modelo se inventa uno cae por C3.
func TestVerify_PendingLineNeedsNoPriceButForbidsOne(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "TORTA", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	if v := quotetext.Verify(draft, "Torta $2100, el envío te lo confirmo con la zona. Total $2100"); !v.OK {
		t.Fatalf("se rechazó un texto correcto: motivo=%q detalle=%q", v.Reason, v.Detail)
	}
	if v := quotetext.Verify(draft, "Torta $2100, envío $700. Total $2100"); v.OK ||
		v.Reason != quotetext.ReasonForeignAmount {
		t.Fatalf("motivo = %q (OK=%v); un envío inventado tiene que caer por C3", v.Reason, v.OK)
	}
}

// TestVerify_RepeatedAmounts son los casos legítimos en que un importe se repite, y
// el lado conservador de cada uno.
func TestVerify_RepeatedAmounts(t *testing.T) {
	twoAlike := quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate — 15 porciones", Qty: 1, UnitPrice: 2100},
		{SKU: "B", Label: "Torta vainilla — 15 porciones", Qty: 1, UnitPrice: 2100},
	})
	single := quotetext.DraftOf([]intakes.Item{{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100}})
	several := quotetext.DraftOf([]intakes.Item{{SKU: "TEQ", Label: "Tequeños", Qty: 4, UnitPrice: 490}})
	cases := []struct {
		name   string
		draft  quotetext.Draft
		text   string
		reason string // vacío = se acepta
		detail string
	}{
		{"two lines at the same price, said twice", twoAlike,
			"Hola! La de chocolate te queda en $2100 y la de vainilla también en $2100. Total $4200", "", ""},
		{"two lines at the same price, said once", twoAlike,
			"Hola! Las dos tortas a $2100. Total $4200",
			quotetext.ReasonAmountsOutOfPlace, "el texto dice 2 importes y el presupuesto tiene 3"},
		{"a single line: price and total, both said", single, "La torta $2100. Total $2100", "", ""},
		{"a single line: the number said only once", single, "La torta te queda en $2100",
			quotetext.ReasonAmountsOutOfPlace, "el texto dice 1 importes y el presupuesto tiene 2"},
		{"quantity above one: unit price, line total and total", several,
			"Las 4 bandejas a $490 cada una son $1960. Total $1960", "", ""},
		{"quantity above one without the line total", several,
			"Las 4 bandejas a $490 cada una. Total $1960",
			quotetext.ReasonAmountsOutOfPlace, "el texto dice 2 importes y el presupuesto tiene 3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict := quotetext.Verify(c.draft, c.text)
			if verdict.OK != (c.reason == "") || verdict.Reason != c.reason || verdict.Detail != c.detail {
				t.Fatalf("veredicto = %+v; se esperaba motivo %q y detalle %q", verdict, c.reason, c.detail)
			}
		})
	}
}
