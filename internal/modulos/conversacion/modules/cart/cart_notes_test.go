//go:build pendiente

package cart_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// cart_notes_test.go — las indicaciones del cliente (Plan 041 · T4.1c, D-041.19 y
// D-041.20), vistas por Module.Step. cart_notes.go no exporta nada.

const (
	noteRules = "\nEs una instrucción para quien lo prepara y NO cambia el precio." +
		"\nNo escribas aquí datos personales, direcciones ni datos de pago." +
		"\nMáx. 280 caracteres."
	noteBack        = "\n0) ← Volver sin indicación"
	itemNoteScreen  = `✏️ Escribe la indicación para "Café".` + noteRules + noteBack
	splitNoteScreen = `✏️ Escribe la indicación para 1 de las 2 "Café".` + noteRules + noteBack
	scopeScreen     = `✏️ Indicación para "Café" (pediste 2):` +
		"\n1) Para las 2\n2) Solo para 1 (la separo en dos líneas)" + noteBack
	orderNoteScreen = "✏️ Escribe una indicación para todo el pedido." + noteRules + noteBack
)

// coffees deja el carrito en L5 con una línea de Café ×qty.
func coffees(t *testing.T, m cart.Module, qty string) map[string]any {
	t.Helper()
	return walk(t, m, seededVars(), "1", "1", "2", qty)
}

// INV-15: quien no pulsa «3» teclea lo mismo que siempre y su estado no gana ni una
// clave de indicación.
func TestNotes_BuyingWithoutNotesTakesTheSameKeystrokes(t *testing.T) {
	for _, keys := range [][]string{{"1", "1", "2", "1", "2", "1"}, {"1", "1", "2", "2", "2", "1"}} {
		m := cart.New()
		vars := seededVars()
		for _, k := range keys {
			res := turn(m, model.Conversation{Vars: vars}, k, nil)
			raw, _ := res.Vars[stateVarKey].(map[string]any) //nolint:errcheck // aserción de tipo sobre un valor que el propio test construyó: si no casa, el test falla (o entra en pánico) igual
			for _, key := range []string{"note", "note_split"} {
				if _, present := raw[key]; present {
					t.Fatalf("tras %q el estado lleva %q sin haber pulsado 3: %v", k, key, raw)
				}
			}
			vars = res.Vars
		}
		if st := stateOf(t, vars); st.Level != cart.LevelClosed {
			t.Fatalf("teclas %v: estado = %+v, quiero el pedido cerrado con las seis de siempre", keys, st)
		}
	}
}

// Con UNA unidad, «3» va directo al texto; con más, pregunta antes el alcance.
func TestNotes_ThreeFromContinue(t *testing.T) {
	m := cart.New()
	st, outs, _ := drive(t, m, coffees(t, m, "1"), "3")
	if st.Level != cart.LevelItemNote || st.NoteSplit {
		t.Fatalf("estado = %+v, quiero item_note sin split", st)
	}
	mustScreen(t, outs, itemNoteScreen)

	st, outs, vars := drive(t, m, coffees(t, m, "2"), "3")
	if st.Level != cart.LevelItemNoteScope {
		t.Fatalf("estado = %+v, quiero item_note_scope", st)
	}
	mustScreen(t, outs, scopeScreen)

	st, outs, _ = drive(t, m, vars, "1") // para las 2
	if st.Level != cart.LevelItemNote || st.NoteSplit {
		t.Fatalf("«para las 2»: estado = %+v, quiero item_note sin split", st)
	}
	mustScreen(t, outs, itemNoteScreen)

	st, outs, _ = drive(t, m, vars, "2") // solo para 1
	if st.Level != cart.LevelItemNote || !st.NoteSplit || len(st.Lines) != 1 {
		t.Fatalf("«solo para 1»: estado = %+v, quiero item_note con split y la línea AÚN sin partir", st)
	}
	mustScreen(t, outs, splitNoteScreen)

	_, outs, _ = drive(t, m, vars, "7") // inválido en el alcance
	mustScreen(t, outs, invalidPrefix+scopeScreen)
}

