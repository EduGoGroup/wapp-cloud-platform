package catalogo_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
)

// catalog_tolerant_test.go es un trozo de catalog_test.go (E-13): cubre el parser
// TOLERANTE del v2, regla a regla. Un campo v2 roto se descarta con su aviso
// literal y el catálogo sigue vendiendo; el que rechaza el documento entero es el
// validador del import, no este.

// tolerantCase es un catálogo de un artículo con JSON extra (ver oneArticleBlob),
// los avisos que debe dejar —enteros y en orden— y cómo queda el artículo.
type tolerantCase struct {
	name          string
	categoryExtra string
	articleExtra  string
	wantWarnings  []catalogo.CatalogWarning
	// wantV2 fija los campos v2 esperados sobre baseArticle; nil = ninguno.
	wantV2            func(a *catalogo.Article)
	wantSubcategories []catalogo.Subcategory
}

func runTolerantCases(t *testing.T, cases []tolerantCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := mustParse(t, oneArticleBlob(tc.categoryExtra, tc.articleExtra))
			got := onlyArticle(t, cat)
			want := baseArticle()
			if tc.wantV2 != nil {
				tc.wantV2(&want)
			}
			assertArticle(t, got, want)
			assertWarnings(t, cat.Warnings, tc.wantWarnings)
			if subs := cat.Categories[0].Subcategories; !reflect.DeepEqual(subs, tc.wantSubcategories) {
				t.Errorf("subcategories = %#v; se esperaban %#v", subs, tc.wantSubcategories)
			}
		})
	}
}

// categoryWarning es un aviso de la categoría "1" (sin sku).
func categoryWarning(reason string) catalogo.CatalogWarning {
	return catalogo.CatalogWarning{Category: "1", Field: fieldSubcategories, Reason: reason}
}

func TestParseCatalog_Subcategories(t *testing.T) {
	const notAList = "no es una lista de {code,label}: se ignoran las subcategorías de la categoría"
	runTolerantCases(t, []tolerantCase{
		{
			name:          "not a list is ignored whole",
			categoryExtra: `,"subcategories":"01a"`,
			wantWarnings:  []catalogo.CatalogWarning{categoryWarning(notAList)},
		},
		{
			name:          "one malformed entry drops the whole list",
			categoryExtra: `,"subcategories":[{"code":"a","label":"Uno"},{"code":"b","label":7}]`,
			wantWarnings:  []catalogo.CatalogWarning{categoryWarning(notAList)},
		},
		{
			name:          "without code and repeated code are discarded, first wins",
			categoryExtra: `,"subcategories":[{"label":"sin"},{"code":"a","label":"Uno"},{"code":"a","label":"Dos"},{"code":"b"}]`,
			wantWarnings: []catalogo.CatalogWarning{
				categoryWarning("subcategoría sin code: se descarta"),
				categoryWarning(`subcategoría con code repetido "a": se descarta`),
			},
			wantSubcategories: []catalogo.Subcategory{{Code: "a", Label: "Uno"}, {Code: "b"}},
		},
		{
			name:          "nothing usable leaves nil",
			categoryExtra: `,"subcategories":[{"label":"sin"}]`,
			wantWarnings:  []catalogo.CatalogWarning{categoryWarning("subcategoría sin code: se descarta")},
		},
		{name: "empty list is silent", categoryExtra: `,"subcategories":[]`},
		{name: "null is silent", categoryExtra: `,"subcategories":null`},
	})
}

