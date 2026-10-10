package cart_test

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// troceo_test.go — «Go descompone → el LLM decide chiquito → Go recompone» (Plan 044 ·
// Ola 3.5 · T3.5-3), visto por Module.Step. troceo.go no exporta nada.

// chunkCatalog es un catálogo cuyas etiquetas se parecen a lo que el cliente escribe:
// cuatro categorías con un artículo cada una, del mismo nombre, más una torta con
// variantes (que el troceado no puede pre-agregar).
func chunkCatalog() map[string]any {
	one := func(code, label, sku string, price float64) map[string]any {
		return map[string]any{"code": code, "label": label, "items": []any{
			map[string]any{"code": "1", "sku": sku, "label": label, "price": price},
		}}
	}
	cakes := map[string]any{"code": "5", "label": "Tortas", "items": []any{
		map[string]any{"code": "1", "sku": "TORTA", "label": "Tortas", "price": 9.0, "variants": []any{
			map[string]any{"code": "V1", "label": "Chica", "price": 9.0},
			map[string]any{"code": "V2", "label": "Grande", "price": 15.0},
		}},
	}}
	return map[string]any{"categories": []any{
		one("1", "Pizzas", "PIZZA", 10), one("2", "Hamburguesas", "HAMB", 6),
		one("3", "Empanadas", "EMPA", 2), one("4", "Jugos", "JUGO", 3), cakes,
	}}
}

func chunkVars() map[string]any { return map[string]any{modules.VarContentRaw: chunkCatalog()} }

// order reduce las líneas a pares «SKU×cantidad», en orden.
func order(st cartState) []string {
	out := make([]string, 0, len(st.Lines))
	for _, l := range st.Lines {
		out = append(out, l.SKU+"×"+strconv.Itoa(l.Qty))
	}
	return out
}

const continuePizzas = "Añadido al pedido ✅\n1) Agregar más de Pizzas\n2) Finalizar pedido\n" +
	"3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver"

// El fixture medido el 2026-08-17: un lote agrupado con ruido de verdad. Los dos
// productos entran con sus cantidades y NI UNA llamada al modelo.
func TestChunking_Fixture_TwoProductsWithTheirQuantities(t *testing.T) {
	s := &spy{}
	m := cart.New(cart.WithMatchHook(s.record))
	const input = "hola\nquiero 2 pizzas\ny una hamburguesa\npara llevar"

	if q := m.Step(model.Node{}, model.Conversation{Vars: chunkVars()}, input).Query; q != nil {
		t.Fatalf("Query = %+v: la cascada resolvía todo, no había nada que preguntar", *q)
	}
	s.seen = nil
	res := turn(m, model.Conversation{Vars: chunkVars()}, input, nil)
	st := stateOf(t, res.Vars)
	if got := order(st); !reflect.DeepEqual(got, []string{"PIZZA×2", "HAMB×1"}) {
		t.Fatalf("pedido = %v, quiero [PIZZA×2 HAMB×1]", got)
	}
	// El foco queda en la ÚLTIMA línea agregada.
	want := cartState{Level: cart.LevelContinue, CatCode: "2", SKU: "HAMB", Started: true, Lines: st.Lines}
	if !reflect.DeepEqual(st, want) {
		t.Errorf("estado = %+v\nquiero   %+v", st, want)
	}
	mustScreen(t, res.Outputs, "Agregué a tu pedido:\n• 2 × Pizzas ($10.00 c/u)\n• 1 × Hamburguesas ($6.00 c/u)\n\n"+
		"Añadido al pedido ✅\n1) Agregar más de Hamburguesas\n2) Finalizar pedido\n"+
		"3) ✏️ Indicación para este artículo\n9) Cancelar pedido\n0) ← Volver")
	if want := [][2]string{{"troceo", cart.LevelCategories}, {"troceo", cart.LevelCategories}}; !reflect.DeepEqual(s.seen, want) {
		t.Errorf("observador = %v, quiero %v: dos trozos contados, ninguno perdido", s.seen, want)
	}
	if res.Next != nil {
		t.Error("el troceado no termina el flujo: Next debía ser nil")
	}
}

