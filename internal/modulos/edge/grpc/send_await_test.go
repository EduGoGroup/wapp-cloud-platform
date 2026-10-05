package grpc

// Los caminos de SendText/SendMedia que necesitan un Ack o la caída del stream (R-G11). Nace
// con el verde: el Ack se entrega por deliverAck y el cierre se simula con
// cancelSessionAcks, que es exactamente lo que hace closeStream con la sesión que se queda
// sin stream (la condición «¿sigue online en otro stream?» es de closeStream y se afirma con
// connect.go).

import (
	"context"
	"errors"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// funcSender adapta una función al puerto session.Sender.
type funcSender func(*cloudlinkv1.CloudToEdge) error

func (f funcSender) Send(msg *cloudlinkv1.CloudToEdge) error { return f(msg) }

// sendFunc es SendText o SendMedia vistos igual: empujan a la sesión y esperan su Ack.
type sendFunc func(ctx context.Context, srv *Server, sessionID string) (*cloudlinkv1.Ack, error)

var sendKinds = []struct {
	name string
	send sendFunc
}{
	{"SendText", func(ctx context.Context, srv *Server, sid string) (*cloudlinkv1.Ack, error) {
		return srv.SendText(ctx, sid, "57301", "hola")
	}},
	{"SendMedia", func(ctx context.Context, srv *Server, sid string) (*cloudlinkv1.Ack, error) {
		return srv.SendMedia(ctx, sid, "57301", "https://x.example/o", "a.pdf", "application/pdf", "", "document")
	}},
}

// inFlight registra en reg una sesión cuyo Edge recibe el comando y avisa por el canal
// devuelto con el command_id de cada frame. No acusa: eso lo decide el test.
func inFlight(t *testing.T, reg *session.Registry, sessionID string) <-chan string {
	t.Helper()
	pushed := make(chan string, 8)
	t.Cleanup(reg.Register(sessionID, funcSender(func(msg *cloudlinkv1.CloudToEdge) error {
		pushed <- msg.GetCommandId()
		return nil
	})))
	return pushed
}

type sendResult struct {
	ack *cloudlinkv1.Ack
	err error
}

// goSend lanza el envío y devuelve por dónde llega su resultado.
func goSend(ctx context.Context, srv *Server, send sendFunc, sessionID string) <-chan sendResult {
	res := make(chan sendResult, 1)
	go func() {
		ack, err := send(ctx, srv, sessionID)
		res <- sendResult{ack: ack, err: err}
	}()
	return res
}

// R-G11: el envío devuelve EL Ack del Edge, correlacionado por command_id, sin comérselo. Un
// Ack con Ok=false también vuelve como Ack y sin error: «acusado» no es «entregado».
func TestSendReturnsTheAckOfTheEdge(t *testing.T) {
	t.Parallel()
	for _, kind := range sendKinds {
		for name, ok := range map[string]bool{"acked ok": true, "acked not ok": false} {
			t.Run(kind.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				reg := session.NewRegistry()
				srv := New(reg, quietLog()) // plazo de producción: 8 s; el Ack llega antes
				pushed := inFlight(t, reg, "s-1")
				res := goSend(context.Background(), srv, kind.send, "s-1")

				cmdID := await(t, pushed, "el comando llega al Edge")
				if ids := pendingAckIDs(srv); len(ids) != 1 || ids[0] != cmdID {
					t.Fatalf("con el envío en vuelo la correlación tiene %v, se esperaba [%s]", ids, cmdID)
				}
				ack := &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: ok, Error: "motivo del Edge"}
				srv.deliverAck(ack)

				got := await(t, res, "el envío vuelve con su Ack")
				if got.err != nil {
					t.Fatalf("envío acusado = error %v", got.err)
				}
				if got.ack != ack {
					t.Fatalf("el envío devolvió %v, se esperaba el Ack del Edge", got.ack)
				}
				requireNoPendingAcks(t, srv)
			})
		}
	}
}

