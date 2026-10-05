//go:build pendiente

package grpc

// El contrato de Connect, por su cara exportada (R-G1, R-G5, R-G12, R3.5.d): el bucle Recv
// del stream CloudLink. El test hace de Edge sobre un stream EN MEMORIA —sin red y sin TLS: la
// identidad mTLS llega como la deja grpc en el contexto del stream— y cada frame que manda se
// da por procesado cuando el bucle vuelve a pedir el siguiente. Aquí van el banco, el registro
// perezoso, el despacho por session_id y el cierre. El canal de control está en
// connect_control_test.go, el orden del calentamiento en connect_readiness_test.go, la
// reconexión y el drenaje en connect_reconnect_test.go, y el camino completo por un cable
// gRPC (bufconn) en connect_wire_test.go.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

const registeredLine = `msg="sesión CloudLink registrada"`

// forgedIdentity es el AuthInfo que grpc dejaría tras un handshake mTLS con el certificado de
// ese Edge: CN = edge_id, Organization[0] = tenant_id. No hay TLS: solo el sujeto.
func forgedIdentity(tenantID, edgeID string) credentials.AuthInfo {
	subject := pkix.Name{CommonName: edgeID}
	if tenantID != "" {
		subject.Organization = []string{tenantID}
	}
	return credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: subject}}}}
}

// memStream es el stream Connect en memoria. Recv entrega lo que el test manda; Send apunta lo
// que el gateway escribe (es una liveSession), cuenta los Send que se solapan y, si hay
// puerta, se queda dentro hasta que se abre.
type memStream struct {
	googlegrpc.ServerStream
	*liveSession
	ctx  context.Context
	in   chan *cloudlinkv1.EdgeToCloud
	idle chan struct{}
	end  chan error

	recvs    atomic.Int32
	inFlight atomic.Int32
	overlap  atomic.Bool
	gate     chan struct{}
	entered  chan struct{}
}

var _ googlegrpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge] = (*memStream)(nil)

func (s *memStream) Context() context.Context { return s.ctx }

func (s *memStream) Recv() (*cloudlinkv1.EdgeToCloud, error) {
	s.recvs.Add(1)
	for {
		select {
		case msg := <-s.in:
			return msg, nil
		case <-s.idle: // el test solo quería saber que el bucle volvió a pedir
		case err := <-s.end:
			return nil, err
		}
	}
}

func (s *memStream) Send(msg *cloudlinkv1.CloudToEdge) error {
	if s.inFlight.Add(1) != 1 {
		s.overlap.Store(true)
	}
	defer s.inFlight.Add(-1)
	if s.gate != nil {
		s.entered <- struct{}{}
		<-s.gate
	}
	return s.liveSession.Send(msg)
}

// memEdge es el Edge del test: un stream abierto contra srv.Connect, que corre en su goroutine.
type memEdge struct {
	*memStream
	// cancel termina el contexto del stream, como cuando el Edge se va a media conexión.
	cancel context.CancelFunc
	done   chan error
	hung   atomic.Bool
}

// openStream abre un stream cuyo peer presentó esa identidad (nil = sin TLS: anónimo) y deja
// Connect atendiéndolo. Si el test no lo cuelga, se cuelga solo al terminar.
func openStream(t *testing.T, srv *Server, identity credentials.AuthInfo) *memEdge {
	t.Helper()
	streamCtx, cancel := context.WithCancel(context.Background())
	if identity != nil {
		streamCtx = peer.NewContext(streamCtx, &peer.Peer{AuthInfo: identity})
	}
	e := &memEdge{
		memStream: &memStream{
			liveSession: &liveSession{id: "el stream del Edge"}, ctx: streamCtx,
			in: make(chan *cloudlinkv1.EdgeToCloud), idle: make(chan struct{}), end: make(chan error),
		},
		cancel: cancel,
		done:   make(chan error, 1),
	}
	go func() { e.done <- srv.Connect(e.memStream) }()
	t.Cleanup(func() {
		defer cancel()
		if !e.hung.Load() {
			e.hangUp(t)
		}
	})
	return e
}

// send entrega los frames al bucle Recv, uno a uno, y vuelve cuando el ÚLTIMO está procesado
// entero (encaminado y con su calentamiento de registro decidido).
func (e *memEdge) send(t *testing.T, frames ...*cloudlinkv1.EdgeToCloud) {
	t.Helper()
	for _, frame := range frames {
		select {
		case e.in <- frame:
		case <-time.After(watchdog):
			t.Fatal("colgado: el bucle Recv no pide el siguiente frame")
		}
	}
	e.settle(t)
}

// settle vuelve cuando el bucle está otra vez en Recv: todo lo inline del frame anterior ya
// ocurrió.
func (e *memEdge) settle(t *testing.T) {
	t.Helper()
	select {
	case e.idle <- struct{}{}:
	case <-time.After(watchdog):
		t.Fatal("colgado: el bucle Recv no volvió a pedir un frame")
	}
}

