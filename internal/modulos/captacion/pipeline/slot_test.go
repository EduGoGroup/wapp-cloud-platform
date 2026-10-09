package pipeline_test

// slot_test.go — el contrato de slot.go: la dirección de la plaza (Slot), el puerto Slots
// y el aforo (Capacity). Cómo lo usa el worker está en pipeline_capacity_test.go.
//
// Como allí, no se mide ningún tiempo: se cuentan plazas tomadas y cadenas esperando, y el
// reloj real solo es el límite de paciencia de una espera.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
)

var (
	slotA = pipeline.Slot{TenantID: "tenant-1", EdgeID: "edge-1"}
	slotB = pipeline.Slot{TenantID: "tenant-1", EdgeID: "edge-2"}
)

// fakeSlots es el doble de pipeline.Slots: dice a qué Edge apunta cada sesión. En
// producción lo satisface el selector de vía, que además sabe que un tenant en vía API NO
// OCUPA PLAZA — aquí eso es `noSlot`.
type fakeSlots struct {
	edges  map[string]string
	noSlot bool
	err    error

	mu    sync.Mutex
	asked []string
}

var _ pipeline.Slots = (*fakeSlots)(nil)

func (f *fakeSlots) PlazaDe(_ context.Context, tenant, session string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, tenant+"/"+session)
	if f.err != nil {
		return "", false, f.err
	}
	if f.noSlot {
		// Con el Edge puesto a propósito: quien manda es el `ok`, no la cadena.
		return f.edges[session], false, nil
	}
	return f.edges[session], true, nil
}

// mustAcquire toma la plaza o falla.
func mustAcquire(t *testing.T, c *pipeline.Capacity, s pipeline.Slot) func() {
	t.Helper()
	release, err := c.Acquire(context.Background(), s)
	if err != nil || release == nil {
		t.Fatalf("Acquire(%s) = (release nil: %v, %v), se esperaba la plaza", s, release == nil, err)
	}
	return release
}

// acquireInBackground pide la plaza en otra goroutine y devuelve por dónde llega el
// resultado.
func acquireInBackground(ctx context.Context, c *pipeline.Capacity, s pipeline.Slot) <-chan func() {
	got := make(chan func(), 1)
	go func() {
		release, err := c.Acquire(ctx, s)
		if err != nil {
			release = nil
		}
		got <- release
	}()
	return got
}

// receive espera el resultado de acquireInBackground.
func receive(t *testing.T, got <-chan func()) func() {
	t.Helper()
	select {
	case release := <-got:
		return release
	case <-time.After(waitLimit):
		t.Fatalf("Acquire no volvió en %v", waitLimit)
		return nil
	}
}

// TestKPerSlot_IsOne: EL ENTERO del Mecanismo 1 (R-01). Con 2, un turno interactivo podría
// quedar detrás de dos llamadas de lote.
func TestKPerSlot_IsOne(t *testing.T) {
	if pipeline.KPerSlot != 1 {
		t.Fatalf("KPerSlot = %d, se esperaba 1", pipeline.KPerSlot)
	}
}

// TestSlot_Valid_NeedsBothHalves: sin Edge el entero sería por tenant; sin tenant, dos
// Edges de tenants distintos podrían colisionar.
func TestSlot_Valid_NeedsBothHalves(t *testing.T) {
	cases := []struct {
		slot pipeline.Slot
		want bool
	}{
		{pipeline.Slot{TenantID: "t", EdgeID: "e"}, true},
		{pipeline.Slot{TenantID: "t"}, false},
		{pipeline.Slot{EdgeID: "e"}, false},
		{pipeline.Slot{}, false},
	}
	for _, c := range cases {
		if got := c.slot.Valid(); got != c.want {
			t.Errorf("%+v.Valid() = %v, se esperaba %v", c.slot, got, c.want)
		}
	}
}

// TestSlot_String_IsTenantSlashEdge: la forma que sale por el log.
func TestSlot_String_IsTenantSlashEdge(t *testing.T) {
	if got := slotA.String(); got != "tenant-1/edge-1" {
		t.Fatalf("String() = %q, se esperaba %q", got, "tenant-1/edge-1")
	}
}

// TestSlots_IsAOneMethodPort: el puerto es de UN método con la firma que el selector de
// vía ya tiene (`PlazaDe`), para que lo satisfaga sin que este paquete lo importe.
func TestSlots_IsAOneMethodPort(t *testing.T) {
	var port pipeline.Slots = &fakeSlots{edges: map[string]string{"s": "edge-9"}}
	edge, ok, err := port.PlazaDe(context.Background(), "t", "s")
	if edge != "edge-9" || !ok || err != nil {
		t.Fatalf("PlazaDe = (%q, %v, %v), se esperaba (edge-9, true, nil)", edge, ok, err)
	}
}

