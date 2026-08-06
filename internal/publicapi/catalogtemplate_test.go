package publicapi_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// TestCatalogTemplate_LaPlantillaDescargadaSeImportaSinUnSoloError es EL criterio
// de T3.2, y se afirma por el camino largo A PROPÓSITO: se DESCARGA la plantilla
// por HTTP y se SUBE tal cual al import, sin tocar un byte.
//
// Un test que validara BuildTemplate() en memoria dejaría fuera justo lo que puede
// romperse entre los dos extremos: la serialización, la indentación, el salto final
// y el techo de bytes. Lo que aquí se afirma es la promesa que el dueño del negocio
// da por hecha —«descargo la plantilla, la lleno, la subo y funciona»— con el único
// paso que él no da: llenarla.
func TestCatalogTemplate_LaPlantillaDescargadaSeImportaSinUnSoloError(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	plantilla := rec.Body.String()

	got := postImport(t, api, keyAContent, "?mode=validate", plantilla)
	if got.Items != 5 {
		t.Fatalf("items=%d, quiero los 5 artículos de la plantilla", got.Items)
	}
	if len(got.Diff.Added) != 5 {
		t.Fatalf("added=%d, quiero 5 (sobre una ref vacía, todo es alta)", len(got.Diff.Added))
	}

	// Y los cuatro casos llegaron enteros al otro lado: el validador los aceptó y
	// el documento que devolvería el apply los conserva.
	if !strings.Contains(plantilla, `"variants"`) || !strings.Contains(plantilla, `"components"`) ||
		!strings.Contains(plantilla, `"tags"`) || !strings.Contains(plantilla, `"attributes"`) {
		t.Fatalf("la plantilla servida no trae los cuatro casos:\n%s", plantilla)
	}
}

// TestCatalogTemplate_JSON_EsLegiblePorUnHumano: la plantilla se abre en un editor
// y se pega en la caja de un LLM, así que va indentada. Una sola línea de 900
// caracteres es ilegible en los dos sitios, y quien no la puede leer no la corrige.
func TestCatalogTemplate_JSON_EsLegiblePorUnHumano(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template?format=json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Count(body, "\n") < 20 {
		t.Fatalf("la plantilla no está indentada (%d saltos de línea)", strings.Count(body, "\n"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q, quiero application/json", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "catalogo-plantilla.json") {
		t.Errorf("Content-Disposition=%q, quiero la descarga nombrada", cd)
	}
}

// TestCatalogTemplate_CSV_EsLaPlanillaCanonica comprueba la planilla tabular que
// T3.4 va a parsear: BOM (sin él Excel en Windows destroza cada tilde), la cabecera
// exacta de D-041.9 y una fila por artículo con la mini-sintaxis en las celdas
// múltiples.
func TestCatalogTemplate_CSV_EsLaPlanillaCanonica(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template?format=csv", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Fatal("el CSV no lleva BOM: Excel en Windows destrozaría cada tilde")
	}

	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatalf("el CSV no se puede releer: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("filas=%d, quiero 6 (cabecera + 5 artículos)", len(records))
	}
	want := catalogimport.TabularColumns()
	if strings.Join(records[0], ",") != strings.Join(want, ",") {
		t.Fatalf("cabecera=%v, quiero %v", records[0], want)
	}
	if records[1][9] != "V1|10-12 porciones|18000; V2|25-30 porciones|32000" {
		t.Errorf("celda de variantes=%q, quiero la mini-sintaxis de D-041.9", records[1][9])
	}
	if records[5][10] != "TEQUENOS-15|1; REFRESCO-15L|2" {
		t.Errorf("celda de componentes=%q, quiero la mini-sintaxis de D-041.9", records[5][10])
	}
	// El precio va sin símbolo ni separador de miles: es un dato, no presentación
	// (D-04, sin moneda tipada). Con "$18.000" el import lo rechazaría.
	if records[1][5] != "18000" {
		t.Errorf("precio=%q, quiero 18000 pelado", records[1][5])
	}
}

// TestCatalogTemplate_XLSX_SeAbreYTraeLaHojaPorNombre reabre el libro como lo haría
// Excel o LibreOffice. La hoja se busca POR NOMBRE porque es así como el parser de
// T3.4 tendrá que encontrarla: quien reciba el archivo puede añadir hojas suyas y
// dejarlas primero.
func TestCatalogTemplate_XLSX_SeAbreYTraeLaHojaPorNombre(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template?format=xlsx", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("el XLSX no se puede abrir: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Logf("cerrando el libro: %v", cerr)
		}
	}()

	rows, err := f.GetRows(catalogimport.TabularSheetName)
	if err != nil {
		t.Fatalf("no existe la hoja %q: %v", catalogimport.TabularSheetName, err)
	}
	if len(rows) != 6 {
		t.Fatalf("filas=%d, quiero 6 (cabecera + 5 artículos)", len(rows))
	}
	if strings.Join(rows[0], ",") != strings.Join(catalogimport.TabularColumns(), ",") {
		t.Fatalf("cabecera=%v", rows[0])
	}

	// El precio se escribió como NÚMERO y el sku como TEXTO, y hay que mirar los dos
	// juntos para que la aserción signifique algo: excelize no marca tipo a los
	// números (los deja «unset», que es como XLSX dice «número») y sí marca los
	// textos. Si el precio saliera como cadena, la hoja no lo sumaría y el dueño lo
	// devolvería con separador de miles, que es justo lo que el import rechaza.
	precio, err := f.GetCellType(catalogimport.TabularSheetName, "F2")
	if err != nil {
		t.Fatalf("leyendo el tipo de F2: %v", err)
	}
	if precio == excelize.CellTypeSharedString || precio == excelize.CellTypeInlineString {
		t.Errorf("el precio de F2 se escribió como TEXTO (%v): la hoja no lo sumaría", precio)
	}
	sku, err := f.GetCellType(catalogimport.TabularSheetName, "D2")
	if err != nil {
		t.Fatalf("leyendo el tipo de D2: %v", err)
	}
	if sku == precio {
		t.Errorf("el sku y el precio tienen el mismo tipo de celda (%v): uno de los dos está mal", sku)
	}
}