// Los efectos de la recomposición: cart_started y un item_added POR LÍNEA, cada uno con
// la foto del carrito HASTA esa línea.
func TestChunking_EffectsCarryACumulativeSnapshot(t *testing.T) {
	res := turn(cart.New(), model.Conversation{Vars: chunkVars()}, "2 pizzas y una hamburguesa", nil)
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartStarted, cart.EffectItemAdded, cart.EffectItemAdded}) {
		t.Fatalf("efectos = %v, quiero cart_started y dos item_added", got)
	}
	first, second := res.Effects[1], res.Effects[2]
	if got := jsonOf(t, first.PublicPayload()); got != `{"label":"Pizzas","qty":2,"sku":"PIZZA","unit_price":10}` {
		t.Errorf("primer item_added = %s", got)
	}
	if got := jsonOf(t, first.Payload["items"]); got != `[{"label":"Pizzas","qty":2,"sku":"PIZZA","unit_price":10}]` {
		t.Errorf("foto del primero = %s, quiero solo la primera línea", got)
	}
	wantSecond := `[{"label":"Pizzas","qty":2,"sku":"PIZZA","unit_price":10},{"label":"Hamburguesas","qty":1,"sku":"HAMB","unit_price":6}]`
	if got := jsonOf(t, second.Payload["items"]); got != wantSecond {
		t.Errorf("foto del segundo = %s\nquiero %s", got, wantSecond)
	}
	for _, e := range []modules.Effect{first, second} {
		if e.Kind != kindEvent || !reflect.DeepEqual(e.PrivateKeys, []string{"items"}) {
			t.Errorf("item_added = %+v, quiero Kind event con la foto privada", e)
		}
	}

	// Sobre un carrito ya empezado no repite cart_started y las líneas se SUMAN.
	vars := chunkVars()
	seedState(t, vars, cartState{Level: cart.LevelCategories, Started: true, Reprompts: 2, RepromptsEvent: "ev-1",
		Lines: []cartLine{{SKU: "JUGO", Label: "Jugos", Qty: 1, UnitPrice: 3}}})
	res = turn(cart.New(), model.Conversation{Vars: vars, EventID: "ev-1"}, "2 pizzas y una hamburguesa", nil)
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectItemAdded, cart.EffectItemAdded}) {
		t.Errorf("efectos = %v, quiero dos item_added sin cart_started", got)
	}
	st := stateOf(t, res.Vars)
	if got := order(st); !reflect.DeepEqual(got, []string{"JUGO×1", "PIZZA×2", "HAMB×1"}) {
		t.Errorf("pedido = %v, quiero las líneas nuevas detrás de la que había", got)
	}
	if st.Reprompts != 0 || st.RepromptsEvent != "" {
		t.Errorf("estado = %+v: el turno fue válido, el contador de inválidos debía reiniciarse", st)
	}
}

// El segundo fixture medido: en campo devolvía UNO y perdía tres.
func TestChunking_Fixture_FourOrdersZeroLoss(t *testing.T) {
	st, outs, _ := drive(t, cart.New(), chunkVars(), "7 pizzas, 2 hamburguesas, 9 empanadas y 4 jugos")
	if got := order(st); !reflect.DeepEqual(got, []string{"PIZZA×7", "HAMB×2", "EMPA×9", "JUGO×4"}) {
		t.Fatalf("pedido = %v, quiero los cuatro", got)
	}
	mustContain(t, outs, "• 7 × Pizzas ($10.00 c/u)", "• 2 × Hamburguesas ($6.00 c/u)", "• 9 × Empanadas ($2.00 c/u)", "• 4 × Jugos ($3.00 c/u)")
	mustNotContain(t, outs, "No pude identificar")
}

