package gatewaygrpc_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
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

// syncBuffer es un io.Writer seguro para uso concurrente: el logger del Server
// escribe desde el goroutine de Recv mientras el test lee para inspeccionarlo.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// count devuelve cuántas veces aparece substr en lo escrito hasta ahora.
func (b *syncBuffer) count(substr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), substr)
}

// harness levanta el Server sobre un bufconn.Listener y devuelve un cliente
// CloudLink ya conectado, junto con el Registry y el Server para inspección.
type harness struct {
	srv      *gatewaygrpc.Server
	registry *session.Registry
	client   cloudlinkv1.CloudLinkClient
	logBuf   *syncBuffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	reg := session.NewRegistry()
	logBuf := &syncBuffer{}
	log := logger.New(logger.WithWriter(logBuf))
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
		logBuf:   logBuf,
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

// TestConnectRuteoPorSessionIDDelFrame verifica que cada frame se despacha bajo
// SU propio session_id (no el de la 1ª sesión clavada): dos IncomingMessage con
// session_id distintos por un mismo stream llegan a OnIncoming con su sid.
func TestConnectRuteoPorSessionIDDelFrame(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	sids := make(chan string, 2)
	h.srv.OnIncoming = func(sessionID string, _ *cloudlinkv1.IncomingMessage) {
		sids <- sessionID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	incoming := func(sid string) *cloudlinkv1.EdgeToCloud {
		return &cloudlinkv1.EdgeToCloud{
			SessionId: sid,
			Payload:   &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{From: sid, Text: "hola"}},
		}
	}
	if sendErr := stream.Send(incoming("s1")); sendErr != nil {
		t.Fatalf("Send s1: %v", sendErr)
	}
	if sendErr := stream.Send(incoming("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case sid := <-sids:
			got[sid] = true
		case <-ctx.Done():
			t.Fatalf("timeout esperando OnIncoming (recibidos: %v)", got)
		}
	}
	if !got["s1"] || !got["s2"] {
		t.Fatalf("OnIncoming ruteó %v, quiero s1 y s2 con su propio session_id", got)
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

	// Dos sesiones sobre el mismo stream: al caer, AMBAS deben quedar offline.
	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send heartbeat s1: %v", sendErr)
	}
	if sendErr := stream.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send heartbeat s2: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)
	waitOnline(t, h.registry, "s2", true)

	// Caída del stream: cerrar el envío y cancelar el contexto del stream.
	if closeErr := stream.CloseSend(); closeErr != nil {
		t.Fatalf("CloseSend: %v", closeErr)
	}
	streamCancel()

	waitOnline(t, h.registry, "s1", false)
	waitOnline(t, h.registry, "s2", false)

	for _, sid := range []string{"s1", "s2"} {
		_, sendErr := h.srv.SendText(ctx, sid, "57303", "ya offline")
		if sendErr == nil {
			t.Fatalf("SendText a %q offline debería fallar", sid)
		}
		if !errors.Is(sendErr, session.ErrSessionOffline) {
			t.Fatalf("error de %q = %v, quiero envolver ErrSessionOffline", sid, sendErr)
		}
	}
}

// TestConnectHotJoinSegundaSesion cubre el hot-join: una 2ª sesión que aparece
// en un frame POSTERIOR (cuando la 1ª ya está viva) se registra igual, en vez de
// descartarse como hacía el guard release==nil (Plan 009 · R4).
func TestConnectHotJoinSegundaSesion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Primera sesión; se confirma online ANTES de que aparezca la segunda.
	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send s1: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)

	// Hot-join: la 2ª sesión llega después, por el mismo stream.
	if sendErr := stream.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}
	waitOnline(t, h.registry, "s2", true)

	// La 1ª sigue viva y ambas cuentan.
	waitOnline(t, h.registry, "s1", true)
	if got := h.registry.Count(); got != 2 {
		t.Fatalf("Count() = %d, quiero 2", got)
	}
}

// TestConnectReconexionIdempotente verifica que re-enviar frames del MISMO
// session_id ya vivo no re-registra: el map local hace no-op para el sid ya
// presente (la última-gana la resuelve el registry). Se cuenta el log de
// registro, que debe dispararse una vez por sid distinto, no por frame.
func TestConnectReconexionIdempotente(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	beats := make(chan struct{}, 8)
	h.srv.OnHeartbeat = func(string, *cloudlinkv1.Heartbeat) { beats <- struct{}{} }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// s1 tres veces (heartbeats repetidos / reconexión del mismo sid) + s2 una.
	for i := 0; i < 3; i++ {
		if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
			t.Fatalf("Send s1 #%d: %v", i, sendErr)
		}
	}
	if sendErr := stream.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}

	// Barrera: OnHeartbeat corre DESPUÉS del registro de su frame; esperar los 4
	// beats garantiza que los 4 frames pasaron por el registro (y su log).
	for i := 0; i < 4; i++ {
		select {
		case <-beats:
		case <-ctx.Done():
			t.Fatalf("timeout esperando heartbeats (recibidos %d/4)", i)
		}
	}

	waitOnline(t, h.registry, "s1", true)
	waitOnline(t, h.registry, "s2", true)

	if n := h.logBuf.count("sesión CloudLink registrada"); n != 2 {
		t.Fatalf("registros = %d, quiero 2 (s1 y s2 una vez cada uno)", n)
	}
	if got := h.registry.Count(); got != 2 {
		t.Fatalf("Count() = %d, quiero 2", got)
	}
}

// TestConnectMultiSesionRace ejercita el Recv del Connect concurrente con Push
// desde otros goroutines (Ping) sobre 2 sesiones, mientras el Edge sigue
// multiplexando heartbeats. Debe quedar limpio bajo -race: el map local lo muta
// solo el goroutine de Recv (D1) y el estado cross-goroutine vive en el registry.
func TestConnectMultiSesionRace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send s1: %v", sendErr)
	}
	if sendErr := stream.Send(heartbeat("s2")); sendErr != nil {
		t.Fatalf("Send s2: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)
	waitOnline(t, h.registry, "s2", true)

	// El cliente drena lo que el servidor empuja, para no bloquear los Send.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			if _, recvErr := stream.Recv(); recvErr != nil {
				return
			}
		}
	}()

	// Push concurrente (Ping) a ambas sesiones desde varios goroutines.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := "s1"
			if n%2 == 0 {
				sid = "s2"
			}
			// Ambas sesiones siguen vivas hasta el CloseSend (tras wg.Wait), así
			// que el Push no debe fallar. t.Errorf es seguro concurrentemente.
			if pingErr := h.srv.Ping(ctx, sid, int64(n)); pingErr != nil {
				t.Errorf("Ping %q: %v", sid, pingErr)
			}
		}(i)
	}
	// A la vez, el Edge sigue mandando heartbeats de ambas sesiones (Recv activo).
	for i := 0; i < 20; i++ {
		sid := "s1"
		if i%2 == 0 {
			sid = "s2"
		}
		if sendErr := stream.Send(heartbeat(sid)); sendErr != nil {
			t.Fatalf("Send heartbeat concurrente: %v", sendErr)
		}
	}
	wg.Wait()

	if closeErr := stream.CloseSend(); closeErr != nil {
		t.Fatalf("CloseSend: %v", closeErr)
	}
	<-drainDone

	waitOnline(t, h.registry, "s1", false)
	waitOnline(t, h.registry, "s2", false)
}
