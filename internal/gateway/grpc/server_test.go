package gatewaygrpc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

const bufSize = 1024 * 1024

// harness levanta el Server sobre un bufconn.Listener y devuelve un cliente
// CloudLink ya conectado, junto con el Registry y el Server para inspección.
type harness struct {
	srv      *gatewaygrpc.Server
	registry *session.Registry
	client   cloudlinkv1.CloudLinkClient
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	reg := session.NewRegistry()
	log := logger.New(logger.WithWriter(io.Discard))
	srv := gatewaygrpc.New(reg, log)

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer()
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() {
		serveErrc <- gs.Serve(lis)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("cerrando conn: %v", closeErr)
		}
		gs.Stop()
		if serveErr := <-serveErrc; serveErr != nil {
			t.Errorf("gs.Serve devolvió error: %v", serveErr)
		}
		if closeErr := lis.Close(); closeErr != nil {
			t.Errorf("cerrando listener: %v", closeErr)
		}
	})

	return &harness{
		srv:      srv,
		registry: reg,
		client:   cloudlinkv1.NewCloudLinkClient(conn),
	}
}

// waitOnline espera hasta que la sesión esté online o falla por timeout.
func waitOnline(t *testing.T, reg *session.Registry, sessionID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reg.Online(sessionID) == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout esperando Online(%q)==%v", sessionID, want)
}

func heartbeat(sessionID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{LeaseCounter: 1}},
	}
}

func TestConnectIncoming(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	got := make(chan *cloudlinkv1.IncomingMessage, 1)
	gotSession := make(chan string, 1)
	h.srv.OnIncoming = func(sessionID string, m *cloudlinkv1.IncomingMessage) {
		gotSession <- sessionID
		got <- m
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	in := &cloudlinkv1.IncomingMessage{
		From:        "57300@s.whatsapp.net",
		Text:        "hola",
		TsUnix:      1234,
		WaMessageId: "wamid.1",
		IsGroup:     false,
	}
	if sendErr := stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s1",
		Payload:   &cloudlinkv1.EdgeToCloud_Incoming{Incoming: in},
	}); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}

	select {
	case sid := <-gotSession:
		if sid != "s1" {
			t.Fatalf("sessionID en OnIncoming = %q, quiero s1", sid)
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando OnIncoming")
	}

	m := <-got
	if m.GetFrom() != in.GetFrom() || m.GetText() != in.GetText() || m.GetWaMessageId() != in.GetWaMessageId() {
		t.Fatalf("IncomingMessage recibido = %+v, no coincide con %+v", m, in)
	}
}

func TestConnectSendTextAck(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Registrar la sesión enviando un primer mensaje.
	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send heartbeat: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)

	type result struct {
		ack *cloudlinkv1.Ack
		err error
	}
	res := make(chan result, 1)
	go func() {
		ack, sendErr := h.srv.SendText(ctx, "s1", "57301", "responde")
		res <- result{ack: ack, err: sendErr}
	}()

	// El cliente recibe el comando SendText y responde con el Ack.
	cmd, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv comando: %v", err)
	}
	st := cmd.GetSendText()
	if st == nil {
		t.Fatalf("comando recibido no es SendText: %+v", cmd)
	}
	if st.GetTo() != "57301" || st.GetText() != "responde" {
		t.Fatalf("SendText = %+v, no coincide", st)
	}
	if cmd.GetCommandId() == "" {
		t.Fatal("comando sin command_id")
	}

	if sendErr := stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s1",
		Payload: &cloudlinkv1.EdgeToCloud_Ack{
			Ack: &cloudlinkv1.Ack{AckedCommandId: cmd.GetCommandId(), Ok: true},
		},
	}); sendErr != nil {
		t.Fatalf("Send ack: %v", sendErr)
	}

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("SendText devolvió error: %v", r.err)
		}
		if !r.ack.GetOk() {
			t.Fatalf("Ack.Ok = false, quiero true: %+v", r.ack)
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando el retorno de SendText")
	}
}

