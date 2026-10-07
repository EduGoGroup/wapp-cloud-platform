package catalogimport_test

import (
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// parseCurrent construye el lado VIEJO del diff como se construye en producción:
// desde el blob crudo de tenant_content y con el MISMO parser tolerante del motor.
// Armar un catalogo.Catalog a mano se saltaría justo lo que el hueco #6 pone en
// duda —qué descarta el parseo— y el test dejaría de hablar del sistema real.
func parseCurrent(t *testing.T, blob string) catalogo.Catalog {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		t.Fatalf("blob de catálogo vigente mal escrito en el test: %v", err)
	}
	cat, err := catalogo.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		t.Fatalf("parseando el catálogo vigente del test: %v", err)
	}
	return cat
}

// assertDiff compara el diff ENTERO, serializado: las cuatro listas, el contador y
// los avisos a la vez, porque el error típico de un diff no es equivocarse en una
// lista, es contar un artículo en dos. De paso sujeta las etiquetas JSON.
func assertDiff(t *testing.T, got catalogimport.Diff, want string) {
	t.Helper()
	if s := compactJSON(t, got); s != want {
		t.Errorf("diff = %s\nse esperaba %s", s, want)
	}
}

// currentFixture es el catálogo vigente de los tests: cinco artículos repartidos en
// dos categorías, uno con tags y otro con variantes.
const currentFixture = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.5},
    {"code":"2","sku":"TE","label":"Té","price":2},
    {"code":"3","sku":"JUGO","label":"Jugo de naranja","price":3}
  ]},
  {"code":"2","label":"Postres","items":[
    {"code":"1","sku":"FLAN","label":"Flan","price":3,"tags":["frio"]},
    {"code":"2","sku":"TORTA","label":"Torta","price":10,
     "variants":[{"code":"V1","label":"chica","price":10},{"code":"V2","label":"grande","price":18}]}
  ]}
]}`

// nextFixture es el documento que se sube contra currentFixture: CAFE sube de
// precio, TE no se toca, JUGO desaparece, FLAN gana un tag, TORTA sube el precio de
// una variante y entra AGUA.
const nextFixture = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.9},
    {"code":"2","sku":"TE","label":"Té","price":2},
    {"code":"3","sku":"AGUA","label":"Agua mineral","price":1.5}
  ]},
  {"code":"2","label":"Postres","items":[
    {"code":"1","sku":"FLAN","label":"Flan","price":3,"tags":["frio","casero"]},
    {"code":"2","sku":"TORTA","label":"Torta","price":10,
     "variants":[{"code":"V1","label":"chica","price":10},{"code":"V2","label":"grande","price":20}]}
  ]}
]}`

// TestDiffCatalog_AgainstFixture: «X precios cambian, Y nuevos, Z desaparecen»
// calculado contra un catálogo vigente real. La etiqueta de una BAJA sale del
// catálogo VIEJO; FLAN (tags) y TORTA (precio de variante) son «detalle», no cambio
// de precio del artículo.
func TestDiffCatalog_AgainstFixture(t *testing.T) {
	d := catalogimport.DiffCatalog(parseCurrent(t, currentFixture), mustBody(t, nextFixture))

	assertDiff(t, d, `{"price_changes":[{"sku":"CAFE","label":"Café","old_price":2.5,"new_price":2.9}],`+
		`"added":[{"sku":"AGUA","label":"Agua mineral"}],`+
		`"removed":[{"sku":"JUGO","label":"Jugo de naranja"}],`+
		`"changed_details":["FLAN","TORTA"],"unchanged":1}`)

	want := catalogimport.PriceChange{SKU: "CAFE", Label: "Café", OldPrice: 2.5, NewPrice: 2.9}
	if len(d.PriceChanges) != 1 || d.PriceChanges[0] != want {
		t.Errorf("price_changes = %+v; se esperaba solo %+v", d.PriceChanges, want)
	}
	if ref := (catalogimport.ItemRef{SKU: "AGUA", Label: "Agua mineral"}); len(d.Added) != 1 || d.Added[0] != ref {
		t.Errorf("added = %+v; se esperaba solo %+v", d.Added, ref)
	}
	if d.Empty() {
		t.Error("Empty() = true con cinco diferencias: el resumen de «no cambia nada» mentiría")
	}
}

