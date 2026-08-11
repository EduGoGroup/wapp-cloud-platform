package runtime_test

// exit_menu_e2e_test.go recorre el MENÚ DE SALIDA (Plan 043 · T5.2, D-043.10) ENTERO
// y por el camino de producción: `rt.HandleIncoming`, con el módulo real componiendo
// la pantalla, el cableado real de advanceLive interpretándola y el despachador real
// respondiendo a la opción 3.
//
// Por qué existe además de exit_menu_test.go (que es `package runtime` y llama a
// exitMenuChoice a pelo): ese fichero no puede tocar el CABLEADO. Medido con dos
// mutaciones sobre el árbol antes de escribir esto —(a) borrar la llamada a
// exitMenuStep de advanceLive y (b) moverla ANTES de liveEventSwitch— y las dos
// SOBREVIVÍAN con todo el paquete en verde: el menú de salida podía quedar
// desconectado de la tubería sin que nada se pusiera rojo. Aquí las dos mueren.
//
// Los cuatro criterios literales de tasks.md T5.2 §Criterio se comprueban en este orden:
// 3 basuras dentro del carrito ⇒ menú de salida · opción 2 deja el status en `open` y
// confirma por nombre de tipo sin identificador · opción 3 muestra el despachador ·
// 3 basuras SIN evento activo ⇒ el mensaje de ayuda de siempre.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// e2eExitMenu arma el runtime completo (resolver de disparos + plano de eventos +
// despachador) y devuelve lo necesario para afirmar sobre el resultado.
func e2eExitMenu(t *testing.T, menu events.Menu, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 9, 13, 45, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	return rt, repo, sender, contacts, evs
}

// e2eMenuDelDespachador es la lista que el despachador enseña en estos tests. Dos
// opciones a propósito: con una sola, un «2» no sería de nadie y el test de la
// opción 3 no podría distinguir «se enseñó la lista» de «se enseñó algo».
func e2eMenuDelDespachador() events.Menu {
	return events.Menu{Options: []events.MenuOption{
		{Number: 1, Action: events.ActionStart, Kind: "cart"},
		{Number: 2, Action: events.ActionStart, Kind: "survey"},
	}}
}

// e2eArmar lleva la conversación hasta el menú de salida ARMADO: abre el evento
// `cart` por su palabra y mete las tres basuras. Devuelve el evento y el contact_id.
func e2eArmar(t *testing.T, rt *runtime.Runtime, repo *store.MemoryRepository, contacts *contact.MemoryResolver, evs *memEventStore, pfx string) (events.Event, string) {
	t.Helper()
	ctx := context.Background()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", pfx+"-0")); err != nil {
		t.Fatalf("palabra del evento: %v", err)
	}
	ev := aliveOfKind(t, evs, "cart")
	cid := resolveID(t, contacts, testContact)
	if st := loadState(t, repo, cid); st.EventID != ev.ID {
		t.Fatalf("precondición: el evento `cart` debe quedar ACTIVO; event_id=%q", st.EventID)
	}
	for i := 1; i <= modules.MaxReprompts; i++ {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "zzz", pfx+"-"+string(rune('a'+i)))); err != nil {
			t.Fatalf("basura %d: %v", i, err)
		}
	}
	return ev, cid
}

