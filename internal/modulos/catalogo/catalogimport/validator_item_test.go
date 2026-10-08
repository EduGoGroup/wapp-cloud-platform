package catalogimport_test

import (
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
)

// validator_item_test.go: las reglas de Validate sobre UN artículo —nombre, código,
// sku, subcategoría, etiquetas, variantes y componentes—, con el corpus adversario
// de espacios Unicode (reglas.md §5). Parte de los tests de validator.go (E-13);
// gemelo de validator_item.go.

// TestValidate_ItemRequiredFields: cada caso trae UN defecto, para que el motivo que
// se comprueba sea inequívoco.
func TestValidate_ItemRequiredFields(t *testing.T) {
	cases := map[string]struct {
		item string
		want []defect
	}{
		"missing label": {`{"code":"1","sku":"CAFE","price":100}`, []defect{
			atItem(0, 0, "label", `el artículo 1 de la categoría "Bebidas" no tiene nombre: es lo que ve el cliente en la lista.`),
		}},
		"label of unicode spaces only": {`{"code":"1","sku":"CAFE","label":"` + emSpace + `","price":100}`, []defect{
			atItem(0, 0, "label", `el artículo 1 de la categoría "Bebidas" no tiene nombre: es lo que ve el cliente en la lista.`),
		}},
		// Tipo equivocado: se dice, y además el artículo se queda sin nombre.
		"label is a number": {`{"code":"1","sku":"CAFE","label":5,"price":100}`, []defect{
			atItem(0, 0, "label", `el artículo 1 de la categoría "Bebidas": el nombre debe ir entre comillas (es un texto, no un número ni una lista).`),
			atItem(0, 0, "label", `el artículo 1 de la categoría "Bebidas" no tiene nombre: es lo que ve el cliente en la lista.`),
		}},
		"missing code": {`{"sku":"CAFE","label":"Café","price":100}`, []defect{
			atItem(0, 0, "code", coffeeSubject+" no tiene el código: es obligatorio."),
		}},
		"code of unicode spaces only": {`{"code":"` + nbsp + `","sku":"CAFE","label":"Café","price":100}`, []defect{
			atItem(0, 0, "code", coffeeSubject+" no tiene el código: es obligatorio."),
		}},
		"missing sku": {`{"code":"1","label":"Café","price":100}`, []defect{
			atItem(0, 0, "sku", coffeeSubject+" no tiene el sku: es obligatorio."),
		}},
		"sku of unicode spaces only": {`{"code":"1","sku":"` + nbsp + ideographicSpace + `","label":"Café","price":100}`, []defect{
			atItem(0, 0, "sku", coffeeSubject+" no tiene el sku: es obligatorio."),
		}},
		"missing price": {`{"code":"1","sku":"CAFE","label":"Café"}`, []defect{
			atItem(0, 0, "price", coffeeSubject+" no tiene el precio: es obligatorio."),
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(c.item))
			assertDefects(t, verr, c.want...)
		})
	}
}

// TestValidate_ItemLabelIsTrimmedButCodeAndSKUAreNot: el nombre se guarda sin
// espacios alrededor; el código y el sku viajan TAL CUAL llegaron, con sus espacios
// (también los Unicode). Es lo que hace el validador viejo, y no se «arregla».
func TestValidate_ItemLabelIsTrimmedButCodeAndSKUAreNot(t *testing.T) {
	doc := mustValidate(t, docOfItems(`{"code":"`+nbsp+`1","sku":"`+nbsp+`CAFE`+nbsp+`","label":"`+emSpace+`Café`+emSpace+`","price":100}`))

	item := doc.Catalog.Categories[0].Items[0]
	if item.Label != "Café" {
		t.Errorf("label = %q; se esperaba \"Café\" (sin espacios alrededor)", item.Label)
	}
	if item.Code != nbsp+"1" || item.SKU != nbsp+"CAFE"+nbsp {
		t.Errorf("code = %q, sku = %q; se esperaban tal cual llegaron, sin recortar", item.Code, item.SKU)
	}
	if cat := runtimeCatalog(t, doc.Catalog); len(cat.Warnings) != 0 {
		t.Errorf("el runtime avisa de un catálogo que el import aceptó: %+v", cat.Warnings)
	}
}

