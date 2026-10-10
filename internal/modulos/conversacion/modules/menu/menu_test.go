package menu_test

import (
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/menu"
)

// Aserción de compilación: el menú es un modules.Module (y su valor cero también).
var (
	_ modules.Module = menu.New()
	_ modules.Module = menu.Module{}
)

const (
	invalidNotice = "Opción no válida. Responde con el número de una de las opciones.\n\n"
	helpNotice    = "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n"
)

func menuNode() model.Node {
	return model.Node{
		Type:    model.NodeTypeMenu,
		Prompt:  "Elige:\n1) A\n2) B",
		Options: map[string]string{"1": "a", "2": "b"},
	}
}

// Las claves y el tope son observables: viven en el JSONB de flow_state.vars.
func TestConstants(t *testing.T) {
	if menu.RepromptKey != "menu_reprompt" {
		t.Errorf("RepromptKey = %q, quiero \"menu_reprompt\"", menu.RepromptKey)
	}
	if menu.MaxReprompts != 3 {
		t.Errorf("MaxReprompts = %d, quiero 3", menu.MaxReprompts)
	}
}

func TestModule_Identity(t *testing.T) {
	m := menu.New()
	if got := m.Type(); got != "menu" {
		t.Errorf("Type() = %q, quiero \"menu\"", got)
	}
	if !m.WaitsForInput() {
		t.Error("WaitsForInput() = false, quiero true: el menú espera la opción")
	}
	if m.ProducesDurableContent() {
		t.Error("ProducesDurableContent() = true, quiero false: el menú no proyecta nada durable")
	}
}

// Render devuelve el prompt del contenido RESUELTO, no el del nodo, y siempre un texto.
func TestModule_Render(t *testing.T) {
	m := menu.New()
	cases := []struct {
		name    string
		content model.Content
		want    string
	}{
		{name: "resolved prompt wins over node prompt", content: model.Content{Prompt: "Resuelto:\n1) X"}, want: "Resuelto:\n1) X"},
		{name: "empty content yields one empty text", content: model.Content{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := m.Render(menuNode(), tc.content)
			if len(out) != 1 || out[0] != tc.want {
				t.Errorf("Render = %q, quiero [%q]", out, tc.want)
			}
		})
	}
}

// Opción válida: transiciona, borra el contador (y su sello) y no dice ni declara nada.
func TestModule_Step_ValidOption(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "exact key", input: "1", want: "a"},
		{name: "ascii spaces are trimmed", input: " 2 ", want: "b"},
		{name: "unicode spaces are trimmed", input: "\u00a02\u3000\n", want: "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := model.Conversation{EventID: "ev-1", Vars: map[string]any{
				menu.RepromptKey: 2,
				modules.RepromptEventKey(menu.RepromptKey): "ev-1",
				"other": "se conserva",
			}}
			res := menu.New().Step(menuNode(), conv, tc.input)
			if res.Next == nil || *res.Next != tc.want {
				t.Fatalf("Next = %v, quiero %q", res.Next, tc.want)
			}
			if !maps.Equal(res.Vars, map[string]any{"other": "se conserva"}) {
				t.Errorf("Vars = %v, quiero solo la clave ajena (contador y sello borrados)", res.Vars)
			}
			if len(res.Outputs) != 0 || len(res.Effects) != 0 || res.Query != nil || res.Outcome != model.OutcomeUndeclared {
				t.Errorf("Result = %+v, quiero una transición sin salidas, efectos, desenlace ni consulta", res)
			}
		})
	}
}

// Opción inválida por debajo del tope: aviso + prompt DEL NODO, contador + 1, sin transición.
func TestModule_Step_InvalidOptionReprompts(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "out of range number", input: "9"},
		{name: "empty input", input: ""},
		{name: "only spaces", input: "   "},
		{name: "non ascii digit", input: "١"},
		{name: "fullwidth digit", input: "１"},
		{name: "key with inner text", input: "1 a"},
		{name: "zero width space is not trimmed", input: "1\u200b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := menu.New().Step(menuNode(), model.Conversation{}, tc.input)
			if res.Next != nil {
				t.Fatalf("Next = %q, quiero nil: una opción inválida no transiciona", *res.Next)
			}
			if !maps.Equal(res.Vars, map[string]any{menu.RepromptKey: 1}) {
				t.Errorf("Vars = %v, quiero solo el contador a 1 (fuera de evento no hay sello)", res.Vars)
			}
			want := invalidNotice + menuNode().Prompt
			if len(res.Outputs) != 1 || res.Outputs[0] != want {
				t.Errorf("Outputs = %q, quiero [%q]", res.Outputs, want)
			}
			if len(res.Effects) != 0 {
				t.Errorf("Effects = %+v, quiero ninguno", res.Effects)
			}
		})
	}
}

