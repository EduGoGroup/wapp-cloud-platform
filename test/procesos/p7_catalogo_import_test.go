//go:build integracion

package procesos

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// La plantilla, el prompt y el import JSON estricto de P7: mirar no escribe, aplicar archiva lo
// anterior, y un documento que no vale se rechaza entero con TODOS sus defectos y su ubicación.

const (
	// p7StrictRef es la ref sobre la que se recorre el import estricto y su versionado. No es la del
	// escenario (`catalogo`), que se reserva para la caché del índice.
	p7StrictRef = "p7-estricto"

	// p7ErrMode es el 400 de un modo que no es ni validate ni apply: no se adivina.
	p7ErrMode = "mode debe ser validate o apply"
	// p7ErrFormat es el 400 de un formato de plantilla desconocido.
	p7ErrFormat = "format inválido: usa json, csv o xlsx"
	// p7UnreadableWarning es el principio del aviso de un diff calculado contra nada porque la ref
	// tiene contenido que no es un catálogo.
	p7UnreadableWarning = "catálogo vigente: la ref tiene contenido, pero no se pudo interpretar como catálogo"
)

// p7TemplateSKUs son los cinco artículos de la plantilla que reparte el servidor.
var p7TemplateSKUs = []string{"TORTA-CHOC", "TORTA-UNICORNIO", "TEQUENOS-15", "REFRESCO-15L", "COMBO-FIESTA"}

// p7DocFirst es el primer catálogo de la ref estricta y p7DocNext el que se sube después: el café
// sube de precio, el té queda igual, el jugo desaparece y entra el agua (los documentos de
// internal/publicapi/catalogimport_test.go: `catalogoVigente` y `docNuevo`).
func p7DocFirst() []byte {
	return p7Doc(p7Cat("1", "Bebidas", p7Item("1", "CAFE", "Café", "2.5"), p7Item("2", "TE", "Té", "2"), p7Item("3", "JUGO", "Jugo", "3")))
}

func p7DocNext() []byte {
	return p7Doc(p7Cat("1", "Bebidas", p7Item("1", "CAFE", "Café", "2.9"), p7Item("2", "TE", "Té", "2"), p7Item("3", "AGUA", "Agua", "1.5")))
}

// templateAndPrompt afirma las dos lecturas del import: la plantilla en sus tres formatos (el JSON
// del contrato, indentado; el CSV de la planilla canónica, con BOM; el XLSX) con su descarga nombrada,
// y el prompt con el format y la versión del contrato. Ninguna toca la base. Un formato desconocido es
// 400 —tampoco en mayúsculas ni con un espacio U+00A0 delante—: no se adivina.
func (w *p7World) templateAndPrompt(t *testing.T) {
	before := p7Mark(t, w.sc, w.admin.tenant)
	get := func(query string) p7Reply {
		return w.send(t, w.admin, "", http.MethodGet, p7RouteTemplate+query, "", nil)
	}

	plain, asJSON := get(""), get("?format=json")
	if plain.Codigo != http.StatusOK || string(plain.Cuerpo) != string(asJSON.Cuerpo) {
		t.Fatalf("la plantilla sin format: HTTP %d, y no es la misma que ?format=json (HTTP %d)", plain.Codigo, asJSON.Codigo)
	}
	p7WantDownload(t, plain, "application/json; charset=utf-8", "catalogo-plantilla.json")
	body := string(plain.Cuerpo)
	if strings.Count(body, "\n") < 20 || !strings.HasSuffix(body, "}\n") {
		t.Errorf("la plantilla JSON no va indentada y con salto final (%d saltos de línea)", strings.Count(body, "\n"))
	}
	for _, field := range []string{`"format": "wapp.catalog_import"`, `"version": 1`, `"variants"`, `"components"`, `"tags"`, `"attributes"`, `"subcategories"`} {
		if !strings.Contains(body, field) {
			t.Errorf("la plantilla JSON no trae %s", field)
		}
	}

	sheet := get("?format=csv")
	p7WantDownload(t, sheet, "text/csv; charset=utf-8", "catalogo-plantilla.csv")
	p7CheckTemplateCSV(t, sheet.Cuerpo)

	book := get("?format=xlsx")
	p7WantDownload(t, book, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "catalogo-plantilla.xlsx")
	if !strings.HasPrefix(string(book.Cuerpo), "PK\x03\x04") {
		t.Errorf("la plantilla XLSX no empieza por la firma de un ZIP")
	}

	for _, format := range []string{"xls", "CSV", "%C2%A0csv", "a@@b"} {
		if r := get("?format=" + format); !p9ErrorIs(r.respuesta, http.StatusBadRequest, p7ErrFormat) {
			t.Errorf("la plantilla con format=%s: HTTP %d %s; quería 400 «%s»", format, r.Codigo, recortar(r.Cuerpo), p7ErrFormat)
		}
	}

	w.checkPrompt(t)
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Errorf("descargar la plantilla o el prompt escribió contenido:\nantes:   %s\ndespués: %s", before, after)
	}
}

