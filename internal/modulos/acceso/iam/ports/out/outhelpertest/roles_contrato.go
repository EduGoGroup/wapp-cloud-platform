package outhelpertest

import (
	"errors"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MontajeRoleRepo es lo que cada implementación de out.RoleRepo entrega a la suite para UN caso:
// el repositorio sobre dos tenants que existen, sin roles propios ni asignaciones, y con el rol
// transversal (domain.TransversalRoleID, plantilla global) ya presente: con Postgres lo siembran
// las migraciones, junto a las demás plantillas globales; un doble lo siembra él.
type MontajeRoleRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.RoleRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
}

// ContratoRoleRepo ejecuta las promesas de out.RoleRepo contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso: alta con unicidad por tenant, lectura, List = propios +
// plantillas globales y nunca ajenos, la cadena de herencia, los grants del rol idempotentes, y la
// asignación por ámbito con la guarda de T5.6 (un rol de empresa no se asigna global; el
// transversal sí).
//
// Lo que NO afirma, a propósito: el orden de List y de RolesOfUser (el puerto no lo promete), ni
// la unicidad entre plantillas globales (el puerto no crea globales: las siembran las
// migraciones).
func ContratoRoleRepo(t *testing.T, nuevo func(t *testing.T) MontajeRoleRepo) {
	t.Helper()
	runCases(t, "ContratoRoleRepo", nuevo, validateRoleMontaje, []testCase[MontajeRoleRepo]{
		{"Create_AssignsIDAndCreatedAt_GetByIDReadsIt", roleCreateAndGet},
		{"Create_SameNameSameTenant_Conflict", roleCreateDuplicate},
		{"Create_SameNameOtherTenant_Allowed", roleCreateSameNameOtherTenant},
		{"GetByID_Missing_NotFound", roleGetMissing},
		{"List_OwnPlusGlobalTemplates_NeverForeign", roleListVisible},
		{"ParentOf_ChildRootAndMissing", roleParentOf},
		{"Grants_AddIdempotent_RemoveOnlyThatOne", roleGrants},
		{"AssignToUser_TenantScoped_OnlyInThatTenant", roleAssignScoped},
		{"AssignToUser_IsIdempotent", roleAssignIdempotent},
		{"AssignToUser_CompanyRoleWithGlobalScope_ErrRoleScopeInvalid", roleAssignGlobalRejected},
		{"AssignToUser_TransversalRoleGlobal_Allowed", roleAssignTransversalGlobal},
		{"UnassignFromUser_OnlyThatScope", roleUnassignScoped},
		{"RolesOfUser_ScopedAndGlobalSameRole_Once", roleRolesOfUserDedup},
	})
}

func validateRoleMontaje(t *testing.T, m MontajeRoleRepo) {
	t.Helper()
	if m.Repo == nil {
		t.Fatal("MontajeRoleRepo.Repo es nil")
	}
	validateTenants(t, m.TenantA, m.TenantB)
}

// mustCreateRole crea el rol del tenant con ese nombre (y padre, si no es nil) y falla el test si
// no puede.
func mustCreateRole(t *testing.T, repo out.RoleRepo, tenantID, name string, parentID *string) domain.Role {
	t.Helper()
	role, err := repo.Create(bg(), domain.Role{TenantID: ptr(tenantID), Name: name, ParentRoleID: parentID})
	if err != nil {
		t.Fatalf("Create(%s en %s): %v", name, tenantID, err)
	}
	return role
}

// roleIDsOf devuelve, ordenados, los ID de RolesOfUser(user, tenant) y falla el test si hay error.
func roleIDsOf(t *testing.T, repo out.RoleRepo, userID, tenantID string) []string {
	t.Helper()
	roles, err := repo.RolesOfUser(bg(), userID, tenantID)
	if err != nil {
		t.Fatalf("RolesOfUser(%s, %q): %v", userID, tenantID, err)
	}
	ids := make([]string, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids
}

// wantRoleIDs compara conjuntos de ID de rol.
func wantRoleIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, quiero %v", what, got, want)
	}
}

