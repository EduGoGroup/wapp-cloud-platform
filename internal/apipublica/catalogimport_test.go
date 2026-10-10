package apipublica_test

// catalogimport_test.go — cubre el contrato de catalogimport.go (CatalogImportContentReader,
// CatalogImportVersionWriter, CatalogImportDeps, MountCatalogImport): el montaje, los tres
// guardias, el modo, la ref, el tenant, los techos y el documento inválido de I14. Aquí viven
// además los dobles y auxiliares (catalogImport…) que comparten los tests de la planilla y de
// la plantilla; el diff, el apply y el versionado están en catalogimport_apply_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

const (
	catalogImportPattern   = "POST /api/v1/catalog/import"
	catalogImportTarget    = "/api/v1/catalog/import"
	catalogImportPermWrite = "content.write"
	catalogImportPermRead  = "content.read"
	catalogImportResource  = "catalog_import"
	catalogImportRef       = "catalogo"

	catalogImportDenied   = `{"error":"feature_not_enabled","feature":"catalog_import"}`
	catalogImportMsgMode  = "mode debe ser validate o apply"
	catalogImportMsgRead  = "no se pudo leer el catálogo vigente"
	catalogImportMsgApply = "no se pudo aplicar el catálogo"

	// catalogImportCurrent es el blob v2 vigente (lo que de verdad hay en tenant_content) y
	// catalogImportDoc el documento que se sube, en el sobre del contrato: el café sube, entra
	// el agua, se va el jugo y el té queda igual.
	catalogImportCurrent = `{"categories":[{"code":"1","label":"Bebidas","items":[` +
		`{"code":"1","sku":"CAFE","label":"Café","price":2.5},` +
		`{"code":"2","sku":"TE","label":"Té","price":2},` +
		`{"code":"3","sku":"JUGO","label":"Jugo","price":3}]}]}`
	catalogImportDoc = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[` +
		`{"code":"1","sku":"CAFE","label":"Café","price":2.9},` +
		`{"code":"2","sku":"TE","label":"Té","price":2},` +
		`{"code":"3","sku":"AGUA","label":"Agua","price":1.5}]}]}}`

	// Los dos diffs de catalogImportDoc: contra catalogImportCurrent y contra una ref vacía.
	catalogImportDiff = `{"price_changes":[{"sku":"CAFE","label":"Café","old_price":2.5,"new_price":2.9}],` +
		`"added":[{"sku":"AGUA","label":"Agua"}],"removed":[{"sku":"JUGO","label":"Jugo"}],` +
		`"changed_details":[],"unchanged":1}`
	catalogImportDiffAllNew = `{"price_changes":[],"added":[{"sku":"AGUA","label":"Agua"},` +
		`{"sku":"CAFE","label":"Café"},{"sku":"TE","label":"Té"}],"removed":[],"changed_details":[],"unchanged":0}`
)

// Los dos puertos del import los cumplen los repositorios REALES del módulo conversación nuevo.
var (
	_ apipublica.CatalogImportContentReader = (*store.PostgresRepository)(nil)
	_ apipublica.CatalogImportContentReader = (*store.MemoryRepository)(nil)
	_ apipublica.CatalogImportVersionWriter = (*store.PostgresRepository)(nil)
	_ apipublica.CatalogImportVersionWriter = (*store.MemoryRepository)(nil)
)

// catalogImportStoreSpy es los dos puertos del import sobre el repositorio en memoria del
// módulo: delega en él (o falla con lo sembrado) y apunta cada llamada.
type catalogImportStoreSpy struct {
	repo       *store.MemoryRepository
	getErr     error
	replaceErr error

	gets, replaces               int
	gotTenant, gotRef, gotSource string
	gotBlob                      string
}

var (
	_ apipublica.CatalogImportContentReader = (*catalogImportStoreSpy)(nil)
	_ apipublica.CatalogImportVersionWriter = (*catalogImportStoreSpy)(nil)
)

func (s *catalogImportStoreSpy) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.repo.GetTenantContent(ctx, tenantID, ref)
}

func (s *catalogImportStoreSpy) ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (int, error) {
	s.replaces++
	s.gotTenant, s.gotRef, s.gotSource, s.gotBlob = tenantID, ref, source, string(blob)
	if s.replaceErr != nil {
		return 0, s.replaceErr
	}
	return s.repo.ReplaceTenantContentVersioned(ctx, tenantID, ref, blob, source)
}

// catalogImportFailingResolver es un resolver de derechos que no puede averiguar nada.
type catalogImportFailingResolver struct{}

var _ entitlements.Resolver = catalogImportFailingResolver{}

func (catalogImportFailingResolver) Has(context.Context, string, string) (bool, error) {
	return false, errors.New("resolver caído")
}

func (catalogImportFailingResolver) ListEffective(context.Context, string) (string, []string, error) {
	return "", nil, errors.New("resolver caído")
}

func (catalogImportFailingResolver) CacheTTL() time.Duration { return 0 }

// catalogImportRig es el banco de las cuatro rutas del import: el arnés, el doble de los dos
// puertos y la cara con lo que el test haya montado.
type catalogImportRig struct {
	h     *apipublicahelpertest.Harness
	store *catalogImportStoreSpy
	cara  *apipublica.Cara
}

// catalogImportDeps son las dependencias del import con la feature encendida para los dos
// tenants y los topes por defecto.
func catalogImportDeps(s *catalogImportStoreSpy) apipublica.CatalogImportDeps {
	return apipublica.CatalogImportDeps{
		Content:         s,
		ContentVersions: s,
		Entitlements:    withFeatures(entitlements.FeatureCatalogImport),
	}
}

// catalogImportSetup arma el banco y deja que mount registre las rutas; adjust (opcional)
// retoca las dependencias antes.
func catalogImportSetup(t *testing.T, mount func(*apipublica.Cara, apipublica.Common, apipublica.CatalogImportDeps),
	adjust ...func(*apipublica.CatalogImportDeps)) catalogImportRig {
	t.Helper()
	rig := catalogImportRig{
		h:     apipublicahelpertest.New(t),
		store: &catalogImportStoreSpy{repo: store.NewMemoryRepository()},
		cara:  apipublica.Nueva(),
	}
	d := catalogImportDeps(rig.store)
	for _, f := range adjust {
		f(&d)
	}
	mount(rig.cara, rig.h.Common(), d)
	return rig
}

// post sube body a I14 como tenantA con el permiso de escritura.
func (rig catalogImportRig) post(query, body string) *httptest.ResponseRecorder {
	return rig.h.Call(rig.cara, rig.h.With(tenantA, catalogImportPermWrite), http.MethodPost, catalogImportTarget+query, body)
}

// blob lee el blob vigente de (tenant, ref) del repositorio; "" si la ref no tiene contenido.
func (rig catalogImportRig) blob(t *testing.T, tenantID, ref string) string {
	t.Helper()
	blob, err := rig.store.repo.GetTenantContent(context.Background(), tenantID, ref)
	if errors.Is(err, store.ErrTenantContentNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("leyendo el contenido de %s/%s: %v", tenantID, ref, err)
	}
	return string(blob)
}

// catalogImportBlobOf es el blob que el import debe escribir para raw: el `catalog` del
// documento ya normalizado por el validador, serializado tal cual.
func catalogImportBlobOf(t *testing.T, raw string) string {
	t.Helper()
	doc, verr := catalogimport.Validate([]byte(raw), catalogimport.DefaultLimits())
	if verr != nil {
		t.Fatalf("el documento de prueba no valida: %v", verr)
	}
	blob, err := json.Marshal(doc.Catalog)
	if err != nil {
		t.Fatalf("serializando el catálogo de prueba: %v", err)
	}
	return string(blob)
}

// catalogImportWantStoreCalls exige cuántas lecturas y cuántas escrituras recibió el almacén.
func catalogImportWantStoreCalls(t *testing.T, what string, s *catalogImportStoreSpy, gets, replaces int) {
	t.Helper()
	if s.gets != gets || s.replaces != replaces {
		t.Errorf("%s: el almacén recibió %d lecturas y %d escrituras, quiero %d y %d", what, s.gets, s.replaces, gets, replaces)
	}
}

// catalogImportWantOneAudit exige EXACTAMENTE un registro de auditoría del import con ese
// resultado y ese código.
func catalogImportWantOneAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: quedaron %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != catalogImportPermWrite || r.Resource != catalogImportResource ||
		r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, catalogImportPermWrite, catalogImportResource, result, status)
	}
}

// catalogImportDefects exige el cuerpo {"error":"validation_failed","errors":[…]} y devuelve la
// lista.
func catalogImportDefects(t *testing.T, what string, rec *httptest.ResponseRecorder) []catalogimport.ImportFieldError {
	t.Helper()
	var body struct {
		Error    string                           `json:"error"`
		Errors   []catalogimport.ImportFieldError `json:"errors"`
		Document json.RawMessage                  `json:"document"`
	}
	wantJSON(t, what, rec, &body)
	if body.Error != "validation_failed" || len(body.Errors) == 0 {
		t.Fatalf("%s: cuerpo %s; quiero validation_failed con su lista de defectos", what, rec.Body.String())
	}
	if body.Document != nil {
		t.Errorf("%s: un documento con defectos viaja en la respuesta; uno a medias es peor que ninguno", what)
	}
	return body.Errors
}

func TestMountCatalogImport_Chain(t *testing.T) {
	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	wantPatterns(t, "I14", rig.cara, []string{catalogImportPattern})
	checkChain(t, rig.h, rig.cara, routeCase{id: "I14", method: http.MethodPost, target: catalogImportTarget,
		body: catalogImportDoc, perm: catalogImportPermWrite, resource: catalogImportResource, want: http.StatusOK})
	// De las cuatro peticiones de checkChain solo la que pasa la cadena lee el vigente, y en
	// validate (el modo por defecto) ninguna escribe.
	catalogImportWantStoreCalls(t, "I14 cadena", rig.store, 1, 0)
}

// TestMountCatalogImport_MissingDependencyIs404: sin el lector, sin el versionador o sin el
// resolver la ruta no existe (T-11), y el montaje ni mira la cadena.
func TestMountCatalogImport_MissingDependencyIs404(t *testing.T) {
	cases := map[string]func(*apipublica.CatalogImportDeps){
		"no_content_reader": func(d *apipublica.CatalogImportDeps) { d.Content = nil },
		"no_version_writer": func(d *apipublica.CatalogImportDeps) { d.ContentVersions = nil },
		"no_resolver":       func(d *apipublica.CatalogImportDeps) { d.Entitlements = nil },
	}
	for name, strip := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogImport, strip)
			wantPatterns(t, name, rig.cara, nil)
			wantCode(t, name, rig.post("", catalogImportDoc), http.StatusNotFound)

			d := catalogImportDeps(rig.store)
			strip(&d)
			if v := recuperar(func() { apipublica.MountCatalogImport(apipublica.Nueva(), apipublica.Common{}, d) }); v != nil {
				t.Errorf("%s con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", name, v)
			}
		})
	}
}

func TestMountCatalogImport_NilMWPanicsAtMount(t *testing.T) {
	d := catalogImportDeps(&catalogImportStoreSpy{repo: store.NewMemoryRepository()})
	v := recuperar(func() { apipublica.MountCatalogImport(apipublica.Nueva(), apipublica.Common{}, d) })
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountCatalogImport") {
		t.Errorf("MountCatalogImport con MW nil: panic = %v; quiero un panic de cableado que nombre MountCatalogImport", v)
	}
}

// TestMountCatalogImport_DepsAreTheSharedOnes: las cuatro rutas montan con la MISMA condición
// porque comparten un solo tipo de dependencias, con estos cinco campos.
func TestMountCatalogImport_DepsAreTheSharedOnes(t *testing.T) {
	deps := reflect.TypeFor[apipublica.CatalogImportDeps]()
	want := []string{"Content", "ContentVersions", "Entitlements", "ContentMaxBytes", "ImportMaxItems"}
	if deps.NumField() != len(want) {
		t.Fatalf("CatalogImportDeps tiene %d campos, quiero %d (%v)", deps.NumField(), len(want), want)
	}
	for i, name := range want {
		if deps.Field(i).Name != name {
			t.Errorf("campo %d de CatalogImportDeps: %s, quiero %s", i, deps.Field(i).Name, name)
		}
	}
}

// TestMountCatalogImport_Guards: el permiso y la feature son dos guardias y ninguno sustituye al
// otro; el gate es fail-closed y, por ir dentro de la auditoría, su 403 queda registrado.
func TestMountCatalogImport_Guards(t *testing.T) {
	noFeature := func(d *apipublica.CatalogImportDeps) { d.Entitlements = withFeatures() }
	failing := func(d *apipublica.CatalogImportDeps) { d.Entitlements = catalogImportFailingResolver{} }

	for name, adjust := range map[string]func(*apipublica.CatalogImportDeps){"without_the_feature": noFeature, "resolver_fails": failing} {
		t.Run(name, func(t *testing.T) {
			for _, query := range []string{"", "?mode=validate", "?mode=apply", "?mode=nope"} {
				rig := catalogImportSetup(t, apipublica.MountCatalogImport, adjust)
				rec := rig.post(query, catalogImportDoc)
				wantCode(t, name+query, rec, http.StatusForbidden)
				wantExactBody(t, name+query, rec, catalogImportDenied)
				catalogImportWantStoreCalls(t, name+query, rig.store, 0, 0)
				catalogImportWantOneAudit(t, name+query, rig.h, "failure", http.StatusForbidden)
			}
		})
	}

	t.Run("read_permission_is_not_enough_even_to_validate", func(t *testing.T) {
		rig := catalogImportSetup(t, apipublica.MountCatalogImport)
		rec := rig.h.Call(rig.cara, rig.h.With(tenantA, catalogImportPermRead), http.MethodPost, catalogImportTarget+"?mode=validate", catalogImportDoc)
		wantCode(t, "solo lectura", rec, http.StatusForbidden)
		wantErrorBody(t, "solo lectura", rec, "permiso denegado")
		catalogImportWantStoreCalls(t, "solo lectura", rig.store, 0, 0)
	})

	t.Run("permission_is_asked_before_the_feature", func(t *testing.T) {
		rig := catalogImportSetup(t, apipublica.MountCatalogImport, noFeature)
		rec := rig.h.Call(rig.cara, rig.h.With(tenantA, "otra.cosa"), http.MethodPost, catalogImportTarget, catalogImportDoc)
		wantCode(t, "sin permiso ni feature", rec, http.StatusForbidden)
		wantErrorBody(t, "sin permiso ni feature", rec, "permiso denegado")
		if n := len(rig.h.Auditor().Records()); n != 0 {
			t.Errorf("sin permiso: quedaron %d registros de auditoría, quiero 0", n)
		}
	})
}

// TestMountCatalogImport_Mode: el modo por defecto es MIRAR, solo valen los dos literales y uno
// desconocido se rechaza antes de leer el cuerpo y sin tocar el almacén.
func TestMountCatalogImport_Mode(t *testing.T) {
	for query, applied := range map[string]bool{"": false, "?mode=": false, "?mode=validate": false, "?mode=apply": true} {
		rig := catalogImportSetup(t, apipublica.MountCatalogImport)
		rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))
		rec := rig.post(query, catalogImportDoc)
		wantCode(t, "modo "+query, rec, http.StatusOK)
		var got struct {
			Mode    string `json:"mode"`
			Applied bool   `json:"applied"`
		}
		wantJSON(t, "modo "+query, rec, &got)
		wantMode := "validate"
		if applied {
			wantMode = "apply"
		}
		if got.Mode != wantMode || got.Applied != applied {
			t.Errorf("modo %q: respondió mode=%q applied=%v, quiero %q y %v", query, got.Mode, got.Applied, wantMode, applied)
		}
		if written := rig.blob(t, tenantA, catalogImportRef) != catalogImportCurrent; written != applied {
			t.Errorf("modo %q: ¿se escribió? %v, quiero %v", query, written, applied)
		}
	}

	for _, mode := range []string{"aply", "APPLY", "Validate", "%20apply", "apply%20", "validate,apply", "1"} {
		for _, body := range []string{catalogImportDoc, "{no es json", ""} {
			rig := catalogImportSetup(t, apipublica.MountCatalogImport)
			rec := rig.post("?mode="+mode, body)
			wantCode(t, "modo "+mode, rec, http.StatusBadRequest)
			wantErrorBody(t, "modo "+mode, rec, catalogImportMsgMode)
			catalogImportWantStoreCalls(t, "modo "+mode, rig.store, 0, 0)
			catalogImportWantOneAudit(t, "modo "+mode, rig.h, "failure", http.StatusBadRequest)
		}
	}
}

// TestMountCatalogImport_RefAndTenant: la ref por defecto es "catalogo" y otra se usa tal cual;
// el tenant es el del token —uno en la query o en el cuerpo no cuenta— y no viaja en la
// respuesta.
func TestMountCatalogImport_RefAndTenant(t *testing.T) {
	want := catalogImportBlobOf(t, catalogImportDoc)

	for query, ref := range map[string]string{"?mode=apply": catalogImportRef, "?mode=apply&ref=": catalogImportRef,
		"?mode=apply&ref=carta-verano": "carta-verano", "?mode=apply&ref=Catalogo": "Catalogo"} {
		rig := catalogImportSetup(t, apipublica.MountCatalogImport)
		rec := rig.post(query, catalogImportDoc)
		wantCode(t, query, rec, http.StatusOK)
		wantExactBody(t, query, rec, `{"mode":"apply","ref":"`+ref+`","applied":true,"items":3,"diff":`+catalogImportDiffAllNew+`}`)
		if got := rig.blob(t, tenantA, ref); got != want {
			t.Errorf("%s: en la ref %q quedó %q, quiero %q", query, ref, got, want)
		}
		if rig.store.gotRef != ref {
			t.Errorf("%s: se escribió en la ref %q, quiero %q", query, rig.store.gotRef, ref)
		}
	}

	rig := catalogImportSetup(t, apipublica.MountCatalogImport)
	rig.store.repo.SetTenantContent(tenantB, catalogImportRef, []byte(catalogImportCurrent))
	body := strings.Replace(catalogImportDoc, `{"format"`, `{"tenant_id":"`+tenantB+`","format"`, 1)
	rec := rig.post("?mode=apply&tenant_id="+tenantB, body)
	wantCode(t, "tenant del token", rec, http.StatusOK)
	// Contra el catálogo de B el diff traería un cambio de precio y una baja: sale «todo nuevo».
	wantExactBody(t, "tenant del token", rec, `{"mode":"apply","ref":"catalogo","applied":true,"items":3,"diff":`+catalogImportDiffAllNew+`}`)
	if rig.store.gotTenant != tenantA {
		t.Errorf("tenant del token: se escribió bajo %q, quiero %q", rig.store.gotTenant, tenantA)
	}
	if got := rig.blob(t, tenantB, catalogImportRef); got != catalogImportCurrent {
		t.Errorf("tenant del token: el catálogo del tenant B cambió: %q", got)
	}
	if n := len(rig.store.repo.TenantContentVersions(tenantB, catalogImportRef)); n != 0 {
		t.Errorf("tenant del token: se archivaron %d versiones bajo el tenant B, quiero 0", n)
	}
	if strings.Contains(rec.Body.String(), tenantA) || strings.Contains(rec.Body.String(), tenantB) {
		t.Errorf("tenant del token: la respuesta lleva un tenant: %s", rec.Body.String())
	}
}

// TestMountCatalogImport_ByteCeiling: el techo se aplica leyendo, es el de tenant_content (el
// configurado, o 1 MiB) y el 413 nombra la cifra en la frase y en `max_bytes`.
func TestMountCatalogImport_ByteCeiling(t *testing.T) {
	limit := int64(len(catalogImportDoc) + 10)
	capped := func(d *apipublica.CatalogImportDeps) { d.ContentMaxBytes = limit }

	rig := catalogImportSetup(t, apipublica.MountCatalogImport, capped)
	wantCode(t, "justo en el techo", rig.post("?mode=apply", catalogImportDoc+strings.Repeat(" ", 10)), http.StatusOK)

	rig = catalogImportSetup(t, apipublica.MountCatalogImport, capped)
	rec := rig.post("?mode=apply", catalogImportDoc+strings.Repeat(" ", 11))
	wantCode(t, "un byte más", rec, http.StatusRequestEntityTooLarge)
	wantExactBody(t, "un byte más", rec, fmt.Sprintf(`{"error":"el documento excede el tamaño máximo de %d bytes","max_bytes":%d}`, limit, limit))
	catalogImportWantStoreCalls(t, "un byte más", rig.store, 0, 0)
	catalogImportWantOneAudit(t, "un byte más", rig.h, "failure", http.StatusRequestEntityTooLarge)

	for _, configured := range []int64{0, -5} {
		rig = catalogImportSetup(t, apipublica.MountCatalogImport, func(d *apipublica.CatalogImportDeps) { d.ContentMaxBytes = configured })
		rec = rig.post("", catalogImportDoc+strings.Repeat(" ", 1<<20))
		wantCode(t, "techo por defecto", rec, http.StatusRequestEntityTooLarge)
		wantExactBody(t, "techo por defecto", rec, `{"error":"el documento excede el tamaño máximo de 1048576 bytes","max_bytes":1048576}`)
	}
}

// TestMountCatalogImport_InvalidDocument: el 400 trae TODOS los defectos del validador tal cual,
// y un documento inválido no lee el vigente ni escribe, tampoco en apply.
func TestMountCatalogImport_InvalidDocument(t *testing.T) {
	cases := map[string]string{
		"empty_body":       "",
		"not_json":         "{no es json",
		"json_array":       `[1,2]`,
		"other_format":     `{"format":"otra.cosa","version":1,"catalog":{"categories":[]}}`,
		"several_defects":  `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","price":-1},{"code":"1","sku":"CAFE","label":"Otro","price":2}]}]}}`,
		"reserved_sku":     strings.Replace(catalogImportDoc, `"sku":"AGUA"`, `"sku":"_envio"`, 1),
		"price_as_text":    strings.Replace(catalogImportDoc, `"price":1.5`, `"price":"1.5"`, 1),
		"blank_only_bytes": "   ",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := catalogimport.Validate([]byte(body), catalogimport.DefaultLimits())
			if verr == nil {
				t.Fatalf("el caso %s valida; no sirve como documento inválido", name)
			}
			rig := catalogImportSetup(t, apipublica.MountCatalogImport)
			rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))
			rec := rig.post("?mode=apply", body)
			wantCode(t, name, rec, http.StatusBadRequest)
			if got := catalogImportDefects(t, name, rec); !reflect.DeepEqual(got, verr.Errors) {
				t.Errorf("%s: defectos\n  %+v\nquiero los del validador, tal cual\n  %+v", name, got, verr.Errors)
			}
			catalogImportWantStoreCalls(t, name, rig.store, 0, 0)
			if got := rig.blob(t, tenantA, catalogImportRef); got != catalogImportCurrent {
				t.Errorf("%s: un apply inválido escribió en tenant_content: %q", name, got)
			}
			catalogImportWantOneAudit(t, name, rig.h, "failure", http.StatusBadRequest)
		})
	}

	// El tope de artículos es el configurado; con cero manda el del validador (500).
	rig := catalogImportSetup(t, apipublica.MountCatalogImport, func(d *apipublica.CatalogImportDeps) { d.ImportMaxItems = 2 })
	rec := rig.post("", catalogImportDoc)
	wantCode(t, "tope de artículos", rec, http.StatusBadRequest)
	_, verr := catalogimport.Validate([]byte(catalogImportDoc), catalogimport.Limits{MaxItems: 2})
	if verr == nil {
		t.Fatal("el documento de prueba cabe en un tope de 2 artículos; no sirve para el caso")
	}
	if got := catalogImportDefects(t, "tope de artículos", rec); !reflect.DeepEqual(got, verr.Errors) {
		t.Errorf("tope de artículos: defectos %+v, quiero %+v", got, verr.Errors)
	}
	rig = catalogImportSetup(t, apipublica.MountCatalogImport, func(d *apipublica.CatalogImportDeps) { d.ImportMaxItems = 0 })
	wantCode(t, "tope por defecto", rig.post("", catalogImportDoc), http.StatusOK)
}
