//go:build pendiente

package trigger_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger/triggerhelpertest"
)

// Este test es EXTERNO (package trigger_test): triggerhelpertest importa trigger, así que un test
// interno que importara la suite daría un ciclo de imports.

// Aserciones de compilación de lo que MemoryStore promete: que cumple el puerto, las firmas de
// sus cinco métodos y un constructor sin argumentos ni error.
var (
	_ trigger.Store                                                                                     = (*trigger.MemoryStore)(nil)
	_ func() *trigger.MemoryStore                                                                       = trigger.NewMemoryStore
	_ func(*trigger.MemoryStore, context.Context, trigger.Rule) (trigger.Rule, error)                   = (*trigger.MemoryStore).Insert
	_ func(*trigger.MemoryStore, context.Context, string) ([]trigger.Rule, error)                       = (*trigger.MemoryStore).List
	_ func(*trigger.MemoryStore, context.Context, string, string, trigger.Kind) ([]trigger.Rule, error) = (*trigger.MemoryStore).ListByKind
	_ func(*trigger.MemoryStore, context.Context, string, string) (trigger.Rule, error)                 = (*trigger.MemoryStore).Get
	_ func(*trigger.MemoryStore, context.Context, string, string) error                                 = (*trigger.MemoryStore).Delete
)

// TestMemoryStore_Contrato corre las promesas del puerto trigger.Store, las mismas que pasan
// contra PostgresStore, contra el gemelo en memoria. Cada caso recibe un store vacío y dos
// tenants nuevos. La memoria no guarda nada fuera de Rule: el Montaje va sin Hidden.
func TestMemoryStore_Contrato(t *testing.T) {
	triggerhelpertest.Contrato(t, func(*testing.T) triggerhelpertest.Montaje {
		return triggerhelpertest.Montaje{
			Store:   trigger.NewMemoryStore(),
			TenantA: uuid.NewString(),
			TenantB: uuid.NewString(),
		}
	})
}

// TestMemoryStore_AnyTriggerID_NotFoundInsteadOfSyntaxError: lo único en que la memoria NO imita
// a Postgres, dicho en su contrato: un trigger_id sin forma de UUID es «no existe», no un error
// de la base. Los dobles del runtime cuentan con ello.
func TestMemoryStore_AnyTriggerID_NotFoundInsteadOfSyntaxError(t *testing.T) {
	s := trigger.NewMemoryStore()
	ctx := context.Background()
	if _, err := s.Get(ctx, "tenant-1", "no-es-un-uuid"); err != trigger.ErrTriggerNotFound { //nolint:errorlint // el centinela sale sin envolver
		t.Errorf("Get con un id cualquiera: err = %v, quería ErrTriggerNotFound", err)
	}
	if err := s.Delete(ctx, "tenant-1", ""); err != trigger.ErrTriggerNotFound { //nolint:errorlint // el centinela sale sin envolver
		t.Errorf("Delete con el id vacío: err = %v, quería ErrTriggerNotFound", err)
	}
}

// TestMemoryStore_Insert_NeverFails: Insert no valida nada, ni la regla cero.
func TestMemoryStore_Insert_NeverFails(t *testing.T) {
	s := trigger.NewMemoryStore()
	out, err := s.Insert(context.Background(), trigger.Rule{})
	if err != nil {
		t.Fatalf("Insert de la regla cero: error inesperado %v", err)
	}
	if _, perr := uuid.Parse(out.TriggerID); perr != nil {
		t.Errorf("TriggerID asignado = %q, quería un UUID: %v", out.TriggerID, perr)
	}
}

// concurrentRound es una vuelta de una goroutine de TestMemoryStore_ConcurrentUse: inserta la
// regla i, la lee de las tres formas y, si i es par, la borra. Devuelve el primer error.
func concurrentRound(ctx context.Context, s *trigger.MemoryStore, tenant string, i int) error {
	out, err := s.Insert(ctx, trigger.Rule{TenantID: tenant, Kind: trigger.KindKeyword, Keyword: "k", Priority: i, Enabled: true})
	if err != nil {
		return err
	}
	if _, err := s.Get(ctx, tenant, out.TriggerID); err != nil {
		return err
	}
	if _, err := s.List(ctx, tenant); err != nil {
		return err
	}
	if _, err := s.ListByKind(ctx, tenant, "", trigger.KindKeyword); err != nil {
		return err
	}
	if i%2 != 0 {
		return nil
	}
	return s.Delete(ctx, tenant, out.TriggerID)
}

// TestMemoryStore_ConcurrentUse es la promesa «segura para concurrencia», para correr con -race:
// varios escritores y lectores sobre el mismo store y dos tenants. Cada goroutine inserta sus
// reglas, las lee de todas las formas y borra la mitad; al final quedan exactamente las que
// nadie borró, cada una en su tenant.
func TestMemoryStore_ConcurrentUse(t *testing.T) {
	const (
		workers = 8
		perWork = 50
	)
	s := trigger.NewMemoryStore()
	ctx := context.Background()
	tenants := []string{"tenant-even", "tenant-odd"}

	errs := make(chan error, workers*perWork)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range perWork {
				if err := concurrentRound(ctx, s, tenants[w%2], i); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("operación concurrente: error inesperado %v", err)
	}

	const wantPerTenant = (workers / 2) * (perWork / 2)
	for _, tenant := range tenants {
		got, err := s.List(ctx, tenant)
		if err != nil {
			t.Fatalf("List(%q): error inesperado %v", tenant, err)
		}
		if len(got) != wantPerTenant {
			t.Errorf("al tenant %q le quedan %d reglas, quería %d", tenant, len(got), wantPerTenant)
		}
		for _, r := range got {
			if r.TenantID != tenant || r.Priority%2 == 0 {
				t.Errorf("al tenant %q le queda la regla %+v: es de otro tenant o debía estar borrada", tenant, r)
			}
		}
	}
}
