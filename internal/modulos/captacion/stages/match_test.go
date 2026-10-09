//go:build pendiente

package stages_test

// match_test.go — el contrato de match.go: el replay del caso Ambar, el cableado, los
// errores de la etapa y la forma del artefacto. Las reglas por ítem (personalizaciones,
// añadidos, degradaciones, variantes) están en match_items_test.go; el envío, la nota
// del pedido y el log, en match_order_test.go; la cascada, en match_cascade_test.go.
//
// Los números van LITERALES: un total que el test vuelve a sumar con la fórmula del
// código pasa siempre.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ambarItems son los tres ítems del caso tal como los deja P4 (design §7.3).
func ambarItems() []llm.NormalizedItem {
	return []llm.NormalizedItem{
		{
			Product:         "torta de chocolate",
			Qty:             1,
			Range:           &llm.Range{Min: 10, Max: 12, Unit: "porciones"},
			AddonCandidates: []string{"decoración infantil"},
			Customizations:  []string{"sin lactosa"},
			Evidence:        chocolateCakeEvidence,
		},
		{
			Product:  "torta de vainilla con lluvia de colores",
			Qty:      1,
			Range:    &llm.Range{Min: 25, Max: 30, Unit: "porciones"},
			Evidence: vanillaCakeEvidence,
		},
		{Product: "tequeños congelados", Qty: 1, UnitKind: "package", PackageSize: 30, Evidence: tequenosEvidence},
	}
}

// chocolateAnswers hace que la zona gris resuelva la torta de chocolate.
func chocolateAnswers() map[string]int { return map[string]int{"torta de chocolate": 0} }

// TestMatchRun_AmbarReplay es el criterio del plan entero: tequeños con match exacto a
// $490, torta de chocolate con `variant_options` 10-12, torta de vainilla `unmatched`,
// envío presente y el escalón caro consultado UNA vez por ítem no cubierto.
//
// 🔴 CADA ASERCIÓN MIRA EL CONTENIDO, NO EL RECUENTO: un artefacto con cinco renglones
// vacíos también tiene cinco renglones.
func TestMatchRun_AmbarReplay(t *testing.T) {
	gz := &fakeGrayZone{answers: chocolateAnswers()}
	b := newMatchBench(t, stages.WithGrayZone(gz))

	art := b.run(t, stages.MatchInput{
		Quantities: p4Of(ambarItems()...),
		Index:      indexOf(t, ambarCatalog()),
		Note:       stages.NoOrderNote,
	})

	// El orden ES contrato: por cada ítem su línea y detrás sus añadidos; el envío al final.
	assertLineCount(t, art, 5, "torta, decoración, vainilla, tequeños, envío")
	assertAmbarChocolateCake(t, art.Lines[0])
	assertAmbarAddon(t, art.Lines[1])
	assertUnmatched(t, art.Lines[2], "torta de vainilla con lluvia de colores")
	assertAmbarTequenos(t, art.Lines[3])
	assertShippingWithoutZones(t, art.Lines[4])

	// EL CONTADOR DEL ESCALÓN CARO: uno por ítem no cubierto, y por LOS ítems.
	if art.GrayZoneCalls != 2 {
		t.Fatalf("gray_zone_calls = %d, se esperaban 2", art.GrayZoneCalls)
	}
	wantAsked := []string{"torta de chocolate", "torta de vainilla con lluvia de colores"}
	if !reflect.DeepEqual(gz.asked, wantAsked) {
		t.Fatalf("se preguntó por %q; solo por los DOS ítems que lo determinista no cubrió", gz.asked)
	}
	if !reflect.DeepEqual(gz.offered[0], []string{"Torta chocolate húmedo + crema choc."}) {
		t.Fatalf("candidatos ofrecidos = %q; los que COMPARTEN TOKENS, no el catálogo entero", gz.offered[0])
	}

	assertWarnings(t, art)
	assertTotal(t, art, 1290, 3, "800 de la decoración + 490 de los tequeños; pendientes: las dos tortas y el envío")

	// Y todo eso quedó persistido UNA vez bajo `artifacts.match`.
	if len(b.store.saved) != 1 || b.store.jobs[0] != jobID || b.store.saved[0].Stage != intake.StageMatch {
		t.Fatalf("persistido = %d artefactos bajo %v", len(b.store.saved), b.store.jobs)
	}
	var reread stages.MatchArtifact
	if err := json.Unmarshal(b.store.saved[0].Payload, &reread); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	if !reflect.DeepEqual(&reread, art) {
		t.Fatalf("lo persistido y lo devuelto NO son lo mismo:\n%+v\n%+v", reread, *art)
	}
}