// El trozo con cantidad que la cascada NO casa sube como petición, con el catálogo
// ENTERO por posición; y la pasada que pide no muta nada.
func TestChunking_UnmatchedChunkRaisesAQueryWithChunks(t *testing.T) {
	vars := chunkVars()
	before := jsonOf(t, vars)
	res := cart.New().Step(model.Node{}, model.Conversation{Vars: vars}, "  2 pizzas, 3 napolitanas y 4 de las otras ")
	want := &modules.Query{
		Class: modules.QueryClassOption, Level: cart.LevelCategories,
		Text:   "2 pizzas, 3 napolitanas y 4 de las otras",
		Chunks: []string{"napolitanas", "de las otras"},
		Options: []modules.QueryOption{
			{Code: "0", Label: "Pizzas"}, {Code: "1", Label: "Hamburguesas"}, {Code: "2", Label: "Empanadas"},
			{Code: "3", Label: "Jugos"}, {Code: "4", Label: "Tortas"},
		},
	}
	if !reflect.DeepEqual(res.Query, want) {
		t.Fatalf("Query = %+v\nquiero  %+v", res.Query, want)
	}
	if len(res.Outputs) != 0 || len(res.Effects) != 0 || res.Next != nil || jsonOf(t, res.Vars) != before {
		t.Errorf("la pasada que pide produjo algo: %+v", res)
	}
}

// La segunda pasada: los códigos se vuelcan POR POSICIÓN sobre los trozos sin casar, y
// solo valen los de la lista que el módulo ofreció.
func TestChunking_VerdictPerChunkIsAppliedAndValidated(t *testing.T) {
	const input = "2 pizzas, 3 napolitanas y 4 de las otras"
	cases := []struct {
		name  string
		codes []string
		want  []string
		lost  int
	}{
		{"both resolved", []string{"2", "3"}, []string{"PIZZA×2", "EMPA×3", "JUGO×4"}, 0},
		{"second resolved only", []string{"", "1"}, []string{"PIZZA×2", "HAMB×4"}, 1},
		{"first resolved only", []string{"1"}, []string{"PIZZA×2", "HAMB×3"}, 1},
		{"invented position", []string{"99", "3"}, []string{"PIZZA×2", "JUGO×4"}, 1},
		{"negative position", []string{"-1", "3"}, []string{"PIZZA×2", "JUGO×4"}, 1},
		{"label instead of position", []string{"Empanadas", "Jugos"}, []string{"PIZZA×2"}, 2},
		{"no codes", nil, []string{"PIZZA×2"}, 2},
		{"more codes than chunks", []string{"2", "3", "1", "0"}, []string{"PIZZA×2", "EMPA×3", "JUGO×4"}, 0},
		{"article with variants stays out", []string{"4", "3"}, []string{"PIZZA×2", "JUGO×4"}, 1},
		{"only the single code", nil, []string{"PIZZA×2"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &spy{}
			m := cart.New(cart.WithMatchHook(s.record))
			v := modules.Verdict{Codes: tc.codes}
			if tc.name == "only the single code" {
				v = modules.Verdict{Code: "2"}
			}
			res := turn(m, model.Conversation{Vars: chunkVars()}, input, func(modules.Query) modules.Verdict { return v })
			if got := order(stateOf(t, res.Vars)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pedido = %v, quiero %v", got, tc.want)
			}
			switch tc.lost {
			case 0:
				mustNotContain(t, res.Outputs, "No pude identificar")
			case 1:
				mustContain(t, res.Outputs, "\n\nNo pude identificar 1 producto más de tu mensaje; podés agregarlo eligiendo del menú.\n\n")
			default:
				mustContain(t, res.Outputs, "\n\nNo pude identificar 2 productos más de tu mensaje; podés agregarlo eligiendo del menú.\n\n")
			}
			// 🔴 Nunca se le devuelve al cliente el texto que no se entendió.
			mustNotContain(t, res.Outputs, "napolitanas", "de las otras")
			lost := 0
			for _, c := range s.seen {
				if c[0] == "troceo_perdido" {
					lost++
				}
			}
			if lost != tc.lost || len(s.seen) != 3 {
				t.Errorf("observador = %v, quiero 3 trozos con %d perdidos", s.seen, tc.lost)
			}
		})
	}
}

