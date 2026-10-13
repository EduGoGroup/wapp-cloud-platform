//go:build integracion

package procesos

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestArnes_PKI y lo que comprueba del material: los ficheros, la CA, el certificado de servidor,
// las variables de entorno y que cada corrida genera claves distintas.
// Sale de pki_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_PKI comprueba que la PKI generada es la que el servidor y el Edge necesitan: ficheros
// con los modos pedidos, CA con el criterio de carga del servidor, cert de servidor verificable
// contra Pool() con ServerName localhost, apretón TLS 1.3 y mTLS reales por loopback, y
// aleatoriedad entre llamadas. No necesita Docker.
func TestArnes_PKI(t *testing.T) {
	p := nuevaPKI(t)
	t.Run("ficheros", func(t *testing.T) { pkiVerificarFicheros(t, p) })
	t.Run("ca", func(t *testing.T) { pkiVerificarCA(t, p) })
	t.Run("servidor", func(t *testing.T) { pkiVerificarServidor(t, p) })
	t.Run("entorno", func(t *testing.T) { pkiVerificarEntorno(t, p) })
	t.Run("tls13", func(t *testing.T) { pkiVerificarTLS(t, p) })
	t.Run("mtls", func(t *testing.T) { pkiVerificarMTLS(t, p) })
	t.Run("aleatoria", func(t *testing.T) { pkiVerificarAleatoria(t, p) })
}

// pkiVerificarFicheros comprueba modos (claves 0600, certs 0644), que los cuatro ficheros estén en
// un mismo directorio y que CACertPEM sea el contenido de CACertFile.
func pkiVerificarFicheros(t *testing.T, p pki) {
	t.Helper()
	esperado := map[string]os.FileMode{
		p.CACertFile:     pkiModoPublic,
		p.CAKeyFile:      pkiModoClave,
		p.ServerCertFile: pkiModoPublic,
		p.ServerKeyFile:  pkiModoClave,
	}
	for ruta, modo := range esperado {
		info, err := os.Stat(ruta)
		if err != nil {
			t.Fatalf("stat %s: %v", ruta, err)
		}
		if got := info.Mode().Perm(); got != modo {
			t.Errorf("%s: modo %#o, esperaba %#o", ruta, got, modo)
		}
		if !filepath.IsAbs(ruta) {
			t.Errorf("%s: la ruta debe ser absoluta (el servidor se lanza con otro directorio de trabajo)", ruta)
		}
	}
	contenido, err := os.ReadFile(p.CACertFile)
	if err != nil {
		t.Fatalf("leer %s: %v", p.CACertFile, err)
	}
	if string(contenido) != string(p.CACertPEM) {
		t.Error("CACertPEM no coincide con el contenido de CACertFile")
	}
}

// pkiCargarCA carga la CA desde sus ficheros con el MISMO criterio que enroll.LoadCAFromPEM: el
// bloque PEM del cert debe ser CERTIFICATE y la clave se prueba como PKCS#8 y, si no, como SEC1.
// Devuelve el cert y la clave firmante; falla el test si no carga.
func pkiCargarCA(t *testing.T, p pki) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	certPEM, err := os.ReadFile(p.CACertFile)
	if err != nil {
		t.Fatalf("leer %s: %v", p.CACertFile, err)
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != pkiTipoCert {
		t.Fatalf("%s no es un bloque CERTIFICATE", p.CACertFile)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		t.Fatalf("parsear el cert de la CA: %v", err)
	}
	keyPEM, err := os.ReadFile(p.CAKeyFile)
	if err != nil {
		t.Fatalf("leer %s: %v", p.CAKeyFile, err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		t.Fatalf("%s no contiene PEM", p.CAKeyFile)
	}
	if k, err := x509.ParsePKCS8PrivateKey(kb.Bytes); err == nil {
		firmante, ok := k.(crypto.Signer)
		if !ok {
			t.Fatalf("la clave PKCS#8 de la CA no es un crypto.Signer (%T)", k)
		}
		return cert, firmante
	}
	k, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		t.Fatalf("la clave de la CA no es PKCS#8 ni SEC1: %v", err)
	}
	return cert, k
}

