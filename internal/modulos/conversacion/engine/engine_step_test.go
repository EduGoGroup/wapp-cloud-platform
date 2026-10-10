package engine_test

// engine_step_test.go — Step sin consulta: la ruta genérica, la permanencia, la
// transición, el fin declarado por el módulo y la siembra del blob crudo. El
// re-entry de las consultas está en consulta_test.go.

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// scripted devuelve un módulo interactivo cuyo Step contesta siempre res.
func scripted(res modules.Result) stubModule {
	return stubModule{
		nodeType: typeChoice,
		waits:    true,
		render:   func(model.Node, model.Content) []string { return []string{"pantalla de ARRANQUE (Render)"} },
		step:     func(model.Node, model.Conversation, string) modules.Result { return res },
	}
}

// oneNodeFlow es un flujo de un solo nodo interactivo, como el del carrito.
func oneNodeFlow() model.Flow {
	return model.Flow{FlowID: "un-nodo", Version: 1, Initial: "only", Nodes: map[string]model.Node{"only": {Type: typeChoice}}}
}

func TestStep_FinishedConversationIsNeutral(t *testing.T) {
	called := false
	mod := stubModule{nodeType: typeChoice, waits: true, step: func(model.Node, model.Conversation, string) modules.Result {
		called = true
		return modules.Result{}
	}}
	in := model.Conversation{FlowID: "f", CurrentNode: model.NodeTerminal, Vars: map[string]any{"k": "v"}}

	// La definición está vacía: sobre una conversación terminada ni se mira.
	st, outs, effects, err := newEngine([]modules.Module{mod}).Step(context.Background(), model.Flow{}, in, engine.Input{Text: "hola"})
	if err != nil || outs != nil || effects != nil {
		t.Errorf("salidas = %v, efectos = %v, err = %v; quiero todo nil", outs, effects, err)
	}
	if !reflect.DeepEqual(st, in) || called {
		t.Errorf("estado = %+v (módulo llamado: %v), quiero el recibido sin llamar a nadie", st, called)
	}
}

func TestStep_Errors(t *testing.T) {
	flow := model.Flow{FlowID: "f", Version: 1, Initial: "root", Nodes: map[string]model.Node{
		"m1":     {Type: model.NodeTypeMessage, Text: "Hola."},
		"n":      {Type: "carousel"},
		"banner": {Type: typeBanner},
	}}
	cases := map[string]struct {
		node       string
		wantDetail string
	}{
		"current node does not exist":  {"ghost", `nodo actual "ghost" no existe en la definición`},
		"message node":                 {"m1", `nodo actual "m1" de tipo "message" no espera entrada`},
		"unregistered type":            {"n", `nodo actual "n" de tipo "carousel" no espera entrada`},
		"output module does not wait":  {"banner", `nodo actual "banner" de tipo "banner" no espera entrada`},
		"empty current node is a miss": {"", `nodo actual "" no existe en la definición`},
	}
	e := newEngine([]modules.Module{choice(), stubModule{nodeType: typeBanner}})
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := model.Conversation{CurrentNode: tc.node, Vars: map[string]any{"k": "v"}}
			st, outs, effects, err := e.Step(context.Background(), flow, in, engine.Input{Text: "hola"})
			assertInvalidFlow(t, err, tc.wantDetail)
			if outs != nil || effects != nil || !reflect.DeepEqual(st, in) {
				t.Errorf("estado = %+v, salidas = %v, efectos = %v; quiero el estado sin tocar y lo demás nil", st, outs, effects)
			}
		})
	}
}

// Dos tipos de módulo distintos recorren la MISMA ruta: Enter renderiza y espera,
// Step delega y transiciona. No hay nada en el engine que mire el tipo.
func TestStep_GenericRouteForAnyInteractiveModule(t *testing.T) {
	e := newEngine([]modules.Module{choice(), stubModule{nodeType: typePoll, waits: true}})
	for _, nodeType := range []string{typeChoice, typePoll} {
		t.Run(nodeType, func(t *testing.T) {
			flow := choiceFlow(nodeType)
			st, outs, err := e.Enter(context.Background(), flow, model.Conversation{})
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if st.CurrentNode != "root" || !slices.Equal(texts(outs), []string{rootPrompt}) {
				t.Fatalf("Enter: nodo = %q, salidas = %q", st.CurrentNode, texts(outs))
			}
			st, outs, effects, err := e.Step(context.Background(), flow, st, engine.Input{Text: "2"})
			if err != nil {
				t.Fatalf("Step: %v", err)
			}
			if !st.Finished() || !slices.Equal(texts(outs), []string{"Cuéntame tu problema."}) || len(effects) != 0 {
				t.Errorf("Step: nodo = %q, salidas = %q, efectos = %v", st.CurrentNode, texts(outs), effects)
			}
		})
	}
}

