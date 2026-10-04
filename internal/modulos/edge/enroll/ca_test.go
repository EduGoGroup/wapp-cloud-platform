//go:build pendiente

package enroll_test

// Los tests de la CA firmante. enroll.CA usa time.Now() sin reloj inyectable (se porta igual):
// las fechas se afirman con tolerancia alrededor del instante del test, sin esperar.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
)

// requireWindow afirma que el certificado vale desde ~un minuto antes del test hasta ~ttl
// después. before y after acotan el instante en que se emitió; x509 trunca a segundos.
func requireWindow(t *testing.T, cert *x509.Certificate, before, after time.Time, ttl time.Duration) {
	t.Helper()
	if lo, hi := before.Add(-time.Minute-time.Second), after.Add(-time.Minute); cert.NotBefore.Before(lo) || cert.NotBefore.After(hi) {
		t.Errorf("NotBefore = %v fuera de [%v, %v]: tenía que ser un minuto antes de ahora", cert.NotBefore, lo, hi)
	}
	if lo, hi := before.Add(ttl-time.Second), after.Add(ttl); cert.NotAfter.Before(lo) || cert.NotAfter.After(hi) {
		t.Errorf("NotAfter = %v fuera de [%v, %v]: la vida tenía que ser %v", cert.NotAfter, lo, hi, ttl)
	}
	if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		t.Errorf("el certificado no vale ahora (%v): ventana [%v, %v]", now, cert.NotBefore, cert.NotAfter)
	}
}

// selfSignedCA fabrica, con la biblioteca estándar, el certificado y la clave de una CA.
func selfSignedCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave de la CA del test: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "ca-del-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creando el certificado de la CA del test: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parseando el certificado de la CA del test: %v", err)
	}
	return cert, key
}

// signLeaf firma el CSR con la CA y falla el test si no puede.
func signLeaf(t *testing.T, ca *enroll.CA, csrPEM []byte, tenantID string) enroll.SignedCert {
	t.Helper()
	signed, err := ca.SignCSR(csrPEM, tenantID)
	if err != nil {
		t.Fatalf("SignCSR: error inesperado %v", err)
	}
	return signed
}

func TestDefaultEdgeCertTTL_IsNinetyDays(t *testing.T) {
	if enroll.DefaultEdgeCertTTL != 90*24*time.Hour {
		t.Errorf("DefaultEdgeCertTTL = %v, quería 90 días", enroll.DefaultEdgeCertTTL)
	}
}

func TestErrInvalidCSR_Text(t *testing.T) {
	if enroll.ErrInvalidCSR.Error() != "enroll: CSR inválido" {
		t.Errorf("ErrInvalidCSR = %q, quería %q", enroll.ErrInvalidCSR, "enroll: CSR inválido")
	}
}

// TestNewDevCA_IsAnEphemeralSelfSignedCA: una CA de verdad, con su nombre y su vida, y distinta
// en cada llamada.
func TestNewDevCA_IsAnEphemeralSelfSignedCA(t *testing.T) {
	before := time.Now()
	ca, err := enroll.NewDevCA("ca-efimera", 3*time.Hour, time.Hour)
	after := time.Now()
	if err != nil {
		t.Fatalf("NewDevCA: error inesperado %v", err)
	}
	cert := ca.Certificate()
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("el certificado no es una CA: IsCA=%v, KeyUsage=%b", cert.IsCA, cert.KeyUsage)
	}
	if cert.Subject.CommonName != "ca-efimera" {
		t.Errorf("CommonName = %q, quería %q", cert.Subject.CommonName, "ca-efimera")
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("la CA de dev no está autofirmada: %v", err)
	}
	requireWindow(t, cert, before, after, 3*time.Hour)

	other, err := enroll.NewDevCA("ca-efimera", 3*time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: error inesperado %v", err)
	}
	if bytes.Equal(other.Certificate().RawSubjectPublicKeyInfo, cert.RawSubjectPublicKeyInfo) {
		t.Error("dos CA de dev comparten clave: tienen que ser efímeras")
	}
}

