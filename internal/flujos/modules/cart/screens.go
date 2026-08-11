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
	// La ranura de indicación de LÍNEA (D-041.19). Es una línea de menú AÑADIDA a
	// una pantalla que ya se imprimía, no un paso nuevo: quien no la pulsa teclea
	// exactamente lo mismo que ayer (INV-15).
	b.WriteString("\n" + codeIndicacion + ") ✏️ Indicación para este artículo")
	b.WriteString("\n" + codeCancelar + ") Cancelar pedido")
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

// screenSummary pinta el resumen antes de confirmar. Las indicaciones se ven AQUÍ
// (REQ-33c) y por eso la firma lleva la nota del pedido: el cliente confirma
// viendo lo que pidió, incluido lo que anotó. La de cada línea va como sub-línea
// bajo ella (pegada a lo que describe) y la del pedido como línea propia bajo el
// total (es del pedido entero, no de un artículo).
func screenSummary(lines []cartLine, note string) string {
	return summaryWith(lines, note, "")
}

// summaryWith es el resumen con un SUFIJO opcional pegado a la línea del TOTAL. Es
// lo ÚNICO que la revalidación del rescate (D-041.25, T4.9) añade al renderizador:
// «TOTAL  $5.00   (antes $7.00)». El resto del resumen —los renglones, las
// indicaciones, el menú— se renderiza EXACTAMENTE igual que siempre, y por eso la
// función que ya existía delega aquí con el sufijo vacío en vez de duplicarse: dos
// resúmenes distintos para el mismo pedido serían dos verdades.
func summaryWith(lines []cartLine, note, totalSuffix string) string {
	var b strings.Builder
	b.WriteString("🧾 Resumen del pedido:")
	for _, l := range lines {
		b.WriteString("\n" + l.Label + " x" + strconv.Itoa(l.Qty) + "  " + money(lineTotal(l)))
		if l.Customization != "" {
			b.WriteString("\n   ✏️ " + l.Customization)
		}
	}
	// El total NO cambia por ninguna indicación (INV-13): se calcula de qty ×
	// unit_price y de nada más.
	b.WriteString("\nTOTAL  " + money(total(lines)) + totalSuffix)
	if note != "" {
		b.WriteString("\n✏️ Para todo el pedido: " + note)
	}
	b.WriteString("\n1) Confirmar y finalizar")
	b.WriteString("\n2) Seguir agregando")
	b.WriteString("\n" + codeIndicacion + ") ✏️ Indicación para todo el pedido")
	b.WriteString("\n" + codeCancelar + ") Cancelar pedido")
	return b.String()
}

// --- pantallas de indicación (D-041.19 / D-041.20) -------------------------

// noteRules son las tres líneas que se repiten en las dos pantallas de texto: qué
// es la ranura, qué NO se debe escribir en ella y cuánto cabe. No es decoración:
// el ADR-0034 §Decisión 1 exige que una UI que captura un campo de nivel 1 diga
// para qué sirve, y la advertencia de no escribir datos personales es la
// CONTENCIÓN de esta ranura —wApp no puede detectar PII aquí, porque el cart
// numérico tiene prohibido llamar al LLM (REQ-21 del Plan 043)—.
func noteRules() string {
	return "\nEs una instrucción para quien lo prepara y NO cambia el precio." +
		"\nNo escribas aquí datos personales, direcciones ni datos de pago." +
		"\nMáx. " + strconv.Itoa(MaxNoteRunes) + " caracteres."
}

// noteCurrent antepone la indicación ya anotada, si la hubiera. Con ella en
// pantalla, el 0 deja de significar "sin indicación" para significar "déjala como
// está": misma tecla, misma idea de volver sin cambiar nada.
func noteCurrent(current string) string {
	if current == "" {
		return ""
	}
	return "Indicación actual: \"" + current + "\" — escribe otra para reemplazarla.\n"
}

// screenItemNoteScope pregunta el ALCANCE de la indicación cuando la línea trae
// más de una unidad (D-041.20). Sin esta pregunta, «dos hamburguesas, una sin
// cebolla» anotaría las dos y nadie se enteraría hasta la cocina; y como el cart
// no tiene edición de líneas por chat (INV-03), el cliente no tendría forma de
// arreglarlo salvo cancelar el pedido entero.
func screenItemNoteScope(l cartLine) string {
	n := strconv.Itoa(l.Qty)
	return "✏️ Indicación para \"" + l.Label + "\" (pediste " + n + "):" +
		"\n1) Para las " + n +
		"\n2) Solo para 1 (la separo en dos líneas)" +
		"\n" + codeVolver + ") ← Volver sin indicación"
}

