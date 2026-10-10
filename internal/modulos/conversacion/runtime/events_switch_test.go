//go:build pendiente

package runtime_test

// events_switch_test.go prueba, por HandleIncoming, el SALTO POR TIPO con el gesto «ve»
// (EV-3, caminos 1, 2 y 4), los dos punteros del estado al entrar a un evento (EV-4, RT-16)
// y event_stop (EV-5). El camino 3 (E-11) va en events_start_new_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Los dos avisos de event_stop, byte a byte (EV-5).
const (
	eventsStopNoticeCart    = "Listo, dejamos el pedido por ahora. Sigue abierto: puedes retomarlo cuando quieras."
	eventsStopNoticeGeneric = "Listo, lo dejamos aquí. Sigue abierto por si quieres retomarlo."
)

// eventsSwitchHarness es el guion con dos tipos (cart y survey) y la palabra de event_stop.
func eventsSwitchHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := eventsCartHarness(t, opts...)
	eventsAddSurvey(h)
	h.seedRule(trigger.Rule{Kind: trigger.KindEventStop, Keyword: eventsStopWord, MatchType: trigger.MatchExact})
	return h
}

// TestEvents_StartOverTheActiveEventIsANoOp (EV-3, camino 1): decir la palabra del tipo que
// YA está activo no crea fila, no conmuta, no cobra token propio y NO consume el turno: el
// texto sigue hacia el módulo.
func TestEvents_StartOverTheActiveEventIsANoOp(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)

	h.say("wa-2", eventsCartWord)

	if rows := h.events.Events(harnessTenant); len(rows) != 1 || rows[0].ID != ev.ID {
		t.Fatalf("eventos = %+v, dos veces la palabra dejan UNA sola fila", rows)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	const noOp = "runtime: event_start sobre el evento que ya estaba activo; no-op"
	if !eventsLogged(h, "debug", noOp) {
		t.Errorf("no se anunció el no-op a Debug\nlog:\n%s", h.log.dump())
	}
	// El turno NO se consumió: el módulo recibió «carrito» —que no es una opción suya— y
	// contestó. Son dos tokens en total (el nacimiento y esa respuesta), no tres.
	if texts := h.texts(); len(texts) != 2 || !strings.HasSuffix(texts[1], texts[0]) || texts[1] == texts[0] {
		t.Errorf("textos = %q, quería la pantalla del arranque y la respuesta del módulo a un texto que no es opción", texts)
	}
	if calls := h.limiter.Calls(); len(calls) != 2 {
		t.Errorf("tokens pedidos = %d, quería 2: el no-op no cobra el suyo", len(calls))
	}
	if st, _ := h.state(); st.EventID != ev.ID || st.OwnerEventID != ev.ID || st.CurrentNode != "root" {
		t.Errorf("estado = %+v, quería la conversación en su nodo y dentro del mismo evento", st)
	}
}

// eventsRequireAllOpen exige `want` filas de evento, todas open y sin sellar.
func eventsRequireAllOpen(t *testing.T, h *harness, want int) {
	t.Helper()
	rows := h.events.Events(harnessTenant)
	if len(rows) != want {
		t.Fatalf("eventos = %+v, quería %d", rows, want)
	}
	for _, row := range rows {
		if row.Status != events.StatusOpen || !row.ClosedAt.IsZero() {
			t.Errorf("el evento %s (%s) quedó %q con closed_at %v: un salto por tipo no toca ningún status", row.ID, row.Kind, row.Status, row.ClosedAt)
		}
	}
}

// TestEvents_SwitchByKindTouchesNoStatus (EV-3, caminos 4 y 2; EV-4; D-043.4): «carrito» →
// «encuesta» → «carrito» deja DOS filas vivas, el puntero en la última pedida y ningún status
// cambiado. Volver CONMUTA: Touch del evento, event_switched y re-entrada a SU flujo
// congelado. Cada salto destruye el estado previo y cierra su racha (EV-10).
func TestEvents_SwitchByKindTouchesNoStatus(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.say("wa-1", eventsCartWord)
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)

	h.clock.Advance(2 * time.Minute)
	h.say("wa-2", eventsSurveyWord)
	surveyEv := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
	if st, _ := h.state(); st.EventID != surveyEv.ID || st.OwnerEventID != surveyEv.ID || st.FlowID != eventsSurveyFlow {
		t.Fatalf("tras saltar a la encuesta el estado es %+v, quería su evento como activo y dueño de su flujo", st)
	}
	if row := eventsRow(t, h, cart.ID); row.Status != events.StatusOpen {
		t.Fatalf("el carrito que dejó de ser activo quedó %q, quería open", row.Status)
	}

	h.clock.Advance(2 * time.Minute)
	h.say("wa-3", eventsCartWord)

	eventsRequireAllOpen(t, h, 2)
	if back := eventsRow(t, h, cart.ID); !back.LastActivityAt.Equal(h.clock.Now()) {
		t.Errorf("last_activity_at del carrito = %v, la conmuta lo refresca a %v", back.LastActivityAt, h.clock.Now())
	}
	st, _ := h.state()
	if st.EventID != cart.ID || st.OwnerEventID != cart.ID || st.FlowID != eventsCartFlow || st.CurrentNode != "root" {
		t.Errorf("estado = %+v, quería la re-entrada al flujo del carrito, con él como activo y dueño", st)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 2)
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 1)[0], cart, 0)
	if texts := h.texts(); len(texts) != 3 {
		t.Errorf("textos = %q, quería una pantalla por cada entrada a un evento", texts)
	}
	if streaks := h.closedStreaks(); len(streaks) != 2 {
		t.Errorf("rachas cerradas = %v, quería una por cada estado destruido al saltar", streaks)
	}
}

