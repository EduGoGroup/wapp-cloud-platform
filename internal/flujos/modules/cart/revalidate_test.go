package cart

import (
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// revalidate_test.go cubre la mitad del carrito de la revalidación del rescate
// (Plan 041 · T4.9, D-041.25): el catálogo aplanado a precios vigentes y el MENSAJE
// literal que recibe el cliente.
//
// Los textos se comparan BYTE A BYTE y no por "contiene". Es lo que exige REQ-35b:
// el mismo string que se manda es el que se guarda en `rendered_text` para poder
// citarlo el día que el cliente diga «a mí me dijeron $2.00», y un test que solo
// mirara si aparece "$2.50" pasaría con el mensaje partido, con el total mal o con
// el menú de teclas roto.

// primeroDeEnero es la fecha del pedido de Marta (D-041.25 §c).
var primeroDeEnero = time.Date(2026, time.January, 1, 15, 4, 0, 0, time.UTC)

// catálogoDeMarta es el catálogo VIGENTE del 15 de enero: el pan subió a $2.50 y el
// queso ya no está.
func catálogoDeMarta() Catalog {
	return Catalog{Categories: []Category{{
		Code: "1", Label: "Panadería",
		Items: []Article{{Code: "1", SKU: "PAN", Label: "Pan", Price: 2.50}},
	}}}
}

// pedidoDeMarta son las líneas tal como quedaron el 1 de enero: 2 × Pan a $2.00 y
// 1 × Queso a $3.00, total $7.00.
func pedidoDeMarta() []intakes.Item {
	return []intakes.Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3.00},
	}
}

// TestPriceListOf_ArtículoSimple: un catálogo v1 se aplana a lo obvio.
func TestPriceListOf_ArtículoSimple(t *testing.T) {
	pl := PriceListOf(catálogoDeMarta())
	if !pl.Resolved() {
		t.Fatal("la lista de un catálogo leído tiene que estar RESUELTA")
	}
	e, ok := pl.Lookup("PAN")
	if !ok || e.Label != "Pan" || e.Price != 2.50 {
		t.Fatalf("PAN=%+v ok=%v; quiero {Pan 2.5}", e, ok)
	}
	if _, ok := pl.Lookup("QUESO"); ok {
		t.Fatal("QUESO ya no está en el catálogo: no puede resolver")
	}
}

// TestPriceListOf_VariantesYNoElSkuPelado: un artículo con variantes publica UNA
// entrada por variante, con el sku y la etiqueta compuestos que escribe newLine, y
// NO publica su sku pelado — cuyo precio es solo una referencia (D-041.2).
func TestPriceListOf_VariantesYNoElSkuPelado(t *testing.T) {
	cat := Catalog{Categories: []Category{{
		Code: "1", Label: "Tortas",
		Items: []Article{{
			Code: "1", SKU: "TORTA-CHOC", Label: "Torta de chocolate", Price: 10,
			Variants: []Variant{
				{Code: "V1", Label: "15-20 porciones", Price: 20},
				{Code: "V2", Label: "25-30 porciones", Price: 30},
			},
		}},
	}}}
	pl := PriceListOf(cat)

	e, ok := pl.Lookup("TORTA-CHOC#V2")
	if !ok || e.Price != 30 || e.Label != "Torta de chocolate — 25-30 porciones" {
		t.Fatalf("variante V2=%+v ok=%v; quiero la etiqueta compuesta a 30", e, ok)
	}
	// La línea del pedido la construye newLine; el catálogo aplanado tiene que
	// resolver EXACTAMENTE el sku que aquélla escribe, o el rescate retiraría
	// líneas perfectamente vivas.
	línea := newLine(cat.Categories[0].Items[0], cat.Categories[0].Items[0].Variants[1], true, 1)
	if _, ok := pl.Lookup(línea.SKU); !ok {
		t.Fatalf("el sku que escribe newLine (%q) no resuelve en la lista de precios", línea.SKU)
	}
	if _, ok := pl.Lookup("TORTA-CHOC"); ok {
		t.Fatal("el sku pelado de un artículo con variantes NO se publica: su precio es una referencia")
	}
}

