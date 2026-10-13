package intakes_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/intakeshelpertest"
)

// Los tests de MemoryStore son EXTERNOS (package intakes_test): intakeshelpertest importa intakes,
// así que un test interno que importara la suite daría un ciclo de imports. Este fichero trae el
// Montaje y los auxiliares que comparten memory_read_test.go, memory_write_test.go y
// memory_reminders_test.go.

// Aserciones de compilación de lo que promete memory.go: el constructor y los mutadores de
// siembra y configuración.
var (
	_ func() *intakes.MemoryStore                                         = intakes.NewMemoryStore
	_ func(*intakes.MemoryStore, func() time.Time)                        = (*intakes.MemoryStore).SetClock
	_ func(*intakes.MemoryStore, time.Duration)                           = (*intakes.MemoryStore).SetLiteralTTL
	_ func(*intakes.MemoryStore, logger.Logger)                           = (*intakes.MemoryStore).SetRetentionLog
	_ func(*intakes.MemoryStore, string, intakes.Intake, ...intakes.Item) = (*intakes.MemoryStore).Add
	_ func(*intakes.MemoryStore, string, string)                          = (*intakes.MemoryStore).SetEvent
	_ func(*intakes.MemoryStore, string) string                           = (*intakes.MemoryStore).EventStatus
	_ func(*intakes.MemoryStore, string, string)                          = (*intakes.MemoryStore).BindEvent
	_ func(*intakes.MemoryStore, string, ...intakes.ShippingZone)         = (*intakes.MemoryStore).SetShippingZones
	_ func(*intakes.MemoryStore, string, string, float64)                 = (*intakes.MemoryStore).SetShippingPrice
	_ func(*intakes.MemoryStore, string, string, int)                     = (*intakes.MemoryStore).SetDepositTemplate
	_ intakeshelpertest.Port                                              = (*intakes.MemoryStore)(nil)
)

const (
	tenant1 = "tenant-1"
	tenant2 = "tenant-2"
)

var ctx = context.Background()

// testClock es un reloj que solo avanza cuando el test lo mueve. Arranca después de las siembras
// de la suite, que son de agosto de 2026.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newMemoryStore devuelve un store vacío con un reloj de test inyectado.
func newMemoryStore() (*intakes.MemoryStore, *testClock) {
	clock := newTestClock()
	store := intakes.NewMemoryStore()
	store.SetClock(clock.Now)
	return store, clock
}

// TestMemoryStore_Contrato corre la suite de la persistencia de solicitudes contra MemoryStore,
// sin BD y sin reloj real: cada caso monta un store nuevo con su reloj, y Advance lo adelanta un
// segundo. La siembra usa los mutadores del doble: Add, y un evento propio con SetEvent y BindEvent.
func TestMemoryStore_Contrato(t *testing.T) {
	intakeshelpertest.Contrato(t, func(*testing.T) intakeshelpertest.Montaje {
		store, clock := newMemoryStore()
		return intakeshelpertest.Montaje{
			Store:   store,
			TenantA: uuid.NewString(),
			TenantB: uuid.NewString(),
			Seed: func(_ *testing.T, tenantID string, s intakeshelpertest.Seed) string {
				eventID := uuid.NewString()
				store.Add(tenantID, s.Intake, s.Items...)
				store.SetEvent(eventID, s.EventStatus)
				store.BindEvent(s.Intake.ID, eventID)
				return eventID
			},
			SetShippingZones: func(_ *testing.T, tenantID string, zones ...intakes.ShippingZone) {
				store.SetShippingZones(tenantID, zones...)
			},
			SetDepositTemplate: func(_ *testing.T, tenantID, template string, dueDays int) {
				store.SetDepositTemplate(tenantID, template, dueDays)
			},
			StoredStatus: func(_ *testing.T, tenantID, intakeID string) string {
				return store.StoredStatus(tenantID, intakeID)
			},
			EventStatus: func(_ *testing.T, eventID string) string { return store.EventStatus(eventID) },
			Now:         func(*testing.T) time.Time { return clock.Now() },
			Advance:     func(*testing.T) { clock.Advance(time.Second) },
		}
	})
}

