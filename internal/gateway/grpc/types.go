package gatewaygrpc

import (
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// connCtx agrupa la identidad de un stream Connect, derivada del cert mTLS.
type connCtx struct {
	sessionID   string
	tenantID    string
	edgeID      string
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
