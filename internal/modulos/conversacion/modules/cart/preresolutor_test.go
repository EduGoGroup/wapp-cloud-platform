package cart_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// preresolutor_test.go — la cascada determinista delante de la sub-máquina (Plan 044 ·
// Ola 3.5 · T3.5-1), vista por Module.Step y por el observador de WithMatchHook.
// preresolutor.go no exporta nada.

// cascadeCatalog: Hamburguesas (Hamburguesa, Papas fritas) y Postres (dos tortas que
// empatan por su primera palabra).
func cascadeCatalog() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{"code": "1", "label": "Hamburguesas", "items": []any{
				map[string]any{"code": "1", "sku": "HAMB", "label": "Hamburguesa", "price": 5.0},
				map[string]any{"code": "2", "sku": "PAPAS", "label": "Papas fritas", "price": 2.0},
			}},
			map[string]any{"code": "2", "label": "Postres", "items": []any{
				map[string]any{"code": "1", "sku": "TCHOC", "label": "Torta de chocolate", "price": 4.0},
				map[string]any{"code": "2", "sku": "TVAIN", "label": "Torta de vainilla", "price": 4.0},
			}},
		},
	}
}

// spy registra los avisos del observador: {escalón, nivel}.
type spy struct{ seen [][2]string }

func (s *spy) record(step, level string) { s.seen = append(s.seen, [2]string{step, level}) }

// hamburgerLine es la línea con la que se siembran los niveles que necesitan una.
var hamburgerLine = []cartLine{{SKU: "HAMB", Label: "Hamburguesa", Qty: 2, UnitPrice: 5}}

// cascadeState es un estado del catálogo de la cascada en el nivel dado.
func cascadeState(level string) cartState {
	st := cartState{Level: level, Started: true}
	switch level {
	case cart.LevelCategories:
	case cart.LevelArticles:
		st.CatCode = "2"
	default:
		st.CatCode, st.SKU, st.Lines = "1", "HAMB", hamburgerLine
	}
	return st
}

// cascadeTurn ejecuta un turno sin resolutor sobre el catálogo de la cascada, desde el
// estado dado, y devuelve el estado resultante y lo que vio el observador.
func cascadeTurn(t *testing.T, st cartState, input string) (cartState, [][2]string) {
	t.Helper()
	s := &spy{}
	vars := map[string]any{modules.VarContentRaw: cascadeCatalog()}
	if st.Level != "" {
		seedState(t, vars, st)
	}
	out, _, _ := drive(t, cart.New(cart.WithMatchHook(s.record)), vars, input)
	return out, s.seen
}

