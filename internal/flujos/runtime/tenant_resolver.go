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

// ResolveTenant devuelve el tenant_id y el PERFIL efectivo de la sesión receptora
// en UNA sola consulta (evita N+1 por entrante). Error claro si hay 0 filas o más
// de un tenant distinto (unicidad práctica del número, design.md §10.A).
//
// El eje que se lee es fleet_sessions.profile (Plan 046 · T1.1), NO la columna
// legada role: role sobrevive un ciclo como alias deprecado y sincronizado en
// escritura, pero ningún camino de negocio decide ya con él (D-046.1). El perfil se
// agrega por tenant con bool_or(profile <> 'active'): si CUALQUIER fila (edge) de la
// sesión bajo ese tenant no es activa, el perfil efectivo es pasivo (elección
// CONSERVADORA anti-loop: ante un binding mixto no se auto-responde). Semántica
// idéntica a la que la 0025 daba sobre role, salvo por el DEFAULT: la 0063 crea la
// columna con DEFAULT passive, así que una sesión NUEVA sin configurar ya no
// resuelve activa (D-07, cambio deliberado); las que existían conservan su
// comportamiento por el backfill de esa misma migración.
//
// 🔴 El predicado es profile <> 'active' (y NO profile = 'passive') a propósito, por
// la misma razón que su gemelo de self_numbers.go: sobre el dominio de dos valores
// de la 0063 los dos son EQUIVALENTES, pero ante un valor desconocido —el día que
// el dominio crezca, que la propia 0063 contempla— divergen en la única dirección
// que importa. Con `= 'passive'` un tercer perfil daría any_passive=false y la
// sesión AUTO-RESPONDERÍA; con `<> 'active'` cae a pasiva. Regla de la ola: ante la
// duda, PASIVA — un fallo hacia pasiva es una sesión que no contesta; uno hacia
// activa es un bot escribiendo a clientes que nadie autorizó. No lo "normalices" a
// un `=`: lo único que hoy hace inalcanzable ese caso es el CHECK de la 0063, no
// este código.
//
// El valor de retorno sigue llamándose role y sigue hablando el vocabulario
// bot|passive del runtime: es el CONTRATO de esta interfaz, y muere con el DROP de
// la columna, no aquí (ver las constantes de runtime.go).
func (r *PostgresTenantResolver) ResolveTenant(ctx context.Context, sessionID string) (tenantID string, role string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id::text, bool_or(profile <> 'active') AS any_passive
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
