package pipeline_test

// doubles_wait_test.go — LA ESPERA SIN DORMIR que comparten los tests del aforo y los del
// worker (sin fichero de producción gemelo). Trozo de doubles_test.go: vive aparte porque
// slot_test.go lo necesita y no necesita nada más de allí.

import (
	"runtime"
	"testing"
	"time"
)

// waitLimit es el límite de paciencia de las esperas: no mide nada, es el punto en que se
// declara que una condición NO va a llegar.
const waitLimit = 5 * time.Second

// eventually gira hasta que `cond` se cumple, cediendo el procesador en cada vuelta.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("nunca se llegó a: %s", what)
		}
		runtime.Gosched()
	}
}
