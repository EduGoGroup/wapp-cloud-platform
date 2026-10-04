//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// La tabla adversaria del import JSON de P7 (reglas.md §2): lo que entra por la puerta con
// separadores repetidos, dígitos no ASCII y espacios Unicode —en el sku, en los códigos, en los
// nombres, en el precio y en la ref—, y los duplicados que solo difieren en eso. Se afirma lo que
// hace el viejo, que NO trata igual todos los campos:
//
//   - el sku y el código de artículo NO se recortan: `PAN` y `PAN`+U+00A0 son dos artículos;
//   - el nombre y el código de CATEGORÍA sí se recortan (con strings.TrimSpace, que quita U+00A0,
//     U+2003 y U+3000 pero no U+200B): `1` y `1`+U+00A0 son la misma categoría, repetida;
//   - el prefijo reservado `_` se mira sin recortar: U+00A0 + `_x` pasa;
//   - un precio solo puede ser un número JSON: los dígitos no ASCII son un error de escritura del
//     archivo (con su línea) si van sueltos, y un precio «que debe ser un número» si van entre comillas.
//
// La planilla, que recorta TODAS sus celdas, resuelve distinto varios de estos casos: están en
// p7_catalogo_adversarial_tabular_test.go.

// Los caracteres adversarios, con nombre para que se vean en el fuente.
const (
	p7NBSP   = "\u00a0" // espacio de no separación: strings.TrimSpace lo recorta
	p7IDSP   = "\u3000" // espacio ideográfico: también
	p7EMSP   = "\u2003" // espacio largo: también
	p7ZWSP   = "\u200b" // espacio de ancho cero: NO es un espacio para Go, no se recorta
	p7Arabic = "١٢٣"    // dígitos árabe-índicos
)

// p7Adversary es un caso de la tabla adversaria. Si wheres está vacío el documento se ACEPTA y stored
// dice qué quedó guardado; si no, se RECHAZA con esos defectos y no se escribe nada.
type p7Adversary struct {
	name string
	doc  []byte

	stored string   // aceptado: lo que devuelve p7Stored
	raw    []string // aceptado: fragmentos que el contenido guardado (content::text) tiene que traer
	absent []string // aceptado: fragmentos que NO puede traer

	wheres string   // rechazado: p7Result.wheres
	says   []string // rechazado: fragmentos de los motivos
}

// runAdversaries recorre una tabla adversaria por la puerta dada (door sube el documento en apply a la
// ref que se le da). Cada caso va a una ref propia: un caso aceptado se comprueba contra lo guardado
// en ella, y uno rechazado contra la marca de estado de la empresa entera.
func (w *p7World) runAdversaries(t *testing.T, prefix string, door func(t *testing.T, ref string, doc []byte) respuesta, cases []p7Adversary) {
	t.Helper()
	sc, tenant := w.sc, w.admin.tenant
	for i, tc := range cases {
		ref := fmt.Sprintf("%s-%02d", prefix, i)
		t.Run(tc.name, func(t *testing.T) {
			if tc.wheres != "" {
				before := p7Mark(t, sc, tenant)
				w.wantRejected(t, door(t, ref, tc.doc), p7Rejection{name: tc.name, wheres: tc.wheres, says: tc.says})
				if after := p7Mark(t, sc, tenant); after != before {
					t.Errorf("el documento rechazado escribió o archivó contenido")
				}
				return
			}
			r := door(t, ref, tc.doc)
			res := p7Decode(t, r)
			if r.Codigo != http.StatusOK || !res.Applied || res.Ref != ref {
				t.Fatalf("HTTP %d, aplicado %v en %q; quería 200 aplicado en %q\n%s", r.Codigo, res.Applied, res.Ref, ref, recortar(r.Cuerpo))
			}
			if got := p7Stored(t, sc, tenant, ref); got != tc.stored {
				t.Errorf("guardó\n%s\nquería\n%s", got, tc.stored)
			}
			if want := strings.Count(tc.stored, "\n") + 1; res.Items != want || len(res.Diff.Added) != want {
				t.Errorf("la respuesta cuenta %d artículos y %d altas; lo guardado son %d", res.Items, len(res.Diff.Added), want)
			}
			content := p7Content(t, sc, tenant, ref)
			for _, fragment := range tc.raw {
				if !strings.Contains(content, fragment) {
					t.Errorf("el contenido guardado no trae %q:\n%s", fragment, content)
				}
			}
			for _, fragment := range tc.absent {
				if strings.Contains(content, fragment) {
					t.Errorf("el contenido guardado trae %q:\n%s", fragment, content)
				}
			}
		})
	}
}

