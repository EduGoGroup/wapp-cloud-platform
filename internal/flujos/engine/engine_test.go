package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

func ptr(s string) *string { return &s }

func newEngine() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	return engine.New(reg)
}

// mustEnter ejecuta Enter y falla el test si hay error (azúcar para el setup).
func mustEnter(t *testing.T, e *engine.Engine, f model.Flow) model.Conversation {
	t.Helper()
	st, _, err := e.Enter(f, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	return st
}

// mustStep ejecuta Step y falla el test si hay error.
func mustStep(t *testing.T, e *engine.Engine, f model.Flow, st model.Conversation, in string) model.Conversation {
	t.Helper()
	st2, _, err := e.Step(f, st, engine.Input{Text: in})
	if err != nil {
		t.Fatalf("Step(%q): %v", in, err)
	}
	return st2
}

const rootPrompt = "¿En qué te ayudo?\n1) Ventas\n2) Soporte"

// flowMenu: menú raíz con dos opciones que llevan a mensajes terminales.
func flowMenu() model.Flow {
	return model.Flow{
		FlowID:  "menu-soporte",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  rootPrompt,
				Options: map[string]string{"1": "ventas", "2": "soporte"},
			},
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas.", Next: nil},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame tu problema.", Next: nil},
		},
	}
}

// flowChain: inicio en message que encadena message->message->fin.
func flowChain() model.Flow {
	return model.Flow{
		FlowID:  "bienvenida",
		Version: 1,
		Initial: "m1",
		Nodes: map[string]model.Node{
			"m1": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("m2")},
			"m2": {Type: model.NodeTypeMessage, Text: "Bienvenido.", Next: ptr("m3")},
			"m3": {Type: model.NodeTypeMessage, Text: "Adiós.", Next: nil},
		},
	}
}

// flowChainToMenu: message inicial que encadena hasta un menú.
func flowChainToMenu() model.Flow {
	return model.Flow{
		FlowID:  "intro-menu",
		Version: 1,
		Initial: "intro",
		Nodes: map[string]model.Node{
			"intro": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("root")},
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  rootPrompt,
				Options: map[string]string{"1": "ventas"},
			},
			"ventas": {Type: model.NodeTypeMessage, Text: "Ventas.", Next: nil},
		},
	}
}

func texts(outs []engine.Output) []string {
	got := make([]string, len(outs))
	for i, o := range outs {
		got[i] = o.Text
	}
	return got
}

func TestEnter(t *testing.T) {
	cases := []struct {
		name         string
		flow         model.Flow
		wantOuts     []string
		wantNode     string
		wantFinished bool
	}{
		{
			name:     "inicial menu produce el prompt y espera",
			flow:     flowMenu(),
			wantOuts: []string{rootPrompt},
			wantNode: "root",
		},
		{
			name:         "inicial message encadena hasta el fin",
			flow:         flowChain(),
			wantOuts:     []string{"Hola.", "Bienvenido.", "Adiós."},
			wantNode:     model.NodeTerminal,
			wantFinished: true,
		},
		{
			name:     "inicial message encadena hasta un menu",
			flow:     flowChainToMenu(),
			wantOuts: []string{"Hola.", rootPrompt},
			wantNode: "root",
		},
	}

	e := newEngine()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, outs, err := e.Enter(tc.flow, model.Conversation{})
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if got := texts(outs); !slices.Equal(got, tc.wantOuts) {
				t.Fatalf("outs = %q, quiero %q", got, tc.wantOuts)
			}
			if st.CurrentNode != tc.wantNode {
				t.Fatalf("CurrentNode = %q, quiero %q", st.CurrentNode, tc.wantNode)
			}
			if st.Finished() != tc.wantFinished {
				t.Fatalf("Finished() = %v, quiero %v", st.Finished(), tc.wantFinished)
			}
			if st.FlowID != tc.flow.FlowID || st.FlowVersion != tc.flow.Version {
				t.Fatalf("Enter no fijó flow_id/version: %+v", st)
			}
		})
	}
}

