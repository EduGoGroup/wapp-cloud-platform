package catalogimport_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// Los fixtures de testdata/ son la salida del paquete VIEJO (internal/catalogimport
// @ 3c74b80), capturada una vez con un programa temporal que no se commitea:
//
//   - template.json: json.MarshalIndent(BuildTemplate(), "", "  ") más un salto de
//     línea, que es exactamente lo que sirve GET /api/v1/catalog/import/template;
//   - template_sheet.json: {"columns": TabularColumns(), "rows": TemplateSheetRows()}
//     con la misma indentación y salto final;
//   - import_prompt.txt: ImportPrompt(), sin salto final.
//
// No hay modo de regenerarlos: si uno falla, lo que se arregla es el código. Los
// tests no importan el paquete viejo (un test nuevo que importa lo viejo sería
// portar a escondidas, 05 E-8).

// readFixture lee un fixture de testdata/.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	//nolint:gosec // G304: la ruta es un nombre fijo bajo testdata/, no entrada externa
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("leyendo testdata/%s: %v", name, err)
	}
	return string(b)
}

// indentedJSON serializa como lo hace la descarga: indentado a dos espacios y con
// salto final.
func indentedJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(%T) = error %v", v, err)
	}
	return string(b) + "\n"
}

// TestBuildTemplate_MatchesTheOldTemplateByteForByte: la plantilla se descarga tal
// cual; un byte distinto es otra plantilla en manos del dueño.
func TestBuildTemplate_MatchesTheOldTemplateByteForByte(t *testing.T) {
	want := readFixture(t, "template.json")
	if got := indentedJSON(t, catalogimport.BuildTemplate()); got != want {
		t.Errorf("la plantilla NO coincide con testdata/template.json.\n--- esperado ---\n%s--- obtenido ---\n%s", want, got)
	}
}

// TestBuildTemplate_PassesItsOwnValidator es el criterio que, si falla, invalida
// todo lo demás: repartir una plantilla que el propio import rechaza deja al dueño
// con una lista de errores que él no cometió. Se valida sobre los BYTES
// serializados —lo que se descarga, se edita y se vuelve a subir—, no sobre el
// struct.
func TestBuildTemplate_PassesItsOwnValidator(t *testing.T) {
	doc := mustValidate(t, compactJSON(t, catalogimport.BuildTemplate()))

	if doc.Format != catalogimport.ImportFormat || doc.Version != catalogimport.ImportVersion {
		t.Fatalf("la plantilla no lleva la cabecera del contrato: format=%q version=%d", doc.Format, doc.Version)
	}
	// El validador la devuelve tal cual: no tenía nada que normalizar.
	if got, want := compactJSON(t, doc), compactJSON(t, catalogimport.BuildTemplate()); got != want {
		t.Errorf("la plantilla validada = %s\nse esperaba la propia plantilla %s", got, want)
	}
}

// templateCase clasifica un artículo de la plantilla en uno de los cuatro casos del
// contrato.
func templateCase(item catalogimport.ImportItem) string {
	switch {
	case len(item.Variants) > 0:
		return "variants"
	case len(item.Components) > 0:
		return "combo"
	case len(item.Tags) > 0 && len(item.Attributes) > 0:
		return "tags+attributes"
	default:
		return "simple"
	}
}

