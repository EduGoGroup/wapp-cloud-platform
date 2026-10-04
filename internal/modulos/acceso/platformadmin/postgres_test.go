//go:build pendiente

package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Este test es INTERNO (package platformadmin): prueba del adaptador Postgres solo lo que no
// necesita base (05 E-6). Su SQL lo prueba la suite platformadminhelpertest.Contrato contra
// Postgres, en los procesos de F9 (test/procesos/platformadmin_contrato_test.go).

// Aserciones de compilación: los métodos del adaptador tienen la firma del puerto TenantStore.
var (
	_ func(*Repository, context.Context, int, int) ([]TenantListItem, error)             = (*Repository).ListTenants
	_ func(*Repository, context.Context, string) (TenantDetail, error)                   = (*Repository).GetTenant
	_ func(*Repository, context.Context, string) (bool, error)                           = (*Repository).ExistsTenant
	_ func(*Repository, context.Context, string, string, *string) (CreatedTenant, error) = (*Repository).CreateTenant
	_ func(*Repository, context.Context, string) ([]InstallationItem, error)             = (*Repository).ListInstallations
)

// Los tres centinelas del adaptador conservan su texto, byte a byte (diseño F2 §5).
func TestSentinels_LiteralTexts(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrNotFound", ErrNotFound, "platformadmin: recurso no encontrado"},
		{"ErrConflict", ErrConflict, "platformadmin: recurso ya existe o conflicto de unicidad"},
		{"ErrInvalidInput", ErrInvalidInput, "platformadmin: entrada inválida"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() != c.want {
				t.Fatalf("%s = %q, quiero %q", c.name, c.err.Error(), c.want)
			}
		})
	}
}

// Los tags JSON de los DTO son contrato con la consola de plataforma: un nombre de campo
// cambiado rompe la consola sin que nada del servidor se entere. Los punteros nil salen null.
func TestDTOs_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	plan := "pro"
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{
			"TenantListItem_NullPlanAndRevocation",
			TenantListItem{ID: "t1", Slug: "s", DisplayName: "D", CreatedAt: at, UpdatedAt: at},
			`{"id":"t1","slug":"s","display_name":"D","plan_id":null,"revoked_at":null,` +
				`"created_at":"2026-10-04T12:00:00Z","updated_at":"2026-10-04T12:00:00Z"}`,
		},
		{
			"TenantDetail_WithPlanAndRevocation",
			TenantDetail{
				ID: "t1", Slug: "s", DisplayName: "D", PlanID: &plan, RevokedAt: &at, CreatedAt: at, UpdatedAt: at,
				InstallationsCount: 2, Features: []string{"a", "b"},
			},
			`{"id":"t1","slug":"s","display_name":"D","plan_id":"pro","revoked_at":"2026-10-04T12:00:00Z",` +
				`"created_at":"2026-10-04T12:00:00Z","updated_at":"2026-10-04T12:00:00Z",` +
				`"installations_count":2,"features":["a","b"]}`,
		},
		{"CreatedTenant", CreatedTenant{ID: "t1", Slug: "s"}, `{"id":"t1","slug":"s"}`},
		{
			"InstallationItem_NeverSeen",
			InstallationItem{EdgeID: "e1", Sessions: 3},
			`{"edge_id":"e1","sessions":3,"last_seen_at":null,"lease_revoked":false}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("JSON = %s\nquiero  %s", got, c.want)
			}
		})
	}
}

// CreateTenant valida ANTES de tocar la base: con un Repository sin pool (cualquier consulta
// sería un pánico) slug o display_name vacíos devuelven ErrInvalidInput con el motivo de siempre.
func TestRepository_CreateTenant_RequiresSlugAndDisplayName_WithoutTouchingTheDB(t *testing.T) {
	const want = "platformadmin: entrada inválida: slug y display_name son requeridos"
	for _, c := range []struct{ name, slug, displayName string }{
		{"EmptySlug", "", "Empresa"},
		{"EmptyDisplayName", "empresa", ""},
		{"BothEmpty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			created, err := (&Repository{}).CreateTenant(context.Background(), c.slug, c.displayName, nil)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, quiero ErrInvalidInput", err)
			}
			if err.Error() != want {
				t.Fatalf("texto = %q, quiero %q", err.Error(), want)
			}
			if created != (CreatedTenant{}) {
				t.Fatalf("con error no se devuelve empresa: %+v", created)
			}
		})
	}
}