// TestCatalogTemplate_FormatoDesconocido_400: «xls» tecleado a las prisas no puede
// degradar en silencio a otro formato, por lo mismo que el modo del import no se
// adivina.
func TestCatalogTemplate_FormatoDesconocido_400(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template?format=xls", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogTemplate_Prompt_LlevaLaVersionDelContrato: el prompt-plantilla se sirve
// con el format y la versión a los que corresponde, para que la consola no muestre
// el texto de un contrato que este servidor ya no acepta.
func TestCatalogTemplate_Prompt_LlevaLaVersionDelContrato(t *testing.T) {
	api, _ := importAPI()

	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/prompt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if got.Format != catalogimport.ImportFormat || got.Version != catalogimport.ImportVersion {
		t.Fatalf("cabecera del prompt = %q/%d, quiero la del contrato", got.Format, got.Version)
	}
	if !strings.Contains(got.Prompt, catalogimport.ImportFormat) {
		t.Fatalf("el prompt no dicta el format del contrato:\n%s", got.Prompt)
	}
}

// TestCatalogTemplate_SinFeature_403: la plantilla y el prompt son la primera mitad
// del mismo acto que el POST, así que llevan el MISMO gate. Repartir la plantilla a
// quien no puede importar sería enseñarle la puerta sin darle la llave: llenaría el
// archivo para encontrarse un 403 al subirlo.
func TestCatalogTemplate_SinFeature_403(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo},
		Entitlements: entitlements.NewFake(), // ninguna feature encendida
	}, apiKeys())

	for _, ruta := range []string{
		"/api/v1/catalog/import/template",
		"/api/v1/catalog/import/template?format=csv",
		"/api/v1/catalog/import/prompt",
	} {
		rec := call(api, keyAContent, http.MethodGet, ruta, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s → code=%d, quiero 403; body=%s", ruta, rec.Code, rec.Body.String())
		}
	}
}

// TestCatalogTemplate_SinScope_403YSinIdentidad_401: la plantilla es lectura de
// contenido del tenant (content.read) aunque no lea la BD. Un portador con otros
// grants no la baja, y sin token no hay nada que bajar.
func TestCatalogTemplate_SinScope_403YSinIdentidad_401(t *testing.T) {
	api, _ := importAPI()

	if rec := call(api, keyARead, http.MethodGet, "/api/v1/catalog/import/template", ""); rec.Code != http.StatusForbidden {
		t.Errorf("con flows.read code=%d, quiero 403", rec.Code)
	}
	if rec := call(api, "", http.MethodGet, "/api/v1/catalog/import/template", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin token code=%d, quiero 401", rec.Code)
	}
}
