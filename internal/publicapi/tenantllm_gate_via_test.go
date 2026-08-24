// tenantllm_gate_via_test.go — Plan 044 · Ola 1.5 · T1.5-1 (D-044.28, ADR-0044).
//
// LA OTRA MITAD DE T1.5-1. El resto de la tarea afloja gates: la capacidad
// (`llm_intake`) deja de depender de la vía. Este fichero afirma lo que NO se
// afloja — que `api_llm` sigue cerrando la puerta de la VÍA API — y lo afirma en el
// único escenario que antes no se podía construir: un tenant que SÍ tiene la
// capacidad.
//
// 🔴 POR QUÉ NO BASTABA TestTenantLLM_SinFeature403 (tenantllm_test.go). Aquel monta
// un Fake VACÍO: el tenant no tiene NADA. Con eso, un gate mal recableado a
// `llm_intake` seguiría dando 403 y el test seguiría verde — estaría probando «un
// tenant sin features no pasa», que es más débil que lo que hace falta. Aquí el
// tenant tiene `llm_intake` ENCENDIDA, así que el 403 solo puede venir de `api_llm`.
//
// ⏳ NO EJECUTADO (entorno sin Go). Lleva su mutación, y la mutación compila.
package publicapi_test

import (
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// TestT151_LaCapacidadNoAbreLaVia_403EnLasTresRutas: tener `llm_intake` no da
// derecho a configurar credenciales de un proveedor externo. Son dos derechos
// distintos y esta es la línea que los separa (ADR-0044): el nivel es lo que el
// tenant paga; la vía API es una configuración DENTRO del nivel, con su propio gate
// porque mueve dinero a una cuenta de pago.
//
// Las TRES rutas, incluida la lectura, por el mismo motivo que el test hermano: el
// GET no devuelve la clave pero sí dice si hay vía API y con qué proveedor.
//
// MUTACIÓN (compila, y es exactamente el exceso de celo que T1.5-1 podría provocar
// —«ya que suelto la capacidad, unifico los dos gates»—): en publicapi.go, dentro de
// registerTenantLLM, sustituir
//
//	apiLLM := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureAPILLM)
//
// por
//
//	apiLLM := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureLLMIntake)
//
// Los tres subtests devolverían 200/204 y este test se pone rojo, mientras
// TestTenantLLM_SinFeature403 seguiría VERDE — que es justo por lo que este fichero
// existe.
func TestT151_LaCapacidadNoAbreLaVia_403EnLasTresRutas(t *testing.T) {
	store := newFakeTenantLLM()

	// El tenant de vía local: capacidad SÍ, vía API NO. El Disable es explícito
	// aunque el Fake ya responda false a lo ausente — así la premisa se lee.
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)
	feats.Disable(tenantA, entitlements.FeatureAPILLM)
	api := newAPI(publicapi.Deps{TenantLLM: store, Entitlements: feats}, llmKeys())

	casos := []struct {
		método string
		cuerpo string
	}{
		{http.MethodGet, ""},
		// 🔧 Con `via` (T1.5-2) aunque el gate dispare antes de validar el cuerpo:
		// si el gate se cayera, un cuerpo VÁLIDO devuelve 200 —el fallo que este
		// test quiere ver— y uno sin vía devolvería 400 `invalid_via`, que dejaría
		// el test rojo por un motivo que no es el que vigila.
		{http.MethodPut, `{"via":"api","provider":"anthropic","model":"m","api_key":"sk-ant-api03-xxxxxxxxxxxx","consented":true}`},
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
		t.Fatalf("tener la capacidad dejó pasar escrituras de la VÍA: upserts=%d deletes=%d",
			store.upserts, store.deletes)
	}
}
