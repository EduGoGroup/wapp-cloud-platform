//go:build pendiente

package modules_test

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// fakeModule es un Module mínimo: solo declara su tipo y si espera entrada.
type fakeModule struct {
	nodeType string
	waits    bool
}

func (m fakeModule) Type() string                              { return m.nodeType }
func (m fakeModule) Render(model.Node, model.Content) []string { return nil }
func (m fakeModule) Step(model.Node, model.Conversation, string) modules.Result {
	return modules.Result{}
}
func (m fakeModule) WaitsForInput() bool          { return m.waits }
func (m fakeModule) ProducesDurableContent() bool { return false }

// errNoCatalog es el error base del módulo validador de prueba.
var errNoCatalog = errors.New("falta el catálogo")

// capableModule suma a fakeModule las tres capacidades opcionales. Como
// NodeValidator rechaza el nodo que no declara `content`.
type capableModule struct{ fakeModule }

func (capableModule) ValidateNode(node model.Node) error {
	if node.Content == nil {
		return errNoCatalog
	}
	return nil
}

func (capableModule) EmitMedia(model.Node, model.Content) (*model.MediaRef, error) {
	return &model.MediaRef{Key: "k"}, nil
}

func (capableModule) Prime(_ model.Node, _ model.Content, vars map[string]any) (modules.Result, bool) {
	return modules.Result{Vars: vars}, true
}

// Aserciones de compilación: el módulo mínimo cumple Module y NO las capacidades
// opcionales; el capaz las cumple todas.
var (
	_ modules.Module        = fakeModule{}
	_ modules.Module        = capableModule{}
	_ modules.NodeValidator = capableModule{}
	_ modules.MediaEmitter  = capableModule{}
	_ modules.Primer        = capableModule{}
)

// Las capacidades opcionales se consultan por aserción: un módulo que no las
// implementa sigue siendo un Module válido.
func TestModule_OptionalCapabilitiesAreAssertions(t *testing.T) {
	var plain modules.Module = fakeModule{nodeType: "menu"}
	if _, ok := plain.(modules.NodeValidator); ok {
		t.Error("el módulo mínimo declara NodeValidator")
	}
	if _, ok := plain.(modules.MediaEmitter); ok {
		t.Error("el módulo mínimo declara MediaEmitter")
	}
	if _, ok := plain.(modules.Primer); ok {
		t.Error("el módulo mínimo declara Primer")
	}
}

func TestResult_ZeroValueDeclaresNothing(t *testing.T) {
	var res modules.Result
	if res.Next != nil || res.Outcome != model.OutcomeUndeclared || res.Query != nil {
		t.Errorf("Result cero = %+v, quiero Next nil, Outcome sin declarar y Query nil", res)
	}
	if res.Outputs != nil || res.Vars != nil || res.Effects != nil {
		t.Errorf("Result cero = %+v, quiero Outputs, Vars y Effects nil", res)
	}
}

// Las claves de Vars y el Kind privado son contrato con el runtime y con el JSONB de
// flow_state: sus valores no cambian.
func TestRegistry_LiteralKeys(t *testing.T) {
	literals := map[string]string{
		modules.VarContentRaw:   "cart_catalog",
		modules.VarIntentParams: "intent_params",
		modules.VarIntentName:   "intent_name",
		modules.KindPrivate:     "private",
	}
	for got, want := range literals {
		if got != want {
			t.Errorf("literal = %q, quiero %q", got, want)
		}
	}
}

