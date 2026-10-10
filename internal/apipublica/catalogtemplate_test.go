//go:build pendiente

package apipublica_test

// catalogtemplate_test.go — cubre el contrato de catalogtemplate.go (MountCatalogTemplate): el
// montaje con la condición de I14, la cadena R con su gate, los tres formatos de la plantilla
// (I16) y el prompt (I17). Los dobles del import (catalogImport…) vienen de
// catalogimport_test.go, los de la planilla (catalogTabular…) de catalogtabular_test.go y los
// lectores de CSV y XLSX (export…) de export_test.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

const (
	catalogTemplatePattern = "GET /api/v1/catalog/import/template"
	catalogTemplateTarget  = "/api/v1/catalog/import/template"
	catalogPromptPattern   = "GET /api/v1/catalog/import/prompt"
	catalogPromptTarget    = "/api/v1/catalog/import/prompt"

	catalogTemplateJSONType  = "application/json; charset=utf-8"
	catalogTemplateMsgFormat = "format inválido: usa json, csv o xlsx"
)

// catalogTemplateTargets son las dos rutas de lectura del import, con su id del mapa.
func catalogTemplateTargets() map[string]string {
	return map[string]string{"I16": catalogTemplateTarget, "I17": catalogPromptTarget}
}

// catalogTemplateMountAll monta las cuatro rutas del import: las descargas se prueban subiéndolas
// a su puerta.
func catalogTemplateMountAll(c *apipublica.Cara, k apipublica.Common, d apipublica.CatalogImportDeps) {
	apipublica.MountCatalogImport(c, k, d)
	apipublica.MountCatalogTabular(c, k, d)
	apipublica.MountCatalogTemplate(c, k, d)
}

// catalogTemplateGet hace un GET como tenantID con SOLO el permiso de lectura.
func catalogTemplateGet(rig catalogImportRig, tenantID, target string) *httptest.ResponseRecorder {
	return rig.h.Call(rig.cara, rig.h.With(tenantID, catalogImportPermRead), http.MethodGet, target, "")
}

// catalogTemplateDownload descarga la plantilla en ese formato y exige el 200.
func catalogTemplateDownload(t *testing.T, rig catalogImportRig, format string) *httptest.ResponseRecorder {
	t.Helper()
	rec := catalogTemplateGet(rig, tenantA, catalogTemplateTarget+"?format="+format)
	wantCode(t, "plantilla "+format, rec, http.StatusOK)
	return rec
}

