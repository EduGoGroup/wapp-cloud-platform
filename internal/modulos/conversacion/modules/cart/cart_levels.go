// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6 (trozo «niveles»: advance y los
// step* de L1 a L6; el viejo es un solo fichero y aquí nace partido, 05 E-13)

// cart_levels.go es el CORAZÓN PURO de la sub-máquina: dada la topología fija, el
// estado actual y la entrada ya recortada y ya traducida a código, produce el nuevo
// estado, la pantalla a emitir y los efectos declarados por la transición (design.md
// §4.2). No toca Vars ni BD; Module.Step la envuelve.
//
// No exporta nada: lo que promete se ve por Module.Step. Los códigos de control son
// fijos: «0» es volver en cada nivel (L1 es la raíz y no lo ofrece), «9» es cancelar
// en los dos niveles de decisión (L5 y L6) y «3» es la tecla de las DOS ranuras de
// indicación, la de línea en L5 y la de pedido en L6 (D-041.19; ver cart_notes.go).
// El código de «Más ▾» es DINÁMICO: el mayor código numérico del nivel más uno, para
// no colisionar con ningún ítem; «3» es la tecla de indicación porque L5 y L6 no
// paginan nunca y ahí «Más ▾» no puede aparecer.
//
// Toda entrada que el nivel no reconoce es un INVÁLIDO DE OPCIÓN: se re-muestra la
// pantalla del nivel precedida de «Opción no válida. Responde con el número de una
// de las opciones.» y una línea en blanco, sin avanzar (y, dentro de un evento,
// cuenta: ver Module.Step).
//
// L1 · CATEGORÍAS (LevelCategories)
//   - el código de «Más ▾», si quedan más páginas ⇒ página siguiente;
//   - el código de una categoría —de CUALQUIER página, no solo de la que está en
//     pantalla— ⇒ L2 de esa categoría, página 0, y el efecto category_selected;
//   - lo demás, inválido.
//
// L2 · ARTÍCULOS (LevelArticles)
//   - «0» ⇒ L1, con las líneas intactas (design.md §9.C) y sin categoría, artículo,
//     página ni variante en foco;
//   - «Más ▾», si quedan más ⇒ página siguiente;
//   - el código de un artículo de la categoría ⇒ L3, la ficha SIN descripción. El
//     artículo en foco se guarda por SKU, no por código: el SKU es el identificador
//     estable y no depende de la posición en el catálogo. Elegir artículo NO declara
//     efecto;
//   - lo demás, inválido.
//
// L3 · FICHA DEL ARTÍCULO (LevelArticle)
//   - «1» ⇒ re-muestra la ficha CON la descripción y declara item_viewed {sku};
//   - «2» ⇒ agregar: el nivel de variante si el artículo tiene, o la cantidad si no
//     (variants.go);
//   - «0» ⇒ L2 de la misma categoría, página 0;
//   - lo demás, inválido.
//
// L4 · CANTIDAD (LevelQuantity)
//   - «0» ⇒ un paso atrás (variants.go);
//   - un entero >= 1 ⇒ agrega la línea AL FINAL (dos veces el mismo artículo son dos
//     líneas), pasa a L5 y declara item_added con la foto del carrito;
//   - lo demás —no numérico, negativo, con decimales— repregunta el MISMO paso con
//     «Escribe una cantidad válida (un número mayor o igual a 1).»,
//     una línea en blanco y la pantalla de la cantidad (design.md §9.D). No es un
//     inválido de opción.
//
// L5 · CONTINUAR (LevelContinue)
//   - «1» ⇒ agregar más de la MISMA categoría: L2 con la categoría intacta (§9.C);
//   - «2» ⇒ L6, el resumen;
//   - «3» ⇒ indicación para la línea recién agregada (cart_notes.go);
//   - «9» ⇒ cancela el pedido completo: LevelCancelled, la pantalla de cancelado y
//     el efecto cart_cancelled;
//   - «0» ⇒ vuelve a la ficha del artículo en foco;
//   - lo demás, inválido.
//
// L6 · RESUMEN (LevelSummary) — no ofrece «volver»
//   - «1» ⇒ confirmar: el checklist del comprador si queda algún campo por
//     preguntar (buyer.go) y, si no, el cierre: LevelClosed, la pantalla de
//     confirmado y el efecto cart_closed;
//   - «2» ⇒ seguir agregando: L2 de la categoría en foco;
//   - «3» ⇒ indicación para TODO el pedido (cart_notes.go);
//   - «9» ⇒ cancela, igual que en L5;
//   - lo demás —«0» incluido—, inválido.
//
// Los niveles de variante, de indicación y del checklist están en variants.go,
// cart_notes.go y buyer.go; qué pasa cuando el catálogo ya no tiene lo que el estado
// dice tener en foco, en cart_navigation.go.

