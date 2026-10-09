//go:build pendiente

package stages_test

// Trozo de match_test.go (E-13): lo que Match.Run hace AL FINAL, que es del pedido
// entero y no de un ítem — la línea de envío, la nota del pedido y la línea de cierre
// del log.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ---------------------------------------------------------------------------
// EL ENVÍO Y LA NOTA DEL PEDIDO
// ---------------------------------------------------------------------------

// TestMatchRun_ShippingLineIsAlwaysThere recorre las tres configuraciones de zonas. El
// precio lo dicta `intakes.DesiredShippingLine`, no esta etapa.
func TestMatchRun_ShippingLineIsAlwaysThere(t *testing.T) {
	providencia := intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3000}
	puenteAlto := intakes.ShippingZone{Code: "z2", Label: "Puente Alto", Price: 5000}
	cases := []struct {
		name   string
		zones  []intakes.ShippingZone
		label  string
		priced bool
		note   string
	}{
		{"no zones: to be confirmed", nil, "Envío por confirmar", false, "por confirmar zona"},
		{"one zone: its rate, charged", []intakes.ShippingZone{providencia}, "Envío — Providencia", true, ""},
		{"two zones: wApp does not choose", []intakes.ShippingZone{providencia, puenteAlto}, "Envío por confirmar", false, "por confirmar zona"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newMatchBench(t)
			art := b.run(t, stages.MatchInput{Quantities: p4Of(), Index: indexOf(t, ambarCatalog()), Zones: c.zones})

			assertLineCount(t, art, 1, "un pedido sin un solo ítem sigue llevando su línea de envío")
			shipping := art.Lines[0]
			if shipping.Kind != stages.KindShipping || shipping.SKU != "_shipping" || shipping.Qty != 1 {
				t.Fatalf("envío = %+v", shipping)
			}
			if shipping.Label != c.label || shipping.Note != c.note {
				t.Fatalf("envío = etiqueta %q, nota %q; se esperaba %q, %q", shipping.Label, shipping.Note, c.label, c.note)
			}
			if c.priced {
				assertPrice(t, shipping, 3000)
				assertTotal(t, art, 3000, 0, "el envío cobrado entra en el total")
				return
			}
			if shipping.UnitPrice != nil {
				t.Fatalf("precio del envío = %v; sin zona resuelta lo pone el dueño", *shipping.UnitPrice)
			}
		})
	}
}

