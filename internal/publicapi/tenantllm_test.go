// tenantllm_test.go cubre el CRUD /api/v1/tenant-llm (Plan 044 · Ola 0 · T0.3):
// los cuatro criterios literales de la tarea —PUT sin consentimiento ⇒ 400, GET
// sin la clave, DELETE revoca, tenant del token (INV-7)—, el gate `api_llm` y el
// barrido que afirma que la API key NO sale por ninguna puerta.
//
// 🔴 EL CRITERIO «el BYTEA está cifrado, verificable por SQL» NO ESTÁ AQUÍ, y no
// es un olvido: un fake en memoria no puede demostrarlo (guarda un string, no un
// blob). Vive en internal/tenantllm/postgres_integration_test.go, contra
// Postgres real, por el mismo argumento que
// integrations/postgres_integration_test.go:1-5.
package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// El store REAL tiene que satisfacer el puerto: si no, estos tests estarían
// probando un fake que no se parece a lo que corre en producción.
var _ publicapi.TenantLLMStore = (*tenantllm.Postgres)(nil)

const (
	keyALLM     = "key-a-llm"      // tenantA, llm.read + llm.write
	keyBLLM     = "key-b-llm"      // tenantB, ídem (el aislamiento debe fallar por tenant, no por scope)
	keyALLMRead = "key-a-llm-read" // tenantA, SOLO lectura

	// La credencial de las pruebas. Lleva el prefijo público real de Anthropic a
	// propósito: es la cadena que el barrido de fugas busca, y buscar una cadena
	// con la forma de la de verdad es lo que hace al barrido creíble.
	//nolint:gosec // no es una credencial: es material de prueba inventado
	claveDePrueba = "sk-ant-api03-CLAVE-FALSA-DE-PRUEBA-0044"
	//nolint:gosec // ídem
	claveRotada = "sk-ant-api03-CLAVE-FALSA-ROTADA-0044"

	modeloDePrueba = "claude-sonnet-4-5"
)

// llmKeys extiende apiKeys() con las credenciales del CRUD de tenant-llm.
func llmKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyALLM] = testIdentity{TenantID: tenantA, Subject: "admin-a",
		Grants: []string{"llm.read", "llm.write"}}
	keys[keyBLLM] = testIdentity{TenantID: tenantB, Subject: "admin-b",
		Grants: []string{"llm.read", "llm.write"}}
	keys[keyALLMRead] = testIdentity{TenantID: tenantA, Subject: "viewer-a",
		Grants: []string{"llm.read"}}
	return keys
}

// errLLMStoreCaído es el fallo de infraestructura que simula el fake.
var errLLMStoreCaído = errors.New("tenantllm: la base no responde")

// --- Fake del store ---

// fakeTenantLLM imita a *tenantllm.Postgres en lo que importa aquí. Guarda la
// clave APARTE de la Config, igual que la tabla la guarda en columnas propias:
// un fake que la metiera dentro de tenantllm.Config haría pasar un handler que
// la serializase por error, que es justo el fallo que estos tests buscan.
type fakeTenantLLM struct {
	rows    map[string]tenantllm.Config
	claves  map[string]string
	upserts int
	deletes int
	// últimaClave es la ÚLTIMA credencial que el handler mandó guardar. Es el
	// testigo que convierte «el PUT guardó» en «el PUT guardó ESTO».
	últimaClave string
	// últimoConsent es el instante que el handler decidió para consented_at.
	últimoConsent time.Time
	// failGet fuerza el fallo de infraestructura, para el 500 del GET.
	failGet error
}

func newFakeTenantLLM() *fakeTenantLLM {
	return &fakeTenantLLM{
		rows:   map[string]tenantllm.Config{},
		claves: map[string]string{},
	}
}

func (f *fakeTenantLLM) Get(_ context.Context, tenantID string) (tenantllm.Config, bool, error) {
	if f.failGet != nil {
		return tenantllm.Config{}, false, f.failGet
	}
	cfg, ok := f.rows[tenantID]
	if !ok {
		return tenantllm.Config{}, false, nil
	}
	cfg.HasAPIKey = f.claves[tenantID] != ""
	return cfg, true, nil
}

