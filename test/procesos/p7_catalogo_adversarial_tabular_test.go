//go:build integracion

package procesos

import (
	"fmt"
	"testing"
)

// La tabla adversaria de la planilla de P7 (reglas.md §2). La planilla RECORTA todas sus celdas antes
// de que el validador las vea, así que resuelve distinto que el import JSON los mismos datos:
//
//   - `PAN` y `PAN`+U+00A0 son el MISMO sku, repetido (en el JSON son dos artículos);
//   - `1` y `1`+U+00A0 son el mismo código de artículo, repetido (en el JSON, dos);
//   - U+00A0 + `_x` es `_x`, reservado (en el JSON pasa);
//   - el precio lo lee strconv.ParseFloat: los dígitos no ASCII, el separador de miles y la coma
//     decimal se rechazan con la fila, pero la notación científica, el signo `+`, el hexadecimal con
//     exponente y el guion bajo entre dígitos (`1_000`) se ACEPTAN.
//
// La cabecera se lee por el nombre de sus columnas, sin distinguir mayúsculas, tildes ni espacios en
// los extremos; un espacio —del tipo que sea— en medio del nombre la deja sin reconocer.

// adversarialTabular es la tabla adversaria de la planilla.
func (w *p7World) adversarialTabular(t *testing.T) {
	door := func(t *testing.T, ref string, doc []byte) respuesta {
		return w.importFile(t, w.admin, "apply", ref, doc)
	}
	w.runAdversaries(t, "p7-advt", door, append(p7TabularHeaders(t), append(p7TabularIdentifiers(t), p7TabularValues(t)...)...))
}

// p7TabularHeaders son los casos de la cabecera.
func p7TabularHeaders(t *testing.T) []p7Adversary {
	t.Helper()
	sheet := func(header []string, row ...string) []byte { return p7CSV(t, ',', [][]string{header, row}) }
	const missing = "a la primera fila le faltan columnas "
	return []p7Adversary{
		{name: "header_with_spaces_case_and_accents",
			doc: sheet([]string{p7NBSP + "Categoria" + p7NBSP, " Código", "SKU" + p7IDSP, "Nombre ", "PRECIO", p7EMSP + "Descripción", "notas mías"},
				"1|A", "1", "S", "L", "1", "d", "llamar al proveedor"),
			stored: p7Art("1", "1", "S", "L", "1"), raw: []string{`"description": "d"`}, absent: []string{"proveedor", "notas"}},
		{name: "header_with_bom_on_the_first_cell",
			doc:    sheet([]string{"\ufeffcategoria", "codigo", "sku", "nombre", "precio"}, "1|A", "1", "S", "L", "1"),
			stored: p7Art("1", "1", "S", "L", "1")},
		{name: "header_with_inner_space",
			doc:    sheet([]string{"cate goria", "codigo", "sku", "nombre", "precio"}, "1|A", "1", "S", "L", "1"),
			wheres: "r1/cabecera", says: []string{missing + "(categoria)"}},
		{name: "header_with_inner_nbsp",
			doc:    sheet([]string{"categoria", "codigo", "s" + p7NBSP + "ku", "nombre", "pre" + p7NBSP + "cio"}, "1|A", "1", "S", "L", "1"),
			wheres: "r1/cabecera", says: []string{missing + "(sku, precio)"}},
		{name: "header_with_zero_width_space",
			doc:    sheet([]string{"categoria", "codigo", "sku" + p7ZWSP, "nombre", "precio"}, "1|A", "1", "S", "L", "1"),
			wheres: "r1/cabecera", says: []string{missing + "(sku)"}},
		{name: "header_with_repeated_separator",
			doc:    sheet([]string{"categoria", "codigo", "sku@@", "nombre", "precio"}, "1|A", "1", "S", "L", "1"),
			wheres: "r1/cabecera", says: []string{missing + "(sku)"}},
		{name: "header_with_non_ascii_digits",
			doc:    sheet([]string{"categoria", "codigo", "sku", "nombre", "precio" + p7Arabic}, "1|A", "1", "S", "L", "1"),
			wheres: "r1/cabecera", says: []string{missing + "(precio)"}},
		{name: "repeated_column_first_one_wins",
			doc:    sheet([]string{"categoria", "codigo", "sku", "nombre", "precio", "Precio" + p7NBSP, "SKU"}, "1|A", "1", "S", "L", "1", "2", "T"),
			stored: p7Art("1", "1", "S", "L", "1")},
	}
}

