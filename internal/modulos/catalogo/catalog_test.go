//go:build pendiente

package catalogo_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// catalog_test.go cubre la forma de los tipos, los goldens, el v1 estricto y los
// errores de ParseCatalog. El parser tolerante del v2 está en
// catalog_tolerant_test.go y el corpus adversario en catalog_adversarial_test.go
// (E-13); los ayudantes, en helpers_test.go.

func TestSystemSKUPrefix_IsTheUnderscore(t *testing.T) {
	if catalogo.SystemSKUPrefix != "_" {
		t.Errorf("SystemSKUPrefix = %q; el prefijo reservado es \"_\" (líneas como _shipping)", catalogo.SystemSKUPrefix)
	}
}

// TestCatalog_SerializesWithGoNamesInOrder sujeta T-1: el golden serializa el
// árbol con los nombres Go de los campos, así que nombre, orden y omitempty de
// cada tipo son contrato.
func TestCatalog_SerializesWithGoNamesInOrder(t *testing.T) {
	v1 := catalogo.Catalog{Categories: []catalogo.Category{{
		Code: "1", Label: "L",
		Items: []catalogo.Article{{Code: "1", SKU: "S", Label: "A", Price: 1.5, Description: "d"}},
	}}}
	want := `{"Categories":[{"Code":"1","Label":"L","Items":[` +
		`{"Code":"1","SKU":"S","Label":"A","Price":1.5,"Description":"d"}]}]}`
	if got := compactJSON(t, v1); got != want {
		t.Errorf("árbol v1 = %s\nse esperaba %s (ningún campo v2 ni Warnings)", got, want)
	}

	v2 := catalogo.Catalog{
		Categories: []catalogo.Category{{
			Code: "1", Label: "L",
			Items: []catalogo.Article{{
				Code: "1", SKU: "S", Label: "A", Price: 1.5, Description: "d",
				Subcategory: "a",
				Tags:        []string{"t"},
				Attributes:  map[string]string{"k": "v"},
				Variants:    []catalogo.Variant{{Code: "V", Label: "vl", Price: 2}},
				Components:  []catalogo.Component{{SKU: "C", Qty: 3}},
			}},
			Subcategories: []catalogo.Subcategory{{Code: "a", Label: "sl"}},
		}},
		Warnings: []catalogo.CatalogWarning{{Category: "1", SKU: "S", Field: "f", Reason: "r"}},
	}
	want = `{"Categories":[{"Code":"1","Label":"L","Items":[` +
		`{"Code":"1","SKU":"S","Label":"A","Price":1.5,"Description":"d","Subcategory":"a","Tags":["t"],` +
		`"Attributes":{"k":"v"},"Variants":[{"Code":"V","Label":"vl","Price":2}],"Components":[{"SKU":"C","Qty":3}]}],` +
		`"Subcategories":[{"Code":"a","Label":"sl"}]}],` +
		`"Warnings":[{"Category":"1","SKU":"S","Field":"f","Reason":"r"}]}`
	if got := compactJSON(t, v2); got != want {
		t.Errorf("árbol v2 = %s\nse esperaba %s", got, want)
	}
}

func compactJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) = error %v", v, err)
	}
	return string(b)
}

func TestArticle_HasVariantsAndIsCombo(t *testing.T) {
	cases := map[string]struct {
		article      catalogo.Article
		wantVariants bool
		wantCombo    bool
	}{
		"plain article":     {catalogo.Article{SKU: "A"}, false, false},
		"empty lists":       {catalogo.Article{Variants: []catalogo.Variant{}, Components: []catalogo.Component{}}, false, false},
		"with one variant":  {catalogo.Article{Variants: []catalogo.Variant{{Code: "V"}}}, true, false},
		"with a component":  {catalogo.Article{Components: []catalogo.Component{{SKU: "C", Qty: 1}}}, false, true},
		"with both by hand": {catalogo.Article{Variants: []catalogo.Variant{{}}, Components: []catalogo.Component{{}}}, true, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.article.HasVariants(); got != tc.wantVariants {
				t.Errorf("HasVariants() = %v; se esperaba %v", got, tc.wantVariants)
			}
			if got := tc.article.IsCombo(); got != tc.wantCombo {
				t.Errorf("IsCombo() = %v; se esperaba %v", got, tc.wantCombo)
			}
		})
	}
}

// TestParseCatalog_GoldenV1 es la red de no-regresión del v1: el blob real del
// e2e del Plan 016 da, byte a byte, el árbol congelado ANTES del catálogo v2.
func TestParseCatalog_GoldenV1(t *testing.T) {
	cat, err := catalogo.ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v1.json")})
	if err != nil {
		t.Fatalf("el blob v1 real debe parsear sin error: %v", err)
	}
	assertGolden(t, "catalog_v1_parsed.golden.json", dumpCatalog(t, cat))
}