// seeded es una cabecera `status` del día d de agosto de 2026, con ese id.
func seeded(id, status string, d int) intakes.Intake {
	at := time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC)
	return intakes.Intake{ID: id, ContactID: "contact-1", SessionID: "sess-1", Status: status, CreatedAt: at, UpdatedAt: at}
}

// twoLines son dos líneas de cliente: 2×2 + 1×3 = 7.
func twoLines() []intakes.Item {
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	return []intakes.Item{
		{SKU: "pan", Label: "Pan", Qty: 2, UnitPrice: 2, AddedAt: at},
		{SKU: "queso", Label: "Queso", Qty: 1, UnitPrice: 3, AddedAt: at.Add(time.Minute)},
	}
}

// mustGet lee el detalle o falla el test.
func mustGet(t *testing.T, store *intakes.MemoryStore, tenantID, intakeID string) intakes.Detail {
	t.Helper()
	d, err := store.Get(ctx, tenantID, intakeID)
	if err != nil {
		t.Fatalf("Get(%s, %s): error inesperado %v", tenantID, intakeID, err)
	}
	return d
}

// TestNewMemoryStore_IsEmptyAndReady: recién construido no tiene nada de nadie, se puede usar sin
// inyectarle reloj (lo que fecha no sale a cero) y su TTL del literal es el por defecto.
func TestNewMemoryStore_IsEmptyAndReady(t *testing.T) {
	store := intakes.NewMemoryStore()
	if store == nil {
		t.Fatal("NewMemoryStore devolvió nil")
	}
	got, total, err := store.List(ctx, tenant1, intakes.Filter{})
	if err != nil || total != 0 || got == nil || len(got) != 0 {
		t.Errorf("List de un store nuevo = (%#v, %d, %v), quería un slice vacío no nil, 0 y nil", got, total, err)
	}
	if zones, err := store.ShippingZones(ctx, tenant1); err != nil || zones != nil {
		t.Errorf("ShippingZones de un store nuevo = (%v, %v), quería nil y nil", zones, err)
	}
	if status := store.EventStatus("evento"); status != "" {
		t.Errorf("EventStatus de un store nuevo = %q, quería vacío", status)
	}
	rev, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`)})
	if err != nil || rev.CreatedAt.IsZero() {
		t.Errorf("sin SetClock, InsertRevision = (%+v, %v); quería una revisión fechada con el reloj del proceso", rev, err)
	}
}

// TestMemoryStore_SetClock_DatesEverythingTheStoreDates: la revisión, el plazo de la seña y el
// UpdatedAt de la cabecera salen EXACTAMENTE del reloj inyectado; un SetClock posterior lo
// sustituye sin remarcar lo guardado; y un reloj nil se ignora.
func TestMemoryStore_SetClock_DatesEverythingTheStoreDates(t *testing.T) {
	store, clock := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusConfirmed, 1))
	first := clock.Now()

	rev, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`)})
	if err != nil || !rev.CreatedAt.Equal(first) {
		t.Fatalf("InsertRevision = (CreatedAt %v, %v), quería el instante del reloj, %v", rev.CreatedAt, err, first)
	}
	head, err := store.UpdateStatus(ctx, tenant1, "i-1", intakes.StatusDepositRequested, []string{intakes.StatusConfirmed})
	if err != nil {
		t.Fatalf("UpdateStatus: error inesperado %v", err)
	}
	if want := first.AddDate(0, 0, intakes.DefaultDepositDueDays); !head.DepositDueAt.Equal(want) {
		t.Errorf("DepositDueAt = %v, quería el reloj más %d días: %v", head.DepositDueAt, intakes.DefaultDepositDueDays, want)
	}
	if !head.UpdatedAt.Equal(first) {
		t.Errorf("UpdatedAt = %v, quería el instante del reloj, %v", head.UpdatedAt, first)
	}

	other := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store.SetClock(func() time.Time { return other })
	store.SetClock(nil) // se ignora: sigue mandando `other`
	if got := mustGet(t, store, tenant1, "i-1"); !got.UpdatedAt.Equal(first) || !got.Revisions[0].CreatedAt.Equal(first) {
		t.Errorf("SetClock remarcó lo ya guardado: UpdatedAt %v, revisión %v", got.UpdatedAt, got.Revisions[0].CreatedAt)
	}
	second, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`)})
	if err != nil || !second.CreatedAt.Equal(other) {
		t.Errorf("tras SetClock, InsertRevision = (CreatedAt %v, %v), quería %v", second.CreatedAt, err, other)
	}
}

// TestMemoryStore_Add_StoresTheHeaderRawAndAppends: la cabecera se guarda entera y con el estado
// TAL CUAL (la clave legada se normaliza solo al leer); las siembras se acumulan por tenant; y sin
// BindEvent la solicitud es una fila legada sin evento.
func TestMemoryStore_Add_StoresTheHeaderRawAndAppends(t *testing.T) {
	store, _ := newMemoryStore()
	in := seeded("i-1", intakes.StatusClosedLegacy, 1)
	in.CustomerNote, in.Total = "dejarlo en portería", 7
	store.Add(tenant1, in, twoLines()...)
	store.Add(tenant1, seeded("i-2", intakes.StatusOpen, 2))
	store.Add(tenant2, seeded("i-3", intakes.StatusOpen, 3))

	if stored := store.StoredStatus(tenant1, "i-1"); stored != intakes.StatusClosedLegacy {
		t.Errorf("StoredStatus = %q, quería la clave tal cual, %q", stored, intakes.StatusClosedLegacy)
	}
	got := mustGet(t, store, tenant1, "i-1")
	want := in
	want.Status = intakes.StatusConfirmed
	if got.Intake != want {
		t.Errorf("cabecera leída = %+v, quería %+v", got.Intake, want)
	}
	if len(got.Items) != 2 || got.Items[0] != twoLines()[0] || got.Items[1] != twoLines()[1] {
		t.Errorf("líneas leídas = %+v, quería las sembradas en su orden", got.Items)
	}
	if _, total, err := store.List(ctx, tenant1, intakes.Filter{}); err != nil || total != 2 {
		t.Errorf("el tenant 1 tiene %d solicitudes (err=%v), quería 2", total, err)
	}
	if _, total, err := store.List(ctx, tenant1, intakes.Filter{Orphan: true}); err != nil || total != 2 {
		t.Errorf("sin BindEvent hay %d huérfanas (err=%v), quería las 2: una fila sin ligadura no tiene evento vivo", total, err)
	}
}

// TestMemoryStore_Events_SeedBindAndObserve: SetEvent siembra y reescribe, EventStatus lo enseña,
// y BindEvent liga, religa y —con el evento vacío— desliga. La ligadura es lo que mira la orfandad.
func TestMemoryStore_Events_SeedBindAndObserve(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1))
	orphans := func() int {
		t.Helper()
		_, total, err := store.List(ctx, tenant1, intakes.Filter{Orphan: true})
		if err != nil {
			t.Fatalf("List: error inesperado %v", err)
		}
		return total
	}

	store.SetEvent("ev-1", "open")
	store.SetEvent("ev-2", "cancelled")
	if a, b, c := store.EventStatus("ev-1"), store.EventStatus("ev-2"), store.EventStatus("ev-3"); a != "open" || b != "cancelled" || c != "" {
		t.Errorf("EventStatus = (%q, %q, %q), quería (open, cancelled, vacío)", a, b, c)
	}
	store.BindEvent("i-1", "ev-1")
	if n := orphans(); n != 0 {
		t.Errorf("ligada a un evento `open` hay %d huérfanas, quería 0", n)
	}
	store.BindEvent("i-1", "ev-2")
	if n := orphans(); n != 1 {
		t.Errorf("religada a un evento `cancelled` hay %d huérfanas, quería 1", n)
	}
	store.BindEvent("i-1", "ev-1")
	store.SetEvent("ev-1", "closed")
	if n := orphans(); n != 1 || store.EventStatus("ev-1") != "closed" {
		t.Errorf("tras reescribir el evento a `closed`: %d huérfanas y estado %q", n, store.EventStatus("ev-1"))
	}
	store.SetEvent("ev-1", "open")
	store.BindEvent("i-1", "ev-desconocido")
	if n := orphans(); n != 1 {
		t.Errorf("ligada a un evento que no existe hay %d huérfanas, quería 1", n)
	}
	store.BindEvent("i-1", "")
	if n := orphans(); n != 1 {
		t.Errorf("desligada hay %d huérfanas, quería 1", n)
	}
}

// TestMemoryStore_SetShippingZones_ReplacesAndCopies: las zonas se sustituyen enteras, son por
// tenant, y el store no se queda con el slice del llamante.
func TestMemoryStore_SetShippingZones_ReplacesAndCopies(t *testing.T) {
	store, _ := newMemoryStore()
	zones := []intakes.ShippingZone{{Code: "z1", Label: "Providencia", Price: 3000}, {Code: "z2", Label: "Macul", Price: 2000}}
	store.SetShippingZones(tenant1, zones...)
	zones[0].Label = "pisada"
	got, err := store.ShippingZones(ctx, tenant1)
	if err != nil {
		t.Fatalf("ShippingZones: error inesperado %v", err)
	}
	if len(got) != 2 || got[0].Label != "Providencia" || got[1].Code != "z2" {
		t.Errorf("zonas guardadas = %+v; cambiar el slice del llamante no debía cambiarlas", got)
	}
	if other, err := store.ShippingZones(ctx, tenant2); err != nil || other != nil {
		t.Errorf("el otro tenant tiene zonas: (%+v, %v)", other, err)
	}
	store.SetShippingZones(tenant1, intakes.ShippingZone{Code: "z3", Price: 1})
	if got, err := store.ShippingZones(ctx, tenant1); err != nil || len(got) != 1 || got[0].Code != "z3" {
		t.Errorf("tras sustituirlas = (%+v, %v), quería solo z3", got, err)
	}
}

// TestMemoryStore_SetShippingPrice_PricesTheLineAndSquaresTheTotal: precifica SOLO la línea de
// envío y deja el total como la suma de las líneas; sin línea de envío, o desde otro tenant, no
// hace nada.
func TestMemoryStore_SetShippingPrice_PricesTheLineAndSquaresTheTotal(t *testing.T) {
	store, _ := newMemoryStore()
	in := seeded("i-1", intakes.StatusOpen, 1)
	in.Total = 7
	store.Add(tenant1, in, twoLines()...)

	store.SetShippingPrice(tenant1, "i-1", 4500)
	if got := mustGet(t, store, tenant1, "i-1"); got.Total != 7 || len(got.Items) != 2 {
		t.Errorf("sin línea de envío cambió algo: total %v, %d líneas", got.Total, len(got.Items))
	}
	if err := store.EnsureShippingLine(ctx, tenant1, "i-1", intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine: error inesperado %v", err)
	}
	store.SetShippingPrice(tenant2, "i-1", 9999)
	store.SetShippingPrice(tenant1, "i-desconocida", 9999)
	if got := mustGet(t, store, tenant1, "i-1"); got.Total != 7 || got.Items[2].UnitPrice != 0 {
		t.Errorf("desde otro tenant o con otro id cambió algo: total %v, envío %v", got.Total, got.Items[2].UnitPrice)
	}
	store.SetShippingPrice(tenant1, "i-1", 4500)
	got := mustGet(t, store, tenant1, "i-1")
	if got.Total != 4507 {
		t.Errorf("Total = %v, quería 4507 (7 de las líneas más 4500 de envío)", got.Total)
	}
	if got.Items[2].SKU != intakes.ShippingSKU || got.Items[2].UnitPrice != 4500 || got.Items[2].Label != intakes.ShippingPendingLabel {
		t.Errorf("línea de envío = %+v, quería la misma línea con precio 4500", got.Items[2])
	}
	if got.Items[0] != twoLines()[0] || got.Items[1] != twoLines()[1] {
		t.Errorf("precificar el envío tocó las líneas del cliente: %+v", got.Items[:2])
	}
}

// TestMemoryStore_SetDepositTemplate_NonPositiveDaysMeanTheDefault: un plazo ≤ 0 se lee como el
// por defecto, en la configuración y al pedir la seña; sembrar de nuevo sustituye lo anterior.
func TestMemoryStore_SetDepositTemplate_NonPositiveDaysMeanTheDefault(t *testing.T) {
	for _, days := range []int{0, -4} {
		store, clock := newMemoryStore()
		store.SetDepositTemplate(tenant1, "plantilla vieja", 9)
		store.SetDepositTemplate(tenant1, "Transfiere a la cuenta 123", days)
		cfg, err := store.NotifySettings(ctx, tenant1)
		if want := (intakes.NotifySettings{DepositTemplate: "Transfiere a la cuenta 123", DepositDueDays: intakes.DefaultDepositDueDays}); err != nil || cfg != want {
			t.Errorf("con %d días: NotifySettings = (%+v, %v), quería %+v", days, cfg, err, want)
		}
		store.Add(tenant1, seeded("i-1", intakes.StatusConfirmed, 1))
		head, err := store.UpdateStatus(ctx, tenant1, "i-1", intakes.StatusDepositRequested, []string{intakes.StatusConfirmed})
		if want := clock.Now().AddDate(0, 0, intakes.DefaultDepositDueDays); err != nil || !head.DepositDueAt.Equal(want) {
			t.Errorf("con %d días: DepositDueAt = %v (err %v), quería %v", days, head.DepositDueAt, err, want)
		}
	}
}

// TestMemoryStore_ConcurrentUse: escritores, lectores y mutadores a la vez sobre el mismo store.
// Bajo -race, un store sin proteger se ve aquí; y al terminar, las revisiones de la solicitud
// compartida están numeradas sin huecos ni repetidos.
func TestMemoryStore_ConcurrentUse(t *testing.T) {
	const workers, rounds = 8, 25
	store, clock := newMemoryStore()
	store.Add(tenant1, seeded("shared", intakes.StatusOpen, 1), twoLines()...)
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*3)
	for w := range workers {
		wg.Go(func() {
			id := uuid.NewString()
			store.Add(tenant1, seeded(id, intakes.StatusOpen, 2+w))
			for range rounds {
				store.SetClock(clock.Now)
				store.SetShippingZones(tenant1, intakes.ShippingZone{Code: "z", Label: "Zona", Price: 1})
				store.SetEvent(id, "open")
				store.BindEvent(id, id)
				_, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "shared", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`)})
				errs <- err
				errs <- store.EnsureShippingLine(ctx, tenant1, "shared", intakes.ShippingAlways)
				_, _, err = store.List(ctx, tenant1, intakes.Filter{Orphan: true})
				errs <- err
				_ = store.Revisions("shared")
				_ = store.EventStatus(id)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("uso concurrente: error inesperado %v", err)
		}
	}
	got := mustGet(t, store, tenant1, "shared")
	if len(got.Revisions) != workers*rounds {
		t.Fatalf("la solicitud compartida tiene %d revisiones, quería %d", len(got.Revisions), workers*rounds)
	}
	for i, rev := range got.Revisions {
		if rev.RevisionNo != i+1 {
			t.Fatalf("revisión en la posición %d numerada %d: la numeración tiene huecos o repetidos", i, rev.RevisionNo)
		}
	}
	if len(got.Items) != 3 || got.Total != 8 {
		t.Errorf("tras garantizar el envío en paralelo: %d líneas y total %v, quería 3 y 8", len(got.Items), got.Total)
	}
}