// TestEvents_SwitchAndBirthChargeOneTokenBeforeWriting (EV-3, RT-8): nacer y conmutar cobran
// UN token antes de escribir nada; agotado, el turno se consume sin tocar la base.
func TestEvents_SwitchAndBirthChargeOneTokenBeforeWriting(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.limiter.Limit(2)
	h.say("wa-1", eventsCartWord)
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.clock.Advance(time.Minute)
	h.say("wa-2", eventsSurveyWord)
	surveyEv := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
	h.clock.Advance(time.Minute)

	h.say("wa-3", eventsCartWord) // la conmuta, ya sin cupo

	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	// El último Touch del carrito fue el del entrante «encuesta», cuando aún era el activo.
	if row, want := eventsRow(t, h, cart.ID), harnessStart.Add(time.Minute); !row.LastActivityAt.Equal(want) {
		t.Errorf("last_activity_at del carrito = %v, sin cupo la conmuta no toca el evento (quería %v)", row.LastActivityAt, want)
	}
	if st, _ := h.state(); st.EventID != surveyEv.ID || st.FlowID != eventsSurveyFlow {
		t.Errorf("estado = %+v, sin cupo la conversación sigue en la encuesta", st)
	}
	if texts := h.texts(); len(texts) != 2 {
		t.Errorf("textos = %q, quería solo las dos pantallas con cupo", texts)
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
}

// TestEvents_BirthWithoutQuotaCreatesNoRow (EV-3, RT-8): parir un evento al que no se va a
// poder contestar es peor que no parirlo.
func TestEvents_BirthWithoutQuotaCreatesNoRow(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.limiter.Limit(0)

	h.say("wa-1", eventsCartWord)

	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, sin cupo no nace ninguno", rows)
	}
	if _, found := h.state(); found || len(h.texts()) != 0 {
		t.Errorf("sin cupo quedó estado (%v) o se envió algo (%q)", found, h.texts())
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
}

// TestEvents_LosingTheBirthRaceIsBenign (EV-3): perder la carrera del INSERT contra otro
// entrante (events.ErrAliveExists) es Info y nil: ni efecto, ni respuesta, ni error.
func TestEvents_LosingTheBirthRaceIsBenign(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsSwitchHarness(t, eventsWithFaults(faults))
	faults.onCreate(func(context.Context, events.NewEvent) (events.Event, error) {
		return events.Event{}, events.ErrAliveExists
	})

	h.say("wa-1", eventsCartWord)

	const benign = "runtime: el evento ya existía al crearlo (carrera benigna)"
	if !eventsLogged(h, "info", benign) {
		t.Errorf("no se anunció la carrera benigna a Info\nlog:\n%s", h.log.dump())
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 0)
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, quien pierde la carrera no contesta", texts)
	}
}

// TestEvents_EnteringAnEventWithoutFlowNeverStampsTheOwner (EV-4, RT-16): el ACTIVO se
// estampa siempre que se entra a un evento; el DUEÑO, solo si ese camino acaba de borrar el
// estado y arrancar el flujo PARA ese evento. Un evento sin flujo sobre un estado heredado, y
// el menú montado sobre un flujo vivo, dejan al dueño como estaba.
func TestEvents_EnteringAnEventWithoutFlowNeverStampsTheOwner(t *testing.T) {
	for _, tc := range []struct{ name, word, kind string }{
		{name: "an event of a kind without flow", word: "documentos", kind: trigger.EventKindMedia},
		{name: "the dispatcher menu", word: eventsMenuWord, kind: trigger.EventKindMenu},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := eventsSwitchHarness(t)
			h.seedRule(eventsStartRule(tc.word, tc.kind, ""))
			h.say("wa-1", eventsCartWord)
			cart := eventsAliveOfKind(t, h, trigger.EventKindCart)

			h.say("wa-2", tc.word)

			active := eventsAliveOfKind(t, h, tc.kind)
			st, found := h.state()
			if !found || st.EventID != active.ID {
				t.Fatalf("estado = (%+v, %v), quería el evento %s (%s) como ACTIVO", st, found, active.ID, tc.kind)
			}
			if st.OwnerEventID != cart.ID || st.FlowID != eventsCartFlow || st.CurrentNode != "root" {
				t.Errorf("estado = %+v, quería el flujo heredado del carrito con el carrito %s como DUEÑO", st, cart.ID)
			}
			if row := eventsRow(t, h, cart.ID); row.Status != events.StatusOpen {
				t.Errorf("el carrito quedó %q, quería open", row.Status)
			}
		})
	}
}

