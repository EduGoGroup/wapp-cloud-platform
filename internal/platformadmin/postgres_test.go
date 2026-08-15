package platformadmin_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	"github.com/google/uuid"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

func TestIntegration_CreateTenant(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	// Crear tenant
	slug := fmt.Sprintf("pa-test-%d", time.Now().UnixNano())
	displayName := "Platform Admin Test Tenant"
	planPro := "pro"
	created, err := repo.CreateTenant(ctx, slug, displayName, &planPro)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if created.ID == "" || created.Slug != slug {
		t.Fatalf("Created: %+v", created)
	}

	// Conflicto de slug duplicado
	_, err = repo.CreateTenant(ctx, slug, "Duplicado", nil)
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("esperado ErrConflict, obtenido: %v", err)
	}

	// Entrada inválida
	_, err = repo.CreateTenant(ctx, "", "", nil)
	if !errors.Is(err, platformadmin.ErrInvalidInput) {
		t.Fatalf("esperado ErrInvalidInput, obtenido: %v", err)
	}
}

func TestIntegration_GetTenant(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	slug := fmt.Sprintf("pa-get-%d", time.Now().UnixNano())
	displayName := "Platform Admin Get Tenant"
	planPro := "pro"
	created, err := repo.CreateTenant(ctx, slug, displayName, &planPro)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// GetTenant
	detail, err := repo.GetTenant(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if detail.ID != created.ID || detail.Slug != slug || detail.DisplayName != displayName {
		t.Fatalf("detalle inesperado: %+v", detail)
	}
	if detail.PlanID == nil || *detail.PlanID != "pro" {
		t.Fatalf("plan_id esperado 'pro', obtenido: %v", detail.PlanID)
	}
	if detail.RevokedAt != nil {
		t.Fatalf("revoked_at esperado nil, obtenido: %v", detail.RevokedAt)
	}
	if detail.InstallationsCount != 0 {
		t.Fatalf("installations_count esperado 0, obtenido: %d", detail.InstallationsCount)
	}
	if len(detail.Features) == 0 {
		t.Error("esperadas features para plan pro, obtenidas 0")
	}

	// GetTenant inexistente
	_, err = repo.GetTenant(ctx, uuid.NewString())
	if !errors.Is(err, platformadmin.ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, obtenido: %v", err)
	}
}

func TestIntegration_ListTenants_PaginationAndRevocation(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	// Crear 2 tenants, uno revocado
	slug1 := fmt.Sprintf("pa-list-1-%d", time.Now().UnixNano())
	slug2 := fmt.Sprintf("pa-list-2-%d", time.Now().UnixNano())
	t1, err := repo.CreateTenant(ctx, slug1, "List 1", nil)
	if err != nil {
		t.Fatalf("CreateTenant 1: %v", err)
	}
	t2, err := repo.CreateTenant(ctx, slug2, "List 2", nil)
	if err != nil {
		t.Fatalf("CreateTenant 2: %v", err)
	}

	// Revocar t2
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `UPDATE public.tenants SET revoked_at = $1 WHERE id = $2`, now, t2.ID)
	if err != nil {
		t.Fatalf("revocar t2: %v", err)
	}

	// ListTenants con limit alto debe recortarse a 500 y funcionar
	items, err := repo.ListTenants(ctx, 1000, 0)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("esperados al menos 2 tenants, obtenidos: %d", len(items))
	}

	var foundT1, foundT2 bool
	for _, item := range items {
		if item.ID == t1.ID {
			foundT1 = true
			if item.RevokedAt != nil {
				t.Fatalf("t1.RevokedAt esperado nil, obtenido: %v", item.RevokedAt)
			}
		}
		if item.ID == t2.ID {
			foundT2 = true
			if item.RevokedAt == nil {
				t.Fatal("t2.RevokedAt esperado no-nil")
			}
		}
	}
	if !foundT1 || !foundT2 {
		t.Fatalf("tenants no encontrados en listado: t1=%v, t2=%v", foundT1, foundT2)
	}
}

