package modules_test

// exit_menu_test.go fija el MENÚ DE SALIDA del reprompt acotado (Plan 043 · T5.2,
// D-043.10): InEvent/ExitMenuText/ArmExitMenu/DisarmExitMenu de modules.numbered.go
// y su enganche en NumberedStep (de la que menu.Module y survey.Module delegan sin
// tocar ni una línea, `menu.go:59` y `survey.go:71`).
//
// REGRESIÓN CERO (la propiedad más importante de esta tarea, D-043.10): FUERA de un
// evento, el 3.er inválido sigue emitiendo numberedHelpText byte a byte, exactamente
// como antes de esta ola. Se fija explícitamente para menu Y survey (no solo para
// NumberedStep en abstracto) llamando a menu.New() y survey.New() directamente.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
)

const (
	t52HelpTextClasico = "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n"
)

func t52Node() model.Node {
	return model.Node{Prompt: "Elige:\n1) A\n2) B", Options: map[string]string{"1": "a", "2": "b"}}
}

// --- modules.InEvent ---------------------------------------------------------

func TestInEvent(t *testing.T) {
	t.Parallel()
	if modules.InEvent(model.Conversation{}) {
		t.Fatal("sin EventID no hay evento activo")
	}
	if !modules.InEvent(model.Conversation{EventID: "ev-1"}) {
		t.Fatal("con EventID poblado hay evento activo")
	}
}

// --- modules.ExitMenuText -----------------------------------------------------

// TestExitMenuText_OrdenDeOpciones fija el orden EXACTO que D-043.10 exige: el «2»
// (dejarlo por ahora) va ENTRE las dos que no abandonan, y la pantalla original va
// al final para que el cliente la tenga a la vista si elige «1».
func TestExitMenuText_OrdenDeOpciones(t *testing.T) {
	t.Parallel()
	got := modules.ExitMenuText("PANTALLA-ORIGINAL")
	want := "Parece que no nos estamos entendiendo. ¿Qué prefieres? Responde con el número:\n" +
		"1) Seguir intentando\n" +
		"2) Dejar esto por ahora\n" +
		"3) Ver el menú\n\n" + "PANTALLA-ORIGINAL"
	if got != want {
		t.Fatalf("ExitMenuText =\n%q\nquiero\n%q", got, want)
	}
}

// --- modules.ArmExitMenu / DisarmExitMenu -------------------------------------

