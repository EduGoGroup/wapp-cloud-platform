package stages_test

// Trozo de match_test.go (E-13): las reglas POR ÍTEM de Match.Run — personalizaciones,
// añadidos, la indicación de la línea, las degradaciones de DEUDA-044.16 y la variante.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ---------------------------------------------------------------------------
// UNA PERSONALIZACIÓN NO ES UN ARTÍCULO
// ---------------------------------------------------------------------------

// TestMatchRun_NoSaltNeverReachesTheMatcher: el catálogo vende «Sal» ($50) y «Salsa»
// ($60). Si «sin sal» entrara a la cascada, el presupuesto cobraría sal que el cliente
// pidió NO tener.
//
// 🔴 EL SEGUNDO CASO NO ES UN DUPLICADO: en el primero el producto casa por clave y el
// comparador NO LLEGA A EJECUTARSE, así que «el espía no vio "sin sal"» sería cierto por
// vacío. El segundo lleva una errata para que el barrido SÍ corra.
func TestMatchRun_NoSaltNeverReachesTheMatcher(t *testing.T) {
	cases := []struct {
		name       string
		product    string
		sweepRuns  bool
		strategy   string
		confidence float64
	}{
		{"written the same: matches by key and the sweep does not run", "hamburguesa", false, "exact", 1.0},
		{"with a typo: the sweep runs and fuzzy rescues it", "hamburgueza", true, "fuzzy", 1 - 1.0/11.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newSpy()
			gz := &fakeGrayZone{}
			b := newMatchBench(t, stages.WithComparator(spy), stages.WithGrayZone(gz))

			art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
				Product: c.product, Qty: 1, Customizations: []string{"sin sal"}, Evidence: "una hamburguesa sin sal",
			})

			assertLineCount(t, art, 2, "UNA línea de producto y el envío")
			burger := art.Lines[0]
			assertMatched(t, burger, "HAMB", c.strategy)
			assertPrice(t, burger, 3000)
			if diff := burger.Match.Confidence - c.confidence; diff > 1e-4 || diff < -1e-4 {
				t.Fatalf("confianza = %v, se esperaba %v", burger.Match.Confidence, c.confidence)
			}
			if burger.Customization != "sin sal" {
				t.Fatalf("customization = %q", burger.Customization)
			}
			if lineWithSKU(art, "SAL") != nil || lineWithSKU(art, "SALSA") != nil {
				t.Fatalf("«sin sal» se convirtió en una línea de Sal o de Salsa: %+v", art.Lines)
			}

			if (spy.calls > 0) != c.sweepRuns {
				t.Fatalf("llamadas al comparador = %d; el caso depende de si el barrido corre: si esto falla, lo de abajo no mide nada", spy.calls)
			}
			if spy.sawAnyContaining("sin sal") {
				t.Fatalf("la personalización llegó al comparador: %q", spy.expected)
			}
			if art.GrayZoneCalls != 0 || len(gz.asked) != 0 {
				t.Fatalf("se gastó una llamada al modelo por la personalización: %q", gz.asked)
			}
		})
	}
}

// TestMatchRun_ACustomizationSoldInTheCatalogIsNotChargedEither es la prueba LIMPIA de
// «las personalizaciones se apartan ANTES del matcher».
//
// 🔴 NO BASTA CON «SIN SAL», que la para ADEMÁS la guarda de negación de los añadidos.
// «Salsa aparte» es una instrucción de preparación sin negación, y el catálogo vende
// «Salsa» a $60: si las personalizaciones llegaran al matcher, el n-grama «salsa»
// casaría y el cliente pagaría 60 por pedir la salsa aparte.
func TestMatchRun_ACustomizationSoldInTheCatalogIsNotChargedEither(t *testing.T) {
	spy := newSpy()
	gz := &fakeGrayZone{}
	b := newMatchBench(t, stages.WithComparator(spy), stages.WithGrayZone(gz))

	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product: "hamburguesa", Qty: 1, Customizations: []string{"salsa aparte", "Queso"}, Evidence: "una hamburguesa con la salsa aparte",
	})

	assertLineCount(t, art, 2, "hamburguesa y envío: la personalización NO puede volverse una línea")
	if lineWithSKU(art, "SALSA") != nil || lineWithSKU(art, "QUESO") != nil {
		t.Fatalf("una personalización se cobró como artículo: %+v", art.Lines)
	}
	if art.Lines[0].Customization != "salsa aparte, Queso" {
		t.Fatalf("customization = %q", art.Lines[0].Customization)
	}
	if spy.sawAnyContaining("salsa") || len(gz.asked) != 0 {
		t.Fatalf("la personalización llegó al comparador (%q) o a la zona gris (%q)", spy.expected, gz.asked)
	}
	assertTotal(t, art, 3000, 1, "el total es el de la hamburguesa sola")
}

