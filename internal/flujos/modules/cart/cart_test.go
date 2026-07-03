package cart

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// catalogRaw es el snapshot crudo del catálogo (misma forma que
// model.Content.Raw tras el round-trip JSONB: números float64, listas []any,
// objetos map[string]any). Bebidas (Café, Té) y Postres (Flan).
func catalogRaw() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{"code": "1", "label": "Bebidas", "items": []any{
				map[string]any{"code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble"},
				map[string]any{"code": "2", "sku": "TE", "label": "Té", "price": 2.0, "description": "Verde o negro"},
			}},
			map[string]any{"code": "2", "label": "Postres", "items": []any{
				map[string]any{"code": "1", "sku": "FLAN", "label": "Flan", "price": 3.0, "description": "Casero"},
			}},
		},
	}
}

// seededVars devuelve Vars con el snapshot del catálogo sembrado (lo que hará el
// runtime en T2; en T1 lo hacen los tests).
func seededVars() map[string]any {
	return map[string]any{catalogVarKey: catalogRaw()}
}

// drive aplica un Step y devuelve el nuevo estado, los outputs y las Vars
// resultantes (para encadenar). Los efectos declarados (T2) no se inspeccionan
// aquí: las pantallas/estado son idénticos al T1; los efectos se prueban aparte
// (cart_effects_test.go).
func drive(t *testing.T, m Module, vars map[string]any, input string) (cartState, []string, map[string]any) {
	t.Helper()
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, input)
	return loadState(res.Vars), res.Outputs, res.Vars
}

func joined(outs []string) string { return strings.Join(outs, "\n") }

func mustContain(t *testing.T, outs []string, subs ...string) {
	t.Helper()
	s := joined(outs)
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Fatalf("salida %q no contiene %q", s, sub)
		}
	}
}

// --- Render ----------------------------------------------------------------

func TestRenderShowsCategoriesPageZero(t *testing.T) {
	m := New()
	outs := m.Render(model.Node{}, model.Content{Raw: catalogRaw()})
	mustContain(t, outs, "Elige una categoría", "1) Bebidas", "2) Postres")
	if strings.Contains(joined(outs), "Volver") {
		t.Fatalf("L1 es raíz: no debe ofrecer Volver: %q", joined(outs))
	}
}

func TestRenderCatalogUnavailable(t *testing.T) {
	m := New()
	outs := m.Render(model.Node{}, model.Content{}) // sin Raw
	mustContain(t, outs, "catálogo no está disponible")
}

// --- Recorrido feliz completo ---------------------------------------------

func TestHappyPath(t *testing.T) {
	m := New()
	vars := seededVars()

	// L1 → elige Bebidas.
	st, outs, vars := drive(t, m, vars, "1")
	assertArticlesFocus(t, st, "1", "esperaba articles/Bebidas, got %+v")
	mustContain(t, outs, "Bebidas:", "1) Café", "2) Té", "0) ← Volver")

	// L2 → elige Café.
	st, outs, vars = drive(t, m, vars, "1")
	assertArticleFocus(t, st, "CAFE", "esperaba article/CAFE, got %+v")
	mustContain(t, outs, "Café", "Ver descripción", "Agregar al pedido")

	// L3 → Agregar.
	st, outs, vars = drive(t, m, vars, "2")
	assertLevel(t, st, LevelQuantity, "esperaba quantity, got %+v")
	mustContain(t, outs, "Cuántos", "Café")

	// L4 → cantidad 2.
	st, outs, vars = drive(t, m, vars, "2")
	assertContinueWithLine(t, st, "CAFE", 2, "esperaba continue con línea CAFE x2, got %+v")
	mustContain(t, outs, "Agregar más de Bebidas", "Finalizar", "Cancelar")

	// L5 → Agregar más (misma categoría).
	st, _, vars = drive(t, m, vars, "1")
	assertArticlesFocus(t, st, "1", "esperaba volver a articles Bebidas, got %+v")

	// Volver a categorías (0), conservando la línea.
	st, _, vars = drive(t, m, vars, "0")
	assertCategoriesWithLines(t, st, 1, "esperaba categories con línea intacta, got %+v")

	// Otra categoría: Postres → Flan → agregar → cantidad 1.
	st, _, vars = drive(t, m, vars, "2")
	assertArticlesFocus(t, st, "2", "esperaba articles Postres, got %+v")
	_, _, vars = drive(t, m, vars, "1")  // Flan
	_, _, vars = drive(t, m, vars, "2")  // agregar
	st, _, vars = drive(t, m, vars, "1") // cantidad 1
	assertContinueWithLineCount(t, st, 2, "esperaba 2 líneas, got %+v")

	// Finalizar → resumen con total 8.00.
	st, outs, vars = drive(t, m, vars, "2")
	assertLevel(t, st, LevelSummary, "esperaba summary, got %+v")
	mustContain(t, outs, "Café x2", "Flan x1", "TOTAL  $8.00", "Confirmar")

	// Confirmar → cerrado.
	st, outs, _ = drive(t, m, vars, "1")
	assertLevel(t, st, LevelClosed, "esperaba closed, got %+v")
	mustContain(t, outs, "Pedido confirmado", "$8.00")
}

