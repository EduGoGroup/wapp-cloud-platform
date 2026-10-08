// Porta internal/publicapi/eventstelemetry_store.go @ ed60c24 (123 líneas: el store, la
// consulta, ListEventTelemetry y nullableTime).
//
// eventstelemetry_store.go — EL ADAPTADOR POSTGRES DE LA TELEMETRÍA DE EVENTOS (mapa §2.7, G18).
// Es el ÚNICO SQL de la cara, y se queda aquí por decisión (D-FX-4): el dueño físico natural de
// la tabla flow_events es el motor de flujos (conversacion, F8), y bajarlo allí sería cambiar dos
// cosas a la vez. Es SQL de solo LECTURA sobre una tabla append-only: no escribe una fila ni
// interpreta el ciclo de vida del evento.
//
// Nivel complejo (05 E-12). Lo que no necesita base —los argumentos de la consulta y el mapeo de
// la fila— está en funciones puras con su test; el SQL lo prueba la suite
// eventstelemetryhelpertest.Contrato contra Postgres (test/procesos, P4).
//
// En el rojo solo existían el tipo, el constructor y la firma de ListEventTelemetry; la consulta
// y los auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresEventTelemetryStore satisface EventTelemetryReader con SQL directo contra la tabla
// PÚBLICA public.flow_events (Plan 043 · Ola 6 · T6.5). Se construye con
// NewPostgresEventTelemetryStore; su valor cero no está listo.
type PostgresEventTelemetryStore struct {
	db *sql.DB
}

var _ EventTelemetryReader = (*PostgresEventTelemetryStore)(nil)

// NewPostgresEventTelemetryStore construye el store sobre el *sql.DB YA abierto que el arranque
// comparte con el resto de la plataforma (el mismo pool, no una segunda conexión). No consulta
// la base ni valida db: devuelve siempre un store distinto de nil.
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
//     índice EN SILENCIO.
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

// ListEventTelemetry implementa EventTelemetryReader contra esa tabla, con UNA sola
// consulta de cinco argumentos, en este orden: el tenant; f.Since (NULL si es cero); f.CursorAt
// (NULL si es cero); f.CursorID (NULL si f.CursorAt es cero, valga lo que valga); y f.Limit.
//
// De cada fila: ID, Name y CreatedAt son sus columnas; EventKind es payload->>'kind', y vacío
// si la base lo da NULL; Payload es el JSONB tal cual llega. Las filas salen en el orden de la
// consulta, y sin filas el resultado es una lista vacía (no nil) con error nil.
//
// Errores, siempre con CERO filas y envolviendo la causa (errors.Is la encuentra):
//
//   - la consulta falla ⇒ "apipublica: consultar telemetría de eventos: …";
//   - una fila no se puede leer ⇒ "apipublica: escanear fila de telemetría de eventos: …";
//   - el recorrido se corta a medias ⇒ "apipublica: iterar telemetría de eventos: …";
//   - el cierre de las filas falla sin que hubiera otro error ⇒ "apipublica: cerrar filas de
//     telemetría de eventos: …". Si ya había un error, gana ese y el del cierre no lo pisa.
//
// (En la cara vieja el prefijo de los cuatro era "publicapi:"; no lo ve ningún cliente: el
// handler responde su 500 fijo.)
func (s *PostgresEventTelemetryStore) ListEventTelemetry(ctx context.Context, tenantID string, f EventTelemetryFilter) (out []EventTelemetryRow, err error) {
	rows, err := s.db.QueryContext(ctx, eventTelemetryQuery, eventTelemetryArgs(tenantID, f)...)
	if err != nil {
		return nil, fmt.Errorf("apipublica: consultar telemetría de eventos: %w", err)
	}
	// Cierre propagado, no descartado (mismo patrón que collect() en
	// internal/flujos/events/store.go): el error de Close solo sustituye al
	// que ya hubiera si no había ninguno peor que contar.
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("apipublica: cerrar filas de telemetría de eventos: %w", cerr)
		}
	}()

	out = make([]EventTelemetryRow, 0, f.Limit)
	for rows.Next() {
		ev, serr := scanEventTelemetryRow(rows)
		if serr != nil {
			return nil, fmt.Errorf("apipublica: escanear fila de telemetría de eventos: %w", serr)
		}
		out = append(out, ev)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("apipublica: iterar telemetría de eventos: %w", rerr)
	}
	return out, nil
}

// eventTelemetryArgs arma los CINCO argumentos de eventTelemetryQuery, en el orden de sus
// placeholders: $1 tenant, $2 since, $3 y $4 el cursor, $5 el límite.
//
// El cursor viaja ENTERO o no viaja: lo enciende CursorAt, y con CursorAt en cero el id va NULL
// aunque CursorID traiga algo (un id suelto sin su instante no es una posición en el orden).
func eventTelemetryArgs(tenantID string, f EventTelemetryFilter) []any {
	var cursorID any
	if !f.CursorAt.IsZero() {
		cursorID = f.CursorID
	}
	return []any{
		tenantID,
		eventTelemetryNullableTime(f.Since),
		eventTelemetryNullableTime(f.CursorAt),
		cursorID,
		f.Limit,
	}
}

// eventTelemetryNullableTime (nullableTime en la cara vieja) traduce el cero de time.Time a NULL
// para los placeholders opcionales de eventTelemetryQuery: el cero de Go («0001-01-01») NUNCA
// debe viajar como fecha real — no significa «sin filtro» para Postgres, significa literalmente
// el año 1.
func eventTelemetryNullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// eventTelemetryScanner es lo que scanEventTelemetryRow necesita de *sql.Rows: leer la fila
// actual en sus destinos.
type eventTelemetryScanner interface {
	Scan(dest ...any) error
}

// scanEventTelemetryRow lee UNA fila de eventTelemetryQuery, con sus cinco columnas en el orden
// del SELECT: id, name, event_kind, payload, created_at. event_kind se lee como nullable y su
// NULL (un payload sin "kind") queda en cadena vacía; el payload se copia tal cual.
func scanEventTelemetryRow(sc eventTelemetryScanner) (EventTelemetryRow, error) {
	var (
		ev        EventTelemetryRow
		eventKind sql.NullString
		payload   []byte
	)
	if err := sc.Scan(&ev.ID, &ev.Name, &eventKind, &payload, &ev.CreatedAt); err != nil {
		return EventTelemetryRow{}, err
	}
	ev.EventKind = eventKind.String
	ev.Payload = payload
	return ev, nil
}
