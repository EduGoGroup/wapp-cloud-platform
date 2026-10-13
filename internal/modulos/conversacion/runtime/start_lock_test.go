package runtime_test

// start_lock_test.go es el trozo de start_test.go (E-13) que prueba que la puerta de la API,
// Start, comparte el candado por conversación con los entrantes (T-1): un solo keyedMutex por
// Runtime, y la misma clave.

import (
	"errors"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// TestStart_WaitsForTheTurnThatHoldsTheConversation (T-1): con un turno del cliente parado en
// su envío, un Start de la API sobre la MISMA conversación no entra: se cuenta en el candado
// y espera. Al soltar el turno, Start ve la conversación ya viva y la rechaza. Sin el candado
// (o con otro candado, u otra clave) Start contestaría con el turno todavía dentro.
//
// No corre en una burbuja de synctest por lo mismo que
// TestOnIncoming_SameConversationIsProcessedOneAtATime: la espera es en un sync.Mutex.
func TestStart_WaitsForTheTurnThatHoldsTheConversation(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	releaseSends := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSends)
	h.sender.OnSend(func(runtimehelpertest.Send) {
		entered <- struct{}{}
		<-release
	})
	ref := startRef(t, harnessPhone)

	incomingDeliver(h, h.incoming("wa-1", incomingKeyword))
	incomingAwaitSend(t, entered, "el turno del cliente llega a su envío")
	returned := make(chan error, 1)
	go func() {
		_, err := h.rt.Start(t.Context(), harnessTenant, incomingFlowID, harnessSession, ref)
		returned <- err
	}()
	incomingAwaitQueued(t, h, returned, "Start")

	releaseSends()
	if err := incomingAwaitReturn(t, returned, "Start vuelve al soltarse el turno"); !errors.Is(err, runtime.ErrConversationExists) {
		t.Fatalf("Start = %v, quería ErrConversationExists: al entrar, la conversación del turno ya está viva", err)
	}
	incomingAwaitLockRefs(t, h, nil, 0)
	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantNode(t, h, "root")
}
