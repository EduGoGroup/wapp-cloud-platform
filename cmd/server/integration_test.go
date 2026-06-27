package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

const (
	itBufSize    = 1024 * 1024
	itTenantID   = "11111111-1111-1111-1111-111111111111"
	itEdgeID     = "edge-it-001"
	itSessionID  = "sess-it-1"
	itCode       = "CODE-IT-1"
	itServerName = "localhost"
)

// TestInProcessEnrollConnectLeaseSendRecv ejerce el FLUJO COMPLETO con el wiring
// real de DOS listeners (enroll TLS-de-servidor + connect mTLS) sobre bufconn,
// con repos en memoria (CI-safe, sin BD):
//
//	enroll -> connect -> lease inicial + fleet online -> send/recv -> revoke.
//
// Reproduce los mismos constructores que cmd/server/main.go cablea en prod
// (enrollServerCreds para el listener de enroll, mtls.ServerCreds para el de
// connect), validando que el cert emitido en el enrolamiento sirve para el mTLS.
func TestInProcessEnrollConnectLeaseSendRecv(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h := startHarness(t)

	// 3-4. Enrola por el listener server-TLS y conecta por el listener mTLS.
	resp, edgeKey := h.enroll(ctx, t)
	if resp.GetTenantId() != itTenantID {
		t.Fatalf("tenant: got %q, want %q", resp.GetTenantId(), itTenantID)
	}
	stream := h.connect(ctx, t, resp, edgeKey)

	edge := newEdgeSim(stream, h.mgr.PublicKey())
	edge.run()

	// Registra la sesión con un primer Heartbeat (session_id no vacío).
	if err := edge.send(heartbeat(itSessionID, 1)); err != nil {
		t.Fatalf("Send heartbeat inicial: %v", err)
	}

	// 5. LeaseUpdate inicial válido (CanOperate(true)) + fleet online.
	requireSignal(ctx, t, edge.leaseOK, "LeaseUpdate inicial válido")
	waitFleetOnline(t, h.fleetRepo, itSessionID)

	// 6a. El server empuja SendText -> el edge responde Ack{ok} -> SendText retorna.
	ack, err := h.gw.SendText(ctx, itSessionID, "5491100000000", "hola desde la nube")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if !ack.GetOk() {
		t.Fatalf("Ack.Ok = false (error=%q)", ack.GetError())
	}

	// 6b. El edge manda un IncomingMessage -> el hook OnIncoming lo observa.
	if err := edge.send(incoming(itSessionID, "5491100000000", "respuesta del usuario")); err != nil {
		t.Fatalf("Send incoming: %v", err)
	}
	requireIncoming(ctx, t, h.incoming, "respuesta del usuario")

	// 7. Revoca (kill-switch) -> el edge recibe LeaseUpdate(Revoked).
	if err := h.gw.RevokeLease(ctx, itTenantID, itEdgeID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	requireSignal(ctx, t, edge.revoked, "LeaseUpdate de revocación")
	if edge.canOperate() {
		t.Fatal("CanOperate debería ser false tras la revocación")
	}

	edge.assertNoErrors(t)
}

// itHarness agrupa los dos servidores gRPC (enroll + connect) y las dependencias
// observables del Gateway, todo sobre bufconn con repos en memoria.
type itHarness struct {
	ca        *enroll.CA
	enrollLis *bufconn.Listener
	connLis   *bufconn.Listener
	gw        *gatewaygrpc.Server
	mgr       *lease.Manager
	fleetRepo *fleet.MemoryRepository
	incoming  chan *cloudlinkv1.IncomingMessage
}

// startHarness construye CA dev, cert de servidor, los servicios de enroll/lease/
// fleet/gateway (memory) y levanta los DOS servidores gRPC con el wiring real.
func startHarness(t *testing.T) *itHarness {
	t.Helper()
	log := logger.New(logger.WithWriter(io.Discard))

	ca, err := enroll.NewDevCA("wapp-it-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	srvCertPEM, srvKeyPEM, err := ca.IssueServerCert(itServerName, []string{itServerName}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair server: %v", err)
	}

	codes := enroll.NewMemoryStore()
	codes.Add(itCode, itTenantID, time.Now().Add(time.Hour))
	enrollSrv := enroll.NewServer(
		enroll.NewService(codes, ca, enroll.NewMemoryEdgeCertRepository()), log)

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	mgr, err := lease.NewManager(priv, lease.NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	fleetRepo := fleet.NewMemoryRepository()

	gw := gatewaygrpc.New(session.NewRegistry(), log,
		gatewaygrpc.WithLease(mgr), gatewaygrpc.WithFleet(fleetRepo))
	incoming := make(chan *cloudlinkv1.IncomingMessage, 1)
	gw.OnIncoming = func(_ string, m *cloudlinkv1.IncomingMessage) { incoming <- m }

	// Listener Enrollment: TLS de servidor SOLAMENTE (reusa el helper de main).
	enrollLis := bufconn.Listen(itBufSize)
	enrollGS := grpc.NewServer(grpc.Creds(enrollServerCreds(serverCert)))
	enrollSrv.Register(enrollGS)
	enrollErrc := serve(enrollGS, enrollLis)

	// Listener CloudLink: mTLS estricto contra la MISMA CA (reusa mtls de main).
	connLis := bufconn.Listen(itBufSize)
	connGS := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	gw.Register(connGS)
	connErrc := serve(connGS, connLis)

	t.Cleanup(func() {
		enrollGS.Stop()
		connGS.Stop()
		requireServeStopped(t, <-enrollErrc, "enroll")
		requireServeStopped(t, <-connErrc, "connect")
	})

	return &itHarness{
		ca: ca, enrollLis: enrollLis, connLis: connLis,
		gw: gw, mgr: mgr, fleetRepo: fleetRepo, incoming: incoming,
	}
}

// enroll genera key+CSR del Edge y enrola por el listener server-TLS (el cliente
// valida el cert del servidor contra la CA pero NO presenta cert de cliente).
func (h *itHarness) enroll(ctx context.Context, t *testing.T) (*cloudlinkv1.EnrollEdgeResponse, *ecdsa.PrivateKey) {
	t.Helper()
	edgeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("edge key: %v", err)
	}

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer(h.enrollLis)),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    h.ca.Pool(),
			ServerName: itServerName,
		})),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient enroll: %v", err)
	}
	t.Cleanup(func() { closeConn(t, conn) })

	resp, err := cloudlinkv1.NewEnrollmentClient(conn).EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: itCode,
		CsrPem:         makeCSR(t, edgeKey, itEdgeID),
	})
	if err != nil {
		t.Fatalf("EnrollEdge: %v", err)
	}
	if len(resp.GetEdgeCertPem()) == 0 || len(resp.GetCaChainPem()) == 0 {
		t.Fatal("EnrollEdge debería devolver edge_cert_pem y ca_chain_pem")
	}
	return resp, edgeKey
}