// ---------------------------------------------------------------------------
// LOS AÑADIDOS: ¿EL CATÁLOGO TIENE UN ARTÍCULO PARA ESO?
// ---------------------------------------------------------------------------

// TestMatchRun_AddonWithArticleIsItsOwnPricedLine: «extra de queso» con un catálogo que
// vende «Queso» ⇒ DOS líneas, la segunda con su precio, y el total sube.
func TestMatchRun_AddonWithArticleIsItsOwnPricedLine(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product: "hamburguesa", Qty: 2, AddonCandidates: []string{"extra de queso"}, Evidence: "dos hamburguesas con extra de queso",
	})

	assertLineCount(t, art, 3, "hamburguesa, queso y envío")
	if art.Lines[0].SKU != "HAMB" || art.Lines[0].Customization != "" {
		t.Fatalf("el añadido que ES artículo no se queda además como indicación: %+v", art.Lines[0])
	}
	cheese := art.Lines[1]
	assertMatched(t, cheese, "QUESO", stages.StrategyNGram)
	assertPrice(t, cheese, 150)
	if cheese.Label != "Queso" || cheese.Match.Confidence != 1 {
		t.Fatalf("queso = %+v (match %+v)", cheese, cheese.Match)
	}
	if cheese.Qty != 1 || cheese.Evidence != "dos hamburguesas con extra de queso" {
		t.Fatalf("el añadido = cantidad %d, evidencia %q; es del pedido: cantidad 1 aunque el ítem sea de 2", cheese.Qty, cheese.Evidence)
	}
	assertTotal(t, art, 6150, 1, "3000×2 + 150×1; solo el envío queda sin precio")
}

// TestMatchRun_AddonWithoutArticleIsAnInstructionAndDoesNotMoveTheTotal: el MISMO texto
// contra un catálogo SIN «Queso» ⇒ UNA línea con la indicación pegada.
//
// 🔴 El «mismo total» se mide contra la corrida SIN la frase, además de contra el
// literal: es la comparación la que demuestra que la personalización no toca el dinero.
func TestMatchRun_AddonWithoutArticleIsAnInstructionAndDoesNotMoveTheTotal(t *testing.T) {
	cat := withoutArticle(ambarCatalog(), "QUESO")
	plain := llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"}
	withPhrase := plain
	withPhrase.AddonCandidates = []string{"más queso"}

	gz := &fakeGrayZone{}
	b := newMatchBench(t, stages.WithGrayZone(gz))
	baseline := b.runItems(t, cat, plain)
	art := b.runItems(t, cat, withPhrase)

	assertLineCount(t, art, 2, "hamburguesa y envío: «más queso» NO inventa un renglón")
	if art.Lines[0].Customization != "más queso" {
		t.Fatalf("customization = %q; llega con el acento que escribió el cliente", art.Lines[0].Customization)
	}
	for _, l := range art.Lines {
		if l.Kind == stages.KindUnmatched {
			t.Fatalf("un añadido sin artículo NUNCA es una línea unmatched: %+v", l)
		}
	}
	baseTotal, basePending := baseline.PartialTotal()
	assertTotal(t, art, baseTotal, basePending, "la customization no entra en ningún total")
	assertTotal(t, art, 3000, 1, "y el número es el del catálogo")
	if len(gz.asked) != 0 || art.GrayZoneCalls != 0 {
		t.Fatalf("un añadido que no casa NO gasta una llamada al modelo: %q", gz.asked)
	}
}