// El texto llega al módulo tal cual: recortar es cosa suya.
func TestStep_HandsTheInputToTheModuleUntouched(t *testing.T) {
	var seen []string
	var seenNode model.Node
	mod := stubModule{nodeType: typeChoice, waits: true, step: func(node model.Node, conv model.Conversation, input string) modules.Result {
		seen, seenNode = append(seen, input), node
		return modules.Result{Vars: conv.Vars}
	}}
	flow := choiceFlow(typeChoice)
	in := model.Conversation{CurrentNode: "root"}
	if _, _, _, err := newEngine([]modules.Module{mod}).Step(context.Background(), flow, in, engine.Input{Text: "  1 \n"}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !slices.Equal(seen, []string{"  1 \n"}) || seenNode.Prompt != rootPrompt {
		t.Errorf("el módulo vio %q sobre %+v; quiero el texto sin recortar, una vez, y el nodo actual", seen, seenNode)
	}
}

func TestStep_Stay(t *testing.T) {
	effect := modules.Effect{Kind: "event", Name: "reprompted"}

	t.Run("module outputs, effects and vars", func(t *testing.T) {
		res := modules.Result{
			Outputs: []string{"otra vez", "elige una"},
			Vars:    map[string]any{"tries": 1},
			Effects: []modules.Effect{effect},
			Outcome: model.OutcomeCancelled, // fuera del fin declarado se ignora
		}
		in := model.Conversation{CurrentNode: "only", Vars: map[string]any{"old": true}}
		st, outs, effects, err := newEngine([]modules.Module{scripted(res)}).Step(context.Background(), oneNodeFlow(), in, engine.Input{Text: "x"})
		if err != nil {
			t.Fatalf("Step: %v", err)
		}
		if st.CurrentNode != "only" || !slices.Equal(texts(outs), []string{"otra vez", "elige una"}) {
			t.Errorf("nodo = %q, salidas = %q; quiero el mismo nodo y las salidas del módulo", st.CurrentNode, texts(outs))
		}
		if len(effects) != 1 || effects[0].Name != "reprompted" {
			t.Errorf("efectos = %v, quiero los del módulo", effects)
		}
		if !maps.Equal(st.Vars, map[string]any{"tries": 1}) {
			t.Errorf("Vars = %v, quiero las del Result (sin desenlace sellado)", st.Vars)
		}
	})

	t.Run("zero result wipes vars and says nothing", func(t *testing.T) {
		in := model.Conversation{CurrentNode: "only", Vars: map[string]any{"old": true}}
		st, outs, effects, err := newEngine([]modules.Module{scripted(modules.Result{})}).Step(context.Background(), oneNodeFlow(), in, engine.Input{Text: "x"})
		if err != nil || outs != nil || effects != nil {
			t.Errorf("salidas = %v, efectos = %v, err = %v; quiero todo nil", outs, effects, err)
		}
		if st.Vars != nil || st.CurrentNode != "only" {
			t.Errorf("estado = %+v, quiero el mismo nodo y las Vars (nil) del Result", st)
		}
	})
}

func TestStep_Transition(t *testing.T) {
	effect := modules.Effect{Kind: "persist", Name: "answer"}
	res := modules.Result{
		Next:    ptr("hello"),
		Outputs: []string{"esta salida se descarta"},
		Vars:    map[string]any{"picked": "1"},
		Effects: []modules.Effect{effect},
		Outcome: model.OutcomeCompleted, // fuera del fin declarado se ignora
	}
	flow := model.Flow{FlowID: "f", Version: 1, Initial: "only", Nodes: map[string]model.Node{
		"only":  {Type: typeChoice},
		"hello": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("only")},
	}}
	e := newEngine([]modules.Module{scripted(res)})

	t.Run("renders the destination and keeps the effects", func(t *testing.T) {
		st, outs, effects, err := e.Step(context.Background(), flow, model.Conversation{CurrentNode: "only"}, engine.Input{Text: "1"})
		if err != nil {
			t.Fatalf("Step: %v", err)
		}
		if want := []string{"Hola.", "pantalla de ARRANQUE (Render)"}; !slices.Equal(texts(outs), want) {
			t.Errorf("salidas = %q, quiero el render del destino %q", texts(outs), want)
		}
		if st.CurrentNode != "only" || !maps.Equal(st.Vars, map[string]any{"picked": "1"}) {
			t.Errorf("estado = %+v, quiero el nodo donde paró el render y las Vars del Result", st)
		}
		if len(effects) != 1 || effects[0].Name != "answer" {
			t.Errorf("efectos = %v, quiero los del módulo", effects)
		}
	})

	t.Run("broken destination still returns the effects", func(t *testing.T) {
		broken := res
		broken.Next = ptr("ghost")
		st, outs, effects, err := newEngine([]modules.Module{scripted(broken)}).
			Step(context.Background(), flow, model.Conversation{CurrentNode: "only"}, engine.Input{Text: "1"})
		assertInvalidFlow(t, err, `nodo "ghost" no existe en la definición`)
		if st.CurrentNode != "ghost" || outs != nil || len(effects) != 1 {
			t.Errorf("nodo = %q, salidas = %v, efectos = %v; quiero el destino roto, sin salidas y con los efectos", st.CurrentNode, outs, effects)
		}
	})
}

