package catalogimport_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// validator_catalog_test.go: las reglas de Validate sobre el cuerpo del documento
// —el bloque "catalog", sus categorías y subcategorías— y el tope de artículos.
// Parte de los tests de validator.go (E-13); gemelo de validator_catalog.go.

// TestValidate_BodyShape: "source" es informativa pero tiene forma, y sin "catalog"
// con al menos una categoría no hay nada que importar.
func TestValidate_BodyShape(t *testing.T) {
	const head = `{"format":"wapp.catalog_import","version":1`
	const noCategories = "el catálogo no trae ninguna categoría: agrega al menos una con sus artículos."
	cases := map[string]struct {
		doc  string
		want defect
	}{
		"source is a text": {head + `,"source":"llm","catalog":{"categories":[{"code":"1","label":"B","items":[` + coffee("") + `]}]}}`,
			atHeader("source", `el bloque "source" debe ser un objeto con kind, model y hint (textos), o no venir.`)},
		"missing catalog": {head + `}`,
			atHeader("catalog", `el documento no trae catálogo: falta el bloque "catalog" con sus categorías.`)},
		"catalog is a list": {head + `,"catalog":[]}`,
			atHeader("catalog", `el bloque "catalog" debe ser un objeto con la lista de categorías dentro.`)},
		"categories is a text": {head + `,"catalog":{"categories":"x"}}`,
			atHeader("categories", `"categories" debe ser una lista de categorías, cada una con su código, su nombre y sus artículos.`)},
		"missing categories": {head + `,"catalog":{}}`, atHeader("categories", noCategories)},
		"null categories":    {head + `,"catalog":{"categories":null}}`, atHeader("categories", noCategories)},
		"empty categories":   {head + `,"catalog":{"categories":[]}}`, atHeader("categories", noCategories)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(c.doc)
			assertDefects(t, verr, c.want)
		})
	}
}

// TestValidate_SourceIsKeptAsDeclared: la procedencia no se interpreta; se devuelve.
func TestValidate_SourceIsKeptAsDeclared(t *testing.T) {
	doc := mustValidate(t, `{"format":"wapp.catalog_import","version":1,"source":{"kind":"manual"},"catalog":{"categories":[`+
		`{"code":"1","label":"Bebidas","items":[`+coffee("")+`]}]}}`)
	if doc.Source == nil || *doc.Source != (catalogimport.ImportSource{Kind: "manual"}) {
		t.Errorf("source = %+v; se esperaba {Kind: manual}", doc.Source)
	}

	without := mustValidate(t, docOfItems(coffee("")))
	if without.Source != nil {
		t.Errorf("sin \"source\" en el documento no se inventa una: %+v", without.Source)
	}
}

