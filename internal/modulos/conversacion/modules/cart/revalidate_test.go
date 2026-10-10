package cart_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// revalidate_test.go — la mitad del carrito de la revalidación del rescate (Plan 041 ·
// T4.9): el catálogo aplanado a precios vigentes y el mensaje literal al cliente.

// La escena del plan: Marta pidió el 1 de enero 2 × Pan a $2.00 y 1 × Queso a $3.00
// (total $7.00). El catálogo de hoy tiene el Pan a $2.50 y ya no tiene Queso.
var januaryFirst = time.Date(2026, time.January, 1, 15, 4, 0, 0, time.UTC)

const summaryMenu = "\n1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido"

func bakery(items ...catalogo.Article) catalogo.Catalog {
	return catalogo.Catalog{Categories: []catalogo.Category{{Code: "1", Label: "Panadería", Items: items}}}
}

func martasCatalog() catalogo.Catalog {
	return bakery(catalogo.Article{Code: "1", SKU: "PAN", Label: "Pan", Price: 2.50})
}

func martasOrder() []intakes.Item {
	return []intakes.Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3.00},
	}
}

// Un artículo sin variantes publica su sku, su etiqueta y su precio; la lista sale
// RESUELTA, también con el catálogo vacío.
func TestPriceListOf_PlainArticle(t *testing.T) {
	pl := cart.PriceListOf(martasCatalog())
	if !pl.Resolved() {
		t.Fatal("Resolved() = false: la lista de un catálogo leído tiene que estar resuelta")
	}
	if e, ok := pl.Lookup("PAN"); !ok || e != (intakes.CatalogEntry{Label: "Pan", Price: 2.50}) {
		t.Errorf("PAN = %+v, %v; quiero {Pan 2.5}", e, ok)
	}
	if _, ok := pl.Lookup("QUESO"); ok {
		t.Error("QUESO resuelve y no está en el catálogo")
	}
	if !cart.PriceListOf(catalogo.Catalog{}).Resolved() {
		t.Error("la lista de un catálogo VACÍO pero leído también está resuelta")
	}
}

// Un artículo con variantes publica una entrada por variante —sku y etiqueta
// compuestos, precio de la variante— y NO su sku pelado. Un combo es una entrada.
func TestPriceListOf_VariantsButNotTheBareSKU(t *testing.T) {
	cat := catalogo.Catalog{Categories: []catalogo.Category{{
		Code: "1", Label: "Tortas",
		Items: []catalogo.Article{
			{Code: "1", SKU: "TORTA-CHOC", Label: "Torta de chocolate", Price: 10,
				Variants: []catalogo.Variant{
					{Code: "V1", Label: "15-20 porciones", Price: 20},
					{Code: "V2", Label: "25-30 porciones", Price: 30},
				}},
			{Code: "2", SKU: "COMBO-1", Label: "Combo", Price: 9,
				Components: []catalogo.Component{{SKU: "HAMB", Qty: 1}}},
		},
	}}}
	pl := cart.PriceListOf(cat)
	want := map[string]intakes.CatalogEntry{
		"TORTA-CHOC#V1": {Label: "Torta de chocolate — 15-20 porciones", Price: 20},
		"TORTA-CHOC#V2": {Label: "Torta de chocolate — 25-30 porciones", Price: 30},
		"COMBO-1":       {Label: "Combo", Price: 9},
	}
	for sku, entry := range want {
		if got, ok := pl.Lookup(sku); !ok || got != entry {
			t.Errorf("%s = %+v, %v; quiero %+v", sku, got, ok, entry)
		}
	}
	for _, sku := range []string{"TORTA-CHOC", "HAMB", "TORTA-CHOC#", "TORTA-CHOC#V3"} {
		if _, ok := pl.Lookup(sku); ok {
			t.Errorf("%s resuelve y no debía publicarse", sku)
		}
	}
}

