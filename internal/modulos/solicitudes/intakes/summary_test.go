package intakes

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/summary.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que
// el viejo devolvió para este mismo corpus.

// summaryDay fija un instante determinista de agosto de 2026.
func summaryDay(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }

// TestMaxTopItems_Is20: el ranking está pensado para un LLM externo (D-041.15).
func TestMaxTopItems_Is20(t *testing.T) {
	t.Parallel()
	if MaxTopItems != 20 {
		t.Errorf("MaxTopItems = %d, quería 20", MaxTopItems)
	}
}

// TestBuildSummary_Aggregates: totales, desglose por estado y ranking sobre un caso hecho a
// mano. La cancelada también suma, el `closed` legado cuenta como `confirmed`, Revenue es el de
// las cabeceras (8700) y no el de las líneas (8000), y del filtro solo viajan From y To.
func TestBuildSummary_Aggregates(t *testing.T) {
	t.Parallel()
	details := []Detail{
		{
			Intake: Intake{ID: "b", ContactID: "opaque", Status: "confirmed", Total: 5000},
			Items: []Item{
				{SKU: "torta", Label: "Torta grande", Qty: 2, UnitPrice: 2000},
				{SKU: "vela", Label: "Velas", Qty: 1, UnitPrice: 1000},
			},
		},
		{
			Intake: Intake{ID: "a", Status: "closed", Total: 3000},
			Items:  []Item{{SKU: "torta", Label: "Torta chica", Qty: 1, UnitPrice: 3000}},
		},
		{Intake: Intake{ID: "c", Status: "cancelled", Total: 700}},
	}
	filter := Filter{From: summaryDay(1), To: summaryDay(5), Statuses: []string{"x"}, Page: 3}

	sum := BuildSummary(details, filter, summaryDay(9))

	if sum.Intakes != 3 || sum.Revenue != 8700 {
		t.Errorf("Intakes = %d, Revenue = %v; quería 3 y 8700", sum.Intakes, sum.Revenue)
	}
	if want := map[string]int{"confirmed": 2, "cancelled": 1}; !maps.Equal(sum.ByStatus, want) {
		t.Errorf("ByStatus = %v, quería %v", sum.ByStatus, want)
	}
	wantTop := []TopItem{
		{SKU: "torta", Label: "Torta grande", QtyTotal: 3, Revenue: 7000},
		{SKU: "vela", Label: "Velas", QtyTotal: 1, Revenue: 1000},
	}
	if !slices.Equal(sum.TopItems, wantTop) {
		t.Errorf("TopItems = %+v, quería %+v", sum.TopItems, wantTop)
	}
	if !sum.GeneratedAt.Equal(summaryDay(9)) || !sum.From.Equal(summaryDay(1)) || !sum.To.Equal(summaryDay(5)) {
		t.Errorf("cabecera = %v / %v / %v, quería el instante dado y el rango del filtro", sum.GeneratedAt, sum.From, sum.To)
	}
	if len(sum.Details) != 3 || &sum.Details[0] != &details[0] {
		t.Errorf("Details no es la lista recibida, en su orden: %+v", sum.Details)
	}
}

// TestBuildSummary_NoIntakes: sin solicitudes todo es cero, ByStatus y TopItems salen vacíos y
// no nil, y GeneratedAt sale en UTC aunque el instante llegue en otra zona.
func TestBuildSummary_NoIntakes(t *testing.T) {
	t.Parallel()
	local := time.Date(2026, 8, 9, 9, 30, 0, 0, time.FixedZone("ART", -3*3600))
	for _, sum := range []Summary{BuildSummary(nil, Filter{}, local), BuildSummary([]Detail{}, Filter{}, local)} {
		if sum.Intakes != 0 || sum.Revenue != 0 {
			t.Errorf("Intakes = %d, Revenue = %v; quería 0 y 0", sum.Intakes, sum.Revenue)
		}
		if sum.ByStatus == nil || len(sum.ByStatus) != 0 {
			t.Errorf("ByStatus = %v (nil=%v), quería un mapa vacío no nulo", sum.ByStatus, sum.ByStatus == nil)
		}
		if sum.TopItems == nil || len(sum.TopItems) != 0 {
			t.Errorf("TopItems = %v (nil=%v), quería una lista vacía no nula", sum.TopItems, sum.TopItems == nil)
		}
		if got := sum.GeneratedAt.Format(time.RFC3339); got != "2026-08-09T12:30:00Z" || sum.GeneratedAt.Location() != time.UTC {
			t.Errorf("GeneratedAt = %s (%s), quería 2026-08-09T12:30:00Z en UTC", got, sum.GeneratedAt.Location())
		}
		if !sum.From.IsZero() || !sum.To.IsZero() {
			t.Errorf("From/To = %v / %v, quería cero (sin cota)", sum.From, sum.To)
		}
	}
}

