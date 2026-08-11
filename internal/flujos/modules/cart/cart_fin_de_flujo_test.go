package cart

// cart_fin_de_flujo_test.go — el contrato de Step: confirmar el pedido (#24) y,
// desde el hallazgo #29 (salida (A), decisión de Jhoan 2026-08-11), cancelarlo
// también DECLARAN el fin del flujo (Result.Next = centinela). Hasta la revisión que
// fijó #24, ese contrato solo lo fijaban dos e2e de internal/publicapi que se OMITEN
// sin WAPP_TEST_DB_DSN; el módulo es PURO y su contrato con el engine se puede —y se
// debe— fijar aquí, sin base de datos.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// pasoConNext es drive() pero conservando el Result ENTERO: lo que este archivo mide
// es precisamente el campo que drive descarta (Next).
func pasoConNext(t *testing.T, m Module, vars map[string]any, input string) (cartState, map[string]any, *string) {
	t.Helper()
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, input)
	return loadState(res.Vars), res.Vars, res.Next
}

// varsHastaResumen deja la sub-máquina en L6 (resumen) con Café ×2, que es el único
// sitio desde el que se puede confirmar o cancelar el pedido entero.
func varsHastaResumen(t *testing.T, m Module) map[string]any {
	t.Helper()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2", "2", "2"} { // Bebidas→Café→Agregar→x2→Finalizar
		_, _, vars = drive(t, m, vars, in)
	}
	if lvl := loadState(vars).Level; lvl != LevelSummary {
		t.Fatalf("precondición: esperaba %q, got %q", LevelSummary, lvl)
	}
	return vars
}

// TestStepPedidoConfirmadoDeclaraFinDeFlujo: el turno que CONFIRMA apunta Next al
// centinela — es lo que hace que engine.Step termine el flujo y closeIfFinished
// cierre el evento del pedido en ese mismo turno (#24). Sin esto el evento quedaba
// `open` para siempre y el pedido siguiente de la misma conversación se perdía en
// silencio contra intakes_event_id_uidx.
func TestStepPedidoConfirmadoDeclaraFinDeFlujo(t *testing.T) {
	m := New()
	st, _, next := pasoConNext(t, m, varsHastaResumen(t, m), "1")
	if st.Level != LevelClosed {
		t.Fatalf("confirmar debe dejar la sub-máquina en %q; está en %q", LevelClosed, st.Level)
	}
	if next == nil {
		t.Fatal("el pedido CONFIRMADO debe declarar el fin del flujo (Result.Next = centinela); Next es nil")
	}
	if *next != model.NodeTerminal {
		t.Fatalf("Result.Next = %q, quiero el centinela %q", *next, model.NodeTerminal)
	}
}

// TestStepNavegacionNoDeclaraFinDeFlujo es el CONTROL: mientras se navega, el carrito
// permanece en su nodo y NO debe declarar ningún fin. Es lo que separa «el pedido se
// cerró» de «el cliente pulsó una tecla».
func TestStepNavegacionNoDeclaraFinDeFlujo(t *testing.T) {
	m := New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2", "2", "2"} {
		var next *string
		_, vars, next = pasoConNext(t, m, vars, in)
		if next != nil {
			t.Fatalf("navegando (%q) el carrito NO debe declarar fin de flujo; Next = %q", in, *next)
		}
	}
}

// TestStepPedidoCanceladoDeclaraFinDeFlujo: desde el hallazgo #29 (salida (A),
// decisión de Jhoan 2026-08-11) cancelar declara fin de flujo exactamente igual que
// confirmar (#24) — es la MISMA condición en Step (newSt.Level == LevelClosed ||
// LevelCancelled), no una regla aparte. ⚠️ Este test fijaba antes la asimetría
// CONTRARIA a propósito (confirmar cerraba, cancelar no): medido en revisión, esa
// asimetría dejaba el evento que contenía el pedido `open` PARA SIEMPRE tras
// cancelar —closeIfFinished nunca se disparaba— y el «carrito» siguiente de la
// misma conversación chocaba en silencio contra `intakes_event_id_uidx`, el MISMO
// síntoma del #24, solo que por la puerta de cancelar. Esa razón dejó de aplicar:
// Jhoan decidió (2026-08-11) que la salida (A) —cerrar también al cancelar— entra
// YA, no se hereda al Plan 045. QUIEN REVIERTA ESTO TIENE QUE VENIR A DECIRLO AQUÍ.
func TestStepPedidoCanceladoDeclaraFinDeFlujo(t *testing.T) {
	m := New()
	st, _, next := pasoConNext(t, m, varsHastaResumen(t, m), codeCancelar)
	if st.Level != LevelCancelled {
		t.Fatalf("cancelar debe dejar la sub-máquina en %q; está en %q", LevelCancelled, st.Level)
	}
	if next == nil {
		t.Fatal("el pedido CANCELADO debe declarar el fin del flujo (Result.Next = centinela, hallazgo #29); Next es nil")
	}
	if *next != model.NodeTerminal {
		t.Fatalf("Result.Next = %q, quiero el centinela %q", *next, model.NodeTerminal)
	}
}
