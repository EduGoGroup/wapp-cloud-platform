// Porta internal/intake/stages/match_lineas.go @ 4cd9cfb

package stages

import (
	"context"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// match_lines.go — DE UN ÍTEM DE P4 A LOS RENGLONES DEL PRESUPUESTO. No tiene
// exportados: lo que promete está escrito en Match.Run.
//
// El orden de las operaciones ES la garantía:
//
//	1. se APARTAN las personalizaciones            (no tocan el catálogo, jamás)
//	2. se busca el PRODUCTO entero                 (cascada completa + zona gris)
//	3. se resuelve la VARIANTE, si el artículo las tiene
//	4. se buscan los AÑADIDOS                      (n-gramas, sin zona gris)
//	5. lo que no casó de (4) se une a (1) y se pega a la línea como `customization`
//
// El paso 1 va primero porque es el único punto en el que se puede garantizar que «sin
// sal» no llegue al matcher: si se separara después, ya habría pasado por él.

// noteSeparator une varias indicaciones en la única ranura que tiene la línea. La coma
// es lo que escribiría una persona («sin sal, más queso») y lo que se lee bien en la
// comanda, que es donde acaba este texto.
const noteSeparator = ", "

// negationPrefix es la única palabra que este fichero interpreta, y hace falta decir por
// qué no es un léxico de producto: «sin X» NO PUEDE ser un añadido facturable de X, sea
// X lo que sea. Es una propiedad de la negación, no una regla sobre el menú de nadie.
//
// Existe porque P3 tiene instrucciones de mandar a `addon_candidates` todo lo que le
// genere duda, así que una negación puede llegar por esa ranura. Sin esta guarda, «sin
// sal» con un catálogo que venda «Sal» produciría una línea CON PRECIO de lo que el
// cliente pidió NO tener.
const negationPrefix = "sin "

// shippingNoteNoZone es lo que la línea de envío le dice al dueño cuando la zona no está
// resuelta (design §7.4).
const shippingNoteNoZone = "por confirmar zona"

// variantSKUSuffix y variantLabelSep son la convención de D-041.4 para el sku y la
// etiqueta compuestos de una variante ("TORTA-CHOC#12", "Torta — 12 porciones").
//
// 🔴 ESTÁN DUPLICADOS: los originales viven en el carrito y NO están exportados. Dos
// caminos que hacen lo mismo y divergen en un dato es de lo que peor envejece: si el
// carrito cambia el separador, hay que cambiarlo aquí.
const (
	variantSKUSuffix = "#"
	variantLabelSep  = " — "
)

// itemLines construye las líneas de UN ítem de P4 y las añade al artefacto.
//
// No devuelve error a propósito: todo lo que puede salir mal de un ÍTEM se degrada y se
// anota (DEUDA-044.16, ver la cabecera de match.go).
func (s *Match) itemLines(ctx context.Context, art *MatchArtifact, sc *scanner, pos int, it llm.NormalizedItem) {
	product := strings.TrimSpace(it.Product)
	if product == "" && strings.TrimSpace(it.Evidence) == "" {
		// Un ítem sin producto y sin evidencia no tiene NADA que enseñarle al dueño: una
		// línea vacía en la bandeja es peor que un aviso que dice que hubo un ítem
		// ilegible y en qué posición estaba.
		art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: WarningNoProduct})
		return
	}
	if product == "" {
		// Con evidencia sí hay algo que enseñar: la frase del cliente. Sale `unmatched`
		// —no se busca en el catálogo una frase suelta— y con su aviso.
		art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: WarningNoProduct})
	}
	if it.Qty <= 0 {
		// La cantidad viaja TAL COMO VINO. Maquillarla a 1 aquí escondería el defecto
		// justo donde el dueño podría verlo, y P4 ya decidió que un 0 ESCRITO es una
		// afirmación falsa, no una omisión con default.
		art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: WarningInvalidQty})
	}

	// (1) Las personalizaciones se apartan ANTES de tocar nada.
	notes := append([]string(nil), it.Customizations...)

	// (2) y (3): el producto y su variante.
	line := s.productLine(ctx, art, sc, pos, product, it)

	// (4) y (5): los añadidos, que o son línea o son indicación.
	extras, withoutArticle := addonLines(sc, it.AddonCandidates, it.Evidence)
	notes = append(notes, withoutArticle...)

	line.Customization = s.instruction(art, pos, notes)
	art.Lines = append(art.Lines, line)
	art.Lines = append(art.Lines, extras...)
}

