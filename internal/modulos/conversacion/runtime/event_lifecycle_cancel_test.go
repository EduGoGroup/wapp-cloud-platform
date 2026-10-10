package runtime_test

// event_lifecycle_cancel_test.go prueba CancelEventForTenant (event_lifecycle.go): la
// cancelación EXPLÍCITA desde la app del dueño (RT-13), lo que el cliente ve después (RT-14,
// T62) y la rama idempotente que repara una cancelación a medias.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Los dos prefijos de error de la cancelación, byte a byte.
const (
	lifecycleCancelPrefix = "runtime: cancelar el evento: "
	lifecycleRepairPrefix = "runtime: completar el abandono de la solicitud del evento cancelado: "
)

// lifecycleCancel cancela el evento por el tenant del guion.
func lifecycleCancel(t *testing.T, h *harness, eventID string) (events.Event, error) {
	t.Helper()
	return h.rt.CancelEventForTenant(t.Context(), harnessTenant, eventID)
}

// TestCancelEventForTenant_CancelsAbandonsAndTurnsThePointerOff (RT-13, RT-14): sobre un
// evento vivo transiciona a cancelled, emite event_cancelled, abandona la solicitud que lo
// declare y apaga el puntero ACTIVO conservando el resto del estado. Devuelve la fila
// releída, con el closed_at que selló el almacén.
func TestCancelEventForTenant_CancelsAbandonsAndTurnsThePointerOff(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)
	before, _ := h.state()

	got, err := lifecycleCancel(t, h, ev.ID)

	if err != nil {
		t.Fatalf("CancelEventForTenant: %v\nlog:\n%s", err, h.log.dump())
	}
	if got.Status != events.StatusCancelled || !got.ClosedAt.Equal(h.clock.Now()) || got != eventsRow(t, h, ev.ID) {
		t.Errorf("devuelto = %+v, quería la fila releída: cancelled y sellada en %v", got, h.clock.Now())
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)[0], ev, 0)
	if calls := h.abandoner.calls(); len(calls) != 1 || calls[0] != ev.ID {
		t.Errorf("abandonos = %v, quería uno, por el evento cancelado", calls)
	}
	st := lifecycleRequirePointers(t, h, "", ev.ID)
	if st.FlowID != before.FlowID || st.CurrentNode != before.CurrentNode || st.FlowVersion != before.FlowVersion {
		t.Errorf("estado = %+v, apagar el puntero conserva el resto: %+v", st, before)
	}
	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, cancelar desde la app no le escribe al cliente", texts)
	}
}

// TestCancelEventForTenant_TheClientCanOpenANewOneAtOnce (RT-14, T62): con el evento
// cancelado el tipo queda libre: el siguiente entrante que lo dispare abre un evento NUEVO
// en el acto, sin esperar a ningún reloj.
func TestCancelEventForTenant_TheClientCanOpenANewOneAtOnce(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)
	if _, err := lifecycleCancel(t, h, ev.ID); err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}

	h.say("wa-2", eventsCartWord)

	born := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if born.ID == ev.ID || !born.CreatedAt.Equal(h.clock.Now()) {
		t.Fatalf("evento vivo = %+v, quería uno NUEVO nacido en este entrante", born)
	}
	lifecycleRequirePointers(t, h, born.ID, born.ID)
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 2)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)
	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusCancelled {
		t.Errorf("el evento cancelado quedó %q", row.Status)
	}
	if texts := h.texts(); len(texts) != 2 || texts[1] != texts[0] {
		t.Errorf("textos = %q, quería la pantalla inicial del carrito nuevo", texts)
	}
}

// TestCancelEventForTenant_UnknownEventTouchesNothing: un id inexistente o de otro tenant
// devuelve el error del almacén tal cual, antes de tocar nada.
func TestCancelEventForTenant_UnknownEventTouchesNothing(t *testing.T) {
	const otherTenant = "22222222-2222-4222-8222-222222222222"
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)

	for name, call := range map[string][2]string{
		"another tenant": {otherTenant, ev.ID},
		"unknown id":     {harnessTenant, uuid.NewString()},
	} {
		got, err := h.rt.CancelEventForTenant(t.Context(), call[0], call[1])
		if !errors.Is(err, events.ErrEventNotFound) || got != (events.Event{}) {
			t.Errorf("%s: CancelEventForTenant = (%+v, %v), quería events.ErrEventNotFound", name, got, err)
		}
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento quedó %q, nadie debía tocarlo", row.Status)
	}
	lifecycleRequirePointers(t, h, ev.ID, ev.ID)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, no debía abandonarse nada", calls)
	}
}