func TestArmExitMenu_DejaLaMarcaYElOutput(t *testing.T) {
	t.Parallel()
	vars := map[string]any{"otro": "dato"}
	res := modules.ArmExitMenu(vars, model.Conversation{EventID: "ev-cart-1"}, "PANTALLA")
	if res.Next != nil {
		t.Fatal("ArmExitMenu no debe transicionar (Next nil): el menú de salida permanece en el nodo")
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != modules.ExitMenuText("PANTALLA") {
		t.Fatalf("Outputs = %v, quiero [ExitMenuText(PANTALLA)]", res.Outputs)
	}
	if res.Vars[modules.ExitMenuVar] != "PANTALLA" {
		t.Fatalf("Vars[%q] = %v, quiero PANTALLA", modules.ExitMenuVar, res.Vars[modules.ExitMenuVar])
	}
	// El marcador va SELLADO con el evento sobre el que se armó: sin el sello, un
	// camino del runtime que conserva las Vars al cambiar de evento (saveMenuState)
	// dejaba el menú vivo en otro contexto y el «2» siguiente desactivaba el evento
	// equivocado. Ver ExitMenuEventVar.
	if got := modules.ExitMenuArmedOn(res.Vars); got != "ev-cart-1" {
		t.Fatalf("ExitMenuArmedOn = %q, quiero el evento sobre el que se armó", got)
	}
	if res.Vars["otro"] != "dato" {
		t.Fatal("ArmExitMenu no debe perder otras claves de vars")
	}
}

func TestExitMenuArmedOn_SinSelloEsVacio(t *testing.T) {
	t.Parallel()
	// Marcador VIEJO (escrito antes del sello) que siguiera vivo en un flow_state:
	// no casa con ningún EventID real ⇒ el runtime lo desarma en vez de secuestrar
	// el turno.
	if got := modules.ExitMenuArmedOn(map[string]any{modules.ExitMenuVar: "PANTALLA"}); got != "" {
		t.Fatalf("ExitMenuArmedOn de un marcador sin sello = %q, quiero \"\"", got)
	}
}

func TestDisarmExitMenu_DevuelveYBorra(t *testing.T) {
	t.Parallel()
	vars := map[string]any{
		modules.ExitMenuVar:      "PANTALLA",
		modules.ExitMenuEventVar: "ev-cart-1",
		"otro":                   "dato",
	}
	screen := modules.DisarmExitMenu(vars)
	if screen != "PANTALLA" {
		t.Fatalf("screen = %q, quiero PANTALLA", screen)
	}
	if _, ok := vars[modules.ExitMenuVar]; ok {
		t.Fatal("DisarmExitMenu debe borrar la marca")
	}
	if _, ok := vars[modules.ExitMenuEventVar]; ok {
		t.Fatal("DisarmExitMenu debe borrar TAMBIÉN el sello del evento: si sobrevive, el marcador siguiente hereda un evento que no es el suyo")
	}
	if vars["otro"] != "dato" {
		t.Fatal("DisarmExitMenu no debe tocar otras claves")
	}
}

func TestDisarmExitMenu_SinMarcaDevuelveVacio(t *testing.T) {
	t.Parallel()
	if got := modules.DisarmExitMenu(map[string]any{}); got != "" {
		t.Fatalf("sin marca armada, screen = %q, quiero \"\"", got)
	}
}

// --- NumberedStep: arma el menú DENTRO de un evento, al 3.er inválido --------

// TestNumberedStep_DentroDeEvento_TercerInvalidoArmaMenu (D-043.10): dos inválidos
// reprompt normal; el TERCERO arma el menú de salida (Vars[ExitMenuVar] = prompt del
// nodo) en vez del mensaje de ayuda de siempre, y reinicia el contador.
func TestNumberedStep_DentroDeEvento_TercerInvalidoArmaMenu(t *testing.T) {
	t.Parallel()
	const key = "rk"
	conv := model.Conversation{EventID: "ev-cart-1"}
	fail := func(vars map[string]any, choice, target string) modules.Result {
		t.Fatal("onValid no debe llamarse con entradas inválidas")
		return modules.Result{}
	}

	res := modules.NumberedStep(t52Node(), conv, "x", key, 3, fail)
	res = modules.NumberedStep(t52Node(), mergeVars(conv, res.Vars), "x", key, 3, fail)
	res = modules.NumberedStep(t52Node(), mergeVars(conv, res.Vars), "x", key, 3, fail)

	if len(res.Outputs) != 1 || res.Outputs[0] != modules.ExitMenuText(t52Node().Prompt) {
		t.Fatalf("al 3er inválido dentro de un evento, Outputs = %v, quiero el menú de salida", res.Outputs)
	}
	if res.Vars[modules.ExitMenuVar] != t52Node().Prompt {
		t.Fatalf("Vars[%q] = %v, quiero el prompt del nodo armado", modules.ExitMenuVar, res.Vars[modules.ExitMenuVar])
	}
	if got := modules.ExitMenuArmedOn(res.Vars); got != conv.EventID {
		t.Fatalf("el menú debe quedar SELLADO con el evento activo: ExitMenuArmedOn=%q, quiero %q", got, conv.EventID)
	}
	if _, ok := res.Vars[key]; ok {
		t.Fatal("el contador de reprompt debe reiniciarse al armar el menú de salida")
	}
}

// TestNumberedStep_FueraDeEvento_TercerInvalidoAyudaClasica es la REGRESIÓN CERO
// (D-043.10, propiedad más importante): sin EventID, el 3.er inválido se comporta
// BYTE A BYTE como antes de esta ola — el mensaje de ayuda de siempre, sin marca de
// menú de salida.
func TestNumberedStep_FueraDeEvento_TercerInvalidoAyudaClasica(t *testing.T) {
	t.Parallel()
	const key = "rk"
	conv := model.Conversation{} // SIN EventID.
	fail := func(vars map[string]any, choice, target string) modules.Result {
		t.Fatal("onValid no debe llamarse con entradas inválidas")
		return modules.Result{}
	}

	res := modules.NumberedStep(t52Node(), conv, "x", key, 3, fail)
	res = modules.NumberedStep(t52Node(), mergeVars(conv, res.Vars), "x", key, 3, fail)
	res = modules.NumberedStep(t52Node(), mergeVars(conv, res.Vars), "x", key, 3, fail)

	want := t52HelpTextClasico + t52Node().Prompt
	if len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Fatalf("fuera de evento, Outputs = %v, quiero [%q] (byte a byte, sin cambios)", res.Outputs, want)
	}
	if _, armado := res.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("fuera de evento el menú de salida NUNCA debe armarse (regresión cero)")
	}
}

