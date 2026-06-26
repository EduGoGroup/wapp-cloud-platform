package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PostgresRepository implementa Repository con SQL raw sobre
// public.fleet_sessions.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository construye el repositorio sobre el pool dado.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// MarkOnline registra/actualiza la sesión como online.
func (r *PostgresRepository) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE
		SET state = 'online',
		    last_connected_at = now(),
		    last_seen_at = now(),
		    updated_at = now()
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar online: %w", err)
	}
	return nil
}

// MarkOffline marca la sesión como offline. No falla si la sesión no existía
// (UPDATE de 0 filas es válido: nunca llegó a registrarse online).
func (r *PostgresRepository) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = 'offline', last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar offline: %w", err)
	}
	return nil
}

// Get devuelve la sesión, o found=false si no existe.
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	s, err := scanSession(r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, edge_id, session_id, state,
		       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch')
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Session{}, false, nil
	case err != nil:
		return Session{}, false, fmt.Errorf("fleet: leer sesión: %w", err)
	}
	return s, true, nil
}

// List devuelve las sesiones de un tenant.
func (r *PostgresRepository) List(ctx context.Context, tenantID string) (out []Session, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id::text, edge_id, session_id, state,
		       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch')
		FROM public.fleet_sessions
		WHERE tenant_id = $1
		ORDER BY edge_id, session_id
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("fleet: listar sesiones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fleet: cerrar filas: %w", cerr)
		}
	}()

	for rows.Next() {
		s, scanErr := scanSession(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("fleet: escanear sesión: %w", scanErr)
		}
		out = append(out, s)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("fleet: iterar sesiones: %w", rowsErr)
	}
	return out, nil
}

// rowScanner abstrae *sql.Row y *sql.Rows para reusar el escaneo.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(sc rowScanner) (Session, error) {
	var s Session
	var state string
	if err := sc.Scan(&s.TenantID, &s.EdgeID, &s.SessionID, &state, &s.LastConnectedAt, &s.LastSeenAt); err != nil {
		return Session{}, err
	}
	s.State = State(state)
	return s, nil
}