// El corpus de la cascada: qué resuelve, por qué escalón, y qué deja pasar intacto.
// `step` vacío ⇒ la cascada NO corrió (ningún aviso). `to` es dónde acaba el carrito:
// "nivel", "nivel/categoría" o "nivel/categoría/sku"; un inválido se queda donde estaba.
func TestCascade_Corpus(t *testing.T) {
	const (
		cat = cart.LevelCategories
		art = cart.LevelArticles
	)
	cases := []struct {
		name  string
		level string
		in    string
		step  string
		to    string
	}{
		// — Puerta 2: un código no pasa por la cascada, exista o no.
		{"typed code", cat, "2", "", "articles/2"},
		{"number that is no option", cat, "77", "", "categories"},
		{"zero is a code too", cat, "0", "", "categories"},
		{"long digit string", cat, "99999999999999999999999", "", "categories"},
		{"empty", cat, "", "", "categories"},
		{"only spaces", cat, " \t\n", "", "categories"},
		// — Resuelve.
		{"exact label", cat, "postres", "exact", "articles/2"},
		{"label in another case", cat, "POSTRES", "exact", "articles/2"},
		{"label with accent", cat, "póstres", "exact", "articles/2"},
		{"label inside a phrase", cat, "quiero ver los postres por favor", "exact", "articles/2"},
		{"typo forgiven from seven runes", cat, "hamburgesas", "fuzzy", "articles/1"},
		{"singular of a long label", cat, "hamburguesa", "fuzzy", "articles/1"},
		{"seven runes already forgive one typo", cat, "postre", "fuzzy", "articles/2"},
		{"abbreviated by the right", art, "torta de vainilla", "exact", "article/2/TVAIN"},
		{"more tokens beat fewer", art, "una torta de vainilla grande", "exact", "article/2/TVAIN"},
		{"menu word", art, "volver", "exact", "categories"},
		{"menu word inside a phrase", art, "mejor volver atrás", "exact", "categories"},
		// — No resuelve: la entrada sale intacta y el nivel repromptea.
		{"no correspondence", cat, "quiero algo rico", "ninguno", "categories"},
		{"tie between two options", art, "torta", "ninguno", "articles/2"},
		{"tie inside a phrase", art, "quiero una torta", "ninguno", "articles/2"},
		{"short word does not forgive a typo", art, "tarta", "ninguno", "articles/2"},
		{"suffix of a label is not a prefix", art, "vainilla", "ninguno", "articles/2"},
		{"the more row is not a candidate", cat, "más", "ninguno", "categories"},
		{"negative number is not a code", cat, "-2", "ninguno", "categories"},
		// — Adversarios: separadores repetidos, dígitos no ASCII, espacios Unicode.
		{"repeated separators around a label", cat, "postres,,,", "exact", "articles/2"},
		{"repeated separators between two labels", cat, "postres@@hamburguesas", "ninguno", "categories"},
		{"label glued by separators", art, "torta@@de@@vainilla", "exact", "article/2/TVAIN"},
		{"no-break spaces", art, "torta\u00a0de\u00a0vainilla", "exact", "article/2/TVAIN"},
		{"em spaces around", cat, "\u2003postres\u2003", "exact", "articles/2"},
		{"zero width space inside a word", cat, "pos\u200btres", "ninguno", "categories"},
		{"arabic-indic digit is not a code", cat, "٢", "ninguno", "categories"},
		{"fullwidth digit is not a code", cat, "２", "ninguno", "categories"},
		{"superscript digit is not a code", cat, "²", "ninguno", "categories"},
		// Se porta tal cual: un dígito PEGADO a la etiqueta no es un número (la puerta 2 mira
		// la entrada entera) y el fuzzy lo perdona como una errata más.
		{"digit glued to a label is a forgiven typo", cat, "2postres", "fuzzy", "articles/2"},
		{"emoji next to a label", cat, "🍰 postres", "exact", "articles/2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, seen := cascadeTurn(t, cascadeState(tc.level), tc.in)
			var want [][2]string
			if tc.step != "" {
				want = [][2]string{{tc.step, tc.level}}
			}
			if !reflect.DeepEqual(seen, want) {
				t.Errorf("%q: observador = %v, quiero %v", tc.in, seen, want)
			}
			got := st.Level
			if st.CatCode != "" {
				got += "/" + st.CatCode
			}
			if st.SKU != "" {
				got += "/" + st.SKU
			}
			if got != tc.to {
				t.Errorf("%q: el carrito acabó en %q, quiero %q", tc.in, got, tc.to)
			}
		})
	}
}

// Lo que no resuelve sale INTACTO: la pantalla es la del inválido de siempre.
func TestCascade_UnresolvedInputReprompsAsAlways(t *testing.T) {
	vars := map[string]any{modules.VarContentRaw: cascadeCatalog()}
	_, outs, _ := drive(t, cart.New(), vars, "quiero algo rico")
	mustScreen(t, outs, invalidPrefix+"🛒 Elige una categoría:\n1) Hamburguesas\n2) Postres")
}

// La prosa no se compara: hasta 16 tokens se mira; con 17, ni corre.
func TestCascade_LongProseIsNotCompared(t *testing.T) {
	sixteen := strings.TrimSpace(strings.Repeat("hola ", 15)) + " postres"
	st, seen := cascadeTurn(t, cascadeState(cart.LevelCategories), sixteen)
	if st.Level != cart.LevelArticles || len(seen) != 1 || seen[0][0] != "exact" {
		t.Errorf("16 tokens: estado %+v y observador %v; quiero que resuelva por exact", st, seen)
	}
	st, seen = cascadeTurn(t, cascadeState(cart.LevelCategories), "hola "+sixteen)
	if st.Level != cart.LevelCategories || len(seen) != 0 {
		t.Errorf("17 tokens: estado %+v y observador %v; quiero que la cascada ni corra", st, seen)
	}
}

