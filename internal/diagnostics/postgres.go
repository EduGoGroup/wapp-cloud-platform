package diagnostics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Postgres implementa Store (y BundleReceiver) con SQL raw sobre
// public.tenant_diagnostics_consent y public.diagnostics_bundles (migración 0036).
// La limpieza de bundles vencidos es PEREZOSA (al crear una solicitud y al descargar
// una vencida): no hay jobs ni goroutines de fondo (estilo del TTL del repo).
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store sobre el pool dado.
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

// ConsentEnabled implementa Store: default ON (opt-out). La AUSENCIA de fila cuenta
// como consentido (true); una fila enabled=FALSE excluye al tenant (false). Un fallo
// de infraestructura se propaga (el gate lo trata como "no verificable" ⇒ el handler
// no abre la capacidad por un error transitorio).
func (p *Postgres) ConsentEnabled(ctx context.Context, tenantID string) (bool, error) {
	var enabled bool
	err := p.db.QueryRowContext(ctx, `
		SELECT enabled
		FROM public.tenant_diagnostics_consent
		WHERE tenant_id = $1
	`, tenantID).Scan(&enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil // default ON: sin fila ⇒ consentido
	case err != nil:
		return false, fmt.Errorf("diagnostics: leer consentimiento: %w", err)
	default:
		return enabled, nil
	}
}

// CreateRequest implementa Store: inserta la solicitud pendiente y, de paso, purga
// las vencidas (limpieza perezosa, un solo DELETE acotado por el índice de expires_at).
func (p *Postgres) CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.diagnostics_bundles WHERE expires_at < now()
	`); err != nil {
		return fmt.Errorf("diagnostics: purgar vencidas: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO public.diagnostics_bundles
			(command_id, tenant_id, session_id, requested_by, requested_at, expires_at, status)
		VALUES ($1, $2, $3, $4, now(), $5, 'pending')
	`, commandID, tenantID, sessionID, requestedBy, expiresAt); err != nil {
		return fmt.Errorf("diagnostics: crear solicitud: %w", err)
	}
	return nil
}

// DeleteRequest implementa Store: borra la solicitud del tenant (rollback si el push
// del DiagnosticsRequest al Edge falla). Acota por tenant_id (INV-8).
func (p *Postgres) DeleteRequest(ctx context.Context, tenantID, commandID string) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.diagnostics_bundles WHERE tenant_id = $1 AND command_id = $2
	`, tenantID, commandID); err != nil {
		return fmt.Errorf("diagnostics: borrar solicitud: %w", err)
	}
	return nil
}

// SaveBundle implementa BundleReceiver: marca ready la solicitud PENDING que case por
// command_id + (tenant_id, session_id) de la identidad mTLS, y aún no vencida. found=
// false si el UPDATE no toca ninguna fila (bundle huérfano/expirado/mismatch ⇒ ignorar).
func (p *Postgres) SaveBundle(ctx context.Context, tenantID, sessionID, commandID string, b Bundle) (bool, error) {
	res, err := p.db.ExecContext(ctx, `
		UPDATE public.diagnostics_bundles
		SET status = 'ready',
		    received_at = now(),
		    log_tail = $1,
		    goroutine_dump = $2,
		    subsystems_json = $3
		WHERE command_id = $4
		  AND tenant_id = $5
		  AND session_id = $6
		  AND status = 'pending'
		  AND expires_at > now()
	`, b.LogTail, b.GoroutineDump, b.SubsystemsJSON, commandID, tenantID, sessionID)
	if err != nil {
		return false, fmt.Errorf("diagnostics: guardar bundle: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("diagnostics: filas afectadas: %w", err)
	}
	return n > 0, nil
}

// GetBundle implementa Store: devuelve la solicitud del tenant. ErrNotFound si no
// existe; ErrExpired (410) + borrado perezoso si venció; ErrPending (202) si el Edge
// aún no respondió.
func (p *Postgres) GetBundle(ctx context.Context, tenantID, commandID string) (Record, error) {
	var (
		rec        Record
		status     string
		expiresAt  time.Time
		receivedAt sql.NullTime
		logTail    sql.NullString
		goroutine  sql.NullString
		subsystems sql.NullString
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT session_id, requested_by, requested_at, expires_at, status,
		       received_at, log_tail, goroutine_dump, subsystems_json
		FROM public.diagnostics_bundles
		WHERE tenant_id = $1 AND command_id = $2
	`, tenantID, commandID).Scan(
		&rec.SessionID, &rec.RequestedBy, &rec.RequestedAt, &expiresAt, &status,
		&receivedAt, &logTail, &goroutine, &subsystems,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Record{}, ErrNotFound
	case err != nil:
		return Record{}, fmt.Errorf("diagnostics: leer bundle: %w", err)
	}
	if !expiresAt.After(time.Now()) {
		// Borrado perezoso de la vencida: si el DELETE falla se propaga (raro), pero el
		// camino normal deja la fila borrada y devuelve 410.
		if _, derr := p.db.ExecContext(ctx, `
			DELETE FROM public.diagnostics_bundles WHERE tenant_id = $1 AND command_id = $2
		`, tenantID, commandID); derr != nil {
			return Record{}, fmt.Errorf("diagnostics: borrar vencida: %w", derr)
		}
		return Record{}, ErrExpired
	}
	if status != "ready" {
		return Record{}, ErrPending
	}
	rec.CommandID = commandID
	rec.ReceivedAt = receivedAt.Time
	rec.Bundle = Bundle{
		LogTail:        logTail.String,
		GoroutineDump:  goroutine.String,
		SubsystemsJSON: subsystems.String,
	}
	return rec, nil
}
