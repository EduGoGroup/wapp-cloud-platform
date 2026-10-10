//go:build pendiente

package cart_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// screens_test.go — lo que screens.go promete y los demás tests no fijan: el formato
// de los importes, el código de «Más ▾» y los bordes de la paginación. Las pantallas
// de cada nivel las sujetan, literales, los tests de su nivel y los goldens.

// oddCatalog tiene códigos no numéricos, precios raros y un artículo cuyas variantes
// no vienen ordenadas por precio.
const oddCatalog = `{"categories":[
  {"code":"A","label":"Alfa","items":[
    {"code":"10","sku":"CARO","label":"Caro","price":18000},
    {"code":"x","sku":"GRATIS","label":"Gratis","price":0},
    {"code":"2","sku":"RARO","label":"Raro","price":1.999},
    {"code":"3","sku":"VAR","label":"Con variantes","price":99,
      "variants":[{"code":"G","label":"Grande","price":30},{"code":"C","label":"Chico","price":20},{"code":"M","label":"Mediano","price":25}]}
  ]},
  {"code":"B","label":"Beta","items":[{"code":"1","sku":"B1","label":"Uno","price":1}]}
]}`

func oddVars(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{modules.VarContentRaw: rawFromJSON(t, oddCatalog)}
}

// Importes: `$` y dos decimales, sin separador de miles; «desde» el precio de la
// variante MÁS BARATA, no el de la primera ni el de referencia del artículo.
func TestScreens_MoneyAndFromPrice(t *testing.T) {
	_, outs, _ := drive(t, cart.New(), oddVars(t), "A")
	mustScreen(t, outs, "Alfa:\n10) Caro · $18000.00\nx) Gratis · $0.00\n2) Raro · $2.00\n"+
		"3) Con variantes · desde $20.00\n0) ← Volver")
}

// El total de una línea es qty × precio y el del pedido la suma de sus líneas.
func TestScreens_TotalsAreQtyTimesUnitPrice(t *testing.T) {
	vars := seededVars()
	seedState(t, vars, cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "CAFE", Started: true, Lines: []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 3, UnitPrice: 2.5, Customization: "sin azúcar"},
		{SKU: "TE", Label: "Té", Qty: 12, UnitPrice: 0.1},
		{SKU: "X", Label: "Regalo", Qty: 1, UnitPrice: 0},
	}})
	_, outs, _ := drive(t, cart.New(), vars, "2")
	mustScreen(t, outs, "🧾 Resumen del pedido:\nCafé x3  $7.50\n   ✏️ sin azúcar\nTé x12  $1.20\nRegalo x1  $0.00\nTOTAL  $8.70\n"+
		"1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido")
}

// «Más ▾» lleva el mayor código NUMÉRICO de toda la lista más uno; los no numéricos no
// cuentan, y sin ninguno numérico es «1».
func TestScreens_MoreCode(t *testing.T) {
	m := cart.New(cart.WithPageSize(1))
	// Categorías «A» y «B»: ningún código numérico ⇒ «1) Más ▾».
	mustScreen(t, m.Render(model.Node{}, contentOf(oddVars(t))), "🛒 Elige una categoría:\nA) Alfa\n1) Más ▾")
	st, outs, vars := drive(t, m, oddVars(t), "1")
	if st.Page != 1 {
		t.Fatalf("estado = %+v, quiero la página 1", st)
	}
	mustScreen(t, outs, "🛒 Elige una categoría:\nB) Beta")

	// Artículos «10», «x», «2», «3»: el mayor numérico es 10 ⇒ «11) Más ▾», aunque el
	// artículo «10» ni esté en la página.
	st, outs, vars = drive(t, m, vars, "A")
	mustScreen(t, outs, "Alfa:\n10) Caro · $18000.00\n11) Más ▾\n0) ← Volver")
	st, outs, _ = drive(t, m, walk(t, m, vars, "11"), "11")
	if st.Level != cart.LevelArticles || st.Page != 2 {
		t.Fatalf("estado = %+v, quiero articles página 2", st)
	}
	mustScreen(t, outs, "Alfa:\n2) Raro · $2.00\n11) Más ▾\n0) ← Volver")
}

// Bordes de la paginación: una página más allá del final enseña la lista vacía, sin
// «Más»; una negativa vale 0.
func TestScreens_PageBounds(t *testing.T) {
	cases := []struct {
		page int
		want string
	}{
		{page: 99, want: "🛒 Elige una categoría:"},
		{page: 2, want: "🛒 Elige una categoría:"},
		{page: -3, want: "🛒 Elige una categoría:\n1) Bebidas\n3) Más ▾"},
	}
	for _, tc := range cases {
		vars := seededVars()
		vars[cart.VarPageSize] = 1
		seedState(t, vars, cartState{Level: cart.LevelCategories, Page: tc.page, Started: true})
		_, outs, _ := drive(t, cart.New(), vars, "zzz")
		mustScreen(t, outs, invalidPrefix+tc.want)
	}
}

// La cantidad nombra lo que se pide: el artículo a secas o con su variante.
func TestScreens_QuantityNamesWhatIsBeingOrdered(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, oddVars(t), "A", "3", "2") // Con variantes → agregar
	_, outs, vars := drive(t, m, vars, "3")       // la tercera: Mediano
	mustScreen(t, outs, `¿Cuántos "Con variantes — Mediano"? Escribe la cantidad (0 ← volver)`)
	_, outs, _ = drive(t, m, vars, "x")
	mustScreen(t, outs, invalidQty+`¿Cuántos "Con variantes — Mediano"? Escribe la cantidad (0 ← volver)`)
}