// checkPrompt afirma GET /api/v1/catalog/import/prompt: 200 con el format y la versión del contrato y
// un texto que los dicta, nombra las piezas que un LLM se salta y no trae verbos sin sustituir.
func (w *p7World) checkPrompt(t *testing.T) {
	t.Helper()
	r := w.send(t, w.admin, "", http.MethodGet, p7RoutePrompt, "", nil)
	var prompt struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Prompt  string `json:"prompt"`
	}
	r.JSON(t, &prompt)
	if r.Codigo != http.StatusOK || prompt.Format != "wapp.catalog_import" || prompt.Version != 1 {
		t.Fatalf("el prompt: HTTP %d %s/%d, quería 200 con la cabecera del contrato", r.Codigo, prompt.Format, prompt.Version)
	}
	for _, word := range []string{`format: "wapp.catalog_import", version: 1`, "sku", "price", "variants", "components"} {
		if !strings.Contains(prompt.Prompt, word) {
			t.Errorf("el prompt no dice %q:\n%s", word, prompt.Prompt)
		}
	}
	if strings.Contains(prompt.Prompt, "%!") {
		t.Errorf("el prompt tiene un verbo de formato sin sustituir:\n%s", prompt.Prompt)
	}
}

// p7WantDownload afirma el 200, el Content-Type y el nombre de la descarga de una plantilla.
func p7WantDownload(t *testing.T, r p7Reply, contentType, filename string) {
	t.Helper()
	if r.Codigo != http.StatusOK {
		t.Fatalf("la plantilla %s: HTTP %d, quería 200\ncuerpo: %s", filename, r.Codigo, recortar(r.Cuerpo))
	}
	if got := r.Header.Get("Content-Type"); got != contentType {
		t.Errorf("la plantilla %s: Content-Type %q, quería %q", filename, got, contentType)
	}
	if got, want := r.Header.Get("Content-Disposition"), fmt.Sprintf("attachment; filename=%q", filename); got != want {
		t.Errorf("la plantilla %s: Content-Disposition %q, quería %q", filename, got, want)
	}
	if got := r.Header.Get("Content-Length"); got != fmt.Sprint(len(r.Cuerpo)) {
		t.Errorf("la plantilla %s: Content-Length %q con %d bytes de cuerpo", filename, got, len(r.Cuerpo))
	}
}

// p7CheckTemplateCSV afirma la planilla canónica: BOM, la cabecera exacta, una fila por artículo, el
// precio pelado y la mini-sintaxis de las celdas múltiples.
func p7CheckTemplateCSV(t *testing.T, raw []byte) {
	t.Helper()
	if !strings.HasPrefix(string(raw), p7BOM) {
		t.Fatalf("la plantilla CSV no lleva BOM")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), p7BOM))).ReadAll()
	if err != nil || len(records) != 6 {
		t.Fatalf("la plantilla CSV: %d filas (error %v), quería la cabecera y cinco artículos", len(records), err)
	}
	if got, want := strings.Join(records[0], ","), strings.Join(p7Columns(), ","); got != want {
		t.Errorf("la cabecera de la plantilla CSV = %q, quería %q", got, want)
	}
	cells := map[string]string{
		"categoría de la fila 2":   records[1][0],
		"subcategoría":             records[1][1],
		"precio de la fila 2":      records[1][5],
		"variantes de la fila 2":   records[1][9],
		"tags de la fila 3":        records[2][7],
		"atributos de la fila 3":   records[2][8],
		"componentes de la fila 6": records[5][10],
	}
	want := map[string]string{
		"categoría de la fila 2":   "1|Tortas",
		"subcategoría":             "clasicas|Clásicas",
		"precio de la fila 2":      "18000",
		"variantes de la fila 2":   "V1|10-12 porciones|18000; V2|25-30 porciones|32000",
		"tags de la fila 3":        "decorada; sin_lactosa",
		"atributos de la fila 3":   "capas|3; porciones|10-12",
		"componentes de la fila 6": "TEQUENOS-15|1; REFRESCO-15L|2",
	}
	for name, got := range cells {
		if got != want[name] {
			t.Errorf("la plantilla CSV, %s = %q, quería %q", name, got, want[name])
		}
	}
	for i, record := range records[1:] {
		if record[3] != p7TemplateSKUs[i] {
			t.Errorf("la plantilla CSV, sku de la fila %d = %q, quería %q", i+2, record[3], p7TemplateSKUs[i])
		}
	}
}