// grantsOfRole devuelve GrantsOf ordenados y falla el test si hay error.
func grantsOfRole(t *testing.T, repo out.RoleRepo, roleID string) []domain.Grant {
	t.Helper()
	grants, err := repo.GrantsOf(bg(), roleID)
	if err != nil {
		t.Fatalf("GrantsOf(%s): %v", roleID, err)
	}
	return sortedGrants(grants)
}

// sortedGrants devuelve una copia ordenada por (pattern, effect): el orden no es contrato.
func sortedGrants(grants []domain.Grant) []domain.Grant {
	sorted := slices.Clone(grants)
	slices.SortFunc(sorted, func(a, b domain.Grant) int {
		if a.Pattern != b.Pattern {
			if a.Pattern < b.Pattern {
				return -1
			}
			return 1
		}
		switch {
		case a.Effect < b.Effect:
			return -1
		case a.Effect > b.Effect:
			return 1
		}
		return 0
	})
	return sorted
}

func roleCreateAndGet(t *testing.T, m MontajeRoleRepo) {
	created := mustCreateRole(t, m.Repo, m.TenantA, "contract-admin", nil)
	if created.ID == "" || created.CreatedAt.IsZero() {
		t.Fatalf("Create = %+v, quiero ID y created_at asignados", created)
	}
	if created.TenantID == nil || *created.TenantID != m.TenantA || created.Name != "contract-admin" || created.ParentRoleID != nil {
		t.Fatalf("Create = %+v, quiero tenant %s, nombre contract-admin y sin padre", created, m.TenantA)
	}
	got, err := m.Repo.GetByID(bg(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.TenantID == nil || *got.TenantID != m.TenantA ||
		got.ParentRoleID != nil || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("GetByID = %+v, quiero la fila creada %+v", got, created)
	}
}

func roleCreateDuplicate(t *testing.T, m MontajeRoleRepo) {
	mustCreateRole(t, m.Repo, m.TenantA, "contract-dup", nil)
	_, err := m.Repo.Create(bg(), domain.Role{TenantID: ptr(m.TenantA), Name: "contract-dup"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mismo nombre en el mismo tenant: err = %v, quiero domain.ErrConflict", err)
	}
}

func roleCreateSameNameOtherTenant(t *testing.T, m MontajeRoleRepo) {
	a := mustCreateRole(t, m.Repo, m.TenantA, "contract-shared", nil)
	b := mustCreateRole(t, m.Repo, m.TenantB, "contract-shared", nil)
	if a.ID == b.ID {
		t.Fatalf("dos tenants con el mismo nombre de rol comparten ID %s", a.ID)
	}
}

func roleGetMissing(t *testing.T, m MontajeRoleRepo) {
	if _, err := m.Repo.GetByID(bg(), newUser()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID de un rol que no existe: err = %v, quiero domain.ErrNotFound", err)
	}
}

func roleListVisible(t *testing.T, m MontajeRoleRepo) {
	own := mustCreateRole(t, m.Repo, m.TenantA, "contract-own", nil)
	foreign := mustCreateRole(t, m.Repo, m.TenantB, "contract-foreign", nil)
	roles, err := m.Repo.List(bg(), m.TenantA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var sawOwn, sawTransversal bool
	for _, r := range roles {
		switch {
		case r.ID == foreign.ID:
			t.Fatalf("List(A) devuelve el rol %s de OTRO tenant", r.ID)
		case r.TenantID != nil && *r.TenantID != m.TenantA:
			t.Fatalf("List(A) devuelve %+v, de un tenant que no es A", r)
		case r.ID == own.ID:
			sawOwn = true
		case r.ID == domain.TransversalRoleID:
			sawTransversal = r.TenantID == nil
		}
	}
	if !sawOwn || !sawTransversal {
		t.Fatalf("List(A) = %+v: quiero el rol propio %s y la plantilla global %s (tenant nil)", roles, own.ID, domain.TransversalRoleID)
	}
}

func roleParentOf(t *testing.T, m MontajeRoleRepo) {
	parent := mustCreateRole(t, m.Repo, m.TenantA, "contract-parent", nil)
	child := mustCreateRole(t, m.Repo, m.TenantA, "contract-child", ptr(parent.ID))
	if child.ParentRoleID == nil || *child.ParentRoleID != parent.ID {
		t.Fatalf("Create del hijo = %+v, quiero ParentRoleID %s", child, parent.ID)
	}
	for _, tc := range []struct {
		name, id   string
		wantParent string
		wantOK     bool
	}{
		{"child", child.ID, parent.ID, true},
		{"root", parent.ID, "", false},
		{"missing", newUser(), "", false},
	} {
		got, ok, err := m.Repo.ParentOf(bg(), tc.id)
		if err != nil || ok != tc.wantOK || got != tc.wantParent {
			t.Fatalf("ParentOf(%s) = (%q, %v, %v), quiero (%q, %v, nil)", tc.name, got, ok, err, tc.wantParent, tc.wantOK)
		}
	}
}

func roleGrants(t *testing.T, m MontajeRoleRepo) {
	role := mustCreateRole(t, m.Repo, m.TenantA, "contract-grants", nil)
	other := mustCreateRole(t, m.Repo, m.TenantA, "contract-other", nil)
	allow := domain.Grant{Pattern: "flows.*", Effect: domain.EffectAllow}
	deny := domain.Grant{Pattern: "flows.*", Effect: domain.EffectDeny}
	for _, g := range []domain.Grant{allow, allow, deny} {
		if err := m.Repo.AddGrant(bg(), role.ID, g); err != nil {
			t.Fatalf("AddGrant(%+v): %v", g, err)
		}
	}
	if err := m.Repo.AddGrant(bg(), other.ID, allow); err != nil {
		t.Fatalf("AddGrant en otro rol: %v", err)
	}
	if got := grantsOfRole(t, m.Repo, role.ID); !slices.Equal(got, sortedGrants([]domain.Grant{allow, deny})) {
		t.Fatalf("GrantsOf = %+v, quiero allow y deny una vez cada uno (AddGrant idempotente por pattern+effect)", got)
	}
	if err := m.Repo.RemoveGrant(bg(), role.ID, allow); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if err := m.Repo.RemoveGrant(bg(), role.ID, domain.Grant{Pattern: "never.added", Effect: domain.EffectAllow}); err != nil {
		t.Fatalf("RemoveGrant de un grant que no estaba: %v, quiero nil (no-op)", err)
	}
	if got := grantsOfRole(t, m.Repo, role.ID); !slices.Equal(got, []domain.Grant{deny}) {
		t.Fatalf("GrantsOf tras quitar allow = %+v, quiero solo deny", got)
	}
	if got := grantsOfRole(t, m.Repo, other.ID); !slices.Equal(got, []domain.Grant{allow}) {
		t.Fatalf("GrantsOf del otro rol = %+v, quiero intacto [allow]", got)
	}
}

func roleAssignScoped(t *testing.T, m MontajeRoleRepo) {
	role := mustCreateRole(t, m.Repo, m.TenantA, "contract-scoped", nil)
	user := newUser()
	if err := m.Repo.AssignToUser(bg(), user, role.ID, ptr(m.TenantA)); err != nil {
		t.Fatalf("AssignToUser acotado: %v", err)
	}
	wantRoleIDs(t, "RolesOfUser(A)", roleIDsOf(t, m.Repo, user, m.TenantA), role.ID)
	wantRoleIDs(t, "RolesOfUser(B)", roleIDsOf(t, m.Repo, user, m.TenantB))
	wantRoleIDs(t, "RolesOfUser(global)", roleIDsOf(t, m.Repo, user, ""))
	wantRoleIDs(t, "RolesOfUser(A) de otra persona", roleIDsOf(t, m.Repo, newUser(), m.TenantA))
}

func roleAssignIdempotent(t *testing.T, m MontajeRoleRepo) {
	role := mustCreateRole(t, m.Repo, m.TenantA, "contract-twice", nil)
	user := newUser()
	for range 2 {
		if err := m.Repo.AssignToUser(bg(), user, role.ID, ptr(m.TenantA)); err != nil {
			t.Fatalf("AssignToUser: %v", err)
		}
	}
	wantRoleIDs(t, "RolesOfUser(A)", roleIDsOf(t, m.Repo, user, m.TenantA), role.ID)
	// Una sola retirada basta: la segunda asignación no dejó una fila duplicada.
	if err := m.Repo.UnassignFromUser(bg(), user, role.ID, ptr(m.TenantA)); err != nil {
		t.Fatalf("UnassignFromUser: %v", err)
	}
	wantRoleIDs(t, "RolesOfUser(A) tras retirar", roleIDsOf(t, m.Repo, user, m.TenantA))
}

func roleAssignGlobalRejected(t *testing.T, m MontajeRoleRepo) {
	role := mustCreateRole(t, m.Repo, m.TenantA, "contract-company", nil)
	for name, scope := range map[string]*string{"nil": nil, "empty": ptr("")} {
		t.Run(name, func(t *testing.T) {
			user := newUser()
			if err := m.Repo.AssignToUser(bg(), user, role.ID, scope); !errors.Is(err, domain.ErrRoleScopeInvalid) {
				t.Fatalf("rol de empresa con ámbito global (%s): err = %v, quiero domain.ErrRoleScopeInvalid", name, err)
			}
			wantRoleIDs(t, "RolesOfUser(global) tras el rechazo", roleIDsOf(t, m.Repo, user, ""))
			wantRoleIDs(t, "RolesOfUser(A) tras el rechazo", roleIDsOf(t, m.Repo, user, m.TenantA))
		})
	}
}

func roleAssignTransversalGlobal(t *testing.T, m MontajeRoleRepo) {
	user := newUser()
	if err := m.Repo.AssignToUser(bg(), user, domain.TransversalRoleID, nil); err != nil {
		t.Fatalf("el rol transversal TIENE que poder asignarse global: %v", err)
	}
	wantRoleIDs(t, "RolesOfUser(global)", roleIDsOf(t, m.Repo, user, ""), domain.TransversalRoleID)
	// Global vale en cualquier empresa: es la razón de que su ámbito lo sea.
	wantRoleIDs(t, "RolesOfUser(A)", roleIDsOf(t, m.Repo, user, m.TenantA), domain.TransversalRoleID)
	wantRoleIDs(t, "RolesOfUser(B)", roleIDsOf(t, m.Repo, user, m.TenantB), domain.TransversalRoleID)
}

func roleUnassignScoped(t *testing.T, m MontajeRoleRepo) {
	user := newUser()
	for _, scope := range []*string{nil, ptr(m.TenantA), ptr(m.TenantB)} {
		if err := m.Repo.AssignToUser(bg(), user, domain.TransversalRoleID, scope); err != nil {
			t.Fatalf("AssignToUser(transversal, %v): %v", scope, err)
		}
	}
	// Retirar la global no toca las acotadas, y retirar la de A no toca la de B.
	if err := m.Repo.UnassignFromUser(bg(), user, domain.TransversalRoleID, nil); err != nil {
		t.Fatalf("UnassignFromUser global: %v", err)
	}
	wantRoleIDs(t, "RolesOfUser(global) tras retirar la global", roleIDsOf(t, m.Repo, user, ""))
	wantRoleIDs(t, "RolesOfUser(A) tras retirar la global", roleIDsOf(t, m.Repo, user, m.TenantA), domain.TransversalRoleID)
	if err := m.Repo.UnassignFromUser(bg(), user, domain.TransversalRoleID, ptr(m.TenantA)); err != nil {
		t.Fatalf("UnassignFromUser A: %v", err)
	}
	wantRoleIDs(t, "RolesOfUser(A) tras retirar la de A", roleIDsOf(t, m.Repo, user, m.TenantA))
	wantRoleIDs(t, "RolesOfUser(B) tras retirar la de A", roleIDsOf(t, m.Repo, user, m.TenantB), domain.TransversalRoleID)
	if err := m.Repo.UnassignFromUser(bg(), user, domain.TransversalRoleID, ptr(m.TenantA)); err != nil {
		t.Fatalf("UnassignFromUser de una asignación que ya no estaba: %v, quiero nil (no-op)", err)
	}
}

func roleRolesOfUserDedup(t *testing.T, m MontajeRoleRepo) {
	user := newUser()
	for _, scope := range []*string{nil, ptr(m.TenantA)} {
		if err := m.Repo.AssignToUser(bg(), user, domain.TransversalRoleID, scope); err != nil {
			t.Fatalf("AssignToUser(transversal, %v): %v", scope, err)
		}
	}
	wantRoleIDs(t, "RolesOfUser(A) con el mismo rol global y acotado", roleIDsOf(t, m.Repo, user, m.TenantA), domain.TransversalRoleID)
}
