//go:build integracion

package procesos

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// La planilla de P7 (POST /api/v1/catalog/import/tabular): el MISMO import por otra puerta. Sube el
// CSV o el XLSX en un multipart y, a partir de leerlo, es el mismo validador, el mismo diff y el
// mismo versionado; lo que cambia es que los defectos salen ubicados por FILA y que la respuesta trae
// el documento normalizado, para confirmar en dos pasos sin volver a pedir el archivo.

const (
	// p7TabularRef es la ref sobre la que se recorre la planilla.
	p7TabularRef = "p7-planilla"

	// Los motivos de un archivo que no se deja leer (sin fila: el problema es del archivo).
	p7ErrNoFile   = "no llegó ningún archivo: súbelo en el campo «file» de un formulario multipart/form-data."
	p7ErrNotExcel = "no se pudo abrir el archivo de Excel: comprueba que sea el .xlsx que descargaste de la plantilla."
)

// tabularImport afirma el camino feliz de la planilla: las tres plantillas descargadas —JSON, CSV y
// XLSX— subidas cada una por su puerta escriben el MISMO blob; la confirmación en dos pasos (validate
// de la planilla → su `document` al import JSON) escribe lo mismo que aplicarla de una vez; mirar no
// escribe; la versión archivada recuerda por qué puerta entró el acto que la desplazó; y el CSV se lee
// con coma, con punto y coma (Excel en español), con tabulador y con BOM.
func (w *p7World) tabularImport(t *testing.T) {
	sc, tenant := w.sc, w.admin.tenant
	template := func(format string) []byte {
		r := w.send(t, w.admin, "", http.MethodGet, p7RouteTemplate+"?format="+format, "", nil)
		if r.Codigo != http.StatusOK {
			t.Fatalf("descargar la plantilla %s: HTTP %d", format, r.Codigo)
		}
		return r.Cuerpo
	}
	asJSON, asCSV, asXLSX := template("json"), template("csv"), template("xlsx")

	viaJSON := p7Decode(t, w.importJSON(t, w.admin, "apply", "p7-via-json", asJSON))
	viaCSV := p7Decode(t, w.importFile(t, w.admin, "apply", "p7-via-csv", asCSV))
	viaXLSX := p7Decode(t, w.importFile(t, w.admin, "apply", "p7-via-xlsx", asXLSX))
	for name, res := range map[string]p7Result{"JSON": viaJSON, "CSV": viaCSV, "XLSX": viaXLSX} {
		if !res.Applied || res.Items != 5 || len(res.Diff.Added) != 5 || res.ArchivedVersion != 0 {
			t.Fatalf("la plantilla %s por su puerta = aplicado %v, %d artículos, diff %q; quería los cinco como alta", name, res.Applied, res.Items, res.diff())
		}
	}
	blob := p7Content(t, sc, tenant, "p7-via-json")
	for _, ref := range []string{"p7-via-csv", "p7-via-xlsx"} {
		if got := p7Content(t, sc, tenant, ref); got != blob {
			t.Errorf("%s escribió otro catálogo que el JSON:\n%s\n%s", ref, got, blob)
		}
	}
	for _, sku := range p7TemplateSKUs {
		if !strings.Contains(blob, `"sku": "`+sku+`"`) {
			t.Errorf("el blob de la plantilla no trae %s: %s", sku, blob)
		}
	}
	if len(viaJSON.Document) != 0 || len(viaCSV.Document) == 0 || len(viaXLSX.Document) == 0 {
		t.Errorf("el documento normalizado: JSON %d bytes, CSV %d, XLSX %d; quería que solo la planilla lo devuelva", len(viaJSON.Document), len(viaCSV.Document), len(viaXLSX.Document))
	}

	w.tabularTwoSteps(t, asXLSX, blob)
	w.tabularVersions(t, asCSV)
	w.tabularDelimiters(t)
}

