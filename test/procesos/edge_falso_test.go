//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// El «Edge de prueba»: un cliente gRPC que habla el contrato REAL de CloudLink con el servidor
// bajo prueba (enrola por :8102 con TLS de servidor, conecta por :8101 con mTLS, late, recibe
// leases, comandos y peticiones de inferencia). Lo que NO hace es lo que el Edge de verdad hace
// con WhatsApp: no hay whatsmeow, ni DEK, ni Ollama. Dónde manda cada cosa lo dice el fichero
// del diseño (plan/F9-procesos/diseno.md §3.3).
//
// Lo que SÍ hace igual que el Edge de verdad es obedecer al lease (el kill-switch, ADR-0007): sin
// un lease vigente NO «entrega» —ni SendText ni SendMedia— y lo dice con el mismo Ack que el Edge
// real (ok=false, «lease no vigente»); y tampoco sirve INFERENCIA: tras la gracia del Edge real,
// contesta el mismo InferenceResult con INFERENCE_ERROR_LEASE_INVALID, sin consultar el guion. Un
// doble más permisivo que el Edge daría por buena una entrega, o una inferencia, que en producción
// no ocurre.
//
// Está partido en dos a propósito. El NÚCLEO (edge.manejar y sus alRecibir…) decide qué hace el
// Edge con cada comando y emite sus frames por edge.salida, una función inyectada: se prueba sin
// servidor ni red, con un colector en vez del stream. El TRANSPORTE (enrolar, conectar, recibir,
// cerrarEnlace) abre el enlace de verdad y le pone al núcleo el Send del stream.

const (
	// edgeNombreServidor es el nombre con el que el Edge verifica al servidor: el SAN DNS del
	// certificado de servidor que genera nuevaPKI.
	edgeNombreServidor = "localhost"
	// edgeSesionControl es el session_id del canal de control (la constante ControlSessionID del
	// paquete transport de wapp-cloudlink; aquí es un literal porque el arnés importa solo
	// gen, lease y mtls). El servidor no lo registra como sesión: lo usa para empujar la config
	// inicial a un Edge que aún no tiene ningún teléfono (connect.go, onControlChannel).
	edgeSesionControl = "__wapp_control__"
	// edgeContadorInicial es el contador del primer lease que emite el servidor (IssueInitial) y,
	// por lo tanto, el que usa el primer latido: el servidor renueva con contador+1 y el Validator
	// rechaza (ErrStaleCounter) cualquier lease que no supere al último aplicado. Con 0, la
	// renovación saldría con 1, igual al lease inicial, y se rechazaría.
	edgeContadorInicial int64 = 1
	// edgeBuferTextos es la capacidad del canal de textos: SendText recibidos y aún no leídos.
	edgeBuferTextos = 256
	// edgeSalidaCalentamiento es lo que se contesta a una inferencia de calentamiento (Warmup):
	// su salida se descarta en la nube, así que no consume un paso del guion.
	edgeSalidaCalentamiento = "{}"
	// edgeLeaseNotValidText es el texto de Ack.error con el que el Edge REAL rechaza un envío cuando
	// el lease de la sesión no está vigente (wapp-edge-agent, internal/adapters/cloudlink/adapter.go,
	// handleSendText y handleSendMedia: `a.ack(cl, sid, cmdID, false, "lease no vigente")`). El doble
	// contesta lo mismo, letra por letra: es lo que la nube devuelve en el campo «error» de su API
	// de envío.
	edgeLeaseNotValidText = "lease no vigente"
	// edgeInferenceLeaseGrace es lo que el Edge REAL espera a que el lease se vuelva operable antes
	// de rechazar una inferencia con INFERENCE_ERROR_LEASE_INVALID (wapp-edge-agent,
	// internal/adapters/cloudlink/inferencia.go: `defaultInferenceLeaseGracia = 2000 *
	// time.Millisecond`). Allí existe porque el Validator nace cerrado y el primer LeaseUpdate tarda
	// en llegar; aquí cubre la misma ventana: una petición que el servidor mande antes de su
	// LeaseUpdate inicial no se rechaza, espera.
	edgeInferenceLeaseGrace = 2000 * time.Millisecond
	// edgeInferenceLeasePoll es cada cuánto se vuelve a mirar el lease durante esa gracia (en el
	// Edge real, `sondeoLease = 50 * time.Millisecond`).
	edgeInferenceLeasePoll = 50 * time.Millisecond

	// edgeTopeEnrolar acota la llamada EnrollEdge.
	edgeTopeEnrolar = 15 * time.Second
	// edgeTopeConectar es lo que se espera, como máximo, a que el servidor mande el LeaseUpdate
	// inicial tras el primer latido.
	edgeTopeConectar = 10 * time.Second
	// edgeTopeCierre acota cada espera del cierre del enlace (que el servidor vea el EOF, que el
	// bucle de recepción termine, que las inferencias en vuelo acaben).
	edgeTopeCierre = 5 * time.Second
	// edgeSondeo es cada cuánto se sondea una condición mientras se espera.
	edgeSondeo = 25 * time.Millisecond
	// edgeTopeFila es el tope por defecto de las esperas contra Postgres y contra el log.
	edgeTopeFila = 10 * time.Second
)

var (
	// errEdgeSinSalida marca que el Edge no tiene un enlace abierto por donde emitir un frame.
	errEdgeSinSalida = errors.New("el Edge de prueba no tiene un enlace abierto por donde emitir")
	// errEdgeLeaseNoLlego marca que el servidor no mandó el LeaseUpdate inicial a tiempo.
	errEdgeLeaseNoLlego = errors.New("el servidor no mandó el LeaseUpdate inicial")
	// errEdgeInitialLeaseRejected marca que el Validator RECHAZÓ el primer LeaseUpdate de la
	// conexión (firma de otra clave, blob malformado): el Edge está conectado pero no puede operar.
	// Va siempre acompañado de la causa (errors.Is con cllease.ErrBadSignature, ErrStaleCounter…).
	errEdgeInitialLeaseRejected = errors.New("el Validator rechazó el LeaseUpdate inicial")
)

// textoRecibido es un SendText que el servidor empujó al Edge: A es el destinatario (SendText.to),
// Texto el cuerpo y ComandoID el command_id del comando (el que el Ack devolvió al servidor).
type textoRecibido struct{ A, Texto, ComandoID string }

// configRecibida es un ConfigUpdate que el servidor empujó al Edge: el kind («jwks», «filters»,
// «intents»…), su versión, el contenido y a qué sesión iba dirigido (el session_id del comando;
// edgeSesionControl si entró por el canal de control).
type configRecibida struct {
	Kind, Version string
	Payload       []byte
	ComandoID     string
	Sesion        string
}

// diagnosticoPedido es un DiagnosticsRequest que el servidor mandó al Edge: el Edge NO responde
// solo; el test contesta con bundle cuando quiera.
type diagnosticoPedido struct{ ComandoID, Scope, Sesion string }

// edgeEnlace es una conexión abierta con el servidor: la conexión gRPC, el stream Connect, la
// cancelación de ese stream y el canal que se cierra cuando el bucle de recepción termina (con el
// error que lo terminó en errFin, que solo se lee después de <-fin).
type edgeEnlace struct {
	conn     *grpc.ClientConn
	stream   cloudlinkv1.CloudLink_ConnectClient
	cancelar context.CancelFunc
	fin      chan struct{}
	errFin   error
}

// edge es el Edge de prueba. Los campos exportados son la identidad que le dio el enrolamiento y
// el guion de inferencia; todo lo demás es privado y se lee por los métodos.
//
// Seguro para uso concurrente: el bucle de recepción, las inferencias en vuelo y el test comparten
// el estado bajo mu, y los envíos van serializados por salidaMu (el Send de gRPC no admite
// llamadas concurrentes sobre un mismo stream).
type edge struct {
	// TenantID es el tenant al que se enroló (el de la respuesta de EnrollEdge).
	TenantID string
	// EdgeID es la identidad del Edge: el CommonName de su CSR y de su certificado de cliente.
	EdgeID string
	// SessionID es el session_id con el que habla: el primer frame con un session_id no vacío
	// registra la sesión en el servidor. enrolar le pone uno aleatorio; se puede cambiar ANTES
	// de conectar. No debe ser edgeSesionControl.
	SessionID string
	// CloudEncPub es la pública X25519 de la nube (EnrollEdgeResponse.cloud_enc_pubkey): con ella
	// sella lo sensible que sube.
	CloudEncPub []byte
	// LeasePub es la pública Ed25519 con la que el Validator verifica los leases.
	LeasePub ed25519.PublicKey
	// Inferir es el guion de inferencia (T9.17): recibe la petición que bajó la nube y devuelve el
	// JSON crudo que contestaría el modelo o, si lo hay, el error nombrado que el Edge reportaría
	// (un nil en fallo significa «sin error»). Si es nil, toda inferencia se contesta con
	// INFERENCE_ERROR_OLLAMA_DOWN. Las peticiones de calentamiento (Warmup) no lo consultan, y
	// tampoco las que el gate de lease bloquea (inferenceBlockedByLease). Se asigna ANTES de
	// conectar; se invoca de una en una (una sola plaza, como el Ollama real).
	Inferir func(req *cloudlinkv1.InferenceRequest) (rawJSON string, fallo *cloudlinkv1.InferenceError)

	conectarAddr string
	clave        *ecdsa.PrivateKey
	certPEM      []byte
	caPEM        []byte

	textos chan textoRecibido

	mu           sync.Mutex
	validador    *cllease.Validator
	leases       int
	configs      []configRecibida
	diagnosticos []diagnosticoPedido
	inferencias  []*cloudlinkv1.InferenceRequest
	errores      []error
	enlace       *edgeEnlace
	cerrando     bool

	// initialLeaseErr (bajo mu) es el rechazo del Validator al PRIMER LeaseUpdate de la conexión
	// actual; nil si lo aceptó (vigente o revocación) o si aún no llegó. Lo fija alRecibirLease y
	// lo borra reiniciarLeases.
	initialLeaseErr error

	salidaMu sync.Mutex
	salida   func(*cloudlinkv1.EdgeToCloud) error

	// inferenceLeaseGrace es la gracia del gate de lease de la inferencia (inferenceBlockedByLease).
	// Cero o negativa vale edgeInferenceLeaseGrace, la del Edge real; solo la cambian los tests del
	// propio doble, y ANTES de mandarle la primera petición (después solo se lee).
	inferenceLeaseGrace time.Duration

	inferMu sync.Mutex
	enVuelo sync.WaitGroup
}

// nuevoEdge construye un Edge sin enlace: con su identidad, las dos públicas del servidor y un
// Validator de leases nuevo. Es lo que comparten el enrolamiento real y los tests del núcleo.
// salida queda en nil: sin enlace no se puede emitir.
func nuevoEdge(tenantID, edgeID, sesionID string, cloudEncPub []byte, leasePub ed25519.PublicKey) *edge {
	return &edge{
		TenantID:    tenantID,
		EdgeID:      edgeID,
		SessionID:   sesionID,
		CloudEncPub: cloudEncPub,
		LeasePub:    leasePub,
		textos:      make(chan textoRecibido, edgeBuferTextos),
		validador:   cllease.NewValidator(leasePub),
	}
}

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

// ---------------------------------------------------------------------------------------------
// Frames que el Edge manda
// ---------------------------------------------------------------------------------------------

// emitir manda un frame por la salida del Edge, serializado con los demás envíos. Devuelve
// errEdgeSinSalida si no hay enlace, o el error de la salida.
func (e *edge) emitir(msg *cloudlinkv1.EdgeToCloud) error {
	e.salidaMu.Lock()
	defer e.salidaMu.Unlock()
	if e.salida == nil {
		return errEdgeSinSalida
	}
	return e.salida(msg)
}

// emitirOAnotar es emitir para el bucle de recepción, que no tiene a quién devolver el error: lo
// anota en Errores, salvo que el cierre ya haya empezado (entonces es lo esperado).
func (e *edge) emitirOAnotar(msg *cloudlinkv1.EdgeToCloud) {
	if err := e.emitir(msg); err != nil && !e.estaCerrando() {
		e.anotarError(fmt.Errorf("emitir %T: %w", msg.GetPayload(), err))
	}
}

// edgeLatido arma el frame de latido: el contador de lease dado y la inferencia declarada READY.
// Sin SelfPn a propósito: con un número propio, el servidor mandaría un SendText de saludo a la
// sesión recién emparejada. READY desde el primer latido evita que el servidor pida un
// calentamiento por compatibilidad con los Edges que no dicen nada.
func edgeLatido(sesion string, contador int64) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sesion,
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter:       contador,
			InferenceReadiness: cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY,
		}},
	}
}

// latir manda un latido con el contador de lease dado. El servidor renueva el lease con contador+1,
// y el Validator solo acepta contadores que superen al último aplicado: tras conectar (contadores 1
// y 2) el siguiente latido útil usa 2 o más. Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) latir(t *testing.T, contador int64) {
	t.Helper()
	if err := e.emitir(edgeLatido(e.SessionID, contador)); err != nil {
		t.Fatalf("latir(%d): %v", contador, err)
	}
}

// entrante manda un IncomingMessage SELLADO, como el Edge real: el texto y el número viajan
// dentro de un SensitivePayload marshalado y sellado con la pública de la nube (enc_payload), y
// los planos sensibles (text, push_name, from_pn, from_lid) van VACÍOS. En claro solo van from
// (de), wa_message_id (waID), el instante y el modo de direccionamiento. Si de es un número (con o
// sin «+» y con o sin el sufijo «@…»), ese número va como from_pn dentro del sobre. Falla
// (t.Fatalf) si no puede sellar o emitir.
func (e *edge) entrante(t *testing.T, de, texto, waID string) {
	t.Helper()
	msg, err := e.armarEntrante(de, texto, waID)
	if err != nil {
		t.Fatalf("entrante %s: %v", waID, err)
	}
	if err := e.emitir(msg); err != nil {
		t.Fatalf("entrante %s: %v", waID, err)
	}
}

// armarEntrante construye el frame de entrante sellado. Devuelve error si el sellado falla.
func (e *edge) armarEntrante(de, texto, waID string) (*cloudlinkv1.EdgeToCloud, error) {
	numero := edgeNumeroDe(de)
	plano, err := proto.Marshal(&cloudlinkv1.SensitivePayload{Text: texto, FromPn: numero})
	if err != nil {
		return nil, fmt.Errorf("serializar el SensitivePayload: %w", err)
	}
	sellado, err := envelope.SealFor(e.CloudEncPub, plano)
	if err != nil {
		return nil, fmt.Errorf("sellar el SensitivePayload: %w", err)
	}
	modo := ""
	if numero != "" {
		modo = "pn"
	}
	return &cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From:           de,
			TsUnix:         time.Now().Unix(),
			WaMessageId:    waID,
			AddressingMode: modo,
			EncPayload:     sellado,
		}},
	}, nil
}

// edgeNumeroDe saca el número de un remitente: la parte anterior a «@» sin el «+» inicial, si son
// solo dígitos. Devuelve "" si no lo es (un LID, un texto cualquiera).
func edgeNumeroDe(de string) string {
	numero, _, _ := strings.Cut(de, "@")
	numero = strings.TrimPrefix(numero, "+")
	if numero == "" || strings.Trim(numero, "0123456789") != "" {
		return ""
	}
	return numero
}

// acuse manda un acuse de recibo del mensaje waID: «leído» si leido, «entregado» si no. Es el
// Receipt de CloudLink, con el session_id del Edge dentro del propio acuse (el servidor persiste
// ese) y sin command_id. Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) acuse(t *testing.T, waID string, leido bool) {
	t.Helper()
	estado := cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED
	if leido {
		estado = cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ
	}
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Receipt{Receipt: &cloudlinkv1.MessageReceipt{
			SessionId:  e.SessionID,
			MessageIds: []string{waID},
			Status:     estado,
			Timestamp:  time.Now().Unix(),
		}},
	})
	if err != nil {
		t.Fatalf("acuse de %s: %v", waID, err)
	}
}

// bundle responde a un DiagnosticsRequest: manda un DiagnosticsBundle correlacionado por
// comandoID, con logTail como cola del log y un volcado de goroutines y un estado de subsistemas
// de relleno (sin llaves ni contenido). Falla (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) bundle(t *testing.T, comandoID, logTail string) {
	t.Helper()
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: e.SessionID,
		Payload: &cloudlinkv1.EdgeToCloud_DiagnosticsBundle{DiagnosticsBundle: &cloudlinkv1.DiagnosticsBundle{
			CommandId:      comandoID,
			LogTail:        logTail,
			GoroutineDump:  "goroutine 1 [running]:\nmain.main()\n\t(volcado de prueba)",
			SubsystemsJson: `{"subsistemas":[{"nombre":"prueba","estado":"ok"}]}`,
		}},
	})
	if err != nil {
		t.Fatalf("bundle %s: %v", comandoID, err)
	}
}