// TestDiffCatalog_NoCurrentCatalogMeansEverythingIsAdded (hueco #7): sin lado viejo,
// el documento entero es alta, ordenada por sku. Ni una baja ni un error; y las
// listas vacías son [], no null.
func TestDiffCatalog_NoCurrentCatalogMeansEverythingIsAdded(t *testing.T) {
	d := catalogimport.DiffCatalog(catalogo.Catalog{}, mustBody(t, nextFixture))

	assertDiff(t, d, `{"price_changes":[],"added":[{"sku":"AGUA","label":"Agua mineral"},{"sku":"CAFE","label":"Café"},`+
		`{"sku":"FLAN","label":"Flan"},{"sku":"TE","label":"Té"},{"sku":"TORTA","label":"Torta"}],`+
		`"removed":[],"changed_details":[],"unchanged":0}`)
}

// TestDiffCatalog_SameCatalogIsEmpty: subir lo que ya hay no cambia nada, y lo dice
// contando los que quedan igual.
func TestDiffCatalog_SameCatalogIsEmpty(t *testing.T) {
	d := catalogimport.DiffCatalog(parseCurrent(t, currentFixture), mustBody(t, currentFixture))

	assertDiff(t, d, `{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":5}`)
	if !d.Empty() {
		t.Error("Empty() = false con el mismo catálogo en los dos lados")
	}
}

// TestDiffCatalog_EmptyDocumentAgainstNothing: los dos lados vacíos dan el diff
// vacío con sus listas presentes.
func TestDiffCatalog_EmptyDocumentAgainstNothing(t *testing.T) {
	d := catalogimport.DiffCatalog(catalogo.Catalog{}, catalogimport.ImportBody{})

	assertDiff(t, d, `{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":0}`)
}

// TestDiffCatalog_WhatTheEngineDiscardsIsWarned es el hueco #6, el que de verdad
// muerde. El catálogo vigente tiene un artículo con sku reservado y un campo v2 mal
// escrito: el parseo tolerante los tira, así que NO están en el lado viejo y NO
// pueden salir en removed aunque desaparezcan de verdad al aplicar. El test afirma
// las dos mitades: que no salen en removed (la trampa) y que current_warnings los
// nombra (la salvaguarda). TE conserva su código "3": renumerarlo sería un cambio
// real y taparía lo que este test aísla.
func TestDiffCatalog_WhatTheEngineDiscardsIsWarned(t *testing.T) {
	current := parseCurrent(t, `{"categories":[{"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.5},
    {"code":"2","sku":"_ENVIO","label":"Envío","price":5},
    {"code":"3","sku":"TE","label":"Té","price":2,"tags":"decoracion"}]}]}`)
	next := mustBody(t, `{"categories":[{"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.5},
    {"code":"3","sku":"TE","label":"Té","price":2}]}]}`)

	d := catalogimport.DiffCatalog(current, next)

	assertDiff(t, d, `{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":2,"current_warnings":[`+
		`"catálogo vigente · categoría \"1\" · artículo \"_ENVIO\" · campo \"sku\": el prefijo \"_\" está reservado para las líneas del sistema: el artículo se descarta (el motor ya lo ignoraba, así que no entra en la comparación de arriba)",`+
		`"catálogo vigente · categoría \"1\" · artículo \"TE\" · campo \"tags\": no es una lista de textos: se ignoran las etiquetas del artículo (el motor ya lo ignoraba, así que no entra en la comparación de arriba)"]}`)
	if !d.Empty() {
		t.Error("Empty() = false: los avisos del catálogo vigente no son un cambio")
	}
}

