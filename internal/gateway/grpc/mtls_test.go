package gatewaygrpc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"net"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

const (
	testTenantID = "11111111-1111-1111-1111-111111111111"
	testEdgeID   = "edge-001"
)

// mtlsHarness levanta el Server con mTLS, lease y fleet (repos en memoria) sobre
// bufconn, y devuelve un cliente CloudLink conectado con el cert de Edge.
type mtlsHarness struct {
	srv       *gatewaygrpc.Server
	mgr       *lease.Manager
	leaseRepo *lease.MemoryRepository
	fleetRepo *fleet.MemoryRepository
	client    cloudlinkv1.CloudLinkClient
}

// newMTLSHarness construye el harness; si edgeCreds proviene de otra CA, el
// handshake fallará (caso negativo).
func newMTLSHarness(t *testing.T, ca *enroll.CA, edgeCert tls.Certificate) *mtlsHarness {
	t.Helper()

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	leaseRepo := lease.NewMemoryRepository()
	mgr, err := lease.NewManager(priv, leaseRepo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	fleetRepo := fleet.NewMemoryRepository()

	reg := session.NewRegistry()
	log := logger.New(logger.WithWriter(io.Discard))
	srv := gatewaygrpc.New(reg, log, gatewaygrpc.WithLease(mgr), gatewaygrpc.WithFleet(fleetRepo))

	srvCertPEM, srvKeyPEM, err := ca.IssueServerCert("localhost", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair server: %v", err)
	}

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() { serveErrc <- gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(mtls.ClientCreds(edgeCert, ca.Pool(), "localhost")),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("cerrando conn: %v", cerr)
		}
		gs.Stop()
		<-serveErrc
		if cerr := lis.Close(); cerr != nil {
			t.Errorf("cerrando listener: %v", cerr)
		}
	})

	return &mtlsHarness{
		srv:       srv,
		mgr:       mgr,
		leaseRepo: leaseRepo,
		fleetRepo: fleetRepo,
		client:    cloudlinkv1.NewCloudLinkClient(conn),
	}
}

// --- helpers de certificados ---

func newEdgeKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return key
}

func makeCSR(t *testing.T, key *ecdsa.PrivateKey, commonName string) []byte {
	t.Helper()
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
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

// issueEdgeCert genera una clave de Edge, su CSR (CN=edgeID) y lo firma con la
// CA dada (O=tenantID), devolviendo el tls.Certificate listo para el cliente.
func issueEdgeCert(t *testing.T, ca *enroll.CA, tenantID, edgeID string) tls.Certificate {
	t.Helper()
	key := newEdgeKey(t)
	signed, err := ca.SignCSR(makeCSR(t, key, edgeID), tenantID)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	cert, err := tls.X509KeyPair(signed.EdgeCertPEM, keyToPEM(t, key))
	if err != nil {
		t.Fatalf("X509KeyPair edge: %v", err)
	}
	return cert
}

func newDevCA(t *testing.T) *enroll.CA {
	t.Helper()
	ca, err := enroll.NewDevCA("wapp-test-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	return ca
}

// waitFleetOnline espera a que la sesión figure online en el fleet repo.
func waitFleetOnline(t *testing.T, repo *fleet.MemoryRepository, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := repo.Get(context.Background(), testTenantID, testEdgeID, sessionID)
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

// waitFleetOffline espera a que la sesión figure offline en el fleet repo.
func waitFleetOffline(t *testing.T, repo *fleet.MemoryRepository, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := repo.Get(context.Background(), testTenantID, testEdgeID, sessionID)
		if err != nil {
			t.Fatalf("fleet Get: %v", err)
		}
		if ok && s.State == fleet.StateOffline {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timeout esperando fleet offline de %q", sessionID)
}

func mtlsHeartbeat(sessionID string, counter int64) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{LeaseCounter: counter}},
	}
}

// mtlsHeartbeatState construye un Heartbeat con un State explícito (Plan 020 · T3:
// LOGGED_OUT anuncia una sesión zombie).
func mtlsHeartbeatState(sessionID string, counter int64, state cloudlinkv1.SessionState) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{
			Heartbeat: &cloudlinkv1.Heartbeat{LeaseCounter: counter, State: state},
		},
	}
}

// waitFleetState espera a que la sesión figure en el estado dado en el fleet repo.
func waitFleetState(t *testing.T, repo *fleet.MemoryRepository, sessionID string, want fleet.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := repo.Get(context.Background(), testTenantID, testEdgeID, sessionID)
		if err != nil {
			t.Fatalf("fleet Get: %v", err)
		}
		if ok && s.State == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timeout esperando fleet %q de %q", want, sessionID)
}

