package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// cart_step_test.go — lo que Module.Step promete como envoltura de la sub-máquina:
// pureza, catálogo ausente, cart_started una vez, el fin de flujo y su desenlace
// (H24, H29, Ola 6) y la pasada que solo pide.

// Sin catálogo sembrado: la pantalla de no disponible, sin efectos, sin transición y
// sin estado guardado.
func TestStep_WithoutCatalog(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]any
	}{
		{name: "nil vars", vars: nil},
		{name: "no snapshot", vars: map[string]any{}},
		{name: "snapshot of another type", vars: map[string]any{modules.VarContentRaw: "texto"}},
		{name: "snapshot without categories", vars: map[string]any{modules.VarContentRaw: map[string]any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := cart.New().Step(model.Node{}, model.Conversation{Vars: tc.vars}, "1")
			mustScreen(t, res.Outputs, catalogUnavailable)
			if len(res.Effects) != 0 || res.Next != nil || res.Query != nil || res.Outcome != model.OutcomeUndeclared {
				t.Errorf("Result = %+v, quiero solo la pantalla", res)
			}
			if _, stored := res.Vars[stateVarKey]; stored {
				t.Errorf("Vars = %v, no debía guardarse estado", res.Vars)
			}
		})
	}
}

// Step no muta las Vars de la conversación: trabaja sobre una copia.
func TestStep_DoesNotMutateConversationVars(t *testing.T) {
	vars := seededVars()
	before := jsonOf(t, vars)
	res := cart.New().Step(model.Node{}, model.Conversation{Vars: vars}, "1")
	if got := jsonOf(t, vars); got != before {
		t.Errorf("las Vars de entrada cambiaron:\n antes %s\n ahora %s", before, got)
	}
	if _, stored := vars[stateVarKey]; stored {
		t.Error("el estado se escribió en el mapa de entrada")
	}
	if _, stored := res.Vars[stateVarKey]; !stored {
		t.Error("el estado no está en las Vars devueltas")
	}
	if reflect.ValueOf(res.Vars).Pointer() == reflect.ValueOf(vars).Pointer() {
		t.Error("Result.Vars es el mismo mapa que el de entrada")
	}
}

// La entrada se recorta antes de mirarla.
func TestStep_TrimsInput(t *testing.T) {
	st, _, _ := drive(t, cart.New(), seededVars(), "  2\n")
	if st.Level != cart.LevelArticles || st.CatCode != "2" {
		t.Errorf("estado = %+v, quiero los artículos de Postres", st)
	}
}

// cart_started: exactamente una vez, en el primer Step, delante de los efectos de
// la transición; también si el primer mensaje es inválido.
func TestStep_DeclaresCartStartedExactlyOnce(t *testing.T) {
	m := cart.New()
	_, effs, vars := driveEffects(t, m, seededVars(), "1")
	if got := effectNames(effs); !reflect.DeepEqual(got, []string{cart.EffectCartStarted, cart.EffectCategorySelected}) {
		t.Fatalf("efectos del primer Step = %v, quiero cart_started y category_selected, en ese orden", got)
	}
	started := effs[0]
	if started.Kind != "event" || started.Payload == nil || len(started.Payload) != 0 || len(started.PrivateKeys) != 0 {
		t.Errorf("cart_started = %+v, quiero Kind event y payload {}", started)
	}
	for _, in := range []string{"1", "1", "0", "zzz"} {
		_, effs, vars = driveEffects(t, m, vars, in)
		for _, e := range effs {
			if e.Name == cart.EffectCartStarted {
				t.Fatalf("cart_started se repitió tras %q: %v", in, effectNames(effs))
			}
		}
	}

	st, effs, _ := driveEffects(t, m, seededVars(), "zzz")
	if got := effectNames(effs); !reflect.DeepEqual(got, []string{cart.EffectCartStarted}) || !st.Started {
		t.Errorf("primer Step inválido: efectos %v y estado %+v, quiero cart_started y started", got, st)
	}
}

// Mientras se navega el carrito permanece en su nodo: ni Next ni desenlace.
func TestStep_NavigationDoesNotDeclareEndOfFlow(t *testing.T) {
	m := cart.New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2", "2", "2"} { // … hasta el resumen
		res := turn(m, model.Conversation{Vars: vars}, in, nil)
		if res.Next != nil || res.Outcome != model.OutcomeUndeclared {
			t.Fatalf("tras %q: Next = %v, Outcome = %q; navegando no se declara ningún fin", in, res.Next, res.Outcome)
		}
		vars = res.Vars
	}
}

