package catalogimport

// Parte de los tests de tabular_locate.go: los auxiliares tabularColumn y rowOf. Van
// en el paquete, no en catalogimport_test, porque llevan una regla que ParseTabular
// NO deja ver desde fuera: adónde va un defecto de la CATEGORÍA entera. La lectura
// de la planilla garantiza categorías con código, nombre y al menos un artículo, y
// subcategorías con código y nombre únicos, así que hoy el validador no puede
// devolver ninguno por este camino; la rama existe para el día en que una regla
// nueva del validador sí lo haga. Nacen en el verde (05 E-4).

import "testing"

// TestTabularColumn_TranslatesTheContractFieldToTheSheetColumn: el campo del
// contrato (inglés, como en el JSON) pasa a la columna que el dueño tiene delante.
// Un defecto de la categoría señala SIEMPRE `categoria` aunque el validador lo
// llame "label" o "code" —las columnas `nombre` y `codigo` son las del artículo—,
// salvo que hable de sus subcategorías. Lo que no tiene columna se devuelve tal
// cual.
func TestTabularColumn_TranslatesTheContractFieldToTheSheetColumn(t *testing.T) {
	category, item := 0, 0
	cases := map[string]struct {
		in   ImportFieldError
		want string
	}{
		"item label":             {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "label"}, "nombre"},
		"item code":              {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "code"}, "codigo"},
		"item sku":               {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "sku"}, "sku"},
		"item price":             {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "price"}, "precio"},
		"item description":       {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "description"}, "descripcion"},
		"item subcategory":       {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "subcategory"}, "subcategoria"},
		"one tag":                {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "tags[1]"}, "tags"},
		"one attribute":          {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: `attributes["k"]`}, "atributos"},
		"a variant field":        {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "variants[1].price"}, "variantes"},
		"a component field":      {ImportFieldError{CategoryIndex: &category, ItemIndex: &item, Field: "components[0].qty"}, "componentes"},
		"category label":         {ImportFieldError{CategoryIndex: &category, Field: "label"}, "categoria"},
		"category code":          {ImportFieldError{CategoryIndex: &category, Field: "code"}, "categoria"},
		"category items":         {ImportFieldError{CategoryIndex: &category, Field: "items"}, "categoria"},
		"category subcategories": {ImportFieldError{CategoryIndex: &category, Field: "subcategories[0].code"}, "subcategoria"},
		"header field":           {ImportFieldError{Field: "categories"}, "categories"},
		"summary":                {ImportFieldError{Field: "(varios)"}, "(varios)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tabularColumn(c.in); got != c.want {
				t.Errorf("tabularColumn(%q) = %q; se esperaba %q", c.in.Field, got, c.want)
			}
		})
	}
}

// TestTabularRowOf_MapsIndexesBackToTheSheetRow: (categoría, artículo) vuelve a la
// fila que lo produjo; un defecto de la categoría entera, a la PRIMERA fila que la
// nombró; y lo que no es de ninguna categoría —o apunta fuera de lo leído—, a la
// fila 0 o a la de su categoría, nunca a un pánico.
func TestTabularRowOf_MapsIndexesBackToTheSheetRow(t *testing.T) {
	p := &tabular{order: []*tabularCategory{
		{code: "1", row: 2, itemRows: []int{2, 5}},
		{code: "2", row: 3, itemRows: []int{3}},
	}}
	at := func(i int) *int { return &i }
	cases := map[string]struct {
		category, item *int
		want           int
	}{
		"second item of the first category": {at(0), at(1), 5},
		"only item of the second category":  {at(1), at(0), 3},
		"the first category itself":         {at(0), nil, 2},
		"the second category itself":        {at(1), nil, 3},
		"no category":                       {nil, nil, 0},
		"category out of range":             {at(2), at(0), 0},
		"item out of range":                 {at(1), at(4), 3},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := p.rowOf(c.category, c.item); got != c.want {
				t.Errorf("rowOf = %d; se esperaba %d", got, c.want)
			}
		})
	}
}
