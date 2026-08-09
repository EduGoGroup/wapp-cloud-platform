// integrations_test.go cubre el CRUD /api/v1/integrations (Plan 042 · T5.1): los
// cuatro comportamientos del design §5, el aislamiento por tenant (INV-8) y el
// barrido que afirma que el secreto de firma NO sale por ninguna puerta.
package publicapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// El store REAL tiene que satisfacer el puerto: si no, estos tests estarían
// probando un fake que no se parece a lo que corre en producción.
var _ publicapi.IntegrationsStore = (*integrations.Postgres)(nil)

const (
	keyAIntegr     = "key-a-integr"      // tenantA, integrations.read + write
	keyBIntegr     = "key-b-integr"      // tenantB, ídem (el aislamiento debe fallar por tenant, no por scope)
	keyAIntegrRead = "key-a-integr-read" // tenantA, SOLO lectura

	// El secreto de las pruebas y su huella, calculada FUERA de este código
	// (`printf '%s' … | shasum -a 256`) para que el test no le pregunte la
	// respuesta a la misma función que verifica.
	//nolint:gosec // no es una credencial: es material de firma de un test
	secretoDePrueba = "secreto-de-firma-del-puente-jjx-2026"
	huellaDePrueba  = "e5c47775"
	//nolint:gosec // ídem
	secretoRotado = "secreto-ROTADO-del-puente-jjx-2026"
	huellaRotada  = "b7d7294f"

	endpointDePrueba = "https://puente.cliente.example/wapp/eventos"
)

// integrKeys extiende apiKeys() con las credenciales del CRUD de integraciones.
func integrKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyAIntegr] = testIdentity{TenantID: tenantA, Subject: "admin-a",
		Grants: []string{"integrations.read", "integrations.write"}}
	keys[keyBIntegr] = testIdentity{TenantID: tenantB, Subject: "admin-b",
		Grants: []string{"integrations.read", "integrations.write"}}
	keys[keyAIntegrRead] = testIdentity{TenantID: tenantA, Subject: "viewer-a",
		Grants: []string{"integrations.read"}}
	return keys
}

// errStoreCaído es el fallo de infraestructura que simula el fake.
var errStoreCaído = errors.New("integrations: la base no responde")

// --- Fake del store ---

// fakeIntegrations imita a *integrations.Postgres en lo que importa aquí,
// INCLUIDA la regla que más fácil sería falsear: `secret == ""` conserva el
// secreto existente (postgres.go:258). Un fake que lo borrara haría pasar un
// handler roto.
type fakeIntegrations struct {
	rows    map[string]integrations.TenantIntegration
	secrets map[string]string
	upserts int
	deletes int
	// failGet fuerza el fallo de infraestructura, para el 500 del GET.
	failGet error
}

func newFakeIntegrations() *fakeIntegrations {
	return &fakeIntegrations{
		rows:    map[string]integrations.TenantIntegration{},
		secrets: map[string]string{},
	}
}

// seed pone una fila ya configurada (el caso del tenant que ya tiene puente).
func (f *fakeIntegrations) seed(tenantID, secret string) {
	f.rows[tenantID] = integrations.TenantIntegration{
		TenantID:       tenantID,
		CatalogAdapter: "local",
		EventsAdapter:  "webhook",
		EndpointURL:    endpointDePrueba,
		Enabled:        true,
		CreatedAt:      time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC),
	}
	if secret != "" {
		f.secrets[tenantID] = secret
	}
}

func (f *fakeIntegrations) GetTenantIntegration(_ context.Context, tenantID string) (integrations.TenantIntegration, bool, error) {
	if f.failGet != nil {
		return integrations.TenantIntegration{}, false, f.failGet
	}
	ti, ok := f.rows[tenantID]
	if !ok {
		return integrations.TenantIntegration{}, false, nil
	}
	ti.HasSecret = f.secrets[tenantID] != ""
	return ti, true, nil
}