func TestParseCatalog_SubcategoryReference(t *testing.T) {
	const declared = `,"subcategories":[{"code":"01a","label":"Uno"}]`
	runTolerantCases(t, []tolerantCase{
		{
			name:              "points to a subcategory of its category",
			categoryExtra:     declared,
			articleExtra:      `,"subcategory":"01a"`,
			wantV2:            func(a *catalogo.Article) { a.Subcategory = "01a" },
			wantSubcategories: []catalogo.Subcategory{{Code: "01a", Label: "Uno"}},
		},
		{
			name:              "dangling reference is ignored",
			categoryExtra:     declared,
			articleExtra:      `,"subcategory":"zzz"`,
			wantWarnings:      []catalogo.CatalogWarning{articleWarning(fieldSubcategory, `referencia "zzz", que no es una subcategoría de su categoría: se ignora`)},
			wantSubcategories: []catalogo.Subcategory{{Code: "01a", Label: "Uno"}},
		},
		{
			name:         "category without subcategories",
			articleExtra: `,"subcategory":"01a"`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldSubcategory, `referencia "01a", que no es una subcategoría de su categoría: se ignora`)},
		},
		{
			name:         "not a text",
			articleExtra: `,"subcategory":7`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldSubcategory, "no es un texto: se ignora")},
		},
		{name: "empty text is silent", categoryExtra: declared, articleExtra: `,"subcategory":""`,
			wantSubcategories: []catalogo.Subcategory{{Code: "01a", Label: "Uno"}}},
		{name: "null is silent", articleExtra: `,"subcategory":null`},
	})

	t.Run("a subcategory of another category does not count", func(t *testing.T) {
		cat := mustParse(t, `{"categories":[
		  {"code":"1","label":"X","subcategories":[{"code":"a","label":"Uno"}],"items":[]},
		  {"code":"2","label":"Y","items":[{"code":"1","sku":"B","label":"B","price":1,"subcategory":"a"}]}]}`)
		if got := cat.Categories[1].Items[0].Subcategory; got != "" {
			t.Errorf("subcategory = %q; la referencia a otra categoría debe quedar vacía", got)
		}
		assertWarnings(t, cat.Warnings, []catalogo.CatalogWarning{{
			Category: "2", SKU: "B", Field: fieldSubcategory,
			Reason: `referencia "a", que no es una subcategoría de su categoría: se ignora`,
		}})
	})
}

func TestParseCatalog_Tags(t *testing.T) {
	const notAList = "no es una lista de textos: se ignoran las etiquetas del artículo"
	const empty = "etiqueta vacía: se descarta"
	runTolerantCases(t, []tolerantCase{
		{
			name:         "text instead of list",
			articleExtra: `,"tags":"decoracion"`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldTags, notAList)},
		},
		{
			name:         "one non text entry drops the whole list",
			articleExtra: `,"tags":["a",1]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldTags, notAList)},
		},
		{
			name:         "empty and blank tags are discarded, the rest is not trimmed",
			articleExtra: `,"tags":["a",""," \t","  b "]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldTags, empty), articleWarning(fieldTags, empty)},
			wantV2:       func(a *catalogo.Article) { a.Tags = []string{"a", "  b "} },
		},
		{
			name:         "nothing usable leaves nil",
			articleExtra: `,"tags":[""]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldTags, empty)},
		},
		{name: "repeated tags are kept", articleExtra: `,"tags":["a","a"]`,
			wantV2: func(a *catalogo.Article) { a.Tags = []string{"a", "a"} }},
		{name: "empty list is silent", articleExtra: `,"tags":[]`},
	})
}

func TestParseCatalog_Attributes(t *testing.T) {
	notSimple := func(key string) catalogo.CatalogWarning {
		return articleWarning(fieldAttributes, "el atributo "+strconv.Quote(key)+" no es un valor simple: se descarta")
	}
	runTolerantCases(t, []tolerantCase{
		{
			name:         "list instead of object",
			articleExtra: `,"attributes":["porciones"]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldAttributes, "no es un objeto de pares clave→valor: se ignoran los atributos del artículo")},
		},
		{
			name:         "scalars become text",
			articleExtra: `,"attributes":{"sabor":"chocolate","porciones":12,"peso":0.5,"grande":1e21,"chico":1.50,"vegano":true,"frio":false,"vacio":""}`,
			wantV2: func(a *catalogo.Article) {
				a.Attributes = map[string]string{
					"sabor": "chocolate", "porciones": "12", "peso": "0.5", "grande": "1000000000000000000000",
					"chico": "1.5", "vegano": "true", "frio": "false", "vacio": "",
				}
			},
		},
		{
			name:         "non scalars and the empty key are discarded in key order",
			articleExtra: `,"attributes":{"z":{"a":1},"ok":"si","lista":[1],"nulo":null,"":"x","a":{}}`,
			wantWarnings: []catalogo.CatalogWarning{
				articleWarning(fieldAttributes, "atributo con clave vacía: se descarta"),
				notSimple("a"), notSimple("lista"), notSimple("nulo"), notSimple("z"),
			},
			wantV2: func(a *catalogo.Article) { a.Attributes = map[string]string{"ok": "si"} },
		},
		{
			name:         "nothing usable leaves nil",
			articleExtra: `,"attributes":{"x":null}`,
			wantWarnings: []catalogo.CatalogWarning{notSimple("x")},
		},
		{name: "empty object is silent", articleExtra: `,"attributes":{}`},
	})
}