// R-G11: los caminos de salida dejan la correlación de acuses VACÍA, termine como termine el
// envío: sesión sin stream, plazo propio vencido o llamante que se rinde con el envío en
// vuelo —que sale por el empuje o por la espera, según dónde lo pille—. (El Ack recibido y
// el stream caído lo afirman sus propios tests.)
func TestEveryExitLeavesNoPendingAck(t *testing.T) {
	t.Parallel()
	for _, kind := range sendKinds {
		t.Run(kind.name+"/offline", func(t *testing.T) {
			t.Parallel()
			srv := New(session.NewRegistry(), quietLog())
			if _, err := kind.send(context.Background(), srv, "ghost"); !errors.Is(err, session.ErrSessionOffline) {
				t.Fatalf("envío a una sesión sin stream = %v", err)
			}
			requireNoPendingAcks(t, srv)
		})
		t.Run(kind.name+"/own clock", func(t *testing.T) {
			t.Parallel()
			srv, _, _ := muteServer(t, "s-1")
			if _, err := kind.send(context.Background(), srv, "s-1"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("envío sin Ack = %v", err)
			}
			requireNoPendingAcks(t, srv)
		})
		t.Run(kind.name+"/caller gives up while waiting", func(t *testing.T) {
			t.Parallel()
			reg := session.NewRegistry()
			srv := New(reg, quietLog()) // 8 s de plazo: quien corta es el llamante
			pushed := inFlight(t, reg, "s-1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := goSend(ctx, srv, kind.send, "s-1")

			cmdID := await(t, pushed, "el comando llega al Edge")
			cancel()

			got := await(t, res, "el envío vuelve cuando el llamante se rinde")
			if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, ErrStreamClosed) {
				t.Fatalf("envío con el llamante rendido = %v, se esperaba su context.Canceled", got.err)
			}
			if se := asSendError(t, got.err); se.CommandID() != cmdID || se.StreamCaido() {
				t.Errorf("el error no lleva el command_id del envío (%s): %v", cmdID, got.err)
			}
			requireNoPendingAcks(t, srv)
		})
	}
}

// awaitAck atiende al ctx del LLAMANTE además de a su reloj: con el llamante ya rendido y
// sin Ack, vuelve en el acto con su ctx.Err() envuelto —no espera los 8 s— y deja rastro a
// nivel de comando. Se llama directo porque, por SendText, el llamante que se rinde justo
// tras el empuje puede salir por el empuje o por la espera, y solo la espera deja esta línea.
func TestAwaitAckGivesUpWithTheCaller(t *testing.T) {
	t.Parallel()
	log, logs := capturedLog()
	srv := New(session.NewRegistry(), log)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ack, err := srv.awaitAck(ctx, make(chan *cloudlinkv1.Ack, 1), "cmd-1", "s-1")

	if ack != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitAck con el llamante rendido = (%v, %v), se esperaba su context.Canceled", ack, err)
	}
	if se := asSendError(t, err); se.CommandID() != "cmd-1" || se.SessionID() != "s-1" || se.StreamCaido() {
		t.Errorf("SendError inesperado: %v", err)
	}
	for _, want := range []string{
		"gateway: se agotó la espera del ack del Edge", "command_id=cmd-1", "session_id=s-1", "ack_timeout=8s",
	} {
		if !logs.contains(want) {
			t.Errorf("la espera abandonada no dejó %q en el log: %s", want, logs.String())
		}
	}
}