// --- "Volver" en cada nivel ------------------------------------------------

func TestVolverArticlesToCategories(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // → articles Bebidas
	st, outs, _ := drive(t, m, vars, "0")
	if st.Level != LevelCategories || st.CatCode != "" {
		t.Fatalf("volver de articles debe ir a categories, got %+v", st)
	}
	mustContain(t, outs, "Elige una categoría")
}

func TestVolverArticleToArticles(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article Café
	st, outs, _ := drive(t, m, vars, "0")
	if st.Level != LevelArticles || st.SKU != "" {
		t.Fatalf("volver de article debe ir a articles, got %+v", st)
	}
	mustContain(t, outs, "Bebidas:")
}

func TestVolverQuantityToArticle(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article
	_, _, vars = drive(t, m, vars, "2") // quantity
	st, outs, _ := drive(t, m, vars, "0")
	if st.Level != LevelArticle {
		t.Fatalf("volver de quantity debe ir a article, got %+v", st)
	}
	mustContain(t, outs, "Café", "Agregar al pedido")
}

func TestVolverContinueToArticle(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article
	_, _, vars = drive(t, m, vars, "2") // quantity
	_, _, vars = drive(t, m, vars, "3") // cantidad 3 → continue
	st, _, _ := drive(t, m, vars, "0")
	if st.Level != LevelArticle || st.SKU != "CAFE" {
		t.Fatalf("volver de continue debe ir al artículo en foco, got %+v", st)
	}
}

// --- Paginación "Más ▾" ----------------------------------------------------

func TestPaginationCategories(t *testing.T) {
	m := New(WithPageSize(1)) // 1 categoría por página; hay 2 categorías.
	vars := seededVars()
	// Render/reprompt de L1: Bebidas + "Más ▾" con código 3 (max cód 2 +1).
	st := cartState{Level: LevelCategories}
	scr := screenCategories(mustCatalog(t), st, 1)
	mustContain(t, []string{scr}, "1) Bebidas", "3) Más ▾")
	if strings.Contains(scr, "Postres") {
		t.Fatalf("Postres no debe aparecer en página 0: %q", scr)
	}
	// "3" → página 1 con Postres.
	st, outs, vars := drive(t, m, vars, "3")
	if st.Level != LevelCategories || st.Page != 1 {
		t.Fatalf("esperaba categories page 1, got %+v", st)
	}
	mustContain(t, outs, "2) Postres")
	if strings.Contains(joined(outs), "Más ▾") {
		t.Fatalf("página final no debe ofrecer Más: %q", joined(outs))
	}
	// Elegir Postres desde la página 1.
	st, _, _ = drive(t, m, vars, "2")
	if st.Level != LevelArticles || st.CatCode != "2" {
		t.Fatalf("esperaba articles Postres, got %+v", st)
	}
}

func TestPaginationArticles(t *testing.T) {
	m := New(WithPageSize(1)) // Bebidas tiene 2 artículos.
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles Bebidas, page 0
	st, outs, vars := drive(t, m, vars, "3")
	if st.Level != LevelArticles || st.Page != 1 {
		t.Fatalf("esperaba articles page 1, got %+v", st)
	}
	mustContain(t, outs, "Té")
	// Elegir Té (código 2) en la página 1.
	st, _, _ = drive(t, m, vars, "2")
	if st.SKU != "TE" {
		t.Fatalf("esperaba SKU TE, got %+v", st)
	}
}

