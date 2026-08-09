// limits_test.go barre TODOS los 413 de la API pública y exige que cada uno
// nombre su cifra (Plan 041 · rojo A5, REQ-16).
//
// El rojo estuvo abierto porque los techos no son uno: cambiar el del import
// desalineaba a los otros. Este test es lo que impide que se vuelvan a
// desalinear — está escrito para que AÑADIR un techo nuevo sin su cifra sea un
// fallo, no un olvido silencioso.
//
// Los números se escriben AQUÍ a mano (8192, 65536, 262144…) en vez de leerse de
// la constante del paquete: son el contrato publicado, y un test que pregunte por
// el mismo símbolo que verifica se movería con él sin decir nada.
package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	sharedintents "github.com/EduGoGroup/wapp-shared/intents"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// techoWire es el cuerpo canónico del 413.
type techoWire struct {
	Error    string `json:"error"`
	MaxBytes int64  `json:"max_bytes"`
}

// exigeTechoNombrado es el criterio, en un sitio: 413, el número en `max_bytes` y
// el MISMO número escrito en la prosa (que es lo que REQ-16 pide literalmente:
// «mensaje que nombre el límite»).
func exigeTechoNombrado(t *testing.T, dónde string, rec *httptest.ResponseRecorder, quiero int64) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%s: code=%d, quiero 413; body=%s", dónde, rec.Code, rec.Body.String())
	}
	var got techoWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: unmarshal del 413: %v; body=%s", dónde, err, rec.Body.String())
	}
	if got.MaxBytes != quiero {
		t.Fatalf("%s: max_bytes=%d, quiero %d; body=%s", dónde, got.MaxBytes, quiero, rec.Body.String())
	}
	if !strings.Contains(got.Error, strconv.FormatInt(quiero, 10)) {
		t.Fatalf("%s: el mensaje NO nombra la cifra (%d): %q", dónde, quiero, got.Error)
	}
}

// exigeTechoNombradoEnvuelto es el mismo criterio para el ÚNICO endpoint que
// envuelve sus fallos en `validation_failed` + lista: el import tabular, que lo
// hace a propósito para que la pantalla que los pinta no tenga que saber por qué
// puerta entró el documento (catalogtabular.go). Se respeta el envoltorio y se
// exige la misma frase con su cifra dentro.
func exigeTechoNombradoEnvuelto(t *testing.T, dónde string, rec *httptest.ResponseRecorder, quiero int64) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%s: code=%d, quiero 413; body=%s", dónde, rec.Code, rec.Body.String())
	}
	var got struct {
		Error  string `json:"error"`
		Errors []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: unmarshal del 413: %v; body=%s", dónde, err, rec.Body.String())
	}
	if got.Error != "validation_failed" || len(got.Errors) != 1 {
		t.Fatalf("%s: el envoltorio del import cambió: %s", dónde, rec.Body.String())
	}
	if !strings.Contains(got.Errors[0].Reason, strconv.FormatInt(quiero, 10)) {
		t.Fatalf("%s: el motivo NO nombra la cifra (%d): %q", dónde, quiero, got.Errors[0].Reason)
	}
}

// contenidoDe fabrica un JSON de exactamente n bytes (mismo truco que blobDe, con
// su propia envoltura para no depender de aquel).
func contenidoDe(t *testing.T, n int) string {
	t.Helper()
	const envoltura = `{"a":""}`
	if n < len(envoltura) {
		t.Fatalf("no se puede fabricar un cuerpo de %d bytes: el mínimo es %d", n, len(envoltura))
	}
	return `{"a":"` + strings.Repeat("x", n-len(envoltura)) + `"}`
}

