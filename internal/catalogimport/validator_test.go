package catalogimport_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// docConNueveDefectos es UN solo documento con NUEVE defectos DISTINTOS, cada uno
// de una regla distinta de D-041.5, repartidos por dos categorías. Es la piedra de
// toque del acumulador: un validador que se pare en el primero devuelve 1 error y
// el test lo delata; uno que valide "por bloques" perderá los de las últimas
// categorías.
//
// Los defectos, en orden de aparición:
//
//  1. precio escrito como texto            (cat 0, art 0)
//  2. artículo sin nombre                  (cat 0, art 1)
//  3. código de artículo repetido          (cat 0, art 2)
//  4. sku repetido                         (cat 0, art 2)
//  5. sku con el prefijo reservado "_"     (cat 0, art 3)
//  6. subcategoría que no existe           (cat 0, art 4)
//  7. variants y components a la vez       (cat 0, art 5)
//  8. componente que apunta a un sku que no está en el catálogo (cat 0, art 5)
//  9. artículo sin precio                  (cat 1, art 2)
const docConNueveDefectos = `{
  "format": "wapp.catalog_import",
  "version": 1,
  "catalog": {
    "categories": [
      {
        "code": "1",
        "label": "Tortas",
        "subcategories": [{"code": "01a", "label": "Infantiles"}],
        "items": [
          {"code": "1", "sku": "TORTA-CHOC", "label": "Torta de chocolate", "price": "18000"},
          {"code": "2", "sku": "TORTA-VAI", "price": 9000},
          {"code": "2", "sku": "TORTA-CHOC", "label": "Torta repetida", "price": 9500},
          {"code": "4", "sku": "_shipping", "label": "Envío a domicilio", "price": 5000},
          {"code": "5", "sku": "TORTA-MIX", "label": "Torta mixta", "price": 12000, "subcategory": "01z"},
          {"code": "6", "sku": "COMBO-X", "label": "Combo fiesta", "price": 20000,
           "variants": [{"code": "V1", "label": "Chica", "price": 15000}],
           "components": [{"sku": "NO-EXISTE", "qty": 1}]}
        ]
      },
      {
        "code": "2",
        "label": "Postres",
        "items": [
          {"code": "1", "sku": "FLAN", "label": "Flan casero", "price": 3000},
          {"code": "2", "sku": "TIRAMISU", "label": "Tiramisú", "price": 4500},
          {"code": "3", "sku": "MOUSSE", "label": "Mousse de maracuyá"}
        ]
      }
    ]
  }
}`

// defectoEsperado es una fila de la lista de errores que el validador debe
// devolver: dónde está y de qué habla.
type defectoEsperado struct {
	categoría int
	artículo  int
	campo     string
	dice      string // fragmento que el mensaje debe contener
}

// TestValidate_AcumulaTodosLosDefectosEnUnaPasada es el criterio de T3.1: un
// documento con 6+ defectos distintos los devuelve TODOS, con sus índices, en UNA
// respuesta.
func TestValidate_AcumulaTodosLosDefectosEnUnaPasada(t *testing.T) {
	esperados := []defectoEsperado{
		{0, 0, "price", "debe ser un número"},
		{0, 1, "label", "no tiene nombre"},
		{0, 2, "code", `el código "2" ya lo usa el artículo 2`},
		{0, 2, "sku", `el sku "TORTA-CHOC" ya lo usa`},
		{0, 3, "sku", `empieza por "_"`},
		{0, 4, "subcategory", `la subcategoría "01z" no está declarada`},
		{0, 5, "variants", "declara variantes y componentes a la vez"},
		{0, 5, "components[0].sku", `el sku "NO-EXISTE" no existe en el catálogo`},
		{1, 2, "price", "no tiene el precio"},
	}

	doc, verr := catalogimport.Validate([]byte(docConNueveDefectos), catalogimport.DefaultLimits())
	if verr == nil {
		t.Fatalf("el documento tiene 9 defectos y Validate lo dio por bueno")
	}
	if len(doc.Catalog.Categories) != 0 {
		t.Errorf("un documento inválido no debe devolver catálogo; devolvió %d categorías", len(doc.Catalog.Categories))
	}
	if len(verr.Errors) != len(esperados) {
		for i, e := range verr.Errors {
			t.Logf("  [%d] cat=%v art=%v campo=%q → %s", i, e.CategoryIndex, e.ItemIndex, e.Field, e.Reason)
		}
		t.Fatalf("se esperaban %d defectos acumulados; llegaron %d", len(esperados), len(verr.Errors))
	}

	for i, esp := range esperados {
		got := verr.Errors[i]
		if idx(got.CategoryIndex) != esp.categoría || idx(got.ItemIndex) != esp.artículo {
			t.Errorf("defecto %d: ubicación (cat=%v, art=%v); se esperaba (cat=%d, art=%d) — %s",
				i, got.CategoryIndex, got.ItemIndex, esp.categoría, esp.artículo, got.Reason)
		}
		if got.Field != esp.campo {
			t.Errorf("defecto %d: campo %q; se esperaba %q — %s", i, got.Field, esp.campo, got.Reason)
		}
		if !strings.Contains(got.Reason, esp.dice) {
			t.Errorf("defecto %d: el motivo %q no dice %q", i, got.Reason, esp.dice)
		}
	}
}