// TestMatchRun_AddonRules recorre el resto de la regla de los añadidos.
func TestMatchRun_AddonRules(t *testing.T) {
	cat := oneCategory(
		catalogo.Article{Code: "1", SKU: "HAMB", Label: "Hamburguesa", Price: 3000},
		catalogo.Article{Code: "2", SKU: "QUESO", Label: "Queso", Price: 150},
		catalogo.Article{Code: "3", SKU: "AZUL", Label: "Queso azul", Price: 300},
		catalogo.Article{Code: "4", SKU: "SALSA", Label: "Salsa", Price: 60},
		catalogo.Article{Code: "5", SKU: "PAPAS", Label: "Papas fritas", Price: 900, Tags: []string{"acompañamiento"}},
		catalogo.Article{Code: "6", SKU: "BEB", Label: "Bebida", Variants: []catalogo.Variant{
			{Code: "C", Label: "Chica", Price: 500}, {Code: "L", Label: "Litro y medio", Price: 1100},
		}},
	)
	cases := []struct {
		name          string
		addons        []string
		skus          []string // las líneas de añadido, en orden
		strategies    []string
		customization string
	}{
		{"blank candidates are ignored", []string{"", "   "}, nil, nil, ""},
		{"the longest n-gram wins", []string{"con queso azul"}, []string{"AZUL"}, []string{"ngrama"}, ""},
		{"the shorter n-gram when the long one is not sold", []string{"con queso verde"}, []string{"QUESO"}, []string{"ngrama"}, ""},
		{"at equal length the leftmost n-gram wins", []string{"salsa y queso"}, []string{"SALSA"}, []string{"ngrama"}, ""},
		{"the whole text equal to a label is the exact key", []string{"Queso Azul"}, []string{"AZUL"}, []string{"exact"}, ""},
		{"the sku of an article", []string{"PAPAS"}, []string{"PAPAS"}, []string{"sku"}, ""},
		{"a unique tag", []string{"acompañamiento"}, []string{"PAPAS"}, []string{"tag"}, ""},
		{"a unique variant label resolves that variant", []string{"litro y medio"}, []string{"BEB#L"}, []string{"variante"}, ""},
		{"negation is never billable whatever is negated", []string{"Sin salsa"}, nil, nil, "Sin salsa"},
		{"a typo is not rescued by fuzzy", []string{"papas fritaz"}, nil, nil, "papas fritaz"},
		{"each candidate is decided on its own and order is kept", []string{"pan sin gluten", "extra de queso", "sin salsa", "bien cocida"},
			[]string{"QUESO"}, []string{"ngrama"}, "pan sin gluten, sin salsa, bien cocida"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newSpy()
			gz := &fakeGrayZone{}
			b := newMatchBench(t, stages.WithComparator(spy), stages.WithGrayZone(gz))
			art := b.runItems(t, cat, llm.NormalizedItem{Product: "hamburguesa", Qty: 1, AddonCandidates: c.addons, Evidence: "x"})

			assertLineCount(t, art, 2+len(c.skus), "la hamburguesa, sus añadidos facturables y el envío")
			for i, sku := range c.skus {
				addon := art.Lines[1+i]
				assertMatched(t, addon, sku, c.strategies[i])
				if addon.Qty != 1 || addon.Match.Confidence != 1 {
					t.Fatalf("añadido %s = cantidad %d, confianza %v", sku, addon.Qty, addon.Match.Confidence)
				}
			}
			if art.Lines[0].Customization != c.customization {
				t.Fatalf("customization = %q, se esperaba %q", art.Lines[0].Customization, c.customization)
			}
			if spy.calls != 0 || len(gz.asked) != 0 {
				t.Fatalf("un añadido no pasa por el barrido (%d llamadas) ni por la zona gris (%q)", spy.calls, gz.asked)
			}
			assertWarnings(t, art)
		})
	}
}

// TestMatchRun_AddonSoldByVariantsOffersThemAllWithoutWarning: el añadido no trae rango,
// así que no hay con qué decidir la variante, y eso no es un aviso.
func TestMatchRun_AddonSoldByVariantsOffersThemAllWithoutWarning(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product: "hamburguesa", Qty: 1, Range: &llm.Range{Min: 40, Max: 50, Unit: "porciones"},
		AddonCandidates: []string{"torta chocolate húmedo + crema choc."}, Evidence: "x",
	})

	assertLineCount(t, art, 3, "hamburguesa, la torta como añadido y envío")
	cake := art.Lines[1]
	assertMatched(t, cake, "TORTA-CHOC", "exact")
	if cake.UnitPrice != nil || len(cake.VariantOptions) != 3 || cake.Range != nil {
		t.Fatalf("añadido por variantes = %+v; las tres opciones, sin precio y sin el rango del ítem", cake)
	}
	assertWarnings(t, art)
}

