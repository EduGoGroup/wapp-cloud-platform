//go:build pendiente

package catalogimport_test

import (
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// tabular_test.go cubre la entrada de ParseTabular: la igualdad con el camino JSON,
// la cabecera, los topes y las filas. La mini-sintaxis de las celdas y el precio
// están en tabular_cells_test.go, y la vuelta de cada defecto a su fila y a su
// columna en tabular_locate_test.go (E-13).

// ============================ andamiaje ============================

// bom es la marca de orden de bytes con la que Excel encabeza sus CSV.
const bom = string(rune(0xFEFF))

// sheet arma una planilla con la cabecera canónica y las filas dadas.
func sheet(rows ...[]string) [][]string {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, catalogimport.TabularColumns())
	return append(out, rows...)
}

// row arma una fila en el ORDEN de las columnas (categoria, subcategoria, codigo,
// sku, nombre, precio, descripcion, tags, atributos, variantes, componentes),
// rellenando con celdas vacías las que no se dan.
func row(cells ...string) []string {
	out := make([]string, len(catalogimport.TabularColumns()))
	copy(out, cells)
	return out
}

// parseSheet lee una planilla con los topes por defecto.
func parseSheet(rows [][]string) (catalogimport.CatalogImport, *catalogimport.ImportValidationError) {
	return catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
}

// mustParseSheet exige que la planilla se importe y devuelve su documento.
func mustParseSheet(t *testing.T, rows [][]string) catalogimport.CatalogImport {
	t.Helper()
	doc, verr := parseSheet(rows)
	if verr != nil {
		for _, e := range verr.Errors {
			t.Logf("  fila=%d columna=%q → %s", e.Row, e.Field, e.Reason)
		}
		t.Fatalf("la planilla debía importarse y devolvió %d defectos", len(verr.Errors))
	}
	return doc
}

// rowDefect es un defecto esperado del camino tabular: la fila de la hoja (0 = la
// planilla entera), la COLUMNA y el motivo literal.
type rowDefect struct {
	row    int
	column string
	reason string
}

// assertRowDefects exige que la lectura fallara con EXACTAMENTE esos defectos, en
// ese orden, y sin índices de categoría ni de artículo: en una planilla se busca
// por fila.
func assertRowDefects(t *testing.T, verr *catalogimport.ImportValidationError, want ...rowDefect) {
	t.Helper()
	if verr == nil {
		t.Fatalf("la planilla se aceptó y se esperaban %d defectos", len(want))
	}
	if len(verr.Errors) != len(want) {
		for i, e := range verr.Errors {
			t.Logf("  [%d] fila=%d columna=%q → %s", i, e.Row, e.Field, e.Reason)
		}
		t.Fatalf("defectos = %d; se esperaban %d", len(verr.Errors), len(want))
	}
	for i, w := range want {
		got := verr.Errors[i]
		if got.Row != w.row || got.Field != w.column {
			t.Errorf("defecto %d: fila %d, columna %q; se esperaba fila %d, columna %q — %s", i, got.Row, got.Field, w.row, w.column, got.Reason)
		}
		if got.Reason != w.reason {
			t.Errorf("defecto %d: motivo\n  %q\nse esperaba\n  %q", i, got.Reason, w.reason)
		}
		if got.CategoryIndex != nil || got.ItemIndex != nil {
			t.Errorf("defecto %d: trae índices (%d/%d); en una planilla se ubica por fila", i, index(got.CategoryIndex), index(got.ItemIndex))
		}
	}
}

// catalogJSON es el catálogo de un documento, serializado: el blob que se escribe.
func catalogJSON(t *testing.T, doc catalogimport.CatalogImport) string {
	t.Helper()
	return compactJSON(t, doc.Catalog)
}

// ============================ igualdad con el JSON ============================

