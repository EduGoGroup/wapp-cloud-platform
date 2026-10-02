//go:build integracion

package procesos

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"google.golang.org/grpc"
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
