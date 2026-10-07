package catalogo_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Ayudantes compartidos por los tests del paquete (catalog_test.go y sus trozos
// catalog_<tema>_test.go). Recrean los del carrito viejo (golden_test.go y
// catalog_test.go), que no viajan solos: readTestdata, rawFromFile, rawFromJSON,
// assertGolden y dumpCatalog.

// invalidPrefix precede a todo motivo de error de ParseCatalog: el texto de
// model.ErrInvalidFlow y la envoltura de fmt.
const invalidPrefix = "definición de flujo inválida: "

// Campos del contrato v2 tal como salen en CatalogWarning.Field.
const (
	fieldSubcategories = "subcategories"
	fieldSubcategory   = "subcategory"
	fieldTags          = "tags"
	fieldAttributes    = "attributes"
	fieldVariants      = "variants"
	fieldComponents    = "components"
	fieldSKU           = "sku"
)

// readTestdata lee un archivo de testdata/ (blobs de catálogo y goldens).
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	//nolint:gosec // G304: la ruta es un nombre fijo bajo testdata/, no entrada externa
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("leyendo testdata/%s: %v", name, err)
	}
	return b
}

// rawFromJSON simula el round-trip JSONB: decodifica un literal JSON a
// map[string]any (números como float64), tal como llega Content.Raw.
func rawFromJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("literal JSON de prueba inválido: %v\n%s", err, s)
	}
	return m
}

// rawFromFile carga un blob de catálogo de testdata/ como Content.Raw.
func rawFromFile(t *testing.T, name string) map[string]any {
	t.Helper()
	return rawFromJSON(t, string(readTestdata(t, name)))
}

// dumpCatalog serializa el ÁRBOL COMPLETO ya parseado, con los nombres Go de los
// campos: por eso el golden del v1 detecta tanto un campo v1 que cambió como un
// campo v2 que se pobló donde no debía.
func dumpCatalog(t *testing.T, cat catalogo.Catalog) string {
	t.Helper()
	b, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		t.Fatalf("serializando el catálogo: %v", err)
	}
	return string(b) + "\n"
}

// assertGolden compara got con el golden BYTE A BYTE. No hay modo de
// regenerarlo: los goldens son los del carrito viejo, copiados tal cual, y si uno
// falla lo que se arregla es el código.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	if want := string(readTestdata(t, name)); want != got {
		t.Fatalf("golden %s NO coincide.\n--- esperado ---\n%s\n--- obtenido ---\n%s", name, want, got)
	}
}

// mustParse parsea un literal JSON de catálogo que NO debe dar error.
func mustParse(t *testing.T, blob string) catalogo.Catalog {
	t.Helper()
	cat, err := catalogo.ParseCatalog(model.Content{Raw: rawFromJSON(t, blob)})
	if err != nil {
		t.Fatalf("el catálogo debía parsear y falló: %v", err)
	}
	return cat
}

// oneArticleBlob es un catálogo de una categoría ("1") y un artículo (sku "A"),
// con JSON extra en la categoría y en el artículo (cada uno empieza por coma).
func oneArticleBlob(categoryExtra, articleExtra string) string {
	return `{"categories":[{"code":"1","label":"X"` + categoryExtra +
		`,"items":[{"code":"1","sku":"A","label":"A","price":1` + articleExtra + `}]}]}`
}

// baseArticle es el artículo de oneArticleBlob sin ningún campo v2.
func baseArticle() catalogo.Article {
	return catalogo.Article{Code: "1", SKU: "A", Label: "A", Price: 1}
}

// onlyArticle exige que el catálogo siga en pie con su única categoría y su
// único artículo, y lo devuelve.
func onlyArticle(t *testing.T, cat catalogo.Catalog) catalogo.Article {
	t.Helper()
	if len(cat.Categories) != 1 || len(cat.Categories[0].Items) != 1 {
		t.Fatalf("el catálogo debe seguir en pie con 1 categoría y 1 artículo: %+v", cat)
	}
	return cat.Categories[0].Items[0]
}

// articleWarning es un aviso del artículo "A" de la categoría "1".
func articleWarning(field, reason string) catalogo.CatalogWarning {
	return catalogo.CatalogWarning{Category: "1", SKU: "A", Field: field, Reason: reason}
}

// assertWarnings compara los avisos enteros y en orden (nil ≠ vacío).
func assertWarnings(t *testing.T, got, want []catalogo.CatalogWarning) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	t.Errorf("avisos = %d, se esperaban %d", len(got), len(want))
	for i, w := range got {
		t.Logf("  obtenido[%d] = %+v", i, w)
	}
	for i, w := range want {
		t.Logf("  esperado[%d] = %+v", i, w)
	}
}

// assertArticle compara el artículo entero (nil ≠ vacío en los campos v2).
func assertArticle(t *testing.T, got, want catalogo.Article) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("artículo = %#v\nse esperaba %#v", got, want)
	}
}
