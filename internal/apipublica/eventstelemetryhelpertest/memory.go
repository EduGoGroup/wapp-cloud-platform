package eventstelemetryhelpertest

// memory.go — Memory, el doble en memoria de apipublica.EventTelemetryReader. Nace COMPLETO y en
// verde, sin rojo: no es código de producción sino la herramienta con la que se prueban el
// handler y la propia suite. Tiene lógica (filtros, orden, cursor), así que lleva su
// memory_test.go, que además le pasa Contrato (05 E-3, fila «Dobles de test»; E-6).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// lifecyclePrefix es por lo que empieza el nombre de una fila de ciclo de vida: el
// `name LIKE 'event\_%'` del adaptador, con el guion bajo literal.
const lifecyclePrefix = "event_"

// Errores de Memory.
var (
	// ErrInvalidPayload lo devuelve Insert cuando el payload no es JSON válido: la columna es
	// jsonb y Postgres rechazaría la fila.
	ErrInvalidPayload = errors.New("eventstelemetryhelpertest: el payload no es JSON válido")
	// ErrZeroCreatedAt lo devuelve Insert cuando la fila no trae created_at: el doble no tiene
	// reloj con el que imitar el DEFAULT now() de la tabla.
	ErrZeroCreatedAt = errors.New("eventstelemetryhelpertest: created_at en cero; el doble no tiene reloj")
	// ErrNegativeLimit lo devuelve ListEventTelemetry con un límite negativo, como Postgres
	// rechaza un LIMIT negativo.
	ErrNegativeLimit = errors.New("eventstelemetryhelpertest: LIMIT no puede ser negativo")
)

// storedEvent es una fila guardada: lo sembrado más su id.
type storedEvent struct {
	id  int64
	row FlowEvent
}

// Memory es un apipublica.EventTelemetryReader en memoria: guarda las filas que se le insertan y
// las lee con las mismas reglas que el adaptador Postgres (las que afirma Contrato). Seguro para
// uso concurrente. Se construye con NewMemory.
type Memory struct {
	mu     sync.Mutex
	lastID int64
	events []storedEvent
}

var _ apipublica.EventTelemetryReader = (*Memory)(nil)

// NewMemory devuelve un doble vacío: ningún tenant tiene filas y el primer id será 1.
func NewMemory() *Memory {
	return &Memory{}
}

// Insert guarda la fila tal cual y devuelve su id: 1 para la primera y uno más en cada
// inserción, sea cual sea el tenant (como el BIGSERIAL de la tabla). created_at se trunca al
// microsegundo, la resolución de timestamptz. No valida el nombre ni la columna kind: una fila
// que no es de ciclo de vida se guarda igual y simplemente no se lee.
//
// Errores, sin guardar nada ni gastar id: ErrInvalidPayload si row.Payload no es JSON válido y
// ErrZeroCreatedAt si row.CreatedAt es el instante cero.
func (m *Memory) Insert(row FlowEvent) (int64, error) {
	if !json.Valid([]byte(row.Payload)) {
		return 0, ErrInvalidPayload
	}
	if row.CreatedAt.IsZero() {
		return 0, ErrZeroCreatedAt
	}
	row.CreatedAt = row.CreatedAt.Truncate(time.Microsecond)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastID++
	m.events = append(m.events, storedEvent{id: m.lastID, row: row})
	return m.lastID, nil
}

// ListEventTelemetry implementa apipublica.EventTelemetryReader con sus mismas promesas: las
// filas de tenantID cuyo nombre empieza literalmente por "event_", desde f.Since si no es cero,
// estrictamente después de (f.CursorAt, f.CursorID) si f.CursorAt no es cero, ordenadas por
// (created_at, id) y como mucho f.Limit. Cada fila lleva una COPIA del payload: mutarla no cambia
// lo guardado.
//
// Errores: el del contexto si ya está cancelado o vencido, y ErrNegativeLimit si f.Limit < 0.
func (m *Memory) ListEventTelemetry(ctx context.Context, tenantID string, f apipublica.EventTelemetryFilter) ([]apipublica.EventTelemetryRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.Limit < 0 {
		return nil, ErrNegativeLimit
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Se recorren AL REVÉS de como se insertaron, a propósito: así el orden del resultado sale
	// solo de la ordenación de abajo y no del orden de inserción, que con ids crecientes
	// taparía una ordenación que se olvidara del id.
	matched := make([]storedEvent, 0, len(m.events))
	for _, ev := range slices.Backward(m.events) {
		if matches(ev, tenantID, f) {
			matched = append(matched, ev)
		}
	}
	slices.SortFunc(matched, compareEvents)
	if len(matched) > f.Limit {
		matched = matched[:f.Limit]
	}

	out := make([]apipublica.EventTelemetryRow, 0, len(matched))
	for _, ev := range matched {
		out = append(out, apipublica.EventTelemetryRow{
			ID:        ev.id,
			Name:      ev.row.Name,
			EventKind: payloadKind(ev.row.Payload),
			Payload:   json.RawMessage(ev.row.Payload),
			CreatedAt: ev.row.CreatedAt,
		})
	}
	return out, nil
}

// matches dice si la fila entra en la lectura de tenantID con el filtro f: el WHERE del
// adaptador, condición a condición.
func matches(ev storedEvent, tenantID string, f apipublica.EventTelemetryFilter) bool {
	if ev.row.TenantID != tenantID {
		return false
	}
	if !strings.HasPrefix(ev.row.Name, lifecyclePrefix) {
		return false
	}
	// created_at >= since.
	if !f.Since.IsZero() && ev.row.CreatedAt.Before(f.Since) {
		return false
	}
	// (created_at, id) > (cursor_at, cursor_id), como PAR. Lo enciende CursorAt, no CursorID.
	if !f.CursorAt.IsZero() && !isAfter(ev, f.CursorAt, f.CursorID) {
		return false
	}
	return true
}

// isAfter dice si la fila va ESTRICTAMENTE después de la posición (at, id) en el orden
// (created_at, id): un instante posterior, o el mismo instante y un id mayor.
func isAfter(ev storedEvent, at time.Time, id int64) bool {
	if c := ev.row.CreatedAt.Compare(at); c != 0 {
		return c > 0
	}
	return ev.id > id
}

// compareEvents es el ORDER BY created_at, id.
func compareEvents(a, b storedEvent) int {
	if c := a.row.CreatedAt.Compare(b.row.CreatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.id, b.id)
}

// payloadKind imita payload->>'kind': la cadena tal cual, un número o un booleano como texto, y
// vacío (el NULL de Postgres) si el payload no es un objeto, no trae "kind" o es null. Un "kind"
// que sea objeto o lista sale con el texto sembrado, que NO es el que daría jsonb (lo
// normaliza): Contrato no lo afirma.
func payloadKind(payload string) string {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return ""
	}
	raw, ok := doc["kind"]
	if !ok {
		return ""
	}
	// Una cadena sale sin comillas, y un null deja text en "" sin error: justo el NULL → "".
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}
