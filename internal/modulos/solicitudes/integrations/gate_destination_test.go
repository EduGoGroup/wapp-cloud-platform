package integrations_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

// gateSigningKey es un secreto de firma cualquiera, de prueba: al gate solo le importa que lo haya.
const gateSigningKey = "clave-de-firma-de-prueba-0123456789"

// newGateWithSecret monta el gate sobre un tenant con la feature y esa integración, guardada con
// ese secreto ("" es no tener ninguno).
func newGateWithSecret(t *testing.T, cfg *integrations.TenantIntegration, secret string) *integrations.EntitlementsGate {
	t.Helper()
	store := integrationshelpertest.NewMemoria()
	if err := store.UpsertTenantIntegration(context.Background(), *cfg, secret); err != nil {
		t.Fatalf("sembrar la integración: %v", err)
	}
	return integrations.NewEntitlementsGate(&fakeFeatures{has: true}, store, gateFeature)
}

// TestEnabled_RequiresADestination: una integración encendida y por webhook NO abre el gate si le
// falta el endpoint o el secreto de firma —lo mismo que el worker exige para entregar—, y cerrar
// por eso tampoco es un error. Con los dos, abre.
func TestEnabled_RequiresADestination(t *testing.T) {
	noEndpoint := openConfig()
	noEndpoint.EndpointURL = ""

	cases := []struct {
		name   string
		cfg    *integrations.TenantIntegration
		secret string
		want   bool
	}{
		{"endpoint and secret", openConfig(), gateSigningKey, true},
		{"no endpoint", noEndpoint, gateSigningKey, false},
		{"no secret", openConfig(), "", false},
		{"neither endpoint nor secret", noEndpoint, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gate := newGateWithSecret(t, c.cfg, c.secret)
			got, err := gate.Enabled(context.Background(), gateTenant)
			if err != nil {
				t.Fatalf("Enabled: error inesperado %v (un gate cerrado no es un error)", err)
			}
			if got != c.want {
				t.Errorf("Enabled = %v, quería %v", got, c.want)
			}
		})
	}
}