// TestParseCatalog_V1HasNoWarningsNorV2Fields: un blob v1 no produce ni un aviso
// ni puebla ningún campo v2 (el golden sujeta el árbol; esto, la ausencia de ruido).
func TestParseCatalog_V1HasNoWarningsNorV2Fields(t *testing.T) {
	cat, err := catalogo.ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v1.json")})
	if err != nil {
		t.Fatalf("blob v1 real: %v", err)
	}
	if cat.Warnings != nil {
		t.Errorf("un blob v1 deja Warnings nil; llegó %+v", cat.Warnings)
	}
	for _, c := range cat.Categories {
		if c.Subcategories != nil {
			t.Errorf("la categoría %q pobló Subcategories con un blob v1", c.Code)
		}
		for _, a := range c.Items {
			if a.Subcategory != "" || a.Tags != nil || a.Attributes != nil || a.Variants != nil || a.Components != nil {
				t.Errorf("el artículo %q pobló campos v2 con un blob v1: %+v", a.SKU, a)
			}
			if a.HasVariants() || a.IsCombo() {
				t.Errorf("el artículo %q de un blob v1 no tiene variantes ni es combo", a.SKU)
			}
		}
	}
}

// TestParseCatalog_GoldenV2 usa el ejemplo del contrato v2 (D-041.2): los cinco
// campos nuevos poblados, sin un solo aviso, y el árbol entero byte a byte.
func TestParseCatalog_GoldenV2(t *testing.T) {
	cat, err := catalogo.ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v2.json")})
	if err != nil {
		t.Fatalf("el catálogo v2 del contrato debe parsear: %v", err)
	}
	assertWarnings(t, cat.Warnings, nil)
	assertGolden(t, "catalog_v2_parsed.golden.json", dumpCatalog(t, cat))

	cakes := cat.Categories[0]
	wantSubcategories := []catalogo.Subcategory{{Code: "01a", Label: "Infantiles"}, {Code: "01b", Label: "Clásicas"}}
	if !reflect.DeepEqual(cakes.Subcategories, wantSubcategories) {
		t.Errorf("subcategories = %+v; se esperaban %+v", cakes.Subcategories, wantSubcategories)
	}
	assertArticle(t, cakes.Items[0], catalogo.Article{
		Code: "1", SKU: "TORTA-CHOC", Label: "Torta de chocolate", Price: 18000,
		Description: "Bizcocho de cacao con ganache",
		Subcategory: "01b",
		Tags:        []string{"decoracion", "sin_lactosa"},
		Attributes:  map[string]string{"porciones": "10-12", "sabor": "chocolate"},
		Variants: []catalogo.Variant{
			{Code: "V1", Label: "10-12 porciones", Price: 18000},
			{Code: "V2", Label: "25-30 porciones", Price: 32000},
		},
	})
	assertArticle(t, cakes.Items[1], catalogo.Article{
		Code: "2", SKU: "COMBO-1", Label: "Combo hamburguesa", Price: 9500,
		Components: []catalogo.Component{{SKU: "HAMB", Qty: 1}, {SKU: "REFR", Qty: 1}, {SKU: "PAPA", Qty: 2}},
	})
	if !cakes.Items[0].HasVariants() || cakes.Items[0].IsCombo() {
		t.Error("la torta se vende por variantes y no es un combo")
	}
	if cakes.Items[1].HasVariants() || !cakes.Items[1].IsCombo() {
		t.Error("el combo es un combo y no tiene variantes")
	}
}

// TestParseCatalog_V1Shape: lo opcional del v1 y lo que NO valida.
func TestParseCatalog_V1Shape(t *testing.T) {
	t.Run("description is optional", func(t *testing.T) {
		assertArticle(t, onlyArticle(t, mustParse(t, oneArticleBlob("", ""))), baseArticle())
	})
	t.Run("category without items keeps an empty list", func(t *testing.T) {
		cat := mustParse(t, `{"categories":[{"code":"1","label":"Vacía"}]}`)
		want := `{"Categories":[{"Code":"1","Label":"Vacía","Items":[]}]}`
		if got := compactJSON(t, cat); got != want {
			t.Errorf("catálogo = %s; se esperaba %s (Items vacío, no null)", got, want)
		}
	})
	t.Run("unknown keys are ignored", func(t *testing.T) {
		cat := mustParse(t, oneArticleBlob(`,"color":"rojo"`, `,"stock":7,"nested":{"a":[1]}`))
		assertArticle(t, onlyArticle(t, cat), baseArticle())
		assertWarnings(t, cat.Warnings, nil)
	})
	t.Run("order and repeated codes are kept as they come", func(t *testing.T) {
		cat := mustParse(t, `{"categories":[
		  {"code":"2","label":"B","items":[{"code":"1","sku":"X","label":"x","price":2},{"code":"1","sku":"X","label":"y","price":0}]},
		  {"code":"2","label":"A","items":[{"code":"","sku":"","label":"","price":-3.25}]}]}`)
		want := []catalogo.Category{
			{Code: "2", Label: "B", Items: []catalogo.Article{
				{Code: "1", SKU: "X", Label: "x", Price: 2}, {Code: "1", SKU: "X", Label: "y", Price: 0},
			}},
			{Code: "2", Label: "A", Items: []catalogo.Article{{Price: -3.25}}},
		}
		if !reflect.DeepEqual(cat.Categories, want) {
			t.Errorf("categorías = %+v\nse esperaban %+v (el parser no ordena, no deduplica y no valida el v1)", cat.Categories, want)
		}
		assertWarnings(t, cat.Warnings, nil)
	})
}