func TestStripIntentSignal(t *testing.T) {
	t.Run("nothing to strip returns the same map", func(t *testing.T) {
		vars := map[string]any{"cart": "estado"}
		out := modules.StripIntentSignal(vars)
		out["probe"] = true
		if _, same := vars["probe"]; !same {
			t.Error("sin señal que barrer devolvió una copia, quiero el mismo mapa")
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if out := modules.StripIntentSignal(nil); out != nil {
			t.Errorf("StripIntentSignal(nil) = %v, quiero nil", out)
		}
	})

	withSignal := map[string]map[string]any{
		"both keys":   {modules.VarIntentParams: map[string]string{"producto": "café"}, modules.VarIntentName: "pedir", "cart": "estado"},
		"only params": {modules.VarIntentParams: map[string]string{"producto": "café"}, "cart": "estado"},
		"only name":   {modules.VarIntentName: "pedir", "cart": "estado"},
	}
	for name, vars := range withSignal {
		t.Run(name, func(t *testing.T) {
			before := maps.Clone(vars)
			out := modules.StripIntentSignal(vars)

			if _, ok := out[modules.VarIntentParams]; ok {
				t.Error("los parámetros de la intención sobrevivieron")
			}
			if _, ok := out[modules.VarIntentName]; ok {
				t.Error("el nombre de la intención sobrevivió")
			}
			if len(out) != 1 || out["cart"] != "estado" {
				t.Errorf("out = %v, quiero solo la clave ajena a la señal", out)
			}
			if len(vars) != len(before) {
				t.Errorf("el mapa recibido se mutó: %v", vars)
			}
		})
	}
}

func TestEffect_PublicPayload(t *testing.T) {
	t.Run("without private keys returns the payload itself", func(t *testing.T) {
		eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "CAF"}}
		out := eff.PublicPayload()
		out["probe"] = true
		if _, same := eff.Payload["probe"]; !same {
			t.Error("sin PrivateKeys devolvió una copia, quiero el mismo mapa")
		}
	})

	t.Run("empty payload is returned as is", func(t *testing.T) {
		eff := modules.Effect{PrivateKeys: []string{"note"}}
		if out := eff.PublicPayload(); out != nil {
			t.Errorf("PublicPayload = %v, quiero nil", out)
		}
	})

	t.Run("private keys are pruned from a copy", func(t *testing.T) {
		eff := modules.Effect{
			Kind:        "event",
			Name:        "cart_closed",
			Payload:     map[string]any{"total": 12.5, "note": "sin azúcar", "phone": "555"},
			PrivateKeys: []string{"note", "phone", "ausente"},
		}
		out := eff.PublicPayload()
		if len(out) != 1 || out["total"] != 12.5 {
			t.Errorf("PublicPayload = %v, quiero solo total", out)
		}
		if eff.Payload["note"] != "sin azúcar" || eff.Payload["phone"] != "555" {
			t.Errorf("el Payload original se mutó: %v", eff.Payload)
		}
	})
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := newRegistry()
	registry.Register(fakeModule{nodeType: "menu", waits: true})

	got, ok := registry.Get("menu")
	if !ok || got.Type() != "menu" {
		t.Fatalf("Get(menu) = %v, %v; quiero el módulo registrado", got, ok)
	}
	if got, ok := registry.Get("desconocido"); ok || got != nil {
		t.Errorf("Get(desconocido) = %v, %v; quiero nil, false", got, ok)
	}
}

// newRegistry fija la firma del constructor.
func newRegistry() *modules.Registry { return modules.NewRegistry() }

func TestRegistry_Register_SameTypeOverwrites(t *testing.T) {
	registry := modules.NewRegistry()
	registry.Register(fakeModule{nodeType: "menu", waits: false})
	registry.Register(fakeModule{nodeType: "menu", waits: true})

	if !registry.WaitsForInput("menu") {
		t.Error("manda el primer registro, quiero el último")
	}
	if types := registry.Types(); len(types) != 1 {
		t.Errorf("Types = %v, quiero un solo tipo", types)
	}
}

func TestRegistry_Types(t *testing.T) {
	registry := modules.NewRegistry()
	if types := registry.Types(); types == nil || len(types) != 0 {
		t.Errorf("Types de un registro vacío = %#v, quiero una lista vacía no nil", types)
	}

	registry.Register(fakeModule{nodeType: "menu"})
	registry.Register(fakeModule{nodeType: "cart"})
	registry.Register(fakeModule{nodeType: "media"})

	types := registry.Types()
	slices.Sort(types)
	if !slices.Equal(types, []string{"cart", "media", "menu"}) {
		t.Errorf("Types = %v, quiero cart, media y menu", types)
	}
}