// Sin resolutor, lo que la cascada casó ENTRA y lo demás se cuenta como perdido: no se
// tira el pedido entero por un trozo.
func TestChunking_DegradationKeepsWhatTheCascadeMatched(t *testing.T) {
	res := turn(cart.New(), model.Conversation{Vars: chunkVars()}, "2 pizzas y 3 napolitanas", nil)
	if got := order(stateOf(t, res.Vars)); !reflect.DeepEqual(got, []string{"PIZZA×2"}) {
		t.Fatalf("pedido = %v, quiero [PIZZA×2]", got)
	}
	mustScreen(t, res.Outputs, "Agregué a tu pedido:\n• 2 × Pizzas ($10.00 c/u)\n\n"+
		"No pude identificar 1 producto más de tu mensaje; podés agregarlo eligiendo del menú.\n\n"+continuePizzas)
}

// Si al final no entra NADA, el turno sigue por el camino de siempre.
func TestChunking_NothingAddedFallsBackToTheUsualPath(t *testing.T) {
	res := turn(cart.New(), model.Conversation{Vars: chunkVars()}, "2 napolitanas y 3 calzones", nil)
	st := stateOf(t, res.Vars)
	if st.Level != cart.LevelCategories || len(st.Lines) != 0 {
		t.Fatalf("estado = %+v, quiero seguir en categorías sin líneas", st)
	}
	mustContain(t, res.Outputs, invalidPrefix+"🛒 Elige una categoría:")
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartStarted}) {
		t.Errorf("efectos = %v, quiero cart_started una sola vez", got)
	}
}

// Cuándo NO se trocea: con una sola petición, fuera del nivel de categorías, con solo
// ruido, con solo números o con prosa larga.
func TestChunking_WhenItStepsAside(t *testing.T) {
	t.Run("a single request", func(t *testing.T) {
		st, _, _ := drive(t, cart.New(), chunkVars(), "quiero pizzas")
		if st.Level != cart.LevelArticles || st.CatCode != "1" || len(st.Lines) != 0 {
			t.Errorf("estado = %+v, quiero navegar a Pizzas sin agregar nada", st)
		}
	})
	t.Run("a request and noise", func(t *testing.T) {
		st, _, _ := drive(t, cart.New(), chunkVars(), "hola, quiero pizzas")
		if len(st.Lines) != 0 {
			t.Errorf("estado = %+v: con UNA petición no se trocea", st)
		}
	})
	t.Run("a request and something unnamed", func(t *testing.T) {
		// «algo de tomar» no trae cantidad ni casa nada: NO se pregunta por él.
		res := cart.New().Step(model.Node{}, model.Conversation{Vars: chunkVars()}, "quiero pizzas y algo de tomar")
		if res.Query != nil && len(res.Query.Chunks) != 0 {
			t.Errorf("Query = %+v: el segundo trozo subió al modelo", *res.Query)
		}
	})
	t.Run("outside categories", func(t *testing.T) {
		vars := chunkVars()
		seedState(t, vars, cartState{Level: cart.LevelArticles, CatCode: "1", Started: true})
		st, _, _ := drive(t, cart.New(), vars, "7 pizzas, 2 hamburguesas y 9 empanadas")
		if len(st.Lines) != 0 {
			t.Errorf("estado = %+v: se troceó fuera del nivel de categorías", st)
		}
	})
	t.Run("only noise", func(t *testing.T) {
		res := cart.New().Step(model.Node{}, model.Conversation{Vars: chunkVars()}, "hola, buenas tardes y gracias")
		if res.Query != nil && len(res.Query.Chunks) != 0 {
			t.Errorf("Query = %+v: el ruido subió al modelo como trozos", *res.Query)
		}
		if len(stateOf(t, turn(cart.New(), model.Conversation{Vars: chunkVars()}, "hola, buenas tardes y gracias", nil).Vars).Lines) != 0 {
			t.Error("el ruido acabó en el pedido")
		}
	})
	t.Run("bare quantities name nothing", func(t *testing.T) {
		st, _, _ := drive(t, cart.New(), chunkVars(), "2, 3 y un par")
		if len(st.Lines) != 0 || st.Level != cart.LevelCategories {
			t.Errorf("estado = %+v: unos números sueltos no son un pedido", st)
		}
	})
	t.Run("long prose", func(t *testing.T) {
		in := "2 pizzas y 3 hamburguesas " + "y luego ya veremos que mas pedimos porque todavia no lo tenemos nada claro la verdad"
		st, _, _ := drive(t, cart.New(), chunkVars(), in)
		if len(st.Lines) != 0 {
			t.Errorf("estado = %+v: por encima de 16 tokens no se trocea", st)
		}
	})
}