func (f *fakeIntegrations) UpsertTenantIntegration(_ context.Context, ti integrations.TenantIntegration, secret string) error {
	f.upserts++
	if secret != "" {
		f.secrets[ti.TenantID] = secret
	}
	prev, existía := f.rows[ti.TenantID]
	ti.CreatedAt = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if existía {
		ti.CreatedAt = prev.CreatedAt
	}
	ti.UpdatedAt = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ti.HasSecret = f.secrets[ti.TenantID] != ""
	f.rows[ti.TenantID] = ti
	return nil
}

func (f *fakeIntegrations) DeleteTenantIntegration(_ context.Context, tenantID string) error {
	f.deletes++
	delete(f.rows, tenantID)
	delete(f.secrets, tenantID)
	return nil
}

func (f *fakeIntegrations) SecretFingerprint(_ context.Context, tenantID string) (string, bool, error) {
	secret := f.secrets[tenantID]
	if secret == "" {
		return "", false, nil
	}
	return integrations.Fingerprint(secret), true, nil
}

// --- Harness ---

// integrationWire espeja el contrato del DTO en el cable. Se declara aquí, y no
// se importa del paquete, para que un cambio de nombre de campo se vea como
// fallo de test y no pase inadvertido: esto es el contrato que consume el BFF.
type integrationWire struct {
	Configured        bool   `json:"configured"`
	CatalogAdapter    string `json:"catalog_adapter"`
	EventsAdapter     string `json:"events_adapter"`
	EndpointURL       string `json:"endpoint_url"`
	Enabled           bool   `json:"enabled"`
	SecretSet         bool   `json:"secret_set"`
	SecretFingerprint string `json:"secret_fingerprint"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// integrAPI arma la API con el fake y la feature crm_bridge encendida para los
// DOS tenants: los tests de aislamiento tienen que fallar por tenant, nunca por
// plan.
func integrAPI(store publicapi.IntegrationsStore) *testAPI {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCRMBridge)
	feats.Enable(tenantB, entitlements.FeatureCRMBridge)
	return newAPI(publicapi.Deps{Integrations: store, Entitlements: feats}, integrKeys())
}

func decodeIntegrWire(t *testing.T, body []byte) integrationWire {
	t.Helper()
	var out integrationWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal de la integración: %v; body=%s", err, body)
	}
	return out
}

// putIntegrBody arma el cuerpo del PUT desde un mapa, y no desde un struct
// tipado, a propósito: así se pueden colar campos que el contrato NO tiene (es lo
// que hace el test de INV-8 con `tenant_id`).
func putIntegrBody(t *testing.T, campos map[string]any) string {
	t.Helper()
	body, err := json.Marshal(campos)
	if err != nil {
		t.Fatalf("marshal del cuerpo: %v", err)
	}
	return string(body)
}

// configuraciónVálida es el cuerpo que deja un puente encendido y coherente.
func configuraciónVálida() map[string]any {
	return map[string]any{
		"catalog_adapter": "local",
		"events_adapter":  "webhook",
		"endpoint_url":    endpointDePrueba,
		"secret":          secretoDePrueba,
		"enabled":         true,
	}
}

// ============================ GET ============================

// TestIntegrations_GET_SinFila_DevuelveElDefaultLocalLocal: un tenant sin puente
// no es un 404. La migración 0047 dice que «sin fila = local/local»; el GET lo
// dice en JSON, y `configured:false` es lo que distingue ese default de una fila
// puesta a mano en local.
func TestIntegrations_GET_SinFila_DevuelveElDefaultLocalLocal(t *testing.T) {
	api := integrAPI(newFakeIntegrations())

	rec := call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeIntegrWire(t, rec.Body.Bytes())
	quiero := integrationWire{CatalogAdapter: "local", EventsAdapter: "local"}
	if got != quiero {
		t.Fatalf("default de un tenant sin fila:\n got %+v\nwant %+v", got, quiero)
	}
}

// TestIntegrations_GET_DevuelveLaHuellaYNuncaElSecreto es el comportamiento 1 del
// design §5: `secret_set` + huella corta, jamás el valor (REQ-13 / D-042.7).
func TestIntegrations_GET_DevuelveLaHuellaYNuncaElSecreto(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, secretoDePrueba)
	api := integrAPI(store)

	rec := call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeIntegrWire(t, rec.Body.Bytes())
	if !got.Configured || !got.SecretSet {
		t.Fatalf("configured=%v secret_set=%v, quiero los dos true; body=%s", got.Configured, got.SecretSet, rec.Body.String())
	}
	if got.SecretFingerprint != huellaDePrueba {
		t.Fatalf("secret_fingerprint=%q, quiero %q", got.SecretFingerprint, huellaDePrueba)
	}
	if got.EndpointURL != endpointDePrueba || got.EventsAdapter != "webhook" || !got.Enabled {
		t.Fatalf("la configuración no viajó entera: %+v", got)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("faltan las marcas de tiempo: %+v", got)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(secretoDePrueba)) {
		t.Fatalf("FUGA: el GET devolvió el secreto.\nCuerpo:\n%s", rec.Body.String())
	}
}

// TestIntegrations_GET_SinSecreto_NoInventaHuella: una fila configurada pero sin
// secreto responde secret_set=false y SIN huella. «No hay» no se disfraza.
func TestIntegrations_GET_SinSecreto_NoInventaHuella(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, "") // fila sí, secreto no
	api := integrAPI(store)

	got := decodeIntegrWire(t, call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "").Body.Bytes())
	if got.SecretSet || got.SecretFingerprint != "" {
		t.Fatalf("secret_set=%v fingerprint=%q, quiero false y vacía", got.SecretSet, got.SecretFingerprint)
	}
	if !got.Configured {
		t.Fatal("la fila existe: configured tenía que ser true")
	}
}

// ============================ PUT ============================

// TestIntegrations_PUT_UpsertCompleto es el comportamiento 2: crea, y luego
// actualiza SIN reenviar el secreto (que es como reconfigura una pantalla, porque
// el GET no devuelve el valor para poder reenviarlo).
func TestIntegrations_PUT_UpsertCompleto(t *testing.T) {
	store := newFakeIntegrations()
	api := integrAPI(store)

	// 1) Alta.
	rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, configuraciónVálida()))
	if rec.Code != http.StatusOK {
		t.Fatalf("alta: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeIntegrWire(t, rec.Body.Bytes())
	if !got.Configured || got.EventsAdapter != "webhook" || got.EndpointURL != endpointDePrueba || !got.Enabled {
		t.Fatalf("el alta no quedó como se pidió: %+v", got)
	}
	if got.SecretFingerprint != huellaDePrueba {
		t.Fatalf("huella tras el alta=%q, quiero %q", got.SecretFingerprint, huellaDePrueba)
	}

	// 2) Cambio de endpoint SIN secreto: el secreto se conserva.
	sinSecreto := configuraciónVálida()
	delete(sinSecreto, "secret")
	sinSecreto["endpoint_url"] = "https://otro.example/hook"
	rec = call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, sinSecreto))
	if rec.Code != http.StatusOK {
		t.Fatalf("reconfiguración: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got = decodeIntegrWire(t, rec.Body.Bytes())
	if got.EndpointURL != "https://otro.example/hook" {
		t.Fatalf("endpoint_url=%q, quiero el nuevo", got.EndpointURL)
	}
	if got.SecretFingerprint != huellaDePrueba {
		t.Fatalf("huella tras reconfigurar=%q, quiero la MISMA %q: el secreto no debía tocarse",
			got.SecretFingerprint, huellaDePrueba)
	}

	// 3) Rotación del secreto: la huella cambia.
	rotado := configuraciónVálida()
	rotado["secret"] = secretoRotado
	rec = call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, rotado))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotación: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if got = decodeIntegrWire(t, rec.Body.Bytes()); got.SecretFingerprint != huellaRotada {
		t.Fatalf("huella tras rotar=%q, quiero %q", got.SecretFingerprint, huellaRotada)
	}
}

// TestIntegrations_PUT_CatalogAdapterHTTP_422 es el comportamiento 3: el valor es
// del vocabulario (el CHECK de la 0047 lo admite) pero el verbo catalog.pull está
// DIFERIDO. 422 y no 400: la petición se entiende, no se puede cumplir.
func TestIntegrations_PUT_CatalogAdapterHTTP_422(t *testing.T) {
	store := newFakeIntegrations()
	api := integrAPI(store)

	body := configuraciónVálida()
	body["catalog_adapter"] = "http"
	rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, body))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "catalog.pull") {
		t.Fatalf("el 422 no explica qué está diferido: %s", rec.Body.String())
	}
	if store.upserts != 0 {
		t.Fatalf("upserts=%d: no debió tocar el store", store.upserts)
	}
}

// TestIntegrations_PUT_FormasInadmisibles recorre lo que la BD no puede rechazar
// sola. La última fila es la que más importa: un puente ENCENDIDO sin endpoint o
// sin secreto se guardaría «bien» y fallaría entrega tras entrega hasta `dead`
// (worker.go:427-437), horas después y en una tabla que nadie mira.
func TestIntegrations_PUT_FormasInadmisibles(t *testing.T) {
	casos := []struct {
		nombre string
		cambio func(map[string]any)
		quiero int
	}{
		{"adaptador de eventos desconocido", func(m map[string]any) { m["events_adapter"] = "kafka" }, http.StatusBadRequest},
		{"adaptador de catálogo desconocido", func(m map[string]any) { m["catalog_adapter"] = "ftp" }, http.StatusBadRequest},
		{"endpoint que no es URL absoluta", func(m map[string]any) { m["endpoint_url"] = "/solo/una/ruta" }, http.StatusBadRequest},
		{"endpoint con esquema raro", func(m map[string]any) { m["endpoint_url"] = "gopher://viejo.example/x" }, http.StatusBadRequest},
		{"secreto demasiado corto", func(m map[string]any) { m["secret"] = "corto" }, http.StatusBadRequest},
		{"webhook encendido sin endpoint", func(m map[string]any) { m["endpoint_url"] = "" }, http.StatusBadRequest},
		{"webhook encendido sin secreto", func(m map[string]any) { delete(m, "secret") }, http.StatusBadRequest},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			store := newFakeIntegrations()
			api := integrAPI(store)
			body := configuraciónVálida()
			c.cambio(body)

			rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, body))
			if rec.Code != c.quiero {
				t.Fatalf("code=%d, quiero %d; body=%s", rec.Code, c.quiero, rec.Body.String())
			}
			if store.upserts != 0 {
				t.Fatalf("upserts=%d: una configuración rechazada no se guarda", store.upserts)
			}
		})
	}
}

// TestIntegrations_PUT_ApagadoAMedioConfigurar_SeGuarda: el reverso del test
// anterior. Un puente APAGADO e incompleto es un estado legítimo — es como se
// prepara uno antes de encenderlo—, así que la regla dura solo aplica con
// enabled=true.
func TestIntegrations_PUT_ApagadoAMedioConfigurar_SeGuarda(t *testing.T) {
	store := newFakeIntegrations()
	api := integrAPI(store)

	body := map[string]any{"events_adapter": "webhook", "enabled": false}
	rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeIntegrWire(t, rec.Body.Bytes())
	if got.Enabled || got.EventsAdapter != "webhook" || got.SecretSet {
		t.Fatalf("no quedó como se pidió: %+v", got)
	}
}

// ============================ El gate y los scopes ============================

// TestIntegrations_403_SinLaFeature es el comportamiento 4: sin `crm_bridge` los
// TRES verbos cortan, también el GET. Fail-closed con el cuerpo estable del gate.
func TestIntegrations_403_SinLaFeature(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, secretoDePrueba)
	// Ninguna feature encendida.
	api := newAPI(publicapi.Deps{Integrations: store, Entitlements: entitlements.NewFake()}, integrKeys())

	casos := []struct {
		método string
		cuerpo string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"events_adapter":"local"}`},
		{http.MethodDelete, ""},
	}
	for _, c := range casos {
		t.Run(c.método, func(t *testing.T) {
			rec := call(api, keyAIntegr, c.método, "/api/v1/integrations", c.cuerpo)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error   string `json:"error"`
				Feature string `json:"feature"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal del 403: %v; body=%s", err, rec.Body.String())
			}
			if body.Error != "feature_not_enabled" || body.Feature != entitlements.FeatureCRMBridge {
				t.Fatalf("cuerpo del 403 = %+v, quiero feature_not_enabled/crm_bridge", body)
			}
		})
	}
	if store.upserts != 0 || store.deletes != 0 {
		t.Fatalf("el gate dejó pasar escrituras: upserts=%d deletes=%d", store.upserts, store.deletes)
	}
}

// TestIntegrations_403_SoloLectura_NoEscribe: los scopes propios reparten como se
// diseñó — quien solo lee no puede repuntar el endpoint del puente ni rotar el
// secreto.
func TestIntegrations_403_SoloLectura_NoEscribe(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, secretoDePrueba)
	api := integrAPI(store)

	if rec := call(api, keyAIntegrRead, http.MethodGet, "/api/v1/integrations", ""); rec.Code != http.StatusOK {
		t.Fatalf("lectura: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, método := range []string{http.MethodPut, http.MethodDelete} {
		rec := call(api, keyAIntegrRead, método, "/api/v1/integrations", putIntegrBody(t, configuraciónVálida()))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: code=%d, quiero 403; body=%s", método, rec.Code, rec.Body.String())
		}
	}
	if store.upserts != 0 || store.deletes != 0 {
		t.Fatalf("escribió sin scope: upserts=%d deletes=%d", store.upserts, store.deletes)
	}
}

// TestIntegrations_GET_500_SiElStoreFalla: un fallo de infraestructura NO se
// disfraza de «no tienes puente». La pantalla tiene que poder distinguir «local»
// de «no pude leerlo».
func TestIntegrations_GET_500_SiElStoreFalla(t *testing.T) {
	store := newFakeIntegrations()
	store.failGet = errStoreCaído
	api := integrAPI(store)

	if rec := call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntegrations_401_SinToken: sin identidad no hay tenant del que sacar nada.
func TestIntegrations_401_SinToken(t *testing.T) {
	api := integrAPI(newFakeIntegrations())
	if rec := call(api, "", http.MethodGet, "/api/v1/integrations", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

// TestIntegrations_SinStore_NoSeMontanLasRutas: 404 de ruta inexistente, mejor que
// un 500 a medio camino.
func TestIntegrations_SinStore_NoSeMontanLasRutas(t *testing.T) {
	api := newAPI(publicapi.Deps{Entitlements: entitlements.NewFake()}, integrKeys())
	if rec := call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404", rec.Code)
	}
}

// ============================ INV-8 ============================

// TestIntegrations_INV8_ElTenantIDDelCuerpoSeIgnora: el criterio explícito de
// T5.1. Un cuerpo con el `tenant_id` de OTRO tenant no mueve nada de ese otro
// tenant: la operación va contra el del token, en silencio.
//
// Se comprueba por los DOS lados —lo que cambió y lo que NO— porque afirmar solo
// que A quedó configurado pasaría igual si además se hubiera pisado a B.
func TestIntegrations_INV8_ElTenantIDDelCuerpoSeIgnora(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantB, secretoRotado) // B ya tenía su puente
	api := integrAPI(store)

	cuerpo := configuraciónVálida()
	cuerpo["tenant_id"] = tenantB // ← el intento
	cuerpo["endpoint_url"] = "https://el-endpoint-de-A.example/hook"

	rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", putIntegrBody(t, cuerpo))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	// 1) Se operó sobre el tenant del TOKEN.
	filaA, okA := store.rows[tenantA]
	if !okA {
		t.Fatal("el PUT no escribió la fila del tenant del token")
	}
	if filaA.EndpointURL != "https://el-endpoint-de-A.example/hook" {
		t.Fatalf("endpoint de A=%q, quiero el del cuerpo", filaA.EndpointURL)
	}
	if store.secrets[tenantA] != secretoDePrueba {
		t.Fatalf("el secreto de A no es el que se mandó")
	}

	// 2) El tenant del CUERPO quedó intacto.
	filaB := store.rows[tenantB]
	if filaB.EndpointURL != endpointDePrueba || store.secrets[tenantB] != secretoRotado {
		t.Fatalf("FUGA DE TENANT: el cuerpo movió la fila de B: %+v / secreto=%q",
			filaB, store.secrets[tenantB])
	}

	// 3) Y el GET de B sigue enseñando lo suyo.
	got := decodeIntegrWire(t, call(api, keyBIntegr, http.MethodGet, "/api/v1/integrations", "").Body.Bytes())
	if got.EndpointURL != endpointDePrueba || got.SecretFingerprint != huellaRotada {
		t.Fatalf("B ve algo que no es suyo: %+v", got)
	}
}

// TestIntegrations_INV8_ElDeleteSoloBorraLoPropio: la otra mitad del aislamiento.
func TestIntegrations_INV8_ElDeleteSoloBorraLoPropio(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, secretoDePrueba)
	store.seed(tenantB, secretoRotado)
	api := integrAPI(store)

	if rec := call(api, keyAIntegr, http.MethodDelete, "/api/v1/integrations", `{"tenant_id":"`+tenantB+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
	if _, sigue := store.rows[tenantA]; sigue {
		t.Fatal("no borró la fila del tenant del token")
	}
	if _, sigue := store.rows[tenantB]; !sigue {
		t.Fatal("FUGA DE TENANT: borró la fila del tenant del cuerpo")
	}
}

// ============================ DELETE ============================

// TestIntegrations_DELETE_VuelveALocalLocalYEsIdempotente: tras borrar, el GET
// responde el default; y un segundo DELETE responde 204 igual, porque lo que se
// pide es un ESTADO y ese estado ya está.
func TestIntegrations_DELETE_VuelveALocalLocalYEsIdempotente(t *testing.T) {
	store := newFakeIntegrations()
	store.seed(tenantA, secretoDePrueba)
	api := integrAPI(store)

	for intento := 1; intento <= 2; intento++ {
		rec := call(api, keyAIntegr, http.MethodDelete, "/api/v1/integrations", "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %d: code=%d, quiero 204; body=%s", intento, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("DELETE %d: el 204 llevó cuerpo: %s", intento, rec.Body.String())
		}
	}
	got := decodeIntegrWire(t, call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "").Body.Bytes())
	if got.Configured || got.EventsAdapter != "local" || got.SecretSet {
		t.Fatalf("tras borrar no volvió a local/local: %+v", got)
	}
}

// ============================ El barrido del secreto ============================

// TestIntegrations_ElSecretoNoSalePorNingunaPuerta es el criterio de fuga de T5.1,
// hecho sobre lo que de verdad importa: los BYTES que salen y los bytes que se
// escriben.
//
// Barre las TRES salidas del CRUD, el log del servidor y la bitácora de
// auditoría, después de un recorrido que mete el secreto por el PUT dos veces
// (alta y rotación). Un solo camino olvidado bastaría para que el secreto de
// firma del puente saliera de la plataforma.
func TestIntegrations_ElSecretoNoSalePorNingunaPuerta(t *testing.T) {
	store := newFakeIntegrations()
	logBuf := &bytes.Buffer{}
	auditor := &recordingAuditor{}
	api := apiConLog(publicapi.Deps{Integrations: store, Entitlements: crmBridgePara(tenantA, tenantB)},
		integrKeys(), logBuf, auditor)

	respuestas := map[string]*httptest.ResponseRecorder{}
	respuestas["PUT (alta)"] = call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations",
		putIntegrBody(t, configuraciónVálida()))
	respuestas["GET"] = call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "")

	rotado := configuraciónVálida()
	rotado["secret"] = secretoRotado
	respuestas["PUT (rotación)"] = call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations",
		putIntegrBody(t, rotado))
	respuestas["GET (tras rotar)"] = call(api, keyAIntegr, http.MethodGet, "/api/v1/integrations", "")
	respuestas["DELETE"] = call(api, keyAIntegr, http.MethodDelete, "/api/v1/integrations", "")

	// El recorrido tiene que haber ocurrido de verdad: si los PUT hubieran
	// fallado, el barrido no probaría nada.
	for dónde, rec := range respuestas {
		if rec.Code >= http.StatusBadRequest {
			t.Fatalf("%s falló (code=%d): el barrido no probaría nada; body=%s", dónde, rec.Code, rec.Body.String())
		}
		sinElSecreto(t, dónde, rec.Body.Bytes())
	}
	sinElSecreto(t, "el log del servidor", logBuf.Bytes())

	auditado, err := json.Marshal(auditor.eventos)
	if err != nil {
		t.Fatalf("marshal de la auditoría: %v", err)
	}
	sinElSecreto(t, "la bitácora de auditoría", auditado)
	if len(auditor.eventos) == 0 {
		t.Fatal("no se auditó ninguna escritura: el barrido de la bitácora no probaría nada")
	}
}