// TestValidate_MensajesLegiblesPorUnHumano comprueba lo que el criterio pide de
// verdad: que el motivo nombre el ARTÍCULO y su CATEGORÍA por su nombre, no por un
// índice, y que esté en español. Un "field price: required" cumpliría el contrato
// y no serviría a quien tiene que arreglar el archivo.
func TestValidate_MensajesLegiblesPorUnHumano(t *testing.T) {
	_, verr := catalogimport.Validate([]byte(docConNueveDefectos), catalogimport.DefaultLimits())
	if verr == nil {
		t.Fatal("el documento es inválido; Validate no devolvió errores")
	}
	sinPrecio := verr.Errors[len(verr.Errors)-1] // el último: el Mousse sin precio
	const quiere = `el artículo 3 ("Mousse de maracuyá") de la categoría "Postres" no tiene el precio`
	if !strings.HasPrefix(sinPrecio.Reason, quiere) {
		t.Errorf("el motivo debería empezar por %q; fue %q", quiere, sinPrecio.Reason)
	}
	for i, e := range verr.Errors {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("defecto %d sin motivo", i)
		}
		if e.Field == "" {
			t.Errorf("defecto %d sin campo", i)
		}
	}
}

// docVálido ejercita el contrato v2 ENTERO: subcategorías, etiquetas, atributos,
// variantes y un combo cuyos componentes existen de verdad.
const docVálido = `{
  "format": "wapp.catalog_import",
  "version": 1,
  "source": {"kind": "llm", "model": "un-modelo", "hint": "lista del dueño"},
  "catalog": {
    "categories": [
      {
        "code": "1",
        "label": "Tortas",
        "subcategories": [{"code": "01a", "label": "Infantiles"}],
        "items": [
          {"code": "1", "sku": "TORTA-CHOC", "label": "Torta de chocolate", "price": 18000,
           "description": "Bizcocho húmedo", "subcategory": "01a",
           "tags": ["sin_lactosa", "decoracion"],
           "attributes": {"porciones": "10-12", "capas": 3},
           "variants": [
             {"code": "V1", "label": "10-12 porciones", "price": 18000},
             {"code": "V2", "label": "25-30 porciones", "price": 32000}
           ]}
        ]
      },
      {
        "code": "2",
        "label": "Combos",
        "items": [
          {"code": "1", "sku": "HAMB", "label": "Hamburguesa", "price": 6000},
          {"code": "2", "sku": "REFR", "label": "Refresco", "price": 2000},
          {"code": "3", "sku": "COMBO-1", "label": "Combo hamburguesa", "price": 7500,
           "components": [{"sku": "HAMB", "qty": 1}, {"sku": "REFR"}]}
        ]
      }
    ]
  }
}`

// TestValidate_DocumentoVálido acepta el contrato v2 completo y devuelve el
// documento tipado con lo que traía.
func TestValidate_DocumentoVálido(t *testing.T) {
	doc, verr := catalogimport.Validate([]byte(docVálido), catalogimport.DefaultLimits())
	if verr != nil {
		for _, e := range verr.Errors {
			t.Logf("  campo=%q → %s", e.Field, e.Reason)
		}
		t.Fatalf("el documento es válido y Validate devolvió %d defectos", len(verr.Errors))
	}
	if doc.Format != catalogimport.ImportFormat || doc.Version != catalogimport.ImportVersion {
		t.Fatalf("cabecera mal resuelta: format=%q version=%d", doc.Format, doc.Version)
	}
	if doc.Source == nil || doc.Source.Kind != "llm" {
		t.Errorf("la procedencia declarada se perdió: %+v", doc.Source)
	}
	torta := doc.Catalog.Categories[0].Items[0]
	if len(torta.Variants) != 2 || torta.Variants[1].Price != 32000 {
		t.Errorf("las variantes no se poblaron: %+v", torta.Variants)
	}
	// El atributo numérico se guarda como texto: es lo MISMO que hace el runtime.
	if torta.Attributes["capas"] != "3" {
		t.Errorf(`attributes["capas"] = %q; se esperaba "3"`, torta.Attributes["capas"])
	}
	combo := doc.Catalog.Categories[1].Items[2]
	if len(combo.Components) != 2 || combo.Components[1].Qty != 1 {
		t.Errorf("los componentes no se poblaron (qty ausente vale 1): %+v", combo.Components)
	}
}