// TestCancelEventForTenant_LeavesAPointerThatLooksElsewhere (RT-13, paso 5): el puntero solo
// se apaga si seguía apuntando a ESTE evento. Si apunta a otro, o no hay estado, no se
// escribe nada.
func TestCancelEventForTenant_LeavesAPointerThatLooksElsewhere(t *testing.T) {
	t.Run("the state points to another event", func(t *testing.T) {
		h := eventsSwitchHarness(t)
		cart := lifecycleLiveCart(t, h)
		h.say("wa-2", eventsSurveyWord)
		surveyEv := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
		before, _ := h.state()
		h.clock.Advance(time.Minute)

		if _, err := lifecycleCancel(t, h, cart.ID); err != nil {
			t.Fatalf("CancelEventForTenant: %v", err)
		}

		st := lifecycleRequirePointers(t, h, surveyEv.ID, surveyEv.ID)
		if !st.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("el estado se reescribió en %v: su puntero no era asunto de esta cancelación", st.UpdatedAt)
		}
		if row := eventsRow(t, h, surveyEv.ID); row.Status != events.StatusOpen {
			t.Errorf("la encuesta quedó %q, quería open", row.Status)
		}
	})
	t.Run("there is no state", func(t *testing.T) {
		h := eventsCartHarness(t)
		ev := h.seedEvent(trigger.EventKindCart, eventsCartFlow, 1)

		got, err := lifecycleCancel(t, h, ev.ID)

		if err != nil || got.Status != events.StatusCancelled {
			t.Fatalf("CancelEventForTenant = (%+v, %v), quería la fila cancelled", got, err)
		}
		if st, found := h.state(); found {
			t.Errorf("estado = %+v, cancelar no crea un estado que no existía", st)
		}
	})
}

// TestCancelEventForTenant_LosingTheRaceRereadsWhatIsLeft (RT-13): events.ErrNotOpen es
// carrera benigna. No abandona ni toca el puntero; relee y devuelve lo que quedó, sin error.
func TestCancelEventForTenant_LosingTheRaceRereadsWhatIsLeft(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	faults.loseTransitionRace(events.StatusClosed)

	got, err := lifecycleCancel(t, h, ev.ID)

	if err != nil || got.Status != events.StatusClosed || got != eventsRow(t, h, ev.ID) {
		t.Fatalf("CancelEventForTenant = (%+v, %v), quería sin error la fila closed que dejó el otro escritor", got, err)
	}
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, quien pierde la carrera no abandona", calls)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	lifecycleRequirePointers(t, h, ev.ID, ev.ID)
}

// TestCancelEventForTenant_ARealTransitionFailureTouchesNothingElse (RT-13): otro fallo de la
// transición sube con su prefijo y nada más se toca.
func TestCancelEventForTenant_ARealTransitionFailureTouchesNothingElse(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	faults.onTransition(func(context.Context, string, events.Status) error { return errEventsInjected })

	got, err := lifecycleCancel(t, h, ev.ID)

	if !errors.Is(err, errEventsInjected) || !strings.HasPrefix(err.Error(), lifecycleCancelPrefix) || got != (events.Event{}) {
		t.Fatalf("CancelEventForTenant = (%+v, %v), quería el fallo con el prefijo %q", got, err, lifecycleCancelPrefix)
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento quedó %q, quería open", row.Status)
	}
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, si el evento no se pudo cerrar la solicitud no se toca", calls)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	lifecycleRequirePointers(t, h, ev.ID, ev.ID)
}

