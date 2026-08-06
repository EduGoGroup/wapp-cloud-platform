package catalogimport_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
)

// ============================ andamiaje ============================

// planilla arma una planilla con la cabecera canónica y las filas dadas.
func planilla(rows ...[]string) [][]string {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, catalogimport.TabularColumns())
	return append(out, rows...)
}

// fila arma una fila en el ORDEN de las columnas (categoria, subcategoria, codigo,
// sku, nombre, precio, descripcion, tags, atributos, variantes, componentes),
// rellenando con celdas vacías las que no se dan.
func fila(celdas ...string) []string {
	row := make([]string, len(catalogimport.TabularColumns()))
	copy(row, celdas)
	return row
}

// unicoError exige que la lectura fallara con UN solo defecto y lo devuelve.
func unicoError(t *testing.T, verr *catalogimport.ImportValidationError) catalogimport.ImportFieldError {
	t.Helper()
	if verr == nil {
		t.Fatal("la planilla se aceptó y esperaba un defecto")
	}
	if len(verr.Errors) != 1 {
		t.Fatalf("defectos=%d, quiero 1: %+v", len(verr.Errors), verr.Errors)
	}
	return verr.Errors[0]
}

// celdaTexto escribe una celda de TemplateSheetRows como la escribiría cualquier
// hoja de cálculo: el precio viaja como número y sale sin separador de miles.
func celdaTexto(cell any) string {
	switch v := cell.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// ============================ igualdad con el JSON ============================

// TestParseTabular_LaPlantillaVuelveAlMismoCatalogo es el criterio de T3.4: la
// planilla canónica llena con los cuatro casos (simple, variantes, combo, tags)
// produce EL MISMO catálogo que su equivalente JSON.
//
// Las dos mitades salen del MISMO documento —TemplateSheetRows() y BuildTemplate()
// son la misma plantilla dicha de dos maneras— y no de un fixture escrito a mano: un
// fixture paralelo sería una segunda verdad, y el día que el contrato cambiara
// seguiría pasando este test mientras la plantilla de verdad se rompía.
//
// Se comparan los blobs SERIALIZADOS y no los structs, porque el blob es lo que se
// escribe en tenant_content y lo que el motor lee: es la igualdad que le importa al
// negocio, y además delata diferencias que == no vería (un nil contra una lista
// vacía).
//
// Los DOS lados pasan por Validate a propósito: el blob que escribe el import JSON
// es el documento ya normalizado por el validador, así que compararse contra el
// documento en crudo probaría una igualdad que nadie usa.
func TestParseTabular_LaPlantillaVuelveAlMismoCatalogo(t *testing.T) {
	sheet := catalogimport.TemplateSheetRows()
	rows := make([][]string, 0, len(sheet)+1)
	rows = append(rows, catalogimport.TabularColumns())
	for _, row := range sheet {
		cells := make([]string, 0, len(row))
		for _, cell := range row {
			cells = append(cells, celdaTexto(cell))
		}
		rows = append(rows, cells)
	}

	desdeLaPlanilla, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la plantilla en planilla no se importa: %+v", verr.Errors)
	}

	crudo, err := json.Marshal(catalogimport.BuildTemplate())
	if err != nil {
		t.Fatalf("serializando la plantilla JSON: %v", err)
	}
	desdeElJSON, verr := catalogimport.Validate(crudo, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la plantilla JSON no valida: %+v", verr.Errors)
	}

	blobPlanilla := mustJSON(t, desdeLaPlanilla.Catalog)
	blobJSON := mustJSON(t, desdeElJSON.Catalog)
	if blobPlanilla != blobJSON {
		t.Fatalf("los dos caminos producen catálogos distintos.\nplanilla: %s\nJSON:     %s", blobPlanilla, blobJSON)
	}
}

// mustJSON serializa un valor o falla el test.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("serializando: %v", err)
	}
	return string(b)
}

// ============================ defectos por fila ============================

// TestParseTabular_PrecioNoNumerico_DiceLaFilaYElMotivo es el segundo criterio de
// T3.4. El literal del mensaje va escrito entero a propósito: lo lee el dueño del
// negocio, y es la clase de texto que se degrada sin que ningún test lo note.
//
// El precio es el ÚNICO campo que la lectura tabular juzga antes que el validador, y
// este test es el que explica por qué: en el documento el precio es un número, así
// que una celda ilegible no puede llegar al validador como «precio malo» —llegaría
// como 0, que es un precio válido— y el artículo se importaría regalado.
func TestParseTabular_PrecioNoNumerico_DiceLaFilaYElMotivo(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "$18.000"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 3 {
		t.Errorf("row=%d, quiero 3 (cabecera 1, primer artículo 2, este 3)", got.Row)
	}
	if got.Field != "precio" {
		t.Errorf("field=%q, quiero la COLUMNA de la planilla, no el campo del JSON", got.Field)
	}
	want := `la fila 3 ("Torta de unicornio"): el precio "$18.000" no es un número; ` +
		`escríbelo sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`
	if got.Reason != want {
		t.Errorf("motivo=%q,\nquiero  %q", got.Reason, want)
	}
	if got.CategoryIndex != nil || got.ItemIndex != nil {
		t.Errorf("el defecto trae índices de categoría/artículo (%v/%v): en una planilla se busca por fila",
			got.CategoryIndex, got.ItemIndex)
	}
}

