// Porta internal/flujos/modules/cart/screens.go @ 9d5a4b6

// screens.go son las PANTALLAS del carrito: los textos que el cliente recibe por
// WhatsApp, la paginación de las listas y el formato de los importes.
//
// No exporta nada: lo que promete se ve por Module.Render, Module.Step, Module.Prime
// y RevalidationMessage. Los textos son OBSERVABLES y se conservan byte a byte: los
// sujetan los goldens de testdata/ (cart_v1_transcript, cart_v2_transcript) y los
// tests de este fichero.
//
// IMPORTES. Siempre `$` y dos decimales, sin separador de miles: «$2.50»,
// «$18000.00». El total de una línea es qty × unit_price y el del pedido la suma de
// sus líneas, y de nada más (INV-13).
//
// PAGINACIÓN (L1 y L2). Una página enseña `tamaño` elementos; un tamaño <= 0 vale
// DefaultPageSize y una página negativa vale 0. Si tras la página quedan más, la
// lista acaba con «<código>) Más ▾», donde el código es el mayor código NUMÉRICO de
// TODA la lista del nivel más uno (los códigos no numéricos no cuentan; sin ninguno
// numérico es «1»). La última página no lo ofrece.
//
//	L1   🛒 Elige una categoría:
//	     <código>) <categoría>            …una por línea; SIN «volver»: es la raíz
//
//	L2   <categoría>:
//	     <código>) <artículo> · <precio>
//	     0) ← Volver
//
//	L3   <artículo> · <precio>
//	     <descripción>                    solo tras «1»; «(sin descripción)» si no tiene
//	     1) Ver descripción
//	     2) Agregar al pedido
//	     0) ← Volver
//
//	L3b  <artículo> · elige una opción:
//	     <n>) <variante> · <precio>       numeradas POR POSICIÓN, sin paginar
//	     0) ← Volver
//
//	L4   ¿Cuántos "<lo que se pide>"? Escribe la cantidad (0 ← volver)
//
//	L5   Añadido al pedido ✅
//	     1) Agregar más de <categoría>    «1) Agregar más» si no hay categoría en foco
//	     2) Finalizar pedido
//	     3) ✏️ Indicación para este artículo
//	     9) Cancelar pedido
//	     0) ← Volver
//
//	L6   🧾 Resumen del pedido:
//	     <línea> x<qty>  <total de la línea>
//	        ✏️ <indicación de la línea>   solo si la tiene, pegada bajo su línea
//	     TOTAL  <total>
//	     ✏️ Para todo el pedido: <nota>   solo si la hay, bajo el total
//	     1) Confirmar y finalizar
//	     2) Seguir agregando
//	     3) ✏️ Indicación para todo el pedido
//	     9) Cancelar pedido
//
// El <precio> de un artículo CON variantes es «desde <el de la variante más
// barata>», en las listas y en la ficha: su Price es solo una referencia (D-041.2), y
// prometer un precio fijo para cobrar otro al elegir la presentación sería mentir en
// pantalla. En L4, <lo que se pide> es el artículo a secas o «<artículo> —
// <variante>».
//
// INDICACIONES (D-041.19 / D-041.20). Las dos pantallas de texto repiten tres líneas
// que no son decoración —el ADR-0034 exige decir para qué sirve el campo, y la
// advertencia es la CONTENCIÓN de la ranura, porque aquí wApp no puede detectar PII—:
//
//	Es una instrucción para quien lo prepara y NO cambia el precio.
//	No escribas aquí datos personales, direcciones ni datos de pago.
//	Máx. 280 caracteres.
//
//	alcance   ✏️ Indicación para "<línea>" (pediste <N>):
//	          1) Para las <N>
//	          2) Solo para 1 (la separo en dos líneas)
//	          0) ← Volver sin indicación
//
//	línea     ✏️ Escribe la indicación para "<línea>".
//	          ✏️ Escribe la indicación para 1 de las <N> "<línea>".    con «solo para 1»
//	          …las tres líneas… y «0) ← Volver sin indicación»
//
//	pedido    ✏️ Escribe una indicación para todo el pedido.
//	          …las tres líneas… y «0) ← Volver sin indicación»
//
// Si ya hay una indicación anotada, las pantallas de texto la anteponen —`Indicación
// actual: "<texto>" — escribe otra para reemplazarla.`— y entonces «0» significa
// «déjala como está».
//
// FINALES. Confirmado: «✅ ¡Pedido confirmado! Total <total>.». Cancelado: «Pedido
// cancelado. Si quieres hacer otro, escribe "carrito" y lo empezamos de cero.». La
// frase del cancelado cambió con el hallazgo #29 (decisión de Jhoan, 2026-08-11):
// decía «Puedes iniciar uno nuevo cuando quieras», que era cierto mientras cualquier
// texto reabría el catálogo; desde que el flujo TERMINA al cancelar hace falta el
// disparador real. ⚠️ Costura conocida: la palabra «carrito» va fija y el módulo es
// PURO —no conoce las reglas `event_start` del tenant—; en un tenant con otra palabra
// ese texto no casa ninguna regla y cae en la oferta/fallback, que sí enumera lo que
// se puede hacer. Nombrarla de verdad exige que el disparador viaje hasta el módulo
// (Plan 053).

