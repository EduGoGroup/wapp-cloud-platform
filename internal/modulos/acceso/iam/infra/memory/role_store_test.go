package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del doble.
var (
	_ out.RoleRepo = (*memory.RoleStore)(nil)

	_ func() *memory.RoleStore                                                        = memory.NewRoleStore
	_ func(*memory.RoleStore, func() time.Time) *memory.RoleStore                     = (*memory.RoleStore).WithClock
	_ func(*memory.RoleStore, domain.Role, []domain.Grant) domain.Role                = (*memory.RoleStore).Seed
	_ func(*memory.RoleStore, context.Context, domain.Role) (domain.Role, error)      = (*memory.RoleStore).Create
	_ func(*memory.RoleStore, context.Context, string) (domain.Role, error)           = (*memory.RoleStore).GetByID
	_ func(*memory.RoleStore, context.Context, string) ([]domain.Role, error)         = (*memory.RoleStore).List
	_ func(*memory.RoleStore, context.Context, string) (string, bool, error)          = (*memory.RoleStore).ParentOf
	_ func(*memory.RoleStore, context.Context, string) ([]domain.Grant, error)        = (*memory.RoleStore).GrantsOf
	_ func(*memory.RoleStore, context.Context, string, domain.Grant) error            = (*memory.RoleStore).AddGrant
	_ func(*memory.RoleStore, context.Context, string, domain.Grant) error            = (*memory.RoleStore).RemoveGrant
	_ func(*memory.RoleStore, context.Context, string, string) ([]domain.Role, error) = (*memory.RoleStore).RolesOfUser
	_ func(*memory.RoleStore, context.Context, string, string, *string) error         = (*memory.RoleStore).AssignToUser
	_ func(*memory.RoleStore, context.Context, string, string, *string) error         = (*memory.RoleStore).UnassignFromUser
	_ func(*memory.RoleStore, string, string, *string)                                = (*memory.RoleStore).SeedAssignment
	_ func(*memory.RoleStore, string) []memory.Assignment                             = (*memory.RoleStore).AssignmentsOf
)

// seedTransversal siembra el rol transversal como plantilla global, como lo dejan las
// migraciones en Postgres (0059).
func seedTransversal(store *memory.RoleStore) {
	store.Seed(domain.Role{ID: domain.TransversalRoleID, Name: "platform_admin"}, nil)
}

// roleMontaje monta la suite sobre un RoleStore con el rol transversal sembrado.
func roleMontaje(store *memory.RoleStore) outhelpertest.MontajeRoleRepo {
	seedTransversal(store)
	return outhelpertest.MontajeRoleRepo{Repo: store, TenantA: uuid.NewString(), TenantB: uuid.NewString()}
}

// TestRoleStore_Contrato corre la suite del puerto contra el doble, con el reloj de prueba.
func TestRoleStore_Contrato(t *testing.T) {
	outhelpertest.ContratoRoleRepo(t, func(*testing.T) outhelpertest.MontajeRoleRepo {
		return roleMontaje(memory.NewRoleStore().WithClock(tickingClock()))
	})
}

// TestRoleStore_SeedAssignmentAndAssignmentsOf: SeedAssignment fabrica la asignación global de un
// rol de empresa que AssignToUser rechaza (filas así existen en UAT), sin duplicar, y
// AssignmentsOf deja ver el ámbito de cada una; el reloj inyectado fecha lo sembrado.
func TestRoleStore_SeedAssignmentAndAssignmentsOf(t *testing.T) {
	ctx := context.Background()
	store := memory.NewRoleStore().WithClock(func() time.Time { return clockStart }).WithClock(nil)
	tenant := uuid.NewString()
	role := store.Seed(domain.Role{TenantID: &tenant, Name: "admin"}, []domain.Grant{{Pattern: "*", Effect: domain.EffectAllow}})
	if !role.CreatedAt.Equal(clockStart) {
		t.Fatalf("Seed: created_at = %v, quiero el del reloj inyectado", role.CreatedAt)
	}
	user := uuid.NewString()
	if err := store.AssignToUser(ctx, user, role.ID, nil); !errors.Is(err, domain.ErrRoleScopeInvalid) {
		t.Fatalf("AssignToUser global de un rol de empresa: err = %v, quiero ErrRoleScopeInvalid", err)
	}
	store.SeedAssignment(user, role.ID, nil)
	store.SeedAssignment(user, role.ID, nil)
	if err := store.AssignToUser(ctx, user, role.ID, &tenant); err != nil {
		t.Fatalf("AssignToUser acotado: %v", err)
	}
	got := store.AssignmentsOf(user)
	if len(got) != 2 || got[0].RoleID != role.ID || got[0].TenantID != nil || got[1].TenantID == nil || *got[1].TenantID != tenant {
		t.Fatalf("AssignmentsOf = %+v, quiero [global, acotada a %s]", got, tenant)
	}
	roles, err := store.RolesOfUser(ctx, user, uuid.NewString())
	if err != nil || len(roles) != 1 || roles[0].ID != role.ID {
		t.Fatalf("RolesOfUser en otra empresa = %+v, %v; quiero el rol por su asignación global sembrada", roles, err)
	}
}