// TestParseTabular_TheTemplateComesBackAsTheSameCatalog: la planilla canónica llena
// con los cuatro casos produce EL MISMO catálogo que su equivalente JSON. Las dos
// mitades salen del MISMO documento (TemplateSheetRows y BuildTemplate), no de un
// fixture paralelo, y se comparan los blobs SERIALIZADOS —lo que se escribe en
// tenant_content—, los dos pasados por el validador.
func TestParseTabular_TheTemplateComesBackAsTheSameCatalog(t *testing.T) {
	template := catalogimport.TemplateSheetRows()
	rows := make([][]string, 0, len(template)+1)
	rows = append(rows, catalogimport.TabularColumns())
	for _, cells := range template {
		line := make([]string, 0, len(cells))
		for _, cell := range cells {
			switch v := cell.(type) {
			case string:
				line = append(line, v)
			case float64: // el precio viaja como número y la hoja lo escribe sin separador de miles
				line = append(line, strconv.FormatFloat(v, 'f', -1, 64))
			default:
				t.Fatalf("celda de tipo inesperado en la plantilla: %v (%T)", cell, cell)
			}
		}
		rows = append(rows, line)
	}

	fromSheet := mustParseSheet(t, rows)
	fromJSON := mustValidate(t, compactJSON(t, catalogimport.BuildTemplate()))

	if got, want := catalogJSON(t, fromSheet), catalogJSON(t, fromJSON); got != want {
		t.Fatalf("los dos caminos producen catálogos distintos.\nplanilla: %s\nJSON:     %s", got, want)
	}
}

