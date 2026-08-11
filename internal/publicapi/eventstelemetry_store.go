package publicapi

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresEventTelemetryStore satisface EventTelemetryReader con SQL directo
// contra la tabla PÚBLICA public.flow_events (Plan 043 · Ola 6 · T6.5).
//
// Vive AQUÍ, en internal/publicapi, y NO en internal/flujos/events —dueño
// físico natural de la tabla— por la frontera de propiedad de ESTA tarea: T6.5
// tiene la exclusiva de internal/publicapi/**, internal/platform/metrics/** e
// internal/bootstrap/** mientras OTRO agente edita internal/flujos/** al mismo
// tiempo (Ola 6 del Plan 043, dos agentes en paralelo; tocar flujos/events
// aquí habría sido editar el paquete del otro agente a mitad de su cambio, y
// las instrucciones de esta tarea lo prohíben explícitamente). Es una
// excepción de ESTA tarea, no un precedente de arquitectura para publicapi en
// general: es SQL de solo LECTURA sobre una tabla append-only ya pública, no
// un segundo dueño del dominio de flujos — no escribe una sola fila, no
// interpreta el ciclo de vida del evento (eso sigue siendo de
// internal/flujos/runtime), y usa exactamente el vocabulario que
// event_effects.go ya fija (payload->>'kind', nunca la columna kind).
type PostgresEventTelemetryStore struct {
	db *sql.DB
}

// NewPostgresEventTelemetryStore construye el store sobre el *sql.DB YA
// abierto que bootstrap.go comparte con el resto de la plataforma (mismo pool,
// no una segunda conexión).
func NewPostgresEventTelemetryStore(db *sql.DB) *PostgresEventTelemetryStore {
	return &PostgresEventTelemetryStore{db: db}
}

// eventTelemetryQuery es la consulta MEDIDA y ENMENDADA (T6.5, refutación
// 2026-08-10/11) tras las CUATRO correcciones exigidas:
//
//  1. `name LIKE 'event\_%'` con el guion bajo ESCAPADO — sin escapar, `_` es
//     comodín de un carácter y contamina el resultado con filas que no
//     empiezan literalmente por "event_" (medido: 5% de contaminación).
//  2. El predicado es LITERAL, carácter a carácter, el MISMO que el WHERE del
//     índice PARCIAL flow_events_tenant_event_idx (migración 0056): tocar esta
//     cadena sin tocar la migración —o viceversa— deja el plan fuera del
//     índice EN SILENCIO. La única defensa real es el test de integración que
//     corre EXPLAIN y afirma el uso del índice (ver
//     eventstelemetry_integration_test.go).
//  3. `ORDER BY created_at, id` + keyset (los placeholders $3/$4 opcionales) +
//     `LIMIT $5` duro: sin ORDER BY el resultado sale en el orden físico del
//     scan (no determinista, imposible de paginar) — medido con OFFSET: 3383
//     buffers y creciendo con la página, frente a 83-139 del keyset.
//  4. `payload->>'kind' AS event_kind` — NUNCA la columna `kind` de la fila
//     ("persist"|"event"), que colapsaría en una lectura sin sentido.
//
// $2/$3/$4 son NULLABLE a propósito (mismo patrón que
// internal/flujos/events/store.go:listEventsWhere — "$N::tipo IS NULL"): deja
// `since` y el cursor OPCIONALES sin bifurcar esta cadena en dos, y evita el
// error de comparar una fila contra NULL (que en SQL evalúa NULL, no true, y
// dejaría la primera página siempre vacía si no se guardara así).
const eventTelemetryQuery = `
SELECT id, name, payload->>'kind' AS event_kind, payload, created_at
  FROM public.flow_events
 WHERE tenant_id = $1
   AND name LIKE 'event\_%'
   AND ($2::timestamptz IS NULL OR created_at >= $2)
   AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4))
 ORDER BY created_at, id
 LIMIT $5
`

// ListEventTelemetry implementa EventTelemetryReader. tenantID SIEMPRE llega
// del token (INV-8, el handler lo saca de httpapi.IdentityFromContext); este
// método no tiene forma de leer el de otro tenant.
func (s *PostgresEventTelemetryStore) ListEventTelemetry(ctx context.Context, tenantID string, f EventTelemetryFilter) (out []EventTelemetryRow, err error) {
	since := nullableTime(f.Since)
	cursorAt := nullableTime(f.CursorAt)
	var cursorID any
	if !f.CursorAt.IsZero() {
		cursorID = f.CursorID
	}

	rows, err := s.db.QueryContext(ctx, eventTelemetryQuery, tenantID, since, cursorAt, cursorID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("publicapi: consultar telemetría de eventos: %w", err)
	}
	// Cierre propagado, no descartado (mismo patrón que collect() en
	// internal/flujos/events/store.go): el error de Close solo sustituye al
	// que ya hubiera si no había ninguno peor que contar.
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("publicapi: cerrar filas de telemetría de eventos: %w", cerr)
		}
	}()

	out = make([]EventTelemetryRow, 0, f.Limit)
	for rows.Next() {
		var ev EventTelemetryRow
		var eventKind sql.NullString
		var payload []byte
		if serr := rows.Scan(&ev.ID, &ev.Name, &eventKind, &payload, &ev.CreatedAt); serr != nil {
			return nil, fmt.Errorf("publicapi: escanear fila de telemetría de eventos: %w", serr)
		}
		ev.EventKind = eventKind.String
		ev.Payload = payload
		out = append(out, ev)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("publicapi: iterar telemetría de eventos: %w", rerr)
	}
	return out, nil
}

// nullableTime traduce el cero de time.Time a NULL para los placeholders
// opcionales de eventTelemetryQuery: el cero de Go («0001-01-01») NUNCA debe
// viajar como fecha real — no significa «sin filtro» para Postgres, significa
// literalmente el año 1.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