func TestParseCatalog_Variants(t *testing.T) {
	const notAList = "no es una lista de {code,label,price}: el artículo se vende sin variantes"
	const noneLeft = "no queda ninguna variante utilizable: el artículo se vende sin variantes"
	variant := func(reason string) catalogo.CatalogWarning { return articleWarning(fieldVariants, reason) }
	runTolerantCases(t, []tolerantCase{
		{
			name:         "object instead of list",
			articleExtra: `,"variants":{"code":"V1"}`,
			wantWarnings: []catalogo.CatalogWarning{variant(notAList)},
		},
		{
			name: "each defect has its reason and the usable ones survive",
			articleExtra: `,"variants":[{"label":"sin code","price":1},{"code":"V1","label":"Chica","price":5},` +
				`{"code":"V1","label":"Otra","price":9},{"code":"V2","price":3},{"code":"V3","label":"Sin precio"},` +
				`{"code":"V4","label":"Negativa","price":-0.01},{"code":"V5","label":"Gratis","price":0}]`,
			wantWarnings: []catalogo.CatalogWarning{
				variant("variante sin code: se descarta"),
				variant(`variante con code repetido "V1": se descarta`),
				variant(`la variante "V2" no tiene label con el que ofrecerla: se descarta`),
				variant(`la variante "V3" no trae price (obligatorio con variantes): se descarta`),
				variant(`la variante "V4" tiene price negativo: se descarta`),
			},
			wantV2: func(a *catalogo.Article) {
				a.Variants = []catalogo.Variant{{Code: "V1", Label: "Chica", Price: 5}, {Code: "V5", Label: "Gratis", Price: 0}}
			},
		},
		{
			name:         "the first defect in order is the one reported",
			articleExtra: `,"variants":[{"price":-1},{"code":"V1","price":-1},{"code":"V2","label":"x"},{"code":"V2","label":"y","price":1}]`,
			wantWarnings: []catalogo.CatalogWarning{
				variant("variante sin code: se descarta"),
				variant(`la variante "V1" no tiene label con el que ofrecerla: se descarta`),
				variant(`la variante "V2" no trae price (obligatorio con variantes): se descarta`),
			},
			// La V2 descartada no «gasta» su code: la siguiente V2, completa, entra.
			wantV2: func(a *catalogo.Article) { a.Variants = []catalogo.Variant{{Code: "V2", Label: "y", Price: 1}} },
		},
		{
			name:         "none usable adds the closing warning",
			articleExtra: `,"variants":[{"code":"V1","label":"Chica"}]`,
			wantWarnings: []catalogo.CatalogWarning{
				variant(`la variante "V1" no trae price (obligatorio con variantes): se descarta`),
				variant(noneLeft),
			},
		},
		{
			name:         "null price is an absent price",
			articleExtra: `,"variants":[{"code":"V1","label":"Chica","price":null}]`,
			wantWarnings: []catalogo.CatalogWarning{
				variant(`la variante "V1" no trae price (obligatorio con variantes): se descarta`),
				variant(noneLeft),
			},
		},
		{name: "empty list is silent", articleExtra: `,"variants":[]`},
	})
}

