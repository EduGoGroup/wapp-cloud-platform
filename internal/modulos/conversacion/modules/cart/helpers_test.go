package cart_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// Ayudantes compartidos por los tests del paquete que conducen la sub-máquina. Viven
// tras la etiqueta mientras el módulo esté en rojo: los tests que ya nacen verdes
// (constantes, validate) no dependen de ellos.

// stateVarKey es la clave de Conversation.Vars del estado del carrito (state.go).
const stateVarKey = "cart"

// cartLine es la forma serializada de una línea del pedido en Vars["cart"].
type cartLine struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
	Customization string  `json:"customization,omitempty"`
}

// cartState es el ESPEJO, en el test, de la forma que state.go promete para
// Vars["cart"]: mismas claves, mismo orden, mismos omitempty. Es lo que deja probar
// el estado sin tocar un solo símbolo no exportado, y lo que hace que los goldens
// sujeten también el orden de serialización.
type cartState struct {
	Level          string     `json:"level"`
	CatCode        string     `json:"cat_code,omitempty"`
	SKU            string     `json:"sku,omitempty"`
	Page           int        `json:"page,omitempty"`
	Lines          []cartLine `json:"lines,omitempty"`
	VariantCode    string     `json:"variant_code,omitempty"`
	Started        bool       `json:"started,omitempty"`
	Note           string     `json:"note,omitempty"`
	NoteSplit      bool       `json:"note_split,omitempty"`
	BuyerIdx       int        `json:"buyer_idx,omitempty"`
	Reprompts      int        `json:"reprompts,omitempty"`
	RepromptsEvent string     `json:"reprompts_event_id,omitempty"`
}

// stateOf lee el estado que Step dejó en Vars["cart"]. Rechaza claves que el
// contrato no declara: una clave nueva en el JSONB tiene que pasar por state.go.
func stateOf(t *testing.T, vars map[string]any) cartState {
	t.Helper()
	raw, ok := vars[stateVarKey]
	if !ok {
		return cartState{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Vars[%q] no es serializable: %v", stateVarKey, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var st cartState
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("Vars[%q] = %s no tiene la forma del contrato: %v", stateVarKey, b, err)
	}
	return st
}

// seedState deja un estado en Vars["cart"] tal como lo deja el round-trip JSONB
// (map[string]any, números float64), para arrancar un test a mitad de la máquina.
func seedState(t *testing.T, vars map[string]any, st cartState) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("estado de prueba no serializable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("estado de prueba ilegible: %v", err)
	}
	vars[stateVarKey] = m
}

// rawFromJSON simula el round-trip JSONB: decodifica un literal JSON a
// map[string]any (números como float64), tal como llega Content.Raw.
func rawFromJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("literal JSON de prueba inválido: %v\n%s", err, s)
	}
	return m
}

// readTestdata lee un archivo de testdata/ (blobs de catálogo y goldens).
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	//nolint:gosec // G304: la ruta es un nombre fijo bajo testdata/, no entrada externa
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("leyendo testdata/%s: %v", name, err)
	}
	return b
}

// rawFromFile carga un blob de catálogo de testdata/ como Content.Raw.
func rawFromFile(t *testing.T, name string) map[string]any {
	t.Helper()
	return rawFromJSON(t, string(readTestdata(t, name)))
}

// catalogRaw es el catálogo v1 de los tests de conducta: Bebidas (Café, Té) y
// Postres (Flan), con la forma de model.Content.Raw tras el round-trip JSONB.
func catalogRaw() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{"code": "1", "label": "Bebidas", "items": []any{
				map[string]any{"code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble"},
				map[string]any{"code": "2", "sku": "TE", "label": "Té", "price": 2.0, "description": "Verde o negro"},
			}},
			map[string]any{"code": "2", "label": "Postres", "items": []any{
				map[string]any{"code": "1", "sku": "FLAN", "label": "Flan", "price": 3.0, "description": "Casero"},
			}},
		},
	}
}

// seededVars devuelve Vars con el snapshot del catálogo v1 sembrado donde lo siembra
// el engine antes de cada Step (modules.VarContentRaw).
func seededVars() map[string]any {
	return map[string]any{modules.VarContentRaw: catalogRaw()}
}

// v2Vars siembra el catálogo v2 del contrato: Tortas (una torta con dos variantes y
// un combo) y Bebidas (un Café v1 de toda la vida).
func v2Vars(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{modules.VarContentRaw: rawFromFile(t, "catalog_v2.json")}
}

