package runtime_test

// dispatcher_menu_seal_test.go — E2 (Ola 6): el menú del despachador (varPendingMenu,
// events.go) pertenece al evento sobre el que se pintó, y a ningún otro. Antes de esta
// tarea no llevaba ninguna marca: saveMenuState CONSERVA las Vars al cambiar de evento
// (crea/hereda el flow_state sin borrar dispatcher_menu — el mismo motivo por el que
// la Ola 5 tuvo que sellar ExitMenuVar y los contadores de reprompt), así que un menú
// armado para el evento B sobrevivía intacto cuando B se desactivaba, y el número
// siguiente se resolvía contra una lista que ya no representaba nada.

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// TestMenuChoice_NoResuelveContraUnMenuDeUnEventoYaInactivo (E-2): reproduce la escena
// real del hallazgo — «evento con flujo → menu → event_stop → número» — y fija que el
// número NO se despache contra la lista de un evento que ya dejó de ser el activo.
//
// Secuencia:
//  1. «carrito» pare el evento A (cart, CON flujo) y lo deja esperando en su nodo raíz
//     (sampleFlow: root, sin terminar).
//  2. «menu» pare el evento B (menu); como A no había terminado, saveMenuState NO
//     resetea el flow_state: hereda FlowID/CurrentNode de A y arma el menú del
//     despachador (dispatcher_menu) apuntando a B.
//  3. «salir» (event_stop) desactiva B: el puntero se apaga (EventID=""), pero
//     summarizeAbandoned/stopEvent NO tocan Vars — dispatcher_menu sigue ahí,
//     sellado (o no, antes de esta tarea) con B.
//  4. «1» — la opción 1 del menú del despachador pedía «empezar un cart» — es TAMBIÉN
//     la opción válida del nodo raíz que A/B heredaron. Sin el sello, menuChoice
//     resuelve contra la lista vieja de B y despacha «empezar cart» con gesto NUEVO,
//     lo que RE-ACTIVA el evento A (reuseOrRetire: A sigue vivo y dentro de su TTL) —
//     "lo mete en otro evento" que el cliente no pidió. Con el sello, el «1» no se
//     resuelve contra esa lista (B ya no es el activo) y sigue su camino normal: es el
//     flujo heredado —vivo bajo EventID=""— quien lo interpreta como su propia opción
//     1, sin tocar el puntero de ningún evento.
func TestMenuChoice_NoResuelveContraUnMenuDeUnEventoYaInactivo(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		eventStartRule("menu", "menu"),
		{TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "salir",
			MatchType: trigger.MatchExact, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	// (1) «carrito»: nace A y se queda esperando en su nodo raíz.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "e2-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if st := loadState(t, repo, cid); st.Finished() {
		t.Fatalf("precondición: el cart debe seguir EN CURSO; nodo=%q", st.CurrentNode)
	}

	// (2) «menu»: nace B y arma el menú del despachador, heredando el flow_state de A.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "e2-2")); err != nil {
		t.Fatalf("menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != men.ID {
		t.Fatalf("precondición: el puntero debe ser el del menú (%q), y vale %q", men.ID, st.EventID)
	}

	// (3) «salir»: event_stop desactiva B. El puntero se apaga, pero el menú
	// pendiente (dispatcher_menu) sigue en Vars.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "e2-3")); err != nil {
		t.Fatalf("salir: %v", err)
	}
	st := loadState(t, repo, cid)
	if st.EventID != "" {
		t.Fatalf("precondición: event_stop debe apagar el puntero, y vale %q", st.EventID)
	}
	if _, armado := st.Vars["dispatcher_menu"]; !armado {
		t.Fatal("precondición: el menú del despachador debe SEGUIR en Vars tras event_stop (no lo borra)")
	}

	// (4) «1»: sin evento activo, no puede resolverse contra la lista de B.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "e2-4")); err != nil {
		t.Fatalf("«1»: %v", err)
	}
	final := loadState(t, repo, cid)
	if final.EventID == cart.ID {
		t.Fatalf("E-2: el «1» se resolvió contra el menú RANCIO de %q y reactivó el cart %q sin que el cliente lo pidiera",
			men.ID, cart.ID)
	}
	if final.EventID != "" {
		t.Fatalf("E-2: sin evento activo, el «1» no debe dejar apuntando a nada nuevo; event_id=%q", final.EventID)
	}
}
