package catalogimport_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// parseCurrent construye el lado VIEJO del diff como se construye en producción:
// desde el blob crudo de tenant_content y con el MISMO parser tolerante del
// motor. Armar un cart.Catalog a mano en el test se saltaría justo lo que el
// hueco #6 de la ola pone en duda —qué descarta el parseo— y el test dejaría de
// hablar del sistema real.
func parseCurrent(t *testing.T, blob string) cart.Catalog {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		t.Fatalf("blob de catálogo vigente mal escrito en el test: %v", err)
	}
	cat, err := cart.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		t.Fatalf("parseando el catálogo vigente del test: %v", err)
	}
	return cat
}

// fixtureCurrent es el catálogo vigente de los tests: cinco artículos repartidos
// en dos categorías, uno con tags y otro con variantes.
const fixtureCurrent = `{"categories":[
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

// fixtureNext es el documento que se sube contra fixtureCurrent: CAFE sube de
// precio, TE no se toca, JUGO desaparece, FLAN gana un tag, TORTA sube el precio
// de una variante y entra AGUA.
func fixtureNext() catalogimport.ImportBody {
	return catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{
		{Code: "1", Label: "Bebidas", Items: []catalogimport.ImportItem{
			{Code: "1", SKU: "CAFE", Label: "Café", Price: 2.9},
			{Code: "2", SKU: "TE", Label: "Té", Price: 2},
			{Code: "3", SKU: "AGUA", Label: "Agua mineral", Price: 1.5},
		}},
		{Code: "2", Label: "Postres", Items: []catalogimport.ImportItem{
			{Code: "1", SKU: "FLAN", Label: "Flan", Price: 3, Tags: []string{"frio", "casero"}},
			{Code: "2", SKU: "TORTA", Label: "Torta", Price: 10, Variants: []catalogimport.ImportVariant{
				{Code: "V1", Label: "chica", Price: 10},
				{Code: "V2", Label: "grande", Price: 20},
			}},
		}},
	}}
}

// TestDiffCatalog_ContraFixture es el criterio literal de T3.3: «X precios
// cambian, Y nuevos, Z desaparecen» calculado contra un catálogo vigente real.
// Comprueba las CUATRO listas a la vez porque el error típico de un diff no es
// equivocarse en una, es contar un artículo en dos.
func TestDiffCatalog_ContraFixture(t *testing.T) {
	d := catalogimport.DiffCatalog(parseCurrent(t, fixtureCurrent), fixtureNext())

	wantSolePriceChange(t, d, catalogimport.PriceChange{SKU: "CAFE", Label: "Café", OldPrice: 2.5, NewPrice: 2.9})
	wantSoleRef(t, "added", d.Added, catalogimport.ItemRef{SKU: "AGUA", Label: "Agua mineral"})
	// La etiqueta de una BAJA sale del catálogo VIEJO: es como se llamaba lo que
	// desaparece, y el documento nuevo ya no la trae.
	wantSoleRef(t, "removed", d.Removed, catalogimport.ItemRef{SKU: "JUGO", Label: "Jugo de naranja"})

	// FLAN cambia de tags y TORTA de precio de variante: los dos son «detalle»,
	// ninguno es un cambio de precio del artículo (D-041.7).
	if !slices.Equal(d.ChangedDetails, []string{"FLAN", "TORTA"}) {
		t.Fatalf("changed_details=%v, quiero [FLAN TORTA] en orden de sku", d.ChangedDetails)
	}
	if d.Unchanged != 1 {
		t.Fatalf("unchanged=%d, quiero 1 (solo TE queda igual)", d.Unchanged)
	}
	if d.Empty() {
		t.Fatal("Empty()=true con seis diferencias: el resumen de «no cambia nada» mentiría")
	}
	if len(d.CurrentWarnings) != 0 {
		t.Fatalf("current_warnings=%v, el catálogo vigente del fixture está impecable", d.CurrentWarnings)
	}
}

// wantSolePriceChange exige que el diff traiga EXACTAMENTE un cambio de precio y
// que sea el esperado, campo a campo.
func wantSolePriceChange(t *testing.T, d catalogimport.Diff, want catalogimport.PriceChange) {
	t.Helper()
	if len(d.PriceChanges) != 1 {
		t.Fatalf("price_changes=%+v, quiero exactamente uno (%s)", d.PriceChanges, want.SKU)
	}
	if d.PriceChanges[0] != want {
		t.Fatalf("price_changes[0]=%+v, quiero %+v", d.PriceChanges[0], want)
	}
}

// wantSoleRef exige que una lista de altas/bajas traiga exactamente la referencia
// esperada, con su etiqueta.
func wantSoleRef(t *testing.T, name string, got []catalogimport.ItemRef, want catalogimport.ItemRef) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s=%+v, quiero exactamente uno (%s)", name, got, want.SKU)
	}
	if got[0] != want {
		t.Fatalf("%s[0]=%+v, quiero %+v", name, got[0], want)
	}
}

// TestDiffCatalog_SinCatalogoVigente_TodoEsAlta cubre el hueco #7 por su lado
// puro: sin lado viejo, el documento entero es alta. Ni una baja (no había nada
// que perder) ni un error.
func TestDiffCatalog_SinCatalogoVigente_TodoEsAlta(t *testing.T) {
	d := catalogimport.DiffCatalog(cart.Catalog{}, fixtureNext())

	if len(d.Added) != 5 {
		t.Fatalf("added=%d artículos, quiero los 5 del documento", len(d.Added))
	}
	if len(d.Removed) != 0 || len(d.PriceChanges) != 0 || len(d.ChangedDetails) != 0 || d.Unchanged != 0 {
		t.Fatalf("sin catálogo vigente todo es alta, pero salió %+v", d)
	}
}

// TestDiffCatalog_LoQueElMotorDESCARTA_SeAvisa es el hueco #6, el que de verdad
// muerde. El catálogo vigente tiene un artículo con sku reservado («_ENVIO») y un
// campo v2 mal escrito: el parseo tolerante los tira, así que NO están en el lado
// viejo y NO pueden salir en `removed` aunque desaparezcan de verdad al aplicar.
//
// El test afirma las dos mitades: que efectivamente no salen en removed (la
// trampa) y que CurrentWarnings los nombra (la salvaguarda). Quitar el volcado de
// avisos deja pasar la primera mitad y rompe este test, que es exactamente lo que
// se quiere.
func TestDiffCatalog_LoQueElMotorDESCARTA_SeAvisa(t *testing.T) {
	current := parseCurrent(t, `{"categories":[
	  {"code":"1","label":"Bebidas","items":[
	    {"code":"1","sku":"CAFE","label":"Café","price":2.5},
	    {"code":"2","sku":"_ENVIO","label":"Envío","price":5},
	    {"code":"3","sku":"TE","label":"Té","price":2,"tags":"decoracion"}
	  ]}
	]}`)

	// TE conserva su código "3" (el "2" era del artículo descartado): renumerarlo
	// sería un cambio real y taparía lo que este test aísla, que son los avisos.
	next := catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{
		{Code: "1", Label: "Bebidas", Items: []catalogimport.ImportItem{
			{Code: "1", SKU: "CAFE", Label: "Café", Price: 2.5},
			{Code: "3", SKU: "TE", Label: "Té", Price: 2},
		}},
	}}
	d := catalogimport.DiffCatalog(current, next)

	if len(d.Removed) != 0 {
		t.Fatalf("removed=%+v: el artículo descartado NO puede aparecer aquí (el parser ya lo había tirado)", d.Removed)
	}
	if len(d.CurrentWarnings) != 2 {
		t.Fatalf("current_warnings=%v, quiero 2 (el sku reservado y los tags mal escritos)", d.CurrentWarnings)
	}
	joined := strings.Join(d.CurrentWarnings, "\n")
	if !strings.Contains(joined, "_ENVIO") {
		t.Fatalf("los avisos no nombran el artículo descartado: %v", d.CurrentWarnings)
	}
	if !strings.Contains(joined, "TE") || !strings.Contains(joined, "tags") {
		t.Fatalf("los avisos no nombran el campo v2 descartado: %v", d.CurrentWarnings)
	}
	// Y el resto del diff sigue siendo correcto: los dos artículos legibles quedan igual.
	if d.Unchanged != 2 || !d.Empty() {
		t.Fatalf("unchanged=%d empty=%v, quiero 2 y true (nada cambia de lo que el motor SÍ ve)", d.Unchanged, d.Empty())
	}
}

// TestDiffCatalog_CambioDeDetalleNoEsCambioDePrecio fija la frontera de D-041.7:
// tags, atributos, componentes, etiqueta y descripción van a changed_details; el
// precio del artículo, y solo él, va a price_changes. Un artículo puede estar en
// las dos listas.
func TestDiffCatalog_CambioDeDetalleNoEsCambioDePrecio(t *testing.T) {
	current := parseCurrent(t, `{"categories":[
	  {"code":"1","label":"Combos","items":[
	    {"code":"1","sku":"SOLO","label":"Solo","price":9,"description":"antes","attributes":{"porciones":"2"}},
	    {"code":"2","sku":"COMBO","label":"Combo","price":15,
	     "components":[{"sku":"SOLO","qty":2}]},
	    {"code":"3","sku":"IGUAL","label":"Igual","price":1,"tags":["a","b"]}
	  ]}
	]}`)

	next := catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{
		{Code: "1", Label: "Combos", Items: []catalogimport.ImportItem{
			// Precio Y descripción: sale en las dos listas.
			{Code: "1", SKU: "SOLO", Label: "Solo", Price: 11, Description: "ahora",
				Attributes: map[string]string{"porciones": "2"}},
			// Mismo combo con la qty EXPLÍCITA donde antes venía implícita al revés:
			// aquí se declara igual que estaba, así que no cambia nada.
			{Code: "2", SKU: "COMBO", Label: "Combo", Price: 15,
				Components: []catalogimport.ImportComponent{{SKU: "SOLO", Qty: 2}}},
			{Code: "3", SKU: "IGUAL", Label: "Igual", Price: 1, Tags: []string{"a", "b"}},
		}},
	}}
	d := catalogimport.DiffCatalog(current, next)

	if len(d.PriceChanges) != 1 || d.PriceChanges[0].SKU != "SOLO" {
		t.Fatalf("price_changes=%+v, quiero solo SOLO", d.PriceChanges)
	}
	if len(d.ChangedDetails) != 1 || d.ChangedDetails[0] != "SOLO" {
		t.Fatalf("changed_details=%v, quiero [SOLO] (la descripción cambió); COMBO e IGUAL están idénticos", d.ChangedDetails)
	}
	if d.Unchanged != 2 {
		t.Fatalf("unchanged=%d, quiero 2 (COMBO e IGUAL)", d.Unchanged)
	}
}

// TestDiffCatalog_QtyImplicitaNoEsCambio protege una trampa concreta de la
// normalización: el runtime materializa la qty ausente de un componente como 1, y
// si el lado nuevo no hiciera lo mismo, un combo idéntico se reportaría como
// cambiado por escribir —o dejar de escribir— `"qty": 1`.
func TestDiffCatalog_QtyImplicitaNoEsCambio(t *testing.T) {
	current := parseCurrent(t, `{"categories":[
	  {"code":"1","label":"Combos","items":[
	    {"code":"1","sku":"PAN","label":"Pan","price":1},
	    {"code":"2","sku":"DESAYUNO","label":"Desayuno","price":5,"components":[{"sku":"PAN"}]}
	  ]}
	]}`)

	next := catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{
		{Code: "1", Label: "Combos", Items: []catalogimport.ImportItem{
			{Code: "1", SKU: "PAN", Label: "Pan", Price: 1},
			{Code: "2", SKU: "DESAYUNO", Label: "Desayuno", Price: 5,
				Components: []catalogimport.ImportComponent{{SKU: "PAN"}}},
		}},
	}}

	if d := catalogimport.DiffCatalog(current, next); !d.Empty() {
		t.Fatalf("diff=%+v, quiero vacío: el combo es el mismo con la qty implícita", d)
	}
}
