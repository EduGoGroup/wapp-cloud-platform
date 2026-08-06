package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// catalogImportWire espeja el contrato del wire de POST /api/v1/catalog/import.
type catalogImportWire struct {
	Mode            string `json:"mode"`
	Ref             string `json:"ref"`
	Applied         bool   `json:"applied"`
	Items           int    `json:"items"`
	ArchivedVersion int    `json:"archived_version"`
	Diff            struct {
		PriceChanges []struct {
			SKU      string  `json:"sku"`
			Label    string  `json:"label"`
			OldPrice float64 `json:"old_price"`
			NewPrice float64 `json:"new_price"`
		} `json:"price_changes"`
		Added []struct {
			SKU   string `json:"sku"`
			Label string `json:"label"`
		} `json:"added"`
		Removed []struct {
			SKU   string `json:"sku"`
			Label string `json:"label"`
		} `json:"removed"`
		ChangedDetails  []string `json:"changed_details"`
		Unchanged       int      `json:"unchanged"`
		CurrentWarnings []string `json:"current_warnings"`
	} `json:"diff"`
}

// Documentos de import de estos tests. El vigente se siembra como blob v2 crudo
// (lo que de verdad hay en tenant_content); el que se sube va en el sobre del
// contrato (format/version/catalog).
const (
	catalogoVigente = `{"categories":[
	  {"code":"1","label":"Bebidas","items":[
	    {"code":"1","sku":"CAFE","label":"Café","price":2.5},
	    {"code":"2","sku":"TE","label":"Té","price":2},
	    {"code":"3","sku":"JUGO","label":"Jugo","price":3}
	  ]}
	]}`

	docNuevo = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[
	  {"code":"1","label":"Bebidas","items":[
	    {"code":"1","sku":"CAFE","label":"Café","price":2.9},
	    {"code":"2","sku":"TE","label":"Té","price":2},
	    {"code":"3","sku":"AGUA","label":"Agua","price":1.5}
	  ]}
	]}}`
)

// importAPI arma la API con el repositorio en memoria como store de contenido Y
// como versionador (el mismo objeto satisface los dos puertos, igual que en
// producción lo hace *store.PostgresRepository) y con la feature catalog_import
// encendida para los dos tenants. Reusa keyAContent/keyBContent: el import
// comparte scope con tenant-content a propósito.
func importAPI() (*testAPI, *flowstore.MemoryRepository) {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	feats.Enable(tenantB, entitlements.FeatureCatalogImport)
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo},
		Entitlements: feats,
	}, apiKeys())
	return api, repo
}

func postImport(t *testing.T, api *testAPI, credential, query, body string) *catalogImportWire {
	t.Helper()
	rec := call(api, credential, http.MethodPost, "/api/v1/catalog/import"+query, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s code=%d, quiero 200; body=%s", query, rec.Code, rec.Body.String())
	}
	var out catalogImportWire
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal de la respuesta: %v; body=%s", err, rec.Body.String())
	}
	return &out
}

// wantDiffContraFixture comprueba el diff de docNuevo contra catalogoVigente: el
// café sube, entra el agua, se va el jugo y el té queda igual. Vive aparte porque
// lo afirman varios tests y porque, dentro del que además comprueba que nada se
// escribió, tapaba lo importante con seis condiciones seguidas.
func wantDiffContraFixture(t *testing.T, got *catalogImportWire) {
	t.Helper()
	if len(got.Diff.PriceChanges) != 1 || got.Diff.PriceChanges[0].SKU != "CAFE" ||
		got.Diff.PriceChanges[0].OldPrice != 2.5 || got.Diff.PriceChanges[0].NewPrice != 2.9 {
		t.Fatalf("price_changes=%+v, quiero CAFE 2.5→2.9", got.Diff.PriceChanges)
	}
	if len(got.Diff.Added) != 1 || got.Diff.Added[0].SKU != "AGUA" {
		t.Fatalf("added=%+v, quiero AGUA", got.Diff.Added)
	}
	if len(got.Diff.Removed) != 1 || got.Diff.Removed[0].SKU != "JUGO" {
		t.Fatalf("removed=%+v, quiero JUGO", got.Diff.Removed)
	}
	if got.Diff.Unchanged != 1 {
		t.Fatalf("unchanged=%d, quiero 1 (TE)", got.Diff.Unchanged)
	}
}

// currentBlob lee el blob vigente de (tenant, catalogo) del repositorio.
func currentBlob(t *testing.T, repo *flowstore.MemoryRepository, tenantID string) string {
	t.Helper()
	blob, err := repo.GetTenantContent(context.Background(), tenantID, "catalogo")
	if err != nil {
		t.Fatalf("leyendo el catálogo de %s: %v", tenantID, err)
	}
	return string(blob)
}

// TestCatalogImport_Validate_NoEscribeYDiceQuePasaria es la mitad "mirar" del
// contrato: el diff correcto contra el fixture Y la garantía de que mirar no
// cambia nada. Las dos cosas en un test porque separarlas permitiría que un
// validate que escribe pasara el primero.
func TestCatalogImport_Validate_NoEscribeYDiceQuePasaria(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	got := postImport(t, api, keyAContent, "?mode=validate", docNuevo)

	if got.Mode != "validate" || got.Applied {
		t.Fatalf("mode=%q applied=%v, quiero validate/false", got.Mode, got.Applied)
	}
	if got.Ref != "catalogo" {
		t.Fatalf("ref=%q, quiero el default «catalogo»", got.Ref)
	}
	if got.Items != 3 {
		t.Fatalf("items=%d, quiero 3 (los del documento subido)", got.Items)
	}
	wantDiffContraFixture(t, got)

	// Lo que de verdad se afirma: el catálogo NO se tocó.
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("validate escribió en tenant_content:\n%s", blob)
	}
	if v := repo.TenantContentVersions(tenantA, "catalogo"); len(v) != 0 {
		t.Fatalf("validate creó %d versiones; no debe crear ninguna", len(v))
	}
}

// TestCatalogImport_Apply_EscribeNuevoYArchivaViejo es el criterio central del
// versionado (D-041.8): tras aplicar, tenant_content tiene el catálogo NUEVO y la
// fila de versión guarda el VIEJO. Confundir los dos blobs es el error que deja
// al operador sin poder volver atrás, y solo se ve comparando los dos a la vez.
func TestCatalogImport_Apply_EscribeNuevoYArchivaViejo(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	got := postImport(t, api, keyAContent, "?mode=apply", docNuevo)
	if !got.Applied || got.Mode != "apply" {
		t.Fatalf("mode=%q applied=%v, quiero apply/true", got.Mode, got.Applied)
	}
	if got.ArchivedVersion != 1 {
		t.Fatalf("archived_version=%d, quiero 1 (había catálogo vigente que archivar)", got.ArchivedVersion)
	}

	// El vigente es el nuevo: AGUA entró y JUGO se fue.
	blob := currentBlob(t, repo, tenantA)
	if !strings.Contains(blob, "AGUA") || strings.Contains(blob, "JUGO") {
		t.Fatalf("el catálogo vigente no es el importado:\n%s", blob)
	}

	// La versión guarda el VIEJO, con la procedencia del acto que lo desplazó.
	versions := repo.TenantContentVersions(tenantA, "catalogo")
	if len(versions) != 1 {
		t.Fatalf("versiones=%d, quiero 1", len(versions))
	}
	if versions[0].Version != 1 || versions[0].Source != flowstore.VersionSourceImportJSON {
		t.Fatalf("versión=%+v, quiero {1, import_json}", versions[0])
	}
	if !strings.Contains(string(versions[0].Content), "JUGO") || strings.Contains(string(versions[0].Content), "AGUA") {
		t.Fatalf("la versión archivada NO es el catálogo viejo:\n%s", versions[0].Content)
	}
}

// TestCatalogImport_PrimerApply_NoVersionaYElSegundoSi es el hueco #9, dictaminado
// y verificado: «no hay versiones» y «no hay contenido» son casos distintos. Sobre
// una ref vacía el primer import escribe y NO archiva (no había nada que
// archivar); el segundo archiva como versión 1 lo que escribió el primero.
func TestCatalogImport_PrimerApply_NoVersionaYElSegundoSi(t *testing.T) {
	api, repo := importAPI()

	first := postImport(t, api, keyAContent, "?mode=apply", docNuevo)
	if first.ArchivedVersion != 0 {
		t.Fatalf("archived_version=%d en el primer import, quiero 0 (nada que archivar)", first.ArchivedVersion)
	}
	if len(first.Diff.Added) != 3 || len(first.Diff.Removed) != 0 {
		t.Fatalf("diff del primer import=%+v, quiero los 3 artículos como alta y ninguna baja", first.Diff)
	}
	if v := repo.TenantContentVersions(tenantA, "catalogo"); len(v) != 0 {
		t.Fatalf("el primer import creó %d versiones; sin blob vigente no se archiva nada", len(v))
	}

	second := postImport(t, api, keyAContent, "?mode=apply", docNuevo)
	if second.ArchivedVersion != 1 {
		t.Fatalf("archived_version=%d en el segundo import, quiero 1", second.ArchivedVersion)
	}
	versions := repo.TenantContentVersions(tenantA, "catalogo")
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versiones=%+v, quiero exactamente la 1", versions)
	}
}

// TestCatalogImport_SegundoApplyIdentico_DiffVacioYVersionNueva fija el desenlace
// DECLARADO del criterio de T3.3: reaplicar el mismo documento no cambia el
// catálogo (diff vacío) y SÍ deja una versión más, idéntica a lo que había.
//
// No es un descuido, es la opción elegida entre las dos que el criterio admite:
// convertirlo en no-op obligaría a decidir «idéntico» sobre el blob crudo, y dos
// blobs semánticamente iguales pero distintos byte a byte —el viejo con un campo
// que el import ya no escribe— dejarían el contenido viejo vivo mientras la
// respuesta dice «aplicado». Se prefiere escribir siempre: el archivo de versiones
// registra que hubo un acto de import, aunque no cambiara nada.
func TestCatalogImport_SegundoApplyIdentico_DiffVacioYVersionNueva(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	postImport(t, api, keyAContent, "?mode=apply", docNuevo)
	blobTrasPrimero := currentBlob(t, repo, tenantA)

	second := postImport(t, api, keyAContent, "?mode=apply", docNuevo)
	if len(second.Diff.PriceChanges) != 0 || len(second.Diff.Added) != 0 ||
		len(second.Diff.Removed) != 0 || len(second.Diff.ChangedDetails) != 0 {
		t.Fatalf("el segundo apply idéntico devolvió cambios: %+v", second.Diff)
	}
	if second.Diff.Unchanged != 3 {
		t.Fatalf("unchanged=%d, quiero los 3 artículos", second.Diff.Unchanged)
	}
	if second.ArchivedVersion != 2 {
		t.Fatalf("archived_version=%d, quiero 2 (versión nueva igual, no no-op)", second.ArchivedVersion)
	}
	if blob := currentBlob(t, repo, tenantA); blob != blobTrasPrimero {
		t.Fatalf("el contenido vigente cambió entre dos imports idénticos:\n%s\n%s", blobTrasPrimero, blob)
	}
	versions := repo.TenantContentVersions(tenantA, "catalogo")
	if len(versions) != 2 || string(versions[1].Content) != blobTrasPrimero {
		t.Fatalf("la versión 2 debería archivar lo que había tras el primer apply; versiones=%d", len(versions))
	}
}

// TestCatalogImport_INV10_SolicitudEnCursoConservaLabelYPrecio es INV-10 sobre el
// sistema real, no sobre una promesa: se aplica un import que SUBE el precio del
// café y después se hace avanzar el carrito de una conversación que ya tenía dos
// cafés dentro.
//
// El resumen que ve ese cliente sigue diciendo el precio al que los agregó, y el
// efecto de cierre —el que proyecta a intakes/intake_items— también. El catálogo
// gobierna lo que se AGREGA; lo agregado ya es historia de esa solicitud.
func TestCatalogImport_INV10_SolicitudEnCursoConservaLabelYPrecio(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	// El cliente ya tiene dos cafés a 2.5 y está en «¿agregar más o finalizar?».
	conv := model.Conversation{TenantID: tenantA, Vars: map[string]any{
		"cart": map[string]any{
			"level":    "continue",
			"cat_code": "1",
			"sku":      "CAFE",
			"lines": []any{map[string]any{
				"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5,
			}},
		},
	}}

	// El dueño sube el precio del café mientras esa conversación está viva.
	if got := postImport(t, api, keyAContent, "?mode=apply", docNuevo); !got.Applied {
		t.Fatal("el import no se aplicó")
	}

	// El carrito ve el catálogo NUEVO (es lo que el motor le sembraría ahora)…
	var nuevo map[string]any
	if err := json.Unmarshal([]byte(currentBlob(t, repo, tenantA)), &nuevo); err != nil {
		t.Fatalf("releyendo el catálogo aplicado: %v", err)
	}
	conv.Vars[modules.VarContentRaw] = nuevo

	// …y aun así, «2) Finalizar» resume la solicitud a los precios de cuando se agregó.
	res := cart.New().Step(model.Node{}, conv, "2")
	if len(res.Outputs) == 0 || !strings.Contains(res.Outputs[0], "5.00") {
		t.Fatalf("el resumen no conserva el precio de la línea (2 × 2.50 = 5.00): %v", res.Outputs)
	}
	if strings.Contains(res.Outputs[0], "5.80") {
		t.Fatalf("el resumen recalculó con el precio NUEVO (2 × 2.90): %v", res.Outputs)
	}

	// Y al confirmar, lo que se persiste lleva label y precio originales.
	confirm := cart.New().Step(model.Node{}, model.Conversation{TenantID: tenantA, Vars: res.Vars}, "1")
	items := closedItems(t, confirm.Effects)
	if len(items) != 1 {
		t.Fatalf("efecto de cierre con %d líneas, quiero 1", len(items))
	}
	if items[0]["label"] != "Café" || items[0]["unit_price"] != 2.5 {
		t.Fatalf("la línea persistida cambió con el catálogo: %+v", items[0])
	}
}

// closedItems extrae las líneas del efecto cart_closed.
func closedItems(t *testing.T, effects []modules.Effect) []map[string]any {
	t.Helper()
	for _, e := range effects {
		if e.Name != cart.EffectCartClosed {
			continue
		}
		items, ok := e.Payload["items"].([]map[string]any)
		if !ok {
			t.Fatalf("payload de cart_closed sin items: %+v", e.Payload)
		}
		return items
	}
	t.Fatalf("no se emitió cart_closed: %+v", effects)
	return nil
}

// TestCatalogImport_DocumentoInvalido_400ConTodosLosDefectos: el 400 del import no
// es un `{"error":"..."}`, es la lista completa de lo que hay que arreglar (T3.1).
// Y no escribe nada, ni siquiera en mode=apply.
func TestCatalogImport_DocumentoInvalido_400ConTodosLosDefectos(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	malo := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[
	  {"code":"1","label":"Bebidas","items":[
	    {"code":"1","sku":"CAFE","price":-1},
	    {"code":"1","sku":"CAFE","label":"Otro","price":2}
	  ]}
	]}}`
	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=apply", malo)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Errors []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 400: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "validation_failed" {
		t.Fatalf("error=%q, quiero validation_failed (la pantalla distingue por este código)", body.Error)
	}
	if len(body.Errors) < 3 {
		t.Fatalf("el 400 trae %d defectos; el documento tiene al menos 3 (label, precio, sku y código repetidos)", len(body.Errors))
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("un apply inválido escribió en tenant_content:\n%s", blob)
	}
}

