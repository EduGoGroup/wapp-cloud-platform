package gatewaygrpc_test

import (
	"context"
	"crypto/tls"
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

// tenantRevokeHarness levanta UN Server mTLS/bufconn compartido (mismo patrón
// que newMTLSHarness en mtls_test.go) pero permite dialar VARIOS clientes con
// certs de Edge distintos contra ese mismo servidor -- lo que newMTLSHarness
// no ofrece (un solo client por harness). Necesario para el criterio de T3.3:
// revocar un tenant con DOS instalaciones vivas y comprobar que AMBAS reciben
// el LeaseUpdate, mientras un TERCER edge de OTRO tenant no se ve afectado.
type tenantRevokeHarness struct {
	srv       *gatewaygrpc.Server
	mgr       *lease.Manager
	leaseRepo *lease.MemoryRepository
	fleetRepo *fleet.MemoryRepository
	ca        *enroll.CA
	lis       *bufconn.Listener
}

func newTenantRevokeHarness(t *testing.T) *tenantRevokeHarness {
	t.Helper()

	ca := newDevCA(t)

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
	t.Cleanup(func() {
		gs.Stop()
		<-serveErrc
		if cerr := lis.Close(); cerr != nil {
			t.Errorf("cerrando listener: %v", cerr)
		}
	})

	return &tenantRevokeHarness{srv: srv, mgr: mgr, leaseRepo: leaseRepo, fleetRepo: fleetRepo, ca: ca, lis: lis}
}

// dial abre un cliente CloudLink NUEVO contra el MISMO servidor bufconn, con
// el cert de Edge dado -- la pieza que newMTLSHarness no expone.
func (h *tenantRevokeHarness) dial(t *testing.T, edgeCert tls.Certificate) cloudlinkv1.CloudLinkClient {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(mtls.ClientCreds(edgeCert, h.ca.Pool(), "localhost")),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("cerrando conn: %v", cerr)
		}
	})
	return cloudlinkv1.NewCloudLinkClient(conn)
}

// waitFleetOnlineFor espera a que sessionID figure online en el fleet repo del
// (tenantID, edgeID) dado -- a diferencia de waitFleetOnline en mtls_test.go,
// que fija testTenantID/testEdgeID, esta versión es genérica: la necesitamos
// para MÁS de un edge en el mismo test.
func waitFleetOnlineFor(t *testing.T, repo *fleet.MemoryRepository, tenantID, edgeID, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := repo.Get(context.Background(), tenantID, edgeID, sessionID)
		if err != nil {
			t.Fatalf("fleet Get: %v", err)
		}
		if ok && s.State == fleet.StateOnline {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timeout esperando fleet online de %q/%q/%q", tenantID, edgeID, sessionID)
}

// drainUntilRevoked lee del stream hasta que el Validator quede revocado
// (Apply de cada LeaseUpdate entrante), con el mismo patrón de
// TestMTLSRevokeBlocks en mtls_test.go.
func drainUntilRevoked(t *testing.T, stream cloudlinkv1.CloudLink_ConnectClient, v *cllease.Validator) {
	t.Helper()
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
}

// TestRevokeTenantNotifiesAllLiveEdgesOfThatTenantOnly cubre el criterio de
// aceptación de T3.3: revocar un tenant con DOS instalaciones conocidas y
// sesiones vivas de las dos ⇒ AMBAS reciben LeaseUpdate(Revoked); la
// regresión (REQ-055.9): un tercer edge de un tenant DISTINTO, no tocado,
// sigue con su tenant activo (TenantRevoked=false) tras la operación.
// Se pone rojo si: RevokeTenant deja de enumerar todas las instalaciones del
// tenant vía fleet.List, si el fan-out deja de firmar/empujar el LeaseUpdate
// por edge, o si la revocación se filtra a un tenant distinto del pedido.
func TestRevokeTenantNotifiesAllLiveEdgesOfThatTenantOnly(t *testing.T) {
	t.Parallel()
	h := newTenantRevokeHarness(t)

	const (
		tenantA = "11111111-1111-1111-1111-111111111111"
		edgeA1  = "edge-a1"
		edgeA2  = "edge-a2"
		tenantB = "22222222-2222-2222-2222-222222222222"
		edgeB1  = "edge-b1"
	)

	clientA1 := h.dial(t, issueEdgeCert(t, h.ca, tenantA, edgeA1))
	clientA2 := h.dial(t, issueEdgeCert(t, h.ca, tenantA, edgeA2))
	clientB1 := h.dial(t, issueEdgeCert(t, h.ca, tenantB, edgeB1))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamA1, err := clientA1.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect A1: %v", err)
	}
	if err := streamA1.Send(mtlsHeartbeat("sA1", 1)); err != nil {
		t.Fatalf("Send A1: %v", err)
	}

	streamA2, err := clientA2.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect A2: %v", err)
	}
	if err := streamA2.Send(mtlsHeartbeat("sA2", 1)); err != nil {
		t.Fatalf("Send A2: %v", err)
	}

	streamB1, err := clientB1.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect B1: %v", err)
	}
	if err := streamB1.Send(mtlsHeartbeat("sB1", 1)); err != nil {
		t.Fatalf("Send B1: %v", err)
	}

	waitFleetOnlineFor(t, h.fleetRepo, tenantA, edgeA1, "sA1")
	waitFleetOnlineFor(t, h.fleetRepo, tenantA, edgeA2, "sA2")
	waitFleetOnlineFor(t, h.fleetRepo, tenantB, edgeB1, "sB1")

	if err := h.srv.RevokeTenant(ctx, tenantA); err != nil {
		t.Fatalf("RevokeTenant: %v", err)
	}

	vA1 := cllease.NewValidator(h.mgr.PublicKey())
	drainUntilRevoked(t, streamA1, vA1)
	if vA1.CanOperate(true) {
		t.Fatal("edge-a1 debería quedar revocado (kill-switch comercial del tenant)")
	}

	vA2 := cllease.NewValidator(h.mgr.PublicKey())
	drainUntilRevoked(t, streamA2, vA2)
	if vA2.CanOperate(true) {
		t.Fatal("edge-a2 debería quedar revocado (kill-switch comercial del tenant)")
	}

	// Regresión REQ-055.9: el tenant B, no tocado, sigue activo.
	revokedB, err := h.leaseRepo.TenantRevoked(ctx, tenantB)
	if err != nil {
		t.Fatalf("TenantRevoked(tenantB): %v", err)
	}
	if revokedB {
		t.Fatal("el tenant NO revocado no debería verse afectado por RevokeTenant(tenantA)")
	}

	// El estado de T3.1 para el tenant revocado es efectivamente revocado.
	revokedA, err := h.leaseRepo.TenantRevoked(ctx, tenantA)
	if err != nil {
		t.Fatalf("TenantRevoked(tenantA): %v", err)
	}
	if !revokedA {
		t.Fatal("TenantRevoked(tenantA) debería ser true tras RevokeTenant")
	}
}