// catalogTemplateWantFile exige las tres cabeceras de una descarga de la plantilla.
func catalogTemplateWantFile(t *testing.T, what string, rec *httptest.ResponseRecorder, contentType, format string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != contentType {
		t.Errorf("%s: Content-Type %q, quiero %q", what, got, contentType)
	}
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="catalogo-plantilla.`+format+`"`; got != want {
		t.Errorf("%s: Content-Disposition %q, quiero %q (sin fecha: la nueva pisa a la vieja)", what, got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("%s: Content-Length %q, quiero %q", what, got, want)
	}
}

// catalogTemplateCell es una celda de TemplateSheetRows tal como debe leerse en la planilla: el
// texto tal cual y el precio pelado, sin símbolo ni separador de miles.
func catalogTemplateCell(t *testing.T, cell any) string {
	t.Helper()
	switch v := cell.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		t.Fatalf("celda de la plantilla de tipo %T; el contrato solo da string y float64", cell)
		return ""
	}
}

func TestMountCatalogTemplate_Chain(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTemplate)
	wantPatterns(t, "I16 e I17", rig.cara, []string{catalogTemplatePattern, catalogPromptPattern})
	for id, target := range catalogTemplateTargets() {
		checkChain(t, rig.h, rig.cara, routeCase{id: id, method: http.MethodGet, target: target,
			perm: catalogImportPermRead, want: http.StatusOK})
	}
	// Ninguna de las dos toca un almacén, jamás.
	catalogImportWantStoreCalls(t, "I16 e I17", rig.store, 0, 0)
}

// TestMountCatalogTemplate_MissingDependencyIs404: la plantilla y el prompt montan con la MISMA
// condición que I14 —también exigen los dos puertos que no usan—: aparecen y desaparecen con el
// POST.
func TestMountCatalogTemplate_MissingDependencyIs404(t *testing.T) {
	cases := map[string]func(*apipublica.CatalogImportDeps){
		"no_content_reader": func(d *apipublica.CatalogImportDeps) { d.Content = nil },
		"no_version_writer": func(d *apipublica.CatalogImportDeps) { d.ContentVersions = nil },
		"no_resolver":       func(d *apipublica.CatalogImportDeps) { d.Entitlements = nil },
	}
	for name, strip := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogTemplate, strip)
			wantPatterns(t, name, rig.cara, nil)
			for id, target := range catalogTemplateTargets() {
				wantCode(t, name+" "+id, catalogTemplateGet(rig, tenantA, target), http.StatusNotFound)
			}

			d := catalogImportDeps(rig.store)
			strip(&d)
			if v := recuperar(func() { apipublica.MountCatalogTemplate(apipublica.Nueva(), apipublica.Common{}, d) }); v != nil {
				t.Errorf("%s con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", name, v)
			}
		})
	}
}

func TestMountCatalogTemplate_NilMWPanicsAtMount(t *testing.T) {
	d := catalogImportDeps(&catalogImportStoreSpy{repo: store.NewMemoryRepository()})
	v := recuperar(func() { apipublica.MountCatalogTemplate(apipublica.Nueva(), apipublica.Common{}, d) })
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountCatalogTemplate") {
		t.Errorf("MountCatalogTemplate con MW nil: panic = %v; quiero un panic de cableado que nombre MountCatalogTemplate", v)
	}
}

// TestMountCatalogTemplate_FeatureGate: repartir la plantilla a quien no puede importar sería
// enseñar la puerta y no dar la llave. El permiso se pregunta antes que la feature, el gate es
// fail-closed y una lectura no deja registro ni cuando la corta el gate.
func TestMountCatalogTemplate_FeatureGate(t *testing.T) {
	cases := map[string]func(*apipublica.CatalogImportDeps){
		"without_the_feature": func(d *apipublica.CatalogImportDeps) { d.Entitlements = withFeatures() },
		"resolver_fails":      func(d *apipublica.CatalogImportDeps) { d.Entitlements = catalogImportFailingResolver{} },
	}
	for name, adjust := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogTemplate, adjust)
			for id, target := range catalogTemplateTargets() {
				rec := catalogTemplateGet(rig, tenantA, target)
				wantCode(t, name+" "+id, rec, http.StatusForbidden)
				wantExactBody(t, name+" "+id, rec, catalogImportDenied)
				if got := rec.Header().Get("Content-Disposition"); got != "" {
					t.Errorf("%s %s: el 403 lleva Content-Disposition %q", name, id, got)
				}

				rec = rig.h.Call(rig.cara, rig.h.With(tenantA, "otra.cosa"), http.MethodGet, target, "")
				wantCode(t, name+" "+id+" sin permiso", rec, http.StatusForbidden)
				wantErrorBody(t, name+" "+id+" sin permiso", rec, "permiso denegado")
			}
			if n := len(rig.h.Auditor().Records()); n != 0 {
				t.Errorf("%s: quedaron %d registros de auditoría, quiero 0 (son lecturas)", name, n)
			}
		})
	}
}

// TestMountCatalogTemplate_FormatSelection: el formato por defecto es json, solo valen los tres
// literales y uno desconocido no degrada en silencio a otro.
func TestMountCatalogTemplate_FormatSelection(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTemplate)
	body, err := json.MarshalIndent(catalogimport.BuildTemplate(), "", "  ")
	if err != nil {
		t.Fatalf("serializando la plantilla de prueba: %v", err)
	}
	want := string(body) + "\n"

	for _, query := range []string{"", "?format=", "?format=json", "?format=json&ref=otra&mode=apply"} {
		rec := catalogTemplateGet(rig, tenantA, catalogTemplateTarget+query)
		wantCode(t, "json "+query, rec, http.StatusOK)
		wantExactBody(t, "json "+query, rec, want)
		catalogTemplateWantFile(t, "json "+query, rec, catalogTemplateJSONType, "json")
	}
	if strings.Count(want, "\n") < 10 || strings.HasSuffix(want, "\n\n") || !strings.Contains(want, "\n  \"format\": \"wapp.catalog_import\"") {
		t.Errorf("la plantilla JSON no va indentada a dos espacios con UN salto final:\n%s", want)
	}

	for _, format := range []string{"xls", "JSON", "CSV", "Xlsx", "%20csv", "csv%20", "yaml", "json,csv"} {
		rec := catalogTemplateGet(rig, tenantA, catalogTemplateTarget+"?format="+format)
		wantCode(t, "formato "+format, rec, http.StatusBadRequest)
		wantErrorBody(t, "formato "+format, rec, catalogTemplateMsgFormat)
		if got := rec.Header().Get("Content-Disposition"); got != "" {
			t.Errorf("formato %s: el 400 lleva Content-Disposition %q", format, got)
		}
	}

	// La plantilla es idéntica para todos: no lleva nada del tenant ni toca un almacén.
	other := catalogTemplateGet(rig, tenantB, catalogTemplateTarget)
	wantExactBody(t, "otro tenant", other, want)
	catalogImportWantStoreCalls(t, "plantilla", rig.store, 0, 0)
}

// TestMountCatalogTemplate_CSVIsTheCanonicalSheet: BOM, CRLF, la cabecera exacta del contrato
// tabular y una fila por artículo de TemplateSheetRows, con el precio pelado.
func TestMountCatalogTemplate_CSVIsTheCanonicalSheet(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTemplate)
	rec := catalogTemplateDownload(t, rig, "csv")
	catalogTemplateWantFile(t, "csv", rec, exportCSVType, "csv")

	body := rec.Body.String()
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") || !strings.HasSuffix(body, "\r\n") {
		t.Errorf("csv: el fin de línea no es CRLF en todas las filas: %q", body)
	}
	records := exportParseCSV(t, body)
	sheet := catalogimport.TemplateSheetRows()
	if len(records) != len(sheet)+1 || len(sheet) == 0 {
		t.Fatalf("csv: %d filas, quiero la cabecera más %d artículos", len(records), len(sheet))
	}
	if got, want := strings.Join(records[0], ","), strings.Join(catalogimport.TabularColumns(), ","); got != want {
		t.Errorf("csv: cabecera %q, quiero %q", got, want)
	}
	for i, row := range sheet {
		for j, cell := range row {
			if got, want := records[i+1][j], catalogTemplateCell(t, cell); got != want {
				t.Errorf("csv: fila %d, columna %s = %q, quiero %q", i+2, records[0][j], got, want)
			}
		}
	}
	if records[1][5] != "18000" {
		t.Errorf("csv: precio %q, quiero 18000 pelado (con «$18.000» el import lo rechazaría)", records[1][5])
	}
}

// TestMountCatalogTemplate_XLSXHasTheSheetByName: el libro se reabre, trae la hoja «catalogo»
// con las mismas filas, el precio como NÚMERO y el texto como cadena.
func TestMountCatalogTemplate_XLSXHasTheSheetByName(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTemplate)
	rec := catalogTemplateDownload(t, rig, "xlsx")
	catalogTemplateWantFile(t, "xlsx", rec, exportXLSXType, "xlsx")

	book := exportOpenXLSX(t, rec.Body.Bytes())
	if got := book.GetSheetList(); len(got) != 1 || got[0] != "catalogo" || catalogimport.TabularSheetName != "catalogo" {
		t.Fatalf("xlsx: hojas %q, quiero solo «catalogo»", got)
	}
	rows, err := book.GetRows(catalogimport.TabularSheetName, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("xlsx: leyendo la hoja: %v", err)
	}
	sheet := catalogimport.TemplateSheetRows()
	if len(rows) != len(sheet)+1 {
		t.Fatalf("xlsx: %d filas, quiero la cabecera más %d artículos", len(rows), len(sheet))
	}
	if got, want := strings.Join(rows[0], ","), strings.Join(catalogimport.TabularColumns(), ","); got != want {
		t.Errorf("xlsx: cabecera %q, quiero %q", got, want)
	}
	if rows[1][5] != catalogTemplateCell(t, sheet[0][5]) {
		t.Errorf("xlsx: precio de F2 = %q, quiero %q", rows[1][5], catalogTemplateCell(t, sheet[0][5]))
	}

	// El precio como número y el sku como texto, mirados juntos: excelize no marca tipo a los
	// números y sí a los textos.
	price, err := book.GetCellType(catalogimport.TabularSheetName, "F2")
	if err != nil {
		t.Fatalf("xlsx: leyendo el tipo de F2: %v", err)
	}
	sku, err := book.GetCellType(catalogimport.TabularSheetName, "D2")
	if err != nil {
		t.Fatalf("xlsx: leyendo el tipo de D2: %v", err)
	}
	if price == excelize.CellTypeSharedString || price == excelize.CellTypeInlineString || sku == price {
		t.Errorf("xlsx: tipos de celda precio=%v sku=%v; quiero el precio como número y el sku como texto", price, sku)
	}
}

// TestMountCatalogTemplate_EveryDownloadImportsAsTheSameCatalog: la plantilla descargada se
// importa sin un solo defecto, y las tres —cada una por su puerta— escriben el MISMO blob.
func TestMountCatalogTemplate_EveryDownloadImportsAsTheSameCatalog(t *testing.T) {
	rig := catalogImportSetup(t, catalogTemplateMountAll)

	template := catalogTemplateDownload(t, rig, "json").Body.String()
	for _, field := range []string{`"variants"`, `"components"`, `"tags"`, `"attributes"`} {
		if !strings.Contains(template, field) {
			t.Errorf("la plantilla JSON no trae el caso %s", field)
		}
	}
	rec := rig.post("?mode=apply&ref=via-json", template)
	wantCode(t, "plantilla JSON por I14", rec, http.StatusOK)
	var got struct {
		Items int `json:"items"`
	}
	wantJSON(t, "plantilla JSON por I14", rec, &got)
	if want := len(catalogimport.TemplateSheetRows()); got.Items != want {
		t.Errorf("plantilla JSON por I14: items = %d, quiero los %d artículos de la plantilla", got.Items, want)
	}

	for _, format := range []string{"csv", "xlsx"} {
		file := catalogTemplateDownload(t, rig, format).Body.Bytes()
		rec = catalogTabularPost(t, rig, "?mode=apply&ref=via-"+format, "catalogo."+format, file)
		wantCode(t, "plantilla "+format+" por I15", rec, http.StatusOK)
	}

	want := rig.blob(t, tenantA, "via-json")
	for _, sku := range []string{"TORTA-CHOC", "TEQUENOS-15", "REFRESCO-15L"} {
		if !strings.Contains(want, sku) {
			t.Fatalf("el blob de la plantilla no trae %s: %s", sku, want)
		}
	}
	for _, ref := range []string{"via-csv", "via-xlsx"} {
		if blob := rig.blob(t, tenantA, ref); blob != want {
			t.Errorf("%s escribió otro catálogo:\n  %s\nquiero el del JSON\n  %s", ref, blob, want)
		}
	}
}

// TestMountCatalogTemplate_Prompt: el prompt viaja tal cual, con el format y la versión del
// contrato al que corresponde, y no lee ningún parámetro.
func TestMountCatalogTemplate_Prompt(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTemplate)
	prompt, err := json.Marshal(catalogimport.ImportPrompt())
	if err != nil {
		t.Fatalf("serializando el prompt de prueba: %v", err)
	}
	want := `{"format":"wapp.catalog_import","version":` + strconv.Itoa(catalogimport.ImportVersion) + `,"prompt":` + string(prompt) + `}`

	for _, query := range []string{"", "?format=csv", "?version=9&mode=apply"} {
		rec := catalogTemplateGet(rig, tenantA, catalogPromptTarget+query)
		wantCode(t, "prompt "+query, rec, http.StatusOK)
		wantExactBody(t, "prompt "+query, rec, want)
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("prompt %s: Content-Type %q, quiero application/json", query, got)
		}
	}
	wantExactBody(t, "prompt de otro tenant", catalogTemplateGet(rig, tenantB, catalogPromptTarget), want)

	var got struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Prompt  string `json:"prompt"`
	}
	wantJSON(t, "prompt", catalogTemplateGet(rig, tenantA, catalogPromptTarget), &got)
	if got.Format != catalogimport.ImportFormat || got.Version != catalogimport.ImportVersion || got.Prompt != catalogimport.ImportPrompt() || got.Prompt == "" {
		t.Errorf("prompt: %q/%d con %d bytes de texto; quiero el format, la versión y el texto del contrato", got.Format, got.Version, len(got.Prompt))
	}
	catalogImportWantStoreCalls(t, "prompt", rig.store, 0, 0)
}