// TestParseTabular_PrecioVacio_NoSeImportaRegalado: una celda de precio en blanco no
// puede colarse como 0. Es el mismo peligro que el precio ilegible, y el mensaje es
// el del validador («no tiene el precio: es obligatorio»).
func TestParseTabular_PrecioVacio_NoSeImportaRegalado(t *testing.T) {
	rows := planilla(fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", ""))

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 2 || got.Field != "precio" {
		t.Fatalf("row=%d field=%q, quiero 2/precio", got.Row, got.Field)
	}
	if !strings.Contains(got.Reason, "no tiene el precio: es obligatorio") {
		t.Errorf("motivo=%q, quiero el registro del validador", got.Reason)
	}
}

// TestParseTabular_LoQueJuzgaElValidadorTambienSalePorFila es la prueba de que el
// camino tabular no tiene validador propio: el sku repetido lo caza el validador del
// JSON (regla de negocio, unicidad global), y aun así el defecto sale con la FILA en
// la que está y con el nombre de la COLUMNA, no con «la categoría 2, artículo 1».
func TestParseTabular_LoQueJuzgaElValidadorTambienSalePorFila(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000"),
		fila("2|Salados", "", "2", "TORTA-CHOC", "Torta salada", "20000"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 4 {
		t.Errorf("row=%d, quiero 4 (la fila que repite el sku, no la que lo estrenó)", got.Row)
	}
	if got.Field != "sku" {
		t.Errorf("field=%q, quiero sku", got.Field)
	}
	if !strings.Contains(got.Reason, "único en TODO el catálogo") {
		t.Errorf("motivo=%q, quiero el del validador (unicidad global del sku)", got.Reason)
	}
}

// TestParseTabular_LosDefectosSalenEnOrdenDeFila: quien arregla una planilla la
// recorre de arriba abajo. El orden del validador es (categoría, artículo), que deja
// de coincidir con el de las filas en cuanto alguien intercala una fila de otra
// categoría — que es exactamente lo que hace esta planilla.
func TestParseTabular_LosDefectosSalenEnOrdenDeFila(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("2|Salados", "", "1", "TEQUENOS", "", "26000"),          // fila 3: sin nombre
		fila("1|Tortas", "", "2", "", "Torta de unicornio", "26000"), // fila 4: sin sku
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr == nil || len(verr.Errors) != 2 {
		t.Fatalf("defectos=%v, quiero 2", verr)
	}
	if verr.Errors[0].Row != 3 || verr.Errors[1].Row != 4 {
		t.Fatalf("filas=%d,%d; quiero 3,4 (orden de la hoja, no del documento)",
			verr.Errors[0].Row, verr.Errors[1].Row)
	}
}

// ============================ cabecera ============================

// TestParseTabular_CabeceraSinColumnasObligatorias: sin las columnas que sostienen un
// artículo no hay nada que leer, y decirlo una vez es mejor que devolver un defecto
// por cada fila de la hoja.
func TestParseTabular_CabeceraSinColumnasObligatorias(t *testing.T) {
	rows := [][]string{
		{"categoria", "nombre", "precio"},
		{"1|Tortas", "Torta de chocolate", "18000"},
	}

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 1 || got.Field != "cabecera" {
		t.Fatalf("row=%d field=%q, quiero 1/cabecera", got.Row, got.Field)
	}
	if !strings.Contains(got.Reason, "codigo") || !strings.Contains(got.Reason, "sku") {
		t.Errorf("motivo=%q, quiero que NOMBRE las columnas que faltan", got.Reason)
	}
}

// TestParseTabular_CabeceraTalYComoLaDevuelveExcel: la hoja la abre una persona, que
// la autocapitaliza, le pone la tilde que le falta a «descripcion» y deja espacios.
// Nada de eso es del negocio. Lo que NO se adivina es una columna con otro nombre:
// «valor» no se lee como «precio», y por eso esta planilla —que trae precio— importa
// con la columna de más ignorada.
func TestParseTabular_CabeceraTalYComoLaDevuelveExcel(t *testing.T) {
	rows := [][]string{
		{"Categoria", " Codigo", "SKU", "Nombre ", "Precio", "Descripción", "notas mías"},
		{"1|Tortas", "1", "TORTA-CHOC", "Torta de chocolate", "18000", "Con arequipe", "llamar al proveedor"},
	}

	doc, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la planilla no se importó: %+v", verr.Errors)
	}
	item := doc.Catalog.Categories[0].Items[0]
	if item.SKU != "TORTA-CHOC" || item.Price != 18000 || item.Description != "Con arequipe" {
		t.Fatalf("artículo=%+v: la cabecera con tildes y mayúsculas no se leyó bien", item)
	}
}

// TestParseTabular_SoloCabecera: una planilla descargada y devuelta sin llenar no es
// un catálogo vacío que haya que aplicar; es un despiste, y se dice como tal.
func TestParseTabular_SoloCabecera(t *testing.T) {
	_, verr := catalogimport.ParseTabular(planilla(), catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if !strings.Contains(got.Reason, "solo trae la cabecera") {
		t.Errorf("motivo=%q", got.Reason)
	}
}

// ============================ mini-sintaxis de las celdas ============================

// TestParseTabular_MiniSintaxisDelArticulo comprueba que lo que la plantilla EMITE en
// una celda vuelve a ser lo que era: subcategoría, etiquetas, atributos y variantes.
// La igualdad con el JSON ya lo prueba en bloque; esto lo prueba campo a campo, que
// es lo que dice CUÁL se rompió.
func TestParseTabular_MiniSintaxisDelArticulo(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "clasicas|Clásicas", "1", "TORTA-CHOC", "Torta de chocolate", "18000",
			"Con arequipe", "decorada; sin_lactosa", "capas|3; porciones|10-12",
			"V1|10-12 porciones|18000; V2|25-30 porciones|32000", ""),
	)

	doc, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la planilla no se importó: %+v", verr.Errors)
	}

	torta := doc.Catalog.Categories[0].Items[0]
	if torta.Subcategory != "clasicas" {
		t.Errorf("subcategoria=%q, quiero el CÓDIGO de la celda", torta.Subcategory)
	}
	subs := doc.Catalog.Categories[0].Subcategories
	if len(subs) != 1 || subs[0].Code != "clasicas" || subs[0].Label != "Clásicas" {
		t.Errorf("subcategorías=%+v: en la planilla se declaran escribiéndolas en la celda", subs)
	}
	if len(torta.Tags) != 2 || torta.Tags[0] != "decorada" || torta.Tags[1] != "sin_lactosa" {
		t.Errorf("tags=%v", torta.Tags)
	}
	if torta.Attributes["capas"] != "3" || torta.Attributes["porciones"] != "10-12" {
		t.Errorf("atributos=%v", torta.Attributes)
	}
	if len(torta.Variants) != 2 || torta.Variants[1].Code != "V2" ||
		torta.Variants[1].Label != "25-30 porciones" || torta.Variants[1].Price != 32000 {
		t.Errorf("variantes=%+v", torta.Variants)
	}
}