// pkiVerificarCA comprueba el cert de la CA (CA:TRUE, CertSign|CRLSign, CN, ventana de validez,
// autofirma), que su clave sea EC P-256 en SEC1 y que ambas se correspondan.
func pkiVerificarCA(t *testing.T, p pki) {
	t.Helper()
	cert, firmante := pkiCargarCA(t, p)
	if !cert.IsCA || !cert.BasicConstraintsValid {
		t.Errorf("la CA debe llevar BasicConstraints CA:TRUE (IsCA=%v, valid=%v)", cert.IsCA, cert.BasicConstraintsValid)
	}
	if want := x509.KeyUsageCertSign | x509.KeyUsageCRLSign; cert.KeyUsage&want != want {
		t.Errorf("KeyUsage de la CA = %b, debe incluir CertSign|CRLSign (%b)", cert.KeyUsage, want)
	}
	if cert.Subject.CommonName != pkiCNCA {
		t.Errorf("CN de la CA = %q, esperaba %q", cert.Subject.CommonName, pkiCNCA)
	}
	pkiVerificarVentana(t, cert)
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("la CA no está autofirmada: %v", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("la clave de la CA debe ser EC P-256, es %T", cert.PublicKey)
	}
	if !pub.Equal(firmante.Public()) {
		t.Error("la clave privada de la CA no corresponde a su certificado")
	}
	keyPEM, err := os.ReadFile(p.CAKeyFile)
	if err != nil {
		t.Fatalf("leer %s: %v", p.CAKeyFile, err)
	}
	if blk, _ := pem.Decode(keyPEM); blk == nil || blk.Type != pkiTipoSEC1 {
		t.Errorf("la clave de la CA debe ser un bloque %q (SEC1, como gen-dev-certs.sh)", pkiTipoSEC1)
	}
}

// pkiVerificarVentana comprueba que el cert valga desde ~ahora-1h hasta ~ahora+24h (los
// certificados codifican segundos: se admite un minuto de tolerancia) y que su serie sea
// positiva.
func pkiVerificarVentana(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	ahora := time.Now()
	if d := ahora.Sub(cert.NotBefore); d < pkiHolgura-time.Minute || d > pkiHolgura+time.Minute {
		t.Errorf("%s: NotBefore = %s, esperaba ~ahora-1h", cert.Subject.CommonName, cert.NotBefore)
	}
	if d := cert.NotAfter.Sub(ahora); d < pkiValidez-time.Minute || d > pkiValidez+time.Minute {
		t.Errorf("%s: NotAfter = %s, esperaba ~ahora+24h", cert.Subject.CommonName, cert.NotAfter)
	}
	if cert.SerialNumber.Sign() <= 0 {
		t.Errorf("%s: número de serie no positivo: %v", cert.Subject.CommonName, cert.SerialNumber)
	}
}

// pkiParServidor carga el par del servidor como lo hace el arranque (tls.LoadX509KeyPair, que
// comprueba además que clave y cert se correspondan) y devuelve también el cert parseado. Falla
// el test si no carga.
func pkiParServidor(t *testing.T, p pki) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	par, err := tls.LoadX509KeyPair(p.ServerCertFile, p.ServerKeyFile)
	if err != nil {
		t.Fatalf("tls.LoadX509KeyPair: %v", err)
	}
	if len(par.Certificate) != 1 {
		t.Fatalf("el fichero del servidor debe traer un solo cert, trae %d", len(par.Certificate))
	}
	hoja, err := x509.ParseCertificate(par.Certificate[0])
	if err != nil {
		t.Fatalf("parsear el cert de servidor: %v", err)
	}
	return par, hoja
}

