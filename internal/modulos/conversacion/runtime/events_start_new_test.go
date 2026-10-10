package runtime_test

// events_start_new_test.go prueba StartNewOfKind (events.go), la TERCERA puerta del
// nacimiento tardío y la única con el gesto «nuevo»: los casos de su comentario, uno por
// función. Aquí vive E-11 (EV-3, camino 3): solo un vivo VENCIDO cede su sitio.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// eventsStartNew llama a StartNewOfKind por el tipo `cart` y su flujo, sin evento activo.
func eventsStartNew(t *testing.T, h *harness) (bool, error) {
	t.Helper()
	return h.rt.StartNewOfKind(t.Context(), h.key(), harnessSession, trigger.EventKindCart, eventsCartFlow, "")
}

// eventsSeedStaleCart siembra un `cart` vivo y deja pasar MÁS que la ventana: vencido.
func eventsSeedStaleCart(t *testing.T, h *harness) events.Event {
	t.Helper()
	old := h.seedEvent(trigger.EventKindCart, eventsCartFlow, 1)
	h.clock.Advance(eventsWindow + time.Minute)
	return old
}

// TestStartNewOfKind_EmptyKindTouchesNothing: con kind vacío devuelve (false, nil) sin crear,
// escribir ni enviar nada.
func TestStartNewOfKind_EmptyKindTouchesNothing(t *testing.T) {
	h := eventsCartHarness(t)

	consumed, err := h.rt.StartNewOfKind(t.Context(), h.key(), harnessSession, "", eventsCartFlow, "")

	if consumed || err != nil {
		t.Errorf("StartNewOfKind con kind vacío = (%v, %v), quería (false, nil)", consumed, err)
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, no debía nacer ninguno", rows)
	}
	if _, found := h.state(); found || len(h.texts()) != 0 || len(h.limiter.Calls()) != 0 {
		t.Errorf("con kind vacío hay estado (%v), textos (%q) o tokens cobrados (%d)", found, h.texts(), len(h.limiter.Calls()))
	}
}

// TestStartNewOfKind_NoneAliveIsBornAndStartsItsFlow: sin ninguno vivo, NACE (event_started),
// arranca flowID y el evento queda como activo y dueño. Lo que la dispara no entra ni en el
// hilo del evento, ni siquiera con llm_intake.
func TestStartNewOfKind_NoneAliveIsBornAndStartsItsFlow(t *testing.T) {
	h := eventsCartHarness(t)
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.enableFeature(entitlements.FeatureCartBasic)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind = (%v, %v), quería (true, nil)\nlog:\n%s", consumed, err, h.log.dump())
	}
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if ev.FlowID != eventsCartFlow || ev.FlowVersion != 1 {
		t.Errorf("el evento nació con %q v%d, quería %q v1", ev.FlowID, ev.FlowVersion, eventsCartFlow)
	}
	st, found := h.state()
	if !found || st.EventID != ev.ID || st.OwnerEventID != ev.ID || st.FlowID != eventsCartFlow || st.CurrentNode != "root" {
		t.Errorf("estado = (%+v, %v), quería el flujo arrancado para el evento %s", st, found, ev.ID)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería la pantalla inicial del flujo", texts)
	}
	if entries := h.thread(ev.ID); len(entries) != 0 {
		t.Errorf("hilo = %+v, la elección de una lista de la plataforma no abre el hilo", entries)
	}
}

// TestStartNewOfKind_WithoutFlowIsBornWithoutOwner: flowID "" = sin flujo. El evento nace,
// no se arranca nada y, sin estado donde apuntar, no se inventa ninguno.
func TestStartNewOfKind_WithoutFlowIsBornWithoutOwner(t *testing.T) {
	h := eventsCartHarness(t)

	consumed, err := h.rt.StartNewOfKind(t.Context(), h.key(), harnessSession, trigger.EventKindSurvey, "", "")

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind sin flujo = (%v, %v), quería (true, nil)", consumed, err)
	}
	if ev := eventsAliveOfKind(t, h, trigger.EventKindSurvey); ev.FlowID != "" || ev.FlowVersion != 0 {
		t.Errorf("el evento nació con %q v%d, quería sin flujo y versión 0", ev.FlowID, ev.FlowVersion)
	}
	if st, found := h.state(); found {
		t.Errorf("estado = %+v, sin flujo que arrancar no debía nacer ninguno", st)
	}
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, sin flujo no hay nada que decir", texts)
	}
}