// TestCatalogImport_ModoPorDefectoEsMirar_YElDesconocidoEs400 protege la decisión
// de seguridad del transporte: olvidar `mode` enseña el diff, nunca reemplaza el
// catálogo; y un modo tecleado mal se rechaza en vez de adivinarse.
func TestCatalogImport_ModoPorDefectoEsMirar_YElDesconocidoEs400(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	got := postImport(t, api, keyAContent, "", docNuevo)
	if got.Mode != "validate" || got.Applied {
		t.Fatalf("sin mode: mode=%q applied=%v, quiero validate/false", got.Mode, got.Applied)
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("una llamada sin mode escribió el catálogo:\n%s", blob)
	}

	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=aply", docNuevo)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mode=aply → code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImport_SinFeature_403 comprueba el gate catalog_import con el MISMO
// portador que entra a tenant-content: sin la feature, la respuesta es
// feature_not_enabled y el catálogo queda intacto. El scope no basta.
func TestCatalogImport_SinFeature_403(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo},
		Entitlements: entitlements.NewFake(), // ninguna feature encendida
	}, apiKeys())

	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=apply", docNuevo)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 403: %v", err)
	}
	if body["error"] != "feature_not_enabled" || body["feature"] != entitlements.FeatureCatalogImport {
		t.Fatalf("cuerpo del 403 = %+v, quiero feature_not_enabled/catalog_import", body)
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("un import sin feature llegó a escribir:\n%s", blob)
	}
}