// El sku que publica la lista es el MISMO que el carrito escribe en la línea: una
// línea con variante recién agregada resuelve contra el catálogo del que salió.
func TestPriceListOf_MatchesTheSKUTheCartWrites(t *testing.T) {
	m := cart.New()
	vars := v2Vars(t)
	parsed, err := catalogo.ParseCatalog(contentOf(vars))
	if err != nil {
		t.Fatalf("catálogo v2 de prueba ilegible: %v", err)
	}
	st, _, _ := drive(t, m, walk(t, m, vars, "01", "1", "2", "2"), "1") // torta, V2, ×1
	if len(st.Lines) != 1 {
		t.Fatalf("estado = %+v, quiero una línea", st)
	}
	entry, ok := cart.PriceListOf(parsed).Lookup(st.Lines[0].SKU)
	if !ok || entry.Label != st.Lines[0].Label || entry.Price != st.Lines[0].UnitPrice {
		t.Errorf("Lookup(%q) = %+v, %v; quiero la etiqueta y el precio de la línea %+v", st.Lines[0].SKU, entry, ok, st.Lines[0])
	}
}

// El sku repetido gana la primera vez que aparece, en el orden del catálogo.
func TestPriceListOf_RepeatedSKUFirstWins(t *testing.T) {
	cat := catalogo.Catalog{Categories: []catalogo.Category{
		{Code: "1", Label: "A", Items: []catalogo.Article{{SKU: "X", Label: "Primero", Price: 1}}},
		{Code: "2", Label: "B", Items: []catalogo.Article{
			{SKU: "X", Label: "Segundo", Price: 2},
			{SKU: "Y", Label: "Con variante", Variants: []catalogo.Variant{{Code: "V", Label: "v", Price: 5}, {Code: "V", Label: "repetida", Price: 6}}},
		}},
	}}
	pl := cart.PriceListOf(cat)
	if e, _ := pl.Lookup("X"); e != (intakes.CatalogEntry{Label: "Primero", Price: 1}) {
		t.Errorf("X = %+v, quiero el PRIMERO del recorrido", e)
	}
	if e, _ := pl.Lookup("Y#V"); e != (intakes.CatalogEntry{Label: "Con variante — v", Price: 5}) {
		t.Errorf("Y#V = %+v, quiero la primera variante", e)
	}
}

