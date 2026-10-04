// Porta internal/receipts/sink.go @ 8896f13

package receipts

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sink implementa el ReceiptSink del gateway gRPC (estructuralmente: por eso la
// firma de Record no se toca): persiste cada MessageReceipt del Edge (Plan 013) en
// el Store, expandiendo el acuse a UNA fila por message_id. Reemplaza el
// LogReceiptSink log-only del Plan 013.
//
// Contrato (Plan 013 §10.F): Record NO bloquea el bucle Recv de forma indefinida
// y NUNCA filtra contenido de negocio (solo metadatos opacos). Un fallo de
// persistencia se propaga como error (el server lo loguea) pero NO altera el
// stream.
type Sink struct {
	store    Store
	onRecord func(status string) // hook de métrica (nil-safe); desacopla de prometheus
}

// NewSink construye el sink persistente sobre store. onRecord es un callback
// opcional (p. ej. metrics.Receipt) que se invoca UNA vez por cada acuse persistido,
// con el status como texto ("delivered" o "read"); nil lo desactiva. Desacopla el
// paquete de prometheus.
func NewSink(store Store, onRecord func(status string)) *Sink {
	return &Sink{store: store, onRecord: onRecord}
}

// Record persiste el acuse. Promete:
//
//   - una llamada a Store.Save por cada message_id del acuse, en su orden, con la
//     sesión, el command_id y el status del acuse (DELIVERED → StatusDelivered, READ →
//     StatusRead);
//   - un status UNSPECIFIED, o uno que este código no conoce, se descarta entero: no
//     se persiste nada, no se cuenta ninguna métrica y devuelve nil (no hay nada útil
//     que registrar);
//   - el instante del acuse es time.Unix(timestamp, 0) en UTC; un timestamp <= 0 es
//     «no informado» y deja ReceiptAt en cero;
//   - un message_id vacío se salta: ni se guarda ni se cuenta;
//   - onRecord se llama una vez por fila GUARDADA, nunca por una que falló;
//   - best-effort: si un Save falla, sigue con los demás message_id y devuelve el
//     PRIMER error, tal cual lo dio el Store;
//   - nil-safe: un *Sink nil o un acuse nil no hacen nada y devuelven nil.
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
