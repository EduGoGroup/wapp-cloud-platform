//go:build pendiente

package catalogimport_test

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// validator_test.go cubre la entrada del validador: el techo de bytes (ReadLimited),
// la acumulación de defectos, lo que corta la validación (archivo inservible,
// cabecera desconocida) y la coherencia con el runtime. Las reglas del cuerpo van
// por tema (E-13): validator_catalog_test.go (categorías y subcategorías),
// validator_item_test.go (el artículo) y validator_fields_test.go (texto, precio,
// cantidad y atributos). Los ayudantes, en helpers_test.go.

func TestErrDocumentTooLarge_Text(t *testing.T) {
	const want = "el documento de import excede el tamaño máximo permitido"
	if got := catalogimport.ErrDocumentTooLarge.Error(); got != want {
		t.Errorf("ErrDocumentTooLarge = %q; se esperaba %q", got, want)
	}
}

// errReadPastCeiling es el veneno del lector: si ReadLimited pide un byte más allá
// del techo, el test falla POR AQUÍ y no por una comparación indirecta.
var errReadPastCeiling = errors.New("se leyó el cuerpo más allá del techo")

// poisonedReader entrega un documento y, agotado este, espacios sin fin —un JSON
// seguido de espacios SIGUE SIENDO JSON válido: si se leyera de más y se
// deserializara igual, el documento saldría impecable y el test lo vería—. Lleva la
// cuenta de lo leído y envenena la lectura pasado limit.
type poisonedReader struct {
	body  []byte
	read  int
	limit int
}

func (r *poisonedReader) Read(p []byte) (int, error) {
	if r.read >= r.limit {
		return 0, errReadPastCeiling
	}
	n := min(len(p), r.limit-r.read)
	for i := range n {
		if pos := r.read + i; pos < len(r.body) {
			p[i] = r.body[pos]
			continue
		}
		p[i] = ' '
	}
	r.read += n
	return n, nil
}