// TestParseTabular_MiniSintaxisDelCombo: la cantidad de un componente se lee de la
// celda y no se da por supuesta. El «|1» explícito del primer integrante es el que
// enseña a escribir el «|2» del segundo.
func TestParseTabular_MiniSintaxisDelCombo(t *testing.T) {
	rows := planilla(
		fila("1|Salados", "", "1", "TEQUENOS-15", "Tequeños", "26000"),
		fila("1|Salados", "", "2", "REFRESCO", "Refresco", "4000"),
		fila("1|Salados", "", "3", "COMBO", "Combo fiesta", "32000", "", "", "", "",
			"TEQUENOS-15|1; REFRESCO|2"),
	)

	doc, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la planilla no se importó: %+v", verr.Errors)
	}

	combo := doc.Catalog.Categories[0].Items[2]
	if len(combo.Components) != 2 {
		t.Fatalf("componentes=%+v, quiero 2", combo.Components)
	}
	if combo.Components[0].SKU != "TEQUENOS-15" || combo.Components[0].Qty != 1 {
		t.Errorf("componente 1=%+v", combo.Components[0])
	}
	if combo.Components[1].SKU != "REFRESCO" || combo.Components[1].Qty != 2 {
		t.Errorf("componente 2=%+v", combo.Components[1])
	}
}

