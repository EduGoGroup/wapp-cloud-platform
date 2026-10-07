package catalogimport_test

import (
	"testing"
)

// tabular_cells_test.go: la mini-sintaxis de las celdas de ParseTabular —categoría y
// subcategoría «codigo|nombre», celdas múltiples, cantidad y precio—, con el corpus
// adversario de reglas.md §5: separadores repetidos («;;», «||»), dígitos no ASCII
// en precios y cantidades. Parte de los tests de tabular.go (E-13); gemelo de
// tabular_cells.go. Los ayudantes (sheet, row, assertRowDefects…) están en
// tabular_test.go.

// itemRow es una fila válida de la categoría «1|Tortas» (sku A, «Art A», precio 1)
// con las cinco celdas opcionales dadas: descripcion, tags, atributos, variantes,
// componentes.
func itemRow(optional ...string) []string {
	return row(append([]string{"1|Tortas", "", "1", "A", "Art A", "1"}, optional...)...)
}

const (
	rowA         = `la fila 2 ("Art A")`
	priceExample = `; escríbelo sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`
)

// TestParseTabular_ItemMiniSyntax: lo que la plantilla EMITE en una celda vuelve a
// ser lo que era: subcategoría, etiquetas, atributos y variantes. La igualdad con el
// JSON ya lo prueba en bloque; esto dice CUÁL se rompió.
func TestParseTabular_ItemMiniSyntax(t *testing.T) {
	doc := mustParseSheet(t, sheet(
		row("1|Tortas", "clasicas|Clásicas", "1", "TORTA-CHOC", "Torta de chocolate", "18000",
			"Con arequipe", "decorada; sin_lactosa", "capas|3; porciones|10-12",
			"V1|10-12 porciones|18000; V2|25-30 porciones|32000", ""),
	))

	const want = `{"categories":[{"code":"1","label":"Tortas","subcategories":[{"code":"clasicas","label":"Clásicas"}],"items":[` +
		`{"code":"1","sku":"TORTA-CHOC","label":"Torta de chocolate","price":18000,"description":"Con arequipe","subcategory":"clasicas",` +
		`"tags":["decorada","sin_lactosa"],"attributes":{"capas":"3","porciones":"10-12"},` +
		`"variants":[{"code":"V1","label":"10-12 porciones","price":18000},{"code":"V2","label":"25-30 porciones","price":32000}]}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_ComboMiniSyntax: la cantidad de un componente se lee de la celda
// y no se da por supuesta. El «|1» explícito del primer integrante es el que enseña
// a escribir el «|2» del segundo.
func TestParseTabular_ComboMiniSyntax(t *testing.T) {
	doc := mustParseSheet(t, sheet(
		row("1|Salados", "", "1", "TEQUENOS-15", "Tequeños", "26000"),
		row("1|Salados", "", "2", "REFRESCO", "Refresco", "4000"),
		row("1|Salados", "", "3", "COMBO", "Combo fiesta", "32000", "", "", "", "", "TEQUENOS-15|1; REFRESCO|2"),
	))

	const want = `[{"sku":"TEQUENOS-15","qty":1},{"sku":"REFRESCO","qty":2}]`
	if got := compactJSON(t, doc.Catalog.Categories[0].Items[2].Components); got != want {
		t.Errorf("componentes = %s; se esperaba %s", got, want)
	}
}

// TestParseTabular_CategoryCell: la categoría se escribe «codigo|nombre», los dos
// obligatorios y nada más. Un «|» repetido o de más rompe la celda (corpus
// adversario): no se adivina cuál de los trozos era el nombre.
func TestParseTabular_CategoryCell(t *testing.T) {
	const example = " se escribe «codigo|nombre», por ejemplo «1|Tortas»."
	cases := map[string]struct{ cell, reason string }{
		"empty":             {"", "la fila 2 no dice a qué categoría pertenece: escribe su código y su nombre separados por «|», por ejemplo «1|Tortas»."},
		"only a name":       {"Tortas", `la fila 2: la categoría "Tortas"` + example},
		"repeated pipe":     {"1||Tortas", `la fila 2: la categoría "1||Tortas"` + example},
		"trailing pipe":     {"1|Tortas|", `la fila 2: la categoría "1|Tortas|"` + example},
		"missing the code":  {"|Tortas", `la fila 2: la categoría "|Tortas"` + example},
		"missing the label": {"1|", `la fila 2: la categoría "1|"` + example},
		"blank label":       {"1| ", `la fila 2: la categoría "1|"` + example},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := parseSheet(sheet(row(c.cell, "", "1", "A", "Art A", "1")))
			assertRowDefects(t, verr, rowDefect{2, "categoria", c.reason})
		})
	}
}

// TestParseTabular_OneCategoryOneName: el código de la categoría es lo que el
// cliente teclea en WhatsApp. Dos filas que le dan nombres distintos al MISMO código
// son un error de quien llena la hoja, y callarlo dejaría el nombre a merced del
// orden de las filas. El defecto es de la segunda y cita la fila de la primera.
func TestParseTabular_OneCategoryOneName(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("1|Pasteles", "", "2", "TORTA-UNI", "Torta de unicornio", "26000"),
	))
	assertRowDefects(t, verr, rowDefect{3, "categoria",
		`la fila 3 llama "Pasteles" a la categoría "1" y la fila 2 la llamó "Tortas": una categoría tiene un solo nombre, escríbelo igual en todas sus filas.`})
}

// TestParseTabular_SubcategoryCell: opcional; si se escribe, es «codigo|nombre» y
// queda declarada en SU categoría por el hecho de escribirla (el mismo código puede
// nombrar otra cosa en otra categoría). Dentro de una categoría tiene un solo
// nombre.
func TestParseTabular_SubcategoryCell(t *testing.T) {
	doc := mustParseSheet(t, sheet(
		row("1|Tortas", "s|Sub", "1", "A", "Art A", "1"),
		row("2|Otras", "s|Otra", "1", "B", "Art B", "1"),
		row("1|Tortas", "s|Sub", "2", "C", "Art C", "1"),
		row("1|Tortas", "t|Segunda", "3", "D", "Art D", "1"),
	))
	const want = `{"categories":[{"code":"1","label":"Tortas","subcategories":[{"code":"s","label":"Sub"},{"code":"t","label":"Segunda"}],"items":[` +
		`{"code":"1","sku":"A","label":"Art A","price":1,"subcategory":"s"},{"code":"2","sku":"C","label":"Art C","price":1,"subcategory":"s"},` +
		`{"code":"3","sku":"D","label":"Art D","price":1,"subcategory":"t"}]},` +
		`{"code":"2","label":"Otras","subcategories":[{"code":"s","label":"Otra"}],"items":[{"code":"1","sku":"B","label":"Art B","price":1,"subcategory":"s"}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}

	_, verr := parseSheet(sheet(
		row("1|Tortas", "a||A", "1", "A", "Art A", "1"),
		row("1|Tortas", "a", "2", "B", "Art B", "1"),
		row("1|Tortas", "s|", "3", "C", "Art C", "1"),
	))
	assertRowDefects(t, verr,
		rowDefect{2, "subcategoria", `la fila 2: la subcategoría "a||A" se escribe «codigo|nombre», por ejemplo «1|Tortas».`},
		rowDefect{3, "subcategoria", `la fila 3: la subcategoría "a" se escribe «codigo|nombre», por ejemplo «1|Tortas».`},
		rowDefect{4, "subcategoria", `la fila 4: la subcategoría "s|" se escribe «codigo|nombre», por ejemplo «1|Tortas».`},
	)

	_, verr = parseSheet(sheet(
		row("1|Tortas", "a|Uno", "1", "A", "Art A", "1"),
		row("1|Tortas", "a|Dos", "2", "B", "Art B", "1"),
	))
	assertRowDefects(t, verr, rowDefect{3, "subcategoria",
		`la fila 3 llama "Dos" a la subcategoría "a" y la fila 2 la llamó "Uno": una subcategoría tiene un solo nombre, escríbelo igual en todas sus filas.`})
}

// TestParseTabular_RepeatedEntrySeparatorsAreASlip (corpus adversario): un «;» de
// más —al final, repetido o con espacios en medio— es un desliz de quien escribe,
// no un defecto del catálogo: las entradas vacías se descartan, y una celda que
// solo trae separadores es una celda vacía.
func TestParseTabular_RepeatedEntrySeparatorsAreASlip(t *testing.T) {
	doc := mustParseSheet(t, sheet(
		itemRow("", "a;;b; ;c;", "k|v;;", "V1|x|1;;V2|y|2", ""),
		row("1|Tortas", "", "2", "B", "Art B", "1", "", ";;", ";", "; ;", ";;"),
		row("1|Tortas", "", "3", "C", "Art C", "1", "", "", "", "", ";A|2;;A|1;"),
	))

	const want = `{"categories":[{"code":"1","label":"Tortas","items":[` +
		`{"code":"1","sku":"A","label":"Art A","price":1,"tags":["a","b","c"],"attributes":{"k":"v"},` +
		`"variants":[{"code":"V1","label":"x","price":1},{"code":"V2","label":"y","price":2}]},` +
		`{"code":"2","sku":"B","label":"Art B","price":1},` +
		`{"code":"3","sku":"C","label":"Art C","price":1,"components":[{"sku":"A","qty":2},{"sku":"A","qty":1}]}]}]}`
	if got := catalogJSON(t, doc); got != want {
		t.Errorf("catálogo = %s\nse esperaba %s", got, want)
	}
}

// TestParseTabular_TagsCell: una etiqueta es un solo campo; con «|» —también
// repetido— se rechaza la etiqueta y las demás se siguen leyendo.
func TestParseTabular_TagsCell(t *testing.T) {
	_, verr := parseSheet(sheet(itemRow("", "a||b; c; d|e")))
	const tail = ` lleva «|»; las etiquetas se escriben una detrás de otra separadas por «;», por ejemplo «decorada; sin_lactosa».`
	assertRowDefects(t, verr,
		rowDefect{2, "tags", rowA + `: la etiqueta "a||b"` + tail},
		rowDefect{2, "tags", rowA + `: la etiqueta "d|e"` + tail},
	)
}

// TestParseTabular_AttributesCell: «clave|valor», los dos obligatorios y nada más.
func TestParseTabular_AttributesCell(t *testing.T) {
	_, verr := parseSheet(sheet(itemRow("", "", "k||v; solo; |v; k|; a|1")))
	const example = ` se escribe «clave|valor», por ejemplo «porciones|10-12».`
	assertRowDefects(t, verr,
		rowDefect{2, "atributos", rowA + `: el atributo "k||v"` + example},
		rowDefect{2, "atributos", rowA + `: el atributo "solo"` + example},
		rowDefect{2, "atributos", rowA + `: el atributo "|v"` + example},
		rowDefect{2, "atributos", rowA + `: el atributo "k|"` + example},
	)

	// Una clave repetida no es un defecto: se queda con el último valor.
	doc := mustParseSheet(t, sheet(itemRow("", "", "a|1; b|x; a|2")))
	if got, want := compactJSON(t, doc.Catalog.Categories[0].Items[0].Attributes), `{"a":"2","b":"x"}`; got != want {
		t.Errorf("atributos = %s; se esperaba %s", got, want)
	}
}

// TestParseTabular_VariantsCell: «codigo|nombre|precio». Sin código o sin nombre —o
// con otro número de campos— la entrada no se entiende; con el precio vacío se
// entiende y lo que falta es el precio.
func TestParseTabular_VariantsCell(t *testing.T) {
	_, verr := parseSheet(sheet(itemRow("", "", "", "V1|grande; V2||1; |y|2; V3|z|; V4|a|b|c")))
	const example = ` se escribe «codigo|nombre|precio», por ejemplo «V1|10-12 porciones|18000».`
	assertRowDefects(t, verr,
		rowDefect{2, "variantes", rowA + `: la variante "V1|grande"` + example},
		rowDefect{2, "variantes", rowA + `: la variante "V2||1"` + example},
		rowDefect{2, "variantes", rowA + `: la variante "|y|2"` + example},
		rowDefect{2, "variantes", rowA + `, variante "V3" no tiene el precio: es obligatorio.`},
		rowDefect{2, "variantes", rowA + `: la variante "V4|a|b|c"` + example},
	)
}

// TestParseTabular_ComponentsCell: «sku|cantidad», con la cantidad SIEMPRE escrita.
// Aceptar además el sku a secas sería tener dos gramáticas para lo mismo: la que
// emitimos y otra que solo conoce el parser.
func TestParseTabular_ComponentsCell(t *testing.T) {
	_, verr := parseSheet(sheet(
		itemRow(),
		row("1|Tortas", "", "2", "B", "Art B", "1", "", "", "", "", "A; A||2; |2; A|"),
	))
	const example = ` se escribe «sku|cantidad», por ejemplo «TEQUENOS-15|1».`
	assertRowDefects(t, verr,
		rowDefect{3, "componentes", `la fila 3 ("Art B"): el componente "A"` + example},
		rowDefect{3, "componentes", `la fila 3 ("Art B"): el componente "A||2"` + example},
		rowDefect{3, "componentes", `la fila 3 ("Art B"): el componente "|2"` + example},
		rowDefect{3, "componentes", `la fila 3 ("Art B"), componente "A": la cantidad "" debe ser un número entero de 1 o más.`},
	)
}

// TestParseTabular_ComponentQuantity (corpus adversario): la cantidad es un entero
// de 1 o más escrito con dígitos ASCII. Los dígitos de otro alfabeto, los decimales
// y el cero se rechazan citando lo que se escribió; un signo «+» o unos ceros
// delante son el mismo número.
func TestParseTabular_ComponentQuantity(t *testing.T) {
	const subject = `la fila 3 ("Art B"), componente "A": la cantidad `
	const tail = ` debe ser un número entero de 1 o más.`
	_, verr := parseSheet(sheet(
		itemRow(),
		row("1|Tortas", "", "2", "B", "Art B", "1", "", "", "", "", "A|٢; A|２; A|0; A|1.0; A|-1; A|dos"),
	))
	assertRowDefects(t, verr,
		rowDefect{3, "componentes", subject + `"٢"` + tail},
		rowDefect{3, "componentes", subject + `"２"` + tail},
		rowDefect{3, "componentes", subject + `"0"` + tail},
		rowDefect{3, "componentes", subject + `"1.0"` + tail},
		rowDefect{3, "componentes", subject + `"-1"` + tail},
		rowDefect{3, "componentes", subject + `"dos"` + tail},
	)

	doc := mustParseSheet(t, sheet(
		itemRow(),
		row("1|Tortas", "", "2", "B", "Art B", "1", "", "", "", "", "A|+2; A|007"),
	))
	if got, want := compactJSON(t, doc.Catalog.Categories[0].Items[1].Components), `[{"sku":"A","qty":2},{"sku":"A","qty":7}]`; got != want {
		t.Errorf("componentes = %s; se esperaba %s", got, want)
	}
}

// TestParseTabular_UnreadablePriceNamesTheRowAndTheReason: el literal va escrito
// entero a propósito: lo lee el dueño del negocio, y es la clase de texto que se
// degrada sin que ningún test lo note.
func TestParseTabular_UnreadablePriceNamesTheRowAndTheReason(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "$18.000"),
	))
	assertRowDefects(t, verr, rowDefect{3, "precio",
		`la fila 3 ("Torta de unicornio"): el precio "$18.000" no es un número; escríbelo sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`})
}

