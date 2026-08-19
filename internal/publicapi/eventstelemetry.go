package publicapi

import (
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// EventTelemetryRow es UNA fila del outbox append-only public.flow_events
// filtrada al vocabulario de ciclo de vida (name LIKE 'event\_%'), tal como la
// lee GET /api/v1/events/telemetry (Plan 043 · Ola 6 · T6.5, cierra MD-043.17).
//
// EventKind es SIEMPRE payload->>'kind' (menu|cart|survey|media), JAMÁS la
// columna `kind` de la fila ("persist"|"event") — la colisión de vocabulario
// documentada en internal/flujos/runtime/event_effects.go. Este tipo no puede
// llevar la columna equivocada por accidente: quien construye el valor decide
// la etiqueta al leerlo del SELECT, no hay un segundo campo `Kind` con el que
// confundirse.
type EventTelemetryRow struct {
	ID        int64
	Name      string
	EventKind string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// EventTelemetryFilter es el filtro NORMALIZADO que consume el puerto de
// lectura. Since/CursorAt en cero ⇒ sin ese filtro (INSTANTE cero de Go, nunca
// una fecha real). Limit YA viene validado (<= intakes.MaxExportIntakes) por el
// handler: el puerto no vuelve a decidir la cota, solo la aplica.
type EventTelemetryFilter struct {
	Since    time.Time
	CursorAt time.Time
	CursorID int64
	Limit    int
}

// EventTelemetryReader es el puerto de UNA sola operación (mismo patrón que
// ConversationEventLister/IntentConfigStore) que consume el handler. Lo
// satisface *PostgresEventTelemetryStore (eventstelemetry_store.go).
//
// El tenant SIEMPRE lo pone el handler desde la Identity del token (INV-8);
// este puerto no tiene forma de pedir los eventos de otro tenant — es un
// parámetro explícito, no un campo del filtro que alguien pudiera rellenar
// desde la query.
type EventTelemetryReader interface {
	ListEventTelemetry(ctx context.Context, tenantID string, f EventTelemetryFilter) ([]EventTelemetryRow, error)
}

// defaultEventTelemetryLimit es la página por defecto sin `limit` en la query.
// El TOPE DURO no es una constante propia: es intakes.MaxExportIntakes,
// reusando el MISMO precedente que el export/summary de
// solicitudes (intakes.MaxExportIntakes, D-041: «un export truncado es peor»
// aplicado aquí a «un limit recortado en silencio es peor»): superarlo NO
// recorta, responde 422 — el llamante pagina con `cursor` en vez de pedir de
// golpe más de lo que el servidor está dispuesto a materializar en una vuelta.
const defaultEventTelemetryLimit = 100

// eventTelemetryItemDTO es la proyección al wire de UNA fila. `payload` viaja
// TAL CUAL (JSONB crudo): para los seis efectos de ciclo de vida hoy solo
// lleva `history_id` y `kind` (ver event_effects.go), CERO PII — es la MISMA
// garantía que ya vale para toda la tabla flow_events (0009: «CERO PII»).
type eventTelemetryItemDTO struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	EventKind string          `json:"event_kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// eventTelemetryListResponse es el contrato de GET /api/v1/events/telemetry:
// la página MÁS el cursor opaco de la siguiente (vacío ⇒ no hay más). NO lleva
// `total`: un COUNT(*) sobre el mismo filtro pagaría el escaneo dos veces, y es
// precisamente lo que el keyset existe para evitar (medido: keyset 83-139
// buffers frente a OFFSET 3383, creciendo con la página) — la bandeja de
// intakes/conversation-events sí puede permitírselo porque pagina con
// OFFSET/page_size acotado a MaxPageSize=200; aquí NO hay ese tope por diseño
// (el filtro puede abarcar toda la ventana temporal de un tenant ruidoso).
type eventTelemetryListResponse struct {
	Events     []eventTelemetryItemDTO `json:"events"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	Limit      int                     `json:"limit"`
}

// eventTelemetryHandler sirve GET /api/v1/events/telemetry (Plan 043 · T6.5,
// cierra MD-043.17): las filas de ciclo de vida del evento (name LIKE
// 'event\_%') del tenant del token (INV-8), ordenadas por (created_at, id) y
// paginadas por keyset — patrón de getIntentsHandler (intents.go): puerto de
// una sola operación, tenant SIEMPRE de httpapi.IdentityFromContext (NUNCA de
// la query).
//
// Query params: `since` (RFC3339, opcional — filtra created_at >= since),
// `cursor` (opaco, opcional — la página siguiente a la última fila vista),
// `limit` (opcional, default defaultEventTelemetryLimit, tope duro
// intakes.MaxExportIntakes).
//
// Respuestas: 200 con la página; 400 si `since`/`cursor`/`limit` no parsean;
// 401 sin identidad; 422 si `limit` excede el tope (ERROR, no recorte
// silencioso — mismo precedente que summary.go/export.go); 500 en fallo del
// store.
func eventTelemetryHandler(reader EventTelemetryReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if reader == nil {
			writeError(w, http.StatusInternalServerError, "lector de telemetría de eventos no configurado")
			return
		}

		filter, msg := parseEventTelemetryFilter(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		if filter.Limit > intakes.MaxExportIntakes {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
				"limit pedido (%d) excede el máximo de %d: pagina con cursor en vez de pedir más de golpe",
				filter.Limit, intakes.MaxExportIntakes))
			return
		}

		// Se pide UNA fila de más que Limit para saber si hay página siguiente SIN
		// una segunda consulta (un COUNT aparte pagaría el mismo escaneo dos
		// veces). La fila de sobra NUNCA sale en la respuesta: solo alimenta el
		// cursor y se descarta.
		queryFilter := filter
		queryFilter.Limit = filter.Limit + 1
		rows, err := reader.ListEventTelemetry(r.Context(), id.TenantID, queryFilter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo leer la telemetría de eventos")
			return
		}

		nextCursor := ""
		if len(rows) > filter.Limit {
			last := rows[filter.Limit-1]
			nextCursor = encodeEventTelemetryCursor(last.CreatedAt, last.ID)
			rows = rows[:filter.Limit]
		}

		out := make([]eventTelemetryItemDTO, 0, len(rows))
		for _, ev := range rows {
			out = append(out, eventTelemetryItemDTO{
				ID:        ev.ID,
				Name:      ev.Name,
				EventKind: ev.EventKind,
				Payload:   ev.Payload,
				CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		writeJSON(w, http.StatusOK, eventTelemetryListResponse{
			Events: out, NextCursor: nextCursor, Limit: filter.Limit,
		})
	})
}

// parseEventTelemetryFilter traduce la query al filtro del store. Un valor mal
// formado se DICE (400) en vez de ignorarse — mismo criterio que
// parseConversationEventFilter: devolver la ventana entera ante un `since`
// mal escrito sería peor que un error, porque quien lo lee creería estar
// viendo lo que pidió.
func parseEventTelemetryFilter(r *http.Request) (EventTelemetryFilter, string) {
	q := r.URL.Query()

	limit := defaultEventTelemetryLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return EventTelemetryFilter{}, "limit inválido: usa un entero positivo"
		}
		limit = n
	}

	var since time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return EventTelemetryFilter{}, "since inválido: usa RFC3339 (p. ej. 2026-08-10T00:00:00Z)"
		}
		since = t
	}

	var cursorAt time.Time
	var cursorID int64
	if raw := q.Get("cursor"); raw != "" {
		at, cid, err := decodeEventTelemetryCursor(raw)
		if err != nil {
			return EventTelemetryFilter{}, "cursor inválido: usa el cursor opaco devuelto por la página anterior"
		}
		cursorAt, cursorID = at, cid
	}

	return EventTelemetryFilter{
		Since: since, CursorAt: cursorAt, CursorID: cursorID, Limit: limit,
	}, ""
}