// TestValidate_RepeatedItemCode: dos artículos con el mismo número en la misma
// categoría hacen imposible saber cuál se pidió. La comparación es exacta: «1» y
// «1 » son códigos distintos.
func TestValidate_RepeatedItemCode(t *testing.T) {
	_, verr := validate(docOfItems(`{"code":"1","sku":"A","label":"Café","price":1},{"code":"1","sku":"B","label":"Té","price":1}`))
	assertDefects(t, verr, atItem(0, 1, "code",
		`el artículo 2 ("Té") de la categoría "Bebidas": el código "1" ya lo usa el artículo 1 de la misma categoría; el cliente teclea ese número y no se sabría cuál de los dos pidió.`))

	mustValidate(t, docOfItems(`{"code":"1","sku":"A","label":"Café","price":1},{"code":"1 ","sku":"B","label":"Té","price":1}`))
	mustValidate(t, docOf(
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"A","label":"Café","price":1}]},`+
			`{"code":"2","label":"Postres","items":[{"code":"1","sku":"B","label":"Flan","price":1}]}`))
}

// TestValidate_SKURepeatedAcrossCategories: la unicidad del sku es de TODO el
// catálogo, así que el motivo nombra la categoría del artículo que se lo llevó
// primero. Mandar a buscar dentro de la categoría equivocada es peor que callar.
func TestValidate_SKURepeatedAcrossCategories(t *testing.T) {
	_, verr := validate(docOf(
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2500}]},` +
			`{"code":"2","label":"Postres","items":[{"code":"1","sku":"CAFE","label":"Café con leche","price":3000}]}`))

	assertDefects(t, verr, atItem(1, 0, "sku",
		`el artículo 1 ("Café con leche") de la categoría "Postres": el sku "CAFE" ya lo usa el artículo 1 ("Café") de la categoría "Bebidas"; `+
			`el sku identifica al artículo en el pedido y tiene que ser único en TODO el catálogo, no solo dentro de su categoría.`))
}

// TestValidate_SKUIsComparedExactly (corpus adversario): el sku no se recorta ni
// para la unicidad ni para nada. Dos skus que solo se distinguen por un espacio
// final son DOS skus, y el espacio de ancho cero —que no es espacio para
// strings.TrimSpace— vale como sku.
func TestValidate_SKUIsComparedExactly(t *testing.T) {
	doc := mustValidate(t, docOfItems(
		`{"code":"1","sku":"CAFE","label":"Café","price":100},`+
			`{"code":"2","sku":"CAFE ","label":"Café 2","price":100},`+
			`{"code":"3","sku":"`+zeroWidthSpace+`","label":"Café 3","price":100}`))

	items := doc.Catalog.Categories[0].Items
	if items[0].SKU != "CAFE" || items[1].SKU != "CAFE " || items[2].SKU != zeroWidthSpace {
		t.Errorf("skus = %q, %q, %q; se esperaban tal cual llegaron", items[0].SKU, items[1].SKU, items[2].SKU)
	}
}

// TestValidate_ReservedPrefixComesFromTheCatalogConstant: el prefijo del sistema no
// se re-declara en el importador (T-8). Si alguien cambiara
// catalogo.SystemSKUPrefix, este test seguiría midiendo la regla de verdad.
func TestValidate_ReservedPrefixComesFromTheCatalogConstant(t *testing.T) {
	for _, sku := range []string{catalogo.SystemSKUPrefix + "loquesea", catalogo.SystemSKUPrefix} {
		_, verr := validate(docOfItems(`{"code":"1","sku":"` + sku + `","label":"Café","price":2500}`))
		assertDefects(t, verr, atItem(0, 0, "sku",
			coffeeSubject+": el sku "+strconv.Quote(sku)+" empieza por "+strconv.Quote(catalogo.SystemSKUPrefix)+
				", que está reservado para las líneas que pone wApp (el envío, por ejemplo). Ponle otro."))
	}
}