// TestCapacity_Acquire_FreeSlot_ReturnsAtOnceWithoutCountingAsWaiting: quien coge la plaza
// sin esperar NO cuenta como esperando, ni mientras la toma ni después.
func TestCapacity_Acquire_FreeSlot_ReturnsAtOnceWithoutCountingAsWaiting(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	if got := c.Waiting(); got != 0 {
		t.Fatalf("un aforo nuevo dice Waiting() = %d", got)
	}
	release := mustAcquire(t, c, slotA)
	if got := c.Waiting(); got != 0 {
		t.Fatalf("Waiting() = %d con la plaza tomada sin esperar, se esperaba 0", got)
	}
	release()
}

// TestCapacity_Acquire_FullSlot_WaitsUntilItIsReleased: el segundo ESPERA —no falla— y
// entra en cuanto el primero suelta.
func TestCapacity_Acquire_FullSlot_WaitsUntilItIsReleased(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	first := mustAcquire(t, c, slotA)
	second := acquireInBackground(context.Background(), c, slotA)

	eventually(t, "la segunda cadena esperando plaza", func() bool { return c.Waiting() == 1 })
	select {
	case <-second:
		t.Fatal("la segunda cadena tomó una plaza LLENA")
	default:
	}

	first()
	release := receive(t, second)
	if release == nil {
		t.Fatal("soltada la plaza, la segunda cadena debía tomarla")
	}
	if got := c.Waiting(); got != 0 {
		t.Fatalf("Waiting() = %d con la que esperaba ya dentro, se esperaba 0", got)
	}
	release()
}

// TestCapacity_Acquire_DifferentSlotsDoNotGetInEachOthersWay: cada dirección tiene su
// entero, y la dirección son las DOS mitades: mismo Edge con otro tenant es otra plaza.
func TestCapacity_Acquire_DifferentSlotsDoNotGetInEachOthersWay(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	defer mustAcquire(t, c, slotA)()
	defer mustAcquire(t, c, slotB)()
	defer mustAcquire(t, c, pipeline.Slot{TenantID: "tenant-2", EdgeID: "edge-1"})()
	if got := c.Waiting(); got != 0 {
		t.Fatalf("Waiting() = %d con tres direcciones distintas, se esperaba 0", got)
	}
}

// TestCapacity_Acquire_ContextCancelledWhileWaiting_ReturnsItsError: quien se rinde recibe
// el error de su ctx y NINGUNA función de soltar, deja de contar como esperando, y la
// plaza sigue siendo de quien la tenía.
func TestCapacity_Acquire_ContextCancelledWhileWaiting_ReturnsItsError(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	held := mustAcquire(t, c, slotA)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	var gotRelease atomic.Bool
	go func() {
		release, err := c.Acquire(ctx, slotA)
		gotRelease.Store(release != nil)
		result <- err
	}()
	eventually(t, "la cadena esperando plaza", func() bool { return c.Waiting() == 1 })

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire devolvió %v, se esperaba context.Canceled", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Acquire no volvió tras cancelar su ctx")
	}
	if gotRelease.Load() {
		t.Error("Acquire devolvió una función de soltar sin haber tomado la plaza")
	}
	if got := c.Waiting(); got != 0 {
		t.Errorf("Waiting() = %d tras rendirse, se esperaba 0", got)
	}

	// La plaza no se movió: sigue llena hasta que su dueño la suelta.
	other := acquireInBackground(context.Background(), c, slotA)
	eventually(t, "otra cadena esperando la misma plaza", func() bool { return c.Waiting() == 1 })
	held()
	receive(t, other)()
}

// TestCapacity_Acquire_CancelledContextButFreeSlot_StillGetsIt: el intento sin bloqueo va
// PRIMERO; el ctx solo decide cuando hay que esperar.
func TestCapacity_Acquire_CancelledContextButFreeSlot_StillGetsIt(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Muchas veces: si el ctx compitiera con la plaza, ganaría la mitad de ellas.
	for i := range 200 {
		release, err := c.Acquire(ctx, slotA)
		if err != nil || release == nil {
			t.Fatalf("vuelta %d: Acquire con la plaza LIBRE = (release nil: %v, %v), se esperaba la plaza", i, release == nil, err)
		}
		if got := c.Waiting(); got != 0 {
			t.Fatalf("vuelta %d: Waiting() = %d sin haber esperado", i, got)
		}
		release()
	}
}

