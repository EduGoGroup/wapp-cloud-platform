package quotetext_test

// render_test.go — EL FORMATO DEL RENDER DETERMINISTA, comprobado POR ESTRUCTURA.
//
// No hay ni un assert de igualdad literal contra el texto completo, y es deliberado:
// un test así se convierte en una copia del código y cambia cada vez que alguien
// mueve una coma. Lo que se afirma es lo que el formato PROMETE (design §1 punto 8):
// producto + tamaño + specs + precio + qué incluye, más el total.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
)

func TestRender_TraeCadaLineaConSuPrecioYElTotal(t *testing.T) {
	texto := quotetext.Render(borradorFusion())

	for _, l := range lineasFusion {
		if !strings.Contains(texto, l.Label) {
			t.Errorf("falta la etiqueta %q (producto + tamaño + specs):\n%s", l.Label, texto)
		}
		if !strings.Contains(texto, quotetext.Importe(l.UnitPrice)) {
			t.Errorf("falta el precio de %q:\n%s", l.Label, texto)
		}
	}
	if !strings.Contains(texto, quotetext.Importe(totalFusion)) {
		t.Errorf("falta el total:\n%s", texto)
	}
	// Una línea por ítem más saludo, total y cierre: el mensaje no se convierte en una
	// carta. El número exacto no se asserta —eso sería copiar el código— pero sí que no
	// se dispare.
	if n := strings.Count(texto, "\n"); n > 3*len(lineasFusion)+6 {
		t.Errorf("el render se fue de largo (%d saltos de línea):\n%s", n, texto)
	}
}

func TestRender_QuéIncluye(t *testing.T) {
	texto := quotetext.Render(quotetext.BorradorDe([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate", Customization: "sin lactosa", Qty: 1, UnitPrice: 2100},
	}))
	if !strings.Contains(texto, "sin lactosa") {
		t.Fatalf("la personalización no aparece en el texto:\n%s", texto)
	}
	// 🔴 INV-13: la personalización NO cobra. El total es el precio pelado.
	if !strings.Contains(texto, quotetext.Importe(2100)) {
		t.Errorf("el total cambió con la personalización:\n%s", texto)
	}
}

func TestRender_CantidadMayorQueUno_TraeUnitarioYTotalDeLinea(t *testing.T) {
	texto := quotetext.Render(quotetext.BorradorDe([]intakes.Item{
		{SKU: "TEQ", Label: "Tequeños bandeja x30", Qty: 4, UnitPrice: 490},
	}))
	if !strings.Contains(texto, quotetext.Importe(490)) {
		t.Errorf("falta el precio unitario:\n%s", texto)
	}
	if !strings.Contains(texto, quotetext.Importe(1960)) {
		t.Errorf("falta el total de la línea (4 × 490):\n%s", texto)
	}
	if !strings.Contains(texto, "4 ×") {
		t.Errorf("falta la cantidad:\n%s", texto)
	}
}

// TestRender_CantidadUno_NoEscribeLaCantidad: «1 ×» es ruido, y además metería en el
// texto un número desnudo que el verificador tendría que perdonar sin necesidad.
func TestRender_CantidadUno_NoEscribeLaCantidad(t *testing.T) {
	texto := quotetext.Render(quotetext.BorradorDe([]intakes.Item{
		{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100},
	}))
	if strings.Contains(texto, "1 ×") {
		t.Errorf("no debe escribirse la cantidad cuando es uno:\n%s", texto)
	}
}

// TestRender_LineaSinPrecio_NoEscribeCero: «$0» le prometería al cliente un envío
// gratis que nadie ha decidido.
func TestRender_LineaSinPrecio_NoEscribeCero(t *testing.T) {
	texto := quotetext.Render(quotetext.BorradorDe([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	}))
	if strings.Contains(texto, quotetext.Importe(0)) {
		t.Errorf("una línea sin precio no puede escribirse como $0:\n%s", texto)
	}
	if !strings.Contains(texto, "confirmar") {
		t.Errorf("una línea sin precio tiene que decirlo con palabras:\n%s", texto)
	}
}

// TestRender_EsDeterminista: la misma entrada, byte a byte la misma salida.
func TestRender_EsDeterminista(t *testing.T) {
	b := borradorFusion()
	primero := quotetext.Render(b)
	for i := 0; i < 20; i++ {
		if otro := quotetext.Render(b); otro != primero {
			t.Fatalf("dos renders del mismo borrador difieren (vuelta %d)", i)
		}
	}
}

// TestBorradorDe_TotalEsLaSumaDeLasLineas: el total del borrador es el que el cliente
// puede comprobar a mano, no el de la cabecera.
func TestBorradorDe_TotalEsLaSumaDeLasLineas(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{
		{SKU: "A", Label: "Torta", Qty: 3, UnitPrice: 100},
		{SKU: "B", Label: "Café", Qty: 2, UnitPrice: 50},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	if b.Total != 400 {
		t.Fatalf("total = %v; se esperaba 400 (3×100 + 2×50 + envío sin precio)", b.Total)
	}
	if !b.Lineas[2].PorConfirmar {
		t.Error("la línea sin precio tiene que salir marcada PorConfirmar")
	}
	if b.Lineas[0].LineTotal != 300 {
		t.Errorf("line_total = %v; se esperaba 300", b.Lineas[0].LineTotal)
	}
}

// TestBorradorDe_QtyCeroEsUno: `Qty` es un int de Go y no distingue «no vino» de
// «cero», así que el cero no puede significar «cero unidades».
func TestBorradorDe_QtyCeroEsUno(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{{SKU: "A", Label: "Torta", UnitPrice: 100}})
	if b.Lineas[0].Qty != 1 || b.Total != 100 {
		t.Fatalf("qty=%d total=%v; se esperaba qty=1 total=100", b.Lineas[0].Qty, b.Total)
	}
}
