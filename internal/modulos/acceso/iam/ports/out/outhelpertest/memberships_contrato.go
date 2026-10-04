package outhelpertest

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// SecondCompanyConflictText es el texto EXACTO del rechazo de una segunda empresa (R-P3): el
// centinela domain.ErrConflict envuelto con el motivo de siempre. Es observable (el 409 que las
// dos bandejas traducen) y no cambia con multi_empresa: sin la feature, el mismo error de antes.
const SecondCompanyConflictText = "iam: conflicto de unicidad: el usuario ya es miembro de otra empresa"

// MontajeMembershipRepo es lo que cada implementación de out.MembershipRepo entrega a la suite
// para UN caso: el repositorio, sin membresías, sobre dos tenants que existen y tienen nombre, y
// el interruptor del resolver de entitlements con el que se construyó.
type MontajeMembershipRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.MembershipRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
	// NameA y NameB son el display_name de TenantA y TenantB (public.tenants.display_name): lo
	// que UserTenants tiene que devolver con cada uno. No vacíos y distintos.
	NameA, NameB string
	// Features mueve el resolver que decide la segunda empresa. Llega sin multi_empresa en
	// ningún tenant y con el resolver funcionando.
	Features FeatureSwitch
}

// ContratoMembershipRepo ejecuta las promesas de out.MembershipRepo contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso. Las reglas son las de diseño F2 §2 y §4
// (R-P1…R-P4, R-U24): alta idempotente que no reescribe created_at, baja acotada, las dos
// lecturas del usuario en el mismo orden, MembersOf acotado al tenant, y la guarda de la segunda
// empresa con multi_empresa en sus tres desenlaces (sin la feature, con ella, resolver caído).
//
// Lo que NO afirma, a propósito: que TenantsOfUser vacío sea no nil (las implementaciones viejas
// devolvían nil, ver el puerto), ni que Remove no toque roles ni grants (son tablas de otros
// puertos; lo cubre F9), ni el desempate por id cuando dos altas comparten created_at (no se
// puede forzar desde el puerto: dos altas seguidas nunca comparten instante en Postgres).
func ContratoMembershipRepo(t *testing.T, nuevo func(t *testing.T) MontajeMembershipRepo) {
	t.Helper()
	runCases(t, "ContratoMembershipRepo", nuevo, validateMembershipMontaje, []testCase[MontajeMembershipRepo]{
		{"Add_WritesTheMembership", membershipAddWrites},
		{"Add_IsIdempotent_AndKeepsCreatedAt", membershipAddIdempotent},
		{"Add_SecondCompanyWithoutMultiCompany_SameConflictAndNothingWritten", membershipSecondCompanyRejected},
		{"Add_SecondCompanyWithMultiCompany_Writes", membershipSecondCompanyWithFeature},
		{"Add_MultiCompanyIsAskedOfTheDestinationTenant", membershipFeatureOfDestination},
		{"Add_BrokenResolverKeepsTheRejection", membershipBrokenResolver},
		{"Add_ConcurrentInTwoCompanies_OnlyOneWrites", membershipConcurrentAdds},
		{"Remove_OnlyThatMembership", membershipRemoveScoped},
		{"Remove_Missing_IsNoop", membershipRemoveMissing},
		{"TenantsOfUser_NoMemberships_EmptyWithoutError", membershipTenantsOfUserEmpty},
		{"UserTenants_NoMemberships_EmptyNotNil", membershipUserTenantsEmpty},
		{"UserTenants_SameOrderAsTenantsOfUser_WithNames", membershipUserTenantsOrder},
		{"UserTenants_OnlyHisOwn", membershipUserTenantsOnlyOwn},
		{"MembersOf_OnlyThatTenant_InOrder", membershipMembersOf},
	})
}

