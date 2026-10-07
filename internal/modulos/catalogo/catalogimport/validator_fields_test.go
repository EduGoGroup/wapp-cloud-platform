//go:build pendiente

package catalogimport_test

import (
	"testing"
)

// validator_fields_test.go: lo que Validate exige de cada CLASE de campo —texto,
// precio, cantidad y atributos—, con los dígitos no ASCII del corpus adversario
// (reglas.md §5). Parte de los tests de validator.go (E-13); gemelo de
// validator_fields.go.

const priceNotANumber = `: el precio debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`

// TestValidate_TextFieldsMustBeQuoted: un texto que llega con otro tipo se dice con
// el nombre humano del campo, no con el del JSON.
func TestValidate_TextFieldsMustBeQuoted(t *testing.T) {
	cases := map[string]struct {
		item string
		want defect
	}{
		"code as number": {`{"code":1,"sku":"CAFE","label":"Café","price":100}`, atItem(0, 0, "code",
			coffeeSubject+": el código debe ir entre comillas (es un texto, no un número ni una lista).")},
		"sku as list": {`{"code":"1","sku":["CAFE"],"label":"Café","price":100}`, atItem(0, 0, "sku",
			coffeeSubject+": el sku debe ir entre comillas (es un texto, no un número ni una lista).")},
		"description as number": {coffee(`,"description":5`), atItem(0, 0, "description",
			coffeeSubject+": la descripción debe ir entre comillas (es un texto, no un número ni una lista).")},
		"subcategory as number": {coffee(`,"subcategory":5`), atItem(0, 0, "subcategory",
			coffeeSubject+": la subcategoría debe ir entre comillas (es un texto, no un número ni una lista).")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(c.item))
			assertDefects(t, verr, c.want)
		})
	}
}

// TestValidate_DescriptionIsKeptVerbatim: la descripción es opcional y no se
// recorta.
func TestValidate_DescriptionIsKeptVerbatim(t *testing.T) {
	doc := mustValidate(t, docOfItems(coffee(`,"description":" Recién molido. "`)))
	if got := doc.Catalog.Categories[0].Items[0].Description; got != " Recién molido. " {
		t.Errorf("description = %q; se esperaba tal cual llegó", got)
	}
}

// TestValidate_PriceMustBeANonNegativeJSONNumber: el precio es un número JSON. Un
// texto no vale aunque sean dígitos —tampoco los dígitos no ASCII, que es lo que
// pega quien copia de una hoja en otro alfabeto—, y null es «no vino».
func TestValidate_PriceMustBeANonNegativeJSONNumber(t *testing.T) {
	cases := map[string]struct {
		price string
		want  string
	}{
		"as text":               {`"18000"`, coffeeSubject + priceNotANumber},
		"with currency":         {`"$18.000"`, coffeeSubject + priceNotANumber},
		"arabic-indic digits":   {`"١٨٠٠٠"`, coffeeSubject + priceNotANumber},
		"fullwidth digits":      {`"１８０００"`, coffeeSubject + priceNotANumber},
		"a list":                {`[18000]`, coffeeSubject + priceNotANumber},
		"negative":              {`-5`, coffeeSubject + ": el precio no puede ser negativo."},
		"negative but tiny":     {`-0.01`, coffeeSubject + ": el precio no puede ser negativo."},
		"null is a missing one": {`null`, coffeeSubject + " no tiene el precio: es obligatorio."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(`{"code":"1","sku":"CAFE","label":"Café","price":` + c.price + `}`))
			assertDefects(t, verr, atItem(0, 0, "price", c.want))
		})
	}
}

// TestValidate_PriceAcceptsAnyJSONNumber: cero es un precio (lo gratis existe), y la
// notación exponencial es un número JSON como otro.
func TestValidate_PriceAcceptsAnyJSONNumber(t *testing.T) {
	cases := map[string]struct {
		price string
		want  float64
	}{
		"zero":     {`0`, 0},
		"decimal":  {`2.5`, 2.5},
		"exponent": {`1.8e4`, 18000},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustValidate(t, docOfItems(`{"code":"1","sku":"CAFE","label":"Café","price":`+c.price+`}`))
			if got := doc.Catalog.Categories[0].Items[0].Price; got != c.want {
				t.Errorf("price = %v; se esperaba %v", got, c.want)
			}
		})
	}
}