// El ejemplo canónico de D-041.20: dos unidades, una comentada, más la nota del
// pedido. La línea se parte al GUARDAR, el resto va primero y el total no se mueve.
func TestNotes_CanonicalExampleSplitPlusOrderNote(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, coffees(t, m, "2"), "3", "2")
	st, outs, vars := drive(t, m, vars, "sin azúcar")
	wantLines := []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5},
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5, Customization: "sin azúcar"},
	}
	if st.Level != cart.LevelContinue || st.NoteSplit || !reflect.DeepEqual(st.Lines, wantLines) {
		t.Fatalf("estado = %+v\nquiero continue, sin split pendiente, con las líneas %+v", st, wantLines)
	}
	mustScreen(t, outs, `Anotado para 1 de las 2: "sin azúcar" ✅`+"\n"+continueBebidas)

	_, outs, vars = drive(t, m, vars, "2") // resumen: la indicación va bajo SU línea
	const summary = "🧾 Resumen del pedido:\nCafé x1  $2.50\nCafé x1  $2.50\n   ✏️ sin azúcar\nTOTAL  $5.00"
	mustContain(t, outs, summary+"\n1) Confirmar y finalizar")

	st, outs, _ = drive(t, m, walk(t, m, vars, "3"), "Dejarlo en portería")
	if st.Level != cart.LevelSummary || st.Note != "Dejarlo en portería" {
		t.Fatalf("estado = %+v, quiero el resumen con la nota del pedido", st)
	}
	mustScreen(t, outs, `Anotado para todo el pedido: "Dejarlo en portería" ✅`+"\n"+summary+
		"\n✏️ Para todo el pedido: Dejarlo en portería"+
		"\n1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido")
}

// «Para las N» y la línea de una unidad: una sola línea con su indicación, sin partir.
func TestNotes_WholeLineNote(t *testing.T) {
	m := cart.New()
	st, outs, _ := drive(t, m, walk(t, m, coffees(t, m, "2"), "3", "1"), "bien caliente")
	want := []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "bien caliente"}}
	if st.Level != cart.LevelContinue || !reflect.DeepEqual(st.Lines, want) {
		t.Fatalf("estado = %+v, quiero continue con %+v", st, want)
	}
	mustScreen(t, outs, `Anotado: "bien caliente" ✅`+"\n"+continueBebidas)
}

// D-044.18: al re-partir, el resto CONSERVA la indicación que la línea ya tenía.
func TestNotes_PreviousNoteSurvivesTheSplit(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, coffees(t, m, "3"), "3", "1", "con canela") // las 3 con canela
	st, _, _ := drive(t, m, walk(t, m, vars, "3", "2"), "sin azúcar")
	want := []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "con canela"},
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5, Customization: "sin azúcar"},
	}
	if !reflect.DeepEqual(st.Lines, want) {
		t.Errorf("líneas = %+v\nquiero   %+v", st.Lines, want)
	}
}

// La línea comentada es SIEMPRE la última, también con dos líneas del mismo producto.
func TestNotes_TheCommentedLineIsAlwaysTheLast(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, coffees(t, m, "1"), "1", "1", "2", "1") // segunda línea de Café ×1
	st, _, _ := drive(t, m, walk(t, m, vars, "3"), "descafeinado")
	want := []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5},
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5, Customization: "descafeinado"},
	}
	if !reflect.DeepEqual(st.Lines, want) {
		t.Errorf("líneas = %+v\nquiero   %+v", st.Lines, want)
	}
}

// Las tres salidas por «0» dejan el carrito como estaba y sin efectos; el split
// pendiente muere con la salida.
func TestNotes_ZeroLeavesTheCartUntouched(t *testing.T) {
	m := cart.New()
	base := coffees(t, m, "2")
	wantLines := stateOf(t, base).Lines
	cases := []struct {
		name   string
		path   []string
		level  string
		screen string
	}{
		{name: "from scope", path: []string{"3"}, level: cart.LevelContinue, screen: continueBebidas},
		{name: "from split text", path: []string{"3", "2"}, level: cart.LevelContinue, screen: continueBebidas},
		{name: "from order text", path: []string{"2", "3"}, level: cart.LevelSummary,
			screen: "🧾 Resumen del pedido:\nCafé x2  $5.00\nTOTAL  $5.00\n1) Confirmar y finalizar\n" +
				"2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := turn(m, model.Conversation{Vars: walk(t, m, base, tc.path...)}, "0", nil)
			mustScreen(t, res.Outputs, tc.screen)
			st := stateOf(t, res.Vars)
			if st.Level != tc.level || st.NoteSplit || st.Note != "" || !reflect.DeepEqual(st.Lines, wantLines) {
				t.Errorf("estado = %+v, quiero %s con el carrito intacto", st, tc.level)
			}
			if len(res.Effects) != 0 {
				t.Errorf("efectos = %v, quiero ninguno", effectNames(res.Effects))
			}
		})
	}
}