func validateMembershipMontaje(t *testing.T, m MontajeMembershipRepo) {
	t.Helper()
	switch {
	case m.Repo == nil:
		t.Fatal("MontajeMembershipRepo.Repo es nil")
	case m.Features == nil:
		t.Fatal("MontajeMembershipRepo.Features es nil: la guarda de la segunda empresa necesita un resolver")
	case m.NameA == "" || m.NameB == "" || m.NameA == m.NameB:
		t.Fatalf("MontajeMembershipRepo: NameA y NameB deben ser no vacíos y distintos y son %q y %q", m.NameA, m.NameB)
	}
	validateTenants(t, m.TenantA, m.TenantB)
}

// mustAdd da de alta la membresía y falla el test si no puede.
func mustAdd(t *testing.T, repo out.MembershipRepo, userID, tenantID string) {
	t.Helper()
	if err := repo.Add(bg(), userID, tenantID); err != nil {
		t.Fatalf("Add(%s, %s): %v", userID, tenantID, err)
	}
}

// tenantsOf devuelve TenantsOfUser y falla el test si hay error.
func tenantsOf(t *testing.T, repo out.MembershipRepo, userID string) []string {
	t.Helper()
	tenants, err := repo.TenantsOfUser(bg(), userID)
	if err != nil {
		t.Fatalf("TenantsOfUser(%s): %v", userID, err)
	}
	return tenants
}

// membersOf devuelve MembersOf y falla el test si hay error.
func membersOf(t *testing.T, repo out.MembershipRepo, tenantID string) []domain.Membership {
	t.Helper()
	members, err := repo.MembersOf(bg(), tenantID)
	if err != nil {
		t.Fatalf("MembersOf(%s): %v", tenantID, err)
	}
	return members
}

// wantTenants compara la lista de tenants con la esperada, en orden.
func wantTenants(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) || !slices.Equal(got, want) {
		t.Fatalf("%s = %v, quiero %v", what, got, want)
	}
}

// wantSecondCompanyConflict exige el rechazo de siempre de la segunda empresa: el centinela Y el
// texto exacto (R-P3).
func wantSecondCompanyConflict(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict", err)
	}
	if err.Error() != SecondCompanyConflictText {
		t.Fatalf("texto del rechazo = %q, quiero %q (el 409 de siempre, byte a byte)", err.Error(), SecondCompanyConflictText)
	}
}

func membershipAddWrites(t *testing.T, m MontajeMembershipRepo) {
	user := newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), m.TenantA)
	members := membersOf(t, m.Repo, m.TenantA)
	if len(members) != 1 || members[0].UserID != user || members[0].TenantID != m.TenantA || members[0].CreatedAt.IsZero() {
		t.Fatalf("MembersOf(A) = %+v, quiero una fila de %s en %s con created_at", members, user, m.TenantA)
	}
}

func membershipAddIdempotent(t *testing.T, m MontajeMembershipRepo) {
	user := newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	before := membersOf(t, m.Repo, m.TenantA)
	if err := m.Repo.Add(bg(), user, m.TenantA); err != nil {
		t.Fatalf("repetir Add tiene que ser un no-op sin error: %v", err)
	}
	after := membersOf(t, m.Repo, m.TenantA)
	if len(after) != 1 || len(before) != 1 || !after[0].CreatedAt.Equal(before[0].CreatedAt) {
		t.Fatalf("tras repetir Add, MembersOf(A) = %+v (antes %+v): quiero la MISMA fila, sin duplicar ni reescribir created_at", after, before)
	}
	wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), m.TenantA)
}

func membershipSecondCompanyRejected(t *testing.T, m MontajeMembershipRepo) {
	user := newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	wantSecondCompanyConflict(t, m.Repo.Add(bg(), user, m.TenantB))
	wantTenants(t, "TenantsOfUser tras el rechazo", tenantsOf(t, m.Repo, user), m.TenantA)
	if members := membersOf(t, m.Repo, m.TenantB); len(members) != 0 {
		t.Fatalf("MembersOf(B) = %+v: un alta rechazada escribió", members)
	}
}

