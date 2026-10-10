package survey_test

import (
	"maps"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
)

// Aserción de compilación: la encuesta es un modules.Module (y su valor cero también).
var (
	_ modules.Module = survey.New()
	_ modules.Module = survey.Module{}
)

const (
	invalidNotice = "Opción no válida. Responde con el número de una de las opciones.\n\n"
	helpNotice    = "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n"
)

func surveyNode() model.Node {
	return model.Node{
		Type:       model.NodeTypeSurveyQuestion,
		QuestionID: "q2",
		Prompt:     "¿Qué tal?\n1) Bien\n2) Mal",
		Options:    map[string]string{"1": "n_good", "2": "n_bad"},
	}
}

// answersOf lee Vars["answers"] exigiendo el tipo que el contrato promete.
func answersOf(t *testing.T, vars map[string]any) map[string]string {
	t.Helper()
	answers, ok := vars["answers"].(map[string]string)
	if !ok {
		t.Fatalf("Vars[\"answers\"] es %T, quiero map[string]string", vars["answers"])
	}
	return answers
}

// Las claves y el tope son observables: viven en el JSONB de flow_state.vars.
func TestConstants(t *testing.T) {
	if survey.RepromptKey != "survey_reprompt" {
		t.Errorf("RepromptKey = %q, quiero \"survey_reprompt\"", survey.RepromptKey)
	}
	if survey.MaxReprompts != 3 {
		t.Errorf("MaxReprompts = %d, quiero 3", survey.MaxReprompts)
	}
}

func TestModule_Identity(t *testing.T) {
	m := survey.New()
	if got := m.Type(); got != "survey_question" {
		t.Errorf("Type() = %q, quiero \"survey_question\"", got)
	}
	if !m.WaitsForInput() {
		t.Error("WaitsForInput() = false, quiero true: la pregunta espera la opción")
	}
	if !m.ProducesDurableContent() {
		t.Error("ProducesDurableContent() = false, quiero true: la encuesta proyecta a survey_results")
	}
}

// Render devuelve el prompt del contenido RESUELTO, no el del nodo, y siempre un texto.
func TestModule_Render(t *testing.T) {
	m := survey.New()
	cases := []struct {
		name    string
		content model.Content
		want    string
	}{
		{name: "resolved prompt wins over node prompt", content: model.Content{Prompt: "Resuelta\n1) Sí"}, want: "Resuelta\n1) Sí"},
		{name: "empty content yields one empty text", content: model.Content{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := m.Render(surveyNode(), tc.content)
			if len(out) != 1 || out[0] != tc.want {
				t.Errorf("Render = %q, quiero [%q]", out, tc.want)
			}
		})
	}
}

// Opción válida: anota la respuesta sin pisar las previas, transiciona, borra el
// contador y declara exactamente el efecto survey_answer.
func TestModule_Step_ValidOptionRecordsAnswer(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		prior       any
		wantNext    string
		wantAnswers map[string]string
	}{
		{
			name: "first answer, no prior map", input: "1", prior: nil,
			wantNext: "n_good", wantAnswers: map[string]string{"q2": "1"},
		},
		{
			name: "trimmed input, prior answers revived from json", input: " 2 ",
			prior:    map[string]any{"q1": "1"},
			wantNext: "n_bad", wantAnswers: map[string]string{"q1": "1", "q2": "2"},
		},
		{
			name: "native prior map, same question is overwritten", input: "\u00a01\u3000",
			prior:    map[string]string{"q1": "3", "q2": "2"},
			wantNext: "n_good", wantAnswers: map[string]string{"q1": "3", "q2": "1"},
		},
		{
			name: "non string prior values are dropped", input: "1",
			prior:    map[string]any{"q0": 7, "q1": "1"},
			wantNext: "n_good", wantAnswers: map[string]string{"q1": "1", "q2": "1"},
		},
		{
			name: "prior answers that are not a map are replaced", input: "2",
			prior:    "basura",
			wantNext: "n_bad", wantAnswers: map[string]string{"q2": "2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]any{survey.RepromptKey: 2, "other": "se conserva"}
			if tc.prior != nil {
				vars["answers"] = tc.prior
			}
			res := survey.New().Step(surveyNode(), model.Conversation{Vars: vars}, tc.input)

			if res.Next == nil || *res.Next != tc.wantNext {
				t.Fatalf("Next = %v, quiero %q", res.Next, tc.wantNext)
			}
			if got := answersOf(t, res.Vars); !maps.Equal(got, tc.wantAnswers) {
				t.Errorf("answers = %v, quiero %v", got, tc.wantAnswers)
			}
			if _, kept := res.Vars[survey.RepromptKey]; kept {
				t.Error("la transición no borró el contador de reprompt")
			}
			if res.Vars["other"] != "se conserva" || len(res.Vars) != 2 {
				t.Errorf("Vars = %v, quiero solo answers y la clave ajena", res.Vars)
			}
			if len(res.Outputs) != 0 || res.Query != nil || res.Outcome != model.OutcomeUndeclared {
				t.Errorf("Result = %+v, quiero sin salidas, consulta ni desenlace", res)
			}

			choice := tc.wantAnswers["q2"]
			want := []modules.Effect{{
				Kind:    "persist",
				Name:    survey.EffectSurveyAnswer,
				Payload: map[string]any{"question_id": "q2", "answer_code": choice},
			}}
			if !reflect.DeepEqual(res.Effects, want) {
				t.Errorf("Effects = %+v, quiero %+v", res.Effects, want)
			}
		})
	}
}

