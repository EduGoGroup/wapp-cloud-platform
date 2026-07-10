package modules_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	r := modules.NewRegistry()
	r.Register(menu.New())

	got, ok := r.Get("menu")
	if !ok {
		t.Fatalf("Get(menu): esperaba encontrar el módulo registrado")
	}
	if got.Type() != "menu" {
		t.Fatalf("Type() = %q, quiero %q", got.Type(), "menu")
	}

	if _, ok := r.Get("desconocido"); ok {
		t.Fatalf("Get(desconocido): no esperaba módulo para un tipo no registrado")
	}
}

// TestRegistryValidateModuleNodes cubre la validación estructural de nodos de módulo
// (Plan 027 · Ola 1 · T6, cierra H11): el cart valida su estructura; los nodos core
// o de tipos sin NodeValidator se saltan (no-regresión).
func TestRegistryValidateModuleNodes(t *testing.T) {
	r := modules.NewRegistry()
	r.Register(menu.New()) // no implementa NodeValidator: se salta
	r.Register(cart.New())

	// Nodo cart SIN catálogo (degradaría en runtime): debe rechazarse.
	bad := model.Flow{
		FlowID: "tienda", Version: 1, Initial: "root",
		Nodes: map[string]model.Node{"root": {Type: "cart"}},
	}
	if err := r.ValidateModuleNodes(bad); err == nil {
		t.Fatal("ValidateModuleNodes: un nodo cart sin content.source debe rechazarse")
	}

	// Nodo cart con catálogo json+ref: válido.
	good := model.Flow{
		FlowID: "tienda", Version: 1, Initial: "root",
		Nodes: map[string]model.Node{"root": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "catalogo-1"}}},
	}
	if err := r.ValidateModuleNodes(good); err != nil {
		t.Fatalf("ValidateModuleNodes(cart válido) = %v, quiero nil", err)
	}

	// Nodo core (menu) y tipo no registrado: se saltan sin error.
	coreAndUnknown := model.Flow{
		FlowID: "f", Version: 1, Initial: "m",
		Nodes: map[string]model.Node{
			"m": {Type: "menu", Options: map[string]string{"1": "m"}},
			"x": {Type: "tipo-no-registrado"},
		},
	}
	if err := r.ValidateModuleNodes(coreAndUnknown); err != nil {
		t.Fatalf("ValidateModuleNodes(core/desconocido) = %v, quiero nil (se saltan)", err)
	}
}

func TestRegistryWaitsForInput(t *testing.T) {
	r := modules.NewRegistry()
	r.Register(menu.New())

	if !r.WaitsForInput("menu") {
		t.Fatalf("WaitsForInput(menu) = false, quiero true (el menú es interactivo)")
	}
	if r.WaitsForInput("desconocido") {
		t.Fatalf("WaitsForInput(desconocido) = true, quiero false (tipo no registrado)")
	}
}