// Vacío tras sanear equivale a «0»: sin indicación y sin error (REQ-33e).
func TestNotes_EmptyAfterSanitizingMeansZero(t *testing.T) {
	m := cart.New()
	for _, in := range []string{"", "   ", "​", "\n \t"} { //nolint:staticcheck // ST1018: el invisible va literal a propósito, es la entrada que se prueba
		res := turn(m, model.Conversation{Vars: walk(t, m, coffees(t, m, "2"), "3", "2")}, in, nil)
		mustScreen(t, res.Outputs, continueBebidas)
		if st := stateOf(t, res.Vars); st.Level != cart.LevelContinue || st.NoteSplit || len(st.Lines) != 1 || st.Lines[0].Customization != "" {
			t.Errorf("%q: estado = %+v, quiero continue con la línea sin partir ni anotar", in, st)
		}
		if len(res.Effects) != 0 {
			t.Errorf("%q: efectos = %v, quiero ninguno", in, effectNames(res.Effects))
		}

		res = turn(m, model.Conversation{Vars: walk(t, m, coffees(t, m, "1"), "2", "3")}, in, nil)
		if st := stateOf(t, res.Vars); st.Level != cart.LevelSummary || st.Note != "" || len(res.Effects) != 0 {
			t.Errorf("%q en la nota de pedido: estado = %+v, efectos %v; quiero el resumen sin nota", in, st, effectNames(res.Effects))
		}
	}
}

// Demasiado largo: se RECHAZA en el mismo paso con el número real y no se trunca; el
// carrito no cambia, ni la línea se parte. El largo se mide DESPUÉS de sanear.
func TestNotes_TooLongIsRejectedInTheSameStep(t *testing.T) {
	m := cart.New()
	long := strings.Repeat("á", 281)
	const tooLong = "Esa indicación es muy larga (281 de 280 caracteres). Escríbela más corta.\n\n"

	res := turn(m, model.Conversation{Vars: walk(t, m, coffees(t, m, "2"), "3", "2")}, long, nil)
	mustScreen(t, res.Outputs, tooLong+splitNoteScreen)
	if st := stateOf(t, res.Vars); st.Level != cart.LevelItemNote || !st.NoteSplit || len(st.Lines) != 1 || st.Lines[0].Qty != 2 {
		t.Errorf("estado = %+v, quiero el mismo paso con la línea sin partir", st)
	}
	if len(res.Effects) != 0 {
		t.Errorf("efectos = %v, quiero ninguno", effectNames(res.Effects))
	}

	res = turn(m, model.Conversation{Vars: walk(t, m, coffees(t, m, "1"), "2", "3")}, long, nil)
	mustScreen(t, res.Outputs, tooLong+orderNoteScreen)
	if st := stateOf(t, res.Vars); st.Level != cart.LevelOrderNote || st.Note != "" {
		t.Errorf("estado = %+v, quiero el mismo paso sin nota", st)
	}

	// 280 runas con un muro de invisibles delante: cabe, porque se mide saneado.
	padded := strings.Repeat("\u200b", 300) + strings.Repeat("á", 280)
	st, _, _ := drive(t, m, walk(t, m, coffees(t, m, "1"), "3"), padded)
	if st.Level != cart.LevelContinue || st.Lines[0].Customization != strings.Repeat("á", 280) {
		t.Errorf("280 runas tras sanear: estado = %+v, quiero la indicación guardada entera", st)
	}
}