// productLine resuelve el renglón principal del ítem: la cascada determinista, la zona
// gris si hizo falta, y la variante si el artículo se vende por presentaciones.
func (s *Match) productLine(ctx context.Context, art *MatchArtifact, sc *scanner,
	pos int, product string, it llm.NormalizedItem) Line {
	base := Line{
		Kind:        KindUnmatched,
		Label:       customerLabel(product, it.Evidence),
		Qty:         it.Qty,
		Range:       it.Range,
		UnitKind:    it.UnitKind,
		PackageSize: it.PackageSize,
		Evidence:    it.Evidence,
	}

	f, ok, err := s.searchProduct(ctx, sc, product)
	if err != nil {
		// Un comparador que revienta no es un dato raro del cliente: se registra y la
		// línea sale `unmatched`, que es lo mismo que habría salido sin cascada.
		s.log.Error("match: el comparador determinista falló; el ítem sale sin match",
			"stage", intake.StageMatch, "item_pos", pos, "error", err.Error())
		return base
	}
	if !ok {
		var reason string
		f, ok, reason = s.askGrayZone(ctx, art, sc, product)
		if reason != "" {
			art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: reason})
		}
	}
	if !ok {
		return base
	}

	line, reason := withArticle(base, f, it.Range)
	if reason != "" {
		art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: reason})
	}
	return line
}

// customerLabel es lo que se pinta cuando el catálogo no puso nombre: el producto tal
// como lo dijo el cliente y, si ni eso hay, la frase que lo sostiene.
func customerLabel(product, evidence string) string {
	if product != "" {
		return product
	}
	return strings.TrimSpace(evidence)
}

// withArticle copia el catálogo a la línea. Devuelve además el motivo del aviso cuando
// el rango pedido no cae en ninguna variante.
//
// # LAS CUATRO SITUACIONES DE LA VARIANTE, Y POR QUÉ NINGUNA ELIGE POR EL CLIENTE
//
//  1. **La variante vino resuelta** (el texto del cliente casó el label de UNA variante
//     concreta) ⇒ sku, etiqueta y precio de esa variante.
//  2. **El artículo no tiene variantes** ⇒ sku, etiqueta y precio del artículo.
//  3. **Tiene variantes y el rango señala EXACTAMENTE UNA** ⇒ esa. No es elegir: es que
//     solo una sirve.
//  4. **Tiene variantes y el rango señala VARIAS, o ninguna, o no hay rango** ⇒
//     `variant_options` con las candidatas y su precio, y `unit_price` a nil. Lo
//     resuelve el dueño (design §7.4). Elegir aquí sería inventar de qué tamaño era la
//     torta.
//
// En (4) la línea sigue siendo `matched`, y eso es deliberado: el PRODUCTO sí está en el
// catálogo. `unmatched` es para el producto que falta, y usarlo aquí le diría al dueño
// que no vende algo que sí vende.
func withArticle(base Line, f finding, rng *llm.Range) (Line, string) {
	a := f.match.Articulo
	base.Kind = KindMatched
	base.Match = &MatchProvenance{Strategy: f.provenance.Strategy, Confidence: f.provenance.Confidence}

	if f.match.HayVariante {
		v := f.match.Variante
		base.SKU = a.SKU + variantSKUSuffix + v.Code
		base.Label = a.Label + variantLabelSep + v.Label
		price := v.Price
		base.UnitPrice = &price
		return base, ""
	}

	base.SKU = a.SKU
	base.Label = a.Label

	if !a.HasVariants() {
		price := a.Price
		base.UnitPrice = &price
		return base, ""
	}

	candidates := variantsInRange(a, rng)
	if len(candidates) == 1 {
		v := a.Variants[candidates[0]]
		base.SKU = a.SKU + variantSKUSuffix + v.Code
		base.Label = a.Label + variantLabelSep + v.Label
		price := v.Price
		base.UnitPrice = &price
		return base, ""
	}

	reason := ""
	if len(candidates) == 0 {
		// Sin candidatas se le enseñan TODAS: el dueño necesita ver qué hay para poder
		// contestar. Que el rango pedido no exista es un aviso; que el cliente no dijera
		// el tamaño, no.
		candidates = allIndexes(len(a.Variants))
		if rng != nil {
			reason = WarningRangeWithoutVariant
		}
	}
	base.VariantOptions = variantOptions(a, candidates)
	return base, reason
}

