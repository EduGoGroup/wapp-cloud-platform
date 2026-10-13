package memory_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del doble.
var (
	_ out.GrantRepo = (*memory.GrantStore)(nil)

	_ func() *memory.GrantStore                                                 = memory.NewGrantStore
	_ func(*memory.GrantStore, context.Context, string) ([]domain.Grant, error) = (*memory.GrantStore).GrantsOfUser
	_ func(*memory.GrantStore, context.Context, string, domain.Grant) error     = (*memory.GrantStore).AddUserGrant
	_ func(*memory.GrantStore, context.Context, string, domain.Grant) error     = (*memory.GrantStore).RemoveUserGrant
)

// TestGrantStore_Contrato corre la suite del puerto contra el doble.
func TestGrantStore_Contrato(t *testing.T) {
	outhelpertest.ContratoGrantRepo(t, func(*testing.T) outhelpertest.MontajeGrantRepo {
		return outhelpertest.MontajeGrantRepo{Repo: memory.NewGrantStore()}
	})
}
