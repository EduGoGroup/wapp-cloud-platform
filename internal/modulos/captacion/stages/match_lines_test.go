package stages

// match_lines_test.go — LAS REGLAS DE match_lines.go, QUE NO TIENE EXPORTADOS (T-11).
//
// Nace con el verde (E-4) y es interno a propósito: el contrato de Match.Run ya recorre
// estas reglas desde fuera (match_items_test.go, match_corpus_test.go), pero un fallo en
// «qué cuenta como número» o en «qué es una negación» se diagnostica mal desde un
// presupuesto entero. Aquí están dichas sobre la función que decide. No se prueba la
// fontanería ni el envío, que ya cubre match_order_test.go.

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// TestNumbersIn_ReadsOnlyASCIIIntegers: qué es «un número» en el label de una variante.
func TestNumbersIn_ReadsOnlyASCIIIntegers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int
	}{
		{"no digits", "Grande", nil},
		{"empty", "", nil},
		{"two numbers", "10-12 porciones", []int{10, 12}},
		{"digits glued to letters", "x25u", []int{25}},
		{"leading zeros", "007 u", []int{7}},
		// El viejo no entiende decimales: el punto corta el número en dos.
		{"a decimal is read as two integers", "1.5 kg", []int{1, 5}},
		{"a minus sign is not part of the number", "-3", []int{3}},
		// Un dígito de otra escritura no es el dígito ASCII: no se lee.
		{"arabic-indic digits are not read", "١٢ porciones", nil},
		{"fullwidth digits are not read", "１２ porciones", nil},
		// El que desborda se SALTA y el siguiente se sigue leyendo.
		{"an overflowing number is skipped, not fatal", "99999999999999999999999 o 12", []int{12}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := numbersIn(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("numbersIn(%q) = %v, se esperaba %v", c.in, got, c.want)
			}
		})
	}
}

// sizes es un artículo con cinco presentaciones: tres con un número, una con dos y una
// sin ninguno.
func sizes() catalogo.Article {
	return catalogo.Article{Code: "1", SKU: "TORTA", Label: "Torta", Variants: []catalogo.Variant{
		{Code: "10", Label: "10 porciones", Price: 2100},
		{Code: "12", Label: "12 porciones", Price: 2400},
		{Code: "25", Label: "25 porciones", Price: 3900},
		{Code: "MIX", Label: "de 30 a 40 porciones", Price: 5000},
		{Code: "G", Label: "Grande", Price: 9000},
	}}
}

// TestVariantsInRange_CandidatesAreThoseNamingANumberInside: extremos incluidos, rango
// invertido ordenado, y cada variante como mucho una vez.
func TestVariantsInRange_CandidatesAreThoseNamingANumberInside(t *testing.T) {
	cases := []struct {
		name string
		rng  *llm.Range
		want []int
	}{
		{"no range: nothing to filter with", nil, nil},
		{"both ends are included", &llm.Range{Min: 10, Max: 12}, []int{0, 1}},
		{"just below the lower end", &llm.Range{Min: 11, Max: 12}, []int{1}},
		{"just above the upper end", &llm.Range{Min: 10, Max: 11}, []int{0}},
		{"a single point", &llm.Range{Min: 25, Max: 25}, []int{2}},
		{"an inverted range is ordered", &llm.Range{Min: 26, Max: 12}, []int{1, 2}},
		{"a variant with two numbers inside counts once", &llm.Range{Min: 30, Max: 40}, []int{3}},
		{"one of its two numbers is enough", &llm.Range{Min: 35, Max: 45}, []int{3}},
		{"nothing sold in the range", &llm.Range{Min: 50, Max: 60}, nil},
		{"a variant without numbers is never a candidate", &llm.Range{Min: 0, Max: 1000}, []int{0, 1, 2, 3}},
		// La unidad no se compara: el catálogo no declara la de sus variantes.
		{"the unit is not compared", &llm.Range{Min: 12, Max: 12, Unit: "kilos"}, []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := variantsInRange(sizes(), c.rng); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("variantsInRange(%+v) = %v, se esperaba %v", c.rng, got, c.want)
			}
		})
	}
}