// assertAmbarChocolateCake: la resolvió la zona gris y su tamaño lo elige el dueño.
func assertAmbarChocolateCake(t *testing.T, cake stages.Line) {
	t.Helper()
	assertMatched(t, cake, "TORTA-CHOC", grayZoneName)
	if cake.Label != "Torta chocolate húmedo + crema choc." {
		t.Fatalf("etiqueta = %q; se COPIA del catálogo, no se deja la del cliente", cake.Label)
	}
	if cake.UnitPrice != nil {
		t.Fatalf("precio = %v; el rango cruza dos variantes: lo pone el dueño", *cake.UnitPrice)
	}
	wantOptions := []stages.VariantOption{
		{SKU: "TORTA-CHOC#10", Label: "Torta chocolate húmedo + crema choc. — 10 porciones", Price: 2100},
		{SKU: "TORTA-CHOC#12", Label: "Torta chocolate húmedo + crema choc. — 12 porciones", Price: 2400},
	}
	if !reflect.DeepEqual(cake.VariantOptions, wantOptions) {
		t.Fatalf("variant_options = %+v; las 25 porciones NO son candidatas de un rango 10-12", cake.VariantOptions)
	}
	if cake.Customization != "sin lactosa" {
		t.Fatalf("customization = %q", cake.Customization)
	}
	if cake.Range == nil || *cake.Range != (llm.Range{Min: 10, Max: 12, Unit: "porciones"}) {
		t.Fatalf("range = %+v; el rango no se colapsa", cake.Range)
	}
	if *cake.Match != (stages.MatchProvenance{Strategy: grayZoneName, Confidence: 0.91}) {
		t.Fatalf("procedencia = %+v; estrategia y confianza son las que declara la zona gris", *cake.Match)
	}
	if cake.Evidence != chocolateCakeEvidence || cake.Qty != 1 {
		t.Fatalf("la línea no conserva la evidencia y la cantidad del ítem: %+v", cake)
	}
}

// assertAmbarAddon: el añadido facturable es línea propia, detrás de la torta.
func assertAmbarAddon(t *testing.T, deco stages.Line) {
	t.Helper()
	assertMatched(t, deco, "DECO-INF", "exact")
	assertPrice(t, deco, 800)
	if deco.Qty != 1 || deco.Evidence != chocolateCakeEvidence || deco.Range != nil {
		t.Fatalf("el añadido va con cantidad 1, la evidencia de su ítem y sin rango: %+v", deco)
	}
}

// assertAmbarTequenos: match EXACTO a $490, y el paquete no se pierde.
func assertAmbarTequenos(t *testing.T, tequenos stages.Line) {
	t.Helper()
	assertMatched(t, tequenos, "TEQ-30", "exact")
	assertPrice(t, tequenos, 490)
	if tequenos.Match.Confidence != 1 || tequenos.UnitKind != "package" || tequenos.PackageSize != 30 {
		t.Fatalf("tequeños = %+v (match %+v); confianza 1 y el paquete de 30 viajan", tequenos, tequenos.Match)
	}
}

// assertShippingWithoutZones: el envío va siempre, y sin zonas lo precifica el dueño.
func assertShippingWithoutZones(t *testing.T, shipping stages.Line) {
	t.Helper()
	if shipping.Kind != stages.KindShipping || shipping.SKU != intakes.ShippingSKU || shipping.Qty != 1 {
		t.Fatalf("la última línea no es el envío: %+v", shipping)
	}
	if shipping.UnitPrice != nil || shipping.Note != "por confirmar zona" || shipping.Match != nil {
		t.Fatalf("envío sin zonas = %+v; va sin precio, con su nota y sin procedencia", shipping)
	}
}

// ---------------------------------------------------------------------------
// EL CABLEADO
// ---------------------------------------------------------------------------

