// Porta internal/publicapi/eventstelemetry.go @ ed60c24 (262 líneas: tipos, puerto, handler,
// parseo del filtro, cursor y registerEventTelemetry).
//
// eventstelemetry.go — LA LECTURA DE LA TELEMETRÍA DE CICLO DE VIDA DEL EVENTO (Plan 043 · Ola 6
// · T6.5, cierra MD-043.17; mapa §2.7, G18): GET /api/v1/events/telemetry. Lee las filas
// `event_*` del outbox append-only public.flow_events. El adaptador SQL del puerto vive al lado,
// en eventstelemetry_store.go (D-FX-4: se queda en la cara).
//
// En el rojo solo existían los tipos, el puerto, EventTelemetryDeps y MountEventTelemetry; el
// handler, el parseo del filtro y el cursor nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// EventTelemetryRow es UNA fila del outbox append-only public.flow_events filtrada al
// vocabulario de ciclo de vida (name LIKE 'event\_%'), tal como la lee G18.
//
// EventKind es SIEMPRE payload->>'kind' (menu|cart|survey|media; vacío si el payload no lo
// trae), JAMÁS la columna `kind` de la fila ("persist"|"event") —la colisión de vocabulario
// documentada en internal/flujos/runtime/event_effects.go—. Este tipo no puede llevar la columna
// equivocada por accidente: no hay un segundo campo `Kind` con el que confundirse.
type EventTelemetryRow struct {
	// ID es la columna id: la identidad técnica de la fila y el desempate del orden.
	ID int64
	// Name es la columna name ("event_started", "event_closed"…).
	Name string
	// EventKind es payload->>'kind'.
	EventKind string
	// Payload es la columna payload, el JSONB crudo.
	Payload json.RawMessage
	// CreatedAt es la columna created_at.
	CreatedAt time.Time
}

// EventTelemetryFilter es el filtro NORMALIZADO que consume el puerto de lectura.
type EventTelemetryFilter struct {
	// Since deja las filas con created_at >= Since. En cero ⇒ sin ese filtro (el INSTANTE cero
	// de Go, nunca una fecha real).
	Since time.Time
	// CursorAt y CursorID son la posición de la última fila ya servida: deja las filas con
	// (created_at, id) estrictamente mayor, comparado como par. CursorAt en cero ⇒ sin cursor,
	// valga lo que valga CursorID.
	CursorAt time.Time
	CursorID int64
	// Limit es el máximo de filas. YA viene validado por el handler (que pide una de más): el
	// puerto no vuelve a decidir la cota, solo la aplica.
	Limit int
}

// EventTelemetryReader es el puerto de UNA sola operación que consume el handler de G18. Lo
// satisfacen *PostgresEventTelemetryStore (eventstelemetry_store.go) y, en los tests,
// *eventstelemetryhelpertest.Memory; sus promesas las afirma la suite
// eventstelemetryhelpertest.Contrato contra las dos.
//
// El tenant SIEMPRE lo pone el handler desde la Identity del token (INV-8); este puerto no tiene
// forma de pedir los eventos de otro tenant —es un parámetro explícito, no un campo del filtro
// que alguien pudiera rellenar desde la query—.
type EventTelemetryReader interface {
	// ListEventTelemetry devuelve las filas de flow_events de tenantID cuyo nombre empieza
	// LITERALMENTE por "event_" (el guion bajo no es comodín: "eventXfoo" no entra), se mire la
	// columna `kind` que se mire, acotadas por f y ordenadas por (created_at, id) ascendente:
	// como mucho f.Limit, las PRIMERAS de ese orden. Un tenant sin filas (o el tenant vacío) da
	// cero filas y error nil. Con el contexto cancelado devuelve un error y ninguna fila.
	ListEventTelemetry(ctx context.Context, tenantID string, f EventTelemetryFilter) ([]EventTelemetryRow, error)
}

// EventTelemetryDeps es lo que G18 necesita. En la cara vieja era el campo EventTelemetry de
// publicapi.Deps.
type EventTelemetryDeps struct {
	// EventTelemetry lee las filas de ciclo de vida. nil ⇒ G18 no se monta.
	EventTelemetry EventTelemetryReader
}