// connect abre el stream Connect por mTLS usando el cert emitido en el enroll.
func (h *itHarness) connect(ctx context.Context, t *testing.T, resp *cloudlinkv1.EnrollEdgeResponse, edgeKey *ecdsa.PrivateKey) cloudlinkv1.CloudLink_ConnectClient {
	t.Helper()
	edgeCert, err := tls.X509KeyPair(resp.GetEdgeCertPem(), keyToPEM(t, edgeKey))
	if err != nil {
		t.Fatalf("X509KeyPair edge: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(resp.GetCaChainPem()) {
		t.Fatal("ca_chain_pem no contiene certs válidos")
	}

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer(h.connLis)),
		grpc.WithTransportCredentials(mtls.ClientCreds(edgeCert, caPool, itServerName)),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient connect: %v", err)
	}
	t.Cleanup(func() { closeConn(t, conn) })

	stream, err := cloudlinkv1.NewCloudLinkClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return stream
}

// edgeSim simula el lado Edge sobre el stream Connect: corre un bucle de
// recepción que aplica los LeaseUpdate al Validator y responde a los SendText
// con un Ack{ok}. Serializa los envíos cliente->servidor con un mutex (gRPC no
// admite Send concurrentes en un mismo stream).
type edgeSim struct {
	stream cloudlinkv1.CloudLink_ConnectClient

	// sentText, si no es nil, recibe cada SendText empujado por el servidor
	// (además de ackearlo). Lo usa el test de flujos para asertar el texto del
	// menú y de los nodos destino. nil en los tests que no lo necesitan.
	sentText chan *cloudlinkv1.SendText

	sendMu sync.Mutex

	vMu sync.Mutex
	v   *cllease.Validator

	leaseOK   chan struct{}
	revoked   chan struct{}
	leaseOnce sync.Once
	revOnce   sync.Once

	errMu sync.Mutex
	errs  []error
}

