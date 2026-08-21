package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
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

// SetRole fija el rol (bot|passive) de la sesión del tenant Y, en el MISMO UPDATE,
// el perfil equivalente (active|passive, Plan 046 · T1.1 · D-046.1). Las dos
// columnas viajan juntas porque `role` se conserva un ciclo como alias deprecado:
// escribirlas en dos sentencias abriría una ventana —por corta que sea— en la que
// la lectura de negocio (que ya mira SOLO `profile`) y el alias legado se
// contradicen. La traducción es la del backfill de la 0063: bot⇒active, resto⇒passive.
//
// UPDATE acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca
// TODAS las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el rol en el dominio antes de
// tocar la BD.
func (r *PostgresRepository) SetRole(ctx context.Context, tenantID, sessionID string, role Role) (bool, error) {
	if !ValidRole(role) {
		return false, ErrInvalidRole
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET role = $3, profile = $4, updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(role), string(ProfileForRole(role)))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar rol: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar rol: %w", err)
	}
	return n > 0, nil
}

// SetProfile fija el PERFIL de la sesión del tenant Y, en el MISMO UPDATE, el rol
// legado equivalente (Plan 046 · T1.2 · D-046.1). Es SetRole por el eje contrario y
// con la misma razón: mientras `role` viva como alias deprecado, escribir las dos
// columnas en dos sentencias abriría una ventana en la que se contradicen. La
// traducción es la inversa del backfill de la 0063: active⇒bot, resto⇒passive.
//
// 🔴 NO delega en SetRole: la delegación haría que el eje NUEVO dependa del viejo, y
// el DROP de `role` obligaría a reescribir este camino. Así, el DROP solo borra
// SetRole y la asignación de `role` de aquí.
//
// UPDATE acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca
// TODAS las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el perfil en el dominio antes de
// tocar la BD.
func (r *PostgresRepository) SetProfile(ctx context.Context, tenantID, sessionID string, profile Profile) (bool, error) {
	if !ValidProfile(profile) {
		return false, ErrInvalidProfile
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET profile = $3, role = $4, updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(profile), string(RoleForProfile(profile)))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar perfil: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar perfil: %w", err)
	}
	return n > 0, nil
}

// SaveHealth persiste el snapshot de salud reportado en el Heartbeat (Plan 031 ·
// T3). UPDATE acotado por (tenant_id, edge_id, session_id): NO toca `state` (link
// CloudLink), solo las columnas de salud. degraded_since se calcula en SQL con un
// CASE que preserva el instante de entrada: al entrar en degradado usa el valor
// previo o now() (COALESCE) y al salir lo pone NULL — atómico contra el valor
// actual de la fila. Un UPDATE de 0 filas (sesión aún sin registrar) es válido.
// El bloque del WORKER (Plan 051 · T4.3, campos 9-15) se escribe en columnas
// NULLABLE: un puntero nil / un texto vacío / un mapa vacío se persisten como NULL
// («este Edge no lo sabe»), NUNCA como cero. Y se escriben SIEMPRE, también cuando
// son NULL: un snapshot que dejó de saber el taskset debe BORRAR el valor previo,
// porque conservar un "disjunta" viejo es publicar una salud inventada.
func (r *PostgresRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	// El desglose de motivos va al JSONB tal cual, SIN sumar nada (INV-051.3). Un
	// mapa nil o vacío se queda como interfaz nil ⇒ NULL, y no como '{}': el
	// contrato solo envía las claves con valor distinto de cero, así que un Edge
	// nuevo SIN omisiones y un Edge viejo llegan indistinguibles — y ante la duda
	// la lectura honesta es «no lo sé», no «cero omisiones».
	var omitted any
	if len(h.IntentOmittedByReason) > 0 {
		raw, merr := json.Marshal(h.IntentOmittedByReason)
		if merr != nil {
			return fmt.Errorf("fleet: serializar desglose de motivos: %w", merr)
		}
		omitted = raw
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET whatsapp_state           = $4,
		    degraded_reason          = $5,
		    last_event_age_s         = $6,
		    dek_load_duration_ms     = $7,
		    intent_circuit           = $8,
		    outbox_depth             = $9,
		    binary_version           = $10,
		    uptime_s                 = $11,
		    last_health_at           = now(),
		    degraded_since           = CASE WHEN $12 THEN COALESCE(degraded_since, now()) ELSE NULL END,
		    worker_taskset           = $13,
		    intent_p50_ms            = $14,
		    intent_omitted_by_reason = $15,
		    stuck_heads              = $16,
		    stuck_head_polls         = $17,
		    failed_seal_dispatch     = $18,
		    failed_seal_budget       = $19,
		    updated_at               = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID,
		h.WhatsappState, h.DegradedReason, h.LastEventAgeS, h.DekLoadDurationMs,
		h.IntentCircuit, h.OutboxDepth, h.BinaryVersion, h.UptimeS, h.Degraded(),
		nullText(h.WorkerTaskset), nullInt64(h.IntentP50Ms), omitted,
		nullInt64(h.StuckHeads), nullInt64(h.StuckHeadPolls),
		nullInt64(h.FailedSealDispatch), nullInt64(h.FailedSealBudget))
	if err != nil {
		return fmt.Errorf("fleet: persistir salud: %w", err)
	}
	return nil
}