// usarCanalControl manda un frame por el canal de control (session_id edgeSesionControl), como
// hace el Edge real al autenticar a su operador antes de emparejar ningún teléfono. El servidor no
// lo registra como sesión; solo le empuja la config inicial por ese mismo stream (jwks y filters),
// que llega a Configs con Sesion = edgeSesionControl. El frame es un Pong sin efecto. Falla
// (t.Fatalf) si no hay enlace o el envío falla.
func (e *edge) usarCanalControl(t *testing.T) {
	t.Helper()
	err := e.emitir(&cloudlinkv1.EdgeToCloud{
		SessionId: edgeSesionControl,
		Payload:   &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{}},
	})
	if err != nil {
		t.Fatalf("usarCanalControl: %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// Comandos que el Edge recibe (el núcleo)
// ---------------------------------------------------------------------------------------------

// despachar es lo que hace el bucle de recepción con cada comando. Las peticiones de inferencia
// van a su propia goroutine, para que ni un guion lento ni la gracia del gate de lease frenen los
// acuses, los leases ni los pings (las inferencias entre sí sí van de una en una); el resto se
// atiende en línea.
func (e *edge) despachar(cmd *cloudlinkv1.CloudToEdge) {
	if cmd.GetInferenceRequest() != nil {
		e.enVuelo.Go(func() { e.manejar(cmd) })
		return
	}
	e.manejar(cmd)
}

// manejar atiende UN comando del servidor, sin transporte: lo que responda lo emite por la salida
// del Edge. SendText: si el lease está vigente publica el texto y lo acusa; si no, lo rechaza.
// SendMedia: si el lease está vigente lo acusa; si no, lo rechaza. LeaseUpdate: lo aplica al
// Validator. ConfigUpdate: lo registra y lo acusa. DiagnosticsRequest: lo registra (no responde
// solo). InferenceRequest: si el lease está vigente contesta con el guion (o con un error); si no,
// tras la gracia, con INFERENCE_ERROR_LEASE_INVALID. Ping: Pong. Cualquier otro comando con
// command_id se acusa como correcto.
//
// El gate de lease cubre lo que en el Edge real es «operar», igual que wapp-edge-agent: los dos
// comandos que despachan a WhatsApp (blockedByLease) y la inferencia, que allí tiene su propia
// regla y aquí también (inferenceBlockedByLease: con gracia y con otra respuesta, un
// InferenceResult y no un Ack). ConfigUpdate, DiagnosticsRequest y Ping no pasan por ninguno.
func (e *edge) manejar(cmd *cloudlinkv1.CloudToEdge) {
	switch p := cmd.GetPayload().(type) {
	case *cloudlinkv1.CloudToEdge_LeaseUpdate:
		e.alRecibirLease(p.LeaseUpdate)
	case *cloudlinkv1.CloudToEdge_SendText:
		e.alRecibirTexto(cmd, p.SendText)
	case *cloudlinkv1.CloudToEdge_SendMedia:
		e.handleSendMedia(cmd)
	case *cloudlinkv1.CloudToEdge_ConfigUpdate:
		e.alRecibirConfig(cmd, p.ConfigUpdate)
	case *cloudlinkv1.CloudToEdge_DiagnosticsRequest:
		e.alRecibirDiagnostico(cmd, p.DiagnosticsRequest)
	case *cloudlinkv1.CloudToEdge_InferenceRequest:
		e.alRecibirInferencia(cmd, p.InferenceRequest)
	case *cloudlinkv1.CloudToEdge_Ping:
		e.alRecibirPing(cmd, p.Ping)
	default:
		e.acusar(cmd)
	}
}

// sesionDe devuelve el session_id con el que se contesta a un comando: el del comando, o el del
// Edge si el comando iba a todas las sesiones (vacío).
func (e *edge) sesionDe(cmd *cloudlinkv1.CloudToEdge) string {
	if sid := cmd.GetSessionId(); sid != "" {
		return sid
	}
	return e.SessionID
}

// acusar contesta a un comando con Ack{ok=true}, correlacionado por su command_id. Un comando sin
// command_id no se acusa (no habría a qué correlacionarlo).
func (e *edge) acusar(cmd *cloudlinkv1.CloudToEdge) {
	e.reply(cmd, true, "")
}

// reply contesta a un comando con Ack{ok, error}, correlacionado por su command_id y en la sesión
// del comando. errText es el motivo cuando ok es falso (vacío si ok). Un comando sin command_id no
// se contesta (no habría a qué correlacionarlo).
func (e *edge) reply(cmd *cloudlinkv1.CloudToEdge, ok bool, errText string) {
	if cmd.GetCommandId() == "" {
		return
	}
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload: &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{
			AckedCommandId: cmd.GetCommandId(),
			Ok:             ok,
			Error:          errText,
		}},
	})
}

// blockedByLease aplica el gate de lease del Edge real a un comando «de operar» (ADR-0007, el
// kill-switch): si el Edge NO puede operar —aún no tiene lease, el Validator rechazó el que llegó,
// venció o está revocado—, contesta Ack{ok=false, error=«lease no vigente»} y devuelve true, y
// quien llama no debe despachar nada. Si puede operar devuelve false sin emitir nada. Es la regla
// de handleSendText y handleSendMedia de wapp-edge-agent
// (internal/adapters/cloudlink/adapter.go: `!validator.CanOperate(hasDEK)`), sin su modo sombra.
// Un bloqueo no es un error del núcleo: no se anota en Errores.
func (e *edge) blockedByLease(cmd *cloudlinkv1.CloudToEdge) bool {
	if e.puedeOperar() {
		return false
	}
	e.reply(cmd, false, edgeLeaseNotValidText)
	return true
}

// alRecibirLease se lo da al Validator y DESPUÉS lo cuenta: cuando Leases() dice n, los n ya están
// aplicados (o rechazados), así que quien espera un lease puede mirar puedeOperar() sin carrera. Un
// lease que el Validator rechaza (firma ajena, contador viejo) se anota en Errores y no cambia el
// estado; si además es el PRIMERO de la conexión, el rechazo queda para conectarErr
// (initialLeaseRejection). Una revocación se acepta: no es un rechazo.
func (e *edge) alRecibirLease(lu *cloudlinkv1.LeaseUpdate) {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	err := v.Apply(lu)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.leases++
	if err == nil {
		return
	}
	if e.leases == 1 {
		e.initialLeaseErr = err
	}
	e.errores = append(e.errores, fmt.Errorf("el Validator rechazó un LeaseUpdate: %w", err))
}

// alRecibirTexto aplica el gate de lease y, si el Edge puede operar, publica el SendText en el
// canal de textos y después lo acusa. Sin lease vigente NO lo publica y lo rechaza con
// Ack{ok=false, «lease no vigente»} (blockedByLease): para el servidor, y para la API de envío, ese
// texto no se entregó. El orden publicar → acusar importa para quien llama por HTTP: el servidor
// devuelve su 200 al recibir el Ack, así que cuando el test ve una respuesta con ok=true el texto
// ya está en el canal, y cuando la ve con ok=false sabe que no lo estará.
func (e *edge) alRecibirTexto(cmd *cloudlinkv1.CloudToEdge, st *cloudlinkv1.SendText) {
	if e.blockedByLease(cmd) {
		return
	}
	txt := textoRecibido{A: st.GetTo(), Texto: st.GetText(), ComandoID: cmd.GetCommandId()}
	select {
	case e.textos <- txt:
	default:
		e.anotarError(fmt.Errorf("el canal de textos está lleno (%d): se descarta el SendText %s", edgeBuferTextos, txt.ComandoID))
	}
	e.acusar(cmd)
}

// handleSendMedia aplica el gate de lease a un SendMedia, como el Edge real: sin lease vigente lo
// rechaza con Ack{ok=false, «lease no vigente»}; con lease lo acusa como correcto. El doble no
// descarga ni registra el archivo: de un SendMedia solo decide si «sale» o no.
func (e *edge) handleSendMedia(cmd *cloudlinkv1.CloudToEdge) {
	if e.blockedByLease(cmd) {
		return
	}
	e.acusar(cmd)
}

// alRecibirConfig registra el ConfigUpdate (con copia del contenido) y lo acusa. No pasa por el
// gate de lease: no es una operación de WhatsApp (igual que en el Edge real).
func (e *edge) alRecibirConfig(cmd *cloudlinkv1.CloudToEdge, cu *cloudlinkv1.ConfigUpdate) {
	e.mu.Lock()
	e.configs = append(e.configs, configRecibida{
		Kind:      cu.GetKind(),
		Version:   cu.GetVersion(),
		Payload:   slices.Clone(cu.GetPayload()),
		ComandoID: cu.GetCommandId(),
		Sesion:    cu.GetSessionId(),
	})
	e.mu.Unlock()
	e.acusar(cmd)
}

// alRecibirDiagnostico registra el DiagnosticsRequest. No responde: el bundle lo manda el test.
func (e *edge) alRecibirDiagnostico(_ *cloudlinkv1.CloudToEdge, dr *cloudlinkv1.DiagnosticsRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.diagnosticos = append(e.diagnosticos, diagnosticoPedido{
		ComandoID: dr.GetCommandId(),
		Scope:     dr.GetScope(),
		Sesion:    dr.GetSessionId(),
	})
}

// alRecibirPing contesta con Pong y el mismo nonce.
func (e *edge) alRecibirPing(cmd *cloudlinkv1.CloudToEdge, p *cloudlinkv1.Ping) {
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload:   &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{Nonce: p.GetNonce()}},
	})
}

// alRecibirInferencia registra la petición (con una copia), le aplica el gate de lease y la
// contesta con un InferenceResult: si el Edge puede operar, la salida del guion sellada con la
// pública de la nube o el error nombrado; si no, INFERENCE_ERROR_LEASE_INVALID sin consultar el
// guion (también a un calentamiento), que es lo que contesta el Edge real
// (internal/adapters/cloudlink/inferencia.go, atender: `c.responderError(c2e,
// app.ErrInferenciaLeaseInvalido)`). La respuesta bloqueada tiene la misma forma que cualquier
// otra del doble: un InferenceResult correlacionado por command_id y NINGÚN Ack. La petición
// bloqueada queda registrada igual (la nube la pidió). El gate va ANTES de la plaza única: una
// petición que espera su gracia no retiene el guion. Las inferencias se atienden de una en una.
func (e *edge) alRecibirInferencia(cmd *cloudlinkv1.CloudToEdge, req *cloudlinkv1.InferenceRequest) {
	if copia, ok := proto.Clone(req).(*cloudlinkv1.InferenceRequest); ok {
		e.mu.Lock()
		e.inferencias = append(e.inferencias, copia)
		e.mu.Unlock()
	}
	id := req.GetCommandId()
	if id == "" {
		id = cmd.GetCommandId()
	}
	var res *cloudlinkv1.InferenceResult
	if e.inferenceBlockedByLease() {
		res = edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID)
	} else {
		e.inferMu.Lock()
		res = e.resultadoDe(id, req)
		e.inferMu.Unlock()
	}
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload:   &cloudlinkv1.EdgeToCloud_InferenceResult{InferenceResult: res},
	})
}

// inferenceBlockedByLease aplica a una inferencia el gate de lease del Edge real (ADR-0007: servir
// inferencia es operar). Devuelve true si NO se puede servir —y quien llama contesta
// INFERENCE_ERROR_LEASE_INVALID— y false si el Edge puede operar. Es la regla de
// carrilInferencia.leaseVigente de wapp-edge-agent (internal/adapters/cloudlink/inferencia.go), que
// NO es la de los envíos (blockedByLease):
//
//   - Alcance DAEMON. Allí basta con que UNA sesión cualquiera tenga su lease vigente
//     (algunaSesionOperable), porque el session_id de un inference_request viene normalmente vacío.
//     El doble tiene una sola sesión con su Validator, así que la pregunta acaba siendo la misma que
//     la de los envíos, puedeOperar(); y como esa sesión existe siempre, la rama «sin ninguna sesión
//     registrada se sirve» del Edge real aquí no ocurre.
//   - Con GRACIA. Si el Edge no puede operar no rechaza en el acto: vuelve a mirar cada
//     edgeInferenceLeasePoll hasta agotar la gracia (inferenceLeaseGrace; por defecto
//     edgeInferenceLeaseGrace) y solo entonces bloquea. Si el lease se vuelve vigente antes, sirve.
//     Con el lease vigente desde el principio no espera nada.
//   - Si el enlace se cierra durante la espera deja de esperar y bloquea (en el Edge real, el
//     contexto del carril cancelado), para que cerrar no tarde lo que dure la gracia.
//
// Sin su modo sombra, igual que blockedByLease. Un bloqueo no es un error del núcleo: no se anota en
// Errores, y el doble no lleva cuenta de los bloqueos.
func (e *edge) inferenceBlockedByLease() bool {
	grace := e.inferenceLeaseGrace
	if grace <= 0 {
		grace = edgeInferenceLeaseGrace
	}
	start := time.Now()
	for !e.puedeOperar() {
		if time.Since(start) >= grace || e.estaCerrando() {
			return true
		}
		time.Sleep(edgeInferenceLeasePoll)
	}
	return false
}

// resultadoDe decide qué contesta el Edge a una inferencia: un calentamiento, una salida vacía
// (sin consultar el guion); sin guion, OLLAMA_DOWN; con guion, lo que devuelva.
func (e *edge) resultadoDe(id string, req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
	switch {
	case req.GetWarmup():
		return e.salidaSellada(id, edgeSalidaCalentamiento)
	case e.Inferir == nil:
		return edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
	}
	crudo, fallo := e.Inferir(req)
	if fallo != nil {
		return edgeErrorInferencia(id, *fallo)
	}
	return e.salidaSellada(id, crudo)
}

// salidaSellada arma el InferenceResult con la salida: un InferenceOutput{raw_json} marshalado y
// sellado con la pública de la nube. Si no se puede sellar anota el error y contesta OLLAMA_DOWN,
// para no dejar a la nube esperando.
func (e *edge) salidaSellada(id, crudo string) *cloudlinkv1.InferenceResult {
	plano, err := proto.Marshal(&cloudlinkv1.InferenceOutput{RawJson: crudo})
	if err == nil {
		var sellado []byte
		if sellado, err = envelope.SealFor(e.CloudEncPub, plano); err == nil {
			return &cloudlinkv1.InferenceResult{
				CommandId: id,
				Result:    &cloudlinkv1.InferenceResult_EncOutput{EncOutput: sellado},
			}
		}
	}
	e.anotarError(fmt.Errorf("sellar la salida de la inferencia %s: %w", id, err))
	return edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
}

// edgeErrorInferencia arma un InferenceResult con error nombrado. UNSPECIFIED no viaja nunca por
// el cable (en el contrato significa «no hay error»), así que se manda como OLLAMA_DOWN.
func edgeErrorInferencia(id string, codigo cloudlinkv1.InferenceError) *cloudlinkv1.InferenceResult {
	if codigo == cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED {
		codigo = cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN
	}
	return &cloudlinkv1.InferenceResult{
		CommandId: id,
		Result:    &cloudlinkv1.InferenceResult_Error{Error: codigo},
	}
}

// anotarError guarda un error del núcleo (un lease rechazado, un envío que falló, un canal lleno)
// para que Errores lo muestre.
func (e *edge) anotarError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errores = append(e.errores, err)
}

// ---------------------------------------------------------------------------------------------
// Lo que el test puede mirar
// ---------------------------------------------------------------------------------------------

// Textos devuelve el canal por el que llegan los SendText que el servidor empuja (búfer de 256) y
// que el Edge «entregó»: los que llegaron sin lease vigente no están (se rechazaron con ok=false).
// Cada texto llega una sola vez; leerlo lo saca del canal.
func (e *edge) Textos() <-chan textoRecibido { return e.textos }

// Configs devuelve una copia de los ConfigUpdate recibidos, en orden de llegada, desde que el Edge
// existe (no se vacía al reconectar).
func (e *edge) Configs() []configRecibida {
	e.mu.Lock()
	defer e.mu.Unlock()
	copia := make([]configRecibida, len(e.configs))
	for i, c := range e.configs {
		c.Payload = slices.Clone(c.Payload)
		copia[i] = c
	}
	return copia
}

// Diagnosticos devuelve una copia de los DiagnosticsRequest recibidos, en orden de llegada.
func (e *edge) Diagnosticos() []diagnosticoPedido {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.diagnosticos)
}

// Inferencias devuelve una copia de las InferenceRequest recibidas (calentamientos incluidos, y
// también las que el gate de lease bloqueó), en orden de llegada. Sirve para ver qué pidió la nube:
// el prompt, el techo de tokens, la clase. No dice cuáles se sirvieron: eso lo dice el guion.
func (e *edge) Inferencias() []*cloudlinkv1.InferenceRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	copia := make([]*cloudlinkv1.InferenceRequest, 0, len(e.inferencias))
	for _, r := range e.inferencias {
		if c, ok := proto.Clone(r).(*cloudlinkv1.InferenceRequest); ok {
			copia = append(copia, c)
		}
	}
	return copia
}

// Leases devuelve cuántos LeaseUpdate ha recibido el Edge en la conexión actual (los rechazados y
// las revocaciones cuentan; el rechazo además queda en Errores). Cuenta los YA procesados por el
// Validator: no dice que haya un lease vigente —eso es puedeOperar()—.
func (e *edge) Leases() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leases
}