// TestCA_PoolAndChain: Certificate, Pool y CAChainPEM hablan de la misma CA.
func TestCA_PoolAndChain(t *testing.T) {
	cert, key := selfSignedCA(t)
	ca := enroll.NewCA(cert, key, time.Hour)
	if ca.Certificate() != cert {
		t.Error("Certificate() no devuelve el certificado con el que se construyó la CA")
	}
	if chain := parseCertPEM(t, ca.CAChainPEM()); !chain.Equal(cert) {
		t.Error("CAChainPEM() no es el certificado de la CA en PEM")
	}
	// El Pool valida lo que firma esta CA y no lo que firma otra.
	csrPEM, _ := newCSR(t, testEdgeCN)
	leaf := parseCertPEM(t, signLeaf(t, ca, csrPEM, testTenant).EdgeCertPEM)
	opts := x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if _, err := leaf.Verify(opts); err != nil {
		t.Errorf("el Pool de la CA no valida el cert que ella firmó: %v", err)
	}
	opts.Roots = newDevCA(t).Pool()
	if _, err := leaf.Verify(opts); err == nil {
		t.Error("el Pool de OTRA CA validó el cert: los pools no pueden ser intercambiables")
	}
}

// TestSignCSR_IssuesAClientLeafForTheTenant: el cert hoja del Edge. EKU ClientAuth (y solo ese),
// Organization = tenant, identidad y clave del CSR, vida corta, y los metadatos que se persisten.
func TestSignCSR_IssuesAClientLeafForTheTenant(t *testing.T) {
	cert, key := selfSignedCA(t)
	ca := enroll.NewCA(cert, key, 2*time.Hour)
	// El CSR trae su propia Organization: la del cert es SIEMPRE el tenant, no la que pida el Edge.
	csrPEM, edgeKey := newCSR(t, testEdgeCN, "organizacion-que-pide-el-edge")

	before := time.Now()
	signed, err := ca.SignCSR(csrPEM, testTenant)
	after := time.Now()
	if err != nil {
		t.Fatalf("SignCSR: error inesperado %v", err)
	}
	leaf := parseCertPEM(t, signed.EdgeCertPEM)

	if !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("ExtKeyUsage = %v, quería solo ClientAuth", leaf.ExtKeyUsage)
	}
	if leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("KeyUsage = %b, quería solo DigitalSignature", leaf.KeyUsage)
	}
	if leaf.IsCA {
		t.Error("el cert hoja del Edge es una CA")
	}
	if !slices.Equal(leaf.Subject.Organization, []string{testTenant}) {
		t.Errorf("Organization = %v, quería [%s]", leaf.Subject.Organization, testTenant)
	}
	if leaf.Subject.CommonName != testEdgeCN {
		t.Errorf("CommonName = %q, quería %q (el del CSR)", leaf.Subject.CommonName, testEdgeCN)
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&edgeKey.PublicKey) {
		t.Error("la clave pública del cert no es la del CSR")
	}
	if err := leaf.CheckSignatureFrom(cert); err != nil {
		t.Errorf("el cert hoja no está firmado por la CA: %v", err)
	}
	requireWindow(t, leaf, before, after, 2*time.Hour)

	// Los metadatos del SignedCert son los del cert emitido.
	block, _ := pem.Decode(signed.EdgeCertPEM)
	if signed.SubjectCN != testEdgeCN {
		t.Errorf("SignedCert.SubjectCN = %q, quería %q", signed.SubjectCN, testEdgeCN)
	}
	if signed.SerialNumber != leaf.SerialNumber.Text(16) {
		t.Errorf("SignedCert.SerialNumber = %q, quería %q (hexadecimal)", signed.SerialNumber, leaf.SerialNumber.Text(16))
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(block.Bytes)); signed.Fingerprint != want {
		t.Errorf("SignedCert.Fingerprint = %q, quería el SHA-256 del DER, %q", signed.Fingerprint, want)
	}
	if !signed.NotBefore.Equal(leaf.NotBefore) || !signed.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("SignedCert validez [%v, %v], quería la del cert [%v, %v]", signed.NotBefore, signed.NotAfter, leaf.NotBefore, leaf.NotAfter)
	}
	if !bytes.Equal(signed.CAChainPEM, ca.CAChainPEM()) {
		t.Error("SignedCert.CAChainPEM no es la cadena de la CA")
	}

	// El serial es aleatorio: firmar otra vez el mismo CSR da otro cert.
	again := signLeaf(t, ca, csrPEM, testTenant)
	if again.SerialNumber == signed.SerialNumber || again.Fingerprint == signed.Fingerprint {
		t.Error("dos firmas del mismo CSR dieron el mismo serial o la misma huella")
	}
}

