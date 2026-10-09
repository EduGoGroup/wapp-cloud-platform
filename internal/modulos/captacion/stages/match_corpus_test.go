package stages_test

// Trozo de match_cascade_test.go (E-13): el CORPUS ADVERSARIO del match (hallazgo 40 de
// F6) — blancos Unicode, caracteres invisibles, acentos combinantes, dígitos que no son
// ASCII, separadores repetidos y skus casi iguales.
//
// Lo esperado de cada fila se fijó ejecutando el paquete viejo (internal/intake/stages @
// 4cd9cfb) sobre estas mismas entradas: es la equivalencia viejo ↔ nuevo, fijada a mano
// porque el paquete nuevo no importa el viejo. Una fila NO dice que su resultado sea el
// deseable: dice lo que el sistema hace hoy. Las que cobran una negación («sin» pegado
// a lo negado por un carácter que no es un blanco) están marcadas: son conducta del
// viejo que se conserva, no una decisión.
//
// 🔴 Los caracteres invisibles van ESCAPADOS, nunca literales, y este fichero se genera
// por script por eso mismo: un U+200B literal en el fuente no se ve en la revisión.

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
)

// corpusCatalog es la carta del corpus: una etiqueta larga con ñ y tag, la misma palabra
// corta y media, un acento, dos etiquetas con dígitos (una de 7 runas, donde cabe UNA
// edición, y una de 16, donde caben dos), la trampa de la negación y un artículo por
// variantes.
func corpusCatalog() catalogo.Catalog {
	return oneCategory(
		catalogo.Article{Code: "1", SKU: "TEQ-30", Label: "Tequeños congelados", Price: 490, Tags: []string{"congelados"}},
		catalogo.Article{Code: "2", SKU: "NOQ", Label: "Ñoquis", Price: 700},
		catalogo.Article{Code: "3", SKU: "NOQ-P", Label: "Ñoquis de papa", Price: 900},
		catalogo.Article{Code: "4", SKU: "CAFE", Label: "Café", Price: 120},
		catalogo.Article{Code: "5", SKU: "PACK-S", Label: "Pack 30", Price: 250},
		catalogo.Article{Code: "6", SKU: "PACK-30", Label: "Pack 30 unidades", Price: 300},
		catalogo.Article{Code: "7", SKU: "SAL", Label: "Sal", Price: 50},
		catalogo.Article{Code: "8", SKU: "QUESO", Label: "Queso", Price: 150},
		catalogo.Article{Code: "9", SKU: "TORTA", Label: "Torta de la casa", Variants: []catalogo.Variant{
			{Code: "10", Label: "10 porciones", Price: 2100},
			{Code: "12", Label: "12 porciones", Price: 2400},
			{Code: "G", Label: "Grande", Price: 3900},
		}},
	)
}

// productProbe es una fila del corpus de PRODUCTOS: el texto de P4 y la línea que sale.
// `kind` unmatched va con sku y estrategia vacíos y confianza 0.
type productProbe struct {
	name       string
	text       string
	kind       string
	sku        string
	strategy   string
	confidence float64 // a cuatro decimales
}