func membershipSecondCompanyWithFeature(t *testing.T, m MontajeMembershipRepo) {
	m.Features.GrantMultiCompany(t, m.TenantB)
	user := newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	if err := m.Repo.Add(bg(), user, m.TenantB); err != nil {
		t.Fatalf("con multi_empresa en el destino, la segunda empresa tiene que escribirse: %v", err)
	}
	wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), m.TenantA, m.TenantB)
}

func membershipFeatureOfDestination(t *testing.T, m MontajeMembershipRepo) {
	// La feature la tiene la empresa de ORIGEN, no la que recibe: no abre nada.
	m.Features.GrantMultiCompany(t, m.TenantA)
	user := newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	wantSecondCompanyConflict(t, m.Repo.Add(bg(), user, m.TenantB))
	wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), m.TenantA)
}

func membershipBrokenResolver(t *testing.T, m MontajeMembershipRepo) {
	m.Features.GrantMultiCompany(t, m.TenantB)
	m.Features.BreakResolver(t)
	user := newUser()
	// La primera membresía no pregunta por ningún derecho: el resolver caído no la toca.
	mustAdd(t, m.Repo, user, m.TenantA)
	// Fail-closed invertido: no poder resolver el derecho es no tenerlo.
	wantSecondCompanyConflict(t, m.Repo.Add(bg(), user, m.TenantB))
	wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), m.TenantA)
}

func membershipConcurrentAdds(t *testing.T, m MontajeMembershipRepo) {
	user := newUser()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, tenant := range []string{m.TenantA, m.TenantB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.Repo.Add(bg(), user, tenant)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrConflict):
		default:
			t.Fatalf("alta concurrente: err = %v, quiero nil o domain.ErrConflict", err)
		}
	}
	if ok != 1 {
		t.Fatalf("dos altas simultáneas en dos empresas sin multi_empresa: %d escribieron (errores %v), quiero exactamente una", ok, errs)
	}
	if tenants := tenantsOf(t, m.Repo, user); len(tenants) != 1 {
		t.Fatalf("TenantsOfUser = %v, quiero una sola empresa", tenants)
	}
}

func membershipRemoveScoped(t *testing.T, m MontajeMembershipRepo) {
	m.Features.GrantMultiCompany(t, m.TenantB)
	user, other := newUser(), newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	mustAdd(t, m.Repo, user, m.TenantB)
	mustAdd(t, m.Repo, other, m.TenantA)
	if err := m.Repo.Remove(bg(), user, m.TenantA); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	wantTenants(t, "TenantsOfUser tras la baja en A", tenantsOf(t, m.Repo, user), m.TenantB)
	members := membersOf(t, m.Repo, m.TenantA)
	if len(members) != 1 || members[0].UserID != other {
		t.Fatalf("MembersOf(A) = %+v, quiero solo a %s: la baja tocó a otra persona", members, other)
	}
	if members := membersOf(t, m.Repo, m.TenantB); len(members) != 1 || members[0].UserID != user {
		t.Fatalf("MembersOf(B) = %+v, quiero a %s: la baja en A tocó su membresía en B", members, user)
	}
}

func membershipRemoveMissing(t *testing.T, m MontajeMembershipRepo) {
	if err := m.Repo.Remove(bg(), newUser(), m.TenantA); err != nil {
		t.Fatalf("Remove de una membresía que no existe: %v, quiero nil (no-op)", err)
	}
}

func membershipTenantsOfUserEmpty(t *testing.T, m MontajeMembershipRepo) {
	if tenants := tenantsOf(t, m.Repo, newUser()); len(tenants) != 0 {
		t.Fatalf("TenantsOfUser de quien no es miembro = %v, quiero vacía", tenants)
	}
}

