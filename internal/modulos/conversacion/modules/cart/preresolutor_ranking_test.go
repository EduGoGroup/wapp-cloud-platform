package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// preresolutor_ranking_test.go — el ORDEN entre coincidencias de la cascada («primero por
// tokens consumidos, luego por confianza») en los tres casos que el corpus no pisaba, y la
// salida del troceado ante un catálogo sin artículos. Los encontró sin ejecutar la medición
// combinada de F9-05 (D-42). Como en preresolutor_test.go, todo se ve por Module.Step.

// lookalikeCatalog son dos categorías que se llevan una sola letra, lo bastante largas (ocho
// runas) para que el fuzzy perdone la diferencia: escribir una casa EXACTO con ella y por
// FUZZY con la otra, las dos consumiendo un token. `exactFirst` decide cuál va antes.
func lookalikeCatalog(exactFirst bool) map[string]any {
	category := func(code, label, sku string) map[string]any {
		return map[string]any{"code": code, "label": label, "items": []any{
			map[string]any{"code": "1", "sku": sku, "label": "Unidad", "price": 3.0},
		}}
	}
	exact, fuzzy := category("A", "Tostadas", "TOSTA"), category("O", "Tostados", "TOSTO")
	if exactFirst {
		return map[string]any{"categories": []any{exact, fuzzy}}
	}
	return map[string]any{"categories": []any{fuzzy, exact}}
}

// A igualdad de tokens consumidos manda la CONFIANZA, venga la mejor antes o después en la
// lista: «tostadas» es la categoría Tostadas, no un empate con Tostados. Sin ese segundo
// criterio las dos coincidencias empatarían y la regla de oro dejaría la entrada sin resolver.
func TestCascade_SameTokensHigherConfidenceWins(t *testing.T) {
	for name, exactFirst := range map[string]bool{"exact option listed first": true, "exact option listed last": false} {
		t.Run(name, func(t *testing.T) {
			s := &spy{}
			vars := map[string]any{modules.VarContentRaw: lookalikeCatalog(exactFirst)}
			st, _, _ := drive(t, cart.New(cart.WithMatchHook(s.record)), vars, "tostadas")
			if st.Level != cart.LevelArticles || st.CatCode != "A" {
				t.Errorf("el carrito acabó en %s/%s, quiero articles/A (la coincidencia exacta)", st.Level, st.CatCode)
			}
			if want := [][2]string{{"exact", cart.LevelCategories}}; !reflect.DeepEqual(s.seen, want) {
				t.Errorf("observador = %v, quiero %v", s.seen, want)
			}
		})
	}
}

// Y la confianza no tapa a los tokens: el fuzzy también gana cuando es el único que casa. Es
// el testigo de que el catálogo de arriba de verdad pone a competir un exacto y un fuzzy.
func TestCascade_LookalikeAloneResolvesByFuzzy(t *testing.T) {
	s := &spy{}
	vars := map[string]any{modules.VarContentRaw: lookalikeCatalog(true)}
	st, _, _ := drive(t, cart.New(cart.WithMatchHook(s.record)), vars, "tostadoz")
	if st.Level != cart.LevelArticles || st.CatCode != "O" {
		t.Errorf("el carrito acabó en %s/%s, quiero articles/O", st.Level, st.CatCode)
	}
	if want := [][2]string{{"fuzzy", cart.LevelCategories}}; !reflect.DeepEqual(s.seen, want) {
		t.Errorf("observador = %v, quiero %v", s.seen, want)
	}
}

// Más tokens ganan a menos TAMBIÉN cuando la opción larga va primero en la lista: «torta de
// chocolate» casa entera con la primera torta y solo por su prefijo («torta de») con la
// segunda, que llega después y no puede quitarle el sitio. El corpus solo tenía el orden
// contrario (la de vainilla, que es la segunda).
func TestCascade_LaterShorterMatchDoesNotDisplaceTheLongerOne(t *testing.T) {
	st, seen := cascadeTurn(t, cascadeState(cart.LevelArticles), "torta de chocolate")
	if st.Level != cart.LevelArticle || st.CatCode != "2" || st.SKU != "TCHOC" {
		t.Errorf("el carrito acabó en %s/%s/%s, quiero article/2/TCHOC", st.Level, st.CatCode, st.SKU)
	}
	if want := [][2]string{{"exact", cart.LevelArticles}}; !reflect.DeepEqual(seen, want) {
		t.Errorf("observador = %v, quiero %v", seen, want)
	}
}

// Un catálogo sin un solo artículo no tiene contra qué casar: el troceado se aparta ANTES de
// interpretar los trozos. Si no lo hiciera, «2 pizzas y 3 tortas» —dos trozos con cantidad
// explícita— subiría al modelo como una consulta por trozos con la lista de opciones vacía.
func TestChunking_CatalogWithoutArticlesStepsAside(t *testing.T) {
	empty := map[string]any{"categories": []any{
		map[string]any{"code": "1", "label": "Pizzas", "items": []any{}},
		map[string]any{"code": "2", "label": "Tortas", "items": []any{}},
	}}
	vars := map[string]any{modules.VarContentRaw: empty}
	const in = "2 pizzas y 3 tortas"
	// 1.ª pasada: la única consulta legítima aquí es la de OPCIÓN del pre-resolutor («pizzas» y
	// «tortas» empatan entre categorías), que no lleva trozos.
	first := cart.New().Step(model.Node{}, model.Conversation{Vars: vars}, in)
	if first.Query != nil && (len(first.Query.Chunks) != 0 || len(first.Query.Options) == 0) {
		t.Fatalf("Query = %+v: se preguntó por trozos sin candidatos que ofrecer", *first.Query)
	}
	res := turn(cart.New(), model.Conversation{Vars: vars}, in, nil)
	st := stateOf(t, res.Vars)
	if st.Level != cart.LevelCategories || len(st.Lines) != 0 {
		t.Errorf("estado = %+v, quiero seguir en categorías sin líneas", st)
	}
	mustContain(t, res.Outputs, invalidPrefix+"🛒 Elige una categoría:")
}