// adversarialProducts son los productos del corpus.
var adversarialProducts = []productProbe{
	{"same words as the label", "tequeños congelados", "matched", "TEQ-30", "exact", 1.0000},
	{"upper case and repeated spaces collapse", "  TEQUEÑOS    CONGELADOS  ", "matched", "TEQ-30", "exact", 1.0000},
	{"tab and newline between words are blanks", "tequeños\t\ncongelados", "matched", "TEQ-30", "exact", 1.0000},
	{"no-break space between words is a blank", "tequeños\u00a0congelados", "matched", "TEQ-30", "exact", 1.0000},
	{"em and ideographic spaces are blanks", "tequeños\u2003\u3000congelados", "matched", "TEQ-30", "exact", 1.0000},
	{"narrow no-break space and line separator are blanks", "tequeños\u202f\u2028congelados", "matched", "TEQ-30", "exact", 1.0000},
	{"zero width space instead of the blank is one edit", "tequeños\u200bcongelados", "matched", "TEQ-30", "fuzzy", 0.9474},
	{"zero width space inside a word is one edit", "teque\u200bños congelados", "matched", "TEQ-30", "fuzzy", 0.9500},
	{"byte order mark before the text is one edit", "\ufefftequeños congelados", "matched", "TEQ-30", "fuzzy", 0.9500},
	{"soft hyphen inside a word is one edit", "teque\u00adños congelados", "matched", "TEQ-30", "fuzzy", 0.9500},
	{"word joiner after the text is one edit", "tequeños congelados\u2060", "matched", "TEQ-30", "fuzzy", 0.9500},
	{"n with combining tilde is the same letter", "tequen\u0303os congelados", "matched", "TEQ-30", "exact", 1.0000},
	{"upper case n with combining tilde is the same letter", "TEQUEN\u0303OS CONGELADOS", "matched", "TEQ-30", "exact", 1.0000},
	{"n without tilde in a long label is one edit", "tequenos congelados", "matched", "TEQ-30", "fuzzy", 0.9474},
	{"n without tilde in a short label does not match", "noquis", "unmatched", "", "", 0.0000},
	{"n without tilde in a medium label is one edit", "noquis de papa", "matched", "NOQ-P", "fuzzy", 0.9286},
	{"combining acute accent folds like the precomposed one", "cafe\u0301", "matched", "CAFE", "exact", 1.0000},
	{"grave accent folds too", "cafè", "matched", "CAFE", "exact", 1.0000},
	{"upper case accented", "CAFÉ", "matched", "CAFE", "exact", 1.0000},
	{"a short word with one letter missing does not match", "caf", "unmatched", "", "", 0.0000},
	{"repeated comma instead of the blank is two edits", "tequeños,,congelados", "matched", "TEQ-30", "fuzzy", 0.9000},
	{"repeated dash instead of the blank is two edits", "tequeños--congelados", "matched", "TEQ-30", "fuzzy", 0.9000},
	{"repeated at sign instead of the blank is two edits", "tequeños@@congelados", "matched", "TEQ-30", "fuzzy", 0.9000},
	{"slash instead of the blank is one edit", "tequeños/congelados", "matched", "TEQ-30", "fuzzy", 0.9474},
	{"trailing punctuation is one edit", "tequeños congelados!", "matched", "TEQ-30", "fuzzy", 0.9500},
	{"three trailing dots are three edits of a long label and still match", "tequeños congelados...", "matched", "TEQ-30", "fuzzy", 0.8636},
	{"ascii digits in the label", "pack 30 unidades", "matched", "PACK-30", "exact", 1.0000},
	{"arabic-indic digits are two edits of a long label", "pack \u0663\u0660 unidades", "matched", "PACK-30", "fuzzy", 0.8750},
	{"fullwidth digits are two edits of a long label", "pack \uff13\uff10 unidades", "matched", "PACK-30", "fuzzy", 0.8750},
	{"arabic-indic digits in a short label do not match", "pack \u0663\u0660", "unmatched", "", "", 0.0000},
	{"a devanagari digit is one edit of a seven-rune label", "pack 3\u0966", "matched", "PACK-S", "fuzzy", 0.8571},
	{"a superscript digit is one edit of a seven-rune label", "pack 3\u2070", "matched", "PACK-S", "fuzzy", 0.8571},
	{"the sku written exactly", "PACK-30", "matched", "PACK-30", "sku", 1.0000},
	{"the sku with blanks around is trimmed first", " PACK-30\t", "matched", "PACK-30", "sku", 1.0000},
	{"the sku with no-break spaces around is trimmed first", "\u00a0PACK-30\u00a0", "matched", "PACK-30", "sku", 1.0000},
	{"the sku in lower case is not the sku and the sweep finds a neighbour label", "pack-30", "matched", "PACK-S", "fuzzy", 0.8571},
	{"the sku with a unicode hyphen is not the sku and the sweep finds a neighbour label", "PACK\u201030", "matched", "PACK-S", "fuzzy", 0.8571},
	{"the sku with a zero width space is not the sku", "PACK-30\u200b", "unmatched", "", "", 0.0000},
	{"a unique variant label", "grande", "matched", "TORTA#G", "variante", 1.0000},
	{"a unique variant label in upper case with blanks", "  GRANDE ", "matched", "TORTA#G", "variante", 1.0000},
	{"a numbered variant label", "12 porciones", "matched", "TORTA#12", "variante", 1.0000},
	{"a numbered variant label with repeated blanks", "12\u00a0\u00a0porciones", "matched", "TORTA#12", "variante", 1.0000},
	{"a variant label with fullwidth digits is not the variant", "\uff11\uff12 porciones", "unmatched", "", "", 0.0000},
	{"a unique tag", "congelados", "matched", "TEQ-30", "tag", 1.0000},
	{"a unique tag in upper case", "CONGELADOS", "matched", "TEQ-30", "tag", 1.0000},
	{"a tag with a zero width space is not the tag", "congelados\u200b", "unmatched", "", "", 0.0000},
	{"only a zero width space is a product nobody sells", "\u200b", "unmatched", "", "", 0.0000},
	{"only punctuation is a product nobody sells", "@@", "unmatched", "", "", 0.0000},
}