// TestIntegration_ListTenants_PaginationStableTiebreak fija M-07: dos tenants
// con el MISMO created_at (el seed real los comparte con now()) tienen que
// aparecer los DOS al paginar de una en una, sin repetir ninguno. Antes del
// fix, `ORDER BY created_at DESC` a secas no garantiza un orden estable entre
// filas empatadas: una página podía devolver la misma fila que la anterior y
// dejar la otra fuera para siempre. El empate se fija a un instante futuro
// ÚNICO por corrida (now + 400 días, con precisión de nanosegundo) para
// aislarlo de cualquier otro tenant, sembrado en paralelo por el resto de la
// suite o dejado por una corrida anterior contra la MISMA base -- una fecha
// futura fija habría chocado consigo misma al repetir el test sin recrear el
// Postgres efímero.
func TestIntegration_ListTenants_PaginationStableTiebreak(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	slug1 := fmt.Sprintf("pa-tie-1-%d", time.Now().UnixNano())
	slug2 := fmt.Sprintf("pa-tie-2-%d", time.Now().UnixNano())
	t1, err := repo.CreateTenant(ctx, slug1, "Tie 1", nil)
	if err != nil {
		t.Fatalf("CreateTenant t1: %v", err)
	}
	t2, err := repo.CreateTenant(ctx, slug2, "Tie 2", nil)
	if err != nil {
		t.Fatalf("CreateTenant t2: %v", err)
	}

	tie := time.Now().UTC().Add(400 * 24 * time.Hour)
	if _, err := db.ExecContext(ctx, `UPDATE public.tenants SET created_at = $1 WHERE id IN ($2, $3)`, tie, t1.ID, t2.ID); err != nil {
		t.Fatalf("igualar created_at: %v", err)
	}

	page0, err := repo.ListTenants(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListTenants offset=0: %v", err)
	}
	page1, err := repo.ListTenants(ctx, 1, 1)
	if err != nil {
		t.Fatalf("ListTenants offset=1: %v", err)
	}
	if len(page0) != 1 || len(page1) != 1 {
		t.Fatalf("esperada 1 fila por página: len(page0)=%d len(page1)=%d", len(page0), len(page1))
	}

	if page0[0].ID == page1[0].ID {
		t.Fatalf("paginación con empate repitió la misma empresa (%s) en offset=0 y offset=1", page0[0].ID)
	}
	seen := map[string]bool{page0[0].ID: true, page1[0].ID: true}
	if !seen[t1.ID] || !seen[t2.ID] {
		t.Fatalf("paginación con empate omitió una empresa: page0=%s page1=%s, want t1=%s t2=%s",
			page0[0].ID, page1[0].ID, t1.ID, t2.ID)
	}
}

func TestIntegration_ListInstallations(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	slug := fmt.Sprintf("pa-inst-%d", time.Now().UnixNano())
	created, err := repo.CreateTenant(ctx, slug, "Installations Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	edge1 := "edge-001"
	edge2 := "edge-002"

	// Sembrar fleet_sessions
	_, err = db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, capabilities, last_seen_at)
		VALUES 
			($1, $2, 'sess-1', 'online', '{"whatsapp":true}', now()),
			($1, $2, 'sess-2', 'online', '{"whatsapp":true}', now()),
			($1, $3, 'sess-3', 'offline', '{"whatsapp":true}', now())
	`, created.ID, edge1, edge2)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions: %v", err)
	}

	// Sembrar leases: edge1 activo, edge2 revocado
	_, err = db.ExecContext(ctx, `
		INSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)
		VALUES 
			($1, $2, 1, now() + interval '1 day', false, now(), now()),
			($1, $3, 1, now() + interval '1 day', true, now(), now())
	`, created.ID, edge1, edge2)
	if err != nil {
		t.Fatalf("sembrar leases: %v", err)
	}

	insts, err := repo.ListInstallations(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("esperadas 2 instalaciones, obtenidas: %d", len(insts))
	}

	if insts[0].EdgeID != edge1 || insts[0].Sessions != 2 || insts[0].LeaseRevoked != false {
		t.Fatalf("insts[0] inesperada: %+v", insts[0])
	}
	if insts[1].EdgeID != edge2 || insts[1].Sessions != 1 || insts[1].LeaseRevoked != true {
		t.Fatalf("insts[1] inesperada: %+v", insts[1])
	}
}
