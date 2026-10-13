package eventstelemetryhelpertest_test

// memory_test.go — cubre memory.go: le pasa a Memory la suite del puerto (Contrato) y prueba
// aparte lo que es SOLO del doble (los errores de Insert, los ids, el truncado al microsegundo,
// la copia del payload, el límite negativo y el uso concurrente).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/eventstelemetryhelpertest"
)

var memoryBase = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// newMemoryMontaje es el Montaje de Memory: un doble nuevo por caso y dos tenants cualesquiera.
func newMemoryMontaje(t *testing.T) eventstelemetryhelpertest.Montaje {
	t.Helper()
	mem := eventstelemetryhelpertest.NewMemory()
	return eventstelemetryhelpertest.Montaje{
		Reader:  mem,
		TenantA: "tenant-a",
		TenantB: "tenant-b",
		Insert: func(t *testing.T, row eventstelemetryhelpertest.FlowEvent) int64 {
			t.Helper()
			id, err := mem.Insert(row)
			if err != nil {
				t.Fatalf("Memory.Insert(%+v): %v", row, err)
			}
			return id
		},
	}
}

// TestMemory_Contrato: el doble cumple las promesas del puerto, las mismas que el adaptador
// Postgres pasa en test/procesos.
func TestMemory_Contrato(t *testing.T) {
	eventstelemetryhelpertest.Contrato(t, newMemoryMontaje)
}

func memoryEvent(tenant, name string, at time.Time) eventstelemetryhelpertest.FlowEvent {
	return eventstelemetryhelpertest.FlowEvent{TenantID: tenant, Kind: "event", Name: name, Payload: `{"kind":"cart"}`, CreatedAt: at}
}

func memoryList(t *testing.T, mem *eventstelemetryhelpertest.Memory, tenant string, f apipublica.EventTelemetryFilter) []apipublica.EventTelemetryRow {
	t.Helper()
	rows, err := mem.ListEventTelemetry(context.Background(), tenant, f)
	if err != nil {
		t.Fatalf("ListEventTelemetry(%q, %+v): %v", tenant, f, err)
	}
	return rows
}

// TestNewMemory_StartsEmpty: un doble nuevo no tiene filas de nadie y su lista no es nil.
func TestNewMemory_StartsEmpty(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if rows == nil || len(rows) != 0 {
		t.Errorf("doble nuevo: filas = %#v; quiero una lista vacía y no nil", rows)
	}
}

// TestMemory_Insert_IDsGrowAcrossTenants: el primer id es 1 y crece de uno en uno sea cual sea
// el tenant, también para filas que luego no se leen.
func TestMemory_Insert_IDsGrowAcrossTenants(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	rows := []eventstelemetryhelpertest.FlowEvent{
		memoryEvent("tenant-a", "event_started", memoryBase),
		memoryEvent("tenant-b", "event_started", memoryBase),
		memoryEvent("tenant-a", "survey_answer", memoryBase),
		memoryEvent("tenant-a", "event_closed", memoryBase),
	}
	for i, row := range rows {
		id, err := mem.Insert(row)
		if err != nil || id != int64(i+1) {
			t.Fatalf("inserción nº %d: id = %d, err = %v; quiero id %d y nil", i, id, err, i+1)
		}
	}
	got := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 4 {
		t.Errorf("tenant-a lee %+v; quiero las filas 1 y 4", got)
	}
}

