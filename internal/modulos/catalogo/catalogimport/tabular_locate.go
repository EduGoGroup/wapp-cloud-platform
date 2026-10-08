// Porta internal/catalogimport/tabular.go @ 3c74b80 (el documento que se arma y la vuelta de cada defecto a su fila, líneas 57-77 y 461-546; partido de tabular.go por E-13)

package catalogimport

import (
	"cmp"
	"slices"
	"strings"
)

// tabularColumnOf traduce el campo del contrato (inglés, como en el JSON) a la
// columna de la planilla, para que un defecto que el validador ubica en "price"
// señale la columna «precio» que el dueño tiene delante. Lo que no está aquí no
// tiene columna (format, version, catalog) y se devuelve tal cual.
var tabularColumnOf = map[string]string{
	"code":        colCodigo,
	"sku":         colSKU,
	"label":       colNombre,
	"price":       colPrecio,
	"description": colDescripcion,
	"subcategory": colSubcategoria,
	"tags":        colTags,
	"attributes":  colAtributos,
	"variants":    colVariantes,
	"components":  colComponentes,
}

// tabularSourceKind es la procedencia que se anota en el documento leído de una
// planilla. Es informativa (el validador no la interpreta) y no viaja al blob: sirve
// para que después se sepa que ese catálogo entró por una hoja de cálculo.
const tabularSourceKind = "planilla"

// ============================ documento y ubicación ============================

// document arma el documento del contrato con lo leído. Va con su format y su
// version puestos por nosotros: quien llena una planilla no tiene dónde escribirlos y
// no se le va a pedir que los sepa.
func (p *tabular) document() CatalogImport {
	cats := make([]ImportCategory, 0, len(p.order))
	for _, c := range p.order {
		subs := make([]ImportSubcategory, 0, len(c.subOrder))
		for _, code := range c.subOrder {
			subs = append(subs, ImportSubcategory{Code: code, Label: c.subs[code].label})
		}
		cats = append(cats, ImportCategory{
			Code:          c.code,
			Label:         c.label,
			Subcategories: subs,
			Items:         c.items,
		})
	}
	return CatalogImport{
		Format:  ImportFormat,
		Version: ImportVersion,
		Source:  &ImportSource{Kind: tabularSourceKind},
		Catalog: ImportBody{Categories: cats},
	}
}

// localize devuelve a su fila los defectos que el validador ubicó por índices, y
// borra los índices: en el camino tabular son una coordenada de un documento que el
// usuario no ha visto, y dejar las dos ubicaciones sería hablarle en dos idiomas de
// los que solo entiende uno.
//
// Ordena por fila porque es el orden en el que la persona va a recorrer su hoja; el
// del validador (categoría, artículo) no coincide con el de las filas en cuanto
// alguien intercala en su planilla una fila de otra categoría.
func (p *tabular) localize(verr *ImportValidationError) *ImportValidationError {
	for i := range verr.Errors {
		e := &verr.Errors[i]
		e.Row = p.rowOf(e.CategoryIndex, e.ItemIndex)
		e.Field = tabularColumn(*e)
		e.CategoryIndex, e.ItemIndex = nil, nil
	}
	slices.SortStableFunc(verr.Errors, func(a, b ImportFieldError) int {
		return cmp.Compare(a.Row, b.Row)
	})
	return verr
}

// rowOf traduce (categoría, artículo) a la fila que lo produjo. Un defecto de la
// categoría entera se atribuye a la PRIMERA fila que la nombró, que es donde el dueño
// va a ir a mirar.
func (p *tabular) rowOf(category, item *int) int {
	if category == nil || *category >= len(p.order) {
		return 0
	}
	cat := p.order[*category]
	if item == nil || *item >= len(cat.itemRows) {
		return cat.row
	}
	return cat.itemRows[*item]
}

// tabularColumn traduce el campo del contrato a la columna de la planilla que lo
// contiene.
//
// Un defecto de la CATEGORÍA (sin artículo) señala siempre la columna `categoria`
// aunque el validador lo llame "label" o "code": en la planilla el nombre y el código
// de la categoría viven los dos en esa celda, mientras que las columnas `nombre` y
// `codigo` son las del artículo. Traducirlo por el diccionario mandaría al dueño a
// mirar la celda equivocada.
func tabularColumn(e ImportFieldError) string {
	if e.CategoryIndex != nil && e.ItemIndex == nil {
		if strings.HasPrefix(e.Field, "subcategories") {
			return colSubcategoria
		}
		return colCategoria
	}
	base := e.Field
	if i := strings.IndexAny(base, "[."); i >= 0 {
		base = base[:i] // "variants[1].price" → "variants": la celda es una sola
	}
	if col, ok := tabularColumnOf[base]; ok {
		return col
	}
	return e.Field
}
