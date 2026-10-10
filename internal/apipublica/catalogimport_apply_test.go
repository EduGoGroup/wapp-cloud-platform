//go:build pendiente

package apipublica_test

// catalogimport_apply_test.go — trozo de catalogimport_test.go (05 E-13): el diff, la respuesta,
// el apply con su versionado y los tres desenlaces del catálogo vigente de I14
// (MountCatalogImport). Los dobles y auxiliares (catalogImport…) viven en catalogimport_test.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// catalogImportUnreadableWarning es el aviso literal de un vigente que no es un catálogo.
const catalogImportUnreadableWarning = "catálogo vigente: la ref tiene contenido, pero no se pudo interpretar como catálogo; " +
	"la comparación se hizo contra un catálogo vacío y por eso todo aparece como nuevo"

// TestMountCatalogImport_ValidateShowsTheDiffAndWritesNothing: validate responde el objeto del
// contrato —cinco claves, en su orden, sin archived_version ni document— y no cambia nada.
func TestMountCatalogImport_ValidateShowsTheDiffAndWritesNothing(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))

	rec := rig.post("?mode=validate", catalogImportDoc)
	wantCode(t, "validate", rec, http.StatusOK)
	wantExactBody(t, "validate", rec, `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":`+catalogImportDiff+`}`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("validate: Content-Type %q, quiero application/json", got)
	}
	catalogImportWantStoreCalls(t, "validate", rig.store, 1, 0)
	if got := rig.blob(t, tenantA, catalogImportRef); got != catalogImportCurrent {
		t.Errorf("validate escribió en tenant_content: %q", got)
	}
	if n := len(rig.store.repo.TenantContentVersions(tenantA, catalogImportRef)); n != 0 {
		t.Errorf("validate archivó %d versiones, quiero 0", n)
	}
	catalogImportWantOneAudit(t, "validate", rig.h, "success", http.StatusOK)
}

