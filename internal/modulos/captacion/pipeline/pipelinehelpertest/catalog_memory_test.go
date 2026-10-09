package pipelinehelpertest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline/pipelinehelpertest"
)

// TestNewCatalogMemory_OneArticlePerLabel: un artículo por etiqueta, con su código, su SKU
// y su precio, en el orden en que se pasaron.
func TestNewCatalogMemory_OneArticlePerLabel(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Torta de chocolate", "Tequeños")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	idx, err := cat.Obtener(context.Background(), "t-1")
	if err != nil || idx == nil {
		t.Fatalf("Obtener = (%v, %v), se esperaba un índice y nil", idx, err)
	}
	if got := idx.Articulos(); got != 2 {
		t.Fatalf("el índice tiene %d artículos, se esperaban 2", got)
	}
	second, ok := idx.PorSKU("ART-2")
	if !ok {
		t.Fatal("el segundo artículo debía tener el SKU ART-2 y no está")
	}
	if second.Articulo.Label != "Tequeños" || second.Articulo.Price != 1000 || second.Articulo.Code != "2" || second.Categoria != "1" {
		t.Errorf("ART-2 = %+v en la categoría %q, se esperaba (código 2, Tequeños, 1000) en la categoría 1", second.Articulo, second.Categoria)
	}
}

// TestNewCatalogMemory_IndexesWithTheProductionNormalizer: «Café» casa «cafe». Con un
// normalizador de laboratorio el doble mentiría sobre lo que casa en producción.
func TestNewCatalogMemory_IndexesWithTheProductionNormalizer(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Café Ñandú")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	idx, err := cat.Obtener(context.Background(), "t-1")
	if err != nil {
		t.Fatalf("Obtener: error inesperado %v", err)
	}
	if got := idx.PorEtiqueta("cafe ñandu"); len(got) != 1 {
		t.Fatalf("PorEtiqueta(\"cafe ñandu\") dio %d coincidencias, se esperaba 1", len(got))
	}
}

// TestNewCatalogMemory_NoLabels_ServesAnEmptyCatalog: sin etiquetas el catálogo está vacío,
// que es un estado legítimo, no un error.
func TestNewCatalogMemory_NoLabels_ServesAnEmptyCatalog(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory()
	if err != nil || cat == nil {
		t.Fatalf("NewCatalogMemory() = (%v, %v), se esperaba un doble y nil", cat, err)
	}
	idx, err := cat.Obtener(context.Background(), "t-1")
	if err != nil || idx == nil {
		t.Fatalf("Obtener = (%v, %v), se esperaba un índice vacío y nil", idx, err)
	}
	if got := idx.Articulos(); got != 0 {
		t.Fatalf("el catálogo vacío tiene %d artículos", got)
	}
}

// TestCatalogMemory_Obtener_SameIndexForEveryTenant: el doble no distingue tenants.
func TestCatalogMemory_Obtener_SameIndexForEveryTenant(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Torta")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	a, errA := cat.Obtener(context.Background(), "t-a")
	b, errB := cat.Obtener(context.Background(), "t-b")
	if errA != nil || errB != nil {
		t.Fatalf("Obtener: errores inesperados %v, %v", errA, errB)
	}
	if a != b {
		t.Fatal("dos tenants recibieron índices distintos; el doble sirve siempre el mismo")
	}
}

// TestCatalogMemory_BreakRead_FailsUntilRepaired: BreakRead hace fallar la lectura con ESE
// error y sin índice; nil la repara.
func TestCatalogMemory_BreakRead_FailsUntilRepaired(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Torta")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	down := errors.New("tenant_content no contesta")
	cat.BreakRead(down)
	idx, err := cat.Obtener(context.Background(), "t-1")
	if !errors.Is(err, down) || idx != nil {
		t.Fatalf("con la lectura rota Obtener = (%v, %v), se esperaba (nil, %v)", idx, err, down)
	}
	cat.BreakRead(nil)
	if idx, err = cat.Obtener(context.Background(), "t-1"); err != nil || idx == nil {
		t.Fatalf("reparada la lectura Obtener = (%v, %v), se esperaba un índice y nil", idx, err)
	}
}

// TestCatalogMemory_Reads_CountsEveryCall: Reads cuenta todas las lecturas, también las
// que fallan, y arranca en cero.
func TestCatalogMemory_Reads_CountsEveryCall(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Torta")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	if got := cat.Reads(); got != 0 {
		t.Fatalf("un doble nuevo dice Reads() = %d, se esperaba 0", got)
	}
	if _, err := cat.Obtener(context.Background(), "t-1"); err != nil {
		t.Fatalf("Obtener: error inesperado %v", err)
	}
	cat.BreakRead(errors.New("caída"))
	if _, err := cat.Obtener(context.Background(), "t-1"); err == nil {
		t.Fatal("con la lectura rota se esperaba un error")
	}
	if got := cat.Reads(); got != 2 {
		t.Fatalf("Reads() = %d tras dos lecturas (una fallida), se esperaba 2", got)
	}
}

// TestCatalogMemory_ConcurrentUse: lecturas, roturas y conteos a la vez no se pisan (se
// corre con -race).
func TestCatalogMemory_ConcurrentUse(t *testing.T) {
	cat, err := pipelinehelpertest.NewCatalogMemory("Torta")
	if err != nil {
		t.Fatalf("NewCatalogMemory: error inesperado %v", err)
	}
	const readers = 16
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			if _, err := cat.Obtener(context.Background(), "t-1"); err != nil {
				t.Errorf("Obtener: error inesperado %v", err)
			}
			cat.BreakRead(nil)
			_ = cat.Reads()
		})
	}
	wg.Wait()
	if got := cat.Reads(); got != readers {
		t.Fatalf("Reads() = %d tras %d lecturas concurrentes", got, readers)
	}
}
