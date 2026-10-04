package outhelpertest

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MontajeActiveTenantRepo es lo que cada implementación de out.ActiveTenantRepo entrega a la
// suite para UN caso: el repositorio, sin elecciones guardadas, sobre dos tenants que existen
// (user_active_tenant.tenant_id los referencia).
type MontajeActiveTenantRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.ActiveTenantRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
}

// ContratoActiveTenantRepo ejecuta las promesas de out.ActiveTenantRepo contra la implementación
// que devuelve nuevo, con un Montaje limpio por caso: la ausencia es ok=false sin error, guardar
// se lee, un segundo guardado REEMPLAZA (un valor por persona), repetir es idempotente y la
// elección de una persona no toca la de otra. Las personas de la suite no son miembros de nada:
// el puerto no comprueba membresías, y si lo hiciera los casos fallarían.
//
// Lo que NO afirma: updated_at, que el puerto no deja ver y nadie lee.
func ContratoActiveTenantRepo(t *testing.T, nuevo func(t *testing.T) MontajeActiveTenantRepo) {
	t.Helper()
	runCases(t, "ContratoActiveTenantRepo", nuevo, validateActiveTenantMontaje, []testCase[MontajeActiveTenantRepo]{
		{"ActiveTenantOf_NothingSaved_NotOkWithoutError", activeTenantAbsent},
		{"SetActiveTenant_ThenRead", activeTenantSetRead},
		{"SetActiveTenant_Replaces_OneValuePerUser", activeTenantReplaces},
		{"SetActiveTenant_IsIdempotent", activeTenantIdempotent},
		{"SetActiveTenant_DoesNotTouchOtherUsers", activeTenantPerUser},
	})
}

func validateActiveTenantMontaje(t *testing.T, m MontajeActiveTenantRepo) {
	t.Helper()
	if m.Repo == nil {
		t.Fatal("MontajeActiveTenantRepo.Repo es nil")
	}
	validateTenants(t, m.TenantA, m.TenantB)
}

// mustSetActive guarda la empresa activa y falla el test si no puede.
func mustSetActive(t *testing.T, repo out.ActiveTenantRepo, userID, tenantID string) {
	t.Helper()
	if err := repo.SetActiveTenant(bg(), userID, tenantID); err != nil {
		t.Fatalf("SetActiveTenant(%s, %s): %v", userID, tenantID, err)
	}
}

// wantActive exige que la empresa activa de userID sea want (o ninguna si want es "").
func wantActive(t *testing.T, repo out.ActiveTenantRepo, userID, want string) {
	t.Helper()
	got, ok, err := repo.ActiveTenantOf(bg(), userID)
	if err != nil {
		t.Fatalf("ActiveTenantOf(%s): %v", userID, err)
	}
	if ok != (want != "") || got != want {
		t.Fatalf("ActiveTenantOf(%s) = (%q, %v), quiero (%q, %v)", userID, got, ok, want, want != "")
	}
}

func activeTenantAbsent(t *testing.T, m MontajeActiveTenantRepo) {
	wantActive(t, m.Repo, newUser(), "")
}

func activeTenantSetRead(t *testing.T, m MontajeActiveTenantRepo) {
	user := newUser()
	mustSetActive(t, m.Repo, user, m.TenantA)
	wantActive(t, m.Repo, user, m.TenantA)
}

func activeTenantReplaces(t *testing.T, m MontajeActiveTenantRepo) {
	user := newUser()
	mustSetActive(t, m.Repo, user, m.TenantA)
	mustSetActive(t, m.Repo, user, m.TenantB)
	wantActive(t, m.Repo, user, m.TenantB)
}

func activeTenantIdempotent(t *testing.T, m MontajeActiveTenantRepo) {
	user := newUser()
	mustSetActive(t, m.Repo, user, m.TenantA)
	mustSetActive(t, m.Repo, user, m.TenantA)
	wantActive(t, m.Repo, user, m.TenantA)
}

func activeTenantPerUser(t *testing.T, m MontajeActiveTenantRepo) {
	first, second := newUser(), newUser()
	mustSetActive(t, m.Repo, first, m.TenantA)
	mustSetActive(t, m.Repo, second, m.TenantB)
	wantActive(t, m.Repo, first, m.TenantA)
	wantActive(t, m.Repo, second, m.TenantB)
}
