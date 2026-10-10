//go:build pendiente

package runtime_test

// events_clock_test.go prueba EL reloj de conversación aplicado al entrante que llega con un
// evento ACTIVO (events.go, EV-6) y quién decide cada vencimiento (RT-15, T61): el reloj del
// almacén de eventos gobierna el evento; el del runtime, solo el TTL del limbo.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// eventsFaultyRepo es el almacén de flujos del guion con la lectura de ajustes y el guardado
// del estado rompibles.
type eventsFaultyRepo struct {
	*store.MemoryRepository
	mu          sync.Mutex
	settingsErr error
	saveErr     error
}

func (r *eventsFaultyRepo) failSave(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveErr = err
}

func (r *eventsFaultyRepo) Save(ctx context.Context, state model.Conversation) error {
	r.mu.Lock()
	err := r.saveErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.MemoryRepository.Save(ctx, state)
}

func (r *eventsFaultyRepo) failSettings(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settingsErr = err
}

func (r *eventsFaultyRepo) GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error) {
	r.mu.Lock()
	err := r.settingsErr
	r.mu.Unlock()
	if err != nil {
		return store.TenantSettings{}, err
	}
	return r.MemoryRepository.GetTenantSettings(ctx, tenantID)
}

// eventsFaultyRepoHarness es el guion del carrito con el Runtime reconstruido sobre un
// eventsFaultyRepo: las mismas piezas y opciones del arnés, otro almacén de flujos.
func eventsFaultyRepoHarness(t *testing.T) (*harness, *eventsFaultyRepo) {
	t.Helper()
	h := eventsCartHarness(t)
	repo := &eventsFaultyRepo{MemoryRepository: h.repo}
	h.rt = runtime.New(repo, h.engine, h.sender, h.tenants, h.contacts, h.log, h.runtimeOptions()...)
	return h, repo
}

// eventsWithRuntimeClock le da al RUNTIME un reloj fijo distinto del del guion, que sigue
// siendo el del almacén de eventos y el del almacén de flujos.
func eventsWithRuntimeClock(at time.Time) harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithClock(func() time.Time { return at })}
	})
}

// TestEvents_ClockWithinTheWindowTouchesAndNeverExpires (EV-6, INV-18): dentro de la ventana
// cada entrante hace Touch, así que un chat que dura más que la ventana —y mucho más que el
// TTL conversacional— no vence nunca: el plazo se mide contra la ÚLTIMA interacción.
func TestEvents_ClockWithinTheWindowTouchesAndNeverExpires(t *testing.T) {
	h := eventsCartHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)

	for i := range 3 {
		h.clock.Advance(eventsWindow - 10*time.Minute)
		h.say(fmt.Sprintf("wa-live-%d", i), "sigo aquí-zzq")

		if row := eventsRow(t, h, ev.ID); !row.LastActivityAt.Equal(h.clock.Now()) {
			t.Fatalf("entrante %d: last_activity_at = %v, quería el Touch en %v", i, row.LastActivityAt, h.clock.Now())
		}
		if st, found := h.state(); !found || st.EventID != ev.ID {
			t.Fatalf("entrante %d: estado = (%+v, %v), con evento activo el TTL conversacional no descarta", i, st, found)
		}
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 0)
	if streaks := h.closedStreaks(); len(streaks) != 0 {
		t.Errorf("rachas cerradas = %v, nadie borró el estado", streaks)
	}
}

// TestEvents_ClockExpiredReleasesTheConversationNotTheEvent (EV-6, EV-10): vencida la
// ventana se BORRA el estado (y se cierra su racha), se emite event_inactivity_expired y el
// evento NO se toca: sigue open, sin sellar y sin refrescar. Callarse no es abandonar: ni
// resumen ni solicitud abandonada.
func TestEvents_ClockExpiredReleasesTheConversationNotTheEvent(t *testing.T) {
	h := eventsCartHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.clock.Advance(eventsWindow + time.Minute)

	h.say("wa-2", "vuelvo tarde-zzq")

	if st, found := h.state(); found {
		t.Errorf("estado = %+v, vencida la ventana la conversación se suelta", st)
	}
	row := eventsRow(t, h, ev.ID)
	if row.Status != events.StatusOpen || !row.ClosedAt.IsZero() || !row.LastActivityAt.Equal(harnessStart) {
		t.Errorf("evento = %+v, quería open, sin closed_at y con el last_activity_at de antes (%v)", row, harnessStart)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 1)[0], ev, 0)
	if streaks := h.closedStreaks(); len(streaks) != 1 {
		t.Errorf("rachas cerradas = %v, quería la del estado que se soltó", streaks)
	}
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, el vencimiento no abandona ninguna solicitud", calls)
	}
	if entries := h.thread(ev.ID); len(entries) != 0 {
		t.Errorf("hilo = %+v, el vencimiento no escribe resumen", entries)
	}
	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, un texto que no dispara nada no recibe respuesta", texts)
	}
}

// TestEvents_AfterExpiryTheIncomingIsANewConversation (EV-6, EV-3 camino 2): el entrante que
// llega vencido se trata como NUEVO, por el resolver de disparos. Con la palabra de su tipo
// vuelve al MISMO evento —«ve» conmuta siempre, vencido o no— y no nace otro.
func TestEvents_AfterExpiryTheIncomingIsANewConversation(t *testing.T) {
	h := eventsCartHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.say("wa-2", "esto no es una opción") // deja Vars que NO deben sobrevivir
	h.clock.Advance(eventsWindow + time.Minute)

	h.say("wa-3", eventsCartWord)

	if rows := h.events.Events(harnessTenant); len(rows) != 1 || rows[0].ID != ev.ID || rows[0].Status != events.StatusOpen {
		t.Fatalf("eventos = %+v, quería el mismo evento, todavía open", rows)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 1)
	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 1)
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	st, found := h.state()
	if !found || st.EventID != ev.ID || st.CurrentNode != "root" || len(st.Vars) != 0 {
		t.Errorf("estado = (%+v, %v), quería una conversación NUEVA dentro del mismo evento", st, found)
	}
	texts := h.texts()
	if len(texts) != 3 || texts[2] != texts[0] {
		t.Errorf("textos = %q, quería de nuevo la pantalla inicial del flujo", texts)
	}
}