// TestValidate_ComponentQtyMustBeAWholeNumberFromOne: media unidad de un integrante
// de combo no significa nada. El motivo cita la cantidad cuando es un número, y no
// cuando ni siquiera lo es.
func TestValidate_ComponentQtyMustBeAWholeNumberFromOne(t *testing.T) {
	const subject = coffeeSubject + ", componente 1: "
	cases := map[string]struct {
		qty  string
		want string
	}{
		"fraction":            {`1.5`, subject + "la cantidad es 1.5 y debe ser un número entero de 1 o más."},
		"zero":                {`0`, subject + "la cantidad es 0 y debe ser un número entero de 1 o más."},
		"negative":            {`-1`, subject + "la cantidad es -1 y debe ser un número entero de 1 o más."},
		"as text":             {`"2"`, subject + "la cantidad debe ser un número entero de 1 o más."},
		"arabic-indic digits": {`"٢"`, subject + "la cantidad debe ser un número entero de 1 o más."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(coffee(`,"components":[{"sku":"CAFE","qty":` + c.qty + `}]`)))
			assertDefects(t, verr, atItem(0, 0, "components[0].qty", c.want))
		})
	}
}

// TestValidate_ComponentQtyDefaultsToOne: ausente —o null— vale 1, lo mismo que
// asume el runtime; un entero escrito con decimales o con exponente sigue siendo
// entero.
func TestValidate_ComponentQtyDefaultsToOne(t *testing.T) {
	cases := map[string]struct {
		component string
		want      int
	}{
		"absent":   {`{"sku":"CAFE"}`, 1},
		"null":     {`{"sku":"CAFE","qty":null}`, 1},
		"explicit": {`{"sku":"CAFE","qty":3}`, 3},
		"2.0":      {`{"sku":"CAFE","qty":2.0}`, 2},
		"1e2":      {`{"sku":"CAFE","qty":1e2}`, 100},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustValidate(t, docOfItems(coffee(`,"components":[`+c.component+`]`)))
			if got := doc.Catalog.Categories[0].Items[0].Components[0].Qty; got != c.want {
				t.Errorf("qty = %d; se esperaba %d", got, c.want)
			}
		})
	}
}

// TestValidate_AttributesAreScalarsStoredAsText: se aceptan textos, números y
// booleanos y se guardan como texto —exactamente lo que hace el runtime: el
// validador no puede ser más estricto que el motor sin rechazar catálogos que
// funcionarían—.
func TestValidate_AttributesAreScalarsStoredAsText(t *testing.T) {
	doc := mustValidate(t, docOfItems(coffee(`,"attributes":{"porciones":"10-12","capas":3,"peso":1.50,"vegano":true}`)))

	const want = `{"capas":"3","peso":"1.5","porciones":"10-12","vegano":"true"}`
	if got := compactJSON(t, doc.Catalog.Categories[0].Items[0].Attributes); got != want {
		t.Errorf("attributes = %s; se esperaba %s", got, want)
	}
	if cat := runtimeCatalog(t, doc.Catalog); len(cat.Warnings) != 0 {
		t.Errorf("el runtime avisa de unos atributos que el import aceptó: %+v", cat.Warnings)
	}

	empty := mustValidate(t, docOfItems(coffee(`,"attributes":{}`)))
	if attrs := empty.Catalog.Categories[0].Items[0].Attributes; attrs != nil {
		t.Errorf("un objeto de atributos vacío no viaja: %+v", attrs)
	}
}

// TestValidate_AttributeRules: ni listas, ni objetos, ni null, ni claves en blanco.
// Los defectos salen ordenados por clave: la lista no baila entre corridas.
func TestValidate_AttributeRules(t *testing.T) {
	_, verr := validate(docOfItems(coffee(`,"attributes":["origen"]`)))
	assertDefects(t, verr, atItem(0, 0, "attributes",
		coffeeSubject+`: los atributos deben ser un objeto de pares clave→valor, por ejemplo {"porciones": "10-12"}.`))

	_, verr = validate(docOfItems(coffee(`,"attributes":{"origen":["co","br"],"ok":"x","ficha":{"a":1},"nulo":null," ":"sin nombre"}`)))
	assertDefects(t, verr,
		atItem(0, 0, "attributes", coffeeSubject+": hay un atributo sin nombre."),
		atItem(0, 0, `attributes["ficha"]`, coffeeSubject+`: el atributo "ficha" debe ser un texto o un número, no una lista ni otro objeto.`),
		atItem(0, 0, `attributes["nulo"]`, coffeeSubject+`: el atributo "nulo" debe ser un texto o un número, no una lista ni otro objeto.`),
		atItem(0, 0, `attributes["origen"]`, coffeeSubject+`: el atributo "origen" debe ser un texto o un número, no una lista ni otro objeto.`),
	)
}