// H24 / Ola 6: el turno que CONFIRMA apunta Next al centinela con desenlace
// completado, y la pantalla final ya va en ese mismo Result.
func TestStep_ConfirmedOrderDeclaresEndOfFlow(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "2", "2")
	res := turn(m, model.Conversation{Vars: vars}, "1", nil)
	if res.Next == nil || *res.Next != model.NodeTerminal {
		t.Fatalf("Next = %v, quiero el centinela model.NodeTerminal", res.Next)
	}
	if res.Outcome != model.OutcomeCompleted {
		t.Errorf("Outcome = %q, quiero %q", res.Outcome, model.OutcomeCompleted)
	}
	mustScreen(t, res.Outputs, "✅ ¡Pedido confirmado! Total $5.00.")
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartClosed}) {
		t.Errorf("efectos = %v, quiero solo cart_closed", got)
	}
	if st := stateOf(t, res.Vars); st.Level != cart.LevelClosed {
		t.Errorf("estado = %+v, quiero closed", st)
	}
}

// H29: cancelar DENTRO del flujo declara el mismo fin, con desenlace cancelado, desde
// los dos menús que lo ofrecen; el efecto sigue al estado.
func TestStep_CancelledOrderDeclaresEndOfFlow(t *testing.T) {
	cases := []struct {
		name string
		path []string
	}{
		{name: "from continue", path: []string{"1", "1", "2", "1"}},
		{name: "from summary", path: []string{"1", "1", "2", "1", "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cart.New()
			vars := walk(t, m, seededVars(), tc.path...)
			res := turn(m, model.Conversation{Vars: vars}, "9", nil)
			if res.Next == nil || *res.Next != model.NodeTerminal {
				t.Fatalf("Next = %v, quiero el centinela", res.Next)
			}
			if res.Outcome != model.OutcomeCancelled {
				t.Errorf("Outcome = %q, quiero %q", res.Outcome, model.OutcomeCancelled)
			}
			mustScreen(t, res.Outputs, `Pedido cancelado. Si quieres hacer otro, escribe "carrito" y lo empezamos de cero.`)
			if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartCancelled}) {
				t.Errorf("efectos = %v, quiero solo cart_cancelled", got)
			}
			if st := stateOf(t, res.Vars); st.Level != cart.LevelCancelled {
				t.Errorf("estado = %+v, quiero cancelled", st)
			}
		})
	}
}

// Un estado legado ya terminal y sin centinela: ignora la entrada, re-muestra su
// pantalla final y vuelve a declarar el fin, sin efecto de cierre.
func TestStep_LegacyTerminalStateIgnoresInput(t *testing.T) {
	cases := []struct {
		level   string
		screen  string
		outcome model.Outcome
	}{
		{cart.LevelClosed, "✅ ¡Pedido confirmado! Total $2.50.", model.OutcomeCompleted},
		{cart.LevelCancelled, `Pedido cancelado. Si quieres hacer otro, escribe "carrito" y lo empezamos de cero.`, model.OutcomeCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			vars := seededVars()
			seedState(t, vars, cartState{Level: tc.level, Started: true,
				Lines: []cartLine{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5}}})
			res := turn(cart.New(), model.Conversation{Vars: vars}, "cualquier cosa", nil)
			mustScreen(t, res.Outputs, tc.screen)
			if len(res.Effects) != 0 {
				t.Errorf("efectos = %v, quiero ninguno", effectNames(res.Effects))
			}
			if res.Next == nil || *res.Next != model.NodeTerminal || res.Outcome != tc.outcome {
				t.Errorf("Next = %v, Outcome = %q; quiero el centinela y %q", res.Next, res.Outcome, tc.outcome)
			}
			if st := stateOf(t, res.Vars); st.Level != tc.level || len(st.Lines) != 1 {
				t.Errorf("estado = %+v, quiero el mismo nivel terminal con su línea", st)
			}
		})
	}
}