// TestValidate_CategoryRules: nombre, código y artículos son obligatorios, y la
// categoría se nombra en el motivo por lo mejor que tenga: su nombre, su código o su
// posición.
func TestValidate_CategoryRules(t *testing.T) {
	item := coffee("")
	cases := map[string]struct {
		category string
		want     []defect
	}{
		"nothing but items": {`{"items":[` + item + `]}`, []defect{
			atCategory(0, "label", "la categoría 1 no tiene nombre: es lo que se le muestra al cliente al elegir."),
			atCategory(0, "code", "la categoría 1 no tiene código: es lo que teclea el cliente para entrar en ella."),
		}},
		"only a code": {`{"code":"7","items":[` + item + `]}`, []defect{
			atCategory(0, "label", `la categoría con código "7" no tiene nombre: es lo que se le muestra al cliente al elegir.`),
		}},
		"only a label": {`{"label":"Bebidas","items":[` + item + `]}`, []defect{
			atCategory(0, "code", `la categoría "Bebidas" no tiene código: es lo que teclea el cliente para entrar en ella.`),
		}},
		// El tipo equivocado se dice con la posición (aún no hay nombre) y, además,
		// la categoría se queda sin nombre.
		"label is a number": {`{"code":"7","label":5,"items":[` + item + `]}`, []defect{
			atCategory(0, "label", "la categoría 1: el nombre debe ir entre comillas (es un texto, no un número ni una lista)."),
			atCategory(0, "label", `la categoría con código "7" no tiene nombre: es lo que se le muestra al cliente al elegir.`),
		}},
		"label of unicode spaces only": {`{"code":"1","label":"` + ideographicSpace + nbsp + `","items":[` + item + `]}`, []defect{
			atCategory(0, "label", `la categoría con código "1" no tiene nombre: es lo que se le muestra al cliente al elegir.`),
		}},
		"empty items": {`{"code":"1","label":"Bebidas","items":[]}`, []defect{
			atCategory(0, "items", `la categoría "Bebidas" no tiene artículos: una categoría vacía deja al cliente en un callejón sin salida.`),
		}},
		"missing items": {`{"code":"1","label":"Bebidas"}`, []defect{
			atCategory(0, "items", `la categoría "Bebidas" no tiene artículos: una categoría vacía deja al cliente en un callejón sin salida.`),
		}},
		"items is an object": {`{"code":"1","label":"Bebidas","items":{"code":"1"}}`, []defect{
			atCategory(0, "items", `la categoría "Bebidas": "items" debe ser una lista de artículos.`),
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOf(c.category))
			assertDefects(t, verr, c.want...)
		})
	}
}

// TestValidate_CategoryLabelAndCodeAreTrimmed: nombre y código de la categoría se
// guardan sin espacios alrededor, también los Unicode. El código del ARTÍCULO, en
// cambio, viaja tal cual llegó (ver validator_item_test.go).
func TestValidate_CategoryLabelAndCodeAreTrimmed(t *testing.T) {
	doc := mustValidate(t, docOf(`{"code":"`+nbsp+`1`+nbsp+`","label":"`+ideographicSpace+`Bebidas`+ideographicSpace+
		`","items":[`+coffee("")+`]}`))

	cat := doc.Catalog.Categories[0]
	if cat.Code != "1" || cat.Label != "Bebidas" {
		t.Errorf("categoría = {code %q, label %q}; se esperaba {\"1\", \"Bebidas\"}", cat.Code, cat.Label)
	}
}