// TestMemory_Insert_Rejects: un payload que no es JSON y un created_at en cero se rechazan sin
// guardar nada ni gastar id.
func TestMemory_Insert_Rejects(t *testing.T) {
	cases := []struct {
		name string
		row  eventstelemetryhelpertest.FlowEvent
		want error
	}{
		{"payload is not json", eventstelemetryhelpertest.FlowEvent{TenantID: "tenant-a", Name: "event_started", Payload: `{"kind":`, CreatedAt: memoryBase}, eventstelemetryhelpertest.ErrInvalidPayload},
		{"empty payload", eventstelemetryhelpertest.FlowEvent{TenantID: "tenant-a", Name: "event_started", Payload: ``, CreatedAt: memoryBase}, eventstelemetryhelpertest.ErrInvalidPayload},
		{"zero created_at", eventstelemetryhelpertest.FlowEvent{TenantID: "tenant-a", Name: "event_started", Payload: `{}`}, eventstelemetryhelpertest.ErrZeroCreatedAt},
		{"payload is checked before created_at", eventstelemetryhelpertest.FlowEvent{TenantID: "tenant-a", Name: "event_started", Payload: `no`}, eventstelemetryhelpertest.ErrInvalidPayload},
	}
	mem := eventstelemetryhelpertest.NewMemory()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := mem.Insert(tc.row)
			if !errors.Is(err, tc.want) || id != 0 {
				t.Errorf("Insert = (%d, %v); quiero (0, %v)", id, err, tc.want)
			}
		})
	}
	if rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10}); len(rows) != 0 {
		t.Errorf("tras cuatro rechazos hay %d filas; quiero 0", len(rows))
	}
	if id, err := mem.Insert(memoryEvent("tenant-a", "event_started", memoryBase)); err != nil || id != 1 {
		t.Errorf("tras cuatro rechazos, Insert = (%d, %v); quiero el id 1: un rechazo no gasta id", id, err)
	}
}

// TestMemory_Insert_TruncatesToMicroseconds: created_at se guarda con la resolución de
// timestamptz, y se trunca (no se redondea).
func TestMemory_Insert_TruncatesToMicroseconds(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	at := memoryBase.Add(1500*time.Microsecond + 999*time.Nanosecond)
	if _, err := mem.Insert(memoryEvent("tenant-a", "event_started", at)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if want := memoryBase.Add(1500 * time.Microsecond); len(rows) != 1 || !rows[0].CreatedAt.Equal(want) {
		t.Errorf("filas = %+v; quiero una con created_at %v (truncado al microsegundo)", rows, want)
	}
}

// TestMemory_List_PayloadIsACopy: mutar el payload devuelto no cambia lo guardado.
func TestMemory_List_PayloadIsACopy(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	if _, err := mem.Insert(memoryEvent("tenant-a", "event_started", memoryBase)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	first := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	for i := range first[0].Payload {
		first[0].Payload[i] = 'x'
	}
	second := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 10})
	if got := string(second[0].Payload); got != `{"kind":"cart"}` {
		t.Errorf("tras mutar el payload devuelto, la siguiente lectura da %q; quiero el sembrado", got)
	}
	if second[0].EventKind != "cart" {
		t.Errorf("tras mutar el payload devuelto, EventKind = %q; quiero %q", second[0].EventKind, "cart")
	}
}

// TestMemory_List_KindOfOddPayloads: lo que Contrato no afirma porque jsonb lo formatea a su
// manera o porque no es un objeto: el doble no falla y da vacío, o el texto sembrado.
func TestMemory_List_KindOfOddPayloads(t *testing.T) {
	cases := []struct{ payload, want string }{
		{`[1,2]`, ""},
		{`"cart"`, ""},
		{`null`, ""},
		{`7`, ""},
		{`{"kind":{"a":1}}`, `{"a":1}`},
		{`{"kind":[1,2]}`, `[1,2]`},
		{`{"kind":1.5}`, "1.5"},
		{`{"kind":false}`, "false"},
		{`{"kind":"a\"b"}`, `a"b`},
		{`{"Kind":"cart"}`, ""},
	}
	mem := eventstelemetryhelpertest.NewMemory()
	for i, tc := range cases {
		row := eventstelemetryhelpertest.FlowEvent{TenantID: "tenant-a", Name: "event_started", Payload: tc.payload, CreatedAt: memoryBase.Add(time.Duration(i) * time.Second)}
		if _, err := mem.Insert(row); err != nil {
			t.Fatalf("Insert(%s): %v", tc.payload, err)
		}
	}
	rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 100})
	if len(rows) != len(cases) {
		t.Fatalf("leídas %d filas; quiero %d", len(rows), len(cases))
	}
	for i, tc := range cases {
		if rows[i].EventKind != tc.want {
			t.Errorf("payload %s: EventKind = %q; quiero %q", tc.payload, rows[i].EventKind, tc.want)
		}
	}
}

