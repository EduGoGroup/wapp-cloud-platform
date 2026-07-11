package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// keyAIntents: tenantA con intents.read + intents.write.
const keyAIntents = "key-a-intents"

// validIntentsJSON es un contrato de intenciones válido para wapp-shared/intents.
const validIntentsJSON = `{"version":"v1","umbral_confianza":0.7,"intents":[{"name":"pedir_pizza","descripcion":"pedir comida","params":["cantidad"],"ejemplos":[{"mensaje":"quiero una pizza"}]}]}`

// intentsKeys extiende apiKeys() con la credencial de administración de intents.
func intentsKeys() map[string]in.ServiceIdentity {
	keys := apiKeys()
	keys[keyAIntents] = in.ServiceIdentity{TenantID: tenantA, ClientID: "admin-a", Scopes: []string{"intents.read", "intents.write"}}
	return keys
}

// fakePusher captura la invocación de PushConfig (ADR-0021) del PUT.
type fakePusher struct {
	called          bool
	tenant, version string
	kind            string
	payload         []byte
}

func (f *fakePusher) PushConfig(_ context.Context, tenantID, kind, version string, payload []byte) error {
	f.called = true
	f.tenant, f.kind, f.version = tenantID, kind, version
	f.payload = append([]byte(nil), payload...)
	return nil
}

// intentsDeps arma las Deps con la feature llm_intent habilitada para tenantA.
func intentsDeps(pusher publicapi.ConfigPusher) publicapi.Deps {
	ents := entitlements.NewFake()
	ents.Enable(tenantA, entitlements.FeatureLLMIntent)
	return publicapi.Deps{
		Intents:      intentcfg.NewMemoryStore(),
		Entitlements: ents,
		ConfigPush:   pusher,
	}
}

func TestIntentsPut_OK_PersisteYEmpuja(t *testing.T) {
	pusher := &fakePusher{}
	d := intentsDeps(pusher)
	mux := newAPI(d, intentsKeys())

	rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", validIntentsJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Version == "" {
		t.Fatal("el PUT debe devolver la version de entidad")
	}
	// Persistido bajo tenantA y empujado con el mismo kind/version.
	got, err := d.Intents.Get(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("no se persistió bajo tenantA: %v", err)
	}
	if got.Version != resp.Version {
		t.Fatalf("version persistida=%q != respuesta=%q", got.Version, resp.Version)
	}
	if !pusher.called || pusher.tenant != tenantA || pusher.kind != intentcfg.Kind || pusher.version != resp.Version {
		t.Fatalf("PushConfig no invocado correctamente: %+v", pusher)
	}
}

func TestIntentsPut_403_SinFeature(t *testing.T) {
	pusher := &fakePusher{}
	// Sin habilitar la feature: entitlements vacío ⇒ Has=false ⇒ 403.
	d := publicapi.Deps{
		Intents:      intentcfg.NewMemoryStore(),
		Entitlements: entitlements.NewFake(),
		ConfigPush:   pusher,
	}
	mux := newAPI(d, intentsKeys())

	rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", validIntentsJSON)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 (sin feature); body=%s", rec.Code, rec.Body.String())
	}
	if pusher.called {
		t.Fatal("no debió empujar config sin la feature")
	}
}

func TestIntentsPut_403_SinScope(t *testing.T) {
	d := intentsDeps(&fakePusher{})
	mux := newAPI(d, intentsKeys())

	// keyARead (flows.read) no cubre intents.write ⇒ 403 en el middleware.
	rec := call(mux, keyARead, http.MethodPut, "/api/v1/intents", validIntentsJSON)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 (sin scope); body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntentsPut_400_ConfigInvalida(t *testing.T) {
	d := intentsDeps(&fakePusher{})
	mux := newAPI(d, intentsKeys())

	// Sin intents ⇒ ParseAndValidate falla ⇒ 400.
	rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", `{"version":"v1","intents":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400 (config inválida); body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntentsGet_404_Y_200TrasPut(t *testing.T) {
	d := intentsDeps(&fakePusher{})
	mux := newAPI(d, intentsKeys())

	// Sin config ⇒ 404.
	if rec := call(mux, keyAIntents, http.MethodGet, "/api/v1/intents", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET sin config code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	// Tras PUT ⇒ 200 con {version, config}.
	if rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", validIntentsJSON); rec.Code != http.StatusOK {
		t.Fatalf("PUT code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	rec := call(mux, keyAIntents, http.MethodGet, "/api/v1/intents", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tras PUT code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Version string          `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Version == "" || len(resp.Config) == 0 {
		t.Fatalf("respuesta incompleta: %+v", resp)
	}
}

func TestIntentsGet_401_SinToken(t *testing.T) {
	d := intentsDeps(&fakePusher{})
	mux := newAPI(d, intentsKeys())

	if rec := call(mux, "", http.MethodGet, "/api/v1/intents", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}
