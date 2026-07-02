package gatewaygrpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
)

// ReceiptSink es el enganche por el que el servidor entrega cada acuse de
// entrega/lectura (MessageReceipt) recibido del Edge (Plan 013 §10.F). En v1 la
// única implementación es log-only (LogReceiptSink): NO hay tabla, NO métricas,
// NO reintentos. Una fase futura podrá persistir el acuse en su propia tabla sin
// tocar el ruteo del stream, sustituyendo la implementación por otra que escriba
// en almacén.
//
// Contrato: Record NO debe bloquear el bucle Recv del stream de forma indefinida
// y NUNCA debe filtrar contenido de negocio (texto/JID/número). Recibe el
// receipt tal cual (metadatos: session_id, message_ids, status, timestamp,
// command_id).
type ReceiptSink interface {
	Record(ctx context.Context, receipt *cloudlinkv1.MessageReceipt) error
}

// LogReceiptSink es la implementación log-only de ReceiptSink (Plan 013 §10.F).
// Registra el acuse en log estructurado, con la higiene §10.G: SOLO metadatos
// (session_id, message_ids, status, timestamp, command_id). NUNCA contenido.
type LogReceiptSink struct {
	log logger.Logger
}

// NewLogReceiptSink construye el sink log-only con el logger dado.
func NewLogReceiptSink(log logger.Logger) *LogReceiptSink {
	return &LogReceiptSink{log: log}
}

// Record registra el acuse en log estructurado (sin contenido) y no falla nunca:
// es el enganche mínimo de v1 a la espera de una persistencia futura.
func (s *LogReceiptSink) Record(_ context.Context, receipt *cloudlinkv1.MessageReceipt) error {
	if s == nil || s.log == nil {
		return nil
	}
	s.log.Info("acuse persistido (log-only)",
		"session_id", receipt.GetSessionId(),
		"command_id", receipt.GetCommandId(),
		"status", receipt.GetStatus().String(),
		"message_ids", receipt.GetMessageIds(),
		"timestamp", receipt.GetTimestamp(),
	)
	return nil
}