// Los menús FIJOS también se entienden por su rótulo, en los cuatro niveles que lo
// tienen; el código que se devuelve es el de la tecla.
func TestCascade_FixedMenus(t *testing.T) {
	cases := []struct {
		level string
		in    string
		want  string // nivel al que lleva
	}{
		{cart.LevelArticle, "ver descripción", cart.LevelArticle},
		{cart.LevelArticle, "agregar al pedido", cart.LevelQuantity},
		{cart.LevelArticle, "agregar", cart.LevelQuantity},
		{cart.LevelArticle, "volver", cart.LevelArticles},
		{cart.LevelContinue, "agregar más", cart.LevelArticles},
		{cart.LevelContinue, "finalizar pedido", cart.LevelSummary},
		{cart.LevelContinue, "finalizar", cart.LevelSummary},
		{cart.LevelContinue, "indicación", cart.LevelItemNoteScope},
		{cart.LevelContinue, "quiero cancelar", cart.LevelCancelled},
		{cart.LevelContinue, "volver", cart.LevelArticle},
		{cart.LevelSummary, "confirmar", cart.LevelClosed},
		{cart.LevelSummary, "confirmar y finalizar", cart.LevelClosed},
		{cart.LevelSummary, "seguir agregando", cart.LevelArticles},
		{cart.LevelSummary, "indicación para todo el pedido", cart.LevelOrderNote},
		{cart.LevelSummary, "cancelar pedido", cart.LevelCancelled},
		{cart.LevelItemNoteScope, "para las 2", cart.LevelItemNote},
		{cart.LevelItemNoteScope, "solo para 1", cart.LevelItemNote},
		{cart.LevelItemNoteScope, "volver sin indicación", cart.LevelContinue},
	}
	for _, tc := range cases {
		t.Run(tc.level+"/"+tc.in, func(t *testing.T) {
			st, seen := cascadeTurn(t, cascadeState(tc.level), tc.in)
			if st.Level != tc.want {
				t.Errorf("estado = %+v, quiero el nivel %q", st, tc.want)
			}
			if len(seen) != 1 || seen[0] != [2]string{"exact", tc.level} {
				t.Errorf("observador = %v, quiero [{exact %s}]", seen, tc.level)
			}
		})
	}
	// «solo para 1» casa la opción 2 ENTERA (3 tokens) y no la 1 por su «para» suelto.
	st, _ := cascadeTurn(t, cascadeState(cart.LevelItemNoteScope), "solo para 1")
	if !st.NoteSplit {
		t.Errorf("«solo para 1»: estado = %+v, quiero el split marcado", st)
	}
	st, _ = cascadeTurn(t, cascadeState(cart.LevelItemNoteScope), "para las 2")
	if st.NoteSplit {
		t.Errorf("«para las 2»: estado = %+v, quiero la indicación para todas", st)
	}
}

// 🔴 REQUISITO DE PRIVACIDAD: los niveles de texto libre no pasan NUNCA por la cascada,
// escriba lo que escriba el cliente; tampoco la cantidad ni los terminales. Y un nivel
// que no existe queda fuera: la ausencia nunca es un sí.
func TestCascade_LevelsThatNeverRun(t *testing.T) {
	inputs := []string{"cancelar pedido", "hamburgesa", "Torta de chocolate", "volver", "confirmar y finalizar"}
	levels := []string{
		cart.LevelItemNote, cart.LevelOrderNote, cart.LevelBuyerData, // texto libre
		cart.LevelQuantity,                    // aritmética del lenguaje: es de la consulta
		cart.LevelClosed, cart.LevelCancelled, // terminales
		"nivel_que_no_existe_todavia",
	}
	for _, level := range levels {
		for _, in := range inputs {
			s := &spy{}
			vars := map[string]any{modules.VarContentRaw: cascadeCatalog(),
				cart.VarBuyerFields: []any{map[string]any{"key": "nombre", "label": "Nombre", "required": true}}}
			seedState(t, vars, cascadeState(level))
			turn(cart.New(cart.WithMatchHook(s.record)), model.Conversation{Vars: vars}, in, nil)
			if len(s.seen) != 0 {
				t.Errorf("nivel %s, entrada %q: la cascada corrió y avisó %v", level, in, s.seen)
			}
		}
	}
}