// tabularTwoSteps afirma la confirmación en dos pasos: el validate de un XLSX no escribe y devuelve un
// documento del contrato (format, version y `source.kind` = planilla) que, mandado TAL CUAL al import
// JSON en apply, deja el mismo blob que la planilla aplicada de una sola vez.
func (w *p7World) tabularTwoSteps(t *testing.T, xlsx []byte, blob string) {
	t.Helper()
	const ref = "p7-dos-pasos"
	before := p7Mark(t, w.sc, w.admin.tenant)
	look := p7Decode(t, w.importFile(t, w.admin, "validate", ref, xlsx))
	if look.Applied || look.Mode != "validate" || look.Items != 5 || len(look.Document) == 0 {
		t.Fatalf("el validate de la planilla = %+v; quería sin aplicar y con el documento normalizado", look)
	}
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Fatalf("el validate de la planilla escribió contenido:\nantes:   %s\ndespués: %s", before, after)
	}
	var doc struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Source  struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	if err := json.Unmarshal(look.Document, &doc); err != nil || doc.Format != "wapp.catalog_import" || doc.Version != 1 || doc.Source.Kind != "planilla" {
		t.Errorf("el documento normalizado = %+v (error %v); quería uno del contrato, de procedencia planilla", doc, err)
	}
	confirmed := p7Decode(t, w.importJSON(t, w.admin, "apply", ref, look.Document))
	if !confirmed.Applied || confirmed.Items != 5 {
		t.Fatalf("confirmar con el documento del validate = %+v", confirmed)
	}
	if got := p7Content(t, w.sc, w.admin.tenant, ref); got != blob {
		t.Errorf("confirmar en dos pasos escribió otra cosa que aplicar de una vez:\n%s\n%s", got, blob)
	}
}

// tabularVersions afirma el diff y la procedencia sobre una ref con contenido: mirar una planilla
// enseña el mismo diff que el import JSON y no escribe; aplicarla archiva lo anterior como versión 1
// con `import_tabular`; y un import JSON detrás archiva la 2 con `import_json`. La procedencia es la
// del ACTO que archivó, no la del blob archivado.
func (w *p7World) tabularVersions(t *testing.T, templateCSV []byte) {
	t.Helper()
	sc, tenant := w.sc, w.admin.tenant
	if res := p7Decode(t, w.importJSON(t, w.admin, "apply", p7TabularRef, p7DocFirst())); !res.Applied {
		t.Fatalf("sembrar la ref de la planilla: %+v", res)
	}
	seeded := p7Content(t, sc, tenant, p7TabularRef)
	next := p7Sheet(t,
		p7Row("1|Bebidas", "", "1", "CAFE", "Café", "2.9"),
		p7Row("1|Bebidas", "", "2", "TE", "Té", "2"),
		p7Row("1|Bebidas", "", "3", "AGUA", "Agua", "1.5"))

	before := p7Mark(t, sc, tenant)
	look := p7Decode(t, w.importFile(t, w.admin, "validate", p7TabularRef, next))
	if look.Applied || look.Items != 3 || look.diff() != "CAFE:2.5>2.9|AGUA|JUGO||1" {
		t.Errorf("mirar la planilla = aplicado %v, %d artículos, diff %q", look.Applied, look.Items, look.diff())
	}
	if after := p7Mark(t, sc, tenant); after != before {
		t.Fatalf("mirar la planilla escribió contenido:\nantes:   %s\ndespués: %s", before, after)
	}

	applied := p7Decode(t, w.importFile(t, w.admin, "apply", p7TabularRef, next))
	if !applied.Applied || applied.ArchivedVersion != 1 || applied.diff() != "CAFE:2.5>2.9|AGUA|JUGO||1" {
		t.Fatalf("aplicar la planilla = %+v", applied)
	}
	fromSheet := p7Content(t, sc, tenant, p7TabularRef)
	if viaJSON := p7Content(t, sc, tenant, p7StrictRef); fromSheet != viaJSON {
		t.Errorf("la planilla y el JSON con el mismo catálogo escribieron blobs distintos:\n%s\n%s", fromSheet, viaJSON)
	}
	p7WantVersions(t, w, p7TabularRef, "1:import_tabular", seeded)

	if res := p7Decode(t, w.importJSON(t, w.admin, "apply", p7TabularRef, p7DocFirst())); res.ArchivedVersion != 2 {
		t.Fatalf("el import JSON tras la planilla archivó la versión %d, quería la 2", res.ArchivedVersion)
	}
	if res := p7Decode(t, w.importFile(t, w.admin, "apply", p7TabularRef, templateCSV)); res.ArchivedVersion != 3 || res.Items != 5 {
		t.Fatalf("la plantilla CSV tras el import JSON = versión %d con %d artículos, quería la 3 con cinco", res.ArchivedVersion, res.Items)
	}
	p7WantVersions(t, w, p7TabularRef, "1:import_tabular,2:import_json,3:import_tabular", seeded, fromSheet, seeded)
}

