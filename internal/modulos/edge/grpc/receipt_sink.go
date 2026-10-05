// Porta internal/gateway/grpc/receipt_sink.go @ c851591

package grpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
)

// ReceiptSink es el enganche por el que el servidor entrega cada acuse de
// entrega/lectura (MessageReceipt) recibido del Edge (Plan 013 §10.F). La
// implementación por defecto es log-only (LogReceiptSink): NO hay tabla, NO métricas,
// NO reintentos. La que persiste (receipts.Sink) lo satisface estructuralmente, sin
// tocar el ruteo del stream.
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

// NewLogReceiptSink construye el sink log-only con el logger dado. Un logger nil
// es válido: el sink resultante no registra nada.
func NewLogReceiptSink(log logger.Logger) *LogReceiptSink {
	return &LogReceiptSink{log: log}
}

// Record registra el acuse en log estructurado (sin contenido) y no falla nunca:
// devuelve siempre nil, también sobre un receptor nil, sin logger o con un receipt
// nil. La línea es "acuse persistido (log-only)" con las claves session_id,
// command_id, status, message_ids y timestamp. No mira el ctx.
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