func TestParseCatalog_Components(t *testing.T) {
	component := func(reason string) catalogo.CatalogWarning { return articleWarning(fieldComponents, reason) }
	lessThanOne := func(sku string) catalogo.CatalogWarning {
		return component("el componente " + strconv.Quote(sku) + " trae una qty menor que 1: se toma 1")
	}
	runTolerantCases(t, []tolerantCase{
		{
			name:         "number instead of list",
			articleExtra: `,"components":42`,
			wantWarnings: []catalogo.CatalogWarning{component("no es una lista de {sku,qty}: se ignoran los componentes del combo")},
		},
		{
			name: "missing sku is discarded and qty is sanitized",
			articleExtra: `,"components":[{"qty":1},{"sku":"CERO","qty":0},{"sku":"SIN"},{"sku":"DOS","qty":2},` +
				`{"sku":"NEG","qty":-3},{"sku":"FRAC","qty":2.9},{"sku":"MEDIO","qty":0.5},{"sku":"NULO","qty":null},{"sku":"DOS","qty":4}]`,
			wantWarnings: []catalogo.CatalogWarning{
				component("componente sin sku: se descarta"),
				lessThanOne("CERO"), lessThanOne("NEG"), lessThanOne("MEDIO"),
			},
			wantV2: func(a *catalogo.Article) {
				a.Components = []catalogo.Component{
					{SKU: "CERO", Qty: 1}, {SKU: "SIN", Qty: 1}, {SKU: "DOS", Qty: 2}, {SKU: "NEG", Qty: 1},
					{SKU: "FRAC", Qty: 2}, {SKU: "MEDIO", Qty: 1}, {SKU: "NULO", Qty: 1}, {SKU: "DOS", Qty: 4},
				}
			},
		},
		{
			name:         "nothing usable leaves nil",
			articleExtra: `,"components":[{"qty":1}]`,
			wantWarnings: []catalogo.CatalogWarning{component("componente sin sku: se descarta")},
		},
		{name: "empty list is silent", articleExtra: `,"components":[]`},
	})
}

// TestParseCatalog_VariantsWinOverComponents: con los dos, quedan las variantes
// (las que cambian lo que se cobra) y los componentes se ignoran con aviso.
func TestParseCatalog_VariantsWinOverComponents(t *testing.T) {
	const both = "el artículo declara variants y components a la vez: se ignoran los components (el import lo rechazará)"
	runTolerantCases(t, []tolerantCase{
		{
			name:         "both usable",
			articleExtra: `,"variants":[{"code":"V1","label":"Chica","price":8}],"components":[{"sku":"OTRO","qty":1}]`,
			wantWarnings: []catalogo.CatalogWarning{articleWarning(fieldComponents, both)},
			wantV2: func(a *catalogo.Article) {
				a.Variants = []catalogo.Variant{{Code: "V1", Label: "Chica", Price: 8}}
			},
		},
		{
			name:         "no usable variant keeps the combo",
			articleExtra: `,"variants":[{"code":"V1","label":"Chica"}],"components":[{"sku":"OTRO","qty":2}]`,
			wantWarnings: []catalogo.CatalogWarning{
				articleWarning(fieldVariants, `la variante "V1" no trae price (obligatorio con variantes): se descarta`),
				articleWarning(fieldVariants, "no queda ninguna variante utilizable: el artículo se vende sin variantes"),
			},
			wantV2: func(a *catalogo.Article) { a.Components = []catalogo.Component{{SKU: "OTRO", Qty: 2}} },
		},
	})
}

// TestParseCatalog_WarningsFollowTheFieldOrder: dentro de un artículo los avisos
// salen subcategory → tags → attributes → variants → components, sea cual sea
// el orden de las claves en el blob.
func TestParseCatalog_WarningsFollowTheFieldOrder(t *testing.T) {
	cat := mustParse(t, oneArticleBlob(`,"subcategories":7`,
		`,"components":1,"variants":2,"attributes":3,"tags":4,"subcategory":5`))
	assertArticle(t, onlyArticle(t, cat), baseArticle())
	gotFields := make([]string, 0, len(cat.Warnings))
	for _, w := range cat.Warnings {
		gotFields = append(gotFields, w.Field)
	}
	want := []string{fieldSubcategories, fieldSubcategory, fieldTags, fieldAttributes, fieldVariants, fieldComponents}
	if !reflect.DeepEqual(gotFields, want) {
		t.Errorf("orden de los avisos = %v; se esperaba %v", gotFields, want)
	}
}

