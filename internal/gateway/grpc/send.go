package gatewaygrpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// RevokeLease dispara el kill-switch del Edge: persiste la revocación y empuja
// el LeaseUpdate(Revoked) a TODAS sus sesiones vivas. Devuelve error si el lease
// no está configurado. El endpoint admin HTTP que lo invoca es T5.
func (s *Server) RevokeLease(ctx context.Context, tenantID, edgeID string) error {
	if s.leaseMgr == nil {
		return errors.New("gatewaygrpc: lease no configurado")
	}
	lu, err := s.leaseMgr.Revoke(ctx, tenantID, edgeID)
	if err != nil {
		return err
	}
	// Push CONCURRENTE del LeaseUpdate(Revoked) a todas las sesiones del Edge (Plan
	// 027 · Ola 1 · T5, cierra H6): cada Push ya está acotado por sendTimeout, y
	// paralelizarlos evita que una sesión bloqueada retrase la revocación en el resto
	// (el kill-switch debe llegar a TODAS cuanto antes). La revocación en el lease.
	// Manager ya está persistida; estos push son la notificación best-effort.
	var wg sync.WaitGroup
	for _, sid := range s.sessionsForEdge(tenantID, edgeID) {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if pushErr := s.registry.Push(sid, leaseToCloud(sid, lu)); pushErr != nil {
				s.log.Debug("revoke: push a sesión", "session_id", sid, "error", pushErr)
			}
		}(sid)
	}
	wg.Wait()
	return nil
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

// handleReceipt procesa un MessageReceipt (acuse de entrega/lectura) recibido del
// Edge (Plan 013 §10.F/§10.G). Correlaciona por command_id con el SendText
// original y lo entrega al receiptSink.
func (s *Server) handleReceipt(ctx context.Context, cc connCtx, receipt *cloudlinkv1.MessageReceipt) {
	if receipt == nil {
		return
	}
	s.log.Info("acuse recibido del Edge",
		"session_id", cc.sessionID,
		"command_id", receipt.GetCommandId(),
		"status", receipt.GetStatus().String(),
		"message_ids", receipt.GetMessageIds(),
		"timestamp", receipt.GetTimestamp(),
	)
	if err := s.receiptSink.Record(ctx, receipt); err != nil {
		s.log.Error("acuse: el sink no pudo registrar el receipt",
			"session_id", cc.sessionID,
			"command_id", receipt.GetCommandId(),
			"error", err,
		)
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

// SendMedia empuja un comando SendMedia (adjunto por URL prefirmada) hacia la
// sesión y espera su Ack, correlacionado por command_id — idéntico patrón a
// SendText, así el acuse delivered/read del Plan 013 funciona sin cambios. El
// binario NO viaja por gRPC: va la presignedURL (design.md §6.1) que el Edge
// descarga (GET sin credenciales) y sube a WhatsApp. kind ("document"|"image")
// elige la rama DocumentMessage/ImageMessage vía mapKind.
func (s *Server) SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error) {
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
		Payload: &cloudlinkv1.CloudToEdge_SendMedia{
			SendMedia: &cloudlinkv1.SendMedia{
				To:       to,
				Caption:  caption,
				Mime:     mime,
				Filename: filename,
				Kind:     mapKind(kind),
				Src:      &cloudlinkv1.SendMedia_PresignedUrl{PresignedUrl: presignedURL},
			},
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

// mapKind traduce el kind del descriptor (MediaRef.Kind) al enum MediaKind del
// proto. Un kind desconocido cae a UNSPECIFIED (el Edge decide el fallback);
// "document" e "image" son los soportados en 017. Se usan literales (no el paquete
// media) para no acoplar el Gateway al módulo del Motor.
func mapKind(kind string) cloudlinkv1.MediaKind {
	switch kind {
	case "document":
		return cloudlinkv1.MediaKind_MEDIA_KIND_DOCUMENT
	case "image":
		return cloudlinkv1.MediaKind_MEDIA_KIND_IMAGE
	default:
		return cloudlinkv1.MediaKind_MEDIA_KIND_UNSPECIFIED
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

// leaseToCloud envuelve un LeaseUpdate en un CloudToEdge dirigido a la sesión
// dada. No lleva command_id: es un push del servidor, no un comando con Ack.
func leaseToCloud(sessionID string, lu *cloudlinkv1.LeaseUpdate) *cloudlinkv1.CloudToEdge {
	return &cloudlinkv1.CloudToEdge{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu},
	}
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
