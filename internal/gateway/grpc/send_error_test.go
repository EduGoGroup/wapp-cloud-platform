package gatewaygrpc_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// TestSendTextSesiónOfflineDevuelveSendErrorConCommandID: el command_id se genera
// DENTRO del envío, así que si el fallo se lo tragara el llamante se quedaría sin
// el único hilo que correlaciona lo que la nube intentó con el outbox del Edge y
// con los acuses del Plan 013.
//
// Este test ata los dos extremos del notificador de solicitudes (Plan 041 · T4.2):
// allí el command_id se saca por duck-typing (`interface{ CommandID() string }`) y
// lo que se prueba con un doble es el CONTRATO; lo que se prueba aquí es que el
// Gateway real lo cumple.
// No usa el harness de bufconn a propósito: una sesión offline se resuelve en el
// Registry, antes de que haya nada que mandar por gRPC, así que levantar un
// servidor y un cliente para esto solo añadiría partes que pueden fallar por
// motivos que no son los del test.
func TestSendTextSesiónOfflineDevuelveSendErrorConCommandID(t *testing.T) {
	t.Parallel()
	srv := gatewaygrpc.New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Registry vacío: el Push no encuentra a quién empujar.
	_, err := srv.SendText(ctx, "fantasma", "57301", "hola")
	if err == nil {
		t.Fatal("SendText a una sesión offline debería fallar")
	}

	// La causa sigue siendo inspeccionable: los writeSendError de los handlers
	// traducen ESTO a un 502, y envolver no puede haberlo roto.
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("errors.Is(err, ErrSessionOffline) = false; err = %v", err)
	}

	var sendErr *gatewaygrpc.SendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("el error no es *SendError: %v", err)
	}
	if sendErr.CommandID() == "" {
		t.Fatal("el *SendError no lleva command_id: el operador se queda sin correlación")
	}
	if sendErr.SessionID() != "fantasma" {
		t.Fatalf("session_id = %q, quiero fantasma", sendErr.SessionID())
	}

	// Cero PII en el texto del error: ni el destino ni el contenido del mensaje.
	if msg := sendErr.Error(); strings.Contains(msg, "57301") || strings.Contains(msg, "hola") {
		t.Fatalf("el error filtra el destino o el texto del mensaje: %q", msg)
	}
}

// muteSender acepta todo lo que se le empuje y NUNCA acusa: es el Edge saturado
// del incidente del 2026-08-06, que lee su stream pero tarda una eternidad en
// contestar. También es el Edge cuyo stream murió sin acusar — desde el punto de
// vista del que espera, ambos casos son el mismo silencio.
type muteSender struct{}

func (muteSender) Send(*cloudlinkv1.CloudToEdge) error { return nil }

// TestSendTextSeRindeConSuPropioRelojSinDeadlineDelLlamante es el test de regresión
// del cuelgue: un POST /api/v1/messages colgó 88s y el servidor cerró la conexión
// sin responder ni loguear nada.
//
// Lo que lo hacía posible: el contexto de un handler HTTP NO trae deadline (en Go
// el WriteTimeout del http.Server no interrumpe al handler ni cancela su contexto,
// solo hace fallar el Write posterior), así que esperar el Ack únicamente contra
// ctx.Done() era esperar para siempre. De ahí que este test pase
// context.Background() a propósito: si el reloj del Gateway desapareciera, esta
// llamada no volvería nunca y el test moriría por timeout de `go test` en vez de
// fallar — el modo exacto en que se manifestó en producción.
func TestSendTextSeRindeConSuPropioRelojSinDeadlineDelLlamante(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	release := reg.Register("s-muda", muteSender{})
	defer release()

	const ackTimeout = 50 * time.Millisecond
	srv := gatewaygrpc.New(reg, logger.New(logger.WithWriter(io.Discard)),
		gatewaygrpc.WithAckTimeout(ackTimeout))

	start := time.Now()
	_, err := srv.SendText(context.Background(), "s-muda", "57301", "hola")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("SendText contra un Edge que no acusa debería rendirse, no devolver nil")
	}
	// La causa tiene que seguir siendo DeadlineExceeded: es lo que writeSendError
	// traduce a 504, y envolver en *SendError no puede haberlo roto.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
	}

	// El command_id es lo que hace diagnosticable al 504: el comando YA viajó al
	// Edge, así que sin él no hay forma de averiguar después si el mensaje salió.
	var sendErr *gatewaygrpc.SendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("el error no es *SendError: %v", err)
	}
	if sendErr.CommandID() == "" {
		t.Fatal("el timeout del ack no lleva command_id: el operador no puede saber si el mensaje salió")
	}
	if sendErr.SessionID() != "s-muda" {
		t.Fatalf("session_id = %q, quiero s-muda", sendErr.SessionID())
	}

	// Se rindió por SU reloj, no por el del llamante (que no tenía). El margen es
	// holgado a propósito: lo que se afirma es "acotado", no una latencia exacta.
	if elapsed > 5*time.Second {
		t.Fatalf("tardó %v en rendirse con ack_timeout=%v: el reloj propio no se está aplicando", elapsed, ackTimeout)
	}
}
