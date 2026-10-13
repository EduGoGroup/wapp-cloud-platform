//go:build integracion

package procesos

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Parámetros de la PKI efímera. Son los de scripts/gen-dev-certs.sh llevados a Go, con una
// validez corta: la corrida de procesos dura minutos, no los 825 días del script de desarrollo.
const (
	pkiCNCA       = "procesos-ca"    // CommonName de la CA
	pkiHolgura    = time.Hour        // NotBefore = ahora - holgura (tolera relojes desalineados)
	pkiValidez    = 24 * time.Hour   // NotAfter = ahora + validez
	pkiDNSServer  = "localhost"      // el Edge verifica con ServerName "localhost"
	pkiIPServer   = "127.0.0.1"      // y los listeners del servidor escuchan en loopback
	pkiTipoCert   = "CERTIFICATE"    // tipo del bloque PEM de un certificado
	pkiTipoSEC1   = "EC PRIVATE KEY" // tipo del bloque PEM de una clave EC en formato SEC1
	pkiPlazoRed   = 10 * time.Second // tope de un apretón TLS de prueba
	pkiModoClave  = 0o600
	pkiModoPublic = 0o644
)

// pki es el material PKI de una corrida: la CA que firma los certs de los Edges (el servidor
// la usa en el enrolamiento y como ClientCAs del mTLS de :8101) y el cert de servidor que
// presentan los DOS gRPC (:8102 TLS de servidor, :8101 mTLS). Nace de nuevaPKI y NO existe en
// el repositorio: es aleatoria por llamada.
type pki struct {
	CACertFile     string // PEM del cert de la CA (WAPP_PKI_CA_CERT_FILE)
	CAKeyFile      string // PEM SEC1 de la clave de la CA (WAPP_PKI_CA_KEY_FILE)
	ServerCertFile string // PEM del cert de servidor (WAPP_PKI_SERVER_CERT_FILE)
	ServerKeyFile  string // PEM SEC1 de la clave del servidor (WAPP_PKI_SERVER_KEY_FILE)
	CACertPEM      []byte // mismo contenido que CACertFile, para sembrar el pool de un cliente
}

// nuevaPKI genera una CA EC P-256 y un cert de servidor firmado por ella, y los escribe en cuatro
// ficheros PEM dentro de un t.TempDir() (claves 0600, certs 0644). Replica los comandos openssl
// de gen-dev-certs.sh: claves en formato SEC1 ("EC PRIVATE KEY"); la CA lleva
// BasicConstraints CA:TRUE y KeyUsage CertSign|CRLSign (CN "procesos-ca"); el cert de servidor
// lleva SAN DNS:localhost e IP:127.0.0.1, EKU serverAuth y KeyUsage digitalSignature. Ambos
// valen desde ahora-1h hasta ahora+24h, con números de serie aleatorios. Falla el test (t.Fatalf)
// si no puede generar o escribir algo; el directorio se borra solo al terminar el test.
func nuevaPKI(t *testing.T) pki {
	t.Helper()
	dir := t.TempDir()

	caClave := pkiClave(t)
	plantillaCA := pkiPlantilla(t, pkiCNCA)
	plantillaCA.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	plantillaCA.BasicConstraintsValid = true
	plantillaCA.IsCA = true
	ca := pkiEmitir(t, plantillaCA, nil, &caClave.PublicKey, caClave)

	srvClave := pkiClave(t)
	plantillaSrv := pkiPlantilla(t, pkiDNSServer)
	plantillaSrv.KeyUsage = x509.KeyUsageDigitalSignature
	plantillaSrv.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	plantillaSrv.BasicConstraintsValid = true
	plantillaSrv.DNSNames = []string{pkiDNSServer}
	plantillaSrv.IPAddresses = []net.IP{net.ParseIP(pkiIPServer)}
	srv := pkiEmitir(t, plantillaSrv, ca, &srvClave.PublicKey, caClave)

	p := pki{
		CACertFile:     filepath.Join(dir, "ca.crt"),
		CAKeyFile:      filepath.Join(dir, "ca.key"),
		ServerCertFile: filepath.Join(dir, "server.crt"),
		ServerKeyFile:  filepath.Join(dir, "server.key"),
		CACertPEM:      pem.EncodeToMemory(&pem.Block{Type: pkiTipoCert, Bytes: ca.Raw}),
	}
	pkiEscribir(t, p.CACertFile, p.CACertPEM, pkiModoPublic)
	pkiEscribir(t, p.CAKeyFile, pkiPEMSEC1(t, caClave), pkiModoClave)
	pkiEscribir(t, p.ServerCertFile, pem.EncodeToMemory(&pem.Block{Type: pkiTipoCert, Bytes: srv.Raw}), pkiModoPublic)
	pkiEscribir(t, p.ServerKeyFile, pkiPEMSEC1(t, srvClave), pkiModoClave)
	return p
}

