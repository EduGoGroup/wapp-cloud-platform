package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// ctxOf es el contexto de un administrador de tenantID. Vive aquí porque roles es el primero
// de sus consumidores en pasar a verde (memberships e invitations también lo usan).
func ctxOf(tenantID string) context.Context {
	return withCaller(context.Background(), in.Caller{TenantID: tenantID, UserID: "admin-" + tenantID})
}

type roleFixture struct {
	svc   *RoleService
	store *memory.Store
}

func newRoleFixture(t *testing.T) roleFixture {
	t.Helper()
	store := memory.NewStore()
	svc, err := NewRoleService(testResolver, store.Roles, store.Grants, store.Memberships)
	if err != nil {
		t.Fatalf("NewRoleService: %v", err)
	}
	return roleFixture{svc: svc, store: store}
}

// memberOf siembra un miembro nuevo de tenantID y devuelve su UUID.
func (f roleFixture) memberOf(tenantID string) string {
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, tenantID)
	return userID
}

// rolesIn devuelve los roles de userID resueltos en tenantID.
func (f roleFixture) rolesIn(t *testing.T, userID, tenantID string) []domain.Role {
	t.Helper()
	roles, err := f.store.Roles.RolesOfUser(context.Background(), userID, tenantID)
	if err != nil {
		t.Fatalf("RolesOfUser: %v", err)
	}
	return roles
}

// visibleRoles devuelve los roles que tenantID ve en el repositorio (propios + plantillas).
func (f roleFixture) visibleRoles(t *testing.T, tenantID string) []domain.Role {
	t.Helper()
	roles, err := f.store.Roles.List(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return roles
}

// grantsOfRole devuelve los grants directos de un rol.
func (f roleFixture) grantsOfRole(t *testing.T, roleID string) []domain.Grant {
	t.Helper()
	gs, err := f.store.Roles.GrantsOf(context.Background(), roleID)
	if err != nil {
		t.Fatalf("GrantsOf: %v", err)
	}
	return gs
}

// overridesOf devuelve los overrides de una persona.
func (f roleFixture) overridesOf(t *testing.T, userID string) []domain.Grant {
	t.Helper()
	gs, err := f.store.Grants.GrantsOfUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("GrantsOfUser: %v", err)
	}
	return gs
}

func TestNewRoleService_RequiresDependencies(t *testing.T) {
	store := memory.NewStore()
	const (
		noCaller = "iam: RoleService requiere un CallerResolver (INV-04: el tenant sale del contexto)"
		noRepos  = "iam: RoleService requiere RoleRepo, GrantRepo y MembershipRepo"
	)
	cases := []struct {
		name  string
		build func() (*RoleService, error)
		want  string
	}{
		{"nil_caller", func() (*RoleService, error) {
			return NewRoleService(nil, store.Roles, store.Grants, store.Memberships)
		}, noCaller},
		{"nil_roles", func() (*RoleService, error) {
			return NewRoleService(testResolver, nil, store.Grants, store.Memberships)
		}, noRepos},
		{"nil_grants", func() (*RoleService, error) { return NewRoleService(testResolver, store.Roles, nil, store.Memberships) }, noRepos},
		{"nil_memberships", func() (*RoleService, error) { return NewRoleService(testResolver, store.Roles, store.Grants, nil) }, noRepos},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := c.build()
			if err == nil || svc != nil || err.Error() != c.want {
				t.Fatalf("= %v, %v; quiere nil y el literal %q", svc, err, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U20 · listado, alta y asignación acotados al tenant del contexto
// ---------------------------------------------------------------------------

func TestListRoles_OwnPlusGlobalTemplatesNeverForeign(t *testing.T) {
	f := newRoleFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	global := f.store.Roles.Seed(domain.Role{Name: "global-template"}, nil)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "foreign"}, nil)

	roles, err := f.svc.ListRoles(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range roles {
		seen[r.ID] = true
	}
	if !seen[own.ID] || !seen[global.ID] || seen[foreign.ID] || len(roles) != 2 {
		t.Fatalf("roles = %+v; quiere el propio y la plantilla, nunca el ajeno", roles)
	}
}

func TestCreateRole_BornInCallerTenant(t *testing.T) {
	f := newRoleFixture(t)
	role, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{Name: "supervisor"})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.TenantID == nil || *role.TenantID != testTenant || role.Name != "supervisor" {
		t.Fatalf("rol = %+v; quiere nacido en %q, nunca global", role, testTenant)
	}
}

func TestCreateRole_VisibleParentIsAccepted(t *testing.T) {
	f := newRoleFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "base"}, nil)
	global := f.store.Roles.Seed(domain.Role{Name: "viewer"}, nil)
	for name, parent := range map[string]string{"own_parent": own.ID, "global_parent": global.ID} {
		t.Run(name, func(t *testing.T) {
			role, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{Name: "child-" + name, ParentRoleID: ptr(parent)})
			if err != nil {
				t.Fatalf("CreateRole: %v", err)
			}
			if role.ParentRoleID == nil || *role.ParentRoleID != parent {
				t.Fatalf("padre = %v; quiere %q", role.ParentRoleID, parent)
			}
		})
	}
}