// TestDiffCatalog_WarningLinesNameOnlyWhatTheWarningBrings: cada línea dice dónde
// estaba el defecto con lo que el aviso traiga —categoría, artículo, campo— y
// termina recordando que eso ya no está en la comparación.
func TestDiffCatalog_WarningLinesNameOnlyWhatTheWarningBrings(t *testing.T) {
	const tail = " (el motor ya lo ignoraba, así que no entra en la comparación de arriba)"
	current := catalogo.Catalog{Warnings: []catalogo.CatalogWarning{
		{Category: "1", SKU: "X", Field: "tags", Reason: "motivo uno"},
		{Category: "1", Reason: "motivo dos"},
		{Reason: "motivo tres"},
		{SKU: "X", Field: "sku"},
	}}

	d := catalogimport.DiffCatalog(current, catalogimport.ImportBody{})

	want := []string{
		`catálogo vigente · categoría "1" · artículo "X" · campo "tags": motivo uno` + tail,
		`catálogo vigente · categoría "1": motivo dos` + tail,
		`catálogo vigente: motivo tres` + tail,
		`catálogo vigente · artículo "X" · campo "sku": ` + tail,
	}
	if got := compactJSON(t, d.CurrentWarnings); got != compactJSON(t, want) {
		t.Errorf("current_warnings = %s\nse esperaba %s", got, compactJSON(t, want))
	}
}

// TestDiffCatalog_PriceAndDetailCanChangeTogether fija la frontera de D-041.7: el
// precio del artículo, y solo él, va a price_changes; lo demás, a changed_details.
// SOLO cambia de precio, de nombre y de descripción: sale en las dos listas, con la
// etiqueta NUEVA. COMBO e IGUAL están idénticos.
func TestDiffCatalog_PriceAndDetailCanChangeTogether(t *testing.T) {
	current := parseCurrent(t, `{"categories":[{"code":"1","label":"Combos","items":[
    {"code":"1","sku":"SOLO","label":"Solo","price":9,"description":"antes","attributes":{"porciones":"2"}},
    {"code":"2","sku":"COMBO","label":"Combo","price":15,"components":[{"sku":"SOLO","qty":2}]},
    {"code":"3","sku":"IGUAL","label":"Igual","price":1,"tags":["a","b"]}]}]}`)
	next := mustBody(t, `{"categories":[{"code":"1","label":"Combos","items":[
    {"code":"1","sku":"SOLO","label":"Solo nuevo","price":11,"description":"ahora","attributes":{"porciones":"2"}},
    {"code":"2","sku":"COMBO","label":"Combo","price":15,"components":[{"sku":"SOLO","qty":2}]},
    {"code":"3","sku":"IGUAL","label":"Igual","price":1,"tags":["a","b"]}]}]}`)

	assertDiff(t, catalogimport.DiffCatalog(current, next),
		`{"price_changes":[{"sku":"SOLO","label":"Solo nuevo","old_price":9,"new_price":11}],`+
			`"added":[],"removed":[],"changed_details":["SOLO"],"unchanged":2}`)
}

