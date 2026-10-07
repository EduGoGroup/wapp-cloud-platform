package catalogimport_test

import (
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Ayudantes compartidos por los tests del paquete (los del validador y, después, los
// de diff, plantilla y planilla). compactJSON vive en contract_test.go.

// Espacios Unicode del corpus adversario (reglas.md §5). Van construidos desde su
// punto de código y no pegados en el literal: pegados no se distinguen de un espacio
// corriente al leer el test. Los tres primeros son espacio para strings.TrimSpace;
// el de ancho cero NO lo es, y por eso se comporta como una letra.
const (
	nbsp             = string(rune(0x00A0)) // espacio de no separación
	emSpace          = string(rune(0x2003)) // espacio eme
	ideographicSpace = string(rune(0x3000)) // espacio ideográfico
	zeroWidthSpace   = string(rune(0x200B)) // espacio de ancho cero
)

// docOf arma un documento de import con la cabecera correcta y las categorías dadas
// (JSON, separadas por comas).
func docOf(categories string) string {
	return `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` + categories + `]}}`
}

// docOfItems arma un documento de una categoría («Bebidas», código "1") con los
// artículos dados (JSON, separados por comas).
func docOfItems(items string) string {
	return docOf(`{"code":"1","label":"Bebidas","items":[` + items + `]}`)
}

// coffee es el artículo válido mínimo de docOfItems (sku CAFE) con JSON extra (que
// empieza por coma) detrás de sus cuatro campos obligatorios.
func coffee(extra string) string {
	return `{"code":"1","sku":"CAFE","label":"Café","price":100` + extra + `}`
}

// coffeeSubject es cómo nombra el validador al artículo de coffee en sus motivos.
const coffeeSubject = `el artículo 1 ("Café") de la categoría "Bebidas"`

// validate corre el validador con los topes por defecto.
func validate(doc string) (catalogimport.CatalogImport, *catalogimport.ImportValidationError) {
	return catalogimport.Validate([]byte(doc), catalogimport.DefaultLimits())
}

// mustValidate exige que el documento valide y lo devuelve tipado.
func mustValidate(t *testing.T, doc string) catalogimport.CatalogImport {
	t.Helper()
	got, verr := validate(doc)
	if verr != nil {
		for _, e := range verr.Errors {
			t.Logf("  campo=%q → %s", e.Field, e.Reason)
		}
		t.Fatalf("el documento es válido y Validate devolvió %d defectos", len(verr.Errors))
	}
	return got
}

// defect es un defecto esperado: dónde está (-1 = sin índice), de qué campo habla y
// su motivo LITERAL.
type defect struct {
	category int
	item     int
	field    string
	reason   string
}

// atHeader es un defecto de cabecera o del documento entero (sin índices).
func atHeader(field, reason string) defect {
	return defect{category: -1, item: -1, field: field, reason: reason}
}

// atCategory es un defecto de una categoría (sin artículo).
func atCategory(category int, field, reason string) defect {
	return defect{category: category, item: -1, field: field, reason: reason}
}

// atItem es un defecto de un artículo.
func atItem(category, item int, field, reason string) defect {
	return defect{category: category, item: item, field: field, reason: reason}
}

// index desenvuelve un índice opcional (nil = -1) para poder compararlo.
func index(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// assertDefects exige que la validación fallara con EXACTAMENTE esos defectos, en
// ese orden, con su ubicación, su campo y su motivo byte a byte; y sin fila, que es
// cosa del camino tabular.
func assertDefects(t *testing.T, verr *catalogimport.ImportValidationError, want ...defect) {
	t.Helper()
	if verr == nil {
		t.Fatalf("el documento se aceptó y se esperaban %d defectos", len(want))
	}
	if len(verr.Errors) != len(want) {
		for i, e := range verr.Errors {
			t.Logf("  [%d] cat=%d art=%d campo=%q → %s", i, index(e.CategoryIndex), index(e.ItemIndex), e.Field, e.Reason)
		}
		t.Fatalf("defectos = %d; se esperaban %d", len(verr.Errors), len(want))
	}
	for i, w := range want {
		got := verr.Errors[i]
		if index(got.CategoryIndex) != w.category || index(got.ItemIndex) != w.item {
			t.Errorf("defecto %d: ubicación (cat=%d, art=%d); se esperaba (cat=%d, art=%d) — %s",
				i, index(got.CategoryIndex), index(got.ItemIndex), w.category, w.item, got.Reason)
		}
		if got.Field != w.field {
			t.Errorf("defecto %d: campo %q; se esperaba %q", i, got.Field, w.field)
		}
		if got.Reason != w.reason {
			t.Errorf("defecto %d: motivo\n  %q\nse esperaba\n  %q", i, got.Reason, w.reason)
		}
		if got.Row != 0 {
			t.Errorf("defecto %d: fila %d; el camino JSON no ubica por fila", i, got.Row)
		}
	}
}

// mustBody lee un catálogo de import escrito en JSON (la forma del blob), sin pasar
// por el validador.
func mustBody(t *testing.T, blob string) catalogimport.ImportBody {
	t.Helper()
	var body catalogimport.ImportBody
	if err := json.Unmarshal([]byte(blob), &body); err != nil {
		t.Fatalf("catálogo de prueba mal escrito: %v\n%s", err, blob)
	}
	return body
}

// runtimeCatalog pasa un catálogo de import por el parser tolerante del runtime,
// como lo leerá el motor: el blob que se escribe en tenant_content es el ImportBody
// tal cual, sin traducción intermedia.
func runtimeCatalog(t *testing.T, body catalogimport.ImportBody) catalogo.Catalog {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(compactJSON(t, body)), &raw); err != nil {
		t.Fatalf("no se pudo releer el blob: %v", err)
	}
	cat, err := catalogo.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		t.Fatalf("el runtime rechazó el catálogo: %v", err)
	}
	return cat
}
