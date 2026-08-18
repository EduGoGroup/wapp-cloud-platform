package gatewaygrpc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// linesContaining devuelve las líneas que contienen substr. count() convierte un
// fallo tragado en un rojo, pero un rojo que solo dice «pasó N veces» obliga a
// reproducir a mano para saber QUÉ pasó: esto adjunta la causa al propio fallo.
func (b *syncBuffer) linesContaining(substr string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// harness levanta el Server sobre un bufconn.Listener y devuelve un cliente
// CloudLink ya conectado, junto con el Registry y el Server para inspección.
//
// NO admite repositorios (fleet/lease) a propósito, y no tiene sentido añadírselos:
// este arnés dialoga en CLARO, así que peerIdentity devuelve hasIdentity=false y
// onSessionRegistered / persistHealth / renewLease retornan ANTES de tocar ningún
// repositorio (connect.go). Un repo inyectado aquí sería código muerto. Los arneses
// que SÍ ejercitan fleet/lease usan mTLS: newMTLSHarness (mtls_test.go),
// newTenantRevokeHarness (tenant_revoke_test.go) y newLoadHarness, el de carga
// contra Postgres real (load_integration_test.go, Plan 050 · T5.0/T5.1).
type harness struct {
	srv      *gatewaygrpc.Server
	registry *session.Registry
	client   cloudlinkv1.CloudLinkClient
	logBuf   *syncBuffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessConSink(t, nil)
}

// newHarnessConSink es el MISMO arnés con un ReceiptSink inyectado. Es la única
// dependencia que tiene sentido añadirle sin mTLS: el sink de acuses no mira la
// identidad del peer (handleReceipt lo llama siempre), así que es el observador
// natural del CARRIL desde fuera —cada Record ocurre dentro de un jobReceipt, en
// la goroutine de su sesión— y por eso lo usan los tests de orden y aislamiento
// de T1.12. sink nil = comportamiento por defecto (LogReceiptSink log-only).
func newHarnessConSink(t *testing.T, sink gatewaygrpc.ReceiptSink) *harness {
	t.Helper()

	reg := session.NewRegistry()
	logBuf := &syncBuffer{}
	log := logger.New(logger.WithWriter(logBuf))
	var opts []gatewaygrpc.Option
	if sink != nil {
		opts = append(opts, gatewaygrpc.WithReceiptSink(sink))
	}
	srv := gatewaygrpc.New(reg, log, opts...)

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
//
// ⚠️ Este test NO afirma nada del carril: es un detector de carreras, no de orden
// ni de aislamiento (lanza 20 goroutines y no mide ninguna de las dos cosas). Lo
// que T1.12 promete se afirma abajo, en TestConnectCarrilMismaSesionConservaElOrden
// y TestConnectCarrilSesionesDistintasNoSeBloquean, que sí discriminan. Se deja tal
// cual porque su valor —el -race sobre el bucle Recv con Push concurrente— sigue
// siendo real y es independiente.
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

// waitLog espera hasta que substr aparezca al menos want veces en el log o falla.
func waitLog(t *testing.T, logBuf *syncBuffer, substr string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logBuf.count(substr) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout esperando %d apariciones de %q en el log", want, substr)
}

// TestConnectReceipt verifica que un MessageReceipt recibido por el stream se
// rutea, se loguea correlacionado por command_id y se entrega al receiptSink
// (log-only por defecto). Higiene §10.G: solo metadatos en el log.
func TestConnectReceipt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	const cmdID = "cmd-abc-123"
	receipt := &cloudlinkv1.MessageReceipt{
		SessionId:  "s1",
		MessageIds: []string{"wamid.SENT.1"},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
		Timestamp:  1700000000,
		CommandId:  cmdID,
	}
	if sendErr := stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s1",
		Payload:   &cloudlinkv1.EdgeToCloud_Receipt{Receipt: receipt},
	}); sendErr != nil {
		t.Fatalf("Send receipt: %v", sendErr)
	}

	// El case del route loguea la recepción y el sink log-only persiste (log).
	waitLog(t, h.logBuf, "acuse recibido del Edge", 1)
	waitLog(t, h.logBuf, "acuse persistido (log-only)", 1)

	// La correlación por command_id debe aparecer en el log; nunca contenido.
	if h.logBuf.count(cmdID) < 2 {
		t.Fatalf("el command_id %q debía aparecer correlacionado en ambos logs", cmdID)
	}
	if h.logBuf.count("RECEIPT_STATUS_DELIVERED") < 1 {
		t.Fatal("el status DELIVERED debía loguearse")
	}
}