func (f *fakeTenantLLM) Upsert(_ context.Context, cfg tenantllm.Config, apiKey string, consentedAt time.Time) error {
	if apiKey == "" {
		return errors.New("fake: upsert sin API key")
	}
	f.upserts++
	f.últimaClave = apiKey
	f.últimoConsent = consentedAt
	previa, existía := f.rows[cfg.TenantID]
	cfg.ConsentedAt = consentedAt
	cfg.UpdatedAt = consentedAt
	if existía {
		cfg.CreatedAt = previa.CreatedAt // el alta es el alta: no se pisa
	} else {
		cfg.CreatedAt = consentedAt
	}
	f.rows[cfg.TenantID] = cfg
	f.claves[cfg.TenantID] = apiKey
	return nil
}

func (f *fakeTenantLLM) Delete(_ context.Context, tenantID string) error {
	f.deletes++
	delete(f.rows, tenantID)
	delete(f.claves, tenantID)
	return nil
}

// --- Andamiaje ---

// llmWire es la forma del JSON tal como VIAJA. Se declara aparte del DTO del
// paquete (que es privado) para que el test lea el cable y no la estructura
// interna: si alguien renombra un campo JSON, este test lo nota.
type llmWire struct {
	Configured  bool   `json:"configured"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	KeySet      bool   `json:"key_set"`
	ConsentedAt string `json:"consented_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// nuevaAPILLM monta la API con el gate `api_llm` CONCEDIDO a los dos tenants: es
// el escenario por defecto de casi todos los tests de este fichero, que van del
// contrato y no del gate.
func nuevaAPILLM(store publicapi.TenantLLMStore) *testAPI {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureAPILLM)
	feats.Enable(tenantB, entitlements.FeatureAPILLM)
	return newAPI(publicapi.Deps{TenantLLM: store, Entitlements: feats}, llmKeys())
}

func decodeLLMWire(t *testing.T, body []byte) llmWire {
	t.Helper()
	var out llmWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal de la configuración LLM: %v; body=%s", err, body)
	}
	return out
}

// putLLMBody arma el cuerpo del PUT desde un mapa, y no desde un struct tipado, a
// propósito: así se pueden colar campos que el contrato NO tiene (es lo que hace
// el test de INV-7 con `tenant_id`).
func putLLMBody(t *testing.T, campos map[string]any) string {
	t.Helper()
	body, err := json.Marshal(campos)
	if err != nil {
		t.Fatalf("marshal del cuerpo: %v", err)
	}
	return string(body)
}

// configuraciónLLMVálida es el cuerpo mínimo que deja una vía API configurada.
func configuraciónLLMVálida() map[string]any {
	return map[string]any{
		"provider":  tenantllm.ProviderAnthropic,
		"model":     modeloDePrueba,
		"api_key":   claveDePrueba,
		"consented": true,
	}
}

// exigeCódigo falla si el código no es el esperado, enseñando el cuerpo. Se
// extrae porque lo usan casi todos los tests y porque gocyclo mide también los
// tests: repetir el if en cada uno los engorda sin decir nada nuevo.
func exigeCódigo(t *testing.T, rec *httptest.ResponseRecorder, quiero int) {
	t.Helper()
	if rec.Code != quiero {
		t.Fatalf("code=%d, quiero %d; body=%s", rec.Code, quiero, rec.Body.String())
	}
}

// exigeErrorJSON comprueba el campo `error` del cuerpo (y opcionalmente un
// segundo campo, que es como la API nombra el sujeto del rechazo:
// {"error":"invalid_provider","provider":"…"}).
func exigeErrorJSON(t *testing.T, cuerpo []byte, quieroError, campo, quieroCampo string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(cuerpo, &body); err != nil {
		t.Fatalf("unmarshal del error: %v; body=%s", err, cuerpo)
	}
	if body["error"] != quieroError {
		t.Fatalf("error=%q, quiero %q; body=%s", body["error"], quieroError, cuerpo)
	}
	if campo != "" && body[campo] != quieroCampo {
		t.Fatalf("%s=%q, quiero %q; body=%s", campo, body[campo], quieroCampo, cuerpo)
	}
}

// ============================ El consentimiento ============================