// TestValidate_LoQueValidaLoParseaElRuntimeSinUnSoloAviso amarra los dos modelos.
// El validador es ESTRICTO y el parseo del runtime es TOLERANTE, pero no pueden
// contradecirse: si un documento que el import acepta produjera avisos en
// cart.ParseCatalog, el dueño estaría publicando un catálogo con partes que el
// motor descarta en silencio, y el import no habría servido de nada.
func TestValidate_LoQueValidaLoParseaElRuntimeSinUnSoloAviso(t *testing.T) {
	doc, verr := catalogimport.Validate([]byte(docVálido), catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("el documento es válido: %v", verr)
	}

	// El blob que se escribiría en tenant_content es doc.Catalog tal cual: mismo
	// contrato v2 (D-041.2), sin traducción intermedia.
	blob, err := json.Marshal(doc.Catalog)
	if err != nil {
		t.Fatalf("no se pudo serializar el catálogo: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("no se pudo releer el blob: %v", err)
	}
	cat, err := cart.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		t.Fatalf("el runtime rechazó un catálogo que el import dio por bueno: %v", err)
	}
	if len(cat.Warnings) != 0 {
		t.Fatalf("el runtime descartó partes de un catálogo válido: %+v", cat.Warnings)
	}
	if len(cat.Categories) != 2 || len(cat.Categories[0].Items[0].Variants) != 2 {
		t.Fatalf("el árbol del runtime no cuadra con el documento: %+v", cat.Categories)
	}
	if cat.Categories[1].Items[2].Components[1].Qty != 1 {
		t.Errorf("la qty ausente debe valer 1 también en el runtime: %+v", cat.Categories[1].Items[2].Components)
	}
}

// TestValidate_CabeceraDesconocida_NoInterpretaElCuerpo: con un format o una
// version que no son los suyos, el validador se planta en la cabecera. Aplicar las
// reglas de la v1 a un documento que dice ser otra cosa produciría una lista de
// errores inventados sobre campos que en SU contrato quizá ni existen.
func TestValidate_CabeceraDesconocida_NoInterpretaElCuerpo(t *testing.T) {
	casos := map[string]struct {
		doc  string
		dice string
	}{
		"format de otra cosa": {
			doc:  `{"format":"edugo.assessment_import","version":1,"catalog":{"categories":[]}}`,
			dice: "solo se importan catálogos",
		},
		"version futura": {
			doc:  `{"format":"wapp.catalog_import","version":7,"catalog":{"categories":[]}}`,
			dice: "es de la versión 7",
		},
		"sin format": {
			doc:  `{"version":1,"catalog":{"categories":[]}}`,
			dice: "no dice qué es",
		},
		"sin version": {
			doc:  `{"format":"wapp.catalog_import","catalog":{"categories":[]}}`,
			dice: "no dice de qué versión es",
		},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, verr := catalogimport.Validate([]byte(caso.doc), catalogimport.DefaultLimits())
			if verr == nil {
				t.Fatal("se esperaba un rechazo de cabecera")
			}
			// El cuerpo trae "categories": [] —un defecto de cuerpo— y NO debe
			// reportarse: la cabecera corta antes.
			if len(verr.Errors) != 1 {
				t.Fatalf("se esperaba 1 defecto de cabecera; llegaron %d: %+v", len(verr.Errors), verr.Errors)
			}
			if got := verr.Errors[0]; !strings.Contains(got.Reason, caso.dice) {
				t.Errorf("el motivo %q no dice %q", got.Reason, caso.dice)
			}
			if verr.Errors[0].CategoryIndex != nil || verr.Errors[0].ItemIndex != nil {
				t.Errorf("un defecto de cabecera no lleva índices: %+v", verr.Errors[0])
			}
		})
	}
}