// TestPriceListOf_SkuRepetidoGanaElPrimero fija el criterio determinista.
func TestPriceListOf_SkuRepetidoGanaElPrimero(t *testing.T) {
	cat := Catalog{Categories: []Category{
		{Code: "1", Label: "A", Items: []Article{{SKU: "X", Label: "Primero", Price: 1}}},
		{Code: "2", Label: "B", Items: []Article{{SKU: "X", Label: "Segundo", Price: 2}}},
	}}
	if e, _ := PriceListOf(cat).Lookup("X"); e.Label != "Primero" || e.Price != 1 {
		t.Fatalf("sku repetido=%+v; quiero el PRIMERO del recorrido", e)
	}
}

// TestRevalidationMessage_EscenaDeMarta es el criterio (a) del plan, entero y
// literal: 2 × Pan $2.00 + 1 × Queso $3.00 (total $7.00) contra el catálogo del 15
// de enero ⇒ las dos viñetas, UNA línea viva y el total $5.00 con «(antes $7.00)».
func TestRevalidationMessage_EscenaDeMarta(t *testing.T) {
	rv := intakes.Revalidate(pedidoDeMarta(), PriceListOf(catálogoDeMarta()))

	quiero := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• Pan: $2.00 → $2.50" +
		"\n• Queso: ya no lo tenemos, lo quité del pedido" +
		"\n" +
		"\n🧾 Resumen del pedido:" +
		"\nPan x2  $5.00" +
		"\nTOTAL  $5.00   (antes $7.00)" +
		"\n1) Confirmar y finalizar" +
		"\n2) Seguir agregando" +
		"\n3) ✏️ Indicación para todo el pedido" +
		"\n9) Cancelar pedido"

	if got := RevalidationMessage(rv, primeroDeEnero, ""); got != quiero {
		t.Fatalf("mensaje distinto BYTE A BYTE.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, quiero)
	}
}

// TestRevalidationMessage_SinCambiosNoHayMensaje es el criterio (b) por el lado del
// texto: el pedido intacto no produce ni una palabra. La cadena vacía es lo que
// después impide escribir la revisión (ApplyRevalidation la rechaza).
func TestRevalidationMessage_SinCambiosNoHayMensaje(t *testing.T) {
	cat := Catalog{Categories: []Category{{Code: "1", Label: "Panadería", Items: []Article{
		{SKU: "PAN", Label: "Pan", Price: 2.00},
		{SKU: "QUESO", Label: "Queso", Price: 3.00},
	}}}}
	rv := intakes.Revalidate(pedidoDeMarta(), PriceListOf(cat))

	if got := RevalidationMessage(rv, primeroDeEnero, ""); got != "" {
		t.Fatalf("sin cambios no se manda nada; obtenido:\n%s", got)
	}
}

// TestRevalidationMessage_PrecioQueBaja es el criterio (c): una bajada también se
// avisa, con la misma viñeta y en el mismo sentido (viejo → nuevo).
func TestRevalidationMessage_PrecioQueBaja(t *testing.T) {
	cat := Catalog{Categories: []Category{{Code: "1", Label: "Panadería", Items: []Article{
		{SKU: "PAN", Label: "Pan", Price: 1.50},
		{SKU: "QUESO", Label: "Queso", Price: 3.00},
	}}}}
	rv := intakes.Revalidate(pedidoDeMarta(), PriceListOf(cat))

	quiero := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• Pan: $2.00 → $1.50" +
		"\n" +
		"\n🧾 Resumen del pedido:" +
		"\nPan x2  $3.00" +
		"\nQueso x1  $3.00" +
		"\nTOTAL  $6.00   (antes $7.00)" +
		"\n1) Confirmar y finalizar" +
		"\n2) Seguir agregando" +
		"\n3) ✏️ Indicación para todo el pedido" +
		"\n9) Cancelar pedido"

	if got := RevalidationMessage(rv, primeroDeEnero, ""); got != quiero {
		t.Fatalf("mensaje distinto BYTE A BYTE.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, quiero)
	}
}

// TestRevalidationMessage_TopeDeViñetas: pasados los 5 cambios se listan 5 y se
// cierra con «…y N más». Por WhatsApp no se pagina.
func TestRevalidationMessage_TopeDeViñetas(t *testing.T) {
	skus := []string{"A", "B", "C", "D", "E", "F", "G"}
	items := make([]intakes.Item, 0, len(skus))
	for _, sku := range skus {
		items = append(items, intakes.Item{SKU: sku, Label: sku, Qty: 1, UnitPrice: 1})
	}
	rv := intakes.Revalidate(items, PriceListOf(Catalog{}))

	quiero := "Recuperé tu pedido del 1 de enero 🧾" +
		"\nOjo, cambiaron cosas desde entonces:" +
		"\n• A: ya no lo tenemos, lo quité del pedido" +
		"\n• B: ya no lo tenemos, lo quité del pedido" +
		"\n• C: ya no lo tenemos, lo quité del pedido" +
		"\n• D: ya no lo tenemos, lo quité del pedido" +
		"\n• E: ya no lo tenemos, lo quité del pedido" +
		"\n…y 2 más" +
		"\n" +
		"\n🧾 Ya no queda nada de lo que tenías en el pedido (antes $7.00)."

	if got := RevalidationMessage(rv, primeroDeEnero, ""); got != quiero {
		t.Fatalf("mensaje distinto BYTE A BYTE.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, quiero)
	}
}

// TestRevalidationMessage_ConIndicaciónDelPedido: la nota del cliente sigue donde
// siempre, bajo el total. El resumen es el de siempre.
func TestRevalidationMessage_ConIndicaciónDelPedido(t *testing.T) {
	rv := intakes.Revalidate(pedidoDeMarta(), PriceListOf(catálogoDeMarta()))

	got := RevalidationMessage(rv, primeroDeEnero, "dejarlo en portería")
	quiero := "\nTOTAL  $5.00   (antes $7.00)" +
		"\n✏️ Para todo el pedido: dejarlo en portería" +
		"\n1) Confirmar y finalizar"
	if !contiene(got, quiero) {
		t.Fatalf("la indicación del pedido no está bajo el total.\n--- obtenido ---\n%s", got)
	}
}

// TestRevalidationMessage_LíneaDeEnvío: si la solicitud trae la línea de la
// plataforma, se pinta como una más y el TOTAL cuadra con los renglones. No se
// re-precia ni se retira (eso lo prueba el dominio); aquí se comprueba que no
// desaparece de la vista mientras se cobra.
func TestRevalidationMessage_LíneaDeEnvío(t *testing.T) {
	items := append(pedidoDeMarta(), intakes.Item{
		SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 3,
	})
	rv := intakes.Revalidate(items, PriceListOf(catálogoDeMarta()))

	quiero := "\n🧾 Resumen del pedido:" +
		"\nPan x2  $5.00" +
		"\nEnvío — Providencia x1  $3.00" +
		"\nTOTAL  $8.00   (antes $10.00)"
	if got := RevalidationMessage(rv, primeroDeEnero, ""); !contiene(got, quiero) {
		t.Fatalf("el envío no se pinta o el total no cuadra.\n--- obtenido ---\n%s", got)
	}
}

// TestSummaryWith_SinSufijoEsElResumenDeSiempre es la no-regresión del cambio en
// screens.go: el resumen que ve cualquier cliente que NO viene de un rescate tiene
// que salir idéntico al de antes de esta tarea.
func TestSummaryWith_SinSufijoEsElResumenDeSiempre(t *testing.T) {
	líneas := []cartLine{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2}}
	if summaryWith(líneas, "", "") != screenSummary(líneas, "") {
		t.Fatal("summaryWith con sufijo vacío tiene que ser screenSummary, byte a byte")
	}
	quiero := "🧾 Resumen del pedido:" +
		"\nPan x2  $4.00" +
		"\nTOTAL  $4.00" +
		"\n1) Confirmar y finalizar" +
		"\n2) Seguir agregando" +
		"\n3) ✏️ Indicación para todo el pedido" +
		"\n9) Cancelar pedido"
	if got := screenSummary(líneas, ""); got != quiero {
		t.Fatalf("el resumen de siempre cambió.\n--- obtenido ---\n%s\n--- quiero ---\n%s", got, quiero)
	}
}

// TestLongDate cubre los bordes de la fecha: primer y último mes.
func TestLongDate(t *testing.T) {
	if got := longDate(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)); got != "1 de enero" {
		t.Fatalf("longDate=%q, quiero \"1 de enero\"", got)
	}
	if got := longDate(time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)); got != "31 de diciembre" {
		t.Fatalf("longDate=%q, quiero \"31 de diciembre\"", got)
	}
}

// contiene busca un BLOQUE literal dentro del mensaje. Se usa solo donde lo que se
// prueba es una parte concreta (dónde cae la nota, cómo se pinta el envío); el
// mensaje entero se compara siempre byte a byte.
func contiene(s, sub string) bool { return strings.Contains(s, sub) }