// R-G11: la caída del stream despierta EN EL ACTO al envío en vuelo. El plazo del Ack es el
// de producción (8 s) y el watchdog del test es menor: si la cancelación no funcionara, el
// test no fallaría por una aserción sino por quedarse esperando — que es exactamente lo que le
// pasaba al llamante HTTP. El error se distingue del plazo vencido, también por duck-typing.
func TestStreamCloseWakesTheInFlightSendAtOnce(t *testing.T) {
	t.Parallel()
	for _, kind := range sendKinds {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			reg := session.NewRegistry()
			log, logs := capturedLog()
			srv := New(reg, log, WithAckTimeout(defaultAckTimeout))
			pushed := inFlight(t, reg, "s-1")
			res := goSend(context.Background(), srv, kind.send, "s-1")
			cmdID := await(t, pushed, "el comando llega al Edge")

			if n := srv.cancelSessionAcks("s-1"); n != 1 {
				t.Fatalf("el cierre canceló %d envíos, se esperaba 1", n)
			}

			got := await(t, res, "el envío en vuelo se rinde en el acto tras el cierre")
			if got.ack != nil {
				t.Fatalf("el envío cancelado devolvió un Ack: %v", got.ack)
			}
			if !errors.Is(got.err, ErrStreamClosed) {
				t.Fatalf("errors.Is(err, ErrStreamClosed) = false; err = %v", got.err)
			}
			if errors.Is(got.err, context.DeadlineExceeded) {
				t.Fatalf("el error de cierre se confunde con el del plazo vencido: %v", got.err)
			}
			// Como lo ven los handlers HTTP: por interfaces anónimas, sin importar este paquete.
			var fallen interface{ StreamCaido() bool }
			if !errors.As(got.err, &fallen) || !fallen.StreamCaido() {
				t.Fatalf("StreamCaido() por duck-typing = false con el stream caído; err = %v", got.err)
			}
			var withID interface{ CommandID() string }
			if !errors.As(got.err, &withID) || withID.CommandID() != cmdID {
				t.Fatalf("el error del stream caído no lleva el command_id del envío (%s): %v", cmdID, got.err)
			}
			if se := asSendError(t, got.err); se.SessionID() != "s-1" {
				t.Errorf("SessionID() = %q, se esperaba s-1", se.SessionID())
			}
			// A nivel de COMANDO: el Warn agregado dice cuántos cayeron; este dice cuáles.
			if !logs.contains("gateway: el ack se canceló porque el stream de la sesión cayó") || !logs.contains("command_id="+cmdID) {
				t.Errorf("la cancelación no dejó rastro con su command_id: %q", logs.String())
			}
			requireNoPendingAcks(t, srv)
		})
	}
}

// R-G11: el cierre de una sesión NO cancela los envíos de otra. La aserción que importa es
// la negativa, y se hace sin reloj: tras caer s-2, los envíos de las demás sesiones siguen
// pudiendo recibir SU Ack (si los hubieran cancelado, volverían con ErrStreamClosed).
func TestClosingOneSessionDoesNotCancelTheSendsOfAnother(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, quietLog())
	sessions := []string{"s-1", "s-2", "s-3", "s-4"}
	results := make(map[string]<-chan sendResult)
	cmdIDs := make(map[string]string)
	for _, sid := range sessions {
		pushed := inFlight(t, reg, sid)
		results[sid] = goSend(context.Background(), srv, sendKinds[0].send, sid)
		cmdIDs[sid] = await(t, pushed, "el comando de "+sid+" llega a su Edge")
	}

	srv.cancelSessionAcks("s-2")

	if got := await(t, results["s-2"], "el envío de la sesión caída se rinde"); !errors.Is(got.err, ErrStreamClosed) {
		t.Fatalf("el envío de la sesión que SÍ cayó no se canceló: %v", got.err)
	}
	for _, sid := range []string{"s-1", "s-3", "s-4"} {
		ack := &cloudlinkv1.Ack{AckedCommandId: cmdIDs[sid], Ok: true}
		srv.deliverAck(ack)
		got := await(t, results[sid], "el envío de "+sid+" recibe su Ack")
		if got.err != nil || got.ack != ack {
			t.Fatalf("el envío de %s murió al caer el stream de s-2: (%v, %v)", sid, got.ack, got.err)
		}
	}
	requireNoPendingAcks(t, srv)
}

