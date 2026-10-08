package intakes

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/revalidate.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que el
// viejo devolvió para este mismo corpus, casos adversarios incluidos.

// martaLines son las líneas del 1 de enero: 2 × Pan $2.00 + 1 × Queso $3.00.
func martaLines() []Item {
	return []Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3},
	}
}

// catalogOfThe15th es el catálogo VIGENTE: el pan a $2.50 y el queso ya no está.
func catalogOfThe15th() PriceList {
	return NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan", Price: 2.5}})
}

// onlyBread es un catálogo con un único artículo, PAN, al precio dado.
func onlyBread(price float64) PriceList {
	return NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan", Price: price}})
}

// oneBread es un pedido de una sola línea: 1 × Pan al precio dado.
func oneBread(price float64) []Item {
	return []Item{{SKU: "PAN", Label: "Pan", Qty: 1, UnitPrice: price}}
}

// TestPriceList_ZeroValueIsUnresolved: el valor cero es «no se pudo leer»: ni resuelve ni encuentra.
func TestPriceList_ZeroValueIsUnresolved(t *testing.T) {
	var zero PriceList
	if zero.Resolved() {
		t.Error("PriceList{}.Resolved() = true, quería false: el valor cero es «no resolvió»")
	}
	if entry, ok := zero.Lookup("PAN"); ok || entry != (CatalogEntry{}) {
		t.Errorf("PriceList{}.Lookup = (%+v, %v), quería la entrada vacía y false", entry, ok)
	}
}

// TestNewPriceList_IsAlwaysResolved: un catálogo leído y vacío (nil o mapa vacío) SÍ resolvió.
func TestNewPriceList_IsAlwaysResolved(t *testing.T) {
	cases := map[string]map[string]CatalogEntry{
		"nil map":   nil,
		"empty map": {},
		"one entry": {"PAN": {Label: "Pan", Price: 2.5}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			list := NewPriceList(entries)
			if !list.Resolved() {
				t.Error("Resolved() = false, quería true: toda lista de NewPriceList está resuelta")
			}
			if entry, ok := list.Lookup("NOPE"); ok || entry != (CatalogEntry{}) {
				t.Errorf("Lookup de un sku ausente = (%+v, %v), quería la entrada vacía y false", entry, ok)
			}
		})
	}
}