// TestWithArticle_NeverChoosesTheVariantForTheCustomer recorre las situaciones de casar
// con un artículo. En todas la línea sale `matched` y con la procedencia del hallazgo.
func TestWithArticle_NeverChoosesTheVariantForTheCustomer(t *testing.T) {
	plain := catalogo.Article{Code: "2", SKU: "QUESO", Label: "Queso", Price: 150}
	all := []string{"TORTA#10", "TORTA#12", "TORTA#25", "TORTA#MIX", "TORTA#G"}
	cases := []struct {
		name    string
		found   indice.Coincidencia
		rng     *llm.Range
		sku     string
		label   string
		price   float64 // -1 = sin precio
		options []string
		reason  string
	}{
		{"the variant came resolved: the range is not even looked at",
			indice.Coincidencia{Articulo: sizes(), Variante: sizes().Variants[4], HayVariante: true},
			&llm.Range{Min: 10, Max: 10}, "TORTA#G", "Torta — Grande", 9000, nil, ""},
		{"an article without variants takes its own price",
			indice.Coincidencia{Articulo: plain}, &llm.Range{Min: 1, Max: 2}, "QUESO", "Queso", 150, nil, ""},
		{"the range points at exactly one variant",
			indice.Coincidencia{Articulo: sizes()}, &llm.Range{Min: 20, Max: 29}, "TORTA#25", "Torta — 25 porciones", 3900, nil, ""},
		{"the range points at several: only those are offered",
			indice.Coincidencia{Articulo: sizes()}, &llm.Range{Min: 12, Max: 25}, "TORTA", "Torta", -1,
			[]string{"TORTA#12", "TORTA#25"}, ""},
		{"no range: all are offered and it is NOT a warning",
			indice.Coincidencia{Articulo: sizes()}, nil, "TORTA", "Torta", -1, all, ""},
		{"range without variant: all are offered WITH the warning",
			indice.Coincidencia{Articulo: sizes()}, &llm.Range{Min: 50, Max: 60}, "TORTA", "Torta", -1, all, WarningRangeWithoutVariant},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := Line{Kind: KindUnmatched, Label: "lo que dijo el cliente", Qty: 3, Evidence: "frase"}
			f := finding{match: c.found, provenance: MatchProvenance{Strategy: "fuzzy", Confidence: 0.9}}
			line, reason := withArticle(base, f, c.rng)

			if reason != c.reason {
				t.Fatalf("motivo = %q, se esperaba %q", reason, c.reason)
			}
			assertCopiedArticle(t, line, f, c.sku, c.label, c.price, c.options)
		})
	}
}

// TestWithArticle_EachOptionCarriesItsOwnLabelAndPrice: la etiqueta compuesta y el
// precio de ESA variante, no el del artículo.
func TestWithArticle_EachOptionCarriesItsOwnLabelAndPrice(t *testing.T) {
	line, _ := withArticle(Line{}, finding{match: indice.Coincidencia{Articulo: sizes()}}, &llm.Range{Min: 10, Max: 12})
	want := []VariantOption{
		{SKU: "TORTA#10", Label: "Torta — 10 porciones", Price: 2100},
		{SKU: "TORTA#12", Label: "Torta — 12 porciones", Price: 2400},
	}
	if !reflect.DeepEqual(line.VariantOptions, want) {
		t.Fatalf("opciones = %+v, se esperaban %+v", line.VariantOptions, want)
	}
}

// assertCopiedArticle comprueba lo que withArticle deja en la línea: sku, etiqueta,
// procedencia, precio (-1 = sin precio) y opciones, sin tocar lo que traía la base.
func assertCopiedArticle(t *testing.T, line Line, f finding, sku, label string, price float64, options []string) {
	t.Helper()
	if line.Kind != KindMatched || line.SKU != sku || line.Label != label {
		t.Fatalf("línea = %s %q %q; se esperaba matched %q %q", line.Kind, line.SKU, line.Label, sku, label)
	}
	if line.Match == nil || *line.Match != f.provenance {
		t.Fatalf("procedencia = %+v; se copia la del hallazgo", line.Match)
	}
	if line.Qty != 3 || line.Evidence != "frase" {
		t.Fatalf("la línea perdió lo que traía la base: %+v", line)
	}
	switch {
	case price < 0 && line.UnitPrice != nil:
		t.Fatalf("precio = %v; con la variante abierta lo pone el dueño", *line.UnitPrice)
	case price >= 0 && (line.UnitPrice == nil || *line.UnitPrice != price):
		t.Fatalf("precio = %v, se esperaba %v", line.UnitPrice, price)
	}
	offered := make([]string, 0, len(line.VariantOptions))
	for _, o := range line.VariantOptions {
		offered = append(offered, o.SKU)
	}
	if !slices.Equal(offered, options) {
		t.Fatalf("variant_options = %q, se esperaban %q", offered, options)
	}
}

// addonScanner es una carta que vende lo que un cliente suele NEGAR, y un artículo cuya
// etiqueta empieza ella misma por la negación.
func addonScanner(t *testing.T) *scanner {
	t.Helper()
	cat := catalogo.Catalog{Categories: []catalogo.Category{{Code: "1", Label: "Extras", Items: []catalogo.Article{
		{Code: "1", SKU: "SAL", Label: "Sal", Price: 50},
		{Code: "2", SKU: "QUESO", Label: "Queso", Price: 150},
		{Code: "3", SKU: "PAN-SG", Label: "Sin gluten", Price: 400},
	}}}}
	idx, err := indice.Construir(cat, textmatch.Normalize)
	if err != nil {
		t.Fatalf("construir el índice del fixture: %v", err)
	}
	return newScanner(idx)
}