// TestTechos_TodosNombranSuCifra recorre las SEIS puertas con techo de cuerpo de
// la API autenticada. Cada una con SU número, que es justo lo que hacía imposible
// arreglar una sola: no comparten cifra, comparten criterio.
func TestTechos_TodosNombranSuCifra(t *testing.T) {
	t.Run("PUT /api/v1/tenant-content/{ref}", func(t *testing.T) {
		repo := flowstore.NewMemoryRepository()
		api := newAPI(publicapi.Deps{MediaDeps: publicapi.MediaDeps{Content: repo, ContentMaxBytes: 64}}, apiKeys())
		rec := call(api, keyAContent, http.MethodPut, "/api/v1/tenant-content/x", contenidoDe(t, 65))
		exigeTechoNombrado(t, "tenant-content", rec, 64)
	})

	t.Run("POST /api/v1/catalog/import", func(t *testing.T) {
		api := importAPIConTecho(64)
		rec := call(api, keyAContent, http.MethodPost, "/api/v1/catalog/import?mode=validate", contenidoDe(t, 65))
		exigeTechoNombrado(t, "catalog/import", rec, 64)
	})

	t.Run("PUT /api/v1/tenant-variables", func(t *testing.T) {
		api := newAPI(publicapi.Deps{TenantVariables: &fakeTenantVars{}}, apiKeys())
		const techo = 1 << 18 // 256 KiB
		rec := call(api, keyAContent, http.MethodPut, "/api/v1/tenant-variables", contenidoDe(t, techo+1))
		exigeTechoNombrado(t, "tenant-variables", rec, techo)
	})

	t.Run("PUT /api/v1/intents", func(t *testing.T) {
		ents := entitlements.NewFake()
		ents.Enable(tenantA, entitlements.FeatureLLMIntent)
		api := newAPI(publicapi.Deps{Intents: intentcfg.NewMemoryStore(), Entitlements: ents}, techosKeys())
		rec := call(api, keyATechos, http.MethodPut, "/api/v1/intents", contenidoDe(t, sharedintents.MaxConfigBytes+1))
		exigeTechoNombrado(t, "intents", rec, int64(sharedintents.MaxConfigBytes))
	})

	t.Run("PUT /api/v1/integrations", func(t *testing.T) {
		api := integrAPI(newFakeIntegrations())
		const techo = 1 << 13 // 8 KiB
		rec := call(api, keyAIntegr, http.MethodPut, "/api/v1/integrations", contenidoDe(t, techo+1))
		exigeTechoNombrado(t, "integrations", rec, techo)
	})

	t.Run("POST /api/v1/integrations/callback", func(t *testing.T) {
		api := newAPI(publicapi.Deps{
			CRMSecrets: fakeSecretoCRM{},
			CRMGate:    fakeGateCRM{},
			CRMReflect: fakeReflectorCRM{},
		}, apiKeys())
		const techo = 64 << 10
		req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/callback",
			strings.NewReader(contenidoDe(t, techo+1)))
		req.Header.Set("X-Wapp-Tenant", tenantA)
		req.Header.Set("X-Wapp-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
		req.Header.Set("X-Wapp-Signature", "v1=da-igual: el techo corta antes de verificar")
		rec := httptest.NewRecorder()
		api.mux.ServeHTTP(rec, req)
		exigeTechoNombrado(t, "callback", rec, techo)
	})
}

// TestTechos_ElImportTabularNombraLaCifraEnSuEnvoltorio cubre los DOS techos de la
// planilla: el del sobre multipart y el del archivo. Los dos nombran el techo del
// ARCHIVO —no el del sobre, que lleva 8 KiB de holgura para las cabeceras—, porque
// el número que le sirve a quien sube es el que su archivo tiene que respetar.
func TestTechos_ElImportTabularNombraLaCifraEnSuEnvoltorio(t *testing.T) {
	const techo = 64

	t.Run("el archivo pasa el sobre y lo corta ReadLimited", func(t *testing.T) {
		api := importAPIConTecho(techo)
		rec := subirPlanilla(t, api, keyAContent, "?mode=validate", "catalogo.csv",
			[]byte(strings.Repeat("x", techo+1)))
		exigeTechoNombradoEnvuelto(t, "import tabular (archivo)", rec, techo)
	})

	t.Run("el sobre entero revienta MaxBytesReader", func(t *testing.T) {
		api := importAPIConTecho(techo)
		// techo + holgura del sobre (8 KiB) + margen: corta el PRIMER techo.
		rec := subirPlanilla(t, api, keyAContent, "?mode=validate", "catalogo.csv",
			[]byte(strings.Repeat("x", techo+(8<<10)+1024)))
		exigeTechoNombradoEnvuelto(t, "import tabular (sobre)", rec, techo)
	})
}

// importAPIConTecho arma la API del import con un techo de bytes a medida (el
// MISMO número gobierna el import JSON, el tabular y el PUT genérico: es lo que
// impide que un blob importable sea rechazado después por la otra puerta).
func importAPIConTecho(maxBytes int64) *testAPI {
	repo := flowstore.NewMemoryRepository()
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureCatalogImport)
	return newAPI(publicapi.Deps{
		MediaDeps:    publicapi.MediaDeps{Content: repo, ContentVersions: repo, ContentMaxBytes: maxBytes},
		Entitlements: feats,
	}, apiKeys())
}

// keyATechos porta los grants que este barrido necesita y que keyAContent no
// tiene (intents.write).
const keyATechos = "key-a-techos"

func techosKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyATechos] = testIdentity{TenantID: tenantA, Subject: "techos-a",
		Grants: []string{"intents.read", "intents.write"}}
	return keys
}

// --- Dobles mínimos: solo existen para que las rutas se monten ---

type fakeTenantVars struct{}

func (fakeTenantVars) List(context.Context, string) ([]tenantvars.Variable, error) { return nil, nil }
func (fakeTenantVars) Replace(context.Context, string, map[string]string) error    { return nil }

type fakeSecretoCRM struct{}

func (fakeSecretoCRM) GetTenantSecret(context.Context, string) (string, bool, error) {
	return "no-se-llega-hasta-aqui", true, nil
}

type fakeGateCRM struct{}

func (fakeGateCRM) Enabled(context.Context, string) (bool, error) { return true, nil }

type fakeReflectorCRM struct{}

func (fakeReflectorCRM) ReflectCRMStatus(context.Context, string, string, string, string,
	time.Time) (intakes.CRMReflection, error) {
	return intakes.CRMReflection{}, nil
}
