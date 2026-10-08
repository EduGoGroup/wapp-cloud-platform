package integrations_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

// Aserciones de compilación de lo que gate.go promete: el constructor, la firma de Enabled y que
// el resolver de features es una interfaz de un solo método.
var (
	_ func(integrations.FeatureResolver, integrations.Store, string) *integrations.EntitlementsGate = integrations.NewEntitlementsGate
	_ func(*integrations.EntitlementsGate, context.Context, string) (bool, error)                   = (*integrations.EntitlementsGate).Enabled
	_ integrations.FeatureResolver                                                                  = (*fakeFeatures)(nil)
)

const (
	gateTenant  = "tenant-del-gate"
	gateFeature = "crm_bridge_de_prueba"
)

// fakeFeatures satisface FeatureResolver: contesta lo sembrado y apunta por quién y por qué
// feature le preguntaron.
type fakeFeatures struct {
	has   bool
	err   error
	asked [][2]string
}

func (f *fakeFeatures) Has(_ context.Context, tenantID, feature string) (bool, error) {
	f.asked = append(f.asked, [2]string{tenantID, feature})
	return f.has, f.err
}

// gateStore es el almacén del gate: el doble Memoria, contando las lecturas de la integración y
// pudiendo hacerlas fallar. El gate no usa nada más del puerto.
type gateStore struct {
	integrations.Store
	reads int
	err   error
}

func (s *gateStore) GetTenantIntegration(ctx context.Context, tenantID string) (integrations.TenantIntegration, bool, error) {
	s.reads++
	if s.err != nil {
		return integrations.TenantIntegration{}, false, s.err
	}
	return s.Store.GetTenantIntegration(ctx, tenantID)
}

// newGate monta el gate sobre un resolver que contesta has y un almacén con esa integración,
// guardada con secreto de firma (o sin ninguna integración, si cfg es nil).
func newGate(t *testing.T, has bool, cfg *integrations.TenantIntegration) (*integrations.EntitlementsGate, *fakeFeatures, *gateStore) {
	t.Helper()
	features := &fakeFeatures{has: has}
	store := &gateStore{Store: integrationshelpertest.NewMemoria()}
	if cfg != nil {
		if err := store.UpsertTenantIntegration(context.Background(), *cfg, gateSigningKey); err != nil {
			t.Fatalf("sembrar la integración: %v", err)
		}
	}
	return integrations.NewEntitlementsGate(features, store, gateFeature), features, store
}

// openConfig es la integración que abre el gate una vez guardada con secreto: encendida, con los
// eventos por webhook y con endpoint.
func openConfig() *integrations.TenantIntegration {
	return &integrations.TenantIntegration{
		TenantID: gateTenant, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://bridge.example/hook", Enabled: true,
	}
}

// TestNewEntitlementsGate_AsksNothing: construir el gate no pregunta ni al resolver ni al almacén.
func TestNewEntitlementsGate_AsksNothing(t *testing.T) {
	gate, features, store := newGate(t, true, openConfig())
	if gate == nil {
		t.Fatal("NewEntitlementsGate devolvió nil")
	}
	if len(features.asked) != 0 || store.reads != 0 {
		t.Errorf("construir el gate consultó (features=%d, almacén=%d), quería 0 y 0", len(features.asked), store.reads)
	}
}

