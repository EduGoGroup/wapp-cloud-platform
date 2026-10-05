// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"
	"fmt"
	"time"
)

// SetProfile fija el PERFIL (active|passive) de la sesión del tenant.
//
// 📌 Hasta la 0064 escribía además el alias legado `role` en el MISMO UPDATE. Al
// retirarse esa columna, el DROP costó exactamente lo que su comentario predijo:
// borrar SetRole y la asignación de `role` de aquí. La decisión de NO delegar el eje
// nuevo en el viejo es la que hizo que el borrado saliera barato.
//
// UPDATE acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca
// TODAS las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el perfil en el dominio antes de
// tocar la BD.
//
// 🔴 ESTA ES LA ÚNICA ESCRITURA DEL REPO QUE MUEVE `profile_updated_at` (0065), y esa
// exclusividad es TODA la garantía de que la `version` del kind:"filters" solo avanza
// cuando avanza el CONTENIDO. No la impone el motor —no hay trigger— sino esta regla:
//
//	ninguna otra escritura de fleet_sessions añade `profile_updated_at = now()`.
//	Ni MarkOnline, ni MarkOffline, ni MarkLoggedOut, ni SetState, ni SetSelfPn, ni
//	SaveHealth. Todas mueven `updated_at`, que es el reloj de la FILA, y ninguna toca
//	el del EJE.
//
// Si alguien la rompe vuelve el bug entero: MarkOnline corre justo antes de
// pushConfigsOnConnect (en el connect.go del gateway), así que un Edge con N sesiones
// del tenant publicaría N versiones nuevas con el mapa idéntico en CADA reconexión —
// y el WARN «versión anterior o igual» del Edge pasaría a ser ruido de operación
// normal, enterrando el caso real que ese log existe para delatar.
//
// Promesas que su test fija: un perfil inválido devuelve (false, ErrInvalidProfile)
// SIN emitir sentencia; 0 filas ⇒ (false, nil); un fallo del driver vuelve envuelto
// como "fleet: fijar perfil: …" y el de leer las filas afectadas como "fleet: filas
// afectadas al fijar perfil: …".
func (r *PostgresRepository) SetProfile(ctx context.Context, tenantID, sessionID string, profile Profile) (bool, error) {
	if !ValidProfile(profile) {
		return false, ErrInvalidProfile
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET profile = $3, profile_updated_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(profile))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar perfil: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar perfil: %w", err)
	}
	return n > 0, nil
}

// ProfilesByTenant devuelve la FOTO del eje `profile` de TODAS las sesiones del
// tenant más su versión (Plan 046 · T2.1). Es la única lectura que alimenta el
// kind:"filters" que se empuja al Edge.
//
// Tres decisiones viven en el SQL y no en el llamante:
//
//  1. GROUP BY session_id. La clave física es (tenant_id, edge_id, session_id): una
//     misma sesión puede tener FILA POR EDGE. El payload de filters se indexa por
//     session_id (contrato D-046.2), así que aquí se colapsa.
//  2. max(profile) para desempatar. SetProfile escribe TODAS las filas de la sesión a
//     la vez, así que discrepar es un estado que no debería existir; si existe, el
//     orden alfabético hace ganar a 'passive' sobre 'active' — que es justo la
//     lectura segura («ante la duda, no auto-responde»), la misma que ya eligió el
//     COALESCE de selectSessionCols.
//  3. La versión sale de `profile_updated_at` (0065) y NO de `updated_at`. Son dos
//     relojes distintos y la diferencia es el bug que el code review del 2026-08-21
//     encontró: `updated_at` es el reloj de la FILA y lo mueve cualquier escritura
//     —MarkOnline, el SaveHealth de CADA heartbeat, SetSelfPn—, así que la versión
//     avanzaba sin que avanzara el mapa. Con MarkOnline corriendo justo antes de
//     pushConfigsOnConnect, un Edge con N sesiones publicaba N versiones nuevas e
//     idénticas por reconexión. `profile_updated_at` lo mueve SOLO SetProfile.
//  4. La versión NO se calcula en SQL. Se escanea `profile_updated_at` como
//     time.Time y el microsegundo lo saca Go con UnixMicro(). Un
//     EXTRACT(EPOCH …)*1000000 daría el mismo número en Postgres 14+ (donde EXTRACT
//     devuelve numeric, exacto) pero en versiones anteriores devuelve double y
//     1,7e15 roza el límite de dígitos significativos del float64: el redondeo se
//     comería el microsegundo justo cuando dos cambios seguidos tienen que
//     distinguirse.
//
// Un tenant sin filas devuelve Sessions vacío (nunca nil) y Version 0, SIN error: es
// una respuesta legítima que igualmente se empuja (regla 2 de T2.1).
//
// Los errores vuelven con TenantProfiles cero y envueltos: "fleet: leer perfiles
// del tenant: …" (la consulta), "fleet: escanear perfil de sesión: …" (una fila),
// y "fleet: iterar perfiles del tenant: …" (la iteración, y también un fallo al
// cerrar las filas tras recorrerlas enteras: database/sql lo entrega por
// rows.Err(), así que la rama "fleet: cerrar filas de perfiles: …" del defer no se
// alcanza por ese camino; se porta tal cual).
func (r *PostgresRepository) ProfilesByTenant(ctx context.Context, tenantID string) (tp TenantProfiles, err error) {
	tp = TenantProfiles{Sessions: make(map[string]Profile)}
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id,
		       max(COALESCE(profile, 'passive')) AS profile,
		       max(profile_updated_at)           AS profile_updated_at
		FROM public.fleet_sessions
		WHERE tenant_id = $1
		GROUP BY session_id
		ORDER BY session_id
	`, tenantID)
	if err != nil {
		return TenantProfiles{}, fmt.Errorf("fleet: leer perfiles del tenant: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fleet: cerrar filas de perfiles: %w", cerr)
		}
	}()

	for rows.Next() {
		var sessionID, profile string
		var profileUpdatedAt time.Time
		if scanErr := rows.Scan(&sessionID, &profile, &profileUpdatedAt); scanErr != nil {
			return TenantProfiles{}, fmt.Errorf("fleet: escanear perfil de sesión: %w", scanErr)
		}
		tp.Sessions[sessionID] = Profile(profile)
		if us := profileUpdatedAt.UnixMicro(); us > tp.Version {
			tp.Version = us
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return TenantProfiles{}, fmt.Errorf("fleet: iterar perfiles del tenant: %w", rowsErr)
	}
	return tp, nil
}
