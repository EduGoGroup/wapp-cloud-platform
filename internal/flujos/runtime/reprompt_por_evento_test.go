package runtime_test

// reprompt_por_evento_test.go — los intentos inválidos pertenecen al EVENTO en que se
// cometieron (Plan 043 · Ola 5).
//
// Nació como sonda de H1 y fija la propiedad para siempre. El defecto que cazó: T5.2
// selló el MARCADOR del menú de salida con su evento (ExitMenuEventVar) pero dejó los
// CONTADORES de reprompt —anteriores al plano de eventos— sin noción de evento, así que
// cruzaban el cambio de contexto por los caminos que conservan Vars. Un contador cargado
// dentro del evento A armaba el menú de salida al PRIMER inválido del evento B, y
// ArmExitMenu lo sellaba —correctamente— con B, de modo que la guarda de T5.2 lo bendecía
// en vez de cortarlo: el «2» acababa desactivando el evento equivocado.
//
// Se arregló SELLANDO el contador con su evento (modules.RepromptEventKey y, para el
// carrito, cartState.RepromptsEvent): la caducidad es POR LECTURA, así que un camino
// nuevo que cambie de evento conservando Vars no puede reintroducir el defecto
// olvidándose de limpiar.
//
// Todos los pasos previos a cada afirmación se comprueban como PRECONDICIÓN con su
// propio Fatalf distinguible ("precondición:"), para que un rojo diga siempre si falló
// la propiedad o el andamio.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// zzSondaMenuUnaOpcion es la lista del despachador de estos tests: UNA sola opción.
// Es deliberado — con una lista de una opción, los números «2» y «3» NO son del
// despachador, y eso es lo que permite que lleguen al camino que se quiere medir
// (el módulo del flujo viejo, y el menú de salida) sin que menuChoice los intercepte.
//
// Los tres helpers con prefijo `zzSonda` CONSERVAN su nombre a propósito: los comparte
// zz_sonda_h2_test.go, que sigue viva (H2 tiene dueño en la Ola 6). Renómbralos cuando
// esa sonda se cierre, no antes.
func zzSondaMenuUnaOpcion() events.Menu {
	return events.Menu{Options: []events.MenuOption{
		{Number: 1, Action: events.ActionStart, Kind: "cart"},
	}}
}

// zzSondaMenuRule es la regla event_start del tipo `menu` SIN flujo: es como la
// configura un tenant real (el menú no es una fila de flow_definitions, D-043.3).
func zzSondaMenuRule() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
		MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true,
	}
}

// zzSondaEnv arma el runtime COMPLETO por el camino de producción (HandleIncoming):
// resolver de disparos, plano de eventos, despachador, PersistSink real (para poder
// leer flow_events) y el hilo del evento cableado sobre el mismo doble.
func zzSondaEnv(t *testing.T, flow model.Flow, eng bool, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore,
) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, flow); err != nil {
		t.Fatalf("precondición: sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("precondición: insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC))
	motor := newEngine()
	if eng {
		motor = newSurveyEngine()
	}
	rt := runtime.New(repo, motor, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo).WithDecisionThread(evs)),
		runtime.WithDispatcher(fakeDispatcher{menu: zzSondaMenuUnaOpcion()}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	return rt, repo, sender, contacts, evs
}

// flujoDeMenusSinFinal es sampleFlow con las dos opciones del nodo raíz apuntando a
// OTROS nodos de menú en vez de a un `message` terminal: sus nodos SIEMPRE tienen a
// dónde ir, así que el flujo nunca acaba.
//
// 🔴 NO LO CAMBIES A UN FLUJO TERMINAL. Que no termine es la condición que permite a
// este fichero fijar UNA propiedad. Con un flujo terminal —como sampleFlow, cuyo «2»
// lleva al `message` sin salida `soporte`— el último test se pondría rojo por un
// defecto que no es el suyo: al acabarse el flujo, closeIfFinished apaga y CIERRA el
// evento `menu` que lo había heredado, un evento que ni siquiera tiene flujo propio
// (D-043.3). Ese cierre falso es el hallazgo H2 del barrido de la Ola 5 (tasks.md,
// hallazgo #22), heredado a la Ola 6 CON DUEÑO y con su propia sonda.
//
// Dicho de otro modo: un test que dependa de que H2 esté arreglado no es un test de
// esta propiedad, sino de dos, y se pondrá rojo por la que no le toca. Si algún día
// quieres simplificar esto a sampleFlow, arregla H2 primero y comprueba entonces que
// el rojo desaparece por la razón correcta.
func flujoDeMenusSinFinal() model.Flow {
	return model.Flow{
		FlowID:  testFlow,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Hola 👋\n1) Ventas\n2) Soporte",
				Options: map[string]string{"1": "ventas", "2": "soporte"},
			},
			"ventas": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Ventas\n1) Volver\n2) Volver",
				Options: map[string]string{"1": "root", "2": "root"},
			},
			"soporte": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Soporte\n1) Volver\n2) Volver",
				Options: map[string]string{"1": "root", "2": "root"},
			},
		},
	}
}