// TestCatalogImport_SinScope_403 verifica el otro guardia, el que la feature NO
// sustituye: un portador sin content.write no pasa aunque su tenant tenga la
// capacidad contratada.
func TestCatalogImport_SinScope_403(t *testing.T) {
	api, _ := importAPI()
	rec := call(api, keyARead, http.MethodPost, "/api/v1/catalog/import?mode=apply", docNuevo)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 por scope; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImport_AisladoPorTenant es INV-8 en el import: el tenant sale del
// token y el documento no lo lleva (INV-05), así que B importando su catálogo no
// puede rozar el de A ni ver el suyo en el diff.
func TestCatalogImport_AisladoPorTenant(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	got := postImport(t, api, keyBContent, "?mode=apply", docNuevo)
	if len(got.Diff.Removed) != 0 || len(got.Diff.PriceChanges) != 0 {
		t.Fatalf("el diff de B vio el catálogo de A: %+v", got.Diff)
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("el import de B tocó el catálogo de A:\n%s", blob)
	}
	if len(repo.TenantContentVersions(tenantA, "catalogo")) != 0 {
		t.Fatal("el import de B archivó una versión bajo el tenant A")
	}
}

// TestCatalogImport_DocumentoDemasiadoGrande_413 comprueba que el techo de bytes
// del import es el MISMO mecanismo (y el mismo número) que gobierna la tabla:
// se aplica leyendo, antes de deserializar.
func TestCatalogImport_DocumentoDemasiadoGrande_413(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo, ContentMaxBytes: 64},
		Entitlements: feats,
	}, apiKeys())

	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=validate", docNuevo)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d, quiero 413; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImport_CatalogoVigenteIlegible_AvisaYNoRevienta es la otra mitad del