// TestBuildTemplate_BringsTheFourCases: la plantilla es la única documentación que
// el dueño lee de verdad; lo que no aparezca en ella no existe para quien la llena.
// Un artículo que se caiga en un refactor la dejaría válida —y muda sobre esa mitad
// del contrato— sin que nada avisara.
func TestBuildTemplate_BringsTheFourCases(t *testing.T) {
	tpl := catalogimport.BuildTemplate()

	seen := make(map[string]int, 4)
	skus := make(map[string]bool)
	for _, cat := range tpl.Catalog.Categories {
		for _, item := range cat.Items {
			seen[templateCase(item)]++
			skus[item.SKU] = true
		}
	}
	for _, name := range []string{"simple", "variants", "combo", "tags+attributes"} {
		if seen[name] == 0 {
			t.Errorf("la plantilla no trae ningún artículo del caso %q: %v", name, seen)
		}
	}

	// Los componentes del combo apuntan a artículos del MISMO documento.
	for _, cat := range tpl.Catalog.Categories {
		for _, item := range cat.Items {
			for _, c := range item.Components {
				if !skus[c.SKU] {
					t.Errorf("el combo %q referencia el sku %q, que no está en la plantilla", item.SKU, c.SKU)
				}
			}
		}
	}

	// Subcategorías declaradas Y usadas: una columna «subcategoria» sin un solo
	// ejemplo sería un enigma para quien llena la planilla.
	first := tpl.Catalog.Categories[0]
	if len(first.Subcategories) != 2 || first.Items[0].Subcategory == "" || first.Items[1].Subcategory == "" {
		t.Errorf("la plantilla no ejercita las subcategorías: %+v", first)
	}
	if tpl.Source == nil || tpl.Source.Kind != "plantilla" {
		t.Errorf("la plantilla declara su procedencia: %+v", tpl.Source)
	}
}

// TestBuildTemplate_TheRuntimeReadsItWithoutWarnings cierra el círculo: la
// plantilla —que valida— tiene que producir un catálogo que el runtime lee entero.
// Si no, el dueño podría importarla tal cual y tener en producción un catálogo con
// partes que el motor descarta en silencio.
func TestBuildTemplate_TheRuntimeReadsItWithoutWarnings(t *testing.T) {
	cat := runtimeCatalog(t, catalogimport.BuildTemplate().Catalog)

	if len(cat.Warnings) != 0 {
		t.Fatalf("el runtime descartó partes de la plantilla: %+v", cat.Warnings)
	}
	if len(cat.Categories) != 2 || len(cat.Categories[0].Items) != 2 || len(cat.Categories[1].Items) != 3 {
		t.Fatalf("el árbol del runtime no cuadra con la plantilla: %+v", cat.Categories)
	}
}

// TestBuildTemplate_ReturnsAFreshDocument: mutar lo que devuelve una llamada no
// cambia lo que devuelve la siguiente.
func TestBuildTemplate_ReturnsAFreshDocument(t *testing.T) {
	tpl := catalogimport.BuildTemplate()
	tpl.Catalog.Categories[0].Items[0].SKU = "destrozado"
	tpl.Catalog.Categories[0].Items[1].Attributes["capas"] = "99"
	tpl.Source.Kind = "otra"

	if got := indentedJSON(t, catalogimport.BuildTemplate()); got != readFixture(t, "template.json") {
		t.Error("mutar el resultado de BuildTemplate cambió la plantilla para todos")
	}
}

// TestTabularConstants_AreTheContract: la hoja se busca por su nombre y las celdas
// múltiples se deshacen por estos dos separadores.
func TestTabularConstants_AreTheContract(t *testing.T) {
	if catalogimport.TabularSheetName != "catalogo" {
		t.Errorf("TabularSheetName = %q; la hoja de datos se llama \"catalogo\"", catalogimport.TabularSheetName)
	}
	if catalogimport.TabularEntrySeparator != ";" {
		t.Errorf("TabularEntrySeparator = %q; las entradas se separan con \";\"", catalogimport.TabularEntrySeparator)
	}
	if catalogimport.TabularFieldSeparator != "|" {
		t.Errorf("TabularFieldSeparator = %q; los campos se separan con \"|\"", catalogimport.TabularFieldSeparator)
	}
}

// TestTabularColumns_NamesAndOrderAreContract: el literal va escrito a mano A
// PROPÓSITO —compararlo con la variable que lo produce no probaría nada—. Es lo que
// se rompe si alguien reordena o renombra una columna.
func TestTabularColumns_NamesAndOrderAreContract(t *testing.T) {
	const want = `["categoria","subcategoria","codigo","sku","nombre","precio","descripcion","tags","atributos","variantes","componentes"]`
	if got := compactJSON(t, catalogimport.TabularColumns()); got != want {
		t.Errorf("columnas = %s\nse esperaba %s (el orden es contrato)", got, want)
	}
}

