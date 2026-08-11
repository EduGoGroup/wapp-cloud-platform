package events_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// menuDeTres es el menú de laboratorio y es el que produce el criterio de
// aceptación, escrito a mano para poder probar el menú SIN despachador: pedir
// carrito, pedir encuesta y —además— retomar el carrito que ya estaba vivo. El
// carrito sale DOS veces a propósito (E-9.3).
func menuDeTres() events.Menu {
	return events.Menu{Options: []events.MenuOption{
		{Number: 1, Action: events.ActionStart, Kind: "cart"},
		{Number: 2, Action: events.ActionStart, Kind: "survey"},
		{Number: 3, Action: events.ActionResume, Kind: "cart", EventID: "9f1d1b7e-0000-4000-8000-0000000000aa"},
	}}
}

// TestResolve_ElNumeroDespachaSinClasificador es el corazón de T2.3: la
// respuesta se lee, no se comprende. Un número devuelve su elección sin tocar
// nada más.
func TestResolve_ElNumeroDespachaSinClasificador(t *testing.T) {
	elec, ok := menuDeTres().Resolve("2")
	if !ok {
		t.Fatalf("«2» debe resolver")
	}
	if elec.Action != events.ActionStart || elec.Kind != "survey" || elec.EventID != "" {
		t.Fatalf("«2» debe empezar una encuesta nueva; got %+v", elec)
	}
}

// TestResolve_ToleraElPuntoYLosEspacios: «2.» y « 2 » son lo que teclea un
// humano, no otra intención.
func TestResolve_ToleraElPuntoYLosEspacios(t *testing.T) {
	for _, respuesta := range []string{" 2 ", "2.", "2)", "2 ."} {
		elec, ok := menuDeTres().Resolve(respuesta)
		if !ok || elec.Kind != "survey" {
			t.Fatalf("%q debe resolver a la opción 2; got %+v ok=%v", respuesta, elec, ok)
		}
	}
}

// TestResolve_ElTextoLibreNoEsUnaEleccion: si la respuesta no es un número del
// menú, el despachador se aparta y el entrante sigue su camino normal. No es un
// error: es que esto no era una elección.
func TestResolve_ElTextoLibreNoEsUnaEleccion(t *testing.T) {
	for _, respuesta := range []string{"quiero el dos", "el segundo", "", "  ", "dos", "2a", "-1"} {
		if _, ok := menuDeTres().Resolve(respuesta); ok {
			t.Fatalf("%q no es una elección numérica y no debe resolver", respuesta)
		}
	}
}

// TestResolve_FueraDeRangoNoResuelve: un número que el menú no enseñó no
// despacha nada. El caso «0» importa aparte: la numeración empieza en 1.
func TestResolve_FueraDeRangoNoResuelve(t *testing.T) {
	for _, respuesta := range []string{"0", "4", "99"} {
		if _, ok := menuDeTres().Resolve(respuesta); ok {
			t.Fatalf("%q está fuera del menú y no debe resolver", respuesta)
		}
	}
}

// TestMenu_SobreviveEntreDosMensajes es la condición de persistencia del
// encargo: el cliente responde el número en el mensaje SIGUIENTE, así que el
// menú tiene que poder guardarse y recuperarse. Se resuelve sobre el menú
// RECUPERADO, no sobre el original — que es lo que hace un proceso distinto.
func TestMenu_SobreviveEntreDosMensajes(t *testing.T) {
	crudo, err := menuDeTres().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	recuperado, err := events.DecodeMenu(crudo)
	if err != nil {
		t.Fatalf("DecodeMenu: %v", err)
	}
	elec, ok := recuperado.Resolve("3")
	if !ok {
		t.Fatalf("el menú recuperado debe resolver «3»")
	}
	if elec.Action != events.ActionResume || elec.EventID != "9f1d1b7e-0000-4000-8000-0000000000aa" || elec.Kind != "cart" {
		t.Fatalf("el menú recuperado perdió a qué despachaba la opción; got %+v", elec)
	}
}

// TestDecodeMenu_SinNadaGuardadoEsElCasoNormal: la mayoría de los entrantes no
// vienen de un menú, y eso no es un fallo. ErrNoMenu deja distinguirlo de un
// estado corrupto sin leer el texto del error.
func TestDecodeMenu_SinNadaGuardadoEsElCasoNormal(t *testing.T) {
	if _, err := events.DecodeMenu(nil); !errors.Is(err, events.ErrNoMenu) {
		t.Fatalf("sin menú guardado quiero ErrNoMenu; got %v", err)
	}
}

// TestDecodeMenu_LoCorruptoNoSeConfundeConLoAusente: un JSON roto es un
// problema, y tiene que verse como tal.
func TestDecodeMenu_LoCorruptoNoSeConfundeConLoAusente(t *testing.T) {
	_, err := events.DecodeMenu([]byte("{esto no es json"))
	if err == nil {
		t.Fatalf("un menú corrupto debe fallar")
	}
	if errors.Is(err, events.ErrNoMenu) {
		t.Fatalf("corrupto NO es lo mismo que ausente; got %v", err)
	}
}

