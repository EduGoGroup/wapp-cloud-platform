// Package gatewaygrpc implementa el servidor del servicio CloudLink sobre los
// tipos generados públicos de wapp-cloudlink (gen/wapp/cloudlink/v1).
//
// El nombre del paquete es gatewaygrpc (no "grpc") deliberadamente: evita la
// colisión con el paquete google.golang.org/grpc, que se importa con su nombre
// natural.
//
// Este corte (Plan 005 · T2) implementa el núcleo del Connect EN MEMORIA:
// registro de sesiones vivas, ruteo de los EdgeToCloud por tipo, correlación de
// Acks por command_id y empuje de comandos (SendText, Ping). mTLS, lease,
// enrolamiento y persistencia entran en tareas posteriores.
package gatewaygrpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// Server implementa cloudlinkv1.CloudLinkServer. Es seguro para uso concurrente.
//
// Los hooks observables (OnIncoming, OnHeartbeat) deben asignarse antes de poner
// el servidor a servir (no se mutan mientras se atienden streams).
type Server struct {
	cloudlinkv1.UnimplementedCloudLinkServer

	registry *session.Registry
	log      logger.Logger

	// OnIncoming, si no es nil, se invoca por cada IncomingMessage recibido del
	// Edge. Lo consume la app/los tests para observar la recepción.
	OnIncoming func(sessionID string, m *cloudlinkv1.IncomingMessage)
	// OnHeartbeat, si no es nil, se invoca por cada Heartbeat recibido. El uso
	// del lease_counter para la lógica de lease entra en una tarea posterior.
	OnHeartbeat func(sessionID string, m *cloudlinkv1.Heartbeat)

	acksMu sync.Mutex
	acks   map[string]chan *cloudlinkv1.Ack
}

// New construye un Server con el registro de sesiones y el logger dados.
func New(registry *session.Registry, log logger.Logger) *Server {
	return &Server{
		registry: registry,
		log:      log,
		acks:     make(map[string]chan *cloudlinkv1.Ack),
	}
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	cloudlinkv1.RegisterCloudLinkServer(reg, s)
}

// Connect atiende el stream bidireccional CloudLink. Registra la sesión en el
// primer mensaje con session_id no vacío y la marca offline al cerrarse el
// stream (EOF o error). Rutea cada EdgeToCloud por el tipo de su payload.
func (s *Server) Connect(stream grpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]) error {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		sessionID := msg.GetSessionId()
		if release == nil && sessionID != "" {
			release = s.registry.Register(sessionID, stream)
			s.log.Info("sesión CloudLink registrada", "session_id", sessionID)
		}

		s.route(sessionID, msg)
	}
}

// route despacha un EdgeToCloud según el tipo de su payload.
func (s *Server) route(sessionID string, msg *cloudlinkv1.EdgeToCloud) {
	switch p := msg.GetPayload().(type) {
	case *cloudlinkv1.EdgeToCloud_Incoming:
		if s.OnIncoming != nil {
			s.OnIncoming(sessionID, p.Incoming)
		}
	case *cloudlinkv1.EdgeToCloud_Ack:
		s.deliverAck(p.Ack)
	case *cloudlinkv1.EdgeToCloud_Heartbeat:
		if s.OnHeartbeat != nil {
			s.OnHeartbeat(sessionID, p.Heartbeat)
		}
	case *cloudlinkv1.EdgeToCloud_Pong:
		s.log.Debug("pong recibido", "session_id", sessionID, "nonce", p.Pong.GetNonce())
	case *cloudlinkv1.EdgeToCloud_Delivery:
		s.log.Debug("delivery status recibido", "session_id", sessionID)
	default:
		s.log.Debug("payload EdgeToCloud desconocido", "session_id", sessionID)
	}
}

// deliverAck entrega un Ack al chan pendiente correlacionado por
// acked_command_id, de forma no bloqueante, y limpia la entrada.
func (s *Server) deliverAck(ack *cloudlinkv1.Ack) {
	id := ack.GetAckedCommandId()

	s.acksMu.Lock()
	ch, ok := s.acks[id]
	if ok {
		delete(s.acks, id)
	}
	s.acksMu.Unlock()

	if !ok {
		s.log.Debug("ack sin comando pendiente", "acked_command_id", id)
		return
	}

	select {
	case ch <- ack:
	default:
	}
}

// SendText empuja un comando SendText hacia la sesión dada y espera su Ack,
// correlacionado por command_id. Devuelve el Ack recibido o un error si la
// sesión está offline o si el contexto se cancela/expira antes del Ack.
func (s *Server) SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	cmdID, err := newCommandID()
	if err != nil {
		return nil, err
	}

	ch := make(chan *cloudlinkv1.Ack, 1)
	s.acksMu.Lock()
	s.acks[cmdID] = ch
	s.acksMu.Unlock()
	defer s.clearAck(cmdID)

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_SendText{
			SendText: &cloudlinkv1.SendText{To: to, Text: text},
		},
	}
	if pushErr := s.registry.Push(sessionID, msg); pushErr != nil {
		return nil, pushErr
	}

	select {
	case ack := <-ch:
		return ack, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("esperando ack de %q: %w", cmdID, ctx.Err())
	}
}

// Ping empuja un comando Ping hacia la sesión dada. No espera el Pong (mínimo
// del corte): el Pong recibido se registra en nivel debug.
func (s *Server) Ping(_ context.Context, sessionID string, nonce int64) error {
	cmdID, err := newCommandID()
	if err != nil {
		return err
	}

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_Ping{
			Ping: &cloudlinkv1.Ping{Nonce: nonce},
		},
	}
	return s.registry.Push(sessionID, msg)
}

// clearAck elimina la entrada de ack pendiente si aún existe (p.ej. tras un
// timeout sin respuesta del Edge).
func (s *Server) clearAck(cmdID string) {
	s.acksMu.Lock()
	delete(s.acks, cmdID)
	s.acksMu.Unlock()
}

// newCommandID genera un identificador único de comando con el formato UUIDv4,
// usando crypto/rand (sin dependencias externas).
func newCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generando command_id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
