package outhelpertest

import (
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MontajeGrantRepo es lo que cada implementación de out.GrantRepo entrega a la suite para UN
// caso: el repositorio, sin overrides. No lleva tenants: public.iam_user_grants no tiene
// tenant_id (por eso el usecase solo deja escribir overrides sobre miembros del tenant, R-U22).
type MontajeGrantRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.GrantRepo
}

// ContratoGrantRepo ejecuta las promesas de out.GrantRepo contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso: alta idempotente por (user, pattern, effect), baja de
// ese override y solo de ese, y lectura acotada a la persona. El orden de GrantsOfUser no es
// contrato y la suite no lo mira.
func ContratoGrantRepo(t *testing.T, nuevo func(t *testing.T) MontajeGrantRepo) {
	t.Helper()
	runCases(t, "ContratoGrantRepo", nuevo, validateGrantMontaje, []testCase[MontajeGrantRepo]{
		{"AddUserGrant_IsIdempotent_ByPatternAndEffect", grantAddIdempotent},
		{"RemoveUserGrant_OnlyThatOverride", grantRemove},
		{"GrantsOfUser_OnlyThatUser_EmptyWithoutError", grantOnlyThatUser},
	})
}

func validateGrantMontaje(t *testing.T, m MontajeGrantRepo) {
	t.Helper()
	if m.Repo == nil {
		t.Fatal("MontajeGrantRepo.Repo es nil")
	}
}

// userGrants devuelve GrantsOfUser ordenados y falla el test si hay error.
func userGrants(t *testing.T, repo out.GrantRepo, userID string) []domain.Grant {
	t.Helper()
	grants, err := repo.GrantsOfUser(bg(), userID)
	if err != nil {
		t.Fatalf("GrantsOfUser(%s): %v", userID, err)
	}
	return sortedGrants(grants)
}

// mustAddUserGrants añade los overrides y falla el test si alguno no se puede.
func mustAddUserGrants(t *testing.T, repo out.GrantRepo, userID string, grants ...domain.Grant) {
	t.Helper()
	for _, g := range grants {
		if err := repo.AddUserGrant(bg(), userID, g); err != nil {
			t.Fatalf("AddUserGrant(%s, %+v): %v", userID, g, err)
		}
	}
}

var (
	grantAllow = domain.Grant{Pattern: "messages.send", Effect: domain.EffectAllow}
	grantDeny  = domain.Grant{Pattern: "messages.send", Effect: domain.EffectDeny}
	grantRead  = domain.Grant{Pattern: "*.read", Effect: domain.EffectAllow}
)

func grantAddIdempotent(t *testing.T, m MontajeGrantRepo) {
	user := newUser()
	mustAddUserGrants(t, m.Repo, user, grantAllow, grantAllow, grantDeny)
	if got, want := userGrants(t, m.Repo, user), sortedGrants([]domain.Grant{grantAllow, grantDeny}); !slices.Equal(got, want) {
		t.Fatalf("GrantsOfUser = %+v, quiero %+v: el mismo override dos veces es uno, y otro efecto es otro", got, want)
	}
}

func grantRemove(t *testing.T, m MontajeGrantRepo) {
	user := newUser()
	mustAddUserGrants(t, m.Repo, user, grantAllow, grantDeny, grantRead)
	if err := m.Repo.RemoveUserGrant(bg(), user, grantAllow); err != nil {
		t.Fatalf("RemoveUserGrant: %v", err)
	}
	if err := m.Repo.RemoveUserGrant(bg(), user, grantAllow); err != nil {
		t.Fatalf("RemoveUserGrant de un override que ya no estaba: %v, quiero nil (no-op)", err)
	}
	if got, want := userGrants(t, m.Repo, user), sortedGrants([]domain.Grant{grantDeny, grantRead}); !slices.Equal(got, want) {
		t.Fatalf("GrantsOfUser tras quitar %+v = %+v, quiero %+v", grantAllow, got, want)
	}
}

func grantOnlyThatUser(t *testing.T, m MontajeGrantRepo) {
	user, other := newUser(), newUser()
	if got := userGrants(t, m.Repo, user); len(got) != 0 {
		t.Fatalf("GrantsOfUser de quien no tiene overrides = %+v, quiero vacía", got)
	}
	mustAddUserGrants(t, m.Repo, other, grantRead)
	mustAddUserGrants(t, m.Repo, user, grantAllow)
	if got := userGrants(t, m.Repo, user); !slices.Equal(got, []domain.Grant{grantAllow}) {
		t.Fatalf("GrantsOfUser = %+v, quiero solo los suyos [%+v]", got, grantAllow)
	}
	if err := m.Repo.RemoveUserGrant(bg(), user, grantRead); err != nil {
		t.Fatalf("RemoveUserGrant: %v", err)
	}
	if got := userGrants(t, m.Repo, other); !slices.Equal(got, []domain.Grant{grantRead}) {
		t.Fatalf("GrantsOfUser de otra persona = %+v, quiero intacto [%+v]", got, grantRead)
	}
}
