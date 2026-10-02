//go:build integracion

package procesos

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"google.golang.org/grpc"
)

// La conexión del Edge de prueba: abrir el stream Connect con mTLS, el bucle de recepción, el lease
// inicial, el cierre del enlace y las variantes del Edge con otro certificado u otra clave de lease.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Conexión
// ---------------------------------------------------------------------------------------------

// conectar abre el stream CloudLink/Connect con mTLS usando el certificado que emitió el
// enrolamiento, lanza el bucle de recepción, manda el primer latido —con el contador del lease
// inicial y la inferencia declarada READY— y devuelve cuando el servidor ya mandó el LeaseUpdate
// inicial (tope 10 s) Y el Validator lo aceptó. Cada conexión es un arranque del Edge: el Validator
// de leases nace nuevo (si no, el lease inicial de una reconexión, de contador 1, se rechazaría como
// replay); lo demás que el Edge registró (textos, configs, peticiones) se conserva. Registra en
// t.Cleanup el cierre del stream y de la conexión. Falla (t.Fatalf) si no consigue conectar o si el
// Validator rechaza el lease inicial (ver conectarErr).
//
// «Aceptado» no es «vigente»: a un Edge ya revocado el servidor le manda una revocación como
// lease inicial, el Validator la acepta y conectar vuelve sin fallar, con puedeOperar() falso y
// revocado() verdadero. Quien necesite un Edge operativo lo afirma con puedeOperar().
func (e *edge) conectar(t *testing.T) {
	t.Helper()
	if err := e.conectarErr(t); err != nil {
		t.Fatalf("conectar (sesión %s): %v", e.SessionID, err)
	}
}

// conectarErr es conectar para los casos negativos: devuelve el error en vez de fallar el test. Si
// el servidor rechaza el handshake (p. ej. un certificado de otra CA) o cierra el stream, el error
// trae el motivo de la recepción; si nunca llega el LeaseUpdate inicial, envuelve
// errEdgeLeaseNoLlego; si llega y el Validator lo RECHAZA (p. ej. el servidor firma con otra clave
// que la que el Edge tiene en LeasePub), envuelve errEdgeInitialLeaseRejected y la causa del
// Validator (errors.Is con cllease.ErrBadSignature, cllease.ErrStaleCounter…). Una revocación NO es
// un rechazo: el Validator la acepta y conectarErr devuelve nil. Tras un error el enlace queda
// cerrado. Solo falla el test por una avería del propio arnés.
func (e *edge) conectarErr(t *testing.T) error {
	t.Helper()
	if err := e.cerrarEnlace(); err != nil { // conectar de nuevo sin desconectar antes: la anterior se cierra
		t.Logf("cerrar la conexión anterior de %s: %v", e.SessionID, err)
	}
	e.reiniciarLeases()
	en, err := e.abrirEnlace(t.Context())
	if err != nil {
		return err
	}
	e.adoptar(en)
	t.Cleanup(func() { e.cerrarYAnotar(t) })
	go e.recibir(en)
	// El motivo real de un enlace roto no está en el error de este Send (un io.EOF a secas) sino en
	// el que devuelve Recv, y lo recoge esperarLeaseInicial.
	if errEnvio := e.emitir(edgeLatido(e.SessionID, edgeContadorInicial)); errEnvio != nil {
		t.Logf("primer latido de %s: %v", e.SessionID, errEnvio)
	}
	if err := e.esperarLeaseInicial(t.Context(), en); err != nil {
		e.cerrarYAnotar(t)
		return err
	}
	return nil
}