// TestStartNewOfKind_AliveWithinItsWindowIsSwitchedNotClosed (E-11.3): «nuevo» no significa
// «cierra lo que haya». Un vivo DENTRO de su ventana se conmuta: nadie pierde un pedido en
// curso por tocar una opción.
func TestStartNewOfKind_AliveWithinItsWindowIsSwitchedNotClosed(t *testing.T) {
	h := eventsCartHarness(t)
	old := h.seedEvent(trigger.EventKindCart, eventsCartFlow, 1)
	h.clock.Advance(eventsWindow - time.Minute)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind = (%v, %v), quería (true, nil)", consumed, err)
	}
	rows := h.events.Events(harnessTenant)
	if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Status != events.StatusOpen {
		t.Fatalf("eventos = %+v, quería el mismo y único evento, todavía open", rows)
	}
	if !rows[0].LastActivityAt.Equal(h.clock.Now()) {
		t.Errorf("last_activity_at = %v, la conmuta refresca el reloj del evento a %v", rows[0].LastActivityAt, h.clock.Now())
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 1)[0], old, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 0)
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, una conmuta no abandona ninguna solicitud", calls)
	}
	if st, found := h.state(); !found || st.EventID != old.ID || st.OwnerEventID != old.ID {
		t.Errorf("estado = (%+v, %v), quería la conversación dentro del evento que ya existía", st, found)
	}
}

// TestStartNewOfKind_StaleIsCancelledAbandonedAndReplaced (E-11): un vivo VENCIDO se cancela
// (event_cancelled), su solicitud se abandona y nace uno nuevo (event_started).
func TestStartNewOfKind_StaleIsCancelledAbandonedAndReplaced(t *testing.T) {
	h := eventsCartHarness(t)
	old := eventsSeedStaleCart(t, h)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind = (%v, %v), quería (true, nil)\nlog:\n%s", consumed, err, h.log.dump())
	}
	retired := eventsRow(t, h, old.ID)
	if retired.Status != events.StatusCancelled || !retired.ClosedAt.Equal(h.clock.Now()) {
		t.Errorf("el vencido quedó %q con closed_at %v, quería cancelled sellado en %v", retired.Status, retired.ClosedAt, h.clock.Now())
	}
	if calls := h.abandoner.calls(); len(calls) != 1 || calls[0] != old.ID {
		t.Errorf("abandonos = %v, quería uno, por el evento retirado %s", calls, old.ID)
	}
	born := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if born.ID == old.ID {
		t.Fatal("el evento vivo es el viejo: no nació uno nuevo")
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)[0], old, 0)
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)[0], born, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	if st, found := h.state(); !found || st.EventID != born.ID || st.OwnerEventID != born.ID {
		t.Errorf("estado = (%+v, %v), quería la conversación dentro del evento nuevo", st, found)
	}
}

// TestStartNewOfKind_LosingTheCancelRaceStillGivesBirth: si la cancelación del vencido
// pierde la carrera (events.ErrNotOpen), el tipo quedó libre igual y se sigue: nace el nuevo.
// No se emite event_cancelled ni se abandona nada: esa muerte no fue de este escritor.
func TestStartNewOfKind_LosingTheCancelRaceStillGivesBirth(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	old := eventsSeedStaleCart(t, h)
	faults.loseTransitionRace(events.StatusClosed)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind = (%v, %v), quería (true, nil): perder la carrera es benigno", consumed, err)
	}
	if row := eventsRow(t, h, old.ID); row.Status != events.StatusClosed {
		t.Errorf("el vencido quedó %q, quería el closed del escritor que ganó", row.Status)
	}
	if born := eventsAliveOfKind(t, h, trigger.EventKindCart); born.ID == old.ID {
		t.Error("no nació un evento nuevo tras la carrera perdida")
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, quien pierde la carrera no abandona la solicitud", calls)
	}
}