// p7TabularIdentifiers son los casos del sku, los códigos, los nombres y la categoría.
func p7TabularIdentifiers(t *testing.T) []p7Adversary {
	t.Helper()
	const asCategory = ` se escribe «codigo|nombre», por ejemplo «1|Tortas».`
	return []p7Adversary{
		{name: "skus_stored_untouched_inside_trimmed_outside",
			doc: p7Sheet(t, p7Row("1|A", "", "1", "A@@B", "L", "1"), p7Row("1|A", "", "2", "A"+p7NBSP+"B", "L", "1"), p7Row("1|A", "", "3", p7Arabic, "L", "1"),
				p7Row("1|A", "", "4", p7NBSP+"PAN"+p7ZWSP+p7IDSP, p7NBSP+"Pan"+p7EMSP, "1"), p7Row("1|A", "", "5", "pan", "L", "1")),
			stored: p7Arts(p7Art("1", "1", "A@@B", "L", "1"), p7Art("1", "2", "A"+p7NBSP+"B", "L", "1"), p7Art("1", "3", p7Arabic, "L", "1"),
				p7Art("1", "4", "PAN"+p7ZWSP, "Pan", "1"), p7Art("1", "5", "pan", "L", "1"))},
		{name: "skus_differing_only_in_spaces_collide",
			doc:    p7Sheet(t, p7Row("1|A", "", "1", "PAN", "L", "1"), p7Row("1|A", "", "2", "PAN"+p7NBSP, "L", "1")),
			wheres: "r3/sku", says: []string{`el sku "PAN" ya lo usa el artículo 1 ("L") de la categoría "A"`}},
		{name: "sku_only_spaces", doc: p7Sheet(t, p7Row("1|A", "", "1", p7NBSP+p7IDSP, "L", "1")), wheres: "r2/sku", says: []string{"no tiene el sku: es obligatorio."}},
		{name: "sku_reserved_prefix_behind_a_space", doc: p7Sheet(t, p7Row("1|A", "", "1", p7NBSP+"_x", "L", "1")),
			wheres: "r2/sku", says: []string{`el sku "_x" empieza por "_", que está reservado`}},
		{name: "item_codes_differing_only_in_spaces_collide",
			doc:    p7Sheet(t, p7Row("1|A", "", "1", "S1", "L", "1"), p7Row("1|A", "", "1"+p7NBSP, "S2", "L", "1")),
			wheres: "r3/codigo", says: []string{`el código "1" ya lo usa el artículo 1 de la misma categoría`}},
		{name: "item_codes_non_ascii_are_distinct",
			doc:    p7Sheet(t, p7Row("1|A", "", "1", "S1", "L", "1"), p7Row("1|A", "", "١", "S2", "L", "1"), p7Row("1|A", "", "a@@b", "S3", "L", "1")),
			stored: p7Arts(p7Art("1", "1", "S1", "L", "1"), p7Art("1", "١", "S2", "L", "1"), p7Art("1", "a@@b", "S3", "L", "1"))},
		{name: "category_cell_with_repeated_separator",
			doc: p7Sheet(t, p7Row("1||A", "", "1", "S1", "L", "1"), p7Row("1|A|", "", "2", "S2", "L", "1"), p7Row("A", "", "3", "S3", "L", "1"),
				p7Row("|A", "", "4", "S4", "L", "1"), p7Row("", "", "5", "S5", "L", "1"), p7Row("1|"+p7NBSP, "", "6", "S6", "L", "1")),
			wheres: "r2/categoria r3/categoria r4/categoria r5/categoria r6/categoria r7/categoria",
			says: []string{`la fila 2: la categoría "1||A"` + asCategory, `la fila 3: la categoría "1|A|"` + asCategory, `la fila 4: la categoría "A"` + asCategory,
				`la fila 5: la categoría "|A"` + asCategory, "la fila 6 no dice a qué categoría pertenece"}},
		{name: "category_cell_spaces_are_trimmed_and_digits_are_not_folded",
			doc: p7Sheet(t, p7Row("1|A", "", "1", "S1", "L", "1"), p7Row(p7NBSP+"1"+p7NBSP+"|"+p7IDSP+"A"+p7EMSP, "", "2", "S2", "L", "1"),
				p7Row("١|B", "", "1", "S3", "L", "1"), p7Row("a@@b|C", "", "1", "S4", "L", "1")),
			stored: p7Arts(p7Art("1", "1", "S1", "L", "1"), p7Art("1", "2", "S2", "L", "1"), p7Art("١", "1", "S3", "L", "1"), p7Art("a@@b", "1", "S4", "L", "1"))},
		{name: "category_names_differing_in_case",
			doc:    p7Sheet(t, p7Row("1|A", "", "1", "S1", "L", "1"), p7Row("1|a", "", "2", "S2", "L", "1")),
			wheres: "r3/categoria", says: []string{`la fila 3 llama "a" a la categoría "1" y la fila 2 la llamó "A"`}},
		{name: "category_names_differing_in_inner_space",
			doc:    p7Sheet(t, p7Row("1|Pan dulce", "", "1", "S1", "L", "1"), p7Row("1|Pan"+p7NBSP+"dulce", "", "2", "S2", "L", "1")),
			wheres: "r3/categoria", says: []string{"una categoría tiene un solo nombre"}},
	}
}

