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

// MarkLoggedOut marca la sesión como zombie (StateLoggedOut): WhatsApp cerró el
// device (Plan 020 · T3). Como MarkOffline es un UPDATE acotado por identidad; no
// falla si la sesión no existía (UPDATE de 0 filas es válido). Se distingue del
// offline-por-red por el estado escrito, no por el camino de código.
func (r *PostgresRepository) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = 'loggedout', last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar loggedout: %w", err)
	}
	return nil
}

// SetState fija el estado (offline|loggedout) de la sesión del tenant. UPDATE
// acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca TODAS
// las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el estado antes de tocar la BD.
func (r *PostgresRepository) SetState(ctx context.Context, tenantID, sessionID string, state State) (bool, error) {
	if !ValidAdminState(state) {
		return false, ErrInvalidState
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = $3, last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(state))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar estado: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar estado: %w", err)
	}
	return n > 0, nil
}

// CountLiveBySelfPn cuenta las sesiones vivas (state != 'loggedout') del tenant con
// el self_pn dado (REQ-D4, aviso del tope de dispositivos). selfPn vacío ⇒ 0 sin
// tocar la BD.
func (r *PostgresRepository) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	if selfPn == "" {
		return 0, nil
	}
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.fleet_sessions
		WHERE tenant_id = $1 AND self_pn = $2 AND state <> 'loggedout'
	`, tenantID, selfPn).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("fleet: contar sesiones vivas por self_pn: %w", err)
	}
	return n, nil
}

// SetSelfPn persiste el self_pn reportado en el Heartbeat (Plan 020 · T2). UPDATE
// acotado por (tenant_id, edge_id, session_id). selfPn vacío es un no-op: NO
// sobrescribe un valor previo bueno (protege el dato). Un UPDATE de 0 filas
// (sesión aún sin registrar) es válido: el próximo Heartbeat lo fijará.
func (r *PostgresRepository) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if selfPn == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET self_pn = $4, updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID, selfPn)
	if err != nil {
		return fmt.Errorf("fleet: fijar self_pn: %w", err)
	}
	return nil
}

// SetRole fija el rol (bot|passive) de la sesión del tenant. UPDATE acotado por
// tenant_id + session_id (aislamiento multi-tenant, INV-8): toca TODAS las filas
// de esa sesión bajo el tenant. found=false si 0 filas (sesión inexistente o de
// otro tenant ⇒ 404 opaco). Valida el rol en el dominio antes de tocar la BD.
func (r *PostgresRepository) SetRole(ctx context.Context, tenantID, sessionID string, role Role) (bool, error) {
	if !ValidRole(role) {
		return false, ErrInvalidRole
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET role = $3, updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(role))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar rol: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar rol: %w", err)
	}
	return n > 0, nil
}

// SaveHealth persiste el snapshot de salud reportado en el Heartbeat (Plan 031 ·
// T3). UPDATE acotado por (tenant_id, edge_id, session_id): NO toca `state` (link
// CloudLink), solo las columnas de salud. degraded_since se calcula en SQL con un
// CASE que preserva el instante de entrada: al entrar en degradado usa el valor
// previo o now() (COALESCE) y al salir lo pone NULL — atómico contra el valor
// actual de la fila. Un UPDATE de 0 filas (sesión aún sin registrar) es válido.
func (r *PostgresRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET whatsapp_state       = $4,
		    degraded_reason      = $5,
		    last_event_age_s     = $6,
		    dek_load_duration_ms = $7,
		    intent_circuit       = $8,
		    outbox_depth         = $9,
		    binary_version       = $10,
		    uptime_s             = $11,
		    last_health_at       = now(),
		    degraded_since       = CASE WHEN $12 THEN COALESCE(degraded_since, now()) ELSE NULL END,
		    updated_at           = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID,
		h.WhatsappState, h.DegradedReason, h.LastEventAgeS, h.DekLoadDurationMs,
		h.IntentCircuit, h.OutboxDepth, h.BinaryVersion, h.UptimeS, h.Degraded())
	if err != nil {
		return fmt.Errorf("fleet: persistir salud: %w", err)
	}
	return nil
}

// Get devuelve la sesión, o found=false si no existe.
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	s, err := scanSession(r.db.QueryRowContext(ctx, selectSessionCols+`
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
	rows, err := r.db.QueryContext(ctx, selectSessionCols+`
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

// selectSessionCols es la lista de columnas (con COALESCE para las nullable) que
// Get y List comparten; el orden DEBE casar con scanSession. Las columnas de salud
// (Plan 031 · T3) van al final: degraded_since/last_health_at se escanean como
// NullTime (NULL ⇒ time.Time cero, que la API lee con IsZero); el resto colapsa a
// su cero con COALESCE.
const selectSessionCols = `
		SELECT tenant_id::text, edge_id, session_id, state, COALESCE(role, 'bot'),
		       COALESCE(self_pn, ''),
		       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch'),
		       COALESCE(whatsapp_state, ''), COALESCE(degraded_reason, ''),
		       degraded_since, last_health_at,
		       COALESCE(last_event_age_s, 0), COALESCE(outbox_depth, 0),
		       COALESCE(binary_version, ''), COALESCE(uptime_s, 0),
		       COALESCE(dek_load_duration_ms, 0), COALESCE(intent_circuit, '')`

// rowScanner abstrae *sql.Row y *sql.Rows para reusar el escaneo.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(sc rowScanner) (Session, error) {
	var s Session
	var state, role string
	var degradedSince, lastHealthAt sql.NullTime
	if err := sc.Scan(&s.TenantID, &s.EdgeID, &s.SessionID, &state, &role, &s.SelfPn,
		&s.LastConnectedAt, &s.LastSeenAt,
		&s.WhatsappState, &s.DegradedReason, &degradedSince, &lastHealthAt,
		&s.LastEventAgeS, &s.OutboxDepth, &s.BinaryVersion, &s.UptimeS,
		&s.DekLoadDurationMs, &s.IntentCircuit); err != nil {
		return Session{}, err
	}
	s.State = State(state)
	s.Role = Role(role)
	if degradedSince.Valid {
		s.DegradedSince = degradedSince.Time
	}
	if lastHealthAt.Valid {
		s.LastHealthAt = lastHealthAt.Time
	}
	return s, nil
}
