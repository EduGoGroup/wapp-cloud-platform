package grpc

// Lo que hay EN VUELO (D-F3-13, hallazgo 81 de F3): InFlight cuenta los envíos que esperan Ack
// y las inferencias que esperan resultado, y vuelve a 0 por cada camino de salida —respuesta,
// reloj propio, stream caído—. La caída del stream se simula con cancelSessionAcks y
// cancelSessionInfers, que es lo que hace closeStream con la sesión que se queda sin stream
// (igual que send_await_test.go).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// requireInFlight afirma la cifra de InFlight en este instante.
func requireInFlight(t *testing.T, srv *Server, want int, when string) {
	t.Helper()
	if got := srv.InFlight(); got != want {
		t.Fatalf("InFlight() %s = %d, se esperaba %d", when, got, want)
	}
}

// inferOf arma una petición de inferencia mínima con ese presupuesto.
func inferOf(timeout time.Duration) InferRequest {
	return InferRequest{Prompt: "p", Timeout: timeout}
}

// En reposo no hay nada en vuelo, con sesiones vivas o sin ellas: una sesión conectada NO es
// una petición esperando.
func TestInFlightIsZeroAtRest(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	requireInFlight(t, srv, 0, "recién construido")

	rig := newInferRig(t)
	rig.live(t, "t-1", "e-1", "s-1", nil)
	requireInFlight(t, rig.srv, 0, "con una sesión viva y nada pedido")
}

// Un envío cuenta mientras espera su Ack —SendText y SendMedia por igual— y deja de contar
// cuando el Ack llega.
func TestInFlightCountsASendUntilItsAck(t *testing.T) {
	t.Parallel()
	for _, kind := range sendKinds {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			reg := session.NewRegistry()
			srv := New(reg, quietLog())
			pushed := inFlight(t, reg, "s-1")
			res := goSend(context.Background(), srv, kind.send, "s-1")

			cmdID := await(t, pushed, "el comando llega al Edge")
			requireInFlight(t, srv, 1, "con el envío esperando Ack")

			srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true})
			if got := await(t, res, "el envío vuelve con su Ack"); got.err != nil {
				t.Fatalf("envío acusado = error %v", got.err)
			}
			requireInFlight(t, srv, 0, "con el envío acusado")
		})
	}
}

// Una inferencia cuenta mientras espera su resultado y deja de contar cuando llega.
func TestInFlightCountsAnInferenceUntilItsResult(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := rig.live(t, "t-1", "e-1", "s-1", nil) // no contesta: lo decide el test
	res := goInfer(context.Background(), rig.srv, "t-1", inferOf(time.Minute))

	cmdID := await(t, edge.pushed, "la inferencia llega al Edge")
	requireInFlight(t, rig.srv, 1, "con la inferencia esperando resultado")

	rig.srv.deliverInference(sealedResult(cmdID, rig.seal(t, `{"ok":true}`)))
	if got := await(t, res, "la inferencia vuelve con su resultado"); got.err != nil {
		t.Fatalf("inferencia contestada = error %v", got.err)
	}
	requireInFlight(t, rig.srv, 0, "con la inferencia contestada")
}

// Las dos correlaciones SUMAN: dos envíos y tres inferencias son cinco, y retirar de una no
// descuenta de la otra. Sembrado directo en los mapas, que es lo que InFlight dice leer.
func TestInFlightAddsSendsAndInferences(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	seedGrid(srv, []string{"s-1"}, 2)
	requireInFlight(t, srv, 2, "con dos envíos sembrados")
	seedInferGrid(srv, []string{"s-1"}, 3)
	requireInFlight(t, srv, 5, "con dos envíos y tres inferencias")

	srv.clearAck("s-1/0")
	requireInFlight(t, srv, 4, "tras retirar un envío")
	srv.clearInfer("s-1/0")
	srv.clearInfer("s-1/1")
	requireInFlight(t, srv, 2, "tras retirar dos inferencias")
}

