package cart

// cart_exit_menu_test.go fija el PRIMER reprompt acotado del carrito (Plan 043 ·
// T5.2, D-043.10, §5.2 del CONTRATO-OLA5: «el carrito nunca tuvo contador» — se le
// da uno por primera vez, condicionado a estar DENTRO de un evento).
//
// REGRESIÓN CERO (la propiedad más importante de esta tarea): FUERA de un evento
// el carrito repromptea SIN TECHO, exactamente como toda la vida
// («Reprompt del MISMO paso: se rechaza y se repregunta, jamás se trunca»,
// comentario original de reprompt() antes de esta ola). Se fija con más de
// MaxReprompts entradas basura seguidas y CERO apariciones del menú de salida.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

const t52CartInvalidPrefix = "Opción no válida. Responde con el número de una de las opciones.\n\n"

// TestCart_DentroDeEvento_TercerInvalidoArmaMenuDeSalida: dos entradas basura en
// L1 (categorías) repromptean como siempre; la TERCERA arma el menú de salida
// (Vars[modules.ExitMenuVar]) con la MISMA pantalla que el reprompt clásico venía
// mostrando, y Outputs pasa a ser el menú de salida.
func TestCart_DentroDeEvento_TercerInvalidoArmaMenuDeSalida(t *testing.T) {
	t.Parallel()
	m := New()
	vars := seededVars()
	conv := model.Conversation{Vars: vars, EventID: "ev-cart-1"}

	res1 := m.Step(model.Node{}, conv, "zzz")
	if len(res1.Outputs) != 1 || !strings.HasPrefix(res1.Outputs[0], t52CartInvalidPrefix) {
		t.Fatalf("1er inválido: Outputs = %v, quiero el reprompt clásico", res1.Outputs)
	}
	pantalla := strings.TrimPrefix(res1.Outputs[0], t52CartInvalidPrefix)
	if _, armado := res1.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("el 1er inválido NO debe armar el menú de salida")
	}

	conv.Vars = res1.Vars
	res2 := m.Step(model.Node{}, conv, "zzz")
	if len(res2.Outputs) != 1 || res2.Outputs[0] != res1.Outputs[0] {
		t.Fatalf("2do inválido: Outputs = %v, quiero el MISMO reprompt clásico (%q)", res2.Outputs, res1.Outputs[0])
	}
	if _, armado := res2.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("el 2do inválido NO debe armar el menú de salida")
	}

	conv.Vars = res2.Vars
	res3 := m.Step(model.Node{}, conv, "zzz")
	if res3.Outputs == nil || len(res3.Outputs) != 1 {
		t.Fatalf("3er inválido: Outputs = %v, quiero el menú de salida (1 salida)", res3.Outputs)
	}
	if res3.Outputs[0] != modules.ExitMenuText(pantalla) {
		t.Fatalf("3er inválido dentro de un evento: Outputs[0] =\n%q\nquiero\n%q", res3.Outputs[0], modules.ExitMenuText(pantalla))
	}
	if res3.Vars[modules.ExitMenuVar] != pantalla {
		t.Fatalf("Vars[%q] = %v, quiero la pantalla del nivel (%q)", modules.ExitMenuVar, res3.Vars[modules.ExitMenuVar], pantalla)
	}
	// El marcador va SELLADO con el evento: es lo que impide que sobreviva a un
	// cambio de contexto (saveMenuState conserva las Vars) y secuestre un número
	// que ya no era del carrito. Ver modules.ExitMenuEventVar.
	if got := modules.ExitMenuArmedOn(res3.Vars); got != conv.EventID {
		t.Fatalf("ExitMenuArmedOn = %q, quiero el evento activo (%q)", got, conv.EventID)
	}
	// El contador transitorio se reinicia al armar (mismo trato que numbered.go):
	// round-trip por JSON, así que el campo cero (omitempty) ni siquiera aparece.
	if st := loadState(res3.Vars); st.Reprompts != 0 {
		t.Fatalf("Reprompts tras armar = %d, quiero 0", st.Reprompts)
	}
}

// TestCart_FueraDeEvento_NuncaArmaYRepromteaSinTecho es la REGRESIÓN CERO: CINCO
// entradas basura seguidas (más de modules.MaxReprompts) fuera de un evento y
// NINGUNA arma el menú de salida — el carrito sigue repreguntando indefinidamente,
// byte a byte igual que antes de esta ola.
func TestCart_FueraDeEvento_NuncaArmaYRepromteaSinTecho(t *testing.T) {
	t.Parallel()
	m := New()
	conv := model.Conversation{Vars: seededVars()} // SIN EventID.

	var primero string
	for i := 0; i < 5; i++ {
		res := m.Step(model.Node{}, conv, "zzz")
		if len(res.Outputs) != 1 || !strings.HasPrefix(res.Outputs[0], t52CartInvalidPrefix) {
			t.Fatalf("intento %d fuera de evento: Outputs = %v, quiero el reprompt clásico", i+1, res.Outputs)
		}
		if i == 0 {
			primero = res.Outputs[0]
		} else if res.Outputs[0] != primero {
			t.Fatalf("intento %d: el reprompt cambió de texto (%q), quiero el MISMO de siempre", i+1, res.Outputs[0])
		}
		if _, armado := res.Vars[modules.ExitMenuVar]; armado {
			t.Fatalf("intento %d fuera de evento: el menú de salida NUNCA debe armarse (regresión cero)", i+1)
		}
		conv.Vars = res.Vars
	}
}

// TestCart_ReprompsCounterSoloCuentaDentroDeEvento es el candado a nivel de
// ESTADO (no solo de Outputs): fuera de un evento, cartState.Reprompts se queda
// SIEMPRE en 0 tras un Step con entrada inválida —confirma que reprompt() nunca
// toca el contador cuando st.inEvent es false, que es la guarda entera de D-043.10
// aplicada al carrito.
func TestCart_ReprompsCounterSoloCuentaDentroDeEvento(t *testing.T) {
	t.Parallel()
	m := New()
	conv := model.Conversation{Vars: seededVars()} // SIN EventID.

	res := m.Step(model.Node{}, conv, "zzz")
	if st := loadState(res.Vars); st.Reprompts != 0 {
		t.Fatalf("Reprompts fuera de evento tras un inválido = %d, quiero 0 (no cuenta)", st.Reprompts)
	}
}

// TestCart_ValidoReiniciaElContador confirma que una entrada VÁLIDA reinicia el
// contador dentro de un evento (misma norma que numbered.go), para que dos rachas
// de dos inválidos separadas por una elección correcta no se sumen entre sí.
func TestCart_ValidoReiniciaElContador(t *testing.T) {
	t.Parallel()
	m := New()
	vars := seededVars()
	conv := model.Conversation{Vars: vars, EventID: "ev-cart-2"}

	res := m.Step(model.Node{}, conv, "zzz") // 1er inválido: Reprompts=1
	if st := loadState(res.Vars); st.Reprompts != 1 {
		t.Fatalf("Reprompts tras 1 inválido = %d, quiero 1", st.Reprompts)
	}
	conv.Vars = res.Vars
	res = m.Step(model.Node{}, conv, "1") // elección VÁLIDA (categoría "1" = Bebidas)
	if st := loadState(res.Vars); st.Reprompts != 0 {
		t.Fatalf("Reprompts tras una elección válida = %d, quiero 0 (se reinicia)", st.Reprompts)
	}
}