// El módulo de un solo nodo declara el fin apuntando Next al centinela con una
// variable PROPIA: se compara por valor y no se busca en la definición.
func TestStep_ModuleDeclaresTheEnd(t *testing.T) {
	cases := map[string]struct {
		outcome  model.Outcome
		vars     map[string]any
		wantVars map[string]any
	}{
		"completed is sealed": {model.OutcomeCompleted, map[string]any{"seen": "fin"}, map[string]any{"seen": "fin", model.VarOutcome: "completed"}},
		"cancelled is sealed": {model.OutcomeCancelled, nil, map[string]any{model.VarOutcome: "cancelled"}},
		"undeclared erases a stale outcome": {model.OutcomeUndeclared,
			map[string]any{"seen": "fin", model.VarOutcome: "completed"}, map[string]any{"seen": "fin"}},
		"undeclared with no vars stays nil": {model.OutcomeUndeclared, nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			end := model.NodeTerminal
			res := modules.Result{
				Next:    &end,
				Outcome: tc.outcome,
				Vars:    tc.vars,
				Outputs: []string{"pantalla FINAL del módulo"},
				Effects: []modules.Effect{{Kind: "event", Name: "closed"}},
			}
			e := newEngine([]modules.Module{scripted(res)})
			def := oneNodeFlow()

			st, outs, effects, err := e.Step(context.Background(), def, model.Conversation{CurrentNode: "only"}, engine.Input{Text: "fin"})
			if err != nil {
				t.Fatalf("Step: %v (el centinela no se busca en la definición)", err)
			}
			if !st.Finished() {
				t.Errorf("CurrentNode = %q, quiero el centinela", st.CurrentNode)
			}
			if !slices.Equal(texts(outs), []string{"pantalla FINAL del módulo"}) {
				t.Errorf("salidas = %q, quiero las del propio Step, sin Render", texts(outs))
			}
			if len(effects) != 1 || effects[0].Name != "closed" {
				t.Errorf("efectos = %v, quiero los del turno que termina", effects)
			}
			if !maps.Equal(st.Vars, tc.wantVars) || st.Outcome() != tc.outcome {
				t.Errorf("Vars = %v (desenlace %q), quiero %v", st.Vars, st.Outcome(), tc.wantVars)
			}
			if tc.wantVars == nil && st.Vars != nil {
				t.Errorf("Vars = %v, quiero nil", st.Vars)
			}

			// El turno siguiente es la salida neutra.
			again, outs, effects, err := e.Step(context.Background(), def, st, engine.Input{Text: "hola"})
			if err != nil || outs != nil || effects != nil || !again.Finished() {
				t.Errorf("turno sobre el fin: nodo = %q, salidas = %v, efectos = %v, err = %v", again.CurrentNode, outs, effects, err)
			}
		})
	}
}