// strictImport recorre el import JSON sobre una ref vacía: mirar (validate, que es además el modo por
// defecto) enseña el diff y NO escribe nada; el primer apply escribe y no archiva (no había nada); el
// segundo archiva lo anterior como versión 1 y deja vigente lo nuevo; reaplicar el mismo documento da
// un diff vacío y aun así archiva la versión 2. El blob guardado es el documento normalizado: sin
// `format` ni `version`. La respuesta del camino JSON no devuelve el documento.
func (w *p7World) strictImport(t *testing.T) {
	w.strictLooking(t)
	blobFirst := w.strictFirstApply(t)
	blobNext := w.strictSecondApply(t, blobFirst)

	third := p7Decode(t, w.importJSON(t, w.admin, "apply", p7StrictRef, p7DocNext()))
	if !third.Applied || third.ArchivedVersion != 2 || third.diff() != "||||3" {
		t.Errorf("reaplicar el mismo documento = %+v; quería diff vacío, tres artículos iguales y la versión 2 archivada", third)
	}
	if got := p7Content(t, w.sc, w.admin.tenant, p7StrictRef); got != blobNext {
		t.Errorf("el contenido vigente cambió entre dos imports idénticos:\n%s\n%s", blobNext, got)
	}
	p7WantVersions(t, w, p7StrictRef, "1:import_json,2:import_json", blobFirst, blobNext)
	const stamps = `SELECT (created_at < updated_at)::text || '|' || (SELECT count(*) FROM public.tenant_content_versions v
			WHERE v.tenant_id = c.tenant_id AND v.ref = c.ref AND v.created_at BETWEEN c.created_at AND c.updated_at)::text
		FROM public.tenant_content c WHERE tenant_id = $1 AND ref = $2`
	if got := p9Scalar(t, w.sc.DB, stamps, w.admin.tenant, p7StrictRef); got != "true|2" {
		t.Errorf("los instantes de la ref = %q; quería created_at intacto, updated_at movido y las dos versiones fechadas entre ambos", got)
	}
}

// strictLooking afirma que mirar una ref vacía —con mode=validate o sin modo— enseña el documento
// entero como alta y no escribe nada; la respuesta no trae ni documento ni versión archivada.
func (w *p7World) strictLooking(t *testing.T) {
	t.Helper()
	empty := p7Mark(t, w.sc, w.admin.tenant)
	for _, mode := range []string{"validate", ""} {
		r := w.importJSON(t, w.admin, mode, p7StrictRef, p7DocFirst())
		res := p7Decode(t, r)
		if r.Codigo != http.StatusOK || res.Mode != "validate" || res.Applied || res.Ref != p7StrictRef || res.Items != 3 ||
			res.diff() != "|CAFE,JUGO,TE|||0" || res.ArchivedVersion != 0 || len(res.Diff.CurrentWarnings) != 0 {
			t.Errorf("mirar con mode=%q: HTTP %d %s", mode, r.Codigo, recortar(r.Cuerpo))
		}
		if strings.Contains(string(r.Cuerpo), `"document"`) || strings.Contains(string(r.Cuerpo), `"archived_version"`) {
			t.Errorf("mirar con mode=%q devuelve el documento o una versión archivada: %s", mode, recortar(r.Cuerpo))
		}
	}
	if after := p7Mark(t, w.sc, w.admin.tenant); after != empty {
		t.Fatalf("mirar escribió contenido:\nantes:   %s\ndespués: %s", empty, after)
	}
}

