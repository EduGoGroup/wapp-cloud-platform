package publicapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// errResolver simula un fallo de infraestructura del resolver de entitlements.
var errResolver = errors.New("entitlements: BD caída")

// keyAEnts: tenantA con el scope de lectura de derechos (Plan 040 · T2.1).
const keyAEnts = "key-a-entitlements"

// entsKeys extiende apiKeys() con la credencial que porta entitlements.read.
func entsKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyAEnts] = testIdentity{TenantID: tenantA, Subject: "consola-a", Grants: []string{"entitlements.read"}}
	return keys
}

// entsResponse espeja el contrato de GET /api/v1/entitlements (design §3).
type entsResponse struct {
	Plan            string   `json:"plan"`
	Features        []string `json:"features"`
	CacheTTLSeconds int      `json:"cache_ttl_seconds"`
}

// advisorAIFeatures es la composición del paquete `advisor_ai` (migración 0039),
// declarada aquí DESORDENADA a propósito: el contrato promete orden alfabético y
// el test lo afirma sobre la respuesta, no sobre el fixture.
var advisorAIFeatures = []string{"llm_intent", "menu", "crm_bridge", "cart_basic", "catalog_import", "intakes_export"}

// entsDeps arma unas Deps con el Fake del resolver poblado por el llamante.
func entsDeps(fake *entitlements.Fake) publicapi.Deps {
	return publicapi.Deps{Entitlements: fake}
}

func decodeEnts(t *testing.T, body []byte) entsResponse {
	t.Helper()
	var resp entsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal de la respuesta: %v; body=%s", err, body)
	}
	return resp
}

// TestEntitlements_200_FeaturesDelPlan: un tenant con plan y sus features
// responde el contrato completo — plan, features en orden alfabético y el TTL
// real del resolver (60 s), que es lo que el cliente usa para saber cuánto tarda
// en propagarse un cambio.
func TestEntitlements_200_FeaturesDelPlan(t *testing.T) {
	fake := entitlements.NewFake()
	fake.SetPlan(tenantA, "advisor_ai")
	for _, f := range advisorAIFeatures {
		fake.Enable(tenantA, f)
	}
	mux := newAPI(entsDeps(fake), entsKeys())

	rec := call(mux, keyAEnts, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeEnts(t, rec.Body.Bytes())
	if resp.Plan != "advisor_ai" {
		t.Fatalf("plan=%q, quiero advisor_ai", resp.Plan)
	}
	want := slices.Clone(advisorAIFeatures)
	slices.Sort(want)
	if !slices.Equal(resp.Features, want) {
		t.Fatalf("features=%v, quiero %v (alfabético)", resp.Features, want)
	}
	if resp.CacheTTLSeconds != 60 {
		t.Fatalf("cache_ttl_seconds=%d, quiero 60 (TTL real del resolver)", resp.CacheTTLSeconds)
	}
}

// TestEntitlements_200_ConOverride: el override manda en los dos sentidos —
// enciende una feature fuera del plan y apaga una que el plan trae. Lo que la
// respuesta lista es el resultado EFECTIVO, no la composición del plan.
func TestEntitlements_200_ConOverride(t *testing.T) {
	fake := entitlements.NewFake()
	fake.SetPlan(tenantA, "commerce")
	for _, f := range []string{"cart_basic", "catalog_import", "crm_bridge", "intakes_export", "menu"} {
		fake.Enable(tenantA, f)
	}
	fake.Enable(tenantA, "stt_audio") // override que ENCIENDE (fuera del plan)
	fake.Disable(tenantA, "menu")     // override que APAGA (el plan sí la trae)
	mux := newAPI(entsDeps(fake), entsKeys())

	rec := call(mux, keyAEnts, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeEnts(t, rec.Body.Bytes())
	want := []string{"cart_basic", "catalog_import", "crm_bridge", "intakes_export", "stt_audio"}
	if !slices.Equal(resp.Features, want) {
		t.Fatalf("features=%v, quiero %v (override on suma, override off excluye)", resp.Features, want)
	}
	if slices.Contains(resp.Features, "menu") {
		t.Fatal("una feature con override enabled=false NO debe aparecer en la lista")
	}
}

// TestEntitlements_200_SinFeatures: un tenant sin derechos responde [] y no null
// (la UI itera sin ramificar por el nulo).
func TestEntitlements_200_SinFeatures(t *testing.T) {
	mux := newAPI(entsDeps(entitlements.NewFake()), entsKeys())

	rec := call(mux, keyAEnts, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if got := string(raw["features"]); got != "[]" {
		t.Fatalf("features=%s, quiero [] (nunca null)", got)
	}
}

// TestEntitlements_403_SinScope: token VÁLIDO pero sin entitlements.read ⇒ 403,
// y el cuerpo no filtra qué claves existen (design §3).
func TestEntitlements_403_SinScope(t *testing.T) {
	fake := entitlements.NewFake()
	fake.SetPlan(tenantA, "advisor_ai")
	fake.Enable(tenantA, entitlements.FeatureLLMIntent)
	mux := newAPI(entsDeps(fake), entsKeys())

	// keyARead solo tiene flows.read: no cubre entitlements.read.
	rec := call(mux, keyARead, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "llm_intent") || strings.Contains(body, "advisor_ai") {
		t.Fatalf("el 403 no debe filtrar claves ni plan existentes: %s", body)
	}
}

// TestEntitlements_401_SinTokenOInvalido: sin Authorization y con un Bearer que
// no es un token firmado, ambos 401 (nunca 200 con lista vacía).
func TestEntitlements_401_SinTokenOInvalido(t *testing.T) {
	mux := newAPI(entsDeps(entitlements.NewFake()), entsKeys())

	if rec := call(mux, "", http.MethodGet, "/api/v1/entitlements", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin token: code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
	// Credencial desconocida ⇒ el harness manda una cadena que NO es un token.
	if rec := call(mux, "credencial-inventada", http.MethodGet, "/api/v1/entitlements", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token inválido: code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEntitlements_500_ResolverCaido: un fallo de infraestructura del resolver es
// un 500 explícito — la lectura NO puede mentir con una lista vacía, que la UI
// leería como "este tenant no tiene nada contratado".
func TestEntitlements_500_ResolverCaido(t *testing.T) {
	fake := entitlements.NewFake()
	fake.Err = errResolver
	mux := newAPI(entsDeps(fake), entsKeys())

	rec := call(mux, keyAEnts, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEntitlements_SinResolver_RutaNoMontada: sin Deps.Entitlements la ruta no
// existe (mismo criterio que /api/v1/intents), no responde un 200 vacío.
func TestEntitlements_SinResolver_RutaNoMontada(t *testing.T) {
	mux := newAPI(publicapi.Deps{}, entsKeys())

	rec := call(mux, keyAEnts, http.MethodGet, "/api/v1/entitlements", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (ruta no montada); body=%s", rec.Code, rec.Body.String())
	}
}
