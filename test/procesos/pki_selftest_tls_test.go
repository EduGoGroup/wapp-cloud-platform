//go:build integracion

package procesos

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// Los casos «tls13» y «mtls» de TestArnes_PKI, que usan la PKI de verdad por loopback: un eco TLS
// con el certificado de servidor y el mTLS con una hoja de cliente emitida por la misma CA.
// Sale de pki_test.go (D-F9-11: solo se movieron declaraciones).

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

// pkiCerrar cierra c ignorando el error a propósito: es el cierre de una conexión de prueba cuyo
// resultado ya se comprobó (si el par cerró antes, Close devuelve un error sin importancia que,
// al correr en una goroutine, tampoco se puede volcar a t con seguridad). Va por io.Closer
// porque errcheck del repo exceptúa (io.Closer).Close; gosec pide la supresión explícita.
func pkiCerrar(c io.Closer) {
	c.Close() //nolint:gosec // G104: cierre best-effort de una conexión de prueba ya comprobada
}
