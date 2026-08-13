package engine_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

// newEngineWithCart registra menu (no durable) y cart (durable) — los dos módulos
// de fábrica que este test necesita para fijar el predicado (design.md D-054.5).
func newEngineWithCart() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(cart.New())
	return engine.New(reg)
}

// TestFlowProducesDurableContent cubre el predicado que consulta startLocked
// (D-054.5): un OR sobre f.Nodes que resuelve cada tipo contra el Registry.
func TestFlowProducesDurableContent(t *testing.T) {
	e := newEngineWithCart()

	t.Run("un nodo cart exige evento", func(t *testing.T) {
		f := model.Flow{
			FlowID: "carrito", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{"root": {Type: cart.NodeTypeCart}},
		}
		if !e.FlowProducesDurableContent(f) {
			t.Fatalf("FlowProducesDurableContent = false, quiero true (nodo cart)")
		}
	})

	t.Run("flujo solo de menu no exige evento", func(t *testing.T) {
		f := model.Flow{
			FlowID: "menu-puro", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{
				"root": {Type: model.NodeTypeMenu, Options: map[string]string{"1": "fin"}},
				"fin":  {Type: model.NodeTypeMessage, Text: "gracias"},
			},
		}
		if e.FlowProducesDurableContent(f) {
			t.Fatalf("FlowProducesDurableContent = true, quiero false (menu/message no son durables)")
		}
	})

	t.Run("tipo de nodo no registrado cuenta como no durable", func(t *testing.T) {
		f := model.Flow{
			FlowID: "desconocido", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{"root": {Type: "carrusel"}},
		}
		if e.FlowProducesDurableContent(f) {
			t.Fatalf("FlowProducesDurableContent = true, quiero false (tipo no registrado en el Registry: sin módulo que produzca nada)")
		}
	})

	// TestFlowProducesDurableContent/varios_nodos fija que el predicado es un OR
	// sobre TODOS los nodos, no solo el inicial: aquí el nodo INICIAL es un menu
	// (no durable) y solo el ÚLTIMO nodo alcanzable es cart (durable). Si el
	// predicado mirase solo def.Nodes[def.Initial] este caso daría false y el
	// bug pasaría desapercibido.
	t.Run("varios nodos: solo el último es durable", func(t *testing.T) {
		f := model.Flow{
			FlowID: "menu-a-carrito", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{
				"root":   {Type: model.NodeTypeMenu, Options: map[string]string{"1": "saludo"}},
				"saludo": {Type: model.NodeTypeMessage, Text: "hola", Next: ptr("compra")},
				"compra": {Type: cart.NodeTypeCart},
			},
		}
		if !e.FlowProducesDurableContent(f) {
			t.Fatalf("FlowProducesDurableContent = false, quiero true (el nodo 'compra' es cart aunque el inicial sea menu)")
		}
	})
}

// TestDurableNodeType cubre el helper de diagnóstico que usa el WARN de la
// degradación (T2.4, D-054.6): mismo criterio que FlowProducesDurableContent, pero
// nombrando el tipo en vez de solo decir sí/no.
func TestDurableNodeType(t *testing.T) {
	e := newEngineWithCart()

	t.Run("nodo cart devuelve su tipo", func(t *testing.T) {
		f := model.Flow{
			FlowID: "carrito", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{"root": {Type: cart.NodeTypeCart}},
		}
		if got := e.DurableNodeType(f); got != cart.NodeTypeCart {
			t.Fatalf("DurableNodeType = %q, quiero %q", got, cart.NodeTypeCart)
		}
	})

	t.Run("flujo sin nodos durables devuelve vacío", func(t *testing.T) {
		f := model.Flow{
			FlowID: "menu-puro", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{
				"root": {Type: model.NodeTypeMenu, Options: map[string]string{"1": "fin"}},
				"fin":  {Type: model.NodeTypeMessage, Text: "gracias"},
			},
		}
		if got := e.DurableNodeType(f); got != "" {
			t.Fatalf("DurableNodeType = %q, quiero \"\" (nada durable)", got)
		}
	})

	t.Run("tipo no registrado no cuenta como durable", func(t *testing.T) {
		f := model.Flow{
			FlowID: "desconocido", Version: 1, Initial: "root",
			Nodes: map[string]model.Node{"root": {Type: "carrusel"}},
		}
		if got := e.DurableNodeType(f); got != "" {
			t.Fatalf("DurableNodeType = %q, quiero \"\" (tipo no registrado)", got)
		}
	})
}