func TestCreateRole_ForeignParentIsRejected(t *testing.T) {
	f := newRoleFixture(t)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "foreign"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})
	_, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{Name: "child", ParentRoleID: ptr(foreign.ID)})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if roles := f.visibleRoles(t, testTenant); len(roles) != 0 {
		t.Fatalf("se creó %+v con un padre ajeno", roles)
	}
}

func TestCreateRole_EmptyNameIsInvalidInput(t *testing.T) {
	f := newRoleFixture(t)
	if _, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v; quiere ErrInvalidInput", err)
	}
}

// INV-04: asignar una plantilla global queda acotado a la empresa del CONTEXTO.
func TestAssignRole_ScopedToCallerTenant(t *testing.T) {
	f := newRoleFixture(t)
	global := f.store.Roles.Seed(domain.Role{Name: "operator-template"}, nil)
	userID := f.memberOf(testTenant)

	if err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userID, RoleID: global.ID}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	assignments := f.store.Roles.AssignmentsOf(userID)
	if len(assignments) != 1 || assignments[0].TenantID == nil || *assignments[0].TenantID != testTenant {
		t.Fatalf("asignaciones = %+v; quiere una acotada a %q, nunca global", assignments, testTenant)
	}
	if other := f.rolesIn(t, userID, testTenantB); len(other) != 0 {
		t.Fatalf("la asignación se ve en otra empresa: %+v", other)
	}
}

// ---------------------------------------------------------------------------
// R-U21 · lo ajeno es opaco
// ---------------------------------------------------------------------------

func TestAssignRole_ForeignRoleIsOpaqueNotFound(t *testing.T) {
	f := newRoleFixture(t)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "foreign"}, nil)
	userID := f.memberOf(testTenant)
	for name, roleID := range map[string]string{"foreign_role": foreign.ID, "missing_role": uuid.NewString()} {
		t.Run(name, func(t *testing.T) {
			err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userID, RoleID: roleID})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v; quiere ErrNotFound", err)
			}
		})
	}
	if assignments := f.store.Roles.AssignmentsOf(userID); len(assignments) != 0 {
		t.Fatalf("se escribió %+v", assignments)
	}
}

func TestAssignRole_NonMemberIsRejected(t *testing.T) {
	f := newRoleFixture(t)
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	outsider := f.memberOf(testTenantB)
	err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: outsider, RoleID: role.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if assignments := f.store.Roles.AssignmentsOf(outsider); len(assignments) != 0 {
		t.Fatalf("se escribió %+v a quien no es miembro", assignments)
	}
}

