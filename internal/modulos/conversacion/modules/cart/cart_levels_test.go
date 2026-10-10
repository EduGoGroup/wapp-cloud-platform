package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// cart_levels_test.go — los niveles L1 a L6 del recorrido, vistos por Module.Step:
// qué acepta cada uno y a dónde lleva. cart_levels.go no exporta nada.

const (
	bebidasScreen  = "Bebidas:\n1) Café · $2.50\n2) Té · $2.00\n0) ← Volver"
	postresScreen  = "Postres:\n1) Flan · $3.00\n0) ← Volver"
	coffeeCard     = "Café · $2.50\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver"
	coffeeQuantity = `¿Cuántos "Café"? Escribe la cantidad (0 ← volver)`
	invalidQty     = "Escribe una cantidad válida (un número mayor o igual a 1).\n\n"
	cancelledText  = `Pedido cancelado. Si quieres hacer otro, escribe "carrito" y lo empezamos de cero.`
)

// El recorrido feliz completo, pantalla a pantalla y estado a estado.
func TestLevels_HappyPath(t *testing.T) {
	m := cart.New()
	steps := []struct {
		in     string
		screen string
		state  cartState
	}{
		{"1", bebidasScreen, cartState{Level: cart.LevelArticles, CatCode: "1", Started: true}},
		{"1", coffeeCard, cartState{Level: cart.LevelArticle, CatCode: "1", SKU: "CAFE", Started: true}},
		{"2", coffeeQuantity, cartState{Level: cart.LevelQuantity, CatCode: "1", SKU: "CAFE", Started: true}},
		{"2", continueBebidas, cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "CAFE", Started: true,
			Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}}},
		{"1", bebidasScreen, cartState{Level: cart.LevelArticles, CatCode: "1", Started: true,
			Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}}},
		{"0", categoriesScreen, cartState{Level: cart.LevelCategories, Started: true,
			Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}}},
	}
	vars := seededVars()
	for i, s := range steps {
		st, outs, next := drive(t, m, vars, s.in)
		mustScreen(t, outs, s.screen)
		if !reflect.DeepEqual(st, s.state) {
			t.Fatalf("paso %d (%q): estado = %+v\nquiero %+v", i, s.in, st, s.state)
		}
		vars = next
	}

	// Otra categoría, otra línea, y el resumen con el total de las dos.
	vars = walk(t, m, vars, "2", "1", "2", "1") // Postres → Flan → agregar → ×1
	st, outs, vars := drive(t, m, vars, "2")
	if st.Level != cart.LevelSummary || len(st.Lines) != 2 {
		t.Fatalf("estado = %+v, quiero el resumen con dos líneas", st)
	}
	mustScreen(t, outs, "🧾 Resumen del pedido:\nCafé x2  $5.00\nFlan x1  $3.00\nTOTAL  $8.00\n"+
		"1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido")

	st, outs, _ = drive(t, m, vars, "1")
	if st.Level != cart.LevelClosed {
		t.Fatalf("estado = %+v, quiero closed", st)
	}
	mustScreen(t, outs, "✅ ¡Pedido confirmado! Total $8.00.")
}

// «0» en cada nivel que lo ofrece es un paso atrás, con las líneas intactas.
func TestLevels_Back(t *testing.T) {
	m := cart.New()
	cases := []struct {
		name   string
		path   []string
		screen string
		state  cartState
	}{
		{name: "articles to categories", path: []string{"1"}, screen: categoriesScreen,
			state: cartState{Level: cart.LevelCategories, Started: true}},
		{name: "article to articles", path: []string{"1", "2"}, screen: bebidasScreen,
			state: cartState{Level: cart.LevelArticles, CatCode: "1", Started: true}},
		{name: "quantity to article", path: []string{"1", "1", "2"}, screen: coffeeCard,
			state: cartState{Level: cart.LevelArticle, CatCode: "1", SKU: "CAFE", Started: true}},
		{name: "continue to the article in focus", path: []string{"1", "1", "2", "3"}, screen: coffeeCard,
			state: cartState{Level: cart.LevelArticle, CatCode: "1", SKU: "CAFE", Started: true,
				Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 3, UnitPrice: 2.5}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, outs, _ := drive(t, m, walk(t, m, seededVars(), tc.path...), "0")
			mustScreen(t, outs, tc.screen)
			if !reflect.DeepEqual(st, tc.state) {
				t.Errorf("estado = %+v\nquiero %+v", st, tc.state)
			}
		})
	}
}