// Errores devuelve una copia de los errores que el núcleo anotó: leases rechazados por el
// Validator (errors.Is con cllease.ErrStaleCounter o cllease.ErrBadSignature), envíos que fallaron,
// textos descartados por canal lleno. Un test que no espera ninguno puede exigir que esté vacío.
func (e *edge) Errores() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.errores)
}

// puedeOperar dice lo que dice el Validator con la DEK presente: hay un lease vigente aplicado y
// no está revocado ni vencido. Falso antes de recibir el primero. Es el gate de los envíos
// (blockedByLease) y, con gracia, el de la inferencia (inferenceBlockedByLease). El doble no late
// solo: el lease vence a su TTL (15 min por defecto en el servidor) contado desde la última
// renovación, y un proceso que durase más tendría que latir.
func (e *edge) puedeOperar() bool {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	return v.CanOperate(true)
}

// revocado dice si el Validator vio un lease de revocación (el kill-switch es pegajoso: una vez
// revocado, ningún lease vigente lo deshace).
func (e *edge) revocado() bool {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	return v.Revoked()
}

// esperarTexto espera, con tope, el siguiente SendText del servidor y lo devuelve. Falla
// (t.Fatalf) si no llega a tiempo o si el test termina mientras tanto.
func (e *edge) esperarTexto(t *testing.T, tope time.Duration) textoRecibido {
	t.Helper()
	txt, err := e.recibirTexto(t.Context(), tope)
	if err != nil {
		t.Fatalf("esperarTexto: %v", err)
	}
	return txt
}

// recibirTexto es la espera de esperarTexto sin test: devuelve el siguiente texto, o un error si
// pasa el tope o se cancela el contexto antes.
func (e *edge) recibirTexto(ctx context.Context, tope time.Duration) (textoRecibido, error) {
	timer := time.NewTimer(tope)
	defer timer.Stop()
	select {
	case txt := <-e.textos:
		return txt, nil
	case <-timer.C:
		return textoRecibido{}, fmt.Errorf("no llegó ningún SendText en %s", tope)
	case <-ctx.Done():
		return textoRecibido{}, fmt.Errorf("se canceló la espera de un SendText: %w", ctx.Err())
	}
}

// esperarConfig espera, con tope, un ConfigUpdate del kind dado y devuelve el primero que haya (los
// recibidos antes de llamar también cuentan). Falla (t.Fatalf) si no llega a tiempo.
func (e *edge) esperarConfig(t *testing.T, kind string, tope time.Duration) configRecibida {
	t.Helper()
	var hallada configRecibida
	edgeEsperar(t, tope, fmt.Sprintf("un ConfigUpdate de kind %q", kind), func() bool {
		for _, c := range e.Configs() {
			if c.Kind == kind {
				hallada = c
				return true
			}
		}
		return false
	})
	return hallada
}

// esperarLeases espera, con tope, a que el Edge haya recibido al menos minimo LeaseUpdate en la
// conexión actual. Falla (t.Fatalf) si no llegan a tiempo.
func (e *edge) esperarLeases(t *testing.T, minimo int, tope time.Duration) {
	t.Helper()
	edgeEsperar(t, tope, fmt.Sprintf("al menos %d LeaseUpdate", minimo), func() bool { return e.Leases() >= minimo })
}

// ---------------------------------------------------------------------------------------------
// Esperas y ayudas comunes
// ---------------------------------------------------------------------------------------------

// edgeEsperar sondea cond cada 25 ms hasta que devuelve true, con tope. Falla el test (t.Fatalf)
// con la descripción si pasa el tope o si el test termina antes. cond se ejecuta en la goroutine del
// test, así que puede llamar a t.Fatalf.
func edgeEsperar(t *testing.T, tope time.Duration, descripcion string, cond func() bool) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), tope)
	defer cancelar()
	if !edgeSondear(ctx, cond) {
		t.Fatalf("pasaron %s sin que se diera: %s", tope, descripcion)
	}
}

// edgeSondear evalúa cond ya y después cada 25 ms hasta que devuelve true (devuelve true) o hasta
// que el contexto termina (devuelve false, tras una última evaluación).
func edgeSondear(ctx context.Context, cond func() bool) bool {
	tic := time.NewTicker(edgeSondeo)
	defer tic.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return cond()
		case <-tic.C:
		}
	}
}

// edgePEM codifica un bloque PEM del tipo dado.
func edgePEM(tipo string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: tipo, Bytes: der})
}

// edgePEMPrimero devuelve el contenido (DER) del primer bloque PEM de datos, que debe ser del tipo
// dado. Falla el test (t.Fatalf) si no hay ningún bloque o es de otro tipo.
func edgePEMPrimero(t *testing.T, tipo string, datos []byte) []byte {
	t.Helper()
	bloque, _ := pem.Decode(datos)
	if bloque == nil || bloque.Type != tipo {
		t.Fatalf("no hay un bloque PEM %q al principio de %.60q", tipo, datos)
	}
	return bloque.Bytes
}

// ---------------------------------------------------------------------------------------------
// Tests del núcleo (sin servidor)
// ---------------------------------------------------------------------------------------------

// edgeColector recoge los frames que el núcleo emite, en lugar del stream. Un fallo programado
// hace que la salida devuelva ese error.
type edgeColector struct {
	mu     sync.Mutex
	frames []*cloudlinkv1.EdgeToCloud
	fallo  error
}

// recoger es la salida del núcleo en los tests: guarda el frame o devuelve el fallo programado.
func (c *edgeColector) recoger(m *cloudlinkv1.EdgeToCloud) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fallo != nil {
		return c.fallo
	}
	c.frames = append(c.frames, m)
	return nil
}

// todos devuelve una copia de los frames recogidos, en orden.
func (c *edgeColector) todos() []*cloudlinkv1.EdgeToCloud {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.frames)
}

// esperar espera, con tope de 5 s, a que haya al menos n frames y devuelve todos los recogidos.
func (c *edgeColector) esperar(t *testing.T, n int) []*cloudlinkv1.EdgeToCloud {
	t.Helper()
	edgeEsperar(t, 5*time.Second, fmt.Sprintf("%d frames emitidos por el Edge", n), func() bool { return len(c.todos()) >= n })
	return c.todos()
}

// edgeDePrueba arma un Edge sin servidor: con las claves de una corrida y una salida que apunta a un
// colector. Devuelve el Edge, el colector y las claves (para emitir leases y abrir sobres).
func edgeDePrueba(t *testing.T) (*edge, *edgeColector, claves) {
	t.Helper()
	k := nuevasClaves(t)
	e := nuevoEdge("11111111-1111-4111-8111-111111111111", "edge-prueba", "sesion-prueba", k.NubePub, k.LeasePub)
	c := &edgeColector{}
	e.salida = c.recoger
	return e, c, k
}

// edgeEmisorLease devuelve el emisor de leases del servidor de las claves dadas (el que firma con
// la semilla Ed25519).
func edgeEmisorLease(t *testing.T, k claves) *cllease.Issuer {
	t.Helper()
	semilla := clavesDecodificar(t, "LeaseSeedB64", k.LeaseSeedB64, ed25519.SeedSize)
	iss, err := cllease.NewIssuer(ed25519.NewKeyFromSeed(semilla))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss
}

// edgeLeaseCommand envuelve un LeaseUpdate en el CloudToEdge con el que el servidor lo manda a la
// sesión del Edge (sin command_id). Falla el test (t.Fatalf) si err, el de haberlo emitido, no es
// nil: está pensada para recibir directamente lo que devuelve Issuer.Issue o Issuer.Revoke.
func edgeLeaseCommand(t *testing.T, e *edge, lu *cloudlinkv1.LeaseUpdate, err error) *cloudlinkv1.CloudToEdge {
	t.Helper()
	if err != nil {
		t.Fatalf("emitir el lease: %v", err)
	}
	return edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
	})
}

// edgeGrantLease deja a un Edge sin servidor como lo deja conectar contra uno de verdad: con un
// lease vigente (una hora, contador 1) firmado con las claves dadas y aplicado por el Validator.
// Sin esto el Edge no «entrega» nada (blockedByLease) ni sirve inferencia
// (inferenceBlockedByLease). Falla el test (t.Fatalf) si tras aplicarlo el Edge no puede operar.
func edgeGrantLease(t *testing.T, e *edge, k claves) {
	t.Helper()
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, time.Hour, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if !e.puedeOperar() {
		t.Fatalf("edgeGrantLease: con un lease vigente recién aplicado el Edge no puede operar (errores: %v)", e.Errores())
	}
}

// edgeComando envuelve un payload en un CloudToEdge con el command_id y el session_id dados.
func edgeComando(id, sesion string, payload func(*cloudlinkv1.CloudToEdge)) *cloudlinkv1.CloudToEdge {
	cmd := &cloudlinkv1.CloudToEdge{CommandId: id, SessionId: sesion}
	payload(cmd)
	return cmd
}

// edgeAckDe extrae el Ack de un frame, o falla el test si el frame es de otro tipo.
func edgeAckDe(t *testing.T, f *cloudlinkv1.EdgeToCloud) *cloudlinkv1.Ack {
	t.Helper()
	ack := f.GetAck()
	if ack == nil {
		t.Fatalf("el frame es %T, quería un Ack", f.GetPayload())
	}
	return ack
}

// TestArnes_EdgeNucleo prueba, sin servidor, qué hace el Edge con cada comando del servidor y qué
// emite: con lease vigente el SendText se publica y se acusa (en ese orden) y el SendMedia se
// acusa; sin lease vigente —ninguno todavía, rechazado, vencido o revocado— ninguno de los dos
// «sale» y se contesta Ack{ok=false, «lease no vigente»}, como el Edge real; el ConfigUpdate se
// registra y se acusa (sin gate), el DiagnosticsRequest solo se registra y se contesta con bundle,
// el Ping se contesta con Pong, los leases pasan por el Validator (vigente, revocado, firma ajena,
// contador viejo), un comando desconocido se acusa si trae command_id, y un fallo de la salida
// queda en Errores.
func TestArnes_EdgeNucleo(t *testing.T) {
	t.Parallel()
	t.Run("SendText: se publica y se acusa, en ese orden", edgeProbarSendText)
	t.Run("gate de lease: sin lease vigente no se entrega y se acusa ok=false", edgeCheckLeaseGate)
	t.Run("gate de lease: un lease vencido tampoco deja entregar", edgeCheckLeaseGateExpired)
	t.Run("SendMedia: con lease vigente se acusa", edgeCheckSendMediaWithLease)
	t.Run("ConfigUpdate: se registra y se acusa", edgeProbarConfig)
	t.Run("DiagnosticsRequest: se registra, no responde y bundle contesta", edgeProbarDiagnosticoNucleo)
	t.Run("Ping: Pong con el mismo nonce", edgeProbarPing)
	t.Run("LeaseUpdate vigente y revocación", edgeProbarLeases)
	t.Run("LeaseUpdate de firma ajena y de contador viejo", edgeProbarLeasesRechazados)
	t.Run("comando desconocido: se acusa solo con command_id", edgeProbarDesconocido)
	t.Run("un fallo de la salida queda en Errores", edgeProbarFalloDeSalida)
	t.Run("el canal de textos lleno se anota y no bloquea", edgeProbarCanalLleno)
	t.Run("las listas devueltas son copias", edgeProbarCopias)
}

// edgeProbarSendText comprueba que, con un lease vigente, el SendText llega al canal con destino,
// texto y command_id, que el Ack sale con ok=true, sin error, con el mismo command_id y el
// session_id del comando, y que cuando el Ack sale el texto ya está en el canal.
func edgeProbarSendText(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var textoYaEnElCanal atomic.Bool
	e.salida = func(m *cloudlinkv1.EdgeToCloud) error {
		textoYaEnElCanal.Store(len(e.textos) == 1)
		return c.recoger(m)
	}
	e.manejar(edgeComando("cmd-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "573001110000", Text: "hola"}}
	}))

	select {
	case txt := <-e.Textos():
		if (txt != textoRecibido{A: "573001110000", Texto: "hola", ComandoID: "cmd-1"}) {
			t.Errorf("texto recibido = %+v", txt)
		}
	default:
		t.Fatalf("el SendText no llegó a Textos()")
	}
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames emitidos = %d, quería 1 (el Ack)", len(frames))
	}
	ack := edgeAckDe(t, frames[0])
	if ack.GetAckedCommandId() != "cmd-1" || !ack.GetOk() || ack.GetError() != "" || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("Ack = %+v en la sesión %q, quería ok de cmd-1, sin error, en sesion-x", ack, frames[0].GetSessionId())
	}
	if !textoYaEnElCanal.Load() {
		t.Errorf("el Ack salió antes de publicar el texto: un 200 HTTP no garantizaría verlo en el canal")
	}
}

// edgeOperateCommands son los dos comandos «de operar» del contrato, los que el gate de lease
// cubre, cada uno con un constructor de su payload.
var edgeOperateCommands = []struct {
	name    string
	payload func(*cloudlinkv1.CloudToEdge)
}{
	{"SendText", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "573001110000", Text: "hola"}}
	}},
	{"SendMedia", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendMedia{SendMedia: &cloudlinkv1.SendMedia{To: "573001110000"}}
	}},
}

// edgeCheckBlocked manda al Edge un SendText y un SendMedia con command_id y comprueba que NINGUNO
// «sale»: cada uno se contesta con un solo Ack{ok=false, error=«lease no vigente»} de su command_id
// en la sesión del comando, y el canal de textos sigue vacío. Recibe la etapa (para los mensajes y
// para que los command_id no se repitan). Falla el test con t.Errorf por cada incumplimiento.
func edgeCheckBlocked(t *testing.T, e *edge, c *edgeColector, stage string) {
	t.Helper()
	for _, oc := range edgeOperateCommands {
		before := len(c.todos())
		id := stage + "-" + oc.name
		e.manejar(edgeComando(id, "sesion-x", oc.payload))
		frames := c.todos()[before:]
		if len(frames) != 1 {
			t.Errorf("%s, %s: %d frames emitidos, quería 1 (el Ack de rechazo)", stage, oc.name, len(frames))
			continue
		}
		ack := edgeAckDe(t, frames[0])
		if ack.GetAckedCommandId() != id || ack.GetOk() || ack.GetError() != edgeLeaseNotValidText || frames[0].GetSessionId() != "sesion-x" {
			t.Errorf("%s, %s: Ack = %+v en la sesión %q; quería ok=false con error %q de %s en sesion-x",
				stage, oc.name, ack, frames[0].GetSessionId(), edgeLeaseNotValidText, id)
		}
	}
	if n := len(e.Textos()); n != 0 {
		t.Errorf("%s: hay %d textos en el canal; sin lease vigente el Edge no entrega ninguno", stage, n)
	}
}

// edgeCheckLeaseGate recorre la vida del lease y comprueba el gate en cada etapa, igual que el Edge
// real (handleSendText/handleSendMedia de wapp-edge-agent): sin ningún lease todavía, bloquea; con
// uno que el Validator rechazó por firma ajena, bloquea; con uno vigente, el SendText se publica y
// los dos comandos se acusan ok=true; tras la revocación, bloquea; y un lease vigente posterior no
// lo reabre (la revocación es pegajosa). El texto del rechazo es literalmente «lease no vigente», y
// un comando bloqueado sin command_id no emite nada. Los bloqueos no se anotan en Errores.
func edgeCheckLeaseGate(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	apply := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		e.manejar(edgeLeaseCommand(t, e, lu, err))
	}
	if edgeLeaseNotValidText != "lease no vigente" {
		t.Fatalf("el texto del rechazo es %q: debe ser el del Edge real, «lease no vigente»", edgeLeaseNotValidText)
	}

	edgeCheckBlocked(t, e, c, "sin-lease")
	before := len(c.todos())
	e.manejar(edgeComando("", "sesion-x", edgeOperateCommands[0].payload))
	if n := len(c.todos()) - before; n != 0 || len(e.Textos()) != 0 {
		t.Errorf("un SendText bloqueado sin command_id emitió %d frames y dejó %d textos; quería 0 y 0", n, len(e.Textos()))
	}

	apply(foreign.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	edgeCheckBlocked(t, e, c, "lease-rechazado")

	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 2))
	before = len(c.todos())
	for _, oc := range edgeOperateCommands {
		e.manejar(edgeComando("vigente-"+oc.name, "sesion-x", oc.payload))
	}
	frames := c.todos()[before:]
	if len(frames) != len(edgeOperateCommands) {
		t.Fatalf("con lease vigente: %d frames, quería %d Ack", len(frames), len(edgeOperateCommands))
	}
	for i, oc := range edgeOperateCommands {
		if ack := edgeAckDe(t, frames[i]); ack.GetAckedCommandId() != "vigente-"+oc.name || !ack.GetOk() || ack.GetError() != "" {
			t.Errorf("con lease vigente, %s: Ack = %+v, quería ok=true sin error", oc.name, ack)
		}
	}
	if txt, err := e.recibirTexto(t.Context(), time.Second); err != nil || txt.ComandoID != "vigente-SendText" {
		t.Errorf("con lease vigente el SendText debía llegar al canal: %+v, %v", txt, err)
	}

	apply(iss.Revoke(e.EdgeID, e.TenantID))
	edgeCheckBlocked(t, e, c, "revocado")
	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	edgeCheckBlocked(t, e, c, "revocado-y-renovado")

	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrBadSignature) {
		t.Errorf("Errores = %v, quería solo el lease de firma ajena (un bloqueo no es un error del núcleo)", errs)
	}
}

