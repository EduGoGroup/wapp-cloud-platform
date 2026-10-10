//go:build pendiente

package cart_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// buyer_test.go — el checklist de datos del comprador (Plan 041 · T4.5, D-041.13),
// visto por Module.Step. buyer.go solo exporta la clave de Vars: lo demás es conducta.

const (
	summaryOneCoffee = "🧾 Resumen del pedido:\nCafé x1  $2.50\nTOTAL  $2.50\n" +
		"1) Confirmar y finalizar\n2) Seguir agregando\n3) ✏️ Indicación para todo el pedido\n9) Cancelar pedido"
	closedOneCoffee = "✅ ¡Pedido confirmado! Total $2.50."
	rutValue        = "12.345.678-5"
	addressValue    = "Av. Siempre Viva 742"
)

// buyerScreen es la pantalla que pide el campo `label`, el n-ésimo de total.
func buyerScreen(label string, n, total string) string {
	return "📋 Falta un dato para completar tu pedido (" + n + " de " + total + "):" +
		"\nEscribe tu " + label + "." +
		"\nSe guarda protegido y solo lo ve el negocio." +
		"\n0) ← Volver al resumen"
}

// twoFields es un checklist con dos campos requeridos y, entre ellos, dos que el
// carrito NO pregunta: uno opcional y uno sin clave.
func twoFields() []store.BuyerField {
	return []store.BuyerField{
		{Key: "rut", Label: "RUT", Required: true},
		{Key: "apodo", Label: "apodo", Required: false},
		{Key: "", Label: "sin clave", Required: true},
		{Key: "direccion", Label: "dirección de entrega", Required: true},
	}
}

// atSummary deja el carrito en el resumen con un Café y el checklist sembrado.
func atSummary(t *testing.T, m cart.Module, fields any) map[string]any {
	t.Helper()
	vars := seededVars()
	if fields != nil {
		vars[cart.VarBuyerFields] = fields
	}
	return walk(t, m, vars, "1", "1", "2", "1", "2")
}

// VarBuyerFields es la clave que siembra el runtime.
func TestVarBuyerFields_IsTheSeededKey(t *testing.T) {
	if cart.VarBuyerFields != "cart_buyer_fields" {
		t.Errorf("VarBuyerFields = %q, quiero \"cart_buyer_fields\"", cart.VarBuyerFields)
	}
}

// INV-15: sin checklist que preguntar, el 1 del resumen cierra como siempre, con la
// misma pantalla y el mismo único efecto.
func TestBuyer_WithoutChecklistConfirmClosesAsAlways(t *testing.T) {
	cases := []struct {
		name   string
		fields any
	}{
		{name: "absent", fields: nil},
		{name: "empty", fields: []store.BuyerField{}},
		{name: "only optional or keyless", fields: []store.BuyerField{{Key: "apodo", Label: "apodo"}, {Label: "x", Required: true}}},
		{name: "unreadable", fields: "no soy una lista"},
		{name: "list of the wrong shape", fields: []any{"rut", 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cart.New()
			res := turn(m, model.Conversation{Vars: atSummary(t, m, tc.fields)}, "1", nil)
			mustScreen(t, res.Outputs, closedOneCoffee)
			if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartClosed}) {
				t.Errorf("efectos = %v, quiero solo cart_closed", got)
			}
			if st := stateOf(t, res.Vars); st.Level != cart.LevelClosed || st.BuyerIdx != 0 {
				t.Errorf("estado = %+v, quiero closed sin contador", st)
			}
		})
	}
}