// TestDiffCatalog_EveryDetailCounts: cada artículo cambia UN detalle —el orden de
// los tags, el orden de las variantes, un atributo, el código, la subcategoría, la
// qty de un componente, la etiqueta, la descripción— y todos salen, ordenados por
// sku. H-SAME solo cambia de categoría y trae tags y atributos vacíos donde antes
// no traía nada: no es un cambio.
func TestDiffCatalog_EveryDetailCounts(t *testing.T) {
	current := parseCurrent(t, `{"categories":[{"code":"1","label":"C","subcategories":[{"code":"s","label":"S"},{"code":"t","label":"T"}],"items":[
    {"code":"1","sku":"A-TAGS","label":"a","price":1,"tags":["a","b"]},
    {"code":"2","sku":"B-VARS","label":"b","price":1,"variants":[{"code":"V1","label":"x","price":1},{"code":"V2","label":"y","price":2}]},
    {"code":"3","sku":"C-ATTR","label":"c","price":1,"attributes":{"k":"v"}},
    {"code":"4","sku":"D-CODE","label":"d","price":1},
    {"code":"5","sku":"E-SUB","label":"e","price":1,"subcategory":"s"},
    {"code":"6","sku":"F-COMP","label":"f","price":1,"components":[{"sku":"D-CODE","qty":1}]},
    {"code":"7","sku":"G-LABEL","label":"g","price":1},
    {"code":"8","sku":"H-SAME","label":"h","price":1},
    {"code":"9","sku":"I-DESC","label":"i","price":1,"description":"antes"}]}]}`)
	next := mustBody(t, `{"categories":[{"code":"1","label":"Otra etiqueta de categoría","items":[
    {"code":"1","sku":"A-TAGS","label":"a","price":1,"tags":["b","a"]},
    {"code":"2","sku":"B-VARS","label":"b","price":1,"variants":[{"code":"V2","label":"y","price":2},{"code":"V1","label":"x","price":1}]},
    {"code":"3","sku":"C-ATTR","label":"c","price":1,"attributes":{"k":"w"}},
    {"code":"40","sku":"D-CODE","label":"d","price":1},
    {"code":"5","sku":"E-SUB","label":"e","price":1,"subcategory":"t"},
    {"code":"6","sku":"F-COMP","label":"f","price":1,"components":[{"sku":"D-CODE","qty":2}]},
    {"code":"7","sku":"G-LABEL","label":"G","price":1},
    {"code":"9","sku":"I-DESC","label":"i","price":1,"description":"ahora"}]},
    {"code":"2","label":"Movida","items":[{"code":"8","sku":"H-SAME","label":"h","price":1,"attributes":{},"tags":[]}]}]}`)

	assertDiff(t, catalogimport.DiffCatalog(current, next),
		`{"price_changes":[],"added":[],"removed":[],`+
			`"changed_details":["A-TAGS","B-VARS","C-ATTR","D-CODE","E-SUB","F-COMP","G-LABEL","I-DESC"],"unchanged":1}`)
}

// TestDiffCatalog_ImplicitQtyIsNotAChange protege una trampa de la normalización:
// el runtime materializa la qty ausente de un componente como 1, y si el lado nuevo
// no hiciera lo mismo, un combo idéntico se reportaría como cambiado por escribir
// —o dejar de escribir— "qty": 1. Va en los dos sentidos.
func TestDiffCatalog_ImplicitQtyIsNotAChange(t *testing.T) {
	current := parseCurrent(t, `{"categories":[{"code":"1","label":"Combos","items":[
    {"code":"1","sku":"PAN","label":"Pan","price":1},
    {"code":"2","sku":"DESAYUNO","label":"Desayuno","price":5,"components":[{"sku":"PAN"}]},
    {"code":"3","sku":"MERIENDA","label":"Merienda","price":5,"components":[{"sku":"PAN","qty":1}]}]}]}`)
	next := mustBody(t, `{"categories":[{"code":"1","label":"Combos","items":[
    {"code":"1","sku":"PAN","label":"Pan","price":1},
    {"code":"2","sku":"DESAYUNO","label":"Desayuno","price":5,"components":[{"sku":"PAN","qty":1}]},
    {"code":"3","sku":"MERIENDA","label":"Merienda","price":5,"components":[{"sku":"PAN"}]}]}]}`)

	d := catalogimport.DiffCatalog(current, next)

	assertDiff(t, d, `{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":3}`)
	if !d.Empty() {
		t.Error("Empty() = false: el combo es el mismo con la qty implícita")
	}
}

