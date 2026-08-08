package integrations

import (
	"context"
	"fmt"
)

// FeatureResolver es lo mínimo que EntitlementsGate necesita del resolver de
// entitlements (interfaz local, ISP): evita que este paquete acople el tipo
// completo entitlements.Resolver. La satisface *entitlements.Postgres/Fake.
type FeatureResolver interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// EntitlementsGate combina las DOS preguntas de D-042.8 en una sola: "tiene el
// tenant el plan comercial que incluye crm_bridge" (features.Has) Y "tiene
// configurado y encendido su puente" (tenant_integrations). Ninguna sustituye a
// la otra — mismo principio del grant vs. la feature que ya usa este repo
// (entitlements.RequireFeature). Satisface runtime.WebhookGate por forma
// estructural (Go no exige el import de la interfaz para satisfacerla).
type EntitlementsGate struct {
	features FeatureResolver
	store    Store
	feature  string
}

// NewEntitlementsGate construye el gate. feature es la clave a comprobar
// (entitlements.FeatureCRMBridge en producción; un valor de prueba en tests
// sin acoplar este paquete a entitlements por el símbolo de la constante).
func NewEntitlementsGate(features FeatureResolver, store Store, feature string) *EntitlementsGate {
	return &EntitlementsGate{features: features, store: store, feature: feature}
}

// Enabled implementa runtime.WebhookGate: fail-closed en las tres formas de
// no-resolución (sin feature, sin integración, o cualquier error de
// infraestructura) — mismo criterio que entitlements.RequireFeature.
func (g *EntitlementsGate) Enabled(ctx context.Context, tenantID string) (bool, error) {
	has, err := g.features.Has(ctx, tenantID, g.feature)
	if err != nil {
		return false, fmt.Errorf("integrations: evaluar feature %s de %s: %w", g.feature, tenantID, err)
	}
	if !has {
		return false, nil
	}

	ti, found, err := g.store.GetTenantIntegration(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("integrations: leer integración de %s: %w", tenantID, err)
	}
	if !found || !ti.Enabled || ti.EventsAdapter != "webhook" {
		return false, nil
	}
	return true, nil
}