// screenItemNote pide el texto de la indicación de una línea. `split` cambia solo
// el encabezado: con él, el cliente ya eligió que la indicación es para UNA de las
// N unidades, y la pantalla lo repite para que no haya duda de qué está anotando.
func screenItemNote(l cartLine, split bool) string {
	head := "✏️ Escribe la indicación para \"" + l.Label + "\"."
	if split {
		head = "✏️ Escribe la indicación para 1 de las " + strconv.Itoa(l.Qty) + " \"" + l.Label + "\"."
	}
	return noteCurrent(l.Customization) + head + noteRules() +
		"\n" + codeVolver + ") ← Volver sin indicación"
}

// screenOrderNote pide el texto de la indicación de TODO el pedido. Los ejemplos
// son de ENTREGA además de receta («dejar en portería»): a nivel de pedido la
// indicación no siempre es personalización de producto, y por eso la columna se
// llama customer_note y no customization.
func screenOrderNote(current string) string {
	return noteCurrent(current) +
		"✏️ Escribe una indicación para todo el pedido." + noteRules() +
		"\n" + codeVolver + ") ← Volver sin indicación"
}

// noteAck es el acuse que se antepone a la pantalla del nivel de origen tras
// capturar una indicación (REQ-33c): el cliente tiene que ver que se anotó, y
// verlo CON el texto, porque es su única confirmación de que se entendió.
func noteAck(text string, scope noteScope, qty int) string {
	switch scope {
	case scopeOrder:
		return "Anotado para todo el pedido: \"" + text + "\" ✅"
	case scopeItemSplit:
		return "Anotado para 1 de las " + strconv.Itoa(qty) + ": \"" + text + "\" ✅"
	default:
		return "Anotado: \"" + text + "\" ✅"
	}
}

// noteTooLongMsg es el reprompt del MISMO paso cuando el texto se pasa del límite
// (§9.D). Dice el número REAL para que el cliente sepa cuánto sobra: la indicación
// NO se trunca (REQ-33e), así que sin ese número no sabría qué recortar.
func noteTooLongMsg(err NoteTooLongError) string {
	return "Esa indicación es muy larga (" + strconv.Itoa(err.Runes) + " de " +
		strconv.Itoa(err.Max) + " caracteres). Escríbela más corta."
}

func screenClosed(t float64) string {
	return "✅ ¡Pedido confirmado! Total " + money(t) + "."
}

// screenCancelled es la despedida del pedido cancelado.
//
// 🔴 LA FRASE CAMBIÓ CON EL #29 (decisión de Jhoan 2026-08-11), y no por estilo:
// decía «Pedido cancelado. Puedes iniciar uno nuevo cuando quieras.» y esa promesa era
// LITERALMENTE cierta hasta esta ola —cancelar dejaba el flow_state vivo y
// cart.ResumePolicy.Restart reabría el catálogo ante CUALQUIER texto—. Desde el #24 y
// su extensión #29 el flujo TERMINA, el flow_state se suelta y hace falta el
// disparador REAL: mantener la frase vieja dejaría a la clienta esperando una
// reapertura que ya no llega. Es la ola la que rompe la promesa, así que la arregla la
// ola.
//
// ⚠️ COSTURA CONOCIDA: la palabra va HARDCODEADA y este módulo es PURO —no conoce las
// reglas `event_start` del tenant, que son configurables—. stopNotice
// (runtime/events.go) resolvió la MISMA tensión al revés, no prometiendo ninguna
// palabra («prometer una palabra que quizá no dispare nada sería peor»). Aquí manda la
// decisión del dueño: sin nombrar nada, la pantalla no dice QUÉ hacer, que es justo el
// reparo que había que arreglar. El peor caso está acotado: en un tenant cuya palabra
// no sea «carrito», ese texto no casa ninguna regla y cae en la oferta/fallback, que
// sí le enumera lo que puede hacer. Nombrarla de verdad exige que el disparador del
// tenant viaje hasta el módulo — dato nuevo en el contrato, materia del Plan 053.
func screenCancelled() string {
	return "Pedido cancelado. Si quieres hacer otro, escribe \"carrito\" y lo empezamos de cero."
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
