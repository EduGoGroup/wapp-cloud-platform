package tenantvars_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars/tenantvarshelpertest"
)

// Este test es EXTERNO (package tenantvars_test): tenantvarshelpertest importa tenantvars, así
// que un test interno que importara la suite daría un ciclo de imports.

// Aserciones de compilación de lo que MemoryStore promete: las firmas del puerto y un
// constructor sin argumentos ni error.
var (
	_ func() *tenantvars.MemoryStore                                                        = tenantvars.NewMemoryStore
	_ func(*tenantvars.MemoryStore, func() time.Time)                                       = (*tenantvars.MemoryStore).SetClock
	_ func(*tenantvars.MemoryStore, context.Context, string) ([]tenantvars.Variable, error) = (*tenantvars.MemoryStore).List
	_ func(*tenantvars.MemoryStore, context.Context, string, map[string]string) error       = (*tenantvars.MemoryStore).Replace
)

// testClock es un reloj que solo avanza cuando el test lo mueve.
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

// memoryCases numera los Montajes para que cada caso tenga sus dos tenants.
var memoryCases atomic.Int64

// newMemoryStore devuelve un store vacío con un reloj de test inyectado.
func newMemoryStore() (*tenantvars.MemoryStore, *testClock) {
	clock := newTestClock()
	store := tenantvars.NewMemoryStore()
	store.SetClock(clock.Now)
	return store, clock
}

// TestMemoryStore_Contrato corre la suite del puerto contra MemoryStore, sin BD y sin reloj
// real: cada caso monta un store nuevo con su reloj, y Advance lo adelanta un segundo.
func TestMemoryStore_Contrato(t *testing.T) {
	tenantvarshelpertest.Contrato(t, func(*testing.T) tenantvarshelpertest.Montaje {
		store, clock := newMemoryStore()
		n := memoryCases.Add(1)
		return tenantvarshelpertest.Montaje{
			Store:   store,
			TenantA: fmt.Sprintf("tenant-%02d-a", n),
			TenantB: fmt.Sprintf("tenant-%02d-b", n),
			Advance: func(*testing.T) { clock.Advance(time.Second) },
		}
	})
}

// TestNewMemoryStore_IsEmptyAndReady: recién construido no tiene variables de nadie, y se puede
// escribir en él sin inyectarle reloj (la marca, la del reloj del proceso, no es cero).
func TestNewMemoryStore_IsEmptyAndReady(t *testing.T) {
	store := tenantvars.NewMemoryStore()
	if store == nil {
		t.Fatal("NewMemoryStore devolvió nil")
	}
	if got := listVars(t, store, "tenant-1"); got == nil || len(got) != 0 {
		t.Errorf("List de un store nuevo = %#v, quería un slice vacío no nil", got)
	}
	replaceVars(t, store, "tenant-1", map[string]string{"currency": "Bs"})
	got := listVars(t, store, "tenant-1")
	if len(got) != 1 || got[0].Key != "currency" || got[0].Value != "Bs" {
		t.Fatalf("List tras el primer Replace = %+v, quería (currency, Bs)", got)
	}
	if got[0].UpdatedAt.IsZero() {
		t.Error("sin SetClock, la variable guardada trae UpdatedAt cero")
	}
}

// TestMemoryStore_SetClock_MarksWithTheInjectedClock: el alta y el cambio llevan EXACTAMENTE el
// instante del reloj inyectado; lo que no cambia conserva el suyo; y un SetClock posterior
// sustituye al reloj sin remarcar lo guardado.
func TestMemoryStore_SetClock_MarksWithTheInjectedClock(t *testing.T) {
	store, clock := newMemoryStore()
	first := clock.Now()
	replaceVars(t, store, "tenant-1", map[string]string{"currency": "Bs", "shipping": "free"})
	for _, v := range listVars(t, store, "tenant-1") {
		if !v.UpdatedAt.Equal(first) {
			t.Errorf("alta de %q marcada con %v, quería el instante del reloj, %v", v.Key, v.UpdatedAt, first)
		}
	}

	other := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store.SetClock(func() time.Time { return other })
	if got := listVars(t, store, "tenant-1"); !got[0].UpdatedAt.Equal(first) || !got[1].UpdatedAt.Equal(first) {
		t.Errorf("SetClock remarcó lo ya guardado: %+v", got)
	}
	replaceVars(t, store, "tenant-1", map[string]string{"currency": "USD", "shipping": "free"})
	got := listVars(t, store, "tenant-1")
	if !got[0].UpdatedAt.Equal(other) {
		t.Errorf("currency cambió con el reloj nuevo y lleva %v, quería %v", got[0].UpdatedAt, other)
	}
	if !got[1].UpdatedAt.Equal(first) {
		t.Errorf("shipping no cambió y lleva %v, quería conservar %v", got[1].UpdatedAt, first)
	}
}

