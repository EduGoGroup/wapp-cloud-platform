// Porta internal/publicapi/eventstelemetry_store.go @ ed60c24 (123 líneas: el store, la
// consulta, ListEventTelemetry y nullableTime).
//
// eventstelemetry_store.go — EL ADAPTADOR POSTGRES DE LA TELEMETRÍA DE EVENTOS (mapa §2.7, G18).
// Es el ÚNICO SQL de la cara, y se queda aquí por decisión (D-FX-4): el dueño físico natural de
// public.flow_events es el motor de flujos (conversacion, F8), y bajarlo allí sería cambiar dos
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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresEventTelemetryStore satisface EventTelemetryReader con SQL directo contra la tabla
// PÚBLICA public.flow_events (Plan 043 · Ola 6 · T6.5). Se construye con
// NewPostgresEventTelemetryStore; su valor cero no está listo.
type PostgresEventTelemetryStore struct{}

var _ EventTelemetryReader = (*PostgresEventTelemetryStore)(nil)

// NewPostgresEventTelemetryStore construye el store sobre el *sql.DB YA abierto que el arranque
// comparte con el resto de la plataforma (el mismo pool, no una segunda conexión). No consulta
// la base ni valida db: devuelve siempre un store distinto de nil.
func NewPostgresEventTelemetryStore(db *sql.DB) *PostgresEventTelemetryStore {
	panic(pendiente.Implementar("apipublica.NewPostgresEventTelemetryStore"))
}

// ListEventTelemetry implementa EventTelemetryReader contra public.flow_events, con UNA sola
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
func (s *PostgresEventTelemetryStore) ListEventTelemetry(ctx context.Context, tenantID string, f EventTelemetryFilter) ([]EventTelemetryRow, error) {
	panic(pendiente.Implementar("apipublica.PostgresEventTelemetryStore.ListEventTelemetry"))
}
