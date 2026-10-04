// Porta internal/receipts/postgres.go @ 8896f13

package receipts

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore implementa Store sobre public.message_receipts (migración 0022).
// Son dos sentencias sueltas sobre el pool, sin transacción. Las reglas del puerto
// las fija la suite receiptshelpertest.ContratoStore, que corre contra él en los
// procesos de F9; su test de fichero afirma, con un driver de mentira, el SQL que
// emite, sus argumentos y el mapeo de filas y errores.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore construye el store sobre el pool dado. No lo consulta.
func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

var _ Store = (*PostgresStore)(nil)

// Save inserta el acuse de forma IDEMPOTENTE: ON CONFLICT sobre la clave única
// (session_id, message_id, status) refresca command_id/receipt_at/recorded_at en
// lugar de duplicar. Un ReceiptAt cero se persiste como NULL; uno informado, en UTC.
// Un fallo de la base se devuelve envuelto con el prefijo "receipts: guardar acuse: ".
func (s *PostgresStore) Save(ctx context.Context, r Receipt) error {
	var receiptAt *time.Time
	if !r.ReceiptAt.IsZero() {
		t := r.ReceiptAt.UTC()
		receiptAt = &t
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO public.message_receipts (session_id, command_id, message_id, status, receipt_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, message_id, status) DO UPDATE
		SET command_id = EXCLUDED.command_id,
		    receipt_at = EXCLUDED.receipt_at,
		    recorded_at = now()
	`, r.SessionID, r.CommandID, r.MessageID, string(r.Status), receiptAt)
	if err != nil {
		return fmt.Errorf("receipts: guardar acuse: %w", err)
	}
	return nil
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
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, command_id, message_id, status,
		       COALESCE(receipt_at, 'epoch'), recorded_at
		FROM public.message_receipts
		WHERE session_id = $1
		ORDER BY recorded_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, sessionID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("receipts: listar acuses: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var out []Stored
	for rows.Next() {
		var (
			st     Stored
			status string
		)
		if serr := rows.Scan(&st.ID, &st.SessionID, &st.CommandID, &st.MessageID, &status, &st.ReceiptAt, &st.RecordedAt); serr != nil {
			return nil, fmt.Errorf("receipts: escanear acuse: %w", serr)
		}
		st.Status = Status(status)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("receipts: iterar acuses: %w", err)
	}
	return out, nil
}