// Con checklist: un campo requerido por mensaje, en el orden configurado, y el pedido
// cierra en el mismo turno del último, con el efecto del comprador ANTES de cart_closed.
func TestBuyer_AsksEachRequiredFieldBeforeClosing(t *testing.T) {
	m := cart.New()
	vars := atSummary(t, m, twoFields())

	res := turn(m, model.Conversation{Vars: vars}, "1", nil)
	mustScreen(t, res.Outputs, buyerScreen("RUT", "1", "2"))
	if len(res.Effects) != 0 || res.Next != nil {
		t.Fatalf("confirmar con checklist: efectos %v y Next %v; aún no se cierra nada", effectNames(res.Effects), res.Next)
	}
	if st := stateOf(t, res.Vars); st.Level != cart.LevelBuyerData || st.BuyerIdx != 0 {
		t.Fatalf("estado = %+v, quiero buyer_data con el contador a cero", st)
	}

	res = turn(m, model.Conversation{Vars: res.Vars}, rutValue, nil)
	mustScreen(t, res.Outputs, "Anotado: RUT ✅\n"+buyerScreen("dirección de entrega", "2", "2"))
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectBuyerDataCaptured}) {
		t.Fatalf("efectos = %v, quiero solo buyer_data_captured", got)
	}
	if st := stateOf(t, res.Vars); st.Level != cart.LevelBuyerData || st.BuyerIdx != 1 {
		t.Fatalf("estado = %+v, quiero buyer_data con un campo capturado", st)
	}

	res = turn(m, model.Conversation{Vars: res.Vars}, addressValue, nil)
	mustScreen(t, res.Outputs, closedOneCoffee)
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectBuyerDataCaptured, cart.EffectCartClosed}) {
		t.Fatalf("efectos = %v, quiero buyer_data_captured y DESPUÉS cart_closed", got)
	}
	if got := jsonOf(t, res.Effects[0].Payload); got != `{"key":"direccion","value":"`+addressValue+`"}` {
		t.Errorf("efecto del último campo = %s", got)
	}
	if res.Next == nil || *res.Next != model.NodeTerminal || res.Outcome != model.OutcomeCompleted {
		t.Errorf("Next = %v, Outcome = %q; el último campo cierra el flujo", res.Next, res.Outcome)
	}
	if st := stateOf(t, res.Vars); st.Level != cart.LevelClosed || st.BuyerIdx != 2 {
		t.Errorf("estado = %+v, quiero closed con los dos campos contados", st)
	}
}

// 🔴 NO FUGA: el valor que teclea el cliente no queda en ninguna parte de las Vars
// (flow_state.vars es JSONB en claro), ni en un efecto público, ni en una pantalla.
func TestBuyer_ValueNeverStaysInVarsScreensOrPublicEffects(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atSummary(t, m, twoFields()), "1")
	for _, value := range []string{rutValue, addressValue} {
		res := turn(m, model.Conversation{Vars: vars}, value, nil)
		if dump := jsonOf(t, res.Vars); strings.Contains(dump, value) {
			t.Fatalf("el valor %q quedó en las Vars: %s", value, dump)
		}
		mustNotContain(t, res.Outputs, value)
		for _, e := range res.Effects {
			if e.Kind == modules.KindPrivate {
				continue
			}
			if dump := jsonOf(t, e.Payload); strings.Contains(dump, value) {
				t.Fatalf("el valor %q salió en el efecto público %s: %s", value, e.Name, dump)
			}
		}
		vars = res.Vars
	}
}

// El 0 vuelve al resumen SIN perder lo capturado, y confirmar otra vez retoma en el
// campo siguiente: no se le pide dos veces el RUT a nadie.
func TestBuyer_ZeroGoesBackToSummaryKeepingWhatWasCaptured(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atSummary(t, m, twoFields()), "1", rutValue)

	st, outs, vars := drive(t, m, vars, "0")
	if st.Level != cart.LevelSummary || st.BuyerIdx != 1 {
		t.Fatalf("estado = %+v, quiero el resumen con el contador en 1", st)
	}
	mustScreen(t, outs, summaryOneCoffee)

	st, outs, _ = drive(t, m, vars, "1")
	if st.Level != cart.LevelBuyerData || st.BuyerIdx != 1 {
		t.Fatalf("estado = %+v, quiero retomar en el segundo campo", st)
	}
	mustScreen(t, outs, buyerScreen("dirección de entrega", "2", "2"))
}

// Un campo obligatorio no se salta: vacío tras sanear repregunta el MISMO campo, sin
// efecto y sin mover el contador.
func TestBuyer_RequiredFieldCannotBeSkipped(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atSummary(t, m, twoFields()), "1")
	for _, in := range []string{"", "   ", "​​", "\n\t"} {
		res := turn(m, model.Conversation{Vars: vars}, in, nil)
		mustScreen(t, res.Outputs, "Necesitamos ese dato para completar tu pedido.\n\n"+buyerScreen("RUT", "1", "2"))
		if len(res.Effects) != 0 {
			t.Errorf("%q: efectos = %v, quiero ninguno", in, effectNames(res.Effects))
		}
		if st := stateOf(t, res.Vars); st.Level != cart.LevelBuyerData || st.BuyerIdx != 0 {
			t.Errorf("%q: estado = %+v, quiero el mismo campo", in, st)
		}
	}
}