// TestTabularColumns_ReturnsACopy: si se devolviera el slice interno, un consumidor
// que lo reordenara dejaría al emisor y al parser leyendo cabeceras distintas.
func TestTabularColumns_ReturnsACopy(t *testing.T) {
	cols := catalogimport.TabularColumns()
	cols[0] = "destrozada"
	if got := catalogimport.TabularColumns()[0]; got != "categoria" {
		t.Fatalf("mutar el resultado de TabularColumns cambió el contrato para todos: %q", got)
	}
}

// TestTemplateSheetRows_MatchTheOldSheetByteForByte: la planilla de ejemplo, celda
// a celda, es la que emitía el paquete viejo.
func TestTemplateSheetRows_MatchTheOldSheetByteForByte(t *testing.T) {
	sheet := struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{catalogimport.TabularColumns(), catalogimport.TemplateSheetRows()}

	want := readFixture(t, "template_sheet.json")
	if got := indentedJSON(t, sheet); got != want {
		t.Errorf("la planilla NO coincide con testdata/template_sheet.json.\n--- esperado ---\n%s--- obtenido ---\n%s", want, got)
	}
}

// TestTemplateSheetRows_OneRowPerItemOneCellPerColumn: cinco artículos, cinco filas,
// y en cada una once celdas: todas texto salvo el precio, que viaja como NÚMERO
// (como texto la hoja no lo sumaría).
func TestTemplateSheetRows_OneRowPerItemOneCellPerColumn(t *testing.T) {
	cols := catalogimport.TabularColumns()
	rows := catalogimport.TemplateSheetRows()

	if len(rows) != 5 {
		t.Fatalf("filas = %d; se esperaban 5 (una por artículo de la plantilla)", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(cols) {
			t.Fatalf("la fila %d tiene %d celdas y hay %d columnas", i, len(row), len(cols))
		}
		for j, cell := range row {
			if cols[j] == "precio" {
				if _, ok := cell.(float64); !ok {
					t.Errorf("fila %d, precio = %v (%T); se esperaba un float64", i, cell, cell)
				}
				continue
			}
			if _, ok := cell.(string); !ok {
				t.Errorf("fila %d, %s = %v (%T); se esperaba un texto", i, cols[j], cell, cell)
			}
		}
	}
}

// TestTemplateSheetRows_CellMiniSyntax afirma cómo se escribe en UNA celda lo que en
// el JSON es una lista. Es la mitad del contrato tabular que no se ve en la
// cabecera y la que ParseTabular tiene que saber deshacer.
func TestTemplateSheetRows_CellMiniSyntax(t *testing.T) {
	rows := catalogimport.TemplateSheetRows()
	cols := catalogimport.TabularColumns()
	cell := func(row int, column string) any {
		for j, name := range cols {
			if name == column {
				return rows[row][j]
			}
		}
		t.Fatalf("no existe la columna %q", column)
		return nil
	}
	cases := []struct {
		row    int
		column string
		want   any
	}{
		// La torta con variantes: categoría y subcategoría con el código explícito.
		{0, "categoria", "1|Tortas"},
		{0, "subcategoria", "clasicas|Clásicas"},
		{0, "precio", float64(18000)},
		{0, "variantes", "V1|10-12 porciones|18000; V2|25-30 porciones|32000"},
		// Tags y atributos; los atributos, ORDENADOS por clave.
		{1, "tags", "decorada; sin_lactosa"},
		{1, "atributos", "capas|3; porciones|10-12"},
		// El artículo simple no inventa nada en las columnas que no usa.
		{2, "categoria", "2|Salados"},
		{2, "subcategoria", ""},
		{2, "tags", ""},
		{2, "atributos", ""},
		{2, "variantes", ""},
		{2, "componentes", ""},
		{3, "descripcion", ""},
		// El combo: la cantidad se escribe SIEMPRE, también cuando vale 1.
		{4, "componentes", "TEQUENOS-15|1; REFRESCO-15L|2"},
	}
	for _, c := range cases {
		if got := cell(c.row, c.column); got != c.want {
			t.Errorf("fila %d, %s = %v (%T); se esperaba %v (%T)", c.row, c.column, got, got, c.want, c.want)
		}
	}
}
