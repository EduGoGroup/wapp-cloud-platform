package catalogimport_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// TestBuildTemplate_PasaSuPropioValidador es EL criterio de T3.2 y el único que, si
// falla, invalida todo lo demás: la plantilla que wApp reparte tiene que pasar el
// validador de T3.1 sin un solo error.
//
// Se valida sobre los BYTES serializados, no sobre el struct: eso es lo que se
// descarga, lo que el dueño edita y lo que vuelve a subir. Un test sobre el struct
// daría por buena una plantilla que se rompe al serializarse (un campo sin etiqueta
// json, un omitempty de más).
//
// Repartir una plantilla que el propio import rechaza es el peor defecto posible de
// esta tarea: el dueño la descarga, la llena bien, la sube y se encuentra con una
// lista de errores que él no cometió.
func TestBuildTemplate_PasaSuPropioValidador(t *testing.T) {
	raw, err := json.Marshal(catalogimport.BuildTemplate())
	if err != nil {
		t.Fatalf("no se pudo serializar la plantilla: %v", err)
	}

	doc, verr := catalogimport.Validate(raw, catalogimport.DefaultLimits())
	if verr != nil {
		for _, e := range verr.Errors {
			t.Logf("  campo=%q → %s", e.Field, e.Reason)
		}
		t.Fatalf("la plantilla que repartimos tiene %d defectos según nuestro propio validador", len(verr.Errors))
	}
	if doc.Format != catalogimport.ImportFormat || doc.Version != catalogimport.ImportVersion {
		t.Fatalf("la plantilla no lleva la cabecera del contrato: format=%q version=%d", doc.Format, doc.Version)
	}
}

// TestBuildTemplate_TraeLosCuatroCasos comprueba que la plantilla enseña los cuatro
// casos del contrato. No es decoración: la plantilla es la única documentación que
// el dueño del negocio lee de verdad, y lo que no aparezca en ella no existe para
// quien la llena. Un artículo que se caiga en un refactor dejaría la plantilla
// válida —y muda sobre esa mitad del contrato— sin que nada avisara.
func TestBuildTemplate_TraeLosCuatroCasos(t *testing.T) {
	tpl := catalogimport.BuildTemplate()

	casos := contarCasos(tpl)
	for _, caso := range []string{"simple", "variantes", "combo", "tags+atributos"} {
		if casos[caso] == 0 {
			t.Errorf("la plantilla no trae ningún artículo del caso %q: %v", caso, casos)
		}
	}
	comprobarCombosResueltos(t, tpl)

	// Subcategorías: declaradas Y usadas. Una columna «subcategoria» sin un solo
	// ejemplo sería un enigma para quien llena la planilla.
	cat0 := tpl.Catalog.Categories[0]
	if len(cat0.Subcategories) == 0 || cat0.Items[0].Subcategory == "" {
		t.Fatalf("la plantilla no ejercita las subcategorías: %+v", cat0)
	}
}

// contarCasos clasifica cada artículo de la plantilla en uno de los cuatro casos
// del contrato.
func contarCasos(tpl catalogimport.CatalogImport) map[string]int {
	casos := make(map[string]int, 4)
	for _, cat := range tpl.Catalog.Categories {
		for _, it := range cat.Items {
			switch {
			case len(it.Variants) > 0:
				casos["variantes"]++
			case len(it.Components) > 0:
				casos["combo"]++
			case len(it.Tags) > 0 && len(it.Attributes) > 0:
				casos["tags+atributos"]++
			default:
				casos["simple"]++
			}
		}
	}
	return casos
}

// comprobarCombosResueltos exige que los componentes del combo apunten a artículos
// del MISMO documento. Si apuntaran fuera, el validador rechazaría la plantilla —y
// el test que la valida se caería—, pero el fallo se leería como «la plantilla no
// vale» sin decir por qué.
func comprobarCombosResueltos(t *testing.T, tpl catalogimport.CatalogImport) {
	t.Helper()
	skus := make(map[string]bool)
	for _, cat := range tpl.Catalog.Categories {
		for _, it := range cat.Items {
			skus[it.SKU] = true
		}
	}
	for _, cat := range tpl.Catalog.Categories {
		for _, it := range cat.Items {
			for _, c := range it.Components {
				if !skus[c.SKU] {
					t.Errorf("el combo %q referencia el sku %q, que no está en la plantilla", it.SKU, c.SKU)
				}
			}
		}
	}
}

