package catalogimport

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// BuildTemplate arma el documento de EJEMPLO que se descarga desde
// GET /api/v1/catalog/import/template. Se construye con los MISMOS structs del
// contrato (patrón buildImportTemplate de EduGo 038, design §1) y no con un
// literal JSON escrito a mano: así el MarshalJSON produce por definición la forma
// que el validador espera, y un cambio de contrato que olvide la plantilla no
// compila —o falla en el test que la valida— en vez de repartir plantillas
// caducadas que el import rechaza.
//
// TRAE LOS CUATRO CASOS del contrato, y no por afán de exhaustividad: la plantilla
// es la única documentación que el dueño del negocio va a leer de verdad. Lo que
// no esté aquí, no existe para quien la llena.
//
//   - Artículo SIMPLE (tequeños): lo mínimo obligatorio, code/sku/label/price.
//   - Artículo con VARIANTES (torta de chocolate): presentaciones con precio
//     propio; el precio del artículo queda como referencia y lo que se cobra es el
//     de la variante elegida.
//   - COMBO con components (combo fiesta): se vende como UNA línea a su propio
//     precio —32000, menos que la suma de sus partes— y sus integrantes son
//     información del dueño. Sus dos componentes referencian skus declarados en el
//     MISMO documento, que es lo que el validador exige.
//   - Artículo con TAGS y ATRIBUTOS (torta de unicornio): la clasificación
//     informativa que el runtime no navega pero que el dueño y el puente sí leen.
//
// Además lleva subcategorías —las dos declaradas y las dos usadas— porque son el
// segundo filtro del catálogo v2 y la planilla tabular tiene una columna para
// ellas: sin un ejemplo, esa columna sería un enigma.
func BuildTemplate() CatalogImport {
	return CatalogImport{
		Format:  ImportFormat,
		Version: ImportVersion,
		Source: &ImportSource{
			Kind: "plantilla",
			Hint: "ejemplo de wApp: reemplaza estas categorías y artículos por los tuyos",
		},
		Catalog: ImportBody{Categories: []ImportCategory{
			{
				Code:  "1",
				Label: "Tortas",
				Subcategories: []ImportSubcategory{
					{Code: "clasicas", Label: "Clásicas"},
					{Code: "infantiles", Label: "Infantiles"},
				},
				Items: []ImportItem{
					{
						Code:        "1",
						SKU:         "TORTA-CHOC",
						Label:       "Torta de chocolate",
						Price:       18000,
						Description: "Bizcocho de chocolate con relleno de arequipe.",
						Subcategory: "clasicas",
						Variants: []ImportVariant{
							{Code: "V1", Label: "10-12 porciones", Price: 18000},
							{Code: "V2", Label: "25-30 porciones", Price: 32000},
						},
					},
					{
						Code:        "2",
						SKU:         "TORTA-UNICORNIO",
						Label:       "Torta de unicornio",
						Price:       26000,
						Description: "Decorada con crema de colores y figura de azúcar.",
						Subcategory: "infantiles",
						Tags:        []string{"decorada", "sin_lactosa"},
						Attributes:  map[string]string{"capas": "3", "porciones": "10-12"},
					},
				},
			},
			{
				Code:  "2",
				Label: "Salados",
				Items: []ImportItem{
					{
						Code:        "1",
						SKU:         "TEQUENOS-15",
						Label:       "Tequeños (15 unidades)",
						Price:       26000,
						Description: "Congelados, listos para freír.",
					},
					{
						Code:  "2",
						SKU:   "REFRESCO-15L",
						Label: "Refresco 1.5 L",
						Price: 4000,
					},
					{
						Code:        "3",
						SKU:         "COMBO-FIESTA",
						Label:       "Combo fiesta",
						Price:       32000,
						Description: "Se cobra como una sola línea; sus partes son informativas.",
						Components: []ImportComponent{
							{SKU: "TEQUENOS-15", Qty: 1},
							{SKU: "REFRESCO-15L", Qty: 2},
						},
					},
				},
			},
		}},
	}
}

// ============================ planilla canónica (D-041.9) ============================

