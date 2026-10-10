//go:build pendiente

package cart_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// state_vars_test.go — la forma de Vars["cart"] que state.go promete, vista por
// Module.Step: qué claves escribe, en qué orden, y con qué tolerancia lee.

// Step deja el estado como map[string]any (no un struct), con las claves del
// contrato y solo las que el carrito usó.
func TestState_IsStoredAsAPlainMapWithOnlyUsedKeys(t *testing.T) {
	m := cart.New()
	_, _, vars := drive(t, m, seededVars(), "1")
	raw, ok := vars[stateVarKey].(map[string]any)
	if !ok {
		t.Fatalf("Vars[%q] es %T, quiero map[string]any", stateVarKey, vars[stateVarKey])
	}
	want := map[string]any{"level": "articles", "cat_code": "1", "started": true}
	if !reflect.DeepEqual(raw, want) {
		t.Errorf("estado = %v\nquiero   %v", raw, want)
	}

	// Con una línea: números como float64 (round-trip JSON) y sin customization.
	vars = walk(t, m, vars, "1", "2", "2")
	raw, _ = vars[stateVarKey].(map[string]any)
	want = map[string]any{
		"level": "continue", "cat_code": "1", "sku": "CAFE", "started": true,
		"lines": []any{map[string]any{"sku": "CAFE", "label": "Café", "qty": float64(2), "unit_price": 2.5}},
	}
	if !reflect.DeepEqual(raw, want) {
		t.Errorf("estado = %v\nquiero   %v", raw, want)
	}
	for _, retired := range []string{"intake_id", "order_id"} {
		if _, present := raw[retired]; present {
			t.Errorf("el estado lleva la clave retirada %q", retired)
		}
	}
}

// El estado que Step escribe se relee igual tras pasar por JSON, que es lo que hace
// el almacén entre mensaje y mensaje.
func TestState_SurvivesTheJSONBRoundTrip(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "2") // continue con Café ×2
	b, err := json.Marshal(vars)
	if err != nil {
		t.Fatalf("Vars no serializables: %v", err)
	}
	var reloaded map[string]any
	if err := json.Unmarshal(b, &reloaded); err != nil {
		t.Fatalf("Vars ilegibles: %v", err)
	}
	st, outs, _ := drive(t, m, reloaded, "2") // finalizar
	if st.Level != cart.LevelSummary || len(st.Lines) != 1 || st.Lines[0] != (cartLine{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}) {
		t.Fatalf("estado = %+v, quiero el resumen con la línea intacta", st)
	}
	mustContain(t, outs, "Café x2  $5.00", "TOTAL  $5.00")
}

// Lectura tolerante: ausente, nil o ilegible es el arranque en categorías.
func TestState_AbsentOrUnreadableStartsAtCategories(t *testing.T) {
	cases := []struct {
		name  string
		value any
		set   bool
	}{
		{name: "absent"},
		{name: "nil", value: nil, set: true},
		{name: "string", value: "roto", set: true},
		{name: "number", value: 7, set: true},
		{name: "list", value: []any{"x"}, set: true},
		{name: "level of another type", value: map[string]any{"level": 3}, set: true},
		{name: "not serializable", value: func() {}, set: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := seededVars()
			if tc.set {
				vars[stateVarKey] = tc.value
			}
			st, _, _ := drive(t, cart.New(), vars, "2") // en L1, «2» es Postres
			want := cartState{Level: cart.LevelArticles, CatCode: "2", Started: true}
			if !reflect.DeepEqual(st, want) {
				t.Errorf("estado = %+v, quiero %+v", st, want)
			}
		})
	}
}

// Un estado legible con `level` vacío arranca en categorías conservando lo demás.
func TestState_EmptyLevelMeansCategoriesKeepingTheRest(t *testing.T) {
	vars := seededVars()
	vars[stateVarKey] = map[string]any{
		"started": true,
		"lines":   []any{map[string]any{"sku": "TE", "label": "Té", "qty": float64(1), "unit_price": 2.0}},
	}
	st, _, _ := drive(t, cart.New(), vars, "2")
	if st.Level != cart.LevelArticles || st.CatCode != "2" || len(st.Lines) != 1 || st.Lines[0].SKU != "TE" {
		t.Errorf("estado = %+v, quiero los artículos de Postres con la línea de Té", st)
	}
}

// El tipo nativo también se lee: un estado sembrado en proceso, sin pasar por JSON.
func TestState_ReadsNativeTypes(t *testing.T) {
	vars := seededVars()
	vars[stateVarKey] = map[string]any{
		"level": cart.LevelContinue, "cat_code": "1", "sku": "CAFE", "started": true,
		"lines": []map[string]any{{"sku": "CAFE", "label": "Café", "qty": 3, "unit_price": 2.5}},
	}
	st, outs, _ := drive(t, cart.New(), vars, "2") // finalizar
	if st.Level != cart.LevelSummary || len(st.Lines) != 1 || st.Lines[0].Qty != 3 {
		t.Fatalf("estado = %+v, quiero el resumen con Café ×3", st)
	}
	mustContain(t, outs, "Café x3  $7.50")
}

// Un estado guardado ANTES de que existieran las indicaciones, las variantes y el
// checklist carga sin rama especial y no gana claves.
func TestState_OldStateLoadsWithoutTheNewKeys(t *testing.T) {
	vars := seededVars()
	vars[stateVarKey] = rawFromJSON(t, `{"level":"summary","cat_code":"1","sku":"CAFE","started":true,
	  "lines":[{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5}]}`)
	_, outs, vars := drive(t, cart.New(), vars, "zzz") // reprompt: re-pinta el resumen
	mustNotContain(t, outs, "✏️ Para todo el pedido", "\n   ✏️ ")
	raw, _ := vars[stateVarKey].(map[string]any)
	for _, key := range []string{"note", "note_split", "variant_code", "buyer_idx", "reprompts", "reprompts_event_id", "page"} {
		if _, present := raw[key]; present {
			t.Errorf("el estado ganó la clave %q sin usarla: %v", key, raw)
		}
	}
	if line, _ := raw["lines"].([]any)[0].(map[string]any); line["customization"] != nil {
		t.Errorf("la línea ganó customization sin indicación: %v", line)
	}
}

// VarPageSize: nativo o float64 del round-trip; ausente, de otro tipo o <= 0 cae al
// tamaño del Module.
func TestVarPageSize_ToleratesTypesAndFallsBack(t *testing.T) {
	const paged = "🛒 Elige una categoría:\n1) Bebidas\n3) Más ▾"
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "int", value: 1, want: paged},
		{name: "int64", value: int64(1), want: paged},
		{name: "float64", value: float64(1), want: paged},
		{name: "float64 truncated", value: 1.9, want: paged},
		{name: "zero", value: 0, want: categoriesScreen},
		{name: "negative", value: -2, want: categoriesScreen},
		{name: "negative float", value: -1.0, want: categoriesScreen},
		{name: "float below one", value: 0.5, want: categoriesScreen},
		{name: "string", value: "1", want: categoriesScreen},
		{name: "nil", value: nil, want: categoriesScreen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := seededVars()
			vars[cart.VarPageSize] = tc.value
			_, outs, _ := drive(t, cart.New(), vars, "zzz") // inválido: re-pinta L1
			mustScreen(t, outs, invalidPrefix+tc.want)
		})
	}
	_, outs, _ := drive(t, cart.New(), seededVars(), "zzz")
	mustScreen(t, outs, invalidPrefix+categoriesScreen)
}
