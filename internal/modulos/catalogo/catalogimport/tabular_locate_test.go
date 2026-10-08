package catalogimport_test

import (
	"testing"
)

// tabular_locate_test.go: cómo vuelve a su FILA y a su COLUMNA lo que juzga el
// validador del JSON cuando el documento entró por una planilla. Parte de los tests
// de tabular.go (E-13); gemelo de tabular_locate.go. Los ayudantes están en
// tabular_test.go.

// TestParseTabular_WhatTheValidatorJudgesAlsoComesByRow es la prueba de que el
// camino tabular no tiene validador propio: el sku repetido lo caza el validador
// del JSON (unicidad global), y aun así el defecto sale con la FILA en la que está
// —la que repite el sku, no la que lo estrenó— y con el nombre de la COLUMNA. El
// motivo es el del validador, sin tocar.
func TestParseTabular_WhatTheValidatorJudgesAlsoComesByRow(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000"),
		row("2|Salados", "", "2", "TORTA-CHOC", "Torta salada", "20000"),
	))
	assertRowDefects(t, verr, rowDefect{4, "sku",
		`el artículo 2 ("Torta salada") de la categoría "Salados": el sku "TORTA-CHOC" ya lo usa el artículo 1 ("Torta de chocolate") de la categoría "Tortas"; ` +
			`el sku identifica al artículo en el pedido y tiene que ser único en TODO el catálogo, no solo dentro de su categoría.`})
}

// TestParseTabular_DefectsComeInRowOrder: quien arregla una planilla la recorre de
// arriba abajo. El orden del validador es (categoría, artículo), que deja de
// coincidir con el de las filas en cuanto alguien intercala una fila de otra
// categoría —que es exactamente lo que hace esta planilla: la fila 3 es de la
// SEGUNDA categoría y la 4, de la primera—.
func TestParseTabular_DefectsComeInRowOrder(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("2|Salados", "", "1", "TEQUENOS", "", "26000"),
		row("1|Tortas", "", "2", "", "Torta de unicornio", "26000"),
	))
	assertRowDefects(t, verr,
		rowDefect{3, "nombre", `el artículo 1 de la categoría "Salados" no tiene nombre: es lo que ve el cliente en la lista.`},
		rowDefect{4, "sku", `el artículo 2 ("Torta de unicornio") de la categoría "Tortas" no tiene el sku: es obligatorio.`},
	)
}

// TestParseTabular_SeveralDefectsOfARowKeepTheValidatorOrder: dentro de una misma
// fila, los defectos conservan el orden en que el validador los encontró.
func TestParseTabular_SeveralDefectsOfARowKeepTheValidatorOrder(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "A", "Art A", "1"),
		row("2|Otras", "", "1", "B", "", "1"),
		row("1|Tortas", "", "1", "C", "Art C", "1", "", "", "", "", "ZZ|1"),
	))
	assertRowDefects(t, verr,
		rowDefect{3, "nombre", `el artículo 1 de la categoría "Otras" no tiene nombre: es lo que ve el cliente en la lista.`},
		rowDefect{4, "codigo", `el artículo 2 ("Art C") de la categoría "Tortas": el código "1" ya lo usa el artículo 1 de la misma categoría; el cliente teclea ese número y no se sabría cuál de los dos pidió.`},
		rowDefect{4, "componentes", `el artículo 2 ("Art C") de la categoría "Tortas", componente 1: el sku "ZZ" no existe en el catálogo; los componentes de un combo tienen que ser artículos declarados.`},
	)
}

// TestParseTabular_FieldIsTheSheetColumn: un defecto que el validador ubica en
// "price" señala la columna «precio» que el dueño tiene delante; y uno de DENTRO de
// una celda múltiple ("variants[1].code") señala la celda entera, que es una sola.
func TestParseTabular_FieldIsTheSheetColumn(t *testing.T) {
	const subject = `el artículo 1 ("Art A") de la categoría "Tortas"`
	cases := map[string]struct {
		row  []string
		want []rowDefect
	}{
		"label → nombre, code → codigo": {row("1|Tortas", "", "", "A", "", "1"), []rowDefect{
			{2, "nombre", `el artículo 1 de la categoría "Tortas" no tiene nombre: es lo que ve el cliente en la lista.`},
			{2, "codigo", `el artículo 1 de la categoría "Tortas" no tiene el código: es obligatorio.`},
		}},
		// La celda se lee recortada, así que el prefijo reservado se detecta aunque
		// lleve espacios delante: al revés que en el camino JSON.
		"sku → sku": {row("1|Tortas", "", "1", " _shipping ", "Art A", "1"), []rowDefect{
			{2, "sku", subject + `: el sku "_shipping" empieza por "_", que está reservado para las líneas que pone wApp (el envío, por ejemplo). Ponle otro.`},
		}},
		"price → precio, variants[0].price → variantes": {row("1|Tortas", "", "1", "A", "Art A", "-5", "", "", "", "V1|x|-1", ""), []rowDefect{
			{2, "precio", subject + ": el precio no puede ser negativo."},
			{2, "variantes", subject + ", variante 1: el precio no puede ser negativo."},
		}},
		"variants[1].code → variantes": {row("1|Tortas", "", "1", "A", "Art A", "1", "", "", "", "V1|x|1; V1|y|2", ""), []rowDefect{
			{2, "variantes", subject + `, variante 2: el código "V1" ya lo usa otra variante del mismo artículo.`},
		}},
		"components[0].sku → componentes": {row("1|Tortas", "", "1", "A", "Art A", "1", "", "", "", "", "NOPE|1"), []rowDefect{
			{2, "componentes", subject + `, componente 1: el sku "NOPE" no existe en el catálogo; los componentes de un combo tienen que ser artículos declarados.`},
		}},
		"variants → variantes": {row("1|Tortas", "", "1", "A", "Art A", "1", "", "", "", "V1|x|1", "A|1"), []rowDefect{
			{2, "variantes", subject + " declara variantes y componentes a la vez: o se vende en presentaciones (variants) o es un combo (components), no las dos cosas."},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := parseSheet(sheet(c.row))
			assertRowDefects(t, verr, c.want...)
		})
	}
}

// TestParseTabular_ZeroWidthSKUTravelsAsWritten (corpus adversario): el recorte de
// la celda no se lleva el espacio de ancho cero, y el validador —que tampoco lo
// tiene por espacio— lo acepta como sku.
func TestParseTabular_ZeroWidthSKUTravelsAsWritten(t *testing.T) {
	doc := mustParseSheet(t, sheet(row("1|Tortas", "", "1", zeroWidthSpace, "Art A", "1")))
	if got := doc.Catalog.Categories[0].Items[0].SKU; got != zeroWidthSpace {
		t.Errorf("sku = %q; se esperaba el espacio de ancho cero tal cual", got)
	}
}