// Un nivel desconocido se reencauza a categorías conservando lo que el cliente ya
// dijo: líneas, started, la nota del pedido y el contador del comprador.
func TestStep_UnknownLevelGoesBackToCategories(t *testing.T) {
	vars := seededVars()
	seedState(t, vars, cartState{
		Level: "nivel_que_no_existe", CatCode: "1", SKU: "CAFE", Page: 3, VariantCode: "V1",
		Started: true, Note: "en portería", NoteSplit: true, BuyerIdx: 2,
		Lines: []cartLine{{SKU: "TE", Label: "Té", Qty: 2, UnitPrice: 2}},
	})
	st, outs, _ := drive(t, cart.New(), vars, "1")
	mustScreen(t, outs, categoriesScreen)
	want := cartState{Level: cart.LevelCategories, Started: true, Note: "en portería", BuyerIdx: 2,
		Lines: []cartLine{{SKU: "TE", Label: "Té", Qty: 2, UnitPrice: 2}}}
	if !reflect.DeepEqual(st, want) {
		t.Errorf("estado = %+v\nquiero   %+v", st, want)
	}
}

// 🔴 La pasada que PIDE no produce nada: solo la petición y la copia de Vars sin
// tocar. Si declarara algo, el engine lo perdería al descartar el Result y la segunda
// pasada lo declararía otra vez.
func TestStep_QueryPassProducesNothingElse(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2") // nivel de la cantidad
	before := jsonOf(t, vars)

	res := m.Step(model.Node{}, model.Conversation{Vars: vars, EventID: "ev-1"}, "mejor dos")
	if res.Query == nil {
		t.Fatal("Query = nil, quiero la petición: «mejor dos» no es un número")
	}
	if len(res.Outputs) != 0 || len(res.Effects) != 0 || res.Next != nil || res.Outcome != model.OutcomeUndeclared {
		t.Errorf("Result = %+v, la pasada que pide no puede producir nada", res)
	}
	if got := jsonOf(t, res.Vars); got != before {
		t.Errorf("Vars de la pasada que pide:\n %s\nquiero las de entrada, sin tocar:\n %s", got, before)
	}
}

// 🔴 En el PRIMER mensaje del carrito la petición sale antes de `started`: la primera
// pasada no declara cart_started, y el turno completo lo declara UNA vez y cuenta UN
// inválido (trampa T-11).
func TestStep_QueryIsRaisedBeforeAnyMutation(t *testing.T) {
	m := cart.New()
	conv := model.Conversation{Vars: seededVars(), EventID: "ev-1"}

	first := m.Step(model.Node{}, conv, "algo rico")
	if first.Query == nil {
		t.Fatal("Query = nil, quiero la petición: la cascada no casa nada")
	}
	if len(first.Effects) != 0 {
		t.Fatalf("efectos de la pasada que pide = %v, quiero ninguno", effectNames(first.Effects))
	}
	if _, stored := first.Vars[stateVarKey]; stored {
		t.Fatal("la pasada que pide guardó estado")
	}

	res := turn(m, conv, "algo rico", nil)
	if got := effectNames(res.Effects); !reflect.DeepEqual(got, []string{cart.EffectCartStarted}) {
		t.Errorf("efectos del turno = %v, quiero cart_started una sola vez", got)
	}
	if st := stateOf(t, res.Vars); st.Reprompts != 1 || st.RepromptsEvent != "ev-1" {
		t.Errorf("estado = %+v, quiero UN inválido sellado con ev-1", st)
	}
	if _, left := res.Vars[modules.VarQueryVerdict]; left {
		t.Error("el veredicto sobrevivió al turno")
	}
}

// Con el veredicto ya sembrado el módulo no vuelve a pedir, resuelva o no.
func TestStep_DoesNotAskAgainWithVerdictSeeded(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2") // nivel de la cantidad
	for _, v := range []modules.Verdict{
		{Reason: modules.QueryReasonNoResolver},
		{Reason: modules.QueryReasonFailure},
		{Reason: modules.QueryReasonInconclusive},
		{Code: "no es un número"},
		{},
	} {
		res := m.Step(model.Node{}, model.Conversation{Vars: modules.WithVerdict(vars, v)}, "mejor dos")
		if res.Query != nil {
			t.Fatalf("veredicto %+v: el módulo volvió a pedir", v)
		}
		mustContain(t, res.Outputs, "Escribe una cantidad válida (un número mayor o igual a 1).")
	}
}
