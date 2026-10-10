//go:build pendiente

package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// variants_test.go conduce la sub-máquina con el catálogo v2 (Plan 041 · T2.3): el
// nivel de variante que aparece SOLO cuando el artículo tiene variantes, el combo que
// sigue siendo UNA línea, y el artículo sin variantes que no gana ni un paso.
// variants.go no exporta nada: todo se ve por Module.Step.

const (
	variantsScreen = "Torta de chocolate · elige una opción:\n" +
		"1) 10-12 porciones · $18000.00\n2) 25-30 porciones · $32000.00\n0) ← Volver"
	cakeCard = "Torta de chocolate · desde $18000.00\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver"
)

// atVariants deja el carrito en el nivel de variante de la torta.
func atVariants(t *testing.T, m cart.Module) map[string]any {
	t.Helper()
	return walk(t, m, v2Vars(t), "01", "1", "2")
}

// «Agregar» en un artículo con variantes pide la variante antes de la cantidad; la
// lista va numerada por posición y la cantidad nombra la presentación elegida.
func TestVariant_AsksForVariantBeforeQuantity(t *testing.T) {
	m := cart.New()
	st, outs, vars := drive(t, m, v2Vars(t), "01")
	if st.Level != cart.LevelArticles {
		t.Fatalf("estado = %+v, quiero articles", st)
	}
	// En la lista, un artículo con variantes anuncia «desde» el precio más bajo.
	mustScreen(t, outs, "Tortas:\n1) Torta de chocolate · desde $18000.00\n2) Combo hamburguesa · $9500.00\n0) ← Volver")

	_, outs, vars = drive(t, m, vars, "1")
	mustScreen(t, outs, cakeCard)

	st, outs, vars = drive(t, m, vars, "2")
	if st.Level != cart.LevelVariant || st.VariantCode != "" {
		t.Fatalf("estado = %+v, quiero el nivel de variante sin variante elegida", st)
	}
	mustScreen(t, outs, variantsScreen)

	st, outs, _ = drive(t, m, vars, "2")
	if st.Level != cart.LevelQuantity || st.VariantCode != "V2" {
		t.Fatalf("estado = %+v, quiero quantity con la variante V2", st)
	}
	mustScreen(t, outs, `¿Cuántos "Torta de chocolate — 25-30 porciones"? Escribe la cantidad (0 ← volver)`)
}

// La línea con variante lleva SU precio, SU etiqueta compuesta y el sku compuesto; la
// variante elegida se suelta al agregar.
func TestVariant_LineTakesTheVariantPriceLabelAndSKU(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atVariants(t, m), "2")
	st, effs, vars := driveEffects(t, m, vars, "3")
	want := cartLine{SKU: "TORTA-CHOC#V2", Label: "Torta de chocolate — 25-30 porciones", Qty: 3, UnitPrice: 32000}
	if st.Level != cart.LevelContinue || len(st.Lines) != 1 || st.Lines[0] != want || st.VariantCode != "" {
		t.Fatalf("estado = %+v\nquiero continue con la línea %+v y sin variante en curso", st, want)
	}
	// Las MISMAS claves del v1; lo único que cambia es su contenido.
	e := effectNamed(t, effs, cart.EffectItemAdded)
	wantPayload := `{"label":"Torta de chocolate — 25-30 porciones","qty":3,"sku":"TORTA-CHOC#V2","unit_price":32000}`
	if got := jsonOf(t, e.PublicPayload()); got != wantPayload {
		t.Errorf("item_added = %s\nquiero      %s", got, wantPayload)
	}

	_, outs, vars := drive(t, m, vars, "2") // resumen
	mustContain(t, outs, "Torta de chocolate — 25-30 porciones x3  $96000.00", "TOTAL  $96000.00")
	_, effs, _ = driveEffects(t, m, vars, "1")
	closed := effectNamed(t, effs, cart.EffectCartClosed)
	if closed.Payload["total"] != 96000.0 {
		t.Errorf("total del cierre = %v, quiero 96000", closed.Payload["total"])
	}
}

// «0» desde la variante vuelve a la ficha; desde la cantidad, a las VARIANTES (un paso
// atrás de verdad). La variante elegida no sobrevive.
func TestVariant_BackSteps(t *testing.T) {
	m := cart.New()
	vars := atVariants(t, m)

	st, outs, _ := drive(t, m, vars, "0")
	if st.Level != cart.LevelArticle || st.SKU != "TORTA-CHOC" {
		t.Fatalf("estado = %+v, quiero la ficha de la torta", st)
	}
	mustScreen(t, outs, cakeCard)

	chosen := walk(t, m, vars, "2")
	st, outs, _ = drive(t, m, chosen, "0")
	if st.Level != cart.LevelVariant || st.VariantCode != "" {
		t.Fatalf("estado = %+v, quiero el nivel de variante sin variante elegida", st)
	}
	mustScreen(t, outs, variantsScreen)
}