// TestStartNewOfKind_FailedAbandonRisesWithTheEventAlreadyCancelled: si el abandono de la
// solicitud falla, el error SUBE con el evento ya cancelado (costura conocida de E-8 §4).
func TestStartNewOfKind_FailedAbandonRisesWithTheEventAlreadyCancelled(t *testing.T) {
	h := eventsCartHarness(t)
	old := eventsSeedStaleCart(t, h)
	h.abandoner.fail(errEventsInjected)

	_, err := eventsStartNew(t, h)

	if !errors.Is(err, errEventsInjected) {
		t.Fatalf("StartNewOfKind = %v, quería el error del abandono", err)
	}
	if row := eventsRow(t, h, old.ID); row.Status != events.StatusCancelled {
		t.Errorf("el vencido quedó %q, quería cancelled: la transición ya estaba sellada", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)
}

// TestStartNewOfKind_WithoutAbandonerCancelsAndWarns: sin IntakeAbandoner cableado el evento
// se cancela igual, su solicitud NO se abandona y se avisa a WARN, byte a byte.
func TestStartNewOfKind_WithoutAbandonerCancelsAndWarns(t *testing.T) {
	h := eventsCartHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithIntakeAbandoner(nil)}
	}))
	old := eventsSeedStaleCart(t, h)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind sin abandonador = (%v, %v), quería (true, nil)", consumed, err)
	}
	if row := eventsRow(t, h, old.ID); row.Status != events.StatusCancelled {
		t.Errorf("el vencido quedó %q, quería cancelled", row.Status)
	}
	const warning = "runtime: evento cancelado sin IntakeAbandoner cableado; si tenía solicitud, quedó sin abandonar"
	if !eventsLogged(h, "warn", warning) {
		t.Errorf("no se avisó a WARN de la solicitud sin abandonar\nlog:\n%s", h.log.dump())
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
}

// TestStartNewOfKind_ExhaustedQuotaConsumesTheTurnWithoutWriting (RT-8): sin cupo del
// limitador devuelve (true, nil) sin crear ni enviar nada, y cuenta `rate_limit`.
func TestStartNewOfKind_ExhaustedQuotaConsumesTheTurnWithoutWriting(t *testing.T) {
	h := eventsCartHarness(t)
	old := eventsSeedStaleCart(t, h)
	h.limiter.Limit(0)

	consumed, err := eventsStartNew(t, h)

	if !consumed || err != nil {
		t.Fatalf("StartNewOfKind sin cupo = (%v, %v), quería (true, nil)", consumed, err)
	}
	rows := h.events.Events(harnessTenant)
	if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Status != events.StatusOpen {
		t.Errorf("eventos = %+v, sin cupo no se cancela ni nace nada", rows)
	}
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, sin cupo no se envía nada", texts)
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 || calls[0] != (runtimehelpertest.LimiterCall{Key: h.key().String(), Allowed: false}) {
		t.Errorf("tokens pedidos = %+v, quería UNO, denegado, por la clave de la conversación", calls)
	}
}

// eventsBrokenFlows es un FlowForKind que no puede resolver el flujo de ningún tipo.
type eventsBrokenFlows struct{}

func (eventsBrokenFlows) FlowForKind(context.Context, string, string, string) (string, error) {
	return "", errEventsInjected
}

// TestEvents_FlowForKindDecidesTheFlowOfTheChosenKind (FlowForKind): la elección «empezar uno
// nuevo» dice el tipo pero no el flujo. Sin el puerto cableado (o con "" sin error) el evento
// nace SIN flujo y sin dueño; un error del puerto sube y corta el turno sin parir nada.
func TestEvents_FlowForKindDecidesTheFlowOfTheChosenKind(t *testing.T) {
	t.Run("not wired: born without flow", func(t *testing.T) {
		h := eventsFallbackHarness(t, withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithFlowForKind(nil)}
		}))
		h.say("wa-1", "hola-zzq")

		h.say("wa-2", "1")

		ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
		if ev.FlowID != "" || ev.FlowVersion != 0 {
			t.Errorf("el evento nació con %q v%d, quería sin flujo", ev.FlowID, ev.FlowVersion)
		}
		if st, found := h.state(); !found || st.EventID != ev.ID || st.OwnerEventID != "" || st.FlowID != "" {
			t.Errorf("estado = (%+v, %v), quería el evento como activo, sin flujo ni dueño", st, found)
		}
		if texts := h.texts(); len(texts) != 1 {
			t.Errorf("textos = %q, sin flujo que arrancar no hay pantalla nueva", texts)
		}
	})
	t.Run("fails: the error rises", func(t *testing.T) {
		h := eventsFallbackHarness(t, withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithFlowForKind(eventsBrokenFlows{})}
		}))
		h.say("wa-1", "hola-zzq")

		err := h.handle(h.incoming("wa-2", "1"))

		if !errors.Is(err, errEventsInjected) {
			t.Fatalf("HandleIncoming = %v, quería el error de FlowForKind", err)
		}
		if rows := h.events.Events(harnessTenant); len(rows) != 0 {
			t.Errorf("eventos = %+v, sin flujo resuelto no nace ninguno", rows)
		}
	})
}
