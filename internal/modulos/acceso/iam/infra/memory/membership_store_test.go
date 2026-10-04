package memory_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del doble.
var (
	_ out.MembershipRepo     = (*memory.MembershipStore)(nil)
	_ memory.FeatureResolver = (*entitlementshelpertest.Fake)(nil)
	_ memory.FeatureResolver = entitlements.Resolver(nil)

	_ func() *memory.MembershipStore                                                      = memory.NewMembershipStore
	_ func(*memory.MembershipStore, func() time.Time) *memory.MembershipStore             = (*memory.MembershipStore).WithClock
	_ func(*memory.MembershipStore, memory.FeatureResolver) *memory.MembershipStore       = (*memory.MembershipStore).WithFeatures
	_ func(*memory.MembershipStore, string, string)                                       = (*memory.MembershipStore).SeedTenantName
	_ func(*memory.MembershipStore, string, string)                                       = (*memory.MembershipStore).Seed
	_ func(*memory.MembershipStore, context.Context, string, string) error                = (*memory.MembershipStore).Add
	_ func(*memory.MembershipStore, context.Context, string, string) error                = (*memory.MembershipStore).Remove
	_ func(*memory.MembershipStore, context.Context, string) ([]string, error)            = (*memory.MembershipStore).TenantsOfUser
	_ func(*memory.MembershipStore, context.Context, string) ([]domain.UserTenant, error) = (*memory.MembershipStore).UserTenants
	_ func(*memory.MembershipStore, context.Context, string) ([]domain.Membership, error) = (*memory.MembershipStore).MembersOf
)

// errResolverDown es el fallo de infraestructura con que BreakResolver tira el Fake.
var errResolverDown = errors.New("resolver de entitlements caído (prueba)")

// fakeSwitch es outhelpertest.FeatureSwitch sobre un entitlementshelpertest.Fake.
type fakeSwitch struct{ fake *entitlementshelpertest.Fake }

func (f fakeSwitch) GrantMultiCompany(_ *testing.T, tenantID string) {
	f.fake.Enable(tenantID, entitlements.FeatureMultiCompany)
}

func (f fakeSwitch) BreakResolver(*testing.T) { f.fake.Err = errResolverDown }

// membershipMontaje monta la suite sobre un MembershipStore ya atado a fake: siembra los nombres
// de los dos tenants (la otra tabla del JOIN de UserTenants).
func membershipMontaje(store *memory.MembershipStore, fake *entitlementshelpertest.Fake) outhelpertest.MontajeMembershipRepo {
	m := outhelpertest.MontajeMembershipRepo{
		Repo:     store,
		TenantA:  uuid.NewString(),
		TenantB:  uuid.NewString(),
		NameA:    "Empresa A",
		NameB:    "Empresa B",
		Features: fakeSwitch{fake: fake},
	}
	store.SeedTenantName(m.TenantA, m.NameA)
	store.SeedTenantName(m.TenantB, m.NameB)
	return m
}

// TestMembershipStore_Contrato corre la suite del puerto contra el doble, con el reloj de prueba.
func TestMembershipStore_Contrato(t *testing.T) {
	outhelpertest.ContratoMembershipRepo(t, func(*testing.T) outhelpertest.MontajeMembershipRepo {
		fake := entitlementshelpertest.NewFake()
		return membershipMontaje(memory.NewMembershipStore().WithClock(tickingClock()).WithFeatures(fake), fake)
	})
}

// TestMembershipStore_NoResolverKeepsTheGuardClosed: sin WithFeatures nadie tiene multi_empresa
// (el extremo fail-closed), y Seed sí fabrica la segunda empresa que Add rechaza. Con el reloj
// parado, el empate de created_at lo desempata el tenant_id, como el ORDER BY de Postgres.
func TestMembershipStore_NoResolverKeepsTheGuardClosed(t *testing.T) {
	ctx := context.Background()
	store := memory.NewMembershipStore().WithClock(func() time.Time { return clockStart }).WithClock(nil)
	user := uuid.NewString()
	first, second := "ffffffff-0000-0000-0000-000000000000", "00000000-0000-0000-0000-00000000000f"
	if err := store.Add(ctx, user, first); err != nil {
		t.Fatalf("primera membresía: %v", err)
	}
	if err := store.Add(ctx, user, second); !errors.Is(err, domain.ErrConflict) || err.Error() != outhelpertest.SecondCompanyConflictText {
		t.Fatalf("segunda empresa sin resolver: err = %v, quiero %q", err, outhelpertest.SecondCompanyConflictText)
	}
	store.Seed(user, second)
	store.Seed(user, second)
	tenants, err := store.TenantsOfUser(ctx, user)
	if err != nil || !slices.Equal(tenants, []string{second, first}) {
		t.Fatalf("TenantsOfUser = %v, %v; quiero [%s %s]: Seed salta la guarda, no duplica, y el empate lo desempata el tenant_id", tenants, err, second, first)
	}
}