// TestCancelEventForTenant_RetryRepairsAHalfDoneCancellation (RT-13): si el abandono falla, el
// error SUBE con el evento ya cancelado, su efecto ya emitido y el puntero sin apagar.
// Reintentar entra por la rama terminal, que completa las dos consecuencias SIN tocar la
// fila del evento: ni transición, ni closed_at nuevo, ni efecto. Sobre una cancelación que
// salió bien no escribe nada.
func TestCancelEventForTenant_RetryRepairsAHalfDoneCancellation(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)
	firstDeath := h.clock.Now()
	h.abandoner.fail(errEventsInjected)

	_, err := lifecycleCancel(t, h, ev.ID)

	if !errors.Is(err, errEventsInjected) || !strings.HasPrefix(err.Error(), lifecycleCancelPrefix) {
		t.Fatalf("CancelEventForTenant = %v, quería el fallo del abandono con el prefijo %q", err, lifecycleCancelPrefix)
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusCancelled {
		t.Fatalf("el evento quedó %q, quería cancelled: la transición ya estaba sellada", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)
	lifecycleRequirePointers(t, h, ev.ID, ev.ID)

	h.clock.Advance(time.Hour)
	_, err = lifecycleCancel(t, h, ev.ID)
	if !errors.Is(err, errEventsInjected) || !strings.HasPrefix(err.Error(), lifecycleRepairPrefix) {
		t.Fatalf("reintento con el abandono roto = %v, quería el prefijo %q", err, lifecycleRepairPrefix)
	}

	h.abandoner.fail(nil)
	got, err := lifecycleCancel(t, h, ev.ID)
	if err != nil || got.Status != events.StatusCancelled || !got.ClosedAt.Equal(firstDeath) {
		t.Fatalf("reintento = (%+v, %v), quería la fila cancelled con el closed_at de la PRIMERA muerte (%v)", got, err, firstDeath)
	}
	if calls := h.abandoner.calls(); len(calls) != 3 || calls[2] != ev.ID {
		t.Errorf("abandonos = %v, el reintento debía completar el abandono de ese evento", calls)
	}
	repaired := lifecycleRequirePointers(t, h, "", ev.ID)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)

	h.clock.Advance(time.Hour)
	if _, err = lifecycleCancel(t, h, ev.ID); err != nil {
		t.Fatalf("cancelar lo ya cancelado: %v", err)
	}
	if st, _ := h.state(); !st.UpdatedAt.Equal(repaired.UpdatedAt) {
		t.Errorf("el estado se reescribió en %v: sobre una cancelación que salió bien no se escribe nada", st.UpdatedAt)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 1)
}

// TestCancelEventForTenant_AClosedEventIsReturnedAsIs (RT-13): un evento que cerró por fin
// natural se devuelve tal cual: no se abandona su solicitud, no se toca closed_at ni el
// estado, y no se emite nada.
func TestCancelEventForTenant_AClosedEventIsReturnedAsIs(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)
	h.say("wa-2", "1")
	closed := eventsRow(t, h, ev.ID)
	before, _ := h.state()
	h.clock.Advance(time.Hour)

	got, err := lifecycleCancel(t, h, ev.ID)

	if err != nil || got != closed || got.Status != events.StatusClosed {
		t.Fatalf("CancelEventForTenant = (%+v, %v), quería la fila closed tal cual: %+v", got, err, closed)
	}
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, un fin natural jamás abandona su solicitud", calls)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	if st, _ := h.state(); !st.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("el estado se reescribió en %v, quería intacto", st.UpdatedAt)
	}
}

// TestCancelEventForTenant_WithoutAbandonerWarnsAndGoesOn (IntakeAbandoner nil): el evento se
// cancela igual, se avisa a WARN y el puntero se apaga.
func TestCancelEventForTenant_WithoutAbandonerWarnsAndGoesOn(t *testing.T) {
	h := eventsCartHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithIntakeAbandoner(nil)}
	}))
	ev := lifecycleLiveCart(t, h)

	got, err := lifecycleCancel(t, h, ev.ID)

	if err != nil || got.Status != events.StatusCancelled {
		t.Fatalf("CancelEventForTenant sin abandonador = (%+v, %v), quería la fila cancelled", got, err)
	}
	const warning = "runtime: evento cancelado sin IntakeAbandoner cableado; si tenía solicitud, quedó sin abandonar"
	if !eventsLogged(h, "warn", warning) {
		t.Errorf("no se avisó a WARN de la solicitud sin abandonar\nlog:\n%s", h.log.dump())
	}
	lifecycleRequirePointers(t, h, "", ev.ID)
}

// TestCancelEventForTenant_AFailedPointerCleanupRises (RT-13): si falla la limpieza del
// puntero el error sube, con el evento ya cancelado y su solicitud ya abandonada.
func TestCancelEventForTenant_AFailedPointerCleanupRises(t *testing.T) {
	h, repo := eventsFaultyRepoHarness(t)
	ev := lifecycleLiveCart(t, h)
	repo.failSave(errEventsInjected)

	_, err := lifecycleCancel(t, h, ev.ID)

	if !errors.Is(err, errEventsInjected) {
		t.Fatalf("CancelEventForTenant = %v, quería el fallo de apagar el puntero", err)
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusCancelled {
		t.Errorf("el evento quedó %q, quería cancelled", row.Status)
	}
	if calls := h.abandoner.calls(); len(calls) != 1 {
		t.Errorf("abandonos = %v, la solicitud ya debía estar abandonada", calls)
	}
}
