//go:build pendiente

package cart_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// consulta_test.go — el TERCER escalón visto desde el módulo (Plan 044 · Ola 3.5 ·
// T3.5-2): cuándo el carrito pide ayuda, qué lleva la petición, qué acepta de vuelta
// y qué NO manda nunca a preguntar. consulta.go no exporta nada.
//
// Lo que aquí NO se prueba es el mecanismo: que el engine resuelva, siembre y vuelva
// a llamar UNA sola vez es suyo (engine/consulta_test.go). Aquí se imita en cuatro
// líneas (turn, en helpers_test.go).

// atQuantity deja el carrito en el nivel de la CANTIDAD del Café.
func atQuantity(t *testing.T, m cart.Module) map[string]any {
	t.Helper()
	return walk(t, m, seededVars(), "1", "1", "2")
}

// queryAt devuelve la petición que eleva la primera pasada desde un estado del
// catálogo de la cascada (nil si no pide).
func queryAt(t *testing.T, st cartState, input string) *modules.Query {
	t.Helper()
	vars := map[string]any{modules.VarContentRaw: cascadeCatalog(),
		cart.VarBuyerFields: []any{map[string]any{"key": "nombre", "label": "Nombre", "required": true}}}
	seedState(t, vars, st)
	return cart.New().Step(model.Node{}, model.Conversation{Vars: vars}, input).Query
}

// «mejor dos» en el nivel de la cantidad: la cascada no puede (es aritmética del
// lenguaje) y se eleva una consulta de CANTIDAD, sin catálogo.
func TestQuery_QuantityInWords(t *testing.T) {
	m := cart.New()
	vars := atQuantity(t, m)
	q := m.Step(model.Node{}, model.Conversation{Vars: vars}, "  mejor dos ").Query
	want := &modules.Query{Class: modules.QueryClassQuantity, Level: cart.LevelQuantity, Text: "mejor dos"}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("Query = %+v\nquiero  %+v", q, want)
	}

	// Con un resolutor que sabe, el pedido avanza con la cantidad correcta.
	res := turn(m, model.Conversation{Vars: vars}, "mejor dos", func(modules.Query) modules.Verdict {
		return modules.Verdict{Code: "2"}
	})
	if st := stateOf(t, res.Vars); st.Level != cart.LevelContinue || len(st.Lines) != 1 || st.Lines[0].Qty != 2 {
		t.Errorf("estado = %+v, quiero una línea de 2 unidades", st)
	}
	mustScreen(t, res.Outputs, continueBebidas)
}

// «finalizar» en el resumen es el ejemplo que la cascada daba por perdido. Se eleva
// una consulta de OPCIÓN con las opciones del nivel, en su orden: código y etiqueta.
func TestQuery_OptionCarriesTheLevelCatalog(t *testing.T) {
	q := queryAt(t, cascadeState(cart.LevelSummary), "finalizar")
	want := &modules.Query{
		Class: modules.QueryClassOption, Level: cart.LevelSummary, Text: "finalizar",
		Options: []modules.QueryOption{
			{Code: "1", Label: "Confirmar y finalizar"},
			{Code: "2", Label: "Seguir agregando"},
			{Code: "3", Label: "Indicación para todo el pedido"},
			{Code: "9", Label: "Cancelar pedido"},
		},
	}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("Query = %+v\nquiero  %+v", q, want)
	}
}

// Las opciones que se ofrecen en cada nivel que pregunta: son las MISMAS de la cascada.
func TestQuery_OptionsPerLevel(t *testing.T) {
	cases := []struct {
		level string
		want  []modules.QueryOption
	}{
		{cart.LevelCategories, []modules.QueryOption{{Code: "1", Label: "Hamburguesas"}, {Code: "2", Label: "Postres"}}},
		{cart.LevelArticles, []modules.QueryOption{
			{Code: "1", Label: "Torta de chocolate"}, {Code: "2", Label: "Torta de vainilla"}, {Code: "0", Label: "Volver"}}},
		{cart.LevelArticle, []modules.QueryOption{
			{Code: "1", Label: "Ver descripción"}, {Code: "2", Label: "Agregar al pedido"}, {Code: "0", Label: "Volver"}}},
		{cart.LevelContinue, []modules.QueryOption{
			{Code: "1", Label: "Agregar más"}, {Code: "2", Label: "Finalizar pedido"},
			{Code: "3", Label: "Indicación para este artículo"}, {Code: "9", Label: "Cancelar pedido"}, {Code: "0", Label: "Volver"}}},
		{cart.LevelItemNoteScope, []modules.QueryOption{
			{Code: "1", Label: "Para las 2"}, {Code: "2", Label: "Solo para 1"}, {Code: "0", Label: "Volver sin indicación"}}},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			q := queryAt(t, cascadeState(tc.level), "xyzzy plugh")
			if q == nil {
				t.Fatal("Query = nil, quiero la petición")
			}
			if q.Class != modules.QueryClassOption || q.Level != tc.level || q.Text != "xyzzy plugh" || len(q.Chunks) != 0 {
				t.Errorf("Query = %+v, quiero clase opción, el nivel y el texto, sin trozos", q)
			}
			if !reflect.DeepEqual(q.Options, tc.want) {
				t.Errorf("Options = %+v\nquiero    %+v", q.Options, tc.want)
			}
		})
	}

	// El nivel de variante ofrece los ORDINALES de pantalla, no el código de negocio.
	vars := v2Vars(t)
	seedState(t, vars, cartState{Level: cart.LevelVariant, CatCode: "01", SKU: "TORTA-CHOC", Started: true})
	q := cart.New().Step(model.Node{}, model.Conversation{Vars: vars}, "la grande").Query
	want := []modules.QueryOption{{Code: "1", Label: "10-12 porciones"}, {Code: "2", Label: "25-30 porciones"}, {Code: "0", Label: "Volver"}}
	if q == nil || !reflect.DeepEqual(q.Options, want) {
		t.Errorf("variante: Query = %+v, quiero las opciones %+v", q, want)
	}
}