// edgeCheckLeaseGateExpired comprueba que el gate mira la vigencia y no solo «hubo un lease»: un
// lease bien firmado y aceptado por el Validator, pero ya vencido, no deja entregar.
func edgeCheckLeaseGateExpired(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, -time.Minute, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if errs := e.Errores(); len(errs) != 0 || e.Leases() != 1 || e.revocado() {
		t.Fatalf("el lease vencido debía aceptarse sin más: errores %v, leases %d, revocado %v", errs, e.Leases(), e.revocado())
	}
	if e.puedeOperar() {
		t.Fatalf("con un lease vencido el Edge dice que puede operar")
	}
	edgeCheckBlocked(t, e, c, "vencido")
}

// edgeCheckSendMediaWithLease comprueba el SendMedia con lease vigente: se acusa ok=true, sin
// error, con su command_id y en la sesión del comando; sin command_id no se acusa; y no deja nada
// en el canal de textos (el doble no registra el archivo).
func edgeCheckSendMediaWithLease(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	media := edgeOperateCommands[1].payload
	e.manejar(edgeComando("", "sesion-x", media))
	if n := len(c.todos()); n != 0 {
		t.Fatalf("un SendMedia sin command_id emitió %d frames", n)
	}
	e.manejar(edgeComando("media-1", "sesion-x", media))
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, quería 1", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "media-1" || !a.GetOk() || a.GetError() != "" || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("Ack = %+v en la sesión %q, quería ok de media-1 en sesion-x", a, frames[0].GetSessionId())
	}
	if n := len(e.Textos()); n != 0 {
		t.Errorf("el SendMedia dejó %d textos en el canal", n)
	}
}

// edgeProbarConfig comprueba que el ConfigUpdate se registra entero (kind, versión, contenido, sesión
// y command_id), que se acusa, y que uno dirigido a todas las sesiones (session_id vacío) se
// acusa con la sesión del Edge. El Edge de este test NO tiene lease: el ConfigUpdate no pasa por
// el gate (como en el Edge real, no es una operación de WhatsApp).
func edgeProbarConfig(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("cfg-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{
			CommandId: "cfg-1", SessionId: "sesion-x", Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`),
		}}
	}))
	e.manejar(edgeComando("cfg-2", "", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{
			CommandId: "cfg-2", Kind: "filters", Version: "7", Payload: []byte(`{}`),
		}}
	}))

	configs := e.Configs()
	if len(configs) != 2 {
		t.Fatalf("configs = %d, quería 2", len(configs))
	}
	quiere := configRecibida{Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`), ComandoID: "cfg-1", Sesion: "sesion-x"}
	if configs[0].Kind != quiere.Kind || configs[0].Version != quiere.Version || string(configs[0].Payload) != string(quiere.Payload) ||
		configs[0].ComandoID != quiere.ComandoID || configs[0].Sesion != quiere.Sesion {
		t.Errorf("config 0 = %+v, quería %+v", configs[0], quiere)
	}
	frames := c.todos()
	if len(frames) != 2 {
		t.Fatalf("frames emitidos = %d, quería 2 Ack", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "cfg-1" || !a.GetOk() {
		t.Errorf("Ack 0 = %+v", a)
	}
	if a := edgeAckDe(t, frames[1]); a.GetAckedCommandId() != "cfg-2" || frames[1].GetSessionId() != "sesion-prueba" {
		t.Errorf("Ack 1 = %+v en la sesión %q, quería cfg-2 en sesion-prueba", a, frames[1].GetSessionId())
	}
}

// edgeProbarDiagnosticoNucleo comprueba que el DiagnosticsRequest se registra sin emitir nada, y que bundle
// manda un DiagnosticsBundle con el command_id y la cola de log dados.
func edgeProbarDiagnosticoNucleo(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("diag-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_DiagnosticsRequest{DiagnosticsRequest: &cloudlinkv1.DiagnosticsRequest{
			CommandId: "diag-1", SessionId: "sesion-x", Scope: "full",
		}}
	}))
	if pedidos := e.Diagnosticos(); len(pedidos) != 1 || pedidos[0] != (diagnosticoPedido{ComandoID: "diag-1", Scope: "full", Sesion: "sesion-x"}) {
		t.Errorf("diagnósticos = %+v", pedidos)
	}
	if n := len(c.todos()); n != 0 {
		t.Fatalf("el DiagnosticsRequest emitió %d frames; el Edge no responde solo", n)
	}

	e.bundle(t, "diag-1", "línea 1\nlínea 2")
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetSessionId() != "sesion-prueba" {
		t.Fatalf("frames tras bundle = %d, sesión %q", len(frames), frames[0].GetSessionId())
	}
	b := frames[0].GetDiagnosticsBundle()
	if b.GetCommandId() != "diag-1" || b.GetLogTail() != "línea 1\nlínea 2" || b.GetGoroutineDump() == "" || b.GetSubsystemsJson() == "" {
		t.Errorf("bundle = %+v", b)
	}
}

// edgeProbarPing comprueba que el Ping se contesta con un Pong del mismo nonce en la sesión del
// comando.
func edgeProbarPing(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeComando("", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 42}}
	}))
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetPong().GetNonce() != 42 || frames[0].GetSessionId() != "sesion-x" {
		t.Errorf("frames tras Ping = %+v", frames)
	}
}

// edgeProbarLeases comprueba que un lease vigente deja operar al Edge, que uno de revocación lo
// corta y marca el kill-switch, y que después ningún lease vigente lo revive.
func edgeProbarLeases(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	aplicar := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("emitir el lease: %v", err)
		}
		e.manejar(edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
			cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
		}))
	}
	if e.puedeOperar() || e.revocado() {
		t.Fatalf("sin lease, puedeOperar=%v revocado=%v; quería falso y falso", e.puedeOperar(), e.revocado())
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	if !e.puedeOperar() || e.revocado() || e.Leases() != 1 {
		t.Errorf("con un lease vigente: puedeOperar=%v revocado=%v leases=%d", e.puedeOperar(), e.revocado(), e.Leases())
	}
	aplicar(iss.Revoke(e.EdgeID, e.TenantID))
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras la revocación: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("un lease vigente revivió al Edge revocado: puedeOperar=%v revocado=%v", e.puedeOperar(), e.revocado())
	}
	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("Errores = %v, quería ninguno", errs)
	}
}

// edgeProbarLeasesRechazados comprueba que un lease firmado por otra clave y uno de contador no
// superior al aplicado se anotan en Errores con su causa (errors.Is) y no cambian el estado.
func edgeProbarLeasesRechazados(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	iss := edgeEmisorLease(t, k)
	ajeno := edgeEmisorLease(t, nuevasClaves(t))
	aplicar := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("emitir el lease: %v", err)
		}
		e.manejar(edgeComando("", e.SessionID, func(cmd *cloudlinkv1.CloudToEdge) {
			cmd.Payload = &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu}
		}))
	}
	aplicar(ajeno.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	if e.puedeOperar() {
		t.Errorf("un lease de firma ajena dejó operar al Edge")
	}
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 5))
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 5))
	aplicar(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 3))
	if !e.puedeOperar() || e.Leases() != 4 {
		t.Errorf("puedeOperar=%v leases=%d; quería verdadero y 4 recibidos", e.puedeOperar(), e.Leases())
	}
	errs := e.Errores()
	if len(errs) != 3 || !errors.Is(errs[0], cllease.ErrBadSignature) ||
		!errors.Is(errs[1], cllease.ErrStaleCounter) || !errors.Is(errs[2], cllease.ErrStaleCounter) {
		t.Errorf("Errores = %v, quería firma ajena y dos contadores viejos", errs)
	}
}

// edgeProbarDesconocido comprueba que un comando que el Edge no interpreta (un UserAuthResponse, la
// respuesta al login del operador) se acusa como correcto si trae command_id, y no se acusa si no
// lo trae. El Edge de este test NO tiene lease: lo que no es «de operar» no pasa por el gate. (El
// SendMedia, que hacía de «desconocido» aquí, dejó de serlo: tiene su gate y sus propios tests.)
func edgeProbarDesconocido(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	unknown := func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_UserAuthResponse{UserAuthResponse: &cloudlinkv1.UserAuthResponse{}}
	}
	e.manejar(edgeComando("", "sesion-x", unknown))
	if n := len(c.todos()); n != 0 {
		t.Fatalf("un comando sin command_id emitió %d frames", n)
	}
	e.manejar(edgeComando("auth-1", "sesion-x", unknown))
	frames := c.todos()
	if len(frames) != 1 {
		t.Fatalf("frames = %d, quería 1", len(frames))
	}
	if a := edgeAckDe(t, frames[0]); a.GetAckedCommandId() != "auth-1" || !a.GetOk() || a.GetError() != "" {
		t.Errorf("Ack = %+v", a)
	}
}

// edgeProbarFalloDeSalida comprueba que un envío que falla se anota en Errores con su causa, y que
// con el cierre ya empezado deja de anotarse.
func edgeProbarFalloDeSalida(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	causa := errors.New("tubería rota")
	c.fallo = causa
	e.manejar(edgeComando("p-1", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 1}}
	}))
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], causa) {
		t.Fatalf("Errores = %v, quería uno que envuelva %v", errs, causa)
	}

	e.mu.Lock()
	e.cerrando = true
	e.mu.Unlock()
	e.manejar(edgeComando("p-2", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 2}}
	}))
	if n := len(e.Errores()); n != 1 {
		t.Errorf("con el cierre empezado se anotó otro error: %d", n)
	}
	e.salida = nil
	if err := e.emitir(edgeLatido("s", 1)); !errors.Is(err, errEdgeSinSalida) {
		t.Errorf("emitir sin salida = %v, quería errEdgeSinSalida", err)
	}
}

// edgeProbarCanalLleno comprueba que, con lease vigente y el canal de textos lleno, el siguiente
// SendText no bloquea al bucle de recepción: se descarta, se anota en Errores y aun así se acusa.
func edgeProbarCanalLleno(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	texto := func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "x", Text: "y"}}
	}
	for i := range edgeBuferTextos + 1 {
		e.manejar(edgeComando(fmt.Sprintf("t-%d", i), "s", texto))
	}
	if len(e.Textos()) != edgeBuferTextos {
		t.Errorf("textos en el canal = %d, quería %d", len(e.Textos()), edgeBuferTextos)
	}
	if errs := e.Errores(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "t-256") {
		t.Errorf("Errores = %v, quería uno que nombre el texto t-256 descartado", errs)
	}
	if n := len(c.todos()); n != edgeBuferTextos+1 {
		t.Errorf("acuses = %d, quería %d (también el del descartado)", n, edgeBuferTextos+1)
	}
}

// edgeProbarCopias comprueba que modificar lo que devuelven Configs, Diagnosticos y Errores no
// altera el registro del Edge.
func edgeProbarCopias(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	e.manejar(edgeComando("cfg-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{CommandId: "cfg-1", Kind: "k", Payload: []byte("abc")}}
	}))
	e.manejar(edgeComando("d-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_DiagnosticsRequest{DiagnosticsRequest: &cloudlinkv1.DiagnosticsRequest{CommandId: "d-1"}}
	}))
	e.anotarError(errors.New("uno"))

	cfg := e.Configs()
	cfg[0].Payload[0] = 'X'
	cfg[0].Kind = "otro"
	diag := e.Diagnosticos()
	diag[0].ComandoID = "otro"
	errs := e.Errores()
	errs[0] = errors.New("otro")

	if again := e.Configs(); string(again[0].Payload) != "abc" || again[0].Kind != "k" {
		t.Errorf("Configs devolvió una vista del registro: %+v", again[0])
	}
	if again := e.Diagnosticos(); again[0].ComandoID != "d-1" {
		t.Errorf("Diagnosticos devolvió una vista del registro: %+v", again[0])
	}
	if again := e.Errores(); again[0].Error() != "uno" {
		t.Errorf("Errores devolvió una vista del registro: %v", again[0])
	}
}

// TestArnes_EdgeInferencia prueba, sin servidor, cómo contesta el Edge a una InferenceRequest. Con
// lease vigente: con guion, el JSON sale sellado y se abre con la privada de la nube; si el guion
// falla, sale el error nombrado; sin guion, OLLAMA_DOWN; un calentamiento no consulta el guion; las
// peticiones quedan registradas; un guion lento no frena el resto de comandos; y las inferencias
// van de una en una. Sin lease vigente —ninguno todavía, rechazado, vencido o revocado—, y como el
// Edge real: agotada la gracia contesta INFERENCE_ERROR_LEASE_INVALID sin consultar el guion; si el
// lease llega dentro de la gracia, sirve; y cerrar el enlace corta la espera.
func TestArnes_EdgeInferencia(t *testing.T) {
	t.Parallel()
	t.Run("gate de lease: sin lease vigente no se infiere y se contesta LEASE_INVALID", edgeCheckInferenceLeaseGate)
	t.Run("gate de lease: un lease vencido tampoco deja inferir", edgeCheckInferenceLeaseGateExpired)
	t.Run("gate de lease: la gracia espera al lease y el cierre la corta", edgeCheckInferenceLeaseGrace)
	t.Run("con guion: la salida va sellada", edgeProbarInferenciaConGuion)
	t.Run("el guion falla: error nombrado", edgeProbarInferenciaFalla)
	t.Run("sin guion: OLLAMA_DOWN", edgeProbarInferenciaSinGuion)
	t.Run("el calentamiento no consulta el guion", edgeProbarCalentamiento)
	t.Run("un guion lento no frena los demás comandos", edgeProbarInferenciaLenta)
	t.Run("las inferencias van de una en una", edgeProbarInferenciasSerializadas)
	t.Run("UNSPECIFIED no viaja", edgeProbarErrorNoEspecificado)
}

// edgePeticion arma un CloudToEdge con una InferenceRequest.
func edgePeticion(id, prompt string, warmup bool, tope *int32) *cloudlinkv1.CloudToEdge {
	return edgeComando(id, "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_InferenceRequest{InferenceRequest: &cloudlinkv1.InferenceRequest{
			CommandId: id, Prompt: prompt, Class: "lote", Warmup: warmup, MaxOutputTokens: tope,
		}}
	})
}

// edgeAbrirSalida abre el InferenceResult de un frame con la privada de la nube y devuelve el JSON
// crudo, o falla el test si el frame no es una salida sellada.
func edgeAbrirSalida(t *testing.T, f *cloudlinkv1.EdgeToCloud, k claves) (id, crudo string) {
	t.Helper()
	res := f.GetInferenceResult()
	if res == nil {
		t.Fatalf("el frame es %T, quería un InferenceResult", f.GetPayload())
	}
	sellado := res.GetEncOutput()
	if len(sellado) == 0 {
		t.Fatalf("el InferenceResult no trae salida sellada (error %v)", res.GetError())
	}
	plano, err := envelope.OpenWith(k.NubePriv, sellado)
	if err != nil {
		t.Fatalf("abrir la salida con la privada de la nube: %v", err)
	}
	var salida cloudlinkv1.InferenceOutput
	if err := proto.Unmarshal(plano, &salida); err != nil {
		t.Fatalf("la salida abierta no es un InferenceOutput: %v", err)
	}
	return res.GetCommandId(), salida.GetRawJson()
}

// edgeInferenceTestGrace es la gracia corta con la que los tests del núcleo recorren el gate de
// lease de la inferencia sin pagar los 2 s del Edge real en cada bloqueo.
const edgeInferenceTestGrace = 100 * time.Millisecond

// edgeCheckInferenceBlocked manda al Edge una inferencia normal y un calentamiento y comprueba que
// NINGUNO se sirve: cada uno se contesta con un solo frame, un InferenceResult con el command_id de
// la petición, INFERENCE_ERROR_LEASE_INVALID y sin salida sellada, en la sesión del comando; no es
// un Ack. Recibe la etapa (para los mensajes y para que los command_id no se repitan). Falla el
// test con t.Errorf por cada incumplimiento.
func edgeCheckInferenceBlocked(t *testing.T, e *edge, c *edgeColector, stage string) {
	t.Helper()
	for _, warmup := range []bool{false, true} {
		before := len(c.todos())
		id := fmt.Sprintf("%s-warmup-%v", stage, warmup)
		e.manejar(edgePeticion(id, "p", warmup, nil))
		frames := c.todos()[before:]
		if len(frames) != 1 {
			t.Errorf("%s, warmup=%v: %d frames emitidos, quería 1 (el InferenceResult de rechazo)", stage, warmup, len(frames))
			continue
		}
		res := frames[0].GetInferenceResult()
		if res == nil {
			t.Errorf("%s, warmup=%v: el frame es %T, quería un InferenceResult (la inferencia no se acusa con Ack)", stage, warmup, frames[0].GetPayload())
			continue
		}
		if res.GetCommandId() != id || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID ||
			len(res.GetEncOutput()) != 0 || frames[0].GetSessionId() != "sesion-x" {
			t.Errorf("%s, warmup=%v: InferenceResult de %q con error %v y %d bytes de salida sellada, en la sesión %q; quería LEASE_INVALID de %s, sin salida, en sesion-x",
				stage, warmup, res.GetCommandId(), res.GetError(), len(res.GetEncOutput()), frames[0].GetSessionId(), id)
		}
	}
}