// TestExitMenuE2E_TresBasurasDentroDelEventoEnseñanElMenuYElDosDesactiva recorre el
// criterio principal de punta a punta: 3 basuras dentro del evento ⇒ el cliente LEE
// el menú de salida; el «2» que contesta NO llega al módulo (que lo tomaría por una
// opción legítima del menú del flujo) sino que desactiva el evento dejándolo `open`,
// con una confirmación que nombra el TIPO y no filtra ningún identificador (E-3).
func TestExitMenuE2E_TresBasurasDentroDelEventoEnseñanElMenuYElDosDesactiva(t *testing.T) {
	rt, repo, sender, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(), eventStartRule("carrito", "cart"))
	ev, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.a")

	leido := sender.texts()[len(sender.texts())-1]
	if leido != modules.ExitMenuText(sampleFlow().Nodes["root"].Prompt) {
		t.Fatalf("al 3er inválido el cliente debe LEER el menú de salida; leyó %q", leido)
	}
	if _, armado := loadState(t, repo, cid).Vars[modules.ExitMenuVar]; !armado {
		t.Fatal("el menú de salida debe quedar armado en el estado PERSISTIDO")
	}

	antes := len(sender.texts())
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "2", "wamid.a-2")); err != nil {
		t.Fatalf("opción 2: %v", err)
	}

	if got := evs.statuses()[ev.ID]; got != events.StatusOpen {
		t.Fatalf("la opción 2 DESACTIVA sin matar: status = %q, quiero %q (D-043.2)", got, events.StatusOpen)
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("la opción 2 apaga el puntero: event_id = %q, quiero \"\"", st.EventID)
	}
	dichos := sender.texts()[antes:]
	if len(dichos) != 1 {
		t.Fatalf("la opción 2 confirma UNA vez; dijo %v", dichos)
	}
	if strings.Contains(dichos[0], ev.HistoryID) || strings.Contains(dichos[0], ev.ID) {
		t.Fatalf("la confirmación NO puede llevar identificador (E-3): %q", dichos[0])
	}
	if !strings.Contains(dichos[0], events.KindName("cart")) {
		t.Fatalf("la confirmación debe nombrar el TIPO (%q): %q", events.KindName("cart"), dichos[0])
	}
	// Y la prueba de que el «2» no fue del módulo: el flujo NO transicionó a la hoja
	// que ese «2» habría elegido en el menú del tenant.
	if strings.Contains(dichos[0], sampleFlow().Nodes["soporte"].Text) {
		t.Fatalf("el «2» llegó al MÓDULO en vez de al menú de salida: %q", dichos[0])
	}
}

// TestExitMenuE2E_Opcion1ReemiteLaPantallaYNoAbandona: «1» re-emite la MISMA pantalla
// en la que el cliente se atascó, deja el evento activo intacto y desarma el menú (el
// turno siguiente vuelve a ser del módulo, con el contador desde cero).
func TestExitMenuE2E_Opcion1ReemiteLaPantallaYNoAbandona(t *testing.T) {
	rt, repo, sender, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(), eventStartRule("carrito", "cart"))
	ev, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.b")

	antes := len(sender.texts())
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "1", "wamid.b-1")); err != nil {
		t.Fatalf("opción 1: %v", err)
	}
	dichos := sender.texts()[antes:]
	if len(dichos) != 1 || dichos[0] != sampleFlow().Nodes["root"].Prompt {
		t.Fatalf("la opción 1 re-emite la pantalla guardada TAL CUAL; dijo %v", dichos)
	}
	st := loadState(t, repo, cid)
	if st.EventID != ev.ID {
		t.Fatalf("la opción 1 NO abandona: event_id = %q, quiero %q", st.EventID, ev.ID)
	}
	if _, armado := st.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("la opción 1 consume el menú: la marca no puede sobrevivir al turno")
	}
	if got := evs.statuses()[ev.ID]; got != events.StatusOpen {
		t.Fatalf("la opción 1 no toca el status: %q", got)
	}
	// Y la prueba de que «1» NO fue al módulo: el flujo sigue en el nodo raíz, no en
	// la hoja «ventas» que ese «1» habría elegido.
	if st.CurrentNode != "root" {
		t.Fatalf("el «1» llegó al MÓDULO: el flujo transicionó a %q", st.CurrentNode)
	}
}

// TestExitMenuE2E_Opcion3MuestraElDespachador es el criterio literal «opción 3 muestra
// el despachador», y se comprueba con el despachador CABLEADO: el cliente lee la lista
// y el número siguiente la despacha de verdad (no basta con que nazca el evento
// `menu`: sin este segundo tramo, un despachador que naciera mudo pasaría).
func TestExitMenuE2E_Opcion3MuestraElDespachador(t *testing.T) {
	rt, repo, sender, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(), eventStartRule("carrito", "cart"))
	cartEv, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.c")

	antes := len(sender.texts())
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "3", "wamid.c-3")); err != nil {
		t.Fatalf("opción 3: %v", err)
	}
	dichos := sender.texts()[antes:]
	if len(dichos) != 1 || dichos[0] != e2eMenuDelDespachador().Render() {
		t.Fatalf("la opción 3 debe ENSEÑAR la lista del despachador; dijo %v", dichos)
	}
	if strings.Contains(dichos[0], cartEv.HistoryID) || strings.Contains(dichos[0], cartEv.ID) {
		t.Fatalf("la lista no puede llevar identificadores (E-3): %q", dichos[0])
	}
	menuEv := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != menuEv.ID {
		t.Fatalf("tras la opción 3 el evento ACTIVO debe ser el menú; event_id=%q", st.EventID)
	}
	if got := evs.statuses()[cartEv.ID]; got != events.StatusOpen {
		t.Fatalf("el carrito que se deja atrás NO se cierra (D-043.4/E-11.3): %q", got)
	}
	// El menú responde de verdad: elegir «1) Hacer un pedido» conmuta al carrito vivo.
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "1", "wamid.c-4")); err != nil {
		t.Fatalf("elegir en el despachador: %v", err)
	}
	if st := loadState(t, repo, cid); st.EventID != cartEv.ID {
		t.Fatalf("la lista debe ser USABLE: elegir el pedido devuelve al carrito; event_id=%q", st.EventID)
	}
}