// --- Cantidad inválida → reprompt -----------------------------------------

func TestInvalidQuantityReprompt(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article
	_, _, vars = drive(t, m, vars, "2") // quantity

	// No numérico → reprompt, permanece en quantity, sin líneas.
	st, outs, vars := drive(t, m, vars, "abc")
	if st.Level != LevelQuantity || len(st.Lines) != 0 {
		t.Fatalf("no numérico debe reprompt en quantity, got %+v", st)
	}
	mustContain(t, outs, "cantidad válida")

	// Cero (<1, no es volver porque volver ya se cubre): "0" es volver → article.
	st, _, vars = drive(t, m, vars, "0")
	if st.Level != LevelArticle {
		t.Fatalf("'0' en quantity es volver, got %+v", st)
	}
	// Volver a quantity y probar negativo.
	_, _, vars = drive(t, m, vars, "2") // quantity
	st, _, _ = drive(t, m, vars, "-3")
	if st.Level != LevelQuantity || len(st.Lines) != 0 {
		t.Fatalf("negativo debe reprompt en quantity, got %+v", st)
	}
}

// --- Cancelar = 9 ----------------------------------------------------------

func TestCancelFromContinue(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article
	_, _, vars = drive(t, m, vars, "2") // quantity
	_, _, vars = drive(t, m, vars, "1") // cantidad → continue
	st, outs, _ := drive(t, m, vars, "9")
	if st.Level != LevelCancelled {
		t.Fatalf("9 en continue debe cancelar, got %+v", st)
	}
	mustContain(t, outs, "cancelado")
}

func TestCancelFromSummary(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article
	_, _, vars = drive(t, m, vars, "2") // quantity
	_, _, vars = drive(t, m, vars, "1") // cantidad → continue
	_, _, vars = drive(t, m, vars, "2") // finalizar → summary
	st, outs, _ := drive(t, m, vars, "9")
	if st.Level != LevelCancelled {
		t.Fatalf("9 en summary debe cancelar, got %+v", st)
	}
	mustContain(t, outs, "cancelado")
}

// --- Categoría / artículo inexistente → reprompt --------------------------

func TestCategoryNotExistReprompt(t *testing.T) {
	m := New()
	vars := seededVars()
	st, outs, _ := drive(t, m, vars, "99")
	if st.Level != LevelCategories {
		t.Fatalf("categoría inexistente debe permanecer en categories, got %+v", st)
	}
	mustContain(t, outs, "Opción no válida", "Elige una categoría")
}

func TestArticleNotExistReprompt(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles Bebidas
	st, outs, _ := drive(t, m, vars, "7")
	if st.Level != LevelArticles {
		t.Fatalf("artículo inexistente debe permanecer en articles, got %+v", st)
	}
	mustContain(t, outs, "Opción no válida", "Bebidas:")
}

// --- Ver descripción -------------------------------------------------------

func TestVerDescripcionStaysArticle(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	_, _, vars = drive(t, m, vars, "1") // article Café
	st, outs, _ := drive(t, m, vars, "1")
	if st.Level != LevelArticle {
		t.Fatalf("ver descripción debe permanecer en article, got %+v", st)
	}
	mustContain(t, outs, "Espresso doble", "Agregar al pedido")
}

// --- Una categoría a la vez (Agregar más reofrece la MISMA) ----------------

func TestAddMoreKeepsSameCategory(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "2") // Postres
	_, _, vars = drive(t, m, vars, "1") // Flan
	_, _, vars = drive(t, m, vars, "2") // agregar
	_, _, vars = drive(t, m, vars, "1") // cantidad 1 → continue
	st, outs, _ := drive(t, m, vars, "1")
	if st.Level != LevelArticles || st.CatCode != "2" {
		t.Fatalf("agregar más debe reofrecer la MISMA categoría (Postres), got %+v", st)
	}
	mustContain(t, outs, "Postres:")
}

// --- Seguir agregando desde el resumen ------------------------------------