// TestDiffCatalog_RepeatedSKUInCurrentKeepsTheFirst: el validador del import
// prohíbe los duplicados, pero el parseo de runtime es tolerante y un blob viejo
// puede traerlos. El diff describe el artículo que el cliente veía: el primero.
func TestDiffCatalog_RepeatedSKUInCurrentKeepsTheFirst(t *testing.T) {
	current := parseCurrent(t, `{"categories":[
    {"code":"1","label":"A","items":[{"code":"1","sku":"X","label":"Primero","price":1}]},
    {"code":"2","label":"B","items":[{"code":"1","sku":"X","label":"Segundo","price":2}]}]}`)
	next := mustBody(t, `{"categories":[{"code":"1","label":"A","items":[{"code":"1","sku":"X","label":"Primero","price":1}]}]}`)

	assertDiff(t, catalogimport.DiffCatalog(current, next),
		`{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":1}`)
}

// TestDiffCatalog_SKUIsComparedExactly (corpus adversario): un sku al que se le
// pega un espacio de no separación es OTRO sku —una baja y un alta—, no el mismo
// artículo.
func TestDiffCatalog_SKUIsComparedExactly(t *testing.T) {
	current := parseCurrent(t, `{"categories":[{"code":"1","label":"A","items":[{"code":"1","sku":"CAFE","label":"Café","price":1}]}]}`)
	next := catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{{
		Code: "1", Label: "A",
		Items: []catalogimport.ImportItem{{Code: "1", SKU: "CAFE" + nbsp, Label: "Café", Price: 1}},
	}}}

	d := catalogimport.DiffCatalog(current, next)

	if len(d.Added) != 1 || d.Added[0].SKU != "CAFE"+nbsp || len(d.Removed) != 1 || d.Removed[0].SKU != "CAFE" {
		t.Errorf("added = %+v, removed = %+v; se esperaba el alta de \"CAFE\"+nbsp y la baja de \"CAFE\"", d.Added, d.Removed)
	}
	if d.Unchanged != 0 || len(d.ChangedDetails) != 0 || len(d.PriceChanges) != 0 {
		t.Errorf("no hay ningún artículo en común: %+v", d)
	}
}

// TestDiffCatalog_ListsAreSortedBySKU: el orden es el de los bytes del sku —cifras,
// mayúsculas, minúsculas—, no el del documento: dos corridas del mismo import se ven
// iguales.
func TestDiffCatalog_ListsAreSortedBySKU(t *testing.T) {
	next := mustBody(t, `{"categories":[{"code":"1","label":"A","items":[
    {"code":"1","sku":"b","label":"3","price":1},{"code":"2","sku":"B","label":"2","price":1},
    {"code":"3","sku":"10","label":"1","price":1},{"code":"4","sku":"9","label":"0","price":1}]}]}`)

	assertDiff(t, catalogimport.DiffCatalog(catalogo.Catalog{}, next),
		`{"price_changes":[],"added":[{"sku":"10","label":"1"},{"sku":"9","label":"0"},{"sku":"B","label":"2"},{"sku":"b","label":"3"}],`+
			`"removed":[],"changed_details":[],"unchanged":0}`)
}

// TestDiff_EmptyLooksOnlyAtTheFourLists: ni Unchanged ni los avisos del catálogo
// vigente cuentan como cambio; cualquiera de las cuatro listas, sí.
func TestDiff_EmptyLooksOnlyAtTheFourLists(t *testing.T) {
	cases := map[string]struct {
		diff catalogimport.Diff
		want bool
	}{
		"zero value":       {catalogimport.Diff{}, true},
		"only unchanged":   {catalogimport.Diff{Unchanged: 7}, true},
		"only warnings":    {catalogimport.Diff{CurrentWarnings: []string{"aviso"}}, true},
		"a price change":   {catalogimport.Diff{PriceChanges: []catalogimport.PriceChange{{SKU: "A"}}}, false},
		"an added item":    {catalogimport.Diff{Added: []catalogimport.ItemRef{{SKU: "A"}}}, false},
		"a removed item":   {catalogimport.Diff{Removed: []catalogimport.ItemRef{{SKU: "A"}}}, false},
		"a changed detail": {catalogimport.Diff{ChangedDetails: []string{"A"}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.diff.Empty(); got != c.want {
				t.Errorf("Empty() = %v; se esperaba %v", got, c.want)
			}
		})
	}
}