// closeWith termina el stream con ese error de Recv y devuelve lo que devolvió Connect, que
// para entonces ya cerró las sesiones y drenó el carril.
func (e *memEdge) closeWith(t *testing.T, recvErr error) error {
	t.Helper()
	e.hung.Store(true)
	select {
	case e.end <- recvErr:
	case <-time.After(watchdog):
		t.Fatal("colgado: el bucle Recv no recoge el fin del stream")
	}
	return await(t, e.done, "que Connect vuelva tras el fin del stream")
}

// hangUp cuelga como un Edge que se apaga con orden (EOF) y exige que Connect vuelva sin error.
func (e *memEdge) hangUp(t *testing.T) {
	t.Helper()
	if err := e.closeWith(t, io.EOF); err != nil {
		t.Fatalf("Connect devolvió %v al colgar el Edge, se esperaba nil", err)
	}
}

// letRun da ocasión de sobra a las demás goroutines, sin reloj: se usa antes de una aserción
// NEGATIVA de estado («esto no ha pasado»), que es cierta por construcción en el código bueno.
func letRun() {
	for range 200 {
		runtime.Gosched()
	}
}

// Connect devuelve nil cuando el Edge cierra con orden (EOF) y, si el stream se rompe, el
// error de Recv tal cual.
func TestConnectReturnsNilOnEOFAndTheStreamErrorOtherwise(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)

	openStream(t, rig.srv, nil).hangUp(t)

	broken := errors.New("el transporte se rompió")
	if err := openStream(t, rig.srv, nil).closeWith(t, broken); !errors.Is(err, broken) {
		t.Errorf("Connect devolvió %v, se esperaba el error del stream", err)
	}
}

// R-G5: cada session_id se registra con su PRIMER frame, sea cual sea —aquí un Pong—, y solo
// entonces: queda en el Registry, rastreado bajo la identidad del certificado, online en
// flota, y recibe su lease y su config POR ESTE stream. Repetir el id no vuelve a registrar.
func TestConnectRegistersEachSessionOnItsFirstFrame(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	first := phone("tenant-1", "edge-1", "s-1")

	edge.settle(t)
	if rig.reg.Count() != 0 || rig.tl.String() != "" {
		t.Fatalf("abrir el stream ya registró algo (%d sesiones, %q)", rig.reg.Count(), rig.tl.String())
	}

	edge.send(t, pongFrame("s-1", 1))
	if !rig.reg.Online("s-1") || sortedSessions(rig.srv, "tenant-1", "edge-1") != "s-1" || rig.row(t, first).State != fleet.StateOnline {
		t.Fatalf("tras su primer frame, s-1 no quedó registrada, rastreada y online")
	}
	frames := edge.received()
	if len(frames) != 4 {
		t.Fatalf("el Edge recibió %d frames, se esperaban el lease de s-1 y sus 3 configs", len(frames))
	}
	rig.requireInitialLease(t, frames[0], first)
	requireConfigs(t, frames[1:], "s-1", connectCfgs)
	requireLogHas(t, rig.log, "level=INFO", registeredLine, "session_id=s-1", "edge_id=edge-1", "tenant_id=tenant-1")

	edge.send(t, pongFrame("s-1", 2), pongFrame("s-1", 3))
	if n := len(edge.received()); n != 4 || strings.Count(rig.log.String(), registeredLine) != 1 || len(rig.audit.recorded()) != 1 {
		t.Fatalf("repetir el session_id volvió a registrar (%d frames, %d aperturas auditadas)", n, len(rig.audit.recorded()))
	}

}

// Una sesión nueva se suma EN CALIENTE al stream, con su propio lease y su config; y al colgar
// el Edge, TODAS las sesiones del stream quedan fuera del Registry, sin rastrear y offline.
func TestConnectHotJoinsASecondSessionAndClosesThemAll(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	first, second := phone("tenant-1", "edge-1", "s-1"), phone("tenant-1", "edge-1", "s-2")

	edge.send(t, pongFrame("s-1", 1), pongFrame("s-2", 1))
	if !rig.reg.Online("s-2") || sortedSessions(rig.srv, "tenant-1", "edge-1") != "s-1,s-2" || rig.row(t, second).State != fleet.StateOnline {
		t.Fatalf("la segunda sesión no se sumó en caliente")
	}
	frames := edge.received()
	if len(frames) != 8 || frames[4].GetLeaseUpdate() == nil || frames[4].GetSessionId() != "s-2" {
		t.Fatalf("s-2 no recibió su propio lease por el stream (%d frames)", len(frames))
	}
	requireConfigs(t, frames[5:], "s-2", connectCfgs)

	edge.hangUp(t)
	if rig.reg.Count() != 0 || len(rig.srv.edgeSessions) != 0 {
		t.Errorf("al colgar quedaron %d sesiones en el Registry y %v rastreadas", rig.reg.Count(), rig.srv.edgeSessions)
	}
	for _, cc := range []connCtx{first, second} {
		if got := rig.row(t, cc).State; got != fleet.StateOffline {
			t.Errorf("al colgar, %s quedó %q en flota; se esperaba offline", cc.sessionID, got)
		}
	}
}