func TestRegistry_WaitsForInput(t *testing.T) {
	registry := modules.NewRegistry()
	registry.Register(fakeModule{nodeType: "menu", waits: true})
	registry.Register(fakeModule{nodeType: "media", waits: false})

	if !registry.WaitsForInput("menu") {
		t.Error("WaitsForInput(menu) = false, quiero true")
	}
	if registry.WaitsForInput("media") {
		t.Error("WaitsForInput(media) = true, quiero false (módulo de salida)")
	}
	if registry.WaitsForInput("desconocido") {
		t.Error("WaitsForInput(desconocido) = true, quiero false (tipo no registrado)")
	}
}

func TestRegistry_ValidateModuleNodes(t *testing.T) {
	registry := modules.NewRegistry()
	registry.Register(fakeModule{nodeType: "menu", waits: true})
	registry.Register(capableModule{fakeModule{nodeType: "cart", waits: true}})

	flowWith := func(nodes map[string]model.Node) model.Flow {
		return model.Flow{FlowID: "tienda", Version: 1, Initial: "root", Nodes: nodes}
	}

	t.Run("invalid module node is rejected with its id", func(t *testing.T) {
		err := registry.ValidateModuleNodes(flowWith(map[string]model.Node{"root": {Type: "cart"}}))
		if !errors.Is(err, errNoCatalog) {
			t.Fatalf("error = %v, quiero el error base del módulo", err)
		}
		if want := `nodo "root": falta el catálogo`; err.Error() != want {
			t.Errorf("error = %q, quiero %q", err.Error(), want)
		}
	})

	t.Run("valid module node passes", func(t *testing.T) {
		node := model.Node{Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "catalogo-1"}}
		if err := registry.ValidateModuleNodes(flowWith(map[string]model.Node{"root": node})); err != nil {
			t.Errorf("error = %v, quiero nil", err)
		}
	})

	t.Run("module without validator and unregistered type are skipped", func(t *testing.T) {
		flow := flowWith(map[string]model.Node{
			"root": {Type: "menu"},
			"x":    {Type: "tipo-no-registrado"},
		})
		if err := registry.ValidateModuleNodes(flow); err != nil {
			t.Errorf("error = %v, quiero nil (se aceptan laxos)", err)
		}
	})

	t.Run("flow without nodes passes", func(t *testing.T) {
		if err := registry.ValidateModuleNodes(model.Flow{}); err != nil {
			t.Errorf("error = %v, quiero nil", err)
		}
	})
}

// El registro se escribe en el arranque y luego se lee, pero nada impide que se
// solapen: escritores y lectores a la vez no deben dar carrera (-race) ni perder un
// registro.
func TestRegistry_ConcurrentRegisterAndRead(t *testing.T) {
	const writers, readers = 16, 16
	registry := modules.NewRegistry()
	registry.Register(capableModule{fakeModule{nodeType: "cart", waits: true}})
	flow := model.Flow{Nodes: map[string]model.Node{
		"root": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "c"}},
	}}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			registry.Register(fakeModule{nodeType: fmt.Sprintf("type-%d", i), waits: true})
		})
	}
	failures := make(chan string, readers*4) // hasta cuatro avisos por lector, sin bloquear
	for i := range readers {
		wg.Go(func() {
			<-start
			nodeType := fmt.Sprintf("type-%d", i)
			if mod, ok := registry.Get(nodeType); ok && mod.Type() != nodeType {
				failures <- "Get devolvió el módulo de otro tipo"
			}
			if !registry.WaitsForInput("cart") {
				failures <- "el módulo registrado antes de empezar dejó de verse"
			}
			if len(registry.Types()) < 1 {
				failures <- "Types perdió el módulo registrado antes de empezar"
			}
			if err := registry.ValidateModuleNodes(flow); err != nil {
				failures <- "ValidateModuleNodes falló con un nodo válido: " + err.Error()
			}
		})
	}
	close(start)
	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
	if got := len(registry.Types()); got != writers+1 {
		t.Errorf("tipos registrados = %d, quiero %d (ningún registro concurrente se pierde)", got, writers+1)
	}
	for i := range writers {
		if !registry.WaitsForInput(fmt.Sprintf("type-%d", i)) {
			t.Errorf("type-%d no quedó registrado", i)
		}
	}
}
