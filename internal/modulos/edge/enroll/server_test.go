//go:build pendiente

package enroll_test

// Los tests del rpc EnrollEdge, por bufconn (en memoria, sin red): los códigos gRPC y los textos
// que ve el Edge, y qué claves publica la respuesta. El montaje (rig) está en helpers_test.go.

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

// El Server es el servicio Enrollment del contrato CloudLink.
var _ cloudlinkv1.EnrollmentServer = (*enroll.Server)(nil)

// dialEnroll levanta el servidor de enrolamiento sobre bufconn, SIN mTLS (el Edge aún no tiene
// cert), y devuelve un cliente conectado. Todo se cierra al acabar el test.
func dialEnroll(t *testing.T, svc *enroll.Service, opts ...enroll.ServerOption) cloudlinkv1.EnrollmentClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	srv := enroll.NewServer(svc, logger.New(logger.WithWriter(io.Discard)), opts...)
	srv.Register(gs)

	served := make(chan error, 1)
	go func() { served <- gs.Serve(lis) }()

	cc, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := cc.Close(); err != nil {
			t.Errorf("cerrando la conexión: %v", err)
		}
		gs.Stop()
		if err := <-served; err != nil {
			t.Errorf("gs.Serve devolvió %v", err)
		}
	})
	return cloudlinkv1.NewEnrollmentClient(cc)
}

// enrollEdge llama al rpc con un plazo.
func enrollEdge(t *testing.T, client cloudlinkv1.EnrollmentClient, code string, csrPEM []byte) (*cloudlinkv1.EnrollEdgeResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{ActivationCode: code, CsrPem: csrPEM})
}

// requireStatus afirma el código gRPC y el texto EXACTO del error, y que no hubo respuesta.
func requireStatus(t *testing.T, resp *cloudlinkv1.EnrollEdgeResponse, err error, wantCode codes.Code, wantMsg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("el rpc tuvo éxito (tenant %q); quería %v %q", resp.GetTenantId(), wantCode, wantMsg)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("el error no es un status gRPC: %v", err)
	}
	if st.Code() != wantCode || st.Message() != wantMsg {
		t.Errorf("status = %v %q, quería %v %q", st.Code(), st.Message(), wantCode, wantMsg)
	}
	if resp != nil {
		t.Errorf("el rpc falló pero devolvió una respuesta: %v", resp)
	}
}

// TestEnrollEdge_ValidCode_ReturnsTheCertificate: código válido → cert, cadena y tenant. Sin
// opciones, la respuesta NO publica ninguna clave.
func TestEnrollEdge_ValidCode_ReturnsTheCertificate(t *testing.T) {
	r := newRig(t)
	client := dialEnroll(t, r.svc)
	csrPEM, _ := newCSR(t, testEdgeCN)
	resp, err := enrollEdge(t, client, testCode, csrPEM)
	if err != nil {
		t.Fatalf("EnrollEdge: error inesperado %v", err)
	}
	if resp.GetTenantId() != testTenant {
		t.Errorf("tenant_id = %q, quería %q", resp.GetTenantId(), testTenant)
	}
	if !bytes.Equal(resp.GetCaChainPem(), r.ca.CAChainPEM()) {
		t.Error("ca_chain_pem no es la cadena de la CA")
	}
	leaf := parseCertPEM(t, resp.GetEdgeCertPem())
	if err := leaf.CheckSignatureFrom(r.ca.Certificate()); err != nil {
		t.Errorf("edge_cert_pem no está firmado por la CA: %v", err)
	}
	if leaf.Subject.CommonName != testEdgeCN {
		t.Errorf("CommonName del cert = %q, quería %q", leaf.Subject.CommonName, testEdgeCN)
	}
	if len(resp.GetCloudEncPubkey()) != 0 || len(resp.GetLeasePubkey()) != 0 {
		t.Errorf("sin opciones la respuesta publicó claves: cloud_enc_pubkey %d bytes, lease_pubkey %d bytes",
			len(resp.GetCloudEncPubkey()), len(resp.GetLeasePubkey()))
	}
	if n := len(r.certs.Records()); n != 1 {
		t.Errorf("se registraron %d certificados, quería 1", n)
	}
}

// TestEnrollEdge_PublishesKeysOnlyWhenConfigured: cloud_enc_pubkey y lease_pubkey salen SOLO si
// se configuraron con su opción, cada una por su lado, y con los bytes dados.
func TestEnrollEdge_PublishesKeysOnlyWhenConfigured(t *testing.T) {
	cloudKey := bytes.Repeat([]byte{0xC1}, 32)
	leaseKey := bytes.Repeat([]byte{0x1E}, 32)
	cases := []struct {
		name                 string
		opts                 []enroll.ServerOption
		wantCloud, wantLease []byte
	}{
		{"none", nil, nil, nil},
		{"only cloud enc pubkey", []enroll.ServerOption{enroll.WithCloudEncPubkey(cloudKey)}, cloudKey, nil},
		{"only lease pubkey", []enroll.ServerOption{enroll.WithLeasePubKey(leaseKey)}, nil, leaseKey},
		{"both", []enroll.ServerOption{enroll.WithCloudEncPubkey(cloudKey), enroll.WithLeasePubKey(leaseKey)}, cloudKey, leaseKey},
		{"empty keys publish nothing", []enroll.ServerOption{enroll.WithCloudEncPubkey(nil), enroll.WithLeasePubKey([]byte{})}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			csrPEM, _ := newCSR(t, testEdgeCN)
			resp, err := enrollEdge(t, dialEnroll(t, r.svc, c.opts...), testCode, csrPEM)
			if err != nil {
				t.Fatalf("EnrollEdge: error inesperado %v", err)
			}
			if !bytes.Equal(resp.GetCloudEncPubkey(), c.wantCloud) {
				t.Errorf("cloud_enc_pubkey = %x, quería %x", resp.GetCloudEncPubkey(), c.wantCloud)
			}
			if !bytes.Equal(resp.GetLeasePubkey(), c.wantLease) {
				t.Errorf("lease_pubkey = %x, quería %x", resp.GetLeasePubkey(), c.wantLease)
			}
		})
	}
}

