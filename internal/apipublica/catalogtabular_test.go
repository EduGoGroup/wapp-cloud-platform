//go:build pendiente

package apipublica_test

// catalogtabular_test.go — cubre el contrato de catalogtabular.go (MountCatalogTabular): el
// montaje, los guardias, el modo, la respuesta con su `document`, el apply y la confirmación en
// dos pasos de I15. Aquí viven los auxiliares del área (catalogTabular…); la lectura del archivo
// —formatos, tolerancias, fallos y techos— está en catalogtabular_files_test.go. Los dobles del
// import (catalogImport…) vienen de catalogimport_test.go.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

const (
	catalogTabularPattern = "POST /api/v1/catalog/import/tabular"
	catalogTabularTarget  = "/api/v1/catalog/import/tabular"
	catalogTabularField   = "file"

	catalogTabularMsgNoFile = "no llegó ningún archivo: súbelo en el campo «file» de un formulario multipart/form-data."
)

// catalogTabularMount monta las dos puertas del import (I14 e I15): la confirmación en dos pasos
// las necesita a las dos.
func catalogTabularMount(c *apipublica.Cara, k apipublica.Common, d apipublica.CatalogImportDeps) {
	apipublica.MountCatalogImport(c, k, d)
	apipublica.MountCatalogTabular(c, k, d)
}

// catalogTabularRow arma una fila en el orden de las columnas canónicas; las que no se dan
// quedan vacías.
func catalogTabularRow(cells ...string) []string {
	row := make([]string, len(catalogimport.TabularColumns()))
	copy(row, cells)
	return row
}

// catalogTabularRows es la planilla equivalente a catalogImportDoc: mismo catálogo, en filas.
func catalogTabularRows() [][]string {
	return [][]string{
		catalogimport.TabularColumns(),
		catalogTabularRow("1|Bebidas", "", "1", "CAFE", "Café", "2.9"),
		catalogTabularRow("1|Bebidas", "", "2", "TE", "Té", "2"),
		catalogTabularRow("1|Bebidas", "", "3", "AGUA", "Agua", "1.5"),
	}
}

// catalogTabularCSV escribe una planilla como la guardaría una hoja de cálculo, con el separador
// que se le diga.
func catalogTabularCSV(t *testing.T, comma rune, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = comma
	if err := w.WriteAll(rows); err != nil {
		t.Fatalf("escribiendo el CSV de prueba: %v", err)
	}
	return buf.Bytes()
}

// catalogTabularDocument es lo que el contrato tabular lee de rows: el documento normalizado
// (como JSON, tal como viaja en `document`) y el blob que su apply debe escribir.
func catalogTabularDocument(t *testing.T, rows [][]string) (document, blob string) {
	t.Helper()
	doc, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("la planilla de prueba no valida: %v", verr)
	}
	docJSON, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("serializando el documento de prueba: %v", err)
	}
	blobJSON, err := json.Marshal(doc.Catalog)
	if err != nil {
		t.Fatalf("serializando el catálogo de prueba: %v", err)
	}
	return string(docJSON), string(blobJSON)
}