func mergeVars(conv model.Conversation, vars map[string]any) model.Conversation {
	conv.Vars = vars
	return conv
}

// --- menu.Module y survey.Module: la misma propiedad, a nivel de MÓDULO --------
// (D-043.10 exige fijar la regresión cero explícitamente para menu Y survey, no
// solo para NumberedStep en abstracto; menu.go:59 y survey.go:71 delegan sin
// tocar ni una línea, así que este es el mismo camino de producción.)

func t52MenuNode() model.Node {
	return model.Node{Prompt: "Hola\n1) Ventas\n2) Soporte", Options: map[string]string{"1": "ventas", "2": "soporte"}}
}

func TestMenuModule_DentroDeEvento_TercerInvalidoArmaMenuDeSalida(t *testing.T) {
	t.Parallel()
	mod := menu.New()
	node := t52MenuNode()
	conv := model.Conversation{EventID: "ev-menu-1"}

	res := mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")

	if res.Vars[modules.ExitMenuVar] != node.Prompt {
		t.Fatalf("menu.Module dentro de un evento debe armar el menú de salida al 3er inválido; Vars[%q]=%v",
			modules.ExitMenuVar, res.Vars[modules.ExitMenuVar])
	}
}

func TestMenuModule_FueraDeEvento_TercerInvalidoAyudaClasicaSinMenu(t *testing.T) {
	t.Parallel()
	mod := menu.New()
	node := t52MenuNode()
	conv := model.Conversation{} // SIN EventID.

	res := mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")

	want := t52HelpTextClasico + node.Prompt
	if len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Fatalf("menu.Module fuera de evento: Outputs = %v, quiero [%q] (regresión cero)", res.Outputs, want)
	}
	if _, armado := res.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("menu.Module fuera de evento NUNCA debe armar el menú de salida")
	}
}

func t52SurveyNode() model.Node {
	return model.Node{
		Prompt: "¿Nos recomendarías?\n1) Sí\n2) No", QuestionID: "q1",
		Options: map[string]string{"1": "gracias", "2": "gracias"},
	}
}

func TestSurveyModule_DentroDeEvento_TercerInvalidoArmaMenuDeSalida(t *testing.T) {
	t.Parallel()
	mod := survey.New()
	node := t52SurveyNode()
	conv := model.Conversation{EventID: "ev-survey-1"}

	res := mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")

	if res.Vars[modules.ExitMenuVar] != node.Prompt {
		t.Fatalf("survey.Module dentro de un evento debe armar el menú de salida al 3er inválido; Vars[%q]=%v",
			modules.ExitMenuVar, res.Vars[modules.ExitMenuVar])
	}
}

func TestSurveyModule_FueraDeEvento_TercerInvalidoAyudaClasicaSinMenu(t *testing.T) {
	t.Parallel()
	mod := survey.New()
	node := t52SurveyNode()
	conv := model.Conversation{} // SIN EventID.

	res := mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")
	conv.Vars = res.Vars
	res = mod.Step(node, conv, "x")

	want := t52HelpTextClasico + node.Prompt
	if len(res.Outputs) != 1 || res.Outputs[0] != want {
		t.Fatalf("survey.Module fuera de evento: Outputs = %v, quiero [%q] (regresión cero)", res.Outputs, want)
	}
	if _, armado := res.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("survey.Module fuera de evento NUNCA debe armar el menú de salida")
	}
	if _, tieneRespuestas := res.Vars["answers"]; tieneRespuestas {
		t.Fatal("una entrada inválida no debe registrar respuesta alguna")
	}
}