func newEdgeSim(stream cloudlinkv1.CloudLink_ConnectClient, pub []byte) *edgeSim {
	return &edgeSim{
		stream:  stream,
		v:       cllease.NewValidator(pub),
		leaseOK: make(chan struct{}),
		revoked: make(chan struct{}),
	}
}

func (e *edgeSim) send(msg *cloudlinkv1.EdgeToCloud) error {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	return e.stream.Send(msg)
}

func (e *edgeSim) canOperate() bool {
	e.vMu.Lock()
	defer e.vMu.Unlock()
	return e.v.CanOperate(true)
}

func (e *edgeSim) recordErr(err error) {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	e.errs = append(e.errs, err)
}

func (e *edgeSim) assertNoErrors(t *testing.T) {
	t.Helper()
	e.errMu.Lock()
	defer e.errMu.Unlock()
	for _, err := range e.errs {
		t.Errorf("edgeSim error: %v", err)
	}
}

// run lanza el bucle de recepción en una goroutine.
func (e *edgeSim) run() {
	go func() {
		for {
			cmd, err := e.stream.Recv()
			if err != nil {
				return
			}
			e.handle(cmd)
		}
	}()
}

// handle despacha un CloudToEdge: aplica leases y responde Ack a los SendText.
func (e *edgeSim) handle(cmd *cloudlinkv1.CloudToEdge) {
	switch {
	case cmd.GetLeaseUpdate() != nil:
		e.applyLease(cmd.GetLeaseUpdate())
	case cmd.GetSendText() != nil:
		// Ackea ANTES de publicar el texto: el SendText del servidor se
		// desbloquea con el Ack, y el texto queda disponible en el canal
		// bufferizado para que el test lo asierte sin carrera.
		e.ackCommand(cmd)
		if e.sentText != nil {
			e.sentText <- cmd.GetSendText()
		}
	}
}

func (e *edgeSim) applyLease(lu *cloudlinkv1.LeaseUpdate) {
	e.vMu.Lock()
	if err := e.v.Apply(lu); err != nil {
		e.recordErr(err)
	}
	revoked := e.v.Revoked()
	canOp := e.v.CanOperate(true)
	e.vMu.Unlock()

	if revoked {
		e.revOnce.Do(func() { close(e.revoked) })
		return
	}
	if canOp {
		e.leaseOnce.Do(func() { close(e.leaseOK) })
	}
}

func (e *edgeSim) ackCommand(cmd *cloudlinkv1.CloudToEdge) {
	err := e.send(&cloudlinkv1.EdgeToCloud{
		SessionId: cmd.GetSessionId(),
		Payload: &cloudlinkv1.EdgeToCloud_Ack{
			Ack: &cloudlinkv1.Ack{AckedCommandId: cmd.GetCommandId(), Ok: true},
		},
	})
	if err != nil {
		e.recordErr(err)
	}
}

// --- helpers de transporte / aserción ---

// serve arranca gs.Serve(lis) en una goroutine y devuelve el canal con su error.
func serve(gs *grpc.Server, lis *bufconn.Listener) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- gs.Serve(lis) }()
	return errc
}

func requireServeStopped(t *testing.T, err error, name string) {
	t.Helper()
	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Errorf("Serve(%s) devolvió error: %v", name, err)
	}
}

func dialer(lis *bufconn.Listener) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

func closeConn(t *testing.T, conn *grpc.ClientConn) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Errorf("cerrando conn: %v", err)
	}
}

func requireSignal(ctx context.Context, t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("timeout esperando %s", what)
	}
}

func requireIncoming(ctx context.Context, t *testing.T, ch <-chan *cloudlinkv1.IncomingMessage, wantText string) {
	t.Helper()
	select {
	case m := <-ch:
		if m.GetText() != wantText {
			t.Fatalf("IncomingMessage.Text: got %q, want %q", m.GetText(), wantText)
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando el IncomingMessage en el hook OnIncoming")
	}
}

func heartbeat(sessionID string, counter int64) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{LeaseCounter: counter}},
	}
}

func incoming(sessionID, from, text string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From:   from,
			Text:   text,
			TsUnix: time.Now().Unix(),
		}},
	}
}

func makeCSR(t *testing.T, key *ecdsa.PrivateKey, commonName string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func keyToPEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func waitFleetOnline(t *testing.T, repo *fleet.MemoryRepository, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := repo.Get(context.Background(), itTenantID, itEdgeID, sessionID)
		if err != nil {
			t.Fatalf("fleet Get: %v", err)
		}
		if ok && s.State == fleet.StateOnline {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timeout esperando fleet online de %q", sessionID)
}