// TestExitMenuE2E_SinEventoActivoElTercerInvalidoEsLaAyudaDeSiempre es la REGRESIÓN
// CERO por el camino de producción: la MISMA conversación, el MISMO flujo y las MISMAS
// tres basuras, pero sin evento activo ⇒ el mensaje de ayuda de siempre, byte a byte,
// y ni una marca de menú de salida en el estado.
func TestExitMenuE2E_SinEventoActivoElTercerInvalidoEsLaAyudaDeSiempre(t *testing.T) {
	rt, repo, sender, contacts, _ := e2eExitMenu(t, e2eMenuDelDespachador())
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cid := resolveID(t, contacts, testContact)
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("precondición: SIN evento activo; event_id=%q", st.EventID)
	}
	for i := 1; i <= modules.MaxReprompts; i++ {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "zzz", "wamid.d-"+string(rune('a'+i)))); err != nil {
			t.Fatalf("basura %d: %v", i, err)
		}
	}
	quiero := "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n" +
		sampleFlow().Nodes["root"].Prompt
	if leido := sender.texts()[len(sender.texts())-1]; leido != quiero {
		t.Fatalf("fuera de evento el 3er inválido es la ayuda de siempre;\nleyó  %q\nquiero %q", leido, quiero)
	}
	st := loadState(t, repo, cid)
	if _, armado := st.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("fuera de evento el menú de salida NUNCA se arma (regresión cero)")
	}
	// Y el «2» siguiente sigue siendo del MÓDULO: elige «Soporte», como toda la vida.
	antes := len(sender.texts())
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "wamid.d-z")); err != nil {
		t.Fatalf("dos: %v", err)
	}
	dichos := sender.texts()[antes:]
	if len(dichos) != 1 || dichos[0] != sampleFlow().Nodes["soporte"].Text {
		t.Fatalf("fuera de evento el «2» es del módulo; dijo %v", dichos)
	}
}

// TestExitMenuE2E_ElMarcadorNoSobreviveAlCambioDeEvento es el candado del defecto que
// destapó esta revisión: saveMenuState (el camino del despachador) CONSERVA las Vars
// al cambiar de evento, así que el marcador del menú de salida sobrevivía al cambio de
// contexto. Con él vivo, el primer número que la lista del despachador no reconociera
// desactivaba el evento reción abierto («Listo, dejamos el menú por ahora»).
//
// Aquí se reproduce el escenario COMPLETO: menú de salida armado en el carrito,
// cambio al despachador por palabra, y luego un «3» que la lista NO ofrece.
func TestExitMenuE2E_ElMarcadorNoSobreviveAlCambioDeEvento(t *testing.T) {
	rt, repo, sender, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(),
		eventStartRule("carrito", "cart"), eventStartRule("menu", "menu"))
	_, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.e")
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "wamid.e-m")); err != nil {
		t.Fatalf("palabra del despachador: %v", err)
	}
	menuEv := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != menuEv.ID {
		t.Fatalf("precondición: el evento activo debe ser el menú; event_id=%q", st.EventID)
	}

	antes := len(sender.texts())
	// «3» NO es una opción de esta lista (tiene 1 y 2) y tampoco puede ser la opción 3
	// de un menú de salida que era del CARRITO.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "3", "wamid.e-3")); err != nil {
		t.Fatalf("tres: %v", err)
	}
	for _, dicho := range sender.texts()[antes:] {
		if strings.Contains(dicho, events.KindName("menu")) && strings.Contains(dicho, "por ahora") {
			t.Fatalf("FUGA: un número ajeno lo interpretó el menú de salida del CARRITO y desactivó el evento activo: %q", dicho)
		}
		if dicho == e2eMenuDelDespachador().Render() {
			t.Fatalf("FUGA: el menú de salida rancio mandó al despachador con un número que no era suyo: %q", dicho)
		}
	}
	if st := loadState(t, repo, cid); st.Vars[modules.ExitMenuVar] != nil {
		t.Fatal("el marcador rancio debe quedar DESARMADO en cuanto se mira")
	}
}