// catalogTabularUpload sube content en un formulario multipart, en el campo field, con la
// credencial dada ("" = sin token).
func catalogTabularUpload(t *testing.T, rig catalogImportRig, credential, query, field, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("armando el multipart: %v", err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatalf("escribiendo el archivo en el multipart: %v", err)
	}
	if err = form.Close(); err != nil {
		t.Fatalf("cerrando el multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, catalogTabularTarget+query, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	rig.cara.ServeHTTP(rec, req)
	return rec
}

// catalogTabularPost sube un archivo a I15 como tenantA con el permiso de escritura.
func catalogTabularPost(t *testing.T, rig catalogImportRig, query, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return catalogTabularUpload(t, rig, rig.h.With(tenantA, catalogImportPermWrite), query, catalogTabularField, filename, content)
}

// catalogTabularReadFailure es el cuerpo de un fallo de LECTURA del archivo: la forma del import
// JSON con una sola entrada, sin fila ni índices.
func catalogTabularReadFailure(reason string) string {
	return `{"error":"validation_failed","errors":[{"field":"archivo","reason":"` + reason + `"}]}`
}

// TestMountCatalogTabular_Chain: la cadena W de I15, afirmada a mano porque su camino feliz es
// un multipart (checkChain manda JSON).
func TestMountCatalogTabular_Chain(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	wantPatterns(t, "I15", rig.cara, []string{catalogTabularPattern})
	file := catalogTabularCSV(t, ',', catalogTabularRows())

	rec := catalogTabularUpload(t, rig, "", "", catalogTabularField, "catalogo.csv", file)
	wantCode(t, "I15 sin token", rec, http.StatusUnauthorized)
	wantErrorBody(t, "I15 sin token", rec, "autenticación requerida")

	for what, credential := range map[string]string{
		"I15 sin el permiso":        rig.h.With(tenantA, "otra.cosa"),
		"I15 solo con lectura":      rig.h.With(tenantA, catalogImportPermRead),
		"I15 con token sin empresa": rig.h.Tenantless("sin-empresa"),
	} {
		rec = catalogTabularUpload(t, rig, credential, "?mode=validate", catalogTabularField, "catalogo.csv", file)
		wantCode(t, what, rec, http.StatusForbidden)
		wantErrorBody(t, what, rec, "permiso denegado")
	}
	if n := len(rig.h.Auditor().Records()); n != 0 {
		t.Errorf("I15: 401/403 dejaron %d registros de auditoría, quiero 0", n)
	}
	catalogImportWantStoreCalls(t, "I15 sin pasar la cadena", rig.store, 0, 0)

	rec = catalogTabularPost(t, rig, "", "catalogo.csv", file)
	wantCode(t, "I15 con el permiso", rec, http.StatusOK)
	// El MISMO recurso de auditoría que I14, y también en validate.
	catalogImportWantOneAudit(t, "I15 con el permiso", rig.h, "success", http.StatusOK)
	if got := rig.h.Auditor().Records()[0].Actor; got != subject {
		t.Errorf("I15: actor %q, quiero %q", got, subject)
	}
}

// TestMountCatalogTabular_MissingDependencyIs404: I15 monta con la MISMA condición que I14.
func TestMountCatalogTabular_MissingDependencyIs404(t *testing.T) {
	cases := map[string]func(*apipublica.CatalogImportDeps){
		"no_content_reader": func(d *apipublica.CatalogImportDeps) { d.Content = nil },
		"no_version_writer": func(d *apipublica.CatalogImportDeps) { d.ContentVersions = nil },
		"no_resolver":       func(d *apipublica.CatalogImportDeps) { d.Entitlements = nil },
	}
	for name, strip := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogTabular, strip)
			wantPatterns(t, name, rig.cara, nil)
			rec := catalogTabularPost(t, rig, "", "catalogo.csv", catalogTabularCSV(t, ',', catalogTabularRows()))
			wantCode(t, name, rec, http.StatusNotFound)

			d := catalogImportDeps(rig.store)
			strip(&d)
			if v := recuperar(func() { apipublica.MountCatalogTabular(apipublica.Nueva(), apipublica.Common{}, d) }); v != nil {
				t.Errorf("%s con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", name, v)
			}
		})
	}
}

func TestMountCatalogTabular_NilMWPanicsAtMount(t *testing.T) {
	d := catalogImportDeps(&catalogImportStoreSpy{repo: store.NewMemoryRepository()})
	v := recuperar(func() { apipublica.MountCatalogTabular(apipublica.Nueva(), apipublica.Common{}, d) })
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountCatalogTabular") {
		t.Errorf("MountCatalogTabular con MW nil: panic = %v; quiero un panic de cableado que nombre MountCatalogTabular", v)
	}
}

// TestMountCatalogTabular_FeatureGate: sin la feature (o con el resolver caído) corta el gate,
// con su cuerpo exacto, sin leer el archivo ni tocar el almacén, y queda auditado como fallo.
func TestMountCatalogTabular_FeatureGate(t *testing.T) {
	cases := map[string]func(*apipublica.CatalogImportDeps){
		"without_the_feature": func(d *apipublica.CatalogImportDeps) { d.Entitlements = withFeatures() },
		"resolver_fails":      func(d *apipublica.CatalogImportDeps) { d.Entitlements = catalogImportFailingResolver{} },
	}
	for name, adjust := range cases {
		for _, query := range []string{"", "?mode=apply"} {
			rig := catalogImportSetup(t, apipublica.MountCatalogTabular, adjust)
			rec := catalogTabularPost(t, rig, query, "catalogo.csv", catalogTabularCSV(t, ',', catalogTabularRows()))
			wantCode(t, name+query, rec, http.StatusForbidden)
			wantExactBody(t, name+query, rec, catalogImportDenied)
			catalogImportWantStoreCalls(t, name+query, rig.store, 0, 0)
			catalogImportWantOneAudit(t, name+query, rig.h, "failure", http.StatusForbidden)
		}
	}
}