// tabularDelimiters afirma las tolerancias del transporte, que no son del contrato: el separador se
// reconoce por la cabecera (coma, punto y coma o tabulador), el BOM se descarta, las filas pueden
// venir más cortas, y las filas en blanco —también las que solo traen espacios Unicode— ni cuentan ni
// estorban. Con «;» de separador, la celda de etiquetas (que también usa «;») llega entre comillas.
func (w *p7World) tabularDelimiters(t *testing.T) {
	t.Helper()
	rows := [][]string{p7Columns(), p7Row("1|Tortas", "", "1", "TORTA", "Torta", "18000", "", "decorada; sin_lactosa")}
	want := p7Art("1", "1", "TORTA", "Torta", "18000")
	cases := map[string][]byte{
		"comma":      p7CSV(t, ',', rows),
		"semicolon":  p7CSV(t, ';', rows),
		"tab":        p7CSV(t, '\t', rows),
		"bom":        append([]byte(p7BOM), p7CSV(t, ';', rows)...),
		"short_rows": []byte("categoria,codigo,sku,nombre,precio,tags\n1|Tortas,1,TORTA,Torta,18000,decorada; sin_lactosa\n,,\n"),
		"blank_rows": p7Sheet(t, p7Row(), rows[1], p7Row("\u00a0", "\u3000", " "), p7Row()),
	}
	for name, file := range cases {
		ref := "p7-separador-" + name
		res := p7Decode(t, w.importFile(t, w.admin, "apply", ref, file))
		if !res.Applied || res.Items != 1 {
			t.Errorf("%s: aplicado %v con %d artículos; quería uno\n%s", name, res.Applied, res.Items, res.reasons())
			continue
		}
		if got := p7Stored(t, w.sc, w.admin.tenant, ref); got != want {
			t.Errorf("%s: guardó %s, quería %s", name, got, want)
		}
		if got := p7Content(t, w.sc, w.admin.tenant, ref); !strings.Contains(got, `"tags": ["decorada", "sin_lactosa"]`) {
			t.Errorf("%s: las etiquetas no se leyeron como dos: %s", name, got)
		}
	}
}