// TestMenu_LoPersistidoNoArrastraElComoSeConstruyo: Unfiltered describe cómo se
// armó ESTE menú, no qué hay que recordar para resolverlo, y por eso no viaja en
// el JSON. Si viajara, un menú recuperado afirmaría una anomalía que ya nadie
// puede comprobar.
func TestMenu_LoPersistidoNoArrastraElComoSeConstruyo(t *testing.T) {
	m := menuDeTres()
	m.Unfiltered = true

	crudo, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(string(crudo), "nfiltered") {
		t.Fatalf("Unfiltered no se persiste; got %s", crudo)
	}
	recuperado, err := events.DecodeMenu(crudo)
	if err != nil {
		t.Fatalf("DecodeMenu: %v", err)
	}
	if recuperado.Unfiltered {
		t.Fatalf("el menú recuperado no puede afirmar cómo se construyó")
	}
}

// TestRender_ElTextoEsElQueLeeElCliente fija el texto literal completo. Es un
// golden a propósito: lo que se manda por WhatsApp cambia cuando alguien decide
// cambiarlo, no de refilón al tocar el render.
func TestRender_ElTextoEsElQueLeeElCliente(t *testing.T) {
	quiero := "¿Qué quieres hacer? Responde con el número de la opción:\n\n" +
		"1. Hacer un pedido\n" +
		"2. Responder una encuesta\n" +
		"3. Retomar el pedido que dejaste a medias\n\n" +
		"Si prefieres otra cosa, escríbelo y te ayudamos."

	if got := menuDeTres().Render(); got != quiero {
		t.Fatalf("el texto del menú cambió.\ngot:\n%s\nquiero:\n%s", got, quiero)
	}
}

// TestRender_LaNumeracionQueSeVeEsLaQueResuelve: el número impreso y el que
// Resolve entiende son el mismo. Si el render numerara por su cuenta (por
// posición en la lista) y las opciones vinieran con otros números, el cliente
// tecleraría lo que ve y le despacharía otra cosa.
func TestRender_LaNumeracionQueSeVeEsLaQueResuelve(t *testing.T) {
	m := events.Menu{Options: []events.MenuOption{
		{Number: 7, Action: events.ActionStart, Kind: "cart"},
		{Number: 9, Action: events.ActionStart, Kind: "survey"},
	}}

	texto := m.Render()
	if !strings.Contains(texto, "7. Hacer un pedido") || !strings.Contains(texto, "9. Responder una encuesta") {
		t.Fatalf("el render debe imprimir el número de la opción; texto:\n%s", texto)
	}
	elec, ok := m.Resolve("9")
	if !ok || elec.Kind != "survey" {
		t.Fatalf("«9» debe resolver a lo que el texto dice que es 9; got %+v ok=%v", elec, ok)
	}
	if _, ok := m.Resolve("2"); ok {
		t.Fatalf("«2» no está en el texto y no debe resolver")
	}
}

// TestKindName_HablaPorNombreDeTipo: el nombre que se le dice al cliente. Lo
// usan también las confirmaciones de T2.4 («cerré tu pedido»), que por E-3 no
// pueden nombrar un identificador.
//
// `cart` se llama «pedido» —nunca «carrito»— en todo lo que lee el cliente
// (decisión de producto de Jhoan, 2026-08-09); `cart` sigue siendo el
// identificador interno del tipo.
func TestKindName_HablaPorNombreDeTipo(t *testing.T) {
	quiero := map[string]string{
		"cart":   "pedido",
		"survey": "encuesta",
		"media":  "documentos",
		"menu":   "menú",
	}
	for kind, nombre := range quiero {
		if got := events.KindName(kind); got != nombre {
			t.Fatalf("KindName(%q) = %q; quiero %q", kind, got, nombre)
		}
	}
}

// TestKindName_NingunTipoTieneDosPalabras es la garantía de la decisión: el
// nombre del tipo tiene que aparecer DENTRO de sus dos frases del menú. Si
// alguien vuelve a poner un nombre por un lado y otra palabra por el otro
// —«carrito» al cerrar y «pedido» al elegir—, este test lo caza aunque el resto
// siga en verde.
func TestKindName_NingunTipoTieneDosPalabras(t *testing.T) {
	for _, kind := range []string{"cart", "survey", "media", "menu"} {
		nombre := events.KindName(kind)
		start := events.Menu{Options: []events.MenuOption{
			{Number: 1, Action: events.ActionStart, Kind: kind},
		}}.Render()
		resume := events.Menu{Options: []events.MenuOption{
			{Number: 1, Action: events.ActionResume, Kind: kind, EventID: "id"},
		}}.Render()

		if !strings.Contains(start, nombre) {
			t.Fatalf("la opción de pedir %q no usa su nombre %q; texto:\n%s", kind, nombre, start)
		}
		if !strings.Contains(resume, nombre) {
			t.Fatalf("la opción de retomar %q no usa su nombre %q; texto:\n%s", kind, nombre, resume)
		}
	}
}

// TestRender_UnTipoNuevoNoDejaLaOpcionEnBlanco: enchufar un módulo en el
// Registry no debe costar una migración ni dejar al cliente ante una línea
// muda. Enseña el NOMBRE DEL TIPO, que E-3 permite; lo que prohíbe es el
// identificador.
func TestRender_UnTipoNuevoNoDejaLaOpcionEnBlanco(t *testing.T) {
	m := events.Menu{Options: []events.MenuOption{
		{Number: 1, Action: events.ActionStart, Kind: "reserva"},
	}}

	if got := m.Render(); !strings.Contains(got, "reserva") {
		t.Fatalf("un tipo sin vocabulario debe salir por su nombre; texto:\n%s", got)
	}
	if got := events.KindName("reserva"); got != "reserva" {
		t.Fatalf("KindName de un tipo nuevo es su propio nombre; got %q", got)
	}
}
