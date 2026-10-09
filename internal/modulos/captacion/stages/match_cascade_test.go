//go:build pendiente

package stages_test

// match_cascade_test.go — el contrato de match_cascade.go: el umbral que NO se baja
// (D-044.45), los escalones por clave y sus desempates, el barrido y la zona gris. El
// diferencial contra el oráculo ingenuo y el prefiltro están en
// match_cascade_sweep_test.go; el corpus adversario, en match_corpus_test.go.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// product es el ítem mínimo: un producto con cantidad 1.
func product(text string) llm.NormalizedItem {
	return llm.NormalizedItem{Product: text, Qty: 1, Evidence: "x"}
}

// ---------------------------------------------------------------------------
// D-044.45 — EL UMBRAL SE QUEDA EN 0,85
// ---------------------------------------------------------------------------

// TestDefaultCascade_ThresholdIs085AndBothFactsAreFixed.
//
// 🔴 FIJA LOS DOS HECHOS A LA VEZ: que a 0,85 «ñoquis»/«noquis» NO casa, y que a 0,80
// sí. Así ninguno de los dos se puede mover en silencio para que un criterio salga verde.
func TestDefaultCascade_ThresholdIs085AndBothFactsAreFixed(t *testing.T) {
	if textmatch.DefaultFuzzyThreshold != 0.85 {
		t.Fatalf("umbral canónico = %v; D-044.45 lo fija en 0,85", textmatch.DefaultFuzzyThreshold)
	}
	cascade := stages.DefaultCascade()
	if cascade == nil {
		t.Fatal("DefaultCascade devolvió nil")
	}
	if cascade.HasGrayZone() {
		t.Fatal("el comparador del barrido es determinista POR CONSTRUCCIÓN: el escalón caro no va cableado en él")
	}

	cases := []struct {
		name       string
		a, b       string
		outcome    textmatch.Outcome
		confidence float64
		strategy   string
	}{
		{"six runes one edit does not match", "ñoquis", "noquis", textmatch.OutcomeNoMatch, 0.8333, "fuzzy"},
		{"neighbour articles one edit apart do not match", "torta", "tarta", textmatch.OutcomeNoMatch, 0.80, "fuzzy"},
		{"nineteen runes one edit matches", "tequenos congelados", "Tequeños congelados", textmatch.OutcomeMatch, 0.9473, "fuzzy"},
		{"equal after normalizing is the exact step", "TEQUEÑOS  congelados", "Tequeños congelados", textmatch.OutcomeMatch, 1, "exact"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := cascade.Compare(context.Background(), c.a, c.b)
			if err != nil {
				t.Fatalf("la cascada por defecto no falla nunca: %v", err)
			}
			if r.Outcome != c.outcome || r.Strategy != c.strategy {
				t.Fatalf("%q vs %q = %v por %q; se esperaba %v por %q", c.a, c.b, r.Outcome, r.Strategy, c.outcome, c.strategy)
			}
			if diff := r.Confidence - c.confidence; diff > 1e-4 || diff < -1e-4 {
				t.Fatalf("confianza = %v, se esperaba %v", r.Confidence, c.confidence)
			}
		})
	}

	// El MISMO par con el umbral que se descartó (0,80) sí casaría: por eso NO se bajó.
	r80, err := textmatch.NewFuzzy(0.80).Compare(context.Background(), "ñoquis", "noquis")
	if err != nil || r80.Outcome != textmatch.OutcomeMatch {
		t.Fatalf("con 0,80 = (%v, %v); el hecho que justifica no bajar el umbral dejó de ser cierto", r80.Outcome, err)
	}
}