// TestNewMatch_RefusesToBeBornHalfWired: sin log o sin store, ErrMatchNotWired y nil.
func TestNewMatch_RefusesToBeBornHalfWired(t *testing.T) {
	cases := []struct {
		name  string
		log   logger.Logger
		store stages.StageStore
	}{
		{"no log", nil, &fakeStore{}},
		{"no store", logger.New(), nil},
		{"neither", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stage, err := stages.NewMatch(c.log, c.store)
			if !errors.Is(err, stages.ErrMatchNotWired) || stage != nil {
				t.Fatalf("NewMatch = (%v, %v); se esperaba (nil, ErrMatchNotWired)", stage, err)
			}
		})
	}
	if stages.ErrMatchNotWired.Error() != "stages: la etapa match necesita log y store" {
		t.Fatalf("texto de ErrMatchNotWired = %q", stages.ErrMatchNotWired)
	}
	if errors.Is(stages.ErrMatchNotWired, stages.ErrNotWired) {
		t.Fatal("ErrMatchNotWired no es ErrNotWired: el match no necesita selector de vía")
	}
}

// TestMatchOption_IsNotTheOptionOfTheLLMStages fija R-03 donde se puede fijar sin dejar
// de compilar: los dos tipos de opción no son asignables ni convertibles, así que
// `NewMatch(log, store, WithCallTimeout(…))` no compila.
func TestMatchOption_IsNotTheOptionOfTheLLMStages(t *testing.T) {
	matchOption := reflect.TypeOf(stages.MatchOption(nil))
	llmOption := reflect.TypeOf(stages.Option(nil))
	if llmOption.AssignableTo(matchOption) || llmOption.ConvertibleTo(matchOption) {
		t.Fatal("una Option (plazo por llamada) se puede pasar donde va una MatchOption: «match con plazo» compilaría")
	}
	if matchOption.AssignableTo(llmOption) || matchOption.ConvertibleTo(llmOption) {
		t.Fatal("una MatchOption se puede pasar a una etapa LLM")
	}
	// Lo que SÍ configura es la etapa del match.
	var stage *stages.Match
	if matchOption.NumIn() != 1 || matchOption.In(0) != reflect.TypeOf(stage) {
		t.Fatalf("MatchOption = %v; configura un *Match", matchOption)
	}
}

// TestWithComparator_NilLeavesTheDefaultCascade: pasar nil no deja a la etapa sin
// comparador. Se ve porque la errata sigue casando por el barrido.
func TestWithComparator_NilLeavesTheDefaultCascade(t *testing.T) {
	b := newMatchBench(t, stages.WithComparator(nil))
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{Product: "hamburgueza", Qty: 1, Evidence: "x"})
	assertMatched(t, art.Lines[0], "HAMB", "fuzzy")
}

// TestWithGrayZone_NilIsNoGrayZone: una zona gris nil es no tenerla.
func TestWithGrayZone_NilIsNoGrayZone(t *testing.T) {
	b := newMatchBench(t, stages.WithGrayZone(nil))
	art := b.runItems(t, ambarCatalog(), llm.NormalizedItem{Product: "torta de chocolate", Qty: 1, Evidence: "x"})
	assertUnmatched(t, art.Lines[0], "torta de chocolate")
	if art.GrayZoneCalls != 0 {
		t.Fatalf("gray_zone_calls = %d sin zona gris", art.GrayZoneCalls)
	}
	assertWarnings(t, art)
}

// TestMatchRun_KeepsNoStateBetweenJobs: dos Run seguidos dan lo mismo y los contadores
// son los de CADA job, no un acumulado.
func TestMatchRun_KeepsNoStateBetweenJobs(t *testing.T) {
	gz := &fakeGrayZone{answers: chocolateAnswers()}
	b := newMatchBench(t, stages.WithGrayZone(gz))
	in := stages.MatchInput{Quantities: p4Of(ambarItems()...), Index: indexOf(t, ambarCatalog())}

	first := b.run(t, in)
	second := b.run(t, in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("el segundo Run no dio lo mismo que el primero:\n%+v\n%+v", *first, *second)
	}
	if second.GrayZoneCalls != 2 {
		t.Fatalf("gray_zone_calls del segundo job = %d; el contador es POR JOB", second.GrayZoneCalls)
	}
	if len(b.store.saved) != 2 {
		t.Fatalf("artefactos persistidos = %d, uno por Run", len(b.store.saved))
	}
}

// ---------------------------------------------------------------------------
// LOS ERRORES DE LA ETAPA
// ---------------------------------------------------------------------------