// TestMatchRun_UnmatchedProductStillGetsItsAddons: que el producto no esté no borra el
// añadido facturable que sí está, y va detrás de su renglón.
func TestMatchRun_UnmatchedProductStillGetsItsAddons(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product: "pizza napolitana", Qty: 1, AddonCandidates: []string{"extra de queso", "bien cocida"}, Evidence: "x",
	})
	assertLineCount(t, art, 3, "la pizza sin precio, el queso y el envío")
	assertUnmatched(t, art.Lines[0], "pizza napolitana")
	if art.Lines[0].Customization != "bien cocida" {
		t.Fatalf("customization del unmatched = %q", art.Lines[0].Customization)
	}
	assertMatched(t, art.Lines[1], "QUESO", "ngrama")
}

// ---------------------------------------------------------------------------
// LA INDICACIÓN DE LA LÍNEA
// ---------------------------------------------------------------------------

// TestMatchRun_InstructionsAreJoinedInTheOnlySlot: personalizaciones primero y detrás
// los añadidos que no eran artículo, recortados, con `", "`, y saneados como la nota.
func TestMatchRun_InstructionsAreJoinedInTheOnlySlot(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product:         "hamburguesa",
		Qty:             1,
		Customizations:  []string{"  sin cebolla ", "", "bien\tcocida", "   "},
		AddonCandidates: []string{" pan   sin gluten "},
		Evidence:        "una hamburguesa",
	})
	assertLineCount(t, art, 2, "hamburguesa y envío")
	if got := art.Lines[0].Customization; got != "sin cebolla, bien cocida, pan sin gluten" {
		t.Fatalf("customization = %q; vacías fuera, bordes recortados y saneada con intakes.SanitizeNote", got)
	}
}

// TestMatchRun_InstructionTooLongIsNeitherTruncatedNorFatal: REQ-33e. La línea sobrevive
// sin indicación y con su aviso, y el Warn no cita el texto.
func TestMatchRun_InstructionTooLongIsNeitherTruncatedNorFatal(t *testing.T) {
	long := strings.Repeat("sin lactosa y sin maní, ", 30)
	if len([]rune(long)) <= intakes.MaxNoteRunes {
		t.Fatalf("el fixture mide %d runas: tiene que pasarse de %d de verdad", len([]rune(long)), intakes.MaxNoteRunes)
	}
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(),
		llm.NormalizedItem{Product: "queso", Qty: 1, Evidence: "un queso"},
		llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Customizations: []string{long}, Evidence: "una hamburguesa"},
	)

	assertLineCount(t, art, 3, "la línea del producto NO se pierde por una indicación larga")
	assertPrice(t, art.Lines[1], 3000)
	if art.Lines[1].Customization != "" {
		t.Fatalf("customization = %q; no se trunca: o cabe entera o no va", art.Lines[1].Customization)
	}
	assertWarnings(t, art, stages.Warning{ItemPos: 1, Reason: stages.WarningNoteTooLong})
	log := b.log.String()
	if !strings.Contains(log, "match: la indicación de la línea no cabe y se descarta SIN truncar") ||
		!strings.Contains(log, "item_pos=1") || !strings.Contains(log, "level=WARN") {
		t.Fatalf("el descarte de la indicación no dejó su Warn con la posición: %q", log)
	}
	if strings.Contains(log, "lactosa") {
		t.Fatalf("el log cita la indicación del cliente: %q", log)
	}
}

// ---------------------------------------------------------------------------
// DEUDA-044.16 — UN ÍTEM MALO NO TIRA EL BORRADOR
// ---------------------------------------------------------------------------

