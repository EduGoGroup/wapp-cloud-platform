package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// mustUpsertContent registra un blob y falla el test si el store devuelve error.
func mustUpsertContent(t *testing.T, repo *store.PostgresRepository, tenantID, ref, blob string) {
	t.Helper()
	if err := repo.UpsertTenantContent(context.Background(), tenantID, ref, []byte(blob)); err != nil {
		t.Fatalf("upsert (%s,%s): %v", tenantID, ref, err)
	}
}

// TestIntegration_TenantContentUpsertGetList ejercita upsert (alta + actualización),
// get y list de tenant_content (Plan 018 · T6) contra Postgres real. Gated por
// WAPP_TEST_DB_DSN (openTestDB salta si no hay BD).
func TestIntegration_TenantContentUpsertGetList(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantA := fmt.Sprintf("tc-a-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupContent(t, repo, tenantA, "menu", "catalogo") })

	mustUpsertContent(t, repo, tenantA, "menu", `{"prompt":"v1"}`)
	mustUpsertContent(t, repo, tenantA, "menu", `{"prompt":"v2"}`) // actualiza, no duplica
	mustUpsertContent(t, repo, tenantA, "catalogo", `{"items":[]}`)

	// Get devuelve la última versión (JSONB re-serializa: comparamos el campo parseado).
	got, err := repo.GetTenantContent(ctx, tenantA, "menu")
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	var parsed struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal blob %q: %v", string(got), err)
	}
	if parsed.Prompt != "v2" {
		t.Fatalf("prompt=%q, quiero v2 (upsert actualizó)", parsed.Prompt)
	}

	// List trae SOLO los refs del tenant, ordenados por ref, con timestamps.
	list, err := repo.ListTenantContent(ctx, tenantA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Ref != "catalogo" || list[1].Ref != "menu" {
		t.Fatalf("list A = %+v, quiero [catalogo, menu]", list)
	}
	if list[1].CreatedAt.IsZero() || list[1].UpdatedAt.IsZero() {
		t.Fatalf("timestamps vacíos en %+v", list[1])
	}
}

// TestIntegration_TenantContentDeleteIsolation ejercita delete + aislamiento por
// tenant de tenant_content (Plan 018 · T6) contra Postgres real. Gated.
func TestIntegration_TenantContentDeleteIsolation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantA := fmt.Sprintf("tc-a-%d", time.Now().UnixNano())
	tenantB := fmt.Sprintf("tc-b-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupContent(t, repo, tenantB, "menu") })

	mustUpsertContent(t, repo, tenantA, "menu", `{"prompt":"a"}`)
	mustUpsertContent(t, repo, tenantB, "menu", `{"prompt":"b"}`) // mismo ref, otro tenant

	// Delete del ref de A.
	if err := repo.DeleteTenantContent(ctx, tenantA, "menu"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetTenantContent(ctx, tenantA, "menu"); !errors.Is(err, store.ErrTenantContentNotFound) {
		t.Fatalf("tras delete quiero ErrTenantContentNotFound, got %v", err)
	}
	// Re-delete → not found (idempotente hacia el 404 del transporte).
	if err := repo.DeleteTenantContent(ctx, tenantA, "menu"); !errors.Is(err, store.ErrTenantContentNotFound) {
		t.Fatalf("re-delete quiero ErrTenantContentNotFound, got %v", err)
	}
	// El de tenantB sigue vivo (aislamiento INV-8: el WHERE tenant_id lo protegió).
	if _, err := repo.GetTenantContent(ctx, tenantB, "menu"); err != nil {
		t.Fatalf("el contenido de tenantB no debió tocarse: %v", err)
	}
}

// cleanupContent borra best-effort los refs del tenant al cerrar el test.
func cleanupContent(t *testing.T, repo *store.PostgresRepository, tenantID string, refs ...string) {
	t.Helper()
	for _, ref := range refs {
		if err := repo.DeleteTenantContent(context.Background(), tenantID, ref); err != nil &&
			!errors.Is(err, store.ErrTenantContentNotFound) {
			t.Logf("cleanup (%s,%s): %v", tenantID, ref, err)
		}
	}
}
