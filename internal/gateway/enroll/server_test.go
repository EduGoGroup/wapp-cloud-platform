package enroll_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
)

// startEnrollServer levanta el Enrollment SIN mTLS (insecure sobre bufconn): el
// Edge aún no tiene cert. Devuelve un EnrollmentClient ya conectado.
func startEnrollServer(t *testing.T, svc *enroll.Service, opts ...enroll.ServerOption) cloudlinkv1.EnrollmentClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	srv := enroll.NewServer(svc, logger.New(logger.WithWriter(io.Discard)), opts...)
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() {
		serveErrc <- gs.Serve(lis)
	}()

	cc, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient (enroll): %v", err)
	}
	t.Cleanup(func() {
		if closeErr := cc.Close(); closeErr != nil {
			t.Errorf("cerrando conn: %v", closeErr)
		}
		gs.Stop()
		if serveErr := <-serveErrc; serveErr != nil {
			t.Errorf("gs.Serve devolvió error: %v", serveErr)
		}
	})
	return cloudlinkv1.NewEnrollmentClient(cc)
}

// newService cablea un Service con MemoryStore + DevCA + repo de certs en memoria.
func newService(t *testing.T) (*enroll.Service, *enroll.MemoryStore, *enroll.MemoryEdgeCertRepository) {
	t.Helper()
	ca, err := enroll.NewDevCA("wapp-dev-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	store := enroll.NewMemoryStore()
	certs := enroll.NewMemoryEdgeCertRepository()
	return enroll.NewService(store, ca, certs), store, certs
}

func TestEnrollEdge_ValidCode(t *testing.T) {
	svc, store, certs := newService(t)
	store.Add("CODE-OK", "tenant-42", time.Now().Add(time.Hour))
	cli := startEnrollServer(t, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-OK",
		CsrPem:         newTestCSR(t, "edge-001"),
	})
	if err != nil {
		t.Fatalf("EnrollEdge (OK): %v", err)
	}
	if resp.GetTenantId() != "tenant-42" {
		t.Errorf("tenant_id: got %q, want tenant-42", resp.GetTenantId())
	}
	if len(resp.GetEdgeCertPem()) == 0 || len(resp.GetCaChainPem()) == 0 {
		t.Fatal("respuesta debería traer edge_cert_pem y ca_chain_pem")
	}
	if got := certs.Records(); len(got) != 1 || got[0].TenantID != "tenant-42" {
		t.Fatalf("debería haberse persistido 1 edge_cert para tenant-42: %+v", got)
	}
}

// TestEnrollEdge_IncluyeCloudEncPubkey verifica que, cableado con
// WithCloudEncPubkey, el enrolamiento publica la pública X25519 de cifrado de la
// nube en la respuesta (Plan 011 §6.4) para que el Edge selle el ingreso.
func TestEnrollEdge_IncluyeCloudEncPubkey(t *testing.T) {
	svc, store, _ := newService(t)
	store.Add("CODE-ENC", "tenant-42", time.Now().Add(time.Hour))

	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	cli := startEnrollServer(t, svc, enroll.WithCloudEncPubkey(pub))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-ENC",
		CsrPem:         newTestCSR(t, "edge-enc"),
	})
	if err != nil {
		t.Fatalf("EnrollEdge (enc): %v", err)
	}
	if got := resp.GetCloudEncPubkey(); !bytes.Equal(got, pub) {
		t.Fatalf("cloud_enc_pubkey = %x, quiero %x", got, pub)
	}
}

// TestEnrollEdge_SinCloudEncPubkey verifica el fallback (§10.H): sin la opción,
// la respuesta no trae cloud_enc_pubkey (el Edge sube en claro, mTLS protege).
func TestEnrollEdge_SinCloudEncPubkey(t *testing.T) {
	svc, store, _ := newService(t)
	store.Add("CODE-NOENC", "tenant-42", time.Now().Add(time.Hour))
	cli := startEnrollServer(t, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-NOENC",
		CsrPem:         newTestCSR(t, "edge-noenc"),
	})
	if err != nil {
		t.Fatalf("EnrollEdge (sin enc): %v", err)
	}
	if got := resp.GetCloudEncPubkey(); len(got) != 0 {
		t.Fatalf("cloud_enc_pubkey debería venir vacío, got %x", got)
	}
}

func TestEnrollEdge_ReusedCode(t *testing.T) {
	svc, store, _ := newService(t)
	store.Add("CODE-1USE", "tenant-42", time.Now().Add(time.Hour))
	cli := startEnrollServer(t, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-1USE", CsrPem: newTestCSR(t, "edge-a"),
	}); err != nil {
		t.Fatalf("primer uso debería funcionar: %v", err)
	}
	_, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-1USE", CsrPem: newTestCSR(t, "edge-b"),
	})
	assertCode(t, err, codes.PermissionDenied, "código reusado")
}

func TestEnrollEdge_AbsentCode(t *testing.T) {
	svc, _, _ := newService(t) // store vacío
	cli := startEnrollServer(t, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "DESCONOCIDO", CsrPem: newTestCSR(t, "edge-x"),
	})
	assertCode(t, err, codes.PermissionDenied, "código ausente")
}

func TestEnrollEdge_InvalidCSR(t *testing.T) {
	svc, store, _ := newService(t)
	store.Add("CODE-OK", "tenant-42", time.Now().Add(time.Hour))
	cli := startEnrollServer(t, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := cli.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: "CODE-OK", CsrPem: []byte("no soy un CSR"),
	})
	assertCode(t, err, codes.InvalidArgument, "CSR inválido")
}

func assertCode(t *testing.T, err error, want codes.Code, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: se esperaba error %v, got nil", what, want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("%s: código gRPC got %v, want %v (err=%v)", what, got, want, err)
	}
}
