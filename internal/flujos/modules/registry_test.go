package modules_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
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