// Los trece niveles, uno a uno: cuáles admiten cascada. Un nivel nuevo que alguien
// añada a state.go tiene que pasar por esta tabla.
func TestCascade_CoversTheThirteenLevels(t *testing.T) {
	admits := map[string]bool{
		cart.LevelCategories:    true,
		cart.LevelArticles:      true,
		cart.LevelArticle:       true,
		cart.LevelVariant:       false, // el artículo del fixture no tiene variantes
		cart.LevelContinue:      true,
		cart.LevelSummary:       true,
		cart.LevelItemNoteScope: true,
		cart.LevelQuantity:      false,
		cart.LevelItemNote:      false,
		cart.LevelOrderNote:     false,
		cart.LevelBuyerData:     false,
		cart.LevelClosed:        false,
		cart.LevelCancelled:     false,
	}
	if len(admits) != 13 {
		t.Fatalf("la tabla cubre %d niveles y state.go declara 13", len(admits))
	}
	for level, want := range admits {
		// Un texto que no casa nada: si la cascada corre, avisa «ninguno».
		_, seen := cascadeTurn(t, cascadeState(level), "xyzzy plugh")
		if got := len(seen) == 1 && seen[0] == [2]string{"ninguno", level}; got != want {
			t.Errorf("nivel %s: la cascada corrió = %v (observador %v), quiero %v", level, got, seen, want)
		}
	}
}

// Si el catálogo ya no puede resolver lo que el nivel tiene en foco, el nivel no
// ofrece opciones: decide la sub-máquina, sin cascada.
func TestCascade_StaleFocusOffersNoOptions(t *testing.T) {
	cases := []cartState{
		{Level: cart.LevelArticles, CatCode: "9", Started: true},
		{Level: cart.LevelArticle, CatCode: "1", SKU: "YA-NO", Started: true},
		{Level: cart.LevelVariant, CatCode: "1", SKU: "YA-NO", Started: true},
		{Level: cart.LevelItemNoteScope, CatCode: "1", SKU: "HAMB", Started: true}, // sin líneas
	}
	for _, st := range cases {
		if _, seen := cascadeTurn(t, st, "volver"); len(seen) != 0 {
			t.Errorf("estado %+v: la cascada corrió y avisó %v", st, seen)
		}
	}
}

// La cascada mira TODAS las categorías, no solo las de la página en pantalla.
func TestCascade_CategoryOfAnotherPage(t *testing.T) {
	s := &spy{}
	vars := map[string]any{modules.VarContentRaw: cascadeCatalog(), cart.VarPageSize: 1}
	st, _, _ := drive(t, cart.New(cart.WithMatchHook(s.record)), vars, "postres")
	if st.Level != cart.LevelArticles || st.CatCode != "2" {
		t.Errorf("estado = %+v, quiero los artículos de Postres, que está en la página 2", st)
	}
}

// Un código NO numérico del nivel tampoco pasa por la cascada: su dueño ya lo resuelve
// por igualdad exacta.
func TestCascade_NonNumericLevelCodeIsLeftAlone(t *testing.T) {
	s := &spy{}
	st, _, _ := drive(t, cart.New(cart.WithMatchHook(s.record)), oddVars(t), "B")
	if st.Level != cart.LevelArticles || st.CatCode != "B" || len(s.seen) != 0 {
		t.Errorf("estado = %+v, observador = %v; quiero la categoría B sin cascada", st, s.seen)
	}
}