func TestSeguirAgregandoFromSummary(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles Bebidas
	_, _, vars = drive(t, m, vars, "1") // Café
	_, _, vars = drive(t, m, vars, "2") // agregar
	_, _, vars = drive(t, m, vars, "1") // cantidad
	_, _, vars = drive(t, m, vars, "2") // finalizar → summary
	st, _, _ := drive(t, m, vars, "2")
	if st.Level != LevelArticles || st.CatCode != "1" {
		t.Fatalf("seguir agregando debe volver a la categoría en foco, got %+v", st)
	}
}

// --- Terminal ignora la entrada -------------------------------------------

func TestTerminalIgnoresInput(t *testing.T) {
	m := New()
	vars := seededVars()
	st := cartState{Level: LevelClosed, Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5}}}
	storeState(vars, st)
	got, outs, _ := drive(t, m, vars, "cualquier cosa")
	if got.Level != LevelClosed {
		t.Fatalf("closed debe permanecer terminal, got %+v", got)
	}
	mustContain(t, outs, "Pedido confirmado", "$2.50")
}

// --- Catálogo no sembrado en Step -----------------------------------------

func TestStepWithoutCatalog(t *testing.T) {
	m := New()
	res := m.Step(model.Node{}, model.Conversation{Vars: map[string]any{}}, "1")
	mustContain(t, res.Outputs, "catálogo no está disponible")
}

// --- Round-trip del estado en Vars ----------------------------------------

func TestStateRoundTripsThroughVars(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = drive(t, m, vars, "1") // articles
	// La sub-clave "cart" debe ser un map serializable (no un struct).
	if _, ok := vars[stateVarKey].(map[string]any); !ok {
		t.Fatalf("Vars[cart] debe ser map[string]any tras Step, got %T", vars[stateVarKey])
	}
	st := loadState(vars)
	if st.Level != LevelArticles || st.CatCode != "1" {
		t.Fatalf("estado no round-trip correctamente, got %+v", st)
	}
}

// assertArticlesFocus verifica que el estado esté en el nivel articles con la
// categoría enfocada indicada.
func assertArticlesFocus(t *testing.T, st cartState, wantCat string, msg string) {
	t.Helper()
	if st.Level != LevelArticles || st.CatCode != wantCat {
		t.Fatalf(msg, st)
	}
}

// assertArticleFocus verifica que el estado esté en el nivel article con el
// SKU enfocado indicado.
func assertArticleFocus(t *testing.T, st cartState, wantSKU string, msg string) {
	t.Helper()
	if st.Level != LevelArticle || st.SKU != wantSKU {
		t.Fatalf(msg, st)
	}
}

// assertLevel verifica únicamente el nivel del estado.
func assertLevel(t *testing.T, st cartState, want string, msg string) {
	t.Helper()
	if st.Level != want {
		t.Fatalf(msg, st)
	}
}

// assertContinueWithLine verifica el nivel continue con exactamente una línea
// que coincide en SKU y cantidad.
func assertContinueWithLine(t *testing.T, st cartState, wantSKU string, wantQty int, msg string) {
	t.Helper()
	if st.Level != LevelContinue || len(st.Lines) != 1 || st.Lines[0].Qty != wantQty || st.Lines[0].SKU != wantSKU {
		t.Fatalf(msg, st)
	}
}

// assertCategoriesWithLines verifica el nivel categories con el número de
// líneas acumuladas indicado.
func assertCategoriesWithLines(t *testing.T, st cartState, wantLines int, msg string) {
	t.Helper()
	if st.Level != LevelCategories || len(st.Lines) != wantLines {
		t.Fatalf(msg, st)
	}
}

// assertContinueWithLineCount verifica el nivel continue con el número de
// líneas acumuladas indicado (sin fijar SKU/cantidad).
func assertContinueWithLineCount(t *testing.T, st cartState, wantLines int, msg string) {
	t.Helper()
	if st.Level != LevelContinue || len(st.Lines) != wantLines {
		t.Fatalf(msg, st)
	}
}

func mustCatalog(t *testing.T) Catalog {
	t.Helper()
	c, err := ParseCatalog(model.Content{Raw: catalogRaw()})
	if err != nil {
		t.Fatalf("parse catálogo: %v", err)
	}
	return c
}