// TestExitMenuE2E_ElSaltoPorTipoGanaAlMenuDeSalida fija el ORDEN del cableado
// (advanceLive: escape → liveEventSwitch → menú de salida → Step) para el único texto
// capaz de distinguirlo: un tenant cuya palabra de event_start es, literalmente, «3».
// Con ese texto y el menú de salida armado, gana la NAVEGACIÓN (nace la encuesta), no
// la opción 3 del menú de salida (que habría abierto el despachador).
//
// Es una elección deliberada del cableado y no un accidente: una orden de navegación
// del tenant conserva su prioridad EXACTA con o sin menú de salida delante (INV-06).
// La contrapartida, dicha entera: un tenant que configure una palabra numérica se
// tapa a sí mismo esa opción del menú de salida mientras esté armado.
func TestExitMenuE2E_ElSaltoPorTipoGanaAlMenuDeSalida(t *testing.T) {
	rt, repo, sender, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(),
		eventStartRule("carrito", "cart"), eventStartRule("3", "survey"))
	cartEv, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.f")

	antes := len(sender.texts())
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "3", "wamid.f-3")); err != nil {
		t.Fatalf("tres: %v", err)
	}
	survey := aliveOfKind(t, evs, "survey")
	if st := loadState(t, repo, cid); st.EventID != survey.ID {
		t.Fatalf("la orden de navegación del tenant manda: event_id=%q, quiero la encuesta %q", st.EventID, survey.ID)
	}
	for _, dicho := range sender.texts()[antes:] {
		if dicho == e2eMenuDelDespachador().Render() {
			t.Fatalf("el menú de salida se adelantó al salto por tipo: %q", dicho)
		}
	}
	if got := evs.statuses()[cartEv.ID]; got != events.StatusOpen {
		t.Fatalf("el carrito que se deja atrás sigue open: %q", got)
	}
}

// TestExitMenuE2E_Opcion3EsIrAlMenu_NoEmpezarUnoNuevo fija el GESTO de la opción 3
// (gestureGoTo y no gestureNew), que es lo único que la distingue de elegir «empezar
// uno nuevo» en el despachador. La diferencia solo es observable sobre un evento
// `menu` vivo y VENCIDO: con «ve» se conmuta hacia él (E-11.3, no se cierra nada) y
// con «nuevo» E-11 lo cancelaría y pariría otro. Sin este test las dos constantes son
// intercambiables y el paquete sigue verde — medido con la mutación.
func TestExitMenuE2E_Opcion3EsIrAlMenu_NoEmpezarUnoNuevo(t *testing.T) {
	rt, repo, _, contacts, evs := e2eExitMenu(t, e2eMenuDelDespachador(), eventStartRule("carrito", "cart"))
	// EL reloj del evento: una hora. El `menu` de hace dos está VIVO y VENCIDO.
	sembrarInactividad(t, repo, time.Hour, 0)
	evs.contactID = resolveID(t, contacts, testContact)
	viejo := evs.seedAlive("menu", time.Date(2026, 8, 9, 11, 45, 0, 0, time.UTC))

	cartEv, cid := e2eArmar(t, rt, repo, contacts, evs, "wamid.g")
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "3", "wamid.g-3")); err != nil {
		t.Fatalf("opción 3: %v", err)
	}

	if got := evs.statuses()[viejo.ID]; got != events.StatusOpen {
		t.Fatalf("«ver el menú» es IR al menú, no empezar uno: el `menu` vencido NO se cancela; quedó %q", got)
	}
	if vivo := aliveOfKind(t, evs, "menu"); vivo.ID != viejo.ID {
		t.Fatalf("debe CONMUTARSE al `menu` que ya existía (%q), no parir otro (%q)", viejo.ID, vivo.ID)
	}
	if got := evs.total(); got != 2 {
		t.Fatalf("no debe nacer ninguna fila nueva: hay %d (quiero 2: el carrito y el menú)", got)
	}
	if st := loadState(t, repo, cid); st.EventID != viejo.ID {
		t.Fatalf("el evento activo debe ser el `menu` de siempre; event_id=%q", st.EventID)
	}
	if got := evs.statuses()[cartEv.ID]; got != events.StatusOpen {
		t.Fatalf("el carrito sigue open: %q", got)
	}
}