// TestTenantLLM_PutSinConsentimiento400 es el primer criterio literal de T0.3.
//
// 🔬 MUTACIÓN QUE LO PONE ROJO: quitar el bloque `if !req.Consented` de
// validateTenantLLM (tenantllm.go) ⇒ el PUT devolvería 200 y habría fila.
func TestTenantLLM_PutSinConsentimiento400(t *testing.T) {
	casos := map[string]map[string]any{
		"ausente": {"provider": tenantllm.ProviderAnthropic, "model": modeloDePrueba, "api_key": claveDePrueba},
		"false":   {"provider": tenantllm.ProviderAnthropic, "model": modeloDePrueba, "api_key": claveDePrueba, "consented": false},
	}
	for nombre, cuerpo := range casos {
		t.Run(nombre, func(t *testing.T) {
			store := newFakeTenantLLM()
			api := nuevaAPILLM(store)
			rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo))
			exigeCódigo(t, rec, http.StatusBadRequest)
			exigeErrorJSON(t, rec.Body.Bytes(), "consent_required", "", "")
			// Y —lo que de verdad importa— NO se guardó nada. Un 400 que además
			// hubiera escrito la fila sería peor que un 200 honesto.
			if store.upserts != 0 {
				t.Fatalf("upserts=%d, quiero 0: un PUT rechazado no puede escribir", store.upserts)
			}
		})
	}
}

// TestTenantLLM_ElConsentimientoSeComprueba PRIMERO: un cuerpo que falla por DOS
// motivos a la vez (sin consentimiento Y con proveedor inválido) tiene que
// rechazarse por el consentimiento.
//
// No es tiquismiquis: si el proveedor se validara antes, el criterio «PUT sin
// consentimiento ⇒ 400» pasaría por casualidad en los cuerpos malformados y
// dejaría de estar probado el orden que design §8.1 fija.
//
// 🔬 MUTACIÓN: mover la comprobación de `provider` por delante de la de
// `Consented` en validateTenantLLM ⇒ el error sería `invalid_provider`.
func TestTenantLLM_ElConsentimientoSeCompruebaPrimero(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)
	cuerpo := putLLMBody(t, map[string]any{
		"provider": "inventado", "model": modeloDePrueba, "api_key": claveDePrueba, "consented": false,
	})
	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", cuerpo)
	exigeCódigo(t, rec, http.StatusBadRequest)
	exigeErrorJSON(t, rec.Body.Bytes(), "consent_required", "", "")
}

// ============================ El PUT que sí vale ============================

// TestTenantLLM_PutVálidoGuardaYResponde: el camino feliz. La respuesta trae la
// configuración releída y el store recibió la clave TAL CUAL (sin recortes ni
// normalizaciones que la cambiarían en silencio).
//
// 🔬 MUTACIÓN (a): que putTenantLLMHandler devuelva el request en vez de releer
// ⇒ `consented_at`/`created_at` saldrían vacíos.
// 🔬 MUTACIÓN (b): añadir `strings.TrimSpace(req.APIKey)` en decodeTenantLLM y
// mandar la clave con un espacio final ⇒ `store.últimaClave` dejaría de casar.
func TestTenantLLM_PutVálidoGuardaYResponde(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)
	antes := time.Now().UTC().Add(-time.Second)

	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, configuraciónLLMVálida()))
	exigeCódigo(t, rec, http.StatusOK)

	w := decodeLLMWire(t, rec.Body.Bytes())
	if !w.Configured || !w.KeySet {
		t.Fatalf("configured=%v key_set=%v, quiero los dos true", w.Configured, w.KeySet)
	}
	if w.Provider != tenantllm.ProviderAnthropic || w.Model != modeloDePrueba {
		t.Fatalf("provider=%q model=%q, quiero anthropic/%s", w.Provider, w.Model, modeloDePrueba)
	}
	if w.ConsentedAt == "" || w.CreatedAt == "" || w.UpdatedAt == "" {
		t.Fatalf("faltan timestamps en la respuesta: %+v (¿el handler dejó de releer?)", w)
	}
	if store.últimaClave != claveDePrueba {
		t.Fatalf("el store recibió una clave distinta de la enviada")
	}
	// El instante del consentimiento lo pone el SERVIDOR, no el cuerpo.
	if store.últimoConsent.Before(antes) {
		t.Fatalf("consented_at=%v es anterior al inicio del test: no lo puso el servidor", store.últimoConsent)
	}
}