// nullText mapea el texto vacío a NULL: en las columnas de salud del worker,
// vacío significa «no lo sé» y NULL es su representación en el esquema.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt64 desreferencia el puntero a un valor que el driver entiende, o nil
// (NULL) si no hay dato. Se desreferencia a mano y no se pasa el *int64 crudo
// para no depender de cómo cada driver trate los punteros.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
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
//
// 🔴 El bloque del WORKER (Plan 051 · T4.3) va SIN COALESCE a propósito: colapsar
// su NULL a 0 borraría la distinción entre «no medible» y «cero», que es
// justamente la información que estas columnas existen para conservar. Se escanean
// como sql.NullInt64 / []byte y se traducen a punteros y mapa nil-able.
//
// `profile` (Plan 046 · T1.1) viaja al lado de `role`: es el eje de NEGOCIO y el
// único con el que se decide, mientras `role` sigue publicándose como alias
// deprecado hasta su DROP. Su COALESCE es defensivo (la 0063 la deja NOT NULL) y
// cae a 'passive', NUNCA a 'active': si algún día faltara el dato, la lectura
// segura es «no auto-responde».
const selectSessionCols = `
		SELECT tenant_id::text, edge_id, session_id, state, COALESCE(role, 'bot'),
		       COALESCE(profile, 'passive'),
		       COALESCE(self_pn, ''),
		       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch'),
		       COALESCE(whatsapp_state, ''), COALESCE(degraded_reason, ''),
		       degraded_since, last_health_at,
		       COALESCE(last_event_age_s, 0), COALESCE(outbox_depth, 0),
		       COALESCE(binary_version, ''), COALESCE(uptime_s, 0),
		       COALESCE(dek_load_duration_ms, 0), COALESCE(intent_circuit, ''),
		       COALESCE(worker_taskset, ''), intent_p50_ms, intent_omitted_by_reason,
		       stuck_heads, stuck_head_polls, failed_seal_dispatch, failed_seal_budget`

// rowScanner abstrae *sql.Row y *sql.Rows para reusar el escaneo.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(sc rowScanner) (Session, error) {
	var s Session
	var state, role, profile string
	var degradedSince, lastHealthAt sql.NullTime
	var p50, stuckHeads, stuckPolls, sealDispatch, sealBudget sql.NullInt64
	var omittedRaw []byte
	if err := sc.Scan(&s.TenantID, &s.EdgeID, &s.SessionID, &state, &role, &profile, &s.SelfPn,
		&s.LastConnectedAt, &s.LastSeenAt,
		&s.WhatsappState, &s.DegradedReason, &degradedSince, &lastHealthAt,
		&s.LastEventAgeS, &s.OutboxDepth, &s.BinaryVersion, &s.UptimeS,
		&s.DekLoadDurationMs, &s.IntentCircuit,
		&s.WorkerTaskset, &p50, &omittedRaw,
		&stuckHeads, &stuckPolls, &sealDispatch, &sealBudget); err != nil {
		return Session{}, err
	}
	s.State = State(state)
	s.Role = Role(role)
	s.Profile = Profile(profile)
	if degradedSince.Valid {
		s.DegradedSince = degradedSince.Time
	}
	if lastHealthAt.Valid {
		s.LastHealthAt = lastHealthAt.Time
	}
	// Bloque del worker (Plan 051 · T4.3): NULL ⇒ puntero nil («no lo sé»), nunca 0.
	s.IntentP50Ms = int64Ptr(p50)
	s.StuckHeads = int64Ptr(stuckHeads)
	s.StuckHeadPolls = int64Ptr(stuckPolls)
	s.FailedSealDispatch = int64Ptr(sealDispatch)
	s.FailedSealBudget = int64Ptr(sealBudget)
	if len(omittedRaw) > 0 {
		if err := json.Unmarshal(omittedRaw, &s.IntentOmittedByReason); err != nil {
			return Session{}, fmt.Errorf("fleet: deserializar desglose de motivos: %w", err)
		}
	}
	return s, nil
}

// int64Ptr convierte un sql.NullInt64 en *int64: NULL ⇒ nil («no lo sé»), nunca 0.
func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}
