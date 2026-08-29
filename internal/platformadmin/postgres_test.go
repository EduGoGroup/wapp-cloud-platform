package platformadmin_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
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
	repo := platformadmin.NewRepository(db, nil)
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
	repo := platformadmin.NewRepository(db, nil)
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
	repo := platformadmin.NewRepository(db, nil)
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

// TestIntegration_ListTenants_PaginationStableTiebreak fija M-07: N tenants
// con el MISMO created_at (el seed real los comparte con now()) tienen que
// aparecer TODOS al paginar de una en una, sin repetir ni omitir ninguno.
// Antes del fix, `ORDER BY created_at DESC` a secas no garantiza un orden
// estable entre filas empatadas: una página podía devolver la misma fila que
// la anterior y dejar otra fuera para siempre. El empate se fija a un
// instante futuro ÚNICO por corrida (now + 400 días, con precisión de
// nanosegundo) para aislarlo de cualquier otro tenant, sembrado en paralelo
// por el resto de la suite o dejado por una corrida anterior contra la MISMA
// base -- una fecha futura fija habría chocado consigo misma al repetir el
// test sin recrear el Postgres efímero.
//
// N=2 (la versión original) NO discriminaba (Tanda 6 · 2.2), verificado por
// mutación: quitando `, id DESC` de postgres.go:99, la versión de 2 filas
// seguía en VERDE. Razón medida: para una tabla ESTÁTICA (sin escrituras
// entremedias), Postgres repite el MISMO orden físico en dos consultas
// independientes -- el "orden inestable" que `id DESC` previene es una
// garantía del ESTÁNDAR SQL, no algo que un docker Postgres sin concurrencia
// vaya a exhibir espontáneamente entre dos SELECT consecutivos sobre la
// misma foto de la tabla. Con solo 2 filas, "sin repetir, sin omitir" además
// se cumple por pura aritmética de conjuntos (offset 0/1 sobre 2 elementos
// particiona el par exacto sea cual sea el orden), así que ni siquiera hacía
// falta la inestabilidad para pasar.
//
// El fix real: los ids son gen_random_uuid() (CreateTenant, postgres.go:250),
// así que el orden de INSERCIÓN -- que es el orden físico que un `ORDER BY
// created_at DESC` a secas expone para las filas empatadas, al no tocarse
// nada entre el INSERT y el SELECT -- es independiente del orden por id. Se
// arma un golden ASC/DESC por id (conocido de antemano, no observado) y se
// compara la secuencia completa que entrega la paginación, offset a offset,
// contra ese golden: con `id DESC` en el ORDER BY debe coincidir EXACTO; sin
// él, coincide con el orden de inserción (que no es el golden por id, con
// probabilidad efectivamente 1 al ser ids aleatorios) y el test cae. Esto SÍ
// es discriminante contra el mutante -- comprueba el ORDEN exigido por el
// contrato, no solo "no faltan ni sobran filas" (que ya cubría la versión
// vieja, sin distinguir el bug).
func TestIntegration_ListTenants_PaginationStableTiebreak(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	const tieCount = 12
	tie := time.Now().UTC().Add(400 * 24 * time.Hour)
	ids := make([]string, 0, tieCount)
	for i := 0; i < tieCount; i++ {
		slug := fmt.Sprintf("pa-tie-%d-%d", i, time.Now().UnixNano())
		ten, err := repo.CreateTenant(ctx, slug, fmt.Sprintf("Tie %d", i), nil)
		if err != nil {
			t.Fatalf("CreateTenant tie[%d]: %v", i, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE public.tenants SET created_at = $1 WHERE id = $2`, tie, ten.ID); err != nil {
			t.Fatalf("igualar created_at tie[%d]: %v", i, err)
		}
		ids = append(ids, ten.ID)
	}

	// Golden: mismos ids, ordenados DESC como texto -- la representación
	// canónica minúscula (google/uuid y el `id::text` de Postgres coinciden)
	// compara byte a byte en el mismo orden que el tipo UUID de Postgres,
	// porque los guiones caen en las MISMAS posiciones fijas en todo UUID
	// v4 y no alteran el orden relativo de los caracteres hex que sí varían.
	wantOrder := append([]string(nil), ids...)
	sort.Slice(wantOrder, func(i, j int) bool { return wantOrder[i] > wantOrder[j] })

	var gotOrder []string
	seen := make(map[string]int, tieCount)
	for offset := 0; offset < tieCount; offset++ {
		page, err := repo.ListTenants(ctx, 1, offset)
		if err != nil {
			t.Fatalf("ListTenants offset=%d: %v", offset, err)
		}
		if len(page) != 1 {
			t.Fatalf("ListTenants offset=%d: esperada 1 fila, obtenidas %d", offset, len(page))
		}
		gotOrder = append(gotOrder, page[0].ID)
		seen[page[0].ID]++
	}

	var dup, missing []string
	for id, count := range seen {
		if count > 1 {
			dup = append(dup, id)
		}
	}
	for _, id := range ids {
		if seen[id] == 0 {
			missing = append(missing, id)
		}
	}
	if len(dup) > 0 || len(missing) > 0 {
		t.Fatalf("paginación de %d filas empatadas: duplicadas=%v faltantes=%v, orden observado=%v",
			tieCount, dup, missing, gotOrder)
	}

	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("paginación con empate NO siguió el desempate `id DESC`:\n got  = %v\n want = %v", gotOrder, wantOrder)
	}
}

func TestIntegration_ListInstallations(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	slug := fmt.Sprintf("pa-inst-%d", time.Now().UnixNano())
	created, err := repo.CreateTenant(ctx, slug, "Installations Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	edge1 := "edge-001"
	edge2 := "edge-002"

	// Sembrar fleet_sessions. El perfil va EXPLÍCITO (Plan 046 · T3.1) aunque a
	// ListInstallations le dé igual —su query no lee la columna profile—: estas
	// sesiones no responden a nadie, así que se declaran 'passive' en vez de caer
	// al DEFAULT de la 0063; que el valor coincida con el default es coincidencia
	// declarada, no dependencia. Si algún día este test necesitara sesiones que
	// respondan, el sitio donde activarlas es este INSERT, a la vista.
	_, err = db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile, capabilities, last_seen_at)
		VALUES
			($1, $2, 'sess-1', 'online', 'passive', '{"whatsapp":true}', now()),
			($1, $2, 'sess-2', 'online', 'passive', '{"whatsapp":true}', now()),
			($1, $3, 'sess-3', 'offline', 'passive', '{"whatsapp":true}', now())
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
