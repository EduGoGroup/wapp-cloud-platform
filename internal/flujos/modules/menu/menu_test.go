package menu_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

func menuNode() model.Node {
	return model.Node{
		Type:    model.NodeTypeMenu,
		Prompt:  "Elige:\n1) A\n2) B",
		Options: map[string]string{"1": "a", "2": "b"},
	}
}

func TestModuleType(t *testing.T) {
	if got := menu.New().Type(); got != model.NodeTypeMenu {
		t.Fatalf("Type() = %q, quiero %q", got, model.NodeTypeMenu)
	}
}

func TestModuleRender(t *testing.T) {
	out := menu.New().Render(menuNode())
	if len(out) != 1 || out[0] != menuNode().Prompt {
		t.Fatalf("Render debe devolver el prompt tal cual; got=%q", out)
	}
}

func TestModuleStep(t *testing.T) {
	m := menu.New()

	t.Run("opción válida transiciona y reinicia contador", func(t *testing.T) {
		conv := model.Conversation{Vars: map[string]any{menu.RepromptKey: 2}}
		res := m.Step(menuNode(), conv, " 2 ")
		if res.Next == nil || *res.Next != "b" {
			t.Fatalf("esperaba Next=b, got=%v", res.Next)
		}
		if _, ok := res.Vars[menu.RepromptKey]; ok {
			t.Fatalf("la transición debe borrar el contador de reprompt")
		}
		if len(res.Outputs) != 0 {
			t.Fatalf("transición no produce outputs propios; got=%q", res.Outputs)
		}
	})

	t.Run("opción inválida incrementa contador y reprompt", func(t *testing.T) {
		res := m.Step(menuNode(), model.Conversation{}, "9")
		if res.Next != nil {
			t.Fatalf("opción inválida no transiciona")
		}
		if res.Vars[menu.RepromptKey] != 1 {
			t.Fatalf("contador = %v, quiero 1", res.Vars[menu.RepromptKey])
		}
		if len(res.Outputs) != 1 || !strings.Contains(res.Outputs[0], "Elige:") {
			t.Fatalf("reprompt debe reincluir el prompt; got=%q", res.Outputs)
		}
	})

	t.Run("tercer inválido envía ayuda y reinicia", func(t *testing.T) {
		conv := model.Conversation{Vars: map[string]any{menu.RepromptKey: menu.MaxReprompts - 1}}
		res := m.Step(menuNode(), conv, "9")
		if res.Next != nil {
			t.Fatalf("ayuda permanece en el nodo")
		}
		if _, ok := res.Vars[menu.RepromptKey]; ok {
			t.Fatalf("tras la ayuda el contador debe reiniciarse")
		}
		if len(res.Outputs) != 1 || !strings.Contains(res.Outputs[0], "elige una de las opciones") {
			t.Fatalf("esperaba mensaje de ayuda; got=%q", res.Outputs)
		}
	})
}
