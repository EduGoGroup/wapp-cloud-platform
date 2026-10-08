package intakes

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/shipping.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que
// el viejo devolvió para este mismo corpus. Los textos van escritos a mano, no con
// las constantes, para que el test no se mueva con lo que vigila.

// shippingTestFlatZone es la configuración más común de un tenant que cobra envío:
// una tarifa plana. Es el único caso en que wApp puede poner precio sola (v1).
func shippingTestFlatZone() ShippingZone {
	return ShippingZone{Code: "z1", Label: "Providencia", Price: 3000}
}

// TestShippingSKU_IsTheReservedLine: el sku de la línea de envío, con el prefijo
// reservado a las líneas que pone wApp.
func TestShippingSKU_IsTheReservedLine(t *testing.T) {
	t.Parallel()
	if ShippingSKU != "_shipping" {
		t.Errorf("ShippingSKU = %q, quería %q", ShippingSKU, "_shipping")
	}
}

// TestShippingPendingLabel_IsTheMarkTheOwnerReads: la etiqueta ES la marca de
// D-041.11; es un texto observable en la bandeja y en el CSV.
func TestShippingPendingLabel_IsTheMarkTheOwnerReads(t *testing.T) {
	t.Parallel()
	if ShippingPendingLabel != "Envío por confirmar" {
		t.Errorf("ShippingPendingLabel = %q, quería %q", ShippingPendingLabel, "Envío por confirmar")
	}
}

// TestShippingPolicy_Values: ShippingAlways es el valor cero del tipo.
func TestShippingPolicy_Values(t *testing.T) {
	t.Parallel()
	var zero ShippingPolicy
	if ShippingAlways != zero || int(ShippingAlways) != 0 {
		t.Errorf("ShippingAlways = %d, quería 0 (el valor cero)", ShippingAlways)
	}
	if int(ShippingOnlyIfZones) != 1 {
		t.Errorf("ShippingOnlyIfZones = %d, quería 1", ShippingOnlyIfZones)
	}
}

// TestShippingZone_JSONTagsAreTheColumnShape: la forma de tenant_settings.shipping_zones.
func TestShippingZone_JSONTagsAreTheColumnShape(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(shippingTestFlatZone())
	if err != nil {
		t.Fatalf("serializar la zona: %v", err)
	}
	const want = `{"code":"z1","label":"Providencia","price":3000}`
	if string(raw) != want {
		t.Errorf("zona = %s, quería %s", raw, want)
	}
}

// TestParseShippingZones_Reads: vacío es «sin zonas» y no un error; el resto se lee
// como lo lee encoding/json.
func TestParseShippingZones_Reads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     []byte
		want    []ShippingZone
		wantNil bool
	}{
		{name: "nil is no zones", raw: nil, wantNil: true},
		{name: "empty is no zones", raw: []byte(``), wantNil: true},
		{name: "json null is no zones", raw: []byte(`null`), wantNil: true},
		{name: "empty list is an empty non nil list", raw: []byte(`[]`), want: []ShippingZone{}},
		{name: "one zone", raw: []byte(`[{"code":"z1","label":"Providencia","price":3000}]`), want: []ShippingZone{shippingTestFlatZone()}},
		{
			name: "two zones keep their order",
			raw:  []byte(`[{"code":"z1","label":"Providencia","price":3000},{"code":"z2","label":"Puente Alto","price":5000.5}]`),
			want: []ShippingZone{shippingTestFlatZone(), {Code: "z2", Label: "Puente Alto", Price: 5000.5}},
		},
		{
			name: "keys match case insensitively and unknown fields are ignored",
			raw:  []byte(`[{"CODE":"z1","Label":"P","PRICE":1,"extra":true}]`),
			want: []ShippingZone{{Code: "z1", Label: "P", Price: 1}},
		},
		{name: "empty object is a zero zone", raw: []byte(`[{}]`), want: []ShippingZone{{}}},
		{name: "null element is a zero zone", raw: []byte(`[null]`), want: []ShippingZone{{}}},
		{name: "surrounding whitespace", raw: []byte(` [ { "code" : "z1" } ] `), want: []ShippingZone{{Code: "z1"}}},
		{
			name: "non ascii digit, nbsp label and negative price are kept as is",
			raw:  []byte(`[{"code":"z` + "\u0661" + `","label":"` + "\u00a0" + `","price":-1}]`),
			want: []ShippingZone{{Code: "z\u0661", Label: "\u00a0", Price: -1}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			zones, err := ParseShippingZones(c.raw)
			if err != nil {
				t.Fatalf("ParseShippingZones: %v", err)
			}
			if c.wantNil {
				if zones != nil {
					t.Errorf("zonas = %+v, quería nil", zones)
				}
				return
			}
			if zones == nil || !slices.Equal(zones, c.want) {
				t.Errorf("zonas = %#v, quería %#v", zones, c.want)
			}
		})
	}
}

