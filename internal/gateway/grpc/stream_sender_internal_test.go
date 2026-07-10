package gatewaygrpc

import (
	"sync"
	"sync/atomic"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// overlapStream es un cloudToEdgeSender que DETECTA solapamiento: si dos Send
// corren a la vez (lo que grpc-go prohíbe sobre un mismo stream) marca la bandera.
// No usa mutex propio: cualquier serialización debe venir del streamSender.
type overlapStream struct {
	inFlight atomic.Int32
	overlap  atomic.Bool
	count    atomic.Int32
}

func (s *overlapStream) Send(*cloudlinkv1.CloudToEdge) error {
	if s.inFlight.Add(1) != 1 {
		s.overlap.Store(true)
	}
	// Ventana de solapamiento: si el candado por-stream no serializara, otra
	// goroutine entraría aquí y inFlight pasaría de 1.
	for i := 0; i < 256; i++ {
		s.count.Add(1)
		s.count.Add(-1)
	}
	s.count.Add(1)
	s.inFlight.Add(-1)
	return nil
}

// TestStreamSenderSerializesConcurrentSends comprueba que UN streamSender por
// stream serializa Send aunque DOS sesiones del mismo Edge (mismo stream) empujen
// en paralelo: el escenario exacto de H2 (Plan 027 · Ola 0 · T3). El registro
// comparte la MISMA instancia de streamSender para ambos session_id, como hace
// Connect. Correr con -race.
func TestStreamSenderSerializesConcurrentSends(t *testing.T) {
	t.Parallel()

	stream := &overlapStream{}
	sender := newStreamSender(stream)

	reg := session.NewRegistry()
	// Las dos sesiones del mismo Edge registran el MISMO sender (un stream por Edge).
	reg.Register("sesion-A", sender)
	reg.Register("sesion-B", sender)

	const perSession = 200
	var wg sync.WaitGroup
	for _, sid := range []string{"sesion-A", "sesion-B"} {
		for i := 0; i < perSession; i++ {
			wg.Add(1)
			go func(sid string) {
				defer wg.Done()
				if err := reg.Push(sid, &cloudlinkv1.CloudToEdge{SessionId: sid}); err != nil {
					t.Errorf("Push(%s) devolvió error: %v", sid, err)
				}
			}(sid)
		}
	}
	wg.Wait()

	if stream.overlap.Load() {
		t.Fatal("se detectó Send concurrente sobre el mismo stream: la serialización por-stream falló (H2)")
	}
	if got := stream.count.Load(); got != 2*perSession {
		t.Fatalf("el stream recibió %d envíos, quiero %d", got, 2*perSession)
	}
}
