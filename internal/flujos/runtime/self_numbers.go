package runtime

import (
	"context"
	"database/sql"
	"fmt"
)

// PostgresSelfNumbers implementa SelfNumberLister leyendo los self_pn poblados de
// public.fleet_sessions para un tenant (Plan 020 · T2). Aislamiento estricto por
// tenant (INV-8): la consulta filtra por tenant_id, así los números de un tenant
// nunca cruzan a otro.
//
// Consciente del ROL (Plan 020 · T1+T2): los números de sesiones PASSIVE se
// EXCLUYEN — un passive nunca auto-responde (reactiveBlocked lo corta antes del
// motor), así que un mensaje que llega DESDE ese número no puede realimentar un
// bucle; bloquearlo solo impediría que una sesión bot atienda al número personal
// del mismo tenant. El predicado es role <> 'passive' (y no role = 'bot') a
// propósito: si mañana existe un tercer rol, sus números SIGUEN bloqueando por
// defecto (no sabemos si auto-responde ⇒ conservador hacia bloquear, misma
// convención que "rol desconocido ⇒ bot"). El rate-limit por conversación (T0)
// queda como red de contención adicional.
//
// Coste: se invoca UNA vez por entrante (dentro de la guarda anti-self-loop). Es
// una query trivial e indexada por tenant_id; para el MVP se acepta SIN caché
// (correcto siempre, sin invalidación que mantener). Si el volumen de entrantes lo
// exigiera, aquí encajaría un cache por-tenant con TTL corto (invalidación NO
// crítica: un self_pn recién reportado tarda como mucho el TTL en proteger).
type PostgresSelfNumbers struct {
	db *sql.DB
}

// NewPostgresSelfNumbers construye el lister sobre el pool dado.
func NewPostgresSelfNumbers(db *sql.DB) *PostgresSelfNumbers {
	return &PostgresSelfNumbers{db: db}
}

// SelfNumbers devuelve los self_pn no vacíos de las sesiones NO passive del
// tenant (los passive no bloquean: nunca auto-responden ⇒ sin riesgo de loop).
func (r *PostgresSelfNumbers) SelfNumbers(ctx context.Context, tenantID string) (out []string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT self_pn
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND self_pn IS NOT NULL AND self_pn <> ''
		  AND role <> 'passive'
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("self_numbers: consulta fleet_sessions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("self_numbers: cerrar filas: %w", cerr)
		}
	}()

	for rows.Next() {
		var pn string
		if err := rows.Scan(&pn); err != nil {
			return nil, fmt.Errorf("self_numbers: scan: %w", err)
		}
		out = append(out, pn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("self_numbers: iterar filas: %w", err)
	}
	return out, nil
}