// edgeCheckInferenceLeaseGate recorre la vida del lease y comprueba el gate de la inferencia en cada
// etapa, igual que el Edge real (carrilInferencia.leaseVigente de wapp-edge-agent): sin ningún lease
// todavía, bloquea; con uno que el Validator rechazó por firma ajena, bloquea; con uno vigente, la
// inferencia consulta el guion y sale sellada, y el calentamiento sale; tras la revocación, bloquea;
// y un lease vigente posterior no lo reabre (la revocación es pegajosa). El guion solo se consulta
// la vez que se sirvió, las peticiones bloqueadas quedan registradas, y los bloqueos no se anotan
// en Errores. La gracia y el sondeo por defecto son literalmente los del Edge real.
func edgeCheckInferenceLeaseGate(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	e.inferenceLeaseGrace = edgeInferenceTestGrace
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	apply := func(lu *cloudlinkv1.LeaseUpdate, err error) {
		t.Helper()
		e.manejar(edgeLeaseCommand(t, e, lu, err))
	}
	if edgeInferenceLeaseGrace != 2000*time.Millisecond || edgeInferenceLeasePoll != 50*time.Millisecond {
		t.Fatalf("la gracia y el sondeo por defecto son %s y %s: deben ser los del Edge real, 2 s y 50 ms",
			edgeInferenceLeaseGrace, edgeInferenceLeasePoll)
	}
	var calls atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		calls.Add(1)
		return `{"servida":true}`, nil
	}

	edgeCheckInferenceBlocked(t, e, c, "sin-lease")
	apply(foreign.Issue(e.EdgeID, e.TenantID, time.Hour, 1))
	edgeCheckInferenceBlocked(t, e, c, "lease-rechazado")

	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 2))
	before := len(c.todos())
	e.manejar(edgePeticion("vigente", "p", false, nil))
	e.manejar(edgePeticion("vigente-calentamiento", "p", true, nil))
	frames := c.todos()[before:]
	if len(frames) != 2 {
		t.Fatalf("con lease vigente: %d frames, quería 2 InferenceResult", len(frames))
	}
	if id, raw := edgeAbrirSalida(t, frames[0], k); id != "vigente" || raw != `{"servida":true}` {
		t.Errorf("con lease vigente, la inferencia = (%q, %q); quería la salida del guion", id, raw)
	}
	if id, raw := edgeAbrirSalida(t, frames[1], k); id != "vigente-calentamiento" || raw != edgeSalidaCalentamiento {
		t.Errorf("con lease vigente, el calentamiento = (%q, %q); quería %q", id, raw, edgeSalidaCalentamiento)
	}

	apply(iss.Revoke(e.EdgeID, e.TenantID))
	edgeCheckInferenceBlocked(t, e, c, "revocado")
	apply(iss.Issue(e.EdgeID, e.TenantID, time.Hour, 9))
	edgeCheckInferenceBlocked(t, e, c, "revocado-y-renovado")

	if n := calls.Load(); n != 1 {
		t.Errorf("el guion se consultó %d veces, quería 1: una inferencia bloqueada no llega al guion", n)
	}
	if n := len(e.Inferencias()); n != 10 {
		t.Errorf("Inferencias registra %d peticiones, quería 10 (las 8 bloqueadas también: la nube las pidió)", n)
	}
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrBadSignature) {
		t.Errorf("Errores = %v, quería solo el lease de firma ajena (un bloqueo no es un error del núcleo)", errs)
	}
}

// edgeCheckInferenceLeaseGateExpired comprueba que el gate de la inferencia mira la vigencia y no
// solo «hubo un lease»: con un lease bien firmado y aceptado por el Validator, pero ya vencido, no
// se infiere.
func edgeCheckInferenceLeaseGateExpired(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	e.inferenceLeaseGrace = edgeInferenceTestGrace
	lu, err := edgeEmisorLease(t, k).Issue(e.EdgeID, e.TenantID, -time.Minute, edgeContadorInicial)
	e.manejar(edgeLeaseCommand(t, e, lu, err))
	if errs := e.Errores(); len(errs) != 0 || e.puedeOperar() {
		t.Fatalf("el lease vencido debía aceptarse y no dejar operar: errores %v, puedeOperar %v", errs, e.puedeOperar())
	}
	edgeCheckInferenceBlocked(t, e, c, "vencido")
}

// edgeCheckInferenceLeaseGrace comprueba la gracia del gate, con el camino real del bucle
// (despachar) y sin depender de cuánto tarde la máquina: (a) el rechazo no sale antes de agotar la
// gracia; (b) una petición que llega ANTES que el lease —la ventana del arranque— espera y, en
// cuanto el lease es vigente, se sirve (con una gracia de 30 s: si el gate no sondeara, el test
// agotaría su espera en vez de pasar por suerte); y (c) cerrar el enlace corta la espera, y la
// petición se contesta LEASE_INVALID sin anotar errores.
func edgeCheckInferenceLeaseGrace(t *testing.T) {
	t.Parallel()
	const long = 30 * time.Second

	t.Run("el rechazo espera la gracia entera", func(t *testing.T) {
		t.Parallel()
		e, c, _ := edgeDePrueba(t)
		e.inferenceLeaseGrace = 3 * edgeInferenceTestGrace
		start := time.Now()
		e.manejar(edgePeticion("espera", "p", false, nil))
		if d := time.Since(start); d < e.inferenceLeaseGrace {
			t.Errorf("el rechazo salió a los %s, antes de agotar la gracia de %s", d, e.inferenceLeaseGrace)
		}
		if res := c.esperar(t, 1)[0].GetInferenceResult(); res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID {
			t.Errorf("resultado = %+v, quería LEASE_INVALID", res)
		}
	})

	t.Run("el lease llega dentro de la gracia: se sirve", func(t *testing.T) {
		t.Parallel()
		e, c, k := edgeDePrueba(t)
		e.inferenceLeaseGrace = long
		e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
			return `{"a tiempo":true}`, nil
		}
		e.despachar(edgePeticion("temprana", "p", false, nil))
		edgeEsperar(t, 5*time.Second, "la petición registrada", func() bool { return len(e.Inferencias()) == 1 })
		if n := len(c.todos()); n != 0 {
			t.Fatalf("sin lease y dentro de la gracia el Edge ya emitió %d frames", n)
		}
		edgeGrantLease(t, e, k)
		if id, raw := edgeAbrirSalida(t, c.esperar(t, 1)[0], k); id != "temprana" || raw != `{"a tiempo":true}` {
			t.Errorf("salida = (%q, %q); quería la del guion", id, raw)
		}
		if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
			t.Errorf("la inferencia en vuelo no terminó")
		}
	})

	t.Run("cerrar el enlace corta la espera", func(t *testing.T) {
		t.Parallel()
		e, c, _ := edgeDePrueba(t)
		e.inferenceLeaseGrace = long
		e.despachar(edgePeticion("al-cerrar", "p", false, nil))
		edgeEsperar(t, 5*time.Second, "la petición registrada", func() bool { return len(e.Inferencias()) == 1 })
		if err := e.cerrarEnlace(); err != nil {
			t.Fatalf("cerrarEnlace: %v", err)
		}
		if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
			t.Fatalf("la inferencia siguió esperando su gracia de %s tras empezar el cierre", long)
		}
		if res := c.esperar(t, 1)[0].GetInferenceResult(); res.GetCommandId() != "al-cerrar" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID {
			t.Errorf("resultado = %+v, quería LEASE_INVALID de al-cerrar", res)
		}
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("Errores = %v; cortar la gracia al cerrar no es un error del núcleo", errs)
		}
	})
}

// edgeProbarInferenciaConGuion comprueba que el guion recibe la petición, que su JSON vuelve sellado
// (se abre con NubePriv y reproduce el JSON), con el command_id de la petición y en la sesión del
// comando, y que la petición queda registrada entera.
func edgeProbarInferenciaConGuion(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var visto atomic.Pointer[cloudlinkv1.InferenceRequest]
	e.Inferir = func(req *cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		visto.Store(req)
		return `{"intencion":"presupuesto","ñandú":"€"}`, nil
	}
	tope := int32(512)
	e.manejar(edgePeticion("inf-1", "clasifica esto", false, &tope))

	frames := c.todos()
	if len(frames) != 1 || frames[0].GetSessionId() != "sesion-x" {
		t.Fatalf("frames = %d", len(frames))
	}
	id, crudo := edgeAbrirSalida(t, frames[0], k)
	if id != "inf-1" || crudo != `{"intencion":"presupuesto","ñandú":"€"}` {
		t.Errorf("salida = (%q, %q)", id, crudo)
	}
	if v := visto.Load(); v == nil || v.GetPrompt() != "clasifica esto" || v.GetMaxOutputTokens() != 512 {
		t.Errorf("el guion vio %+v", v)
	}
	reg := e.Inferencias()
	if len(reg) != 1 || reg[0].GetPrompt() != "clasifica esto" || reg[0].GetClass() != "lote" || reg[0].MaxOutputTokens == nil {
		t.Errorf("Inferencias = %+v", reg)
	}
}

// edgeProbarInferenciaFalla comprueba que el error del guion sale como error nombrado, sin salida
// sellada.
func edgeProbarInferenciaFalla(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		return "ignorado", cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT.Enum()
	}
	e.manejar(edgePeticion("inf-2", "p", false, nil))
	res := c.todos()[0].GetInferenceResult()
	if res.GetCommandId() != "inf-2" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT || len(res.GetEncOutput()) != 0 {
		t.Errorf("resultado = %+v, quería TIMEOUT sin salida", res)
	}
}

// edgeProbarInferenciaSinGuion comprueba que sin guion se contesta OLLAMA_DOWN.
func edgeProbarInferenciaSinGuion(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	e.manejar(edgePeticion("inf-3", "p", false, nil))
	res := c.todos()[0].GetInferenceResult()
	if res.GetCommandId() != "inf-3" || res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN || len(res.GetEncOutput()) != 0 {
		t.Errorf("resultado = %+v, quería OLLAMA_DOWN", res)
	}
}

// edgeProbarCalentamiento comprueba que una petición de calentamiento se contesta con la salida
// vacía sellada SIN llamar al guion (no gasta un paso), y que queda registrada como calentamiento.
func edgeProbarCalentamiento(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var llamadas atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		llamadas.Add(1)
		return `{"no":"debe verse"}`, nil
	}
	e.manejar(edgePeticion("cal-1", "prefijo", true, nil))
	_, crudo := edgeAbrirSalida(t, c.todos()[0], k)
	if crudo != edgeSalidaCalentamiento || llamadas.Load() != 0 {
		t.Errorf("calentamiento: salida %q con %d llamadas al guion; quería %q y 0", crudo, llamadas.Load(), edgeSalidaCalentamiento)
	}
	if reg := e.Inferencias(); len(reg) != 1 || !reg[0].GetWarmup() {
		t.Errorf("Inferencias = %+v, quería un calentamiento", reg)
	}
}

// edgeProbarInferenciaLenta comprueba, con el camino real del bucle (despachar), que mientras un
// guion está bloqueado el Edge sigue contestando Pings, y que al soltarlo la salida sale.
func edgeProbarInferenciaLenta(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	entro, suelta := make(chan struct{}), make(chan struct{})
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		close(entro)
		<-suelta
		return `{"tarde":true}`, nil
	}
	e.despachar(edgePeticion("lenta", "p", false, nil))
	select {
	case <-entro:
	case <-time.After(5 * time.Second):
		t.Fatalf("el guion no llegó a ejecutarse")
	}
	e.despachar(edgeComando("", "sesion-x", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_Ping{Ping: &cloudlinkv1.Ping{Nonce: 7}}
	}))
	frames := c.todos()
	if len(frames) != 1 || frames[0].GetPong().GetNonce() != 7 {
		t.Fatalf("con el guion bloqueado, frames = %d; quería solo el Pong", len(frames))
	}
	close(suelta)
	frames = c.esperar(t, 2)
	if _, crudo := edgeAbrirSalida(t, frames[1], k); crudo != `{"tarde":true}` {
		t.Errorf("salida tras soltar el guion = %q", crudo)
	}
	if !edgeEsperarGrupo(&e.enVuelo, 5*time.Second) {
		t.Errorf("la inferencia en vuelo no terminó")
	}
}

// edgeProbarInferenciasSerializadas comprueba que dos peticiones simultáneas no ejecutan el guion a
// la vez (una plaza única, como el Ollama real) y que las dos se contestan.
func edgeProbarInferenciasSerializadas(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	var dentro, maximo atomic.Int64
	e.Inferir = func(*cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
		n := dentro.Add(1)
		for {
			m := maximo.Load()
			if n <= m || maximo.CompareAndSwap(m, n) {
				break
			}
		}
		// Cede el procesador varias veces dentro del guion, como lo haría el modelo trabajando: si
		// otra inferencia pudiera entrar a la vez, aquí tendría tiempo de hacerlo.
		for range 200 {
			runtime.Gosched()
		}
		dentro.Add(-1)
		return "{}", nil
	}
	for _, id := range []string{"a", "b", "c"} {
		e.despachar(edgePeticion(id, "p", false, nil))
	}
	c.esperar(t, 3)
	if m := maximo.Load(); m != 1 {
		t.Errorf("el guion llegó a correr %d veces a la vez, quería 1", m)
	}
}

// edgeProbarErrorNoEspecificado comprueba que un error UNSPECIFIED, que en el contrato significa
// «no hay error», se manda como OLLAMA_DOWN y no como un resultado sin salida ni error.
func edgeProbarErrorNoEspecificado(t *testing.T) {
	t.Parallel()
	res := edgeErrorInferencia("x", cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED)
	if res.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN {
		t.Errorf("error = %v, quería OLLAMA_DOWN", res.GetError())
	}
}

// TestArnes_EdgeSaliente prueba, sin servidor, los frames que el Edge manda por iniciativa del
// test: el latido (contador, READY, sin número propio), el entrante SELLADO (en claro solo from y
// wa_message_id; lo sensible solo se abre con la privada de la nube y reproduce el texto, con el
// número cuando el remitente es un número y sin él cuando es un LID), el acuse en sus dos estados
// y el frame del canal de control.
func TestArnes_EdgeSaliente(t *testing.T) {
	t.Parallel()
	e, c, k := edgeDePrueba(t)

	e.latir(t, 3)
	e.entrante(t, "573001110000@s.whatsapp.net", "quiero 3 cajas de tornillos", "WA-1")
	e.entrante(t, "abc123@lid", "desde un LID", "WA-2")
	e.acuse(t, "WA-OUT-1", false)
	e.acuse(t, "WA-OUT-2", true)
	e.usarCanalControl(t)
	frames := c.esperar(t, 6)

	hb := frames[0].GetHeartbeat()
	if frames[0].GetSessionId() != "sesion-prueba" || hb.GetLeaseCounter() != 3 ||
		hb.GetInferenceReadiness() != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY ||
		hb.GetSelfPn() != "" || hb.GetState() != cloudlinkv1.SessionState_SESSION_STATE_UNSPECIFIED {
		t.Errorf("latido = %+v en %q", hb, frames[0].GetSessionId())
	}

	edgeVerificarEntrante(t, frames[1], k, "573001110000@s.whatsapp.net", "quiero 3 cajas de tornillos", "573001110000", "WA-1")
	edgeVerificarEntrante(t, frames[2], k, "abc123@lid", "desde un LID", "", "WA-2")

	for i, quiere := range []cloudlinkv1.ReceiptStatus{
		cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED, cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ,
	} {
		r := frames[3+i].GetReceipt()
		if r.GetStatus() != quiere || r.GetSessionId() != "sesion-prueba" || len(r.GetMessageIds()) != 1 ||
			r.GetMessageIds()[0] != fmt.Sprintf("WA-OUT-%d", i+1) || r.GetTimestamp() == 0 || frames[3+i].GetSessionId() != "sesion-prueba" {
			t.Errorf("acuse %d = %+v", i, r)
		}
	}
	if frames[5].GetSessionId() != edgeSesionControl || frames[5].GetPong() == nil {
		t.Errorf("canal de control: %T en la sesión %q", frames[5].GetPayload(), frames[5].GetSessionId())
	}
}