// receiptSinkGrabador es un ReceiptSink que anota QUÉ acuses procesó, EN QUÉ
// ORDEN y CUÁNTOS a la vez. Los tres datos son lo que T1.12 viene a medir sobre
// el servidor YA CABLEADO: sin el orden no hay serialización por sesión, y sin el
// pico de simultáneos no se distingue un carril por sesión de un `go s.route(...)`.
//
// Corre dentro del carril (jobReceipt), es decir, en la goroutine de su sesión y
// no en la del bucle Recv: por eso todos sus campos mutables viven bajo mu.
type receiptSinkGrabador struct {
	mu      sync.Mutex
	visto   []string
	enVuelo int
	pico    int

	// dura simula el trabajo del sink de producción (una fila por message_id).
	// No es sincronización —para eso están los canales de abajo—: es la latencia
	// que hace SOLAPARSE a los jobs si alguien los suelta en paralelo, y sin ella
	// un `go s.route(...)` podría pasar desapercibido por puro azar del scheduler.
	dura time.Duration

	// taponID identifica el acuse que se queda DENTRO del sink hasta que el test
	// lo suelte; dentro avisa de que ya entró y soltar lo libera. Con la sesión
	// del tapón retenida, lo que ocurra con OTRA sesión es la prueba del
	// aislamiento. Los tres se fijan al construir el sink, antes de que arranque
	// ningún stream, y no se vuelven a escribir.
	taponID string
	dentro  chan struct{}
	soltar  chan struct{}
}

// Record implementa gatewaygrpc.ReceiptSink.
func (s *receiptSinkGrabador) Record(_ context.Context, receipt *cloudlinkv1.MessageReceipt) error {
	id := receipt.GetCommandId()
	s.entra(id)
	defer s.sale()

	if s.taponID != "" && id == s.taponID {
		select {
		case s.dentro <- struct{}{}:
		default:
		}
		<-s.soltar
		return nil
	}
	if s.dura > 0 {
		time.Sleep(s.dura)
	}
	return nil
}

func (s *receiptSinkGrabador) entra(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visto = append(s.visto, id)
	s.enVuelo++
	if s.enVuelo > s.pico {
		s.pico = s.enVuelo
	}
}

func (s *receiptSinkGrabador) sale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enVuelo--
}

// orden devuelve los command_id procesados, en orden de entrada al sink.
func (s *receiptSinkGrabador) orden() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.visto, ",")
}

// cuenta devuelve cuántos acuses ENTRARON al sink (aunque sigan dentro).
func (s *receiptSinkGrabador) cuenta() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.visto)
}

// picoEnVuelo devuelve el máximo de acuses procesándose a la vez.
func (s *receiptSinkGrabador) picoEnVuelo() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pico
}

// receiptFrame arma un EdgeToCloud con un MessageReceipt de la sesión dada,
// identificado por commandID (que es lo que el sink anota).
func receiptFrame(sessionID, commandID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Receipt{Receipt: &cloudlinkv1.MessageReceipt{
			SessionId:  sessionID,
			MessageIds: []string{"wamid." + commandID},
			Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
			CommandId:  commandID,
		}},
	}
}

// esperarAcuses sondea hasta que al menos n acuses hayan ENTRADO al sink. Sondeo,
// no time.Sleep fijo: el mismo molde que waitOnline/waitLog de este arnés.
func esperarAcuses(t *testing.T, sink *receiptSinkGrabador, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sink.cuenta() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout esperando %d acuses en el sink (llegaron %d: %s)", n, sink.cuenta(), sink.orden())
}