// Con una indicación ya escrita, la pantalla la enseña; «0» la deja y otra la reemplaza.
func TestNotes_ReplacingAnExistingNote(t *testing.T) {
	m := cart.New()
	noted := walk(t, m, coffees(t, m, "1"), "3", "sin azúcar")
	st, outs, again := drive(t, m, noted, "3")
	if st.Level != cart.LevelItemNote {
		t.Fatalf("estado = %+v, quiero item_note", st)
	}
	mustScreen(t, outs, `Indicación actual: "sin azúcar" — escribe otra para reemplazarla.`+"\n"+itemNoteScreen)

	st, _, _ = drive(t, m, again, "0")
	if st.Lines[0].Customization != "sin azúcar" {
		t.Errorf("«0»: indicación = %q, quiero que siga la que había", st.Lines[0].Customization)
	}
	st, _, _ = drive(t, m, again, "con stevia")
	if st.Lines[0].Customization != "con stevia" {
		t.Errorf("otra: indicación = %q, quiero la nueva", st.Lines[0].Customization)
	}

	// Lo mismo con la nota del pedido.
	withNote := walk(t, m, noted, "2", "3", "en portería")
	_, outs, again = drive(t, m, withNote, "3")
	mustScreen(t, outs, `Indicación actual: "en portería" — escribe otra para reemplazarla.`+"\n"+orderNoteScreen)
	if st, _, _ = drive(t, m, again, "0"); st.Note != "en portería" {
		t.Errorf("«0»: nota = %q, quiero que siga la que había", st.Note)
	}
	if st, _, _ = drive(t, m, again, "timbre 4B"); st.Note != "timbre 4B" {
		t.Errorf("otra: nota = %q, quiero la nueva", st.Note)
	}
}

// La tecla 3 SOLO es indicación en L5 y L6: en los demás niveles sigue siendo lo que
// era, y un texto que parece una orden en un nivel de nota es solo texto.
func TestNotes_ThreeIsOnlyANoteKeyInContinueAndSummary(t *testing.T) {
	m := cart.New()
	for _, path := range [][]string{nil, {"1"}, {"1", "1"}} {
		st, _, _ := drive(t, m, walk(t, m, seededVars(), path...), "3")
		for _, level := range []string{cart.LevelItemNote, cart.LevelItemNoteScope, cart.LevelOrderNote} {
			if st.Level == level {
				t.Errorf("camino %v: «3» abrió %s", path, level)
			}
		}
	}
	st, _, _ := drive(t, m, walk(t, m, seededVars(), "1", "1", "2"), "3") // en cantidad, 3 son tres
	if st.Level != cart.LevelContinue || st.Lines[0].Qty != 3 {
		t.Errorf("en cantidad: estado = %+v, quiero una línea de 3", st)
	}

	st, _, _ = drive(t, m, walk(t, m, coffees(t, m, "1"), "3"), "9")
	if st.Level != cart.LevelContinue || st.Lines[0].Customization != "9" {
		t.Errorf("«9» en el texto de la indicación: estado = %+v, quiero la indicación \"9\", no un pedido cancelado", st)
	}
}

// Estados que el recorrido no produce: sin líneas no hay nada que comentar.
func TestNotes_WithoutLinesThereIsNothingToAnnotate(t *testing.T) {
	m := cart.New()
	for _, level := range []string{cart.LevelItemNoteScope, cart.LevelItemNote} {
		vars := seededVars()
		seedState(t, vars, cartState{Level: level, CatCode: "1", Started: true, NoteSplit: true})
		res := turn(m, model.Conversation{Vars: vars}, "sin azúcar", nil)
		mustScreen(t, res.Outputs, continueBebidas)
		if st := stateOf(t, res.Vars); st.Level != cart.LevelContinue || st.NoteSplit || len(res.Effects) != 0 {
			t.Errorf("%s sin líneas: estado = %+v, efectos %v; quiero volver a continue sin nada", level, st, effectNames(res.Effects))
		}
	}
	vars := seededVars()
	seedState(t, vars, cartState{Level: cart.LevelContinue, CatCode: "1", Started: true})
	st, outs, _ := drive(t, m, vars, "3")
	if st.Level != cart.LevelContinue {
		t.Fatalf("estado = %+v, quiero seguir en continue", st)
	}
	mustScreen(t, outs, invalidPrefix+continueBebidas)
}