// edgeVerificarEntrante comprueba un frame de entrante sellado: lo único que viaja en claro es from,
// wa_message_id y el instante (los planos sensibles, vacíos), trae enc_payload, y abierto con la
// privada de la nube reproduce el texto y el número esperados.
func edgeVerificarEntrante(t *testing.T, f *cloudlinkv1.EdgeToCloud, k claves, de, texto, numero, waID string) {
	t.Helper()
	m := f.GetIncoming()
	if m == nil {
		t.Fatalf("el frame es %T, quería un IncomingMessage", f.GetPayload())
	}
	if m.GetFrom() != de || m.GetWaMessageId() != waID || m.GetTsUnix() == 0 {
		t.Errorf("campos en claro = from %q, wa_message_id %q, ts %d", m.GetFrom(), m.GetWaMessageId(), m.GetTsUnix())
	}
	if m.GetText() != "" || m.GetPushName() != "" || m.GetFromPn() != "" || m.GetFromLid() != "" {
		t.Errorf("lo sensible viaja en claro: text %q push_name %q from_pn %q from_lid %q",
			m.GetText(), m.GetPushName(), m.GetFromPn(), m.GetFromLid())
	}
	if len(m.GetEncPayload()) == 0 || strings.Contains(string(m.GetEncPayload()), texto) {
		t.Fatalf("enc_payload vacío o con el texto a la vista")
	}
	sp := edgeAbrirSensible(t, m.GetEncPayload(), k)
	if sp.GetText() != texto || sp.GetFromPn() != numero || sp.GetPushName() != "" || sp.GetFromLid() != "" {
		t.Errorf("SensitivePayload = %+v, quería texto %q y número %q", sp, texto, numero)
	}
}

// edgeAbrirSensible abre un enc_payload con la privada de la nube y lo interpreta como
// SensitivePayload. Falla el test (t.Fatalf) si no se abre o no se interpreta.
func edgeAbrirSensible(t *testing.T, sellado []byte, k claves) *cloudlinkv1.SensitivePayload {
	t.Helper()
	plano, err := envelope.OpenWith(k.NubePriv, sellado)
	if err != nil {
		t.Fatalf("abrir enc_payload con la privada de la nube: %v", err)
	}
	sp := &cloudlinkv1.SensitivePayload{}
	if err := proto.Unmarshal(plano, sp); err != nil {
		t.Fatalf("enc_payload abierto no es un SensitivePayload: %v", err)
	}
	return sp
}

// TestArnes_EdgeEnvioSerializado prueba que el Edge nunca llama a la salida desde dos goroutines a
// la vez (el Send de gRPC no lo admite): 16 goroutines emiten 50 frames cada una y la salida, que
// detecta el solape, no ve ninguno. Bajo -race caza además cualquier acceso sin candado.
func TestArnes_EdgeEnvioSerializado(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	var dentro, solapes, total atomic.Int64
	e.salida = func(*cloudlinkv1.EdgeToCloud) error {
		if dentro.Add(1) > 1 {
			solapes.Add(1)
		}
		total.Add(1)
		dentro.Add(-1)
		return nil
	}
	var espera sync.WaitGroup
	for range 16 {
		espera.Go(func() {
			for n := range 50 {
				if err := e.emitir(edgeLatido("s", int64(n))); err != nil {
					t.Errorf("emitir: %v", err)
					return
				}
			}
		})
	}
	espera.Wait()
	if solapes.Load() != 0 || total.Load() != 16*50 {
		t.Errorf("solapes = %d, emitidos = %d; quería 0 y %d", solapes.Load(), total.Load(), 16*50)
	}
}

// TestArnes_EdgeEsperas prueba las esperas del Edge sin servidor: el texto que ya está llega, el que
// no llega agota el tope con un error, un contexto cancelado corta la espera, esperarConfig y
// esperarLeases encuentran lo que ya está (un lease rechazado también cuenta), y edgeSondear
// devuelve falso al agotar su contexto. El Edge tiene un lease vigente: sin él no habría texto que
// esperar.
func TestArnes_EdgeEsperas(t *testing.T) {
	t.Parallel()
	e, _, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)

	if cap(e.Textos()) != edgeBuferTextos {
		t.Errorf("el canal de textos tiene capacidad %d, quería %d", cap(e.Textos()), edgeBuferTextos)
	}
	if _, err := e.recibirTexto(t.Context(), 30*time.Millisecond); err == nil {
		t.Errorf("sin texto, recibirTexto debía fallar por el tope")
	}
	cancelado, cancelar := context.WithCancel(t.Context())
	cancelar()
	if _, err := e.recibirTexto(cancelado, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("con el contexto cancelado, recibirTexto = %v, quería context.Canceled", err)
	}

	e.manejar(edgeComando("c-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_SendText{SendText: &cloudlinkv1.SendText{To: "x", Text: "y"}}
	}))
	e.manejar(edgeComando("cfg-1", "s", func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_ConfigUpdate{ConfigUpdate: &cloudlinkv1.ConfigUpdate{CommandId: "cfg-1", Kind: "jwks", Version: "v"}}
	}))
	if txt := e.esperarTexto(t, time.Second); txt.Texto != "y" {
		t.Errorf("esperarTexto = %+v", txt)
	}
	if c := e.esperarConfig(t, "jwks", time.Second); c.Version != "v" {
		t.Errorf("esperarConfig = %+v", c)
	}
	e.esperarLeases(t, 1, time.Second) // el vigente de edgeGrantLease
	e.alRecibirLease(&cloudlinkv1.LeaseUpdate{})
	e.esperarLeases(t, 2, time.Second) // y el vacío, que el Validator rechaza pero cuenta

	corto, cancelarCorto := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancelarCorto()
	if edgeSondear(corto, func() bool { return false }) {
		t.Errorf("edgeSondear devolvió verdadero con una condición siempre falsa")
	}
}

// edgeTestTenant y edgeTestID son el tenant y el id del Edge de los casos de
// TestArnes_EdgeInitialLease (los mismos que usa edgeDePrueba).
const (
	edgeTestTenant = "11111111-1111-4111-8111-111111111111"
	edgeTestID     = "edge-prueba"
)

// edgeIssuedLease es lo que devuelve Issuer.Issue o Issuer.Revoke: el LeaseUpdate y el error de
// emitirlo, tal cual, para dárselo a edgeLeaseCommand.
type edgeIssuedLease struct {
	lu  *cloudlinkv1.LeaseUpdate
	err error
}

// edgeInitialLeaseCase es un caso de TestArnes_EdgeInitialLease: los leases que recibe la conexión,
// en orden, y lo que se espera: la causa del rechazo del primero (nil si conectar lo da por
// bueno), si el Edge puede operar, si queda revocado y cuántos errores anota el núcleo.
type edgeInitialLeaseCase struct {
	name       string
	leases     []edgeIssuedLease
	cause      error
	canOperate bool
	revoked    bool
	coreErrors int
}

// TestArnes_EdgeInitialLease prueba, sin servidor, qué concluye conectar del PRIMER LeaseUpdate de
// una conexión (esperarLeaseInicial sobre un enlace que no termina): si el Validator lo rechaza
// —firma de otra clave, blob malformado— devuelve un error que envuelve errEdgeInitialLeaseRejected
// y la causa del Validator, y el Edge no puede operar; si es un lease vigente, nil y puede operar;
// si es una REVOCACIÓN, nil también (el Validator la acepta), con puedeOperar falso y revocado
// verdadero; un rechazo que no es el primero (contador viejo tras uno bueno) no cuenta; y una
// conexión nueva (reiniciarLeases) olvida el rechazo de la anterior. Leases() cuenta todos.
func TestArnes_EdgeInitialLease(t *testing.T) {
	t.Parallel()
	k := nuevasClaves(t)
	iss := edgeEmisorLease(t, k)
	foreign := edgeEmisorLease(t, nuevasClaves(t))
	issue := func(i *cllease.Issuer, counter int64) edgeIssuedLease {
		lu, err := i.Issue(edgeTestID, edgeTestTenant, time.Hour, counter)
		return edgeIssuedLease{lu, err}
	}
	revocation := func() edgeIssuedLease {
		lu, err := iss.Revoke(edgeTestID, edgeTestTenant)
		return edgeIssuedLease{lu, err}
	}
	cases := []edgeInitialLeaseCase{
		{"vigente", []edgeIssuedLease{issue(iss, 1)}, nil, true, false, 0},
		{"firma de otra clave", []edgeIssuedLease{issue(foreign, 1)}, cllease.ErrBadSignature, false, false, 1},
		{"malformado", []edgeIssuedLease{{&cloudlinkv1.LeaseUpdate{}, nil}}, cllease.ErrMalformed, false, false, 1},
		{"revocación", []edgeIssuedLease{revocation()}, nil, false, true, 0},
		{"vigente y después uno de contador viejo", []edgeIssuedLease{issue(iss, 5), issue(iss, 3)}, nil, true, false, 1},
		{"firma de otra clave y después uno vigente", []edgeIssuedLease{issue(foreign, 1), issue(iss, 2)}, cllease.ErrBadSignature, true, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := edgeCheckInitialLease(t, k, c)
			// Una conexión nueva empieza limpia: el rechazo de la anterior no la contamina.
			e.reiniciarLeases()
			if err := e.initialLeaseRejection(); err != nil || e.Leases() != 0 || e.puedeOperar() || e.revocado() {
				t.Errorf("tras reiniciarLeases: rechazo %v, leases %d, puedeOperar %v, revocado %v; quería todo a cero",
					err, e.Leases(), e.puedeOperar(), e.revocado())
			}
		})
	}
}

// edgeCheckInitialLease es el cuerpo de un caso de TestArnes_EdgeInitialLease: le da a un Edge
// nuevo los leases del caso, en orden, y comprueba lo que devuelve esperarLeaseInicial (nil, o un
// error que envuelve errEdgeInitialLeaseRejected y la causa), el estado del Validator y las cuentas
// de Leases y Errores. Devuelve el Edge, tal como quedó. Falla el test con t.Errorf por cada
// incumplimiento.
func edgeCheckInitialLease(t *testing.T, k claves, c edgeInitialLeaseCase) *edge {
	t.Helper()
	e := nuevoEdge(edgeTestTenant, edgeTestID, "sesion-prueba", k.NubePub, k.LeasePub)
	for _, l := range c.leases {
		e.manejar(edgeLeaseCommand(t, e, l.lu, l.err))
	}
	err := e.esperarLeaseInicial(t.Context(), &edgeEnlace{fin: make(chan struct{})})
	switch {
	case c.cause == nil && err != nil:
		t.Errorf("esperarLeaseInicial = %v, quería nil", err)
	case c.cause != nil && (!errors.Is(err, errEdgeInitialLeaseRejected) || !errors.Is(err, c.cause)):
		t.Errorf("esperarLeaseInicial = %v, quería un error que envuelva errEdgeInitialLeaseRejected y %v", err, c.cause)
	}
	if errors.Is(err, errEdgeLeaseNoLlego) {
		t.Errorf("un lease rechazado no es un lease que no llegó: %v", err)
	}
	if e.puedeOperar() != c.canOperate || e.revocado() != c.revoked {
		t.Errorf("puedeOperar=%v revocado=%v, quería %v y %v", e.puedeOperar(), e.revocado(), c.canOperate, c.revoked)
	}
	if e.Leases() != len(c.leases) || len(e.Errores()) != c.coreErrors {
		t.Errorf("Leases()=%d Errores()=%v, quería %d leases y %d errores", e.Leases(), e.Errores(), len(c.leases), c.coreErrors)
	}
	return e
}

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

// ---------------------------------------------------------------------------------------------
// Tests contra el servidor real
// ---------------------------------------------------------------------------------------------

// edgeRolTenantAdmin es la plantilla global del rol tenant_admin, sembrada por la migración 0015.
const edgeRolTenantAdmin = "10000000-0000-0000-0000-000000000001"

// edgeEscenario es lo que necesitan los tests del Edge contra el servidor real: el servidor, su base
// abierta, una empresa ya creada y el Context Token del staff que la creó y, si se pidió, el de una
// administradora de esa empresa.
type edgeEscenario struct {
	S          *servidor
	DB         *sql.DB
	Tenant     string
	TokenStaff string
	TokenAdmin string // vacío si no se pidió administradora
}

// edgeEscenarioNuevo arranca un servidor para el proceso dado, da de alta a un staff de plataforma
// (altaStaffPlataforma), crea la empresa slug por la puerta HTTP con su token y, si conAdmin, da de
// alta una administradora de esa empresa (edgeAltaAdminDelTenant) y canjea su token. Falla
// (t.Fatalf) si algo de eso no sale.
func edgeEscenarioNuevo(t *testing.T, proceso, slug string, conAdmin bool) edgeEscenario {
	t.Helper()
	s := arrancar(t, opcionesServidor{Proceso: proceso})
	db := s.Base.Abrir(t)
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, db, staff)
	tokenStaff := canjear(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	esc := edgeEscenario{S: s, DB: db, TokenStaff: tokenStaff, Tenant: crearTenant(t, s, tokenStaff, slug)}
	if conAdmin {
		admin := uuidAleatorio(t)
		edgeAltaAdminDelTenant(t, db, admin, esc.Tenant)
		esc.TokenAdmin = canjear(t, s, s.Identidad.TokenDe(admin, "wapp.bff"))
	}
	return esc
}

// edgeAltaAdminDelTenant deja a usuario (un UUID) como administradora de la empresa tenant: miembro
// (tenant_members) y con el rol tenant_admin acotado a esa empresa (iam_user_roles). Después, el
// canje de su Identity Token da un Context Token de esa empresa con todos los permisos de su ámbito.
// Es idempotente. Falla (t.Fatalf) si algún id no es un UUID, la empresa no existe o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: la primera administradora de una empresa no la puede dar de alta
// nadie desde dentro (las invitaciones las crea una administradora que ya exista) y la ruta del
// staff que sí lo haría (aprobar una solicitud de acceso) llama a identity-core, que el arnés no
// levanta. Es la misma situación que altaStaffPlataforma; este fixture es privado de este fichero
// y puede subir a fixtures_test.go cuando lo necesite otro proceso.
func edgeAltaAdminDelTenant(t *testing.T, db *sql.DB, usuario, tenant string) {
	t.Helper()
	for nombre, valor := range map[string]string{"el usuario": usuario, "la empresa": tenant} {
		if err := exigirUUID(nombre, valor); err != nil {
			t.Fatalf("edgeAltaAdminDelTenant: %v", err)
		}
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`, usuario, tenant); err != nil {
		t.Fatalf("edgeAltaAdminDelTenant: insertar en tenant_members: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT DO NOTHING`, usuario, edgeRolTenantAdmin, tenant); err != nil {
		t.Fatalf("edgeAltaAdminDelTenant: insertar en iam_user_roles: %v", err)
	}
}

// edgeEmitirCodigo pide un código de enrolamiento para la empresa por la puerta HTTP del staff
// (POST /admin/tenants/{id}/enrollment-codes, plazo por defecto de 24 h) y lo devuelve. Falla
// (t.Fatalf) si la respuesta no es un 201 con un código y un vencimiento futuro.
func edgeEmitirCodigo(t *testing.T, s *servidor, tokenStaff, tenant string) string {
	t.Helper()
	r := s.Admin(tokenStaff).Post(t, rutaTenants+"/"+tenant+"/enrollment-codes", nil)
	if r.Codigo != http.StatusCreated {
		t.Fatalf("emitir un código de enrolamiento: HTTP %d, quería 201\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var res struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	r.JSON(t, &res)
	if res.Code == "" || !res.ExpiresAt.After(time.Now()) {
		t.Fatalf("el código de enrolamiento llegó sin código o ya vencido: %s", recortar(r.Cuerpo))
	}
	return res.Code
}

// edgeEsperarValor sondea una consulta de una fila y una columna de texto hasta que valga quiere,
// con tope de 10 s. Una consulta sin filas cuenta como «». Falla (t.Fatalf) con el último valor
// visto si no llega a valer lo esperado.
func edgeEsperarValor(t *testing.T, db *sql.DB, quiere, descripcion, consulta string, args ...any) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), edgeTopeFila)
	defer cancelar()
	ultimo := ""
	hecho := edgeSondear(ctx, func() bool {
		var valor string
		// La consulta usa el contexto del test y no el del sondeo: al vencer el tope, la última
		// evaluación tiene que poder leer el valor real para decir qué vio.
		err := db.QueryRowContext(t.Context(), consulta, args...).Scan(&valor)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			valor = ""
		case err != nil:
			valor = "error: " + err.Error()
		}
		ultimo = valor
		return valor == quiere
	})
	if !hecho {
		t.Fatalf("%s: pasaron %s y vale %q, quería %q", descripcion, edgeTopeFila, ultimo, quiere)
	}
}

