package engine_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// Tipos de nodo de los dobles. Ninguno es un tipo real ("menu", "cart", "media") a
// propósito: el engine no puede distinguir un módulo por su nombre.
const (
	typeChoice = "choice" // interactivo, no durable
	typePoll   = "poll"   // interactivo, no durable: un segundo tipo para la ruta genérica
	typeOrder  = "order"  // interactivo y durable, con capacidad Primer
	typeBanner = "banner" // de salida, con capacidad MediaEmitter
)

const rootPrompt = "¿En qué te ayudo?\n1) Ventas\n2) Soporte"

// stubModule es un Module configurable. Sin funciones propias se comporta como un
// selector trivial: Render emite el Prompt resuelto y Step transiciona por la opción
// EXACTA (sin recortar) o permanece.
type stubModule struct {
	nodeType string
	waits    bool
	durable  bool
	render   func(model.Node, model.Content) []string
	step     func(model.Node, model.Conversation, string) modules.Result
}

func (m stubModule) Type() string                 { return m.nodeType }
func (m stubModule) WaitsForInput() bool          { return m.waits }
func (m stubModule) ProducesDurableContent() bool { return m.durable }

func (m stubModule) Render(node model.Node, resolved model.Content) []string {
	if m.render != nil {
		return m.render(node, resolved)
	}
	return []string{resolved.Prompt}
}

func (m stubModule) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	if m.step != nil {
		return m.step(node, conv, input)
	}
	if target, ok := node.Options[input]; ok {
		return modules.Result{Next: &target, Vars: conv.Vars}
	}
	return modules.Result{Vars: conv.Vars, Outputs: []string{"invalid:" + input}}
}

// primingModule suma la capacidad modules.Primer.
type primingModule struct {
	stubModule
	prime func(model.Node, model.Content, map[string]any) (modules.Result, bool)
}

func (m primingModule) Prime(node model.Node, resolved model.Content, vars map[string]any) (modules.Result, bool) {
	return m.prime(node, resolved, vars)
}

// emittingModule suma la capacidad modules.MediaEmitter.
type emittingModule struct {
	stubModule
	emit func(model.Node, model.Content) (*model.MediaRef, error)
}

func (m emittingModule) EmitMedia(node model.Node, resolved model.Content) (*model.MediaRef, error) {
	return m.emit(node, resolved)
}

// sourceFunc adapta una función al puerto content.Source.
type sourceFunc func(ctx context.Context, tenantID string, node model.Node) (model.Content, error)

func (f sourceFunc) Resolve(ctx context.Context, tenantID string, node model.Node) (model.Content, error) {
	return f(ctx, tenantID, node)
}

var (
	_ modules.Module       = stubModule{}
	_ modules.Primer       = primingModule{}
	_ modules.MediaEmitter = emittingModule{}
	_ content.Source       = sourceFunc(nil)
)

var errCatalogDown = errors.New("catálogo no disponible")

// failingSource no resuelve ningún nodo.
func failingSource() content.Source {
	return sourceFunc(func(context.Context, string, model.Node) (model.Content, error) {
		return model.Content{}, errCatalogDown
	})
}

func ptr(s string) *string { return &s }

func choice() stubModule { return stubModule{nodeType: typeChoice, waits: true} }

// newEngine monta un engine con los módulos dados.
func newEngine(mods []modules.Module, opts ...engine.Option) *engine.Engine {
	reg := modules.NewRegistry()
	for _, m := range mods {
		reg.Register(m)
	}
	return engine.New(reg, opts...)
}

func texts(outs []engine.Output) []string {
	got := make([]string, len(outs))
	for i, o := range outs {
		got[i] = o.Text
	}
	return got
}

// assertInvalidFlow exige el error controlado del engine: envuelve ErrInvalidFlow y
// su texto es el del contrato, byte a byte.
func assertInvalidFlow(t *testing.T, err error, wantDetail string) {
	t.Helper()
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("err = %v, quiero uno que envuelva ErrInvalidFlow", err)
	}
	if want := "definición de flujo inválida: " + wantDetail; err.Error() != want {
		t.Fatalf("err = %q, quiero %q", err.Error(), want)
	}
}

// choiceFlow: nodo interactivo inicial con dos opciones a mensajes terminales.
func choiceFlow(nodeType string) model.Flow {
	return model.Flow{
		FlowID:  "menu-soporte",
		Version: 3,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root":    {Type: nodeType, Prompt: rootPrompt, Options: map[string]string{"1": "sales", "2": "support"}},
			"sales":   {Type: model.NodeTypeMessage, Text: "Te paso con Ventas."},
			"support": {Type: model.NodeTypeMessage, Text: "Cuéntame tu problema."},
		},
	}
}

// chainFlow: tres mensajes encadenados hasta el fin.
func chainFlow() model.Flow {
	return model.Flow{
		FlowID:  "bienvenida",
		Version: 1,
		Initial: "m1",
		Nodes: map[string]model.Node{
			"m1": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("m2")},
			"m2": {Type: model.NodeTypeMessage, Text: "Bienvenido.", Next: ptr("m3")},
			"m3": {Type: model.NodeTypeMessage, Text: "Adiós."},
		},
	}
}

