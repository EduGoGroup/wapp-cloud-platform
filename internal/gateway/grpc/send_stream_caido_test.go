package gatewaygrpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// streamCaidoDe pregunta por el contrato REAL que van a usar los handlers: la
// interfaz anónima, no el tipo del Gateway ni su centinela. Está escrita aquí a mano
// —y no llamando a un helper del paquete— porque duplicarla es justamente el
// desacople: si esto compilase solo importando gatewaygrpc, el contrato no sería la
// interfaz. Es la misma forma que commandIDFrom tiene en httpapi/admin.go y en
// publicapi, duplicada allí por el mismo motivo.
func streamCaidoDe(err error) bool {
	var caido interface{ StreamCaido() bool }
	if errors.As(err, &caido) {
		return caido.StreamCaido()
	}
	return false
}

// TestStreamCaidoSeDistingueDelTimeoutPorDuckTyping es el contrato que consume T2.4.
//
// Las dos mitades importan por igual. La primera prueba que un stream que muere con
// un envío en vuelo se puede reconocer SIN importar el paquete del Gateway. La
// segunda prueba que ese reconocimiento DISCRIMINA: el timeout normal y la sesión
// offline contestan false por el mismo camino. Sin la segunda, un StreamCaido() que
// devolviera true a secas dejaría el test verde y le daría a T2.4 un contrato que no
// distingue nada — que es exactamente el defecto que la Ola 2 viene a corregir, con
// otra cara.
func TestStreamCaidoSeDistingueDelTimeoutPorDuckTyping(t *testing.T) {
	t.Parallel()

	t.Run("stream que cae con el envío en vuelo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		streamCtx, streamCancel := context.WithCancel(ctx)
		stream, err := h.client.Connect(streamCtx)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
			t.Fatalf("Send heartbeat: %v", sendErr)
		}
		waitOnline(t, h.registry, "s1", true)

		res := make(chan error, 1)
		go func() {
			_, sendErr := h.srv.SendText(ctx, "s1", "57301", "en vuelo")
			res <- sendErr
		}()

		// El Edge RECIBE el comando y no acusa: es la situación que hace honesto el 504
		// en vez del 502 — el comando ya viajó, así que nadie puede afirmar que no salió.
		if _, recvErr := stream.Recv(); recvErr != nil {
			t.Fatalf("Recv del comando: %v", recvErr)
		}

		if closeErr := stream.CloseSend(); closeErr != nil {
			t.Fatalf("CloseSend: %v", closeErr)
		}
		streamCancel()

		// El margen es holgado a propósito: aquí se afirma el CONTRATO, no la latencia
		// (esa la mide el test interno, con el cierre en la misma goroutine). Aun así el
		// techo está muy por debajo del ackTimeout por defecto —8 s— para que un
		// StreamCaido() que llegara solo después de agotar el plazo no pase por bueno.
		var got error
		select {
		case got = <-res:
		case <-time.After(4 * time.Second):
			t.Fatal("SendText no volvió tras caerse el stream: el llamante sigue esperando un acuse imposible")
		}

		if got == nil {
			t.Fatal("SendText con el stream caído devolvió nil")
		}
		if !streamCaidoDe(got) {
			t.Fatalf("StreamCaido() por duck-typing = false con el stream caído; err = %v", got)
		}
		// Y el command_id sigue viajando por su propio duck-typing: el operador necesita
		// los dos datos a la vez para poder preguntar después si el mensaje llegó a salir.
		var conID interface{ CommandID() string }
		if !errors.As(got, &conID) || conID.CommandID() == "" {
			t.Fatalf("el error del stream caído no expone command_id por duck-typing: %v", got)
		}
	})

	t.Run("plazo vencido contesta false", func(t *testing.T) {
		t.Parallel()
		reg := session.NewRegistry()
		release := reg.Register("s-muda", muteSender{})
		defer release()
		srv := gatewaygrpc.New(reg, logger.New(logger.WithWriter(io.Discard)),
			gatewaygrpc.WithAckTimeout(50*time.Millisecond))

		_, err := srv.SendText(context.Background(), "s-muda", "57301", "hola")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("el montaje no produjo un timeout: %v", err)
		}
		if streamCaidoDe(err) {
			t.Fatalf("StreamCaido() = true para un plazo vencido: el contrato no discrimina, y T2.4 "+
				"mapearía como caída de stream a un Edge que sigue conectado y solo va lento; err = %v", err)
		}
	})

	t.Run("sesión offline al empujar contesta false", func(t *testing.T) {
		t.Parallel()
		srv := gatewaygrpc.New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

		_, err := srv.SendText(context.Background(), "fantasma", "57301", "hola")
		if !errors.Is(err, session.ErrSessionOffline) {
			t.Fatalf("el montaje no produjo una sesión offline: %v", err)
		}
		// Este es el caso en que el mensaje NO salió (el Push falló), y por eso su
		// respuesta es 502 y no 504. Que StreamCaido() lo confundiera con la caída sería
		// afirmarle al llamante «no sabemos si le llegó» cuando sí se sabe: no llegó.
		if streamCaidoDe(err) {
			t.Fatalf("StreamCaido() = true para una sesión offline al empujar: err = %v", err)
		}
	})
}