// allIndexes devuelve los índices 0..n-1.
func allIndexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// variantOptions proyecta las variantes elegidas a lo que ve el dueño, con su precio ya
// copiado del catálogo.
func variantOptions(a catalogo.Article, idx []int) []VariantOption {
	out := make([]VariantOption, 0, len(idx))
	for _, i := range idx {
		v := a.Variants[i]
		out = append(out, VariantOption{
			SKU:   a.SKU + variantSKUSuffix + v.Code,
			Label: a.Label + variantLabelSep + v.Label,
			Price: v.Price,
		})
	}
	return out
}

// variantsInRange devuelve las posiciones de las variantes compatibles con el rango
// pedido: aquellas cuyo label menciona algún número dentro de [Min, Max].
//
// Sin rango no hay con qué filtrar y devuelve nil, que el llamante lee como «no se puede
// decidir». Una variante sin números en el label («Grande») tampoco es candidata de un
// rango numérico: casarla exigiría saber cuántas porciones tiene una grande, y eso no
// está en el catálogo.
//
// La UNIDAD del rango («porciones») no se compara: el catálogo no declara la unidad de
// sus variantes, así que exigir que coincida descartaría variantes correctas por una
// palabra que el dueño no tenía dónde escribir.
func variantsInRange(a catalogo.Article, rng *llm.Range) []int {
	if rng == nil {
		return nil
	}
	lo, hi := rng.Min, rng.Max
	if lo > hi {
		lo, hi = hi, lo
	}
	var out []int
	for i, v := range a.Variants {
		for _, n := range numbersIn(v.Label) {
			if n >= lo && n <= hi {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// numbersIn extrae los enteros ASCII que aparecen en un texto («10-12 porciones» ⇒
// [10 12]). Un número que desborda se SALTA en vez de propagar un error: uno de veinte
// cifras en el label de una variante no es un tamaño.
func numbersIn(s string) []int {
	var out []int
	for i := 0; i < len(s); {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if n, err := strconv.Atoi(s[i:j]); err == nil {
			out = append(out, n)
		}
		i = j
	}
	return out
}

// addonLines pasa cada `addon_candidate` por el catálogo y reparte según la ÚNICA regla
// de D-044.14: *¿el catálogo tiene un ítem para eso?*
//
//   - **Sí** ⇒ línea propia con su precio, que suma al total. Va DETRÁS de la línea que
//     acompaña, que es como lo pinta la bandeja (§7.5).
//   - **No** ⇒ vuelve como texto, para pegarse a la `customization` de esa línea. Nunca
//     una línea `unmatched`: ese kind es del producto que falta, y usarlo para un añadido
//     inventaría un renglón que el cliente no pidió (D-041.24).
//
// # LA CANTIDAD DEL AÑADIDO ES 1, Y ES UNA DECISIÓN
//
// No se multiplica por la del ítem que lo acompaña. «Dos hamburguesas con queso» no dice
// cuántos quesos son: puede ser uno en cada una o uno en total. D-041.24 zanjó que el
// añadido facturable **es del pedido**, no de una hamburguesa concreta —por eso no nace
// `parent_item_id`—, así que multiplicar sería inventar una cantidad que el cliente no
// dijo. El dueño la ajusta en la bandeja si hace falta.
//
// # NI FUZZY NI ZONA GRIS
//
// Un añadido que no casa cae a `customization`: sin precio, pero llega a producción. Es
// un desenlace SEGURO, y gastar en él una llamada al modelo —2-4 s— para convertirlo en
// un cobro es pagar por añadir un cargo que nadie ha confirmado.
func addonLines(sc *scanner, candidates []string, evidence string) (lines []Line, withoutArticle []string) {
	for _, c := range candidates {
		text := strings.TrimSpace(c)
		if text == "" {
			continue
		}
		if strings.HasPrefix(textmatch.Normalize(text), negationPrefix) {
			withoutArticle = append(withoutArticle, text)
			continue
		}
		f, ok := byKey(sc.idx, text)
		if !ok {
			f, ok = sc.searchNGram(text)
		}
		if !ok {
			withoutArticle = append(withoutArticle, text)
			continue
		}
		line, _ := withArticle(Line{Kind: KindMatched, Qty: 1, Evidence: evidence}, f, nil)
		lines = append(lines, line)
	}
	return lines, withoutArticle
}

// instruction une las indicaciones de una línea y las sanea con intakes.SanitizeNote
// (ver orderNote en match.go: es la MISMA columna que escribe el carrito).
//
// # SI NO CABE, SE PIERDE LA INDICACIÓN — PERO NO EN SILENCIO
//
// Pasarse de MaxNoteRunes NO trunca (REQ-33e): recortar «…y sin maní» se lleva justo el
// final, que es donde va el alérgeno. Y tampoco tumba la línea: el producto que el
// cliente pidió no desaparece porque su indicación fuera larga. Queda el Warning, que es
// lo que le dice al dueño que ahí hay algo que tiene que leer en el original.
func (s *Match) instruction(art *MatchArtifact, pos int, parts []string) string {
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	sanitized, err := intakes.SanitizeNote(strings.Join(clean, noteSeparator))
	if err != nil {
		// El error NO cita el texto: es del cliente (ADR-0034).
		s.log.Warn("match: la indicación de la línea no cabe y se descarta SIN truncar",
			"stage", intake.StageMatch, "item_pos", pos, "error", err.Error())
		art.Warnings = append(art.Warnings, Warning{ItemPos: pos, Reason: WarningNoteTooLong})
		return ""
	}
	return sanitized
}

// shippingLine construye la línea de envío, que va SIEMPRE (D-041.11): una solicitud que
// entra a `pending_approval` la lleva aunque el tenant no haya configurado zonas, porque
// el ciclo de aprobación es exactamente el momento en el que el dueño le pone precio.
//
// 🔴 QUIÉN DECIDE EL PRECIO NO ES ESTE FICHERO: es `intakes.DesiredShippingLine`, la
// misma función que usa el cierre del carrito numérico. Con UNA zona configurada no hay
// ambigüedad y su tarifa va cobrada; con ninguna o con VARIAS sale «por confirmar»,
// porque cobrarle a alguien la tarifa de otra zona porque estaba antes en la lista es
// peor que no cobrarle y que el dueño lo mire.
//
// ⚠️ La etiqueta es la canónica de `intakes` («Envío por confirmar», «Envío —
// Providencia») y no el «Envío» del ejemplo de design §7.4: ese JSON es ilustrativo.
func shippingLine(zones []intakes.ShippingZone) Line {
	dictated := intakes.DesiredShippingLine(zones)
	line := Line{
		Kind:  KindShipping,
		SKU:   intakes.ShippingSKU,
		Label: dictated.Label,
		Qty:   1,
	}
	if dictated.Priced {
		price := dictated.UnitPrice
		line.UnitPrice = &price
		return line
	}
	line.Note = shippingNoteNoZone
	return line
}