// TestMatchRun_OrderNoteGoesToItsSlotAndNowhereElse: la nota va a `customer_note`,
// saneada, y NO crea líneas ni ensucia ninguna `customization`.
func TestMatchRun_OrderNoteGoesToItsSlotAndNowhereElse(t *testing.T) {
	b := newMatchBench(t)
	art := b.run(t, stages.MatchInput{
		Quantities: p4Of(llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"}),
		Index:      indexOf(t, ambarCatalog()),
		Note:       stages.OrderNote("dejarlo\ten   portería"),
	})

	if art.CustomerNote != "dejarlo en portería" {
		t.Fatalf("customer_note = %q; pasa por intakes.SanitizeNote (tabulador a espacio, repetidos colapsados)", art.CustomerNote)
	}
	assertLineCount(t, art, 2, "hamburguesa y envío: la nota NO crea líneas")
	for _, l := range art.Lines {
		if l.Customization != "" {
			t.Fatalf("la nota del pedido no se reparte: la línea %q lleva %q", l.Label, l.Customization)
		}
	}
	assertWarnings(t, art)
}

// TestMatchRun_OrderNoteTooLongIsDroppedWholeNotTruncated: no se trunca (el final es
// donde va el alérgeno) y no tumba nada; queda un Warn que no cita la nota.
func TestMatchRun_OrderNoteTooLongIsDroppedWholeNotTruncated(t *testing.T) {
	long := strings.Repeat("dejarlo en la portería del edificio azul, ", 10)
	if len([]rune(long)) <= intakes.MaxNoteRunes {
		t.Fatalf("el fixture mide %d runas: tiene que pasarse de %d de verdad", len([]rune(long)), intakes.MaxNoteRunes)
	}
	b := newMatchBench(t)
	art := b.run(t, stages.MatchInput{
		Quantities: p4Of(llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"}),
		Index:      indexOf(t, ambarCatalog()),
		Note:       stages.OrderNote(long),
	})

	if art.CustomerNote != "" {
		t.Fatalf("customer_note = %q; o cabe entera o no va", art.CustomerNote)
	}
	assertLineCount(t, art, 2, "la nota larga no tumba el borrador")
	assertPrice(t, art.Lines[0], 3000)
	assertWarnings(t, art) // la nota no es de ningún ítem: no hay posición que avisar
	log := b.log.String()
	if !strings.Contains(log, "match: la nota del pedido no cabe y se descarta SIN truncar") || !strings.Contains(log, "level=WARN") {
		t.Fatalf("el descarte de la nota no dejó su Warn: %q", log)
	}
	if strings.Contains(log, "portería") {
		t.Fatalf("el log cita la nota del cliente: %q", log)
	}
}

// TestMatchRun_TodayNobodyProducesTheOrderNote fija el HUECO con literales.
//
// 🔴 AFIRMA LO QUE HOY PASA, NO LO QUE DEBERÍA: ninguna etapa anterior emite una nota de
// pedido, así que «dejarlo en portería» llega como UN ÍTEM MÁS y sale `unmatched`. Si
// algún día P2 aprende a separarla, este test se pondrá rojo: será la señal de que el
// hueco se cerró, no una regresión.
func TestMatchRun_TodayNobodyProducesTheOrderNote(t *testing.T) {
	b := newMatchBench(t)
	art := b.run(t, stages.MatchInput{
		Quantities: p4Of(
			llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"},
			llm.NormalizedItem{Product: "dejarlo en portería", Qty: 1, Evidence: "y dejarlo en portería"},
		),
		Index: indexOf(t, ambarCatalog()),
		Note:  stages.NoOrderNote,
	})

	if art.CustomerNote != "" || stages.NoOrderNote != "" {
		t.Fatalf("customer_note = %q; hoy la nota NO llega por su ranura", art.CustomerNote)
	}
	assertLineCount(t, art, 3, "hamburguesa, la instrucción como renglón y envío")
	assertUnmatched(t, art.Lines[1], "dejarlo en portería")
}

// ---------------------------------------------------------------------------
// EL LOG
// ---------------------------------------------------------------------------

// TestMatchRun_ClosingLogCountsAndNeverQuotesTheCustomer: la línea de cierre lleva las
// cuentas, y por el log no sale ni una palabra del cliente.
func TestMatchRun_ClosingLogCountsAndNeverQuotesTheCustomer(t *testing.T) {
	b := newMatchBench(t, stages.WithGrayZone(&fakeGrayZone{err: errGrayZone}))
	items := append(ambarItems(), llm.NormalizedItem{Product: "hamburguesa", Qty: 0, Evidence: "y una hamburguesa doble"})
	b.run(t, stages.MatchInput{Quantities: p4Of(items...), Index: indexOf(t, ambarCatalog())})

	log := b.log.String()
	for _, want := range []string{
		"match: catálogo cruzado y líneas construidas", "job_id=" + jobID, "stage=match",
		"items=4", "lineas=6", "total_parcial=1290", "lineas_sin_precio=3", "avisos=3",
		"zona_gris_llamadas=2", "catalogo_articulos=7",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("la línea de cierre no lleva %q: %q", want, log)
		}
	}
	for _, customer := range []string{
		"torta de chocolate", "vainilla", "sin lactosa", "decoración infantil", "hamburguesa doble",
		chocolateCakeEvidence, vanillaCakeEvidence, tequenosEvidence,
	} {
		if strings.Contains(log, customer) {
			t.Fatalf("el log volcó texto del cliente (%q): %q", customer, log)
		}
	}
}