// Pasarse del largo RECHAZA, con el número real, y no trunca: un RUT o una dirección
// truncados serían un dato falso. Se porta el aviso tal cual, que habla de
// «indicación» también aquí.
func TestBuyer_TooLongIsRejectedNotTruncated(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atSummary(t, m, twoFields()), "1")
	res := turn(m, model.Conversation{Vars: vars}, strings.Repeat("ñ", 281), nil)
	mustScreen(t, res.Outputs,
		"Esa indicación es muy larga (281 de 280 caracteres). Escríbela más corta.\n\n"+buyerScreen("RUT", "1", "2"))
	if len(res.Effects) != 0 {
		t.Errorf("efectos = %v, quiero ninguno", effectNames(res.Effects))
	}
	if st := stateOf(t, res.Vars); st.BuyerIdx != 0 {
		t.Errorf("estado = %+v, el contador no debía moverse", st)
	}
	// Justo en el límite sí entra.
	res = turn(m, model.Conversation{Vars: vars}, strings.Repeat("ñ", 280), nil)
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectBuyerDataCaptured}) {
		t.Errorf("280 runas: efectos = %v, quiero buyer_data_captured", got)
	}
}

// Lo que llega al efecto es el valor SANEADO (higiene de la puerta), no validado.
func TestBuyer_ValueIsSanitizedNotValidated(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, atSummary(t, m, twoFields()), "1")
	res := turn(m, model.Conversation{Vars: vars}, "  no​ soy\nun   RUT  ", nil)
	if len(res.Effects) != 1 {
		t.Fatalf("efectos = %v, quiero uno", effectNames(res.Effects))
	}
	if got := res.Effects[0].Payload["value"]; got != "no soy un RUT" {
		t.Errorf("value = %q, quiero el texto saneado \"no soy un RUT\"", got)
	}
}

// Si el dueño recorta su checklist a media conversación, el contador por encima de la
// lista no bloquea el cierre ni deja al cliente atrapado.
func TestBuyer_TrimmedChecklistDoesNotBlockTheClose(t *testing.T) {
	oneField := []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}}

	t.Run("from summary", func(t *testing.T) {
		vars := seededVars()
		vars[cart.VarBuyerFields] = oneField
		seedState(t, vars, cartState{Level: cart.LevelSummary, CatCode: "1", Started: true, BuyerIdx: 3,
			Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5}}})
		res := turn(cart.New(), model.Conversation{Vars: vars}, "1", nil)
		mustScreen(t, res.Outputs, closedOneCoffee)
		if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartClosed}) {
			t.Errorf("efectos = %v, quiero solo cart_closed", got)
		}
	})
	t.Run("already inside the checklist", func(t *testing.T) {
		vars := seededVars()
		vars[cart.VarBuyerFields] = oneField
		seedState(t, vars, cartState{Level: cart.LevelBuyerData, CatCode: "1", Started: true, BuyerIdx: 3,
			Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5}}})
		res := turn(cart.New(), model.Conversation{Vars: vars}, "un texto cualquiera", nil)
		mustScreen(t, res.Outputs, closedOneCoffee)
		if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartClosed}) {
			t.Errorf("efectos = %v: un texto que ya no hay dónde guardar no produce efecto del comprador", got)
		}
	})
}

// El checklist se lee tras el round-trip JSONB ([]any de map[string]any), y un campo
// sin etiqueta se pregunta y se acusa por su clave.
func TestBuyer_ReadsFieldsAfterJSONBRoundTripAndFallsBackToKey(t *testing.T) {
	fields := []any{
		map[string]any{"key": "rut", "label": "", "required": true},
		map[string]any{"key": "ref", "label": "referencia", "required": true},
		map[string]any{"key": "extra", "label": "extra", "required": false},
	}
	m := cart.New()
	vars := atSummary(t, m, fields)
	_, outs, vars := drive(t, m, vars, "1")
	mustScreen(t, outs, buyerScreen("rut", "1", "2"))
	_, outs, _ = drive(t, m, vars, rutValue)
	mustScreen(t, outs, "Anotado: rut ✅\n"+buyerScreen("referencia", "2", "2"))
}

// El nivel del checklist es texto libre con dato personal: ni consulta ni cascada, sea
// lo que sea lo que el cliente escriba.
func TestBuyer_LevelNeverAsksNorMatches(t *testing.T) {
	calls := 0
	m := cart.New(cart.WithMatchHook(func(string, string) { calls++ }))
	vars := walk(t, m, atSummary(t, m, twoFields()), "1")
	for _, in := range []string{"cancelar pedido", "Bebidas", "mejor dos", "confirmar y finalizar"} {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, in)
		if res.Query != nil {
			t.Fatalf("%q: el checklist elevó una consulta con el dato del cliente: %+v", in, *res.Query)
		}
		if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectBuyerDataCaptured}) {
			t.Errorf("%q: efectos = %v, quiero que se capture como dato", in, got)
		}
	}
	if calls != 0 {
		t.Errorf("el observador recibió %d avisos desde el checklist, quiero 0", calls)
	}
}