// TestPriceList_LookupIsExact: la clave se busca byte a byte, sin plegar mayúsculas ni recortar.
func TestPriceList_LookupIsExact(t *testing.T) {
	spaced := " ESP " // clave con espacios a propósito: no se recorta
	list := NewPriceList(map[string]CatalogEntry{
		"PAN":  {Label: "Pan", Price: 2.5},
		spaced: {Label: "e", Price: 1},
		"":     {Label: "vacio", Price: 0},
	})
	cases := []struct {
		name   string
		sku    string
		want   CatalogEntry
		wantOK bool
	}{
		{name: "exact key", sku: "PAN", want: CatalogEntry{Label: "Pan", Price: 2.5}, wantOK: true},
		{name: "lower case is another key", sku: "pan"},
		{name: "mixed case is another key", sku: "Pan"},
		{name: "leading space is not trimmed", sku: " PAN"},
		{name: "trailing space is not trimmed", sku: "PAN "},
		{name: "key with spaces matches itself", sku: " ESP ", want: CatalogEntry{Label: "e", Price: 1}, wantOK: true},
		{name: "key with spaces does not match trimmed", sku: "ESP"},
		{name: "empty sku is a key like any other", sku: "", want: CatalogEntry{Label: "vacio"}, wantOK: true},
		{name: "absent sku", sku: "NOPE"},
		{name: "reserved sku is simply absent", sku: "_shipping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := list.Lookup(c.sku)
			if got != c.want || ok != c.wantOK {
				t.Errorf("Lookup(%q) = (%+v, %v), quería (%+v, %v)", c.sku, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestNewPriceList_DoesNotCopyTheMap: la lista consulta el mismo mapa que recibió.
func TestNewPriceList_DoesNotCopyTheMap(t *testing.T) {
	entries := map[string]CatalogEntry{"PAN": {Label: "Pan", Price: 2.5}}
	list := NewPriceList(entries)
	entries["NUEVO"] = CatalogEntry{Label: "n", Price: 1}
	if got, ok := list.Lookup("NUEVO"); !ok || got != (CatalogEntry{Label: "n", Price: 1}) {
		t.Errorf("Lookup tras añadir al mapa = (%+v, %v), quería la entrada nueva y true", got, ok)
	}
}

// TestRevalidate: una fila por regla de D-041.25 §a y por caso adversario. Items y Changes se
// comparan ENTEROS y en orden; nunca son nil.
func TestRevalidate(t *testing.T) {
	shipping := Item{SKU: "_shipping", Label: "Envío por confirmar", Qty: 1, UnitPrice: 4}
	cases := []struct {
		name        string
		items       []Item
		catalog     PriceList
		wantItems   []Item
		wantChanges []LineChange
		wantBefore  float64
		wantAfter   float64
	}{
		{
			name: "marta: one repriced, one removed, in line order", items: martaLines(), catalog: catalogOfThe15th(),
			wantItems: []Item{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.5}},
			wantChanges: []LineChange{
				{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, To: 2.5},
				{SKU: "QUESO", Label: "Queso", Qty: 1, From: 3, Removed: true},
			},
			wantBefore: 7, wantAfter: 5,
		},
		{
			name: "nothing changed", items: martaLines(),
			catalog:   NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan", Price: 2}, "QUESO": {Label: "Queso", Price: 3}}),
			wantItems: martaLines(), wantBefore: 7, wantAfter: 7,
		},
		{
			name: "a price drop is told too", items: martaLines(),
			catalog: NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan", Price: 1.5}, "QUESO": {Label: "Queso", Price: 3}}),
			wantItems: []Item{
				{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 1.5},
				{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3},
			},
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, To: 1.5}},
			wantBefore:  7, wantAfter: 6,
		},
		{
			name: "unresolved catalog touches nothing", items: martaLines(), catalog: PriceList{},
			wantItems: martaLines(), wantBefore: 7, wantAfter: 7,
		},
		{
			name: "catalog read and empty removes every line", items: martaLines(), catalog: NewPriceList(nil),
			wantChanges: []LineChange{
				{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, Removed: true},
				{SKU: "QUESO", Label: "Queso", Qty: 1, From: 3, Removed: true},
			},
			wantBefore: 7, wantAfter: 0,
		},
		{name: "nil items", items: nil, catalog: catalogOfThe15th()},
		{name: "empty items", items: []Item{}, catalog: catalogOfThe15th()},
		{name: "nil items with unresolved catalog", items: nil, catalog: PriceList{}},
		{
			name:      "a new label alone is applied but is not a change",
			items:     []Item{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2}},
			catalog:   NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan de masa madre", Price: 2}}),
			wantItems: []Item{{SKU: "PAN", Label: "Pan de masa madre", Qty: 2, UnitPrice: 2}}, wantBefore: 4, wantAfter: 4,
		},
		{
			name:        "a repriced change carries the current label",
			items:       []Item{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2}},
			catalog:     NewPriceList(map[string]CatalogEntry{"PAN": {Label: "Pan de masa madre", Price: 2.5}}),
			wantItems:   []Item{{SKU: "PAN", Label: "Pan de masa madre", Qty: 2, UnitPrice: 2.5}},
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan de masa madre", Qty: 2, From: 2, To: 2.5}},
			wantBefore:  4, wantAfter: 5,
		},
		// La tolerancia del céntimo, con fracciones binarias exactas a cada lado de 0.005.
		{
			name: "noise below the cent is not a change but the line takes the current price", items: oneBread(2), catalog: onlyBread(2.000000001),
			wantItems: oneBread(2.000000001), wantBefore: 2, wantAfter: 2.000000001,
		},
		{
			name: "0.00390625 above is within the cent", items: oneBread(0.5), catalog: onlyBread(0.50390625),
			wantItems: oneBread(0.50390625), wantBefore: 0.5, wantAfter: 0.50390625,
		},
		{
			name: "0.0078125 above is a change", items: oneBread(0.5), catalog: onlyBread(0.5078125),
			wantItems:   oneBread(0.5078125),
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 1, From: 0.5, To: 0.5078125}},
			wantBefore:  0.5, wantAfter: 0.5078125,
		},
		{
			name: "0.00390625 below is within the cent", items: oneBread(0.5), catalog: onlyBread(0.49609375),
			wantItems: oneBread(0.49609375), wantBefore: 0.5, wantAfter: 0.49609375,
		},
		{
			name: "0.0078125 below is a change", items: oneBread(0.5), catalog: onlyBread(0.4921875),
			wantItems:   oneBread(0.4921875),
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 1, From: 0.5, To: 0.4921875}},
			wantBefore:  0.5, wantAfter: 0.4921875,
		},
		// Precio cero y negativo: se re-precia igual, no se confunde con «retirada».
		{
			name: "current price zero reprices to zero", items: []Item{{SKU: "PAN", Label: "Pan", Qty: 3, UnitPrice: 2}}, catalog: onlyBread(0),
			wantItems:   []Item{{SKU: "PAN", Label: "Pan", Qty: 3, UnitPrice: 0}},
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 3, From: 2, To: 0}},
			wantBefore:  6, wantAfter: 0,
		},
		{
			name: "zero on both sides is no change", items: []Item{{SKU: "PAN", Label: "Pan", Qty: 3}}, catalog: onlyBread(0),
			wantItems: []Item{{SKU: "PAN", Label: "Pan", Qty: 3}},
		},
		{
			name: "a line at zero is repriced up", items: []Item{{SKU: "PAN", Label: "Pan", Qty: 3}}, catalog: onlyBread(2),
			wantItems:   []Item{{SKU: "PAN", Label: "Pan", Qty: 3, UnitPrice: 2}},
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 3, From: 0, To: 2}},
			wantAfter:   6,
		},
		{
			name: "negative current price is taken as is", items: oneBread(2), catalog: onlyBread(-1),
			wantItems:   oneBread(-1),
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 1, From: 2, To: -1}},
			wantBefore:  2, wantAfter: -1,
		},
		{
			name: "sku is matched exactly: case, spaces and empty are other skus",
			items: []Item{
				{SKU: "pan", Label: "Pan min", Qty: 1, UnitPrice: 2},
				{SKU: " PAN", Label: "Pan esp", Qty: 1, UnitPrice: 2},
				{SKU: "PAN ", Label: "Pan esp2", Qty: 1, UnitPrice: 2},
				{SKU: "", Label: "sin sku", Qty: 1, UnitPrice: 2},
				{SKU: "PAN", Label: "Pan", Qty: 1, UnitPrice: 2},
			},
			catalog:   onlyBread(2),
			wantItems: oneBread(2),
			wantChanges: []LineChange{
				{SKU: "pan", Label: "Pan min", Qty: 1, From: 2, Removed: true},
				{SKU: " PAN", Label: "Pan esp", Qty: 1, From: 2, Removed: true},
				{SKU: "PAN ", Label: "Pan esp2", Qty: 1, From: 2, Removed: true},
				{SKU: "", Label: "sin sku", Qty: 1, From: 2, Removed: true},
			},
			wantBefore: 10, wantAfter: 2,
		},
		{
			name:        "empty sku present in the catalog is repriced like any other",
			items:       []Item{{SKU: "", Label: "x", Qty: 1, UnitPrice: 2}},
			catalog:     NewPriceList(map[string]CatalogEntry{"": {Label: "Vacio", Price: 3}}),
			wantItems:   []Item{{SKU: "", Label: "Vacio", Qty: 1, UnitPrice: 3}},
			wantChanges: []LineChange{{SKU: "", Label: "Vacio", Qty: 1, From: 2, To: 3}},
			wantBefore:  2, wantAfter: 3,
		},
		{
			name: "duplicated skus are judged line by line and keep their customization",
			items: []Item{
				{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2, Customization: "sin sal"},
				{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3, Customization: "en lonchas"},
				{SKU: "PAN", Label: "Pan", Qty: 1, UnitPrice: 2.5, Customization: "tostado"},
				{SKU: "QUESO", Label: "Queso viejo", Qty: 4, UnitPrice: 1},
			},
			catalog: catalogOfThe15th(),
			wantItems: []Item{
				{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.5, Customization: "sin sal"},
				{SKU: "PAN", Label: "Pan", Qty: 1, UnitPrice: 2.5, Customization: "tostado"},
			},
			wantChanges: []LineChange{
				{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, To: 2.5},
				{SKU: "QUESO", Label: "Queso", Qty: 1, From: 3, Removed: true},
				{SKU: "QUESO", Label: "Queso viejo", Qty: 4, From: 1, Removed: true},
			},
			wantBefore: 13.5, wantAfter: 7.5,
		},
		{
			name: "reserved prefix lines are kept as they are, even when the catalog lists them",
			items: []Item{
				shipping,
				{SKU: "_", Label: "raya", Qty: 2, UnitPrice: 1},
				{SKU: "_PAN", Label: "falso", Qty: 1, UnitPrice: 9},
				{SKU: " _shipping", Label: "con espacio", Qty: 1, UnitPrice: 4},
				{SKU: "PAN_", Label: "sufijo", Qty: 1, UnitPrice: 4},
				{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2},
			},
			catalog: NewPriceList(map[string]CatalogEntry{
				"PAN": {Label: "Pan", Price: 2.5}, "_shipping": {Label: "Envío", Price: 99},
				"_PAN": {Label: "x", Price: 1}, "_": {Label: "y", Price: 7},
			}),
			wantItems: []Item{
				shipping,
				{SKU: "_", Label: "raya", Qty: 2, UnitPrice: 1},
				{SKU: "_PAN", Label: "falso", Qty: 1, UnitPrice: 9},
				{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.5},
			},
			wantChanges: []LineChange{
				{SKU: " _shipping", Label: "con espacio", Qty: 1, From: 4, Removed: true},
				{SKU: "PAN_", Label: "sufijo", Qty: 1, From: 4, Removed: true},
				{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, To: 2.5},
			},
			wantBefore: 27, wantAfter: 20,
		},
		{
			name:        "shipping survives and still adds up when every customer line is removed",
			items:       []Item{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2}, shipping},
			catalog:     NewPriceList(nil),
			wantItems:   []Item{shipping},
			wantChanges: []LineChange{{SKU: "PAN", Label: "Pan", Qty: 2, From: 2, Removed: true}},
			wantBefore:  8, wantAfter: 4,
		},
		{
			name: "zero and negative quantities are not validated here",
			items: []Item{
				{SKU: "PAN", Label: "Pan", Qty: 0, UnitPrice: 2},
				{SKU: "QUESO", Label: "Queso", Qty: -1, UnitPrice: 3},
			},
			catalog:   catalogOfThe15th(),
			wantItems: []Item{{SKU: "PAN", Label: "Pan", Qty: 0, UnitPrice: 2.5}},
			wantChanges: []LineChange{
				{SKU: "PAN", Label: "Pan", Qty: 0, From: 2, To: 2.5},
				{SKU: "QUESO", Label: "Queso", Qty: -1, From: 3, Removed: true},
			},
			wantBefore: -3, wantAfter: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Revalidate(c.items, c.catalog)
			if got.Items == nil || got.Changes == nil {
				t.Fatalf("Items nil=%v, Changes nil=%v: ninguna de las dos listas puede ser nil", got.Items == nil, got.Changes == nil)
			}
			if !slices.Equal(got.Items, c.wantItems) {
				t.Errorf("Items = %+v, quería %+v", got.Items, c.wantItems)
			}
			if !slices.Equal(got.Changes, c.wantChanges) {
				t.Errorf("Changes = %+v, quería %+v", got.Changes, c.wantChanges)
			}
			if got.TotalBefore != c.wantBefore || got.TotalAfter != c.wantAfter {
				t.Errorf("totales = (%v, %v), quería (%v, %v)", got.TotalBefore, got.TotalAfter, c.wantBefore, c.wantAfter)
			}
			if got.Changed() != (len(c.wantChanges) > 0) {
				t.Errorf("Changed() = %v con %d cambios esperados", got.Changed(), len(c.wantChanges))
			}
		})
	}
}