// documentOfSize fabrica un documento VÁLIDO de exactamente n bytes, rellenando la
// descripción de su único artículo: lo único que puede hacerlo fallar es su tamaño.
func documentOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const template = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2500,"description":"%s"}]}]}}`
	base := len(template) - len("%s")
	if n < base {
		t.Fatalf("no se puede fabricar un documento de %d bytes: el mínimo es %d", n, base)
	}
	doc := []byte(strings.Replace(template, "%s", strings.Repeat("a", n-base), 1))
	if len(doc) != n {
		t.Fatalf("el documento fabricado mide %d bytes y se pedían %d", len(doc), n)
	}
	return doc
}

// TestReadLimited_ExactlyAtTheCeilingPasses: el byte de más es lo que separa «cabe»
// de «se pasó». Un documento de exactamente el máximo entra y valida.
func TestReadLimited_ExactlyAtTheCeilingPasses(t *testing.T) {
	limits := catalogimport.DefaultLimits()
	ceiling := int(limits.MaxJSONBytes)

	body, err := catalogimport.ReadLimited(bytes.NewReader(documentOfSize(t, ceiling)), limits)
	if err != nil {
		t.Fatalf("un documento de exactamente %d bytes debe pasar: %v", ceiling, err)
	}
	if len(body) != ceiling {
		t.Fatalf("se leyeron %d bytes de %d", len(body), ceiling)
	}
	if _, verr := catalogimport.Validate(body, limits); verr != nil {
		t.Fatalf("el documento leído debe validar: %v", verr)
	}
}

// TestReadLimited_OneByteMoreIsRejectedWithoutReadingFurther: el cuerpo es 1 MiB+1
// de un documento IMPECABLE seguido de un mar de espacios. Si se hubiera
// deserializado, habría validado; si se hubiera leído un byte más, el error sería el
// veneno y no ErrDocumentTooLarge.
func TestReadLimited_OneByteMoreIsRejectedWithoutReadingFurther(t *testing.T) {
	limits := catalogimport.DefaultLimits()
	ceiling := int(limits.MaxJSONBytes)
	reader := &poisonedReader{body: documentOfSize(t, ceiling+1), limit: ceiling + 1}

	body, err := catalogimport.ReadLimited(reader, limits)
	if !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
		t.Fatalf("se esperaba ErrDocumentTooLarge; llegó %v", err)
	}
	if body != nil {
		t.Errorf("un cuerpo rechazado no se devuelve; llegaron %d bytes", len(body))
	}
	if reader.read != ceiling+1 {
		t.Errorf("se leyeron %d bytes; el techo permite leer exactamente %d (uno más que el máximo)", reader.read, ceiling+1)
	}
}

// TestReadLimited_OwnCeiling respeta un techo configurado más bajo que el default y
// lo cita en el error.
func TestReadLimited_OwnCeiling(t *testing.T) {
	limits := catalogimport.Limits{MaxJSONBytes: 64, MaxItems: 10}

	body, err := catalogimport.ReadLimited(strings.NewReader(strings.Repeat("x", 64)), limits)
	if err != nil || len(body) != 64 {
		t.Fatalf("64 bytes con el techo en 64 debe pasar entero: %d bytes, err=%v", len(body), err)
	}

	_, err = catalogimport.ReadLimited(strings.NewReader(strings.Repeat("x", 65)), limits)
	if !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
		t.Fatalf("con el techo en 64 bytes, 65 debe rechazarse; llegó %v", err)
	}
	const want = "el documento de import excede el tamaño máximo permitido (máximo 64 bytes)"
	if err.Error() != want {
		t.Errorf("error = %q; se esperaba %q", err.Error(), want)
	}
}

// TestReadLimited_NonPositiveCeilingFallsBackToDefault: una configuración a 0 —o
// negativa— no DESACTIVA el techo. Aquí es de seguridad: un valor mal escrito
// abriría la puerta a subir cualquier cosa.
func TestReadLimited_NonPositiveCeilingFallsBackToDefault(t *testing.T) {
	atDefault := strings.Repeat("x", int(catalogimport.DefaultMaxJSONBytes))
	for _, limits := range []catalogimport.Limits{{}, {MaxJSONBytes: -1, MaxItems: -1}} {
		if _, err := catalogimport.ReadLimited(strings.NewReader(atDefault), limits); err != nil {
			t.Errorf("con %+v el techo es el default y %d bytes caben: %v", limits, len(atDefault), err)
		}
		if _, err := catalogimport.ReadLimited(strings.NewReader(atDefault+"x"), limits); !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
			t.Errorf("con %+v el techo debe caer al default y rechazar un byte más; llegó %v", limits, err)
		}
	}
}

// failingReader falla siempre con su error.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadLimited_ReaderFailureIsWrapped(t *testing.T) {
	boom := errors.New("conexión cortada")

	body, err := catalogimport.ReadLimited(failingReader{err: boom}, catalogimport.DefaultLimits())
	if !errors.Is(err, boom) {
		t.Fatalf("el error del lector debe viajar envuelto; llegó %v", err)
	}
	if errors.Is(err, catalogimport.ErrDocumentTooLarge) {
		t.Errorf("un fallo de lectura no es un rechazo por tamaño: %v", err)
	}
	if want := "no se pudo leer el documento de import: conexión cortada"; err.Error() != want {
		t.Errorf("error = %q; se esperaba %q", err.Error(), want)
	}
	if body != nil {
		t.Errorf("una lectura fallida no devuelve cuerpo; llegaron %d bytes", len(body))
	}
}

// nineDefectsDoc es UN documento con NUEVE defectos de nueve reglas distintas,
// repartidos por dos categorías. Es la piedra de toque del acumulador: un validador
// que se pare en el primero devuelve 1, y uno que valide «por bloques» pierde los de
// la última categoría.
const nineDefectsDoc = `{
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

// TestValidate_AccumulatesEveryDefectInOnePass: los nueve, con su ubicación, su
// campo y su motivo legible —el artículo y la categoría por su nombre, en español—,
// en UNA respuesta y sin devolver catálogo.
func TestValidate_AccumulatesEveryDefectInOnePass(t *testing.T) {
	doc, verr := validate(nineDefectsDoc)

	assertDefects(t, verr,
		atItem(0, 0, "price", `el artículo 1 ("Torta de chocolate") de la categoría "Tortas": el precio debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`),
		atItem(0, 1, "label", `el artículo 2 de la categoría "Tortas" no tiene nombre: es lo que ve el cliente en la lista.`),
		atItem(0, 2, "code", `el artículo 3 ("Torta repetida") de la categoría "Tortas": el código "2" ya lo usa el artículo 2 de la misma categoría; el cliente teclea ese número y no se sabría cuál de los dos pidió.`),
		atItem(0, 2, "sku", `el artículo 3 ("Torta repetida") de la categoría "Tortas": el sku "TORTA-CHOC" ya lo usa el artículo 1 ("Torta de chocolate") de la categoría "Tortas"; el sku identifica al artículo en el pedido y tiene que ser único en TODO el catálogo, no solo dentro de su categoría.`),
		atItem(0, 3, "sku", `el artículo 4 ("Envío a domicilio") de la categoría "Tortas": el sku "_shipping" empieza por "_", que está reservado para las líneas que pone wApp (el envío, por ejemplo). Ponle otro.`),
		atItem(0, 4, "subcategory", `el artículo 5 ("Torta mixta") de la categoría "Tortas": la subcategoría "01z" no está declarada en su categoría; decláralas en "subcategories" o quita la referencia.`),
		atItem(0, 5, "variants", `el artículo 6 ("Combo fiesta") de la categoría "Tortas" declara variantes y componentes a la vez: o se vende en presentaciones (variants) o es un combo (components), no las dos cosas.`),
		atItem(0, 5, "components[0].sku", `el artículo 6 ("Combo fiesta") de la categoría "Tortas", componente 1: el sku "NO-EXISTE" no existe en el catálogo; los componentes de un combo tienen que ser artículos declarados.`),
		atItem(1, 2, "price", `el artículo 3 ("Mousse de maracuyá") de la categoría "Postres" no tiene el precio: es obligatorio.`),
	)
	if got := compactJSON(t, doc); got != compactJSON(t, catalogimport.CatalogImport{}) {
		t.Errorf("un documento inválido devuelve el cero de CatalogImport; devolvió %s", got)
	}
}

// validDoc ejercita el contrato v2 ENTERO: subcategorías, etiquetas, atributos,
// variantes y un combo cuyos componentes existen de verdad.
const validDoc = `{
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

// TestValidate_ValidDocumentComesBackTyped: el documento vuelve tipado y
// normalizado —el atributo numérico como texto (lo mismo que hace el runtime) y la
// qty ausente materializada en 1—.
func TestValidate_ValidDocumentComesBackTyped(t *testing.T) {
	doc := mustValidate(t, validDoc)

	const want = `{"format":"wapp.catalog_import","version":1,"source":{"kind":"llm","model":"un-modelo","hint":"lista del dueño"},` +
		`"catalog":{"categories":[{"code":"1","label":"Tortas","subcategories":[{"code":"01a","label":"Infantiles"}],` +
		`"items":[{"code":"1","sku":"TORTA-CHOC","label":"Torta de chocolate","price":18000,"description":"Bizcocho húmedo",` +
		`"subcategory":"01a","tags":["sin_lactosa","decoracion"],"attributes":{"capas":"3","porciones":"10-12"},` +
		`"variants":[{"code":"V1","label":"10-12 porciones","price":18000},{"code":"V2","label":"25-30 porciones","price":32000}]}]},` +
		`{"code":"2","label":"Combos","items":[{"code":"1","sku":"HAMB","label":"Hamburguesa","price":6000},` +
		`{"code":"2","sku":"REFR","label":"Refresco","price":2000},` +
		`{"code":"3","sku":"COMBO-1","label":"Combo hamburguesa","price":7500,"components":[{"sku":"HAMB","qty":1},{"sku":"REFR","qty":1}]}]}]}}`
	if got := compactJSON(t, doc); got != want {
		t.Errorf("documento = %s\nse esperaba %s", got, want)
	}
}

// TestValidate_WhatValidatesTheRuntimeParsesWithoutWarnings amarra los dos rigores:
// si un documento que el import acepta produjera avisos en catalogo.ParseCatalog, el
// dueño publicaría un catálogo con partes que el motor descarta en silencio.
func TestValidate_WhatValidatesTheRuntimeParsesWithoutWarnings(t *testing.T) {
	doc := mustValidate(t, validDoc)

	cat := runtimeCatalog(t, doc.Catalog)
	if len(cat.Warnings) != 0 {
		t.Fatalf("el runtime descartó partes de un catálogo válido: %+v", cat.Warnings)
	}
	if len(cat.Categories) != 2 || len(cat.Categories[0].Items[0].Variants) != 2 {
		t.Fatalf("el árbol del runtime no cuadra con el documento: %+v", cat.Categories)
	}
	if combo := cat.Categories[1].Items[2]; len(combo.Components) != 2 || combo.Components[1].Qty != 1 {
		t.Errorf("la qty ausente debe valer 1 también en el runtime: %+v", combo.Components)
	}
}

// TestValidate_UnknownHeaderDoesNotInterpretTheBody: con un format o una version que
// no son los suyos el validador se planta en la cabecera. Todos los casos traen
// "categories": [] —un defecto de cuerpo— que NO debe salir.
func TestValidate_UnknownHeaderDoesNotInterpretTheBody(t *testing.T) {
	const (
		noFormat      = `el archivo no dice qué es: le falta la línea "format": "wapp.catalog_import". Descarga la plantilla y parte de ella.`
		noVersion     = `el archivo no dice de qué versión es: le falta la línea "version": 1.`
		emptyBody     = `,"catalog":{"categories":[]}}`
		otherVersion  = ` y esta consola entiende la versión 1: vuelve a descargar la plantilla y genera el catálogo con ella.`
		otherFormat   = `el archivo dice ser "edugo.assessment_import" y aquí solo se importan catálogos ("wapp.catalog_import"): comprueba que no te hayas equivocado de archivo.`
		formatNotText = `el campo "format" debe ser un texto entre comillas: "wapp.catalog_import".`
	)
	cases := map[string]struct {
		doc  string
		want []defect
	}{
		"another format":    {`{"format":"edugo.assessment_import","version":1` + emptyBody, []defect{atHeader("format", otherFormat)}},
		"format not a text": {`{"format":7,"version":1` + emptyBody, []defect{atHeader("format", formatNotText)}},
		"missing format":    {`{"version":1` + emptyBody, []defect{atHeader("format", noFormat)}},
		"future version":    {`{"format":"wapp.catalog_import","version":7` + emptyBody, []defect{atHeader("version", "el archivo es de la versión 7"+otherVersion)}},
		"fractional version": {`{"format":"wapp.catalog_import","version":1.5` + emptyBody,
			[]defect{atHeader("version", "el archivo es de la versión 1.5"+otherVersion)}},
		"version as text": {`{"format":"wapp.catalog_import","version":"1"` + emptyBody,
			[]defect{atHeader("version", `el campo "version" debe ser un número: 1.`)}},
		"missing version": {`{"format":"wapp.catalog_import"` + emptyBody, []defect{atHeader("version", noVersion)}},
		"both wrong": {`{"format":"x","version":2}`, []defect{
			atHeader("format", `el archivo dice ser "x" y aquí solo se importan catálogos ("wapp.catalog_import"): comprueba que no te hayas equivocado de archivo.`),
			atHeader("version", "el archivo es de la versión 2"+otherVersion),
		}},
		// null es JSON válido y no trae nada: faltan las dos líneas de la cabecera.
		"json null": {`null`, []defect{atHeader("format", noFormat), atHeader("version", noVersion)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(c.doc)
			assertDefects(t, verr, c.want...)
		})
	}
}

// TestValidate_VersionWrittenAsOnePointZeroIsTheSameVersion: 1.0 es el entero 1.
func TestValidate_VersionWrittenAsOnePointZeroIsTheSameVersion(t *testing.T) {
	doc := mustValidate(t, `{"format":"wapp.catalog_import","version":1.0,"catalog":{"categories":[`+
		`{"code":"1","label":"Bebidas","items":[`+coffee("")+`]}]}}`)
	if doc.Version != catalogimport.ImportVersion {
		t.Errorf("version = %d; se esperaba %d", doc.Version, catalogimport.ImportVersion)
	}
}

// TestValidate_UselessFile cubre lo que llega cuando alguien pega mal: nada, texto
// que no es JSON (con la línea hacia la que está el error) o un JSON que no es un
// objeto.
func TestValidate_UselessFile(t *testing.T) {
	const (
		notJSON   = "el archivo no es un JSON válido: hay un error de escritura hacia la línea %d. Revisa que no falte una coma, una comilla o una llave."
		notObject = `el archivo debe ser un objeto JSON con "format", "version" y "catalog": lo que llegó no tiene esa forma.`
	)
	lineOf := func(n int) string { return strings.Replace(notJSON, "%d", strconv.Itoa(n), 1) }
	cases := map[string]struct{ doc, reason string }{
		"empty":         {"", "el documento está vacío: pega el JSON del catálogo o sube el archivo."},
		"only spaces":   {"   \n ", "el documento está vacío: pega el JSON del catálogo o sube el archivo."},
		"plain text":    {"esto no es json", lineOf(1)},
		"unclosed":      {"{\n  \"format\": \"wapp.catalog_import\",\n  \"version\": 1", lineOf(3)},
		"a list":        {`[{"format":"wapp.catalog_import"}]`, notObject},
		"a number":      {`7`, notObject},
		"arabic digits": {docOfItems("{\"code\":\"1\",\"sku\":\"CAFE\",\"label\":\"Café\",\n\"price\":١٨٠٠٠}"), lineOf(2)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := validate(c.doc)
			assertDefects(t, verr, atHeader("documento", c.reason))
		})
	}
}

// TestValidate_DefectsComeOrderedByPosition: los defectos salen por posición en el
// documento y, dentro de un artículo, en el orden en que se validan sus campos.
func TestValidate_DefectsComeOrderedByPosition(t *testing.T) {
	_, verr := validate(docOf(`{"code":"1","label":"A","items":[{"code":"1","sku":"X","label":"x","price":1}]},` +
		`{"label":"B","items":[{"code":"1"}]}`))

	assertDefects(t, verr,
		atCategory(1, "code", `la categoría "B" no tiene código: es lo que teclea el cliente para entrar en ella.`),
		atItem(1, 0, "label", `el artículo 1 de la categoría "B" no tiene nombre: es lo que ve el cliente en la lista.`),
		atItem(1, 0, "sku", `el artículo 1 de la categoría "B" no tiene el sku: es obligatorio.`),
		atItem(1, 0, "price", `el artículo 1 de la categoría "B" no tiene el precio: es obligatorio.`),
	)
}

// TestValidate_DefectCapSummarizesTheRest: un documento roto de cabo a rabo (300
// artículos sin sku, nombre ni precio: 900 defectos) se corta en 200 y dice cuántos
// quedaron fuera.
func TestValidate_DefectCapSummarizesTheRest(t *testing.T) {
	items := make([]string, 0, 300)
	for i := range 300 {
		items = append(items, `{"code":"`+strconv.Itoa(i)+`"}`)
	}
	_, verr := validate(docOfItems(strings.Join(items, ",")))
	if verr == nil {
		t.Fatal("300 artículos rotos deben rechazarse")
	}
	if len(verr.Errors) != 201 {
		t.Fatalf("entradas = %d; se esperaban 201 (200 detalladas y el resumen)", len(verr.Errors))
	}
	if first := verr.Errors[0]; index(first.ItemIndex) != 0 || first.Field != "label" {
		t.Errorf("el primer defecto debe ser del primer artículo: %+v", first)
	}
	if lastDetailed := verr.Errors[199]; index(lastDetailed.ItemIndex) != 66 || lastDetailed.Field != "sku" {
		t.Errorf("el defecto 200 debe ser el sku del artículo 67: %+v", lastDetailed)
	}
	summary := verr.Errors[200]
	const want = "se omitieron 700 problemas más: arregla los de arriba y vuelve a subir el archivo para ver el resto."
	if summary.Field != "(varios)" || summary.Reason != want || summary.CategoryIndex != nil || summary.ItemIndex != nil {
		t.Errorf("la última entrada debe resumir lo omitido, sin índices: %+v", summary)
	}
}

// TestValidate_UnknownFieldsAreIgnored: lo que el contrato no conoce no es un
// defecto ni viaja al documento tipado.
func TestValidate_UnknownFieldsAreIgnored(t *testing.T) {
	doc := mustValidate(t, `{"format":"wapp.catalog_import","version":1,"extra":true,"catalog":{"categories":[`+
		`{"code":"1","label":"Bebidas","otra":1,"items":[`+coffee(`,"zzz":[]`)+`]}]}}`)

	const want = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":100}]}]}}`
	if got := compactJSON(t, doc); got != want {
		t.Errorf("documento = %s\nse esperaba %s", got, want)
	}
}
