package grpc

// El camino Connect COMPLETO por un cable gRPC de verdad (bufconn, en memoria): un gateway y
// varios Edge conectados A LA VEZ, cada uno por su conexión. Es lo único que aquí pasa por el
// servidor gRPC; sin red y sin TLS —la identidad mTLS de cada conexión la pone un handshake de
// mentira, tal como la dejaría el de verdad (ese es de F3-05)—.
//
// Las aserciones negativas («a este Edge no le llegó nada ajeno») no esperan un rato: el Edge
// cuelga, el gateway drena y cierra, y se mira TODO lo que recibió hasta el fin del stream.

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// forgedHandshake es la credencial de transporte del servidor de test: no cifra nada y a cada
// conexión entrante le pone la identidad que el test dejó en cola antes de marcar.
type forgedHandshake struct {
	next chan credentials.AuthInfo
}

func (h forgedHandshake) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	select {
	case identity := <-h.next:
		return conn, identity, nil
	default:
		return nil, nil, errors.New("conexión que el test no anunció")
	}
}

func (forgedHandshake) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("credencial solo de servidor")
}

func (forgedHandshake) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "forged"}
}
func (h forgedHandshake) Clone() credentials.TransportCredentials { return h }
func (forgedHandshake) OverrideServerName(string) error           { return nil }

// wire es UN gateway servido por gRPC sobre bufconn.
type wire struct {
	lis       *bufconn.Listener
	handshake forgedHandshake
}

func newWire(t *testing.T, srv *Server) *wire {
	t.Helper()
	w := &wire{lis: bufconn.Listen(1 << 20), handshake: forgedHandshake{next: make(chan credentials.AuthInfo, 1)}}
	gs := googlegrpc.NewServer(googlegrpc.Creds(w.handshake))
	srv.Register(gs)
	served := make(chan error, 1)
	go func() { served <- gs.Serve(w.lis) }()
	t.Cleanup(func() {
		gs.Stop()
		if err := await(t, served, "que el servidor gRPC termine"); err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return w
}

// wireEdge es un Edge conectado: su stream y un lector que apunta lo que el gateway le manda.
type wireEdge struct {
	name   string
	stream googlegrpc.BidiStreamingClient[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]
	frames chan *cloudlinkv1.CloudToEdge
	seen   []*cloudlinkv1.CloudToEdge
	ended  chan error
}

// dial conecta un Edge con esa identidad y abre su stream Connect.
func (w *wire) dial(t *testing.T, tenantID, edgeID string) *wireEdge {
	t.Helper()
	w.handshake.next <- forgedIdentity(tenantID, edgeID)
	conn, err := googlegrpc.NewClient("passthrough:///bufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return w.lis.DialContext(ctx) }),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("cerrando la conexión de %s: %v", edgeID, err)
		}
	})
	stream, err := cloudlinkv1.NewCloudLinkClient(conn).Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect de %s: %v", edgeID, err)
	}
	e := &wireEdge{name: edgeID, stream: stream, frames: make(chan *cloudlinkv1.CloudToEdge, 64), ended: make(chan error, 1)}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				close(e.frames)
				e.ended <- err
				return
			}
			e.frames <- msg
		}
	}()
	return e
}

func (e *wireEdge) send(t *testing.T, frames ...*cloudlinkv1.EdgeToCloud) {
	t.Helper()
	for _, frame := range frames {
		if err := e.stream.Send(frame); err != nil {
			t.Fatalf("%s: Send: %v", e.name, err)
		}
	}
}

// until lee frames hasta el primero que cumple match y lo devuelve; todo lo leído queda en seen.
func (e *wireEdge) until(t *testing.T, what string, match func(*cloudlinkv1.CloudToEdge) bool) *cloudlinkv1.CloudToEdge {
	t.Helper()
	for {
		frame := await(t, e.frames, e.name+" espera "+what)
		if frame == nil {
			t.Fatalf("%s: el stream terminó esperando %s", e.name, what)
		}
		e.seen = append(e.seen, frame)
		if match(frame) {
			return frame
		}
	}
}

