package publicapi_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const tabularRoute = "/api/v1/catalog/import/tabular"

// ============================ andamiaje ============================

// subirPlanilla ejecuta el POST multipart contra la ruta del import tabular, en
// nombre de la credencial dada. El archivo viaja en el campo «file», que es el único
// que este endpoint mira.
func subirPlanilla(t *testing.T, api *testAPI, credential, query, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("armando el multipart: %v", err)
	}
	if _, werr := fw.Write(content); werr != nil {
		t.Fatalf("escribiendo el archivo en el multipart: %v", werr)
	}
	if cerr := mw.Close(); cerr != nil {
		t.Fatalf("cerrando el multipart: %v", cerr)
	}

	req := httptest.NewRequest(http.MethodPost, tabularRoute+query, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+api.token(credential))
	}
	rec := httptest.NewRecorder()
	api.mux.ServeHTTP(rec, req)
	return rec
}

// postTabular sube una planilla y exige que el import haya salido bien.
func postTabular(t *testing.T, api *testAPI, query, filename string, content []byte) *catalogImportWire {
	t.Helper()
	rec := subirPlanilla(t, api, keyAContent, query, filename, content)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST tabular %s code=%d, quiero 200; body=%s", query, rec.Code, rec.Body.String())
	}
	var out catalogImportWire
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal de la respuesta: %v; body=%s", err, rec.Body.String())
	}
	return &out
}

// plantilla descarga la plantilla en el formato pedido, por la MISMA ruta pública
// que usaría la consola.
func plantilla(t *testing.T, api *testAPI, format string) []byte {
	t.Helper()
	rec := call(api, keyAContent, http.MethodGet, "/api/v1/catalog/import/template?format="+format, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("descargando la plantilla %s: code=%d", format, rec.Code)
	}
	return rec.Body.Bytes()
}

// blobVigente lee el blob vigente de una ref del tenant.
func blobVigente(t *testing.T, repo *flowstore.MemoryRepository, tenantID, ref string) string {
	t.Helper()
	blob, err := repo.GetTenantContent(context.Background(), tenantID, ref)
	if err != nil {
		t.Fatalf("leyendo %s/%s: %v", tenantID, ref, err)
	}
	return string(blob)
}

