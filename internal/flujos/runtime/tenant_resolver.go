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

// ResolveTenant devuelve el tenant_id y el ROL efectivo (bot|passive, Plan 020 ·
// T1) de la sesión receptora en UNA sola consulta (evita N+1 por entrante). Error
// claro si hay 0 filas o más de un tenant distinto (unicidad práctica del número,
// design.md §10.A). El rol se agrega por tenant con bool_or(role='passive'): si
// CUALQUIER fila (edge) de la sesión bajo ese tenant está en passive, el rol
// efectivo es passive (elección CONSERVADORA anti-loop: ante un binding mixto no
// se auto-responde). Con DEFAULT 'bot' en la columna, una sesión sin configurar
// resuelve bot ⇒ no-regresión.
func (r *PostgresTenantResolver) ResolveTenant(ctx context.Context, sessionID string) (tenantID string, role string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id::text, bool_or(role = 'passive') AS any_passive
		FROM public.fleet_sessions
		WHERE session_id = $1
		GROUP BY tenant_id
	`, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("resolver tenant: consulta fleet_sessions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("resolver tenant: cerrar filas: %w", cerr)
		}
	}()

	type tenantRole struct {
		id      string
		passive bool
	}
	var found []tenantRole
	for rows.Next() {
		var tr tenantRole
		if err := rows.Scan(&tr.id, &tr.passive); err != nil {
			return "", "", fmt.Errorf("resolver tenant: scan: %w", err)
		}
		found = append(found, tr)
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("resolver tenant: iterar filas: %w", err)
	}

	switch len(found) {
	case 1:
		return found[0].id, roleString(found[0].passive), nil
	case 0:
		return "", "", fmt.Errorf("%w: session_id=%s (0 filas en fleet_sessions)", ErrTenantNotResolved, sessionID)
	default:
		return "", "", fmt.Errorf("%w: session_id=%s ambiguo (%d tenants)", ErrTenantNotResolved, sessionID, len(found))
	}
}

// roleString mapea el agregado any_passive al rol de sesión que consume el runtime.
func roleString(passive bool) string {
	if passive {
		return rolePassive
	}
	return roleBot
}
