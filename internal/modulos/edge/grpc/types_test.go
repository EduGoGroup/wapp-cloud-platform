package grpc

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// streamSender es lo que Connect registra en el Registry: tiene que cumplir su puerto.
var _ session.Sender = (*streamSender)(nil)

// overlapStream es un cloudToEdgeSender que DETECTA solapamiento: si dos Send corren a la
// vez (lo que grpc-go prohíbe sobre un mismo stream) marca la bandera. No lleva candado
// propio: toda la serialización tiene que venir del streamSender. Dentro de la ventana cede
// el procesador a propósito, para que sin candado el solapamiento sea la norma y no un azar;
// y `sent` es un entero pelado, para que -race lo delate además por su cuenta.
type overlapStream struct {
	inFlight atomic.Int32
	overlap  atomic.Bool
	sent     int
}

func (s *overlapStream) Send(*cloudlinkv1.CloudToEdge) error {
	if s.inFlight.Add(1) != 1 {
		s.overlap.Store(true)
	}
	runtime.Gosched()
	s.sent++
	runtime.Gosched()
	s.inFlight.Add(-1)
	return nil
}

// R-G12 (ADR-0008): UN streamSender por stream serializa Send aunque varias sesiones del
// mismo Edge (mismo stream) empujen en paralelo. Las sesiones comparten la MISMA instancia,
// como hace Connect, y empujan por el Registry, que no añade candado propio.
func TestStreamSenderSerializesConcurrentSends(t *testing.T) {
	t.Parallel()
	stream := &overlapStream{}
	sender := newStreamSender(stream)

	reg := session.NewRegistry()
	sessions := []string{"session-a", "session-b", "session-c"}
	for _, sid := range sessions {
		reg.Register(sid, sender)
	}

	const perSession = 200
	var wg sync.WaitGroup
	for _, sid := range sessions {
		for range perSession {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := reg.Push(context.Background(), sid, &cloudlinkv1.CloudToEdge{SessionId: sid}); err != nil {
					t.Errorf("Push(%s) = %v", sid, err)
				}
			}()
		}
	}
	wg.Wait()

	if stream.overlap.Load() {
		t.Fatal("dos Send se solaparon sobre el mismo stream: falta la serialización por-stream (R-G12)")
	}
	if want := len(sessions) * perSession; stream.sent != want {
		t.Fatalf("el stream recibió %d envíos, se esperaban %d", stream.sent, want)
	}
}

// failingStream devuelve siempre el mismo error y apunta el último mensaje que vio.
type failingStream struct {
	err  error
	last *cloudlinkv1.CloudToEdge
}

func (s *failingStream) Send(msg *cloudlinkv1.CloudToEdge) error {
	s.last = msg
	return s.err
}

// Send entrega al stream EL MISMO mensaje y devuelve su resultado tal cual, sin envolver.
func TestStreamSenderPassesMessageAndResultThrough(t *testing.T) {
	t.Parallel()
	errStream := errors.New("stream roto")
	stream := &failingStream{err: errStream}
	sender := newStreamSender(stream)
	msg := &cloudlinkv1.CloudToEdge{CommandId: "cmd-1", SessionId: "s-1"}

	if err := sender.Send(msg); err != errStream { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
		t.Fatalf("Send = %v, se esperaba el error del stream tal cual", err)
	}
	if stream.last != msg {
		t.Fatal("el stream no recibió el mismo mensaje que se le dio al streamSender")
	}

	stream.err = nil
	if err := sender.Send(msg); err != nil {
		t.Fatalf("Send sobre un stream sano = %v", err)
	}
}
