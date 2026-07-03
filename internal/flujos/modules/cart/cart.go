// Package cart es el módulo del carrito conversacional (Plan 016, design.md §4):
// una sub-máquina jerárquica que navega un catálogo de dos niveles
// (categorías → artículos), acumula líneas de pedido y cierra con un resumen.
// Es el tercer módulo del Motor de Flujos tras Menú (Plan 006) y Encuesta
// (Plan 014).
//
// PUREZA (invariante, design.md §2/§4.1): el módulo NO hace I/O. Recibe el
// catálogo ya resuelto y navega en memoria; todo su estado vive en
// Conversation.Vars (JSONB) y lo persiste el engine. En T1 no emite efectos ni
// persiste órdenes (Result.Effects vacío); los efectos llegan en T2 y la
// persistencia/TTL en T2/T3.
//
// UN SOLO NODO (design.md §9.A): la definición del flujo tiene un único nodo
// {"type":"cart"}; los niveles (categorías/artículos/artículo/cantidad/
// continuar/resumen) y el "volver" se manejan internamente con el estado en
// Vars, sin multiplicar nodos en flow_definitions.
//
// CATÁLOGO EN Step (nota de ejecución, design.md §9): la interfaz Module entrega
// el content resuelto SOLO a Render, no a Step (registry.go). Para que Step
// navegue sin romper la pureza, el catálogo viaja como snapshot en
// Vars["cart_catalog"] (misma forma que model.Content.Raw): en T1 lo siembran
// los tests; en T2 lo siembra el runtime al resolver el content del nodo. Así el
// engine, menú y encuesta NO se tocan y el módulo sigue siendo I/O-free.
package cart

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// catalogUnavailable es la pantalla que se muestra cuando no hay catálogo con el
// que navegar (Raw ausente / snapshot no sembrado). No debería ocurrir en el
// camino real: el runtime siembra el catálogo antes del primer Step (T2).
const catalogUnavailable = "El catálogo no está disponible en este momento. Intenta más tarde."

// Module implementa modules.Module para el tipo de nodo "cart".
type Module struct {
	pageSize int
}

// Option configura el Module al construirlo (patrón functional-options).
type Option func(*Module)

// WithPageSize fija el tamaño de página de los niveles de lista. Un valor <= 0
// se ignora (se mantiene el default). En T1 es la vía PURA para probar la
// paginación; el cableado desde tenant_settings llega en T3.
func WithPageSize(n int) Option {
	return func(m *Module) {
		if n > 0 {
			m.pageSize = n
		}
	}
}

// New crea el módulo Carrito con el tamaño de página por defecto (design.md §9.E).
func New(opts ...Option) Module {
	m := Module{pageSize: DefaultPageSize}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return NodeTypeCart }

// WaitsForInput indica que el carrito es interactivo: se renderiza y detiene el
// flujo esperando la entrada del usuario (igual que menú/encuesta).
func (Module) WaitsForInput() bool { return true }

// Render produce la pantalla de ARRANQUE del carrito: la lista de categorías
// (L1, página 0). Recibe el catálogo ya resuelto por el engine (model.Content.Raw
// vía ParseCatalog). El resto de pantallas (tras cada Step) las produce Step en
// Result.Outputs, porque el carrito permanece en el MISMO nodo (Next==nil) y el
// engine no vuelve a llamar Render dentro de la sub-máquina.
func (m Module) Render(_ model.Node, content model.Content) []string {
	cat, err := ParseCatalog(content)
	if err != nil {
		return []string{catalogUnavailable}
	}
	return []string{screenCategories(cat, cartState{Level: LevelCategories}, m.pageSize)}
}

