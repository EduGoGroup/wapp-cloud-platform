package intakes

import (
	"errors"
	"math"
	"slices"
	"testing"
)

// Los defectos esperados de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/edit.go @ 64c181a) para este mismo corpus, casos
// adversarios incluidos (ver la cabecera de edit_test.go). Las runas invisibles van
// con su escape numérico, nunca pegadas en el fuente.

// Los cinco mensajes de defecto, tal como viajan al 400.
const (
	editMsgSKURequired   = "el sku es obligatorio: es lo que identifica al artículo en el pedido"
	editMsgSKUReserved   = "el sku empieza por _, que está reservado para las líneas que pone wApp (el envío): esas no se editan por aquí"
	editMsgLabelRequired = "la etiqueta es obligatoria: es lo que se lee en el pedido, en la comanda y en el CSV"
	editMsgQtyMinimum    = "la cantidad tiene que ser 1 o más; para quitar la línea, mándala fuera de la lista"
	editMsgPriceNegative = "el precio no puede ser negativo (0 sí: es un artículo de regalo)"
)

// TestValidateEditableItems_DefectsOfOneLine: una fila por regla y por frontera, sobre UNA
// línea. `want` vacío significa que la línea pasa.
func TestValidateEditableItems_DefectsOfOneLine(t *testing.T) {
	t.Parallel()
	one := func(field, msg string) []LineDefect { return []LineDefect{{Index: 0, Field: field, Message: msg}} }
	cases := []struct {
		name string
		item Item
		want []LineDefect
	}{
		{name: "everything wrong accumulates in field order", item: Item{UnitPrice: -1}, want: []LineDefect{
			{Index: 0, Field: "sku", Message: editMsgSKURequired},
			{Index: 0, Field: "label", Message: editMsgLabelRequired},
			{Index: 0, Field: "qty", Message: editMsgQtyMinimum},
			{Index: 0, Field: "unit_price", Message: editMsgPriceNegative},
		}},
		{name: "reserved sku with everything else wrong", item: Item{SKU: "_x", Label: " ", UnitPrice: -2}, want: []LineDefect{
			{Index: 0, Field: "sku", Message: editMsgSKUReserved},
			{Index: 0, Field: "label", Message: editMsgLabelRequired},
			{Index: 0, Field: "qty", Message: editMsgQtyMinimum},
			{Index: 0, Field: "unit_price", Message: editMsgPriceNegative},
		}},

		// sku.
		{name: "sku of ascii spaces is empty", item: Item{SKU: " \t\n", Label: "x", Qty: 1}, want: one("sku", editMsgSKURequired)},
		{name: "sku of nbsp is empty", item: Item{SKU: "\u00a0", Label: "x", Qty: 1}, want: one("sku", editMsgSKURequired)},
		{name: "sku of ideographic space is empty", item: Item{SKU: "\u3000", Label: "x", Qty: 1}, want: one("sku", editMsgSKURequired)},
		{name: "sku of zero width space counts as content", item: Item{SKU: "\u200b", Label: "x", Qty: 1}},
		{name: "sku of bom counts as content", item: Item{SKU: "\ufeff", Label: "x", Qty: 1}},
		{name: "reserved prefix", item: Item{SKU: "_envio", Label: "x", Qty: 1}, want: one("sku", editMsgSKUReserved)},
		{name: "the prefix alone is reserved", item: Item{SKU: "_", Label: "x", Qty: 1}, want: one("sku", editMsgSKUReserved)},
		{name: "the shipping sku is reserved", item: Item{SKU: ShippingSKU, Label: "x", Qty: 1}, want: one("sku", editMsgSKUReserved)},
		{name: "a leading space hides the prefix", item: Item{SKU: " _envio", Label: "x", Qty: 1}},
		{name: "fullwidth underscore is not the prefix", item: Item{SKU: "\uff3fenvio", Label: "x", Qty: 1}},
		{name: "inner underscore is fine", item: Item{SKU: "a_b", Label: "x", Qty: 1}},
		{name: "uppercase sku is fine", item: Item{SKU: "PAN", Label: "x", Qty: 1}},

		// label.
		{name: "label of unicode spaces is empty", item: Item{SKU: "a", Label: "\u00a0\u2003", Qty: 1}, want: one("label", editMsgLabelRequired)},
		{name: "label of zero width space counts as content", item: Item{SKU: "a", Label: "\u200b", Qty: 1}},

		// qty.
		{name: "qty zero", item: Item{SKU: "a", Label: "x", Qty: 0}, want: one("qty", editMsgQtyMinimum)},
		{name: "qty negative", item: Item{SKU: "a", Label: "x", Qty: -5}, want: one("qty", editMsgQtyMinimum)},
		{name: "qty one is the minimum", item: Item{SKU: "a", Label: "x", Qty: 1}},

		// unit_price.
		{name: "price zero is a gift", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: 0}},
		{name: "negative zero is zero", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: math.Copysign(0, -1)}},
		{name: "one cent below zero", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: -0.01}, want: one("unit_price", editMsgPriceNegative)},
		{name: "minus infinity", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: math.Inf(-1)}, want: one("unit_price", editMsgPriceNegative)},
		{name: "nan is not below zero", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: math.NaN()}},
		{name: "plus infinity is not below zero", item: Item{SKU: "a", Label: "x", Qty: 1, UnitPrice: math.Inf(1)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEditableItems([]Item{c.item})
			if len(c.want) == 0 {
				if err != nil {
					t.Fatalf("ValidateEditableItems = %v, quería nil", err)
				}
				return
			}
			var invalid *InvalidItemsError
			if !errors.As(err, &invalid) {
				t.Fatalf("ValidateEditableItems = %v, quería un *InvalidItemsError", err)
			}
			if !slices.Equal(invalid.Defects, c.want) {
				t.Errorf("defectos = %+v, quería %+v", invalid.Defects, c.want)
			}
		})
	}
}

// TestValidateEditableItems_IndexPointsAtTheLine: los defectos salen en el orden de las
// líneas, cada uno con la posición de SU línea, y las líneas buenas no inventan ninguno.
func TestValidateEditableItems_IndexPointsAtTheLine(t *testing.T) {
	t.Parallel()
	err := ValidateEditableItems([]Item{
		{SKU: "a", Label: "x", Qty: 1},
		{SKU: "", Label: "x", Qty: 1},
		{SKU: "a", Label: "x", Qty: 1},
		{SKU: "b", Label: "", Qty: 0},
	})
	var invalid *InvalidItemsError
	if !errors.As(err, &invalid) {
		t.Fatalf("ValidateEditableItems = %v, quería un *InvalidItemsError", err)
	}
	want := []LineDefect{
		{Index: 1, Field: "sku", Message: editMsgSKURequired},
		{Index: 3, Field: "label", Message: editMsgLabelRequired},
		{Index: 3, Field: "qty", Message: editMsgQtyMinimum},
	}
	if !slices.Equal(invalid.Defects, want) {
		t.Errorf("defectos = %+v, quería %+v", invalid.Defects, want)
	}
}