// TestLengthMargin_IsTheTableOfD04445: el margen está DERIVADO del umbral, y las
// ediciones que caben son su suelo. Literales escritos a mano.
func TestLengthMargin_IsTheTableOfD04445(t *testing.T) {
	if diff := stages.LengthMargin - 0.15; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("LengthMargin = %v; es 1 − 0,85: si el umbral se mueve, el prefiltro también", stages.LengthMargin)
	}
	table := []struct{ runes, edits int }{
		{3, 0}, {4, 0}, {5, 0}, // «pan», «café», «torta»: ni una errata
		{6, 0},  // «ñoquis» ⇒ por esto NO casa «noquis»
		{7, 1},  // desde aquí sí cabe UNA
		{8, 1},  // «tequeños»
		{19, 2}, // «tequeños congelados»
		{20, 3}, {40, 6},
	}
	for _, row := range table {
		if got := int(stages.LengthMargin * float64(row.runes)); got != row.edits {
			t.Errorf("con %d runas caben %d ediciones; la tabla de D-044.45 dice %d", row.runes, got, row.edits)
		}
	}
}

// TestMatchRun_OneTypoIsForgivenFromSevenRunes es la tabla vista DESDE la etapa: la
// misma errata casa en una etiqueta de 7 runas y no en una de 6.
func TestMatchRun_OneTypoIsForgivenFromSevenRunes(t *testing.T) {
	cat := oneCategory(
		catalogo.Article{Code: "1", SKU: "SEIS", Label: "Ñoquis", Price: 100},
		catalogo.Article{Code: "2", SKU: "SIETE", Label: "Lasagna", Price: 200},
		catalogo.Article{Code: "3", SKU: "TORTA", Label: "Torta", Price: 300},
	)
	b := newMatchBench(t)
	art := b.runItems(t, cat, product("noquis"), product("lasagma"), product("tarta"))

	assertUnmatched(t, art.Lines[0], "noquis")
	assertMatched(t, art.Lines[1], "SIETE", "fuzzy")
	if diff := art.Lines[1].Match.Confidence - (1 - 1.0/7.0); diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("confianza = %v; es la similitud que declara el comparador", art.Lines[1].Match.Confidence)
	}
	assertUnmatched(t, art.Lines[2], "tarta") // 🔴 con 0,80 llevaría la Torta: el artículo equivocado
}

// ---------------------------------------------------------------------------
// LOS ESCALONES POR CLAVE
// ---------------------------------------------------------------------------

// TestStrategies_ValuesAreLiteral: viajan en el artefacto y en la revisión.
func TestStrategies_ValuesAreLiteral(t *testing.T) {
	got := []string{stages.StrategySKU, stages.StrategyExact, stages.StrategyVariant, stages.StrategyTag, stages.StrategyNGram}
	want := []string{"sku", "exact", "variante", "tag", "ngrama"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("estrategias = %q, se esperaba %q", got, want)
	}
}

