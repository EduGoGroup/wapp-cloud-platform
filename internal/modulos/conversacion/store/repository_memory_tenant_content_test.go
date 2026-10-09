//go:build pendiente

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Aserciones de compilación de lo que promete repository_memory_tenant_content.go.
var (
	_ func(*store.MemoryRepository, context.Context, string, string) ([]byte, error)               = (*store.MemoryRepository).GetTenantContent
	_ func(*store.MemoryRepository, string, string, []byte)                                        = (*store.MemoryRepository).SetTenantContent
	_ func(*store.MemoryRepository, context.Context, string, string, []byte) error                 = (*store.MemoryRepository).UpsertTenantContent
	_ func(*store.MemoryRepository, context.Context, string, string, []byte, string) (int, error)  = (*store.MemoryRepository).ReplaceTenantContentVersioned
	_ func(*store.MemoryRepository, string, string) []store.TenantContentVersion                   = (*store.MemoryRepository).TenantContentVersions
	_ func(*store.MemoryRepository, context.Context, string) ([]store.TenantContentSummary, error) = (*store.MemoryRepository).ListTenantContent
	_ func(*store.MemoryRepository, context.Context, string, string) error                         = (*store.MemoryRepository).DeleteTenantContent
)

// TestMemoryRepository_SetTenantContent: la siembra de tests deja el blob legible BYTE A BYTE y
// solo para ese (tenant, ref); guarda una copia; sustituye sin archivar versión; y, como no fecha,
// la ref sale en el listado con sus marcas de tiempo a cero.
func TestMemoryRepository_SetTenantContent(t *testing.T) {
	repo, _ := newMemoryRepository()
	raw := []byte(`{ "prompt" :  "Hola" }`)
	repo.SetTenantContent("t1", "catalogo", raw)
	raw[2] = 'X'

	got, err := repo.GetTenantContent(ctx, "t1", "catalogo")
	if err != nil || string(got) != `{ "prompt" :  "Hola" }` {
		t.Fatalf("GetTenantContent = (%s, %v), quería el blob sembrado byte a byte", got, err)
	}
	if _, err := repo.GetTenantContent(ctx, "t2", "catalogo"); !errors.Is(err, store.ErrTenantContentNotFound) {
		t.Errorf("el blob sembrado en t1 se ve desde t2: err = %v", err)
	}

	list, err := repo.ListTenantContent(ctx, "t1")
	if err != nil || len(list) != 1 || list[0].Ref != "catalogo" || !list[0].CreatedAt.IsZero() || !list[0].UpdatedAt.IsZero() {
		t.Errorf("ListTenantContent = (%+v, %v), quería la ref sembrada con sus marcas a cero", list, err)
	}

	repo.SetTenantContent("t1", "catalogo", []byte(`{"prompt":"Otro"}`))
	if got, err := repo.GetTenantContent(ctx, "t1", "catalogo"); err != nil || string(got) != `{"prompt":"Otro"}` {
		t.Errorf("tras sembrar otra vez = (%s, %v), quería el blob nuevo", got, err)
	}
	if versions := repo.TenantContentVersions("t1", "catalogo"); len(versions) != 0 {
		t.Errorf("SetTenantContent archivó %d versiones, quería ninguna", len(versions))
	}
}

// seedThreeImports hace tres imports sobre (t1, catalogo), adelantando el reloj un segundo tras
// cada uno: quedan archivadas las versiones 1 y 2.
func seedThreeImports(t *testing.T, repo *store.MemoryRepository, clock *testClock) {
	t.Helper()
	for i, raw := range []string{`{"v":1}`, `{"v":2}`, `{"v":3}`} {
		archived, err := repo.ReplaceTenantContentVersioned(ctx, "t1", "catalogo", []byte(raw), store.VersionSourceManual)
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		requireEqual(t, "versión archivada por el import", archived, i)
		clock.Advance(time.Second)
	}
}

// TestMemoryRepository_TenantContentVersions: el mirador devuelve las versiones archivadas de ESA
// (tenant, ref) en orden, fechadas por el reloj inyectado y con el blob byte a byte. Sin versiones,
// ninguna.
func TestMemoryRepository_TenantContentVersions(t *testing.T) {
	repo, clock := newMemoryRepository()
	requireEqual(t, "versiones de una ref sin historia", len(repo.TenantContentVersions("t1", "catalogo")), 0)
	start := clock.Now()
	seedThreeImports(t, repo, clock)

	got := repo.TenantContentVersions("t1", "catalogo")
	requireEqual(t, "versiones archivadas", len(got), 2)
	for i, want := range []string{`{"v":1}`, `{"v":2}`} {
		requireEqual(t, "número de versión", got[i].Version, i+1)
		requireEqual(t, "blob archivado", string(got[i].Content), want)
		requireEqual(t, "procedencia", got[i].Source, store.VersionSourceManual)
		// La versión i+1 la archivó el import i+2, que corrió i+1 segundos de reloj después del primero.
		requireEqual(t, "fecha de archivado", got[i].CreatedAt, start.Add(time.Duration(i+1)*time.Second))
	}
	requireEqual(t, "versiones vistas desde otro tenant", len(repo.TenantContentVersions("t2", "catalogo")), 0)
}

// TestMemoryRepository_TenantContentVersions_CopiesAndSurvivesDelete: lo que devuelve el mirador es
// una COPIA —mutarlo no cambia lo guardado—, y borrar el blob deja la historia donde estaba.
func TestMemoryRepository_TenantContentVersions_CopiesAndSurvivesDelete(t *testing.T) {
	repo, clock := newMemoryRepository()
	seedThreeImports(t, repo, clock)

	got := repo.TenantContentVersions("t1", "catalogo")
	got[0].Content[2] = 'X'
	got[0].Source = "mutado"
	again := repo.TenantContentVersions("t1", "catalogo")
	requireEqual(t, "blob guardado tras mutar el devuelto", string(again[0].Content), `{"v":1}`)
	requireEqual(t, "procedencia guardada tras mutar la devuelta", again[0].Source, store.VersionSourceManual)

	// UpsertTenantContent y DeleteTenantContent son de los dos adaptadores (los afirma la suite):
	// aquí solo se mira que ninguno toca la historia.
	if err := repo.UpsertTenantContent(ctx, "t1", "catalogo", []byte(`{"v":4}`)); err != nil {
		t.Fatalf("UpsertTenantContent: %v", err)
	}
	if err := repo.DeleteTenantContent(ctx, "t1", "catalogo"); err != nil {
		t.Fatalf("DeleteTenantContent: %v", err)
	}
	requireEqual(t, "versiones tras sobrescribir y borrar el blob", len(repo.TenantContentVersions("t1", "catalogo")), 2)
}