func answerTo(cmdID string) func(*cloudlinkv1.CloudToEdge) bool {
	return func(f *cloudlinkv1.CloudToEdge) bool {
		return f.GetUserAuthResponse() != nil && f.GetCommandId() == cmdID
	}
}

// hangUp cierra el envío, espera a que el gateway termine el stream SIN error y devuelve TODO
// lo que este Edge recibió en su vida.
func (e *wireEdge) hangUp(t *testing.T) []*cloudlinkv1.CloudToEdge {
	t.Helper()
	if err := e.stream.CloseSend(); err != nil {
		t.Fatalf("%s: CloseSend: %v", e.name, err)
	}
	if err := await(t, e.ended, "que el gateway cierre el stream de "+e.name); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: el stream terminó con %v, se esperaba un cierre limpio", e.name, err)
	}
	for frame := range e.frames {
		e.seen = append(e.seen, frame)
	}
	return e.seen
}

// De punta a punta con un teléfono: la identidad sale del certificado de la conexión, la
// sesión recibe su lease por el cable, un envío de la nube va y su Ack vuelve, y al colgar el
// Edge la sesión queda fuera del Registry y offline en flota.
func TestWireCarriesASessionEndToEnd(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	edge := newWire(t, rig.srv).dial(t, "tenant-1", "edge-1")
	cc := phone("tenant-1", "edge-1", "s-1")

	edge.send(t, pongFrame("s-1", 1))
	lease := edge.until(t, "su lease inicial", func(f *cloudlinkv1.CloudToEdge) bool { return f.GetLeaseUpdate() != nil })
	rig.requireInitialLease(t, lease, cc)
	if row := rig.row(t, cc); row.State != fleet.StateOnline {
		t.Fatalf("fila de flota en %q, se esperaba online bajo la identidad del certificado", row.State)
	}

	sent := goSend(t.Context(), rig.srv, sendKinds[0].send, "s-1")
	cmd := edge.until(t, "el SendText", func(f *cloudlinkv1.CloudToEdge) bool { return f.GetSendText() != nil })
	edge.send(t, ackFrame("s-1", cmd.GetCommandId()))
	if res := await(t, sent, "que el envío reciba su Ack"); res.err != nil || !res.ack.GetOk() {
		t.Fatalf("el envío volvió con (%v, %v)", res.ack, res.err)
	}

	edge.hangUp(t)
	if rig.reg.Online("s-1") || rig.row(t, cc).State != fleet.StateOffline {
		t.Error("al colgar el Edge la sesión no quedó fuera del Registry y offline")
	}
}

// R-G9, R3.4.a con DOS CONEXIONES reales: los dos Edge hacen login por el canal de control
// —el mismo session_id en los dos— y cada uno recibe SU respuesta, sean de la misma empresa o
// de empresas distintas. El segundo pide después del primero (con el Registry de por medio
// quedaba él como destinatario) y el primero vuelve a pedir; y apagar uno no deja al otro sin
// auth.
func TestWireTwoEdgesEachGetTheirOwnAuthAnswer(t *testing.T) {
	t.Parallel()
	for name, tenants := range map[string][2]string{
		"same tenant":       {"tenant-a", "tenant-a"},
		"different tenants": {"tenant-a", "tenant-b"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newRouteRig(t)
			w := newWire(t, rig.srv)
			first, second := w.dial(t, tenants[0], "edge-1"), w.dial(t, tenants[1], "edge-2")

			first.send(t, loginFrame(control, "cmd-1-a"))
			first.until(t, "su primer login", answerTo("cmd-1-a"))
			second.send(t, loginFrame(control, "cmd-2-a"))
			second.until(t, "su login", answerTo("cmd-2-a"))
			first.send(t, loginFrame(control, "cmd-1-b"))
			first.until(t, "su segundo login", answerTo("cmd-1-b"))
			firstGot := first.hangUp(t)
			second.send(t, refreshFrame(control, "cmd-2-b", "refresh-"+tenants[1]))
			second.until(t, "su refresh, con el otro Edge ya apagado", answerTo("cmd-2-b"))
			secondGot := second.hangUp(t)

			for _, tc := range []struct {
				got      []*cloudlinkv1.CloudToEdge
				tenantID string
				cmdIDs   []string
			}{{firstGot, tenants[0], []string{"cmd-1-a", "cmd-1-b"}}, {secondGot, tenants[1], []string{"cmd-2-a", "cmd-2-b"}}} {
				if len(tc.got) != len(tc.cmdIDs) {
					t.Fatalf("un Edge de %s recibió %d frames, se esperaban solo sus %d respuestas", tc.tenantID, len(tc.got), len(tc.cmdIDs))
				}
				for i, cmdID := range tc.cmdIDs {
					resp := requireAuthResponse(t, tc.got[i], controlChannel("", "", nil), cmdID)
					requireTokens(t, resp, tokensFor(tc.tenantID, "user-1"))
				}
			}
			if rig.reg.Count() != 0 {
				t.Errorf("el canal de control dejó %d entradas en el Registry", rig.reg.Count())
			}
		})
	}
}

