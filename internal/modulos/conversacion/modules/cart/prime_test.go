package cart_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// prime_test.go — la capacidad modules.Primer del carrito (Plan 029 · T8): «el LLM
// extrae, el código resuelve».

const continueBebidas = "Añadido al pedido ✅\n1) Agregar más de Bebidas\n2) Finalizar pedido\n" +
	"3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver"

// intentVars son las Vars que siembra el runtime al arrancar por decisión kind='llm'.
func intentVars(params map[string]string) map[string]any {
	return map[string]any{
		modules.VarIntentParams: params,
		modules.VarIntentName:   "pedido",
		"otra":                  "se conserva",
	}
}

func v1Content() model.Content { return model.Content{Raw: catalogRaw()} }

// assertConsumed exige que Result.Vars sea una copia sin la señal de intención y que
// el mapa de entrada siga intacto.
func assertConsumed(t *testing.T, in map[string]any, res modules.Result) {
	t.Helper()
	for _, key := range []string{modules.VarIntentParams, modules.VarIntentName} {
		if _, left := res.Vars[key]; left {
			t.Errorf("Result.Vars conserva %q: los params se consumen UNA vez", key)
		}
		if _, kept := in[key]; !kept {
			t.Errorf("Prime mutó el mapa de entrada: falta %q", key)
		}
	}
	if res.Vars["otra"] != "se conserva" {
		t.Errorf("Result.Vars perdió las demás claves: %v", res.Vars)
	}
	if _, stored := in[stateVarKey]; stored {
		t.Error("Prime escribió el estado en el mapa de entrada")
	}
}

// Match claro: pre-agrega la línea, salta a la confirmación de ítem y declara
// cart_started e item_added, en ese orden.
func TestPrime_ClearMatchPreAddsAndConfirms(t *testing.T) {
	in := intentVars(map[string]string{"producto": "cafés", "cantidad": "2", "ignorada": "x"})
	res, handled := cart.New().Prime(model.Node{}, v1Content(), in)
	if !handled {
		t.Fatal("handled = false, quiero true")
	}
	assertConsumed(t, in, res)
	mustScreen(t, res.Outputs, "Agregué 2 × Café ($2.50 c/u) a tu pedido.\n\n"+continueBebidas)

	want := cartState{Level: cart.LevelContinue, CatCode: "1", SKU: "CAFE", Started: true,
		Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}}
	if st := stateOf(t, res.Vars); !reflect.DeepEqual(st, want) {
		t.Errorf("estado = %+v\nquiero   %+v", st, want)
	}
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartStarted, cart.EffectItemAdded}) {
		t.Fatalf("efectos = %v, quiero cart_started e item_added, en ese orden", got)
	}
	if started := res.Effects[0]; started.Kind != kindEvent || started.Payload == nil || len(started.Payload) != 0 {
		t.Errorf("cart_started = %+v, quiero Kind event y payload {}", started)
	}
	added := res.Effects[1]
	if got := jsonOf(t, added.PublicPayload()); got != `{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5}` {
		t.Errorf("item_added público = %s", got)
	}
	if !reflect.DeepEqual(added.PrivateKeys, []string{"items"}) ||
		jsonOf(t, added.Payload["items"]) != `[{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5}]` {
		t.Errorf("item_added = %+v, quiero la foto privada de la línea", added)
	}
	if res.Next != nil || res.Query != nil {
		t.Errorf("Result = %+v: Prime no transiciona ni pregunta", res)
	}

	// El Step siguiente NO repite cart_started: el estado ya va marcado.
	res.Vars[modules.VarContentRaw] = catalogRaw()
	_, effs, _ := driveEffects(t, cart.New(), res.Vars, "2")
	if len(effs) != 0 {
		t.Errorf("efectos del Step siguiente = %v, quiero ninguno", effectNames(effs))
	}
}

// El artículo puede estar en cualquier categoría: el foco queda en la suya.
func TestPrime_MatchAcrossCategoriesSetsFocus(t *testing.T) {
	res, handled := cart.New().Prime(model.Node{}, v1Content(), intentVars(map[string]string{"producto": "un FLAN casero"}))
	if !handled {
		t.Fatal("handled = false, quiero true")
	}
	st := stateOf(t, res.Vars)
	if st.CatCode != "2" || st.SKU != "FLAN" || len(st.Lines) != 1 || st.Lines[0].Qty != 1 {
		t.Errorf("estado = %+v, quiero el Flan ×1 con el foco en Postres", st)
	}
	mustContain(t, res.Outputs, "Agregué 1 × Flan ($3.00 c/u) a tu pedido.", "1) Agregar más de Postres")
}