// TestBuildSummary_ByStatusNormalizesWithoutValidating: solo el `closed` exacto cae en
// `confirmed`; lo desconocido, lo vacío y los parecidos tienen su propio cubo. Ningún estado se
// excluye de Revenue, y un total negativo resta.
func TestBuildSummary_ByStatusNormalizesWithoutValidating(t *testing.T) {
	t.Parallel()
	sum := BuildSummary([]Detail{
		{Intake: Intake{Status: "closed", Total: 0.1}},
		{Intake: Intake{Status: "CLOSED", Total: 0.2}},
		{Intake: Intake{Status: "", Total: 1}},
		{Intake: Intake{Status: "en_camino", Total: -5}},
		{Intake: Intake{Status: " closed"}},
		{Intake: Intake{Status: "expired"}},
		{Intake: Intake{Status: "confirmed"}},
	}, Filter{}, summaryDay(9))

	want := map[string]int{"": 1, " closed": 1, "CLOSED": 1, "confirmed": 2, "en_camino": 1, "expired": 1}
	if !maps.Equal(sum.ByStatus, want) {
		t.Errorf("ByStatus = %v, quería %v", sum.ByStatus, want)
	}
	if sum.Intakes != 7 || sum.Revenue != -3.7 {
		t.Errorf("Intakes = %d, Revenue = %v; quería 7 y -3.7", sum.Intakes, sum.Revenue)
	}
}

// TestBuildSummary_RankingOrderAndTieBreaks: facturación descendente, después cantidad
// descendente, después SKU ascendente (byte a byte: "Alfa" antes que "alfa"). El SKU vacío se
// agrega como cualquier otro con la etiqueta de su primera línea, la línea de la plataforma
// entra, y los de facturación cero o negativa quedan al final. Repetido para que un orden
// decidido por el recorrido del mapa no pase por suerte.
func TestBuildSummary_RankingOrderAndTieBreaks(t *testing.T) {
	t.Parallel()
	details := []Detail{{Intake: Intake{Status: "open", Total: 1}, Items: []Item{
		{SKU: "zeta", Label: "Zeta", Qty: 1, UnitPrice: 100},
		{SKU: "alfa", Label: "Alfa", Qty: 1, UnitPrice: 100},
		{SKU: "many", Label: "Many", Qty: 4, UnitPrice: 25},
		{SKU: "big", Label: "Big", Qty: 1, UnitPrice: 100.5},
		{SKU: "", Label: "sin sku", Qty: 2, UnitPrice: 10},
		{SKU: "", Label: "otra", Qty: 1, UnitPrice: 10},
		{SKU: "free", Label: "Gratis", Qty: 9, UnitPrice: 0},
		{SKU: "neg", Label: "Descuento", Qty: 1, UnitPrice: -50},
		{SKU: "sys:shipping", Label: "Envío", Customization: "no cuenta", Qty: 1, UnitPrice: 7},
		{SKU: "Alfa", Label: "Mayúscula", Qty: 1, UnitPrice: 100},
	}}}
	want := []TopItem{
		{SKU: "big", Label: "Big", QtyTotal: 1, Revenue: 100.5},
		{SKU: "many", Label: "Many", QtyTotal: 4, Revenue: 100},
		{SKU: "Alfa", Label: "Mayúscula", QtyTotal: 1, Revenue: 100},
		{SKU: "alfa", Label: "Alfa", QtyTotal: 1, Revenue: 100},
		{SKU: "zeta", Label: "Zeta", QtyTotal: 1, Revenue: 100},
		{SKU: "", Label: "sin sku", QtyTotal: 3, Revenue: 30},
		{SKU: "sys:shipping", Label: "Envío", QtyTotal: 1, Revenue: 7},
		{SKU: "free", Label: "Gratis", QtyTotal: 9, Revenue: 0},
		{SKU: "neg", Label: "Descuento", QtyTotal: 1, Revenue: -50},
	}
	for range 20 {
		if got := BuildSummary(details, Filter{}, summaryDay(9)).TopItems; !slices.Equal(got, want) {
			t.Fatalf("TopItems = %+v, quería %+v", got, want)
		}
	}
}

// TestBuildSummary_RankingIsCappedAtMaxTopItems: 20 entran enteros; con 21 se cae el que menos
// factura y arriba queda el que más.
func TestBuildSummary_RankingIsCappedAtMaxTopItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		skus                int
		wantLen             int
		wantFirst, wantLast string
	}{
		{19, 19, "sku-18", "sku-00"},
		{20, 20, "sku-19", "sku-00"},
		{21, 20, "sku-20", "sku-01"},
		{25, 20, "sku-24", "sku-05"},
	}
	for _, c := range cases {
		items := make([]Item, 0, c.skus)
		for i := range c.skus {
			items = append(items, Item{SKU: fmt.Sprintf("sku-%02d", i), Qty: 1, UnitPrice: float64(i + 1)})
		}
		top := BuildSummary([]Detail{{Intake: Intake{Status: "confirmed"}, Items: items}}, Filter{}, summaryDay(9)).TopItems
		if len(top) != c.wantLen {
			t.Errorf("%d SKUs: len(TopItems) = %d, quería %d", c.skus, len(top), c.wantLen)
			continue
		}
		if top[0].SKU != c.wantFirst || top[len(top)-1].SKU != c.wantLast {
			t.Errorf("%d SKUs: ranking de %q a %q, quería de %q a %q", c.skus, top[0].SKU, top[len(top)-1].SKU, c.wantFirst, c.wantLast)
		}
	}
}