// TestRevalidate_DoesNotMutateInput: la función es pura; la lista de entrada queda intacta.
func TestRevalidate_DoesNotMutateInput(t *testing.T) {
	in := martaLines()
	Revalidate(in, catalogOfThe15th())
	if !slices.Equal(in, martaLines()) {
		t.Errorf("la entrada se mutó: %+v", in)
	}
}

// TestRevalidation_ChangedRepricedRemoved: las tres vistas salen SOLO de Changes, en su orden, y
// las dos listas nunca son nil — tampoco sobre el valor cero.
func TestRevalidation_ChangedRepricedRemoved(t *testing.T) {
	var zero Revalidation
	if zero.Changed() {
		t.Error("Revalidation{}.Changed() = true, quería false")
	}
	if got := zero.Repriced(); got == nil || len(got) != 0 {
		t.Errorf("Revalidation{}.Repriced() = %#v, quería una lista vacía no nil", got)
	}
	if got := zero.Removed(); got == nil || len(got) != 0 {
		t.Errorf("Revalidation{}.Removed() = %#v, quería una lista vacía no nil", got)
	}

	a := LineChange{SKU: "A", Label: "a", Qty: 1, From: 1, To: 2}
	b := LineChange{SKU: "B", Label: "b", Qty: 2, From: 3, Removed: true}
	c := LineChange{SKU: "C", Label: "c", Qty: 3, From: 4, To: 5.25}
	d := LineChange{SKU: "D", Label: "d", Qty: 4, From: 6, To: 99, Removed: true}
	rv := Revalidation{Changes: []LineChange{a, b, c, d}}
	if !rv.Changed() {
		t.Error("Changed() = false con cuatro cambios, quería true")
	}
	if got := rv.Repriced(); !slices.Equal(got, []LineChange{a, c}) {
		t.Errorf("Repriced() = %+v, quería A y C en ese orden", got)
	}
	if got := rv.Removed(); !slices.Equal(got, []LineChange{b, d}) {
		t.Errorf("Removed() = %+v, quería B y D en ese orden", got)
	}
	// Items con contenido y sin Changes: no hay nada que contar.
	if (Revalidation{Items: martaLines(), TotalBefore: 7, TotalAfter: 7}).Changed() {
		t.Error("Changed() = true sin Changes, quería false: solo Changes cuenta")
	}
}