// TestEvents_StopDeactivatesWithoutKilling (EV-5): event_stop apaga el activo, conserva el
// dueño, el flujo y las Vars, no transiciona nada, emite event_deactivated y confirma por
// NOMBRE DE TIPO, nunca por history_id.
func TestEvents_StopDeactivatesWithoutKilling(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.say("wa-1", eventsCartWord)
	h.say("wa-2", "esto no es una opción") // deja un contador en Vars
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	before, _ := h.state()

	h.say("wa-3", eventsStopWord)

	st, found := h.state()
	if !found || st.EventID != "" {
		t.Fatalf("estado = (%+v, %v), quería el puntero ACTIVO apagado y la fila conservada", st, found)
	}
	if st.OwnerEventID != ev.ID || st.FlowID != before.FlowID || st.CurrentNode != before.CurrentNode || len(st.Vars) != len(before.Vars) || len(st.Vars) == 0 {
		t.Errorf("estado = %+v, quería el dueño, el flujo, el nodo y las Vars de antes: %+v", st, before)
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen || !row.ClosedAt.IsZero() {
		t.Errorf("el evento quedó %q, event_stop no transiciona nada", row.Status)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventDeactivated, 1)[0], ev, 0)
	texts := h.texts()
	if got := texts[len(texts)-1]; got != eventsStopNoticeCart {
		t.Errorf("confirmación = %q, quería %q", got, eventsStopNoticeCart)
	}
	if strings.Contains(texts[len(texts)-1], ev.HistoryID) {
		t.Errorf("la confirmación nombra el history_id: %q", texts[len(texts)-1])
	}
}

// TestEvents_StopWithoutQuotaStillDeactivates (EV-5, RT-8): el Save y el efecto van ANTES del
// token. Sin cupo no se avisa, pero la desactivación ya ocurrió.
func TestEvents_StopWithoutQuotaStillDeactivates(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.limiter.Limit(1)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)

	h.say("wa-2", eventsStopWord)

	if st, _ := h.state(); st.EventID != "" || st.OwnerEventID != ev.ID {
		t.Errorf("estado = %+v, quería el activo apagado aunque no haya cupo para avisar", st)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventDeactivated, 1)
	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, sin cupo no sale la confirmación", texts)
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
}

// TestEvents_StopOverAnUnknownEventUsesTheGenericNotice (EV-5): si el activo ya no está vivo
// (lo cerró la app del dueño y el puntero quedó colgando), se confirma sin nombre —antes que
// mentir con uno— y no hay efecto que emitir.
func TestEvents_StopOverAnUnknownEventUsesTheGenericNotice(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if err := h.events.TransitionEvent(t.Context(), ev.ID, events.StatusClosed); err != nil {
		t.Fatalf("cerrar el evento por fuera: %v", err)
	}

	h.say("wa-2", eventsStopWord)

	if st, _ := h.state(); st.EventID != "" {
		t.Errorf("estado = %+v, quería el puntero activo apagado", st)
	}
	texts := h.texts()
	if got := texts[len(texts)-1]; got != eventsStopNoticeGeneric {
		t.Errorf("confirmación = %q, quería la genérica %q", got, eventsStopNoticeGeneric)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventDeactivated, 0)
}

// TestEvents_StopWithoutActiveEventConsumesTheTurnSilently (EV-5): sin evento activo no hay
// nada que desactivar ni que confirmar; la palabra se consume y el módulo no la ve.
func TestEvents_StopWithoutActiveEventConsumesTheTurnSilently(t *testing.T) {
	h := eventsSwitchHarness(t)
	h.seedFlow(menuFlow("plain-flow-zzq"))
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "plain-flow-zzq"})
	h.say("wa-1", "hola")
	before, _ := h.state()

	h.say("wa-2", eventsStopWord)

	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, sin evento que desactivar no se dice nada", texts)
	}
	st, found := h.state()
	if !found || st.CurrentNode != before.CurrentNode || len(st.Vars) != len(before.Vars) || st.LastWaMessageID != before.LastWaMessageID {
		t.Errorf("estado = (%+v, %v), quería la conversación intacta: %+v", st, found, before)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventDeactivated, 0)
}