// p7TabularValues son los casos del precio y de la mini-sintaxis de las celdas múltiples.
func p7TabularValues(t *testing.T) []p7Adversary {
	t.Helper()
	price := func(prices ...string) []byte {
		rows := make([][]string, len(prices))
		for i, p := range prices {
			rows[i] = p7Row("1|A", "", fmt.Sprint(i), "S"+fmt.Sprint(i), "L", p)
		}
		return p7Sheet(t, rows...)
	}
	art := func(i int, p string) string { return p7Art("1", fmt.Sprint(i), "S"+fmt.Sprint(i), "L", p) }
	return []p7Adversary{
		{name: "price_non_ascii_digits", doc: price(p7Arabic), wheres: "r2/precio",
			says: []string{`la fila 2 ("L"): el precio "` + p7Arabic + `" no es un número; escríbelo sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`}},
		{name: "price_notations_accepted", doc: price(p7NBSP+"12"+p7IDSP, "1e3", "+5", "0x10p0", ".5", "1_000", "0", "-0"),
			stored: p7Arts(art(0, "12"), art(1, "1000"), art(2, "5"), art(3, "16"), art(4, "0.5"), art(5, "1000"), art(6, "0"), art(7, "0"))},
		{name: "prices_rejected", doc: price("1"+p7NBSP+"000", "12,5", "Inf", "NaN", "0x10", "$18.000", "18.000,00", "1 000"),
			wheres: "r2/precio r3/precio r4/precio r5/precio r6/precio r7/precio r8/precio r9/precio",
			says:   []string{`el precio "12,5" no es un número`, `el precio "Inf" no es un número`, `el precio "NaN" no es un número`, `el precio "0x10" no es un número`}},
		{name: "price_negative", doc: price("-5"), wheres: "r2/precio", says: []string{"el precio no puede ser negativo."}},
		{name: "cells_with_repeated_separators_accepted",
			doc:    p7Sheet(t, p7Row("1|A", "s|Sub", "1", "S1", "L", "1", "d", "a;;b; a@@b ;", "k|v;;k2|"+p7Arabic, "V1|x|1;;V2"+p7NBSP+"|y|2", "")),
			stored: p7Art("1", "1", "S1", "L", "1"),
			raw: []string{`"tags": ["a", "b", "a@@b"]`, `"attributes": {"k": "v", "k2": "` + p7Arabic + `"}`, `"subcategory": "s"`,
				`"variants": [{"code": "V1", "label": "x", "price": 1}, {"code": "V2", "label": "y", "price": 2}]`}},
		{name: "cells_with_broken_mini_syntax",
			doc: p7Sheet(t, p7Row("1|A", "", "1", "S1", "L", "1", "", "", "", "V1|x|1; V2|y|"+p7Arabic, ""),
				p7Row("1|A", "", "2", "S2", "L", "1", "", "a|b", "k||v", "V1||1", "S1|"+p7Arabic)),
			wheres: "r2/variantes r3/tags r3/atributos r3/variantes r3/componentes",
			says: []string{`variante "V2": el precio "` + p7Arabic + `" no es un número`, `la etiqueta "a|b" lleva «|»`, `el atributo "k||v" se escribe «clave|valor»`,
				`la variante "V1||1" se escribe «codigo|nombre|precio»`, `componente "S1": la cantidad "` + p7Arabic + `" debe ser un número entero de 1 o más.`}},
		{name: "component_cells_are_trimmed",
			doc:    p7Sheet(t, p7Row("1|A", "", "1", "PAN", "L", "1"), p7Row("1|A", "", "2", "C", "L", "1", "", "", "", "", "PAN"+p7NBSP+"|1; PAN | 2")),
			stored: p7Arts(p7Art("1", "1", "PAN", "L", "1"), p7Art("1", "2", "C", "L", "1")),
			raw:    []string{`"components": [{"qty": 1, "sku": "PAN"}, {"qty": 2, "sku": "PAN"}]`}},
	}
}