// TestParseTabular_EmptyPriceIsNotImportedForFree: una celda de precio en blanco no
// puede colarse como 0, que es un precio válido: el artículo se importaría
// regalado.
func TestParseTabular_EmptyPriceIsNotImportedForFree(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", ""),
		row("1|Tortas", "", "2", "", "", " "),
	))
	assertRowDefects(t, verr,
		rowDefect{2, "precio", `la fila 2 ("Torta de chocolate") no tiene el precio: es obligatorio.`},
		rowDefect{3, "precio", `la fila 3 no tiene el precio: es obligatorio.`},
	)
}

// TestParseTabular_PricesThatAreNotNumbers (corpus adversario): los dígitos no
// ASCII —lo que pega quien copia de una hoja en otro alfabeto— no son un número, ni
// lo son NaN, el infinito, el desbordamiento ni el separador de miles.
func TestParseTabular_PricesThatAreNotNumbers(t *testing.T) {
	for _, price := range []string{"١٨٠٠٠", "１８０００", "NaN", "Inf", "-inf", "1e999", "18.000,50", "18 000", "dieciocho mil"} {
		t.Run(price, func(t *testing.T) {
			_, verr := parseSheet(sheet(row("1|Tortas", "", "1", "A", "Art A", price)))
			assertRowDefects(t, verr, rowDefect{2, "precio", rowA + `: el precio "` + price + `" no es un número` + priceExample})
		})
	}

	// El precio de una variante se juzga igual, y se ubica en la celda de variantes.
	_, verr := parseSheet(sheet(itemRow("", "", "", "V1|x|١٨")))
	assertRowDefects(t, verr, rowDefect{2, "variantes", rowA + `, variante "V1": el precio "١٨" no es un número` + priceExample})
}

