package gatewaygrpc

import (
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// connCtx agrupa la identidad de un stream Connect, derivada del cert mTLS.
type connCtx struct {
	sessionID string
	tenantID  string
	edgeID    string
	// hasIdentity es true solo si se extrajo (tenantID, edgeID) del cert mTLS.
	// false en streams sin TLS (tests T2): se degrada sin lease ni fleet.
	hasIdentity bool
}

// edgeKey identifica un Edge dentro de un tenant.
type edgeKey struct {
	tenantID string
	edgeID   string
}

// cloudToEdgeSender es la cara de escritura de un stream Connect: el único
// método que streamSender necesita del stream gRPC (facilita el test con un fake).
type cloudToEdgeSender interface {
	Send(*cloudlinkv1.CloudToEdge) error
}

// streamSender serializa las escrituras al stream Connect de UN Edge. grpc-go
// prohíbe SendMsg concurrente sobre el mismo stream, y un Edge multiplexa N
// sesiones sobre UN solo stream (ADR-0008): por eso el candado es POR-STREAM
// (por-Edge), no por-session_id. Connect crea UNA instancia por stream y la
// registra para TODAS sus sesiones, de modo que los Push de dos sesiones del
// mismo Edge se serializan sobre el único mutex del stream (Plan 027 · Ola 0 ·
// T3, cierra H2). Satisface session.Sender.
type streamSender struct {
	mu     sync.Mutex
	stream cloudToEdgeSender
}

func newStreamSender(stream cloudToEdgeSender) *streamSender {
	return &streamSender{stream: stream}
}

func (s *streamSender) Send(msg *cloudlinkv1.CloudToEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(msg)
}

// pendingAck es la entrada de s.acks (server.go): el canal por el que viajará el Ack
// correlacionado por command_id, y la sesión a la que se empujó ese comando. El
// sessionID no es decorativo ni informativo: es lo ÚNICO que permite a
// cancelSessionAcks (send.go) encontrar, cuando un stream cae, exactamente los envíos
// que se acaban de quedar sin nadie que los acuse (Plan 050 · Ola 2 · T2.1).
//
// 🔴 Por qué el sessionID vive DENTRO de la entrada y NO en un índice aparte. La
// alternativa natural —y la que proponía el tasks.md— es un segundo mapa
// `acksBySess map[string]map[string]struct{}`. Se descarta por una razón de clase, no
// de rendimiento: ese índice hay que retirarlo en TRES sitios (clearAck, deliverAck y
// el cierre del stream), y olvidarse de uno cualquiera lo deja creciendo con
// command_ids que ya no existen — es decir, convierte la ola que viene a demostrar
// que aquí no hay fugas en la que las introduce. Guardar el dato dentro de la entrada
// elimina la clase entera: no quedan dos estructuras que puedan desincronizarse,
// porque hay una sola cosa que retirar, en un sitio, bajo un solo mutex.
//
// El precio es un barrido O(n) del mapa al cerrar un stream, donde n son los envíos
// en vuelo de TODO el gateway —vida máxima ackTimeout, 8 s: decenas—, en un camino
// frío que ocurre una vez por caída de stream. Es irrelevante frente al presupuesto
// de 5 s del carril, y comprar con eso la imposibilidad de desincronizar dos mapas es
// un buen trato.
type pendingAck struct {
	ch        chan *cloudlinkv1.Ack
	sessionID string
}
