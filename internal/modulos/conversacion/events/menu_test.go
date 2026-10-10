//go:build pendiente

package events

import (
	"errors"
	"strings"
	"testing"
)

// threeOptionMenu es el menú de referencia: dos tipos que pedir y un pedido que retomar.
func threeOptionMenu() Menu {
	return Menu{Options: []MenuOption{
		{Number: 1, Action: ActionStart, Kind: "cart"},
		{Number: 2, Action: ActionStart, Kind: "survey"},
		{Number: 3, Action: ActionResume, Kind: "cart", EventID: "9f1d1b7e-0000-4000-8000-0000000000aa"},
	}}
}

// TestMenuAction_Vocabulary: los tres literales que se PERSISTEN entre dos mensajes.
func TestMenuAction_Vocabulary(t *testing.T) {
	cases := []struct {
		got  MenuAction
		want string
	}{{ActionResume, "resume"}, {ActionRescue, "rescue"}, {ActionStart, "start"}}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("acción = %q, quería %q", c.got, c.want)
		}
	}
}

// TestMenu_Empty: sin opciones (nil o vacío) está vacío; con una, no.
func TestMenu_Empty(t *testing.T) {
	if !(Menu{}).Empty() || !(Menu{Options: []MenuOption{}}).Empty() {
		t.Error("un menú sin opciones no dice Empty")
	}
	if threeOptionMenu().Empty() {
		t.Error("un menú con opciones dice Empty")
	}
	if !(Menu{Unfiltered: true}).Empty() {
		t.Error("Unfiltered no hace que un menú sin opciones deje de estar vacío")
	}
}