// El JSON es el contrato; la planilla es la MISMA información en la forma que un
// dueño de negocio ya sabe manejar. Todo lo que sigue —el nombre de las columnas,
// su orden y la mini-sintaxis de las celdas múltiples— es el contrato TABULAR, y
// vive aquí, en un solo sitio, porque lo escriben dos manos distintas: esta lo
// EMITE (la plantilla que se descarga) y el parser tabular (T3.4) lo LEE. Dos
// copias del literal se desincronizan al primer retoque, y el síntoma sería una
// planilla que el propio wApp no sabe volver a leer.

// TabularSheetName es el nombre de la hoja de datos del XLSX. El parser tabular
// debe buscarla POR NOMBRE y no por índice: quien abre el libro en Excel puede
// añadir hojas suyas —cuentas, notas— y dejarlas primero sin sospechar que con eso
// rompe el import.
const TabularSheetName = "catalogo"

const (
	// TabularEntrySeparator separa las ENTRADAS dentro de una celda múltiple: cada
	// variante, cada componente, cada etiqueta. Se emite seguido de un espacio para
	// que la celda se lea; el parser debe recortar los espacios de cada entrada.
	TabularEntrySeparator = ";"
	// TabularFieldSeparator separa los CAMPOS dentro de una entrada
	// (`V1|10-12 porciones|18000`). Es también el separador de `codigo|nombre` en las
	// celdas de categoría y subcategoría.
	TabularFieldSeparator = "|"
)

// tabularColumns es el ORDEN y el NOMBRE exactos de las columnas de la planilla
// canónica: el literal final que anuncia el design (D-041.9) y que el parser
// tabular tiene que reconocer. Las columnas de una hoja son POSICIONALES para
// quien la llena, así que este orden es contrato: añadir una en medio correría
// todo lo que viene detrás y rompería cualquier planilla ya llenada.
//
// ATRIBUTOS ES LA UNDÉCIMA, Y NO ESTABA EN LA LISTA DEL DESIGN (que enumera diez y
// remite «el literal final» a esta plantilla). Se añade porque el contrato JSON
// tiene `attributes` y un artículo del cuarto caso —tags Y atributos— no se podría
// expresar en la planilla: el camino tabular sería LOSSY frente al JSON y el
// criterio de T3.4 («la planilla llenada produce el mismo blob que su equivalente
// JSON») sería imposible de cumplir. Va junto a `tags` porque las dos son
// clasificación informativa, y antes de `variantes`/`componentes`, que son
// estructura.
//
// Los nombres salen de las constantes que declara el LECTOR (tabular.go): el
// emisor y el parser tienen que decir exactamente lo mismo, y con el literal
// escrito dos veces bastaría una errata en una de ellas para emitir una planilla
// que este servidor no sabe releer.
var tabularColumns = []string{
	colCategoria,
	colSubcategoria,
	colCodigo,
	colSKU,
	colNombre,
	colPrecio,
	colDescripcion,
	colTags,
	colAtributos,
	colVariantes,
	colComponentes,
}

// TabularColumns devuelve las columnas de la planilla canónica, en orden. Devuelve
// una COPIA: es un contrato, y un consumidor que reordene o recorte el original
// dejaría al emisor y al parser mirando cabeceras distintas sin que nada avise.
func TabularColumns() []string {
	return slices.Clone(tabularColumns)
}

// TemplateSheetRows son las filas de la planilla canónica que corresponden a
// BuildTemplate(): UNA FILA POR ARTÍCULO, en el mismo orden en que están en el
// documento, con la cabecera de categoría repetida en cada fila (así se filtra y
// se ordena en la hoja sin cruzar pestañas).
//
// Salen del MISMO documento que el JSON, no de un literal paralelo: es lo que hace
// que las dos descargas sean el mismo catálogo dicho de dos maneras y lo que le
// permite a T3.4 afirmar la igualdad de blobs sin escribir un fixture nuevo.
//
// Las celdas viajan como `any` porque los dos formatos las quieren distintas: el
// CSV las formatea a texto y el XLSX escribe el precio COMO NÚMERO —si fuera
// texto, la hoja no lo sumaría y, peor, el dueño lo editaría como cadena y lo
// devolvería con separador de miles.
func TemplateSheetRows() [][]any {
	return sheetRows(BuildTemplate())
}