// TestEnabled_ThreeConditionsAtOnce: el gate abre SOLO con las tres a la vez —la feature, la
// integración encendida y los eventos por webhook—; si falta cualquiera, cierra SIN error (un gate
// cerrado no es un fallo). El destino, la cuarta, la afirma gate_destination_test.go.
func TestEnabled_ThreeConditionsAtOnce(t *testing.T) {
	disabled, local, otherCase := openConfig(), openConfig(), openConfig()
	disabled.Enabled = false
	local.EventsAdapter = "local"
	otherCase.EventsAdapter = "Webhook"

	cases := []struct {
		name string
		has  bool
		cfg  *integrations.TenantIntegration
		want bool
	}{
		{"feature, enabled and webhook", true, openConfig(), true},
		{"no feature", false, openConfig(), false},
		{"no integration row", true, nil, false},
		{"integration switched off", true, disabled, false},
		{"events adapter is local", true, local, false},
		{"events adapter compared byte by byte", true, otherCase, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gate, _, _ := newGate(t, c.has, c.cfg)
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

// TestEnabled_AsksForTheConfiguredFeatureOfThatTenant: la pregunta al resolver lleva el tenant de
// la llamada y la clave que recibió el constructor, una sola vez.
func TestEnabled_AsksForTheConfiguredFeatureOfThatTenant(t *testing.T) {
	gate, features, _ := newGate(t, true, openConfig())
	if _, err := gate.Enabled(context.Background(), gateTenant); err != nil {
		t.Fatalf("Enabled: error inesperado %v", err)
	}
	if len(features.asked) != 1 || features.asked[0] != [2]string{gateTenant, gateFeature} {
		t.Errorf("preguntas al resolver = %v, quería una: (%s, %s)", features.asked, gateTenant, gateFeature)
	}
}

// TestEnabled_ReadsTheIntegrationOfThatTenant: la integración que cuenta es la del tenant de la
// llamada; la de otro tenant, por abierta que esté, no abre el gate de este.
func TestEnabled_ReadsTheIntegrationOfThatTenant(t *testing.T) {
	other := openConfig()
	other.TenantID = "otro-tenant"
	gate, _, _ := newGate(t, true, other)
	if got, err := gate.Enabled(context.Background(), gateTenant); err != nil || got {
		t.Errorf("Enabled = (%v, %v), quería (false, nil): la integración abierta es de otro tenant", got, err)
	}
	if got, err := gate.Enabled(context.Background(), "otro-tenant"); err != nil || !got {
		t.Errorf("Enabled del tenant con integración = (%v, %v), quería (true, nil)", got, err)
	}
}

// TestEnabled_WithoutFeature_DoesNotReadTheStore: primero la feature. Si el tenant no la tiene, o
// si preguntarla falla, el almacén no se consulta.
func TestEnabled_WithoutFeature_DoesNotReadTheStore(t *testing.T) {
	t.Run("no feature", func(t *testing.T) {
		gate, _, store := newGate(t, false, openConfig())
		if _, err := gate.Enabled(context.Background(), gateTenant); err != nil {
			t.Fatalf("Enabled: error inesperado %v", err)
		}
		if n := store.reads; n != 0 {
			t.Errorf("sin la feature se leyó la integración %d veces, quería 0", n)
		}
	})
	t.Run("feature lookup fails", func(t *testing.T) {
		gate, features, store := newGate(t, true, openConfig())
		features.err = errors.New("resolver caído")
		if _, err := gate.Enabled(context.Background(), gateTenant); err == nil {
			t.Fatal("Enabled no devolvió el error del resolver")
		}
		if n := store.reads; n != 0 {
			t.Errorf("con el resolver caído se leyó la integración %d veces, quería 0", n)
		}
	})
}

// TestEnabled_Errors_FailClosed: un error de infraestructura cierra el gate (false) y sale
// envuelto con su prefijo byte a byte: el del resolver nombra la feature y el tenant; el del
// almacén, el tenant.
func TestEnabled_Errors_FailClosed(t *testing.T) {
	boom := errors.New("base caída")
	cases := []struct {
		name     string
		sabotage func(*fakeFeatures, *gateStore)
		want     string
	}{
		{
			"feature resolver fails",
			func(f *fakeFeatures, _ *gateStore) { f.err = boom },
			"integrations: evaluar feature " + gateFeature + " de " + gateTenant + ": base caída",
		},
		{
			"store fails",
			func(_ *fakeFeatures, s *gateStore) { s.err = boom },
			"integrations: leer integración de " + gateTenant + ": base caída",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gate, features, store := newGate(t, true, openConfig())
			c.sabotage(features, store)
			got, err := gate.Enabled(context.Background(), gateTenant)
			if got {
				t.Error("Enabled = true con un error de infraestructura: tiene que cerrar")
			}
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, quería uno que envuelva la causa", err)
			}
			if err.Error() != c.want {
				t.Errorf("texto del error =\n%s\nquería, byte a byte:\n%s", err, c.want)
			}
			if strings.Contains(err.Error(), "bridge.example") {
				t.Errorf("el error cita el endpoint del tenant: %s", err)
			}
		})
	}
}
