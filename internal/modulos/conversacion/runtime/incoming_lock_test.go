package runtime_test

// incoming_lock_test.go es el trozo de incoming_test.go (E-13) que prueba la serialización por
// conversación de la puerta asíncrona, OnIncoming. Es el único test de esa puerta que no corre
// en una burbuja de synctest; el porqué está en el comentario del test.

import (
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// incomingWatchdog es el tope de espera de un suceso obligado. No es una pausa: nadie duerme
// este tiempo; con el código correcto no vence nunca y con un mutante da un fallo legible.
const incomingWatchdog = 10 * time.Second

// incomingAwaitSend espera a que un turno llegue a su envío (el doble avisa por entered).
func incomingAwaitSend(t *testing.T, entered <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(incomingWatchdog):
		t.Fatalf("no ocurrió: %s", what)
	}
}

// incomingAwaitLockRefs cede el procesador hasta que el conteo del candado de la conversación
// del guion vale want. No duerme. Si mientras tanto OTRO turno llega a su envío, falla: nadie
// más puede estar dentro de la conversación.
func incomingAwaitLockRefs(t *testing.T, h *harness, entered <-chan struct{}, want int) {
	t.Helper()
	key := h.key()
	deadline := time.After(incomingWatchdog)
	for {
		refs := h.rt.ConversationLockRefs(key)
		if refs == want {
			return
		}
		select {
		case <-entered:
			t.Fatal("un segundo turno de la MISMA conversación llegó a su envío con el primero todavía dentro: no se procesan de uno en uno")
		case <-deadline:
			t.Fatalf("el conteo del candado de la conversación no llegó a %d (vale %d)", want, refs)
		default:
			goruntime.Gosched()
		}
	}
}

// TestOnIncoming_SameConversationIsProcessedOneAtATime: la serialización por conversación la
// sigue dando HandleIncoming. Con el primer turno parado en su envío, el segundo entrante de
// la MISMA conversación no avanza; al soltarlo, se procesan los dos en orden.
//
// No corre en una burbuja de synctest: el segundo turno espera en un sync.Mutex (el del
// keyedMutex), que para la burbuja no es un bloqueo «durable», y synctest.Wait no volvería
// nunca. Que el segundo «ya está esperando» se observa como en keyedmutex_test.go: leyendo el
// conteo del candado de la conversación (2 = el titular y uno contado ANTES de esperar, KM-4).
// Sin el candado por conversación el conteo no llega a 2 y el segundo turno alcanza su envío:
// lo dice incomingAwaitLockRefs, no el -timeout.
func TestOnIncoming_SameConversationIsProcessedOneAtATime(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	// Una sola suelta, pase lo que pase: si una aserción corta el test, los turnos parados
	// en su envío no se quedan colgados.
	releaseSends := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSends)
	h.sender.OnSend(func(runtimehelpertest.Send) {
		entered <- struct{}{}
		<-release
	})

	incomingDeliver(h, h.incoming("wa-1", incomingKeyword))
	incomingAwaitSend(t, entered, "el primer turno llega a su envío")
	incomingDeliver(h, h.incoming("wa-2", "1"))
	incomingAwaitLockRefs(t, h, entered, 2)

	if attempts := h.sender.Attempts(); len(attempts) != 1 {
		t.Fatalf("intentos de envío = %d, quería 1: el segundo turno espera el candado de la conversación", len(attempts))
	}
	incomingWantNode(t, h, "root")

	releaseSends()
	incomingAwaitLockRefs(t, h, nil, 0)
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
	incomingWantNode(t, h, "sub")
}

// incomingAwaitQueued cede el procesador hasta que una SEGUNDA entrada a la conversación del
// guion queda contada en su candado (2 = el titular y uno contado ANTES de esperar, KM-4): es
// decir, hasta que la llamada vigilada está esperando su turno. No duerme. Si la llamada
// TERMINA antes (entrega su error por returned), falla: no esperó al turno que estaba dentro.
func incomingAwaitQueued(t *testing.T, h *harness, returned <-chan error, who string) {
	t.Helper()
	key := h.key()
	deadline := time.After(incomingWatchdog)
	for {
		if h.rt.ConversationLockRefs(key) == 2 {
			return
		}
		select {
		case err := <-returned:
			t.Fatalf("%s terminó (error: %v) con un turno de la MISMA conversación todavía dentro: no tomó el candado de la conversación", who, err)
		case <-deadline:
			t.Fatalf("%s no quedó esperando el candado de la conversación (conteo = %d, quería 2)", who, h.rt.ConversationLockRefs(key))
		default:
			goruntime.Gosched()
		}
	}
}

// incomingAwaitReturn espera el resultado de una llamada lanzada en otra goroutine.
func incomingAwaitReturn(t *testing.T, returned <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-returned:
		return err
	case <-time.After(incomingWatchdog):
		t.Fatalf("no ocurrió: %s", what)
		return nil
	}
}