// TestMatchRun_AdversarialProducts: cada producto, solo en su pedido, contra la carta
// del corpus y sin zona gris.
func TestMatchRun_AdversarialProducts(t *testing.T) {
	idx := indexOf(t, corpusCatalog())
	for _, c := range adversarialProducts {
		t.Run(c.name, func(t *testing.T) {
			b := newMatchBench(t)
			art := b.run(t, stages.MatchInput{Quantities: p4Of(product(c.text)), Index: idx})
			assertLineCount(t, art, 2, "el producto y el envío")
			assertWarnings(t, art)

			line := art.Lines[0]
			if line.Kind != c.kind || line.SKU != c.sku {
				t.Fatalf("%q ⇒ kind %q, sku %q; el viejo daba kind %q, sku %q", c.text, line.Kind, line.SKU, c.kind, c.sku)
			}
			if c.kind == stages.KindUnmatched {
				if line.Match != nil || line.UnitPrice != nil {
					t.Fatalf("%q ⇒ unmatched con procedencia o precio: %+v", c.text, line)
				}
				return
			}
			if line.Match == nil || line.Match.Strategy != c.strategy {
				t.Fatalf("%q ⇒ procedencia %+v; el viejo daba la estrategia %q", c.text, line.Match, c.strategy)
			}
			if diff := line.Match.Confidence - c.confidence; diff > 1e-4 || diff < -1e-4 {
				t.Fatalf("%q ⇒ confianza %v; el viejo daba %v", c.text, line.Match.Confidence, c.confidence)
			}
		})
	}
}

// addonProbe es una fila del corpus de AÑADIDOS: el candidato y qué fue de él. `sku`
// vacío es «no fue línea»; `customization` es lo que quedó pegado al producto.
type addonProbe struct {
	name          string
	text          string
	sku           string
	strategy      string
	customization string
}

// adversarialAddons son los añadidos del corpus.
//
// ⚠️ Las cuatro filas `… is charged` son la conducta del viejo, conservada: la negación
// solo se reconoce cuando tras «sin» hay un BLANCO (de los que colapsa el normalizador)
// y el texto empieza por ella.
var adversarialAddons = []addonProbe{
	{"plain noun", "queso", "QUESO", "exact", ""},
	{"noun wrapped in filler", "extra de queso", "QUESO", "ngrama", ""},
	{"upper case and repeated blanks", "EXTRA   DE   QUESO", "QUESO", "ngrama", ""},
	{"no-break spaces between words", "extra\u00a0de\u00a0queso", "QUESO", "ngrama", ""},
	{"repeated separators between words", "extra,,de--queso", "QUESO", "ngrama", ""},
	{"zero width space glued to the noun still splits it", "extra de\u200bqueso", "QUESO", "ngrama", ""},
	{"zero width space inside the noun breaks it and the note drops the mark", "que\u200bso", "", "", "queso"},
	{"noun with a typo is not charged", "quezo", "", "", "quezo"},
	{"negation", "sin sal", "", "", "sin sal"},
	{"negation in upper case with repeated blanks", "SIN    SAL", "", "", "SIN SAL"},
	{"negation with a no-break space", "sin\u00a0sal", "", "", "sin sal"},
	{"negation with leading blanks", "  sin sal", "", "", "sin sal"},
	{"negation glued with a zero width space is charged", "sin\u200bsal", "SAL", "ngrama", ""},
	{"negation glued with a dash is charged", "sin-sal", "SAL", "ngrama", ""},
	{"negation glued with repeated commas is charged", "sin,,sal", "SAL", "ngrama", ""},
	{"negation after a byte order mark is charged", "\ufeffsin sal", "SAL", "ngrama", ""},
	{"the word sin alone is not a negation", "sin", "", "", "sin"},
	{"only blanks is ignored", " \u00a0\u2003", "", "", ""},
	{"only a zero width space leaves no instruction", "\u200b", "", "", ""},
	{"the sku of an article", "QUESO", "QUESO", "sku", ""},
	{"a unique variant label", "grande", "TORTA#G", "variante", ""},
	{"digits of another script", "pack \u0663\u0660", "", "", "pack \u0663\u0660"},
}

