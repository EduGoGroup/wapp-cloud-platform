// Porta internal/catalogimport/template.go @ 3c74b80

package catalogimport

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

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
//
// Promete: el documento, serializado indentado a dos espacios y con salto final,
// es testdata/template.json byte a byte (es lo que se descarga); pasa su propio
// validador sin un defecto; el runtime lee su catálogo sin un aviso; y cada
// llamada devuelve un documento nuevo (mutar uno no cambia el siguiente).
func BuildTemplate() CatalogImport {
	panic(pendiente.Implementar("catalogimport.BuildTemplate"))
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

// TabularColumns devuelve las columnas de la planilla canónica, en orden. Devuelve
// una COPIA: es un contrato, y un consumidor que reordene o recorte el original
// dejaría al emisor y al parser mirando cabeceras distintas sin que nada avise.
//
// Son ONCE, en este orden y con estos nombres exactos: categoria, subcategoria,
// codigo, sku, nombre, precio, descripcion, tags, atributos, variantes,
// componentes. Las columnas de una hoja son POSICIONALES para quien la llena, así
// que el orden es contrato: añadir una en medio correría todo lo que viene detrás y
// rompería cualquier planilla ya llenada. `atributos` es la undécima del design
// (D-041.9 enumera diez): sin ella, un artículo con tags Y atributos no se podría
// expresar en la planilla y el camino tabular sería lossy frente al JSON.
func TabularColumns() []string {
	panic(pendiente.Implementar("catalogimport.TabularColumns"))
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
//
// Promete (testdata/template_sheet.json, byte a byte): cada fila tiene una celda por
// columna de TabularColumns, todas string salvo el precio, que es float64; categoría
// y subcategoría se escriben `codigo|nombre` con el código EXPLÍCITO (vacía si el
// artículo no tiene subcategoría); etiquetas, atributos, variantes y componentes
// usan la mini-sintaxis —entradas separadas por «; », campos por «|»—, los atributos
// ordenados por clave, las variantes como `codigo|nombre|precio` y los componentes
// como `sku|cantidad` con la cantidad SIEMPRE escrita, también cuando vale 1; y las
// columnas que un artículo no usa van vacías.
func TemplateSheetRows() [][]any {
	panic(pendiente.Implementar("catalogimport.TemplateSheetRows"))
}