package cart

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// advance es el CORAZÓN PURO de la sub-máquina: dada la topología fija, el
// estado actual y la entrada, produce el nuevo estado, la pantalla a emitir y los
// efectos declarados por la transición (design.md §4.2). No toca Vars ni BD; el
// Module la envuelve.
func advance(cat catalogo.Catalog, st cartState, input string, size int, fields []store.BuyerField) (cartState, []string, []modules.Effect) {
	in := strings.TrimSpace(input)
	switch st.Level {
	case LevelCategories:
		return stepCategories(cat, st, in, size)
	case LevelArticles:
		return stepArticles(cat, st, in, size)
	case LevelArticle:
		return stepArticle(cat, st, in, size)
	case LevelVariant:
		return stepVariant(cat, st, in, size)
	case LevelQuantity:
		return stepQuantity(cat, st, in, size)
	case LevelContinue:
		return stepContinue(cat, st, in, size)
	case LevelItemNoteScope:
		return stepItemNoteScope(cat, st, in)
	case LevelItemNote:
		return stepItemNote(cat, st, in)
	case LevelSummary:
		return stepSummary(cat, st, in, size, fields)
	case LevelOrderNote:
		return stepOrderNote(st, in)
	case LevelBuyerData:
		return stepBuyerData(st, in, fields)
	case LevelClosed, LevelCancelled:
		// Terminal: la entrada se ignora, se re-muestra la pantalla final. Desde #24
		// (LevelClosed) y desde el #29 (LevelCancelled, decisión de Jhoan 2026-08-11)
		// esta rama NO es alcanzable en el camino caliente para NINGUNO de los dos
		// niveles: Step (arriba) fija Next al centinela en el MISMO turno en que se
		// alcanza cualquiera de los dos, así que engine.Step ignora cualquier turno
		// siguiente ANTES de llegar aquí (st.Finished()==true). Se conserva —barato y
		// sin ambigüedad— por si algún Vars legado quedara con Level=Closed/Cancelled
		// sin el centinela puesto (p. ej. una fila escrita antes de estas tareas).
		return st, []string{terminalScreen(st)}, nil
	default:
		// Estado inconsistente: reencauzar a la raíz (preservando Started). La nota
		// del pedido se conserva por la misma razón que las líneas: es algo que el
		// cliente ya dijo, y un nivel que no existe no es motivo para olvidarlo.
		// BuyerIdx viaja con ellas por una razón más dura: los campos que cuenta YA
		// están escritos y cifrados, y reiniciarlo volvería a pedirle al cliente un
		// dato personal que ya dio.
		st = cartState{Level: LevelCategories, Lines: st.Lines,
			Started: st.Started, Note: st.Note, BuyerIdx: st.BuyerIdx}
		return st, []string{screenCategories(cat, st, size)}, nil
	}
}

// --- L1 · Categorías -------------------------------------------------------

func stepCategories(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	codes := categoryCodes(cat)
	if in == moreCode(codes) && hasMore(len(cat.Categories), st.Page, size) {
		st.Page++
		return st, []string{screenCategories(cat, st, size)}, nil
	}
	if c, ok := findCategory(cat, in); ok {
		st.Level = LevelArticles
		st.CatCode = c.Code
		st.Page = 0
		eff := event(EffectCategorySelected, map[string]any{"category_code": c.Code})
		return st, []string{screenArticles(c, st, size)}, []modules.Effect{eff}
	}
	st, outs := reprompt(st, screenCategories(cat, st, size))
	return st, outs, nil
}

// --- L2 · Artículos de la categoría ---------------------------------------

func stepArticles(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, ok := findCategory(cat, st.CatCode)
	if !ok {
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	}
	if in == codeBack {
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	}
	codes := articleCodes(category)
	if in == moreCode(codes) && hasMore(len(category.Items), st.Page, size) {
		st.Page++
		return st, []string{screenArticles(category, st, size)}, nil
	}
	if a, ok := findArticle(category, in); ok {
		st.Level = LevelArticle
		st.SKU = a.SKU
		st.Page = 0
		return st, []string{screenArticle(a, false)}, nil
	}
	st, outs := reprompt(st, screenArticles(category, st, size))
	return st, outs, nil
}

// --- L3 · Menú del artículo ------------------------------------------------

func stepArticle(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	switch in {
	case "1": // Ver descripción → item_viewed{sku}; re-muestra L3 con la descripción.
		eff := event(EffectItemViewed, map[string]any{"sku": a.SKU})
		return st, []string{screenArticle(a, true)}, []modules.Effect{eff}
	case "2": // Agregar al pedido → L3b variante (si tiene) o L4 cantidad.
		st, screen := toAdd(st, a)
		return st, []string{screen}, nil
	case codeBack:
		st, outs := toArticlesOf(category, st, size)
		return st, outs, nil
	default:
		st, outs := reprompt(st, screenArticle(a, false))
		return st, outs, nil
	}
}

// --- L3b · Variante del artículo (Plan 041 · D-041.4) ----------------------