// TestValidate_ArchivoInservible cubre lo que llega cuando alguien pega mal: nada,
// texto que no es JSON, o un JSON que no es un objeto.
func TestValidate_ArchivoInservible(t *testing.T) {
	casos := map[string]struct{ doc, dice string }{
		"vacío":            {"   \n ", "está vacío"},
		"solo texto":       {"esto no es json", "no es un JSON válido"},
		"llave sin cerrar": {"{\n  \"format\": \"wapp.catalog_import\",\n  \"version\": 1", "hacia la línea 3"},
		"lista":            {`[{"format":"wapp.catalog_import"}]`, "debe ser un objeto JSON"},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, verr := catalogimport.Validate([]byte(caso.doc), catalogimport.DefaultLimits())
			if verr == nil || len(verr.Errors) != 1 {
				t.Fatalf("se esperaba 1 defecto; llegó %+v", verr)
			}
			if !strings.Contains(verr.Errors[0].Reason, caso.dice) {
				t.Errorf("el motivo %q no dice %q", verr.Errors[0].Reason, caso.dice)
			}
		})
	}
}

// TestValidate_ReglasDeCampo recorre una a una las reglas de D-041.5 que no entran
// en el documento de los nueve defectos, cada caso con UN defecto para que el
// mensaje que se comprueba sea inequívoco.
func TestValidate_ReglasDeCampo(t *testing.T) {
	casos := map[string]struct {
		item string // el artículo que se inyecta en la categoría
		cat  string // o la categoría entera, si el caso es de categoría
		dice string
	}{
		"sin sku":                 {item: `{"code":"1","label":"Café","price":100}`, dice: "no tiene el sku"},
		"sin código":              {item: `{"sku":"CAFE","label":"Café","price":100}`, dice: "no tiene el código"},
		"código numérico":         {item: `{"code":1,"sku":"CAFE","label":"Café","price":100}`, dice: "debe ir entre comillas"},
		"precio negativo":         {item: `{"code":"1","sku":"CAFE","label":"Café","price":-5}`, dice: "no puede ser negativo"},
		"etiquetas como texto":    {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"tags":"decoracion"}`, dice: "deben ser una lista de textos"},
		"etiqueta vacía":          {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"tags":["  "]}`, dice: "la etiqueta 1 está vacía"},
		"atributo compuesto":      {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"attributes":{"origen":["co","br"]}}`, dice: "debe ser un texto o un número"},
		"atributos como lista":    {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"attributes":["origen"]}`, dice: "objeto de pares clave→valor"},
		"variante sin precio":     {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"variants":[{"code":"V1","label":"Chico"}]}`, dice: "variante 1 no tiene el precio"},
		"variante repetida":       {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"variants":[{"code":"V1","label":"Chico","price":1},{"code":"V1","label":"Grande","price":2}]}`, dice: "ya lo usa otra variante"},
		"variantes vacías":        {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"variants":[]}`, dice: "lista de variantes vacía"},
		"componente fraccionado":  {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"components":[{"sku":"CAFE","qty":1.5}]}`, dice: "número entero de 1 o más"},
		"componentes vacíos":      {item: `{"code":"1","sku":"CAFE","label":"Café","price":100,"components":[]}`, dice: "lista de componentes vacía"},
		"categoría sin nombre":    {cat: `{"code":"1","items":[{"code":"1","sku":"CAFE","label":"Café","price":100}]}`, dice: "no tiene nombre"},
		"categoría sin código":    {cat: `{"label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":100}]}`, dice: "no tiene código"},
		"categoría sin artículos": {cat: `{"code":"1","label":"Bebidas","items":[]}`, dice: "callejón sin salida"},
		"items no es lista":       {cat: `{"code":"1","label":"Bebidas","items":{"code":"1"}}`, dice: `"items" debe ser una lista`},
		"subcategoría sin código": {cat: `{"code":"1","label":"Bebidas","subcategories":[{"label":"Frías"}],"items":[{"code":"1","sku":"CAFE","label":"Café","price":100}]}`, dice: "subcategoría 1 no tiene el código"},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			categoría := caso.cat
			if categoría == "" {
				categoría = `{"code":"1","label":"Bebidas","items":[` + caso.item + `]}`
			}
			doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` + categoría + `]}}`
			_, verr := catalogimport.Validate([]byte(doc), catalogimport.DefaultLimits())
			if verr == nil {
				t.Fatalf("se esperaba un defecto que dijera %q; el documento pasó", caso.dice)
			}
			if len(verr.Errors) != 1 {
				t.Fatalf("se esperaba 1 defecto; llegaron %d: %+v", len(verr.Errors), verr.Errors)
			}
			if !strings.Contains(verr.Errors[0].Reason, caso.dice) {
				t.Errorf("el motivo %q no dice %q", verr.Errors[0].Reason, caso.dice)
			}
		})
	}
}

// TestValidate_CategoríasRepetidasYCatálogoVacío cubre las dos reglas del cuerpo
// que no son de artículo.
func TestValidate_CategoríasRepetidasYCatálogoVacío(t *testing.T) {
	t.Run("sin categorías", func(t *testing.T) {
		doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[]}}`
		_, verr := catalogimport.Validate([]byte(doc), catalogimport.DefaultLimits())
		if verr == nil || !strings.Contains(verr.Errors[0].Reason, "no trae ninguna categoría") {
			t.Fatalf("se esperaba el rechazo del catálogo vacío; llegó %+v", verr)
		}
	})
	t.Run("código de categoría repetido", func(t *testing.T) {
		item := `{"code":"1","sku":"%s","label":"Algo","price":1}`
		doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
			`{"code":"1","label":"Bebidas","items":[` + strings.Replace(item, "%s", "A", 1) + `]},` +
			`{"code":"1","label":"Postres","items":[` + strings.Replace(item, "%s", "B", 1) + `]}]}}`
		_, verr := catalogimport.Validate([]byte(doc), catalogimport.DefaultLimits())
		if verr == nil || len(verr.Errors) != 1 {
			t.Fatalf("se esperaba 1 defecto; llegó %+v", verr)
		}
		if got := verr.Errors[0]; idx(got.CategoryIndex) != 1 || !strings.Contains(got.Reason, "ya lo usa la categoría 1") {
			t.Errorf("el defecto debería señalar la SEGUNDA categoría: %+v", got)
		}
	})
}

