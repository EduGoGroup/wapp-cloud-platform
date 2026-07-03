package receipts

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresStore implementa Store sobre public.message_receipts (migración 0022).
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore construye el store sobre el pool dado.
func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

var _ Store = (*PostgresStore)(nil)

// Save inserta el acuse de forma IDEMPOTENTE: ON CONFLICT sobre la clave única
// (session_id, message_id, status) refresca command_id/receipt_at en lugar de
// duplicar. receipt_at cero se persiste como NULL.
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

// List devuelve los acuses de una sesión, más recientes primero, paginados.
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