// TestParseTabular_CeldasMalEscritas_UnaPorFila: cada gramática rota se cuenta donde
// está y con un ejemplo de cómo se escribe. Sin ejemplo, «formato inválido» deja a
// quien llenó la hoja exactamente igual de perdido que antes.
func TestParseTabular_CeldasMalEscritas_UnaPorFila(t *testing.T) {
	rows := planilla(
		fila("Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000", "", "", "", "V1|grande", ""),
		fila("2|Salados", "", "2", "COMBO", "Combo", "32000", "", "", "", "", "TEQUENOS"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr == nil || len(verr.Errors) != 3 {
		t.Fatalf("defectos=%v, quiero 3 (uno por fila, todos en la misma pasada)", verr)
	}
	casos := []struct {
		row     int
		field   string
		ejemplo string
	}{
		{2, "categoria", "«1|Tortas»"},
		{3, "variantes", "«V1|10-12 porciones|18000»"},
		{4, "componentes", "«TEQUENOS-15|1»"},
	}
	for i, want := range casos {
		got := verr.Errors[i]
		if got.Row != want.row || got.Field != want.field {
			t.Errorf("defecto %d: row=%d field=%q, quiero %d/%q", i, got.Row, got.Field, want.row, want.field)
		}
		if !strings.Contains(got.Reason, want.ejemplo) {
			t.Errorf("defecto %d: motivo=%q, quiero que enseñe cómo se escribe (%s)", i, got.Reason, want.ejemplo)
		}
	}
}

// TestParseTabular_UnaCategoriaUnNombre: el código de la categoría es lo que el
// cliente teclea en WhatsApp. Dos filas que le dan nombres distintos al MISMO código
// son un error de quien llena la hoja, y callarlo dejaría el nombre a merced del
// orden de las filas.
func TestParseTabular_UnaCategoriaUnNombre(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("1|Pasteles", "", "2", "TORTA-UNI", "Torta de unicornio", "26000"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 3 || got.Field != "categoria" {
		t.Fatalf("row=%d field=%q, quiero 3/categoria", got.Row, got.Field)
	}
	if !strings.Contains(got.Reason, "la fila 2") {
		t.Errorf("motivo=%q, quiero que señale también la fila que lo llamó distinto", got.Reason)
	}
}

// ============================ filas vacías y topes ============================

// TestParseTabular_FilasVaciasNiCuentanNiEstorban: una hoja de cálculo está llena de
// filas en blanco —las de abajo, las que alguien vació sin borrar— y ninguna es un
// defecto ni un artículo.
func TestParseTabular_FilasVaciasNiCuentanNiEstorban(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila(),
		fila("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "26000"),
		fila(),
	)

	doc, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("las filas vacías rompieron la lectura: %+v", verr.Errors)
	}
	if n := len(doc.Catalog.Categories[0].Items); n != 2 {
		t.Fatalf("artículos=%d, quiero 2", n)
	}
}

// TestParseTabular_PlantillaDevueltaSinLlenar: filas con formato pero sin nada
// escrito es lo que devuelve quien creyó que ya la había llenado. No es un catálogo
// vacío que haya que aplicar —eso borraría el catálogo entero— sino un despiste, y lo
// dice el validador con su propio mensaje.
func TestParseTabular_PlantillaDevueltaSinLlenar(t *testing.T) {
	_, verr := catalogimport.ParseTabular(planilla(fila(), fila(), fila()), catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if !strings.Contains(got.Reason, "no trae ninguna categoría") {
		t.Errorf("motivo=%q", got.Reason)
	}
}

// TestParseTabular_TopeDeArticulos: el tope se comprueba ANTES de armar nada, y por
// eso el mensaje habla de la planilla y no de ninguna fila. Una hoja de cien mil
// filas no se construye en memoria para acabar rechazándola.
func TestParseTabular_TopeDeArticulos(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "A", "Artículo A", "1"),
		fila("1|Tortas", "", "2", "B", "Artículo B", "2"),
		fila("1|Tortas", "", "3", "C", "Artículo C", "3"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.Limits{MaxItems: 2})
	got := unicoError(t, verr)

	if got.Row != 0 {
		t.Errorf("row=%d, quiero 0: el defecto no es de ninguna fila, es de la planilla entera", got.Row)
	}
	if !strings.Contains(got.Reason, "el máximo por importación es 2") {
		t.Errorf("motivo=%q", got.Reason)
	}
}

// TestParseTabular_LeerYJuzgarSonDosPasadas: la fila 3 no se puede leer (categoría
// rota), así que su sku nunca entra en el catálogo. Si se validara igualmente, el
// combo de la fila 4 reportaría un componente «que no existe» — un defecto inventado
// por el propio parser, que manda a buscar un problema donde no lo hay.
func TestParseTabular_LeerYJuzgarSonDosPasadas(t *testing.T) {
	rows := planilla(
		fila("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		fila("Salados", "", "1", "TEQUENOS-15", "Tequeños", "26000"),
		fila("1|Tortas", "", "2", "COMBO", "Combo fiesta", "32000", "", "", "", "", "TEQUENOS-15|1"),
	)

	_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	got := unicoError(t, verr)

	if got.Row != 3 || got.Field != "categoria" {
		t.Fatalf("defectos=%+v: quiero SOLO el de lectura de la fila 3", verr.Errors)
	}
}
