// catalog_v2_test.go cubre el contrato v2 del catálogo (Plan 041 · T2.2): que los
// cinco campos nuevos se pueblen cuando vienen bien, y —lo que de verdad importa
// en runtime— que cuando vienen mal se descarten CON AVISO sin tumbar el
// catálogo. El validador que rechaza el documento entero es el del import (Ola 3);
// aquí, un tenant con media ficha rota tiene que seguir vendiendo.
package cart

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// parseRaw parsea un literal JSON de catálogo como si llegara del JSONB.
func parseRaw(t *testing.T, s string) Catalog {
	t.Helper()
	cat, err := ParseCatalog(model.Content{Raw: rawFromJSON(t, s)})
	if err != nil {
		t.Fatalf("el catálogo debía parsear pese al campo v2 malformado, pero falló: %v", err)
	}
	return cat
}

// findWarning localiza el aviso de un campo (opcionalmente de un sku concreto).
func findWarning(cat Catalog, sku, field string) (CatalogWarning, bool) {
	for _, w := range cat.Warnings {
		if w.Field == field && (sku == "" || w.SKU == sku) {
			return w, true
		}
	}
	return CatalogWarning{}, false
}

func mustWarning(t *testing.T, cat Catalog, sku, field string) CatalogWarning {
	t.Helper()
	w, ok := findWarning(cat, sku, field)
	if !ok {
		t.Fatalf("faltó el aviso de %q (sku %q); avisos: %+v", field, sku, cat.Warnings)
	}
	return w
}

// --- v1 intacto ------------------------------------------------------------

// TestParseCatalogV1SinAvisos: un blob v1 no produce ni un solo aviso (nada que
// descartar) ni puebla ningún campo v2. El golden de golden_test.go sujeta el
// árbol entero; esto sujeta la ausencia de ruido.
func TestParseCatalogV1SinAvisos(t *testing.T) {
	cat, err := ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v1.json")})
	if err != nil {
		t.Fatalf("blob v1 real: %v", err)
	}
	if len(cat.Warnings) != 0 {
		t.Fatalf("un blob v1 no debe generar avisos, got %+v", cat.Warnings)
	}
	for _, c := range cat.Categories {
		if c.Subcategories != nil {
			t.Errorf("categoría %q pobló Subcategories con un blob v1", c.Code)
		}
		for _, a := range c.Items {
			if a.Subcategory != "" || a.Tags != nil || a.Attributes != nil || a.Variants != nil || a.Components != nil {
				t.Errorf("artículo %q pobló campos v2 con un blob v1: %+v", a.SKU, a)
			}
		}
	}
}

// --- v2 completo -----------------------------------------------------------

// TestParseCatalogV2PueblaLosCincoCampos usa el ejemplo del contrato (D-041.2) y
// comprueba los cinco campos nuevos, más el golden del árbol completo.
func TestParseCatalogV2PueblaLosCincoCampos(t *testing.T) {
	cat, err := ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v2.json")})
	if err != nil {
		t.Fatalf("el catálogo v2 del contrato debe parsear: %v", err)
	}
	if len(cat.Warnings) != 0 {
		t.Fatalf("el catálogo v2 bien formado no debe generar avisos, got %+v", cat.Warnings)
	}

	tortas := cat.Categories[0]
	if len(tortas.Subcategories) != 2 || tortas.Subcategories[0].Code != "01a" || tortas.Subcategories[1].Label != "Clásicas" {
		t.Fatalf("subcategories: %+v", tortas.Subcategories)
	}
	assertTortaConVariantes(t, tortas.Items[0])
	assertComboConComponentes(t, tortas.Items[1])

	assertGolden(t, "catalog_v2_parsed.golden.json", dumpCatalog(t, cat))
}

// assertTortaConVariantes comprueba los cuatro campos v2 del artículo con
// variantes del ejemplo del contrato.
func assertTortaConVariantes(t *testing.T, torta Article) {
	t.Helper()
	if torta.Subcategory != "01b" {
		t.Errorf("subcategory = %q, quiero 01b", torta.Subcategory)
	}
	if len(torta.Tags) != 2 || torta.Tags[0] != "decoracion" || torta.Tags[1] != "sin_lactosa" {
		t.Errorf("tags = %v", torta.Tags)
	}
	if torta.Attributes["porciones"] != "10-12" || torta.Attributes["sabor"] != "chocolate" {
		t.Errorf("attributes = %v", torta.Attributes)
	}
	if !torta.HasVariants() || len(torta.Variants) != 2 {
		t.Fatalf("variants = %+v", torta.Variants)
	}
	if torta.Variants[1].Code != "V2" || torta.Variants[1].Label != "25-30 porciones" || torta.Variants[1].Price != 32000 {
		t.Errorf("variante V2 = %+v", torta.Variants[1])
	}
	if torta.IsCombo() {
		t.Error("la torta no es un combo")
	}
}

