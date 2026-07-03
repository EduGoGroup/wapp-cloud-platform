package receipts

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sink implementa gatewaygrpc.ReceiptSink (estructuralmente): persiste cada
// MessageReceipt del Edge (Plan 013) en el Store, expandiendo el acuse a UNA
// fila por message_id. Reemplaza el LogReceiptSink log-only del Plan 013.
//
// Contrato (Plan 013 §10.F): Record NO bloquea el bucle Recv de forma indefinida
// y NUNCA filtra contenido de negocio (solo metadatos opacos). Un fallo de
// persistencia se propaga como error (el server lo loguea) pero NO altera el
// stream.
type Sink struct {
	store    Store
	onRecord func(status string) // hook de métrica (nil-safe); desacopla de prometheus
}

// NewSink construye el sink persistente. onRecord es un callback opcional (p. ej.
// metrics.Receipt) que se invoca por cada acuse persistido; nil lo desactiva.
func NewSink(store Store, onRecord func(status string)) *Sink {
	return &Sink{store: store, onRecord: onRecord}
}

// Record persiste el acuse. Descarta el status UNSPECIFIED (no hay nada útil que
// registrar) y expande MessageIds a una fila por id. Devuelve el primer error de
// persistencia (best-effort: intenta todos los ids).
func (s *Sink) Record(ctx context.Context, receipt *cloudlinkv1.MessageReceipt) error {
	if s == nil || receipt == nil {
		return nil
	}
	status, ok := mapStatus(receipt.GetStatus())
	if !ok {
		return nil
	}
	var at time.Time
	if ts := receipt.GetTimestamp(); ts > 0 {
		at = time.Unix(ts, 0).UTC()
	}

	var firstErr error
	for _, mid := range receipt.GetMessageIds() {
		if mid == "" {
			continue
		}
		err := s.store.Save(ctx, Receipt{
			SessionID: receipt.GetSessionId(),
			CommandID: receipt.GetCommandId(),
			MessageID: mid,
			Status:    status,
			ReceiptAt: at,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if s.onRecord != nil {
			s.onRecord(string(status))
		}
	}
	return firstErr
}

// mapStatus traduce el enum del proto a nuestro Status. ok=false para
// UNSPECIFIED (o valores desconocidos): el caller lo descarta.
func mapStatus(st cloudlinkv1.ReceiptStatus) (Status, bool) {
	switch st {
	case cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED:
		return StatusDelivered, true
	case cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ:
		return StatusRead, true
	default:
		return "", false
	}
}