// Step procesa la entrada del usuario sobre el nodo carrito: carga el catálogo
// (snapshot en Vars) y el cartState, aplica la transición de la sub-máquina
// (advance), guarda el nuevo estado en Vars y DECLARA los efectos de negocio
// (design.md §3.3). El carrito permanece en el mismo nodo (Next==nil) durante
// toda la navegación; devuelve la pantalla del nuevo nivel en Outputs.
//
// cart_started se emite EXACTAMENTE UNA vez, en el primer Step (flag Started): la
// pureza del módulo y el contrato de efectos impiden emitirlo en el Enter/Render
// (Render no declara Effects). Es lo más cercano a "al arranque" (design.md §3.3)
// sin tocar el contrato del engine.
func (m Module) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	vars := cloneVars(conv.Vars)
	cat, err := loadCatalog(vars)
	if err != nil {
		return modules.Result{Vars: vars, Outputs: []string{catalogUnavailable}}
	}
	// page_size REAL del tenant: el runtime lo siembra en Vars[VarPageSize]
	// (tenant_settings.page_size); sin sembrar cae al default del Module (design.md
	// §9.E). Toda la paginación de la sub-máquina (que ocurre en Step) lo respeta;
	// Render (una sola pantalla, al arranque) usa el default del Module.
	size := pageSizeFromVars(vars, m.pageSize)
	st := loadState(vars)
	var effects []modules.Effect
	if !st.Started {
		st.Started = true
		effects = append(effects, event(EffectCartStarted, map[string]any{}))
	}
	newSt, outs, stepEffects := advance(cat, st, input, size)
	effects = append(effects, stepEffects...)
	storeState(vars, newSt)
	return modules.Result{Vars: vars, Outputs: outs, Effects: effects}
}

// advance es el CORAZÓN PURO de la sub-máquina: dada la topología fija, el
// estado actual y la entrada, produce el nuevo estado, la pantalla a emitir y los
// efectos declarados por la transición (design.md §4.2). No toca Vars ni BD; el
// Module la envuelve.
func advance(cat Catalog, st cartState, input string, size int) (cartState, []string, []modules.Effect) {
	in := strings.TrimSpace(input)
	switch st.Level {
	case LevelCategories:
		return stepCategories(cat, st, in, size)
	case LevelArticles:
		return stepArticles(cat, st, in, size)
	case LevelArticle:
		return stepArticle(cat, st, in, size)
	case LevelQuantity:
		return stepQuantity(cat, st, in, size)
	case LevelContinue:
		return stepContinue(cat, st, in, size)
	case LevelSummary:
		return stepSummary(cat, st, in, size)
	case LevelClosed, LevelCancelled:
		// Terminal: la entrada se ignora, se re-muestra la pantalla final.
		return st, []string{terminalScreen(st)}, nil
	default:
		// Estado inconsistente: reencauzar a la raíz (preservando Started).
		st = cartState{Level: LevelCategories, Lines: st.Lines, OrderID: st.OrderID, Started: st.Started}
		return st, []string{screenCategories(cat, st, size)}, nil
	}
}

// --- L1 · Categorías -------------------------------------------------------

