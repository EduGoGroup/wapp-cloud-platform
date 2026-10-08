package apipublica_test

// crmcallback_gate_test.go — el GATE de G17 del contrato de MountCRMCallback: va después de la
// firma, es fail-closed y, con el gate real del módulo, exige el destino (D-F6-11). Es un trozo
// de crmcallback_test.go, partido por tema (05 E-13).

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

// TestMountCRMCallback_Gate: el gate va DESPUÉS de la firma, es fail-closed y corta antes de
// mirar el cuerpo.
func TestMountCRMCallback_Gate(t *testing.T) {
	t.Run("closed_is_403", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.gate.enabled = false
		// El cuerpo es inválido a propósito: el 403 gana al 422.
		rec := crmSend(rig.cara, crmGood(`{"tenant":"acme"}`))
		wantCode(t, "gate cerrado", rec, http.StatusForbidden)
		wantExactBody(t, "gate cerrado", rec, crmGateClosed)
		if len(rig.reflector.calls) != 0 {
			t.Errorf("con el gate cerrado se reflejó %d veces", len(rig.reflector.calls))
		}
	})
	t.Run("error_fails_closed_and_warns", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.gate.err = errors.New("bd caída")
		rig.gate.enabled = true // aunque dijera true: con error no se abre
		rec := crmSend(rig.cara, crmGood(crmBody("paid")))
		wantCode(t, "gate con error", rec, http.StatusForbidden)
		wantExactBody(t, "gate con error", rec, crmGateClosed)
		entry, ok := crmLogged(rig.h, "warn", "callback CRM: no se pudo resolver el puente del tenant")
		if !ok || entry.Fields["tenant"] != tenantA || entry.Fields["error"] == nil {
			t.Errorf("el fallo del gate no dejó su Warn con tenant y error: %+v", rig.h.Log().Entries())
		}
	})
	t.Run("bad_signature_never_reaches_the_gate", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.gate.enabled = false
		rec := crmSend(rig.cara, crmSigned(tenantA, crmOtherSigner, crmBody("paid"), crmNow.Unix()))
		wantCode(t, "firma mala con el gate cerrado", rec, http.StatusUnauthorized)
		if len(rig.gate.tenants) != 0 {
			t.Errorf("el gate se consultó %d veces sin firma buena: delataría quién tiene puente", len(rig.gate.tenants))
		}
	})
}

// TestMountCRMCallback_WithTheModuleGate: con el gate REAL del módulo y su doble de almacén. Un
// puente completo pasa; sin la feature, apagado o —D-F6-11, la divergencia aceptada con el
// viejo— encendido pero sin endpoint, 403 aunque la firma sea buena.
func TestMountCRMCallback_WithTheModuleGate(t *testing.T) {
	cases := []struct {
		name    string
		feature bool
		row     integrations.TenantIntegration
		want    int
	}{
		{"live_bridge", true, integrationLive(tenantA), http.StatusOK},
		{"no_feature", false, integrationLive(tenantA), http.StatusForbidden},
		{"disabled", true, integrations.TenantIntegration{TenantID: tenantA, EventsAdapter: "webhook", EndpointURL: integrationEndpoint}, http.StatusForbidden},
		{"local_events", true, integrations.TenantIntegration{TenantID: tenantA, EventsAdapter: "local", EndpointURL: integrationEndpoint, Enabled: true}, http.StatusForbidden},
		{"enabled_without_endpoint_D_F6_11", true, integrations.TenantIntegration{TenantID: tenantA, EventsAdapter: "webhook", Enabled: true}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := integrationshelpertest.NewMemoria()
			if err := store.UpsertTenantIntegration(context.Background(), tc.row, crmFakeSigner); err != nil {
				t.Fatalf("sembrando la integración: %v", err)
			}
			features := withFeatures()
			if tc.feature {
				features = withFeatures(entitlements.FeatureCRMBridge)
			}
			cara := apipublica.Nueva()
			apipublica.MountCRMCallback(cara, h.Common(), apipublica.CRMCallbackDeps{
				CRMSecrets: store,
				CRMGate:    integrations.NewEntitlementsGate(features, store, entitlements.FeatureCRMBridge),
				CRMReflect: &crmReflectorFake{anyIntake: true},
				Now:        func() time.Time { return crmNow },
			})
			rec := crmSend(cara, crmGood(crmBody("paid")))
			wantCode(t, tc.name, rec, tc.want)
			if tc.want == http.StatusForbidden {
				wantExactBody(t, tc.name, rec, crmGateClosed)
			}
		})
	}
}