// TestParseShippingZones_BrokenBlobIsAnError: un blob ilegible se propaga; devolver
// «sin zonas» haría que un tenant que SÍ cobra envío dejara de cobrarlo en silencio.
func TestParseShippingZones_BrokenBlobIsAnError(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, raw, want string }{
		{"object where the list goes", `{"z1":3000}`, "intakes: zonas de envío del tenant ilegibles: json: cannot unmarshal object into Go value of type []intakes.ShippingZone"},
		{"string where the list goes", `"zonas"`, "intakes: zonas de envío del tenant ilegibles: json: cannot unmarshal string into Go value of type []intakes.ShippingZone"},
		{"price is a string", `[{"code":"z1","price":"3000"}]`, "intakes: zonas de envío del tenant ilegibles: json: cannot unmarshal string into Go struct field ShippingZone.price of type float64"},
		{"code is a number", `[{"code":1}]`, "intakes: zonas de envío del tenant ilegibles: json: cannot unmarshal number into Go struct field ShippingZone.code of type string"},
		{"price out of range", `[{"price":1e400}]`, "intakes: zonas de envío del tenant ilegibles: json: cannot unmarshal number 1e400 into Go struct field ShippingZone.price of type float64"},
		{"truncated blob", `[{"code":"z1"`, "intakes: zonas de envío del tenant ilegibles: unexpected end of JSON input"},
		{"whitespace only blob", ` `, "intakes: zonas de envío del tenant ilegibles: unexpected end of JSON input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			zones, err := ParseShippingZones([]byte(c.raw))
			if err == nil {
				t.Fatalf("un shipping_zones ilegible devolvió %+v sin error", zones)
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, quería %q", got, c.want)
			}
			if zones != nil {
				t.Errorf("con error las zonas deben ser nil, got %+v", zones)
			}
			var typeErr *json.UnmarshalTypeError
			var syntaxErr *json.SyntaxError
			if !errors.As(err, &typeErr) && !errors.As(err, &syntaxErr) {
				t.Errorf("el error de encoding/json no va envuelto con %%w: %v", err)
			}
		})
	}
}

// TestDesiredShippingLine: una zona nombrable pone el precio; ninguna o varias
// dejan la línea «por confirmar» a 0. Las zonas sin nombre no cuentan.
func TestDesiredShippingLine(t *testing.T) {
	t.Parallel()
	pending := ShippingLine{Label: "Envío por confirmar"}
	cases := []struct {
		name  string
		zones []ShippingZone
		want  ShippingLine
	}{
		{"nil zones", nil, pending},
		{"empty zones", []ShippingZone{}, pending},
		{"one zone sets the price", []ShippingZone{shippingTestFlatZone()}, ShippingLine{Label: "Envío — Providencia", UnitPrice: 3000, Priced: true}},
		{"two zones are not guessed", []ShippingZone{shippingTestFlatZone(), {Code: "z2", Label: "Puente Alto", Price: 5000}}, pending},
		{"three code only zones are not guessed", []ShippingZone{{Code: "a", Price: 1}, {Code: "b", Price: 2}, {Code: "c", Price: 3}}, pending},
		{"two identical zones are two zones", []ShippingZone{{Code: "z1", Label: "A", Price: 1}, {Price: 5}, {Code: "z1", Label: "A", Price: 1}}, pending},
		{"unnamed zone does not count, the other is named by its code", []ShippingZone{{Price: 9999}, {Code: "z9", Price: 2000}}, ShippingLine{Label: "Envío — z9", UnitPrice: 2000, Priced: true}},
		{"named zone between unnamed ones wins, negative price copied", []ShippingZone{{Price: 5}, {Label: "B", Price: -7}, {}}, ShippingLine{Label: "Envío — B", UnitPrice: -7, Priced: true}},
		{"single unnamed zone resolves nothing", []ShippingZone{{Price: 9999}}, pending},
		{"only unnamed zones resolve nothing", []ShippingZone{{Price: 1}, {Price: 2}}, pending},
		{"label only", []ShippingZone{{Label: "Solo etiqueta", Price: 1500.5}}, ShippingLine{Label: "Envío — Solo etiqueta", UnitPrice: 1500.5, Priced: true}},
		{"code only at zero price is still priced", []ShippingZone{{Code: "solo-codigo"}}, ShippingLine{Label: "Envío — solo-codigo", UnitPrice: 0, Priced: true}},
		{"label wins over code", []ShippingZone{{Code: "z1", Label: "Providencia", Price: 1}}, ShippingLine{Label: "Envío — Providencia", UnitPrice: 1, Priced: true}},
		{"a space is a name, nothing is trimmed", []ShippingZone{{Label: " ", Price: 10}}, ShippingLine{Label: "Envío —  ", UnitPrice: 10, Priced: true}},
		{"a no-break space label is a name and wins over the code", []ShippingZone{{Code: "Z1", Label: "\u00a0", Price: 3}}, ShippingLine{Label: "Envío — \u00a0", UnitPrice: 3, Priced: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := DesiredShippingLine(c.zones); got != c.want {
				t.Errorf("línea = %+v, quería %+v", got, c.want)
			}
		})
	}
}