func stepCategories(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
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

func stepArticles(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, ok := findCategory(cat, st.CatCode)
	if !ok {
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	}
	if in == codeVolver {
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

func stepArticle(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	switch in {
	case "1": // Ver descripción → item_viewed{sku}; re-muestra L3 con la descripción.
		eff := event(EffectItemViewed, map[string]any{"sku": a.SKU})
		return st, []string{screenArticle(a, true)}, []modules.Effect{eff}
	case "2": // Agregar al pedido → L4 cantidad.
		st.Level = LevelQuantity
		return st, []string{screenQuantity(a)}, nil
	case codeVolver:
		st, outs := toArticlesOf(category, st, size)
		return st, outs, nil
	default:
		st, outs := reprompt(st, screenArticle(a, false))
		return st, outs, nil
	}
}

// --- L4 · Cantidad ---------------------------------------------------------

func stepQuantity(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	if in == codeVolver {
		st.Level = LevelArticle
		return st, []string{screenArticle(a, false)}, nil
	}
	qty, err := strconv.Atoi(in)
	if err != nil || qty < 1 {
		// Entrada no numérica o < 1 → reprompt del MISMO paso (design.md §9.D).
		return st, []string{"Escribe una cantidad válida (un número mayor o igual a 1).\n\n" + screenQuantity(a)}, nil
	}
	// item_added: agrega la línea y pasa a L5 continuar. El runtime, al recibir
	// este efecto, ASEGURA una orden "open" por (tenant, contact) (design.md §3.4).
	st.Lines = append(cloneLines(st.Lines), cartLine{SKU: a.SKU, Label: a.Label, Qty: qty, UnitPrice: a.Price})
	st.Level = LevelContinue
	eff := event(EffectItemAdded, map[string]any{
		"sku":        a.SKU,
		"label":      a.Label,
		"qty":        qty,
		"unit_price": a.Price,
	})
	return st, []string{screenContinue(category)}, []modules.Effect{eff}
}

// --- L5 · Continuar --------------------------------------------------------

func stepContinue(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
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
		return st, []string{screenSummary(st.Lines)}, nil
	case codeCancelar: // Cancelar pedido completo (design.md §1.2) → cart_cancelled.
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	case codeVolver: // Volver al artículo en foco (L3).
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

func stepSummary(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	switch in {
	case "1": // Confirmar → cierra: cart_closed (persist) proyecta orders/order_items.
		st.Level = LevelClosed
		return st, []string{screenClosed(total(st.Lines))}, []modules.Effect{closedEffect(st.Lines)}
	case "2": // Seguir agregando → L2 misma categoría, o L1 si no hay categoría en foco.
		if category, ok := findCategory(cat, st.CatCode); ok {
			st, outs := toArticlesOf(category, st, size)
			return st, outs, nil
		}
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	case codeCancelar:
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	default:
		st, outs := reprompt(st, screenSummary(st.Lines))
		return st, outs, nil
	}
}

// --- transiciones auxiliares ----------------------------------------------

// toCategories reencauza a L1 conservando las líneas y la orden (design.md §9.C:
// "volver" desde artículos sube a categorías con el carrito intacto).
func toCategories(cat Catalog, st cartState, size int) (cartState, []string) {
	st.Level = LevelCategories
	st.CatCode = ""
	st.SKU = ""
	st.Page = 0
	return st, []string{screenCategories(cat, st, size)}
}

// toArticles reencauza a L2 de la categoría en foco; si ya no existe, cae a L1.
func toArticles(cat Catalog, st cartState, size int) (cartState, []string) {
	if category, ok := findCategory(cat, st.CatCode); ok {
		return toArticlesOf(category, st, size)
	}
	return toCategories(cat, st, size)
}

// toArticlesOf reencauza a L2 de una categoría concreta (misma categoría).
func toArticlesOf(category Category, st cartState, size int) (cartState, []string) {
	st.Level = LevelArticles
	st.CatCode = category.Code
	st.SKU = ""
	st.Page = 0
	return st, []string{screenArticles(category, st, size)}
}

// mustCategory devuelve la categoría si es válida, o una vacía si no (para el
// reprompt de L5 cuando el catálogo cambió: se re-muestra un continuar neutro).
func mustCategory(category Category, ok bool) Category {
	if ok {
		return category
	}
	return Category{}
}

// reprompt re-muestra el nivel actual precedido de un aviso, sin avanzar
// (design.md §4.2: entrada inválida → reprompt acotado). El carrito re-emite la
// pantalla contextual completa como ayuda; el estado no cambia.
func reprompt(st cartState, screen string) (cartState, []string) {
	return st, []string{"Opción no válida. Responde con el número de una de las opciones.\n\n" + screen}
}

// --- localización en el catálogo ------------------------------------------

func findCategory(cat Catalog, code string) (Category, bool) {
	if code == "" {
		return Category{}, false
	}
	for _, c := range cat.Categories {
		if c.Code == code {
			return c, true
		}
	}
	return Category{}, false
}

func findArticle(category Category, code string) (Article, bool) {
	if code == "" {
		return Article{}, false
	}
	for _, a := range category.Items {
		if a.Code == code {
			return a, true
		}
	}
	return Article{}, false
}

// locate resuelve la categoría (por código) y el artículo en foco (por SKU) del
// estado. El artículo se guarda por SKU (identificador de negocio estable),
// no por Code, para no depender de la posición en el catálogo.
func locate(cat Catalog, catCode, sku string) (Category, Article, bool) {
	category, ok := findCategory(cat, catCode)
	if !ok {
		return Category{}, Article{}, false
	}
	for _, a := range category.Items {
		if a.SKU == sku {
			return category, a, true
		}
	}
	return category, Article{}, false
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

// --- render de pantallas ---------------------------------------------------

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
		b.WriteString("\n" + a.Code + ") " + a.Label + " · " + money(a.Price))
	}
	if end < len(category.Items) {
		b.WriteString("\n" + moreCode(articleCodes(category)) + ") Más ▾")
	}
	b.WriteString("\n" + codeVolver + ") ← Volver")
	return b.String()
}

func screenArticle(a Article, showDesc bool) string {
	var b strings.Builder
	b.WriteString(a.Label + " · " + money(a.Price))
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

func screenQuantity(a Article) string {
	return "¿Cuántos \"" + a.Label + "\"? Escribe la cantidad (" + codeVolver + " ← volver)"
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

// cloneVars copia el mapa de variables para mantener la pureza (no mutar el
// estado de entrada). nil → mapa nuevo. Mismo patrón que menu/survey.
func cloneVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
