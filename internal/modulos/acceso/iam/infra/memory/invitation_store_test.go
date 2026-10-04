package memory_test

import (
	"context"
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
	_ out.InvitationRepo = (*memory.InvitationStore)(nil)

	_ func() *memory.InvitationStore                                                               = memory.NewInvitationStore
	_ func(*memory.InvitationStore, func() time.Time) *memory.InvitationStore                      = (*memory.InvitationStore).WithClock
	_ func(*memory.InvitationStore, context.Context, domain.Invitation) (domain.Invitation, error) = (*memory.InvitationStore).Create
	_ func(*memory.InvitationStore, context.Context, string) ([]domain.Invitation, error)          = (*memory.InvitationStore).ListByTenant
	_ func(*memory.InvitationStore, context.Context, string, string) error                         = (*memory.InvitationStore).Revoke
	_ func(*memory.InvitationStore, domain.Invitation) domain.Invitation                           = (*memory.InvitationStore).Seed
	_ func(*memory.InvitationStore, string) (domain.Invitation, bool)                              = (*memory.InvitationStore).Get
	_ func(*memory.InvitationStore, string)                                                        = (*memory.InvitationStore).DetachRole
)

// invitationTables es InvitationTables sobre los ayudantes del doble: Seed tal cual y el borrado
// del rol como DetachRole (el doble no tiene tabla de roles).
type invitationTables struct{ store *memory.InvitationStore }

func (it invitationTables) Seed(_ *testing.T, inv domain.Invitation) domain.Invitation {
	return it.store.Seed(inv)
}

func (it invitationTables) DeleteRole(_ *testing.T, roleID string) { it.store.DetachRole(roleID) }

// invitationMontaje monta la suite sobre un InvitationStore: en memoria el rol no necesita
// existir en ninguna tabla, basta un UUID.
func invitationMontaje(store *memory.InvitationStore) outhelpertest.MontajeInvitationRepo {
	return outhelpertest.MontajeInvitationRepo{
		Repo:    store,
		TenantA: uuid.NewString(),
		TenantB: uuid.NewString(),
		RoleA:   uuid.NewString(),
		Tables:  invitationTables{store: store},
	}
}

// TestInvitationStore_Contrato corre la suite del puerto contra el doble, con el reloj de prueba.
func TestInvitationStore_Contrato(t *testing.T) {
	outhelpertest.ContratoInvitationRepo(t, func(*testing.T) outhelpertest.MontajeInvitationRepo {
		return invitationMontaje(memory.NewInvitationStore().WithClock(tickingClock()))
	})
}

// TestInvitationStore_HelpersAndClock: Create y Revoke fechan con el reloj inyectado; Get lee la
// fila escrita (y no encuentra la que no existe); Seed conserva id y created_at que trae, y no
// valida el digest.
func TestInvitationStore_HelpersAndClock(t *testing.T) {
	ctx := context.Background()
	store := memory.NewInvitationStore().WithClock(func() time.Time { return clockStart }).WithClock(nil)
	tenant := uuid.NewString()
	created, err := store.Create(ctx, domain.Invitation{TenantID: tenant, TokenHash: domain.HashInvitationToken("x"), CreatedBy: uuid.NewString()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.CreatedAt.Equal(clockStart) {
		t.Fatalf("created_at = %v, quiero el del reloj inyectado %v", created.CreatedAt, clockStart)
	}
	if err := store.Revoke(ctx, created.ID, tenant); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, ok := store.Get(created.ID)
	if !ok || got.RevokedAt == nil || !got.RevokedAt.Equal(clockStart) {
		t.Fatalf("Get tras Revoke = %+v, %v; quiero revoked_at del reloj inyectado", got, ok)
	}
	if _, ok := store.Get(uuid.NewString()); ok {
		t.Fatal("Get de un id que no existe devolvió ok=true")
	}
	id := uuid.NewString()
	seeded := store.Seed(domain.Invitation{ID: id, TenantID: tenant, TokenHash: []byte("corto"), CreatedAt: clockStart.Add(-time.Hour)})
	if seeded.ID != id || !seeded.CreatedAt.Equal(clockStart.Add(-time.Hour)) {
		t.Fatalf("Seed = %+v, quiero id y created_at conservados", seeded)
	}
}