// erroresDe deserializa el cuerpo de un 400 del import.
func erroresDe(t *testing.T, rec *httptest.ResponseRecorder) []catalogimport.ImportFieldError {
	t.Helper()
	var body struct {
		Error  string                           `json:"error"`
		Errors []catalogimport.ImportFieldError `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del error: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "validation_failed" {
		t.Fatalf("error=%q, quiero validation_failed (la misma forma que el import JSON)", body.Error)
	}
	return body.Errors
}

// csvDe escribe una planilla como la guardaría una hoja de cálculo, con el separador
// de columnas que se le diga: Excel en español guarda los CSV con «;».
func csvDe(t *testing.T, comma rune, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = comma
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			t.Fatalf("escribiendo el CSV de prueba: %v", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("volcando el CSV de prueba: %v", err)
	}
	return buf.Bytes()
}

// filaDe arma una fila en el orden de las columnas canónicas.
func filaDe(celdas ...string) []string {
	row := make([]string, len(catalogimport.TabularColumns()))
	copy(row, celdas)
	return row
}

// ============================ el criterio: mismo blob por las dos puertas ============================

// TestCatalogImportTabular_LasTresPlantillasProducenElMismoCatalogo es el criterio de
// T3.4 verificado de extremo a extremo y por las rutas de verdad: se DESCARGA la
// plantilla en los tres formatos —el JSON del contrato, el CSV y el XLSX de la
// planilla canónica— y se sube cada una por su puerta. Lo que queda escrito en
// tenant_content tiene que ser el MISMO blob, byte a byte.
//
// Es la afirmación entera de esta tarea: que el camino tabular no es otro import sino
// otra forma de escribir el mismo, y que la plantilla que repartimos se puede llenar y
// devolver sin perder nada por el camino (los cuatro casos: simple, con variantes,
// combo y con tags/atributos).
//
// Sin fixture: los tres archivos salen del mismo BuildTemplate. Un fixture escrito a
// mano sería una segunda verdad que envejecería sola.
func TestCatalogImportTabular_LasTresPlantillasProducenElMismoCatalogo(t *testing.T) {
	api, repo := importAPI()

	postImport(t, api, keyAContent, "?mode=apply&ref=via-json", string(plantilla(t, api, "json")))
	postTabular(t, api, "?mode=apply&ref=via-csv", "catalogo.csv", plantilla(t, api, "csv"))
	postTabular(t, api, "?mode=apply&ref=via-xlsx", "catalogo.xlsx", plantilla(t, api, "xlsx"))

	desdeJSON := blobVigente(t, repo, tenantA, "via-json")
	if got := blobVigente(t, repo, tenantA, "via-csv"); got != desdeJSON {
		t.Fatalf("el CSV escribió otro catálogo.\ncsv:  %s\njson: %s", got, desdeJSON)
	}
	if got := blobVigente(t, repo, tenantA, "via-xlsx"); got != desdeJSON {
		t.Fatalf("el XLSX escribió otro catálogo.\nxlsx: %s\njson: %s", got, desdeJSON)
	}

	// Y el catálogo es el de la plantilla de verdad, no un objeto vacío que también
	// sería «igual a sí mismo» tres veces.
	for _, sku := range []string{"TORTA-CHOC", "TORTA-UNICORNIO", "TEQUENOS-15", "REFRESCO-15L", "COMBO-FIESTA"} {
		if !strings.Contains(desdeJSON, sku) {
			t.Fatalf("el blob no trae %s: %s", sku, desdeJSON)
		}
	}
}

// TestCatalogImportTabular_LaProcedenciaQuedaAnotada: las dos puertas escriben lo
// mismo, pero la versión archivada recuerda por cuál entró. Es lo único que distingue
// un import de otro después, porque la auditoría anota el mismo recurso para los dos.
func TestCatalogImportTabular_LaProcedenciaQuedaAnotada(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	postTabular(t, api, "?mode=apply", "catalogo.csv", plantilla(t, api, "csv"))

	versions := repo.TenantContentVersions(tenantA, "catalogo")
	if len(versions) != 1 {
		t.Fatalf("versiones=%d, quiero 1 (la que archivó el catálogo viejo)", len(versions))
	}
	if versions[0].Source != flowstore.VersionSourceImportTabular {
		t.Fatalf("source=%q, quiero %q", versions[0].Source, flowstore.VersionSourceImportTabular)
	}
}

// ============================ mirar sin tocar ============================

// TestCatalogImportTabular_Validate_NoEscribeYEnseñaElDiff: la planilla entra por el
// MISMO camino que el JSON a partir de la lectura, así que hereda su garantía —mirar
// no cambia nada— y su diff. Aquí se comprueba sobre el catálogo sembrado: el café
// sube de precio, entra el agua y se va el jugo.
func TestCatalogImportTabular_Validate_NoEscribeYEnseñaElDiff(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	rows := [][]string{
		catalogimport.TabularColumns(),
		filaDe("1|Bebidas", "", "1", "CAFE", "Café", "2.9"),
		filaDe("1|Bebidas", "", "2", "TE", "Té", "2"),
		filaDe("1|Bebidas", "", "3", "AGUA", "Agua", "1.5"),
	}
	got := postTabular(t, api, "?mode=validate", "catalogo.csv", csvDe(t, ',', rows))

	if got.Applied || got.Mode != "validate" {
		t.Fatalf("mode=%q applied=%v", got.Mode, got.Applied)
	}
	if got.Items != 3 {
		t.Errorf("items=%d, quiero 3", got.Items)
	}
	wantDiffContraFixture(t, got)
	if blob := blobVigente(t, repo, tenantA, "catalogo"); blob != catalogoVigente {
		t.Fatal("validate escribió el catálogo: mirar no puede cambiar nada")
	}
}

// ============================ defectos que lee una persona ============================

// TestCatalogImportTabular_PrecioNoNumerico_400ConFilaYMotivo: el segundo criterio de
// T3.4, visto como lo ve la consola. Lo que llega al BFF es la MISMA forma que el
// import JSON (validation_failed + lista), pero ubicado por `row`: en una hoja de
// cálculo, «la categoría 1, artículo 2» no es una dirección a la que nadie pueda ir.
func TestCatalogImportTabular_PrecioNoNumerico_400ConFilaYMotivo(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	rows := [][]string{
		catalogimport.TabularColumns(),
		filaDe("1|Bebidas", "", "1", "CAFE", "Café", "2.9"),
		filaDe("1|Bebidas", "", "2", "TE", "Té", "$18.000"),
	}
	rec := subirPlanilla(t, api, keyAContent, "?mode=apply", "catalogo.csv", csvDe(t, ',', rows))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}

	errs := erroresDe(t, rec)
	if len(errs) != 1 {
		t.Fatalf("defectos=%+v, quiero 1", errs)
	}
	if errs[0].Row != 3 || errs[0].Field != "precio" {
		t.Errorf("row=%d field=%q, quiero 3/precio", errs[0].Row, errs[0].Field)
	}
	if !strings.Contains(errs[0].Reason, `el precio "$18.000" no es un número`) {
		t.Errorf("motivo=%q", errs[0].Reason)
	}
	if blob := blobVigente(t, repo, tenantA, "catalogo"); blob != catalogoVigente {
		t.Fatal("un apply con defectos escribió igualmente")
	}
}

// TestCatalogImportTabular_SinArchivo_400: quien manda el formulario sin archivo —o
// con el campo mal nombrado— tiene que enterarse de cuál es el campo, no recibir un
// «documento vacío» que le haga buscar el problema en su catálogo.
func TestCatalogImportTabular_SinArchivo_400(t *testing.T) {
	api, _ := importAPI()

	req := httptest.NewRequest(http.MethodPost, tabularRoute, strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=nada")
	req.Header.Set("Authorization", "Bearer "+api.token(keyAContent))
	rec := httptest.NewRecorder()
	api.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file") {
		t.Errorf("el mensaje no nombra el campo esperado: %s", rec.Body.String())
	}
}

// TestCatalogImportTabular_LibroSinLaHoja_400: la hoja se busca por NOMBRE, así que
// un libro que no la tiene se rechaza diciendo cómo se llama. Buscar «la primera
// hoja» sería importar las cuentas personales de quien dejó su pestaña delante.
func TestCatalogImportTabular_LibroSinLaHoja_400(t *testing.T) {
	api, _ := importAPI()

	f := excelize.NewFile()
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Logf("cerrando el libro: %v", cerr)
		}
	}()
	if err := f.SetSheetName(f.GetSheetName(0), "mis cuentas"); err != nil {
		t.Fatalf("nombrando la hoja: %v", err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("serializando el libro: %v", err)
	}

	rec := subirPlanilla(t, api, keyAContent, "", "cuentas.xlsx", buf.Bytes())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), catalogimport.TabularSheetName) {
		t.Errorf("el mensaje no dice cómo debe llamarse la hoja: %s", rec.Body.String())
	}
}

// TestCatalogImportTabular_CSVGuardadoPorExcelEnEspanol: Excel configurado en español
// guarda los CSV con «;» en vez de «,». Sin reconocerlo, la planilla que nosotros
// emitimos volvería del ordenador del dueño convertida en una sola columna y el
// import diría que no tiene ninguna de las columnas obligatorias: el error más
// desesperante posible sobre un archivo que está bien.
func TestCatalogImportTabular_CSVGuardadoPorExcelEnEspanol(t *testing.T) {
	api, _ := importAPI()

	rows := [][]string{
		catalogimport.TabularColumns(),
		filaDe("1|Tortas", "", "1", "TORTA-CHOC", "Torta de chocolate", "18000", "", "decorada; sin_lactosa", "",
			"V1|10-12 porciones|18000; V2|25-30 porciones|32000", ""),
	}
	got := postTabular(t, api, "?mode=validate", "catalogo.csv", csvDe(t, ';', rows))

	if got.Items != 1 {
		t.Fatalf("items=%d, quiero 1: el CSV con «;» no se leyó", got.Items)
	}
	if len(got.Diff.Added) != 1 || got.Diff.Added[0].SKU != "TORTA-CHOC" {
		t.Fatalf("added=%+v", got.Diff.Added)
	}
}

// ============================ techos y guardias ============================

// TestCatalogImportTabular_ArchivoDemasiadoGrande_413 comprueba los DOS techos, que
// no son el mismo: el del cuerpo entero corta la conexión antes de acumular nada (es
// el que impide que alguien empuje gigabytes) y el del archivo es el que expresa el
// requisito —un catálogo que pasa el import tiene que caber en tenant_content—.
func TestCatalogImportTabular_ArchivoDemasiadoGrande_413(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo, ContentMaxBytes: 200},
		Entitlements: feats,
	}, apiKeys())

	casos := map[string]int{
		"pasa el techo del archivo": 500,
		"pasa el techo del cuerpo":  64 << 10,
	}
	for nombre, size := range casos {
		t.Run(nombre, func(t *testing.T) {
			rec := subirPlanilla(t, api, keyAContent, "", "catalogo.csv", bytes.Repeat([]byte("a"), size))
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("code=%d, quiero 413; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestCatalogImportTabular_ModoDesconocido_400: mismo criterio que el import JSON.
// «aply» tecleado a las prisas no puede degradar en silencio, ni a «no hagas nada»
// ni —mucho menos— a «escribe».
func TestCatalogImportTabular_ModoDesconocido_400(t *testing.T) {
	api, _ := importAPI()

	rec := subirPlanilla(t, api, keyAContent, "?mode=aply", "catalogo.csv", plantilla(t, api, "csv"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImportTabular_Guardias: los tres guardias de la ruta, y ninguno sustituye
// a otro. Sin identidad no hay tenant (401); con identidad pero sin el scope de
// escritura, 403; con el scope pero sin la feature del plan, 403 del gate. El scope es
// content.write también para validate: hacer depender el permiso de un parámetro de la
// URL sería dejar que la entrada del usuario decida la autorización.
func TestCatalogImportTabular_Guardias(t *testing.T) {
	api, _ := importAPI()
	csvPlantilla := plantilla(t, api, "csv")

	if rec := subirPlanilla(t, api, "", "", "catalogo.csv", csvPlantilla); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin identidad: code=%d, quiero 401", rec.Code)
	}
	if rec := subirPlanilla(t, api, keyASessions, "", "catalogo.csv", csvPlantilla); rec.Code != http.StatusForbidden {
		t.Errorf("sin content.write: code=%d, quiero 403", rec.Code)
	}

	// Mismo tenant y mismo scope, pero sin la feature en el plan.
	repo := flowstore.NewMemoryRepository()
	sinFeature := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo},
		Entitlements: entitlements.NewFake(),
	}, apiKeys())
	rec := subirPlanilla(t, sinFeature, keyAContent, "", "catalogo.csv", csvPlantilla)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin la feature: code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "feature_not_enabled") {
		t.Errorf("cuerpo=%s, quiero el error del gate", rec.Body.String())
	}
}