// probe anota lo que el módulo ve bajo la clave del blob crudo y permanece.
func probe(seen *[]any) stubModule {
	return stubModule{nodeType: typeChoice, waits: true, step: func(_ model.Node, conv model.Conversation, _ string) modules.Result {
		*seen = append(*seen, conv.Vars[modules.VarContentRaw])
		return modules.Result{Vars: conv.Vars}
	}}
}

func TestStep_ExposesRawContentBeforeTheModuleRuns(t *testing.T) {
	raw := map[string]any{"items": []any{"empanada"}}
	withRaw := func(tenants *[]string) engine.Option {
		return engine.WithContentSource(sourceFunc(func(_ context.Context, tenantID string, _ model.Node) (model.Content, error) {
			*tenants = append(*tenants, tenantID)
			return model.Content{Raw: raw}, nil
		}))
	}

	t.Run("raw blob is sown, creating the map", func(t *testing.T) {
		var seen []any
		var tenants []string
		e := newEngine([]modules.Module{probe(&seen)}, withRaw(&tenants))
		st, _, _, err := e.Step(context.Background(), oneNodeFlow(), model.Conversation{TenantID: "tenant-a", CurrentNode: "only"}, engine.Input{Text: "x"})
		if err != nil {
			t.Fatalf("Step: %v", err)
		}
		if len(seen) != 1 || !reflect.DeepEqual(seen[0], raw) || !reflect.DeepEqual(st.Vars[modules.VarContentRaw], raw) {
			t.Errorf("el módulo vio %v y el estado lleva %v; quiero el blob crudo", seen, st.Vars)
		}
		if !slices.Equal(tenants, []string{"tenant-a"}) {
			t.Errorf("la fuente se consultó con %v, quiero el tenant del estado", tenants)
		}
	})

	// 🔴 Rareza portada: el blob se escribe en el MISMO mapa que trae el estado.
	t.Run("written into the caller's own map", func(t *testing.T) {
		var seen []any
		var tenants []string
		caller := map[string]any{"k": "v"}
		e := newEngine([]modules.Module{probe(&seen)}, withRaw(&tenants))
		if _, _, _, err := e.Step(context.Background(), oneNodeFlow(), model.Conversation{CurrentNode: "only", Vars: caller}, engine.Input{Text: "x"}); err != nil {
			t.Fatalf("Step: %v", err)
		}
		if !reflect.DeepEqual(caller[modules.VarContentRaw], raw) || caller["k"] != "v" {
			t.Errorf("mapa del llamante = %v, quiero sus claves más el blob", caller)
		}
	})
}

// Sembrar el blob es best-effort: sin blob, o si la fuente falla, el módulo corre igual.
func TestStep_WithoutRawContentSowsNothing(t *testing.T) {
	t.Run("no raw blob sows nothing", func(t *testing.T) {
		var seen []any
		e := newEngine([]modules.Module{probe(&seen)}) // estática: Raw nil
		st, _, _, err := e.Step(context.Background(), oneNodeFlow(), model.Conversation{CurrentNode: "only"}, engine.Input{Text: "x"})
		if err != nil || st.Vars != nil || len(seen) != 1 || seen[0] != nil {
			t.Errorf("Vars = %v, visto = %v, err = %v; quiero Vars nil y nada sembrado", st.Vars, seen, err)
		}
	})

	t.Run("resolution failure does not abort the step", func(t *testing.T) {
		var seen []any
		e := newEngine([]modules.Module{probe(&seen)}, engine.WithContentSource(failingSource()))
		st, _, _, err := e.Step(context.Background(), oneNodeFlow(), model.Conversation{CurrentNode: "only"}, engine.Input{Text: "x"})
		if err != nil {
			t.Fatalf("Step: %v (el fallo de contenido se degrada)", err)
		}
		if st.Vars != nil || len(seen) != 1 || seen[0] != nil {
			t.Errorf("Vars = %v, visto = %v; quiero que el módulo corra sin blob", st.Vars, seen)
		}
	})
}
