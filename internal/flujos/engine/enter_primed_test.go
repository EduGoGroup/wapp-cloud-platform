package engine_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// menuFlowForPrime es un flujo de menú simple (un nodo interactivo sin capacidad
// Primer): sirve para verificar la NO-REGRESIÓN de EnterPrimed.
func menuFlowForPrime() model.Flow {
	return model.Flow{
		FlowID:  "menu",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: "Hola\n1) A\n2) B", Options: map[string]string{"1": "a", "2": "b"}},
			"a":    {Type: model.NodeTypeMessage, Text: "A"},
			"b":    {Type: model.NodeTypeMessage, Text: "B"},
		},
	}
}

// TestEnterPrimed_NoParams_EqualsEnter: sin intent_params, EnterPrimed produce el
// MISMO estado y salidas que Enter (y efectos nil): el arranque por API no cambia.
func TestEnterPrimed_NoParams_EqualsEnter(t *testing.T) {
	e := newEngine()
	f := menuFlowForPrime()
	stEnter, outsEnter, err := e.Enter(context.Background(), f, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	stPrimed, outsPrimed, effects, err := e.EnterPrimed(context.Background(), f, model.Conversation{})
	if err != nil {
		t.Fatalf("EnterPrimed: %v", err)
	}
	if stPrimed.CurrentNode != stEnter.CurrentNode {
		t.Fatalf("EnterPrimed debe posicionar en el mismo nodo que Enter: %q vs %q", stPrimed.CurrentNode, stEnter.CurrentNode)
	}
	if len(outsPrimed) != len(outsEnter) || (len(outsEnter) > 0 && outsPrimed[0].Text != outsEnter[0].Text) {
		t.Fatalf("salidas distintas: %+v vs %+v", outsPrimed, outsEnter)
	}
	if len(effects) != 0 {
		t.Fatalf("sin Primer no debe declarar efectos, got %d", len(effects))
	}
}

// TestEnterPrimed_NonPrimerModule_IgnoresParams: aunque haya intent_params sembrados,
// un módulo que NO implementa Primer (menú) los ignora ⇒ Render normal (no-regresión).
func TestEnterPrimed_NonPrimerModule_IgnoresParams(t *testing.T) {
	e := newEngine()
	f := menuFlowForPrime()
	vars := map[string]any{modules.VarIntentParams: map[string]any{"producto": "x"}}
	st, outs, effects, err := e.EnterPrimed(context.Background(), f, model.Conversation{Vars: vars})
	if err != nil {
		t.Fatalf("EnterPrimed: %v", err)
	}
	if st.CurrentNode != "root" || len(outs) == 0 || len(effects) != 0 {
		t.Fatalf("un módulo sin Primer debe hacer Render normal e ignorar params, got node=%q outs=%d eff=%d", st.CurrentNode, len(outs), len(effects))
	}
}