// TestMenu_Encode_ShortTagsAndNoUnfiltered: la forma literal que se guarda: etiquetas cortas, el
// evento y la cuenta solo cuando los hay, y Unfiltered fuera.
func TestMenu_Encode_ShortTagsAndNoUnfiltered(t *testing.T) {
	m := Menu{Unfiltered: true, Options: []MenuOption{
		{Number: 1, Action: ActionStart, Kind: "cart"},
		{Number: 2, Action: ActionResume, Kind: "cart", EventID: "ev-1"},
		{Number: 3, Action: ActionRescue, Count: 2},
	}}
	raw, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"options":[{"n":1,"a":"start","k":"cart"},{"n":2,"a":"resume","k":"cart","e":"ev-1"},{"n":3,"a":"rescue","k":"","c":2}]}`
	if string(raw) != want {
		t.Errorf("Encode = %s\nquería  %s", raw, want)
	}
	if raw, err = (Menu{}).Encode(); err != nil || string(raw) != `{"options":null}` {
		t.Errorf("Encode del menú vacío = (%s, %v), quería {\"options\":null}", raw, err)
	}
}

// TestDecodeMenu_RoundTripForgetsHowItWasBuilt: lo guardado vuelve igual, opción a opción, y
// resuelve lo mismo; Unfiltered vuelve a false.
func TestDecodeMenu_RoundTripForgetsHowItWasBuilt(t *testing.T) {
	original := threeOptionMenu()
	original.Unfiltered = true
	raw, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := DecodeMenu(raw)
	if err != nil {
		t.Fatalf("DecodeMenu: %v", err)
	}
	if back.Unfiltered {
		t.Error("Unfiltered sobrevivió a la persistencia")
	}
	if len(back.Options) != len(original.Options) {
		t.Fatalf("volvieron %d opciones, quería %d", len(back.Options), len(original.Options))
	}
	for i, o := range original.Options {
		if back.Options[i] != o {
			t.Errorf("opción %d = %+v, quería %+v", i, back.Options[i], o)
		}
	}
	choice, ok := back.Resolve("3")
	if want := (Choice{Action: ActionResume, Kind: "cart", EventID: "9f1d1b7e-0000-4000-8000-0000000000aa"}); !ok || choice != want {
		t.Errorf("Resolve(\"3\") tras recuperar = (%+v, %v), quería %+v", choice, ok, want)
	}
}

// TestDecodeMenu_NothingSavedIsNotCorruption: sin nada guardado es ErrNoMenu a secas; lo que no se
// entiende es OTRO error, que no casa con ErrNoMenu.
func TestDecodeMenu_NothingSavedIsNotCorruption(t *testing.T) {
	if want := "events: no hay menú guardado para esta conversación"; ErrNoMenu.Error() != want {
		t.Errorf("ErrNoMenu = %q, quería %q", ErrNoMenu.Error(), want)
	}
	for _, raw := range [][]byte{nil, {}} {
		if m, err := DecodeMenu(raw); !errors.Is(err, ErrNoMenu) || err.Error() != ErrNoMenu.Error() || !m.Empty() {
			t.Errorf("DecodeMenu(%v) = (%+v, %v), quería (vacío, ErrNoMenu)", raw, m, err)
		}
	}
	for _, raw := range []string{"{", `"prosa"`, `{"options":"no es una lista"}`} {
		m, err := DecodeMenu([]byte(raw))
		if err == nil || errors.Is(err, ErrNoMenu) || !strings.HasPrefix(err.Error(), "events: leer el menú guardado: ") || !m.Empty() {
			t.Errorf("DecodeMenu(%s) = (%+v, %v); quería el menú vacío y un error de lectura", raw, m, err)
		}
	}
}

// TestMenu_Resolve_ReadsANumberAndNothingElse: un número de ESTE menú despacha; tolera espacios y
// el «.», «)», «-», «·» finales; todo lo demás no es una elección.
func TestMenu_Resolve_ReadsANumberAndNothingElse(t *testing.T) {
	m := threeOptionMenu()
	resume := Choice{Action: ActionResume, Kind: "cart", EventID: "9f1d1b7e-0000-4000-8000-0000000000aa"}
	cases := []struct {
		reply string
		want  Choice
		ok    bool
	}{
		{"1", Choice{Action: ActionStart, Kind: "cart"}, true},
		{"2", Choice{Action: ActionStart, Kind: "survey"}, true},
		{"3", resume, true},
		{" 3 ", resume, true},
		{"3.", resume, true},
		{"3)", resume, true},
		{"3 -", resume, true},
		{"3·", resume, true},
		{"\t3.) \n", resume, true},
		{"03", resume, true},
		{"", Choice{}, false},
		{"   ", Choice{}, false},
		{".", Choice{}, false},
		{"0", Choice{}, false},
		{"4", Choice{}, false},
		{"-1", Choice{}, false},
		{"+1", Choice{}, false},
		{"1.5", Choice{}, false},
		{"1 2", Choice{}, false},
		{"el segundo", Choice{}, false},
		{"quiero el 2", Choice{}, false},
		{"2 por favor", Choice{}, false},
		{"٢", Choice{}, false},
		{"２", Choice{}, false},
		{"99999999999999999999999", Choice{}, false},
	}
	for _, c := range cases {
		got, ok := m.Resolve(c.reply)
		if ok != c.ok || got != c.want {
			t.Errorf("Resolve(%q) = (%+v, %v), quería (%+v, %v)", c.reply, got, ok, c.want, c.ok)
		}
	}
	if got, ok := (Menu{}).Resolve("1"); ok || got != (Choice{}) {
		t.Errorf("Resolve sobre el menú vacío = (%+v, %v), quería (cero, false)", got, ok)
	}
}

// TestMenu_Resolve_ByNumberNotPosition_AndRescueCarriesNoEvent: se busca el Number de la opción;
// la de rescate resuelve sin tipo, sin evento y sin su cuenta.
func TestMenu_Resolve_ByNumberNotPosition_AndRescueCarriesNoEvent(t *testing.T) {
	m := Menu{Options: []MenuOption{
		{Number: 7, Action: ActionStart, Kind: "cart"},
		{Number: 9, Action: ActionRescue, Count: 4},
	}}
	if got, ok := m.Resolve("7"); !ok || got != (Choice{Action: ActionStart, Kind: "cart"}) {
		t.Errorf("Resolve(\"7\") = (%+v, %v)", got, ok)
	}
	if got, ok := m.Resolve("9"); !ok || got != (Choice{Action: ActionRescue}) {
		t.Errorf("Resolve(\"9\") = (%+v, %v), quería la acción de rescate sin evento", got, ok)
	}
	if _, ok := m.Resolve("1"); ok {
		t.Error("Resolve(\"1\") resolvió por posición")
	}
}

// TestKindName_OneWordPerKind: el vocabulario literal de los cuatro tipos de fábrica («pedido»,
// nunca «carrito») y el propio tipo para uno desconocido.
func TestKindName_OneWordPerKind(t *testing.T) {
	cases := map[string]string{
		"cart": "pedido", "survey": "encuesta", "media": "documentos", "menu": "menú",
		"reserva": "reserva", "": "",
	}
	for kind, want := range cases {
		if got := KindName(kind); got != want {
			t.Errorf("KindName(%q) = %q, quería %q", kind, got, want)
		}
	}
}

// TestMenu_Render_TheTextTheClientReads: el texto literal: cabecera, opciones numeradas y cierre,
// cada bloque separado por una línea en blanco.
func TestMenu_Render_TheTextTheClientReads(t *testing.T) {
	want := "¿Qué quieres hacer? Responde con el número de la opción:\n\n" +
		"1. Hacer un pedido\n" +
		"2. Responder una encuesta\n" +
		"3. Retomar el pedido que dejaste a medias\n\n" +
		"Si prefieres otra cosa, escríbelo y te ayudamos."
	if got := threeOptionMenu().Render(); got != want {
		t.Errorf("Render:\n%s\n--- quería ---\n%s", got, want)
	}
	if got := (Menu{}).Render(); got != "" {
		t.Errorf("Render del menú vacío = %q, quería la cadena vacía", got)
	}
}

// TestMenu_Render_EveryLabel: la etiqueta literal de cada acción y tipo, la de un tipo sin
// vocabulario y la de rescate con su cuenta; el número impreso es el Number de la opción.
func TestMenu_Render_EveryLabel(t *testing.T) {
	cases := []struct {
		option MenuOption
		want   string
	}{
		{MenuOption{Number: 1, Action: ActionStart, Kind: "cart"}, "1. Hacer un pedido"},
		{MenuOption{Number: 2, Action: ActionStart, Kind: "survey"}, "2. Responder una encuesta"},
		{MenuOption{Number: 3, Action: ActionStart, Kind: "media"}, "3. Ver los documentos"},
		{MenuOption{Number: 4, Action: ActionStart, Kind: "menu"}, "4. Ver el menú"},
		{MenuOption{Number: 5, Action: ActionStart, Kind: "reserva"}, "5. Empezar: reserva"},
		{MenuOption{Number: 6, Action: ActionResume, Kind: "cart", EventID: "ev-1"}, "6. Retomar el pedido que dejaste a medias"},
		{MenuOption{Number: 7, Action: ActionResume, Kind: "survey", EventID: "ev-2"}, "7. Continuar la encuesta que dejaste a medias"},
		{MenuOption{Number: 8, Action: ActionResume, Kind: "media", EventID: "ev-3"}, "8. Volver a los documentos que dejaste a medias"},
		{MenuOption{Number: 9, Action: ActionResume, Kind: "menu", EventID: "ev-4"}, "9. Volver al menú"},
		{MenuOption{Number: 10, Action: ActionResume, Kind: "reserva", EventID: "ev-5"}, "10. Retomar: reserva"},
		{MenuOption{Number: 11, Action: ActionRescue, Count: 3}, "11. Retomar algo que dejaste a medias (3)"},
		{MenuOption{Number: 42, Action: ActionStart, Kind: "cart"}, "42. Hacer un pedido"},
	}
	for _, c := range cases {
		got := Menu{Options: []MenuOption{c.option}}.Render()
		want := "¿Qué quieres hacer? Responde con el número de la opción:\n\n" + c.want + "\n\n" +
			"Si prefieres otra cosa, escríbelo y te ayudamos."
		if got != want {
			t.Errorf("Render de %+v:\n%s\n--- quería ---\n%s", c.option, got, want)
		}
		if c.option.EventID != "" && strings.Contains(got, c.option.EventID) {
			t.Errorf("el render enseña el identificador del evento: %s", got)
		}
	}
}
