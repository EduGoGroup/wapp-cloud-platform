package cart

import (
	"fmt"
	"strconv"
	"strings"
)

func screenCategories(cat Catalog, st cartState, size int) string {
	var b strings.Builder
	b.WriteString("🛒 Elige una categoría:")
	start, end := pageBounds(len(cat.Categories), st.Page, size)
	for _, c := range cat.Categories[start:end] {
		b.WriteString("\n" + c.Code + ") " + c.Label)
	}
	if end < len(cat.Categories) {
		b.WriteString("\n" + moreCode(categoryCodes(cat)) + ") Más ▾")
	}
	return b.String() // L1 es la raíz: sin "volver".
}

func screenArticles(category Category, st cartState, size int) string {
	var b strings.Builder
	b.WriteString(category.Label + ":")
	start, end := pageBounds(len(category.Items), st.Page, size)
	for _, a := range category.Items[start:end] {
		b.WriteString("\n" + a.Code + ") " + a.Label + " · " + articlePrice(a))
	}
	if end < len(category.Items) {
		b.WriteString("\n" + moreCode(articleCodes(category)) + ") Más ▾")
	}
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

func screenArticle(a Article, showDesc bool) string {
	var b strings.Builder
	b.WriteString(a.Label + " · " + articlePrice(a))
	if showDesc {
		desc := a.Description
		if desc == "" {
			desc = "(sin descripción)"
		}
		b.WriteString("\n" + desc)
	}
	b.WriteString("\n1) Ver descripción")
	b.WriteString("\n2) Agregar al pedido")
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

// screenVariants es el nivel de variante (L3b, D-041.4): lista NUMERADA POR
// POSICIÓN —no por Variant.Code, que es un identificador de negocio ("V2") y no
// algo que nadie vaya a teclear— más el "volver" de siempre. Sin paginación: las
// variantes de un artículo son un puñado por definición (una presentación, un
// tamaño), no un catálogo dentro del catálogo.
func screenVariants(a Article) string {
	var b strings.Builder
	b.WriteString(a.Label + " · elige una opción:")
	for i, v := range a.Variants {
		b.WriteString("\n" + strconv.Itoa(i+1) + ") " + v.Label + " · " + money(v.Price))
	}
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

func screenQuantity(a Article) string {
	return screenQuantityOf(a.Label)
}

// screenQuantityOf pide la cantidad de algo ya nombrado: el artículo a secas, o
// el artículo con su variante ("Torta de chocolate — 25-30 porciones"). Con un
// artículo sin variantes produce EXACTAMENTE el texto de siempre.
func screenQuantityOf(label string) string {
	return "¿Cuántos \"" + label + "\"? Escribe la cantidad (" + codeVolver + " ← volver)"
}

// articlePrice es el precio que se muestra en las listas y en la ficha. Con
// variantes el Price del artículo es solo una referencia (D-041.2), así que se
// enseña "desde" el más barato: prometer un precio fijo y cobrar otro al elegir
// la presentación sería mentir en pantalla. Sin variantes, el precio de siempre.
func articlePrice(a Article) string {
	if !a.HasVariants() {
		return money(a.Price)
	}
	return "desde " + money(minVariantPrice(a.Variants))
}

func minVariantPrice(vs []Variant) float64 {
	lowest := vs[0].Price
	for _, v := range vs[1:] {
		if v.Price < lowest {
			lowest = v.Price
		}
	}
	return lowest
}

func screenContinue(category Category) string {
	var b strings.Builder
	b.WriteString("Añadido al pedido ✅")
	if category.Label != "" {
		b.WriteString("\n1) Agregar más de " + category.Label)
	} else {
		b.WriteString("\n1) Agregar más")
	}
	b.WriteString("\n2) Finalizar pedido")
	b.WriteString("\n" + codeCancelar + ") Cancelar pedido")
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

func screenSummary(lines []cartLine) string {
	var b strings.Builder
	b.WriteString("🧾 Resumen del pedido:")
	for _, l := range lines {
		b.WriteString("\n" + l.Label + " x" + strconv.Itoa(l.Qty) + "  " + money(lineTotal(l)))
	}
	b.WriteString("\nTOTAL  " + money(total(lines)))
	b.WriteString("\n1) Confirmar y finalizar")
	b.WriteString("\n2) Seguir agregando")
	b.WriteString("\n" + codeCancelar + ") Cancelar pedido")
	return b.String()
}

func screenClosed(t float64) string {
	return "✅ ¡Pedido confirmado! Total " + money(t) + "."
}

func screenCancelled() string {
	return "Pedido cancelado. Puedes iniciar uno nuevo cuando quieras."
}

func terminalScreen(st cartState) string {
	if st.Level == LevelCancelled {
		return screenCancelled()
	}
	return screenClosed(total(st.Lines))
}

// --- paginación ------------------------------------------------------------

// pageBounds devuelve el rango [start,end) de la página actual sobre una lista
// de total elementos con el tamaño de página dado (>= 1).
func pageBounds(total, page, size int) (int, int) {
	if size <= 0 {
		size = DefaultPageSize
	}
	if page < 0 {
		page = 0
	}
	start := page * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return start, end
}

// hasMore indica si tras la página actual quedan más elementos (habilita "Más ▾").
func hasMore(total, page, size int) bool {
	_, end := pageBounds(total, page, size)
	return end < total
}

// moreCode calcula el código del ítem "Más ▾": el siguiente entero fuera del
// rango de códigos del nivel (design.md §4.2/§9.E). Se toma el máximo código
// numérico de la lista + 1, garantizando que NO colisiona con ningún ítem (ni
// con "0" de volver). Con categorías/artículos de códigos 1..N, "Más" = N+1.
func moreCode(codes []string) string {
	max := 0
	for _, c := range codes {
		if n, err := strconv.Atoi(c); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}

func categoryCodes(cat Catalog) []string {
	out := make([]string, 0, len(cat.Categories))
	for _, c := range cat.Categories {
		out = append(out, c.Code)
	}
	return out
}

func articleCodes(category Category) []string {
	out := make([]string, 0, len(category.Items))
	for _, a := range category.Items {
		out = append(out, a.Code)
	}
	return out
}

// --- utilidades de importe -------------------------------------------------

func money(f float64) string       { return fmt.Sprintf("$%.2f", f) }
func lineTotal(l cartLine) float64 { return float64(l.Qty) * l.UnitPrice }

func total(lines []cartLine) float64 {
	var t float64
	for _, l := range lines {
		t += lineTotal(l)
	}
	return t
}