func membershipUserTenantsEmpty(t *testing.T, m MontajeMembershipRepo) {
	tenants, err := m.Repo.UserTenants(bg(), newUser())
	if err != nil {
		t.Fatalf("UserTenants de quien no es miembro: %v, quiero sin error", err)
	}
	if tenants == nil || len(tenants) != 0 {
		t.Fatalf("UserTenants de quien no es miembro = %#v, quiero vacía y NO nil (se serializa como [])", tenants)
	}
}

func membershipUserTenantsOrder(t *testing.T, m MontajeMembershipRepo) {
	m.Features.GrantMultiCompany(t, m.TenantA)
	m.Features.GrantMultiCompany(t, m.TenantB)
	first, second := newUser(), newUser()
	mustAdd(t, m.Repo, first, m.TenantA)
	mustAdd(t, m.Repo, first, m.TenantB)
	// El orden es el de alta, no el de los identificadores: la segunda persona entra al revés.
	mustAdd(t, m.Repo, second, m.TenantB)
	mustAdd(t, m.Repo, second, m.TenantA)
	names := map[string]string{m.TenantA: m.NameA, m.TenantB: m.NameB}
	for user, want := range map[string][]string{first: {m.TenantA, m.TenantB}, second: {m.TenantB, m.TenantA}} {
		wantTenants(t, "TenantsOfUser", tenantsOf(t, m.Repo, user), want...)
		got, err := m.Repo.UserTenants(bg(), user)
		if err != nil {
			t.Fatalf("UserTenants(%s): %v", user, err)
		}
		if len(got) != len(want) {
			t.Fatalf("UserTenants(%s) = %+v, quiero %v", user, got, want)
		}
		for i, tenant := range want {
			if got[i].ID != tenant || got[i].DisplayName != names[tenant] {
				t.Fatalf("UserTenants(%s)[%d] = %+v, quiero {%s %s}: mismo orden que TenantsOfUser y con su nombre", user, i, got[i], tenant, names[tenant])
			}
		}
	}
}

func membershipUserTenantsOnlyOwn(t *testing.T, m MontajeMembershipRepo) {
	user, other := newUser(), newUser()
	mustAdd(t, m.Repo, user, m.TenantA)
	mustAdd(t, m.Repo, other, m.TenantB)
	got, err := m.Repo.UserTenants(bg(), user)
	if err != nil {
		t.Fatalf("UserTenants: %v", err)
	}
	if len(got) != 1 || got[0].ID != m.TenantA || got[0].DisplayName != m.NameA {
		t.Fatalf("UserTenants = %+v, quiero solo {%s %s}: devolvió una empresa de la que no es miembro", got, m.TenantA, m.NameA)
	}
}

func membershipMembersOf(t *testing.T, m MontajeMembershipRepo) {
	empty := membersOf(t, m.Repo, m.TenantA)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("MembersOf de una empresa sin miembros = %#v, quiero vacía y NO nil", empty)
	}
	first, second, foreign := newUser(), newUser(), newUser()
	mustAdd(t, m.Repo, first, m.TenantA)
	mustAdd(t, m.Repo, second, m.TenantA)
	mustAdd(t, m.Repo, foreign, m.TenantB)
	members := membersOf(t, m.Repo, m.TenantA)
	if len(members) != 2 || members[0].UserID != first || members[1].UserID != second {
		t.Fatalf("MembersOf(A) = %+v, quiero [%s %s] en orden de alta y sin %s (de otra empresa)", members, first, second, foreign)
	}
	for _, member := range members {
		if member.TenantID != m.TenantA || member.CreatedAt.IsZero() {
			t.Fatalf("MembersOf(A) trae %+v: quiero tenant %s y created_at", member, m.TenantA)
		}
	}
	if members[1].CreatedAt.Before(members[0].CreatedAt) {
		t.Fatalf("MembersOf(A): created_at %v antes que %v, quiero orden (created_at, user_id)", members[1].CreatedAt, members[0].CreatedAt)
	}
}