// TestParseTabular_DocumentCarriesTheContractHeaderAndItsSource: quien llena una
// planilla no tiene dónde escribir el format ni la version: los pone el parser, y
// anota de dónde vino el catálogo.
func TestParseTabular_DocumentCarriesTheContractHeaderAndItsSource(t *testing.T) {
	doc := mustParseSheet(t, sheet(row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000")))

	const want = `{"format":"wapp.catalog_import","version":1,"source":{"kind":"planilla"},"catalog":{"categories":[` +
		`{"code":"1","label":"Tortas","items":[{"code":"1","sku":"TORTA-CHOC","label":"Torta de chocolate","price":18000}]}]}}`
	if got := compactJSON(t, doc); got != want {
		t.Errorf("documento = %s\nse esperaba %s", got, want)
	}
}

// ============================ cabecera ============================

const missingColumnsTail = "): la planilla se lee por el NOMBRE de sus columnas y sin esas no hay artículo que valga. " +
	"Descarga la plantilla y parte de ella."

// TestParseTabular_EmptySheetAndHeaderOnly: una planilla sin filas, o descargada y
// devuelta sin llenar, no es un catálogo vacío que haya que aplicar; es un
// despiste, y se dice como tal.
func TestParseTabular_EmptySheetAndHeaderOnly(t *testing.T) {
	_, verr := parseSheet(nil)
	assertRowDefects(t, verr, rowDefect{0, "planilla", "la planilla está vacía: descarga la plantilla, llénala y vuelve a subirla."})

	_, verr = parseSheet(sheet())
	assertRowDefects(t, verr, rowDefect{1, "planilla", "la planilla solo trae la cabecera: escribe debajo un artículo por fila."})
}

// TestParseTabular_HeaderWithoutRequiredColumns: sin las columnas que sostienen un
// artículo no hay nada que leer, y decirlo UNA vez —nombrándolas, en el orden del
// contrato— es mejor que devolver un defecto por cada fila de la hoja.
func TestParseTabular_HeaderWithoutRequiredColumns(t *testing.T) {
	cases := map[string]struct {
		header  []string
		missing string
	}{
		"two missing":    {[]string{"categoria", "nombre", "precio"}, "codigo, sku"},
		"all missing":    {[]string{"a", "b"}, "categoria, codigo, sku, nombre, precio"},
		"empty header":   {[]string{}, "categoria, codigo, sku, nombre, precio"},
		"another name":   {[]string{"categoria", "codigo", "sku", "nombre", "valor"}, "precio"},
		"only optionals": {[]string{"tags", "atributos", "variantes", "componentes", "descripcion", "subcategoria"}, "categoria, codigo, sku, nombre, precio"},
		// El BOM solo se quita si es lo PRIMERO de la celda: detrás de un espacio se
		// queda pegado al nombre (no es espacio para TrimSpace) y la columna no casa.
		"bom after a space": {[]string{" " + bom + "categoria", "codigo", "sku", "nombre", "precio"}, "categoria"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := parseSheet([][]string{c.header, {"1|Tortas", "Torta de chocolate", "18000"}})
			assertRowDefects(t, verr, rowDefect{1, "cabecera", "a la primera fila le faltan columnas (" + c.missing + missingColumnsTail})
		})
	}
}

// TestParseTabular_HeaderAsExcelReturnsIt: la hoja la abre una persona, que la
// autocapitaliza, le pone la tilde que le falta a «descripcion» y deja espacios; y
// Excel le pega un BOM delante. Nada de eso es del negocio. Lo que NO se adivina es
// una columna con otro nombre: «notas mías» se ignora.
func TestParseTabular_HeaderAsExcelReturnsIt(t *testing.T) {
	doc := mustParseSheet(t, [][]string{
		{bom + "CATEGORÍA", nbsp + "Código" + nbsp, "SKU", "Nombre ", " PRECIO", "Descripción", "notas mías"},
		{"1|Tortas", "1", "TORTA-CHOC", "Torta de chocolate", "18000", "Con arequipe", "llamar al proveedor"},
	})

	const want = `{"categories":[{"code":"1","label":"Tortas","items":[` +
		`{"code":"1","sku":"TORTA-CHOC","label":"Torta de chocolate","price":18000,"description":"Con arequipe"}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_ColumnsAreFoundByNameNotByPosition: mover una columna de sitio no
// puede romper el import; una columna repetida la gana la primera que apareció, y
// una sin nombre no es de nadie.
func TestParseTabular_ColumnsAreFoundByNameNotByPosition(t *testing.T) {
	doc := mustParseSheet(t, [][]string{
		{"precio", "", "nombre", "sku", "codigo", "categoria", "precio"},
		{"1", "suelta", "Art A", "A", "7", "1|Tortas", "2"},
	})

	const want = `{"categories":[{"code":"1","label":"Tortas","items":[{"code":"7","sku":"A","label":"Art A","price":1}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_ShortRowsAndMissingOptionalColumns: quien no vende nada con
// variantes borra esa columna, y quien borra las celdas vacías del final deja filas
// más cortas que la cabecera. Una columna ausente es una columna vacía, no un
// corrimiento.
func TestParseTabular_ShortRowsAndMissingOptionalColumns(t *testing.T) {
	doc := mustParseSheet(t, [][]string{
		{"categoria", "codigo", "sku", "nombre", "precio", "tags"},
		{"1|Tortas", "1", "A", "Art A", "1"},
		{"1|Tortas", "2", "B", "Art B", "2", "frio"},
	})

	const want = `{"categories":[{"code":"1","label":"Tortas","items":[` +
		`{"code":"1","sku":"A","label":"Art A","price":1},{"code":"2","sku":"B","label":"Art B","price":2,"tags":["frio"]}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// ============================ filas y topes ============================

// TestParseTabular_BlankRowsNeitherCountNorHinder: una hoja de cálculo está llena de
// filas en blanco —las de abajo, las que alguien vació sin borrar, las que solo
// tienen espacios (también Unicode)— y ninguna es un defecto ni un artículo. Pero
// SÍ ocupan su número: el defecto de la última fila con datos es de la fila 5 de la hoja.
func TestParseTabular_BlankRowsNeitherCountNorHinder(t *testing.T) {
	rows := sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row(),
		row(nbsp, ideographicSpace, emSpace+" "),
		row("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "26000"),
		[]string{},
	)
	doc := mustParseSheet(t, rows)
	if n := len(doc.Catalog.Categories[0].Items); n != 2 {
		t.Fatalf("artículos = %d; se esperaban 2", n)
	}

	rows[4] = row("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "")
	_, verr := parseSheet(rows)
	assertRowDefects(t, verr, rowDefect{5, "precio", `la fila 5 ("Torta de unicornio") no tiene el precio: es obligatorio.`})
}

// TestParseTabular_ZeroWidthSpaceIsNotBlank (corpus adversario): el espacio de ancho
// cero no es espacio para strings.TrimSpace, así que una fila que solo trae uno NO
// está vacía: se lee, y su celda de categoría no se entiende.
func TestParseTabular_ZeroWidthSpaceIsNotBlank(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "A", "Art A", "1"),
		row(zeroWidthSpace),
	))
	assertRowDefects(t, verr, rowDefect{3, "categoria",
		"la fila 3: la categoría " + strconv.Quote(zeroWidthSpace) + " se escribe «codigo|nombre», por ejemplo «1|Tortas»."})
}

// TestParseTabular_TemplateReturnedUnfilled: filas con formato pero sin nada escrito
// es lo que devuelve quien creyó que ya la había llenado. No es un catálogo vacío
// que haya que aplicar —eso borraría el catálogo entero—, y lo dice el validador con
// su propio mensaje.
func TestParseTabular_TemplateReturnedUnfilled(t *testing.T) {
	_, verr := parseSheet(sheet(row(), row(), row()))
	assertRowDefects(t, verr, rowDefect{0, "categories", "el catálogo no trae ninguna categoría: agrega al menos una con sus artículos."})
}

// TestParseTabular_ItemLimit: el tope se comprueba ANTES de armar nada —las tres
// filas están rotas y no sale ningún defecto suyo—, y por eso el mensaje habla de
// la planilla y no de ninguna fila. Las filas vacías no cuentan.
func TestParseTabular_ItemLimit(t *testing.T) {
	rows := sheet(
		row("Tortas", "", "1", "A", "Artículo A", "x"),
		row(),
		row("Tortas", "", "2", "B", "Artículo B", "x"),
		row("Tortas", "", "3", "C", "Artículo C", "x"),
		row(),
	)
	_, verr := catalogimport.ParseTabular(rows, catalogimport.Limits{MaxItems: 2})
	assertRowDefects(t, verr, rowDefect{0, "planilla",
		"la planilla trae 3 artículos y el máximo por importación es 2: divide la carga en varias planillas."})

	valid := sheet(
		row("1|Tortas", "", "1", "A", "Artículo A", "1"),
		row(),
		row("1|Tortas", "", "2", "B", "Artículo B", "2"),
	)
	if _, verr := catalogimport.ParseTabular(valid, catalogimport.Limits{MaxItems: 2}); verr != nil {
		t.Errorf("2 artículos (y una fila vacía) con el tope en 2 deben pasar: %v", verr)
	}
}

// TestParseTabular_NonPositiveItemLimitFallsBackToDefault: un tope a 0 no lo
// desactiva.
func TestParseTabular_NonPositiveItemLimitFallsBackToDefault(t *testing.T) {
	rows := sheet()
	for i := range catalogimport.DefaultMaxItems + 1 {
		id := strconv.Itoa(i)
		rows = append(rows, row("1|Tortas", "", id, "S"+id, "Artículo", "1"))
	}
	_, verr := catalogimport.ParseTabular(rows, catalogimport.Limits{})
	assertRowDefects(t, verr, rowDefect{0, "planilla",
		"la planilla trae 501 artículos y el máximo por importación es 500: divide la carga en varias planillas."})
}

// TestParseTabular_ReadingAndJudgingAreTwoPasses: la fila 3 no se puede leer
// (categoría rota), así que su sku nunca entra en el catálogo. Si se validara
// igualmente, el combo de la fila 4 reportaría un componente «que no existe»: un
// defecto inventado por el propio parser.
func TestParseTabular_ReadingAndJudgingAreTwoPasses(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("Salados", "", "1", "TEQUENOS-15", "Tequeños", "26000"),
		row("1|Tortas", "", "2", "COMBO", "Combo fiesta", "32000", "", "", "", "", "TEQUENOS-15|1"),
	))
	assertRowDefects(t, verr, rowDefect{3, "categoria",
		`la fila 3: la categoría "Salados" se escribe «codigo|nombre», por ejemplo «1|Tortas».`})
}

// TestParseTabular_RowsOfACategoryNeedNotBeTogether: las categorías salen en el
// orden en que aparecen por primera vez, y cada una reúne sus filas aunque alguien
// haya intercalado las de otra.
func TestParseTabular_RowsOfACategoryNeedNotBeTogether(t *testing.T) {
	doc := mustParseSheet(t, sheet(
		row("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000"),
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("2|Salados", "", "2", "REFRESCO", "Refresco", "4000"),
	))

	const want = `{"categories":[` +
		`{"code":"2","label":"Salados","items":[{"code":"1","sku":"TEQUENOS","label":"Tequeños","price":26000},{"code":"2","sku":"REFRESCO","label":"Refresco","price":4000}]},` +
		`{"code":"1","label":"Tortas","items":[{"code":"1","sku":"TORTA-CHOC","label":"Torta de chocolate","price":18000}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_CellsAreReadWithoutSurroundingSpaces (corpus adversario): toda
// celda —y cada campo y cada entrada dentro de ella— se lee sin espacios alrededor,
// también los Unicode. Por eso el sku de la planilla llega recortado, al revés que
// el del JSON.
func TestParseTabular_CellsAreReadWithoutSurroundingSpaces(t *testing.T) {
	doc := mustParseSheet(t, sheet(row(
		nbsp+"1"+nbsp+"|"+ideographicSpace+"Tortas"+ideographicSpace,
		emSpace+"a"+emSpace+"|"+emSpace+"Sub"+emSpace,
		nbsp+"1",
		nbsp+"CAFE"+nbsp,
		ideographicSpace+"Café"+ideographicSpace,
		nbsp+"18000"+nbsp,
		" Recién molido ",
		"a;"+ideographicSpace+";"+nbsp+"b"+nbsp,
		"k"+nbsp+"|"+nbsp+"v",
	)))

	const want = `{"categories":[{"code":"1","label":"Tortas","subcategories":[{"code":"a","label":"Sub"}],"items":[` +
		`{"code":"1","sku":"CAFE","label":"Café","price":18000,"description":"Recién molido","subcategory":"a","tags":["a","b"],"attributes":{"k":"v"}}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_DefectCapSummarizesTheRest: el tope de 200 defectos y su resumen
// son los del validador; el resumen no es de ninguna fila y cierra la lista.
func TestParseTabular_DefectCapSummarizesTheRest(t *testing.T) {
	rows := sheet()
	for i := range 205 {
		id := strconv.Itoa(i)
		rows = append(rows, row("1|Tortas", "", id, "S"+id, "Artículo", "x"))
	}
	_, verr := parseSheet(rows)
	if verr == nil || len(verr.Errors) != 201 {
		t.Fatalf("se esperaban 201 entradas (200 detalladas y el resumen); llegó %v", verr)
	}
	if first := verr.Errors[0]; first.Row != 2 || first.Field != "precio" {
		t.Errorf("el primer defecto debe ser el precio de la fila 2: %+v", first)
	}
	summary := verr.Errors[200]
	const want = "se omitieron 5 problemas más: arregla los de arriba y vuelve a subir el archivo para ver el resto."
	if summary.Row != 0 || summary.Field != "(varios)" || summary.Reason != want {
		t.Errorf("la última entrada debe resumir lo omitido: %+v", summary)
	}
}