// TestMountCatalogTabular_Mode: las reglas del modo son las de I14, y el desconocido se rechaza
// con la forma SIMPLE antes de leer el archivo (que aquí ni existe).
func TestMountCatalogTabular_Mode(t *testing.T) {
	file := catalogTabularCSV(t, ',', catalogTabularRows())
	for query, applied := range map[string]bool{"": false, "?mode=": false, "?mode=validate": false, "?mode=apply": true} {
		rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
		rec := catalogTabularPost(t, rig, query, "catalogo.csv", file)
		wantCode(t, "modo "+query, rec, http.StatusOK)
		var got struct {
			Mode    string `json:"mode"`
			Applied bool   `json:"applied"`
		}
		wantJSON(t, "modo "+query, rec, &got)
		wantMode, wantWrites := "validate", 0
		if applied {
			wantMode, wantWrites = "apply", 1
		}
		if got.Mode != wantMode || got.Applied != applied || rig.store.replaces != wantWrites {
			t.Errorf("modo %q: mode=%q applied=%v escrituras=%d; quiero %q, %v y %d",
				query, got.Mode, got.Applied, rig.store.replaces, wantMode, applied, wantWrites)
		}
	}

	for _, mode := range []string{"aply", "APPLY", "Validate", "apply%20"} {
		rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
		rec := rig.h.Call(rig.cara, rig.h.With(tenantA, catalogImportPermWrite), http.MethodPost, catalogTabularTarget+"?mode="+mode, "")
		wantCode(t, "modo "+mode, rec, http.StatusBadRequest)
		wantErrorBody(t, "modo "+mode, rec, catalogImportMsgMode)
		catalogImportWantStoreCalls(t, "modo "+mode, rig.store, 0, 0)
		catalogImportWantOneAudit(t, "modo "+mode, rig.h, "failure", http.StatusBadRequest)
	}
}

// TestMountCatalogTabular_ValidateEchoesTheDocument: validate responde el objeto de I14 —mismo
// diff— más `document` al final, y no cambia nada.
func TestMountCatalogTabular_ValidateEchoesTheDocument(t *testing.T) {
	rows := catalogTabularRows()
	document, _ := catalogTabularDocument(t, rows)
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))

	rec := catalogTabularPost(t, rig, "?mode=validate", "catalogo.csv", catalogTabularCSV(t, ',', rows))
	wantCode(t, "validate", rec, http.StatusOK)
	wantExactBody(t, "validate", rec, `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":`+catalogImportDiff+`,"document":`+document+`}`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("validate: Content-Type %q, quiero application/json", got)
	}
	catalogImportWantStoreCalls(t, "validate", rig.store, 1, 0)
	if got := rig.blob(t, tenantA, catalogImportRef); got != catalogImportCurrent {
		t.Errorf("validate escribió en tenant_content: %q", got)
	}
}

// TestMountCatalogTabular_ApplyWritesWithItsOwnSource: apply escribe el catálogo normalizado,
// archiva el vigente con la procedencia del camino tabular y TAMBIÉN devuelve el documento.
func TestMountCatalogTabular_ApplyWritesWithItsOwnSource(t *testing.T) {
	rows := catalogTabularRows()
	document, blob := catalogTabularDocument(t, rows)
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))
	rig.store.repo.SetTenantContent(tenantB, catalogImportRef, []byte(catalogImportCurrent))

	rec := catalogTabularPost(t, rig, "?mode=apply&tenant_id="+tenantB, "catalogo.csv", catalogTabularCSV(t, ',', rows))
	wantCode(t, "apply", rec, http.StatusOK)
	wantExactBody(t, "apply", rec, `{"mode":"apply","ref":"catalogo","applied":true,"items":3,"diff":`+catalogImportDiff+`,"archived_version":1,"document":`+document+`}`)
	catalogImportWantStoreCalls(t, "apply", rig.store, 1, 1)
	catalogImportWantOneAudit(t, "apply", rig.h, "success", http.StatusOK)

	if rig.store.gotBlob != blob || rig.blob(t, tenantA, catalogImportRef) != blob {
		t.Errorf("apply: se escribió\n  %s\nquiero el catálogo normalizado\n  %s", rig.store.gotBlob, blob)
	}
	if rig.store.gotTenant != tenantA || rig.store.gotSource != store.VersionSourceImportTabular || store.VersionSourceImportTabular != "import_tabular" {
		t.Errorf("apply: el versionador recibió tenant=%q source=%q; quiero %q e import_tabular", rig.store.gotTenant, rig.store.gotSource, tenantA)
	}
	versions := rig.store.repo.TenantContentVersions(tenantA, catalogImportRef)
	if len(versions) != 1 || string(versions[0].Content) != catalogImportCurrent || versions[0].Source != store.VersionSourceImportTabular {
		t.Errorf("apply: versiones %+v; quiero una, con el catálogo anterior y la procedencia import_tabular", versions)
	}
	// El tenant es el del token: el de la query no se toca.
	if got := rig.blob(t, tenantB, catalogImportRef); got != catalogImportCurrent {
		t.Errorf("apply: el catálogo del tenant B cambió: %q", got)
	}

	// Otra ref y primer import: no hay nada que archivar y `archived_version` no viaja.
	rec = catalogTabularPost(t, rig, "?mode=apply&ref=carta-verano", "catalogo.csv", catalogTabularCSV(t, ',', rows))
	wantCode(t, "otra ref", rec, http.StatusOK)
	wantExactBody(t, "otra ref", rec, `{"mode":"apply","ref":"carta-verano","applied":true,"items":3,"diff":`+catalogImportDiffAllNew+`,"document":`+document+`}`)
	if got := rig.blob(t, tenantA, "carta-verano"); got != blob {
		t.Errorf("otra ref: quedó %q, quiero %q", got, blob)
	}
}