// hueco #7: la ref tiene contenido, pero no es un catálogo interpretable. El diff
// dirá «todo nuevo» —no hay contra qué comparar— y eso, a secas, se leería como
// «no pierdo nada»: el aviso es lo que impide esa lectura. Nunca un 500.
func TestCatalogImport_CatalogoVigenteIlegible_AvisaYNoRevienta(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(`{"prompt":"esto no es un catálogo"}`))

	got := postImport(t, api, keyAContent, "?mode=validate", docNuevo)
	if len(got.Diff.Added) != 3 {
		t.Fatalf("added=%+v, quiero los 3 artículos (no hay lado viejo comparable)", got.Diff.Added)
	}
	if len(got.Diff.CurrentWarnings) != 1 || !strings.Contains(got.Diff.CurrentWarnings[0], "no se pudo interpretar") {
		t.Fatalf("current_warnings=%v, quiero el aviso de catálogo vigente ilegible", got.Diff.CurrentWarnings)
	}
}

// storeCaido es un TenantContentStore que falla al leer, para el único desenlace
// del hueco #7 que NO se degrada.
type storeCaido struct{ publicapi.TenantContentStore }

func (storeCaido) GetTenantContent(_ context.Context, _, _ string) ([]byte, error) {
	return nil, errors.New("conexión perdida")
}

