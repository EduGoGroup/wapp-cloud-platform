//go:build pendiente

package iampostgres

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// companyRoleID es un rol de empresa cualquiera: lo único que importa es que NO es el
// transversal.
const companyRoleID = "7d1e2c3b-4a5f-4e6d-8c7b-9a0f1e2d3c4b"

var _ func(*sql.DB) *RoleRepo = NewRoleRepo

// TestNewRoleRepo_DoesNotTouchPool: construir no consulta nada.
func TestNewRoleRepo_DoesNotTouchPool(t *testing.T) {
	if NewRoleRepo(downPool(t)) == nil {
		t.Fatal("NewRoleRepo devolvió nil")
	}
}

// TestRoleRepo_InfraErrors: con la base caída cada método propaga la causa envuelta con su
// texto; ninguno la disfraza de conflicto, de ausencia, de «sin padre» ni de lista vacía.
func TestRoleRepo_InfraErrors(t *testing.T) {
	tenant := testTenantID
	grant := domain.Grant{Pattern: "intake.read", Effect: domain.EffectAllow}
	cases := []struct {
		name   string
		prefix string
		call   func(r *RoleRepo) error
	}{
		{"create_is_not_conflict", "iam: crear rol: ", func(r *RoleRepo) error {
			_, err := r.Create(t.Context(), domain.Role{TenantID: &tenant, Name: "ventas"})
			if errors.Is(err, domain.ErrConflict) {
				t.Errorf("Create con la base caída = %v; no es un conflicto", err)
			}
			return err
		}},
		{"get_is_not_not_found", "iam: leer rol: ", func(r *RoleRepo) error {
			_, err := r.GetByID(t.Context(), companyRoleID)
			if errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetByID con la base caída = %v; no es ausencia", err)
			}
			return err
		}},
		{"list_returns_nil", "iam: listar roles: ", func(r *RoleRepo) error {
			roles, err := r.List(t.Context(), testTenantID)
			if roles != nil {
				t.Errorf("List con error devolvió %d roles; quiere nil", len(roles))
			}
			return err
		}},
		{"parent_of_is_not_no_parent", "iam: leer parent de rol: ", func(r *RoleRepo) error {
			parent, ok, err := r.ParentOf(t.Context(), companyRoleID)
			if ok || parent != "" {
				t.Errorf("ParentOf con error = (%q, %v); quiere (\"\", false)", parent, ok)
			}
			return err
		}},
		{"grants_of_returns_nil", "iam: leer grants: ", func(r *RoleRepo) error {
			grants, err := r.GrantsOf(t.Context(), companyRoleID)
			if grants != nil {
				t.Errorf("GrantsOf con error devolvió %d grants; quiere nil", len(grants))
			}
			return err
		}},
		{"add_grant", "iam: añadir grant a rol: ", func(r *RoleRepo) error {
			return r.AddGrant(t.Context(), companyRoleID, grant)
		}},
		{"remove_grant", "iam: quitar grant de rol: ", func(r *RoleRepo) error {
			return r.RemoveGrant(t.Context(), companyRoleID, grant)
		}},
		{"roles_of_user_scoped", "iam: listar roles de usuario: ", func(r *RoleRepo) error {
			roles, err := r.RolesOfUser(t.Context(), testUserID, testTenantID)
			if roles != nil {
				t.Errorf("RolesOfUser con error devolvió %d roles; quiere nil", len(roles))
			}
			return err
		}},
		{"roles_of_user_global_only", "iam: listar roles de usuario: ", func(r *RoleRepo) error {
			_, err := r.RolesOfUser(t.Context(), testUserID, "")
			return err
		}},
		{"unassign", "iam: quitar rol de usuario: ", func(r *RoleRepo) error {
			return r.UnassignFromUser(t.Context(), testUserID, companyRoleID, &tenant)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantInfraError(t, c.name, c.call(NewRoleRepo(downPool(t))), c.prefix)
		})
	}
}

// TestAssignToUser_ScopeGuard es T5.6: un rol de empresa con ámbito global (tenant nil O "") se
// rechaza con ErrRoleScopeInvalid nombrando el rol y SIN tocar la base; el transversal con ámbito
// global y cualquier rol acotado a una empresa pasan la guarda y llegan a la base (aquí, caída).
func TestAssignToUser_ScopeGuard(t *testing.T) {
	empty, tenant := "", testTenantID
	cases := []struct {
		name     string
		roleID   string
		tenantID *string
		rejected bool
	}{
		{"company_role_nil_tenant_rejected", companyRoleID, nil, true},
		{"company_role_empty_tenant_rejected", companyRoleID, &empty, true},
		{"transversal_role_global_allowed", domain.TransversalRoleID, nil, false},
		{"transversal_role_empty_tenant_allowed", domain.TransversalRoleID, &empty, false},
		{"company_role_scoped_allowed", companyRoleID, &tenant, false},
		{"transversal_role_scoped_allowed", domain.TransversalRoleID, &tenant, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := NewRoleRepo(downPool(t)).AssignToUser(t.Context(), testUserID, c.roleID, c.tenantID)
			if !c.rejected {
				wantInfraError(t, "AssignToUser", err, "iam: asignar rol a usuario: ")
				return
			}
			if !errors.Is(err, domain.ErrRoleScopeInvalid) {
				t.Fatalf("AssignToUser(%s, global) = %v; quiere ErrRoleScopeInvalid", c.roleID, err)
			}
			if errors.Is(err, errPoolDown) {
				t.Errorf("AssignToUser llegó a la base antes de rechazar el ámbito: %v", err)
			}
			if !strings.Contains(err.Error(), c.roleID) {
				t.Errorf("el error %q no nombra el rol %s", err.Error(), c.roleID)
			}
		})
	}
}

// TestUnassignFromUser_NoScopeGuard: retirar una asignación global de un rol de empresa NO se
// rechaza: llega a la base (aquí, caída), para que la fila mala se pueda limpiar.
func TestUnassignFromUser_NoScopeGuard(t *testing.T) {
	err := NewRoleRepo(downPool(t)).UnassignFromUser(t.Context(), testUserID, companyRoleID, nil)
	if errors.Is(err, domain.ErrRoleScopeInvalid) {
		t.Fatalf("UnassignFromUser global de un rol de empresa = %v; no lleva guarda de ámbito", err)
	}
	wantInfraError(t, "UnassignFromUser", err, "iam: quitar rol de usuario: ")
}
