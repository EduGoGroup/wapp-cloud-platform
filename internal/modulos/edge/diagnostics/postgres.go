// Porta internal/diagnostics/postgres.go @ 8896f13

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
//
// Son sentencias sueltas sobre el pool, SIN transacción: CreateRequest y GetBundle
// emiten dos cada una, y si la segunda falla la primera ya se aplicó.
//
// ⚠️ El vencimiento se mira con DOS relojes: la purga de CreateRequest y el filtro de
// SaveBundle usan el now() de la base; GetBundle compara expires_at con el reloj del
// PROCESO. Con los dos relojes desfasados, una solicitud puede estar vencida para uno
// y viva para el otro durante ese desfase. Se porta tal cual.
type Postgres struct {
	db *sql.DB
	// now es el reloj del proceso con el que GetBundle mira el vencimiento. Siempre es
	// time.Now (lo fija NewPostgres, y no hay opción pública que lo cambie): en el
	// fichero viejo era la llamada directa. Es un campo solo para que el test del
	// paquete pueda afirmar el borde exacto del vencimiento.
	now func() time.Time
}

// NewPostgres construye el store sobre el pool dado. No lo consulta.
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db, now: time.Now}
}

var _ Store = (*Postgres)(nil)

// ConsentEnabled implementa Store: default ON (opt-out). La AUSENCIA de fila cuenta
// como consentido (true); una fila enabled=FALSE excluye al tenant (false). Un fallo
// de infraestructura se propaga con false y el prefijo "diagnostics: leer
// consentimiento: " (el gate lo trata como "no verificable" ⇒ el handler no abre la
// capacidad por un error transitorio).
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

// CreateRequest implementa Store con DOS sentencias, en este orden y sin
// transacción: primero purga las vencidas de cualquier tenant (limpieza perezosa, un
// solo DELETE acotado por el índice de expires_at, contra el now() de la base) y
// después inserta la solicitud pendiente, con requested_at = now() de la base y
// expiresAt tal cual llega.
//
// Si la purga falla no inserta y devuelve "diagnostics: purgar vencidas: …"; si
// falla el INSERT devuelve "diagnostics: crear solicitud: …" y la purga ya se hizo
// (no hay nada que deshacer).
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
// del DiagnosticsRequest al Edge falla). Acota por tenant_id (INV-8). Un fallo sale
// como "diagnostics: borrar solicitud: …".
func (p *Postgres) DeleteRequest(ctx context.Context, tenantID, commandID string) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.diagnostics_bundles WHERE tenant_id = $1 AND command_id = $2
	`, tenantID, commandID); err != nil {
		return fmt.Errorf("diagnostics: borrar solicitud: %w", err)
	}
	return nil
}

// SaveBundle implementa BundleReceiver: marca ready la solicitud PENDING que case por
// command_id + (tenant_id, session_id) de la identidad mTLS, y aún no vencida según
// el now() de la base. found=false si el UPDATE no toca ninguna fila (bundle
// huérfano/expirado/mismatch ⇒ ignorar).
//
// Errores, con found=false: "diagnostics: guardar bundle: " (el UPDATE) y
// "diagnostics: filas afectadas: " (leer cuántas filas tocó).
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

// GetBundle implementa Store: lee la solicitud del tenant y decide, por este orden:
//
//   - sin fila ⇒ ErrNotFound;
//   - vencida según el reloj del PROCESO (expires_at no es posterior a ahora: el
//     instante exacto del vencimiento ya cuenta como vencida) ⇒ una SEGUNDA sentencia
//     la borra (borrado perezoso) y devuelve ErrExpired (410), esté pendiente o lista;
//   - viva pero sin bundle ⇒ ErrPending (202), sin borrar nada;
//   - viva y lista ⇒ el Record, con el command_id pedido; las columnas del bundle que
//     estén a NULL salen como texto vacío, y un received_at NULL, como el cero.
//
// Son dos sentencias sin transacción: si el borrado perezoso falla se devuelve
// "diagnostics: borrar vencida: …" en lugar de ErrExpired (raro; la fila sigue ahí y
// la siguiente descarga lo reintenta). Un fallo de la lectura sale como "diagnostics:
// leer bundle: …". Con cualquier error el Record vuelve vacío.
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
	if !expiresAt.After(p.now()) {
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