// TestEvents_TheEventStoreClockDecidesExpiry (RT-15, T61): quién decide «vencido», y con qué
// instante se sella last_activity_at, es el reloj del almacén de eventos, no el del runtime.
func TestEvents_TheEventStoreClockDecidesExpiry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		runtimeNow  time.Time
		storeMoves  time.Duration
		wantExpired int
	}{
		{name: "store clock past the window, runtime clock frozen", runtimeNow: harnessStart, storeMoves: eventsWindow + time.Minute, wantExpired: 1},
		{name: "runtime clock hours ahead, store clock within the window", runtimeNow: harnessStart.Add(10 * time.Hour), storeMoves: time.Minute, wantExpired: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := eventsCartHarness(t, eventsWithRuntimeClock(tc.runtimeNow))
			h.say("wa-1", eventsCartWord)
			ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
			h.clock.Advance(tc.storeMoves)

			h.say("wa-2", "sigo aquí-zzq")

			eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, tc.wantExpired)
			_, found := h.state()
			if found == (tc.wantExpired == 1) {
				t.Errorf("estado presente = %v con %d vencimientos esperados", found, tc.wantExpired)
			}
			want := harnessStart
			if tc.wantExpired == 0 {
				want = h.clock.Now()
			}
			if row := eventsRow(t, h, ev.ID); !row.LastActivityAt.Equal(want) {
				t.Errorf("last_activity_at = %v, quería %v: lo sella el reloj del almacén de eventos", row.LastActivityAt, want)
			}
		})
	}
}

// TestEvents_TheRuntimeClockDecidesTheLimboTTL (RT-15, INV-18): SIN evento activo manda el
// TTL conversacional, medido con el reloj del RUNTIME contra Conversation.UpdatedAt.
func TestEvents_TheRuntimeClockDecidesTheLimboTTL(t *testing.T) {
	h := eventsCartHarness(t, eventsWithRuntimeClock(harnessStart.Add(eventsLimboTTL+time.Minute)))
	h.seedFlow(menuFlow("plain-flow-zzq"))
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "plain-flow-zzq"})
	h.say("wa-1", "hola")
	if st, found := h.state(); !found || !st.UpdatedAt.Equal(harnessStart) {
		t.Fatalf("estado = (%+v, %v), quería la conversación guardada en %v", st, found, harnessStart)
	}

	h.say("wa-2", "1")

	if st, found := h.state(); found {
		t.Errorf("estado = %+v: para el reloj del runtime el TTL del limbo ya venció y la conversación se suelta", st)
	}
	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, el «1» no debía avanzar un flujo vencido", texts)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 0)
}

// TestEvents_ClockIgnoresAnActiveEventThatIsNoLongerAlive (EV-6): un evento activo que ya no
// está vivo (lo cerró la app del dueño) ni vence ni refresca; la conversación avanza.
func TestEvents_ClockIgnoresAnActiveEventThatIsNoLongerAlive(t *testing.T) {
	h := eventsCartHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if err := h.events.TransitionEvent(t.Context(), ev.ID, events.StatusClosed); err != nil {
		t.Fatalf("cerrar el evento por fuera: %v", err)
	}
	h.clock.Advance(eventsWindow + time.Minute)

	h.say("wa-2", "sigo aquí-zzq")

	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 0)
	if _, found := h.state(); !found {
		t.Error("el estado se soltó por el reloj de un evento que ya no gobierna el entrante")
	}
	if row := eventsRow(t, h, ev.ID); !row.LastActivityAt.Equal(harnessStart) {
		t.Errorf("last_activity_at = %v, un evento muerto no se refresca (quería %v)", row.LastActivityAt, harnessStart)
	}
	const notAlive = "runtime: el evento activo ya no está vivo; su reloj no gobierna este entrante"
	if !eventsLogged(h, "debug", notAlive) {
		t.Errorf("no se anunció a Debug que el evento activo ya no está vivo\nlog:\n%s", h.log.dump())
	}
	if texts := h.texts(); len(texts) != 2 {
		t.Errorf("textos = %q, la conversación debía avanzar y contestar", texts)
	}
}

// TestEvents_UnreadableTTLRisesInsteadOfExpiring (EV-6): un fallo al leer el TTL de
// inactividad NO se traga. Aquí el valor decide si se suelta la conversación de un cliente:
// soltarla por un timeout de lectura sería destruir su sitio en el pedido.
func TestEvents_UnreadableTTLRisesInsteadOfExpiring(t *testing.T) {
	h, repo := eventsFaultyRepoHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.clock.Advance(eventsWindow + time.Minute)
	repo.failSettings(errEventsInjected)

	err := h.handle(h.incoming("wa-2", "sigo aquí-zzq"))

	if !errors.Is(err, errEventsInjected) {
		t.Fatalf("HandleIncoming = %v, quería que subiera el fallo de leer el TTL", err)
	}
	if st, found := h.state(); !found || st.EventID != ev.ID {
		t.Errorf("estado = (%+v, %v), sin TTL legible no se suelta nada", st, found)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 0)
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento quedó %q, quería open", row.Status)
	}
}
