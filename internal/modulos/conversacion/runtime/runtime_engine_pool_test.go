package runtime_test

// runtime_engine_pool_test.go prueba lo que New deja resuelto para OnIncoming (RT-3): el plazo
// de cada entrante (<= 0 → 30 s) y el semáforo (0 → 64 cupos, > 0 → ese cupo, < 0 → sin
// techo), y que sin cupo a tiempo el entrante se cuenta como `saturation`.
//
// Corre en testing/synctest: el tiempo es de mentira y no hay esperas reales. El Sender de
// estos tests RETIENE cada envío hasta que el test lo suelta, que es lo que mantiene ocupado
// el cupo de un entrante.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// poolSender es un runtime.Sender que apunta el plazo que le queda al contexto de cada envío y
// lo retiene hasta que se cierra release. No mira la cancelación del contexto: un Edge mudo.
type poolSender struct {
	release chan struct{}

	mu        sync.Mutex
	remaining []time.Duration
	inFlight  int
	total     int
}

func newPoolSender() *poolSender { return &poolSender{release: make(chan struct{})} }

func (s *poolSender) hold(ctx context.Context) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		s.remaining = append(s.remaining, time.Until(deadline))
	} else {
		s.remaining = append(s.remaining, -1)
	}
	s.inFlight++
	s.total++
	s.mu.Unlock()

	<-s.release

	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return &cloudlinkv1.Ack{Ok: true}, nil
}

func (s *poolSender) SendText(ctx context.Context, _, _, _ string) (*cloudlinkv1.Ack, error) {
	return s.hold(ctx)
}

func (s *poolSender) SendMedia(ctx context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	return s.hold(ctx)
}

// snapshot devuelve los plazos apuntados, cuántos envíos están retenidos y cuántos entraron.
func (s *poolSender) snapshot() (remaining []time.Duration, inFlight, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.remaining...), s.inFlight, s.total
}

// poolScript monta, DENTRO de la burbuja de synctest, un Runtime con el Sender que retiene y
// esas opciones, y le entrega por OnIncoming un «hola» de n teléfonos distintos (cada uno
// arranca su menú y se queda retenido en el envío). Vuelve con todo quieto.
func poolScript(t *testing.T, n int, opts ...runtime.Option) (*harness, *poolSender) {
	t.Helper()
	h := engineKeywordHarness(t)
	sender := newPoolSender()
	rt := runtime.New(h.repo, h.engine, sender, h.tenants, h.contacts, h.log, append(h.runtimeOptions(), opts...)...)
	for i := range n {
		rt.OnIncoming(harnessSession, h.incomingFrom(fmt.Sprintf("5730000%05d", i), fmt.Sprintf("wa-%d", i), "hola"))
	}
	synctest.Wait()
	return h, sender
}

// RT-3 · El plazo del entrante: sin opción, o con un valor <= 0, son 30 s; con uno positivo,
// ese. El camino caliente nunca queda sin plazo.
func TestIncomingPool_Timeout(t *testing.T) {
	cases := []struct {
		name string
		opts []runtime.Option
		want time.Duration
	}{
		{"no option", nil, 30 * time.Second},
		{"zero", []runtime.Option{runtime.WithIncomingTimeout(0)}, 30 * time.Second},
		{"negative", []runtime.Option{runtime.WithIncomingTimeout(-time.Second)}, 30 * time.Second},
		{"positive", []runtime.Option{runtime.WithIncomingTimeout(5 * time.Second)}, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, sender := poolScript(t, 1, tc.opts...)

				remaining, _, _ := sender.snapshot()
				if len(remaining) != 1 || remaining[0] != tc.want {
					t.Errorf("plazo del contexto del entrante = %v, quería %v", remaining, tc.want)
				}
				close(sender.release)
				synctest.Wait()
			})
		})
	}
}

// RT-3 · El semáforo: sin opción o con 0, 64 cupos; con n > 0, n; con n < 0, sin techo. El que
// espera cupo entra en cuanto se libera uno, sin descartarse.
func TestIncomingPool_Ceiling(t *testing.T) {
	cases := []struct {
		name     string
		opts     []runtime.Option
		incoming int
		atOnce   int
	}{
		{"no option", nil, 65, 64},
		{"zero", []runtime.Option{runtime.WithMaxConcurrentIncoming(0)}, 65, 64},
		{"positive", []runtime.Option{runtime.WithMaxConcurrentIncoming(2)}, 3, 2},
		{"negative", []runtime.Option{runtime.WithMaxConcurrentIncoming(-1)}, 70, 70},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sender := poolScript(t, tc.incoming, tc.opts...)

				if _, inFlight, _ := sender.snapshot(); inFlight != tc.atOnce {
					t.Errorf("entrantes procesándose a la vez = %d, quería %d", inFlight, tc.atOnce)
				}
				close(sender.release)
				synctest.Wait()
				if _, _, total := sender.snapshot(); total != tc.incoming {
					t.Errorf("entrantes procesados = %d, quería los %d: el que esperaba cupo entra al liberarse", total, tc.incoming)
				}
				if got := h.blockedReasons(); len(got) != 0 {
					t.Errorf("motivos de corte = %v, nadie se quedó sin cupo dentro del plazo", got)
				}
			})
		})
	}
}

// RT-3 / RT-2 · Con el cupo ocupado durante todo el plazo, el entrante se descarta sin llegar
// al motor: se cuenta `saturation` y el WARN lleva solo session_id y wa_message_id.
func TestIncomingPool_NoSlotInTimeIsSaturation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, sender := poolScript(t, 2, runtime.WithMaxConcurrentIncoming(1), runtime.WithIncomingTimeout(5*time.Second))

		<-time.After(6 * time.Second) // tiempo de la burbuja: vence el plazo del que espera cupo
		synctest.Wait()

		if got := h.blockedReasons(); !resumeEqual(got, []string{"saturation"}) {
			t.Errorf("motivos de corte = %v, quería [saturation]", got)
		}
		if _, _, total := sender.snapshot(); total != 1 {
			t.Errorf("entrantes que llegaron al motor = %d, quería 1: el otro se descarta", total)
		}
		line, ok := resumeLogLine(h, "warn", "runtime: entrante descartado por saturación (sin cupo en el pool a tiempo)")
		if !ok {
			t.Fatalf("falta el WARN de saturación\nlog:\n%s", h.log.dump())
		}
		if len(line.fields) != 2 || line.fields["session_id"] != harnessSession || line.fields["wa_message_id"] == nil {
			t.Errorf("campos = %v, quería solo session_id y wa_message_id (sin PII)", line.fields)
		}
		close(sender.release)
		synctest.Wait()
	})
}