// TestTenantLLM_PutSobreFilaExistenteReemplazaLaClave: la decisión que más fácil
// sería copiar mal del molde de /api/v1/integrations, donde `secret` vacío
// CONSERVA el secreto. Aquí NO: `api_key` es obligatoria en cada PUT, y un PUT
// que la trae la SUSTITUYE.
//
// 🔬 MUTACIÓN (a): hacer `api_key` opcional (quitar el mínimo de longitud del
// caso vacío) ⇒ el subtest "sin clave" devolvería 200.
// 🔬 MUTACIÓN (b): que el store conserve la clave anterior en el upsert ⇒
// `últimaClave` seguiría siendo la vieja tras el segundo PUT.
func TestTenantLLM_PutSobreFilaExistenteReemplazaLaClave(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)

	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, configuraciónLLMVálida()))
	exigeCódigo(t, rec, http.StatusOK)

	rotado := configuraciónLLMVálida()
	rotado["api_key"] = claveRotada
	rotado["model"] = "claude-opus-4-1"
	rec = call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, rotado))
	exigeCódigo(t, rec, http.StatusOK)

	if store.upserts != 2 {
		t.Fatalf("upserts=%d, quiero 2", store.upserts)
	}
	if store.últimaClave != claveRotada {
		t.Fatalf("la clave no se reemplazó: el segundo PUT dejó la anterior")
	}
	if w := decodeLLMWire(t, rec.Body.Bytes()); w.Model != "claude-opus-4-1" {
		t.Fatalf("model=%q, quiero claude-opus-4-1", w.Model)
	}
}

// TestTenantLLM_PutSinClave400: sin credencial no hay fila (0071). Se prueba el
// campo ausente y el vacío por separado porque en JSON son cosas distintas y en
// Go las dos llegan como "".
//
// 🔬 MUTACIÓN: bajar minLLMAPIKeyLen a 0 ⇒ los dos subtests darían 200.
func TestTenantLLM_PutSinClave400(t *testing.T) {
	casos := map[string]any{
		"ausente": nil,
		"vacía":   "",
		"corta":   "sk-ant-1",
	}
	for nombre, valor := range casos {
		t.Run(nombre, func(t *testing.T) {
			store := newFakeTenantLLM()
			api := nuevaAPILLM(store)
			cuerpo := configuraciónLLMVálida()
			if valor == nil {
				delete(cuerpo, "api_key")
			} else {
				cuerpo["api_key"] = valor
			}
			rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo))
			exigeCódigo(t, rec, http.StatusBadRequest)
			if store.upserts != 0 {
				t.Fatalf("upserts=%d, quiero 0", store.upserts)
			}
		})
	}
}

// ============================ El vocabulario de proveedor ============================

// TestTenantLLM_ProviderLocal422: `local` se ENTIENDE y no se puede (D-044.4,
// D-044.21). Es 422 y no 400, y el código de error es el mismo que design §8.1
// fija para /reanalyze — el mismo «no» se dice con la misma palabra.
//
// 🔬 MUTACIÓN: mover el `case tenantllm.ProviderLocal` al `default` de
// validateLLMProvider ⇒ saldría 400 `invalid_provider`.
func TestTenantLLM_ProviderLocal422(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)
	cuerpo := configuraciónLLMVálida()
	cuerpo["provider"] = tenantllm.ProviderLocal

	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo))
	exigeCódigo(t, rec, http.StatusUnprocessableEntity)
	exigeErrorJSON(t, rec.Body.Bytes(), "llm_provider_unavailable", "provider", tenantllm.ProviderLocal)
	if store.upserts != 0 {
		t.Fatalf("upserts=%d, quiero 0", store.upserts)
	}
}

// TestTenantLLM_ProviderDesconocido400: un valor que no está en el CHECK de la
// 0071 se rechaza AQUÍ y no en la BD — dejar que el CHECK valide convertiría un
// error del cliente en un 500 (integrations.go:26-27).
//
// 🔬 MUTACIÓN: quitar el `default` de validateLLMProvider y devolver (0, nil) ⇒
// el upsert llegaría al store (y en producción, al CHECK, con un 500).
func TestTenantLLM_ProviderDesconocido400(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)
	cuerpo := configuraciónLLMVálida()
	cuerpo["provider"] = "openai"

	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo))
	exigeCódigo(t, rec, http.StatusBadRequest)
	exigeErrorJSON(t, rec.Body.Bytes(), "invalid_provider", "provider", "openai")
	if store.upserts != 0 {
		t.Fatalf("upserts=%d, quiero 0", store.upserts)
	}
}