// TestParseCatalog_Errors: los cuatro rechazos, sobre model.ErrInvalidFlow, con
// su texto y el Catalog cero.
func TestParseCatalog_Errors(t *testing.T) {
	const (
		emptyRaw     = "el carrito exige content.raw con el árbol de catálogo, pero llegó vacío"
		notSerial    = "no se pudo re-serializar content.raw del catálogo: "
		malformed    = "blob de catálogo mal formado: "
		noCategories = "el catálogo no tiene categorías"
	)
	article := func(fields string) map[string]any {
		return rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[{`+fields+`}]}]}`)
	}
	cases := []struct {
		name string
		raw  map[string]any
		// exact dice si want es el motivo entero o solo su comienzo (lo que sigue
		// es el error de encoding/json).
		want  string
		exact bool
	}{
		{"nil raw", nil, emptyRaw, true},
		{"raw that cannot be serialized", map[string]any{"categories": func() {}}, notSerial + "json: unsupported type: func()", true},
		{"empty raw map", map[string]any{}, noCategories, true},
		{"no categories key", rawFromJSON(t, `{"otra":1}`), noCategories, true},
		{"empty categories", rawFromJSON(t, `{"categories":[]}`), noCategories, true},
		{"null categories", rawFromJSON(t, `{"categories":null}`), noCategories, true},
		{"categories is not a list", rawFromJSON(t, `{"categories":"no-soy-un-array"}`), malformed, false},
		{"items is not a list", rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":{}}]}`), malformed, false},
		{"price is a text", article(`"code":"1","sku":"A","label":"A","price":"gratis"`), malformed, false},
		{"price is a numeric text", article(`"code":"1","sku":"A","label":"A","price":"2.5"`), malformed, false},
		{"code is a number", article(`"code":1,"sku":"A","label":"A","price":1`), malformed, false},
		{"sku is a number", article(`"code":"1","sku":7,"label":"A","price":1`), malformed, false},
		{"description is a list", article(`"code":"1","sku":"A","label":"A","price":1,"description":["x"]`), malformed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalogo.ParseCatalog(model.Content{Raw: tc.raw})
			if err == nil {
				t.Fatalf("se esperaba error y llegó el catálogo %+v", got)
			}
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Errorf("el error no envuelve model.ErrInvalidFlow: %v", err)
			}
			want := invalidPrefix + tc.want
			if tc.exact && err.Error() != want {
				t.Errorf("texto del error = %q; se esperaba %q", err.Error(), want)
			}
			if !tc.exact && !strings.HasPrefix(err.Error(), want) {
				t.Errorf("texto del error = %q; debía empezar por %q", err.Error(), want)
			}
			if !reflect.DeepEqual(got, catalogo.Catalog{}) {
				t.Errorf("con error se devuelve el Catalog cero; llegó %+v", got)
			}
		})
	}
}

// TestParseCatalog_ErrorsKeepTheJSONCause: el error de encoding/json sigue en la
// cadena (se envuelve con %w, no se aplana).
func TestParseCatalog_ErrorsKeepTheJSONCause(t *testing.T) {
	_, err := catalogo.ParseCatalog(model.Content{Raw: map[string]any{"categories": make(chan int)}})
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Errorf("un Raw no serializable debe conservar *json.UnsupportedTypeError: %v", err)
	}
	_, err = catalogo.ParseCatalog(model.Content{Raw: rawFromJSON(t, `{"categories":"x"}`)})
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("un blob mal formado debe conservar *json.UnmarshalTypeError: %v", err)
	}
}

// TestParseCatalog_IgnoresTheRestOfContent: solo se lee Raw; Prompt, Options e
// Items del Content no entran en el catálogo.
func TestParseCatalog_IgnoresTheRestOfContent(t *testing.T) {
	content := model.Content{
		Prompt:  "p",
		Options: map[string]string{"1": "x"},
		Items:   []model.ContentItem{{Code: "9", SKU: "OTRO", Label: "Otro", Price: 9}},
		Raw:     rawFromJSON(t, oneArticleBlob("", "")),
	}
	cat, err := catalogo.ParseCatalog(content)
	if err != nil {
		t.Fatalf("ParseCatalog = error %v", err)
	}
	assertArticle(t, onlyArticle(t, cat), baseArticle())
}
