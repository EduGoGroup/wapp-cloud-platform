package memory_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Las firmas del doble.
var (
	_ out.InvitationRedeemRepo = (*memory.RedeemStore)(nil)

	_ func(*memory.InvitationStore, *memory.MembershipStore, *memory.RoleStore) *memory.RedeemStore = memory.NewRedeemStore
	_ func(*memory.RedeemStore, string)                                                             = (*memory.RedeemStore).SeedPendingAccessRequest
	_ func(*memory.RedeemStore, string) (memory.AccessRequest, bool)                                = (*memory.RedeemStore).AccessRequestOf
	_ func(*memory.RedeemStore, context.Context, []byte, string) error                              = (*memory.RedeemStore).Redeem
)

// redeemTables es outhelpertest.RedeemTables sobre los dobles: siembra con sus ayudantes y
// fotografía las cuatro «tablas» por ellos. now es el reloj compartido de los stores, el que
// hace de now() de la base para expires_at.
type redeemTables struct {
	invitations *memory.InvitationStore
	memberships *memory.MembershipStore
	roles       *memory.RoleStore
	redeem      *memory.RedeemStore
	tenants     []string
	now         func() time.Time
}

func (rt *redeemTables) SeedInvitation(_ *testing.T, seed outhelpertest.InvitationSeed) {
	now := rt.now().UTC()
	inv := domain.Invitation{
		TenantID:  seed.TenantID,
		TokenHash: seed.TokenHash,
		RoleID:    seed.RoleID,
		ExpiresAt: now.Add(seed.ExpiresIn),
		CreatedBy: uuid.NewString(),
	}
	if seed.RedeemedBy != "" {
		inv.RedeemedBy, inv.RedeemedAt = &seed.RedeemedBy, &now
	}
	if seed.Revoked {
		inv.RevokedAt = &now
	}
	rt.invitations.Seed(inv)
}

func (rt *redeemTables) SeedMembership(_ *testing.T, userID, tenantID string) {
	rt.memberships.Seed(userID, tenantID)
}

func (rt *redeemTables) SeedPendingAccessRequest(_ *testing.T, userID string) {
	rt.redeem.SeedPendingAccessRequest(userID)
}

func (rt *redeemTables) Snapshot(t *testing.T, tokenHash []byte, userID string) outhelpertest.RedeemState {
	t.Helper()
	ctx := context.Background()
	var state outhelpertest.RedeemState
	// La invitación del digest: el doble no indexa por digest, así que se busca en los listados
	// de los dos tenants del montaje.
	for _, tenant := range rt.tenants {
		invs, err := rt.invitations.ListByTenant(ctx, tenant)
		if err != nil {
			t.Fatalf("ListByTenant: %v", err)
		}
		for _, inv := range invs {
			if bytes.Equal(inv.TokenHash, tokenHash) {
				state.Invitation = &inv
			}
		}
	}
	tenants, err := rt.memberships.TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	for _, tenant := range tenants {
		members, err := rt.memberships.MembersOf(ctx, tenant)
		if err != nil {
			t.Fatalf("MembersOf: %v", err)
		}
		for _, member := range members {
			if member.UserID == userID {
				state.Memberships = append(state.Memberships, member)
			}
		}
	}
	for _, a := range rt.roles.AssignmentsOf(userID) {
		scope := ""
		if a.TenantID != nil {
			scope = *a.TenantID
		}
		state.Roles = append(state.Roles, outhelpertest.RoleAssignment{RoleID: a.RoleID, TenantID: scope})
	}
	if req, ok := rt.redeem.AccessRequestOf(userID); ok {
		state.AccessRequest = &outhelpertest.AccessRequestRow{Status: req.Status, DecidedAt: req.DecidedAt}
	}
	return state
}

// redeemMontaje monta la suite del canje sobre los tres dobles y el RedeemStore, con un mismo
// reloj de prueba y el Fake de entitlements.
func redeemMontaje() outhelpertest.MontajeInvitationRedeemRepo {
	clock := tickingClock()
	fake := entitlementshelpertest.NewFake()
	invitations := memory.NewInvitationStore().WithClock(clock)
	memberships := memory.NewMembershipStore().WithClock(clock).WithFeatures(fake)
	roles := memory.NewRoleStore().WithClock(clock)
	return redeemMontajeOver(invitations, memberships, roles, memory.NewRedeemStore(invitations, memberships, roles), fake, clock)
}

// redeemMontajeOver monta la suite del canje sobre stores ya construidos (lo reutiliza el test
// del agregado Store).
func redeemMontajeOver(invitations *memory.InvitationStore, memberships *memory.MembershipStore, roles *memory.RoleStore,
	redeem *memory.RedeemStore, fake *entitlementshelpertest.Fake, clock func() time.Time,
) outhelpertest.MontajeInvitationRedeemRepo {
	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	role := roles.Seed(domain.Role{TenantID: &tenantA, Name: "operator"}, nil)
	return outhelpertest.MontajeInvitationRedeemRepo{
		Repo:     redeem,
		TenantA:  tenantA,
		TenantB:  tenantB,
		RoleA:    role.ID,
		Features: fakeSwitch{fake: fake},
		Tables: &redeemTables{
			invitations: invitations,
			memberships: memberships,
			roles:       roles,
			redeem:      redeem,
			tenants:     []string{tenantA, tenantB},
			now:         clock,
		},
	}
}

// TestRedeemStore_Contrato corre la suite del puerto contra el doble.
func TestRedeemStore_Contrato(t *testing.T) {
	outhelpertest.ContratoInvitationRedeemRepo(t, func(*testing.T) outhelpertest.MontajeInvitationRedeemRepo {
		return redeemMontaje()
	})
}

// TestNewRedeemStore_NilStorePanics: un canje sin alguna de sus tablas es un error de cableado y
// el constructor lo dice en el acto.
func TestNewRedeemStore_NilStorePanics(t *testing.T) {
	inv, mem, rol := memory.NewInvitationStore(), memory.NewMembershipStore(), memory.NewRoleStore()
	for name, build := range map[string]func(){
		"invitations": func() { memory.NewRedeemStore(nil, mem, rol) },
		"memberships": func() { memory.NewRedeemStore(inv, nil, rol) },
		"roles":       func() { memory.NewRedeemStore(inv, mem, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewRedeemStore sin %s no entró en pánico", name)
				}
			}()
			build()
		})
	}
}