// Lo que expira por su reloj propio deja de contar: un envío sin Ack y una inferencia sin
// resultado no se quedan «en vuelo» para siempre.
func TestInFlightDropsWhatExpires(t *testing.T) {
	t.Parallel()
	t.Run("send", func(t *testing.T) {
		t.Parallel()
		srv, _, _ := muteServer(t, "s-1") // plazo de Ack de 1 ns
		if _, err := srv.SendText(context.Background(), "s-1", "57301", "hola"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("envío sin Ack = %v, se esperaba el plazo vencido", err)
		}
		requireInFlight(t, srv, 0, "con el envío expirado")
	})
	t.Run("inference", func(t *testing.T) {
		t.Parallel()
		rig := newInferRig(t)
		rig.srv.inferGrace = time.Nanosecond
		rig.live(t, "t-1", "e-1", "s-1", nil)
		_, err := rig.srv.Infer(context.Background(), "t-1", inferOf(time.Nanosecond))
		if ie := asInferError(t, err); ie.Motivo() != ReasonTimeout {
			t.Fatalf("inferencia sin resultado = %v, se esperaba el motivo %s", err, ReasonTimeout)
		}
		requireInFlight(t, rig.srv, 0, "con la inferencia expirada")
	})
}

// La caída del stream de una sesión descuenta LO SUYO y nada más: lo de otra sesión sigue en
// vuelo.
func TestInFlightDropsWhatTheFallenStreamCarried(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	sessions := []string{"s-1", "s-2"}
	seedGrid(srv, sessions, 2)
	seedInferGrid(srv, sessions, 3)
	requireInFlight(t, srv, 10, "con dos sesiones cargadas")

	srv.cancelSessionAcks("s-1")
	requireInFlight(t, srv, 8, "caídos los envíos de s-1")
	srv.cancelSessionInfers("s-1")
	requireInFlight(t, srv, 5, "caídas las inferencias de s-1")
	srv.cancelSessionAcks("s-2")
	srv.cancelSessionInfers("s-2")
	requireInFlight(t, srv, 0, "caídas las dos sesiones")
}

// InFlight se puede leer mientras nacen, se resuelven y caen envíos e inferencias: -race no
// tiene nada que decir, la cifra nunca es negativa y al acabar todo vuelve a 0.
func TestInFlightIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	rig.srv.ackTimeout = time.Nanosecond
	rig.srv.inferGrace = time.Nanosecond
	// Un Edge que traga todo sin avisar a nadie: el de rig.live avisa por un canal con tope, y
	// aquí salen más frames de los que nadie va a leer.
	t.Cleanup(rig.reg.Register("s-1", funcSender(func(*cloudlinkv1.CloudToEdge) error { return nil })))
	rig.srv.trackSession(phone("t-1", "e-1", "s-1"))

	const rounds = 50
	var writers sync.WaitGroup
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := rig.srv.InFlight(); n < 0 {
				t.Errorf("InFlight() = %d: nunca puede ser negativo", n)
				return
			}
		}
	}()
	// Los resultados no importan —cada uno acaba por su reloj o por la caída, según le pille—:
	// lo que se afirma es la cifra mientras tanto y al final.
	for range rounds {
		writers.Go(func() {
			if _, err := rig.srv.SendText(context.Background(), "s-1", "57301", "hola"); err == nil {
				t.Error("un envío que nadie acusa volvió sin error")
			}
		})
		writers.Go(func() {
			if _, err := rig.srv.Infer(context.Background(), "t-1", inferOf(time.Nanosecond)); err == nil {
				t.Error("una inferencia que nadie contesta volvió sin error")
			}
		})
		writers.Go(func() { rig.srv.cancelSessionAcks("s-1"); rig.srv.cancelSessionInfers("s-1") })
	}
	writers.Wait()
	close(stop)
	await(t, readerDone, "el lector termina")
	requireInFlight(t, rig.srv, 0, "con todo resuelto")
}
