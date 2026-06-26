package enroll_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
)

// newTestCSR genera un par de claves EC y un CSR en PEM con el CommonName dado.
// Modela lo que hará el Edge: la clave privada se queda aquí (en el cliente).
func newTestCSR(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generar clave: %v", err)
	}
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatalf("crear CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestParseAndVerifyCSR(t *testing.T) {
	csrPEM := newTestCSR(t, "edge-001")
	if _, err := enroll.ParseAndVerifyCSR(csrPEM); err != nil {
		t.Fatalf("CSR válido debería parsear: %v", err)
	}

	if _, err := enroll.ParseAndVerifyCSR([]byte("no soy un PEM")); err == nil {
		t.Fatal("CSR corrupto debería fallar")
	}
}

func TestCASignCSR(t *testing.T) {
	ca, err := enroll.NewDevCA("wapp-dev-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	csrPEM := newTestCSR(t, "edge-001")

	signed, err := ca.SignCSR(csrPEM, "tenant-42")
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	block, _ := pem.Decode(signed.EdgeCertPEM)
	if block == nil {
		t.Fatal("el cert emitido no es PEM válido")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsear cert hoja: %v", err)
	}

	// EKU ClientAuth.
	hasClientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("el cert hoja debería tener EKU ClientAuth")
	}

	// Organization = tenantID.
	if len(leaf.Subject.Organization) != 1 || leaf.Subject.Organization[0] != "tenant-42" {
		t.Errorf("Organization: got %v, want [tenant-42]", leaf.Subject.Organization)
	}

	// CommonName preservado del CSR.
	if leaf.Subject.CommonName != "edge-001" {
		t.Errorf("CommonName: got %q, want edge-001", leaf.Subject.CommonName)
	}

	// El cert hoja verifica contra la CA (Pool).
	roots := ca.Pool()
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("el cert hoja debería verificar contra la CA: %v", err)
	}

	// Metadatos poblados.
	if signed.SerialNumber == "" || signed.Fingerprint == "" {
		t.Error("serial/fingerprint deberían venir poblados")
	}
	if signed.NotAfter.Before(signed.NotBefore) {
		t.Error("not_after debería ser posterior a not_before")
	}
}