// TestMemoryStore_List_ReturnsTheCallersCopy: lo que List devuelve es del llamante; pisarlo no
// cambia lo guardado.
func TestMemoryStore_List_ReturnsTheCallersCopy(t *testing.T) {
	store, _ := newMemoryStore()
	replaceVars(t, store, "tenant-1", map[string]string{"currency": "Bs"})
	got := listVars(t, store, "tenant-1")
	got[0].Value = "pisado"
	if again := listVars(t, store, "tenant-1"); again[0].Value != "Bs" {
		t.Errorf("pisar el slice de List cambió lo guardado: currency = %q, quería Bs", again[0].Value)
	}
}

// TestMemoryStore_Replace_DoesNotKeepTheCallersMap: el store copia el mapa; cambiarlo después
// del Replace no cambia lo guardado.
func TestMemoryStore_Replace_DoesNotKeepTheCallersMap(t *testing.T) {
	store, _ := newMemoryStore()
	vars := map[string]string{"currency": "Bs"}
	replaceVars(t, store, "tenant-1", vars)
	vars["currency"] = "pisado"
	vars["late"] = "x"
	got := listVars(t, store, "tenant-1")
	if len(got) != 1 || got[0].Value != "Bs" {
		t.Errorf("cambiar el mapa tras el Replace cambió lo guardado: %+v", got)
	}
}

// TestMemoryStore_ConcurrentUse: escritores, lectores y SetClock a la vez, sobre el mismo tenant
// y sobre otros. Bajo -race, un store sin proteger se ve aquí; y al terminar, cada tenant tiene
// justo su último conjunto.
func TestMemoryStore_ConcurrentUse(t *testing.T) {
	const workers, rounds = 8, 50
	store, clock := newMemoryStore()
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*2)
	for w := range workers {
		tenant := fmt.Sprintf("tenant-%d", w)
		wg.Go(func() {
			for r := range rounds {
				store.SetClock(clock.Now)
				vars := map[string]string{"round": fmt.Sprint(r), "worker": tenant}
				errs <- store.Replace(context.Background(), tenant, vars)
				errs <- store.Replace(context.Background(), "shared", map[string]string{"currency": "Bs"})
				if _, err := store.List(context.Background(), "shared"); err != nil {
					errs <- err
				}
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
	for w := range workers {
		tenant := fmt.Sprintf("tenant-%d", w)
		got := listVars(t, store, tenant)
		if len(got) != 2 || got[0].Value != fmt.Sprint(rounds-1) || got[1].Value != tenant {
			t.Errorf("variables de %q = %+v, quería (round=%d, worker=%s)", tenant, got, rounds-1, tenant)
		}
	}
	if got := listVars(t, store, "shared"); len(got) != 1 || got[0].Value != "Bs" {
		t.Errorf("variables del tenant compartido = %+v, quería (currency, Bs)", got)
	}
}

// replaceVars reemplaza el conjunto del tenant o falla el test.
func replaceVars(t *testing.T, store tenantvars.Store, tenant string, vars map[string]string) {
	t.Helper()
	if err := store.Replace(context.Background(), tenant, vars); err != nil {
		t.Fatalf("Replace(%q, %v): error inesperado %v", tenant, vars, err)
	}
}

// listVars lee las variables del tenant o falla el test.
func listVars(t *testing.T, store tenantvars.Store, tenant string) []tenantvars.Variable {
	t.Helper()
	got, err := store.List(context.Background(), tenant)
	if err != nil {
		t.Fatalf("List(%q): error inesperado %v", tenant, err)
	}
	return got
}
