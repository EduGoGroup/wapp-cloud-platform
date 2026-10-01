//go:build integracion

package procesos

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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

// pkiEco hace un apretón TLS real por loopback entre un servidor con srv y un cliente con cli, y
// un intercambio de un byte en cada sentido (en TLS 1.3 el rechazo del cert de cliente solo se
// ve al leer). Devuelve la versión TLS negociada que ve el cliente y el CommonName del primer
// cert que presentó el cliente al servidor ("" si no presentó ninguno). Devuelve error si
// cualquiera de los dos lados falla o si nada ocurre en pkiPlazoRed; no falla el test por sí
// mismo, para poder probar los rechazos.
func pkiEco(t *testing.T, srv, cli *tls.Config) (version uint16, cnCliente string, err error) {
	t.Helper()
	escucha, err := tls.Listen("tcp", pkiIPServer+":0", srv)
	if err != nil {
		t.Fatalf("escuchar en loopback: %v", err)
	}
	defer func() {
		if cerr := escucha.Close(); cerr != nil {
			t.Logf("cerrar el listener de prueba: %v", cerr)
		}
	}()

	type vistoPorServidor struct {
		cn  string
		err error
	}
	servidor := make(chan vistoPorServidor, 1)
	go func() {
		cn, serr := pkiServirEco(escucha)
		servidor <- vistoPorServidor{cn, serr}
	}()

	version, cerr := pkiLlamarEco(escucha.Addr().String(), cli)
	select {
	case visto := <-servidor:
		if cerr == nil && visto.err != nil {
			cerr = fmt.Errorf("lado servidor: %w", visto.err)
		}
		return version, visto.cn, cerr
	case <-time.After(pkiPlazoRed):
		return 0, "", errors.Join(fmt.Errorf("el lado servidor no terminó en %s", pkiPlazoRed), cerr)
	}
}

// pkiServirEco acepta UNA conexión TLS, lee un byte (lo que fuerza el apretón completo, incluida
// la verificación del cert de cliente) y lo devuelve. Devuelve el CN del primer cert de cliente
// ("" si no hubo) y el error del primer paso que falle.
func pkiServirEco(escucha net.Listener) (string, error) {
	conn, err := escucha.Accept()
	if err != nil {
		return "", fmt.Errorf("aceptar: %w", err)
	}
	defer pkiCerrar(conn)
	if err := conn.SetDeadline(time.Now().Add(pkiPlazoRed)); err != nil {
		return "", fmt.Errorf("plazo: %w", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", fmt.Errorf("leer: %w", err)
	}
	cn := ""
	if tc, ok := conn.(*tls.Conn); ok {
		if pares := tc.ConnectionState().PeerCertificates; len(pares) > 0 {
			cn = pares[0].Subject.CommonName
		}
	}
	if _, err := conn.Write(buf); err != nil {
		return cn, fmt.Errorf("escribir: %w", err)
	}
	return cn, nil
}

// pkiLlamarEco abre una conexión TLS a addr con cli, escribe un byte y espera el eco. Devuelve
// la versión TLS negociada o el error del primer paso que falle.
func pkiLlamarEco(addr string, cli *tls.Config) (uint16, error) {
	dialer := &net.Dialer{Timeout: pkiPlazoRed}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, cli)
	if err != nil {
		return 0, fmt.Errorf("conectar: %w", err)
	}
	defer pkiCerrar(conn)
	if err := conn.SetDeadline(time.Now().Add(pkiPlazoRed)); err != nil {
		return 0, fmt.Errorf("plazo: %w", err)
	}
	if _, err := conn.Write([]byte{'x'}); err != nil {
		return 0, fmt.Errorf("escribir: %w", err)
	}
	eco := make([]byte, 1)
	if _, err := io.ReadFull(conn, eco); err != nil {
		return 0, fmt.Errorf("leer: %w", err)
	}
	return conn.ConnectionState().Version, nil
}

// pkiConfigServidor es la configuración TLS del servidor de prueba: la del :8102 (solo TLS de
// servidor) si exigirCliente es false, la del :8101 (mTLS estricto con esta CA) si es true. En
// los dos casos TLS 1.3 como mínimo, igual que el servidor real.
func pkiConfigServidor(t *testing.T, p pki, exigirCliente bool) *tls.Config {
	t.Helper()
	par, _ := pkiParServidor(t, p)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{par}}
	if exigirCliente {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = p.Pool()
	}
	return cfg
}