// TestTenantLLM_ProviderGeminiSeAcepta: el stub de T0.2 tiene que ser
// ALCANZABLE. Un proveedor que el CHECK admite y la API rechaza sería código
// muerto por decreto.
//
// 🔬 MUTACIÓN: sacar `ProviderGemini` del `case` de validateLLMProvider ⇒ 400.
func TestTenantLLM_ProviderGeminiSeAcepta(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)
	cuerpo := configuraciónLLMVálida()
	cuerpo["provider"] = tenantllm.ProviderGemini

	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo))
	exigeCódigo(t, rec, http.StatusOK)
	if w := decodeLLMWire(t, rec.Body.Bytes()); w.Provider != tenantllm.ProviderGemini {
		t.Fatalf("provider=%q, quiero gemini", w.Provider)
	}
}

// ============================ El GET ============================

// TestTenantLLM_GetSinFilaNoEs404: «no tengo vía API» es una respuesta, y es la
// que la pantalla necesita para dibujar el formulario vacío.
//
// 🔬 MUTACIÓN: devolver 404 cuando `!found` en getTenantLLMHandler.
func TestTenantLLM_GetSinFilaNoEs404(t *testing.T) {
	api := nuevaAPILLM(newFakeTenantLLM())
	rec := call(api, keyALLM, http.MethodGet, "/api/v1/tenant-llm", "")
	exigeCódigo(t, rec, http.StatusOK)
	w := decodeLLMWire(t, rec.Body.Bytes())
	if w.Configured || w.KeySet {
		t.Fatalf("configured=%v key_set=%v, quiero los dos false", w.Configured, w.KeySet)
	}
}

// TestTenantLLM_LaClaveNoSalePorNingunaPuerta es el criterio «el GET nunca
// devuelve la clave», llevado a las TRES puertas y no solo al GET: se configura
// una credencial y después se barre el cuerpo CRUDO de cada respuesta buscando
// la clave y su prefijo.
//
// Se barre el cuerpo crudo, y no un campo del DTO, a propósito: un campo nuevo
// que alguien añadiera al struct sin pensar quedaría cubierto por este test sin
// que nadie lo actualice. Es la diferencia entre probar lo que sabemos y probar
// lo que sale.
//
// 🔬 MUTACIÓN: añadir a tenantLLMDTO el campo APIKey string con tag json:"api_key" y
// rellenarlo ⇒ el barrido del GET y el del PUT se ponen rojos a la vez.
func TestTenantLLM_LaClaveNoSalePorNingunaPuerta(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)

	respuestas := map[string]string{}
	rec := call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, configuraciónLLMVálida()))
	exigeCódigo(t, rec, http.StatusOK)
	respuestas["PUT"] = rec.Body.String()

	rec = call(api, keyALLM, http.MethodGet, "/api/v1/tenant-llm", "")
	exigeCódigo(t, rec, http.StatusOK)
	respuestas["GET"] = rec.Body.String()

	rec = call(api, keyALLM, http.MethodDelete, "/api/v1/tenant-llm", "")
	exigeCódigo(t, rec, http.StatusNoContent)
	respuestas["DELETE"] = rec.Body.String()

	for puerta, cuerpo := range respuestas {
		if strings.Contains(cuerpo, claveDePrueba) {
			t.Fatalf("FUGA por %s: la respuesta contiene la API key entera", puerta)
		}
		// El prefijo, además de la clave entera: un truncado «de cortesía»
		// (`sk-ant-…3 últimos`) seguiría siendo una fuga y pasaría el test de
		// arriba.
		if strings.Contains(cuerpo, "sk-ant-") {
			t.Fatalf("FUGA por %s: la respuesta contiene el prefijo de la API key", puerta)
		}
	}
}

// ============================ El DELETE ============================

