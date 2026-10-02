//go:build integracion

package procesos

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// El enrolamiento del Edge de prueba: la llamada EnrollEdge por el listener de enrolamiento, la
// validación de lo que devuelve, y el CSR y la clave que el Edge fabrica para pedir su certificado.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Enrolamiento
// ---------------------------------------------------------------------------------------------

// enrolar enrola un Edge nuevo con el código de activación dado y devuelve el Edge listo para
// conectar: genera una clave ECDSA P-256 y un CSR cuyo CommonName es el id del Edge, llama a
// Enrollment/EnrollEdge en el listener de enrolamiento del servidor (s.EnrolarAddr) con TLS de
// servidor —raíz: la CA de la PKI del servidor, ServerName «localhost», sin certificado de
// cliente— y guarda el certificado emitido, la cadena, el tenant y las dos públicas. Falla
// (t.Fatalf) si el enrolamiento no sale: el código es inválido, ya se usó o caducó, el servidor
// no responde o la respuesta viene incompleta.
func enrolar(t *testing.T, s *servidor, codigo string) *edge {
	t.Helper()
	e, err := enrolarErr(t, s, codigo)
	if err != nil {
		t.Fatalf("enrolar con el código %q: %v", codigo, err)
	}
	return e
}

// enrolarErr es enrolar para los casos negativos: en vez de fallar el test devuelve el error del
// enrolamiento. Un código inválido, usado o caducado devuelve un error que envuelve un status
// gRPC PermissionDenied (status.Code(err) lo dice); un CSR inválido, InvalidArgument; una
// respuesta incompleta, un error sin status. Solo falla el test (t.Fatalf) ante un defecto del
// propio arnés (no puede generar la clave o el CSR).
func enrolarErr(t *testing.T, s *servidor, codigo string) (*edge, error) {
	t.Helper()
	clave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("enrolar: generar la clave del Edge: %v", err)
	}
	edgeID := "edge-" + edgeAleatorioHex(t, 6)
	csr, err := edgeCSR(clave, edgeID)
	if err != nil {
		t.Fatalf("enrolar: %v", err)
	}
	resp, err := edgeLlamarEnrolamiento(t.Context(), s, codigo, csr)
	if err != nil {
		return nil, err
	}
	if err := edgeValidarEnrolamiento(resp); err != nil {
		return nil, err
	}
	e := nuevoEdge(resp.GetTenantId(), edgeID, "sesion-"+edgeAleatorioHex(t, 6),
		resp.GetCloudEncPubkey(), ed25519.PublicKey(resp.GetLeasePubkey()))
	e.conectarAddr = s.ConectarAddr
	e.clave = clave
	e.certPEM = resp.GetEdgeCertPem()
	e.caPEM = resp.GetCaChainPem()
	return e, nil
}

// edgeLlamarEnrolamiento abre una conexión TLS de servidor al listener de enrolamiento, llama a
// EnrollEdge con el código y el CSR y cierra la conexión. Devuelve la respuesta, o el error de la
// llamada (envuelto, así que status.Code lo atraviesa) unido al del cierre si lo hubo.
func edgeLlamarEnrolamiento(ctx context.Context, s *servidor, codigo string, csr []byte) (resp *cloudlinkv1.EnrollEdgeResponse, err error) {
	creds := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    s.PKI.Pool(),
		ServerName: edgeNombreServidor,
	})
	conn, err := grpc.NewClient("passthrough:///"+s.EnrolarAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc.NewClient hacia el enrolamiento %s: %w", s.EnrolarAddr, err)
	}
	defer func() {
		if errCierre := conn.Close(); errCierre != nil {
			err = errors.Join(err, fmt.Errorf("cerrar la conexión de enrolamiento: %w", errCierre))
		}
	}()
	ctx, cancelar := context.WithTimeout(ctx, edgeTopeEnrolar)
	defer cancelar()
	resp, err = cloudlinkv1.NewEnrollmentClient(conn).EnrollEdge(ctx, &cloudlinkv1.EnrollEdgeRequest{
		ActivationCode: codigo,
		CsrPem:         csr,
	})
	if err != nil {
		return nil, fmt.Errorf("EnrollEdge: %w", err)
	}
	return resp, nil
}

// edgeValidarEnrolamiento comprueba que la respuesta de EnrollEdge trae todo lo que el Edge
// necesita: certificado, cadena, tenant, la pública X25519 de cifrado de la nube (32 B) y la
// Ed25519 del lease (32 B). Devuelve el primer faltante, o nil.
func edgeValidarEnrolamiento(resp *cloudlinkv1.EnrollEdgeResponse) error {
	switch {
	case len(resp.GetEdgeCertPem()) == 0:
		return errors.New("EnrollEdge respondió sin edge_cert_pem")
	case len(resp.GetCaChainPem()) == 0:
		return errors.New("EnrollEdge respondió sin ca_chain_pem")
	case resp.GetTenantId() == "":
		return errors.New("EnrollEdge respondió sin tenant_id")
	case len(resp.GetCloudEncPubkey()) != 32:
		return fmt.Errorf("cloud_enc_pubkey mide %d bytes, quería 32 (X25519)", len(resp.GetCloudEncPubkey()))
	case len(resp.GetLeasePubkey()) != ed25519.PublicKeySize:
		return fmt.Errorf("lease_pubkey mide %d bytes, quería %d (Ed25519)", len(resp.GetLeasePubkey()), ed25519.PublicKeySize)
	}
	return nil
}

// edgeCSR arma el CSR de un Edge: un PEM «CERTIFICATE REQUEST» firmado con la clave dada, cuyo
// Subject.CommonName es el id del Edge (el servidor le añade el tenant como Organization al emitir
// el certificado). Devuelve error si x509 no puede firmarlo.
func edgeCSR(clave *ecdsa.PrivateKey, edgeID string) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: edgeID}}, clave)
	if err != nil {
		return nil, fmt.Errorf("crear el CSR de %s: %w", edgeID, err)
	}
	return edgePEM("CERTIFICATE REQUEST", der), nil
}

// edgeClavePEM serializa la clave del Edge en PKCS#8 con el tipo PEM «PRIVATE KEY», el formato con
// el que el Edge real guarda la suya. Devuelve error si no se puede serializar.
func edgeClavePEM(clave *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(clave)
	if err != nil {
		return nil, fmt.Errorf("serializar la clave del Edge (PKCS#8): %w", err)
	}
	return edgePEM("PRIVATE KEY", der), nil
}

// edgeAleatorioHex devuelve n bytes aleatorios en hexadecimal (2n caracteres). Falla el test
// (t.Fatalf) si el sistema no da bytes aleatorios.
func edgeAleatorioHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("leer aleatoriedad del sistema: %v", err)
	}
	return hex.EncodeToString(b)
}