// El contador sobrevive al round-trip por JSON (float64) y se sella con el evento activo.
func TestModule_Step_CounterToleratesJSONAndIsSealed(t *testing.T) {
	conv := model.Conversation{EventID: "ev-1", Vars: map[string]any{
		menu.RepromptKey: float64(1),
		modules.RepromptEventKey(menu.RepromptKey): "ev-1",
	}}
	res := menu.New().Step(menuNode(), conv, "9")
	want := map[string]any{
		menu.RepromptKey: 2,
		modules.RepromptEventKey(menu.RepromptKey): "ev-1",
	}
	if !maps.Equal(res.Vars, want) {
		t.Errorf("Vars = %v, quiero %v", res.Vars, want)
	}
}

// Tercer inválido FUERA de un evento: ayuda clásica, contador a cero, sin menú de salida.
func TestModule_Step_ThirdInvalidOutsideEventSendsHelp(t *testing.T) {
	m, node := menu.New(), menuNode()
	conv := model.Conversation{}
	var res modules.Result
	for range menu.MaxReprompts {
		res = m.Step(node, conv, "x")
		conv.Vars = res.Vars
	}
	if res.Next != nil {
		t.Fatalf("Next = %q, quiero nil: la ayuda permanece en el nodo", *res.Next)
	}
	want := helpNotice + node.Prompt
	if len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Errorf("Outputs = %q, quiero [%q]", res.Outputs, want)
	}
	if len(res.Vars) != 0 {
		t.Errorf("Vars = %v, quiero vacío: contador reiniciado y ningún menú de salida armado", res.Vars)
	}
}

// Tercer inválido DENTRO de un evento: arma el menú de salida sobre el prompt del nodo.
func TestModule_Step_ThirdInvalidInsideEventArmsExitMenu(t *testing.T) {
	m, node := menu.New(), menuNode()
	conv := model.Conversation{EventID: "ev-menu-1"}
	var res modules.Result
	for range menu.MaxReprompts {
		res = m.Step(node, conv, "x")
		conv.Vars = res.Vars
	}
	if res.Next != nil {
		t.Fatalf("Next = %q, quiero nil: el menú de salida no transiciona", *res.Next)
	}
	wantVars := map[string]any{
		modules.ExitMenuVar:      node.Prompt,
		modules.ExitMenuEventVar: "ev-menu-1",
	}
	if !maps.Equal(res.Vars, wantVars) {
		t.Errorf("Vars = %v, quiero %v (marcador armado, contador y sello borrados)", res.Vars, wantVars)
	}
	if want := modules.ExitMenuText(node.Prompt); len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Errorf("Outputs = %q, quiero [%q]", res.Outputs, want)
	}
}

// Un contador cargado en OTRO evento vale 0: el inválido es el primero de este.
func TestModule_Step_CounterFromAnotherEventIsStale(t *testing.T) {
	conv := model.Conversation{EventID: "ev-b", Vars: map[string]any{
		menu.RepromptKey: 2,
		modules.RepromptEventKey(menu.RepromptKey): "ev-a",
	}}
	res := menu.New().Step(menuNode(), conv, "x")
	if res.Vars[menu.RepromptKey] != 1 || res.Vars[modules.RepromptEventKey(menu.RepromptKey)] != "ev-b" {
		t.Errorf("Vars = %v, quiero el contador a 1 sellado con ev-b", res.Vars)
	}
	if _, armed := res.Vars[modules.ExitMenuVar]; armed {
		t.Error("un contador de otro evento armó el menú de salida")
	}
}

// Step es puro: no muta las Vars de la conversación en ninguna rama.
func TestModule_Step_DoesNotMutateInputVars(t *testing.T) {
	for _, input := range []string{"1", "9"} {
		in := map[string]any{menu.RepromptKey: 1, "other": "x"}
		before := maps.Clone(in)
		_ = menu.New().Step(menuNode(), model.Conversation{Vars: in}, input)
		if !maps.Equal(in, before) {
			t.Errorf("entrada %q: Vars de entrada = %v, quiero %v intactas", input, in, before)
		}
	}
}

// El reprompt re-emite el prompt del NODO: Step no ve el contenido resuelto. Un nodo
// sin opciones trata toda entrada como inválida.
func TestModule_Step_NodeWithoutOptions(t *testing.T) {
	node := model.Node{Type: model.NodeTypeMenu, Prompt: "Sin opciones"}
	res := menu.New().Step(node, model.Conversation{}, "1")
	if res.Next != nil || len(res.Outputs) != 1 || res.Outputs[0] != invalidNotice+"Sin opciones" {
		t.Errorf("Result = %+v, quiero el reprompt sobre el prompt del nodo", res)
	}
}