// Sin opciones —o con una fuente nil— el contenido sale del propio nodo: el render
// es el Prompt byte a byte.
func TestNew_DefaultsToStaticContent(t *testing.T) {
	sets := map[string][]engine.Option{
		"no options": nil,
		"nil source": {engine.WithContentSource(nil)},
		"nil after a real source": {
			engine.WithContentSource(failingSource()),
			engine.WithContentSource(nil),
		},
	}
	for name, opts := range sets {
		e := newEngine([]modules.Module{choice()}, opts...)
		_, outs, err := e.Enter(context.Background(), choiceFlow(typeChoice), model.Conversation{})
		if err != nil {
			t.Fatalf("%s: Enter: %v", name, err)
		}
		if got := texts(outs); !slices.Equal(got, []string{rootPrompt}) {
			t.Errorf("%s: render = %q, quiero el Prompt del nodo", name, got)
		}
	}
}

type ctxKey struct{}

// La fuente inyectada resuelve ANTES del Render y recibe el ctx de la llamada, el
// tenant del estado y el nodo; si se inyectan dos, manda la última.
func TestWithContentSource_ResolvesBeforeRender(t *testing.T) {
	type call struct {
		marker any
		tenant string
		node   string
	}
	var calls []call
	ignored := sourceFunc(func(context.Context, string, model.Node) (model.Content, error) {
		return model.Content{Prompt: "FUENTE-PISADA"}, nil
	})
	var src content.Source = sourceFunc(func(ctx context.Context, tenantID string, node model.Node) (model.Content, error) {
		calls = append(calls, call{ctx.Value(ctxKey{}), tenantID, node.Type})
		return model.Content{Prompt: "PROMPT-RESUELTO", Options: node.Options}, nil
	})
	e := newEngine([]modules.Module{choice()}, engine.WithContentSource(ignored), engine.WithContentSource(src))

	ctx := context.WithValue(context.Background(), ctxKey{}, "turno-7")
	_, outs, err := e.Enter(ctx, choiceFlow(typeChoice), model.Conversation{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{"PROMPT-RESUELTO"}) {
		t.Errorf("render = %q, quiero el prompt de la última fuente inyectada", got)
	}
	if want := []call{{"turno-7", "tenant-a", typeChoice}}; !slices.Equal(calls, want) {
		t.Errorf("llamadas a la fuente = %v, quiero %v", calls, want)
	}
}

func TestEnter(t *testing.T) {
	intro := model.Flow{
		FlowID:  "intro-menu",
		Version: 2,
		Initial: "intro",
		Nodes: map[string]model.Node{
			"intro": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("root")},
			"root":  {Type: typeChoice, Prompt: rootPrompt, Options: map[string]string{"1": "sales"}},
			"sales": {Type: model.NodeTypeMessage, Text: "Ventas."},
		},
	}
	cases := map[string]struct {
		flow     model.Flow
		wantOuts []string
		wantNode string
	}{
		"interactive initial renders and waits": {choiceFlow(typeChoice), []string{rootPrompt}, "root"},
		"message chain runs to the end":         {chainFlow(), []string{"Hola.", "Bienvenido.", "Adiós."}, model.NodeTerminal},
		"message chain stops at an interactive": {intro, []string{"Hola.", rootPrompt}, "root"},
	}
	e := newEngine([]modules.Module{choice()})
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// El estado trae un flujo anterior y unas Vars con la señal de intención.
			vars := map[string]any{modules.VarIntentParams: "x", "keep": "v"}
			in := model.Conversation{TenantID: "tenant-a", FlowID: "viejo", FlowVersion: 99, CurrentNode: "otro", Vars: vars}

			st, outs, err := e.Enter(context.Background(), tc.flow, in)
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if got := texts(outs); !slices.Equal(got, tc.wantOuts) {
				t.Errorf("salidas = %q, quiero %q", got, tc.wantOuts)
			}
			if st.CurrentNode != tc.wantNode || st.Finished() != (tc.wantNode == model.NodeTerminal) {
				t.Errorf("CurrentNode = %q (Finished=%v), quiero %q", st.CurrentNode, st.Finished(), tc.wantNode)
			}
			if st.FlowID != tc.flow.FlowID || st.FlowVersion != tc.flow.Version || st.TenantID != "tenant-a" {
				t.Errorf("estado = %+v, quiero el flujo y la versión de la definición", st)
			}
			// Enter no barre la señal: eso es de EnterPrimed.
			if len(st.Vars) != 2 || st.Vars[modules.VarIntentParams] != "x" || st.Vars["keep"] != "v" {
				t.Errorf("Vars = %v, quiero las recibidas sin tocar", st.Vars)
			}
			if in.CurrentNode != "otro" || in.FlowID != "viejo" {
				t.Errorf("el estado recibido cambió: %+v", in)
			}
		})
	}
}

