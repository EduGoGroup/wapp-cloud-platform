package cart

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// updateGolden regenera los archivos golden de testdata en vez de compararlos:
//
//	go test ./internal/flujos/modules/cart/ -run Golden -update
//
// Los golden del V1 se generaron con el código ANTERIOR al catálogo v2 (Plan 041 ·
// T2.2/T2.4). Regenerarlos borra justamente la prueba que sujeta la no-regresión:
// si uno del v1 falla, la respuesta correcta es arreglar el código, NO actualizarlo.
var updateGolden = flag.Bool("update", false, "regenera los golden de testdata")

// goldenPath resuelve un archivo dentro de testdata/.
func goldenPath(name string) string { return filepath.Join("testdata", name) }

// readTestdata lee un archivo de entrada de testdata/ (los blobs de catálogo).
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("leyendo testdata/%s: %v", name, err)
	}
	return b
}

// assertGolden compara `got` con el contenido del golden, o lo reescribe con
// -update. Es la única puerta de escritura de testdata/.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("escribiendo golden %s: %v", path, err)
		}
		t.Logf("golden regenerado: %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leyendo golden %s (¿falta generarlo con -update?): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s NO coincide.\n--- golden (esperado) ---\n%s\n--- obtenido ---\n%s", path, want, got)
	}
}

// rawFromFile carga un blob de catálogo de testdata/ como model.Content.Raw
// (map[string]any con números float64: el mismo round-trip JSONB del adapter json).
func rawFromFile(t *testing.T, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readTestdata(t, name), &m); err != nil {
		t.Fatalf("testdata/%s no es JSON válido: %v", name, err)
	}
	return m
}

// dumpCatalog serializa el catálogo YA PARSEADO de forma determinista para el
// golden. Es un volcado del ÁRBOL COMPLETO (todos los campos exportados de
// Catalog/Category/Article): por eso el golden del v1 detecta tanto que un campo
// v1 cambió como que un campo v2 se pobló donde no debía.
func dumpCatalog(t *testing.T, cat Catalog) string {
	t.Helper()
	b, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		t.Fatalf("serializando catálogo: %v", err)
	}
	return string(b) + "\n"
}

// TestParseCatalogGoldenV1 es la RED del catálogo v1 (Plan 041 · T2.2): parsea el
// blob v1 REAL del e2e del Plan 016 (Bebidas/Postres, el mismo que se sembró en
// tenant_content para el cierre por WhatsApp) y compara el árbol resultante,
// campo a campo, con el golden congelado ANTES del catálogo v2.
func TestParseCatalogGoldenV1(t *testing.T) {
	cat, err := ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v1.json")})
	if err != nil {
		t.Fatalf("el blob v1 real debe parsear sin error: %v", err)
	}
	assertGolden(t, "catalog_v1_parsed.golden.json", dumpCatalog(t, cat))
}
