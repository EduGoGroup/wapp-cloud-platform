package modules_test

// reprompt_por_evento_test.go fija el SELLO del contador de reprompt (Plan 043 · Ola 5):
// los intentos inválidos pertenecen al EVENTO en que se cometieron, y un contador
// cargado en otro evento vale 0.
//
// Es el candado de unidad del defecto que la Ola 5 cazó por ejecución: T5.2 selló el
// marcador del menú de salida con su evento pero dejó el CONTADOR sin sello, así que
// cruzaba los caminos del runtime que cambian de evento CONSERVANDO Vars y armaba el
// menú de salida al primer inválido del evento siguiente. El test de recorrido completo
// está en runtime/reprompt_por_evento_test.go; aquí se fija la pieza.
//
// REGRESIÓN CERO: fuera de todo evento (EventID "") el contador se comporta exactamente
// como antes —el sello ausente casa con ""— y no aparece ni una clave nueva en Vars.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

const claveContador = "menu_reprompt"

// --- modules.RepromptCount / SetRepromptCount / ClearRepromptCount ------------

func TestRepromptCount_SinSelloSoloValeFueraDeEvento(t *testing.T) {
	vars := map[string]any{claveContador: 2}
	if got := modules.RepromptCount(vars, "", claveContador); got != 2 {
		t.Fatalf("fuera de evento, un contador sin sello = %d, quiero 2 (regresión cero)", got)
	}
	if got := modules.RepromptCount(vars, "ev-1", claveContador); got != 0 {
		t.Fatalf("dentro de un evento, un contador SIN sello = %d, quiero 0 "+
			"(es un contador escrito antes de esta corrección: degradar a 0 es el lado seguro)", got)
	}
}

func TestRepromptCount_SoloCuentaParaSuEvento(t *testing.T) {
	vars := map[string]any{}
	modules.SetRepromptCount(vars, "ev-1", claveContador, 2)

	if got := modules.RepromptCount(vars, "ev-1", claveContador); got != 2 {
		t.Fatalf("en el evento en que se contó, RepromptCount = %d, quiero 2", got)
	}
	if got := modules.RepromptCount(vars, "ev-2", claveContador); got != 0 {
		t.Fatalf("en OTRO evento, RepromptCount = %d, quiero 0 "+
			"(el contador es del evento en que se falló, no de la conversación)", got)
	}
	if got := modules.RepromptCount(vars, "", claveContador); got != 0 {
		t.Fatalf("fuera de todo evento, un contador sellado con uno = %d, quiero 0", got)
	}
}

func TestSetRepromptCount_FueraDeEventoNoEscribeSello(t *testing.T) {
	vars := map[string]any{}
	modules.SetRepromptCount(vars, "", claveContador, 1)
	if _, hay := vars[modules.RepromptEventKey(claveContador)]; hay {
		t.Fatalf("fuera de un evento el sello NO debe escribirse (el JSONB de flow_state.vars "+
			"queda byte a byte como antes); Vars=%v", vars)
	}
	// Y si venía sellado de un evento anterior, se suelta al contar fuera de él.
	modules.SetRepromptCount(vars, "ev-1", claveContador, 1)
	modules.SetRepromptCount(vars, "", claveContador, 1)
	if _, hay := vars[modules.RepromptEventKey(claveContador)]; hay {
		t.Fatalf("el sello viejo debe soltarse al contar fuera de un evento; Vars=%v", vars)
	}
}

func TestClearRepromptCount_BorraContadorYSello(t *testing.T) {
	vars := map[string]any{}
	modules.SetRepromptCount(vars, "ev-1", claveContador, 2)
	modules.ClearRepromptCount(vars, claveContador)
	if len(vars) != 0 {
		t.Fatalf("Clear debe borrar el contador Y su sello (o el sello queda huérfano en el JSONB); Vars=%v", vars)
	}
}

// --- el enganche en NumberedStep ---------------------------------------------

// TestNumberedStep_ContadorDeOtroEvento_EsElPrimerInvalidoDeEste es la propiedad que el
// defecto violaba: con el contador en MaxReprompts-1 pero sellado con OTRO evento, esta
// entrada inválida es la PRIMERA de este evento — repromptea, no arma el menú de salida.
func TestNumberedStep_ContadorDeOtroEvento_EsElPrimerInvalidoDeEste(t *testing.T) {
	vars := map[string]any{}
	modules.SetRepromptCount(vars, "ev-1", claveContador, modules.MaxReprompts-1)
	conv := model.Conversation{Vars: vars, EventID: "ev-2"}

	res := modules.NumberedStep(t52Node(), conv, "zzz", claveContador, modules.MaxReprompts,
		func(v map[string]any, _, _ string) modules.Result { return modules.Result{Vars: v} })

	if _, armado := res.Vars[modules.ExitMenuVar]; armado {
		t.Fatalf("un contador heredado del evento %q no puede armar el menú de salida en el %q "+
			"al PRIMER inválido; Outputs=%v", "ev-1", conv.EventID, res.Outputs)
	}
	if got := modules.RepromptCount(res.Vars, conv.EventID, claveContador); got != 1 {
		t.Fatalf("el contador en el evento nuevo = %d, quiero 1 (arranca de cero y este es su primer fallo)", got)
	}
}

// TestNumberedStep_MismoEvento_LlegaAlMenuDeSalida es el contraste: sin cambio de
// contexto, el sello casa y la escalera funciona como fijó T5.2.
func TestNumberedStep_MismoEvento_LlegaAlMenuDeSalida(t *testing.T) {
	vars := map[string]any{}
	modules.SetRepromptCount(vars, "ev-1", claveContador, modules.MaxReprompts-1)
	conv := model.Conversation{Vars: vars, EventID: "ev-1"}

	res := modules.NumberedStep(t52Node(), conv, "zzz", claveContador, modules.MaxReprompts,
		func(v map[string]any, _, _ string) modules.Result { return modules.Result{Vars: v} })

	if _, armado := res.Vars[modules.ExitMenuVar]; !armado {
		t.Fatalf("dentro del MISMO evento el %d.º inválido SÍ debe armar el menú de salida; Outputs=%v",
			modules.MaxReprompts, res.Outputs)
	}
	if _, queda := res.Vars[claveContador]; queda {
		t.Fatalf("al armar, el contador se reinicia y su sello se suelta; Vars=%v", res.Vars)
	}
}
