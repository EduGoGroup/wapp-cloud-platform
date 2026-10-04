package platformadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
)

// Repository implementa el puerto de empresas (el de solicitudes llega con
// access_requests_postgres.go).
var _ TenantStore = (*Repository)(nil)

// El constructor recibe el pool y el resolver de derechos del alta, y no devuelve error.
var _ func(*sql.DB, iampostgres.FeatureResolver) *Repository = NewRepository

// yesResolver es un FeatureResolver que concede todo.
type yesResolver struct{}

func (yesResolver) Has(context.Context, string, string) (bool, error) { return true, nil }

// NewRepository no valida ni toca la base: con db nil devuelve un repositorio (que valida igual
// antes de consultar) y guarda el resolver tal cual, que es el que viajará a GrantTenantAccess.
func TestNewRepository_KeepsTheResolverWithoutTouchingTheDB(t *testing.T) {
	features := yesResolver{}
	r := NewRepository(nil, features)
	if r == nil {
		t.Fatal("NewRepository(nil, …) devolvió nil")
	}
	if r.features != features {
		t.Fatalf("features = %#v, quiero el resolver recibido", r.features)
	}
	if _, err := r.CreateTenant(context.Background(), "", "x", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateTenant sin slug = %v, quiero ErrInvalidInput sin tocar la base", err)
	}
	if NewRepository(nil, nil).features != nil {
		t.Fatal("con features nil el repositorio guarda nil (la regla de una empresa queda en «no»)")
	}
}

// clampPage es la regla de la página de empresas que comparten el adaptador y el handler. Su tope
// superior (500) no lo afirma la suite del puerto (sembrar 501 empresas por caso no compensa).
func TestClampPage(t *testing.T) {
	for _, c := range []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"ZeroLimit_Default50", 0, 0, 50, 0},
		{"NegativeLimit_Default50", -7, 3, 50, 3},
		{"InRange_Kept", 1, 1, 1, 1},
		{"AtMax_Kept", 500, 0, 500, 0},
		{"AboveMax_500", 501, 0, 500, 0},
		{"NegativeOffset_Zero", 10, -1, 10, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			limit, offset := clampPage(c.limit, c.offset)
			if limit != c.wantLimit || offset != c.wantOffset {
				t.Fatalf("clampPage(%d, %d) = (%d, %d), quiero (%d, %d)", c.limit, c.offset, limit, offset, c.wantLimit, c.wantOffset)
			}
		})
	}
}

// isUniqueViolation reconoce el 23505 de Postgres, también envuelto, y solo ese: es lo que
// convierte un slug repetido en ErrConflict (409) en vez de un 500.
func TestIsUniqueViolation(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"UniqueViolation", &pgconn.PgError{Code: "23505"}, true},
		{"Wrapped", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"}), true},
		{"ForeignKeyViolation", &pgconn.PgError{Code: "23503"}, false},
		{"NotPostgres", errors.New("23505"), false},
		{"Nil", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isUniqueViolation(c.err); got != c.want {
				t.Fatalf("isUniqueViolation(%v) = %v, quiero %v", c.err, got, c.want)
			}
		})
	}
}

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