// cruceDeEventoConContadorCargado lleva la conversación EXACTAMENTE al escenario del
// defecto:
//
//	«carrito» → evento A (cart) con el flujo del tenant en su nodo raíz
//	2 basuras → contador de reprompt = 2 DENTRO del evento A   (precondición dura)
//	«menu»    → evento B (menu); saveMenuState CONSERVA las Vars y el FlowID
//
// Devuelve el evento A, el evento B y el contact_id.
func cruceDeEventoConContadorCargado(t *testing.T, rt *runtime.Runtime, repo *store.MemoryRepository,
	contacts *contact.MemoryResolver, evs *memEventStore, pfx string,
) (events.Event, events.Event, string) {
	t.Helper()
	ctx := context.Background()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", pfx+"-0")); err != nil {
		t.Fatalf("precondición: palabra del evento: %v", err)
	}
	evA := aliveOfKind(t, evs, "cart")
	cid := resolveID(t, contacts, testContact)
	if st := loadState(t, repo, cid); st.EventID != evA.ID {
		t.Fatalf("precondición: el `cart` debe quedar ACTIVO; event_id=%q", st.EventID)
	}
	// DOS basuras: una menos que MaxReprompts. El contador queda cargado y el menú de
	// salida NO se ha armado todavía.
	for i := 1; i <= modules.MaxReprompts-1; i++ {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "zzz", pfx+"-b"+string(rune('0'+i)))); err != nil {
			t.Fatalf("precondición: basura %d: %v", i, err)
		}
	}
	st := loadState(t, repo, cid)
	if got := modules.GetInt(st.Vars, menu.RepromptKey); got != modules.MaxReprompts-1 {
		t.Fatalf("precondición: el contador dentro del evento A debe valer %d, y vale %d (Vars=%v)",
			modules.MaxReprompts-1, got, st.Vars)
	}
	if _, armado := st.Vars[modules.ExitMenuVar]; armado {
		t.Fatalf("precondición: con %d inválidos el menú de salida NO debe estar armado", modules.MaxReprompts-1)
	}
	// CAMBIO DE CONTEXTO por el camino que CONSERVA las Vars (saveMenuState).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", pfx+"-m")); err != nil {
		t.Fatalf("precondición: palabra del despachador: %v", err)
	}
	evB := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != evB.ID {
		t.Fatalf("precondición: el evento ACTIVO debe ser el menú (%q); event_id=%q", evB.ID, st.EventID)
	}
	return evA, evB, cid
}

// TestReprompt_ElContadorNoCruzaElCambioDeEvento: los intentos inválidos son del nodo Y
// del evento en que se cometieron; al pasar a otro evento el contador arranca de cero.
//
// Se lee con modules.RepromptCount y no con GetInt porque el contador CADUCA POR
// LECTURA: el número crudo puede seguir en Vars, pero sellado con el evento anterior no
// vale para este. Quien lo lea de otra forma se estará saltando la corrección — no hay
// más lectores que este y NumberedStep.
func TestReprompt_ElContadorNoCruzaElCambioDeEvento(t *testing.T) {
	rt, repo, _, contacts, evs := zzSondaEnv(t, sampleFlow(), false,
		eventStartRule("carrito", "cart"), zzSondaMenuRule())
	_, evB, cid := cruceDeEventoConContadorCargado(t, rt, repo, contacts, evs, "reprompt-a")

	st := loadState(t, repo, cid)
	if got := modules.RepromptCount(st.Vars, evB.ID, menu.RepromptKey); got != 0 {
		t.Fatalf("ASSERT 1: contador de reprompt tras cambiar del evento %q al %q = %d, quiero 0 "+
			"(el contador es del evento en que se falló, no de la conversación)", "cart", evB.Kind, got)
	}
}