// edgeEstadoSesion es la consulta del estado de una sesión del Edge en la flota.
const edgeEstadoSesion = `SELECT state FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

// edgeLineaLog busca en el log JSON del servidor una línea con el mensaje msg cuyo campo clave de
// texto valga valor. Devuelve la línea y true, o nil y false si aún no está.
func edgeLineaLog(s *servidor, msg, clave, valor string) (map[string]any, bool) {
	for _, l := range s.LineasLog() {
		if l["msg"] != msg {
			continue
		}
		if v, ok := l[clave].(string); ok && v == valor {
			return l, true
		}
	}
	return nil, false
}

// edgeEsperarLinea espera, con tope de 10 s, a que el log del servidor tenga la línea
// edgeLineaLog(msg, clave, valor) y la devuelve. Falla (t.Fatalf) si no aparece.
func edgeEsperarLinea(t *testing.T, s *servidor, msg, clave, valor string) map[string]any {
	t.Helper()
	var linea map[string]any
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("una línea de log %q con %s=%q", msg, clave, valor), func() bool {
		var ok bool
		linea, ok = edgeLineaLog(s, msg, clave, valor)
		return ok
	})
	return linea
}

// edgeSinErrores falla el test (t.Errorf) por cada línea de nivel ERROR del log del servidor que no
// esté prevista. esperados dice cuántas líneas ERROR de cada mensaje (campo msg) puede haber como
// máximo; nil o vacío significa que no se espera ninguna.
func edgeSinErrores(t *testing.T, s *servidor, esperados map[string]int) {
	t.Helper()
	vistos := map[string]int{}
	for _, l := range s.LineasLog() {
		if l["level"] != "ERROR" {
			continue
		}
		msg, ok := l["msg"].(string)
		if !ok {
			msg = fmt.Sprint(l["msg"])
		}
		vistos[msg]++
		if vistos[msg] > esperados[msg] {
			t.Errorf("línea ERROR inesperada en el log del servidor: %v", l)
		}
	}
}

// TestArnes_EdgeEnrolaYConecta prueba el Edge de prueba contra el servidor real, de punta a punta:
// el staff crea una empresa y pide un código; el Edge se enrola (certificado, cadena, tenant, las
// dos públicas, todo coherente con las claves del servidor y con las filas de Postgres); conecta
// con mTLS y recibe su lease inicial, su renovación por el primer latido y la config inicial
// (jwks y filters); el canal de control recibe la suya sin registrar sesión; el mismo código no se
// puede canjear dos veces; un certificado de OTRA CA no pasa el handshake; y un Edge que espera
// otra clave de lease conecta pero conectarErr devuelve el rechazo del lease inicial. Necesita
// Docker.
func TestArnes_EdgeEnrolaYConecta(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_enrola", "edge-enrola", false)
	codigo := edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant)

	e := enrolar(t, esc.S, codigo)
	edgeVerificarEnrolado(t, esc, e, codigo)
	edgeVerificarCodigoRepetido(t, esc, codigo)

	e.conectar(t)
	edgeVerificarConectado(t, esc, e)
	edgeVerificarConfigsIniciales(t, esc, e)
	edgeVerificarCanalControl(t, esc, e)
	edgeVerificarCertificadoAjeno(t, esc, e)
	edgeCheckForeignLeaseKey(t, esc, e)

	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
	edgeSinErrores(t, esc.S, nil)
}

// edgeVerificarEnrolado comprueba lo que entregó el enrolamiento: el tenant de la empresa, las dos
// públicas idénticas a las del servidor, un certificado de cliente con la identidad del Edge que
// verifica contra la CA del servidor, y en Postgres el código consumido y el certificado registrado.
func edgeVerificarEnrolado(t *testing.T, esc edgeEscenario, e *edge, codigo string) {
	t.Helper()
	s := esc.S
	if e.TenantID != esc.Tenant {
		t.Errorf("tenant del Edge = %s, quería %s", e.TenantID, esc.Tenant)
	}
	if !bytes.Equal(e.CloudEncPub, s.Claves.NubePub) {
		t.Errorf("cloud_enc_pubkey no es la pública X25519 del servidor")
	}
	if !e.LeasePub.Equal(s.Claves.LeasePub) {
		t.Errorf("lease_pubkey no es la pública Ed25519 del servidor")
	}
	if e.SessionID == "" || e.SessionID == edgeSesionControl || !strings.HasPrefix(e.EdgeID, "edge-") {
		t.Errorf("identidad del Edge inesperada: edge %q sesión %q", e.EdgeID, e.SessionID)
	}
	edgeVerificarCertificadoEmitido(t, esc, e)

	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.enrollment_codes WHERE code = $1 AND tenant_id = $2::uuid AND used_at IS NOT NULL`, codigo, esc.Tenant); n != 1 {
		t.Errorf("códigos de enrolamiento consumidos = %d, quería 1", n)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1::uuid AND subject_cn = $2`, esc.Tenant, e.EdgeID); n != 1 {
		t.Errorf("certificados registrados del Edge = %d, quería 1", n)
	}
}

// edgeVerificarCertificadoEmitido comprueba el certificado de cliente que emitió el servidor:
// CommonName = id del Edge, Organization = tenant, la clave pública del CSR, y que verifica como
// certificado de cliente contra la CA de la PKI del servidor.
func edgeVerificarCertificadoEmitido(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	cert, err := x509.ParseCertificate(edgePEMPrimero(t, "CERTIFICATE", e.certPEM))
	if err != nil {
		t.Fatalf("el certificado emitido no se interpreta: %v", err)
	}
	if cert.Subject.CommonName != e.EdgeID || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != esc.Tenant {
		t.Errorf("sujeto del certificado = %v, quería CN %s y O %s", cert.Subject, e.EdgeID, esc.Tenant)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: esc.S.PKI.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("el certificado emitido no verifica contra la CA del servidor: %v", err)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&e.clave.PublicKey) {
		t.Errorf("el certificado emitido no lleva la clave pública del CSR")
	}
}

// edgeVerificarCodigoRepetido comprueba que el código ya canjeado y uno que no existe se rechazan con
// PermissionDenied, y que el rechazo no deja un Edge.
func edgeVerificarCodigoRepetido(t *testing.T, esc edgeEscenario, codigo string) {
	t.Helper()
	for nombre, c := range map[string]string{"repetido": codigo, "inexistente": "WAPP-" + edgeAleatorioHex(t, 10)} {
		e, err := enrolarErr(t, esc.S, c)
		if err == nil || e != nil || status.Code(err) != codes.PermissionDenied {
			t.Errorf("enrolar con un código %s: edge %v, error %v (código %v); quería PermissionDenied", nombre, e, err, status.Code(err))
		}
	}
}

// edgeVerificarConectado comprueba la conexión: el Edge puede operar y no está revocado, recibió el
// lease inicial y la renovación del primer latido (contadores 1 y 2) sin que el Validator rechazara
// ninguno, y Postgres tiene la sesión online y el lease con el contador 2 sin revocar.
func edgeVerificarConectado(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() || e.revocado() {
		t.Errorf("tras conectar: puedeOperar=%v revocado=%v; quería verdadero y falso", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge en la flota", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	// El servidor leyó el READY del primer latido como la transición que dispara el calentamiento.
	edgeEsperarLinea(t, esc.S, "calentamiento: el Edge acaba de decir que puede servir inferencia", "session_id", e.SessionID)
	edgeEsperarValor(t, esc.DB, "2/false", "el lease del Edge en Postgres (contador/revocado)",
		`SELECT counter::text || '/' || revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID)
}

// edgeVerificarConfigsIniciales comprueba la config que el servidor empuja al conectar: un jwks
// cuya versión es el kid de la clave de firma del servidor y cuyo contenido es un JWKS ES256 con
// ese kid, y el mapa de filters; los dos dirigidos a la sesión del Edge y con command_id.
func edgeVerificarConfigsIniciales(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	jwks := e.esperarConfig(t, "jwks", edgeTopeFila)
	if jwks.Version != esc.S.Claves.JWTKid || jwks.Sesion != e.SessionID || jwks.ComandoID == "" {
		t.Errorf("jwks = versión %q sesión %q comando %q; quería %q, %q y un command_id", jwks.Version, jwks.Sesion, jwks.ComandoID, esc.S.Claves.JWTKid, e.SessionID)
	}
	var cuerpo struct {
		Keys []struct{ Kty, Crv, Kid, Alg string } `json:"keys"`
	}
	if err := json.Unmarshal(jwks.Payload, &cuerpo); err != nil || len(cuerpo.Keys) != 1 {
		t.Fatalf("el contenido del jwks no es un JWKS de una clave: %v\n%s", err, jwks.Payload)
	}
	if k := cuerpo.Keys[0]; k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Kid != esc.S.Claves.JWTKid {
		t.Errorf("la clave del jwks = %+v, quería EC P-256 ES256 con kid %s", k, esc.S.Claves.JWTKid)
	}

	filters := e.esperarConfig(t, "filters", edgeTopeFila)
	if filters.Sesion != e.SessionID || filters.ComandoID == "" || !json.Valid(filters.Payload) {
		t.Errorf("filters = sesión %q comando %q payload válido %v", filters.Sesion, filters.ComandoID, json.Valid(filters.Payload))
	}
}

// edgeVerificarCanalControl comprueba que un frame por el canal de control hace que el servidor
// empuje la config inicial por ese mismo stream (jwks con la sesión de control) sin registrar el
// canal como sesión de flota.
func edgeVerificarCanalControl(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	e.usarCanalControl(t)
	edgeEsperar(t, edgeTopeFila, "un ConfigUpdate jwks por el canal de control", func() bool {
		return slices.ContainsFunc(e.Configs(), func(c configRecibida) bool { return c.Kind == "jwks" && c.Sesion == edgeSesionControl })
	})
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`, esc.Tenant, edgeSesionControl); n != 0 {
		t.Errorf("el canal de control quedó registrado como sesión de flota (%d filas)", n)
	}
}

// edgeVerificarCertificadoAjeno comprueba el caso negativo del mTLS: el mismo Edge con un certificado
// firmado por la CA de otra PKI no completa la conexión (conectarErr devuelve un error de transporte),
// no deja sesión en la flota, y el Edge legítimo sigue conectado.
func edgeVerificarCertificadoAjeno(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	ajeno := e.conCertificadoDe(t, nuevaPKI(t))
	err := ajeno.conectarErr(t)
	if err == nil {
		t.Fatalf("un certificado de otra CA conectó: el servidor no exige su CA")
	}
	t.Logf("conectar con el certificado de otra CA: %v", err)
	// El texto del error varía según quién gane la carrera entre la alerta TLS del servidor y el
	// reinicio de la conexión («unknown certificate authority» o «connection reset by peer»); lo
	// estable es que el canal falla al instante como Unavailable y no como un silencio.
	if errors.Is(err, errEdgeLeaseNoLlego) || status.Code(err) != codes.Unavailable || ajeno.Leases() != 0 || ajeno.puedeOperar() {
		t.Errorf("el rechazo debía ser un fallo inmediato del transporte (Unavailable), no un silencio: error %v, leases %d", err, ajeno.Leases())
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`, esc.Tenant, ajeno.SessionID); n != 0 {
		t.Errorf("el certificado de otra CA dejó %d sesiones en la flota", n)
	}
	if !e.puedeOperar() {
		t.Errorf("el Edge legítimo dejó de poder operar tras el intento ajeno")
	}
}

// edgeCheckForeignLeaseKey comprueba, contra el servidor real, que conectar NO da por buena una
// conexión cuyo lease inicial rechazó el Validator: el mismo Edge, con su certificado bueno pero
// esperando otra clave de lease, pasa el mTLS, recibe el lease inicial del servidor y conectarErr
// devuelve el rechazo (errEdgeInitialLeaseRejected y cllease.ErrBadSignature), con el Edge sin
// poder operar y el enlace cerrado; y el Edge legítimo, en su propia sesión, sigue operando.
func edgeCheckForeignLeaseKey(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	other := e.withLeasePub(t, nuevasClaves(t).LeasePub)
	err := other.conectarErr(t)
	if !errors.Is(err, errEdgeInitialLeaseRejected) || !errors.Is(err, cllease.ErrBadSignature) {
		t.Fatalf("conectar esperando otra clave de lease = %v; quería el rechazo del lease inicial por firma inválida", err)
	}
	if other.puedeOperar() || other.revocado() || other.Leases() == 0 {
		t.Errorf("tras el rechazo: puedeOperar=%v revocado=%v leases=%d; quería falso, falso y al menos 1 recibido",
			other.puedeOperar(), other.revocado(), other.Leases())
	}
	if err := other.emitir(edgeLatido(other.SessionID, edgeContadorInicial)); !errors.Is(err, errEdgeSinSalida) {
		t.Errorf("tras el rechazo el enlace debía quedar cerrado: emitir = %v, quería errEdgeSinSalida", err)
	}
	// El servidor sí llegó a registrar esa sesión (el mTLS era bueno) y la ve irse.
	edgeEsperarValor(t, esc.DB, "offline", "la sesión del Edge que rechazó su lease", edgeEstadoSesion, esc.Tenant, e.EdgeID, other.SessionID)
	if !e.puedeOperar() || len(e.Errores()) != 0 {
		t.Errorf("el Edge legítimo quedó afectado: puedeOperar=%v errores=%v", e.puedeOperar(), e.Errores())
	}
}

// TestArnes_EdgeFrames prueba, contra el servidor real, los frames del Edge que dejan un efecto que
// se puede observar: el SendText que el servidor empuja por las dos rutas de envío (llega al
// canal y el servidor recibe el Ack), el entrante sellado (el servidor lo abre, no registra el
// texto, lo deduplica), los acuses (filas de message_receipts), el diagnóstico remoto (el Edge
// recibe el DiagnosticsRequest y el bundle sale por la ruta de descarga), la renovación del lease
// por latido y el anti-replay del Validator, la desconexión (sesión offline, envío con 502) y la
// reconexión, y la revocación del lease con su efecto: tras revocar, el envío del servidor ya no se
// entrega (200 con ok=false y «lease no vigente»), tampoco tras reconectar. Necesita Docker.
//
// NO ejercita contra el servidor: los Ping (no hay ruta que los provoque), la inferencia (aquí no:
// el calentamiento y su gate de lease los recorre TestArnes_EdgeInferenceLeaseGate; la que pide la
// canalización LLM la monta T9.17), ni el camino del entrante hasta una respuesta (perfil y flujos,
// que monta P3).
func TestArnes_EdgeFrames(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_frames", "edge-frames", true)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)

	edgeProbarTextoDelServidor(t, esc, e)
	edgeProbarEntranteSellado(t, esc, e)
	edgeProbarAcuses(t, esc, e)
	edgeProbarDiagnostico(t, esc, e)
	edgeProbarRenovacionPorLatido(t, esc, e)
	edgeProbarReconexion(t, esc, e)
	edgeProbarRevocacion(t, esc, e)

	// El único error esperado es el del latido de contador viejo de edgeProbarRenovacionPorLatido.
	if errs := e.Errores(); len(errs) != 1 || !errors.Is(errs[0], cllease.ErrStaleCounter) {
		t.Errorf("errores del núcleo del Edge = %v, quería solo el ErrStaleCounter provocado", errs)
	}
	// El servidor registra como ERROR cada 502: el que provoca el envío con la sesión offline.
	edgeSinErrores(t, esc.S, map[string]int{"envío por la API pública fallido": 1})
}

