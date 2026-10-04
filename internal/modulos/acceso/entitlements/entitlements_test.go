package entitlements

import (
	"context"
	"testing"
	"time"
)

// Las firmas del puerto: si cambian, cambian todos sus consumidores (el middleware, el endpoint
// de entitlements, los checks in-code). Las expresiones de método sobre la interfaz fijan cada
// firma tal cual.
var (
	_ func(Resolver, context.Context, string, string) (bool, error)     = Resolver.Has
	_ func(Resolver, context.Context, string) (string, []string, error) = Resolver.ListEffective
	_ func(Resolver) time.Duration                                      = Resolver.CacheTTL
)

// allFeatures son las once claves con el valor literal que tienen en BD y en el cable.
var allFeatures = []struct {
	name string
	got  string
	want string
}{
	{"FeatureLLMIntent", FeatureLLMIntent, "llm_intent"},
	{"FeatureCartBasic", FeatureCartBasic, "cart_basic"},
	{"FeatureIntakesExport", FeatureIntakesExport, "intakes_export"},
	{"FeatureCatalogImport", FeatureCatalogImport, "catalog_import"},
	{"FeatureCRMBridge", FeatureCRMBridge, "crm_bridge"},
	{"FeatureMenu", FeatureMenu, "menu"},
	{"FeatureSurvey", FeatureSurvey, "survey"},
	{"FeatureMedia", FeatureMedia, "media"},
	{"FeatureLLMIntake", FeatureLLMIntake, "llm_intake"},
	{"FeatureAPILLM", FeatureAPILLM, "api_llm"},
	{"FeatureMultiCompany", FeatureMultiCompany, "multi_empresa"},
}

// TestFeatureKeys_LiteralValues: cada constante vale, byte a byte, la clave sembrada en
// plan_features/tenant_features. Un valor distinto no rompe la compilación: deja la ruta sin gate
// (o cerrada para todos) en silencio.
func TestFeatureKeys_LiteralValues(t *testing.T) {
	if len(allFeatures) != 11 {
		t.Fatalf("la tabla tiene %d claves y el paquete declara 11", len(allFeatures))
	}
	for _, tc := range allFeatures {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, quería %q (es la clave literal de la BD)", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestFeatureKeys_Distinct: dos constantes con la misma clave fundirían dos capacidades
// comerciales en una (p. ej. ver la bandeja y exportarla, o la vía API y el nivel LLM: R-E5 exige
// que api_llm y llm_intake sean derechos distintos).
func TestFeatureKeys_Distinct(t *testing.T) {
	seen := make(map[string]string, len(allFeatures))
	for _, tc := range allFeatures {
		if prev, ok := seen[tc.got]; ok {
			t.Errorf("%s y %s comparten la clave %q", prev, tc.name, tc.got)
		}
		seen[tc.got] = tc.name
	}
}
