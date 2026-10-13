package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Este test es EXTERNO (package memory_test): prueba el doble solo por sus exportados y por la
// suite del puerto, como lo usarán los tests de los usecases.

// Las firmas del doble: es un out.ActiveTenantRepo y sus dos métodos son los del puerto.
var (
	_ out.ActiveTenantRepo = (*memory.ActiveTenantStore)(nil)

	_ func() *memory.ActiveTenantStore                                               = memory.NewActiveTenantStore
	_ func(*memory.ActiveTenantStore, context.Context, string) (string, bool, error) = (*memory.ActiveTenantStore).ActiveTenantOf
	_ func(*memory.ActiveTenantStore, context.Context, string, string) error         = (*memory.ActiveTenantStore).SetActiveTenant
)

// activeTenantMontaje monta la suite sobre un ActiveTenantStore: la tabla no tiene FK que exigir
// en memoria, así que los tenants solo tienen que ser dos UUID.
func activeTenantMontaje(store *memory.ActiveTenantStore) outhelpertest.MontajeActiveTenantRepo {
	return outhelpertest.MontajeActiveTenantRepo{Repo: store, TenantA: uuid.NewString(), TenantB: uuid.NewString()}
}

// TestActiveTenantStore_Contrato corre la suite del puerto contra el doble.
func TestActiveTenantStore_Contrato(t *testing.T) {
	outhelpertest.ContratoActiveTenantRepo(t, func(*testing.T) outhelpertest.MontajeActiveTenantRepo {
		return activeTenantMontaje(memory.NewActiveTenantStore())
	})
}
