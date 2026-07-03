package cart

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// driveE aplica un Step devolviendo también los efectos declarados (para las
// pruebas de efectos T2). Encadena Vars como drive.
func driveE(t *testing.T, m Module, vars map[string]any, input string) (cartState, []modules.Effect, map[string]any) {
	t.Helper()
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, input)
	return loadState(res.Vars), res.Effects, res.Vars
}

// effByName devuelve el (primer) efecto con el Name dado, o falla si no está.
func effByName(t *testing.T, effs []modules.Effect, name string) modules.Effect {
	t.Helper()
	for _, e := range effs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no se encontró el efecto %q en %+v", name, effs)
	return modules.Effect{}
}

func hasEffect(effs []modules.Effect, name string) bool {
	for _, e := range effs {
		if e.Name == name {
			return true
		}
	}
	return false
}

// --- cart_started: exactamente una vez, en el primer Step -------------------

func TestEffectCartStartedOnce(t *testing.T) {
	m := New()
	vars := seededVars()

	// Primer Step (elige Bebidas): emite cart_started + category_selected.
	_, effs, vars := driveE(t, m, vars, "1")
	if !hasEffect(effs, EffectCartStarted) {
		t.Fatalf("primer Step debe emitir cart_started, got %+v", effs)
	}
	started := effByName(t, effs, EffectCartStarted)
	if started.Kind != kindEvent || len(started.Payload) != 0 {
		t.Fatalf("cart_started debe ser event con payload vacío, got %+v", started)
	}

	// Segundo Step: NO debe repetir cart_started.
	_, effs2, _ := driveE(t, m, vars, "1") // elige Café
	if hasEffect(effs2, EffectCartStarted) {
		t.Fatalf("cart_started no debe repetirse en el segundo Step, got %+v", effs2)
	}
}

// --- category_selected{category_code} --------------------------------------

func TestEffectCategorySelected(t *testing.T) {
	m := New()
	vars := seededVars()
	_, effs, _ := driveE(t, m, vars, "2") // Postres
	e := effByName(t, effs, EffectCategorySelected)
	if e.Kind != kindEvent || e.Payload["category_code"] != "2" {
		t.Fatalf("category_selected inesperado: %+v", e)
	}
}

// --- item_viewed{sku} ------------------------------------------------------

func TestEffectItemViewed(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles Bebidas
	_, _, vars = driveE(t, m, vars, "1") // article Café
	_, effs, _ := driveE(t, m, vars, "1")
	e := effByName(t, effs, EffectItemViewed)
	if e.Kind != kindEvent || e.Payload["sku"] != "CAFE" {
		t.Fatalf("item_viewed inesperado: %+v", e)
	}
}

// --- item_added{sku,label,qty,unit_price} ----------------------------------

func TestEffectItemAdded(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles
	_, _, vars = driveE(t, m, vars, "1") // article Café
	_, _, vars = driveE(t, m, vars, "2") // quantity
	_, effs, _ := driveE(t, m, vars, "2")
	e := effByName(t, effs, EffectItemAdded)
	if e.Kind != kindEvent {
		t.Fatalf("item_added debe ser event, got %+v", e)
	}
	if e.Payload["sku"] != "CAFE" || e.Payload["label"] != "Café" ||
		e.Payload["qty"] != 2 || e.Payload["unit_price"] != 2.5 {
		t.Fatalf("item_added payload inesperado: %+v", e.Payload)
	}
}

// --- cart_closed{items:[{sku,label,qty,unit_price}], total} ----------------

func TestEffectCartClosed(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles
	_, _, vars = driveE(t, m, vars, "1") // Café
	_, _, vars = driveE(t, m, vars, "2") // agregar
	_, _, vars = driveE(t, m, vars, "2") // cantidad 2 → continue
	_, _, vars = driveE(t, m, vars, "2") // finalizar → summary
	_, effs, _ := driveE(t, m, vars, "1")

	e := effByName(t, effs, EffectCartClosed)
	if e.Kind != kindPersist {
		t.Fatalf("cart_closed debe ser persist, got %+v", e)
	}
	if e.Payload["total"] != 5.0 {
		t.Fatalf("cart_closed total esperado 5.0, got %v", e.Payload["total"])
	}
	items, ok := e.Payload["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("cart_closed items inesperado: %+v", e.Payload["items"])
	}
	it := items[0]
	if it["sku"] != "CAFE" || it["label"] != "Café" || it["qty"] != 2 || it["unit_price"] != 2.5 {
		t.Fatalf("cart_closed item inesperado: %+v", it)
	}
}

// --- cart_cancelled{} (desde continue y desde summary) ---------------------

func TestEffectCartCancelledFromContinue(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles
	_, _, vars = driveE(t, m, vars, "1") // article
	_, _, vars = driveE(t, m, vars, "2") // quantity
	_, _, vars = driveE(t, m, vars, "1") // cantidad → continue
	_, effs, _ := driveE(t, m, vars, "9")
	e := effByName(t, effs, EffectCartCancelled)
	if e.Kind != kindEvent || len(e.Payload) != 0 {
		t.Fatalf("cart_cancelled debe ser event con payload vacío, got %+v", e)
	}
}

func TestEffectCartCancelledFromSummary(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles
	_, _, vars = driveE(t, m, vars, "1") // article
	_, _, vars = driveE(t, m, vars, "2") // quantity
	_, _, vars = driveE(t, m, vars, "1") // cantidad → continue
	_, _, vars = driveE(t, m, vars, "2") // finalizar → summary
	_, effs, _ := driveE(t, m, vars, "9")
	if !hasEffect(effs, EffectCartCancelled) {
		t.Fatalf("9 en summary debe emitir cart_cancelled, got %+v", effs)
	}
}

// --- Navegación sin efecto de negocio --------------------------------------

func TestNoEffectOnNavigation(t *testing.T) {
	m := New()
	vars := seededVars()
	_, _, vars = driveE(t, m, vars, "1") // articles Bebidas (category_selected + cart_started)
	// Elegir artículo (article) NO emite efecto de negocio; "volver" tampoco.
	_, effs, vars := driveE(t, m, vars, "1") // article Café
	if len(effs) != 0 {
		t.Fatalf("elegir artículo no debe emitir efectos, got %+v", effs)
	}
	_, effs, _ = driveE(t, m, vars, "0") // volver a articles
	if len(effs) != 0 {
		t.Fatalf("volver no debe emitir efectos, got %+v", effs)
	}
}
