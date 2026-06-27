package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrTenantNotResolved lo devuelve PostgresTenantResolver cuando la sesión no
// mapea a exactamente un tenant (0 filas o ambigüedad entre tenants). Se
// inspecciona con errors.Is.
var ErrTenantNotResolved = errors.New("no se pudo resolver tenant para la sesión")

// PostgresTenantResolver implementa TenantResolver consultando
// public.fleet_sessions (design.md §10.A). La PK de fleet es
// (tenant_id, edge_id, session_id): un mismo session_id puede aparecer bajo
// varios edge_id del MISMO tenant (se colapsa con DISTINCT tenant_id), pero si
// apareciera bajo tenants distintos la resolución es ambigua y se rechaza.
type PostgresTenantResolver struct {
	db *sql.DB
}

// NewPostgresTenantResolver construye el resolver sobre el pool dado.
func NewPostgresTenantResolver(db *sql.DB) *PostgresTenantResolver {
	return &PostgresTenantResolver{db: db}
}

// ResolveTenant devuelve el tenant_id de la sesión. Error claro si hay 0 filas
// o más de un tenant distinto (unicidad práctica del número, design.md §10.A).
func (r *PostgresTenantResolver) ResolveTenant(ctx context.Context, sessionID string) (tenantID string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT tenant_id::text
		FROM public.fleet_sessions
		WHERE session_id = $1
	`, sessionID)
	if err != nil {
		return "", fmt.Errorf("resolver tenant: consulta fleet_sessions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("resolver tenant: cerrar filas: %w", cerr)
		}
	}()

	var tenants []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", fmt.Errorf("resolver tenant: scan: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("resolver tenant: iterar filas: %w", err)
	}

	switch len(tenants) {
	case 1:
		return tenants[0], nil
	case 0:
		return "", fmt.Errorf("%w: session_id=%s (0 filas en fleet_sessions)", ErrTenantNotResolved, sessionID)
	default:
		return "", fmt.Errorf("%w: session_id=%s ambiguo (%d tenants)", ErrTenantNotResolved, sessionID, len(tenants))
	}
}
