//go:build pendiente

package intakeahead_test

// intakeahead_run_test.go — los workers de Run, vistos por el contrato de intakeahead.go:
// cuántas inferencias corren a la vez, cuándo vuelve Run y qué reloj gobierna una
// inferencia en vuelo. (E-13: partido de intakeahead_test.go por tema.)

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
)

// TestRun_BoundsTheConcurrentInferences: Run arranca tantos workers como diga el pool y
// ni uno más. Con todas las inferencias bloqueadas, las que están DENTRO son
// exactamente los workers; el resto espera en la cola y sale después.
func TestRun_BoundsTheConcurrentInferences(t *testing.T) {
	cases := []struct {
		name string
		opts []intakeahead.Option
		want int
	}{
		{"default", nil, intakeahead.DefaultWorkers},
		{"one", []intakeahead.Option{intakeahead.WithWorkers(1)}, 1},
		{"two", []intakeahead.Option{intakeahead.WithWorkers(2)}, 2},
		{"six", []intakeahead.Option{intakeahead.WithWorkers(6)}, 6},
		{"zero is ignored", []intakeahead.Option{intakeahead.WithWorkers(0)}, intakeahead.DefaultWorkers},
		{"negative is ignored", []intakeahead.Option{intakeahead.WithWorkers(-3)}, intakeahead.DefaultWorkers},
		{"ignored value keeps the previous one",
			[]intakeahead.Option{intakeahead.WithWorkers(2), intakeahead.WithWorkers(0)}, 2},
		{"last one wins", []intakeahead.Option{intakeahead.WithWorkers(2), intakeahead.WithWorkers(3)}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, tc.opts...)
				g := b.blockProvider()
				b.start()

				const requests = 9
				for i := range requests {
					b.pool.Request(windowOf(fmt.Sprintf("c-%d", i)), clientText)
				}
				settle()
				if now, _ := b.prov.flying(); now != tc.want {
					t.Fatalf("inferencias en vuelo = %d, quiero %d", now, tc.want)
				}

				g.open()
				settle()
				if _, peak := b.prov.flying(); peak != tc.want {
					t.Errorf("pico de inferencias simultáneas = %d, quiero %d", peak, tc.want)
				}
				if got := len(b.sink.seen()); got != requests {
					t.Errorf("entregas = %d, quiero las %d: lo encolado se atiende después", got, requests)
				}
			})
		})
	}
}

// TestRun_BlocksUntilTheContextIsCancelled: Run no vuelve mientras el ctx viva —ni
// ocioso ni después de servir— y vuelve cuando se cancela.
func TestRun_BlocksUntilTheContextIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.start()
		if !b.running() {
			t.Fatal("Run volvió sin que nadie cancelara el ctx")
		}
		b.pool.Request(windowKey(), clientText)
		if !b.running() {
			t.Fatal("Run volvió tras servir una petición")
		}
		if len(b.sink.seen()) != 1 {
			t.Fatal("la petición no se sirvió")
		}

		b.cancel()
		if b.running() {
			t.Fatal("Run no volvió tras cancelar el ctx: los workers no respetan ctx.Done()")
		}
	})
}

// TestRun_CancellingCutsTheInferenceInFlight: el ctx de la inferencia cuelga del de Run
// —el del proceso—, que es de donde sale su reloj.
func TestRun_CancellingCutsTheInferenceInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.prov.setHook(func(ctx context.Context, _ int) error {
			<-ctx.Done()
			return ctx.Err()
		})
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()
		if now, _ := b.prov.flying(); now != 1 {
			t.Fatalf("la inferencia no llegó a arrancar")
		}

		b.cancel()
		if b.running() {
			t.Fatal("Run no volvió: la inferencia en vuelo no vio la cancelación del ctx de Run")
		}
		if len(b.sink.seen()) != 0 {
			t.Errorf("una inferencia cortada no entrega nada")
		}
	})
}

// TestRequest_BudgetExpiryLosesOnlyTheAdvance: una inferencia que no vuelve se corta al
// agotar el presupuesto —ni antes—, no entrega nada, y la ventana queda libre.
func TestRequest_BudgetExpiryLosesOnlyTheAdvance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithWorkers(1))
		b.prov.setHook(func(ctx context.Context, n int) error {
			if n > 0 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		})
		b.start()

		b.pool.Request(windowKey(), clientText)
		time.Sleep(intakeahead.DefaultTimeout - time.Nanosecond) // reloj de la burbuja
		settle()
		if now, _ := b.prov.flying(); now != 1 {
			t.Fatal("la inferencia se cortó ANTES de agotar el presupuesto")
		}
		time.Sleep(2 * time.Nanosecond) // cruza el deadline
		settle()
		if now, _ := b.prov.flying(); now != 0 {
			t.Fatal("la inferencia sigue viva pasado el presupuesto")
		}
		if len(b.sink.seen()) != 0 {
			t.Errorf("una inferencia vencida no entrega nada")
		}

		b.pool.Request(windowKey(), clientText)
		settle()
		if len(b.sink.seen()) != 1 {
			t.Errorf("tras vencer, la ventana queda libre y el worker vivo: la siguiente se sirve")
		}
	})
}