// Un Ack de OTRO comando no despierta a este envío: sigue esperando el suyo.
func TestAnAckForAnotherCommandDoesNotWakeTheSend(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, quietLog())
	pushed := inFlight(t, reg, "s-1")
	res := goSend(context.Background(), srv, sendKinds[0].send, "s-1")
	cmdID := await(t, pushed, "el comando llega al Edge")

	srv.deliverAck(&cloudlinkv1.Ack{AckedCommandId: "another-command", Ok: true})
	if ids := pendingAckIDs(srv); len(ids) != 1 || ids[0] != cmdID {
		t.Fatalf("un Ack ajeno alteró la correlación: %v", ids)
	}

	mine := &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true}
	srv.deliverAck(mine)
	if got := await(t, res, "el envío vuelve con SU Ack"); got.err != nil || got.ack != mine {
		t.Fatalf("el envío devolvió (%v, %v), se esperaba su Ack", got.ack, got.err)
	}
}

// El ctx del llamante acota TAMBIÉN el empuje, no solo la espera: contra un Edge que no lee
// su stream, el llamante que se rinde vuelve en el acto con session.ErrPushAbandoned (y su
// ctx.Err()), sin esperar al techo del Registry. Vale para los envíos y para Ping.
func TestCallerContextBoundsThePush(t *testing.T) {
	t.Parallel()
	stuck := func(t *testing.T) (*Server, <-chan struct{}) {
		t.Helper()
		reg := session.NewRegistry()
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		t.Cleanup(reg.Register("s-1", funcSender(func(*cloudlinkv1.CloudToEdge) error {
			entered <- struct{}{}
			<-release
			return nil
		})))
		return New(reg, quietLog()), entered
	}

	for _, kind := range sendKinds {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			srv, entered := stuck(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := goSend(ctx, srv, kind.send, "s-1")
			await(t, entered, "el empuje entra en el Edge atascado")
			cancel()

			got := await(t, res, "el envío vuelve cuando el llamante se rinde empujando")
			if !errors.Is(got.err, session.ErrPushAbandoned) || !errors.Is(got.err, context.Canceled) {
				t.Fatalf("envío abandonado al empujar = %v, se esperaba ErrPushAbandoned con context.Canceled", got.err)
			}
			if se := asSendError(t, got.err); !uuidV4.MatchString(se.CommandID()) || se.StreamCaido() {
				t.Errorf("SendError inesperado: %v", got.err)
			}
			requireNoPendingAcks(t, srv)
		})
	}
	t.Run("Ping", func(t *testing.T) {
		t.Parallel()
		srv, entered := stuck(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res := make(chan error, 1)
		go func() { res <- srv.Ping(ctx, "s-1", 7) }()
		await(t, entered, "el empuje del Ping entra en el Edge atascado")
		cancel()

		if err := await(t, res, "Ping vuelve cuando el llamante se rinde"); !errors.Is(err, session.ErrPushAbandoned) {
			t.Fatalf("Ping abandonado al empujar = %v, se esperaba ErrPushAbandoned", err)
		}
	})
}

// sendErr envuelve sin ramificar: con causa da un *SendError que la lleva; con err nil
// devuelve un error NIL de verdad (no un *SendError nil metido en la interfaz).
func TestSendErrWrapsOnlyRealErrors(t *testing.T) {
	t.Parallel()
	if err := sendErr("cmd-1", "s-1", nil); err != nil {
		t.Fatalf("sendErr(nil) = %#v, se esperaba nil", err)
	}
	cause := errors.New("causa")
	se := asSendError(t, sendErr("cmd-1", "s-1", cause))
	if se.CommandID() != "cmd-1" || se.SessionID() != "s-1" || !errors.Is(se, cause) {
		t.Fatalf("sendErr no conserva command_id, sesión y causa: %v", se)
	}
}