// TestTenantLLM_DeleteRevoca: el cuarto criterio literal. Revoca la credencial Y
// el consentimiento (viven en la misma fila), y es IDEMPOTENTE.
//
// 🔬 MUTACIÓN (a): que deleteTenantLLMHandler responda 404 cuando no había fila
// ⇒ el segundo DELETE se pone rojo.
// 🔬 MUTACIÓN (b): que Delete conserve la fila y solo vacíe la clave ⇒ el GET
// posterior devolvería `configured:true`.
func TestTenantLLM_DeleteRevoca(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)

	exigeCódigo(t, call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm",
		putLLMBody(t, configuraciónLLMVálida())), http.StatusOK)

	exigeCódigo(t, call(api, keyALLM, http.MethodDelete, "/api/v1/tenant-llm", ""), http.StatusNoContent)

	rec := call(api, keyALLM, http.MethodGet, "/api/v1/tenant-llm", "")
	exigeCódigo(t, rec, http.StatusOK)
	w := decodeLLMWire(t, rec.Body.Bytes())
	if w.Configured || w.KeySet || w.ConsentedAt != "" {
		t.Fatalf("tras el DELETE quedó rastro: %+v", w)
	}

	// Idempotente: borrar lo que no hay no es un error.
	exigeCódigo(t, call(api, keyALLM, http.MethodDelete, "/api/v1/tenant-llm", ""), http.StatusNoContent)
	if store.deletes != 2 {
		t.Fatalf("deletes=%d, quiero 2", store.deletes)
	}
}

// ============================ INV-7 ============================

// TestTenantLLM_INV7_ElTenantIDDelCuerpoSeIgnora: el criterio explícito de T0.3.
// Un cuerpo con el `tenant_id` de OTRO tenant no mueve nada de ese otro tenant:
// la operación va contra el del token, en silencio.
//
// Se comprueba por los DOS lados —lo que cambió y lo que NO— porque afirmar solo
// que A quedó configurado pasaría igual si además se hubiera pisado a B.
//
// 🔬 MUTACIÓN: añadir a tenantLLMRequest el campo TenantID string con tag json:"tenant_id" y
// usarlo en vez de `id.TenantID` en putTenantLLMHandler ⇒ A quedaría sin fila y
// B con una que no pidió.
func TestTenantLLM_INV7_ElTenantIDDelCuerpoSeIgnora(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)

	cuerpo := configuraciónLLMVálida()
	cuerpo["tenant_id"] = tenantB

	exigeCódigo(t, call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm", putLLMBody(t, cuerpo)), http.StatusOK)

	if _, ok := store.rows[tenantA]; !ok {
		t.Fatalf("el PUT del token de A no configuró a A")
	}
	if _, ok := store.rows[tenantB]; ok {
		t.Fatalf("FUGA DE TENANT: el tenant_id del cuerpo pisó la fila de B")
	}
}

// TestTenantLLM_INV7_CadaTenantVeLoSuyo: el GET de B no ve la configuración de A
// ni al revés. Es la otra mitad del aislamiento: la escritura ya está cubierta
// arriba, esto cubre la lectura.
//
// 🔬 MUTACIÓN: pasar una constante en vez de `id.TenantID` al `ts.Get` de
// getTenantLLMHandler.
func TestTenantLLM_INV7_CadaTenantVeLoSuyo(t *testing.T) {
	store := newFakeTenantLLM()
	api := nuevaAPILLM(store)

	exigeCódigo(t, call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm",
		putLLMBody(t, configuraciónLLMVálida())), http.StatusOK)

	rec := call(api, keyBLLM, http.MethodGet, "/api/v1/tenant-llm", "")
	exigeCódigo(t, rec, http.StatusOK)
	if w := decodeLLMWire(t, rec.Body.Bytes()); w.Configured {
		t.Fatalf("B ve la configuración de A: %+v", w)
	}
}

// ============================ El gate api_llm ============================

