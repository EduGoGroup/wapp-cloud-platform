package modules_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

func numberedNode() model.Node {
	return model.Node{Prompt: "Elige:\n1) A\n2) B", Options: map[string]string{"1": "a", "2": "b"}}
}

// TestNumberedStep cubre la base común de opción numerada extraída de menú/encuesta
// (Plan 027 · Ola 2 · T9): opción válida delega en onValid; inválida hace reprompt;
// al agotar los intentos manda ayuda y reinicia. La conducta observable (mensajes,
// tolerancia de TrimSpace, contador) es la que ya validan menu_test/survey_test.
func TestNumberedStep(t *testing.T) {
	t.Parallel()
	const key = "rk"

	t.Run("válida delega en onValid y borra contador", func(t *testing.T) {
		t.Parallel()
		conv := model.Conversation{Vars: map[string]any{key: 2}}
		called := false
		res := modules.NumberedStep(numberedNode(), conv, " 2 ", key, 3,
			func(vars map[string]any, choice, target string) modules.Result {
				called = true
				if choice != "2" || target != "b" {
					t.Fatalf("onValid recibió choice=%q target=%q, quiero 2/b", choice, target)
				}
				if _, ok := vars[key]; ok {
					t.Fatal("el contador debe estar borrado al entrar a onValid")
				}
				dest := target
				return modules.Result{Next: &dest, Vars: vars}
			})
		if !called {
			t.Fatal("onValid no se invocó con una opción válida")
		}
		if res.Next == nil || *res.Next != "b" {
			t.Fatalf("Next = %v, quiero b", res.Next)
		}
	})

	t.Run("inválida incrementa el contador y reprompt", func(t *testing.T) {
		t.Parallel()
		res := modules.NumberedStep(numberedNode(), model.Conversation{}, "9", key, 3, failOnValid(t))
		if res.Next != nil {
			t.Fatal("una opción inválida no transiciona")
		}
		if res.Vars[key] != 1 {
			t.Fatalf("contador = %v, quiero 1", res.Vars[key])
		}
		if len(res.Outputs) != 1 {
			t.Fatalf("reprompt debe emitir 1 salida; got=%q", res.Outputs)
		}
	})

	t.Run("float64 del round-trip cuenta como int", func(t *testing.T) {
		t.Parallel()
		conv := model.Conversation{Vars: map[string]any{key: float64(1)}}
		res := modules.NumberedStep(numberedNode(), conv, "9", key, 3, failOnValid(t))
		if res.Vars[key] != 2 {
			t.Fatalf("float64(1)+1 debe ser 2; got=%v", res.Vars[key])
		}
	})

	t.Run("al agotar intentos manda ayuda y reinicia", func(t *testing.T) {
		t.Parallel()
		conv := model.Conversation{Vars: map[string]any{key: 2}} // 2+1 >= 3
		res := modules.NumberedStep(numberedNode(), conv, "9", key, 3, failOnValid(t))
		if res.Next != nil {
			t.Fatal("la ayuda permanece en el nodo")
		}
		if _, ok := res.Vars[key]; ok {
			t.Fatal("tras la ayuda el contador debe reiniciarse")
		}
	})
}

func failOnValid(t *testing.T) func(map[string]any, string, string) modules.Result {
	return func(map[string]any, string, string) modules.Result {
		t.Helper()
		t.Fatal("onValid no debe invocarse en la rama inválida")
		return modules.Result{}
	}
}

func TestCloneVarsAndGetInt(t *testing.T) {
	t.Parallel()
	in := map[string]any{"a": 1}
	out := modules.CloneVars(in)
	out["b"] = 2
	if _, mutated := in["b"]; mutated {
		t.Fatal("CloneVars no debe mutar el mapa de entrada")
	}
	if modules.GetInt(map[string]any{"n": float64(3)}, "n") != 3 {
		t.Fatal("GetInt debe tolerar float64 del round-trip JSON")
	}
	if modules.GetInt(map[string]any{}, "ausente") != 0 {
		t.Fatal("GetInt de clave ausente debe ser 0")
	}
}