// Cada frame se despacha bajo SU session_id, no bajo el de la primera sesión del stream; y un
// frame sin session_id se encamina igual (su Ack llega) sin registrar nada.
func TestConnectDispatchesEachFrameUnderItsOwnSessionID(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	var got []string
	rig.srv.OnIncoming = func(sessionID string, m *cloudlinkv1.IncomingMessage) {
		got = append(got, sessionID+"/"+m.GetWaMessageId())
	}
	waiting := seedAck(rig.srv, "cmd-1", "s-1")
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))

	edge.send(t, ackFrame("", "cmd-1"))
	if ack, _ := ackState(waiting); ack == nil {
		t.Error("el Ack de un frame sin session_id no se entregó")
	}
	if rig.reg.Count() != 0 || len(rig.srv.edgeSessions) != 0 || rig.tl.String() != "" {
		t.Fatalf("un frame sin session_id registró algo (%d sesiones, %q)", rig.reg.Count(), rig.tl.String())
	}

	edge.send(t,
		incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "a"}),
		incomingFrame("s-2", &cloudlinkv1.IncomingMessage{WaMessageId: "b"}),
		incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "c"}),
	)
	if want := "s-1/a,s-2/b,s-1/c"; strings.Join(got, ",") != want {
		t.Errorf("OnIncoming recibió %v, se esperaba %s", got, want)
	}
}

// Un stream SIN identidad mTLS degrada: sus sesiones entran en el Registry —se les puede
// empujar— y sus frames se encaminan, pero no hay a quién atribuir flota, lease, config,
// seguimiento ni calentamiento, ni al registrar ni al colgar.
func TestConnectWithoutIdentityOnlyRegistersInTheRegistry(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
	warmed := 0
	rig.srv.OnWarmup = func(string, string, string, string) { warmed++ }
	edge := openStream(t, rig.srv, nil)

	edge.send(t, heartbeatFrame("s-1", fullHeartbeat()), receiptFrame("s-1", "cmd-r"))
	if !rig.reg.Online("s-1") {
		t.Fatal("la sesión de un stream anónimo no entró en el Registry")
	}
	edge.hangUp(t)

	if rig.tl.String() != "" || len(rig.srv.edgeSessions) != 0 || len(rig.audit.recorded()) != 0 || warmed != 0 {
		t.Errorf("un stream anónimo dejó rastro: escrituras %q, seguimiento %v, auditoría %d, calentamientos %d",
			rig.tl.String(), rig.srv.edgeSessions, len(rig.audit.recorded()), warmed)
	}
	requireNothing(t, edge.liveSession)
	if len(rig.sink.got) != 1 {
		t.Errorf("el acuse de un stream anónimo no llegó al sink (%d)", len(rig.sink.got))
	}
	if rig.reg.Online("s-1") {
		t.Error("al colgar, la sesión siguió en el Registry")
	}
}

// El camino de vuelta de un envío: lo que la nube empuja a una sesión sale por el stream que
// la registró, y el Ack que el Edge manda por ese stream desbloquea al que esperaba.
func TestConnectCarriesACommandOutAndItsAckBack(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame("s-1", 1))
	pushed := make(chan string, 1)
	edge.onSend = func() { pushed <- "" }

	sent := goSend(context.Background(), rig.srv, sendKinds[0].send, "s-1")
	await(t, pushed, "que el SendText salga por el stream")
	frames := edge.received()
	cmd := frames[len(frames)-1]
	if cmd.GetSendText() == nil || cmd.GetSessionId() != "s-1" {
		t.Fatalf("lo último que recibió el Edge no es el SendText de s-1: %v", cmd)
	}
	edge.send(t, ackFrame("s-1", cmd.GetCommandId()))

	if res := await(t, sent, "que el envío reciba su Ack"); res.err != nil || res.ack.GetAckedCommandId() != cmd.GetCommandId() {
		t.Errorf("el envío volvió con (%v, %v), se esperaba el Ack de su comando", res.ack, res.err)
	}
}

// R-G12: todas las sesiones de un stream escriben por UN mismo candado. Con un empuje a s-1
// parado DENTRO del Send, el empuje a s-2 no entra hasta que el primero sale: grpc-go prohíbe
// dos SendMsg a la vez sobre un stream.
func TestConnectSerializesEverySessionOnTheOneStream(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame("s-1", 1), pongFrame("s-2", 1))
	edge.entered = make(chan struct{}, 2)
	edge.gate = make(chan struct{})

	ping := func(sessionID string) <-chan error {
		done := make(chan error, 1)
		go func() { done <- rig.srv.Ping(context.Background(), sessionID, 7) }()
		return done
	}
	first := ping("s-1")
	await(t, edge.entered, "que el primer empuje entre en el Send")
	second := ping("s-2")
	letRun()
	close(edge.gate)

	if err := await(t, first, "el primer empuje"); err != nil {
		t.Errorf("Ping(s-1) = %v", err)
	}
	if err := await(t, second, "el segundo empuje"); err != nil {
		t.Errorf("Ping(s-2) = %v", err)
	}
	if edge.overlap.Load() {
		t.Fatal("dos sesiones del mismo stream escribieron a la vez: no comparten el candado del stream")
	}
}