// TestTenantLLM_SinFeature403: las TRES rutas, incluida la lectura. Que el GET no
// devuelva la clave no lo hace inocuo: dice si el tenant tiene vía API y con qué
// proveedor, que es la información del add-on que el gate acota.
//
// 🔬 MUTACIÓN: quitar el envoltorio `apiLLM(...)` de cualquiera de las tres
// rutas en registerTenantLLM ⇒ ese subtest devolvería 200/204.
func TestTenantLLM_SinFeature403(t *testing.T) {
	store := newFakeTenantLLM()
	// Fake SIN la feature encendida para nadie: es el tenant que no compró el
	// add-on.
	api := newAPI(publicapi.Deps{TenantLLM: store, Entitlements: entitlements.NewFake()}, llmKeys())

	casos := []struct {
		método string
		cuerpo string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"provider":"anthropic","model":"m","api_key":"sk-ant-api03-xxxxxxxxxxxx","consented":true}`},
		{http.MethodDelete, ""},
	}
	for _, c := range casos {
		t.Run(c.método, func(t *testing.T) {
			rec := call(api, keyALLM, c.método, "/api/v1/tenant-llm", c.cuerpo)
			exigeCódigo(t, rec, http.StatusForbidden)
			exigeErrorJSON(t, rec.Body.Bytes(), "feature_not_enabled", "feature", entitlements.FeatureAPILLM)
		})
	}
	if store.upserts != 0 || store.deletes != 0 {
		t.Fatalf("el gate dejó pasar escrituras: upserts=%d deletes=%d", store.upserts, store.deletes)
	}
}

// TestTenantLLM_SinStoreNoSeMontanLasRutas: mejor un 404 de ruta inexistente que
// un endpoint de credenciales que responde 500 a medio camino.
//
// 🔬 MUTACIÓN: quitar la guarda `d.TenantLLM == nil` de registerTenantLLM ⇒ 500.
func TestTenantLLM_SinStoreNoSeMontanLasRutas(t *testing.T) {
	api := newAPI(publicapi.Deps{Entitlements: entitlements.NewFake()}, llmKeys())
	exigeCódigo(t, call(api, keyALLM, http.MethodGet, "/api/v1/tenant-llm", ""), http.StatusNotFound)
}

// TestTenantLLM_LecturaSinEscritura403: el scope `llm.read` no abre el PUT. El
// aislamiento por scope es del middleware, pero sin este test nadie afirma que
// las rutas pidan los scopes que dicen pedir.
//
// 🔬 MUTACIÓN: cambiar "llm.write" por "llm.read" en el PUT de registerTenantLLM.
func TestTenantLLM_LecturaSinEscritura403(t *testing.T) {
	api := nuevaAPILLM(newFakeTenantLLM())
	exigeCódigo(t, call(api, keyALLMRead, http.MethodGet, "/api/v1/tenant-llm", ""), http.StatusOK)
	exigeCódigo(t, call(api, keyALLMRead, http.MethodPut, "/api/v1/tenant-llm",
		putLLMBody(t, configuraciónLLMVálida())), http.StatusForbidden)
}

// ============================ Fallos de infraestructura ============================

// TestTenantLLM_GetConStoreCaído500: un fallo de la base es 500, no un
// `configured:false` — decir «no tienes vía API» cuando lo que pasa es que la
// base no responde haría que la pantalla ofreciera configurar algo ya
// configurado, y el siguiente PUT pisaría la credencial buena.
//
// 🔬 MUTACIÓN: tragarse el error en getTenantLLMHandler y caer al
// `notConfiguredTenantLLM()`.
func TestTenantLLM_GetConStoreCaído500(t *testing.T) {
	store := newFakeTenantLLM()
	store.failGet = errLLMStoreCaído
	api := nuevaAPILLM(store)
	exigeCódigo(t, call(api, keyALLM, http.MethodGet, "/api/v1/tenant-llm", ""), http.StatusInternalServerError)
}

// TestTenantLLM_CuerpoExcesivo413: el techo se aplica ANTES de deserializar.
//
// 🔬 MUTACIÓN: subir maxTenantLLMBytes o leer el cuerpo sin LimitReader.
func TestTenantLLM_CuerpoExcesivo413(t *testing.T) {
	api := nuevaAPILLM(newFakeTenantLLM())
	cuerpo := configuraciónLLMVálida()
	cuerpo["model"] = strings.Repeat("x", 1<<14)
	exigeCódigo(t, call(api, keyALLM, http.MethodPut, "/api/v1/tenant-llm",
		putLLMBody(t, cuerpo)), http.StatusRequestEntityTooLarge)
}