// TestEnrollEdge_RejectedCode_IsPermissionDenied: reutilizado, ausente, vencido o «inválido»
// (el de Postgres): el MISMO código gRPC y el MISMO texto. El Edge no sabe por qué.
func TestEnrollEdge_RejectedCode_IsPermissionDenied(t *testing.T) {
	const msg = "código de activación inválido"
	csrPEM, _ := newCSR(t, testEdgeCN)

	t.Run("reused", func(t *testing.T) {
		r := newRig(t)
		client := dialEnroll(t, r.svc)
		if _, err := enrollEdge(t, client, testCode, csrPEM); err != nil {
			t.Fatalf("primer EnrollEdge: error inesperado %v", err)
		}
		resp, err := enrollEdge(t, client, testCode, csrPEM)
		requireStatus(t, resp, err, codes.PermissionDenied, msg)
		if n := len(r.certs.Records()); n != 1 {
			t.Errorf("tras reutilizar el código hay %d certificados registrados, quería 1", n)
		}
	})
	t.Run("absent", func(t *testing.T) {
		r := newRig(t)
		resp, err := enrollEdge(t, dialEnroll(t, r.svc), "codigo-que-no-existe", csrPEM)
		requireStatus(t, resp, err, codes.PermissionDenied, msg)
	})
	t.Run("expired", func(t *testing.T) {
		r := newRig(t)
		r.codes.Add("vencido", testTenant, time.Now().Add(-time.Hour))
		resp, err := enrollEdge(t, dialEnroll(t, r.svc), "vencido", csrPEM)
		requireStatus(t, resp, err, codes.PermissionDenied, msg)
	})
	t.Run("store says invalid", func(t *testing.T) {
		r := newRig(t)
		r.store.err = enroll.ErrCodeInvalid
		resp, err := enrollEdge(t, dialEnroll(t, r.svc), testCode, csrPEM)
		requireStatus(t, resp, err, codes.PermissionDenied, msg)
	})
}

// TestEnrollEdge_BadCSR_IsInvalidArgument: CSR ausente o inválido → InvalidArgument, cada uno
// con su texto, y sin quemar el código.
func TestEnrollEdge_BadCSR_IsInvalidArgument(t *testing.T) {
	r := newRig(t)
	client := dialEnroll(t, r.svc)

	resp, err := enrollEdge(t, client, testCode, nil)
	requireStatus(t, resp, err, codes.InvalidArgument, "csr_pem requerido")
	resp, err = enrollEdge(t, client, testCode, []byte("esto no es un CSR"))
	requireStatus(t, resp, err, codes.InvalidArgument, "CSR inválido")

	if got := r.journal.list(); len(got) != 0 {
		t.Errorf("con el CSR mal hubo escrituras: %q", got)
	}
	csrPEM, _ := newCSR(t, testEdgeCN)
	if _, err := enrollEdge(t, client, testCode, csrPEM); err != nil {
		t.Errorf("el código se quemó con un CSR malo: %v", err)
	}
}

// TestEnrollEdge_OtherFailure_IsInternal: un fallo que no es del CSR ni del código —aquí, el
// registro del certificado— sale como Internal con un texto que no cuenta la causa.
func TestEnrollEdge_OtherFailure_IsInternal(t *testing.T) {
	r := newRig(t)
	r.repo.err = errors.New("detalle interno que el Edge no debe ver")
	csrPEM, _ := newCSR(t, testEdgeCN)
	resp, err := enrollEdge(t, dialEnroll(t, r.svc), testCode, csrPEM)
	requireStatus(t, resp, err, codes.Internal, "enrolamiento falló")
}

// TestEnrollEdge_ActivationCodeIsNotNormalized: el corpus adversario, por el rpc. Ninguna
// variante se «arregla» por el camino: todas son un código desconocido (PermissionDenied), el
// store recibe cada una tal cual, y el código bueno sigue sin quemar.
func TestEnrollEdge_ActivationCodeIsNotNormalized(t *testing.T) {
	r := newRig(t)
	client := dialEnroll(t, r.svc)
	csrPEM, _ := newCSR(t, testEdgeCN)
	for name, code := range enrollhelpertest.AdversarialCodes(testCode) {
		t.Run(name, func(t *testing.T) {
			before := len(r.journal.list())
			resp, err := enrollEdge(t, client, code, csrPEM)
			requireStatus(t, resp, err, codes.PermissionDenied, "código de activación inválido")
			got := r.journal.list()[before:]
			if len(got) != 1 || got[0] != "consume:"+code {
				t.Errorf("al store llegó %q, quería solo %q: el código tal cual", got, "consume:"+code)
			}
		})
	}
	if n := len(r.certs.Records()); n != 0 {
		t.Fatalf("se registraron %d certificados con códigos adversarios", n)
	}
	resp, err := enrollEdge(t, client, testCode, csrPEM)
	if err != nil || resp.GetTenantId() != testTenant {
		t.Errorf("el código exacto tras el corpus = (%q, %v), quería (%q, nil)", resp.GetTenantId(), err, testTenant)
	}
}
