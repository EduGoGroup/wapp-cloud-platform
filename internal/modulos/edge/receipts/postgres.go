// Porta internal/receipts/postgres.go @ 8896f13

package receipts

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresStore implementa Store sobre public.message_receipts (migración 0022).
// Son dos sentencias sueltas sobre el pool, sin transacción. Las reglas del puerto
// las fija la suite receiptshelpertest.ContratoStore, que corre contra él en los
// procesos de F9; su test de fichero afirma, con un driver de mentira, el SQL que
// emite, sus argumentos y el mapeo de filas y errores.
type PostgresStore struct{}

// NewPostgresStore construye el store sobre el pool dado. No lo consulta.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	panic(pendiente.Implementar("receipts.NewPostgresStore"))
}

var _ Store = (*PostgresStore)(nil)

// Save inserta el acuse de forma IDEMPOTENTE: ON CONFLICT sobre la clave única
// (session_id, message_id, status) refresca command_id/receipt_at/recorded_at en
// lugar de duplicar. Un ReceiptAt cero se persiste como NULL; uno informado, en UTC.
// Un fallo de la base se devuelve envuelto con el prefijo "receipts: guardar acuse: ".
func (s *PostgresStore) Save(ctx context.Context, r Receipt) error {
	panic(pendiente.Implementar("receipts.PostgresStore.Save"))
}

// List devuelve los acuses de una sesión, más recientes primero (recorded_at DESC,
// id DESC), paginados: limit <= 0 vale 100 y offset < 0 vale 0. Sin filas devuelve
// una lista nil sin error.
//
// ⚠️ Un receipt_at NULL se lee como la época Unix (COALESCE(receipt_at, 'epoch')),
// no como el time.Time cero: quien lo guardó «no informado» lo recibe como
// 1970-01-01. El doble en memoria devuelve el cero; la suite no afirma ese caso.
//
// Errores, envueltos: "receipts: listar acuses: " (la consulta), "receipts: escanear
// acuse: " (una fila) y "receipts: iterar acuses: " (el final de la iteración).
func (s *PostgresStore) List(ctx context.Context, sessionID string, limit, offset int) ([]Stored, error) {
	panic(pendiente.Implementar("receipts.PostgresStore.List"))
}