// contentOf devuelve, como contenido resuelto (lo que reciben Render y Prime), el
// catálogo sembrado en unas Vars.
func contentOf(t *testing.T, vars map[string]any) model.Content {
	t.Helper()
	raw := mustBe[map[string]any](t, vars[modules.VarContentRaw], "el catálogo sembrado en Vars")
	return model.Content{Raw: raw}
}

// resolver responde a una Query. nil ⇒ nadie responde, que es la degradación
// «sin_resolutor».
type resolver func(q modules.Query) modules.Verdict

// turn ejecuta UN turno del carrito tal como lo ejecuta engine.Step: primera pasada
// y, si el módulo pide consulta, UNA re-entrada con el veredicto sembrado, que se
// barre al volver. Es el mecanismo del engine reducido a lo imprescindible: lo que
// aquí se mide es la conducta del carrito dentro de un turno completo; el mecanismo
// lo prueba engine/consulta_test.go con el engine de verdad.
func turn(m cart.Module, conv model.Conversation, input string, r resolver) modules.Result {
	res := m.Step(model.Node{}, conv, input)
	if res.Query == nil {
		return res
	}
	v := modules.Verdict{Reason: modules.QueryReasonNoResolver}
	if r != nil {
		v = r(*res.Query)
	}
	conv.Vars = modules.WithVerdict(conv.Vars, v)
	res = m.Step(model.Node{}, conv, input)
	res.Vars = modules.StripQueryVerdict(res.Vars)
	return res
}

// drive aplica un turno SIN resolutor y fuera de un evento, y devuelve el estado,
// las pantallas y las Vars resultantes (para encadenar).
func drive(t *testing.T, m cart.Module, vars map[string]any, input string) (cartState, []string, map[string]any) {
	t.Helper()
	res := turn(m, model.Conversation{Vars: vars}, input, nil)
	return stateOf(t, res.Vars), res.Outputs, res.Vars
}

// driveEffects es drive devolviendo los efectos declarados en vez de las pantallas.
func driveEffects(t *testing.T, m cart.Module, vars map[string]any, input string) (cartState, []modules.Effect, map[string]any) {
	t.Helper()
	res := turn(m, model.Conversation{Vars: vars}, input, nil)
	return stateOf(t, res.Vars), res.Effects, res.Vars
}

// walk encadena varios turnos y devuelve las Vars finales.
func walk(t *testing.T, m cart.Module, vars map[string]any, inputs ...string) map[string]any {
	t.Helper()
	for _, in := range inputs {
		_, _, vars = drive(t, m, vars, in)
	}
	return vars
}

func joined(outs []string) string { return strings.Join(outs, "\n") }

// mustContain falla si la salida no contiene alguno de los fragmentos.
func mustContain(t *testing.T, outs []string, subs ...string) {
	t.Helper()
	s := joined(outs)
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Fatalf("la salida %q no contiene %q", s, sub)
		}
	}
}

// mustNotContain falla si la salida contiene alguno de los fragmentos.
func mustNotContain(t *testing.T, outs []string, subs ...string) {
	t.Helper()
	s := joined(outs)
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Fatalf("la salida %q NO debía contener %q", s, sub)
		}
	}
}

// mustScreen exige UNA pantalla y que sea exactamente la esperada.
func mustScreen(t *testing.T, outs []string, want string) {
	t.Helper()
	if len(outs) != 1 || outs[0] != want {
		t.Fatalf("pantallas = %q\nquiero una sola:\n%q", outs, want)
	}
}

// effectNamed devuelve el primer efecto con ese nombre, o falla si no está.
func effectNamed(t *testing.T, effs []modules.Effect, name string) modules.Effect {
	t.Helper()
	for _, e := range effs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no se encontró el efecto %q en %+v", name, effs)
	return modules.Effect{}
}

// effectNames lista los nombres de los efectos, en orden.
func effectNames(effs []modules.Effect) []string {
	out := make([]string, 0, len(effs))
	for _, e := range effs {
		out = append(out, e.Name)
	}
	return out
}

// jsonOf serializa un valor para comparar formas de payload (claves ordenadas).
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("valor no serializable: %v", err)
	}
	return string(b)
}

// mustBe devuelve v como T, o corta el test diciendo qué tipo se esperaba y cuál
// llegó. Es la aserción de tipo de los tests: nunca descarta el `ok`.
func mustBe[T any](t *testing.T, v any, what string) T {
	t.Helper()
	got, ok := v.(T)
	if !ok {
		t.Fatalf("%s es %T, quiero %T", what, v, got)
	}
	return got
}

// itemsOf devuelve las líneas («items») del payload de un efecto del carrito, tal
// como las emite la sub-máquina en proceso: un []map[string]any.
func itemsOf(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	return mustBe[[]map[string]any](t, payload["items"], `payload["items"]`)
}