func TestConnectMultiplexado(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream1, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect s1: %v", err)
	}
	stream2, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect s2: %v", err)
	}

	if sendErr := stream1.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send s1: %v", sendErr)
	}
	if sendErr := stream2.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)
	waitOnline(t, h.registry, "s2", true)

	// SendText a s2 debe llegar SOLO al stream de s2.
	sendDone := make(chan error, 1)
	go func() {
		_, sendErr := h.srv.SendText(ctx, "s2", "57302", "para s2")
		sendDone <- sendErr
	}()

	cmd, err := stream2.Recv()
	if err != nil {
		t.Fatalf("Recv s2: %v", err)
	}
	st := cmd.GetSendText()
	if st == nil || st.GetText() != "para s2" {
		t.Fatalf("s2 recibió comando inesperado: %+v", cmd)
	}

	// Responder el Ack y esperar el retorno de SendText (sin goroutine colgada).
	if sendErr := stream2.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s2",
		Payload:   &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{AckedCommandId: cmd.GetCommandId(), Ok: true}},
	}); sendErr != nil {
		t.Fatalf("Send ack s2: %v", sendErr)
	}
	if sendErr := <-sendDone; sendErr != nil {
		t.Fatalf("SendText a s2 devolvió error: %v", sendErr)
	}
}

// TestConnectMultiSesionMismoStream es el reproductor del bug del Plan 009: dos
// session_id distintos multiplexados sobre UN MISMO stream CloudLink (ADR-0008).
// Hoy el Connect clava la 1ª sesión que ve y descarta las demás, así que solo
// una queda online; el fix (registro por-frame) debe dejar ambas online.
func TestConnectMultiSesionMismoStream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Dos sesiones distintas por EL MISMO stream (el Edge las multiplexa, Plan 008).
	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send s1: %v", sendErr)
	}
	if sendErr := stream.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}

	// Ambas deben quedar online. Hoy solo la 1ª se registra (bug) → la 2ª falla.
	waitOnline(t, h.registry, "s1", true)
	waitOnline(t, h.registry, "s2", true)

	// Ruteo: SendText a s2 no debe fallar con ErrSessionOffline.
	sendDone := make(chan error, 1)
	go func() {
		_, sendErr := h.srv.SendText(ctx, "s2", "57302", "para s2")
		sendDone <- sendErr
	}()

	cmd, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv s2: %v", err)
	}
	st := cmd.GetSendText()
	if st == nil || st.GetText() != "para s2" {
		t.Fatalf("s2 recibió comando inesperado: %+v", cmd)
	}
	if sendErr := stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s2",
		Payload:   &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{AckedCommandId: cmd.GetCommandId(), Ok: true}},
	}); sendErr != nil {
		t.Fatalf("Send ack s2: %v", sendErr)
	}
	if sendErr := <-sendDone; sendErr != nil {
		t.Fatalf("SendText a s2 devolvió error: %v", sendErr)
	}

	// Idempotencia: re-enviar el heartbeat de s1 no registra una sesión nueva
	// (register-on-first-frame); siguen siendo exactamente 2 sesiones online.
	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send s1 (repetido): %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)
	if got := h.registry.Count(); got != 2 {
		t.Fatalf("registry.Count() = %d tras reenviar s1, quiero 2 (idempotente)", got)
	}
}

func TestConnectStreamDownGoesOffline(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	// Caída del stream: cerrar el envío y cancelar el contexto del stream.
	if closeErr := stream.CloseSend(); closeErr != nil {
		t.Fatalf("CloseSend: %v", closeErr)
	}
	streamCancel()

	waitOnline(t, h.registry, "s1", false)

	_, err = h.srv.SendText(ctx, "s1", "57303", "ya offline")
	if err == nil {
		t.Fatal("SendText a sesión offline debería fallar")
	}
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("error = %v, quiero envolver ErrSessionOffline", err)
	}
}
