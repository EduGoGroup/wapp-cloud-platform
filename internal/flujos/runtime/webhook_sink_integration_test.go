// webhook_sink_integration_test.go es el criterio literal de T3.2: contra
// Postgres real (no fakes), un cart_closed de un tenant CON feature+integración
// habilitadas crea una fila pending en webhook_outbox; el mismo efecto de un
// tenant SIN feature o sin integración habilitada no crea nada.
package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

func webhookCipherDePrueba(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: "test-kek-1:ERERERERERERERERERERERERERERERERERERERERERE=",
		CurrentID:  "test-kek-1",
		IndexB64:   "RERERERERERERERERERERERERERERERERERERERERES=",
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

func closedEffectForTenant() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: cart.EffectCartClosed,
		Payload: map[string]any{
			"intake_id": "i-integration-test",
			"items":     []map[string]any{{"sku": "A1", "label": "Café", "qty": 1, "unit_price": 9.9}},
			"total":     9.9,
		},
	}
}

// TestWebhookSink_Integration_ConFeatureEIntegracion_CreaFilaPending es el
// camino feliz de T3.2 contra Postgres real: gate real (entitlements.Postgres +
// integrations.EntitlementsGate) + store real (integrations.Postgres).
func TestWebhookSink_Integration_ConFeatureEIntegracion_CreaFilaPending(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1, $2, true)
	`, tenantID, entitlements.FeatureCRMBridge); err != nil {
		t.Fatalf("sembrar tenant_features: %v", err)
	}

	store := integrations.NewPostgres(db, webhookCipherDePrueba(t))
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: tenantID, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://bridge.example/callback", Enabled: true,
	}, "s3cr3t"); err != nil {
		t.Fatalf("UpsertTenantIntegration: %v", err)
	}

	entResolver := entitlements.NewPostgres(db)
	gate := integrations.NewEntitlementsGate(entResolver, store, entitlements.FeatureCRMBridge)
	sink := runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed, store, gate)

	if err := sink.Handle(ctx, runtime.EffectContext{TenantID: tenantID, ContactID: "c-opaco"}, closedEffectForTenant()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.webhook_outbox WHERE tenant_id = $1 AND kind = 'intake.push' AND status = 'pending'
	`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contar webhook_outbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("filas pending en webhook_outbox = %d, quiero 1 (tenant con feature+integración)", n)
	}
}

// TestWebhookSink_Integration_SinFeature_NoCreaFila: mismo efecto, mismo
// tenant, pero SIN el override de tenant_features — el gate debe cerrarse
// (fail-closed: sin plan comercial que la incluya, sin override, no hay
// crm_bridge) y no encolar nada.
func TestWebhookSink_Integration_SinFeature_NoCreaFila(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	ctx := context.Background()

	store := integrations.NewPostgres(db, webhookCipherDePrueba(t))
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: tenantID, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://bridge.example/callback", Enabled: true,
	}, "s3cr3t"); err != nil {
		t.Fatalf("UpsertTenantIntegration: %v", err)
	}

	entResolver := entitlements.NewPostgres(db)
	gate := integrations.NewEntitlementsGate(entResolver, store, entitlements.FeatureCRMBridge)
	sink := runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed, store, gate)

	if err := sink.Handle(ctx, runtime.EffectContext{TenantID: tenantID, ContactID: "c-opaco"}, closedEffectForTenant()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.webhook_outbox WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contar webhook_outbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("filas en webhook_outbox = %d, quiero 0 (tenant SIN feature crm_bridge)", n)
	}
}

// TestWebhookSink_Integration_SinIntegracionHabilitada_NoCreaFila: el tenant
// TIENE la feature comercial pero no configuró (o apagó) su puente — tampoco
// debe encolar.
func TestWebhookSink_Integration_SinIntegracionHabilitada_NoCreaFila(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1, $2, true)
	`, tenantID, entitlements.FeatureCRMBridge); err != nil {
		t.Fatalf("sembrar tenant_features: %v", err)
	}
	// A propósito: NINGÚN UpsertTenantIntegration — el tenant nunca configuró su puente.

	store := integrations.NewPostgres(db, webhookCipherDePrueba(t))
	entResolver := entitlements.NewPostgres(db)
	gate := integrations.NewEntitlementsGate(entResolver, store, entitlements.FeatureCRMBridge)
	sink := runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed, store, gate)

	if err := sink.Handle(ctx, runtime.EffectContext{TenantID: tenantID, ContactID: "c-opaco"}, closedEffectForTenant()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.webhook_outbox WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contar webhook_outbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("filas en webhook_outbox = %d, quiero 0 (tenant sin tenant_integrations)", n)
	}
}