// TestMemory_List_NegativeLimit: un límite negativo es ErrNegativeLimit y ninguna fila; -1 ya lo
// es y 0 no.
func TestMemory_List_NegativeLimit(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	if _, err := mem.Insert(memoryEvent("tenant-a", "event_started", memoryBase)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for _, limit := range []int{-1, -100} {
		rows, err := mem.ListEventTelemetry(context.Background(), "tenant-a", apipublica.EventTelemetryFilter{Limit: limit})
		if !errors.Is(err, eventstelemetryhelpertest.ErrNegativeLimit) || rows != nil {
			t.Errorf("Limit %d: (%v, %v); quiero (nil, ErrNegativeLimit)", limit, rows, err)
		}
	}
	if rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: 0}); rows == nil || len(rows) != 0 {
		t.Errorf("Limit 0: filas = %#v; quiero una lista vacía y no nil", rows)
	}
}

// TestMemory_List_ContextError: el error es el del contexto (cancelado o vencido) y se mira
// antes que el límite.
func TestMemory_List_ContextError(t *testing.T) {
	mem := eventstelemetryhelpertest.NewMemory()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), memoryBase)
	defer cancelExpired()
	for name, tc := range map[string]struct {
		ctx  context.Context
		want error
	}{
		"cancelled": {cancelled, context.Canceled},
		"expired":   {expired, context.DeadlineExceeded},
	} {
		rows, err := mem.ListEventTelemetry(tc.ctx, "tenant-a", apipublica.EventTelemetryFilter{Limit: -1})
		if !errors.Is(err, tc.want) || rows != nil {
			t.Errorf("%s: (%v, %v); quiero (nil, %v)", name, rows, err, tc.want)
		}
	}
}

// TestMemory_ConcurrentUse: insertar y leer a la vez no corrompe nada (lo vigila -race) y al
// final están todas las filas, con ids únicos.
func TestMemory_ConcurrentUse(t *testing.T) {
	const writers, perWriter = 8, 25
	mem := eventstelemetryhelpertest.NewMemory()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				at := memoryBase.Add(time.Duration(w*perWriter+i) * time.Millisecond)
				if _, err := mem.Insert(memoryEvent("tenant-a", "event_started", at)); err != nil {
					t.Errorf("Insert concurrente: %v", err)
				}
				if _, err := mem.ListEventTelemetry(context.Background(), "tenant-a", apipublica.EventTelemetryFilter{Limit: 5}); err != nil {
					t.Errorf("ListEventTelemetry concurrente: %v", err)
				}
			}
		})
	}
	wg.Wait()

	rows := memoryList(t, mem, "tenant-a", apipublica.EventTelemetryFilter{Limit: writers*perWriter + 1})
	if len(rows) != writers*perWriter {
		t.Fatalf("leídas %d filas; quiero %d", len(rows), writers*perWriter)
	}
	seen := make(map[int64]bool, len(rows))
	for i, r := range rows {
		if seen[r.ID] || r.ID < 1 || r.ID > writers*perWriter {
			t.Errorf("id %d repetido o fuera de 1..%d", r.ID, writers*perWriter)
		}
		seen[r.ID] = true
		if i > 0 && rows[i-1].CreatedAt.After(r.CreatedAt) {
			t.Errorf("fila %d desordenada: %v va antes que %v", i, rows[i-1].CreatedAt, r.CreatedAt)
		}
	}
}