// strictFirstApply aplica el primer documento sobre la ref vacía y devuelve el blob guardado: se
// escribe, no se archiva nada, y lo guardado es el catálogo normalizado, sin el sobre del contrato.
func (w *p7World) strictFirstApply(t *testing.T) string {
	t.Helper()
	sc, tenant := w.sc, w.admin.tenant
	first := p7Decode(t, w.importJSON(t, w.admin, "apply", p7StrictRef, p7DocFirst()))
	if !first.Applied || first.Mode != "apply" || first.ArchivedVersion != 0 || first.diff() != "|CAFE,JUGO,TE|||0" {
		t.Fatalf("el primer apply = %+v; quería aplicado, sin versión archivada y todo como alta", first)
	}
	if got := p7Versions(t, sc, tenant, p7StrictRef); got != "" {
		t.Errorf("el primer apply dejó versiones (%q): sin contenido vigente no hay nada que archivar", got)
	}
	if want := p7Arts(p7Art("1", "1", "CAFE", "Café", "2.5"), p7Art("1", "2", "TE", "Té", "2"), p7Art("1", "3", "JUGO", "Jugo", "3")); p7Stored(t, sc, tenant, p7StrictRef) != want {
		t.Errorf("lo guardado tras el primer apply = %s, quería %s", p7Stored(t, sc, tenant, p7StrictRef), want)
	}
	if got := p9Scalar(t, sc.DB, `SELECT (SELECT string_agg(k, ',' ORDER BY k) FROM jsonb_object_keys(content) k) FROM public.tenant_content
		WHERE tenant_id = $1 AND ref = $2`, tenant, p7StrictRef); got != "categories" {
		t.Errorf("las claves del blob guardado = %q, quería solo `categories` (ni format, ni version, ni source)", got)
	}
	return p7Content(t, sc, tenant, p7StrictRef)
}

// strictSecondApply mira y luego aplica el segundo documento, y devuelve el blob que queda vigente:
// mirar enseña el diff contra lo vigente sin escribir; aplicar archiva lo anterior (blobFirst) como
// versión 1 y deja vigente lo nuevo.
func (w *p7World) strictSecondApply(t *testing.T, blobFirst string) string {
	t.Helper()
	sc, tenant := w.sc, w.admin.tenant
	held := p7Mark(t, sc, tenant)
	look := p7Decode(t, w.importJSON(t, w.admin, "validate", p7StrictRef, p7DocNext()))
	if look.Applied || look.diff() != "CAFE:2.5>2.9|AGUA|JUGO||1" || look.Diff.Removed[0].Label != "Jugo" || look.Diff.PriceChanges[0].Label != "Café" {
		t.Errorf("mirar el segundo documento = aplicado %v, diff %q; quería el café 2.5→2.9, el agua de alta, el jugo de baja y el té igual", look.Applied, look.diff())
	}
	if after := p7Mark(t, sc, tenant); after != held {
		t.Fatalf("mirar el segundo documento escribió contenido:\nantes:   %s\ndespués: %s", held, after)
	}

	second := p7Decode(t, w.importJSON(t, w.admin, "apply", p7StrictRef, p7DocNext()))
	if !second.Applied || second.ArchivedVersion != 1 || second.diff() != "CAFE:2.5>2.9|AGUA|JUGO||1" {
		t.Fatalf("el segundo apply = %+v; quería aplicado con la versión 1 archivada", second)
	}
	blobNext := p7Content(t, sc, tenant, p7StrictRef)
	if !strings.Contains(blobNext, "AGUA") || strings.Contains(blobNext, "JUGO") {
		t.Errorf("el contenido vigente no es el importado: %s", blobNext)
	}
	p7WantVersions(t, w, p7StrictRef, "1:import_json", blobFirst)
	return blobNext
}

// p7WantVersions afirma las versiones archivadas de una ref de la empresa del proceso: su resumen
// («versión:procedencia») y, por versión, el contenido que archivó —el VIEJO, no el nuevo—.
func p7WantVersions(t *testing.T, w *p7World, ref, summary string, contents ...string) {
	t.Helper()
	if got := p7Versions(t, w.sc, w.admin.tenant, ref); got != summary {
		t.Errorf("las versiones de %s = %q, quería %q", ref, got, summary)
	}
	for i, want := range contents {
		got := p9Scalar(t, w.sc.DB, `SELECT content::text FROM public.tenant_content_versions WHERE tenant_id = $1 AND ref = $2 AND version = $3`,
			w.admin.tenant, ref, i+1)
		if got != want {
			t.Errorf("la versión %d de %s archivó\n%s\nquería el contenido que había antes:\n%s", i+1, ref, got, want)
		}
	}
}

