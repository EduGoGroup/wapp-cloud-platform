// prime.go implementa la capacidad modules.Primer del carrito (Plan 029 · T8,
// design.md §4.c): al ARRANCAR el flujo por una decisión kind='llm', el runtime
// siembra en Vars los intent_params extraídos por el clasificador (p. ej.
// {producto:"pizza", cantidad:"2"}); el carrito los CONSUME aquí, hace matching
// difuso contra el catálogo del tenant y —si hay un match claro— pre-agrega la línea
// y salta directo al estado de confirmación de ítem (LevelContinue), en vez de
// mostrar el listado de categorías vacío. "el LLM extrae, el código resuelve".
//
// PUREZA (invariante del módulo): sin I/O. El catálogo llega ya resuelto (Prime recibe
// model.Content, igual que Render); el matching es en memoria. Si no hay match o es
// ambiguo, arranca el flujo normal desde categorías SIN inventar nada.
//
// El matching difuso (normalize/commonPrefix/matchArticle) es un PORTE de
// miniWapp/handlers_negocio.go (normalizar/prefijoComun/buscarArticulo) renombrado al
// estilo del módulo (ADR-0004, copia-adaptación).
package cart

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// paramProducto y paramCantidad son las claves de intent_params que el carrito
// interpreta (el resto se ignora). El clasificador las extrae del mensaje; el
// vocabulario lo fija la config de intents del tenant (wapp-shared/intents).
const (
	paramProducto = "producto"
	paramCantidad = "cantidad"
)

// Prime pre-carga el carrito a partir de los intent_params sembrados en Vars.
// Contrato de modules.Primer:
//   - handled=false ⇒ el engine hace el Render normal (sin params, o catálogo no
//     disponible: se deja el camino de siempre, que muestra catalogUnavailable).
//   - handled=true  ⇒ el módulo consumió los params (los limpia de Vars, UNA sola
//     vez) y produjo su estado/pantalla: con match claro, la línea pre-agregada +
//     la confirmación de ítem (LevelContinue) + los efectos cart_started/item_added;
//     sin producto o sin match/ambiguo, el arranque normal desde categorías (L1).
func (m Module) Prime(_ model.Node, content model.Content, vars map[string]any) (modules.Result, bool) {
	params := readIntentParams(vars)
	if params == nil {
		return modules.Result{}, false // sin intent_params ⇒ Render normal (no-regresión)
	}
	cat, err := ParseCatalog(content)
	if err != nil {
		return modules.Result{}, false // catálogo no disponible ⇒ Render normal (catalogUnavailable)
	}

	// A partir de aquí SIEMPRE consumimos los params (una sola vez): sea con pre-add o
	// con arranque normal, no deben persistir en Vars ni reaplicarse en el próximo Step.
	out := cloneVars(vars)
	delete(out, modules.VarIntentParams)
	delete(out, modules.VarIntentName)

	producto := strings.TrimSpace(params[paramProducto])
	category, article, ok := matchArticle(cat, producto)
	if producto == "" || !ok {
		// Sin producto o sin match único/claro (o ambiguo): flujo normal desde L1. El
		// cart_started lo emitirá el primer Step (Started queda false), como en el
		// arranque normal ⇒ no-regresión de telemetría.
		return modules.Result{
			Vars:    out,
			Outputs: []string{screenCategories(cat, cartState{Level: LevelCategories}, m.pageSize)},
		}, true
	}

	// Match claro: pre-agrega la línea y salta a la confirmación de ítem (LevelContinue,
	// el MISMO estado al que lleva stepQuantity tras un add). cantidad ausente/inválida
	// ⇒ 1. Emite cart_started (arranque) + item_added (el runtime ASEGURA la orden
	// "open" con este efecto, design.md §3.4): mismo contrato que el add manual.
	qty := parseQty(params[paramCantidad])
	st := cartState{
		Level:   LevelContinue,
		CatCode: category.Code,
		SKU:     article.SKU,
		Started: true,
		Lines:   []cartLine{{SKU: article.SKU, Label: article.Label, Qty: qty, UnitPrice: article.Price}},
	}
	storeState(out, st)
	effects := []modules.Effect{
		event(EffectCartStarted, map[string]any{}),
		event(EffectItemAdded, map[string]any{
			"sku":        article.SKU,
			"label":      article.Label,
			"qty":        qty,
			"unit_price": article.Price,
		}),
	}
	return modules.Result{
		Vars:    out,
		Outputs: []string{primeAddedScreen(article, qty, category)},
		Effects: effects,
	}, true
}