// Una posición que no existe repromptea el nivel de variante. El código de negocio
// de la variante ("V2") no se teclea.
func TestVariant_InvalidEntryReprompts(t *testing.T) {
	m := cart.New()
	vars := atVariants(t, m)
	for _, in := range []string{"3", "9", "V2", "-1"} {
		st, outs, _ := drive(t, m, vars, in)
		if st.Level != cart.LevelVariant || len(st.Lines) != 0 {
			t.Fatalf("%q: estado = %+v, quiero seguir en variante sin líneas", in, st)
		}
		mustScreen(t, outs, invalidPrefix+variantsScreen)
	}
}

// La variante elegida es del artículo en foco: al soltar el artículo, muere.
func TestVariant_DoesNotSurviveLeavingTheArticle(t *testing.T) {
	m := cart.New()
	chosen := walk(t, m, atVariants(t, m), "2") // quantity con V2
	// 0 (variantes) → 0 (ficha) → 0 (artículos) → 2 (combo) → 2 (agregar)
	st, outs, _ := drive(t, m, walk(t, m, chosen, "0", "0", "0", "2"), "2")
	if st.Level != cart.LevelQuantity || st.SKU != "COMBO-1" || st.VariantCode != "" {
		t.Fatalf("estado = %+v, quiero la cantidad del combo sin variante heredada", st)
	}
	mustScreen(t, outs, `¿Cuántos "Combo hamburguesa"? Escribe la cantidad (0 ← volver)`)
}

// El combo es UNA línea a su propio precio: no pide variante ni despliega componentes.
func TestCombo_IsASingleLine(t *testing.T) {
	m := cart.New()
	st, outs, vars := drive(t, m, walk(t, m, v2Vars(t), "01", "2"), "2")
	if st.Level != cart.LevelQuantity {
		t.Fatalf("estado = %+v, quiero ir directo a la cantidad", st)
	}
	mustScreen(t, outs, `¿Cuántos "Combo hamburguesa"? Escribe la cantidad (0 ← volver)`)
	st, _, _ = drive(t, m, vars, "1")
	want := []cartLine{{SKU: "COMBO-1", Label: "Combo hamburguesa", Qty: 1, UnitPrice: 9500}}
	if !reflect.DeepEqual(st.Lines, want) {
		t.Errorf("líneas = %+v, quiero %+v", st.Lines, want)
	}
}

// Un artículo sin variantes del catálogo v2 recorre los mismos pasos y textos de
// siempre (REQ-08), y «0» desde la cantidad vuelve a su ficha.
func TestNoVariants_SameStepsAsAlways(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, v2Vars(t), "02", "1")
	st, outs, vars := drive(t, m, vars, "2")
	if st.Level != cart.LevelQuantity {
		t.Fatalf("estado = %+v, quiero ir directo a la cantidad", st)
	}
	mustScreen(t, outs, `¿Cuántos "Café"? Escribe la cantidad (0 ← volver)`)
	st, outs, _ = drive(t, m, vars, "0")
	if st.Level != cart.LevelArticle {
		t.Fatalf("estado = %+v, quiero la ficha", st)
	}
	mustScreen(t, outs, "Café · $2.50\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver")
}

// El catálogo cambió bajo los pies: en «cantidad» de un artículo con variantes pero
// sin variante válida se vuelve a preguntar, en vez de cobrar el precio de referencia.
func TestVariant_QuantityWithoutValidVariantAsksAgain(t *testing.T) {
	for _, code := range []string{"", "V9"} {
		vars := v2Vars(t)
		seedState(t, vars, cartState{Level: cart.LevelQuantity, CatCode: "01", SKU: "TORTA-CHOC", VariantCode: code, Started: true})
		st, outs, _ := drive(t, cart.New(), vars, "2")
		if st.Level != cart.LevelVariant || st.VariantCode != "" || len(st.Lines) != 0 {
			t.Fatalf("variante %q: estado = %+v, quiero volver a variante sin línea", code, st)
		}
		mustScreen(t, outs, variantsScreen)
	}
}

// Y al revés: en «variante» de un artículo que ya no tiene variantes se sigue por la
// cantidad, sin quedar atrapado.
func TestVariant_ArticleThatLostItsVariantsGoesToQuantity(t *testing.T) {
	vars := v2Vars(t)
	seedState(t, vars, cartState{Level: cart.LevelVariant, CatCode: "01", SKU: "COMBO-1", VariantCode: "V1", Started: true})
	st, outs, _ := drive(t, cart.New(), vars, "1")
	if st.Level != cart.LevelQuantity || st.VariantCode != "" {
		t.Fatalf("estado = %+v, quiero quantity sin variante", st)
	}
	mustScreen(t, outs, `¿Cuántos "Combo hamburguesa"? Escribe la cantidad (0 ← volver)`)
}

// La variante se puede nombrar por su etiqueta: la cascada devuelve el ORDINAL de
// pantalla, no el código de negocio.
func TestVariant_ResolvesByLabel(t *testing.T) {
	var steps []string
	m := cart.New(cart.WithMatchHook(func(step, _ string) { steps = append(steps, step) }))
	vars := atVariants(t, m)
	st, _, _ := drive(t, m, vars, "25-30 porciones")
	if st.Level != cart.LevelQuantity || st.VariantCode != "V2" {
		t.Fatalf("estado = %+v, quiero quantity con V2", st)
	}
	if !reflect.DeepEqual(steps, []string{"exact"}) {
		t.Errorf("escalones = %v, quiero [exact]", steps)
	}
}
