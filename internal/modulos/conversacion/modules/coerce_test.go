package modules_test

import (
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

func TestAsFloat(t *testing.T) {
	cases := map[string]struct {
		in   any
		want float64
	}{
		"float64":        {2.5, 2.5},
		"float32":        {float32(1.5), 1.5},
		"int":            {3, 3},
		"int64":          {int64(4), 4},
		"negative":       {-7, -7},
		"numeric string": {"2.5", 0},
		"int32":          {int32(9), 0},
		"bool":           {true, 0},
		"nil":            {nil, 0},
	}
	for name, tc := range cases {
		if got := modules.AsFloat(tc.in); got != tc.want {
			t.Errorf("%s: AsFloat(%#v) = %v, quiero %v", name, tc.in, got, tc.want)
		}
	}
}

func TestAsInt(t *testing.T) {
	cases := map[string]struct {
		in   any
		want int
	}{
		"int":                {3, 3},
		"int64":              {int64(4), 4},
		"float64":            {float64(5), 5},
		"float64 truncates":  {2.9, 2},
		"negative truncates": {-2.9, -2},
		"float32 is not int": {float32(6), 0},
		"numeric string":     {"7", 0},
		"nil":                {nil, 0},
	}
	for name, tc := range cases {
		if got := modules.AsInt(tc.in); got != tc.want {
			t.Errorf("%s: AsInt(%#v) = %v, quiero %v", name, tc.in, got, tc.want)
		}
	}
}

func TestAsString(t *testing.T) {
	if got := modules.AsString("hola"); got != "hola" {
		t.Errorf("AsString(hola) = %q", got)
	}
	for _, in := range []any{nil, 3, 2.5, []byte("x"), true} {
		if got := modules.AsString(in); got != "" {
			t.Errorf("AsString(%#v) = %q, quiero vacío", in, got)
		}
	}
}

// El mismo payload da lo mismo construido en proceso que tras pasar por JSON, que es
// para lo que existen las coerciones.
func TestCoerce_SurvivesJSONRoundTrip(t *testing.T) {
	native := map[string]any{"qty": 2, "price": 2.5, "sku": "CAF"}
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for name, payload := range map[string]map[string]any{"native": native, "round trip": decoded} {
		if modules.AsInt(payload["qty"]) != 2 || modules.AsFloat(payload["qty"]) != 2 {
			t.Errorf("%s: qty = %#v no se lee como 2", name, payload["qty"])
		}
		if modules.AsFloat(payload["price"]) != 2.5 || modules.AsString(payload["sku"]) != "CAF" {
			t.Errorf("%s: price o sku mal leídos: %v", name, payload)
		}
	}
}
