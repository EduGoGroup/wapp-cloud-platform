package runtime_test

// orphan_menu_escape_test.go — E6 (Ola 6, decisión de Jhoan: cubrirlo en la misma
// pasada que E5): el QUINTO camino de suelta. Cuando el texto no lo reconoce la lista
// del despachador y st.FlowID=="" (el caso `menu`, D-043.3, sin flujo propio), el
// flow_state se borraba SIN emitir ningún efecto: el evento `menu` quedaba `open` y
// HUÉRFANO, sin una sola fila de telemetría que dijera que se soltó.
//
// Se reutiliza EffectEventEscaped (no un nombre nuevo): el eje que E5 fijó para este
// efecto es «¿el Delete destruyó el flow_state?», no «¿el cliente pronunció la
// palabra exacta de escape?». Aquí también se destruye —es el MISMO store.Delete que
// handleEscape usa— así que un lector de flow_events que pregunte «¿hay progreso de
// nodo que perder?» recibe la misma respuesta con el mismo nombre. Ver el comentario
// en incoming.go (rama st.FlowID=="") para la objeción y por qué se acepta.

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

// TestQuintoCaminoDeSuelta_EmiteEventEscaped (E-6): un texto que no casa NINGUNA
// opción de la lista del despachador y cae sobre un flow_state SIN flujo (el `menu`)
// debe emitir event_escaped para el evento que se suelta, no dejarlo huérfano y mudo.
func TestQuintoCaminoDeSuelta_EmiteEventEscaped(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
			MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC))
	sink := &ecSink{}
	// La lista solo ofrece "9": así "pues no sé" (texto libre) y CUALQUIER dígito
	// real del despachador no interfieren — lo que se mide es el camino SIN opción.
	menu := events.Menu{Options: []events.MenuOption{{Number: 9, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(sink),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}

	// (1) «menu»: nace el evento `menu` (sin flujo, D-043.3) y arma su lista.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "e6-1")); err != nil {
		t.Fatalf("menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")
	if st := loadState(t, repo, cid); st.EventID != men.ID || st.FlowID != "" {
		t.Fatalf("precondición: el `menu` debe quedar activo y SIN flujo; event_id=%q flow=%q", st.EventID, st.FlowID)
	}

	// (2) Un texto libre que la lista NO reconoce: el QUINTO camino de suelta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pues no sé", "e6-2")); err != nil {
		t.Fatalf("texto libre: %v", err)
	}

	// El estado del `menu` se soltó (comportamiento previo, sin cambios): el texto
	// libre cae al fallback del tenant, que arranca SU flujo — un flow_state nuevo,
	// ya no el del `menu` — así que lo que se comprueba es que el puntero YA NO es
	// el evento `menu`, no que la fila desaparezca.
	if st, ok, err := repo.Load(ctx, key); err != nil {
		t.Fatalf("load: %v", err)
	} else if ok && st.EventID == men.ID {
		t.Fatal("el quinto camino debe soltar el puntero del `menu`")
	}
	// Y AHORA además emite event_escaped para el evento que quedó huérfano.
	conEscape, visto := findEffectContext(sink, runtime.EffectEventEscaped)
	if !visto {
		t.Fatalf("E-6: el quinto camino debe emitir %q; efectos vistos=%v", runtime.EffectEventEscaped, effectNames(sink))
	}
	if conEscape.EventID != men.ID {
		t.Fatalf("event_escaped debe llevar el EventID del `menu` que quedó huérfano (%q), llegó %q", men.ID, conEscape.EventID)
	}
	// El evento sigue open y HUÉRFANO (sin puntero): el efecto es informativo, no
	// cierra ni transiciona nada — la misma norma que event_deactivated/event_escaped
	// en los demás caminos.
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("el evento debe seguir open (solo se suelta el puntero, no se cierra), quedó %q", got)
	}
}
