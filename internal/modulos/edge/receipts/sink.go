// Porta internal/receipts/sink.go @ 8896f13

package receipts

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type Sink struct{}

// NewSink construye el sink persistente sobre store. onRecord es un callback
// opcional (p. ej. metrics.Receipt) que se invoca UNA vez por cada acuse persistido,
// con el status como texto ("delivered" o "read"); nil lo desactiva. Desacopla el
// paquete de prometheus.
func NewSink(store Store, onRecord func(status string)) *Sink {
	panic(pendiente.Implementar("receipts.NewSink"))
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
	panic(pendiente.Implementar("receipts.Sink.Record"))
}