// MountEventTelemetry registra en c "GET /api/v1/events/telemetry" (G18), solo si
// d.EventTelemetry no es nil. Sin lector la ruta no existe (404 de ruta inexistente): es más
// honesto que una telemetría que responde 500.
//
// Cadena R con permiso "events_telemetry.read" (ver Common: 401 sin token; 403
// {"error":"permiso denegado"} sin el permiso o con un token sin empresa; NINGÚN registro de
// auditoría, tampoco en el camino feliz). SIN gate de feature, a propósito: es capa técnica de
// observabilidad (el mismo criterio que tenant-variables, ADR-0035), no una capacidad comercial
// —un tenant del plan más simple no debe quedar ciego a su propia telemetría por no tener
// contratada `survey` o `media`—.
//
// La petición:
//
//   - el puerto recibe SIEMPRE el tenant del token (INV-8): no hay parámetro que lo cambie y un
//     `tenant_id` en la query no cuenta;
//   - "limit": entero positivo en dígitos ASCII (lo que acepta strconv.Atoi); si falta o es
//     vacío ⇒ 100. Si no es un entero, o es <= 0 ⇒ 400 {"error":"limit inválido: usa un entero
//     positivo"}. Por encima de intakes.MaxExportIntakes (5000) NO se recorta ⇒ 422
//     {"error":"limit pedido (N) excede el máximo de 5000: pagina con cursor en vez de pedir
//     más de golpe"}, y el puerto no se consulta; 5000 justo sí pasa;
//   - "since": instante RFC3339 (con o sin fracción, con cualquier desplazamiento), que llega al
//     puerto como f.Since; si falta o es vacío no filtra. Ilegible ⇒ 400 {"error":"since
//     inválido: usa RFC3339 (p. ej. 2026-08-10T00:00:00Z)"};
//   - "cursor": el valor opaco de "next_cursor" de la página anterior, que llega al puerto como
//     (f.CursorAt, f.CursorID); si falta o es vacío no hay cursor. Si no es base64 URL sin
//     relleno de «instante RFC3339|id entero» —base64 roto o con relleno, sin separador, con un
//     campo de más, un instante ilegible o fuera de rango, un id que no es un entero de 64 bits—
//     ⇒ 400 {"error":"cursor inválido: usa el cursor opaco devuelto por la página anterior"}.
//     El id solo tiene que ser un entero: uno negativo se acepta y llega al puerto tal cual. Y
//     un cursor cuyo instante es el cero de Go (0001-01-01T00:00:00Z) se acepta y llega al
//     puerto con f.CursorAt en cero, que para el puerto es «sin cursor»: equivale a no mandarlo;
//   - los tres se validan en ese orden (limit, since, cursor) y la cota del 422 después: una
//     petición con dos fallos responde el del primero, y un 400 gana al 422;
//   - un valor mal formado se DICE (400) en vez de ignorarse: devolver la ventana entera ante un
//     `since` mal escrito sería peor que un error, porque quien lo lee creería estar viendo lo
//     que pidió;
//   - al puerto se le pide UNA fila más que el límite (f.Limit = limit + 1) para saber si hay
//     página siguiente sin una segunda consulta.
//
// La respuesta:
//
//   - 200 {"events":[…],"next_cursor":"…","limit":L}: como mucho L eventos, en el orden del
//     puerto; "limit" es el aplicado; "events":[] (nunca null) si no hay ninguno. NO hay
//     "total": un COUNT sobre el mismo filtro pagaría el escaneo dos veces;
//   - "next_cursor" SOLO si el puerto devolvió más de L filas: codifica la ÚLTIMA fila servida
//     (la L-ésima, no la de sobra, que se descarta), con su instante al nanosegundo y en UTC. Su
//     ausencia significa que no hay más. Pedir la página siguiente con él da al puerto
//     exactamente el instante y el id de esa fila;
//   - cada evento: {"id","name","event_kind","payload","created_at"}, en ese orden. "payload"
//     viaja TAL CUAL (el JSONB crudo; CERO PII, la garantía de toda la tabla flow_events) y
//     "created_at" en UTC, RFC3339 con la fracción de segundo que tenga. El tenant NO viaja;
//   - un fallo del puerto ⇒ 500 {"error":"no se pudo leer la telemetría de eventos"}, que NO
//     repite el error del puerto (el del driver puede llevar el DSN).
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con el lector presente hace panic AL MONTAR (ver Common).
func MountEventTelemetry(c *Cara, k Common, d EventTelemetryDeps) {
	panic(pendiente.Implementar("apipublica.MountEventTelemetry"))
}