// TestValidate_ReservedPrefixIsNotTrimmed (corpus adversario): el prefijo se mira
// sobre el sku TAL CUAL llega. Con un espacio delante —corriente o Unicode— el sku
// ya no «empieza por» el prefijo y pasa. Es lo que hace el validador viejo, y el
// runtime tampoco lo descarta: los dos rigores siguen sin contradecirse.
func TestValidate_ReservedPrefixIsNotTrimmed(t *testing.T) {
	for _, sku := range []string{" _shipping", nbsp + "_shipping"} {
		doc := mustValidate(t, docOfItems(`{"code":"1","sku":"`+sku+`","label":"Café","price":100}`))
		if got := doc.Catalog.Categories[0].Items[0].SKU; got != sku {
			t.Errorf("sku = %q; se esperaba %q, tal cual llegó", got, sku)
		}
		if cat := runtimeCatalog(t, doc.Catalog); len(cat.Warnings) != 0 || len(cat.Categories[0].Items) != 1 {
			t.Errorf("el runtime no conserva sin avisos el sku %q que el import aceptó: %+v", sku, cat)
		}
	}
}

// TestValidate_SubcategoryReference: si apunta a algo que su categoría no declaró,
// promete un filtro que no existe.
func TestValidate_SubcategoryReference(t *testing.T) {
	_, verr := validate(docOfItems(coffee(`,"subcategory":"01z"`)))
	assertDefects(t, verr, atItem(0, 0, "subcategory",
		coffeeSubject+`: la subcategoría "01z" no está declarada en su categoría; decláralas en "subcategories" o quita la referencia.`))

	// Declarada en OTRA categoría no vale: la referencia es a las de la suya.
	_, verr = validate(docOf(
		`{"code":"1","label":"Bebidas","items":[` + coffee(`,"subcategory":"frias"`) + `]},` +
			`{"code":"2","label":"Postres","subcategories":[{"code":"frias","label":"Fríos"}],"items":[{"code":"1","sku":"FLAN","label":"Flan","price":1}]}`))
	assertDefects(t, verr, atItem(0, 0, "subcategory",
		coffeeSubject+`: la subcategoría "frias" no está declarada en su categoría; decláralas en "subcategories" o quita la referencia.`))

	// Vacía, solo espacios o null: no hay referencia.
	for _, ref := range []string{`""`, `"` + nbsp + `"`, `null`} {
		doc := mustValidate(t, docOfItems(coffee(`,"subcategory":`+ref)))
		if got := doc.Catalog.Categories[0].Items[0].Subcategory; got != "" {
			t.Errorf("subcategory %s → %q; se esperaba vacía", ref, got)
		}
	}
}

// TestValidate_SubcategoryReferenceIsTrimmedButDeclarationIsNot (corpus
// adversario): la REFERENCIA se recorta y el código DECLARADO no. Con espacios
// alrededor de la referencia, casa; con un espacio en la declaración, esa
// subcategoría es irreferenciable aunque el artículo la escriba idéntica.
func TestValidate_SubcategoryReferenceIsTrimmedButDeclarationIsNot(t *testing.T) {
	doc := mustValidate(t, docOf(`{"code":"1","label":"Bebidas","subcategories":[{"code":"01a","label":"Frías"}],`+
		`"items":[`+coffee(`,"subcategory":"`+nbsp+`01a`+nbsp+`"`)+`]}`))
	if got := doc.Catalog.Categories[0].Items[0].Subcategory; got != "01a" {
		t.Errorf("subcategory = %q; se esperaba \"01a\" (la referencia se recorta)", got)
	}

	_, verr := validate(docOf(`{"code":"1","label":"Bebidas","subcategories":[{"code":"01a ","label":"Frías"}],` +
		`"items":[` + coffee(`,"subcategory":"01a "`) + `]}`))
	assertDefects(t, verr, atItem(0, 0, "subcategory",
		coffeeSubject+`: la subcategoría "01a" no está declarada en su categoría; decláralas en "subcategories" o quita la referencia.`))
}

