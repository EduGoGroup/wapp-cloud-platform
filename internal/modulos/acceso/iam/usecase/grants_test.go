package usecase

import (
	"context"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// grants.go no tiene exportados: estos tests nacen en su verde (E-4, P6) y prueban las reglas
// de sus dos auxiliares (R-U32) sobre los dobles en memoria.

// grantsTenant devuelve un puntero nuevo a testTenant (el ámbito de los roles sembrados).
func grantsTenant() *string {
	v := testTenant
	return &v
}

// countingRoleRepo envuelve un RoleRepo y cuenta las lecturas de GrantsOf por rol: es como se
// ve que un rol compartido por dos cadenas se agrega UNA vez (el aplanado deduplica patrones, así
// que el resultado solo no lo distingue).
type countingRoleRepo struct {
	out.RoleRepo
	grantsReads map[string]int
}

func (c *countingRoleRepo) GrantsOf(ctx context.Context, roleID string) ([]domain.Grant, error) {
	c.grantsReads[roleID]++
	return c.RoleRepo.GrantsOf(ctx, roleID)
}

// R-U32: allow a Allow, deny a Deny, en el orden de entrada; los dos slices no nil aunque la
// entrada esté vacía (el token lleva `[]`, nunca `null`).
func TestGrantsToAuth_SplitsByEffectWithNonNilSlices(t *testing.T) {
	cases := []struct {
		name      string
		in        []domain.Grant
		wantAllow []string
		wantDeny  []string
	}{
		{"nil_input_gives_empty_not_nil", nil, []string{}, []string{}},
		{"only_allow_leaves_deny_empty", []domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}},
			[]string{"flows.*"}, []string{}},
		{"only_deny_leaves_allow_empty", []domain.Grant{{Pattern: "flows.delete", Effect: domain.EffectDeny}},
			[]string{}, []string{"flows.delete"}},
		{"mixed_keeps_input_order", []domain.Grant{
			{Pattern: "a.read", Effect: domain.EffectAllow},
			{Pattern: "a.delete", Effect: domain.EffectDeny},
			{Pattern: "b.read", Effect: domain.EffectAllow},
		}, []string{"a.read", "b.read"}, []string{"a.delete"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := grantsToAuth(c.in)
			if got.Allow == nil || got.Deny == nil {
				t.Fatalf("grantsToAuth = %#v; Allow y Deny tienen que salir no nil", got)
			}
			if !slices.Equal(got.Allow, c.wantAllow) || !slices.Equal(got.Deny, c.wantDeny) {
				t.Fatalf("grantsToAuth = %+v; quiere Allow=%v Deny=%v", got, c.wantAllow, c.wantDeny)
			}
		})
	}
}

// La herencia por parent_role_id agrega: el hijo asignado trae los grants de toda su cadena, y
// el nombre que se devuelve es el del rol asignado, no el de sus ancestros.
func TestResolveEffectiveGrants_InheritanceAggregatesTheChain(t *testing.T) {
	store := memory.NewStore()
	grand := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "grand"},
		[]domain.Grant{{Pattern: "audit.read", Effect: domain.EffectAllow}})
	parent := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "parent", ParentRoleID: &grand.ID},
		[]domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}})
	child := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "child", ParentRoleID: &parent.ID},
		[]domain.Grant{{Pattern: "intakes.read", Effect: domain.EffectAllow}})
	store.Roles.SeedAssignment("user-1", child.ID, grantsTenant())

	got, names, err := resolveEffectiveGrants(context.Background(), store.Roles, store.Grants, "user-1", testTenant)
	if err != nil {
		t.Fatalf("resolveEffectiveGrants: %v", err)
	}
	for _, p := range []string{"intakes.read", "contacts.read", "audit.read"} {
		if !slices.Contains(got.Allow, p) {
			t.Errorf("Allow = %v; falta %q de la cadena de herencia", got.Allow, p)
		}
	}
	if !slices.Equal(names, []string{"child"}) {
		t.Errorf("nombres = %v; quiere solo el rol asignado directamente [child]", names)
	}
}