// TestValidate_RepeatedCategoryCode: el código se compara ya recortado, y el
// defecto señala la SEGUNDA categoría citando la posición de la primera.
func TestValidate_RepeatedCategoryCode(t *testing.T) {
	_, verr := validate(docOf(
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"A","label":"Algo","price":1}]},` +
			`{"code":" 1 ","label":"Postres","items":[{"code":"1","sku":"B","label":"Algo","price":1}]}`))

	assertDefects(t, verr, atCategory(1, "code",
		`la categoría "Postres": el código "1" ya lo usa la categoría 1; el cliente teclea ese número y no se sabría a cuál quiere entrar.`))
}

// TestValidate_SubcategoryRules: el segundo filtro es opcional, pero si viene, viene
// bien.
func TestValidate_SubcategoryRules(t *testing.T) {
	cases := map[string]struct {
		subcategories string
		want          defect
	}{
		"not a list": {`"x"`, atCategory(0, "subcategories",
			`la categoría "Bebidas": "subcategories" debe ser una lista de {code, label}.`)},
		"missing code": {`[{"label":"Frías"}]`, atCategory(0, "subcategories[0].code",
			`la categoría "Bebidas", subcategoría 1 no tiene el código: es obligatorio.`)},
		"missing label": {`[{"code":"a"}]`, atCategory(0, "subcategories[0].label",
			`la categoría "Bebidas", subcategoría 1 no tiene el nombre: es obligatorio.`)},
		"repeated code": {`[{"code":"a","label":"A"},{"code":"a","label":"B"}]`, atCategory(0, "subcategories[1].code",
			`la categoría "Bebidas", subcategoría 2: el código "a" ya lo usa otra subcategoría de la misma categoría.`)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOf(`{"code":"1","label":"Bebidas","subcategories":` + c.subcategories + `,"items":[` + coffee("") + `]}`))
			assertDefects(t, verr, c.want)
		})
	}
}

// TestValidate_EmptySubcategoriesDisappear: una lista vacía es lo mismo que no
// declararla, y no viaja al documento.
func TestValidate_EmptySubcategoriesDisappear(t *testing.T) {
	doc := mustValidate(t, docOf(`{"code":"1","label":"Bebidas","subcategories":[],"items":[`+coffee("")+`]}`))
	if subs := doc.Catalog.Categories[0].Subcategories; subs != nil {
		t.Errorf("subcategories = %+v; se esperaba nil", subs)
	}
}

// itemsDoc arma un documento de una categoría con n artículos válidos y distintos.
func itemsDoc(n int) string {
	items := make([]string, 0, n)
	for i := range n {
		id := strconv.Itoa(i)
		items = append(items, `{"code":"`+id+`","sku":"S`+id+`","label":"Artículo","price":1}`)
	}
	return docOfItems(strings.Join(items, ","))
}

// TestValidate_ItemLimitStopsAndSaysSo: pasado el tope la validación no sigue. Los
// cuatro artículos están rotos a propósito: si el tope no cortara, saldrían sus
// defectos.
func TestValidate_ItemLimitStopsAndSaysSo(t *testing.T) {
	doc := docOfItems(`{"code":"0"},{"code":"1"},{"code":"2"},{"code":"3"}`)

	_, verr := catalogimport.Validate([]byte(doc), catalogimport.Limits{MaxItems: 3})
	assertDefects(t, verr, atHeader("catalog",
		"el catálogo trae 4 artículos y el máximo por importación es 3: divide la carga en varios archivos."))
}

// TestValidate_ItemLimitCountsTheWholeDocument: el tope es del documento entero,
// sumando todas las categorías; justo en el tope pasa.
func TestValidate_ItemLimitCountsTheWholeDocument(t *testing.T) {
	doc := docOf(
		`{"code":"1","label":"A","items":[{"code":"1","sku":"A1","label":"a","price":1},{"code":"2","sku":"A2","label":"a","price":1}]},` +
			`{"code":"2","label":"B","items":[{"code":"1","sku":"B1","label":"b","price":1}]}`)

	if _, verr := catalogimport.Validate([]byte(doc), catalogimport.Limits{MaxItems: 3}); verr != nil {
		t.Fatalf("3 artículos con el tope en 3 deben pasar: %v", verr)
	}
	_, verr := catalogimport.Validate([]byte(doc), catalogimport.Limits{MaxItems: 2})
	assertDefects(t, verr, atHeader("catalog",
		"el catálogo trae 3 artículos y el máximo por importación es 2: divide la carga en varios archivos."))
}

// TestValidate_NonPositiveItemLimitFallsBackToDefault: un tope a 0 —o negativo— no
// lo desactiva: cae a DefaultMaxItems.
func TestValidate_NonPositiveItemLimitFallsBackToDefault(t *testing.T) {
	if catalogimport.DefaultMaxItems != 500 {
		t.Fatalf("este test fabrica 500 y 501 artículos; DefaultMaxItems = %d", catalogimport.DefaultMaxItems)
	}
	for _, limits := range []catalogimport.Limits{{}, {MaxJSONBytes: -1, MaxItems: -3}} {
		if _, verr := catalogimport.Validate([]byte(itemsDoc(500)), limits); verr != nil {
			t.Errorf("con %+v el tope es el default y 500 artículos caben: %v", limits, verr)
		}
		_, verr := catalogimport.Validate([]byte(itemsDoc(501)), limits)
		assertDefects(t, verr, atHeader("catalog",
			"el catálogo trae 501 artículos y el máximo por importación es 500: divide la carga en varios archivos."))
	}
}