// TestMatchRun_ABadItemDoesNotTakeTheOthersWithIt: los dos ítems degenerados que hoy son
// alcanzables —la reanudación del worker decodifica el artefacto de P4 sin el validador
// de calidad— y los buenos INTACTOS, con su sku y su precio.
func TestMatchRun_ABadItemDoesNotTakeTheOthersWithIt(t *testing.T) {
	b := newMatchBench(t)
	art := b.runItems(t, ambarCatalog(),
		llm.NormalizedItem{Product: "tequeños congelados", Qty: 2, Evidence: tequenosEvidence},
		llm.NormalizedItem{AddonCandidates: []string{"queso"}}, // ni producto ni evidencia
		llm.NormalizedItem{Product: "hamburguesa", Qty: 0, Evidence: "una hamburguesa"},
		llm.NormalizedItem{Product: "queso", Qty: 3, Evidence: "tres quesos"},
		llm.NormalizedItem{Product: " \t", Evidence: "  "}, // blancos: tampoco hay nada que enseñar
	)

	assertLineCount(t, art, 4, "tequeños, hamburguesa, queso y envío: los ítems vacíos no dejan renglón ni añadidos")
	tequenos, burger, cheese := art.Lines[0], art.Lines[1], art.Lines[2]
	assertMatched(t, tequenos, "TEQ-30", "exact")
	assertPrice(t, tequenos, 490)
	assertMatched(t, burger, "HAMB", "exact")
	assertPrice(t, burger, 3000)
	assertMatched(t, cheese, "QUESO", "exact")
	assertPrice(t, cheese, 150)
	if tequenos.Qty != 2 || burger.Qty != 0 || cheese.Qty != 3 {
		t.Fatalf("cantidades = %d, %d, %d; la inválida no se maquilla a 1: se enseña como vino", tequenos.Qty, burger.Qty, cheese.Qty)
	}
	assertWarnings(t, art,
		stages.Warning{ItemPos: 1, Reason: stages.WarningNoProduct},
		stages.Warning{ItemPos: 2, Reason: stages.WarningInvalidQty},
		stages.Warning{ItemPos: 4, Reason: stages.WarningNoProduct},
	)
	assertTotal(t, art, 1430, 1, "490×2 + 150×3 + 3000×0")
}

// TestMatchRun_ItemWithoutProductButWithEvidenceIsStillARow: el cliente pidió ALGO y el
// dueño tiene que verlo; no se busca la frase en el catálogo ni en la zona gris.
func TestMatchRun_ItemWithoutProductButWithEvidenceIsStillARow(t *testing.T) {
	spy := newSpy()
	gz := &fakeGrayZone{}
	b := newMatchBench(t, stages.WithComparator(spy), stages.WithGrayZone(gz))
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{Qty: -1, Evidence: "  y una hamburguesa para el final "})

	assertLineCount(t, art, 2, "el renglón del ítem y el envío")
	assertUnmatched(t, art.Lines[0], "y una hamburguesa para el final")
	if art.Lines[0].Evidence != "  y una hamburguesa para el final " || art.Lines[0].Qty != -1 {
		t.Fatalf("la evidencia y la cantidad viajan tal como vinieron: %+v", art.Lines[0])
	}
	assertWarnings(t, art,
		stages.Warning{ItemPos: 0, Reason: stages.WarningNoProduct},
		stages.Warning{ItemPos: 0, Reason: stages.WarningInvalidQty},
	)
	if spy.calls != 0 || len(gz.asked) != 0 {
		t.Fatalf("una frase suelta no se busca: comparador %d, zona gris %q", spy.calls, gz.asked)
	}
}

// TestMatchRun_WarningsOfOneItemKeepTheirOrder: cantidad, cascada o variante, indicación.
func TestMatchRun_WarningsOfOneItemKeepTheirOrder(t *testing.T) {
	long := strings.Repeat("sin lactosa y sin maní, ", 30)
	b := newMatchBench(t, stages.WithGrayZone(&fakeGrayZone{answers: chocolateAnswers()}))
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
		Product: "torta de chocolate", Qty: 0, Range: &llm.Range{Min: 40, Max: 50, Unit: "porciones"},
		Customizations: []string{long}, Evidence: "x",
	})
	assertWarnings(t, art,
		stages.Warning{ItemPos: 0, Reason: stages.WarningInvalidQty},
		stages.Warning{ItemPos: 0, Reason: stages.WarningRangeWithoutVariant},
		stages.Warning{ItemPos: 0, Reason: stages.WarningNoteTooLong},
	)
}

// ---------------------------------------------------------------------------
// LA VARIANTE — CUÁNDO SE RESUELVE Y CUÁNDO LA ELIGE EL DUEÑO
// ---------------------------------------------------------------------------