// Un ancestro compartido por dos cadenas se lee y se agrega UNA vez.
func TestResolveEffectiveGrants_SharedRoleIsAggregatedOnce(t *testing.T) {
	store := memory.NewStore()
	shared := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "shared"},
		[]domain.Grant{{Pattern: "contacts.read", Effect: domain.EffectAllow}})
	left := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "left", ParentRoleID: &shared.ID},
		[]domain.Grant{{Pattern: "flows.read", Effect: domain.EffectAllow}})
	right := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "right", ParentRoleID: &shared.ID},
		[]domain.Grant{{Pattern: "messages.send", Effect: domain.EffectAllow}})
	store.Roles.SeedAssignment("user-1", left.ID, grantsTenant())
	store.Roles.SeedAssignment("user-1", right.ID, grantsTenant())
	roles := &countingRoleRepo{RoleRepo: store.Roles, grantsReads: map[string]int{}}

	got, names, err := resolveEffectiveGrants(context.Background(), roles, store.Grants, "user-1", testTenant)
	if err != nil {
		t.Fatalf("resolveEffectiveGrants: %v", err)
	}
	if n := roles.grantsReads[shared.ID]; n != 1 {
		t.Errorf("el rol compartido se leyó %d veces; quiere 1", n)
	}
	if roles.grantsReads[left.ID] != 1 || roles.grantsReads[right.ID] != 1 {
		t.Errorf("lecturas = %v; cada rol asignado se lee una vez", roles.grantsReads)
	}
	want := []string{"flows.read", "contacts.read", "messages.send"}
	if !slices.Equal(got.Allow, want) {
		t.Errorf("Allow = %v; quiere %v, sin repetir el del compartido", got.Allow, want)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"left", "right"}) {
		t.Errorf("nombres = %v; quiere [left right]", names)
	}
}

// Los overrides del usuario se funden encima: un deny propio viaja aunque un rol permita, y un
// allow propio se suma a los del rol sin repetirse.
func TestResolveEffectiveGrants_UserOverridesAreMergedOnTop(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	role := store.Roles.Seed(domain.Role{TenantID: grantsTenant(), Name: "wide"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})
	store.Roles.SeedAssignment("user-1", role.ID, grantsTenant())
	for _, g := range []domain.Grant{
		{Pattern: "flows.delete", Effect: domain.EffectDeny},
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "reports.read", Effect: domain.EffectAllow},
	} {
		if err := store.Grants.AddUserGrant(ctx, "user-1", g); err != nil {
			t.Fatalf("AddUserGrant: %v", err)
		}
	}

	got, _, err := resolveEffectiveGrants(ctx, store.Roles, store.Grants, "user-1", testTenant)
	if err != nil {
		t.Fatalf("resolveEffectiveGrants: %v", err)
	}
	if !slices.Equal(got.Allow, []string{"flows.*", "reports.read"}) {
		t.Errorf("Allow = %v; quiere [flows.* reports.read]: el del rol primero y sin duplicar", got.Allow)
	}
	if !slices.Equal(got.Deny, []string{"flows.delete"}) {
		t.Errorf("Deny = %v; quiere el override [flows.delete]", got.Deny)
	}
}

// Sin roles ni overrides: grants vacíos NO nil y lista de nombres vacía.
func TestResolveEffectiveGrants_NothingAssignedGivesEmptyNonNil(t *testing.T) {
	store := memory.NewStore()
	got, names, err := resolveEffectiveGrants(context.Background(), store.Roles, store.Grants, "nobody", testTenant)
	if err != nil {
		t.Fatalf("resolveEffectiveGrants: %v", err)
	}
	if got.Allow == nil || got.Deny == nil || len(got.Allow) != 0 || len(got.Deny) != 0 {
		t.Errorf("grants = %#v; quiere Allow y Deny vacíos y no nil", got)
	}
	if names == nil || len(names) != 0 {
		t.Errorf("nombres = %#v; quiere vacío y no nil", names)
	}
}
