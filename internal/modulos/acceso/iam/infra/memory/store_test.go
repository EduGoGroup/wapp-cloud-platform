package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del agregado.
var (
	_ func() *memory.Store                                = memory.NewStore
	_ func(*memory.Store, func() time.Time) *memory.Store = (*memory.Store).WithClock
)

// newClockedStore es un Store recién hecho con el reloj de prueba.
func newClockedStore() (*memory.Store, func() time.Time) {
	clock := tickingClock()
	return memory.NewStore().WithClock(clock), clock
}

// TestStore_Contrato corre las siete suites (outhelpertest.Contrato) contra los stores del
// agregado: cada fábrica construye un Store y monta su puerto con la pieza correspondiente. El
// canje se monta sobre Store.Redeem y los Invitations, Memberships y Roles del MISMO agregado, así
// que la suite del canje prueba también que Redeem está cableado a ellos.
func TestStore_Contrato(t *testing.T) {
	outhelpertest.Contrato(t, outhelpertest.Montajes{
		MembershipRepo: func(*testing.T) outhelpertest.MontajeMembershipRepo {
			st, _ := newClockedStore()
			fake := entitlementshelpertest.NewFake()
			st.Memberships.WithFeatures(fake)
			return membershipMontaje(st.Memberships, fake)
		},
		RoleRepo: func(*testing.T) outhelpertest.MontajeRoleRepo {
			st, _ := newClockedStore()
			return roleMontaje(st.Roles)
		},
		GrantRepo: func(*testing.T) outhelpertest.MontajeGrantRepo {
			st, _ := newClockedStore()
			return outhelpertest.MontajeGrantRepo{Repo: st.Grants}
		},
		AuditRepo: func(*testing.T) outhelpertest.MontajeAuditRepo {
			st, _ := newClockedStore()
			return auditMontaje(st.Audit)
		},
		InvitationRepo: func(*testing.T) outhelpertest.MontajeInvitationRepo {
			st, _ := newClockedStore()
			return invitationMontaje(st.Invitations)
		},
		ActiveTenantRepo: func(*testing.T) outhelpertest.MontajeActiveTenantRepo {
			st, _ := newClockedStore()
			return activeTenantMontaje(st.ActiveTenants)
		},
		InvitationRedeemRepo: func(*testing.T) outhelpertest.MontajeInvitationRedeemRepo {
			st, clock := newClockedStore()
			fake := entitlementshelpertest.NewFake()
			st.Memberships.WithFeatures(fake)
			return redeemMontajeOver(st.Invitations, st.Memberships, st.Roles, st.Redeem, fake, clock)
		},
	})
}

// TestStore_WithClockReachesEveryStamp: el reloj del agregado llega a los cuatro stores que
// fechan algo (alta de rol, evento, invitación y membresía).
func TestStore_WithClockReachesEveryStamp(t *testing.T) {
	ctx := context.Background()
	st := memory.NewStore().WithClock(func() time.Time { return clockStart })
	tenant, user := uuid.NewString(), uuid.NewString()

	role, err := st.Roles.Create(ctx, domain.Role{TenantID: &tenant, Name: "admin"})
	if err != nil {
		t.Fatalf("Roles.Create: %v", err)
	}
	if err := st.Audit.Record(ctx, domain.AuditEvent{TenantID: &tenant, Action: "a"}); err != nil {
		t.Fatalf("Audit.Record: %v", err)
	}
	inv, err := st.Invitations.Create(ctx, domain.Invitation{TenantID: tenant, TokenHash: domain.HashInvitationToken("x")})
	if err != nil {
		t.Fatalf("Invitations.Create: %v", err)
	}
	if err := st.Memberships.Add(ctx, user, tenant); err != nil {
		t.Fatalf("Memberships.Add: %v", err)
	}
	members, err := st.Memberships.MembersOf(ctx, tenant)
	if err != nil || len(members) != 1 {
		t.Fatalf("MembersOf = %+v, %v", members, err)
	}
	stamps := map[string]time.Time{
		"rol":        role.CreatedAt,
		"evento":     st.Audit.Events()[0].At,
		"invitación": inv.CreatedAt,
		"membresía":  members[0].CreatedAt,
	}
	for what, at := range stamps {
		if !at.Equal(clockStart) {
			t.Errorf("instante de %s = %v, quiero el reloj del agregado %v", what, at, clockStart)
		}
	}
}