func TestStepValidOptionAdvances(t *testing.T) {
	e := newEngine()
	flow := flowMenu()
	st := mustEnter(t, e, flow)

	st2, outs, err := e.Step(flow, st, engine.Input{Text: "2"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{"Cuéntame tu problema."}) {
		t.Fatalf("outs = %q, quiero el texto destino", got)
	}
	if !st2.Finished() {
		t.Fatalf("tras opción válida a message terminal, esperaba Finished; node=%q", st2.CurrentNode)
	}
}

func TestStepValidOptionTrimsInput(t *testing.T) {
	e := newEngine()
	flow := flowMenu()
	st := mustEnter(t, e, flow)

	st2, outs, err := e.Step(flow, st, engine.Input{Text: "  1 \n"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{"Te paso con Ventas."}) {
		t.Fatalf("outs = %q, quiero el texto de ventas (trim)", got)
	}
	if !st2.Finished() {
		t.Fatalf("esperaba Finished tras opción válida")
	}
}

func TestStepInvalidOptionReprompts(t *testing.T) {
	e := newEngine()
	flow := flowMenu()
	st := mustEnter(t, e, flow)

	st2, outs, err := e.Step(flow, st, engine.Input{Text: "9"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(outs) != 1 || !strings.Contains(outs[0].Text, rootPrompt) {
		t.Fatalf("reprompt debe reincluir el prompt; outs=%q", texts(outs))
	}
	if st2.CurrentNode != "root" {
		t.Fatalf("opción inválida NO debe avanzar; node=%q", st2.CurrentNode)
	}
	if got := getCount(t, st2); got != 1 {
		t.Fatalf("contador reprompt = %d, quiero 1", got)
	}
}

func TestStepThirdInvalidSendsHelpAndStays(t *testing.T) {
	e := newEngine()
	flow := flowMenu()
	st := mustEnter(t, e, flow)

	var outs []engine.Output
	for i := 0; i < 3; i++ {
		var err error
		st, outs, err = e.Step(flow, st, engine.Input{Text: "x"})
		if err != nil {
			t.Fatalf("Step #%d: %v", i+1, err)
		}
	}
	// 3er intento → mensaje de ayuda, permanece, contador reiniciado.
	if len(outs) != 1 || !strings.Contains(outs[0].Text, "elige una de las opciones") {
		t.Fatalf("3er inválido debe enviar ayuda; outs=%q", texts(outs))
	}
	if st.CurrentNode != "root" {
		t.Fatalf("3er inválido debe permanecer en root; node=%q", st.CurrentNode)
	}
	if got := getCount(t, st); got != 0 {
		t.Fatalf("tras ayuda el contador debe reiniciarse a 0, fue %d", got)
	}
}

func TestStepTransitionResetsRepromptCounter(t *testing.T) {
	e := newEngine()
	// Flujo con dos menús encadenados por opción para poder transicionar y
	// volver a evaluar el contador en el destino.
	flow := model.Flow{
		FlowID:  "dos-menus",
		Version: 1,
		Initial: "a",
		Nodes: map[string]model.Node{
			"a": {Type: model.NodeTypeMenu, Prompt: "A?", Options: map[string]string{"1": "b"}},
			"b": {Type: model.NodeTypeMenu, Prompt: "B?", Options: map[string]string{"1": "c"}},
			"c": {Type: model.NodeTypeMessage, Text: "fin", Next: nil},
		},
	}
	st := mustEnter(t, e, flow)

	// Un intento inválido en 'a' → contador 1.
	st = mustStep(t, e, flow, st, "z")
	if getCount(t, st) != 1 {
		t.Fatalf("esperaba contador 1 antes de transicionar")
	}
	// Opción válida → transición a 'b'; el contador debe reiniciarse.
	st = mustStep(t, e, flow, st, "1")
	if st.CurrentNode != "b" {
		t.Fatalf("esperaba estar en 'b', estoy en %q", st.CurrentNode)
	}
	if getCount(t, st) != 0 {
		t.Fatalf("la transición debe reiniciar el contador; fue %d", getCount(t, st))
	}
}

func TestStepOnTerminalIsNeutral(t *testing.T) {
	e := newEngine()
	flow := flowMenu()
	st := mustEnter(t, e, flow)
	st = mustStep(t, e, flow, st, "1") // → terminal
	if !st.Finished() {
		t.Fatalf("precondición: debía estar terminado")
	}
	st2, outs, err := e.Step(flow, st, engine.Input{Text: "cualquier cosa"})
	if err != nil {
		t.Fatalf("Step terminal: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("entrada en nodo terminal debe ser neutra; outs=%q", texts(outs))
	}
	if !st2.Finished() {
		t.Fatalf("debe seguir terminado")
	}
}

func getCount(t *testing.T, st model.Conversation) int {
	t.Helper()
	v, ok := st.Vars[menu.RepromptKey]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		t.Fatalf("contador de tipo inesperado %T", v)
		return 0
	}
}