// tabularRejections afirma los rechazos de la planilla, en mode=apply: la misma forma que el import
// JSON (400 `validation_failed` y la lista), pero con cada defecto ubicado por FILA y nombrando la
// COLUMNA; los que juzga el validador del JSON (sku repetido) salen igualmente por fila; los defectos
// van en orden de fila, no de categoría; y una fila que no se puede leer no fabrica defectos en otras.
// Un archivo que no llega, o que no se deja abrir, se dice como defecto del archivo. Nada escribe.
func (w *p7World) tabularRejections(t *testing.T) {
	before := p7Mark(t, w.sc, w.admin.tenant)
	cases := []p7Rejection{
		{"price_not_a_number", p7Sheet(t,
			p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("1|Tortas", "", "2", "TORTA-UNI", "Torta de unicornio", "$18.000")),
			"r3/precio", []string{`la fila 3 ("Torta de unicornio"): el precio "$18.000" no es un número; escríbelo sin símbolo de moneda y sin separadores de miles (18000, no "$18.000").`}},
		{"empty_price", p7Sheet(t, p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "")),
			"r2/precio", []string{`la fila 2 ("Torta de chocolate") no tiene el precio: es obligatorio.`}},
		{"repeated_sku_located_by_row", p7Sheet(t,
			p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000"),
			p7Row("2|Salados", "", "2", "TORTA-CHOC", "Torta salada", "20000")),
			"r4/sku", []string{"único en TODO el catálogo"}},
		{"defects_in_row_order", p7Sheet(t,
			p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("2|Salados", "", "1", "TEQUENOS", "", "26000"),
			p7Row("1|Tortas", "", "2", "", "Torta de unicornio", "26000")),
			"r3/nombre r4/sku", []string{"no tiene nombre", "no tiene el sku"}},
		{"cells_badly_written", p7Sheet(t,
			p7Row("Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("2|Salados", "", "1", "TEQUENOS", "Tequeños", "26000", "", "", "", "V1|grande", ""),
			p7Row("2|Salados", "", "2", "COMBO", "Combo", "32000", "", "", "", "", "TEQUENOS")),
			"r2/categoria r3/variantes r4/componentes", []string{"«1|Tortas»", "«V1|10-12 porciones|18000»", "«TEQUENOS-15|1»"}},
		{"one_category_one_name", p7Sheet(t,
			p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("1|Pasteles", "", "2", "TORTA-UNI", "Torta de unicornio", "26000")),
			"r3/categoria", []string{`la fila 3 llama "Pasteles" a la categoría "1" y la fila 2 la llamó "Tortas"`}},
		{"unreadable_row_invents_nothing", p7Sheet(t,
			p7Row("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000"),
			p7Row("Salados", "", "1", "TEQUENOS-15", "Tequeños", "26000"),
			p7Row("1|Tortas", "", "2", "COMBO", "Combo fiesta", "32000", "", "", "", "", "TEQUENOS-15|1")),
			"r3/categoria", nil},
		{"header_without_required_columns", p7CSV(t, ',', [][]string{{"categoria", "nombre", "precio"}, {"1|Tortas", "Torta de chocolate", "18000"}}),
			"r1/cabecera", []string{"le faltan columnas (codigo, sku)"}},
		{"header_only", p7Sheet(t), "r1/planilla", []string{"solo trae la cabecera"}},
		{"unfilled_rows", p7Sheet(t, p7Row(), p7Row(), p7Row()), "r0/categories", []string{"no trae ninguna categoría"}},
		{"empty_file", []byte{}, "r0/planilla", []string{"la planilla está vacía"}},
		{"fake_zip", []byte("PK\x03\x04 esto no es un libro"), "r0/archivo", []string{p7ErrNotExcel}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { w.wantRejected(t, w.importFile(t, w.admin, "apply", p7TabularRef, tc.doc), tc) })
	}

	t.Run("no_file", func(t *testing.T) {
		wrongField, contentType := p7Multipart(t, "archivo", p7Sheet(t, p7Row("1|Tortas", "", "1", "TORTA", "Torta", "1")))
		bodies := map[string]p7Reply{
			"wrong_field":    w.send(t, w.admin, p7ActContent, http.MethodPost, p7RouteTabular+p7Query("apply", p7TabularRef), contentType, wrongField),
			"json_body":      w.send(t, w.admin, p7ActContent, http.MethodPost, p7RouteTabular+p7Query("apply", p7TabularRef), "application/json", p7DocFirst()),
			"empty_boundary": w.send(t, w.admin, p7ActContent, http.MethodPost, p7RouteTabular+p7Query("apply", p7TabularRef), "multipart/form-data; boundary=nada", []byte{}),
		}
		for name, r := range bodies {
			w.wantRejected(t, r.respuesta, p7Rejection{name: name, wheres: "r0/archivo", says: []string{p7ErrNoFile}})
		}
	})
	t.Run("unknown_mode", func(t *testing.T) {
		for _, mode := range []string{"aply", "APPLY", "apply\u00a0"} {
			if r := w.importFile(t, w.admin, mode, p7TabularRef, p7Sheet(t, p7Row("1|Tortas", "", "1", "TORTA", "Torta", "1"))); !p9ErrorIs(r, http.StatusBadRequest, p7ErrMode) {
				t.Errorf("mode=%q: HTTP %d %s; quería 400 «%s»", mode, r.Codigo, recortar(r.Cuerpo), p7ErrMode)
			}
		}
	})
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Errorf("una planilla rechazada escribió o archivó contenido:\nantes:   %s\ndespués: %s", before, after)
	}
}