// TestMatchRun_AdversarialAddons: cada candidato como único añadido de «café» ×2.
func TestMatchRun_AdversarialAddons(t *testing.T) {
	idx := indexOf(t, corpusCatalog())
	for _, c := range adversarialAddons {
		t.Run(c.name, func(t *testing.T) {
			b := newMatchBench(t)
			art := b.run(t, stages.MatchInput{
				Quantities: p4Of(llm.NormalizedItem{Product: "café", Qty: 2, AddonCandidates: []string{c.text}, Evidence: "x"}),
				Index:      idx,
			})
			assertWarnings(t, art)
			assertMatched(t, art.Lines[0], "CAFE", "exact")
			if art.Lines[0].Customization != c.customization {
				t.Fatalf("%q ⇒ customization %q; el viejo daba %q", c.text, art.Lines[0].Customization, c.customization)
			}
			if c.sku == "" {
				assertLineCount(t, art, 2, "el café y el envío: el añadido no fue línea en el viejo")
				return
			}
			assertLineCount(t, art, 3, "el café, el añadido y el envío")
			assertMatched(t, art.Lines[1], c.sku, c.strategy)
			if art.Lines[1].Qty != 1 {
				t.Fatalf("%q ⇒ cantidad %d; el añadido va con 1", c.text, art.Lines[1].Qty)
			}
		})
	}
}

// rangeProbe es una fila del corpus de VARIANTES EN RANGO: los labels de las variantes
// de «Torta de la casa» (codes A, B, C… por posición), el rango pedido y lo que sale.
type rangeProbe struct {
	name     string
	labels   []string
	min, max int
	sku      string   // el de la línea: compuesto si se resolvió UNA
	options  []string // los sku ofrecidos, o nil si se resolvió
	warned   bool     // `rango_sin_variante`
}

// adversarialRanges son los rangos del corpus.
var adversarialRanges = []rangeProbe{
	{"fullwidth digits in a variant label are not a number", []string{"10 porciones", "\uff11\uff12 porciones", "Grande"}, 10, 12, "TORTA#A", nil, false},
	{"arabic-indic digits in every numbered label leave the range without variant", []string{"\u0661\u0660 porciones", "Grande"}, 10, 12, "TORTA", []string{"TORTA#A", "TORTA#B"}, true},
	{"a label with two numbers is a candidate by either", []string{"10-12 porciones", "25 porciones"}, 12, 20, "TORTA#A", nil, false},
	{"repeated and no-break blanks do not hide the number", []string{"10  porciones", "12\u00a0porciones"}, 10, 12, "TORTA", []string{"TORTA#A", "TORTA#B"}, false},
	{"a number that overflows is skipped and the next label still counts", []string{"99999999999999999999 porciones", "10 porciones"}, 1, 10, "TORTA#B", nil, false},
	{"an inverted range is read in order", []string{"10 porciones", "25 porciones"}, 12, 10, "TORTA#A", nil, false},
	{"a decimal is read as two integers", []string{"1.5 kg", "3 kg"}, 5, 5, "TORTA#A", nil, false},
	{"the limits of the range are included", []string{"9 porciones", "10 porciones", "12 porciones", "13 porciones"}, 10, 12, "TORTA", []string{"TORTA#B", "TORTA#C"}, false},
	{"digits glued to letters still count", []string{"x10", "x25"}, 10, 10, "TORTA#A", nil, false},
}

// TestMatchRun_AdversarialVariantRanges: qué cuenta como «un número dentro del rango» en
// el label de una variante.
func TestMatchRun_AdversarialVariantRanges(t *testing.T) {
	for _, c := range adversarialRanges {
		t.Run(c.name, func(t *testing.T) {
			article := catalogo.Article{Code: "1", SKU: "TORTA", Label: "Torta de la casa"}
			for i, label := range c.labels {
				article.Variants = append(article.Variants, catalogo.Variant{
					Code: string(rune('A' + i)), Label: label, Price: float64(1000 * (i + 1)),
				})
			}
			item := product("torta de la casa")
			item.Range = &llm.Range{Min: c.min, Max: c.max, Unit: "porciones"}

			b := newMatchBench(t)
			art := b.runItems(t, oneCategory(article), item)

			line := art.Lines[0]
			assertMatched(t, line, c.sku, "exact")
			offered := offeredSKUs(line)
			if !reflect.DeepEqual(offered, c.options) {
				t.Fatalf("variant_options = %q; el viejo ofrecía %q", offered, c.options)
			}
			if (line.UnitPrice != nil) != (len(c.options) == 0) {
				t.Fatalf("precio = %v con %d opciones: hay precio si y solo si la variante quedó resuelta", line.UnitPrice, len(offered))
			}
			if c.warned {
				assertWarnings(t, art, stages.Warning{ItemPos: 0, Reason: stages.WarningRangeWithoutVariant})
				return
			}
			assertWarnings(t, art)
		})
	}
}
