package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// Las tres operaciones que QUITAN (UnassignRole, RevokeFromRole, RevokeFromUser) prometen «las
// mismas comprobaciones» que las que ponen (R-U21, R-U22). Los tests de roles_test.go las
// ejercitan solo por el camino feliz; aquí va cada rechazo, con algo sembrado que el rechazo no
// puede tocar: un borrado que se cuela no devuelve error, así que sin la siembra no se vería.

// ---------------------------------------------------------------------------
// R-U21 · UnassignRole: lo ajeno es opaco también al retirar
// ---------------------------------------------------------------------------

// Quien no es miembro de la empresa del llamante no existe para ella, aunque arrastre una
// asignación acotada a esa empresa (RemoveMember no retira roles).
func TestUnassignRole_NonMemberIsNotFound(t *testing.T) {
	f := newRoleFixture(t)
	global := f.store.Roles.Seed(domain.Role{Name: "shared-template"}, nil)
	outsider := f.memberOf(testTenantB)
	f.store.Roles.SeedAssignment(outsider, global.ID, ptr(testTenant))

	err := f.svc.UnassignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: outsider, RoleID: global.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if assignments := f.store.Roles.AssignmentsOf(outsider); len(assignments) != 1 {
		t.Fatalf("asignaciones = %+v; se retiró la de quien no es miembro", assignments)
	}
}

func TestUnassignRole_ForeignOrMissingRoleIsNotFound(t *testing.T) {
	f := newRoleFixture(t)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "foreign"}, nil)
	userID := f.memberOf(testTenant)
	// Una fila que la guarda de visibilidad tiene que proteger: el rol de OTRA empresa, acotado
	// a la del llamante.
	f.store.Roles.SeedAssignment(userID, foreign.ID, ptr(testTenant))
	for name, roleID := range map[string]string{"foreign_role": foreign.ID, "missing_role": uuid.NewString()} {
		t.Run(name, func(t *testing.T) {
			err := f.svc.UnassignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userID, RoleID: roleID})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v; quiere ErrNotFound", err)
			}
		})
	}
	if assignments := f.store.Roles.AssignmentsOf(userID); len(assignments) != 1 {
		t.Fatalf("asignaciones = %+v; se retiró la de un rol que el llamante no ve", assignments)
	}
}

// ---------------------------------------------------------------------------
// R-U22 · RevokeFromRole: las reglas de GrantToRole
// ---------------------------------------------------------------------------

func TestRevokeFromRole_InvalidGrantIsInvalidInput(t *testing.T) {
	f := newRoleFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "own"}, nil)
	for name, g := range map[string]domain.Grant{
		"empty_pattern":   {Effect: domain.EffectAllow},
		"empty_effect":    {Pattern: "flows.read"},
		"unknown_effect":  {Pattern: "flows.read", Effect: domain.Effect("permitir")},
		"both_are_absent": {},
	} {
		t.Run(name, func(t *testing.T) {
			err := f.svc.RevokeFromRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: own.ID, Grant: g})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
}

// La plantilla global no se modifica desde una empresa, y el rol de otra empresa no existe: ni
// para añadir ni para quitar.
func TestRevokeFromRole_GlobalIsImmutableAndForeignIsNotFound(t *testing.T) {
	g := domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}
	cases := []struct {
		name string
		role domain.Role
		want error
	}{
		{"global_template", domain.Role{Name: "global-template"}, domain.ErrGlobalRoleImmutable},
		{"foreign_role", domain.Role{TenantID: ptr(testTenantB), Name: "foreign"}, domain.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRoleFixture(t)
			role := f.store.Roles.Seed(c.role, []domain.Grant{g})
			err := f.svc.RevokeFromRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: role.ID, Grant: g})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v; quiere %v", err, c.want)
			}
			if gs := f.grantsOfRole(t, role.ID); len(gs) != 1 || gs[0] != g {
				t.Fatalf("grants = %+v; se tocó un rol que no es del llamante", gs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U22 · RevokeFromUser: las reglas de GrantToUser
// ---------------------------------------------------------------------------

func TestRevokeFromUser_InvalidGrantIsInvalidInput(t *testing.T) {
	f := newRoleFixture(t)
	member := f.memberOf(testTenant)
	for name, g := range map[string]domain.Grant{
		"empty_pattern":  {Effect: domain.EffectAllow},
		"empty_effect":   {Pattern: "flows.read"},
		"unknown_effect": {Pattern: "flows.read", Effect: domain.Effect("permitir")},
	} {
		t.Run(name, func(t *testing.T) {
			err := f.svc.RevokeFromUser(ctxOf(testTenant), in.UserGrantInput{UserID: member, Grant: g})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
}

// iam_user_grants no tiene tenant: sin la comprobación de membresía un administrador le quitaría
// overrides a cualquier UUID del grupo con solo teclearlo.
func TestRevokeFromUser_NonMemberIsNotFoundAndKeepsOverride(t *testing.T) {
	f := newRoleFixture(t)
	g := domain.Grant{Pattern: "tenants.delete", Effect: domain.EffectDeny}
	outsider := f.memberOf(testTenantB)
	if err := f.store.Grants.AddUserGrant(context.Background(), outsider, g); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}
	err := f.svc.RevokeFromUser(ctxOf(testTenant), in.UserGrantInput{UserID: outsider, Grant: g})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound", err)
	}
	if gs := f.overridesOf(t, outsider); len(gs) != 1 || gs[0] != g {
		t.Fatalf("overrides = %+v; se le quitó a alguien de otra empresa", gs)
	}
	if err := f.svc.RevokeFromUser(ctxOf(testTenant), in.UserGrantInput{Grant: g}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("user_id vacío: err = %v; quiere ErrInvalidInput", err)
	}
}