// TestValidate_TopeDeArtículos_SeDetieneYLoDice: pasado el tope, la validación no
// sigue. Devolver además los defectos de 600 artículos de un archivo que se va a
// rechazar de todos modos es ruido, no ayuda.
func TestValidate_TopeDeArtículos_SeDetieneYLoDice(t *testing.T) {
	items := make([]string, 0, 4)
	for i := range 4 {
		// Todos rotos a propósito: si el tope no cortara, saldrían sus defectos.
		items = append(items, `{"code":"`+strconv.Itoa(i)+`"}`)
	}
	doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[` + strings.Join(items, ",") + `]}]}}`

	_, verr := catalogimport.Validate([]byte(doc), catalogimport.Limits{MaxItems: 3})
	if verr == nil {
		t.Fatal("4 artículos con el tope en 3 debe rechazarse")
	}
	if len(verr.Errors) != 1 {
		t.Fatalf("se esperaba solo el defecto del tope; llegaron %d: %+v", len(verr.Errors), verr.Errors)
	}
	const quiere = "el catálogo trae 4 artículos y el máximo por importación es 3"
	if !strings.Contains(verr.Errors[0].Reason, quiere) {
		t.Errorf("el motivo %q no dice %q", verr.Errors[0].Reason, quiere)
	}
}

// TestValidate_TopeDeDefectos_NoDevuelveMilesYAvisa: un documento roto de cabo a
// rabo se corta en 200 defectos y dice cuántos quedaron fuera. Sin ese tope, la
// respuesta y la pantalla se vuelven inmanejables justo cuando más ayuda hace
// falta.
func TestValidate_TopeDeDefectos_NoDevuelveMilesYAvisa(t *testing.T) {
	items := make([]string, 0, 300)
	for i := range 300 {
		items = append(items, `{"code":"`+strconv.Itoa(i)+`"}`) // sin sku, sin label, sin precio
	}
	doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[` + strings.Join(items, ",") + `]}]}}`

	_, verr := catalogimport.Validate([]byte(doc), catalogimport.DefaultLimits())
	if verr == nil {
		t.Fatal("300 artículos rotos deben rechazarse")
	}
	if len(verr.Errors) != 201 { // 200 detallados + el que dice cuántos faltan
		t.Fatalf("se esperaban 201 entradas (200 + resumen); llegaron %d", len(verr.Errors))
	}
	último := verr.Errors[200]
	if último.Field != "(varios)" || !strings.Contains(último.Reason, "se omitieron") {
		t.Errorf("la última entrada debería resumir lo omitido: %+v", último)
	}
}

// idx desenvuelve un índice opcional (nil = cabecera) para poder compararlo.
func idx(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