// TestAddonLines_NegationGuardAndItsEdges: «sin X» nunca es un añadido facturable de X.
// La guarda mira el texto NORMALIZADO y solo conoce el prefijo «sin » —con su espacio—:
// los bordes de abajo son los del viejo, también los que son un defecto.
func TestAddonLines_NegationGuardAndItsEdges(t *testing.T) {
	cases := []struct {
		name  string
		addon string
		sku   string // "" = no es línea: vuelve como indicación
		back  string
	}{
		{"plain negation", "sin sal", "", "sin sal"},
		{"uppercase and surrounding blanks: the text returns trimmed, not normalized", "  SIN Sal ", "", "SIN Sal"},
		{"accented and with repeated blanks", "Sín   sal", "", "Sín   sal"},
		{"a tab counts as the blank of the prefix", "sin\tsal", "", "sin\tsal"},
		// La guarda va ANTES que el catálogo: aunque el dueño venda un «Sin gluten».
		{"the guard wins even over an article labelled with the negation", "sin gluten", "", "sin gluten"},
		{"negation of something billable in the middle of the phrase", "sin nada de queso", "", "sin nada de queso"},
		// Bordes: sin el espacio no es la negación.
		{"a word that merely starts with sin", "sinfonía de queso", "QUESO", ""},
		{"the bare word, once trimmed, has no blank after it", "sin ", "", "sin"},
		// 🔴 Defecto conservado del viejo: la guarda solo ve «sin » AL PRINCIPIO.
		{"another negation word is not seen", "nada de sal", "SAL", ""},
		{"a negation that is not at the start is not seen", "queso sin sal", "QUESO", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, back := addonLines(addonScanner(t), []string{c.addon}, "la frase del ítem")
			if c.sku == "" {
				if len(lines) != 0 || !reflect.DeepEqual(back, []string{c.back}) {
					t.Fatalf("líneas = %+v, indicaciones = %q; se esperaba solo la indicación %q", lines, back, c.back)
				}
				return
			}
			if len(lines) != 1 || len(back) != 0 {
				t.Fatalf("líneas = %+v, indicaciones = %q; se esperaba UNA línea %s", lines, back, c.sku)
			}
			l := lines[0]
			if l.SKU != c.sku || l.Kind != KindMatched || l.Qty != 1 || l.Evidence != "la frase del ítem" || l.Range != nil {
				t.Fatalf("línea = %+v; el añadido es matched, de cantidad 1, con la evidencia del ítem y sin rango", l)
			}
		})
	}
}

// TestInstruction_JoinsTrimsAndNeverTruncates: la única ranura de la línea.
func TestInstruction_JoinsTrimsAndNeverTruncates(t *testing.T) {
	var buf bytes.Buffer
	s := &Match{log: logger.New(logger.WithWriter(&buf))}

	art := &MatchArtifact{}
	if got := s.instruction(art, 0, []string{"  sin sal ", "", "   ", "bien cocida\n"}); got != "sin sal, bien cocida" {
		t.Fatalf("indicación = %q; se recorta cada parte, se saltan las vacías y se unen con coma", got)
	}
	if got := s.instruction(art, 0, []string{" ", ""}); got != "" {
		t.Fatalf("indicación = %q; sin partes con texto no hay indicación", got)
	}

	// El borde: dos partes que, CON su separador, miden exactamente el tope, caben.
	half := (intakes.MaxNoteRunes - len(noteSeparator)) / 2
	left, right := strings.Repeat("a", half), strings.Repeat("ñ", intakes.MaxNoteRunes-len(noteSeparator)-half)
	if got := s.instruction(art, 0, []string{left, right}); got != left+noteSeparator+right {
		t.Fatalf("una indicación de %d runas cabe entera; salió de %d", intakes.MaxNoteRunes, len([]rune(got)))
	}
	if len(art.Warnings) != 0 || buf.Len() != 0 {
		t.Fatalf("nada de lo anterior es un aviso: %+v / %q", art.Warnings, buf.String())
	}

	// Una runa más: no se trunca, se descarta ENTERA, con su aviso en la posición.
	if got := s.instruction(art, 7, []string{left, right + "ñ"}); got != "" {
		t.Fatalf("indicación = %q; o cabe entera o no va", got)
	}
	if !reflect.DeepEqual(art.Warnings, []Warning{{ItemPos: 7, Reason: WarningNoteTooLong}}) {
		t.Fatalf("avisos = %+v; se esperaba `indicacion_larga` en la posición 7", art.Warnings)
	}
	if log := buf.String(); !strings.Contains(log, "item_pos=7") || strings.Contains(log, "ñññ") {
		t.Fatalf("el Warn lleva la posición y no cita el texto del cliente: %q", log)
	}
}