// TestValidate_Tags: lista de textos no vacíos. Una etiqueta de solo espacios
// —también Unicode— está vacía; las demás viajan tal cual, sin recortar, y la de
// ancho cero cuenta como texto.
func TestValidate_Tags(t *testing.T) {
	_, verr := validate(docOfItems(coffee(`,"tags":"decoracion"`)))
	assertDefects(t, verr, atItem(0, 0, "tags",
		coffeeSubject+`: las etiquetas deben ser una lista de textos, por ejemplo ["sin_lactosa", "vegano"].`))

	_, verr = validate(docOfItems(coffee(`,"tags":["a","  ","b","` + nbsp + `"]`)))
	assertDefects(t, verr,
		atItem(0, 0, "tags[1]", coffeeSubject+": la etiqueta 2 está vacía."),
		atItem(0, 0, "tags[3]", coffeeSubject+": la etiqueta 4 está vacía."),
	)

	doc := mustValidate(t, docOfItems(coffee(`,"tags":["`+zeroWidthSpace+`"," a ","b"]`)))
	if got, want := compactJSON(t, doc.Catalog.Categories[0].Items[0].Tags), compactJSON(t, []string{zeroWidthSpace, " a ", "b"}); got != want {
		t.Errorf("tags = %s; se esperaban %s, sin recortar", got, want)
	}

	empty := mustValidate(t, docOfItems(coffee(`,"tags":[]`)))
	if tags := empty.Catalog.Categories[0].Items[0].Tags; tags != nil {
		t.Errorf("una lista de etiquetas vacía no viaja: %+v", tags)
	}
}

// TestValidate_VariantRules: código, nombre y precio obligatorios; código único
// dentro del artículo; la lista vacía se rechaza.
func TestValidate_VariantRules(t *testing.T) {
	cases := map[string]struct {
		variants string
		want     defect
	}{
		"not a list": {`"x"`, atItem(0, 0, "variants",
			coffeeSubject+": las variantes deben ser una lista de {code, label, price}.")},
		"empty list": {`[]`, atItem(0, 0, "variants",
			coffeeSubject+" trae la lista de variantes vacía: quítala o declara al menos una presentación.")},
		"missing code": {`[{"label":"Chico","price":1}]`, atItem(0, 0, "variants[0].code",
			coffeeSubject+", variante 1 no tiene el código: es obligatorio.")},
		"missing label": {`[{"code":"V1","price":1}]`, atItem(0, 0, "variants[0].label",
			coffeeSubject+", variante 1 no tiene el nombre: es obligatorio.")},
		"missing price": {`[{"code":"V1","label":"Chico"}]`, atItem(0, 0, "variants[0].price",
			coffeeSubject+", variante 1 no tiene el precio: es obligatorio.")},
		"price as text": {`[{"code":"V1","label":"Chico","price":"1"}]`, atItem(0, 0, "variants[0].price",
			coffeeSubject+`, variante 1: el precio debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`)},
		"repeated code": {`[{"code":"V1","label":"Chico","price":1},{"code":"V1","label":"Grande","price":2}]`, atItem(0, 0, "variants[1].code",
			coffeeSubject+`, variante 2: el código "V1" ya lo usa otra variante del mismo artículo.`)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(coffee(`,"variants":` + c.variants)))
			assertDefects(t, verr, c.want)
		})
	}
}

// TestValidate_ComponentRules: lista no vacía de {sku, qty}; el sku tiene que ser un
// artículo declarado.
func TestValidate_ComponentRules(t *testing.T) {
	cases := map[string]struct {
		components string
		want       defect
	}{
		"not a list": {`"x"`, atItem(0, 0, "components",
			coffeeSubject+": los componentes deben ser una lista de {sku, qty}.")},
		"empty list": {`[]`, atItem(0, 0, "components",
			coffeeSubject+" trae la lista de componentes vacía: quítala o declara de qué se compone el combo.")},
		"missing sku": {`[{"qty":1}]`, atItem(0, 0, "components[0].sku",
			coffeeSubject+", componente 1 no tiene el sku: es obligatorio.")},
		"unknown sku": {`[{"sku":"CAFE"},{"sku":"NOPE"}]`, atItem(0, 0, "components[1].sku",
			coffeeSubject+`, componente 2: el sku "NOPE" no existe en el catálogo; los componentes de un combo tienen que ser artículos declarados.`)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(docOfItems(coffee(`,"components":` + c.components)))
			assertDefects(t, verr, c.want)
		})
	}
}