// TestErrEmptyRevalidationText_Text: el texto del centinela es observable y se conserva byte a byte.
func TestErrEmptyRevalidationText_Text(t *testing.T) {
	const want = "la revalidación cambió el pedido pero no trae el texto que se le mandó al cliente"
	if got := ErrEmptyRevalidationText.Error(); got != want {
		t.Errorf("ErrEmptyRevalidationText = %q, quería %q", got, want)
	}
	if !errors.Is(fmt.Errorf("envuelto: %w", ErrEmptyRevalidationText), ErrEmptyRevalidationText) {
		t.Error("errors.Is no reconoce el centinela envuelto")
	}
}

// TestRevalidatedRevisionPayload: el JSON v1 de D-041.25 §d, entero y byte a byte.
func TestRevalidatedRevisionPayload(t *testing.T) {
	const pii = "para la Sra. Marta Pérez, Av. Siempre Viva 742"
	cases := []struct {
		name string
		rv   Revalidation
		want string
	}{
		{
			name: "marta", rv: Revalidate(martaLines(), catalogOfThe15th()),
			want: `{"version":1,"repriced":[{"sku":"PAN","from":2,"to":2.5}],"removed":[{"sku":"QUESO","label":"Queso","qty":1,"unit_price":3}],"total_before":7,"total_after":5}`,
		},
		{
			name: "zero value serializes both lists as empty arrays, never null", rv: Revalidation{},
			want: `{"version":1,"repriced":[],"removed":[],"total_before":0,"total_after":0}`,
		},
		{
			name: "unresolved catalog", rv: Revalidate(martaLines(), PriceList{}),
			want: `{"version":1,"repriced":[],"removed":[],"total_before":7,"total_after":7}`,
		},
		{
			// unit_price de la retirada es From (el To=99 de D se ignora); Items no entra.
			name: "each list follows the order of Changes",
			rv: Revalidation{
				Items: martaLines(),
				Changes: []LineChange{
					{SKU: "A", Label: "a", Qty: 1, From: 1, To: 2},
					{SKU: "B", Label: "b", Qty: 2, From: 3, Removed: true},
					{SKU: "C", Label: "c", Qty: 3, From: 4, To: 5.25},
					{SKU: "D", Label: "d", Qty: 4, From: 6, To: 99, Removed: true},
				},
				TotalBefore: 1.1, TotalAfter: 2.2,
			},
			want: `{"version":1,"repriced":[{"sku":"A","from":1,"to":2},{"sku":"C","from":4,"to":5.25}],"removed":[{"sku":"B","label":"b","qty":2,"unit_price":3},{"sku":"D","label":"d","qty":4,"unit_price":6}],"total_before":1.1,"total_after":2.2}`,
		},
		{
			// Criterio (g): la personalización de la línea retirada no viaja al payload.
			name: "a removed line never carries its customization",
			rv:   Revalidate([]Item{{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3, Customization: pii}}, catalogOfThe15th()),
			want: `{"version":1,"repriced":[],"removed":[{"sku":"QUESO","label":"Queso","qty":1,"unit_price":3}],"total_before":3,"total_after":0}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := RevalidatedRevisionPayload(c.rv)
			if err != nil {
				t.Fatalf("RevalidatedRevisionPayload devolvió el error %v, quería nil", err)
			}
			if got := string(raw); got != c.want {
				t.Errorf("payload = %s\nquería    %s", got, c.want)
			}
			if prefix := fmt.Sprintf(`{"version":%d,`, RevisionPayloadVersion); !strings.HasPrefix(string(raw), prefix) {
				t.Errorf("payload = %s, quería que empezara por %s", raw, prefix)
			}
			for _, needle := range []string{pii, "Marta", "Siempre Viva", "customization"} {
				if strings.Contains(string(raw), needle) {
					t.Errorf("el payload lleva %q: %s", needle, raw)
				}
			}
		})
	}
}

// TestRevalidatedRevisionPayload_UnserializableAmount: un importe NaN o infinito da error con el
// prefijo del paquete, byte a byte, y ningún payload.
func TestRevalidatedRevisionPayload_UnserializableAmount(t *testing.T) {
	cases := []struct {
		name string
		rv   Revalidation
		want string
	}{
		{
			name: "nan total", rv: Revalidation{TotalBefore: math.NaN()},
			want: "intakes: serializar payload de la revisión de revalidación: json: unsupported value: NaN",
		},
		{
			name: "infinite total", rv: Revalidation{TotalAfter: math.Inf(1)},
			want: "intakes: serializar payload de la revisión de revalidación: json: unsupported value: +Inf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := RevalidatedRevisionPayload(c.rv)
			if err == nil {
				t.Fatalf("RevalidatedRevisionPayload = %s sin error, quería un error", raw)
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, quería %q", got, c.want)
			}
			if raw != nil {
				t.Errorf("payload = %s junto al error, quería nil", raw)
			}
		})
	}
}