// sheetRows desnormaliza un documento de import a filas de la planilla canónica.
func sheetRows(doc CatalogImport) [][]any {
	rows := make([][]any, 0, countTemplateItems(doc))
	for _, cat := range doc.Catalog.Categories {
		subs := make(map[string]string, len(cat.Subcategories))
		for _, s := range cat.Subcategories {
			subs[s.Code] = s.Label
		}
		for _, it := range cat.Items {
			rows = append(rows, []any{
				codeLabelCell(cat.Code, cat.Label),
				codeLabelCell(it.Subcategory, subs[it.Subcategory]),
				it.Code,
				it.SKU,
				it.Label,
				it.Price,
				it.Description,
				tagsCell(it.Tags),
				attributesCell(it.Attributes),
				variantsCell(it.Variants),
				componentsCell(it.Components),
			})
		}
	}
	return rows
}

// countTemplateItems cuenta los artículos del documento (para dimensionar las
// filas de una vez).
func countTemplateItems(doc CatalogImport) int {
	n := 0
	for _, c := range doc.Catalog.Categories {
		n += len(c.Items)
	}
	return n
}

// codeLabelCell escribe una categoría o subcategoría como `codigo|nombre`.
//
// EL CÓDIGO VA EXPLÍCITO, no se deduce del orden de las filas, y es una decisión de
// negocio, no de formato: el código de una categoría es LO QUE EL CLIENTE TECLEA en
// WhatsApp para entrar en ella. Numerarlas solas por posición le cambiaría los
// números al cliente cada vez que el dueño reordena filas en su hoja, y nadie
// entendería por qué «el 2 ya no es Salados».
func codeLabelCell(code, label string) string {
	if code == "" {
		return ""
	}
	return code + TabularFieldSeparator + label
}

// tagsCell escribe las etiquetas como entradas de un solo campo:
// `decorada; sin_lactosa`.
func tagsCell(tags []string) string {
	return joinEntries(tags)
}

// attributesCell escribe los atributos como entradas `clave|valor`, ordenadas por
// clave: un mapa de Go no tiene orden y la plantilla tiene que salir idéntica en
// cada descarga (si no, no se puede afirmar en un test ni comparar dos archivos).
func attributesCell(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	entries := make([]string, 0, len(attrs))
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		entries = append(entries, k+TabularFieldSeparator+attrs[k])
	}
	return joinEntries(entries)
}

// variantsCell escribe las presentaciones como `codigo|nombre|precio`:
// `V1|10-12 porciones|18000; V2|25-30 porciones|32000` (D-041.9).
func variantsCell(vars []ImportVariant) string {
	if len(vars) == 0 {
		return ""
	}
	entries := make([]string, 0, len(vars))
	for _, v := range vars {
		entries = append(entries, joinFields(v.Code, v.Label, formatAmount(v.Price)))
	}
	return joinEntries(entries)
}

// componentsCell escribe los integrantes del combo como `sku|cantidad`:
// `TEQUENOS-15|1; REFRESCO-15L|2`.
//
// La cantidad se emite SIEMPRE, también cuando vale 1. En el JSON es omitible
// porque el contrato dice que ausente vale 1; en una celda, `TEQUENOS-15` a secas
// se leería como un campo incompleto y el dueño no tendría dónde escribir el 2 del
// refresco por analogía.
func componentsCell(comps []ImportComponent) string {
	if len(comps) == 0 {
		return ""
	}
	entries := make([]string, 0, len(comps))
	for _, c := range comps {
		qty := c.Qty
		if qty <= 0 {
			qty = 1
		}
		entries = append(entries, joinFields(c.SKU, strconv.Itoa(qty)))
	}
	return joinEntries(entries)
}

// joinEntries une entradas de una celda múltiple con el separador de entradas más
// un espacio (legibilidad; el parser recorta).
func joinEntries(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, TabularEntrySeparator+" ")
}

// joinFields une los campos de UNA entrada.
func joinFields(fields ...string) string {
	return strings.Join(fields, TabularFieldSeparator)
}

// formatAmount escribe un importe como lo escribiría una persona en una celda: sin
// ceros de relleno, con punto decimal y sin separador de miles (es un dato, no
// presentación — la moneda ni se nombra, D-04).
func formatAmount(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