// 🔴 REQUISITO DE PRIVACIDAD: los niveles de texto libre NO consultan nunca —una
// consulta manda el texto FUERA, a un modelo—, ni los terminales, ni un nivel
// desconocido.
func TestQuery_FreeTextLevelsNeverAsk(t *testing.T) {
	inputs := []string{"cancelar pedido", "Juan Pérez, RUT 12.345.678-5", "mejor dos", "finalizar", "Av. Siempre Viva 742"}
	levels := []string{cart.LevelItemNote, cart.LevelOrderNote, cart.LevelBuyerData,
		cart.LevelClosed, cart.LevelCancelled, "nivel_que_no_existe_todavia"}
	for _, level := range levels {
		for _, in := range inputs {
			if q := queryAt(t, cascadeState(level), in); q != nil {
				t.Errorf("nivel %s, entrada %q: se elevó una consulta con el texto del cliente: %+v", level, in, *q)
			}
		}
	}
}

// Ni un número, ni un código del nivel, ni lo que la cascada ya resuelve, ni la
// entrada vacía, ni la prosa larga: nada de eso paga una consulta.
func TestQuery_WhatNeverAsks(t *testing.T) {
	prose := strings.TrimSpace(strings.Repeat("palabra ", 17))
	cases := []struct {
		name  string
		level string
		in    string
	}{
		{"typed code", cart.LevelCategories, "2"},
		{"number that is no option", cart.LevelCategories, "77"},
		{"digits at quantity", cart.LevelQuantity, "3"},
		{"zero at quantity", cart.LevelQuantity, "0"},
		{"huge number at quantity", cart.LevelQuantity, "99999999999999999999999"},
		{"cascade resolves exact", cart.LevelCategories, "postres"},
		{"cascade resolves fuzzy", cart.LevelCategories, "hamburgesas"},
		{"empty", cart.LevelCategories, ""},
		{"spaces", cart.LevelQuantity, "  \n"},
		{"only punctuation", cart.LevelQuantity, "¿?!"},
		{"long prose at categories", cart.LevelCategories, prose},
		{"long prose at quantity", cart.LevelQuantity, prose},
		{"stale focus offers nothing", cart.LevelArticles + "!", "volver"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := cascadeState(strings.TrimSuffix(tc.level, "!"))
			if strings.HasSuffix(tc.level, "!") {
				st.CatCode = "9" // categoría que el catálogo no tiene
			}
			if q := queryAt(t, st, tc.in); q != nil {
				t.Errorf("Query = %+v, quiero nil", *q)
			}
		})
	}
	// Y justo en el techo, 16 tokens, sí pregunta.
	sixteen := strings.TrimSpace(strings.Repeat("palabra ", 16))
	if q := queryAt(t, cascadeState(cart.LevelQuantity), sixteen); q == nil || q.Text != sixteen {
		t.Errorf("16 tokens: Query = %+v, quiero la petición", q)
	}
}

