//go:build pendiente

package modules_test

import (
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

const (
	counterKey = "menu_reprompt"
	sealKey    = "menu_reprompt_event_id"

	// Los tres textos del reprompt acotado, byte a byte.
	invalidOptionText = "Opción no válida. Responde con el número de una de las opciones.\n\n"
	helpText          = "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n"
	exitMenuHeader    = "Parece que no nos estamos entendiendo. ¿Qué prefieres? Responde con el número:\n" +
		"1) Seguir intentando\n" +
		"2) Dejar esto por ahora\n" +
		"3) Ver el menú\n\n"
)

func numberedNode() model.Node {
	return model.Node{Prompt: "Elige:\n1) A\n2) B", Options: map[string]string{"1": "a", "2": "b"}}
}

// failOnValid es el onValid de los casos inválidos: que se llame ya es el fallo.
func failOnValid(t *testing.T) func(map[string]any, string, string) modules.Result {
	return func(map[string]any, string, string) modules.Result {
		t.Helper()
		t.Error("onValid se llamó con una entrada inválida")
		return modules.Result{}
	}
}

// Las claves de Vars y los códigos del menú de salida acaban en el JSONB de
// flow_state y los lee el runtime: sus valores no cambian.
func TestNumbered_Literals(t *testing.T) {
	literals := map[string]string{
		modules.ExitMenuVar:        "event_exit_menu",
		modules.ExitMenuEventVar:   "event_exit_menu_event_id",
		modules.ExitMenuKeepTrying: "1",
		modules.ExitMenuStop:       "2",
		modules.ExitMenuDispatcher: "3",
	}
	for got, want := range literals {
		if got != want {
			t.Errorf("literal = %q, quiero %q", got, want)
		}
	}
	if modules.MaxReprompts != 3 {
		t.Errorf("MaxReprompts = %d, quiero 3", modules.MaxReprompts)
	}
}

func TestCloneVars(t *testing.T) {
	in := map[string]any{"a": 1}
	out := modules.CloneVars(in)
	out["b"] = 2
	if _, mutated := in["b"]; mutated || out["a"] != 1 {
		t.Errorf("in = %v, out = %v; quiero una copia independiente con las mismas claves", in, out)
	}

	empty := modules.CloneVars(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("CloneVars(nil) = %#v, quiero un mapa vacío no nil", empty)
	}
	empty["x"] = 1 // escribible: un mapa nil reventaría aquí
}

func TestGetInt(t *testing.T) {
	vars := map[string]any{"int": 3, "int64": int64(4), "float64": float64(5), "string": "6", "float32": float32(7)}
	want := map[string]int{"int": 3, "int64": 4, "float64": 5, "string": 0, "float32": 0, "absent": 0}
	for key, n := range want {
		if got := modules.GetInt(vars, key); got != n {
			t.Errorf("GetInt(%s) = %d, quiero %d", key, got, n)
		}
	}
	if got := modules.GetInt(nil, "x"); got != 0 {
		t.Errorf("GetInt sobre nil = %d, quiero 0", got)
	}
}

func TestRepromptEventKey(t *testing.T) {
	if got := modules.RepromptEventKey(counterKey); got != sealKey {
		t.Errorf("RepromptEventKey = %q, quiero %q", got, sealKey)
	}
}

func TestRepromptCount(t *testing.T) {
	cases := map[string]struct {
		vars    map[string]any
		eventID string
		want    int
	}{
		"unsealed counter counts outside any event":   {map[string]any{counterKey: 2}, "", 2},
		"unsealed counter is zero inside an event":    {map[string]any{counterKey: 2}, "ev-1", 0},
		"sealed counter counts in its event":          {map[string]any{counterKey: 2, sealKey: "ev-1"}, "ev-1", 2},
		"sealed counter is zero in another event":     {map[string]any{counterKey: 2, sealKey: "ev-1"}, "ev-2", 0},
		"sealed counter is zero outside any event":    {map[string]any{counterKey: 2, sealKey: "ev-1"}, "", 0},
		"unreadable seal is zero inside an event":     {map[string]any{counterKey: 2, sealKey: 7}, "ev-1", 0},
		"unreadable seal reads as absent outside":     {map[string]any{counterKey: 2, sealKey: 7}, "", 2},
		"json round trip counter":                     {map[string]any{counterKey: float64(2), sealKey: "ev-1"}, "ev-1", 2},
		"no counter":                                  {map[string]any{}, "ev-1", 0},
		"seal of the event without counter":           {map[string]any{sealKey: "ev-1"}, "ev-1", 0},
		"counter of another module is not this one's": {map[string]any{"survey_reprompt": 2}, "", 0},
		"nil vars": {nil, "", 0},
		"seal of another counter does not seal this 1": {map[string]any{counterKey: 2, "survey_reprompt_event_id": "ev-1"}, "", 2},
	}
	for name, tc := range cases {
		if got := modules.RepromptCount(tc.vars, tc.eventID, counterKey); got != tc.want {
			t.Errorf("%s: RepromptCount = %d, quiero %d", name, got, tc.want)
		}
	}
}

func TestSetRepromptCount(t *testing.T) {
	vars := map[string]any{"otro": "dato"}

	modules.SetRepromptCount(vars, "ev-1", counterKey, 2)
	if vars[counterKey] != 2 || vars[sealKey] != "ev-1" {
		t.Fatalf("vars = %v, quiero el contador en 2 sellado con ev-1", vars)
	}

	// Fuera de un evento el sello no se escribe, y el que hubiera se suelta.
	modules.SetRepromptCount(vars, "", counterKey, 1)
	if _, sealed := vars[sealKey]; sealed || vars[counterKey] != 1 {
		t.Errorf("vars = %v, quiero el contador en 1 y sin sello", vars)
	}
	if vars["otro"] != "dato" || len(vars) != 2 {
		t.Errorf("vars = %v, quiero solo el contador y la clave ajena", vars)
	}
}

func TestClearRepromptCount(t *testing.T) {
	vars := map[string]any{"otro": "dato", "survey_reprompt": 1}
	modules.SetRepromptCount(vars, "ev-1", counterKey, 2)

	modules.ClearRepromptCount(vars, counterKey)
	if !maps.Equal(vars, map[string]any{"otro": "dato", "survey_reprompt": 1}) {
		t.Errorf("vars = %v, quiero sin contador ni sello y lo ajeno intacto", vars)
	}
	modules.ClearRepromptCount(vars, counterKey) // sin nada que borrar no hace nada
	if len(vars) != 2 {
		t.Errorf("vars = %v tras un segundo Clear", vars)
	}
}

func TestInEvent(t *testing.T) {
	if modules.InEvent(model.Conversation{}) {
		t.Error("sin EventID hay evento activo")
	}
	if !modules.InEvent(model.Conversation{EventID: "ev-1"}) {
		t.Error("con EventID no hay evento activo")
	}
}

// El «2» (dejarlo) va entre las dos opciones que no abandonan, y la pantalla al final.
func TestExitMenuText(t *testing.T) {
	if got, want := modules.ExitMenuText("PANTALLA"), exitMenuHeader+"PANTALLA"; got != want {
		t.Errorf("ExitMenuText =\n%q\nquiero\n%q", got, want)
	}
	if got := modules.ExitMenuText(""); got != exitMenuHeader {
		t.Errorf("ExitMenuText sin pantalla = %q", got)
	}
}

func TestArmExitMenu(t *testing.T) {
	vars := map[string]any{"otro": "dato"}
	res := modules.ArmExitMenu(vars, model.Conversation{EventID: "ev-cart-1"}, "PANTALLA")

	if res.Next != nil {
		t.Error("el menú de salida transiciona, quiero Next nil")
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != exitMenuHeader+"PANTALLA" {
		t.Errorf("Outputs = %q, quiero el menú de salida sobre la pantalla", res.Outputs)
	}
	want := map[string]any{"otro": "dato", "event_exit_menu": "PANTALLA", "event_exit_menu_event_id": "ev-cart-1"}
	if !maps.Equal(res.Vars, want) {
		t.Errorf("Vars = %v, quiero %v", res.Vars, want)
	}
	if !maps.Equal(vars, want) {
		t.Errorf("el mapa recibido = %v, quiero que quede armado (se muta)", vars)
	}
	if got := modules.ExitMenuArmedOn(res.Vars); got != "ev-cart-1" {
		t.Errorf("ExitMenuArmedOn = %q, quiero el evento sobre el que se armó", got)
	}
}

func TestExitMenuArmedOn(t *testing.T) {
	cases := map[string]struct {
		vars map[string]any
		want string
	}{
		"armed":                   {map[string]any{modules.ExitMenuVar: "P", modules.ExitMenuEventVar: "ev-1"}, "ev-1"},
		"nothing armed":           {map[string]any{}, ""},
		"old marker without seal": {map[string]any{modules.ExitMenuVar: "P"}, ""},
		"unreadable seal":         {map[string]any{modules.ExitMenuVar: "P", modules.ExitMenuEventVar: 7}, ""},
		"nil vars":                {nil, ""},
	}
	for name, tc := range cases {
		if got := modules.ExitMenuArmedOn(tc.vars); got != tc.want {
			t.Errorf("%s: ExitMenuArmedOn = %q, quiero %q", name, got, tc.want)
		}
	}
}

func TestDisarmExitMenu(t *testing.T) {
	vars := map[string]any{modules.ExitMenuVar: "PANTALLA", modules.ExitMenuEventVar: "ev-1", "otro": "dato"}
	if got := modules.DisarmExitMenu(vars); got != "PANTALLA" {
		t.Errorf("pantalla = %q, quiero PANTALLA", got)
	}
	if !maps.Equal(vars, map[string]any{"otro": "dato"}) {
		t.Errorf("vars = %v, quiero sin la pantalla ni el sello y lo ajeno intacto", vars)
	}

	if got := modules.DisarmExitMenu(map[string]any{}); got != "" {
		t.Errorf("sin menú armado, pantalla = %q", got)
	}

	unreadable := map[string]any{modules.ExitMenuVar: 7, modules.ExitMenuEventVar: "ev-1"}
	if got := modules.DisarmExitMenu(unreadable); got != "" || len(unreadable) != 0 {
		t.Errorf("marca ilegible: pantalla = %q y vars = %v, quiero vacío y borradas", got, unreadable)
	}
}

func TestNumberedStep_ValidOption(t *testing.T) {
	original := map[string]any{counterKey: 2, sealKey: "ev-1", "otro": "dato"}
	conv := model.Conversation{EventID: "ev-1", Vars: maps.Clone(original)}
	calls := 0

	res := modules.NumberedStep(numberedNode(), conv, " 2 \n", counterKey, modules.MaxReprompts,
		func(vars map[string]any, choice, target string) modules.Result {
			calls++
			if choice != "2" || target != "b" {
				t.Errorf("onValid recibió choice=%q target=%q, quiero 2 y b", choice, target)
			}
			if !maps.Equal(vars, map[string]any{"otro": "dato"}) {
				t.Errorf("onValid recibió vars = %v, quiero sin contador ni sello", vars)
			}
			vars["answer"] = choice
			return modules.Result{Next: &target, Vars: vars, Outputs: []string{"propio"}}
		})

	if calls != 1 {
		t.Fatalf("onValid se llamó %d veces, quiero 1", calls)
	}
	if res.Next == nil || *res.Next != "b" || res.Vars["answer"] != "2" || len(res.Outputs) != 1 {
		t.Errorf("Result = %+v, quiero el de onValid tal cual", res)
	}
	if !maps.Equal(conv.Vars, original) {
		t.Errorf("conv.Vars = %v, quiero intacto (pureza)", conv.Vars)
	}
}

// La entrada se recorta pero se casa exacta contra las claves de Options.
func TestNumberedStep_MatchIsExactAfterTrim(t *testing.T) {
	for _, input := range []string{"", "  ", "3", "1.", "1 2", "a", "uno"} {
		res := modules.NumberedStep(numberedNode(), model.Conversation{}, input, counterKey, modules.MaxReprompts, failOnValid(t))
		if res.Next != nil || len(res.Outputs) != 1 || res.Outputs[0] != invalidOptionText+numberedNode().Prompt {
			t.Errorf("entrada %q: Result = %+v, quiero el aviso de opción no válida", input, res)
		}
	}
}

func TestNumberedStep_InvalidOptionReprompts(t *testing.T) {
	cases := map[string]struct {
		conv     model.Conversation
		wantVars map[string]any
	}{
		"first invalid outside any event": {
			model.Conversation{},
			map[string]any{counterKey: 1},
		},
		"second invalid outside any event": {
			model.Conversation{Vars: map[string]any{counterKey: 1}},
			map[string]any{counterKey: 2},
		},
		"first invalid inside an event is sealed": {
			model.Conversation{EventID: "ev-1"},
			map[string]any{counterKey: 1, sealKey: "ev-1"},
		},
		"json round trip counter keeps counting": {
			model.Conversation{EventID: "ev-1", Vars: map[string]any{counterKey: float64(1), sealKey: "ev-1"}},
			map[string]any{counterKey: 2, sealKey: "ev-1"},
		},
		"counter loaded in another event starts over": {
			model.Conversation{EventID: "ev-2", Vars: map[string]any{counterKey: 2, sealKey: "ev-1"}},
			map[string]any{counterKey: 1, sealKey: "ev-2"},
		},
		"unsealed counter starts over inside an event": {
			model.Conversation{EventID: "ev-1", Vars: map[string]any{counterKey: 2}},
			map[string]any{counterKey: 1, sealKey: "ev-1"},
		},
		"sealed counter starts over outside any event": {
			model.Conversation{Vars: map[string]any{counterKey: 2, sealKey: "ev-1"}},
			map[string]any{counterKey: 1},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := maps.Clone(tc.conv.Vars)
			res := modules.NumberedStep(numberedNode(), tc.conv, "9", counterKey, modules.MaxReprompts, failOnValid(t))

			if res.Next != nil {
				t.Error("una opción inválida transiciona")
			}
			if len(res.Outputs) != 1 || res.Outputs[0] != invalidOptionText+numberedNode().Prompt {
				t.Errorf("Outputs = %q, quiero el aviso seguido del prompt", res.Outputs)
			}
			if !maps.Equal(res.Vars, tc.wantVars) {
				t.Errorf("Vars = %v, quiero %v", res.Vars, tc.wantVars)
			}
			if !maps.Equal(tc.conv.Vars, before) {
				t.Errorf("conv.Vars = %v, quiero intacto (pureza)", tc.conv.Vars)
			}
			if res.Effects != nil || res.Query != nil {
				t.Errorf("Result = %+v, quiero sin efectos ni consulta", res)
			}
		})
	}
}

// Regresión cero (D-043.10): fuera de un evento, el intento que agota el tope da la
// ayuda de siempre, reinicia el contador y NO arma el menú de salida.
func TestNumberedStep_LastAttemptOutsideEventSendsHelp(t *testing.T) {
	conv := model.Conversation{Vars: map[string]any{counterKey: 2, "otro": "dato"}}
	res := modules.NumberedStep(numberedNode(), conv, "9", counterKey, modules.MaxReprompts, failOnValid(t))

	if res.Next != nil {
		t.Error("la ayuda transiciona, quiero permanecer en el nodo")
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != helpText+numberedNode().Prompt {
		t.Errorf("Outputs = %q, quiero la ayuda seguida del prompt", res.Outputs)
	}
	if !maps.Equal(res.Vars, map[string]any{"otro": "dato"}) {
		t.Errorf("Vars = %v, quiero el contador reiniciado y sin menú de salida", res.Vars)
	}
}

// Dentro de un evento, el intento que agota el tope arma el menú de salida sobre el
// prompt del nodo, sellado con el evento activo, y reinicia el contador y su sello.
func TestNumberedStep_LastAttemptInsideEventArmsExitMenu(t *testing.T) {
	conv := model.Conversation{EventID: "ev-1", Vars: map[string]any{counterKey: 2, sealKey: "ev-1"}}
	res := modules.NumberedStep(numberedNode(), conv, "9", counterKey, modules.MaxReprompts, failOnValid(t))

	if res.Next != nil {
		t.Error("el menú de salida transiciona, quiero permanecer en el nodo")
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != exitMenuHeader+numberedNode().Prompt {
		t.Errorf("Outputs = %q, quiero el menú de salida sobre el prompt", res.Outputs)
	}
	want := map[string]any{modules.ExitMenuVar: numberedNode().Prompt, modules.ExitMenuEventVar: "ev-1"}
	if !maps.Equal(res.Vars, want) {
		t.Errorf("Vars = %v, quiero %v", res.Vars, want)
	}
}

// La escalera entera con el tope por defecto: dos avisos y, al tercero, la escalada;
// el cuarto inválido vuelve a ser el primero.
func TestNumberedStep_LadderRestartsAfterEscalation(t *testing.T) {
	for name, eventID := range map[string]string{"outside any event": "", "inside an event": "ev-1"} {
		t.Run(name, func(t *testing.T) {
			conv := model.Conversation{EventID: eventID}
			const turns = 4
			outputs := make([]string, 0, turns)
			for range turns {
				res := modules.NumberedStep(numberedNode(), conv, "x", counterKey, modules.MaxReprompts, failOnValid(t))
				if len(res.Outputs) != 1 {
					t.Fatalf("Outputs = %q, quiero una sola salida por turno", res.Outputs)
				}
				outputs = append(outputs, res.Outputs[0])
				conv.Vars = res.Vars
			}

			prompt := numberedNode().Prompt
			escalation := helpText + prompt
			if eventID != "" {
				escalation = exitMenuHeader + prompt
			}
			want := []string{invalidOptionText + prompt, invalidOptionText + prompt, escalation, invalidOptionText + prompt}
			for i := range want {
				if outputs[i] != want[i] {
					t.Errorf("turno %d: salida = %q, quiero %q", i+1, outputs[i], want[i])
				}
			}
		})
	}
}

// El tope lo fija el parámetro, no la constante.
func TestNumberedStep_HonorsMaxRepromptsArgument(t *testing.T) {
	res := modules.NumberedStep(numberedNode(), model.Conversation{}, "9", counterKey, 1, failOnValid(t))
	if len(res.Outputs) != 1 || res.Outputs[0] != helpText+numberedNode().Prompt {
		t.Errorf("con tope 1, Outputs = %q; quiero la ayuda al primer inválido", res.Outputs)
	}

	conv := model.Conversation{Vars: map[string]any{counterKey: 3}}
	res = modules.NumberedStep(numberedNode(), conv, "9", counterKey, 5, failOnValid(t))
	if res.Vars[counterKey] != 4 || res.Outputs[0] != invalidOptionText+numberedNode().Prompt {
		t.Errorf("con tope 5 y 3 fallos, Result = %+v; quiero el cuarto aviso", res)
	}
}