// sinElSecreto falla si el buffer contiene alguno de los dos secretos. La huella
// SÍ puede aparecer: es lo que la API publica a propósito.
func sinElSecreto(t *testing.T, dónde string, datos []byte) {
	t.Helper()
	for _, secreto := range []string{secretoDePrueba, secretoRotado} {
		if bytes.Contains(datos, []byte(secreto)) {
			t.Fatalf("FUGA en %s: contiene el secreto de firma %q.\nContenido:\n%s", dónde, secreto, datos)
		}
	}
}

// crmBridgePara enciende la feature del puente para los tenants dados.
func crmBridgePara(tenants ...string) *entitlements.Fake {
	feats := entitlements.NewFake()
	for _, t := range tenants {
		feats.Enable(t, entitlements.FeatureCRMBridge)
	}
	return feats
}

// recordingAuditor guarda lo que se auditó, para poder barrerlo.
type recordingAuditor struct{ eventos []in.AuditInput }

func (r *recordingAuditor) Record(_ context.Context, ev in.AuditInput) error {
	r.eventos = append(r.eventos, ev)
	return nil
}

// apiConLog es newAPI con un logger y un auditor observables. Existe solo para el
// barrido: el resto de tests no necesita mirar lo que se escribe.
func apiConLog(d publicapi.Deps, keys map[string]testIdentity, w *bytes.Buffer, auditor httpapi.AuditRecorder) *testAPI {
	jwt := sharedjwt.NewJWTManager(tokenSecret, tokenIssuer)
	mux := http.NewServeMux()
	publicapi.Register(mux, d, httpapi.NewMiddleware(jwt, nil), auditor, sharedlogger.New(sharedlogger.WithWriter(w)))
	return &testAPI{mux: mux, jwt: jwt, identities: keys}
}