// durableEngine registra un módulo no durable y uno durable.
func durableEngine() *engine.Engine {
	return newEngine([]modules.Module{
		choice(),
		stubModule{nodeType: typeOrder, waits: true, durable: true},
		stubModule{nodeType: "ticket", waits: true, durable: true},
	})
}

func TestFlowProducesDurableContent(t *testing.T) {
	cases := map[string]struct {
		nodes map[string]model.Node
		want  bool
	}{
		"durable node": {map[string]model.Node{"root": {Type: typeOrder}}, true},
		"only non durable nodes": {map[string]model.Node{
			"root": {Type: typeChoice}, "end": {Type: model.NodeTypeMessage},
		}, false},
		"unregistered type is not durable": {map[string]model.Node{"root": {Type: "carousel"}}, false},
		"empty flow":                       {nil, false},
		// Es un OR sobre TODOS los nodos: el durable no es el inicial.
		"durable node is not the initial one": {map[string]model.Node{
			"root": {Type: typeChoice}, "hello": {Type: model.NodeTypeMessage, Next: ptr("buy")}, "buy": {Type: typeOrder},
		}, true},
	}
	e := durableEngine()
	for name, tc := range cases {
		flow := model.Flow{FlowID: "f", Version: 1, Initial: "root", Nodes: tc.nodes}
		if got := e.FlowProducesDurableContent(flow); got != tc.want {
			t.Errorf("%s: FlowProducesDurableContent = %v, quiero %v", name, got, tc.want)
		}
	}
}

func TestNodeProducesDurableContent(t *testing.T) {
	cases := map[string]bool{
		typeOrder:             true,
		typeChoice:            false,
		"carousel":            false, // no registrado
		model.NodeTypeMessage: false, // no es un módulo
		"":                    false,
	}
	e := durableEngine()
	for nodeType, want := range cases {
		if got := e.NodeProducesDurableContent(nodeType); got != want {
			t.Errorf("NodeProducesDurableContent(%q) = %v, quiero %v", nodeType, got, want)
		}
	}
}

func TestDurableNodeType(t *testing.T) {
	cases := map[string]struct {
		nodes map[string]model.Node
		want  string
	}{
		"durable node names its type": {map[string]model.Node{"root": {Type: typeOrder}}, typeOrder},
		"nothing durable":             {map[string]model.Node{"root": {Type: typeChoice}, "end": {Type: model.NodeTypeMessage}}, ""},
		"unregistered type":           {map[string]model.Node{"root": {Type: "carousel"}}, ""},
		"empty flow":                  {nil, ""},
		// Con varios durables manda el primero por CLAVE de nodo, no por tipo.
		"first by node id wins": {map[string]model.Node{
			"z": {Type: typeOrder}, "b": {Type: "ticket"}, "a": {Type: typeChoice}, "c": {Type: typeOrder},
		}, "ticket"},
	}
	e := durableEngine()
	for name, tc := range cases {
		flow := model.Flow{FlowID: "f", Version: 1, Initial: "root", Nodes: tc.nodes}
		// Se repite: la iteración de un map no es determinista y el resultado sí.
		for range 20 {
			if got := e.DurableNodeType(flow); got != tc.want {
				t.Fatalf("%s: DurableNodeType = %q, quiero %q", name, got, tc.want)
			}
		}
	}
}

// Núcleo puro: el engine no guarda nada entre llamadas. El mismo turno, repetido y
// en paralelo sobre el mismo Engine, da siempre el mismo resultado.
func TestEngine_IsStatelessAndSafeForConcurrentUse(t *testing.T) {
	e := newEngine([]modules.Module{choice()})
	flow := choiceFlow(typeChoice)

	type turn struct {
		entered, primed, stepped model.Conversation
		outs                     []engine.Output
		effects                  []modules.Effect
	}
	run := func() (turn, error) {
		var got turn
		var err error
		if got.entered, _, err = e.Enter(context.Background(), flow, model.Conversation{SessionID: "s"}); err != nil {
			return got, err
		}
		if got.primed, _, _, err = e.EnterPrimed(context.Background(), flow, model.Conversation{SessionID: "s"}); err != nil {
			return got, err
		}
		got.stepped, got.outs, got.effects, err = e.Step(context.Background(), flow, got.entered, engine.Input{Text: "2"})
		return got, err
	}

	want, err := run()
	if err != nil {
		t.Fatalf("turno de referencia: %v", err)
	}
	if !want.stepped.Finished() || !slices.Equal(texts(want.outs), []string{"Cuéntame tu problema."}) {
		t.Fatalf("turno de referencia = %+v", want)
	}

	results := make([]turn, 16)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], errs[i] = run() })
	}
	wg.Wait()
	for i, got := range results {
		if errs[i] != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("turno %d = %+v (err %v), quiero el de referencia", i, got, errs[i])
		}
	}
}