// TestMTLSHeartbeatLoggedOutMarksZombie verifica (Plan 020 · T3) que un Heartbeat
// con State=LOGGED_OUT marca la sesión loggedout (zombie) SIN renovar el lease
// (sesión muerta): el counter del lease queda en el inicial (1), no avanza al
// counter alto del Heartbeat. Contrasta con TestMTLSHeartbeatRenewsLeaseCounter
// (un Heartbeat normal SÍ renueva): confirma que loggedout NO sigue ese camino.
func TestMTLSHeartbeatLoggedOutMarksZombie(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Heartbeat zombie con un counter ALTO: si (por error) renovara el lease, el
	// counter saltaría a hbCounter+1; como no debe renovar, se queda en el inicial.
	const hbCounter int64 = 99
	if err := stream.Send(mtlsHeartbeatState("s1", hbCounter, cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// La sesión queda loggedout (zombie), distinta de offline.
	waitFleetState(t, h.fleetRepo, "s1", fleet.StateLoggedOut)

	// El lease NO se renovó: sigue en el counter inicial (1) que fijó
	// onSessionRegistered, no en hbCounter+1. Se comprueba tras un margen para
	// descartar una renovación tardía.
	time.Sleep(50 * time.Millisecond)
	st, ok, getErr := h.leaseRepo.Get(context.Background(), testTenantID, testEdgeID)
	if getErr != nil {
		t.Fatalf("leaseRepo.Get: %v", getErr)
	}
	if !ok {
		t.Fatal("se esperaba el lease inicial del registro de la sesión")
	}
	if st.Counter == hbCounter+1 {
		t.Fatalf("un Heartbeat LOGGED_OUT NO debe renovar el lease (counter=%d)", st.Counter)
	}
}

// TestMTLSHeartbeatUnspecifiedStaysOnline verifica la NO-regresión (INV): un
// Heartbeat sin State (UNSPECIFIED = 0, default de proto) sigue el camino de
// siempre → la sesión queda online, nunca zombie.
func TestMTLSHeartbeatUnspecifiedStaysOnline(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// mtlsHeartbeat no fija State ⇒ UNSPECIFIED (0).
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFleetOnline(t, h.fleetRepo, "s1")

	s, _, err := h.fleetRepo.Get(context.Background(), testTenantID, testEdgeID, "s1")
	if err != nil {
		t.Fatalf("fleet Get: %v", err)
	}
	if s.State == fleet.StateLoggedOut {
		t.Fatal("un Heartbeat UNSPECIFIED nunca debe marcar zombie")
	}
}

// --- tests ---

func TestMTLSHandshakeIdentityAndInitialLease(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Identidad extraída del cert == (tenant, edge): el fleet la registra online.
	waitFleetOnline(t, h.fleetRepo, "s1")

	// El cliente recibe un LeaseUpdate vigente y el Validator puede operar.
	v := cllease.NewValidator(h.mgr.PublicKey())
	cmd, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv lease: %v", err)
	}
	lu := cmd.GetLeaseUpdate()
	if lu == nil {
		t.Fatalf("primer CloudToEdge no es LeaseUpdate: %+v", cmd)
	}
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if !v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser true tras el lease inicial")
	}
}

// TestMTLSMultiSesionCadaUnaRecibeLease verifica que, con dos session_id sobre
// UN mismo stream mTLS, AMBAS quedan online en el fleet y CADA una recibe su
// LeaseUpdate push (correlacionado por el SessionId del CloudToEdge que fija
// leaseToCloud por sesión). El lease subyacente es del Edge (tenant, edge) pero
// el push es por session_id (Plan 009 · R2 / D2).
func TestMTLSMultiSesionCadaUnaRecibeLease(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send s1: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s2", 1)); err != nil {
		t.Fatalf("Send s2: %v", err)
	}

	// Ambas sesiones online en el fleet (MarkOnline por sesión).
	waitFleetOnline(t, h.fleetRepo, "s1")
	waitFleetOnline(t, h.fleetRepo, "s2")

	// Cada session_id recibe su LeaseUpdate push.
	got := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for !got["s1"] || !got["s2"] {
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando LeaseUpdate por sesión (recibidos: %v)", got)
		}
		cmd, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		if cmd.GetLeaseUpdate() != nil {
			got[cmd.GetSessionId()] = true
		}
	}
}

