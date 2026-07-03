package survey_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
)

func surveyNode() model.Node {
	return model.Node{
		Type:       model.NodeTypeSurveyQuestion,
		QuestionID: "q2",
		Prompt:     "¿Qué tal?\n1) Bien\n2) Mal",
		Options:    map[string]string{"1": "n_next", "2": "n_next"},
	}
}

func answersOf(vars map[string]any) map[string]string {
	out := map[string]string{}
	switch m := vars["answers"].(type) {
	case map[string]string:
		for k, v := range m {
			out[k] = v
		}
	case map[string]any:
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func TestModuleType(t *testing.T) {
	if got := survey.New().Type(); got != model.NodeTypeSurveyQuestion {
		t.Fatalf("Type() = %q, quiero %q", got, model.NodeTypeSurveyQuestion)
	}
}

func TestModuleWaitsForInput(t *testing.T) {
	if !survey.New().WaitsForInput() {
		t.Fatalf("survey_question debe esperar entrada del usuario")
	}
}

func TestModuleRender(t *testing.T) {
	// El engine entrega el contenido YA resuelto; en T0 es un placeholder inline
	// con content.Prompt == node.Prompt, de observable idéntico (byte-a-byte).
	content := model.Content{Prompt: surveyNode().Prompt, Options: surveyNode().Options}
	out := survey.New().Render(surveyNode(), content)
	if len(out) != 1 || out[0] != surveyNode().Prompt {
		t.Fatalf("Render debe devolver el prompt tal cual; got=%q", out)
	}
}

func TestStepValidRegistersAnswerAndTransitions(t *testing.T) {
	m := survey.New()
	// Respuesta previa de otra pregunta que NO debe pisarse.
	conv := model.Conversation{Vars: map[string]any{
		"answers":          map[string]any{"q1": "1"},
		survey.RepromptKey: 2,
	}}
	res := m.Step(surveyNode(), conv, " 2 ")

	if res.Next == nil || *res.Next != "n_next" {
		t.Fatalf("esperaba Next=n_next, got=%v", res.Next)
	}
	if _, ok := res.Vars[survey.RepromptKey]; ok {
		t.Fatalf("la transición debe borrar el contador de reprompt")
	}
	if len(res.Outputs) != 0 {
		t.Fatalf("transición no produce outputs propios; got=%q", res.Outputs)
	}

	answers := answersOf(res.Vars)
	if answers["q2"] != "2" {
		t.Fatalf("answer_code de q2 debe ser la clave elegida '2'; got=%q", answers["q2"])
	}
	if answers["q1"] != "1" {
		t.Fatalf("la respuesta previa q1 no debe pisarse; got=%q", answers["q1"])
	}

	// La rama válida DECLARA exactamente 1 Effect{persist,survey_answer} con la
	// pregunta y el answer_code correctos (Plan 015 · T3). El módulo es PURO: solo
	// lo anota; no persiste.
	if len(res.Effects) != 1 {
		t.Fatalf("la opción válida debe declarar 1 efecto; got=%d (%+v)", len(res.Effects), res.Effects)
	}
	eff := res.Effects[0]
	if eff.Kind != "persist" || eff.Name != "survey_answer" {
		t.Fatalf("efecto inesperado: kind=%q name=%q", eff.Kind, eff.Name)
	}
	if qid, ok := eff.Payload["question_id"].(string); !ok || qid != "q2" {
		t.Fatalf("question_id del efecto = %v, quiero q2", eff.Payload["question_id"])
	}
	if code, ok := eff.Payload["answer_code"].(string); !ok || code != "2" {
		t.Fatalf("answer_code del efecto = %v, quiero 2 (la clave elegida)", eff.Payload["answer_code"])
	}
}

func TestStepValidDoesNotMutateInput(t *testing.T) {
	m := survey.New()
	in := map[string]any{"answers": map[string]any{"q1": "1"}}
	conv := model.Conversation{Vars: in}
	_ = m.Step(surveyNode(), conv, "1")

	// El mapa de entrada NO debe haber recibido la respuesta de q2.
	prev, ok := in["answers"].(map[string]any)
	if !ok {
		t.Fatalf("answers de entrada debía seguir siendo map[string]any; got=%T", in["answers"])
	}
	if _, mutated := prev["q2"]; mutated {
		t.Fatalf("Step no debe mutar el Vars de entrada; answers previo mutado=%v", prev)
	}
}

func TestStepInvalidReprompt(t *testing.T) {
	m := survey.New()
	res := m.Step(surveyNode(), model.Conversation{}, "9")
	if res.Next != nil {
		t.Fatalf("opción inválida no transiciona")
	}
	if res.Vars[survey.RepromptKey] != 1 {
		t.Fatalf("contador = %v, quiero 1", res.Vars[survey.RepromptKey])
	}
	if _, ok := res.Vars["answers"]; ok {
		t.Fatalf("una opción inválida no debe registrar respuesta")
	}
	if len(res.Effects) != 0 {
		t.Fatalf("la rama de reprompt NO debe declarar efectos; got=%+v", res.Effects)
	}
	if len(res.Outputs) != 1 || !strings.Contains(res.Outputs[0], "¿Qué tal?") {
		t.Fatalf("reprompt debe reincluir el prompt; got=%q", res.Outputs)
	}
}

func TestStepInvalidExhaustsStays(t *testing.T) {
	m := survey.New()
	conv := model.Conversation{Vars: map[string]any{survey.RepromptKey: survey.MaxReprompts - 1}}
	res := m.Step(surveyNode(), conv, "9")
	if res.Next != nil {
		t.Fatalf("al agotar los intentos permanece en el nodo")
	}
	if _, ok := res.Vars[survey.RepromptKey]; ok {
		t.Fatalf("tras la ayuda el contador debe reiniciarse")
	}
	if len(res.Effects) != 0 {
		t.Fatalf("la rama de ayuda (intentos agotados) NO debe declarar efectos; got=%+v", res.Effects)
	}
	if len(res.Outputs) != 1 || !strings.Contains(res.Outputs[0], "elige una de las opciones") {
		t.Fatalf("esperaba mensaje de ayuda; got=%q", res.Outputs)
	}
}

func TestStepGetIntToleratesFloat64(t *testing.T) {
	m := survey.New()
	// Round-trip JSON revive el contador como float64.
	conv := model.Conversation{Vars: map[string]any{survey.RepromptKey: float64(1)}}
	res := m.Step(surveyNode(), conv, "9")
	// float64(1)+1 = 2 < MaxReprompts(3) → reprompt con contador 2.
	if res.Vars[survey.RepromptKey] != 2 {
		t.Fatalf("float64(1) debe contar como 1; contador resultante = %v, quiero 2", res.Vars[survey.RepromptKey])
	}
}