// TestBuildTemplate_ElRuntimeLaLeeSinUnSoloAviso cierra el círculo del paquete: el
// validador acepta exactamente lo que el runtime conserva, así que la plantilla
// —que valida— tiene que producir un catálogo que cart.ParseCatalog lee entero.
//
// Si esto fallara, el dueño podría importar la plantilla tal cual, ver «aplicado» y
// tener en producción un catálogo con partes que el motor descarta en silencio.
func TestBuildTemplate_ElRuntimeLaLeeSinUnSoloAviso(t *testing.T) {
	blob, err := json.Marshal(catalogimport.BuildTemplate().Catalog)
	if err != nil {
		t.Fatalf("no se pudo serializar el catálogo de la plantilla: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("no se pudo releer el blob: %v", err)
	}

	cat, err := cart.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		t.Fatalf("el runtime rechazó la plantilla: %v", err)
	}
	if len(cat.Warnings) != 0 {
		t.Fatalf("el runtime descartó partes de la plantilla: %+v", cat.Warnings)
	}
	if len(cat.Categories) != 2 {
		t.Fatalf("categorías en el runtime=%d, quiero 2", len(cat.Categories))
	}
}

// TestTemplateSheet_CabeceraYFormaDeLaTabla afirma el esqueleto del contrato
// TABULAR que T3.4 va a parsear: el nombre y el ORDEN exactos de las columnas
// (D-041.9 más `atributos`, ver template.go) y una fila por artículo.
//
// El literal de la cabecera va escrito a mano A PROPÓSITO: compararlo con la misma
// variable que lo produce no probaría nada. Escrito entero, es lo que se rompe si
// alguien reordena o renombra una columna, que es justo lo que dejaría al parser de
// T3.4 leyendo otra cosa y a cualquier planilla ya llenada sin sitio donde encajar.
func TestTemplateSheet_CabeceraYFormaDeLaTabla(t *testing.T) {
	cols := catalogimport.TabularColumns()
	want := []string{
		"categoria", "subcategoria", "codigo", "sku", "nombre", "precio",
		"descripcion", "tags", "atributos", "variantes", "componentes",
	}
	if len(cols) != len(want) {
		t.Fatalf("columnas=%v, quiero %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Fatalf("columna %d = %q, quiero %q (el orden es contrato)", i, cols[i], want[i])
		}
	}

	rows := catalogimport.TemplateSheetRows()
	if len(rows) != 5 {
		t.Fatalf("filas=%d, quiero 5 (una por artículo de la plantilla)", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(cols) {
			t.Fatalf("la fila %d tiene %d celdas y hay %d columnas", i, len(row), len(cols))
		}
	}
}

// TestTemplateSheet_MiniSintaxisDeLasCeldas afirma cómo se escribe en UNA celda lo
// que en el JSON es una lista: variantes, componentes, etiquetas y atributos
// (D-041.9), más el `codigo|nombre` de categoría y subcategoría.
//
// Es la mitad del contrato tabular que no se ve en la cabecera y la que T3.4 tiene
// que saber deshacer.
func TestTemplateSheet_MiniSintaxisDeLasCeldas(t *testing.T) {
	rows := catalogimport.TemplateSheetRows()
	cols := catalogimport.TabularColumns()

	// Fila 0: la torta con variantes. Categoría y subcategoría llevan el código
	// explícito, el precio viaja como NÚMERO y las variantes usan la mini-sintaxis.
	got := rows[0]
	if got[0] != "1|Tortas" {
		t.Errorf("categoria=%v, quiero %q (codigo|nombre)", got[0], "1|Tortas")
	}
	if got[1] != "clasicas|Clásicas" {
		t.Errorf("subcategoria=%v, quiero %q", got[1], "clasicas|Clásicas")
	}
	if precio, ok := got[5].(float64); !ok || precio != 18000 {
		t.Errorf("precio=%v (%T), quiero el número 18000: como texto la hoja no lo suma", got[5], got[5])
	}
	if got[9] != "V1|10-12 porciones|18000; V2|25-30 porciones|32000" {
		t.Errorf("variantes=%v, quiero la mini-sintaxis de D-041.9", got[9])
	}

	// Fila 1: tags y atributos. Los atributos salen ORDENADOS por clave (un mapa de
	// Go no tiene orden y la plantilla tiene que ser idéntica en cada descarga).
	if rows[1][7] != "decorada; sin_lactosa" {
		t.Errorf("tags=%v, quiero %q", rows[1][7], "decorada; sin_lactosa")
	}
	if rows[1][8] != "capas|3; porciones|10-12" {
		t.Errorf("atributos=%v, quiero %q (ordenados por clave)", rows[1][8], "capas|3; porciones|10-12")
	}

	// Fila 2: el artículo simple no inventa nada en las columnas que no usa.
	for _, i := range []int{1, 7, 8, 9, 10} {
		if rows[2][i] != "" {
			t.Errorf("el artículo simple trae la columna %q = %v; debería ir vacía", cols[i], rows[2][i])
		}
	}

	// Fila 4: el combo. La cantidad se escribe SIEMPRE, también cuando vale 1.
	if rows[4][10] != "TEQUENOS-15|1; REFRESCO-15L|2" {
		t.Errorf("componentes=%v, quiero %q", rows[4][10], "TEQUENOS-15|1; REFRESCO-15L|2")
	}
}

// TestTabularColumns_DevuelveCopia: las columnas son un contrato compartido entre el
// emisor de la plantilla y el parser tabular. Si se devolviera el slice interno, un
// consumidor que lo reordenara dejaría a los dos leyendo cabeceras distintas sin que
// nada avisase.
func TestTabularColumns_DevuelveCopia(t *testing.T) {
	cols := catalogimport.TabularColumns()
	cols[0] = "destrozada"
	if catalogimport.TabularColumns()[0] != "categoria" {
		t.Fatal("mutar el resultado de TabularColumns cambió el contrato para todos")
	}
}

// TestImportPrompt_SeRevisaConElContrato es el mecanismo que hace REAL el «versionado
// junto al contrato» del design §6. Sin esto, la promesa de revisar el prompt al
// cambiar el contrato depende de que alguien se acuerde; con esto, subir
// ImportVersion pone la suite en rojo hasta que alguien abra prompt.go.
func TestImportPrompt_SeRevisaConElContrato(t *testing.T) {
	if catalogimport.PromptContractVersion != catalogimport.ImportVersion {
		t.Fatalf("PromptContractVersion=%d e ImportVersion=%d: el contrato del import cambió y el prompt-plantilla no se revisó. "+
			"Lee prompt.go, decide si el texto sigue diciendo la verdad y sube el número EN ESE MISMO commit.",
			catalogimport.PromptContractVersion, catalogimport.ImportVersion)
	}
}

// TestImportPrompt_DictaElContratoQueElValidadorExige: el prompt le dice al LLM
// exactamente el format y la versión que este validador acepta. Escritos a mano se
// quedarían viejos al primer cambio de contrato y el dueño del negocio culparía a su
// LLM de un documento que rechazamos nosotros.
func TestImportPrompt_DictaElContratoQueElValidadorExige(t *testing.T) {
	prompt := catalogimport.ImportPrompt()

	if !strings.Contains(prompt, catalogimport.ImportFormat) {
		t.Errorf("el prompt no nombra el format %q:\n%s", catalogimport.ImportFormat, prompt)
	}
	if !strings.Contains(prompt, "version: 1") {
		t.Errorf("el prompt no dicta la versión del contrato:\n%s", prompt)
	}
	// Las tres piezas del contrato que un LLM se salta si no se las nombran.
	for _, campo := range []string{"sku", "price", "variants", "components"} {
		if !strings.Contains(prompt, campo) {
			t.Errorf("el prompt no menciona %q:\n%s", campo, prompt)
		}
	}
	if strings.Contains(prompt, "%!") {
		t.Errorf("el prompt tiene un verbo de formato sin sustituir:\n%s", prompt)
	}
}