// Pool devuelve un pool de certificados con la CA de la PKI, listo para ser RootCAs de un cliente
// que verifica el cert de servidor o ClientCAs de un servidor que exige cert de cliente. No
// falla: si CACertPEM no contiene ningún certificado (una pki de valor cero) devuelve un pool
// vacío, en el que ninguna verificación pasa.
func (p pki) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.CACertPEM)
	return pool
}

// Entorno devuelve las cuatro variables "K=V" que apuntan el servidor a esta PKI:
// WAPP_PKI_CA_CERT_FILE, WAPP_PKI_CA_KEY_FILE, WAPP_PKI_SERVER_CERT_FILE y
// WAPP_PKI_SERVER_KEY_FILE. Cada llamada devuelve un slice nuevo.
func (p pki) Entorno() []string {
	return []string{
		"WAPP_PKI_CA_CERT_FILE=" + p.CACertFile,
		"WAPP_PKI_CA_KEY_FILE=" + p.CAKeyFile,
		"WAPP_PKI_SERVER_CERT_FILE=" + p.ServerCertFile,
		"WAPP_PKI_SERVER_KEY_FILE=" + p.ServerKeyFile,
	}
}

// pkiClave genera una clave ECDSA P-256 con el generador criptográfico del sistema. Falla el
// test si el generador no responde.
func pkiClave(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	clave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generar clave ECDSA P-256: %v", err)
	}
	return clave
}

// pkiPlantilla devuelve una plantilla de certificado con lo común a todos: número de serie
// aleatorio de 127 bits (siempre positivo), Subject con el CommonName dado y validez de ahora-1h
// a ahora+24h. El llamante completa el uso (KeyUsage, EKU, SAN, CA). Falla el test si no hay
// aleatoriedad.
func pkiPlantilla(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	serie, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatalf("generar número de serie: %v", err)
	}
	ahora := time.Now()
	return &x509.Certificate{
		SerialNumber: serie.Add(serie, big.NewInt(1)),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    ahora.Add(-pkiHolgura),
		NotAfter:     ahora.Add(pkiValidez),
	}
}

// pkiEmitir firma la plantilla con la clave del firmante y devuelve el certificado ya parseado
// (su campo Raw es el DER). padre es el cert del emisor; con nil el certificado es autofirmado
// (el firmante es la clave de la propia plantilla). Falla el test si x509 rechaza la plantilla.
func pkiEmitir(t *testing.T, plantilla, padre *x509.Certificate, pub crypto.PublicKey, firmante crypto.Signer) *x509.Certificate {
	t.Helper()
	if padre == nil {
		padre = plantilla
	}
	der, err := x509.CreateCertificate(rand.Reader, plantilla, padre, pub, firmante)
	if err != nil {
		t.Fatalf("emitir certificado %q: %v", plantilla.Subject.CommonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsear el certificado %q recién emitido: %v", plantilla.Subject.CommonName, err)
	}
	return cert
}

// pkiPEMSEC1 serializa una clave EC como PEM "EC PRIVATE KEY" (SEC1), el formato que escribe
// "openssl ecparam -genkey". Falla el test si la clave no se puede serializar.
func pkiPEMSEC1(t *testing.T, clave *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(clave)
	if err != nil {
		t.Fatalf("serializar clave EC (SEC1): %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pkiTipoSEC1, Bytes: der})
}

// pkiEscribir escribe datos en ruta con el modo exacto pedido (aunque el umask del proceso sea
// más permisivo) y falla el test si no puede.
func pkiEscribir(t *testing.T, ruta string, datos []byte, modo os.FileMode) {
	t.Helper()
	if err := os.WriteFile(ruta, datos, modo); err != nil {
		t.Fatalf("escribir %s: %v", ruta, err)
	}
	if err := os.Chmod(ruta, modo); err != nil {
		t.Fatalf("fijar el modo %#o de %s: %v", modo, ruta, err)
	}
}

// entornoAMapa convierte un entorno "K=V" en mapa y falla el test si alguna entrada no trae '='
// o si una clave se repite (un entorno de subproceso con duplicados es ambiguo).
func entornoAMapa(t *testing.T, entorno []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(entorno))
	for _, kv := range entorno {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			t.Fatalf("entrada de entorno mal formada: %q", kv)
		}
		if _, dup := m[k]; dup {
			t.Fatalf("variable de entorno repetida: %s", k)
		}
		m[k] = v
	}
	return m
}