// El resumen NO ofrece «volver»: «0» ahí es un inválido.
func TestLevels_SummaryHasNoBack(t *testing.T) {
	m := cart.New()
	st, outs, _ := drive(t, m, walk(t, m, seededVars(), "1", "1", "2", "1", "2"), "0")
	if st.Level != cart.LevelSummary {
		t.Fatalf("estado = %+v, quiero seguir en el resumen", st)
	}
	mustScreen(t, outs, invalidPrefix+summaryOneCoffee)
}

// Paginación de L1: «Más ▾» lleva el código siguiente al mayor de la lista, avanza de
// página, y una categoría de OTRA página se puede elegir igual.
func TestLevels_CategoriesPagination(t *testing.T) {
	m := cart.New(cart.WithPageSize(1))
	st, outs, vars := drive(t, m, seededVars(), "3")
	if st.Level != cart.LevelCategories || st.Page != 1 {
		t.Fatalf("estado = %+v, quiero categories página 1", st)
	}
	mustScreen(t, outs, "🛒 Elige una categoría:\n2) Postres") // la última página no ofrece «Más»

	// En la última página «3» ya no es «Más»: es un inválido, y la página no se mueve.
	st, outs, _ = drive(t, m, vars, "3")
	if st.Page != 1 {
		t.Fatalf("estado = %+v, la página no debía moverse", st)
	}
	mustScreen(t, outs, invalidPrefix+"🛒 Elige una categoría:\n2) Postres")

	// Bebidas es de la página 0 y se elige desde la 1; al entrar, la página vuelve a 0.
	st, outs, _ = drive(t, m, vars, "1")
	if st.Level != cart.LevelArticles || st.CatCode != "1" || st.Page != 0 {
		t.Fatalf("estado = %+v, quiero los artículos de Bebidas en la página 0", st)
	}
	mustScreen(t, outs, "Bebidas:\n1) Café · $2.50\n3) Más ▾\n0) ← Volver")
}

// Paginación de L2, con su «volver» siempre al final.
func TestLevels_ArticlesPagination(t *testing.T) {
	m := cart.New(cart.WithPageSize(1))
	vars := walk(t, m, seededVars(), "1")
	st, outs, vars := drive(t, m, vars, "3")
	if st.Level != cart.LevelArticles || st.Page != 1 {
		t.Fatalf("estado = %+v, quiero articles página 1", st)
	}
	mustScreen(t, outs, "Bebidas:\n2) Té · $2.00\n0) ← Volver")
	st, _, _ = drive(t, m, vars, "2")
	if st.Level != cart.LevelArticle || st.SKU != "TE" || st.Page != 0 {
		t.Fatalf("estado = %+v, quiero la ficha del Té en la página 0", st)
	}

	// Postres tiene un solo artículo: su «Más» (2) no existe y es un inválido.
	_, outs, _ = drive(t, m, walk(t, m, seededVars(), "3", "2"), "2")
	mustScreen(t, outs, invalidPrefix+postresScreen)
}

// Lo que el nivel no reconoce repromptea sin avanzar, en los seis niveles de opción.
func TestLevels_InvalidOptionReprompts(t *testing.T) {
	m := cart.New()
	cases := []struct {
		name   string
		path   []string
		in     string
		level  string
		screen string
	}{
		{name: "categories", path: nil, in: "99", level: cart.LevelCategories, screen: categoriesScreen},
		{name: "categories zero", path: nil, in: "0", level: cart.LevelCategories, screen: categoriesScreen},
		{name: "articles", path: []string{"1"}, in: "7", level: cart.LevelArticles, screen: bebidasScreen},
		{name: "article", path: []string{"1", "1"}, in: "3", level: cart.LevelArticle, screen: coffeeCard},
		{name: "continue", path: []string{"1", "1", "2", "1"}, in: "5", level: cart.LevelContinue, screen: continueBebidas},
		{name: "summary", path: []string{"1", "1", "2", "1", "2"}, in: "5", level: cart.LevelSummary, screen: summaryOneCoffee},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := walk(t, m, seededVars(), tc.path...)
			st, outs, _ := drive(t, m, before, tc.in)
			mustScreen(t, outs, invalidPrefix+tc.screen)
			want := stateOf(t, before)
			want.Level, want.Started = tc.level, true
			if !reflect.DeepEqual(st, want) {
				t.Errorf("estado = %+v\nquiero el de antes: %+v", st, want)
			}
		})
	}
}