// El corpus del troceo y de la cantidad en Go: separadores, tabla de números, el
// compuesto «un par» y los casos adversarios.
func TestChunking_Corpus(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// — Separadores, del más fuerte al más débil.
		{"newline", "2 pizzas\n3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"comma", "2 pizzas, 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"semicolon", "2 pizzas; 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"plus", "2 pizzas + 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"conjunction y", "2 pizzas y 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"conjunction e", "2 jugos e 3 empanadas", []string{"JUGO×2", "EMPA×3"}},
		{"order is the client's", "4 jugos, 1 pizzas", []string{"JUGO×4", "PIZZA×1"}},
		{"y inside words does not split", "2 jugos y 3 empanadas y pizzas", []string{"JUGO×2", "EMPA×3", "PIZZA×1"}},
		// — La cantidad en Go.
		{"number words", "dos pizzas, tres jugos y diez empanadas", []string{"PIZZA×2", "JUGO×3", "EMPA×10"}},
		{"number words in capitals", "DOS pizzas, Una hamburguesa", []string{"PIZZA×2", "HAMB×1"}},
		{"dozen and pair", "una docena de empanadas y un par de jugos", []string{"EMPA×12", "JUGO×2"}},
		{"docena alone", "docena de empanadas, par de pizzas", []string{"EMPA×12", "PIZZA×2"}},
		{"no quantity means one", "pizzas y jugos", []string{"PIZZA×1", "JUGO×1"}},
		{"quantity after the product", "pizzas 3, jugos 2", []string{"PIZZA×3", "JUGO×2"}},
		{"first number wins", "2 empanadas de 3, 1 jugos", []string{"EMPA×2", "JUGO×1"}},
		{"zero is not a quantity", "0 pizzas, 2 jugos", []string{"PIZZA×1", "JUGO×2"}},
		{"overflow is not a quantity", "99999999999999999999 pizzas, 2 jugos", []string{"PIZZA×1", "JUGO×2"}},
		// — Adversarios: separadores repetidos, dígitos no ASCII, espacios Unicode.
		{"repeated separators", "2 pizzas,,;; 3 jugos\n\n\n", []string{"PIZZA×2", "JUGO×3"}},
		{"repeated conjunction", "2 pizzas y y 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"separators glued without spaces", "2 pizzas,3 jugos;1 empanadas+4 hamburguesas", []string{"PIZZA×2", "JUGO×3", "EMPA×1", "HAMB×4"}},
		{"at signs are not separators", "2 pizzas@@3 jugos", nil},
		{"conjunction without spaces is not a separator", "2 pizzasy3 jugos", nil},
		{"conjunction with no-break spaces is not a separator", "2 pizzas y 3 jugos", nil},
		{"no-break space inside a chunk", "2 pizzas, 3 jugos", []string{"PIZZA×2", "JUGO×3"}},
		{"arabic-indic digits are not a quantity", "٢ pizzas, ٣ jugos", []string{"PIZZA×1", "JUGO×1"}},
		{"fullwidth digits are not a quantity", "２ pizzas, ３ jugos", []string{"PIZZA×1", "JUGO×1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _, _ := drive(t, cart.New(), chunkVars(), tc.in)
			got := order(st)
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%q: pedido = %v, quiero %v", tc.in, got, tc.want)
			}
		})
	}
}
