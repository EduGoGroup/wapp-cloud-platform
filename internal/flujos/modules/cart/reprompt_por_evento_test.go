package cart

// reprompt_por_evento_test.go fija, para el carrito, la misma propiedad que
// modules/reprompt_por_evento_test.go fija para menú y encuesta (Plan 043 · Ola 5): los
// inválidos pertenecen al EVENTO en que se cometieron.
//
// El carrito necesita su propio candado porque su contador NO vive suelto en Vars como
// el de los nodos numerados, sino dentro de cartState (cartState.Reprompts), que viaja
// serializado en Vars — y por tanto sobrevive intacto a los caminos del runtime que
// cambian de evento conservando Vars. Su sello es cartState.RepromptsEvent.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// TestCart_ContadorDeOtroEvento_EsElPrimerInvalidoDeEste: dos inválidos dentro del
// evento A, cambio de contexto al evento B conservando Vars (lo que hace saveMenuState),
// y UN inválido en B. Antes del sello, ese único inválido armaba el menú de salida.
func TestCart_ContadorDeOtroEvento_EsElPrimerInvalidoDeEste(t *testing.T) {
	t.Parallel()
	m := New()
	conv := model.Conversation{Vars: seededVars(), EventID: "ev-cart-1"}

	for i := 1; i <= modules.MaxReprompts-1; i++ {
		res := turno(m, conv, "zzz", nil)
		conv.Vars = res.Vars
		if _, armado := res.Vars[modules.ExitMenuVar]; armado {
			t.Fatalf("precondición: el inválido %d NO debe armar el menú de salida", i)
		}
	}
	st := loadState(conv.Vars)
	if st.Reprompts != modules.MaxReprompts-1 {
		t.Fatalf("precondición: Reprompts dentro del evento A = %d, quiero %d", st.Reprompts, modules.MaxReprompts-1)
	}
	if st.RepromptsEvent != "ev-cart-1" {
		t.Fatalf("precondición: el contador debe quedar SELLADO con el evento en que se contó; RepromptsEvent=%q",
			st.RepromptsEvent)
	}

	// CAMBIO DE CONTEXTO: las mismas Vars, otro evento activo.
	conv.EventID = "ev-menu-2"
	res := turno(m, conv, "zzz", nil)

	if _, armado := res.Vars[modules.ExitMenuVar]; armado {
		t.Fatalf("un contador heredado del evento anterior armó el menú de salida al PRIMER inválido "+
			"del evento nuevo; Outputs=%v", res.Outputs)
	}
	if len(res.Outputs) != 1 || !strings.HasPrefix(res.Outputs[0], t52CartInvalidPrefix) {
		t.Fatalf("Outputs = %v, quiero el reprompt clásico (es el primer fallo de este evento)", res.Outputs)
	}
	nueva := loadState(res.Vars)
	if nueva.Reprompts != 1 {
		t.Fatalf("Reprompts en el evento nuevo = %d, quiero 1 (arranca de cero)", nueva.Reprompts)
	}
	if nueva.RepromptsEvent != "ev-menu-2" {
		t.Fatalf("RepromptsEvent = %q, quiero el evento nuevo (%q)", nueva.RepromptsEvent, "ev-menu-2")
	}
}

// TestCart_ValidoSueltaElSello: una entrada válida reinicia el contador, y el sello se
// va con él (no se queda huérfano en el JSONB).
func TestCart_ValidoSueltaElSello(t *testing.T) {
	t.Parallel()
	m := New()
	conv := model.Conversation{Vars: seededVars(), EventID: "ev-cart-1"}

	res := turno(m, conv, "zzz", nil)
	if st := loadState(res.Vars); st.RepromptsEvent == "" {
		t.Fatalf("precondición: el inválido debe dejar el contador sellado; estado=%+v", st)
	}
	conv.Vars = res.Vars
	// "1" es la primera categoría del catálogo sembrado: entrada VÁLIDA en L1.
	valido := turno(m, conv, "1", nil)
	st := loadState(valido.Vars)
	if st.Reprompts != 0 || st.RepromptsEvent != "" {
		t.Fatalf("tras una entrada válida quiero Reprompts=0 y sin sello; tengo %d / %q",
			st.Reprompts, st.RepromptsEvent)
	}
}