// stepVariant resuelve la elección de variante: la lista va numerada POR
// POSICIÓN (1..N), "0" vuelve a la ficha del artículo y cualquier otra cosa
// reprompt. Este nivel SOLO existe para artículos con variantes; uno sin ellas
// nunca lo pisa (REQ-08: el flujo v1 no gana un paso).
func stepVariant(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	_, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	if !a.HasVariants() {
		// El catálogo cambió bajo los pies (el artículo perdió sus variantes):
		// se sigue por el camino sin variante en vez de quedar atrapado.
		st.Level = LevelQuantity
		st.VariantCode = ""
		return st, []string{screenQuantity(a)}, nil
	}
	if in == codeBack {
		st.Level = LevelArticle
		st.VariantCode = ""
		return st, []string{screenArticle(a, false)}, nil
	}
	if v, ok := variantByPosition(a, in); ok {
		st.Level = LevelQuantity
		st.VariantCode = v.Code
		return st, []string{screenQuantityOf(lineLabel(a, v, true))}, nil
	}
	st, outs := reprompt(st, screenVariants(a))
	return st, outs, nil
}

// --- L4 · Cantidad ---------------------------------------------------------

func stepQuantity(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	v, hasVariant := selectedVariant(a, st)
	if in == codeBack {
		st, screen := backFromQuantity(st, a)
		return st, []string{screen}, nil
	}
	if a.HasVariants() && !hasVariant {
		// Estado incoherente (la variante elegida ya no existe en el catálogo):
		// se vuelve a preguntar en vez de cobrar el precio de referencia, que es
		// justo lo que el contrato v2 prohíbe.
		st.Level = LevelVariant
		st.VariantCode = ""
		return st, []string{screenVariants(a)}, nil
	}
	qty, err := strconv.Atoi(in)
	if err != nil || qty < 1 {
		// Entrada no numérica o < 1 → reprompt del MISMO paso (design.md §9.D).
		return st, []string{"Escribe una cantidad válida (un número mayor o igual a 1).\n\n" + screenQuantityOf(lineLabel(a, v, hasVariant))}, nil
	}
	// item_added: agrega la línea y pasa a L5 continuar. El runtime, al recibir
	// este efecto, ASEGURA una solicitud "open" por (tenant, contact) (design.md §3.4).
	// Con variante la línea lleva SU precio y SU etiqueta y el sku compuesto; el
	// combo (components) es UNA línea al precio del artículo, sin nada especial:
	// sus componentes son información del dueño, no artículos que se cobren.
	line := newLine(a, v, hasVariant, qty)
	st.Lines = append(cloneLines(st.Lines), line)
	st.Level = LevelContinue
	st.VariantCode = ""
	eff := withLineSnapshot(event(EffectItemAdded, map[string]any{
		"sku":        line.SKU,
		"label":      line.Label,
		"qty":        line.Qty,
		"unit_price": line.UnitPrice,
	}), st.Lines)
	return st, []string{screenContinue(category)}, []modules.Effect{eff}
}

// --- L5 · Continuar --------------------------------------------------------

func stepContinue(cat catalogo.Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, hasCat := findCategory(cat, st.CatCode)
	switch in {
	case "1": // Agregar más de la MISMA categoría → L2 (CatCode intacto, design.md §9.C).
		if !hasCat {
			st, outs := toCategories(cat, st, size)
			return st, outs, nil
		}
		st, outs := toArticlesOf(category, st, size)
		return st, outs, nil
	case "2": // Finalizar → L6 resumen.
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	case codeNote: // Indicación para la línea recién agregada (D-041.19).
		return toItemNote(cat, st, size)
	case codeCancel: // Cancelar pedido completo (design.md §1.2) → cart_cancelled.
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	case codeBack: // Volver al artículo en foco (L3).
		if _, a, ok := locate(cat, st.CatCode, st.SKU); ok {
			st.Level = LevelArticle
			return st, []string{screenArticle(a, false)}, nil
		}
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	default:
		category = mustCategory(category, hasCat)
		st, outs := reprompt(st, screenContinue(category))
		return st, outs, nil
	}
}

// --- L6 · Resumen y confirmar ---------------------------------------------

func stepSummary(cat catalogo.Catalog, st cartState, in string, size int, fields []store.BuyerField) (cartState, []string, []modules.Effect) {
	switch in {
	case "1": // Confirmar → checklist del comprador si lo hay, y si no cierra.
		// El checklist (D-041.13) se intercala AQUÍ y en ningún otro sitio: entre
		// "confirmo" y el pedido cerrado. Con buyer_fields vacío —el default de la
		// columna y el caso de todos los tenants de hoy— esta rama es literalmente el
		// cierre de siempre, misma pantalla y mismo efecto (INV-15).
		if pending := pendingBuyerFields(fields, st.BuyerIdx); len(pending) > 0 {
			st.Level = LevelBuyerData
			return st, []string{screenBuyerData(pending[0], st.BuyerIdx, len(fields))}, nil
		}
		return closeCart(st)
	case "2": // Seguir agregando → L2 misma categoría, o L1 si no hay categoría en foco.
		if category, ok := findCategory(cat, st.CatCode); ok {
			st, outs := toArticlesOf(category, st, size)
			return st, outs, nil
		}
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	case codeNote: // Indicación para TODO el pedido (D-041.19).
		st.Level = LevelOrderNote
		return st, []string{screenOrderNote(st.Note)}, nil
	case codeCancel:
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	default:
		st, outs := reprompt(st, screenSummary(st.Lines, st.Note))
		return st, outs, nil
	}
}