// TestReprompt_UnSoloInvalidoEnElEventoNuevoNoArmaElMenuDeSalida es donde el defecto se
// volvía visible: UN solo número inválido dentro del evento nuevo no puede escalar al
// menú de salida (haría falta llegar a MaxReprompts), y si aun así se armara, quedaría
// SELLADO con el evento equivocado.
//
// «3» se elige a propósito: no está en la lista del despachador (que solo ofrece 1) y
// tampoco es una opción del nodo raíz del flujo (que ofrece 1 y 2), así que es
// inválido para los dos y llega al módulo del flujo VIEJO bajo el evento `menu`.
func TestReprompt_UnSoloInvalidoEnElEventoNuevoNoArmaElMenuDeSalida(t *testing.T) {
	rt, repo, _, contacts, evs := zzSondaEnv(t, sampleFlow(), false,
		eventStartRule("carrito", "cart"), zzSondaMenuRule())
	evA, evB, cid := cruceDeEventoConContadorCargado(t, rt, repo, contacts, evs, "reprompt-b")

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "3", "reprompt-b-3")); err != nil {
		t.Fatalf("precondición: primer inválido dentro del evento nuevo: %v", err)
	}
	st := loadState(t, repo, cid)
	sello := modules.ExitMenuArmedOn(st.Vars)
	if _, armado := st.Vars[modules.ExitMenuVar]; armado {
		t.Fatalf("ASSERT 2: con UN solo inválido dentro del evento %q el menú de salida NO debe armarse, "+
			"y está armado con el sello event_id=%q (evento activo=%q · evento del contador=%q)",
			evB.Kind, sello, evB.ID, evA.ID)
	}
}

// TestReprompt_ElDosNoDesactivaElEventoConMenuHeredado: el menú de salida que se armaría
// por un contador del carrito no puede acabar apagando el evento `menu`. Como con un
// solo inválido no debe haber menú de salida, el «2» siguiente es del MÓDULO (opción
// legítima del nodo raíz) y el evento activo sigue siendo el menú.
//
// Corre sobre flujoDeMenusSinFinal para que el «2» no termine el flujo: ver el porqué
// en el comentario de ese helper (aísla esta propiedad de H2).
func TestReprompt_ElDosNoDesactivaElEventoConMenuHeredado(t *testing.T) {
	rt, repo, sender, contacts, evs := zzSondaEnv(t, flujoDeMenusSinFinal(), false,
		eventStartRule("carrito", "cart"), zzSondaMenuRule())
	evA, evB, cid := cruceDeEventoConContadorCargado(t, rt, repo, contacts, evs, "reprompt-c")
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "3", "reprompt-c-3")); err != nil {
		t.Fatalf("precondición: primer inválido dentro del evento nuevo: %v", err)
	}
	antes := len(sender.texts())
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "reprompt-c-2")); err != nil {
		t.Fatalf("precondición: el «2»: %v", err)
	}
	dichos := sender.texts()[antes:]
	st := loadState(t, repo, cid)

	confirmacionDeAbandono := ""
	for _, d := range dichos {
		if strings.Contains(d, "por ahora") || strings.Contains(d, "lo dejamos") {
			confirmacionDeAbandono = d
		}
	}
	if confirmacionDeAbandono != "" {
		t.Fatalf("ASSERT 3: el «2» desactivó un evento con un menú de salida heredado del %q; "+
			"dijo %q · event_id tras el turno=%q (menú=%q, carrito=%q) · status menú=%q carrito=%q",
			evA.Kind, confirmacionDeAbandono, st.EventID, evB.ID, evA.ID,
			evs.statuses()[evB.ID], evs.statuses()[evA.ID])
	}
	if st.EventID != evB.ID {
		t.Fatalf("ASSERT 3b: el evento activo tras el «2» = %q, quiero el menú %q (nadie pidió abandonarlo)",
			st.EventID, evB.ID)
	}
}