// TestConnectCarrilMismaSesionConservaElOrden (T1.12, REQ-050.3) mide sobre el
// SERVIDOR YA CABLEADO —no sobre el carril suelto— lo que la ola promete dentro de
// una sesión: el trabajo pesado de un mismo session_id se ejecuta EN SERIE y EN EL
// ORDEN en que llegó por el stream.
//
// Por qué discrimina, que es lo único que lo hace válido:
//   - Si el carril se sustituyera por `go s.route(...)`, los ocho acuses entrarían
//     al sink a la vez —cada uno tarda 15 ms— y el pico de simultáneos sería 8, no
//     1. Cae por la primera aserción aunque el orden saliera bien por azar.
//   - Si el carril reordenara (p. ej. coalesciendo receipts o reencolando al
//     final), cae por la segunda.
//   - Si la rama Receipt volviera al bucle Recv, seguiría siendo serie y en orden…
//     pero eso lo cubre T1.14, que es donde se afirma que NO está inline.
func TestConnectCarrilMismaSesionConservaElOrden(t *testing.T) {
	t.Parallel()
	sink := &receiptSinkGrabador{dura: 15 * time.Millisecond}
	h := newHarnessConSink(t, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	quiero := make([]string, 0, 8)
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("r%d", i)
		quiero = append(quiero, id)
		if sendErr := stream.Send(receiptFrame("s1", id)); sendErr != nil {
			t.Fatalf("Send receipt %s: %v", id, sendErr)
		}
	}
	esperarAcuses(t, sink, len(quiero))

	if got := sink.picoEnVuelo(); got != 1 {
		t.Fatalf("acuses simultáneos de la MISMA sesión = %d, quiero 1: el carril es serie por sesión "+
			"(un %d > 1 es la firma de un `go s.route(...)`)", got, got)
	}
	if got, want := sink.orden(), strings.Join(quiero, ","); got != want {
		t.Fatalf("orden de proceso = %s, quiero %s", got, want)
	}
}

// TestConnectCarrilSesionesDistintasNoSeBloquean (T1.12, REQ-050.3) es la otra
// mitad y la razón de ser de la ola: el trabajo lento de UNA sesión no puede
// retener al de otra. El acuse de s1 se queda DENTRO del sink (retenido a
// propósito) y, con él ahí, el acuse de s2 —que viaja por el MISMO stream— tiene
// que procesarse igual.
//
// Por qué discrimina: con una cola única por stream (o con el trabajo inline en el
// bucle Recv, que es el defecto que este plan viene a arreglar) el acuse de s2 no
// entraría al sink hasta soltar a s1, y esperarAcuses agotaría su plazo. El pico de
// 2 simultáneos lo confirma en positivo: hubo dos sesiones trabajando a la vez.
func TestConnectCarrilSesionesDistintasNoSeBloquean(t *testing.T) {
	t.Parallel()
	sink := &receiptSinkGrabador{
		taponID: "tapon-s1",
		dentro:  make(chan struct{}, 1),
		soltar:  make(chan struct{}),
	}
	h := newHarnessConSink(t, sink)

	// Soltar SIEMPRE, y una sola vez: si el test falla con el tapón puesto, el
	// cierre del stream se quedaría drenando hasta agotar el presupuesto.
	var unaVez sync.Once
	liberar := func() { unaVez.Do(func() { close(sink.soltar) }) }
	defer liberar()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if sendErr := stream.Send(receiptFrame("s1", "tapon-s1")); sendErr != nil {
		t.Fatalf("Send receipt s1: %v", sendErr)
	}
	select {
	case <-sink.dentro:
	case <-time.After(5 * time.Second):
		t.Fatal("el acuse de s1 no llegó nunca al sink: el carril no procesó su cola")
	}

	// s1 sigue DENTRO del sink. El acuse de s2 tiene que adelantarlo.
	if sendErr := stream.Send(receiptFrame("s2", "libre-s2")); sendErr != nil {
		t.Fatalf("Send receipt s2: %v", sendErr)
	}
	esperarAcuses(t, sink, 2)

	if got := sink.picoEnVuelo(); got != 2 {
		t.Fatalf("acuses simultáneos de sesiones DISTINTAS = %d, quiero 2: "+
			"cada sesión tiene su propia cola y su propia goroutine", got)
	}
	liberar()
}
