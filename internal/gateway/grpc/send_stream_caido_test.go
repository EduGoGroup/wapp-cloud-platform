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

// El contrato con los handlers HTTP es una interfaz ANÓNIMA, no un tipo compartido:
// eso es lo que permite que publicapi, platform/httpapi, flujos/admin e intakes
// reconozcan este error sin importar el paquete del Gateway (el desacople que
// argumenta httpapi/admin.go sobre commandIDFrom). El precio, que hasta ahora nadie
// pagaba explícitamente, es que **el compilador no verifica ese contrato en ningún
// punto**: renombrar aquí StreamCaido o CommandID compila sin queja, y los CINCO
// consumidores dejan de reconocer el error EN SILENCIO —errors.As devuelve false— de
// modo que el 504 «se cayó» vuelve a salir como el genérico y el command_id
// desaparece de las respuestas, sin un solo test en rojo.
//
// Estas dos líneas son el único sitio donde ese contrato se afirma. NO prueban
// comportamiento —de eso se encargan los tests de arriba—: prueban que los NOMBRES
// siguen siendo los que el otro lado busca. Si alguna deja de compilar, no la
// "arregles" cambiándola: hay cinco duck-typings ahí fuera que hay que mover con ella
// (grep de `CommandID() string` y `StreamCaido() bool` fuera de este paquete).
//
// Salió de la verificación por mutación de la Ola 2: romper el productor —hacer que
// awaitAck devolviera DeadlineExceeded, o que StreamCaido() mintiera siempre— dejaba
// las CUATRO superficies HTTP en verde, porque cada una prueba contra su propio doble.
// El agujero de CommandID() es anterior a esta ola y se cierra con la misma línea.
//
// El //nolint no tapa nada: aquí NO hay error que comprobar. errcheck ve un blank
// identifier a la izquierda de algo que satisface error —*SendError lo satisface— y lo
// toma por un descarte, cuando es la aserción de tipo canónica de Go. Escribirla de
// otra forma para contentar al linter le quitaría lo único que la hace útil: que sea
// el compilador quien la verifique.
var (
	//nolint:errcheck // aserción de tipo, no descarte de error: ver el bloque de arriba.
	_ interface{ StreamCaido() bool } = (*gatewaygrpc.SendError)(nil)
	//nolint:errcheck // aserción de tipo, no descarte de error: ver el bloque de arriba.
	_ interface{ CommandID() string } = (*gatewaygrpc.SendError)(nil)
)
