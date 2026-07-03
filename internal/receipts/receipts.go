// Package receipts persiste los acuses de entrega/lectura (MessageReceipt del
// Plan 013) que el Edge emite por CloudLink. Hasta el Plan 018 el acuse era
// log-only (gatewaygrpc.LogReceiptSink); T10 (R11) añade una tabla idempotente
// (message_receipts, migración 0022) y un Sink que la puebla sin tocar el ruteo
// del stream.
//
// REGLA DURA (INV-5, zero-knowledge): CERO PII. session_id, command_id y
// message_id son metadatos OPACOS del transporte (whatsmeow/CloudLink); NUNCA el
// número/JID del contacto ni el contenido del mensaje. El acuse NO porta tenant:
// el aislamiento operativo es por session_id (la sesión ya pertenece a un tenant
// en fleet_sessions).
package receipts

import (
	"context"
	"time"
)

// Status es el desenlace de un acuse. Solo delivered/read se persisten (el
// UNSPECIFIED del proto se descarta en el Sink).
type Status string

const (
	// StatusDelivered es el ✓✓ de entrega.
	StatusDelivered Status = "delivered"
	// StatusRead es el ✓✓ azul de lectura.
	StatusRead Status = "read"
)

// Receipt es un acuse persistible: metadatos OPACOS del transporte. Un acuse del
// Edge puede referir varios MessageID; el Sink lo expande a UN Receipt por
// message_id (una fila por (session_id, message_id, status)).
type Receipt struct {
	// SessionID es la sesión del Edge que emitió el acuse (metadato opaco).
	SessionID string
	// CommandID correlaciona con el SendText original (opaco); "" si no viene.
	CommandID string
	// MessageID es el MessageID acusado (metadato de whatsmeow, NO PII).
	MessageID string
	// Status es delivered o read.
	Status Status
	// ReceiptAt es el instante del acuse reportado por el Edge; cero = no informado.
	ReceiptAt time.Time
}

// Stored es un Receipt ya persistido (incluye la marca de la nube y el id).
type Stored struct {
	Receipt
	ID         int64
	RecordedAt time.Time
}

// Store persiste y consulta acuses. Save es IDEMPOTENTE: el mismo acuse (misma
// sesión + message_id + status) repetido NO crea filas nuevas (refresca el
// timestamp). List devuelve los acuses de una sesión, más recientes primero.
type Store interface {
	Save(ctx context.Context, r Receipt) error
	List(ctx context.Context, sessionID string, limit, offset int) ([]Stored, error)
}