// TestValidate_ComponentMayPointToAnItemDeclaredLater: la integridad de los combos
// se cierra contra el catálogo COMPLETO, no contra lo leído hasta ese momento.
func TestValidate_ComponentMayPointToAnItemDeclaredLater(t *testing.T) {
	doc := mustValidate(t, docOf(
		`{"code":"1","label":"Combos","items":[{"code":"1","sku":"COMBO","label":"Combo","price":9,"components":[{"sku":"FLAN"}]}]},`+
			`{"code":"2","label":"Postres","items":[{"code":"1","sku":"FLAN","label":"Flan","price":3}]}`))

	if got, want := compactJSON(t, doc.Catalog.Categories[0].Items[0].Components), `[{"sku":"FLAN","qty":1}]`; got != want {
		t.Errorf("components = %s; se esperaba %s (qty ausente vale 1)", got, want)
	}
}

// TestValidate_VariantsAndComponentsTogetherAreRejected cumple una promesa escrita
// en el runtime: catalogo.ParseCatalog conserva las variantes, ignora los
// componentes y avisa «(el import lo rechazará)». Este test es esa frase.
func TestValidate_VariantsAndComponentsTogetherAreRejected(t *testing.T) {
	const combo = `{"code":"2","sku":"COMBO","label":"Combo","price":9000,` +
		`"variants":[{"code":"V1","label":"Chico","price":9000}],"components":[{"sku":"HAMB","qty":1}]}`
	_, verr := validate(docOf(`{"code":"1","label":"Combos","items":[` +
		`{"code":"1","sku":"HAMB","label":"Hamburguesa","price":6000},` + combo + `]}`))

	assertDefects(t, verr, atItem(0, 1, "variants",
		`el artículo 2 ("Combo") de la categoría "Combos" declara variantes y componentes a la vez: `+
			`o se vende en presentaciones (variants) o es un combo (components), no las dos cosas.`))

	// El MISMO artículo en el runtime no se rompe: conserva las variantes y suelta
	// los componentes. Los dos rigores conviven; el import es el que corta.
	body := mustBody(t, `{"categories":[{"code":"1","label":"Combos","items":[`+combo+`]}]}`)
	article := runtimeCatalog(t, body).Categories[0].Items[0]
	if !article.HasVariants() || article.IsCombo() {
		t.Errorf("el runtime debe conservar las variantes y soltar los componentes: %+v", article)
	}
}

// TestValidate_VariantsAndComponentsLookAtPresenceNotAtTheParse: si uno de los dos
// campos viene mal formado ya tiene su propio defecto, pero la incompatibilidad se
// declaró igual; y un null es ausencia.
func TestValidate_VariantsAndComponentsLookAtPresenceNotAtTheParse(t *testing.T) {
	_, verr := validate(docOfItems(coffee(`,"variants":"x","components":[{"sku":"CAFE"}]`)))
	assertDefects(t, verr,
		atItem(0, 0, "variants", coffeeSubject+": las variantes deben ser una lista de {code, label, price}."),
		atItem(0, 0, "variants", coffeeSubject+" declara variantes y componentes a la vez: o se vende en presentaciones (variants) o es un combo (components), no las dos cosas."),
	)

	doc := mustValidate(t, docOfItems(coffee(`,"variants":null,"components":[{"sku":"CAFE"}]`)))
	if item := doc.Catalog.Categories[0].Items[0]; item.Variants != nil || len(item.Components) != 1 {
		t.Errorf("variants null es ausencia: %+v", item)
	}
}