// TestMatchRun_KeySteps recorre los cuatro accesos por clave, sus desempates y su orden.
// El espía demuestra que lo que casa por clave NO paga el barrido.
func TestMatchRun_KeySteps(t *testing.T) {
	cat := oneCategory(
		catalogo.Article{Code: "1", SKU: "TEQ-30", Label: "Tequeños congelados", Price: 490, Tags: []string{"congelados", "Fritura"}},
		catalogo.Article{Code: "2", SKU: "A", Label: "Alfajor", Price: 100, Tags: []string{"vegano"}},
		catalogo.Article{Code: "3", SKU: "B", Label: "Brownie", Price: 200, Tags: []string{"vegano"}},
		catalogo.Article{Code: "4", SKU: "C", Label: "Cheesecake", Price: 300, Tags: []string{"sin gluten"}},
		catalogo.Article{Code: "5", SKU: "DUP-1", Label: "Medialuna", Price: 50},
		catalogo.Article{Code: "6", SKU: "DUP-2", Label: "MEDIALUNA", Price: 60},
		catalogo.Article{Code: "7", SKU: "PIZZA", Label: "Pizza", Variants: []catalogo.Variant{
			{Code: "G", Label: "Grande", Price: 9000}, {Code: "F", Label: "Familiar", Price: 12000},
		}},
		catalogo.Article{Code: "8", SKU: "EMP", Label: "Empanadas", Variants: []catalogo.Variant{
			{Code: "G", Label: "Grande", Price: 4000}, {Code: "D", Label: "Docena", Price: 7000},
		}},
		catalogo.Article{Code: "9", SKU: "alfajor", Label: "Caja sorpresa", Price: 999},
		catalogo.Article{Code: "10", SKU: "FRIT", Label: "Fritura", Price: 800},
	)
	cases := []struct {
		name     string
		text     string
		sku      string // "" = unmatched
		strategy string
		label    string
		price    float64
	}{
		{"the sku written exactly", "TEQ-30", "TEQ-30", "sku", "Tequeños congelados", 490},
		{"the sku is opaque: lower case and blank are not it", "teq 30", "", "", "", 0},
		{"the sku goes before the label of another article", "alfajor", "alfajor", "sku", "Caja sorpresa", 999},
		{"the label after normalizing", "  ALFAJOR ", "A", "exact", "Alfajor", 100},
		{"duplicated labels: the first of the document", "medialuna", "DUP-1", "exact", "Medialuna", 50},
		{"a variant label of ONE article resolves that variant", "docena", "EMP#D", "variante", "Empanadas — Docena", 7000},
		{"a variant label of TWO articles decides nothing", "grande", "", "", "", 0},
		{"a tag of ONE article", "Sin Gluten", "C", "tag", "Cheesecake", 300},
		{"a tag of TWO articles decides nothing", "vegano", "", "", "", 0},
		{"the label goes before the tag of another article", "fritura", "FRIT", "exact", "Fritura", 800},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newSpy()
			b := newMatchBench(t, stages.WithComparator(spy))
			art := b.runItems(t, cat, product(c.text))

			line := art.Lines[0]
			if c.sku == "" {
				assertUnmatched(t, line, strings.TrimSpace(c.text))
				return
			}
			assertMatched(t, line, c.sku, c.strategy)
			assertPrice(t, line, c.price)
			if line.Label != c.label || line.Match.Confidence != 1 {
				t.Fatalf("línea = etiqueta %q, confianza %v; se esperaba %q y 1", line.Label, line.Match.Confidence, c.label)
			}
			if spy.calls != 0 {
				t.Fatalf("lo que casa por clave no paga el barrido: %d comparaciones", spy.calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EL BARRIDO
// ---------------------------------------------------------------------------

// TestMatchRun_SweepKeepsTheBestNotTheFirst: con dos artículos que pasan el umbral gana
// el más parecido aunque esté después; y a igualdad, el primero del documento.
func TestMatchRun_SweepKeepsTheBestNotTheFirst(t *testing.T) {
	cat := oneCategory(
		catalogo.Article{Code: "1", SKU: "FAR", Label: "Empanada de carne", Price: 100},
		catalogo.Article{Code: "2", SKU: "NEAR", Label: "Empanada de carna", Price: 200},
		catalogo.Article{Code: "3", SKU: "TIE-1", Label: "Milanesa napolitana a", Price: 300},
		catalogo.Article{Code: "4", SKU: "TIE-2", Label: "Milanesa napolitana b", Price: 400},
	)
	b := newMatchBench(t)
	art := b.runItems(t, cat, product("empanada de carnas"), product("milanesa napolitana c"))

	// «carnas» está a 1 edición de «carna» y a 2 de «carne».
	assertMatched(t, art.Lines[0], "NEAR", "fuzzy")
	assertMatched(t, art.Lines[1], "TIE-1", "fuzzy")
}

// TestMatchRun_ProductIsSearchedWholeNeverByPieces: lo contrario —n-gramas, como en los
// añadidos— casaría «torta de chocolate» con «Chocolate» y cobraría una tableta.
func TestMatchRun_ProductIsSearchedWholeNeverByPieces(t *testing.T) {
	cat := oneCategory(catalogo.Article{Code: "1", SKU: "CHOC", Label: "Chocolate", Price: 500})
	b := newMatchBench(t)
	art := b.runItems(t, cat, product("  torta de chocolate \t"))
	// Y la etiqueta del renglón es el producto SIN los blancos de los bordes.
	assertUnmatched(t, art.Lines[0], "torta de chocolate")
}

// TestMatchRun_AFailingComparatorIsLoggedNotDisguised: es un defecto nuestro. La línea
// sale sin match, el job sigue, la zona gris no se consulta y queda un Error en el log.
func TestMatchRun_AFailingComparatorIsLoggedNotDisguised(t *testing.T) {
	spy := newSpy()
	spy.failWith = errGrayZone
	// La sonda comparte el token «congelados» con una etiqueta: la zona gris TENDRÍA un
	// candidato que ofrecer, y aun así no se le pregunta.
	gz := &fakeGrayZone{answers: map[string]int{"tequenos congelados": 0}}
	b := newMatchBench(t, stages.WithComparator(spy), stages.WithGrayZone(gz))

	art := b.runItems(t, ambarCatalog(), product("queso"), product("tequenos congelados"))

	assertMatched(t, art.Lines[0], "QUESO", "exact") // el que casó por clave ni se entera
	assertUnmatched(t, art.Lines[1], "tequenos congelados")
	if spy.calls == 0 {
		t.Fatal("el comparador no llegó a ejecutarse: el test no mide nada")
	}
	if len(gz.asked) != 0 || art.GrayZoneCalls != 0 {
		t.Fatalf("tras un comparador roto no se consulta la zona gris: %q", gz.asked)
	}
	assertWarnings(t, art)
	log := b.log.String()
	if !strings.Contains(log, "match: el comparador determinista falló; el ítem sale sin match") ||
		!strings.Contains(log, "level=ERROR") || !strings.Contains(log, "item_pos=1") {
		t.Fatalf("el fallo del comparador no quedó a nivel Error con la posición: %q", log)
	}
	if strings.Contains(log, "tequenos") {
		t.Fatalf("el log cita el producto del cliente: %q", log)
	}
}

// ---------------------------------------------------------------------------
// LA ZONA GRIS
// ---------------------------------------------------------------------------

// TestMatchRun_WithoutGrayZoneTheDraftComesOutAnyway (R-04, que es como corre en
// producción): su ausencia se paga en renglones para el dueño, no en errores.
func TestMatchRun_WithoutGrayZoneTheDraftComesOutAnyway(t *testing.T) {
	b := newMatchBench(t) // sin WithGrayZone
	art := b.runItems(t, ambarCatalog(), ambarItems()...)

	if art.GrayZoneCalls != 0 {
		t.Fatalf("gray_zone_calls = %d sin zona gris", art.GrayZoneCalls)
	}
	assertUnmatched(t, art.Lines[0], "torta de chocolate")
	if art.Lines[0].Customization != "sin lactosa" || art.Lines[0].Range == nil {
		t.Fatalf("el unmatched conserva la indicación y el rango del cliente: %+v", art.Lines[0])
	}
	assertPrice(t, *lineWithSKU(art, "DECO-INF"), 800)
	assertPrice(t, *lineWithSKU(art, "TEQ-30"), 490)
	assertWarnings(t, art) // no casar NO es un aviso: es el renglón que precifica la dueña
}

// TestMatchRun_GrayZoneDownDegradesTheItemNotTheJob: el escalón caro es el TERCERO de
// una cascada cuyos dos primeros ya corrieron.
func TestMatchRun_GrayZoneDownDegradesTheItemNotTheJob(t *testing.T) {
	gz := &fakeGrayZone{err: errGrayZone}
	b := newMatchBench(t, stages.WithGrayZone(gz))
	art := b.runItems(t, ambarCatalog(), product("torta de chocolate"), product("tequeños congelados"))

	assertUnmatched(t, art.Lines[0], "torta de chocolate")
	assertPrice(t, *lineWithSKU(art, "TEQ-30"), 490)
	assertWarnings(t, art, stages.Warning{ItemPos: 0, Reason: stages.WarningGrayZoneDown})
	if art.GrayZoneCalls != 1 {
		t.Fatalf("gray_zone_calls = %d; la llamada que falló también se cuenta", art.GrayZoneCalls)
	}
}

// TestMatchRun_GrayZoneAnswerOutOfTheListIsNoneOfThem: «ninguno corresponde» es una
// respuesta válida, no un fallo: `unmatched` SIN aviso.
func TestMatchRun_GrayZoneAnswerOutOfTheListIsNoneOfThem(t *testing.T) {
	for _, answer := range []int{-1, 1, 99} { // se ofrece UN candidato: el único índice válido es 0
		t.Run(fmt.Sprintf("index %d", answer), func(t *testing.T) {
			gz := &fakeGrayZone{answers: map[string]int{"torta de chocolate": answer}}
			b := newMatchBench(t, stages.WithGrayZone(gz))
			art := b.runItems(t, ambarCatalog(), product("torta de chocolate"))

			assertUnmatched(t, art.Lines[0], "torta de chocolate")
			assertWarnings(t, art)
			if art.GrayZoneCalls != 1 || len(gz.offered) != 1 || len(gz.offered[0]) != 1 {
				t.Fatalf("consultas = %d, candidatos = %q", art.GrayZoneCalls, gz.offered)
			}
		})
	}
}

// grayZoneCatalog es una carta para medir la PRESELECCIÓN: siete artículos comparten
// tokens con «torta de chocolate con crema», en grados distintos.
func grayZoneCatalog() catalogo.Catalog {
	return oneCategory(
		catalogo.Article{Code: "1", SKU: "T1", Label: "Torta selva negra", Price: 1},                 // 1: torta
		catalogo.Article{Code: "2", SKU: "T2", Label: "Chocolate en rama", Price: 2},                 // 1: chocolate
		catalogo.Article{Code: "3", SKU: "T3", Label: "Torta húmeda de chocolate y crema", Price: 3}, // 3
		catalogo.Article{Code: "4", SKU: "T4", Label: "Crema chantilly", Price: 4},                   // 1: crema
		catalogo.Article{Code: "5", SKU: "T5", Label: "Torta + chocolate", Price: 5},                 // 2
		catalogo.Article{Code: "6", SKU: "T6", Label: "Pan de campo", Price: 6},                      // 0: «de» no cuenta
		catalogo.Article{Code: "7", SKU: "T7", Label: "Torta de ricota", Price: 7},                   // 1: torta
		catalogo.Article{Code: "8", SKU: "T8", Label: "Bombón con chocolate", Price: 8},              // 2: con, chocolate
		catalogo.Article{Code: "9", SKU: "T9", Label: "Jugo de naranja", Price: 9},                   // 0
	)
}

// TestMatchRun_GrayZoneCandidatesArePreselectedByTokenOverlap: como mucho 5, los que más
// tokens comparten primero, a igualdad por orden de documento, con la etiqueta tal como
// la escribió el dueño; y el índice que conteste el modelo es el de ESA lista.
func TestMatchRun_GrayZoneCandidatesArePreselectedByTokenOverlap(t *testing.T) {
	if stages.MaxGrayZoneCandidates != 5 {
		t.Fatalf("MaxGrayZoneCandidates = %d; la cota de prompt es 5", stages.MaxGrayZoneCandidates)
	}
	gz := &fakeGrayZone{answers: map[string]int{"torta de chocolate con crema": 2}}
	b := newMatchBench(t, stages.WithGrayZone(gz))
	art := b.runItems(t, grayZoneCatalog(), product("  torta de chocolate con crema "))

	want := []string{
		"Torta húmeda de chocolate y crema", // 3 tokens
		"Torta + chocolate",                 // 2, antes en el documento
		"Bombón con chocolate",              // 2
		"Torta selva negra",                 // 1, por orden de documento…
		"Chocolate en rama",                 // …y hasta aquí: «Crema chantilly» y «Torta de ricota» ya no caben
	}
	if len(gz.asked) != 1 || gz.asked[0] != "torta de chocolate con crema" {
		t.Fatalf("se preguntó por %q; UNA vez, con el producto sin blancos en los bordes", gz.asked)
	}
	if !reflect.DeepEqual(gz.offered[0], want) {
		t.Fatalf("candidatos = %q\nse esperaban %q", gz.offered[0], want)
	}
	// El índice 2 es «Bombón con chocolate», no el tercer artículo del catálogo.
	assertMatched(t, art.Lines[0], "T8", grayZoneName)
	assertPrice(t, art.Lines[0], 8)
	if art.Lines[0].Match.Confidence != 0.91 {
		t.Fatalf("confianza = %v; es la que declara la zona gris", art.Lines[0].Match.Confidence)
	}
}

// TestMatchRun_GrayZoneIsNotAskedWithoutCandidates: sin nada que ofrecer, preguntar
// sería gastar una llamada para que el modelo conteste «ninguno» sobre una lista vacía.
func TestMatchRun_GrayZoneIsNotAskedWithoutCandidates(t *testing.T) {
	cases := []struct{ name, text string }{
		{"no token in common", "bicicleta rodado veinte"},
		// Comparte «té», «de» y «la» con el primer artículo, y ninguno cuenta.
		{"only tokens shorter than three runes", "té de la"},
	}
	cat := oneCategory(
		catalogo.Article{Code: "1", SKU: "X", Label: "Té de la casa", Price: 1},
		catalogo.Article{Code: "2", SKU: "Y", Label: "Jugo de naranja", Price: 2},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gz := &fakeGrayZone{}
			b := newMatchBench(t, stages.WithGrayZone(gz))
			art := b.runItems(t, cat, product(c.text))
			assertUnmatched(t, art.Lines[0], c.text)
			if len(gz.asked) != 0 || art.GrayZoneCalls != 0 {
				t.Fatalf("se consultó a la zona gris sin candidatos: %q con %q", gz.asked, gz.offered)
			}
		})
	}
}

// TestMatchRun_GrayZoneIsAskedOncePerUncoveredItemOnly: ni por el ítem que ya casó, ni
// por las personalizaciones, ni por los añadidos.
func TestMatchRun_GrayZoneIsAskedOncePerUncoveredItemOnly(t *testing.T) {
	gz := &fakeGrayZone{}
	b := newMatchBench(t, stages.WithGrayZone(gz))
	art := b.runItems(t, ambarCatalog(),
		llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Customizations: []string{"torta aparte"}, AddonCandidates: []string{"torta extra"}, Evidence: "x"},
		llm.NormalizedItem{Product: "torta helada", Qty: 1, Customizations: []string{"sin sal"}, Evidence: "x"},
		llm.NormalizedItem{Product: "torta helada", Qty: 1, Evidence: "x"},
	)
	if !reflect.DeepEqual(gz.asked, []string{"torta helada", "torta helada"}) || art.GrayZoneCalls != 2 {
		t.Fatalf("consultas = %q (contador %d); una por ítem no cubierto, aunque el texto se repita", gz.asked, art.GrayZoneCalls)
	}
}

// TestIndexAndSweepShareOneNormalizer es R-05 visto desde aquí: el normalizador con el
// que se construye el índice de los tests —y el de producción— es `textmatch.Normalize`,
// el del barrido, y el índice lo acepta como válido.
func TestIndexAndSweepShareOneNormalizer(t *testing.T) {
	if err := indice.VerificarNormalizador(textmatch.Normalize); err != nil {
		t.Fatalf("textmatch.Normalize ya no cumple el contrato del índice: %v", err)
	}
	// La misma «ñ» decide igual por clave y por barrido: «Tequeños» casa exacto y
	// «tequenos» solo por distancia.
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(), product("TEQUEÑOS CONGELADOS"), product("tequenos congelados"))
	assertMatched(t, art.Lines[0], "TEQ-30", "exact")
	assertMatched(t, art.Lines[1], "TEQ-30", "fuzzy")
}