// TestMatchRun_WithoutInputs_FailsAndSavesNothing: sin artefacto de P4 o sin índice no se
// inventa un borrador; las cantidades se miran primero.
func TestMatchRun_WithoutInputs_FailsAndSavesNothing(t *testing.T) {
	cases := []struct {
		name string
		in   stages.MatchInput
		want error
	}{
		{"no catalog index", stages.MatchInput{Quantities: p4Of()}, stages.ErrNoCatalog},
		{"no P4 artifact", stages.MatchInput{Index: indexOf(t, ambarCatalog())}, stages.ErrNoQuantities},
		{"neither: quantities first", stages.MatchInput{}, stages.ErrNoQuantities},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newMatchBench(t)
			art, err := b.stage.Run(context.Background(), ambarJob(), c.in)
			if !errors.Is(err, c.want) || art != nil {
				t.Fatalf("Run = (%v, %v); se esperaba (nil, %v)", art, err, c.want)
			}
			if len(b.store.saved) != 0 {
				t.Fatal("se persistió un artefacto a medias")
			}
		})
	}
	if stages.ErrNoCatalog.Error() != "stages: el match necesita el índice del catálogo del tenant" ||
		stages.ErrNoQuantities.Error() != "stages: el match necesita el artefacto de P4" {
		t.Fatalf("textos de los centinelas: %q / %q", stages.ErrNoCatalog, stages.ErrNoQuantities)
	}
}

// TestMatchRun_PersistenceFailures: el error del store sale envuelto, y `(false, nil)`
// es ErrJobNotProcessing; en los dos casos el artefacto devuelto es nil.
func TestMatchRun_PersistenceFailures(t *testing.T) {
	in := stages.MatchInput{Quantities: p4Of(), Index: indexOf(t, ambarCatalog())}

	t.Run("the store fails", func(t *testing.T) {
		boom := errors.New("postgres caído")
		b := newMatchBench(t)
		b.store.err = boom
		art, err := b.stage.Run(context.Background(), ambarJob(), in)
		if !errors.Is(err, boom) || art != nil {
			t.Fatalf("Run = (%v, %v); se esperaba el error del store envuelto", art, err)
		}
		if !strings.HasPrefix(err.Error(), "match: persistir el artefacto: ") {
			t.Fatalf("texto del error = %q", err)
		}
	})

	t.Run("the job left processing", func(t *testing.T) {
		b := newMatchBench(t)
		b.store.lost = true
		art, err := b.stage.Run(context.Background(), ambarJob(), in)
		if !errors.Is(err, stages.ErrJobNotProcessing) || art != nil {
			t.Fatalf("Run = (%v, %v); se esperaba (nil, ErrJobNotProcessing)", art, err)
		}
	})
}

// ---------------------------------------------------------------------------
// LA FORMA DEL ARTEFACTO
// ---------------------------------------------------------------------------

// TestMatchRun_ArtifactPassesTheMachineGateWithItsLiteralKeys: el doble valida con la
// MISMA puerta que Postgres (`intake.Artifact.Validate`), y las claves JSON son contrato.
func TestMatchRun_ArtifactPassesTheMachineGateWithItsLiteralKeys(t *testing.T) {
	b := newMatchBench(t)
	b.runItems(t, ambarCatalog()) // un pedido sin ítems es legítimo
	if len(b.store.saved) != 1 || b.store.saved[0].Stage != intake.StageMatch {
		t.Fatalf("persistido = %+v", b.store.saved)
	}
	if err := b.store.saved[0].Validate(); err != nil {
		t.Fatalf("el artefacto no pasa la puerta de la máquina: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b.store.saved[0].Payload, &raw); err != nil {
		t.Fatalf("el artefacto no es un objeto JSON: %v", err)
	}
	assertExactKeys(t, "artefacto sin avisos ni nota", b.store.saved[0].Payload, "gray_zone_calls", "lines", "version")
	if string(raw["version"]) != "1" || llm.ArtifactVersion != 1 {
		t.Fatalf("version = %s; es llm.ArtifactVersion", raw["version"])
	}
	if string(raw["gray_zone_calls"]) != "0" {
		t.Fatalf("gray_zone_calls = %s; la clave viaja también a cero", raw["gray_zone_calls"])
	}
	var lines []json.RawMessage
	if err := json.Unmarshal(raw["lines"], &lines); err != nil || len(lines) != 1 {
		t.Fatalf("lines = %s; un pedido sin ítems lleva la sola línea de envío", raw["lines"])
	}
	// `unit_price: null` viaja EXPLÍCITO: un campo ausente no dice lo mismo que un precio
	// vacío (design §7.4).
	assertExactKeys(t, "línea de envío sin precio", lines[0], "kind", "label", "note", "qty", "sku", "unit_price")
	var shipping map[string]json.RawMessage
	if err := json.Unmarshal(lines[0], &shipping); err != nil || string(shipping["unit_price"]) != "null" {
		t.Fatalf("unit_price del envío = %s; tiene que ser null explícito", shipping["unit_price"])
	}
}

// TestMatchArtifact_JSONKeysAreTheContract fija TODAS las claves del artefacto lleno:
// `draft` y la bandeja las leen por nombre.
func TestMatchArtifact_JSONKeysAreTheContract(t *testing.T) {
	price := 2400.0
	full := stages.MatchArtifact{
		Version: 1,
		Lines: []stages.Line{{
			Kind: stages.KindMatched, SKU: "S", Label: "L", Qty: 2, UnitPrice: &price, Customization: "c",
			Range: &llm.Range{Min: 1, Max: 2, Unit: "u"}, UnitKind: "package", PackageSize: 30,
			VariantOptions: []stages.VariantOption{{SKU: "S#1", Label: "L — 1", Price: 1}},
			Match:          &stages.MatchProvenance{Strategy: "fuzzy", Confidence: 0.91},
			Note:           "n", Evidence: "e",
		}},
		CustomerNote:  "nota",
		Warnings:      []stages.Warning{{ItemPos: 3, Reason: stages.WarningNoProduct}},
		GrayZoneCalls: 1,
	}
	payload, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("serializar el artefacto: %v", err)
	}
	const want = `{"version":1,"lines":[{"kind":"matched","sku":"S","label":"L","qty":2,"unit_price":2400,` +
		`"customization":"c","range":{"min":1,"max":2,"unit":"u"},"unit_kind":"package","package_size":30,` +
		`"variant_options":[{"sku":"S#1","label":"L — 1","price":1}],"match":{"strategy":"fuzzy","confidence":0.91},` +
		`"note":"n","evidence":"e"}],"customer_note":"nota","warnings":[{"item_pos":3,"reason":"sin_producto"}],` +
		`"gray_zone_calls":1}`
	if string(payload) != want {
		t.Fatalf("el JSON del artefacto cambió:\n got %s\nwant %s", payload, want)
	}
}