// TestParseCatalog_DropsReservedSKU: el prefijo "_" es del sistema; el artículo
// que lo usa se cae entero —sin mirar sus campos v2— y el resto sigue vendiéndose.
func TestParseCatalog_DropsReservedSKU(t *testing.T) {
	const reserved = `el prefijo "_" está reservado para las líneas del sistema: el artículo se descarta`
	cat := mustParse(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"_shipping","label":"Envío","price":3,"tags":"rota","variants":7},
	  {"code":"2","sku":"BUENO","label":"Bueno","price":1},
	  {"code":"3","sku":"_","label":"Solo el prefijo","price":1}]},
	  {"code":"2","label":"Y","items":[{"code":"1","sku":"__x","label":"Doble","price":1}]}]}`)
	items := cat.Categories[0].Items
	if len(items) != 1 || items[0].SKU != "BUENO" {
		t.Errorf("solo debe quedar el artículo con sku legítimo; quedaron %+v", items)
	}
	if left := cat.Categories[1].Items; left == nil || len(left) != 0 {
		t.Errorf("una categoría que pierde todos sus artículos queda con la lista vacía, no nil: %#v", left)
	}
	assertWarnings(t, cat.Warnings, []catalogo.CatalogWarning{
		{Category: "1", SKU: "_shipping", Field: fieldSKU, Reason: reserved},
		{Category: "1", SKU: "_", Field: fieldSKU, Reason: reserved},
		{Category: "2", SKU: "__x", Field: fieldSKU, Reason: reserved},
	})
	if !strings.HasPrefix("_shipping", catalogo.SystemSKUPrefix) {
		t.Errorf("el caso usa un sku que ya no empieza por SystemSKUPrefix %q", catalogo.SystemSKUPrefix)
	}
}

// brokenTagsBlob es un catálogo con n artículos, cada uno con un aviso de tags.
func brokenTagsBlob(n int) string {
	var b strings.Builder
	b.WriteString(`{"categories":[{"code":"1","label":"X","items":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"code":"1","sku":"A` + strconv.Itoa(i) + `","label":"A","price":1,"tags":"no-soy-lista"}`)
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// TestParseCatalog_CapsTheWarnings: un catálogo roto en masa no llena la memoria
// ni el log; los avisos se cortan en 50 y uno final dice cuántos faltaron.
func TestParseCatalog_CapsTheWarnings(t *testing.T) {
	const limit = 50
	cases := map[string]struct {
		broken      int
		wantLen     int
		wantSummary string
	}{
		"below the cap":      {limit - 1, limit - 1, ""},
		"exactly the cap":    {limit, limit, ""},
		"one over the cap":   {limit + 1, limit + 1, "se omitieron 1 avisos más del mismo parseo"},
		"ten over the cap":   {limit + 10, limit + 1, "se omitieron 10 avisos más del mismo parseo"},
		"far beyond the cap": {limit * 5, limit + 1, "se omitieron 200 avisos más del mismo parseo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cat := mustParse(t, brokenTagsBlob(tc.broken))
			if len(cat.Categories[0].Items) != tc.broken {
				t.Errorf("artículos = %d; el tope es de avisos, no de artículos (%d)", len(cat.Categories[0].Items), tc.broken)
			}
			if len(cat.Warnings) != tc.wantLen {
				t.Fatalf("avisos = %d; se esperaban %d", len(cat.Warnings), tc.wantLen)
			}
			if first := cat.Warnings[0]; first.SKU != "A0" || first.Field != fieldTags {
				t.Errorf("el primer aviso es el del primer artículo; llegó %+v", first)
			}
			last := cat.Warnings[len(cat.Warnings)-1]
			if tc.wantSummary == "" {
				if last.SKU != "A"+strconv.Itoa(tc.broken-1) || last.Field != fieldTags {
					t.Errorf("sin pasar del tope no hay resumen; el último aviso es %+v", last)
				}
				return
			}
			want := catalogo.CatalogWarning{Field: "(varios)", Reason: tc.wantSummary}
			if last != want {
				t.Errorf("resumen = %+v; se esperaba %+v", last, want)
			}
		})
	}
}
