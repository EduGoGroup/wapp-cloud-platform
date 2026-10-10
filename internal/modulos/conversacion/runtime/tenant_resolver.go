// Porta internal/flujos/runtime/tenant_resolver.go @ e0159171

package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrTenantNotResolved lo devuelve PostgresTenantResolver cuando la sesión no mapea a exactamente
// un tenant: cero filas, o el mismo session_id bajo tenants distintos. Se inspecciona con
// errors.Is; quien lo devuelve le añade detrás el session_id y el porqué.
var ErrTenantNotResolved = errors.New("no se pudo resolver tenant para la sesión")

// PostgresTenantResolver implementa TenantResolver consultando public.fleet_sessions.
//
// La clave primaria de la flota es (tenant_id, edge_id, session_id): un mismo session_id puede
// aparecer bajo varios edge_id del MISMO tenant —se agrupa por tenant y cuenta como uno—, pero si
// apareciera bajo tenants distintos la resolución es ambigua y se rechaza.
//
// Solo lee: no escribe ni una fila. Y no filtra por state: una sesión offline o retirada
// (loggedout) resuelve igual que una online, porque la pregunta es «¿de quién es?», no «¿está
// viva?».
type PostgresTenantResolver struct {
	db *sql.DB
}

// NewPostgresTenantResolver construye el resolver sobre el pool dado. No toca la base ni valida
// el pool: un db nil no falla aquí sino en la primera consulta.
func NewPostgresTenantResolver(db *sql.DB) *PostgresTenantResolver {
	return &PostgresTenantResolver{db: db}
}

// ResolveTenant devuelve el tenant_id (como texto) y el PERFIL efectivo de la sesión receptora en
// UNA sola consulta (evita un N+1 por entrante).
//
// Resultados:
//
//   - exactamente un tenant tiene filas con ese session_id → (tenant_id, perfil, nil);
//   - ninguna fila → ("", "", error) que casa con ErrTenantNotResolved y cuyo texto es
//     «no se pudo resolver tenant para la sesión: session_id=<id> (0 filas en fleet_sessions)».
//     Un session_id vacío es este caso;
//   - filas bajo N > 1 tenants distintos → ("", "", error) que casa con ErrTenantNotResolved y
//     cuyo texto es «no se pudo resolver tenant para la sesión: session_id=<id> ambiguo (<N> tenants)»;
//   - fallo de la base (o contexto cancelado) → ("", "", error) envuelto con %w tras su prefijo
//     literal: «resolver tenant: consulta fleet_sessions: » si falla la consulta, «resolver
//     tenant: scan: » si no se puede leer una fila y «resolver tenant: iterar filas: » si falla el
//     recorrido. NO casa con ErrTenantNotResolved.
//
// Manda UNA sentencia, cuyo único argumento es sessionID.
//
// El perfil devuelto es SIEMPRE uno de los dos literales del motor, "active" o "passive", el
// mismo vocabulario que la columna. Hasta la 0064 se llamaba rol y hablaba bot|passive, por la
// columna legada que ya no existe.
//
// El eje que se lee es fleet_sessions.profile (Plan 046 · T1.1). El perfil se agrega por tenant
// con bool_or(profile <> 'active'): si CUALQUIER fila (edge) de la sesión bajo ese tenant no es
// activa, el perfil efectivo es pasivo (elección CONSERVADORA anti-bucle: ante un binding mixto no
// se auto-responde). Solo si TODAS sus filas son 'active' resuelve activa.
//
// El DEFAULT de la columna es cosa del esquema, no de este código, pero se nota aquí: la 0063 la
// crea con DEFAULT passive, así que una sesión NUEVA sin configurar resuelve pasiva (D-07, cambio
// deliberado: una sesión recién emparejada no auto-responde hasta que su dueño la activa).
//
// 🔴 El predicado es profile <> 'active' (y NO profile = 'passive') a propósito, por la misma
// razón que su gemelo de self_numbers.go: sobre el dominio de dos valores de la 0063 los dos son
// EQUIVALENTES, pero ante un valor desconocido —el día que el dominio crezca, que la propia 0063
// contempla— divergen en la única dirección que importa. Con `= 'passive'` un tercer perfil daría
// «ninguna pasiva» y la sesión AUTO-RESPONDERÍA; con `<> 'active'` cae a pasiva. Regla de la ola:
// ante la duda, PASIVA — un fallo hacia pasiva es una sesión que no contesta; uno hacia activa es
// un bot escribiendo a clientes que nadie autorizó. No se «normaliza» a un `=`: lo único que hoy
// hace inalcanzable ese caso es el CHECK de la 0063, no este código.
//
// ⚠️ Y NO se unifica con el predicado de PostgresSelfNumbers (profile <> 'passive'): se parecen y
// fallan hacia lados distintos (ver su comentario).
//
// D-17 (deuda que se porta tal cual): las filas se cierran con el ritual `defer rows.Close()` que
// solo informa del fallo del cierre («resolver tenant: cerrar filas: ») si no había ya otro error.
//
// El SQL lo prueba runtimehelpertest.ContratoTenantResolver contra Postgres (test/procesos).
func (r *PostgresTenantResolver) ResolveTenant(ctx context.Context, sessionID string) (tenantID string, profile string, err error) {
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

	type tenantProfile struct {
		id      string
		passive bool
	}
	var found []tenantProfile
	for rows.Next() {
		var tp tenantProfile
		if err := rows.Scan(&tp.id, &tp.passive); err != nil {
			return "", "", fmt.Errorf("resolver tenant: scan: %w", err)
		}
		found = append(found, tp)
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("resolver tenant: iterar filas: %w", err)
	}

	switch len(found) {
	case 1:
		return found[0].id, profileString(found[0].passive), nil
	case 0:
		return "", "", fmt.Errorf("%w: session_id=%s (0 filas en fleet_sessions)", ErrTenantNotResolved, sessionID)
	default:
		return "", "", fmt.Errorf("%w: session_id=%s ambiguo (%d tenants)", ErrTenantNotResolved, sessionID, len(found))
	}
}

// profileString mapea el agregado any_passive al perfil que consume el runtime.
func profileString(passive bool) string {
	if passive {
		return profilePassive
	}
	return profileActive
}