// TestCatalogImport_StoreCaido_500NoDiffMentiroso: si el catálogo vigente no se
// puede leer por un fallo de infraestructura, la respuesta es un error, NO un diff
// de «todo nuevo». Degradar aquí empujaría al operador a confirmar un reemplazo
// creyendo que no pierde nada.
func TestCatalogImport_StoreCaido_500NoDiffMentiroso(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: storeCaido{repo}, ContentVersions: repo},
		Entitlements: feats,
	}, apiKeys())

	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=validate", docNuevo)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImport_RefPropia escribe en una ref distinta de la de por defecto: el
// catálogo por defecto no se toca y el versionado va por (tenant, ref).
func TestCatalogImport_RefPropia(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	got := postImport(t, api, keyAContent, "?mode=apply&ref=catalogo-navidad", docNuevo)
	if got.Ref != "catalogo-navidad" || got.ArchivedVersion != 0 {
		t.Fatalf("ref=%q archived=%d, quiero catalogo-navidad sin versión (ref nueva)", got.Ref, got.ArchivedVersion)
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("importar a otra ref tocó el catálogo por defecto:\n%s", blob)
	}
}

// TestCatalogImport_SinVersionador_NoSeMonta: sin el puerto de versionado la ruta
// no existe (404), en vez de aplicar un import que no puede archivar nada.
func TestCatalogImport_SinVersionador_NoSeMonta(t *testing.T) {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	api := newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo},
		Entitlements: feats,
	}, apiKeys())

	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=validate", docNuevo)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (ruta no montada); body=%s", rec.Code, rec.Body.String())
	}
}

