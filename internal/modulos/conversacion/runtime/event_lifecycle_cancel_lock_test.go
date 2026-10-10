package runtime_test

// event_lifecycle_cancel_lock_test.go es el trozo de event_lifecycle_cancel_test.go (E-13)
// que prueba que CancelEventForTenant toma el candado de la conversación del evento (T-1) en
// sus dos ramas: la que cancela un evento vivo y la que repara una cancelación a medias.

import (
	"errors"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// lifecycleHoldTurn deja un turno del cliente del guion PARADO en su envío (el paso del menú
// raíz al submenú), con el candado de la conversación tomado. Devuelve la suelta.
func lifecycleHoldTurn(t *testing.T, h *harness) (releaseSends func()) {
	t.Helper()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	releaseSends = sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSends)
	h.sender.OnSend(func(runtimehelpertest.Send) {
		entered <- struct{}{}
		<-release
	})
	incomingDeliver(h, h.incoming("wa-held", "1"))
	incomingAwaitSend(t, entered, "el turno del cliente llega a su envío")
	return releaseSends
}

// lifecycleCancelAsync cancela el evento en otra goroutine y entrega su error por el canal.
func lifecycleCancelAsync(t *testing.T, h *harness, eventID string, got *events.Event) <-chan error {
	t.Helper()
	returned := make(chan error, 1)
	go func() {
		ev, err := h.rt.CancelEventForTenant(t.Context(), harnessTenant, eventID)
		*got = ev
		returned <- err
	}()
	return returned
}

// TestCancelEventForTenant_WaitsForTheTurnThatHoldsTheConversation (RT-13, T-1): con un turno
// del cliente parado en su envío, cancelar su evento desde la app espera el candado de ESA
// conversación; no cancela ni apaga el puntero por debajo del turno. Al soltarlo, cancela.
func TestCancelEventForTenant_WaitsForTheTurnThatHoldsTheConversation(t *testing.T) {
	h, eventID := incomingLiveEvent(t)
	releaseSends := lifecycleHoldTurn(t, h)

	var got events.Event
	returned := lifecycleCancelAsync(t, h, eventID, &got)
	incomingAwaitQueued(t, h, returned, "CancelEventForTenant")
	if row := eventsRow(t, h, eventID); row.Status != events.StatusOpen {
		t.Fatalf("el evento quedó %q con el turno todavía dentro: la cancelación no esperó", row.Status)
	}

	releaseSends()
	if err := incomingAwaitReturn(t, returned, "CancelEventForTenant vuelve al soltarse el turno"); err != nil {
		t.Fatalf("CancelEventForTenant: %v\nlog:\n%s", err, h.log.dump())
	}
	incomingAwaitLockRefs(t, h, nil, 0)
	if got.Status != events.StatusCancelled {
		t.Errorf("devuelto = %+v, quería el evento cancelled", got)
	}
	lifecycleRequirePointers(t, h, "", eventID)
	incomingWantNode(t, h, "sub")
}

// TestCancelEventForTenant_TheRepairWaitsForTheTurnThatHoldsTheConversation (RT-13, T-1): la
// rama que repara una cancelación a medias apaga el puntero bajo el MISMO candado: con un
// turno parado en su envío, el reintento espera y no reescribe el estado por debajo.
func TestCancelEventForTenant_TheRepairWaitsForTheTurnThatHoldsTheConversation(t *testing.T) {
	h, eventID := incomingLiveEvent(t)
	h.abandoner.fail(errEventsInjected)
	if _, err := lifecycleCancel(t, h, eventID); !errors.Is(err, errEventsInjected) {
		t.Fatalf("CancelEventForTenant = %v, quería el fallo del abandono (cancelación a medias)", err)
	}
	h.abandoner.fail(nil)
	lifecycleRequirePointers(t, h, eventID, eventID)
	releaseSends := lifecycleHoldTurn(t, h)

	var got events.Event
	returned := lifecycleCancelAsync(t, h, eventID, &got)
	incomingAwaitQueued(t, h, returned, "el reintento de CancelEventForTenant")
	lifecycleRequirePointers(t, h, eventID, eventID)

	releaseSends()
	if err := incomingAwaitReturn(t, returned, "el reintento vuelve al soltarse el turno"); err != nil {
		t.Fatalf("reintento de CancelEventForTenant: %v\nlog:\n%s", err, h.log.dump())
	}
	incomingAwaitLockRefs(t, h, nil, 0)
	if got.Status != events.StatusCancelled {
		t.Errorf("devuelto = %+v, quería el evento cancelled", got)
	}
	lifecycleRequirePointers(t, h, "", eventID)
	incomingWantNode(t, h, "sub")
}