// pkiConfigCliente es la configuración TLS de un Edge de prueba: confía en raices, verifica el
// nombre nombreServidor y presenta los certs de cliente que se le pasen (ninguno si no hay).
func pkiConfigCliente(raices *x509.CertPool, nombreServidor string, hojas ...tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      raices,
		ServerName:   nombreServidor,
		Certificates: hojas,
	}
}

// pkiVerificarTLS comprueba el :8102: un cliente que confía en Pool() completa un apretón TLS 1.3
// verificando tanto por nombre (localhost) como por IP (127.0.0.1), y falla si confía en la CA de
// otra pki o verifica otro nombre.
func pkiVerificarTLS(t *testing.T, p pki) {
	t.Helper()
	srv := pkiConfigServidor(t, p, false)
	for _, nombre := range []string{pkiDNSServer, pkiIPServer} {
		version, _, err := pkiEco(t, srv, pkiConfigCliente(p.Pool(), nombre))
		if err != nil {
			t.Errorf("apretón TLS con ServerName %q: %v", nombre, err)
			continue
		}
		if version != tls.VersionTLS13 {
			t.Errorf("ServerName %q: versión TLS %#x, esperaba 1.3 (%#x)", nombre, version, tls.VersionTLS13)
		}
	}
	if _, _, err := pkiEco(t, srv, pkiConfigCliente(nuevaPKI(t).Pool(), pkiDNSServer)); err == nil {
		t.Error("el cliente completó el apretón confiando en la CA de OTRA pki")
	}
	if _, _, err := pkiEco(t, srv, pkiConfigCliente(p.Pool(), "otro.invalido")); err == nil {
		t.Error("el cliente completó el apretón verificando un nombre que no está en el SAN")
	}
}

// pkiHojaCliente emite un cert de Edge firmado por la CA de p cargada desde sus ficheros (como
// hace el enrolamiento: KeyUsage digitalSignature y EKU clientAuth). Devuelve el par listo para
// tls.Config.Certificates; falla el test si no puede.
func pkiHojaCliente(t *testing.T, p pki, cn string) tls.Certificate {
	t.Helper()
	ca, firmante := pkiCargarCA(t, p)
	clave := pkiClave(t)
	plantilla := pkiPlantilla(t, cn)
	plantilla.KeyUsage = x509.KeyUsageDigitalSignature
	plantilla.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	plantilla.BasicConstraintsValid = true
	hoja := pkiEmitir(t, plantilla, ca, &clave.PublicKey, firmante)
	return tls.Certificate{Certificate: [][]byte{hoja.Raw}, PrivateKey: clave, Leaf: hoja}
}

// pkiVerificarMTLS comprueba el :8101: con mTLS estricto entra un Edge cuyo cert firmó la CA de
// la pki (el servidor ve su CN) y quedan fuera el que no presenta cert y el firmado por una CA
// ajena. Demuestra de paso que la clave de la CA en disco sirve para firmar certs de cliente.
func pkiVerificarMTLS(t *testing.T, p pki) {
	t.Helper()
	srv := pkiConfigServidor(t, p, true)

	const cnEdge = "edge-de-prueba"
	_, cn, err := pkiEco(t, srv, pkiConfigCliente(p.Pool(), pkiDNSServer, pkiHojaCliente(t, p, cnEdge)))
	if err != nil {
		t.Fatalf("el Edge con cert de la CA no entró por mTLS: %v", err)
	}
	if cn != cnEdge {
		t.Errorf("el servidor vio el CN %q, esperaba %q", cn, cnEdge)
	}

	if _, _, err := pkiEco(t, srv, pkiConfigCliente(p.Pool(), pkiDNSServer)); err == nil {
		t.Error("un cliente sin cert entró por mTLS")
	}
	ajena := nuevaPKI(t)
	intruso := pkiConfigCliente(p.Pool(), pkiDNSServer, pkiHojaCliente(t, ajena, cnEdge))
	if _, _, err := pkiEco(t, srv, intruso); err == nil {
		t.Error("un cliente con cert de OTRA CA entró por mTLS")
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

// pkiCerrar cierra c ignorando el error a propósito: es el cierre de una conexión de prueba cuyo
// resultado ya se comprobó (si el par cerró antes, Close devuelve un error sin importancia que,
// al correr en una goroutine, tampoco se puede volcar a t con seguridad). Va por io.Closer
// porque errcheck del repo exceptúa (io.Closer).Close; gosec pide la supresión explícita.
func pkiCerrar(c io.Closer) {
	c.Close() //nolint:gosec // G104: cierre best-effort de una conexión de prueba ya comprobada
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