// Step es puro: ni las Vars de entrada ni su mapa de respuestas reciben la respuesta nueva.
func TestModule_Step_DoesNotMutateInputVars(t *testing.T) {
	cases := []struct {
		name  string
		prior any
	}{
		{name: "json revived map", prior: map[string]any{"q1": "1"}},
		{name: "native map", prior: map[string]string{"q1": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"answers": tc.prior, survey.RepromptKey: 1}
			_ = survey.New().Step(surveyNode(), model.Conversation{Vars: in}, "1")
			if in[survey.RepromptKey] != 1 || len(in) != 2 {
				t.Errorf("Vars de entrada = %v, quiero intactas", in)
			}
			want := any(map[string]string{"q1": "1"})
			if _, isAny := tc.prior.(map[string]any); isAny {
				want = map[string]any{"q1": "1"}
			}
			if !reflect.DeepEqual(in["answers"], want) {
				t.Errorf("answers de entrada = %v, quiero %v sin la respuesta nueva", in["answers"], want)
			}
		})
	}
}

// Un QuestionID vacío no se rechaza aquí: la respuesta queda bajo la clave "".
func TestModule_Step_EmptyQuestionID(t *testing.T) {
	node := surveyNode()
	node.QuestionID = ""
	res := survey.New().Step(node, model.Conversation{}, "1")
	if got := answersOf(t, res.Vars); !maps.Equal(got, map[string]string{"": "1"}) {
		t.Errorf("answers = %v, quiero la respuesta bajo la clave vacía", got)
	}
	if len(res.Effects) != 1 || res.Effects[0].Payload["question_id"] != "" {
		t.Errorf("Effects = %+v, quiero un efecto con question_id vacío", res.Effects)
	}
}

// Opción inválida por debajo del tope: aviso + prompt del nodo, contador + 1, sin
// respuesta ni efecto.
func TestModule_Step_InvalidOptionReprompts(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "out of range number", input: "9"},
		{name: "empty input", input: ""},
		{name: "only spaces", input: " \t "},
		{name: "non ascii digit", input: "٢"},
		{name: "fullwidth digit", input: "２"},
		{name: "zero width space is not trimmed", input: "2\u200b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := survey.New().Step(surveyNode(), model.Conversation{}, tc.input)
			if res.Next != nil {
				t.Fatalf("Next = %q, quiero nil: una opción inválida no transiciona", *res.Next)
			}
			if !maps.Equal(res.Vars, map[string]any{survey.RepromptKey: 1}) {
				t.Errorf("Vars = %v, quiero solo el contador a 1, sin answers", res.Vars)
			}
			want := invalidNotice + surveyNode().Prompt
			if len(res.Outputs) != 1 || res.Outputs[0] != want {
				t.Errorf("Outputs = %q, quiero [%q]", res.Outputs, want)
			}
			if len(res.Effects) != 0 {
				t.Errorf("Effects = %+v, quiero ninguno", res.Effects)
			}
		})
	}
}

// El contador revivido de JSON (float64) cuenta, y el de la encuesta no es el del menú.
func TestModule_Step_CounterToleratesJSONAndIsOwn(t *testing.T) {
	conv := model.Conversation{Vars: map[string]any{survey.RepromptKey: float64(1), "menu_reprompt": 2}}
	res := survey.New().Step(surveyNode(), conv, "9")
	if res.Vars[survey.RepromptKey] != 2 {
		t.Errorf("contador = %v, quiero 2: float64(1) cuenta como 1", res.Vars[survey.RepromptKey])
	}
	if res.Vars["menu_reprompt"] != 2 {
		t.Errorf("menu_reprompt = %v, quiero 2: el contador del menú no se toca", res.Vars["menu_reprompt"])
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != invalidNotice+surveyNode().Prompt {
		t.Errorf("Outputs = %q, quiero el reprompt y no la ayuda", res.Outputs)
	}
}

// Tercer inválido FUERA de un evento: ayuda clásica, contador a cero, sin menú de
// salida, sin respuesta y sin efecto.
func TestModule_Step_ThirdInvalidOutsideEventSendsHelp(t *testing.T) {
	m, node := survey.New(), surveyNode()
	conv := model.Conversation{}
	var res modules.Result
	for range survey.MaxReprompts {
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
		t.Errorf("Vars = %v, quiero vacío: sin contador, sin menú de salida y sin answers", res.Vars)
	}
	if len(res.Effects) != 0 {
		t.Errorf("Effects = %+v, quiero ninguno", res.Effects)
	}
}

// Tercer inválido DENTRO de un evento: arma el menú de salida sobre el prompt del nodo.
func TestModule_Step_ThirdInvalidInsideEventArmsExitMenu(t *testing.T) {
	m, node := survey.New(), surveyNode()
	conv := model.Conversation{EventID: "ev-survey-1"}
	var res modules.Result
	for range survey.MaxReprompts {
		res = m.Step(node, conv, "x")
		conv.Vars = res.Vars
	}
	if res.Next != nil {
		t.Fatalf("Next = %q, quiero nil: el menú de salida no transiciona", *res.Next)
	}
	wantVars := map[string]any{
		modules.ExitMenuVar:      node.Prompt,
		modules.ExitMenuEventVar: "ev-survey-1",
	}
	if !maps.Equal(res.Vars, wantVars) {
		t.Errorf("Vars = %v, quiero %v (marcador armado, contador y sello borrados)", res.Vars, wantVars)
	}
	if want := modules.ExitMenuText(node.Prompt); len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Errorf("Outputs = %q, quiero [%q]", res.Outputs, want)
	}
	if len(res.Effects) != 0 {
		t.Errorf("Effects = %+v, quiero ninguno", res.Effects)
	}
}