// R3.4.b y R3.4.c de extremo a extremo. Un Edge recién instalado, sin ningún teléfono, recibe
// el catálogo de SU tenant por el cable con solo hacer login (c). Y cuando otra empresa
// publica su catálogo, PushConfig llega al teléfono de esa empresa y NO al canal de control de
// nadie (b): ni al del Edge ajeno, ni al del propio.
func TestWireConfigReachesAPhonelessEdgeAndPushConfigNeverItsControlChannel(t *testing.T) {
	t.Parallel()
	perTenant := providerFunc(func(_ context.Context, tenantID string) ([]ConfigPayload, error) {
		return []ConfigPayload{{Kind: "intents", Version: "v1", Payload: []byte("catalog-of-" + tenantID)}}, nil
	})
	rig := newRouteRig(t, WithConfigProvider(perTenant))
	w := newWire(t, rig.srv)
	withPhone, phoneless := w.dial(t, "tenant-a", "edge-a"), w.dial(t, "tenant-b", "edge-b")

	withPhone.send(t, loginFrame(control, "cmd-a"), pongFrame("s-phone", 1))
	withPhone.until(t, "la config de su teléfono", func(f *cloudlinkv1.CloudToEdge) bool {
		return f.GetConfigUpdate() != nil && f.GetSessionId() == "s-phone"
	})
	phoneless.send(t, loginFrame(control, "cmd-b")) // conecta después: con última-gana, la clave de control sería suya
	phoneless.until(t, "su login", answerTo("cmd-b"))

	if err := pushIntents(t.Context(), rig.srv, "tenant-a"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	type cfg struct{ sessionID, payload string }
	configsOf := func(frames []*cloudlinkv1.CloudToEdge) []cfg {
		var out []cfg
		for _, f := range frames {
			if cu := f.GetConfigUpdate(); cu != nil {
				out = append(out, cfg{f.GetSessionId(), string(cu.GetPayload())})
			}
		}
		return out
	}
	gotB := configsOf(phoneless.hangUp(t))
	if len(gotB) != 1 || gotB[0] != (cfg{control, "catalog-of-tenant-b"}) {
		t.Errorf("el Edge sin teléfonos recibió %v, se esperaba SOLO el catálogo de su tenant por el canal de control", gotB)
	}
	gotA := configsOf(withPhone.hangUp(t))
	want := []cfg{{control, "catalog-of-tenant-a"}, {"s-phone", "catalog-of-tenant-a"}, {"s-phone", string(intentsV2.Payload)}}
	if len(gotA) != len(want) {
		t.Fatalf("el Edge con teléfono recibió %v, se esperaba %v", gotA, want)
	}
	for i := range want {
		if gotA[i] != want[i] {
			t.Errorf("config %d del Edge con teléfono = %v, se esperaba %v", i, gotA[i], want[i])
		}
	}
}