// assertComboConComponentes comprueba el quinto campo v2 (components).
func assertComboConComponentes(t *testing.T, combo Article) {
	t.Helper()
	if !combo.IsCombo() || len(combo.Components) != 3 {
		t.Fatalf("components = %+v", combo.Components)
	}
	if combo.Components[2].SKU != "PAPA" || combo.Components[2].Qty != 2 {
		t.Errorf("componente PAPA = %+v", combo.Components[2])
	}
	if combo.HasVariants() {
		t.Error("el combo no tiene variantes")
	}
}

// --- variants XOR components ----------------------------------------------

// TestParseCatalogVariantsXorComponents: con los dos campos en el mismo artículo
// se conservan las VARIANTES (las que cambian lo que se cobra) y se ignoran los
// componentes, con aviso. El import lo rechazará.
func TestParseCatalogVariantsXorComponents(t *testing.T) {
	cat := parseRaw(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"AMBOS","label":"Ambos","price":10,
	   "variants":[{"code":"V1","label":"Chica","price":8}],
	   "components":[{"sku":"OTRO","qty":1}]}
	]}]}`)
	a := cat.Categories[0].Items[0]
	if !a.HasVariants() || len(a.Variants) != 1 {
		t.Fatalf("las variantes se conservan: %+v", a.Variants)
	}
	if a.Components != nil {
		t.Fatalf("los components deben ignorarse, got %+v", a.Components)
	}
	w := mustWarning(t, cat, "AMBOS", fieldComponents)
	if !strings.Contains(w.Reason, "variants y components a la vez") {
		t.Errorf("el aviso debe explicar el conflicto, got %q", w.Reason)
	}
}

// --- tolerancia campo por campo -------------------------------------------

// malformedCase describe un blob con UN campo v2 roto y lo que debe sobrevivir.
type malformedCase struct {
	name  string
	blob  string
	sku   string
	field string
	check func(t *testing.T, a Article)
}

// Los `check` de la tabla viven en funciones con nombre —no en closures dentro
// del literal— para que su complejidad se contabilice aparte del test (mismo
// criterio que catalog_test.go).

func checkSubcategoryVacia(t *testing.T, a Article) {
	t.Helper()
	if a.Subcategory != "" {
		t.Errorf("la referencia colgante debe quedar vacía, got %q", a.Subcategory)
	}
}

func checkTagsVacias(t *testing.T, a Article) {
	t.Helper()
	if a.Tags != nil {
		t.Errorf("tags debe quedar vacío, got %v", a.Tags)
	}
}

func checkAtributoSanoSobrevive(t *testing.T, a Article) {
	t.Helper()
	if a.Attributes["ok"] != "si" || len(a.Attributes) != 1 {
		t.Errorf("el atributo sano sobrevive y el otro se cae, got %v", a.Attributes)
	}
}

func checkSinVariantes(t *testing.T, a Article) {
	t.Helper()
	if a.HasVariants() {
		t.Errorf("sin variantes utilizables el artículo se vende sin ellas, got %+v", a.Variants)
	}
}

func checkSoloSobreviveV2(t *testing.T, a Article) {
	t.Helper()
	if len(a.Variants) != 1 || a.Variants[0].Code != "V2" {
		t.Errorf("solo sobrevive la variante con precio, got %+v", a.Variants)
	}
}

func checkGanaLaPrimeraVariante(t *testing.T, a Article) {
	t.Helper()
	if len(a.Variants) != 1 || a.Variants[0].Label != "Chica" {
		t.Errorf("con code repetido gana la primera, got %+v", a.Variants)
	}
}

func checkSinComponentes(t *testing.T, a Article) {
	t.Helper()
	if a.IsCombo() {
		t.Errorf("components ilegible se ignora entero, got %+v", a.Components)
	}
}

func checkComponenteSaneado(t *testing.T, a Article) {
	t.Helper()
	if len(a.Components) != 1 || a.Components[0].SKU != "OK" || a.Components[0].Qty != 1 {
		t.Errorf("el componente sin sku se cae y la qty inválida se corrige a 1, got %+v", a.Components)
	}
}

func TestParseCatalogToleraCamposV2Malformados(t *testing.T) {
	cases := []malformedCase{
		{
			name:  "subcategories no es una lista",
			blob:  `{"categories":[{"code":"1","label":"X","subcategories":"01a","items":[{"code":"1","sku":"A","label":"A","price":1}]}]}`,
			field: fieldSubcategories,
		},
		{
			name:  "subcategory apunta a una subcategoría inexistente",
			blob:  `{"categories":[{"code":"1","label":"X","subcategories":[{"code":"01a","label":"Uno"}],"items":[{"code":"1","sku":"A","label":"A","price":1,"subcategory":"zzz"}]}]}`,
			sku:   "A",
			field: fieldSubcategory,
			check: checkSubcategoryVacia,
		},
		{
			name:  "subcategory no es texto",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"subcategory":7}]}]}`,
			sku:   "A",
			field: fieldSubcategory,
		},
		{
			name:  "tags es una cadena en vez de lista",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"tags":"decoracion"}]}]}`,
			sku:   "A",
			field: fieldTags,
			check: checkTagsVacias,
		},
		{
			name:  "attributes es una lista en vez de objeto",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"attributes":["porciones"]}]}]}`,
			sku:   "A",
			field: fieldAttributes,
		},
		{
			name:  "attributes con un valor que no es simple",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"attributes":{"ok":"si","malo":{"a":1}}}]}]}`,
			sku:   "A",
			field: fieldAttributes,
			check: checkAtributoSanoSobrevive,
		},
		{
			name:  "variants no es una lista",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"variants":{"code":"V1"}}]}]}`,
			sku:   "A",
			field: fieldVariants,
			check: checkSinVariantes,
		},
		{
			name:  "variante sin price (obligatorio con variantes)",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"variants":[{"code":"V1","label":"Chica"},{"code":"V2","label":"Grande","price":5}]}]}]}`,
			sku:   "A",
			field: fieldVariants,
			check: checkSoloSobreviveV2,
		},
		{
			name:  "variantes con code repetido",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"variants":[{"code":"V1","label":"Chica","price":5},{"code":"V1","label":"Otra","price":9}]}]}]}`,
			sku:   "A",
			field: fieldVariants,
			check: checkGanaLaPrimeraVariante,
		},
		{
			name:  "components no es una lista",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"components":42}]}]}`,
			sku:   "A",
			field: fieldComponents,
			check: checkSinComponentes,
		},
		{
			name:  "componente sin sku y otro con qty menor que 1",
			blob:  `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1,"components":[{"qty":1},{"sku":"OK","qty":0}]}]}]}`,
			sku:   "A",
			field: fieldComponents,
			check: checkComponenteSaneado,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := parseRaw(t, tc.blob)
			// Lo primero y más importante: el catálogo NO se cayó y sigue vendiendo.
			if len(cat.Categories) != 1 || len(cat.Categories[0].Items) != 1 {
				t.Fatalf("el catálogo debe seguir en pie: %+v", cat)
			}
			w := mustWarning(t, cat, tc.sku, tc.field)
			if w.Reason == "" {
				t.Error("el aviso debe llevar motivo legible")
			}
			if tc.check != nil {
				tc.check(t, cat.Categories[0].Items[0])
			}
		})
	}
}

// --- sku reservado del sistema ---------------------------------------------

// TestParseCatalogDescartaSKUReservado: el prefijo "_" es del sistema (líneas como
// "_shipping"), así que un artículo que lo use se cae del catálogo con aviso — el
// resto sigue vendiéndose.
func TestParseCatalogDescartaSKUReservado(t *testing.T) {
	cat := parseRaw(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"_shipping","label":"Envío","price":3},
	  {"code":"2","sku":"BUENO","label":"Bueno","price":1}
	]}]}`)
	items := cat.Categories[0].Items
	if len(items) != 1 || items[0].SKU != "BUENO" {
		t.Fatalf("solo debe quedar el artículo con sku legítimo, got %+v", items)
	}
	w := mustWarning(t, cat, "_shipping", fieldSKU)
	if !strings.Contains(w.Reason, "reservado") {
		t.Errorf("el aviso debe decir que el prefijo está reservado, got %q", w.Reason)
	}
}

// --- tope de avisos ---------------------------------------------------------

// TestParseCatalogAcotaLosAvisos: un catálogo roto en masa no llena la memoria ni
// el log; los avisos se cortan en el tope y el último dice cuántos faltaron.
func TestParseCatalogAcotaLosAvisos(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"categories":[{"code":"1","label":"X","items":[`)
	for i := 0; i < maxCatalogWarnings+10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"code":"1","sku":"A`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`","label":"A","price":1,"tags":"no-soy-lista"}`)
	}
	b.WriteString(`]}]}`)

	cat := parseRaw(t, b.String())
	if len(cat.Warnings) != maxCatalogWarnings+1 {
		t.Fatalf("avisos = %d, quiero el tope %d más el resumen", len(cat.Warnings), maxCatalogWarnings)
	}
	last := cat.Warnings[len(cat.Warnings)-1]
	if !strings.Contains(last.Reason, "se omitieron 10 avisos más") {
		t.Errorf("el último aviso debe contar los omitidos, got %q", last.Reason)
	}
}