// TestMatchVocabulary_ValuesAreLiteral: los `kind` y los motivos viajan en el artefacto
// y en la revisión; un renombre del identificador no puede mover el valor.
func TestMatchVocabulary_ValuesAreLiteral(t *testing.T) {
	got := []string{
		stages.KindMatched, stages.KindUnmatched, stages.KindShipping,
		stages.WarningNoProduct, stages.WarningInvalidQty, stages.WarningNoteTooLong,
		stages.WarningGrayZoneDown, stages.WarningRangeWithoutVariant,
	}
	want := []string{
		"matched", "unmatched", "shipping",
		"sin_producto", "cantidad_invalida", "indicacion_larga", "zona_gris_caida", "rango_sin_variante",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vocabulario del match = %q, se esperaba %q", got, want)
	}
	// Y la ausencia de nota es la nota vacía, con su tipo propio.
	var none stages.OrderNote
	if stages.NoOrderNote != none {
		t.Fatalf("NoOrderNote = %q; es la OrderNote vacía", stages.NoOrderNote)
	}
}

// TestPartialTotal_SumsPricedLinesAndCountsThePending: los dos números van juntos.
func TestPartialTotal_SumsPricedLinesAndCountsThePending(t *testing.T) {
	price := func(v float64) *float64 { return &v }
	cases := []struct {
		name    string
		lines   []stages.Line
		total   float64
		pending int
	}{
		{"no lines", nil, 0, 0},
		{"price times quantity", []stages.Line{{Qty: 3, UnitPrice: price(150)}}, 450, 0},
		{"a line without price is pending and adds nothing", []stages.Line{{Qty: 2, UnitPrice: price(490)}, {Qty: 5}}, 980, 1},
		{"quantity zero with price adds zero and is not pending", []stages.Line{{Qty: 0, UnitPrice: price(3000)}}, 0, 0},
		{"a price of zero is a price", []stages.Line{{Qty: 1, UnitPrice: price(0)}}, 0, 0},
		{"shipping without price counts as pending", []stages.Line{{Kind: stages.KindShipping, Qty: 1}}, 0, 1},
		{"customization never adds", []stages.Line{{Qty: 1, UnitPrice: price(3000), Customization: "más queso"}}, 3000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			artifact := stages.MatchArtifact{Lines: c.lines}
			total, pending := artifact.PartialTotal()
			if total != c.total || pending != c.pending {
				t.Fatalf("PartialTotal = (%v, %d); se esperaba (%v, %d)", total, pending, c.total, c.pending)
			}
		})
	}
}
