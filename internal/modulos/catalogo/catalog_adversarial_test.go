package catalogo_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// catalog_adversarial_test.go es un trozo de catalog_test.go (E-13): el corpus
// adversario de la equivalencia viejo ↔ nuevo (reglas.md §5, hallazgo 40).
// Separadores repetidos, dígitos no ASCII y espacios Unicode: lo que el parser
// viejo hace con ellos es lo que hace este, y cada valor esperado se fijó
// ejecutando el viejo como oráculo. Nada se «arregla» aquí.

// TestParseCatalog_NonASCIIDigits: un número escrito con dígitos no ASCII es un
// TEXTO para JSON. En un campo v1 tumba el parseo; en uno v2 tumba solo su lista.
func TestParseCatalog_NonASCIIDigits(t *testing.T) {
	t.Run("price of an article breaks the whole blob", func(t *testing.T) {
		for _, price := range []string{`"\u0661\u0662"`, `"\uff11\uff12"`, `"12"`} {
			raw := rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":`+price+`}]}]}`)
			_, err := catalogo.ParseCatalog(model.Content{Raw: raw})
			if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
				t.Fatalf("price %s: se esperaba ErrInvalidFlow; error = %v", price, err)
			}
			if !strings.HasPrefix(err.Error(), invalidPrefix+"blob de catálogo mal formado: ") {
				t.Errorf("price %s: texto del error = %q", price, err.Error())
			}
		}
	})
	runTolerantCases(t, []tolerantCase{
		{
			name:         "price of a variant drops all the variants",
			articleExtra: `,"variants":[{"code":"V1","label":"Chica","price":5},{"code":"V2","label":"Grande","price":"\u0669"}]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldVariants, "no es una lista de {code,label,price}: el artículo se vende sin variantes")},
		},
		{
			name:         "qty of a component drops all the components",
			articleExtra: `,"components":[{"sku":"UNO","qty":1},{"sku":"DOS","qty":"\u0662"}]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldComponents, "no es una lista de {sku,qty}: se ignoran los componentes del combo")},
		},
		{
			name:         "qty as ASCII text is not converted either",
			articleExtra: `,"components":[{"sku":"DOS","qty":"2"}]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldComponents, "no es una lista de {sku,qty}: se ignoran los componentes del combo")},
		},
		{
			name:         "as codes and attribute values they are plain text",
			articleExtra: `,"attributes":{"porciones":"\u0661\u0662"},"variants":[{"code":"1","label":"a","price":1},{"code":"\uff11","label":"b","price":2},{"code":"\u0661","label":"c","price":3}]`,
			wantV2: func(a *catalogo.Article) {
				a.Attributes = map[string]string{"porciones": "\u0661\u0662"}
				a.Variants = []catalogo.Variant{
					{Code: "1", Label: "a", Price: 1}, {Code: "\uff11", Label: "b", Price: 2}, {Code: "\u0661", Label: "c", Price: 3},
				}
			},
		},
	})
}

// TestParseCatalog_UnicodeSpaces: solo las ETIQUETAS se miran con TrimSpace (que
// conoce los espacios Unicode, no los caracteres de ancho cero). Todo lo demás se
// compara byte a byte y no se recorta.
func TestParseCatalog_UnicodeSpaces(t *testing.T) {
	const emptyTag = "etiqueta vacía: se descarta"
	runTolerantCases(t, []tolerantCase{
		{
			name:         "tags of only unicode spaces are empty",
			articleExtra: `,"tags":["\u00a0","\u3000\u2003","\u2028","\u200b","\ufeff"," a\u00a0"]`,
			wantWarnings: []catalogo.CatalogWarning{
				articleWarning(fieldTags, emptyTag), articleWarning(fieldTags, emptyTag), articleWarning(fieldTags, emptyTag),
			},
			// El espacio de ancho cero y el BOM no son espacio: esas etiquetas quedan.
			wantV2: func(a *catalogo.Article) { a.Tags = []string{"\u200b", "\ufeff", " a\u00a0"} },
		},
		{
			name:          "subcategory codes differing in a space are different",
			categoryExtra: `,"subcategories":[{"code":"a","label":"Uno"},{"code":"a\u00a0","label":"Dos"},{"code":"\u3000","label":"Tres"}]`,
			articleExtra:  `,"subcategory":"a\u00a0"`,
			wantV2:        func(a *catalogo.Article) { a.Subcategory = "a\u00a0" },
			wantSubcategories: []catalogo.Subcategory{
				{Code: "a", Label: "Uno"}, {Code: "a\u00a0", Label: "Dos"}, {Code: "\u3000", Label: "Tres"},
			},
		},
		{
			name:              "reference with a trailing space dangles",
			categoryExtra:     `,"subcategories":[{"code":"a","label":"Uno"}]`,
			articleExtra:      `,"subcategory":"a\u00a0"`,
			wantWarnings:      []catalogo.CatalogWarning{articleWarning(fieldSubcategory, `referencia "a\u00a0", que no es una subcategoría de su categoría: se ignora`)},
			wantSubcategories: []catalogo.Subcategory{{Code: "a", Label: "Uno"}},
		},
		{
			name:         "blank keys, labels and skus are not empty",
			articleExtra: `,"attributes":{"\u00a0":"x"," ":"y"},"variants":[{"code":"\u2003","label":"\u00a0","price":1}]`,
			wantV2: func(a *catalogo.Article) {
				a.Attributes = map[string]string{"\u00a0": "x", " ": "y"}
				a.Variants = []catalogo.Variant{{Code: "\u2003", Label: "\u00a0", Price: 1}}
			},
		},
		{
			name:         "component with a blank sku is kept",
			articleExtra: `,"components":[{"sku":"\u00a0","qty":2},{"sku":" HAMB ","qty":1}]`,
			wantV2: func(a *catalogo.Article) {
				a.Components = []catalogo.Component{{SKU: "\u00a0", Qty: 2}, {SKU: " HAMB ", Qty: 1}}
			},
		},
	})

	t.Run("a space before the reserved prefix saves the article", func(t *testing.T) {
		cat := mustParse(t, `{"categories":[{"code":"1","label":"X","items":[
		  {"code":"1","sku":" _shipping","label":"a","price":1},
		  {"code":"2","sku":"\u00a0_shipping","label":"b","price":1},
		  {"code":"3","sku":"\u200b_shipping","label":"c","price":1},
		  {"code":"4","sku":"_\u00a0shipping","label":"d","price":1},
		  {"code":"5","sku":"\uff3fshipping","label":"e","price":1}]}]}`)
		gotSKUs := make([]string, 0, 4)
		for _, a := range cat.Categories[0].Items {
			gotSKUs = append(gotSKUs, a.SKU)
		}
		want := []string{" _shipping", "\u00a0_shipping", "\u200b_shipping", "\uff3fshipping"}
		if strings.Join(gotSKUs, "|") != strings.Join(want, "|") {
			t.Errorf("skus que quedan = %q; se esperaban %q", gotSKUs, want)
		}
		assertWarnings(t, cat.Warnings, []catalogo.CatalogWarning{{
			Category: "1", SKU: "_\u00a0shipping", Field: fieldSKU,
			Reason: `el prefijo "_" está reservado para las líneas del sistema: el artículo se descarta`,
		}})
	})
}

// TestParseCatalog_RepeatedSeparators: `;` y `|` son separadores de la PLANILLA
// del import, no de este parser: aquí son texto y nada se trocea.
func TestParseCatalog_RepeatedSeparators(t *testing.T) {
	runTolerantCases(t, []tolerantCase{
		{
			name: "separators are plain text in every field",
			articleExtra: `,"tags":["a;;b","a||b",";;","||"],"attributes":{"k;;1":"v||2"},` +
				`"variants":[{"code":"V;;1","label":"Chica||Grande","price":1}]`,
			wantV2: func(a *catalogo.Article) {
				a.Tags = []string{"a;;b", "a||b", ";;", "||"}
				a.Attributes = map[string]string{"k;;1": "v||2"}
				a.Variants = []catalogo.Variant{{Code: "V;;1", Label: "Chica||Grande", Price: 1}}
			},
		},
		{
			name:         "component skus with separators are one sku each",
			articleExtra: `,"components":[{"sku":"HAMB;;REFR","qty":1},{"sku":"PAPA||2","qty":2}]`,
			wantV2: func(a *catalogo.Article) {
				a.Components = []catalogo.Component{{SKU: "HAMB;;REFR", Qty: 1}, {SKU: "PAPA||2", Qty: 2}}
			},
		},
		{
			name:              "a subcategory code with separators is referenced whole",
			categoryExtra:     `,"subcategories":[{"code":"01a||01b","label":"Dos;;Tres"}]`,
			articleExtra:      `,"subcategory":"01a||01b"`,
			wantV2:            func(a *catalogo.Article) { a.Subcategory = "01a||01b" },
			wantSubcategories: []catalogo.Subcategory{{Code: "01a||01b", Label: "Dos;;Tres"}},
		},
	})
}