// TestSignCSR_InvalidCSR: no firma nada.
func TestSignCSR_InvalidCSR(t *testing.T) {
	signed, err := newDevCA(t).SignCSR([]byte("no es un CSR"), testTenant)
	if !errors.Is(err, enroll.ErrInvalidCSR) {
		t.Fatalf("error = %v, quería ErrInvalidCSR", err)
	}
	if signed.EdgeCertPEM != nil || signed.CAChainPEM != nil || signed.Fingerprint != "" || signed.SerialNumber != "" {
		t.Errorf("SignCSR de un CSR inválido devolvió datos: %+v", signed)
	}
}

// TestNewCA_NonPositiveTTLUsesDefault: certTTL <= 0 ⇒ 90 días.
func TestNewCA_NonPositiveTTLUsesDefault(t *testing.T) {
	cert, key := selfSignedCA(t)
	csrPEM, _ := newCSR(t, testEdgeCN)
	for _, ttl := range []time.Duration{0, -time.Hour} {
		before := time.Now()
		signed, err := enroll.NewCA(cert, key, ttl).SignCSR(csrPEM, testTenant)
		after := time.Now()
		if err != nil {
			t.Fatalf("SignCSR con certTTL=%v: error inesperado %v", ttl, err)
		}
		requireWindow(t, parseCertPEM(t, signed.EdgeCertPEM), before, after, enroll.DefaultEdgeCertTTL)
	}
}

// TestIssueServerCert: un cert de servidor de la misma CA, con sus SAN y su clave.
func TestIssueServerCert(t *testing.T) {
	ca := newDevCA(t)
	before := time.Now()
	certPEM, keyPEM, err := ca.IssueServerCert("gateway.test", []string{"gateway.test", "localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	after := time.Now()
	if err != nil {
		t.Fatalf("IssueServerCert: error inesperado %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("ExtKeyUsage = %v, quería solo ServerAuth", cert.ExtKeyUsage)
	}
	if cert.Subject.CommonName != "gateway.test" || !slices.Equal(cert.DNSNames, []string{"gateway.test", "localhost"}) {
		t.Errorf("CN = %q, DNSNames = %v", cert.Subject.CommonName, cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("IPAddresses = %v, quería [127.0.0.1]", cert.IPAddresses)
	}
	requireWindow(t, cert, before, after, time.Hour)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: "localhost"}); err != nil {
		t.Errorf("el cert de servidor no valida contra el Pool de la CA: %v", err)
	}
	if block, _ := pem.Decode(keyPEM); block == nil || block.Type != "PRIVATE KEY" {
		t.Errorf("la clave no es un PEM PKCS#8 (\"PRIVATE KEY\"): %q", keyPEM)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Errorf("la clave devuelta no corresponde al cert: %v", err)
	}
}

// TestLoadCAFromPEM: carga la CA de PEM con la clave en PKCS#8 o en EC, y firma con ella.
func TestLoadCAFromPEM(t *testing.T) {
	cert, key := selfSignedCA(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("serializando la clave PKCS#8: %v", err)
	}
	sec1, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("serializando la clave EC: %v", err)
	}
	keys := map[string][]byte{
		"pkcs8 key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
		"ec key":    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}),
	}
	csrPEM, _ := newCSR(t, testEdgeCN)
	for name, keyPEM := range keys {
		t.Run(name, func(t *testing.T) {
			ca, err := enroll.LoadCAFromPEM(certPEM, keyPEM, time.Hour)
			if err != nil {
				t.Fatalf("LoadCAFromPEM: error inesperado %v", err)
			}
			if !ca.Certificate().Equal(cert) {
				t.Error("la CA cargada no trae el certificado del PEM")
			}
			signed, err := ca.SignCSR(csrPEM, testTenant)
			if err != nil {
				t.Fatalf("SignCSR con la CA cargada: error inesperado %v", err)
			}
			if err := parseCertPEM(t, signed.EdgeCertPEM).CheckSignatureFrom(cert); err != nil {
				t.Errorf("lo firmado con la CA cargada no verifica contra su certificado: %v", err)
			}
		})
	}
}

