package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// cart_navigation_test.go — qué hace la sub-máquina cuando el catálogo ya no tiene lo
// que el estado dice tener en foco: sube al nivel más cercano que existe, sin aviso
// de inválido y sin perder lo que el cliente ya dijo. cart_navigation.go no exporta
// nada: todo se ve por Module.Step.

var keptLines = []cartLine{{SKU: "TE", Label: "Té", Qty: 2, UnitPrice: 2}}

// stale es un estado cuyo foco apunta a algo que el catálogo v1 de prueba no tiene.
func stale(level, catCode, sku string) cartState {
	return cartState{Level: level, CatCode: catCode, SKU: sku, Page: 4, VariantCode: "V1",
		Started: true, Note: "en portería", Lines: keptLines}
}

// atCategories y atArticles son los dos destinos del reencauce, con lo conservado.
func atCategories() cartState {
	return cartState{Level: cart.LevelCategories, Started: true, Note: "en portería", Lines: keptLines}
}

func atArticles(catCode string) cartState {
	return cartState{Level: cart.LevelArticles, CatCode: catCode, Started: true, Note: "en portería", Lines: keptLines}
}

// La categoría o el artículo en foco desaparecieron: se sube sin reprompt, sea cual
// sea la entrada, soltando el foco, la página y la variante que ya no aplican.
func TestNavigation_StaleFocusGoesUpWithoutLosingTheOrder(t *testing.T) {
	cases := []struct {
		name   string
		state  cartState
		in     string
		want   cartState
		screen string
	}{
		{name: "articles, category gone", state: stale(cart.LevelArticles, "9", ""), in: "1",
			want: atCategories(), screen: categoriesScreen},
		{name: "articles, category gone, back", state: stale(cart.LevelArticles, "9", ""), in: "0",
			want: atCategories(), screen: categoriesScreen},
		{name: "article gone", state: stale(cart.LevelArticle, "1", "YA-NO"), in: "2",
			want: atArticles("1"), screen: bebidasScreen},
		{name: "article and category gone", state: stale(cart.LevelArticle, "9", "YA-NO"), in: "2",
			want: atCategories(), screen: categoriesScreen},
		{name: "variant, article gone", state: stale(cart.LevelVariant, "1", "YA-NO"), in: "1",
			want: atArticles("1"), screen: bebidasScreen},
		{name: "quantity, article gone", state: stale(cart.LevelQuantity, "2", "YA-NO"), in: "3",
			want: atArticles("2"), screen: postresScreen},
		{name: "quantity, article gone, back", state: stale(cart.LevelQuantity, "2", "YA-NO"), in: "0",
			want: atArticles("2"), screen: postresScreen},
		{name: "continue, add more without category", state: stale(cart.LevelContinue, "9", "CAFE"), in: "1",
			want: atCategories(), screen: categoriesScreen},
		{name: "continue, back without article", state: stale(cart.LevelContinue, "1", "YA-NO"), in: "0",
			want: atArticles("1"), screen: bebidasScreen},
		{name: "continue, back without category", state: stale(cart.LevelContinue, "9", "YA-NO"), in: "0",
			want: atCategories(), screen: categoriesScreen},
		{name: "summary, keep adding without category", state: stale(cart.LevelSummary, "9", ""), in: "2",
			want: atCategories(), screen: categoriesScreen},
		{name: "summary, keep adding without any category in focus", state: stale(cart.LevelSummary, "", ""), in: "2",
			want: atCategories(), screen: categoriesScreen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := seededVars()
			seedState(t, vars, tc.state)
			st, outs, _ := drive(t, cart.New(), vars, tc.in)
			mustScreen(t, outs, tc.screen)
			if !reflect.DeepEqual(st, tc.want) {
				t.Errorf("estado = %+v\nquiero   %+v", st, tc.want)
			}
		})
	}
}

// En L5 sin categoría, el inválido re-muestra un continuar NEUTRO, sin nombre.
func TestNavigation_ContinueWithoutCategoryShowsNeutralMenu(t *testing.T) {
	vars := seededVars()
	seedState(t, vars, cartState{Level: cart.LevelContinue, CatCode: "9", SKU: "CAFE", Started: true, Lines: keptLines})
	st, outs, _ := drive(t, cart.New(), vars, "5")
	if st.Level != cart.LevelContinue {
		t.Fatalf("estado = %+v, quiero seguir en continue", st)
	}
	mustScreen(t, outs, invalidPrefix+"Añadido al pedido ✅\n1) Agregar más\n2) Finalizar pedido\n"+
		"3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver")
}

// La localización es por igualdad EXACTA: ni se recorta ni se pliegan mayúsculas, y el
// artículo en foco se busca por SKU, no por su código de pantalla.
func TestNavigation_FocusIsLocatedByExactCodeAndSKU(t *testing.T) {
	cases := []struct {
		name  string
		state cartState
		want  string // nivel al que va «0»
	}{
		// El SKU existe: «0» desde continue vuelve a su ficha.
		{name: "sku matches", state: cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "CAFE", Started: true, Lines: keptLines}, want: cart.LevelArticle},
		// El código de pantalla del Café es "1": como SKU no localiza nada.
		{name: "screen code is not a sku", state: cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "1", Started: true, Lines: keptLines}, want: cart.LevelArticles},
		{name: "sku in another case", state: cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "cafe", Started: true, Lines: keptLines}, want: cart.LevelArticles},
		// FLAN existe, pero en otra categoría: el foco es categoría + artículo.
		{name: "sku of another category", state: cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "FLAN", Started: true, Lines: keptLines}, want: cart.LevelArticles},
		{name: "category code with spaces", state: cartState{Level: cart.LevelContinue, CatCode: " 1", SKU: "CAFE", Started: true, Lines: keptLines}, want: cart.LevelCategories},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := seededVars()
			seedState(t, vars, tc.state)
			st, _, _ := drive(t, cart.New(), vars, "0")
			if st.Level != tc.want {
				t.Errorf("estado = %+v, quiero el nivel %q", st, tc.want)
			}
		})
	}
}

// Un catálogo SIN categorías no es un catálogo: catalogo.ParseCatalog lo rechaza y el
// carrito dice que no está disponible, sin guardar estado.
func TestNavigation_CatalogWithoutCategoriesIsUnavailable(t *testing.T) {
	vars := map[string]any{"cart_catalog": map[string]any{"categories": []any{}}}
	res := turn(cart.New(), model.Conversation{Vars: vars}, "1", nil)
	mustScreen(t, res.Outputs, catalogUnavailable)
	if _, stored := res.Vars[stateVarKey]; stored {
		t.Errorf("Vars = %v, no debía guardarse estado", res.Vars)
	}
}