// La ficha: «1» enseña la descripción y se queda; sin descripción lo dice.
func TestLevels_ArticleDescription(t *testing.T) {
	m := cart.New()
	st, outs, _ := drive(t, m, walk(t, m, seededVars(), "1", "1"), "1")
	if st.Level != cart.LevelArticle || st.SKU != "CAFE" {
		t.Fatalf("estado = %+v, quiero seguir en la ficha", st)
	}
	mustScreen(t, outs, "Café · $2.50\nEspresso doble\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver")

	vars := map[string]any{"cart_catalog": rawFromJSON(t,
		`{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"Alfajor","price":1}]}]}`)}
	_, outs, _ = drive(t, m, walk(t, m, vars, "1", "1"), "1")
	mustScreen(t, outs, "Alfajor · $1.00\n(sin descripción)\n1) Ver descripción\n2) Agregar al pedido\n0) ← Volver")
}

// La cantidad: solo un entero >= 1 agrega; lo demás repregunta el mismo paso sin
// tocar las líneas.
func TestLevels_Quantity(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2")
	for _, in := range []string{"abc", "-3", "2.5", "1,5", "+", "٣", "dos"} {
		st, outs, _ := drive(t, m, vars, in)
		if st.Level != cart.LevelQuantity || len(st.Lines) != 0 {
			t.Fatalf("%q: estado = %+v, quiero seguir en quantity sin líneas", in, st)
		}
		mustScreen(t, outs, invalidQty+coffeeQuantity)
	}
	valid := []struct {
		in   string
		want int
	}{{"1", 1}, {"12", 12}, {"007", 7}, {"+4", 4}, {" 3 ", 3}}
	for _, tc := range valid {
		st, _, _ := drive(t, m, vars, tc.in)
		if st.Level != cart.LevelContinue || len(st.Lines) != 1 || st.Lines[0].Qty != tc.want {
			t.Errorf("%q: estado = %+v, quiero una línea de %d", tc.in, st, tc.want)
		}
	}
}

// Dos veces el mismo artículo son DOS líneas, en el orden en que se agregaron.
func TestLevels_SameArticleTwiceIsTwoLines(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "2", "1", "1", "2")
	st, _, _ := drive(t, m, vars, "3")
	want := []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}, {SKU: "CAFE", Label: "Café", Qty: 3, UnitPrice: 2.5}}
	if !reflect.DeepEqual(st.Lines, want) {
		t.Errorf("líneas = %+v\nquiero %+v", st.Lines, want)
	}
}

// «Agregar más» desde L5 y «seguir agregando» desde L6 reofrecen la MISMA categoría.
func TestLevels_AddMoreKeepsTheSameCategory(t *testing.T) {
	m := cart.New()
	atContinue := walk(t, m, seededVars(), "2", "1", "2", "1") // Flan ×1
	want := cartState{Level: cart.LevelArticles, CatCode: "2", Started: true,
		Lines: []cartLine{{SKU: "FLAN", Label: "Flan", Qty: 1, UnitPrice: 3}}}

	st, outs, _ := drive(t, m, atContinue, "1")
	mustScreen(t, outs, postresScreen)
	if !reflect.DeepEqual(st, want) {
		t.Errorf("desde continue: estado = %+v\nquiero %+v", st, want)
	}
	st, outs, _ = drive(t, m, walk(t, m, atContinue, "2"), "2")
	mustScreen(t, outs, postresScreen)
	if !reflect.DeepEqual(st, want) {
		t.Errorf("desde summary: estado = %+v\nquiero %+v", st, want)
	}
}

// Cancelar con «9» desde L5 y desde L6 deja la sub-máquina en cancelled, con las
// líneas todavía en el estado.
func TestLevels_Cancel(t *testing.T) {
	m := cart.New()
	for _, path := range [][]string{{"1", "1", "2", "1"}, {"1", "1", "2", "1", "2"}} {
		st, outs, _ := drive(t, m, walk(t, m, seededVars(), path...), "9")
		if st.Level != cart.LevelCancelled || len(st.Lines) != 1 {
			t.Errorf("estado = %+v, quiero cancelled con su línea", st)
		}
		mustScreen(t, outs, cancelledText)
	}
}