// TestParseTabular_PricesReadAsStrconvReadsThem (corpus adversario): lo que
// strconv.ParseFloat lee es un precio, también lo que nadie espera en una celda: la
// notación exponencial, el hexadecimal de Go y el guion bajo entre cifras. Es lo
// que hace el parser viejo y no se «arregla» aquí.
func TestParseTabular_PricesReadAsStrconvReadsThem(t *testing.T) {
	cases := []struct {
		cell string
		want float64
	}{
		{"0", 0},
		{"2.50", 2.5},
		{".5", 0.5},
		{"+5", 5},
		{"1e3", 1000},
		{"0x1p4", 16},
		{"1_000", 1000},
		{" 18000 ", 18000},
	}
	for _, c := range cases {
		t.Run(c.cell, func(t *testing.T) {
			doc := mustParseSheet(t, sheet(row("1|Tortas", "", "1", "A", "Art A", c.cell)))
			if got := doc.Catalog.Categories[0].Items[0].Price; got != c.want {
				t.Errorf("precio %q → %v; se esperaba %v", c.cell, got, c.want)
			}
		})
	}
}

// TestParseTabular_BadlyWrittenCellsAreAllReportedInOnePass: cada gramática rota se
// cuenta donde está y con un ejemplo de cómo se escribe —sin ejemplo, «formato
// inválido» deja a quien llenó la hoja igual de perdido—. Dentro de una fila salen
// en el orden de las columnas del artículo, con el precio primero.
func TestParseTabular_BadlyWrittenCellsAreAllReportedInOnePass(t *testing.T) {
	_, verr := parseSheet(sheet(
		row("Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
		row("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000", "", "", "", "V1|grande", ""),
		row("2|Salados", "x", "2", "COMBO", "Combo", "abc", "", "a|b", "k", "V1", "TEQUENOS"),
	))
	assertRowDefects(t, verr,
		rowDefect{2, "categoria", `la fila 2: la categoría "Tortas" se escribe «codigo|nombre», por ejemplo «1|Tortas».`},
		rowDefect{3, "variantes", `la fila 3 ("Tequeños"): la variante "V1|grande" se escribe «codigo|nombre|precio», por ejemplo «V1|10-12 porciones|18000».`},
		rowDefect{4, "precio", `la fila 4 ("Combo"): el precio "abc" no es un número` + priceExample},
		rowDefect{4, "subcategoria", `la fila 4: la subcategoría "x" se escribe «codigo|nombre», por ejemplo «1|Tortas».`},
		rowDefect{4, "tags", `la fila 4 ("Combo"): la etiqueta "a|b" lleva «|»; las etiquetas se escriben una detrás de otra separadas por «;», por ejemplo «decorada; sin_lactosa».`},
		rowDefect{4, "atributos", `la fila 4 ("Combo"): el atributo "k" se escribe «clave|valor», por ejemplo «porciones|10-12».`},
		rowDefect{4, "variantes", `la fila 4 ("Combo"): la variante "V1" se escribe «codigo|nombre|precio», por ejemplo «V1|10-12 porciones|18000».`},
		rowDefect{4, "componentes", `la fila 4 ("Combo"): el componente "TEQUENOS" se escribe «sku|cantidad», por ejemplo «TEQUENOS-15|1».`},
	)
}