// abrirEnlace abre la conexión mTLS y el stream Connect: el certificado y la clave del Edge, la
// cadena como raíz y «localhost» como nombre del servidor. Devuelve el enlace aún sin adoptar, o el
// error de montar las credenciales, de crear el cliente o de abrir el stream.
func (e *edge) abrirEnlace(ctx context.Context) (*edgeEnlace, error) {
	clavePEM, err := edgeClavePEM(e.clave)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(e.certPEM, clavePEM)
	if err != nil {
		return nil, fmt.Errorf("el certificado emitido no casa con la clave del Edge: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(e.caPEM) {
		return nil, errors.New("ca_chain_pem no contiene certificados válidos")
	}
	conn, err := grpc.NewClient("passthrough:///"+e.conectarAddr,
		grpc.WithTransportCredentials(mtls.ClientCreds(cert, pool, edgeNombreServidor)))
	if err != nil {
		return nil, fmt.Errorf("grpc.NewClient hacia CloudLink %s: %w", e.conectarAddr, err)
	}
	ctxStream, cancelar := context.WithCancel(ctx)
	// Connect espera a que el canal esté listo: si el servidor acepta la conexión y no contesta, no
	// vuelve nunca. El temporizador corta ese caso cancelando el stream (el cancel es idempotente).
	limite := time.AfterFunc(edgeTopeConectar, cancelar)
	stream, err := cloudlinkv1.NewCloudLinkClient(conn).Connect(ctxStream)
	limite.Stop()
	if err != nil {
		cancelar()
		return nil, errors.Join(fmt.Errorf("abrir Connect: %w", err), conn.Close())
	}
	return &edgeEnlace{conn: conn, stream: stream, cancelar: cancelar, fin: make(chan struct{})}, nil
}

// adoptar deja al enlace como el vigente del Edge y le pone al núcleo el Send de su stream como
// salida.
func (e *edge) adoptar(en *edgeEnlace) {
	e.mu.Lock()
	e.enlace = en
	e.cerrando = false
	e.mu.Unlock()
	e.salidaMu.Lock()
	e.salida = en.stream.Send
	e.salidaMu.Unlock()
}

// recibir es el bucle de recepción de un enlace: lee comandos del servidor hasta que Recv falla
// (el servidor cerró, el contexto se canceló, el transporte cayó), deja el error en el enlace y
// cierra su canal fin.
func (e *edge) recibir(en *edgeEnlace) {
	defer close(en.fin)
	for {
		cmd, err := en.stream.Recv()
		if err != nil {
			en.errFin = err
			return
		}
		e.despachar(cmd)
	}
}

// esperarLeaseInicial sondea hasta que el Edge haya recibido su primer LeaseUpdate, con tope de 10
// s. Devuelve nil cuando llegó y el Validator lo aceptó (vigente o revocación); un error que
// envuelve errEdgeInitialLeaseRejected y la causa si llegó y el Validator lo rechazó; el error con
// el que terminó el stream si el bucle de recepción acabó antes; o un error que envuelve
// errEdgeLeaseNoLlego si venció el tope.
func (e *edge) esperarLeaseInicial(ctx context.Context, en *edgeEnlace) error {
	ctx, cancelar := context.WithTimeout(ctx, edgeTopeConectar)
	defer cancelar()
	tic := time.NewTicker(edgeSondeo)
	defer tic.Stop()
	for e.Leases() == 0 {
		select {
		case <-en.fin:
			return fmt.Errorf("el stream terminó antes del LeaseUpdate inicial: %w", en.errFin)
		case <-ctx.Done():
			return fmt.Errorf("%w en %s: %w", errEdgeLeaseNoLlego, edgeTopeConectar, ctx.Err())
		case <-tic.C:
		}
	}
	return e.initialLeaseRejection()
}

// initialLeaseRejection devuelve nil si el Validator aceptó el primer LeaseUpdate de la conexión
// actual (o si aún no llegó ninguno), y si lo rechazó, un error que envuelve
// errEdgeInitialLeaseRejected y la causa del Validator. Solo mira el PRIMERO: un rechazo posterior
// (un lease de contador viejo tras uno bueno) queda en Errores y no cambia esto.
func (e *edge) initialLeaseRejection() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.initialLeaseErr == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errEdgeInitialLeaseRejected, e.initialLeaseErr)
}

// desconectar cierra el stream y la conexión, como un Edge que se va: cierra el lado de envío (el
// servidor ve el fin del stream y marca la sesión offline), espera a que termine la recepción,
// cancela y cierra la conexión. Es idempotente y no falla el test: un fallo al cerrar se anota con
// t.Logf. Después se puede volver a conectar.
func (e *edge) desconectar(t *testing.T) {
	t.Helper()
	e.cerrarYAnotar(t)
}

// cerrarYAnotar es el cuerpo de desconectar y del Cleanup de conectar: cierra el enlace y anota con
// t.Logf lo que no se pudo cerrar.
func (e *edge) cerrarYAnotar(t *testing.T) {
	t.Helper()
	if err := e.cerrarEnlace(); err != nil {
		t.Logf("cerrar el enlace de %s: %v", e.SessionID, err)
	}
}

// cerrarEnlace cierra el enlace vigente, si lo hay, y devuelve los errores de cierre unidos. Orden:
// marca el cierre propio (los errores de envío posteriores dejan de anotarse), cierra el lado de
// envío, espera a que el bucle de recepción vea el fin, cancela el stream, cierra la conexión y
// espera a las inferencias en vuelo. Cada espera tiene tope.
func (e *edge) cerrarEnlace() error {
	e.mu.Lock()
	en := e.enlace
	e.enlace = nil
	e.cerrando = true
	e.mu.Unlock()
	if en == nil {
		return nil
	}
	e.salidaMu.Lock()
	errEnvio := en.stream.CloseSend()
	e.salida = nil
	e.salidaMu.Unlock()
	edgeEsperarCanal(en.fin, edgeTopeCierre) // el servidor devuelve nil al ver el EOF y el Recv termina
	en.cancelar()
	errConn := en.conn.Close()
	edgeEsperarCanal(en.fin, edgeTopeCierre)
	if !edgeEsperarGrupo(&e.enVuelo, edgeTopeCierre) {
		return errors.Join(errEnvio, errConn, errors.New("una inferencia del guion no terminó al cerrar el enlace"))
	}
	return errors.Join(errEnvio, errConn)
}