// p7One envuelve UN artículo (JSON crudo) en un documento de una categoría «1 · A».
func p7One(item string) []byte { return p7Doc(p7Cat("1", "A", item)) }

// p7RawItem arma un artículo «1 / S / L / 1» con campos de más (JSON crudo, con la coma delante).
func p7RawItem(sku, extra string) string {
	return `{"code":"1","sku":` + p7Quote(sku) + `,"label":"L","price":1` + extra + `}`
}

// adversarialJSON es la tabla adversaria del import JSON.
func (w *p7World) adversarialJSON(t *testing.T) {
	door := func(t *testing.T, ref string, doc []byte) respuesta {
		return w.importJSON(t, w.admin, "apply", ref, doc)
	}
	w.runAdversaries(t, "p7-adv", door, append(p7AdversarialIdentifiers(), p7AdversarialValues()...))

	// El documento no decide ni la empresa ni la ref: los campos ajenos del caso `foreign_fields_ignored`
	// (un `tenant_id` y una `ref` en el sobre) no escribieron nada en otro sitio.
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.tenant_content WHERE tenant_id = 'otra-empresa' OR ref = 'otra-ref'`); n != 0 {
		t.Errorf("hay %d filas de tenant_content escritas con la empresa o la ref que traía el documento", n)
	}
}

// p7AdversarialIdentifiers son los casos de los identificadores y los nombres: sku, códigos, etiquetas.
func p7AdversarialIdentifiers() []p7Adversary {
	item := func(sku string) string { return p7Item("1", sku, "L", "1") }
	return []p7Adversary{
		{name: "skus_stored_untouched",
			doc: p7Doc(p7Cat("1", "A", p7Item("1", "A@@B", "L1", "1"), p7Item("2", "A"+p7NBSP+"B", "L2", "2"), p7Item("3", p7Arabic, "L3", "3"), p7Item("4", "123", "L4", "4"))),
			stored: p7Arts(p7Art("1", "1", "A@@B", "L1", "1"), p7Art("1", "2", "A"+p7NBSP+"B", "L2", "2"),
				p7Art("1", "3", p7Arabic, "L3", "3"), p7Art("1", "4", "123", "L4", "4"))},
		{name: "skus_differing_only_in_spaces_are_distinct",
			doc: p7Doc(p7Cat("1", "A", p7Item("1", "PAN", "L1", "1"), p7Item("2", "PAN"+p7NBSP, "L2", "2"), p7Item("3", p7IDSP+"PAN", "L3", "3"),
				p7Item("4", "pan", "L4", "4"), p7Item("5", "PAN"+p7ZWSP, "L5", "5"), p7Item("6", " PAN ", "L6", "6"))),
			stored: p7Arts(p7Art("1", "1", "PAN", "L1", "1"), p7Art("1", "2", "PAN"+p7NBSP, "L2", "2"), p7Art("1", "3", p7IDSP+"PAN", "L3", "3"),
				p7Art("1", "4", "pan", "L4", "4"), p7Art("1", "5", "PAN"+p7ZWSP, "L5", "5"), p7Art("1", "6", " PAN ", "L6", "6"))},
		{name: "sku_only_spaces", doc: p7One(item(p7NBSP + p7IDSP)), wheres: "c0/i0/sku", says: []string{`el artículo 1 ("L") de la categoría "A" no tiene el sku: es obligatorio.`}},
		{name: "sku_zero_width_space_is_a_sku", doc: p7One(item(p7ZWSP)), stored: p7Art("1", "1", p7ZWSP, "L", "1")},
		{name: "sku_reserved_prefix", doc: p7One(item("_x")), wheres: "c0/i0/sku", says: []string{`el sku "_x" empieza por "_", que está reservado`}},
		{name: "sku_reserved_prefix_behind_a_space_passes", doc: p7One(item(p7NBSP + "_x")), stored: p7Art("1", "1", p7NBSP+"_x", "L", "1")},
		{name: "sku_shaped_like_a_variant_option", doc: p7One(item("TORTA#10")), stored: p7Art("1", "1", "TORTA#10", "L", "1")},
		{name: "labels_are_trimmed",
			doc: p7Doc(p7Cat("1", p7NBSP+"A"+p7IDSP, p7Item("1", "S1", p7NBSP+"Pan"+p7EMSP, "1"), p7Item("2", "S2", "Pan", "2"),
				p7Item("3", "S3", "a@@b", "3"), p7Item("4", "S4", p7Arabic, "4"), p7Item("5", "S5", "Pan"+p7NBSP+"de"+p7NBSP+"molde", "5"))),
			stored: p7Arts(p7Art("1", "1", "S1", "Pan", "1"), p7Art("1", "2", "S2", "Pan", "2"), p7Art("1", "3", "S3", "a@@b", "3"),
				p7Art("1", "4", "S4", p7Arabic, "4"), p7Art("1", "5", "S5", "Pan"+p7NBSP+"de"+p7NBSP+"molde", "5")),
			raw: []string{`"label": "A"}`}},
		{name: "label_only_spaces", doc: p7One(p7Item("1", "S1", p7NBSP+p7EMSP, "1")), wheres: "c0/i0/label",
			says: []string{`el artículo 1 de la categoría "A" no tiene nombre`}},
		{name: "label_zero_width_space_is_a_name", doc: p7One(p7Item("1", "S1", p7ZWSP, "1")), stored: p7Art("1", "1", "S1", p7ZWSP, "1")},
		{name: "category_codes_differing_only_in_spaces_collide",
			doc:    p7Doc(p7Cat("1", "A", item("S1")) + "," + p7Cat("1"+p7NBSP, "B", item("S2"))),
			wheres: "c1/i-/code", says: []string{`la categoría "B": el código "1" ya lo usa la categoría 1`}},
		{name: "category_codes_non_ascii_are_distinct",
			doc:    p7Doc(p7Cat("1", "A", item("S1")) + "," + p7Cat("١", "B", item("S2")) + "," + p7Cat("a@@b", "C", item("S3"))),
			stored: p7Arts(p7Art("1", "1", "S1", "L", "1"), p7Art("١", "1", "S2", "L", "1"), p7Art("a@@b", "1", "S3", "L", "1"))},
		{name: "item_codes_differing_only_in_spaces_are_distinct",
			doc:    p7Doc(p7Cat("1", "A", p7Item("1", "S1", "L", "1"), p7Item("1"+p7NBSP, "S2", "L", "1"), p7Item("١", "S3", "L", "1"), p7Item("a@@b", "S4", "L", "1"))),
			stored: p7Arts(p7Art("1", "1", "S1", "L", "1"), p7Art("1", "1"+p7NBSP, "S2", "L", "1"), p7Art("1", "١", "S3", "L", "1"), p7Art("1", "a@@b", "S4", "L", "1"))},
	}
}

// p7AdversarialValues son los casos del precio, de los campos v2 (subcategoría, variantes,
// componentes, etiquetas) y de la cabecera del documento.
func p7AdversarialValues() []p7Adversary {
	const priceReason = `el precio debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`
	sub := func(declared, used string) []byte {
		return p7Doc(`{"code":"1","label":"A","subcategories":[{"code":` + p7Quote(declared) + `,"label":"Sub"}],"items":[` +
			p7RawItem("S", `,"subcategory":`+p7Quote(used)) + `]}`)
	}
	return []p7Adversary{
		{name: "price_as_non_ascii_text", doc: p7One(p7Item("1", "S1", "L", `"`+p7Arabic+`"`)), wheres: "c0/i0/price", says: []string{priceReason}},
		{name: "price_as_ascii_text", doc: p7One(p7Item("1", "S1", "L", `"12"`)), wheres: "c0/i0/price", says: []string{priceReason}},
		{name: "price_bare_non_ascii_digits_fail_on_their_line",
			doc: []byte("{\"format\":\"wapp.catalog_import\",\n\"version\":1,\n\"catalog\":{\"categories\":[\n{\"code\":\"1\",\"label\":\"A\",\"items\":[\n" +
				"{\"code\":\"1\",\"sku\":\"S\",\"label\":\"L\",\"price\":" + p7Arabic + "}]}]}}"),
			wheres: "r0/documento", says: []string{"hay un error de escritura hacia la línea 5."}},
		{name: "price_with_trailing_nbsp", doc: p7One(p7Item("1", "S1", "L", "12"+p7NBSP)), wheres: "r0/documento", says: []string{"hacia la línea 1."}},
		{name: "price_notations",
			doc:    p7Doc(p7Cat("1", "A", p7Item("1", "S1", "L", "1e3"), p7Item("2", "S2", "L", "0"), p7Item("3", "S3", "L", "-0"), p7Item("4", "S4", "L", "0.1"))),
			stored: p7Arts(p7Art("1", "1", "S1", "L", "1000"), p7Art("1", "2", "S2", "L", "0"), p7Art("1", "3", "S3", "L", "0"), p7Art("1", "4", "S4", "L", "0.1"))},
		{name: "price_out_of_range", doc: p7One(p7Item("1", "S1", "L", "1e400")), wheres: "c0/i0/price", says: []string{priceReason}},
		{name: "price_negative", doc: p7One(p7Item("1", "S1", "L", "-1")), wheres: "c0/i0/price", says: []string{"el precio no puede ser negativo."}},
		{name: "subcategory_reference_is_trimmed", doc: sub("a", "a"+p7NBSP), stored: p7Art("1", "1", "S", "L", "1"), raw: []string{`"subcategory": "a"`}},
		{name: "subcategory_declared_with_a_space_is_unreachable", doc: sub("a"+p7NBSP, "a"+p7NBSP), wheres: "c0/i0/subcategory",
			says: []string{`la subcategoría "a" no está declarada en su categoría`}},
		{name: "variant_codes_differing_only_in_spaces_are_distinct",
			doc:    p7One(p7RawItem("S", `,"variants":[{"code":"V1","label":"a","price":1},{"code":`+p7Quote("V1"+p7NBSP)+`,"label":"b","price":2}]`)),
			stored: p7Art("1", "1", "S", "L", "1"), raw: []string{`{"code": "V1", "label": "a", "price": 1}, {"code": ` + p7Quote("V1"+p7NBSP) + `, "label": "b", "price": 2}`}},
		{name: "component_with_a_trailing_space_does_not_resolve",
			doc:    p7Doc(p7Cat("1", "A", p7Item("1", "PAN", "L", "1"), `{"code":"2","sku":"COMBO","label":"C","price":1,"components":[{"sku":`+p7Quote("PAN"+p7NBSP)+`,"qty":1}]}`)),
			wheres: "c0/i1/components[0].sku", says: []string{`no existe en el catálogo`}},
		{name: "tag_only_spaces", doc: p7One(p7RawItem("S", `,"tags":["a@@b",`+p7Quote(p7NBSP)+`,"`+p7Arabic+`"]`)), wheres: "c0/i0/tags[1]", says: []string{"la etiqueta 2 está vacía."}},
		{name: "tags_stored_untouched", doc: p7One(p7RawItem("S", `,"tags":["a@@b",`+p7Quote(p7NBSP+"x"+p7NBSP)+`,"`+p7Arabic+`"]`)), stored: p7Art("1", "1", "S", "L", "1"),
			raw: []string{`"tags": ["a@@b", ` + p7Quote(p7NBSP+"x"+p7NBSP) + `, "` + p7Arabic + `"]`}},
		{name: "format_with_a_trailing_space", doc: []byte(`{"format":` + p7Quote("wapp.catalog_import"+p7NBSP) + `,"version":1,"catalog":{"categories":[]}}`),
			wheres: "r0/format", says: []string{"aquí solo se importan catálogos"}},
		{name: "version_as_text", doc: []byte(`{"format":"wapp.catalog_import","version":"1","catalog":{"categories":[]}}`),
			wheres: "r0/version", says: []string{`el campo "version" debe ser un número: 1.`}},
		{name: "version_as_non_ascii_digit", doc: []byte(`{"format":"wapp.catalog_import","version":١,"catalog":{"categories":[]}}`),
			wheres: "r0/documento", says: []string{"hacia la línea 1."}},
		{name: "version_1_point_0", doc: []byte(`{"format":"wapp.catalog_import","version":1.0,"catalog":{"categories":[` + p7Cat("1", "A", p7Item("1", "S", "L", "1")) + `]}}`),
			stored: p7Art("1", "1", "S", "L", "1")},
		{name: "foreign_fields_ignored",
			doc: []byte(`{"format":"wapp.catalog_import","version":1,"tenant_id":"otra-empresa","ref":"otra-ref","catalog":{"tenant_id":"otra-empresa","categories":[` +
				`{"code":"1","label":"A","extra":1,"items":[` + p7RawItem("S", `,"extra":"z","description":`+p7Quote(p7NBSP+"d"+p7NBSP)) + `]}]}}`),
			stored: p7Art("1", "1", "S", "L", "1"), raw: []string{`"description": ` + p7Quote(p7NBSP+"d"+p7NBSP)}, absent: []string{"extra", "tenant_id", "otra-"}},
	}
}

// adversarialRefs afirma que la ref del query no se valida ni se recorta: un separador repetido, un
// espacio U+00A0 solo y unos dígitos no ASCII son tres refs distintas, guardadas tal cual; la ref
// vacía es la de por defecto (`catalogo`). La ref por defecto se mira en validate, para no tocar el
// catálogo del escenario.
func (w *p7World) adversarialRefs(t *testing.T) {
	refs := []string{"a@@b", p7NBSP, p7Arabic, "catalogo" + p7NBSP, "CATALOGO"}
	for _, ref := range refs {
		res := p7Decode(t, w.importJSON(t, w.admin, "apply", ref, p7One(p7Item("1", "S", "L", "1"))))
		if !res.Applied || res.Ref != ref || res.diff() != "|S|||0" || res.ArchivedVersion != 0 {
			t.Errorf("import a la ref %q: %+v; quería aplicado en esa ref, como alta y sin archivar", ref, res)
		}
		if got := p7Stored(t, w.sc, w.admin.tenant, ref); got != p7Art("1", "1", "S", "L", "1") {
			t.Errorf("la ref %q guardó %s", ref, got)
		}
	}
	w.refsAreListed(t, refs)
	w.defaultRefOnlyLooked(t)
}

// refsAreListed afirma que las refs adversarias se listan y se leen por GET /api/v1/tenant-content,
// cada una por su nombre exacto (URL-escapado en la ruta).
func (w *p7World) refsAreListed(t *testing.T, refs []string) {
	t.Helper()
	listed := w.send(t, w.admin, "", http.MethodGet, p7RouteContent, "", nil)
	var list []struct {
		Ref string `json:"ref"`
	}
	listed.JSON(t, &list)
	names := make(map[string]bool, len(list))
	for _, e := range list {
		names[e.Ref] = true
	}
	for _, ref := range refs {
		if !names[ref] {
			t.Errorf("GET /api/v1/tenant-content no lista la ref %q", ref)
		}
		got := w.send(t, w.admin, "", http.MethodGet, p7RouteContent+"/"+url.PathEscape(ref), "", nil)
		if got.Codigo != http.StatusOK || !strings.Contains(string(got.Cuerpo), `"S"`) {
			t.Errorf("GET /api/v1/tenant-content/{ref} con la ref %q: HTTP %d %s", ref, got.Codigo, recortar(got.Cuerpo))
		}
	}
}

// defaultRefOnlyLooked afirma que sin ref, o con la ref vacía, el import va a la de por defecto
// (`catalogo`), y que sin modo solo mira: el catálogo del escenario sale entero de baja en el diff y
// nada se escribe.
func (w *p7World) defaultRefOnlyLooked(t *testing.T) {
	t.Helper()
	before := p7Mark(t, w.sc, w.admin.tenant)
	for _, query := range []string{"?mode=validate&ref=", "?mode=validate", ""} {
		r := w.send(t, w.admin, p7ActContent, http.MethodPost, p7RouteImport+query, "application/json", p7One(p7Item("1", "S", "L", "1")))
		if res := p7Decode(t, r.respuesta); r.Codigo != http.StatusOK || res.Ref != draftCatalogRef || res.Applied || len(res.Diff.Removed) != draftCatalogItems {
			t.Errorf("import con %q: HTTP %d ref %q aplicado %v con %d bajas; quería mirar la ref por defecto, con el catálogo del escenario entero de baja",
				query, r.Codigo, res.Ref, res.Applied, len(res.Diff.Removed))
		}
	}
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Errorf("mirar la ref por defecto escribió contenido")
	}
}