// edgeProbarTextoDelServidor manda un texto por la ruta pública y otro por la de admin; cada una
// debe responder 200 con el Ack del Edge y dejar el texto en el canal con el mismo command_id.
func edgeProbarTextoDelServidor(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	rutas := []struct {
		nombre  string
		cliente *clienteHTTP
		ruta    string
	}{
		{"API pública", esc.S.Publica(esc.TokenAdmin), "/api/v1/messages"},
		{"admin", esc.S.Admin(esc.TokenAdmin), "/admin/messages/send"},
	}
	for i, r := range rutas {
		texto := fmt.Sprintf("hola desde la nube (%s)", r.nombre)
		resp := r.cliente.Post(t, r.ruta, map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": texto})
		var ack struct {
			AckedCommandID string `json:"acked_command_id"`
			OK             bool   `json:"ok"`
		}
		resp.JSON(t, &ack)
		if resp.Codigo != http.StatusOK || !ack.OK || ack.AckedCommandID == "" {
			t.Fatalf("%s: HTTP %d %+v, quería 200 con el Ack del Edge\ncuerpo: %s", r.nombre, resp.Codigo, ack, recortar(resp.Cuerpo))
		}
		txt := e.esperarTexto(t, 5*time.Second)
		if quiere := (textoRecibido{A: "573001110000", Texto: texto, ComandoID: ack.AckedCommandID}); txt != quiere {
			t.Errorf("paso %d, %s: el Edge recibió %+v, quería %+v", i, r.nombre, txt, quiere)
		}
	}
}

// edgeProbarEntranteSellado manda un entrante sellado y comprueba que el servidor lo abre (línea de
// log con el tamaño del sobre y cero bytes en claro), registra el wa_message_id en ingest_dedupe, nunca
// escribe el texto en el log y reconoce el reenvío del mismo id como duplicado.
func edgeProbarEntranteSellado(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const texto, waID = "quiero un presupuesto de tornillos galvanizados", "WA-IN-1"
	e.entrante(t, "573001110000@s.whatsapp.net", texto, waID)

	linea := edgeEsperarLinea(t, esc.S, "ingreso: enc_payload sellado abierto", "wa_message_id", waID)
	if plano, ok := linea["text_plano_en_cable_len"].(float64); !ok || plano != 0 {
		t.Errorf("text_plano_en_cable_len = %v, quería 0 (nada sensible en claro)", linea["text_plano_en_cable_len"])
	}
	if sobre, ok := linea["enc_payload_bytes"].(float64); !ok || sobre <= 0 {
		t.Errorf("enc_payload_bytes = %v, quería > 0", linea["enc_payload_bytes"])
	}
	edgeEsperarValor(t, esc.DB, "1", "el entrante en ingest_dedupe",
		`SELECT count(*)::text FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = $2`, e.SessionID, waID)

	e.entrante(t, "573001110000@s.whatsapp.net", texto, waID)
	edgeEsperarLinea(t, esc.S, "runtime: entrante duplicado ignorado (dedupe de ingesta)", "wa_message_id", waID)
	if strings.Contains(esc.S.Log(), texto) {
		t.Errorf("el texto del cliente aparece en el log del servidor")
	}
}

// edgeProbarAcuses manda un acuse de entregado y otro de leído del mismo mensaje y comprueba que
// el servidor los persiste como dos filas de message_receipts de la sesión del Edge.
func edgeProbarAcuses(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const waID = "WA-OUT-9"
	e.acuse(t, waID, false)
	e.acuse(t, waID, true)
	edgeEsperarValor(t, esc.DB, "delivered,read", "los acuses en message_receipts",
		`SELECT string_agg(status, ',' ORDER BY status) FROM public.message_receipts WHERE session_id = $1 AND message_id = $2`, e.SessionID, waID)
}

// edgeProbarDiagnostico recorre el diagnóstico remoto completo: la administradora lo pide (202), el
// Edge recibe el DiagnosticsRequest con el scope, la descarga responde «pendiente» (202) hasta que el
// Edge manda su bundle, y entonces responde 200 con la cola de log. Un bundle de un command_id que
// nadie pidió queda en el log como huérfano y no rompe el stream.
func edgeProbarDiagnostico(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	pub := esc.S.Publica(esc.TokenAdmin)
	r := pub.Post(t, "/api/v1/sessions/"+e.SessionID+"/diagnostics", map[string]string{"scope": "logs"})
	var pedido struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
	}
	r.JSON(t, &pedido)
	if r.Codigo != http.StatusAccepted || pedido.CommandID == "" || pedido.Status != "pending" {
		t.Fatalf("pedir el diagnóstico: HTTP %d %+v, quería 202 pendiente\ncuerpo: %s", r.Codigo, pedido, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "el DiagnosticsRequest en el Edge", func() bool { return len(e.Diagnosticos()) == 1 })
	if d := e.Diagnosticos()[0]; d != (diagnosticoPedido{ComandoID: pedido.CommandID, Scope: "logs", Sesion: e.SessionID}) {
		t.Errorf("el Edge recibió %+v", d)
	}
	ruta := "/api/v1/diagnostics/" + pedido.CommandID
	if antes := pub.Get(t, ruta, nil); antes.Codigo != http.StatusAccepted {
		t.Errorf("la descarga antes del bundle: HTTP %d, quería 202 (pendiente)", antes.Codigo)
	}

	e.bundle(t, pedido.CommandID, "línea de log del Edge")
	var descarga struct {
		CommandID string `json:"command_id"`
		LogTail   string `json:"log_tail"`
	}
	edgeEsperar(t, edgeTopeFila, "la descarga del bundle", func() bool {
		d := pub.Get(t, ruta, nil)
		if d.Codigo != http.StatusOK {
			return false
		}
		d.JSON(t, &descarga)
		return true
	})
	if descarga.CommandID != pedido.CommandID || descarga.LogTail != "línea de log del Edge" {
		t.Errorf("la descarga trae %+v", descarga)
	}

	e.bundle(t, "diag-que-nadie-pidio", "x")
	edgeEsperarLinea(t, esc.S, "diagnóstico: bundle sin solicitud pendiente; ignorado", "command_id", "diag-que-nadie-pidio")
}

// edgeProbarRenovacionPorLatido comprueba que un latido con contador 10 hace que el servidor renueve
// el lease con 11 (en el Edge y en Postgres) y que el Validator lo acepta; y que un latido con un
// contador viejo (4) produce un lease que el Validator rechaza con ErrStaleCounter (anti-replay),
// sin quitarle al Edge su lease vigente.
func edgeProbarRenovacionPorLatido(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	const consulta = `SELECT counter::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`
	antes := e.Leases()
	e.latir(t, 10)
	e.esperarLeases(t, antes+1, edgeTopeFila)
	edgeEsperarValor(t, esc.DB, "11", "el contador del lease tras un latido de 10", consulta, esc.Tenant, e.EdgeID)
	if len(e.Errores()) != 0 || !e.puedeOperar() {
		t.Fatalf("tras el latido de 10: errores %v, puedeOperar %v", e.Errores(), e.puedeOperar())
	}

	antes = e.Leases()
	e.latir(t, 4)
	e.esperarLeases(t, antes+1, edgeTopeFila)
	edgeEsperar(t, edgeTopeFila, "el rechazo del lease de contador viejo", func() bool {
		errs := e.Errores()
		return len(errs) == 1 && errors.Is(errs[0], cllease.ErrStaleCounter)
	})
	if !e.puedeOperar() {
		t.Errorf("el lease rechazado le quitó al Edge su lease vigente")
	}
}

// edgeProbarReconexion comprueba la caída y la vuelta: al desconectar, la sesión pasa a offline en la
// flota y el envío del servidor responde 502 (no hay stream vivo); al reconectar, la sesión vuelve a
// online, el Edge recibe un lease inicial nuevo y el envío vuelve a llegar.
func edgeProbarReconexion(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	envio := map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": "¿sigues ahí?"}

	e.desconectar(t)
	edgeEsperarValor(t, esc.DB, "offline", "la sesión tras desconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	if r := esc.S.Publica(esc.TokenAdmin).Post(t, "/api/v1/messages", envio); r.Codigo != http.StatusBadGateway {
		t.Errorf("enviar con la sesión offline: HTTP %d, quería 502\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}

	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	edgeEsperarValor(t, esc.DB, "online", "la sesión tras reconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	if !e.puedeOperar() {
		t.Errorf("tras reconectar, el Edge no puede operar")
	}
	if r := esc.S.Publica(esc.TokenAdmin).Post(t, "/api/v1/messages", envio); r.Codigo != http.StatusOK {
		t.Fatalf("enviar tras reconectar: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	if txt := e.esperarTexto(t, 5*time.Second); txt.Texto != envio["text"] {
		t.Errorf("tras reconectar el Edge recibió %+v", txt)
	}
}

// edgeProbarRevocacion comprueba el kill-switch de punta a punta: la administradora revoca el lease
// del Edge (204) y el Edge recibe la revocación firmada, queda revocado y sin poder operar, y
// Postgres lo refleja; a partir de ahí el envío del servidor NO se entrega (edgeCheckNotDelivered);
// y reconectar no lo arregla: conectar vuelve sin error —el servidor manda la revocación como
// lease inicial y el Validator la acepta—, pero el Edge sigue revocado y sin entregar.
func edgeProbarRevocacion(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	r := esc.S.Admin(esc.TokenAdmin).Post(t, "/admin/leases/revoke", map[string]string{"edge_id": e.EdgeID})
	if r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", e.revocado)
	if e.puedeOperar() {
		t.Errorf("el Edge revocado sigue pudiendo operar")
	}
	edgeEsperarValor(t, esc.DB, "true", "leases.revoked en Postgres",
		`SELECT revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID)
	edgeCheckNotDelivered(t, esc, e, "tras revocar")

	e.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reconectar el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge revocado tras reconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	edgeCheckNotDelivered(t, esc, e, "tras reconectar revocado")
}

// edgeCheckNotDelivered manda un texto por las dos rutas de envío del servidor (la pública y la de
// admin) a un Edge que no puede operar, y comprueba lo que ve quien llama: 200 con el Ack del
// Edge, ok=false y error «lease no vigente» (la API refleja el Ack, no lo convierte en un 5xx), y
// que el texto NO llegó al canal de textos. Recibe la etapa, para los mensajes. Falla el test con
// t.Errorf por cada incumplimiento.
func edgeCheckNotDelivered(t *testing.T, esc edgeEscenario, e *edge, stage string) {
	t.Helper()
	routes := []struct {
		name   string
		client *clienteHTTP
		path   string
	}{
		{"API pública", esc.S.Publica(esc.TokenAdmin), "/api/v1/messages"},
		{"admin", esc.S.Admin(esc.TokenAdmin), "/admin/messages/send"},
	}
	for _, r := range routes {
		resp := r.client.Post(t, r.path, map[string]string{"session_id": e.SessionID, "to": "573001110000", "text": "esto no debe salir"})
		var ack struct {
			AckedCommandID string `json:"acked_command_id"`
			OK             bool   `json:"ok"`
			Error          string `json:"error"`
		}
		resp.JSON(t, &ack)
		if resp.Codigo != http.StatusOK || ack.OK || ack.Error != edgeLeaseNotValidText || ack.AckedCommandID == "" {
			t.Errorf("%s, %s: HTTP %d %+v; quería 200 con ok=false y error %q\ncuerpo: %s",
				stage, r.name, resp.Codigo, ack, edgeLeaseNotValidText, recortar(resp.Cuerpo))
		}
	}
	// El Edge decide ANTES de acusar: con las respuestas ya recibidas, si hubiera publicado algo
	// estaría en el canal.
	if n := len(e.Textos()); n != 0 {
		t.Errorf("%s: hay %d textos en el canal de un Edge que no puede operar", stage, n)
	}
}

// Los dos mensajes con los que el servidor dice, en su log de nivel debug, cómo acabó un
// calentamiento de la caché de prefijo de un Edge (internal/intakeahead/calentamiento.go, calentar):
// el Edge lo sirvió, o no; en el segundo caso el campo «error» trae el motivo del gateway.
const (
	edgeLogWarmupServed    = "calentamiento: emitido contra el Edge"
	edgeLogWarmupNotServed = "calentamiento: no se emitió"
)

// edgeIntentsCatalog arma un catálogo de intenciones mínimo y válido para PUT /api/v1/intents (una
// intención con un ejemplo), con la versión dada. Dos versiones distintas dan dos catálogos
// distintos: el servidor empuja un ConfigUpdate nuevo y pide otro calentamiento.
func edgeIntentsCatalog(version string) map[string]any {
	return map[string]any{
		"version": version,
		"intents": []map[string]any{{
			"name":        "solicitud_pedido",
			"descripcion": "El cliente pide productos o un presupuesto",
			"ejemplos":    []map[string]any{{"mensaje": "quiero 3 cajas de tornillos"}},
		}},
	}
}

// edgePublishIntents publica el catálogo de la versión dada por la puerta HTTP de la administradora
// (PUT /api/v1/intents) y espera a que el Edge reciba, por su efecto, una InferenceRequest más de
// las want-1 que ya tenía: el servidor persiste el catálogo, empuja el ConfigUpdate «intents» a las
// sesiones vivas y pide UN calentamiento por Edge. Devuelve esa petición. Falla (t.Fatalf) si el PUT
// no da 200 o si la petición no llega.
func edgePublishIntents(t *testing.T, esc edgeEscenario, e *edge, version string, want int) *cloudlinkv1.InferenceRequest {
	t.Helper()
	r := esc.S.Publica(esc.TokenAdmin).Put(t, "/api/v1/intents", edgeIntentsCatalog(version))
	if r.Codigo != http.StatusOK {
		t.Fatalf("publicar el catálogo %q: HTTP %d, quería 200\ncuerpo: %s", version, r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("la InferenceRequest número %d en el Edge", want), func() bool { return len(e.Inferencias()) >= want })
	return e.Inferencias()[want-1]
}

// edgeWarmupLogLines devuelve las líneas del log del servidor con el mensaje msg (uno de los dos
// de arriba) cuya sesión es la del Edge, en orden.
func edgeWarmupLogLines(s *servidor, e *edge, msg string) []map[string]any {
	var lines []map[string]any
	for _, l := range s.LineasLog() {
		if l["msg"] == msg && l["session_id"] == e.SessionID {
			lines = append(lines, l)
		}
	}
	return lines
}

// edgeCheckWarmupRefused espera a que el servidor haya registrado want calentamientos NO servidos
// por el Edge y comprueba que el último lo fue por el lease: el campo «error» de la línea trae el
// motivo `lease_invalid` del gateway y el nombre del error del contrato,
// INFERENCE_ERROR_LEASE_INVALID. Recibe la etapa, para los mensajes. Falla el test (t.Fatalf) si las
// líneas no llegan, y con t.Errorf si el motivo es otro.
func edgeCheckWarmupRefused(t *testing.T, esc edgeEscenario, e *edge, stage string, want int) {
	t.Helper()
	var lines []map[string]any
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("%s: %d líneas %q en el log del servidor", stage, want, edgeLogWarmupNotServed), func() bool {
		lines = edgeWarmupLogLines(esc.S, e, edgeLogWarmupNotServed)
		return len(lines) >= want
	})
	reason := fmt.Sprint(lines[want-1]["error"])
	if !strings.Contains(reason, "lease_invalid") ||
		!strings.Contains(reason, cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID.String()) {
		t.Errorf("%s: el servidor dice que el calentamiento no salió por %q; quería el motivo lease_invalid (INFERENCE_ERROR_LEASE_INVALID)", stage, reason)
	}
}

// TestArnes_EdgeInferenceLeaseGate prueba contra el servidor real que el Edge de prueba obedece al
// lease también en la INFERENCIA, como el Edge real (ADR-0007: servir inferencia es operar). La
// inferencia se provoca por la única puerta que hoy no exige montar la canalización LLM: publicar
// el catálogo de intenciones, tras lo cual el servidor pide al Edge un calentamiento
// (InferenceRequest con warmup=true).
//
//  1. Con lease vigente (el control: sin él, lo de abajo pasaría en falso): el Edge recibe el
//     ConfigUpdate «intents» y el calentamiento, lo sirve, y el servidor lo registra como emitido.
//  2. Tras POST /admin/leases/revoke, otro catálogo provoca otro calentamiento: el Edge lo recibe y,
//     agotada su gracia, contesta INFERENCE_ERROR_LEASE_INVALID; el servidor registra que no salió,
//     con el motivo lease_invalid.
//  3. Reconectar no lo arregla: el servidor vuelve a pedir el calentamiento al Edge revocado, y
//     vuelve a recibir lease_invalid.
//
// En ningún momento el servidor da un calentamiento por servido después de la revocación. Necesita
// Docker.
func TestArnes_EdgeInferenceLeaseGate(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_inference_gate", "edge-inference-gate", true)
	e := enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant))
	e.conectar(t)
	// El latido de conectar ya provocó la renovación del lease y el aviso de calentamiento del
	// arranque, que sin catálogo publicado termina sin pedir nada: se espera a esa renovación para
	// que el calentamiento del primer PUT no coincida con aquel (el servidor lleva uno en vuelo por
	// Edge y descartaría el segundo).
	e.esperarLeases(t, 2, edgeTopeFila)
	if n := len(e.Inferencias()); n != 0 {
		t.Fatalf("sin catálogo publicado el Edge ya recibió %d peticiones de inferencia", n)
	}

	req := edgePublishIntents(t, esc, e, "1", 1)
	if !req.GetWarmup() || req.GetPrompt() == "" {
		t.Errorf("la petición tras publicar el catálogo: warmup=%v, prompt de %d bytes; quería un calentamiento con prompt", req.GetWarmup(), len(req.GetPrompt()))
	}
	if cfg := e.esperarConfig(t, "intents", edgeTopeFila); cfg.Sesion != e.SessionID || !json.Valid(cfg.Payload) {
		t.Errorf("el ConfigUpdate intents = sesión %q, payload válido %v", cfg.Sesion, json.Valid(cfg.Payload))
	}
	edgeEsperar(t, edgeTopeFila, "el calentamiento servido, en el log del servidor", func() bool {
		return len(edgeWarmupLogLines(esc.S, e, edgeLogWarmupServed)) == 1
	})
	if lines := edgeWarmupLogLines(esc.S, e, edgeLogWarmupNotServed); len(lines) != 0 {
		t.Fatalf("con lease vigente el servidor registró calentamientos no servidos: %v", lines)
	}

	r := esc.S.Admin(esc.TokenAdmin).Post(t, "/admin/leases/revoke", map[string]string{"edge_id": e.EdgeID})
	if r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", e.revocado)

	if req := edgePublishIntents(t, esc, e, "2", 2); !req.GetWarmup() {
		t.Errorf("la petición tras publicar el segundo catálogo no es un calentamiento: %+v", req)
	}
	edgeCheckWarmupRefused(t, esc, e, "tras revocar", 1)

	e.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reconectar el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperar(t, edgeTopeFila, "el calentamiento del arranque en el Edge reconectado", func() bool { return len(e.Inferencias()) >= 3 })
	edgeCheckWarmupRefused(t, esc, e, "tras reconectar revocado", 2)

	if lines := edgeWarmupLogLines(esc.S, e, edgeLogWarmupServed); len(lines) != 1 {
		t.Errorf("el servidor dio por servidos %d calentamientos, quería 1 (el de antes de revocar): %v", len(lines), lines)
	}
	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
	edgeSinErrores(t, esc.S, nil)
}