// La ADUANA: solo pasa un código que el propio módulo ofreció —o de 1 a 4 dígitos
// ASCII para la cantidad—. Lo demás deja la entrada intacta y el nivel repromptea.
func TestQuery_VerdictIsValidatedAgainstWhatWasOffered(t *testing.T) {
	summaryScreen := "🧾 Resumen del pedido:\nHamburguesa x2  $10.00\nTOTAL  $10.00\n" +
		"1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido"
	option := []struct {
		name    string
		verdict modules.Verdict
		want    string // nivel al que lleva
	}{
		{"offered code", modules.Verdict{Code: "1"}, cart.LevelClosed},
		{"another offered code", modules.Verdict{Code: "9"}, cart.LevelCancelled},
		{"code of another level", modules.Verdict{Code: "0"}, cart.LevelSummary},
		{"invented code", modules.Verdict{Code: "7"}, cart.LevelSummary},
		{"the client's phrase", modules.Verdict{Code: "finalizar"}, cart.LevelSummary},
		{"the label instead of the code", modules.Verdict{Code: "Confirmar y finalizar"}, cart.LevelSummary},
		{"padded code", modules.Verdict{Code: " 1"}, cart.LevelSummary},
		{"only codes by chunk", modules.Verdict{Codes: []string{"1"}}, cart.LevelSummary},
		{"no resolver", modules.Verdict{Reason: modules.QueryReasonNoResolver}, cart.LevelSummary},
		{"failure", modules.Verdict{Reason: modules.QueryReasonFailure}, cart.LevelSummary},
		{"inconclusive", modules.Verdict{Reason: modules.QueryReasonInconclusive}, cart.LevelSummary},
	}
	for _, tc := range option {
		t.Run("option/"+tc.name, func(t *testing.T) {
			vars := map[string]any{modules.VarContentRaw: cascadeCatalog()}
			seedState(t, vars, cascadeState(cart.LevelSummary))
			res := turn(cart.New(), model.Conversation{Vars: vars}, "finalizar", func(modules.Query) modules.Verdict { return tc.verdict })
			if st := stateOf(t, res.Vars); st.Level != tc.want {
				t.Fatalf("estado = %+v, quiero el nivel %q", st, tc.want)
			}
			if tc.want == cart.LevelSummary {
				mustScreen(t, res.Outputs, invalidPrefix+summaryScreen)
			}
			if dump := jsonOf(t, res.Vars[stateVarKey]); strings.Contains(dump, "finalizar") {
				t.Errorf("el texto del cliente entró en el estado: %s", dump)
			}
		})
	}

	quantity := []struct {
		code string
		qty  int // 0 ⇒ no agrega
	}{
		{"2", 2}, {"0012", 12}, {"9999", 9999},
		{"10000", 0}, {"-2", 0}, {"+2", 0}, {"2.0", 0}, {"dos", 0}, {"٢", 0}, {" 2", 0}, {"", 0},
	}
	for _, tc := range quantity {
		t.Run("quantity/"+tc.code, func(t *testing.T) {
			m := cart.New()
			res := turn(m, model.Conversation{Vars: atQuantity(t, m)}, "mejor dos", func(modules.Query) modules.Verdict {
				return modules.Verdict{Code: tc.code}
			})
			st := stateOf(t, res.Vars)
			if tc.qty == 0 {
				if st.Level != cart.LevelQuantity || len(st.Lines) != 0 {
					t.Fatalf("estado = %+v, quiero seguir en quantity sin línea", st)
				}
				mustScreen(t, res.Outputs, invalidQty+coffeeQuantity)
				return
			}
			if st.Level != cart.LevelContinue || len(st.Lines) != 1 || st.Lines[0].Qty != tc.qty {
				t.Errorf("estado = %+v, quiero una línea de %d", st, tc.qty)
			}
		})
	}
}

// Se porta tal cual: en la cantidad, un veredicto «0» pasa la aduana (son dígitos) y la
// sub-máquina lo lee como lo que «0» es en ese nivel: volver.
func TestQuery_ZeroVerdictAtQuantityMeansBack(t *testing.T) {
	m := cart.New()
	res := turn(m, model.Conversation{Vars: atQuantity(t, m)}, "ninguno", func(modules.Query) modules.Verdict {
		return modules.Verdict{Code: "0"}
	})
	if st := stateOf(t, res.Vars); st.Level != cart.LevelArticle {
		t.Errorf("estado = %+v, quiero la ficha del artículo", st)
	}
}

// Degradación: sin resolutor el turno acaba con la pantalla de SIEMPRE, byte a byte, y
// el módulo no inventa un mensaje sobre la avería.
func TestQuery_DegradationProducesTheUsualScreen(t *testing.T) {
	m := cart.New()
	res := turn(m, model.Conversation{Vars: atQuantity(t, m)}, "mejor dos", nil)
	mustScreen(t, res.Outputs, invalidQty+coffeeQuantity)
	if st := stateOf(t, res.Vars); st.Level != cart.LevelQuantity || len(st.Lines) != 0 {
		t.Errorf("estado = %+v, quiero seguir en quantity", st)
	}
	if _, left := res.Vars[modules.VarQueryVerdict]; left {
		t.Error("el veredicto sobrevivió al turno")
	}
}

// La cascada cuenta UNA vez por mensaje: la segunda pasada no la re-ejecuta.
func TestQuery_CascadeCountsOncePerMessage(t *testing.T) {
	s := &spy{}
	m := cart.New(cart.WithMatchHook(s.record))
	vars := map[string]any{modules.VarContentRaw: cascadeCatalog()}
	seedState(t, vars, cascadeState(cart.LevelSummary))
	turn(m, model.Conversation{Vars: vars}, "finalizar", func(modules.Query) modules.Verdict { return modules.Verdict{Code: "1"} })
	if want := [][2]string{{"ninguno", cart.LevelSummary}}; !reflect.DeepEqual(s.seen, want) {
		t.Errorf("observador = %v, quiero %v: una vez, en la primera pasada", s.seen, want)
	}
}
