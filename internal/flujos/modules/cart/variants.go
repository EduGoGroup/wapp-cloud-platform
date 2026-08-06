// variants.go es el sub-nivel de variantes de la sub-máquina (Plan 041 · T2.3,
// design.md §D-041.4): UN nivel más entre la ficha del artículo y la cantidad,
// no una máquina nueva. Aquí viven las transiciones que dependen de si el
// artículo tiene variantes y la construcción de la línea resultante.
//
// La regla que gobierna todo el archivo: un artículo SIN variantes recorre
// exactamente el mismo camino que en el Plan 016, sin un paso de más y con los
// mismos textos (REQ-08). Los combos (components) tampoco añaden pasos: un combo
// es UNA línea a su propio precio, y sus componentes son información del dueño.
package cart

import "strconv"

// toAdd es la transición de «2) Agregar al pedido» desde la ficha del artículo:
// al nivel de variante si el artículo tiene, o directo a cantidad si no.
// Devuelve también la pantalla del destino.
func toAdd(st cartState, a Article) (cartState, string) {
	st.VariantCode = ""
	if a.HasVariants() {
		st.Level = LevelVariant
		return st, screenVariants(a)
	}
	st.Level = LevelQuantity
	return st, screenQuantity(a)
}

// backFromQuantity es el «0 ← volver» desde la cantidad: un paso atrás de
// verdad. Con variantes eso es la lista de variantes (volver a la ficha
// obligaría a re-elegirla sin decirlo); sin variantes, la ficha del artículo,
// como siempre.
func backFromQuantity(st cartState, a Article) (cartState, string) {
	st.VariantCode = ""
	if a.HasVariants() {
		st.Level = LevelVariant
		return st, screenVariants(a)
	}
	st.Level = LevelArticle
	return st, screenArticle(a, false)
}

// variantByPosition resuelve la entrada del nivel de variante contra la POSICIÓN
// en la lista (1..N), que es lo que se enseña numerado. El Variant.Code ("V2") es
// identificador de negocio: viaja en el sku de la línea, no se teclea.
func variantByPosition(a Article, in string) (Variant, bool) {
	n, err := strconv.Atoi(in)
	if err != nil || n < 1 || n > len(a.Variants) {
		return Variant{}, false
	}
	return a.Variants[n-1], true
}

// selectedVariant resuelve la variante que el estado dice tener elegida. Devuelve
// ok=false si no hay ninguna o si ya no existe en el catálogo (el dueño pudo
// haberla quitado con la conversación viva).
func selectedVariant(a Article, st cartState) (Variant, bool) {
	if st.VariantCode == "" {
		return Variant{}, false
	}
	for _, v := range a.Variants {
		if v.Code == st.VariantCode {
			return v, true
		}
	}
	return Variant{}, false
}

// lineLabel es cómo se nombra lo que se está pidiendo: el artículo a secas, o el
// artículo con su variante ("Torta de chocolate — 25-30 porciones").
func lineLabel(a Article, v Variant, hasVariant bool) string {
	if !hasVariant {
		return a.Label
	}
	return a.Label + variantLabelSep + v.Label
}

// newLine construye la línea del pedido. Sin variante es la línea de siempre
// (sku, label y precio del artículo). Con variante, el precio y la etiqueta son
// los de la VARIANTE y el sku lleva su sufijo ("TORTA-CHOC#V2"), que es lo que
// deja trazable en el pedido qué presentación se vendió.
func newLine(a Article, v Variant, hasVariant bool, qty int) cartLine {
	if !hasVariant {
		return cartLine{SKU: a.SKU, Label: a.Label, Qty: qty, UnitPrice: a.Price}
	}
	return cartLine{
		SKU:       a.SKU + variantSKUSuffix + v.Code,
		Label:     lineLabel(a, v, true),
		Qty:       qty,
		UnitPrice: v.Price,
	}
}