// TestMountCatalogImport_ItemsCountsEveryCategory: `items` suma los artículos de todas las
// categorías del documento subido.
func TestMountCatalogImport_ItemsCountsEveryCategory(t *testing.T) {
	doc := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2},{"code":"2","sku":"TE","label":"Té","price":2}]},` +
		`{"code":"2","label":"Dulces","items":[{"code":"1","sku":"TORTA","label":"Torta","price":9}]},` +
		`{"code":"3","label":"Salados","items":[{"code":"1","sku":"AREPA","label":"Arepa","price":4},{"code":"2","sku":"PAN","label":"Pan","price":1}]}]}}`
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rec := rig.post("", doc)
	wantCode(t, "items", rec, http.StatusOK)
	var got struct {
		Items int `json:"items"`
	}
	wantJSON(t, "items", rec, &got)
	if got.Items != 5 {
		t.Errorf("items = %d, quiero 5 (2 + 1 + 2)", got.Items)
	}
}

// TestMountCatalogImport_ApplyWritesTheNormalizedCatalogAndArchivesTheOld: apply re-valida,
// escribe el `catalog` normalizado (no los bytes que llegaron) y archiva el vigente con la
// procedencia del camino JSON.
func TestMountCatalogImport_ApplyWritesTheNormalizedCatalogAndArchivesTheOld(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))

	// El documento llega con un `source`, un campo ajeno e indentación propia: nada de eso se
	// guarda.
	sent := strings.Replace(catalogImportDoc, `{"format"`, "{\n  \"source\":{\"kind\":\"llm\",\"hint\":\"x\"}, \"ajeno\": 7,\n  \"format\"", 1)
	rec := rig.post("?mode=apply", sent)
	wantCode(t, "apply", rec, http.StatusOK)
	wantExactBody(t, "apply", rec, `{"mode":"apply","ref":"catalogo","applied":true,"items":3,"diff":`+catalogImportDiff+`,"archived_version":1}`)
	catalogImportWantStoreCalls(t, "apply", rig.store, 1, 1)
	catalogImportWantOneAudit(t, "apply", rig.h, "success", http.StatusOK)

	want := catalogImportBlobOf(t, catalogImportDoc)
	if rig.store.gotBlob != want || rig.blob(t, tenantA, catalogImportRef) != want {
		t.Errorf("apply: se escribió\n  %s\nquiero el catálogo normalizado\n  %s", rig.store.gotBlob, want)
	}
	if rig.store.gotTenant != tenantA || rig.store.gotRef != catalogImportRef || rig.store.gotSource != store.VersionSourceImportJSON {
		t.Errorf("apply: el versionador recibió tenant=%q ref=%q source=%q; quiero %q, %q y %q",
			rig.store.gotTenant, rig.store.gotRef, rig.store.gotSource, tenantA, catalogImportRef, store.VersionSourceImportJSON)
	}
	if store.VersionSourceImportJSON != "import_json" {
		t.Errorf("la procedencia del camino JSON es %q, quiero import_json", store.VersionSourceImportJSON)
	}

	versions := rig.store.repo.TenantContentVersions(tenantA, catalogImportRef)
	if len(versions) != 1 || versions[0].Version != 1 || string(versions[0].Content) != catalogImportCurrent ||
		versions[0].Source != store.VersionSourceImportJSON {
		t.Errorf("apply: versiones %+v; quiero la 1, con el catálogo anterior y la procedencia import_json", versions)
	}
}

// TestMountCatalogImport_WrittenBlobIsTheEngineCatalog: lo que queda guardado es el catálogo v2
// que consume el motor, sin el sobre del contrato ni campos ajenos.
func TestMountCatalogImport_WrittenBlobIsTheEngineCatalog(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	sent := strings.Replace(catalogImportDoc, `{"format"`, `{"source":{"kind":"llm"},"ajeno":7,"format"`, 1)
	wantCode(t, "apply", rig.post("?mode=apply", sent), http.StatusOK)

	var raw map[string]any
	if err := json.Unmarshal([]byte(rig.store.gotBlob), &raw); err != nil {
		t.Fatalf("apply: el blob escrito no es JSON: %v", err)
	}
	for _, key := range []string{"format", "version", "source", "ajeno", "catalog"} {
		if _, ok := raw[key]; ok {
			t.Errorf("apply: el blob escrito lleva %q; se guarda el catálogo v2, no el sobre", key)
		}
	}
	// Lo escrito lo lee el parser del motor sin un aviso.
	cat, err := catalogo.ParseCatalog(model.Content{Raw: raw})
	if err != nil || len(cat.Categories) != 1 {
		t.Errorf("apply: el motor no lee el blob escrito (err=%v, categorías=%d)", err, len(cat.Categories))
	}
}

// TestMountCatalogImport_FirstImportAndIdenticalReapply: sobre una ref vacía el primer import
// escribe y NO archiva (y `archived_version` no viaja); reaplicar el mismo documento no es un
// no-op: diff vacío, mismo blob y una versión más.
func TestMountCatalogImport_FirstImportAndIdenticalReapply(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)

	rec := rig.post("?mode=apply", catalogImportDoc)
	wantCode(t, "primer import", rec, http.StatusOK)
	wantExactBody(t, "primer import", rec, `{"mode":"apply","ref":"catalogo","applied":true,"items":3,"diff":`+catalogImportDiffAllNew+`}`)
	if n := len(rig.store.repo.TenantContentVersions(tenantA, catalogImportRef)); n != 0 {
		t.Errorf("primer import: creó %d versiones; sin blob vigente no se archiva nada", n)
	}
	afterFirst := rig.blob(t, tenantA, catalogImportRef)

	const unchanged = `{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":3}`
	for i, archived := range []int{1, 2} {
		what := fmt.Sprintf("reaplicación %d", i+1)
		rec = rig.post("?mode=apply", catalogImportDoc)
		wantCode(t, what, rec, http.StatusOK)
		wantExactBody(t, what, rec, fmt.Sprintf(`{"mode":"apply","ref":"catalogo","applied":true,"items":3,"diff":%s,"archived_version":%d}`, unchanged, archived))
		if got := rig.blob(t, tenantA, catalogImportRef); got != afterFirst {
			t.Errorf("%s: el contenido vigente cambió entre dos imports idénticos:\n  %s\n  %s", what, afterFirst, got)
		}
	}
	versions := rig.store.repo.TenantContentVersions(tenantA, catalogImportRef)
	if len(versions) != 2 || string(versions[1].Content) != afterFirst {
		t.Errorf("reaplicación: %d versiones; quiero 2, y la segunda con lo que había tras el primer import", len(versions))
	}
	catalogImportWantStoreCalls(t, "reaplicación", rig.store, 3, 3)
}

// TestMountCatalogImport_ApplyIsStateless: no hay ticket: un apply sin validate previo se
// aplica, y un validate previo no deja nada que un apply posterior de OTRO documento confirme.
func TestMountCatalogImport_ApplyIsStateless(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	wantCode(t, "validate previo", rig.post("?mode=validate", catalogImportDoc), http.StatusOK)

	other := strings.Replace(catalogImportDoc, `"price":1.5`, `"price":7`, 1)
	wantCode(t, "apply de otro documento", rig.post("?mode=apply", other), http.StatusOK)
	if got, want := rig.blob(t, tenantA, catalogImportRef), catalogImportBlobOf(t, other); got != want {
		t.Errorf("apply: quedó\n  %s\nquiero el documento que llegó en ESTA petición\n  %s", got, want)
	}
}

// TestMountCatalogImport_UnreadableCurrentCatalogWarns: la ref tiene contenido que no es un
// catálogo: el diff dice «todo nuevo» y el aviso literal, al final de current_warnings, es lo
// que impide leerlo como «no pierdo nada». Nunca un 500.
func TestMountCatalogImport_UnreadableCurrentCatalogWarns(t *testing.T) {
	cases := map[string]string{
		"other_object": `{"prompt":"esto no es un catálogo"}`,
		"json_array":   `[1,2,3]`,
		"json_string":  `"texto suelto"`,
		"json_null":    `null`,
		"not_json":     `{roto`,
	}
	wantDiff := strings.TrimSuffix(catalogImportDiffAllNew, "}") + `,"current_warnings":["` + catalogImportUnreadableWarning + `"]}`
	for name, current := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogImport)
			rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(current))
			rec := rig.post("?mode=validate", catalogImportDoc)
			wantCode(t, name, rec, http.StatusOK)
			wantExactBody(t, name, rec, `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":`+wantDiff+`}`)
		})
	}

	// Una ref SIN contenido no avisa: es el primer import, no un catálogo roto. También cuando
	// el centinela llega envuelto.
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.getErr = fmt.Errorf("leyendo: %w", store.ErrTenantContentNotFound)
	rec := rig.post("", catalogImportDoc)
	wantCode(t, "ref vacía", rec, http.StatusOK)
	wantExactBody(t, "ref vacía", rec, `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":`+catalogImportDiffAllNew+`}`)
}

