//go:build pendiente

package cart_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// cart_reprompt_test.go — el contador de inválidos de Module.Step (Plan 043 · T5.2,
// D-043.10): solo cuenta dentro de un evento, al tercero arma el menú de salida y va
// sellado con el evento en que se contó.

// turnIn ejecuta un turno dentro del evento dado y devuelve el Result.
func turnIn(m cart.Module, vars map[string]any, eventID, input string) modules.Result {
	return turn(m, model.Conversation{Vars: vars, EventID: eventID}, input, nil)
}

// Dentro de un evento, dos inválidos repromptean como siempre y el TERCERO arma el
// menú de salida sobre la pantalla del nivel, sin el aviso.
func TestStep_InEvent_ThirdInvalidArmsExitMenu(t *testing.T) {
	m := cart.New()
	vars := seededVars()
	for i := 1; i <= 2; i++ {
		res := turnIn(m, vars, "ev-cart-1", "zzz")
		mustScreen(t, res.Outputs, invalidPrefix+categoriesScreen)
		if _, armed := res.Vars[modules.ExitMenuVar]; armed {
			t.Fatalf("el inválido %d armó el menú de salida", i)
		}
		if st := stateOf(t, res.Vars); st.Reprompts != i || st.RepromptsEvent != "ev-cart-1" {
			t.Fatalf("inválido %d: estado = %+v, quiero el contador en %d sellado con el evento", i, st, i)
		}
		vars = res.Vars
	}

	res := turnIn(m, vars, "ev-cart-1", "zzz")
	mustScreen(t, res.Outputs, modules.ExitMenuText(categoriesScreen))
	if res.Vars[modules.ExitMenuVar] != categoriesScreen {
		t.Errorf("Vars[%q] = %v, quiero la pantalla del nivel", modules.ExitMenuVar, res.Vars[modules.ExitMenuVar])
	}
	if got := modules.ExitMenuArmedOn(res.Vars); got != "ev-cart-1" {
		t.Errorf("menú armado sobre %q, quiero ev-cart-1", got)
	}
	if res.Next != nil {
		t.Error("el menú de salida no transiciona: Next debía ser nil")
	}
	st := stateOf(t, res.Vars)
	if st.Level != cart.LevelCategories || st.Reprompts != 0 || st.RepromptsEvent != "" {
		t.Errorf("estado = %+v, quiero el mismo nivel con el contador a cero y sin sello", st)
	}
}

// Fuera de un evento el carrito repromptea sin techo y el estado no lleva contador
// (regresión cero).
func TestStep_OutsideEvent_NeverArmsNorCounts(t *testing.T) {
	m := cart.New()
	vars := seededVars()
	for i := 1; i <= 5; i++ {
		res := turnIn(m, vars, "", "zzz")
		mustScreen(t, res.Outputs, invalidPrefix+categoriesScreen)
		if _, armed := res.Vars[modules.ExitMenuVar]; armed {
			t.Fatalf("el inválido %d armó el menú de salida fuera de un evento", i)
		}
		if st := stateOf(t, res.Vars); st.Reprompts != 0 || st.RepromptsEvent != "" {
			t.Fatalf("inválido %d: estado = %+v, fuera de un evento no hay contador", i, st)
		}
		vars = res.Vars
	}
}

// Una entrada válida reinicia el contador y suelta el sello: dos rachas de dos
// inválidos separadas por un acierto no arman el menú.
func TestStep_InEvent_ValidInputResetsCounterAndSeal(t *testing.T) {
	m := cart.New()
	vars := turnIn(m, seededVars(), "ev-1", "zzz").Vars
	vars = turnIn(m, vars, "ev-1", "zzz").Vars

	res := turnIn(m, vars, "ev-1", "1") // Bebidas
	if st := stateOf(t, res.Vars); st.Level != cart.LevelArticles || st.Reprompts != 0 || st.RepromptsEvent != "" {
		t.Fatalf("estado = %+v, quiero articles con el contador a cero y sin sello", st)
	}
	vars = res.Vars
	for i := 1; i <= 2; i++ {
		res = turnIn(m, vars, "ev-1", "zzz")
		if _, armed := res.Vars[modules.ExitMenuVar]; armed {
			t.Fatalf("el inválido %d de la segunda racha armó el menú", i)
		}
		vars = res.Vars
	}
}

// El contador es del evento en que se contó: sellado con otro, arranca de cero.
func TestStep_CounterSealedWithAnotherEventStartsOver(t *testing.T) {
	m := cart.New()
	vars := turnIn(m, seededVars(), "ev-A", "zzz").Vars
	vars = turnIn(m, vars, "ev-A", "zzz").Vars

	res := turnIn(m, vars, "ev-B", "zzz")
	mustScreen(t, res.Outputs, invalidPrefix+categoriesScreen)
	if _, armed := res.Vars[modules.ExitMenuVar]; armed {
		t.Fatal("el primer inválido del evento B armó el menú con los dos del evento A")
	}
	if st := stateOf(t, res.Vars); st.Reprompts != 1 || st.RepromptsEvent != "ev-B" {
		t.Errorf("estado = %+v, quiero UN inválido sellado con ev-B", st)
	}

	// Y al salir del evento (EventID vacío) el contador sellado también muere.
	res = turnIn(m, vars, "", "zzz")
	if st := stateOf(t, res.Vars); st.Reprompts != 0 || st.RepromptsEvent != "" {
		t.Errorf("fuera de evento: estado = %+v, quiero el contador a cero y sin sello", st)
	}
}

// Se porta tal cual: una cantidad inválida repregunta por otro camino —no es un
// inválido de opción—, así que NO cuenta y además reinicia el contador.
func TestStep_InEvent_InvalidQuantityDoesNotCountAndResets(t *testing.T) {
	m := cart.New()
	vars := turnIn(m, seededVars(), "ev-1", "1").Vars // Bebidas
	vars = turnIn(m, vars, "ev-1", "zzz").Vars        // inválido 1 en artículos
	vars = turnIn(m, vars, "ev-1", "zzz").Vars        // inválido 2
	vars = turnIn(m, vars, "ev-1", "1").Vars          // Café: válido, reinicia
	vars = turnIn(m, vars, "ev-1", "zzz").Vars        // inválido 1 en la ficha
	if st := stateOf(t, vars); st.Reprompts != 1 {
		t.Fatalf("precondición: estado = %+v, quiero un inválido contado", st)
	}
	vars = turnIn(m, vars, "ev-1", "2").Vars // agregar ⇒ cantidad (válido)

	for i := 1; i <= 4; i++ {
		res := turnIn(m, vars, "ev-1", "-3")
		mustContain(t, res.Outputs, "Escribe una cantidad válida (un número mayor o igual a 1).")
		if _, armed := res.Vars[modules.ExitMenuVar]; armed {
			t.Fatalf("la cantidad inválida %d armó el menú de salida", i)
		}
		if st := stateOf(t, res.Vars); st.Level != cart.LevelQuantity || st.Reprompts != 0 {
			t.Fatalf("cantidad inválida %d: estado = %+v, quiero quantity con el contador a cero", i, st)
		}
		vars = res.Vars
	}
}