func TestMTLSRejectsForeignCert(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	foreignCA := newDevCA(t)
	// Cert de Edge firmado por OTRA CA: el server (que confía en ca) debe rechazar.
	h := newMTLSHarness(t, ca, issueEdgeCert(t, foreignCA, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		// Algunos stacks fallan ya en Connect; es un rechazo válido.
		return
	}
	// Si Connect no falló de inmediato, el handshake mTLS falla al usar el stream.
	if sendErr := stream.Send(mtlsHeartbeat("s1", 1)); sendErr != nil {
		return // el envío ya falló: rechazo válido.
	}
	if _, recvErr := stream.Recv(); recvErr == nil {
		t.Fatal("se esperaba fallo de handshake con cert de CA ajena")
	}
}

func TestMTLSRevokeBlocks(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFleetOnline(t, h.fleetRepo, "s1")

	// Dispara el kill-switch.
	if err := h.srv.RevokeLease(ctx, testTenantID, testEdgeID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}

	// Aplica todos los LeaseUpdates entrantes hasta ver la revocación pegajosa.
	v := cllease.NewValidator(h.mgr.PublicKey())
	deadline := time.Now().Add(3 * time.Second)
	for !v.Revoked() {
		if time.Now().After(deadline) {
			t.Fatal("timeout esperando el LeaseUpdate de revocación")
		}
		cmd, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		if lu := cmd.GetLeaseUpdate(); lu != nil {
			if applyErr := v.Apply(lu); applyErr != nil {
				t.Fatalf("Apply: %v", applyErr)
			}
		}
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate debería ser false tras la revocación")
	}
}

func TestMTLSFleetOnlineThenOffline(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamCtx, streamCancel := context.WithCancel(ctx)
	stream, err := h.client.Connect(streamCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFleetOnline(t, h.fleetRepo, "s1")

	// Caída del stream -> fleet offline.
	if closeErr := stream.CloseSend(); closeErr != nil {
		t.Fatalf("CloseSend: %v", closeErr)
	}
	streamCancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, getErr := h.fleetRepo.Get(context.Background(), testTenantID, testEdgeID, "s1")
		if getErr != nil {
			t.Fatalf("fleet Get: %v", getErr)
		}
		if ok && s.State == fleet.StateOffline {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("timeout esperando fleet offline tras caída del stream")
}

// TestMTLSMultiSesionCierreTodasOffline verifica el cierre multi-sesión: con dos
// session_id sobre un stream mTLS, al caer el stream AMBAS quedan offline en el
// fleet (fleet.MarkOffline por cada una), no solo una (Plan 009 · R3 / D4).
func TestMTLSMultiSesionCierreTodasOffline(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamCtx, streamCancel := context.WithCancel(ctx)
	stream, err := h.client.Connect(streamCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s1", 1)); err != nil {
		t.Fatalf("Send s1: %v", err)
	}
	if err := stream.Send(mtlsHeartbeat("s2", 1)); err != nil {
		t.Fatalf("Send s2: %v", err)
	}
	waitFleetOnline(t, h.fleetRepo, "s1")
	waitFleetOnline(t, h.fleetRepo, "s2")

	// Caída del stream -> ambas sesiones offline en el fleet.
	if closeErr := stream.CloseSend(); closeErr != nil {
		t.Fatalf("CloseSend: %v", closeErr)
	}
	streamCancel()

	waitFleetOffline(t, h.fleetRepo, "s1")
	waitFleetOffline(t, h.fleetRepo, "s2")
}

func TestMTLSHeartbeatRenewsLeaseCounter(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	h := newMTLSHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Heartbeat con counter alto: la renovación debe persistir counter+1.
	const hbCounter int64 = 41
	if err := stream.Send(mtlsHeartbeat("s1", hbCounter)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFleetOnline(t, h.fleetRepo, "s1")

	// El Manager comparte su repo: la renovación persiste counter = hbCounter+1
	// con expiración ~5min. (El lease inicial fijó counter=1; el Heartbeat lo
	// avanza.)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timeout esperando la renovación del lease (counter hbCounter+1)")
		}
		st, ok, getErr := h.leaseRepo.Get(context.Background(), testTenantID, testEdgeID)
		if getErr != nil {
			t.Fatalf("leaseRepo.Get: %v", getErr)
		}
		if ok && st.Counter == hbCounter+1 {
			if d := time.Until(st.ExpiresAt); d < 4*time.Minute || d > 6*time.Minute {
				t.Fatalf("expires_at fuera de rango (~5min): %v", d)
			}
			break
		}
		time.Sleep(3 * time.Millisecond)
	}

	// Y el LeaseUpdate renovado aplica limpio en el Validator del Edge.
	v := cllease.NewValidator(h.mgr.PublicKey())
	cmd, recvErr := stream.Recv()
	if recvErr != nil {
		t.Fatalf("Recv: %v", recvErr)
	}
	if lu := cmd.GetLeaseUpdate(); lu != nil {
		if applyErr := v.Apply(lu); applyErr != nil {
			t.Fatalf("Apply: %v", applyErr)
		}
		if !v.CanOperate(true) {
			t.Fatal("CanOperate(true) debería ser true tras renovar")
		}
	}
}
