package runtime_test

// flujo_ajeno_terminal_test.go — el MUDO de H2 (#28 / H2, Plan 043 · Ola 6).
//
// La guarda de posesión de H2 (closeIfFinished: un flujo solo cierra el evento que
// POSEE) es correcta y se queda, pero dejaba la fila de flow_state en {terminal, con
// puntero} PARA SIEMPRE: la condición es determinista, se reevalúa idéntica en cada
// entrante, así que `st.EventID == ""` no se cumplía nunca y la rama de suelta del
// #24 no se alcanzaba. Medido por sonda (2026-08-11): tras el turno terminal, tres
// textos normales seguidos produjeron CERO salientes; solo rescataban al contacto el
// menú numerado del despachador, las palabras clave, el escape y la cancelación desde
// la app.
//
// Este test fija la conducta nueva por el camino de producción (HandleIncoming) y
// cuenta los SALIENTES, que es lo que el cliente nota.

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

// mudoH2Env arma el montaje de TestCierreNatural_NoMataElEventoActivoConFlujoAjenoAunEnCurso
// —la regla `menu` con FlowID="" a mano (config REAL de un tenant, D-043.3) y una
// lista del despachador que NO ofrece «1» ni «2», para que esos números lleguen al
// Step del flujo heredado en vez de interceptarlos menuChoice— más una regla de
// FALLBACK: es la que convierte «un texto que no casa nada» en una respuesta, y sin
// ella el turno del texto libre sería mudo por configuración y no por el defecto.
//
// Va aparte del test por gocyclo (min-complexity 15), no por reutilización.
func mudoH2Env(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("precondición: sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
			MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("precondición: insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 9, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	return rt, repo, sender, contacts, evs
}

// TestFlujoAjenoTerminal_NoDejaMudoAlContacto (#28 / H2, Ola 6): cuando un flujo
// AJENO heredado termina bajo un evento que no es suyo, el flow_state terminal se
// suelta y el siguiente texto NORMAL vuelve a tener respuesta.
func TestFlujoAjenoTerminal_NoDejaMudoAlContacto(t *testing.T) {
	rt, repo, sender, contacts, evs := mudoH2Env(t)
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	// (1) «carrito»: el evento nace y su flujo se queda ESPERANDO en el nodo raíz.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "h2mudo-1")); err != nil {
		t.Fatalf("precondición: carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")

	// (2) «menu»: nace el evento `menu` (sin flujo propio) y HEREDA el flow_state del
	// cart, que todavía no ha terminado.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "h2mudo-2")); err != nil {
		t.Fatalf("precondición: menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != men.ID || st.FlowID != testFlow || st.Finished() {
		t.Fatalf("precondición: el estado debe heredar el flujo AJENO en curso bajo el evento del menú; "+
			"event_id=%q (menú=%q) flow=%q finished=%v", st.EventID, men.ID, st.FlowID, st.Finished())
	}

	// (3) «1»: no está en la lista del despachador, así que cae al Step del flujo
	// heredado y lo TERMINA (sampleFlow: root → ventas, sin salida). El evento activo
	// sigue siendo el `menu`, que no es su dueño: la guarda de H2 no lo cierra.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "h2mudo-3")); err != nil {
		t.Fatalf("precondición: completar el flujo ajeno: %v", err)
	}
	if st := loadState(t, repo, cid); !st.Finished() || st.EventID != men.ID {
		t.Fatalf("precondición: el turno terminal debe dejar el estado TERMINAL con el puntero del menú intacto "+
			"(la guarda de H2 no cierra un evento ajeno); nodo=%q event_id=%q", st.CurrentNode, st.EventID)
	}

	// (4) LA PROPIEDAD: un texto NORMAL después de eso tiene respuesta.
	antes := len(sender.texts())
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "necesito ayuda", "h2mudo-4")); err != nil {
		t.Fatalf("texto libre tras el turno terminal: %v", err)
	}
	dichos := sender.texts()[antes:]
	if len(dichos) == 0 {
		st := loadState(t, repo, cid)
		t.Fatalf("MUDO (#28 / H2): un texto normal tras terminar un flujo AJENO no produjo ni un saliente; "+
			"flow_state quedó {nodo=%q, flow=%q, event_id=%q} y el contacto no puede salir de ahí con palabras",
			st.CurrentNode, st.FlowID, st.EventID)
	}

	// Lo que se suelta es la FILA, no el evento: el `menu` sigue vivo y su muerte
	// sigue siendo de la cancelación desde la app (D-043.5).
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("el evento `menu` debe seguir open tras soltar el flow_state, y quedó %q", got)
	}
	if got := evs.statuses()[cart.ID]; got != events.StatusOpen {
		t.Fatalf("y el `cart` tampoco se toca por aquí: quedó %q", got)
	}
	// Y la conversación quedó REUTILIZABLE: el fallback arrancó su flujo desde cero
	// sobre una fila nueva, no sobre el cadáver terminal.
	if st := loadState(t, repo, cid); st.Finished() {
		t.Fatalf("tras soltar, el estado debe ser el del flujo recién arrancado por el fallback y no el cadáver; "+
			"nodo=%q event_id=%q", st.CurrentNode, st.EventID)
	}
}