// La escena de Marta, entera y literal: es el texto que se persiste en
// intake_revisions.rendered_text.
func TestRevalidationMessage_MartasScene(t *testing.T) {
	rv := intakes.Revalidate(martasOrder(), cart.PriceListOf(martasCatalog()))
	want := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• Pan: $2.00 → $2.50" +
		"\n• Queso: ya no lo tenemos, lo quité del pedido" +
		"\n" +
		"\n🧾 Resumen del pedido:" +
		"\nPan x2  $5.00" +
		"\nTOTAL  $5.00   (antes $7.00)" + summaryMenu
	if got := cart.RevalidationMessage(rv, januaryFirst, ""); got != want {
		t.Errorf("mensaje distinto byte a byte.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, want)
	}
}

// Sin cambios no hay mensaje: la cadena vacía es el «no hay nada que mandar».
func TestRevalidationMessage_NoChangesNoMessage(t *testing.T) {
	same := bakery(catalogo.Article{SKU: "PAN", Label: "Pan", Price: 2.00}, catalogo.Article{SKU: "QUESO", Label: "Queso", Price: 3.00})
	rv := intakes.Revalidate(martasOrder(), cart.PriceListOf(same))
	if got := cart.RevalidationMessage(rv, januaryFirst, "dejarlo en portería"); got != "" {
		t.Errorf("mensaje = %q, quiero la cadena vacía", got)
	}
	if got := cart.RevalidationMessage(intakes.Revalidation{}, januaryFirst, ""); got != "" {
		t.Errorf("mensaje del valor cero = %q, quiero la cadena vacía", got)
	}
}

// Una bajada también se avisa, con la misma viñeta y en el mismo sentido.
func TestRevalidationMessage_PriceGoingDown(t *testing.T) {
	cheaper := bakery(catalogo.Article{SKU: "PAN", Label: "Pan", Price: 1.50}, catalogo.Article{SKU: "QUESO", Label: "Queso", Price: 3.00})
	rv := intakes.Revalidate(martasOrder(), cart.PriceListOf(cheaper))
	want := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• Pan: $2.00 → $1.50" +
		"\n" +
		"\n🧾 Resumen del pedido:" +
		"\nPan x2  $3.00" +
		"\nQueso x1  $3.00" +
		"\nTOTAL  $6.00   (antes $7.00)" + summaryMenu
	if got := cart.RevalidationMessage(rv, januaryFirst, ""); got != want {
		t.Errorf("mensaje distinto byte a byte.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, want)
	}
}

// Pasados los cinco cambios se listan cinco y se cierra con «…y N más»; y con el
// pedido vacío no se pinta un resumen que ofrecería confirmar la nada.
func TestRevalidationMessage_BulletCapAndEmptyOrder(t *testing.T) {
	skus := []string{"A", "B", "C", "D", "E", "F", "G"}
	items := make([]intakes.Item, 0, len(skus))
	for _, sku := range skus {
		items = append(items, intakes.Item{SKU: sku, Label: sku, Qty: 1, UnitPrice: 1})
	}
	want := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• A: ya no lo tenemos, lo quité del pedido" +
		"\n• B: ya no lo tenemos, lo quité del pedido" +
		"\n• C: ya no lo tenemos, lo quité del pedido" +
		"\n• D: ya no lo tenemos, lo quité del pedido" +
		"\n• E: ya no lo tenemos, lo quité del pedido" +
		"\n…y 2 más" +
		"\n" +
		"\n🧾 Ya no queda nada de lo que tenías en el pedido (antes $7.00)."
	rv := intakes.Revalidate(items, cart.PriceListOf(catalogo.Catalog{}))
	if got := cart.RevalidationMessage(rv, januaryFirst, "una nota"); got != want {
		t.Errorf("mensaje distinto byte a byte.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, want)
	}

	// Justo cinco cambios: las cinco viñetas y ningún «…y N más».
	rv = intakes.Revalidate(items[:5], cart.PriceListOf(catalogo.Catalog{}))
	got := cart.RevalidationMessage(rv, januaryFirst, "")
	if strings.Count(got, "\n• ") != 5 || strings.Contains(got, "…y ") {
		t.Errorf("con cinco cambios quiero cinco viñetas y ningún resto:\n%s", got)
	}
}

// La indicación del pedido sigue bajo el total y las de línea bajo su línea; la línea
// de envío se pinta como una más y el TOTAL cuadra con sus renglones.
func TestRevalidationMessage_NotesAndShippingLine(t *testing.T) {
	items := []intakes.Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00, Customization: "bien cocido"},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3.00},
		{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 3},
	}
	rv := intakes.Revalidate(items, cart.PriceListOf(martasCatalog()))
	want := "\n🧾 Resumen del pedido:" +
		"\nPan x2  $5.00" +
		"\n   ✏️ bien cocido" +
		"\nEnvío — Providencia x1  $3.00" +
		"\nTOTAL  $8.00   (antes $10.00)" +
		"\n✏️ Para todo el pedido: dejarlo en portería" + summaryMenu
	got := cart.RevalidationMessage(rv, januaryFirst, "dejarlo en portería")
	if !strings.HasSuffix(got, want) {
		t.Errorf("el cuerpo no es el resumen de siempre con el total anterior.\n--- obtenido ---\n%s\n--- quiero que acabe en ---\n%s", got, want)
	}
}

// La fecha se dice como en voz alta, sin año, sin cero a la izquierda y SIN convertir
// de zona: se formatea tal cual llega.
func TestRevalidationMessage_DateIsSpokenAndNotConverted(t *testing.T) {
	rv := intakes.Revalidate(martasOrder(), cart.PriceListOf(martasCatalog()))
	santiago := time.FixedZone("CLT", -4*3600)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), "Recuperé tu pedido del 1 de enero 🧾\n"},
		{time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC), "Recuperé tu pedido del 31 de diciembre 🧾\n"},
		{time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC), "Recuperé tu pedido del 9 de septiembre 🧾\n"},
		// Las 22:00 del 14 en Santiago son ya el 15 en UTC: manda el huso que trae el valor.
		{time.Date(2026, time.March, 14, 22, 0, 0, 0, santiago), "Recuperé tu pedido del 14 de marzo 🧾\n"},
		{time.Date(2026, time.March, 14, 22, 0, 0, 0, santiago).UTC(), "Recuperé tu pedido del 15 de marzo 🧾\n"},
	}
	for _, tc := range cases {
		if got := cart.RevalidationMessage(rv, tc.at, ""); !strings.HasPrefix(got, tc.want) {
			t.Errorf("fecha %s: el mensaje empieza por %q, quiero %q", tc.at, strings.SplitN(got, "\n", 2)[0], tc.want)
		}
	}
}