// TestLoadCAFromPEM_Errors: cada forma de PEM inservible, con su texto.
func TestLoadCAFromPEM_Errors(t *testing.T) {
	cert, key := selfSignedCA(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("serializando la clave PKCS#8: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	// Una clave PKCS#8 válida que NO firma: X25519 es de acuerdo de claves, no crypto.Signer.
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave X25519 del test: %v", err)
	}
	x25519DER, err := x509.MarshalPKCS8PrivateKey(x25519)
	if err != nil {
		t.Fatalf("serializando la clave X25519: %v", err)
	}

	cases := []struct {
		name       string
		cert, key  []byte
		wantPrefix string
	}{
		{"cert is not pem", []byte("no es PEM"), keyPEM, "enroll: PEM de CA no es un CERTIFICATE"},
		{"cert pem of another type", keyPEM, keyPEM, "enroll: PEM de CA no es un CERTIFICATE"},
		{"cert does not parse", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("basura")}), keyPEM, "enroll: parsear cert CA: "},
		{"key is not pem", certPEM, []byte("no es PEM"), "enroll: PEM de clave CA inválido"},
		{"key does not parse", certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("basura")}), "enroll: no se pudo parsear la clave de la CA (PKCS#8 o EC)"},
		{"key cannot sign", certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x25519DER}), "enroll: clave CA no implementa crypto.Signer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ca, err := enroll.LoadCAFromPEM(c.cert, c.key, time.Hour)
			if err == nil || ca != nil {
				t.Fatalf("LoadCAFromPEM = (%v, %v), quería (nil, error)", ca, err)
			}
			if !strings.HasPrefix(err.Error(), c.wantPrefix) {
				t.Errorf("error = %q, quería el prefijo %q", err, c.wantPrefix)
			}
		})
	}
}

// TestParseAndVerifyCSR: acepta un CSR bien firmado; cualquier otro fallo es ErrInvalidCSR.
func TestParseAndVerifyCSR(t *testing.T) {
	csrPEM, _ := newCSR(t, testEdgeCN)
	csr, err := enroll.ParseAndVerifyCSR(csrPEM)
	if err != nil {
		t.Fatalf("ParseAndVerifyCSR: error inesperado %v", err)
	}
	if csr.Subject.CommonName != testEdgeCN {
		t.Errorf("CommonName = %q, quería %q", csr.Subject.CommonName, testEdgeCN)
	}

	block, _ := pem.Decode(csrPEM)
	// Manipulado: se cambia un byte del Subject (el CN) dentro del DER; la firma deja de verificar.
	tampered := bytes.Replace(block.Bytes, []byte(testEdgeCN), []byte("edge-evil-1"), 1)
	if bytes.Equal(tampered, block.Bytes) {
		t.Fatal("el CSR del test no contiene el CN en claro: no se pudo manipular")
	}
	bad := map[string][]byte{
		"nil":               nil,
		"not pem":           []byte("esto no es PEM"),
		"pem of other type": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}),
		"garbage der":       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("basura")}),
		"tampered subject":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered}),
	}
	for name, input := range bad {
		t.Run(name, func(t *testing.T) {
			csr, err := enroll.ParseAndVerifyCSR(input)
			if !errors.Is(err, enroll.ErrInvalidCSR) {
				t.Fatalf("error = %v, quería ErrInvalidCSR", err)
			}
			if csr != nil {
				t.Error("devolvió un CSR junto con el error")
			}
		})
	}
}