func TestAssignRole_EmptyIDsAreInvalidInput(t *testing.T) {
	f := newRoleFixture(t)
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	userID := f.memberOf(testTenant)
	for name, input := range map[string]in.RoleAssignmentInput{
		"empty_user": {RoleID: role.ID},
		"empty_role": {UserID: userID},
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.svc.AssignRole(ctxOf(testTenant), input); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
}

// Retirar en A no toca la fila de B.
func TestUnassignRole_DoesNotTouchOtherCompanyAssignment(t *testing.T) {
	f := newRoleFixture(t)
	global := f.store.Roles.Seed(domain.Role{Name: "shared-template"}, nil)
	userA, userB := f.memberOf(testTenant), f.memberOf(testTenantB)
	for tenant, user := range map[string]string{testTenant: userA, testTenantB: userB} {
		if err := f.svc.AssignRole(ctxOf(tenant), in.RoleAssignmentInput{UserID: user, RoleID: global.ID}); err != nil {
			t.Fatalf("AssignRole(%s): %v", tenant, err)
		}
	}
	if err := f.svc.UnassignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userA, RoleID: global.ID}); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
	if left := f.rolesIn(t, userA, testTenant); len(left) != 0 {
		t.Errorf("la asignación de A debía irse: %+v", left)
	}
	if left := f.rolesIn(t, userB, testTenantB); len(left) != 1 {
		t.Errorf("la asignación de B no se tocaba: %+v", left)
	}
}

// ---------------------------------------------------------------------------
// R-U22 · grants
// ---------------------------------------------------------------------------

func TestGrantToRole_GlobalTemplateIsImmutable(t *testing.T) {
	f := newRoleFixture(t)
	global := f.store.Roles.Seed(domain.Role{Name: "global-template"}, nil)
	err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{
		RoleID: global.ID, Grant: domain.Grant{Pattern: "tenants.*", Effect: domain.EffectAllow},
	})
	if !errors.Is(err, domain.ErrGlobalRoleImmutable) {
		t.Fatalf("err = %v; quiere ErrGlobalRoleImmutable", err)
	}
	if gs := f.grantsOfRole(t, global.ID); len(gs) != 0 {
		t.Fatalf("se escribió %+v en la plantilla", gs)
	}
}

func TestGrantToRole_ForeignRoleIsNotFound(t *testing.T) {
	f := newRoleFixture(t)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "foreign"}, nil)
	err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{
		RoleID: foreign.ID, Grant: domain.Grant{Pattern: "flows.*", Effect: domain.EffectAllow},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if gs := f.grantsOfRole(t, foreign.ID); len(gs) != 0 {
		t.Fatalf("se escribió %+v en el rol ajeno", gs)
	}
}

func TestGrantToRole_OwnRoleWritesAndRevokes(t *testing.T) {
	f := newRoleFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	g := domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}
	if err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: own.ID, Grant: g}); err != nil {
		t.Fatalf("GrantToRole: %v", err)
	}
	if gs := f.grantsOfRole(t, own.ID); len(gs) != 1 || gs[0] != g {
		t.Fatalf("grants = %+v; quiere [%+v]", gs, g)
	}
	if err := f.svc.RevokeFromRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: own.ID, Grant: g}); err != nil {
		t.Fatalf("RevokeFromRole: %v", err)
	}
	if gs := f.grantsOfRole(t, own.ID); len(gs) != 0 {
		t.Fatalf("tras revocar quedan %+v", gs)
	}
}