// TestDesiredShippingLine_SeparatorIsAnEmDash: la etiqueta de zona usa la raya
// larga (U+2014) entre espacios, no un guion.
func TestDesiredShippingLine_SeparatorIsAnEmDash(t *testing.T) {
	t.Parallel()
	got := DesiredShippingLine([]ShippingZone{{Code: "z"}})
	want := "Env" + "\u00ed" + "o " + "\u2014" + " z"
	if got.Label != want {
		t.Errorf("label = %+q, quería %+q", got.Label, want)
	}
}

// TestShippingLine_Supersedes: la mitad delicada de la idempotencia. Con zona, la
// configuración manda y pisa lo que difiera; sin zona, lo que hay es el precio que
// puso el dueño y no se toca NUNCA.
func TestShippingLine_Supersedes(t *testing.T) {
	t.Parallel()
	zoned := ShippingLine{Label: "Envío — Providencia", UnitPrice: 3000, Priced: true}
	pending := ShippingLine{Label: "Envío por confirmar"}
	unpricedTwin := ShippingLine{Label: "Envío — Providencia", UnitPrice: 3000}

	cases := []struct {
		name   string
		stored Item
		want   bool // lo que responde la línea CON zona
	}{
		{"same line: nothing to write", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000}, false},
		{"fare changed", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 2000}, true},
		{"zone changed", Item{SKU: "_shipping", Label: "Envío — Puente Alto", Qty: 1, UnitPrice: 3000}, true},
		{"quantity is two", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 2, UnitPrice: 3000}, true},
		{"quantity is zero", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 0, UnitPrice: 3000}, true},
		{"sku is not looked at", Item{SKU: "otro", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000}, false},
		{"customization is not looked at", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000, Customization: "sin cebolla"}, false},
		{"label differs in case", Item{SKU: "_shipping", Label: "envío — providencia", Qty: 1, UnitPrice: 3000}, true},
		{"label has a trailing space", Item{SKU: "_shipping", Label: "Envío — Providencia ", Qty: 1, UnitPrice: 3000}, true},
		{"label uses a hyphen instead of the dash", Item{SKU: "_shipping", Label: "Envío - Providencia", Qty: 1, UnitPrice: 3000}, true},
		{"line priced by hand while pending", Item{SKU: "_shipping", Label: "Envío por confirmar", Qty: 1, UnitPrice: 2500}, true},
		{"price differs in the seventh decimal", Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000.0000001}, true},
		{"zero item", Item{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := zoned.Supersedes(c.stored); got != c.want {
				t.Errorf("con zona: Supersedes(%+v) = %v, quería %v", c.stored, got, c.want)
			}
			if pending.Supersedes(c.stored) {
				t.Errorf("sin zona no hay autoridad de configuración: pisar %+v borra el precio del dueño", c.stored)
			}
			if unpricedTwin.Supersedes(c.stored) {
				t.Errorf("lo que decide es Priced, no la etiqueta: una línea sin Priced no pisa %+v", c.stored)
			}
		})
	}
}

// TestShippingPolicy_Applies: la regla de CUÁNDO se materializa la línea. Cuenta las
// zonas CONFIGURADAS, no las resolubles: una zona sin nombre también es cobrar
// envío. Es un auxiliar no exportado con regla de negocio (05 E-4, P6): su
// consumidor —el servicio y los stores— nace en otra tarea.
func TestShippingPolicy_Applies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		policy ShippingPolicy
		zones  []ShippingZone
		want   bool
	}{
		{"always without zones", ShippingAlways, nil, true},
		{"always with zones", ShippingAlways, []ShippingZone{shippingTestFlatZone()}, true},
		{"only if zones without zones", ShippingOnlyIfZones, nil, false},
		{"only if zones with an empty list", ShippingOnlyIfZones, []ShippingZone{}, false},
		{"only if zones with one zone", ShippingOnlyIfZones, []ShippingZone{shippingTestFlatZone()}, true},
		{"only if zones counts a zone that cannot be named", ShippingOnlyIfZones, []ShippingZone{{Price: 1}}, true},
		{"only if zones with several zones still applies", ShippingOnlyIfZones, []ShippingZone{shippingTestFlatZone(), {Code: "z2"}}, true},
	}
	for _, c := range cases {
		if got := c.policy.applies(c.zones); got != c.want {
			t.Errorf("%s: applies = %v, quería %v", c.name, got, c.want)
		}
	}
}

// TestShippingLine_ItemIsTheStoredRow: la línea dictada se persiste con el sku
// reservado, UNA unidad y sin personalización, que es del cliente y no de wApp.
func TestShippingLine_ItemIsTheStoredRow(t *testing.T) {
	t.Parallel()
	got := ShippingLine{Label: "Envío — Providencia", UnitPrice: 3000, Priced: true}.item()
	want := Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000}
	if got != want {
		t.Errorf("fila = %+v, quería %+v", got, want)
	}
}
