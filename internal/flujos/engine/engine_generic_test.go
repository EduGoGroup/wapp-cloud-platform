package engine_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

// fakeSurvey es un módulo de test que implementa modules.Module para el tipo
// "survey_question". Es trivial (render = prompt; step = transición por opción)
// y existe solo para validar en T0 que el engine recorre la MISMA ruta genérica
// que el menú, sin depender del módulo real (que llega en T1).
type fakeSurvey struct{}

func (fakeSurvey) Type() string        { return model.NodeTypeSurveyQuestion }
func (fakeSurvey) WaitsForInput() bool { return true }

func (fakeSurvey) Render(node model.Node) []string { return []string{node.Prompt} }

func (fakeSurvey) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	if target, ok := node.Options[strings.TrimSpace(input)]; ok {
		dest := target
		return modules.Result{Next: &dest, Vars: conv.Vars}
	}
	return modules.Result{Vars: conv.Vars, Outputs: []string{"opción no válida"}}
}

// newEngineWithSurvey construye un engine con el menú real y el survey fake.
func newEngineWithSurvey() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(fakeSurvey{})
	return engine.New(reg)
}

const surveyPrompt = "¿Nos recomendarías?\n1) Sí\n2) No"

// flowSurvey: nodo survey_question inicial con dos opciones a mensajes terminales.
// Estructuralmente idéntico a flowMenu: prueba que la ruta del engine es la misma.
func flowSurvey() model.Flow {
	return model.Flow{
		FlowID:  "encuesta-nps",
		Version: 1,
		Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {
				Type:       model.NodeTypeSurveyQuestion,
				QuestionID: "recomienda",
				Prompt:     surveyPrompt,
				Options:    map[string]string{"1": "gracias", "2": "lastima"},
			},
			"gracias": {Type: model.NodeTypeMessage, Text: "¡Gracias!", Next: nil},
			"lastima": {Type: model.NodeTypeMessage, Text: "Lo sentimos.", Next: nil},
		},
	}
}

// TestGenericRouteMenuAndSurvey comprueba que menu y survey_question recorren la
// MISMA ruta genérica del engine (Enter renderiza el nodo interactivo y espera;
// Step delega en el módulo y transiciona) sin ningún switch por tipo.
func TestGenericRouteMenuAndSurvey(t *testing.T) {
	cases := []struct {
		name       string
		flow       model.Flow
		wantPrompt string
		input      string
		wantOut    string
	}{
		{name: "menu", flow: flowMenu(), wantPrompt: rootPrompt, input: "2", wantOut: "Cuéntame tu problema."},
		{name: "survey_question", flow: flowSurvey(), wantPrompt: surveyPrompt, input: "1", wantOut: "¡Gracias!"},
	}

	e := newEngineWithSurvey()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, outs, err := e.Enter(tc.flow, model.Conversation{})
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if got := texts(outs); !slices.Equal(got, []string{tc.wantPrompt}) {
				t.Fatalf("Enter outs = %q, quiero %q (render del nodo interactivo)", got, tc.wantPrompt)
			}
			if st.CurrentNode != tc.flow.Initial {
				t.Fatalf("Enter debe detenerse en el nodo interactivo %q; node=%q", tc.flow.Initial, st.CurrentNode)
			}

			st2, outs2, err := e.Step(tc.flow, st, engine.Input{Text: tc.input})
			if err != nil {
				t.Fatalf("Step: %v", err)
			}
			if got := texts(outs2); !slices.Equal(got, []string{tc.wantOut}) {
				t.Fatalf("Step outs = %q, quiero %q (render del destino)", got, tc.wantOut)
			}
			if !st2.Finished() {
				t.Fatalf("tras opción válida a message terminal esperaba Finished; node=%q", st2.CurrentNode)
			}
		})
	}
}

// TestStepUnregisteredTypeIsControlledError comprueba que un nodo actual cuyo
// tipo no espera input (p. ej. "message") o no está registrado produce un error
// controlado (envuelve ErrInvalidFlow), NO un pánico.
func TestStepUnregisteredTypeIsControlledError(t *testing.T) {
	e := newEngineWithSurvey()

	t.Run("nodo message como nodo actual", func(t *testing.T) {
		flow := flowChain() // nodos message
		st := model.Conversation{CurrentNode: "m1"}
		_, _, err := e.Step(flow, st, engine.Input{Text: "hola"})
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("esperaba ErrInvalidFlow controlado, obtuve: %v", err)
		}
	})

	t.Run("nodo de tipo no registrado", func(t *testing.T) {
		flow := model.Flow{
			FlowID:  "x",
			Version: 1,
			Initial: "n",
			Nodes:   map[string]model.Node{"n": {Type: "carrusel"}},
		}
		st := model.Conversation{CurrentNode: "n"}
		_, _, err := e.Step(flow, st, engine.Input{Text: "hola"})
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("esperaba ErrInvalidFlow controlado, obtuve: %v", err)
		}
	})
}

// TestRenderFromUnregisteredTypeIsControlledError comprueba que renderFrom (vía
// Enter) sobre un nodo de tipo no registrado (ni message) produce un error
// controlado, no un pánico.
func TestRenderFromUnregisteredTypeIsControlledError(t *testing.T) {
	e := newEngineWithSurvey()
	flow := model.Flow{
		FlowID:  "x",
		Version: 1,
		Initial: "n",
		Nodes:   map[string]model.Node{"n": {Type: "carrusel"}},
	}
	_, _, err := e.Enter(flow, model.Conversation{})
	if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("esperaba ErrInvalidFlow controlado, obtuve: %v", err)
	}
}