// encodeEventTelemetryCursor/decodeEventTelemetryCursor son el par opaco del
// keyset: base64(url-safe, sin padding) de "RFC3339Nano|id". Opaco a propósito
// (mismo criterio que cualquier cursor de paginación): el CLIENTE no debe
// construir uno a mano ni depender de su forma interna, solo devolver el que
// recibió.
func encodeEventTelemetryCursor(at time.Time, id int64) string {
	raw := fmt.Sprintf("%s|%d", at.UTC().Format(time.RFC3339Nano), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeEventTelemetryCursor(s string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("decodificar cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, errors.New("formato de cursor inválido")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("instante de cursor inválido: %w", err)
	}
	cid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("id de cursor inválido: %w", err)
	}
	return at, cid, nil
}

// registerEventTelemetry monta GET /api/v1/events/telemetry (Plan 043 · T6.5).
// Va en su propia función por la MISMA razón que registerConversationEvents/
// registerTenantVariables: el `if` de montaje sumaría uno más a la complejidad
// de Register, que ya roza el techo del linter (gocyclo 15).
//
// SIN gate de feature (a diferencia de la bandeja de conversation-events): es
// CAPA TÉCNICA/observabilidad (mismo criterio que tenant-variables, ADR-0035),
// no una capacidad comercial — un tenant del plan más simple no debería quedar
// ciego a su propia telemetría de ciclo de vida por no tener contratada
// `survey` o `media`.
//
// Lectura sin auditoría (protectRead): es idempotente y sin efecto, mismo
// criterio que el resto de lecturas de este archivo (audit.read,
// entitlements.read, intents.read GET).
func registerEventTelemetry(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, log sharedlogger.Logger) {
	if d.EventTelemetry == nil {
		return
	}
	mux.Handle("GET /api/v1/events/telemetry", protectRead(mw, log,
		"events_telemetry.read", eventTelemetryHandler(d.EventTelemetry)))
}