// TestCatalogImport_SkuDeEnvíoReservado_400ConMensajeClaro cierra el círculo de la
// línea estándar de envío (Plan 041 · T4.3, D-041.11): el espacio de nombres
// `_shipping` es seguro PORQUE ningún catálogo puede reclamarlo. Si esta puerta se
// abriera, un artículo del tenant sería indistinguible de la línea que pone wApp y
// el dueño no sabría cuál de las dos cobra el reparto.
//
// Se prueba por el ENDPOINT y con el sku exacto —no con un `_` cualquiera, que ya
// tiene su test unitario en el validador—: lo que aquí importa es que el rechazo
// llegue hasta quien sube el archivo, con el motivo que le dice qué arreglar.
func TestCatalogImport_SkuDeEnvíoReservado_400ConMensajeClaro(t *testing.T) {
	api, repo := importAPI()
	repo.SetTenantContent(tenantA, "catalogo", []byte(catalogoVigente))

	conEnvío := `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[
	  {"code":"1","label":"Bebidas","items":[
	    {"code":"1","sku":"` + intakes.ShippingSKU + `","label":"Despacho a domicilio","price":3000}
	  ]}
	]}}`
	rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=apply", conEnvío)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Error  string `json:"error"`
		Errors []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 400: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "validation_failed" || len(body.Errors) == 0 {
		t.Fatalf("respuesta=%+v; quiero validation_failed con el defecto detallado", body)
	}
	motivo := body.Errors[0].Reason
	if !strings.Contains(motivo, intakes.ShippingSKU) || !strings.Contains(motivo, "reservado") {
		t.Fatalf("motivo=%q; tiene que citar el sku %q y decir que está reservado",
			motivo, intakes.ShippingSKU)
	}
	if blob := currentBlob(t, repo, tenantA); blob != catalogoVigente {
		t.Fatalf("un apply rechazado escribió en tenant_content:\n%s", blob)
	}
}