// TestMatchRun_VariantIsResolvedOnlyWhenExactlyOneFits cubre las situaciones de la
// variante con literales. En todas la línea es `matched`: el PRODUCTO está en el
// catálogo aunque el tamaño no se pueda decidir.
func TestMatchRun_VariantIsResolvedOnlyWhenExactlyOneFits(t *testing.T) {
	const article = "Torta chocolate húmedo + crema choc."
	allThree := []string{"TORTA-CHOC#10", "TORTA-CHOC#12", "TORTA-CHOC#25"}
	cases := []struct {
		name    string
		rng     *llm.Range
		sku     string
		label   string
		price   float64 // 0 = sin precio
		options []string
		warned  bool
	}{
		{"the range points at ONE variant: resolved", &llm.Range{Min: 11, Max: 12, Unit: "porciones"},
			"TORTA-CHOC#12", article + " — 12 porciones", 2400, nil, false},
		{"the unit of the range is not compared", &llm.Range{Min: 25, Max: 25, Unit: "personas"},
			"TORTA-CHOC#25", article + " — 25 porciones", 3900, nil, false},
		{"an inverted range is read in order", &llm.Range{Min: 30, Max: 20},
			"TORTA-CHOC#25", article + " — 25 porciones", 3900, nil, false},
		{"the range crosses TWO: the owner chooses", &llm.Range{Min: 10, Max: 12, Unit: "porciones"},
			"TORTA-CHOC", article, 0, []string{"TORTA-CHOC#10", "TORTA-CHOC#12"}, false},
		{"no range: nothing to decide with, all are offered", nil, "TORTA-CHOC", article, 0, allThree, false},
		{"the requested range is not sold: all and a WARNING", &llm.Range{Min: 40, Max: 50, Unit: "porciones"},
			"TORTA-CHOC", article, 0, allThree, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newMatchBench(t, stages.WithGrayZone(&fakeGrayZone{answers: chocolateAnswers()}))
			art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{
				Product: "torta de chocolate", Qty: 1, Range: c.rng, Evidence: chocolateCakeEvidence,
			})

			line := art.Lines[0]
			assertMatched(t, line, c.sku, grayZoneName)
			if line.Label != c.label {
				t.Fatalf("etiqueta = %q, se esperaba %q", line.Label, c.label)
			}
			if c.price != 0 {
				assertPrice(t, line, c.price)
			} else if line.UnitPrice != nil {
				t.Fatalf("precio = %v; con opciones abiertas lo pone el dueño", *line.UnitPrice)
			}
			if offered := offeredSKUs(line); !reflect.DeepEqual(offered, c.options) {
				t.Fatalf("variant_options = %q, se esperaban %q", offered, c.options)
			}
			if !reflect.DeepEqual(line.Range, c.rng) {
				t.Fatalf("range = %+v; viaja tal como vino", line.Range)
			}
			if c.warned {
				assertWarnings(t, art, stages.Warning{ItemPos: 0, Reason: stages.WarningRangeWithoutVariant})
				return
			}
			assertWarnings(t, art)
		})
	}
}

// TestMatchRun_VariantWithoutNumbersIsNeverARangeCandidate: casar «Grande» con un rango
// exigiría saber cuántas porciones tiene una grande, y eso no está en el catálogo.
func TestMatchRun_VariantWithoutNumbersIsNeverARangeCandidate(t *testing.T) {
	cat := oneCategory(catalogo.Article{Code: "1", SKU: "PIZZA", Label: "Pizza", Variants: []catalogo.Variant{
		{Code: "G", Label: "Grande", Price: 9000}, {Code: "8", Label: "8 porciones", Price: 7000},
	}})
	b := newMatchBench(t)
	art := b.runItems(t, cat, llm.NormalizedItem{Product: "pizza", Qty: 1, Range: &llm.Range{Min: 6, Max: 8}, Evidence: "x"})

	assertMatched(t, art.Lines[0], "PIZZA#8", "exact")
	assertPrice(t, art.Lines[0], 7000)
	if art.Lines[0].Label != "Pizza — 8 porciones" || len(art.Lines[0].VariantOptions) != 0 {
		t.Fatalf("línea = %+v; solo «8 porciones» es candidata de 6-8", art.Lines[0])
	}
}