// El override solo sobre miembros del tenant (iam_user_grants no tiene tenant).
func TestUserGrants_OnlyOverMembersOfTenant(t *testing.T) {
	f := newRoleFixture(t)
	g := domain.Grant{Pattern: "tenants.delete", Effect: domain.EffectAllow}
	outsider := f.memberOf(testTenantB)
	if err := f.svc.GrantToUser(ctxOf(testTenant), in.UserGrantInput{UserID: outsider, Grant: g}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if gs := f.overridesOf(t, outsider); len(gs) != 0 {
		t.Fatalf("se escribió %+v a alguien de otra empresa", gs)
	}

	member := f.memberOf(testTenant)
	if err := f.svc.GrantToUser(ctxOf(testTenant), in.UserGrantInput{UserID: member, Grant: g}); err != nil {
		t.Fatalf("GrantToUser: %v", err)
	}
	if gs := f.overridesOf(t, member); len(gs) != 1 {
		t.Fatalf("overrides = %+v; quiere el escrito", gs)
	}
	if err := f.svc.RevokeFromUser(ctxOf(testTenant), in.UserGrantInput{UserID: member, Grant: g}); err != nil {
		t.Fatalf("RevokeFromUser: %v", err)
	}
	if gs := f.overridesOf(t, member); len(gs) != 0 {
		t.Fatalf("tras revocar quedan %+v", gs)
	}
}

// Un efecto vacío no vale como allow; patrón vacío tampoco.
func TestGrant_InvalidGrantIsNotWritten(t *testing.T) {
	f := newRoleFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	member := f.memberOf(testTenant)
	cases := []struct {
		name  string
		grant domain.Grant
	}{
		{"empty_pattern", domain.Grant{Effect: domain.EffectAllow}},
		{"empty_effect_is_not_allow", domain.Grant{Pattern: "flows.read"}},
		{"unknown_effect", domain.Grant{Pattern: "flows.read", Effect: domain.Effect("permitir")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: own.ID, Grant: c.grant}); !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("GrantToRole: err = %v; quiere ErrInvalidInput", err)
			}
			if err := f.svc.GrantToUser(ctxOf(testTenant), in.UserGrantInput{UserID: member, Grant: c.grant}); !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("GrantToUser: err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
	if gs := f.grantsOfRole(t, own.ID); len(gs) != 0 {
		t.Fatalf("se escribió %+v en el rol", gs)
	}
	if gs := f.overridesOf(t, member); len(gs) != 0 {
		t.Fatalf("se escribió %+v a la persona", gs)
	}
}

// ---------------------------------------------------------------------------
// R-U23 · sin tenant en el contexto no se ejecuta nada
// ---------------------------------------------------------------------------

func TestRoleService_NoTenantInContextExecutesNothing(t *testing.T) {
	f := newRoleFixture(t)
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	userID := f.memberOf(testTenant)
	g := domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}
	assignment := in.RoleAssignmentInput{UserID: userID, RoleID: role.ID}

	for name, ctx := range map[string]context.Context{
		"no_identity":                context.Background(),
		"identity_without_company":   withCaller(context.Background(), in.Caller{UserID: "subject-without-company"}),
		"identity_with_empty_tenant": withCaller(context.Background(), in.Caller{TenantID: "", UserID: "x"}),
	} {
		t.Run(name, func(t *testing.T) {
			calls := map[string]error{}
			_, calls["ListRoles"] = f.svc.ListRoles(ctx)
			_, calls["CreateRole"] = f.svc.CreateRole(ctx, in.CreateRoleInput{Name: "x"})
			calls["AssignRole"] = f.svc.AssignRole(ctx, assignment)
			calls["UnassignRole"] = f.svc.UnassignRole(ctx, assignment)
			calls["GrantToRole"] = f.svc.GrantToRole(ctx, in.RoleGrantInput{RoleID: role.ID, Grant: g})
			calls["RevokeFromRole"] = f.svc.RevokeFromRole(ctx, in.RoleGrantInput{RoleID: role.ID, Grant: g})
			calls["GrantToUser"] = f.svc.GrantToUser(ctx, in.UserGrantInput{UserID: userID, Grant: g})
			calls["RevokeFromUser"] = f.svc.RevokeFromUser(ctx, in.UserGrantInput{UserID: userID, Grant: g})
			for method, err := range calls {
				if !errors.Is(err, domain.ErrNoTenant) {
					t.Errorf("%s: err = %v; quiere ErrNoTenant", method, err)
				}
			}
		})
	}
	if roles := f.visibleRoles(t, testTenant); len(roles) != 1 {
		t.Errorf("se creó algún rol: %+v", roles)
	}
	if assignments := f.store.Roles.AssignmentsOf(userID); len(assignments) != 0 {
		t.Errorf("se asignó algún rol: %+v", assignments)
	}
	if gs := f.overridesOf(t, userID); len(gs) != 0 {
		t.Errorf("se escribió algún override: %+v", gs)
	}
}