// La cantidad: entero >= 1 con espacios tolerados; ausente, ilegible o < 1 ⇒ 1.
func TestPrime_Quantity(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   int
	}{
		{name: "absent", params: map[string]string{"producto": "flan"}, want: 1},
		{name: "empty", params: map[string]string{"producto": "flan", "cantidad": ""}, want: 1},
		{name: "garbage", params: map[string]string{"producto": "flan", "cantidad": "abc"}, want: 1},
		{name: "word", params: map[string]string{"producto": "flan", "cantidad": "dos"}, want: 1},
		{name: "zero", params: map[string]string{"producto": "flan", "cantidad": "0"}, want: 1},
		{name: "negative", params: map[string]string{"producto": "flan", "cantidad": "-2"}, want: 1},
		{name: "decimal", params: map[string]string{"producto": "flan", "cantidad": "2.5"}, want: 1},
		{name: "non ASCII digits", params: map[string]string{"producto": "flan", "cantidad": "٣"}, want: 1},
		{name: "padded", params: map[string]string{"producto": "flan", "cantidad": " 3 "}, want: 3},
		{name: "plain", params: map[string]string{"producto": "flan", "cantidad": "12"}, want: 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, handled := cart.New().Prime(model.Node{}, v1Content(), intentVars(tc.params))
			st := stateOf(t, res.Vars)
			if !handled || len(st.Lines) != 1 || st.Lines[0].Qty != tc.want {
				t.Errorf("handled = %v, líneas = %+v; quiero una línea de %d", handled, st.Lines, tc.want)
			}
		})
	}
}

// Sin producto, sin match o ambiguo: arranque normal desde categorías, consumiendo
// los params, sin estado y sin efectos. Nunca se inventa nada.
func TestPrime_NoClearMatchFallsBackToNormalStart(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
	}{
		{name: "no producto", params: map[string]string{"cantidad": "2"}},
		{name: "blank producto", params: map[string]string{"producto": "   "}},
		{name: "no match", params: map[string]string{"producto": "pizza napolitana"}},
		{name: "ambiguous between two articles", params: map[string]string{"producto": "cafe te"}},
		{name: "short word needs equality", params: map[string]string{"producto": "tes"}},
		{name: "only punctuation", params: map[string]string{"producto": "¿?"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := intentVars(tc.params)
			res, handled := cart.New().Prime(model.Node{}, v1Content(), in)
			if !handled {
				t.Fatal("handled = false, quiero true: los params se consumen igual")
			}
			assertConsumed(t, in, res)
			mustScreen(t, res.Outputs, categoriesScreen)
			if len(res.Effects) != 0 {
				t.Errorf("efectos = %v, quiero ninguno: cart_started lo declara el primer Step", effectNames(res.Effects))
			}
			if _, stored := res.Vars[stateVarKey]; stored {
				t.Errorf("Vars = %v, el arranque normal no guarda estado", res.Vars)
			}
		})
	}

	// El arranque normal usa el tamaño de página del Module, no el sembrado en Vars.
	in := intentVars(map[string]string{"producto": "pizza"})
	in[cart.VarPageSize] = 5
	res, _ := cart.New(cart.WithPageSize(1)).Prime(model.Node{}, v1Content(), in)
	mustScreen(t, res.Outputs, "🛒 Elige una categoría:\n1) Bebidas\n3) Más ▾")
}

// Sin intent_params no hay nada que hacer: handled=false y el Result cero.
func TestPrime_WithoutIntentParamsIsNotHandled(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]any
	}{
		{name: "nil vars", vars: nil},
		{name: "absent", vars: map[string]any{modules.VarIntentName: "pedido"}},
		{name: "nil", vars: map[string]any{modules.VarIntentParams: nil}},
		{name: "empty native map", vars: map[string]any{modules.VarIntentParams: map[string]string{}}},
		{name: "empty JSONB map", vars: map[string]any{modules.VarIntentParams: map[string]any{}}},
		{name: "JSONB map without strings", vars: map[string]any{modules.VarIntentParams: map[string]any{"producto": 7, "cantidad": 2.0}}},
		{name: "another shape", vars: map[string]any{modules.VarIntentParams: "producto=cafe"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, handled := cart.New().Prime(model.Node{}, v1Content(), tc.vars)
			if handled || !reflect.DeepEqual(res, modules.Result{}) {
				t.Errorf("Prime = %+v, %v; quiero el Result cero y handled=false", res, handled)
			}
		})
	}
}

// Con el catálogo no disponible tampoco maneja: el Render normal dirá que no está.
func TestPrime_CatalogUnavailableIsNotHandled(t *testing.T) {
	in := intentVars(map[string]string{"producto": "café"})
	res, handled := cart.New().Prime(model.Node{}, model.Content{}, in)
	if handled || !reflect.DeepEqual(res, modules.Result{}) {
		t.Errorf("Prime = %+v, %v; quiero el Result cero y handled=false", res, handled)
	}
	if _, kept := in[modules.VarIntentParams]; !kept {
		t.Error("Prime tocó las Vars de entrada sin manejar")
	}
}