// primeAddedScreen antecede a la pantalla de confirmación de ítem (screenContinue, el
// estado LevelContinue) con una línea que nombra lo pre-agregado, para que el cliente
// vea QUÉ se agregó a partir de su intención (no solo "Añadido al pedido ✅").
func primeAddedScreen(a Article, qty int, category Category) string {
	head := "Agregué " + strconv.Itoa(qty) + " × " + a.Label + " (" + money(a.Price) + " c/u) a tu pedido."
	return head + "\n\n" + screenContinue(category)
}

// parseQty interpreta la cantidad de intent_params: entero >= 1 o, ausente/inválida,
// 1 (design.md §4.c). El clasificador puede omitirla o extraer basura; el código
// nunca falla por eso.
func parseQty(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// readIntentParams lee Vars[VarIntentParams] tolerando el tipo nativo (map[string]
// string, recién sembrado por el runtime) y el round-trip JSONB (map[string]any con
// valores string). Ausente/nil/forma inesperada ⇒ nil (⇒ Prime devuelve handled=false).
func readIntentParams(vars map[string]any) map[string]string {
	raw, ok := vars[modules.VarIntentParams]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		if len(m) == 0 {
			return nil
		}
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// --- matching difuso producto↔catálogo (porte de miniWapp, ADR-0004) -------------

// normalize canoniza un texto para el matching difuso: minúsculas + strip de
// diacríticos comunes del español (porte de miniWapp `normalizar`). Basta el
// reemplazo directo para el vocabulario de catálogo (no requiere NFD).
func normalize(s string) string {
	s = strings.ToLower(s)
	repl := strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ñ", "n")
	return repl.Replace(s)
}

// commonPrefix tolera plurales y errores leves de tipeo comparando prefijos de 4+
// letras (porte de miniWapp `prefijoComun`): con palabras < 4 letras exige igualdad.
func commonPrefix(a, b string) bool {
	if len(a) < 4 || len(b) < 4 {
		return a == b
	}
	n := min(len(a), len(b), 5)
	return a[:n] == b[:n]
}

// matchArticle hace matching difuso del texto del cliente contra TODO el catálogo
// (porte de miniWapp `buscarArticulo`): gana el artículo con más palabras en común
// ("pizzas de peperoni" → "Pizza pepperoni"). Devuelve ok=false si NADA casa (mejor
// score 0) o si hay EMPATE en el mejor score (ambiguo): el llamante cae al flujo
// normal, nunca inventa. También devuelve la categoría del artículo ganador (para
// fijar CatCode y renderizar la confirmación de la categoría en foco).
func matchArticle(cat Catalog, query string) (Category, Article, bool) {
	qWords := strings.Fields(normalize(query))
	if len(qWords) == 0 {
		return Category{}, Article{}, false
	}
	var (
		bestCat     Category
		bestArticle Article
		bestScore   int
		tie         bool
	)
	for ci := range cat.Categories {
		c := cat.Categories[ci]
		for ii := range c.Items {
			it := c.Items[ii]
			nWords := strings.Fields(normalize(it.Label))
			score := 0
			for _, qw := range qWords {
				for _, nw := range nWords {
					if commonPrefix(qw, nw) {
						score++
					}
				}
			}
			switch {
			case score > bestScore:
				bestCat, bestArticle, bestScore, tie = c, it, score, false
			case score == bestScore && score > 0:
				tie = true
			}
		}
	}
	if bestScore == 0 || tie {
		return Category{}, Article{}, false
	}
	return bestCat, bestArticle, true
}
