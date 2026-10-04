// Porta internal/gateway/lease/repository_postgres.go @ 8896f13

package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresRepository implementa Repository con SQL raw sobre public.leases y,
// para el corte por tenant, sobre public.tenants.revoked_at.
//
// Las seis sentencias se portan BYTE A BYTE del paquete viejo (su test afirma
// el texto exacto de cada una contra constantes escritas aparte): son la mitad
// servidora de la doble llave (ADR-0007) y no se «mejoran» al portar.
//
// Deuda D-9 (reglas.md T-9, decisión D-F3-4): este paquete LEE y ESCRIBE una
// tabla que no es suya, public.tenants, que es de platform, sin API interna
// por medio. Se copia literal y se deja anotado; no se arregla aquí.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository construye el repositorio sobre el pool dado. No abre
// ni comprueba la conexión.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// Upsert inserta el lease vigente del Edge (revoked=false para una fila
// nueva) o actualiza counter/expires_at de una fila existente SIN tocar la
// columna revoked (D-055.1 · T2.1): el ON CONFLICT ya no fuerza
// revoked=false, para que Upsert no pueda resucitar un lease revocado si
// algo lo llamara directamente sobre uno. s.Revoked se ignora (contrato de
// Repository.Upsert; para revocar está MarkRevoked). En el camino de
// producción esto no cambia el comportamiento observable:
// Manager.issueAndPersist solo llama a Upsert tras comprobar (wasRevoked) que
// ni el tenant ni el Edge están revocados. Conserva issued_at en la primera
// emisión.
//
// Promesa que su test fija con el texto exacto de la sentencia: el INSERT
// escribe `false` literal en revoked (s.Revoked no viaja como argumento) y el
// SET del ON CONFLICT NO nombra la columna revoked. Un fallo del driver vuelve
// envuelto como "lease: upsert lease: …".
func (r *PostgresRepository) Upsert(ctx context.Context, s State) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)
		VALUES ($1, $2, $3, $4, false, now(), now())
		ON CONFLICT (tenant_id, edge_id) DO UPDATE
		SET counter = EXCLUDED.counter,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = now()
	`, s.TenantID, s.EdgeID, s.Counter, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("lease: upsert lease: %w", err)
	}
	return nil
}

// MarkRevoked marca el lease del Edge como revocado conservando el counter; crea
// la fila (counter=0) si aún no existía, para que el kill-switch siempre deje
// rastro. Un fallo del driver vuelve envuelto como "lease: marcar revocado: …".
func (r *PostgresRepository) MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)
		VALUES ($1, $2, 0, $3, true, now(), now())
		ON CONFLICT (tenant_id, edge_id) DO UPDATE
		SET revoked = true,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = now()
	`, tenantID, edgeID, expiresAt)
	if err != nil {
		return fmt.Errorf("lease: marcar revocado: %w", err)
	}
	return nil
}

// Get devuelve el estado del lease del Edge, o found=false si no existe
// (sql.ErrNoRows no es un error: devuelve el State cero, false y nil). Otro
// fallo vuelve envuelto como "lease: leer lease: …", con found=false.
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID string) (State, bool, error) {
	var s State
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, edge_id, counter, expires_at, revoked, issued_at, updated_at
		FROM public.leases
		WHERE tenant_id = $1 AND edge_id = $2
	`, tenantID, edgeID).Scan(
		&s.TenantID, &s.EdgeID, &s.Counter, &s.ExpiresAt, &s.Revoked, &s.IssuedAt, &s.UpdatedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return State{}, false, nil
	case err != nil:
		return State{}, false, fmt.Errorf("lease: leer lease: %w", err)
	}
	return s, true, nil
}

// TenantRevoked implementa Repository: consulta public.tenants.revoked_at
// (D-055.2, kill-switch COMERCIAL). Un tenant sin fila (no debería ocurrir en
// producción) se trata como no revocado, mismo criterio que Get con
// found=false: la ausencia de estado no es un "sí". revoked_at NULL es
// «activo»; cualquier instante, «revocado». Otro fallo vuelve envuelto como
// "lease: consultar revocación del tenant: …", con false. Lee una tabla de
// platform (D-9).
func (r *PostgresRepository) TenantRevoked(ctx context.Context, tenantID string) (bool, error) {
	var revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT revoked_at FROM public.tenants WHERE id = $1
	`, tenantID).Scan(&revokedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("lease: consultar revocación del tenant: %w", err)
	}
	return revokedAt.Valid, nil
}

// MarkTenantRevoked implementa Repository: fija tenants.revoked_at = now().
// Pegajoso hasta RestoreTenant; NO toca ninguna fila de leases (D-055.2: los
// dos sujetos de corte son independientes). Escribe una tabla de platform
// (D-9, D-F3-4: se copia literal). Un fallo vuelve envuelto como "lease: marcar
// tenant revocado: …".
func (r *PostgresRepository) MarkTenantRevoked(ctx context.Context, tenantID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.tenants SET revoked_at = now(), updated_at = now() WHERE id = $1
	`, tenantID)
	if err != nil {
		return fmt.Errorf("lease: marcar tenant revocado: %w", err)
	}
	return nil
}

// RestoreTenant implementa Repository: fija tenants.revoked_at = NULL. Escribe
// una tabla de platform (D-9, D-F3-4). Un fallo vuelve envuelto como "lease:
// restaurar tenant: …".
func (r *PostgresRepository) RestoreTenant(ctx context.Context, tenantID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.tenants SET revoked_at = NULL, updated_at = now() WHERE id = $1
	`, tenantID)
	if err != nil {
		return fmt.Errorf("lease: restaurar tenant: %w", err)
	}
	return nil
}