// p7Rejection es un documento que el import rechaza: dónde están sus defectos y qué dicen.
type p7Rejection struct {
	name   string
	doc    []byte
	wheres string   // p7Result.wheres: la ubicación de cada defecto, en orden
	says   []string // fragmentos que los motivos tienen que contener
}

// rejectedDocuments afirma el rechazo estricto, en mode=apply: 400 `validation_failed` con TODOS los
// defectos en una sola respuesta, cada uno con su ubicación (índices de categoría y artículo, o la
// LÍNEA cuando el archivo ni siquiera es JSON) y un motivo que nombra el artículo y su categoría. Una
// cabecera desconocida corta antes de mirar el cuerpo. Nada de esto escribe ni archiva.
func (w *p7World) rejectedDocuments(t *testing.T) {
	sc, tenant := w.sc, w.admin.tenant
	before := p7Mark(t, sc, tenant)
	broken := make([]string, 300)
	for i := range broken {
		broken[i] = `{"code":"` + fmt.Sprint(i) + `"}`
	}
	cases := []p7Rejection{
		{"nine_defects", []byte(p7NineDefects),
			"c0/i0/price c0/i1/label c0/i2/code c0/i2/sku c0/i3/sku c0/i4/subcategory c0/i5/variants c0/i5/components[0].sku c1/i2/price",
			[]string{"debe ser un número", "no tiene nombre", `el código "2" ya lo usa el artículo 2`, `el sku "TORTA-CHOC" ya lo usa`,
				`empieza por "_"`, `la subcategoría "01z" no está declarada`, "declara variantes y componentes a la vez",
				`el sku "NO-EXISTE" no existe en el catálogo`, `el artículo 3 ("Mousse de maracuyá") de la categoría "Postres" no tiene el precio: es obligatorio.`}},
		{"empty_body", []byte("   \n "), "r0/documento", []string{"el documento está vacío"}},
		{"not_json", []byte("esto no es json"), "r0/documento", []string{"no es un JSON válido: hay un error de escritura hacia la línea 1."}},
		{"unclosed_brace", []byte("{\n  \"format\": \"wapp.catalog_import\",\n  \"version\": 1"), "r0/documento", []string{"hacia la línea 3."}},
		{"missing_comma_on_line_6", []byte("{\n\"format\":\"wapp.catalog_import\",\n\"version\":1,\n\"catalog\":{\"categories\":[\n{\"code\":\"1\",\"label\":\"A\",\"items\":[\n{\"code\":\"1\" \"sku\":\"S\"}]}]}}"),
			"r0/documento", []string{"hacia la línea 6."}},
		{"a_list", []byte(`[{"format":"wapp.catalog_import"}]`), "r0/documento", []string{"debe ser un objeto JSON"}},
		{"other_format", []byte(`{"format":"edugo.assessment_import","version":1,"catalog":{"categories":[]}}`), "r0/format", []string{"solo se importan catálogos"}},
		{"future_version", []byte(`{"format":"wapp.catalog_import","version":7,"catalog":{"categories":[]}}`), "r0/version", []string{"es de la versión 7"}},
		{"no_format", []byte(`{"version":1,"catalog":{"categories":[]}}`), "r0/format", []string{"no dice qué es"}},
		{"no_version", []byte(`{"format":"wapp.catalog_import","catalog":{"categories":[]}}`), "r0/version", []string{"no dice de qué versión es"}},
		{"no_catalog", []byte(`{"format":"wapp.catalog_import","version":1}`), "r0/catalog", []string{"no trae catálogo"}},
		{"no_categories", p7Doc(""), "r0/categories", []string{"no trae ninguna categoría"}},
		{"category_without_items", p7Doc(`{"code":"1","label":"Bebidas","items":[]}`), "c0/i-/items", []string{"callejón sin salida"}},
		{"sku_across_categories", p7Doc(p7Cat("1", "Bebidas", p7Item("1", "CAFE", "Café", "2500")) + "," + p7Cat("2", "Postres", p7Item("1", "CAFE", "Café con leche", "3000"))),
			"c1/i0/sku", []string{`de la categoría "Postres"`, `de la categoría "Bebidas"`, "único en TODO el catálogo"}},
		{"shipping_sku", p7Doc(p7Cat("1", "Bebidas", p7Item("1", p4ShippingSKU, "Despacho a domicilio", "3000"))), "c0/i0/sku", []string{p4ShippingSKU, "reservado"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { w.wantRejected(t, w.importJSON(t, w.admin, "apply", p7StrictRef, tc.doc), tc) })
	}

	t.Run("defect_cap", func(t *testing.T) {
		res := p7Decode(t, w.importJSON(t, w.admin, "apply", p7StrictRef, p7Doc(`{"code":"1","label":"Bebidas","items":[`+strings.Join(broken, ",")+`]}`)))
		if res.Error != p7ValidationFailed || len(res.Errors) != 201 {
			t.Fatalf("300 artículos rotos: %q con %d defectos, quería 200 y el resumen de los omitidos", res.Error, len(res.Errors))
		}
		if last := res.Errors[200]; last.Field != "(varios)" || !strings.HasPrefix(last.Reason, "se omitieron 700 problemas más") {
			t.Errorf("el último defecto = %+v, quería el resumen de los 700 omitidos", last)
		}
	})
	t.Run("unknown_mode", func(t *testing.T) {
		for _, mode := range []string{"aply", "APPLY", "apply\u00a0", "a@@b", "١"} {
			if r := w.importJSON(t, w.admin, mode, p7StrictRef, p7DocFirst()); !p9ErrorIs(r, http.StatusBadRequest, p7ErrMode) {
				t.Errorf("mode=%q: HTTP %d %s; quería 400 «%s»", mode, r.Codigo, recortar(r.Cuerpo), p7ErrMode)
			}
		}
	})
	if after := p7Mark(t, sc, tenant); after != before {
		t.Errorf("un documento rechazado escribió o archivó contenido:\nantes:   %s\ndespués: %s", before, after)
	}

	t.Run("unreadable_current_content", func(t *testing.T) {
		const ref = "p7-ilegible"
		if r := w.calls.call(t, w.admin, p7ActContent, http.MethodPut, p7RouteContent+"/"+ref, json.RawMessage(`{"prompt":"esto no es un catálogo"}`)); r.Codigo != http.StatusOK {
			t.Fatalf("PUT de un contenido que no es catálogo: HTTP %d\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
		}
		res := p7Decode(t, w.importJSON(t, w.admin, "validate", ref, p7DocNext()))
		if res.diff() != "|AGUA,CAFE,TE|||0" || len(res.Diff.CurrentWarnings) != 1 || !strings.HasPrefix(res.Diff.CurrentWarnings[0], p7UnreadableWarning) {
			t.Errorf("el diff contra un contenido ilegible = %q con avisos %q; quería todo como alta y UN aviso", res.diff(), res.Diff.CurrentWarnings)
		}
	})
}

// wantRejected afirma un rechazo del import: 400 `validation_failed`, los defectos en su sitio y en
// su orden, cada uno con campo y motivo, los fragmentos esperados y ningún documento en la respuesta.
func (w *p7World) wantRejected(t *testing.T, r respuesta, tc p7Rejection) {
	t.Helper()
	res := p7Decode(t, r)
	if r.Codigo != http.StatusBadRequest || res.Error != p7ValidationFailed {
		t.Fatalf("HTTP %d %q, quería 400 %s\ncuerpo: %s", r.Codigo, res.Error, p7ValidationFailed, recortar(r.Cuerpo))
	}
	if got := res.wheres(); got != tc.wheres {
		t.Errorf("los defectos están en %q, quería %q\n%s", got, tc.wheres, res.reasons())
	}
	reasons := res.reasons()
	for _, say := range tc.says {
		if !strings.Contains(reasons, say) {
			t.Errorf("ningún motivo dice %q:\n%s", say, reasons)
		}
	}
	for i, e := range res.Errors {
		if e.Field == "" || strings.TrimSpace(e.Reason) == "" {
			t.Errorf("el defecto %d va sin campo o sin motivo: %+v", i, e)
		}
	}
	if len(res.Document) != 0 || res.Applied {
		t.Errorf("un rechazo trae documento o dice que aplicó: %s", recortar(r.Cuerpo))
	}
}

// p7NineDefects es UN documento con nueve defectos distintos repartidos por dos categorías
// (`docConNueveDefectos`, internal/catalogimport/validator_test.go): precio como texto, artículo sin
// nombre, código repetido, sku repetido, sku reservado, subcategoría inexistente, variantes y
// componentes a la vez, componente que no existe y artículo sin precio.
const p7NineDefects = `{
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