// TestMountCatalogTabular_TwoStepConfirmation: el `document` del validate, enviado TAL CUAL al
// import JSON, escribe el mismo blob que aplicar la planilla; y ese blob es el del JSON
// equivalente (un solo camino, dos puertas).
func TestMountCatalogTabular_TwoStepConfirmation(t *testing.T) {
	rig := catalogImportSetup(t, catalogTabularMount)
	file := catalogTabularCSV(t, ',', catalogTabularRows())

	rec := catalogTabularPost(t, rig, "?mode=validate&ref=dos-pasos", "catalogo.csv", file)
	wantCode(t, "paso 1", rec, http.StatusOK)
	var shown struct {
		Document json.RawMessage `json:"document"`
	}
	wantJSON(t, "paso 1", rec, &shown)
	if len(shown.Document) == 0 {
		t.Fatalf("paso 1: la respuesta no trae `document`: %s", rec.Body.String())
	}
	wantCode(t, "paso 2", rig.post("?mode=apply&ref=dos-pasos", string(shown.Document)), http.StatusOK)
	wantCode(t, "de una vez", catalogTabularPost(t, rig, "?mode=apply&ref=de-una-vez", "catalogo.csv", file), http.StatusOK)
	wantCode(t, "por JSON", rig.post("?mode=apply&ref=por-json", catalogImportDoc), http.StatusOK)

	want := rig.blob(t, tenantA, "por-json")
	if want == "" || !strings.Contains(want, "AGUA") {
		t.Fatalf("por JSON: el blob escrito no es el catálogo de prueba: %q", want)
	}
	for _, ref := range []string{"dos-pasos", "de-una-vez"} {
		if got := rig.blob(t, tenantA, ref); got != want {
			t.Errorf("%s: se escribió\n  %s\nquiero el mismo blob que por JSON\n  %s", ref, got, want)
		}
	}
}

// TestMountCatalogTabular_StoreFailures: los mismos 500 de I14, con la forma simple.
func TestMountCatalogTabular_StoreFailures(t *testing.T) {
	file := catalogTabularCSV(t, ',', catalogTabularRows())

	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rig.store.getErr = errors.New("almacén caído")
	rec := catalogTabularPost(t, rig, "?mode=apply", "catalogo.csv", file)
	wantCode(t, "lectura caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "lectura caída", rec, catalogImportMsgRead)
	catalogImportWantStoreCalls(t, "lectura caída", rig.store, 1, 0)

	rig = catalogImportSetup(t, apipublica.MountCatalogTabular)
	rig.store.replaceErr = errors.New("almacén caído")
	rec = catalogTabularPost(t, rig, "?mode=apply", "catalogo.csv", file)
	wantCode(t, "escritura caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "escritura caída", rec, catalogImportMsgApply)
	catalogImportWantOneAudit(t, "escritura caída", rig.h, "failure", http.StatusInternalServerError)

	// Un vigente que no es un catálogo avisa igual que por la puerta JSON.
	rig = catalogImportSetup(t, apipublica.MountCatalogTabular)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(`{"prompt":"esto no es un catálogo"}`))
	rec = catalogTabularPost(t, rig, "", "catalogo.csv", file)
	wantCode(t, "vigente ilegible", rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"current_warnings":["`+catalogImportUnreadableWarning+`"]`) {
		t.Errorf("vigente ilegible: la respuesta no trae el aviso literal: %s", rec.Body.String())
	}
}