// edgeEsperarCanal espera a que se cierre c, con tope. Devuelve true si se cerró.
func edgeEsperarCanal(c <-chan struct{}, tope time.Duration) bool {
	timer := time.NewTimer(tope)
	defer timer.Stop()
	select {
	case <-c:
		return true
	case <-timer.C:
		return false
	}
}

// edgeEsperarGrupo espera a que el grupo termine, con tope. Devuelve true si terminó. Si vence el
// tope deja viva una goroutine que espera al grupo: solo ocurre con un guion que no vuelve.
func edgeEsperarGrupo(wg *sync.WaitGroup, tope time.Duration) bool {
	hecho := make(chan struct{})
	go func() {
		wg.Wait()
		close(hecho)
	}()
	return edgeEsperarCanal(hecho, tope)
}

// estaCerrando dice si el cierre del enlace ya empezó (por el test o por el Cleanup).
func (e *edge) estaCerrando() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cerrando
}

// reiniciarLeases deja al Edge como recién arrancado en lo que toca al lease: Validator nuevo,
// contador de leases a cero y sin rechazo del lease inicial. Lo hace conectarErr antes de cada
// conexión.
func (e *edge) reiniciarLeases() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.validador = cllease.NewValidator(e.LeasePub)
	e.leases = 0
	e.initialLeaseErr = nil
}

// conCertificadoDe devuelve OTRO Edge con la misma identidad (id, tenant, clave) pero con un
// certificado de cliente firmado por la CA de la PKI dada, que no es la del servidor, y con un
// session_id propio. Sirve para el caso negativo del mTLS: conectarErr de ese Edge debe fallar en
// el handshake. El certificado es correcto en todo lo demás (CommonName, Organization = tenant,
// uso de clave de cliente, vigencia), de modo que el único defecto es la CA. Falla (t.Fatalf) si
// no puede cargar la CA o firmar el certificado.
func (e *edge) conCertificadoDe(t *testing.T, otra pki) *edge {
	t.Helper()
	caCert, caClave := edgeCargarCA(t, otra)
	serie, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatalf("generar el número de serie: %v", err)
	}
	ahora := time.Now()
	plantilla := &x509.Certificate{
		SerialNumber: serie.Add(serie, big.NewInt(1)),
		Subject:      pkix.Name{CommonName: e.EdgeID, Organization: []string{e.TenantID}},
		NotBefore:    ahora.Add(-time.Hour),
		NotAfter:     ahora.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, plantilla, caCert, &e.clave.PublicKey, caClave)
	if err != nil {
		t.Fatalf("firmar el certificado del Edge con la CA ajena: %v", err)
	}
	copia := nuevoEdge(e.TenantID, e.EdgeID, "sesion-ajena-"+edgeAleatorioHex(t, 6), e.CloudEncPub, e.LeasePub)
	copia.conectarAddr = e.conectarAddr
	copia.clave = e.clave
	copia.certPEM = edgePEM("CERTIFICATE", der)
	copia.caPEM = e.caPEM // la raíz que verifica al SERVIDOR sigue siendo la buena
	return copia
}

// withLeasePub devuelve OTRO Edge con la misma identidad, la misma clave y el MISMO certificado (el
// bueno: pasa el mTLS), pero que verifica los leases con la pública dada en vez de con la del
// servidor, y con un session_id propio. Sirve para el caso «el servidor firma los leases con otra
// clave que la que el Edge tiene»: conectarErr de ese Edge debe devolver el rechazo del lease
// inicial (errEdgeInitialLeaseRejected, cllease.ErrBadSignature). No falla.
func (e *edge) withLeasePub(t *testing.T, pub ed25519.PublicKey) *edge {
	t.Helper()
	other := nuevoEdge(e.TenantID, e.EdgeID, "sesion-otra-clave-"+edgeAleatorioHex(t, 6), e.CloudEncPub, pub)
	other.conectarAddr = e.conectarAddr
	other.clave = e.clave
	other.certPEM = e.certPEM
	other.caPEM = e.caPEM
	return other
}

// edgeCargarCA lee el certificado y la clave SEC1 de la CA de una PKI del arnés. Falla el test
// (t.Fatalf) si no se pueden leer o interpretar.
func edgeCargarCA(t *testing.T, p pki) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	bloqueCert := edgePEMPrimero(t, "CERTIFICATE", p.CACertPEM)
	cert, err := x509.ParseCertificate(bloqueCert)
	if err != nil {
		t.Fatalf("interpretar el certificado de la CA: %v", err)
	}
	crudo, err := os.ReadFile(p.CAKeyFile) //nolint:gosec // fichero de la PKI que generó este mismo test
	if err != nil {
		t.Fatalf("leer la clave de la CA: %v", err)
	}
	clave, err := x509.ParseECPrivateKey(edgePEMPrimero(t, pkiTipoSEC1, crudo))
	if err != nil {
		t.Fatalf("interpretar la clave de la CA (SEC1): %v", err)
	}
	return cert, clave
}