package cart

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

func screenCategories(cat catalogo.Catalog, st cartState, size int) string {
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

func screenArticles(category catalogo.Category, st cartState, size int) string {
	var b strings.Builder
	b.WriteString(category.Label + ":")
	start, end := pageBounds(len(category.Items), st.Page, size)
	for _, a := range category.Items[start:end] {
		b.WriteString("\n" + a.Code + ") " + a.Label + " · " + articlePrice(a))
	}
	if end < len(category.Items) {
		b.WriteString("\n" + moreCode(articleCodes(category)) + ") Más ▾")
	}
	b.WriteString("\n" + codeBack + ") ← Volver")
	return b.String()
}

func screenArticle(a catalogo.Article, showDesc bool) string {
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
	b.WriteString("\n" + codeBack + ") ← Volver")
	return b.String()
}

// screenVariants es el nivel de variante (L3b, D-041.4): lista NUMERADA POR
// POSICIÓN —no por Variant.Code, que es un identificador de negocio ("V2") y no
// algo que nadie vaya a teclear— más el "volver" de siempre. Sin paginación: las
// variantes de un artículo son un puñado por definición (una presentación, un
// tamaño), no un catálogo dentro del catálogo.
func screenVariants(a catalogo.Article) string {
	var b strings.Builder
	b.WriteString(a.Label + " · elige una opción:")
	for i, v := range a.Variants {
		b.WriteString("\n" + strconv.Itoa(i+1) + ") " + v.Label + " · " + money(v.Price))
	}
	b.WriteString("\n" + codeBack + ") ← Volver")
	return b.String()
}

func screenQuantity(a catalogo.Article) string {
	return screenQuantityOf(a.Label)
}

// screenQuantityOf pide la cantidad de algo ya nombrado: el artículo a secas, o
// el artículo con su variante ("Torta de chocolate — 25-30 porciones"). Con un
// artículo sin variantes produce EXACTAMENTE el texto de siempre.
func screenQuantityOf(label string) string {
	return "¿Cuántos \"" + label + "\"? Escribe la cantidad (" + codeBack + " ← volver)"
}

// articlePrice es el precio que se muestra en las listas y en la ficha. Con
// variantes el Price del artículo es solo una referencia (D-041.2), así que se
// enseña "desde" el más barato: prometer un precio fijo y cobrar otro al elegir
// la presentación sería mentir en pantalla. Sin variantes, el precio de siempre.
func articlePrice(a catalogo.Article) string {
	if !a.HasVariants() {
		return money(a.Price)
	}
	return "desde " + money(minVariantPrice(a.Variants))
}

func minVariantPrice(vs []catalogo.Variant) float64 {
	lowest := vs[0].Price
	for _, v := range vs[1:] {
		if v.Price < lowest {
			lowest = v.Price
		}
	}
	return lowest
}

func screenContinue(category catalogo.Category) string {
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
	b.WriteString("\n" + codeNote + ") ✏️ Indicación para este artículo")
	b.WriteString("\n" + codeCancel + ") Cancelar pedido")
	b.WriteString("\n" + codeBack + ") ← Volver")
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
	b.WriteString("\n" + codeNote + ") ✏️ Indicación para todo el pedido")
	b.WriteString("\n" + codeCancel + ") Cancelar pedido")
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
		"\nMáx. " + strconv.Itoa(intakes.MaxNoteRunes) + " caracteres."
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
		"\n" + codeBack + ") ← Volver sin indicación"
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
		"\n" + codeBack + ") ← Volver sin indicación"
}

// screenOrderNote pide el texto de la indicación de TODO el pedido. Los ejemplos
// son de ENTREGA además de receta («dejar en portería»): a nivel de pedido la
// indicación no siempre es personalización de producto, y por eso la columna se
// llama customer_note y no customization.
func screenOrderNote(current string) string {
	return noteCurrent(current) +
		"✏️ Escribe una indicación para todo el pedido." + noteRules() +
		"\n" + codeBack + ") ← Volver sin indicación"
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
func noteTooLongMsg(err intakes.NoteTooLongError) string {
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
	highest := 0
	for _, c := range codes {
		if n, err := strconv.Atoi(c); err == nil && n > highest {
			highest = n
		}
	}
	return strconv.Itoa(highest + 1)
}

func categoryCodes(cat catalogo.Catalog) []string {
	out := make([]string, 0, len(cat.Categories))
	for _, c := range cat.Categories {
		out = append(out, c.Code)
	}
	return out
}

func articleCodes(category catalogo.Category) []string {
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