// Los params también llegan tras el round-trip JSONB (map[string]any): solo cuentan
// los valores string.
func TestPrime_ReadsParamsAfterJSONBRoundTrip(t *testing.T) {
	vars := map[string]any{modules.VarIntentParams: map[string]any{"producto": "CAFÉ", "cantidad": "2", "confianza": 0.9}}
	res, handled := cart.New().Prime(model.Node{}, v1Content(), vars)
	st := stateOf(t, res.Vars)
	if !handled || len(st.Lines) != 1 || st.Lines[0] != (cartLine{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}) {
		t.Errorf("handled = %v, líneas = %+v; quiero Café ×2", handled, st.Lines)
	}
}

// Catálogo v2: la variante nombrada se pre-agrega con SU precio; el artículo claro sin
// variante clara se PREGUNTA; el combo es una línea.
func TestPrime_VariantsAndCombo(t *testing.T) { //nolint:gocyclo // una aserción por promesa del contrato, en secuencia; partirlo no lo aclara
	content := contentOf(v2Vars(t))
	const continueTortas = "Añadido al pedido ✅\n1) Agregar más de Tortas\n2) Finalizar pedido\n" +
		"3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver"

	t.Run("clear variant", func(t *testing.T) {
		res, handled := cart.New().Prime(model.Node{}, content,
			intentVars(map[string]string{"producto": "torta de chocolate de 25-30 porciones", "cantidad": "1"}))
		if !handled {
			t.Fatal("handled = false, quiero true")
		}
		want := cartLine{SKU: "TORTA-CHOC#V2", Label: "Torta de chocolate — 25-30 porciones", Qty: 1, UnitPrice: 32000}
		st := stateOf(t, res.Vars)
		if st.Level != cart.LevelContinue || st.CatCode != "01" || st.SKU != "TORTA-CHOC" || len(st.Lines) != 1 || st.Lines[0] != want {
			t.Errorf("estado = %+v, quiero continue con la línea %+v", st, want)
		}
		mustScreen(t, res.Outputs,
			"Agregué 1 × Torta de chocolate — 25-30 porciones ($32000.00 c/u) a tu pedido.\n\n"+continueTortas)
	})

	t.Run("clear article, unclear variant asks", func(t *testing.T) {
		for _, producto := range []string{"una torta de chocolate", "torta de chocolate de porciones"} {
			in := intentVars(map[string]string{"producto": producto, "cantidad": "2"})
			res, handled := cart.New().Prime(model.Node{}, content, in)
			if !handled {
				t.Fatal("handled = false, quiero true")
			}
			assertConsumed(t, in, res)
			want := cartState{Level: cart.LevelVariant, CatCode: "01", SKU: "TORTA-CHOC"}
			if st := stateOf(t, res.Vars); !reflect.DeepEqual(st, want) {
				t.Errorf("%q: estado = %+v, quiero %+v (sin línea y sin started)", producto, st, want)
			}
			mustScreen(t, res.Outputs, variantsScreen)
			if len(res.Effects) != 0 {
				t.Errorf("%q: efectos = %v, quiero ninguno", producto, effectNames(res.Effects))
			}
		}
	})

	t.Run("combo by name", func(t *testing.T) {
		res, handled := cart.New().Prime(model.Node{}, content, intentVars(map[string]string{"producto": "combo hamburguesa"}))
		st := stateOf(t, res.Vars)
		want := []cartLine{{SKU: "COMBO-1", Label: "Combo hamburguesa", Qty: 1, UnitPrice: 9500}}
		if !handled || !reflect.DeepEqual(st.Lines, want) {
			t.Errorf("handled = %v, líneas = %+v; quiero %+v", handled, st.Lines, want)
		}
	})

	t.Run("article without variants as always", func(t *testing.T) {
		res, handled := cart.New().Prime(model.Node{}, content, intentVars(map[string]string{"producto": "café"}))
		st := stateOf(t, res.Vars)
		if !handled || st.CatCode != "02" || len(st.Lines) != 1 || st.Lines[0].SKU != "CAFE" {
			t.Errorf("handled = %v, estado = %+v; quiero el Café de Bebidas", handled, st)
		}
	})
}

// WithLogger: Prime, el otro camino de entrada al nodo, también avisa de los campos
// descartados (viene de cart_test.go: usa su logCapture y su brokenCatalog).
func TestWithLogger_PrimeWarnsAboutDiscardedFields(t *testing.T) {
	capture := &logCapture{}
	m := cart.New(cart.WithLogger(capture))
	vars := map[string]any{modules.VarIntentParams: map[string]string{"producto": "alfajor"}}
	if _, handled := m.Prime(model.Node{}, model.Content{Raw: rawFromJSON(t, brokenCatalog)}, vars); !handled {
		t.Fatal("handled = false, quiero true: el alfajor casa")
	}
	if len(capture.warns) != 1 || !strings.Contains(capture.warns[0], "tags") {
		t.Errorf("avisos = %q, quiero uno sobre tags", capture.warns)
	}
}