// pkiVerificarServidor comprueba el cert de servidor: hoja (no CA), SAN DNS localhost e IP
// 127.0.0.1, EKU serverAuth, KeyUsage digitalSignature, ventana de validez y cadena contra
// Pool() con DNSName localhost y EKU serverAuth. También que NO valga para otro nombre ni como
// cert de cliente, y que una CA ajena no lo verifique.
func pkiVerificarServidor(t *testing.T, p pki) {
	t.Helper()
	_, hoja := pkiParServidor(t, p)
	if hoja.IsCA {
		t.Error("el cert de servidor no debe ser CA")
	}
	if hoja.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Errorf("KeyUsage del servidor = %b, debe incluir digitalSignature", hoja.KeyUsage)
	}
	if len(hoja.ExtKeyUsage) != 1 || hoja.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("EKU del servidor = %v, esperaba solo serverAuth", hoja.ExtKeyUsage)
	}
	if len(hoja.DNSNames) != 1 || hoja.DNSNames[0] != pkiDNSServer {
		t.Errorf("SAN DNS = %v, esperaba [%s]", hoja.DNSNames, pkiDNSServer)
	}
	if len(hoja.IPAddresses) != 1 || !hoja.IPAddresses[0].Equal(net.ParseIP(pkiIPServer)) {
		t.Errorf("SAN IP = %v, esperaba [%s]", hoja.IPAddresses, pkiIPServer)
	}
	pkiVerificarVentana(t, hoja)

	opciones := x509.VerifyOptions{
		Roots:     p.Pool(),
		DNSName:   pkiDNSServer,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if _, err := hoja.Verify(opciones); err != nil {
		t.Errorf("la cadena del servidor no verifica contra Pool() con DNSName localhost: %v", err)
	}
	if err := hoja.VerifyHostname(pkiIPServer); err != nil {
		t.Errorf("el cert no vale para la IP %s: %v", pkiIPServer, err)
	}

	otroNombre := opciones
	otroNombre.DNSName = "otro.invalido"
	if _, err := hoja.Verify(otroNombre); err == nil {
		t.Error("el cert verificó para un nombre que no está en su SAN")
	}
	comoCliente := opciones
	comoCliente.KeyUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if _, err := hoja.Verify(comoCliente); err == nil {
		t.Error("el cert de servidor verificó para clientAuth: su EKU es solo serverAuth")
	}
	ajena := opciones
	ajena.Roots = nuevaPKI(t).Pool()
	if _, err := hoja.Verify(ajena); err == nil {
		t.Error("el cert verificó contra la CA de OTRA pki")
	}
}

// pkiVerificarEntorno comprueba que Entorno() trae exactamente las cuatro variables, sin
// duplicados, con las rutas de los ficheros.
func pkiVerificarEntorno(t *testing.T, p pki) {
	t.Helper()
	env := entornoAMapa(t, p.Entorno())
	esperado := map[string]string{
		"WAPP_PKI_CA_CERT_FILE":     p.CACertFile,
		"WAPP_PKI_CA_KEY_FILE":      p.CAKeyFile,
		"WAPP_PKI_SERVER_CERT_FILE": p.ServerCertFile,
		"WAPP_PKI_SERVER_KEY_FILE":  p.ServerKeyFile,
	}
	if len(env) != len(esperado) {
		t.Errorf("Entorno() trae %d variables, esperaba %d: %v", len(env), len(esperado), env)
	}
	for k, v := range esperado {
		if env[k] != v {
			t.Errorf("%s = %q, esperaba %q", k, env[k], v)
		}
	}
}

// pkiVerificarAleatoria comprueba que cada llamada a nuevaPKI produce otra CA y otro servidor
// (claves y series distintas) y directorios distintos: nada es reutilizable entre corridas.
func pkiVerificarAleatoria(t *testing.T, p pki) {
	t.Helper()
	otra := nuevaPKI(t)
	if filepath.Dir(p.CACertFile) == filepath.Dir(otra.CACertFile) {
		t.Error("dos pki comparten directorio")
	}
	if string(p.CACertPEM) == string(otra.CACertPEM) {
		t.Error("dos pki tienen la misma CA")
	}
	a, b := pkiLeerPEM(t, p.ServerKeyFile), pkiLeerPEM(t, otra.ServerKeyFile)
	if string(a) == string(b) {
		t.Error("dos pki tienen la misma clave de servidor")
	}
	_, hojaA := pkiParServidor(t, p)
	_, hojaB := pkiParServidor(t, otra)
	if hojaA.SerialNumber.Cmp(hojaB.SerialNumber) == 0 {
		t.Error("dos pki tienen el mismo número de serie de servidor")
	}
}

// pkiLeerPEM lee un fichero y devuelve su contenido; falla el test si no puede.
func pkiLeerPEM(t *testing.T, ruta string) []byte {
	t.Helper()
	datos, err := os.ReadFile(ruta) //nolint:gosec // G304: la ruta la fabrica el propio arnés dentro de un t.TempDir()
	if err != nil {
		t.Fatalf("leer %s: %v", ruta, err)
	}
	return datos
}