// TestCapacity_Release_IsIdempotent: una liberación de más le quitaría el sitio a otro y
// dejaría DOS cadenas sobre la misma plaza, sin un solo error.
func TestCapacity_Release_IsIdempotent(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	first := mustAcquire(t, c, slotA)
	first()
	second := mustAcquire(t, c, slotA) // ahora la plaza es de la segunda
	first()                            // de más: no puede liberar la plaza de la segunda

	third := acquireInBackground(context.Background(), c, slotA)
	eventually(t, "la tercera cadena esperando", func() bool { return c.Waiting() == 1 })
	select {
	case <-third:
		t.Fatal("la liberación repetida soltó la plaza de OTRA cadena")
	default:
	}
	second()
	receive(t, third)()
}

// TestNewCapacity_K_IsThePerSlotLimit: con `k` plazas entran `k` y el siguiente espera; un
// `k <= 0` cae a KPerSlot (un aforo de cero no dejaría pasar a NADIE).
func TestNewCapacity_K_IsThePerSlotLimit(t *testing.T) {
	cases := []struct{ k, fits int }{{1, 1}, {2, 2}, {3, 3}, {0, 1}, {-4, 1}}
	for _, c := range cases {
		capacity := pipeline.NewCapacity(c.k)
		var releases []func()
		for range c.fits {
			releases = append(releases, mustAcquire(t, capacity, slotA))
		}
		if got := capacity.Waiting(); got != 0 {
			t.Fatalf("k=%d: Waiting() = %d con %d plazas tomadas, se esperaba 0", c.k, got, c.fits)
		}
		extra := acquireInBackground(context.Background(), capacity, slotA)
		eventually(t, "la cadena de más esperando", func() bool { return capacity.Waiting() == 1 })
		select {
		case <-extra:
			t.Fatalf("k=%d: entró una cadena de más (la %d)", c.k, c.fits+1)
		default:
		}
		releases[0]()
		receive(t, extra)()
		for _, release := range releases[1:] {
			release()
		}
	}
}

// TestCapacity_Waiting_AddsUpEverySlot: el contador suma todas las direcciones.
func TestCapacity_Waiting_AddsUpEverySlot(t *testing.T) {
	c := pipeline.NewCapacity(pipeline.KPerSlot)
	heldA, heldB := mustAcquire(t, c, slotA), mustAcquire(t, c, slotB)
	waiters := []<-chan func(){
		acquireInBackground(context.Background(), c, slotA),
		acquireInBackground(context.Background(), c, slotA),
		acquireInBackground(context.Background(), c, slotB),
	}
	eventually(t, "las tres cadenas esperando", func() bool { return c.Waiting() == 3 })

	heldB()
	receive(t, waiters[2])()
	eventually(t, "quedan las dos de la otra plaza", func() bool { return c.Waiting() == 2 })
	heldA()
	// Las dos que quedan entran de una en una, en el orden que sea.
	entered := 0
	for entered < 2 {
		select {
		case release := <-waiters[0]:
			release()
			entered++
		case release := <-waiters[1]:
			release()
			entered++
		case <-time.After(waitLimit):
			t.Fatalf("solo entraron %d de las 2 cadenas que esperaban", entered)
		}
	}
	if got := c.Waiting(); got != 0 {
		t.Fatalf("Waiting() = %d al final, se esperaba 0", got)
	}
}

// TestCapacity_ConcurrentUse_NeverExceedsK: muchas cadenas sobre pocas plazas, con -race:
// en ningún instante hay más de K dentro de la misma dirección, y todas acaban entrando.
func TestCapacity_ConcurrentUse_NeverExceedsK(t *testing.T) {
	const k, chains = 2, 64
	c := pipeline.NewCapacity(k)
	slots := []pipeline.Slot{slotA, slotB}
	var inside [2]atomic.Int32
	var worst atomic.Int32
	var wg sync.WaitGroup
	for i := range chains {
		wg.Go(func() {
			which := i % len(slots)
			release, err := c.Acquire(context.Background(), slots[which])
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			if now := inside[which].Add(1); now > worst.Load() {
				worst.Store(now)
			}
			inside[which].Add(-1)
			release()
		})
	}
	wg.Wait()
	if got := worst.Load(); got > k {
		t.Fatalf("llegó a haber %d cadenas dentro de una plaza de %d", got, k)
	}
	if got := c.Waiting(); got != 0 {
		t.Fatalf("Waiting() = %d al final, se esperaba 0", got)
	}
}
