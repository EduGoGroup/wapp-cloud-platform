//go:build pendiente

package iampostgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// Executor lo cumplen el pool y la transacción: las vías de alta pasan uno u otra.
var (
	_ Executor = (*sql.DB)(nil)
	_ Executor = (*sql.Tx)(nil)
)

var _ func(*sql.DB, FeatureResolver) *MembershipRepo = NewMembershipRepo

// recordingResolver es un FeatureResolver que apunta cada pregunta y contesta has, err.
type recordingResolver struct {
	asked []string
	has   bool
	err   error
}

var _ FeatureResolver = (*recordingResolver)(nil)

func (r *recordingResolver) Has(_ context.Context, tenantID, feature string) (bool, error) {
	r.asked = append(r.asked, tenantID+"/"+feature)
	return r.has, r.err
}

// recordingExecutor es un Executor sin base que apunta el orden de las operaciones ("exec",
// "query"). ExecContext contesta execErr (nil: una sentencia que no afectó filas); QueryRowContext
// devuelve la fila de un pool caído, que al escanear falla con errPoolDown. Así se ve hasta dónde
// llega GrantTenantAccess antes de cortar.
type recordingExecutor struct {
	pool    *sql.DB
	execErr error
	calls   []string
}

func (e *recordingExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	e.calls = append(e.calls, "exec")
	if e.execErr != nil {
		return nil, e.execErr
	}
	return driver.RowsAffected(0), nil
}

func (e *recordingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	e.calls = append(e.calls, "query")
	return e.pool.QueryRowContext(ctx, query, args...)
}

// TestNewMembershipRepo_NilResolverAllowed: el resolver nil es válido (fail-closed, no un
// pánico) y construir no toca el pool.
func TestNewMembershipRepo_NilResolverAllowed(t *testing.T) {
	if NewMembershipRepo(downPool(t), nil) == nil {
		t.Fatal("NewMembershipRepo(db, nil) devolvió nil")
	}
}

// TestMembershipRepo_InfraErrors: con la base caída cada método propaga la causa envuelta con su
// texto; ninguna lectura devuelve lista (ni vacía) y Add no lo disfraza de conflicto.
func TestMembershipRepo_InfraErrors(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		call   func(r *MembershipRepo) error
	}{
		{"tenants_of_user_returns_nil", "iam: leer membresías: ", func(r *MembershipRepo) error {
			tenants, err := r.TenantsOfUser(t.Context(), testUserID)
			if tenants != nil {
				t.Errorf("TenantsOfUser con error devolvió %v; quiere nil", tenants)
			}
			return err
		}},
		{"user_tenants_returns_nil", "iam: listar las empresas del usuario: ", func(r *MembershipRepo) error {
			tenants, err := r.UserTenants(t.Context(), testUserID)
			if tenants != nil {
				t.Errorf("UserTenants con error devolvió %v; quiere nil", tenants)
			}
			return err
		}},
		{"members_of_returns_nil", "iam: listar miembros del tenant: ", func(r *MembershipRepo) error {
			members, err := r.MembersOf(t.Context(), testTenantID)
			if members != nil {
				t.Errorf("MembersOf con error devolvió %v; quiere nil", members)
			}
			return err
		}},
		{"add_cannot_open_tx_is_not_conflict", "iam: abrir tx de alta de membresía: ", func(r *MembershipRepo) error {
			err := r.Add(t.Context(), testUserID, testTenantID)
			if errors.Is(err, domain.ErrConflict) {
				t.Errorf("Add con la base caída = %v; no es un conflicto", err)
			}
			return err
		}},
		{"remove", "iam: baja de membresía: ", func(r *MembershipRepo) error {
			return r.Remove(t.Context(), testUserID, testTenantID)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantInfraError(t, c.name, c.call(NewMembershipRepo(downPool(t), &recordingResolver{has: true})), c.prefix)
		})
	}
}

// TestGrantTenantAccess_RoleScopeGuardBeforeDB es la guarda de T5.6 en la segunda vía que escribe
// en iam_user_roles: un rol de empresa con tenant "" se rechaza con ErrRoleScopeInvalid SIN una
// sola operación sobre exec y sin preguntar por ningún derecho.
func TestGrantTenantAccess_RoleScopeGuardBeforeDB(t *testing.T) {
	exec := &recordingExecutor{pool: downPool(t)}
	resolver := &recordingResolver{has: true}
	role := companyRoleID
	err := GrantTenantAccess(t.Context(), exec, resolver, testUserID, "", &role)
	if !errors.Is(err, domain.ErrRoleScopeInvalid) {
		t.Fatalf("GrantTenantAccess(rol de empresa, tenant \"\") = %v; quiere ErrRoleScopeInvalid", err)
	}
	if len(exec.calls) != 0 || len(resolver.asked) != 0 {
		t.Errorf("la guarda de ámbito tocó la base (%v) o el resolver (%v)", exec.calls, resolver.asked)
	}
}

// TestGrantTenantAccess_LockIsFirstAndStops es R-P2 visto sin base: la PRIMERA operación sobre
// exec es el cerrojo (una sentencia, no una consulta), y si falla no se cuenta, no se escribe y no
// se pregunta por multi_empresa. Vale para toda forma de alta que pasa la guarda de ámbito.
func TestGrantTenantAccess_LockIsFirstAndStops(t *testing.T) {
	transversal, company, empty := domain.TransversalRoleID, companyRoleID, ""
	cases := []struct {
		name     string
		tenantID string
		roleID   *string
	}{
		{"without_role", testTenantID, nil},
		{"with_company_role_scoped", testTenantID, &company},
		{"with_transversal_role", testTenantID, &transversal},
		{"without_role_empty_tenant_has_no_scope_guard", empty, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec := &recordingExecutor{pool: downPool(t), execErr: errPoolDown}
			resolver := &recordingResolver{has: true}
			err := GrantTenantAccess(t.Context(), exec, resolver, testUserID, c.tenantID, c.roleID)
			wantInfraError(t, "GrantTenantAccess", err, "iam: tomar el cerrojo del alta de membresía: ")
			if !slices.Equal(exec.calls, []string{"exec"}) {
				t.Errorf("operaciones sobre exec = %v; quiere solo el cerrojo [exec]", exec.calls)
			}
			if len(resolver.asked) != 0 {
				t.Errorf("con el cerrojo fallido se preguntó por %v", resolver.asked)
			}
		})
	}
}

// TestGrantTenantAccess_CountFailureStopsBeforeWriting: tras el cerrojo va el conteo de las otras
// membresías (una consulta); si falla, el alta corta con su texto, sin escribir nada y sin
// preguntar por multi_empresa — un conteo caído no se lee como «no tiene otras».
func TestGrantTenantAccess_CountFailureStopsBeforeWriting(t *testing.T) {
	exec := &recordingExecutor{pool: downPool(t)}
	resolver := &recordingResolver{has: true}
	role := companyRoleID
	err := GrantTenantAccess(t.Context(), exec, resolver, testUserID, testTenantID, &role)
	wantInfraError(t, "GrantTenantAccess", err, "iam: contar membresías en otros tenants: ")
	if !slices.Equal(exec.calls, []string{"exec", "query"}) {
		t.Errorf("operaciones sobre exec = %v; quiere [exec query]: cerrojo, conteo y nada más", exec.calls)
	}
	if len(resolver.asked) != 0 {
		t.Errorf("con el conteo fallido se preguntó por %v", resolver.asked)
	}
	if strings.Contains(err.Error(), "ya es miembro") {
		t.Errorf("un conteo fallido se tradujo en el rechazo de negocio: %v", err)
	}
}
