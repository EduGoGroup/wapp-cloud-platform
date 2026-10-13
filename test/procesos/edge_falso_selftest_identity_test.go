//go:build integracion

package procesos

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Los tests propios, sin servidor, de la identidad del Edge de prueba: el CSR, la clave y el
// certificado de cliente (TestArnes_EdgeIdentidad), y la validación de la respuesta de enrolamiento.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeIdentidad prueba, sin servidor, lo que el Edge fabrica para el mTLS: el CSR es un
// PEM «CERTIFICATE REQUEST» con firma válida y el id del Edge como CommonName; la clave se
// serializa en PKCS#8 y se interpreta de vuelta; y conCertificadoDe emite un certificado de cliente
// con la identidad del Edge (CommonName, Organization = tenant) que verifica contra la CA ajena y
// NO contra la de otra PKI, con la misma clave.
func TestArnes_EdgeIdentidad(t *testing.T) {
	t.Parallel()
	clave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edgeProbarCSRyClave(t, clave)
	edgeProbarCertificadoAjeno(t, clave)
}

// edgeProbarCSRyClave comprueba el CSR (tipo PEM, CommonName, firma) y la ida y vuelta de la clave
// en PKCS#8.
func edgeProbarCSRyClave(t *testing.T, clave *ecdsa.PrivateKey) {
	t.Helper()
	csrPEM, err := edgeCSR(clave, "edge-uno")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(edgePEMPrimero(t, "CERTIFICATE REQUEST", csrPEM))
	if err != nil {
		t.Fatalf("el CSR no se interpreta: %v", err)
	}
	if csr.Subject.CommonName != "edge-uno" || csr.CheckSignature() != nil {
		t.Errorf("CSR: CommonName %q, firma %v", csr.Subject.CommonName, csr.CheckSignature())
	}

	clavePEM, err := edgeClavePEM(clave)
	if err != nil {
		t.Fatal(err)
	}
	vuelta, err := x509.ParsePKCS8PrivateKey(edgePEMPrimero(t, "PRIVATE KEY", clavePEM))
	if err != nil {
		t.Fatalf("la clave PKCS#8 no se interpreta: %v", err)
	}
	if k, ok := vuelta.(*ecdsa.PrivateKey); !ok || !k.Equal(clave) {
		t.Errorf("la clave de vuelta no es la original")
	}
}

// edgeProbarCertificadoAjeno comprueba lo que emite conCertificadoDe: sujeto, clave pública,
// verificación contra la CA que lo firmó y no contra otra, y que la copia conserva la identidad con
// una sesión propia.
func edgeProbarCertificadoAjeno(t *testing.T, clave *ecdsa.PrivateKey) {
	t.Helper()
	e := nuevoEdge("11111111-1111-4111-8111-111111111111", "edge-uno", "sesion-1", nil, nil)
	e.clave = clave
	e.conectarAddr = "127.0.0.1:1"
	e.caPEM = []byte("ca")
	otra, tercera := nuevaPKI(t), nuevaPKI(t)
	ajeno := e.conCertificadoDe(t, otra)

	cert, err := x509.ParseCertificate(edgePEMPrimero(t, "CERTIFICATE", ajeno.certPEM))
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "edge-uno" || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != e.TenantID {
		t.Errorf("sujeto = %v, quería CN edge-uno y O = %s", cert.Subject, e.TenantID)
	}
	opciones := func(p pki) x509.VerifyOptions {
		return x509.VerifyOptions{Roots: p.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	}
	if _, err := cert.Verify(opciones(otra)); err != nil {
		t.Errorf("el certificado no verifica contra la CA que lo firmó: %v", err)
	}
	if _, err := cert.Verify(opciones(tercera)); err == nil {
		t.Errorf("el certificado verificó contra una CA que no lo firmó")
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&clave.PublicKey) {
		t.Errorf("el certificado ajeno no lleva la clave del Edge")
	}
	if ajeno.SessionID == e.SessionID || ajeno.EdgeID != e.EdgeID || ajeno.conectarAddr != e.conectarAddr || string(ajeno.caPEM) != "ca" {
		t.Errorf("la copia ajena no conserva la identidad o comparte sesión: %+v", ajeno)
	}
}

// TestArnes_EdgeEnrolamientoIncompleto prueba edgeValidarEnrolamiento con una respuesta buena y con
// cada defecto posible, y edgeNumeroDe con los formatos de remitente que se usan.
func TestArnes_EdgeEnrolamientoIncompleto(t *testing.T) {
	t.Parallel()
	buena := func() *cloudlinkv1.EnrollEdgeResponse {
		return &cloudlinkv1.EnrollEdgeResponse{
			EdgeCertPem: []byte("c"), CaChainPem: []byte("ca"), TenantId: "t",
			CloudEncPubkey: make([]byte, 32), LeasePubkey: make([]byte, ed25519.PublicKeySize),
		}
	}
	if err := edgeValidarEnrolamiento(buena()); err != nil {
		t.Errorf("una respuesta completa se rechazó: %v", err)
	}
	defectos := map[string]func(*cloudlinkv1.EnrollEdgeResponse){
		"sin certificado": func(r *cloudlinkv1.EnrollEdgeResponse) { r.EdgeCertPem = nil },
		"sin cadena":      func(r *cloudlinkv1.EnrollEdgeResponse) { r.CaChainPem = nil },
		"sin tenant":      func(r *cloudlinkv1.EnrollEdgeResponse) { r.TenantId = "" },
		"X25519 corta":    func(r *cloudlinkv1.EnrollEdgeResponse) { r.CloudEncPubkey = make([]byte, 31) },
		"sin X25519":      func(r *cloudlinkv1.EnrollEdgeResponse) { r.CloudEncPubkey = nil },
		"Ed25519 larga":   func(r *cloudlinkv1.EnrollEdgeResponse) { r.LeasePubkey = make([]byte, 33) },
		"sin Ed25519":     func(r *cloudlinkv1.EnrollEdgeResponse) { r.LeasePubkey = nil },
	}
	for nombre, rompe := range defectos {
		r := buena()
		rompe(r)
		if err := edgeValidarEnrolamiento(r); err == nil {
			t.Errorf("%s: se aceptó una respuesta incompleta", nombre)
		}
	}
	for de, quiere := range map[string]string{
		"573001110000@s.whatsapp.net": "573001110000",
		"+573001110000":               "573001110000",
		"573001110000":                "573001110000",
		"abc123@lid":                  "",
		"":                            "",
		"@s.whatsapp.net":             "",
		"57 300":                      "",
	} {
		if got := edgeNumeroDe(de); got != quiere {
			t.Errorf("edgeNumeroDe(%q) = %q, quería %q", de, got, quiere)
		}
	}
}