// TestMountCatalogImport_TolerantParseWarningsAreNotTheUnreadableOne: los avisos del parseo
// tolerante del vigente son los de DiffCatalog; el de «ilegible» no aparece si el catálogo se leyó.
func TestMountCatalogImport_TolerantParseWarningsAreNotTheUnreadableOne(t *testing.T) {
	// El vigente trae un artículo con sku reservado: el motor ya lo ignoraba.
	current := strings.Replace(catalogImportCurrent, `"sku":"JUGO"`, `"sku":"_jugo"`, 1)
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(current))
	rec := rig.post("", catalogImportDoc)
	wantCode(t, "avisos del vigente", rec, http.StatusOK)
	var got struct {
		Diff struct {
			Removed         []json.RawMessage `json:"removed"`
			CurrentWarnings []string          `json:"current_warnings"`
		} `json:"diff"`
	}
	wantJSON(t, "avisos del vigente", rec, &got)
	if len(got.Diff.CurrentWarnings) != 1 || !strings.HasPrefix(got.Diff.CurrentWarnings[0], "catálogo vigente") ||
		got.Diff.CurrentWarnings[0] == catalogImportUnreadableWarning {
		t.Errorf("avisos del vigente: %q; quiero solo el aviso del artículo que el motor ignoraba", got.Diff.CurrentWarnings)
	}
	if len(got.Diff.Removed) != 0 {
		t.Errorf("avisos del vigente: %d bajas; el artículo ignorado no entra en la comparación", len(got.Diff.Removed))
	}
}

// TestMountCatalogImport_StoreFailures: el almacén caído NO se degrada a «catálogo vacío» (un
// diff mentiroso empujaría a confirmar a ciegas) y ningún 500 repite el error del almacén.
func TestMountCatalogImport_StoreFailures(t *testing.T) {
	secret := errors.New("dial tcp: postgres://usuario:clave@host/db")

	for _, query := range []string{"", "?mode=apply"} {
		rig := catalogImportSetup(t, apipublica.MountCatalogImport)
		rig.store.getErr = secret
		rec := rig.post(query, catalogImportDoc)
		wantCode(t, "lectura caída"+query, rec, http.StatusInternalServerError)
		wantErrorBody(t, "lectura caída"+query, rec, catalogImportMsgRead)
		catalogImportWantStoreCalls(t, "lectura caída"+query, rig.store, 1, 0)
		catalogImportWantOneAudit(t, "lectura caída"+query, rig.h, "failure", http.StatusInternalServerError)
	}

	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))
	rig.store.replaceErr = secret
	rec := rig.post("?mode=apply", catalogImportDoc)
	wantCode(t, "escritura caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "escritura caída", rec, catalogImportMsgApply)
	catalogImportWantStoreCalls(t, "escritura caída", rig.store, 1, 1)
	if strings.Contains(rec.Body.String(), "postgres") {
		t.Errorf("escritura caída: el 500 repite el error del almacén: %s", rec.Body.String())
	}
	if got := rig.blob(t, tenantA, catalogImportRef); got != catalogImportCurrent {
		t.Errorf("escritura caída: el catálogo vigente cambió: %q", got)
	}

	// En validate el versionador ni se toca: su fallo no existe.
	rig = catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.replaceErr = secret
	wantCode(t, "validate con el versionador caído", rig.post("?mode=validate", catalogImportDoc), http.StatusOK)
}
