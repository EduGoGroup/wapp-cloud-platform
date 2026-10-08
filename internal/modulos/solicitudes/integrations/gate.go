// Porta internal/integrations/gate.go @ 36d5a04

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
// (entitlements.RequireFeature). Satisface runtime.WebhookGate y crmpush.Gate por
// forma estructural (Go no exige el import de la interfaz para satisfacerla).
//
// Es UNA sola evaluación para todas las puertas que encolan (R-13): un tenant no
// puede tener el puente abierto para una y cerrado para otra.
type EntitlementsGate struct {
	features FeatureResolver
	store    Store
	feature  string
}

// NewEntitlementsGate construye el gate. feature es la clave a comprobar
// (entitlements.FeatureCRMBridge en producción; un valor de prueba en tests
// sin acoplar este paquete a entitlements por el símbolo de la constante). No
// consulta nada al construirse.
func NewEntitlementsGate(features FeatureResolver, store Store, feature string) *EntitlementsGate {
	return &EntitlementsGate{features: features, store: store, feature: feature}
}

// Enabled implementa runtime.WebhookGate: fail-closed en las tres formas de
// no-resolución (sin feature, sin integración, o cualquier error de
// infraestructura) — mismo criterio que entitlements.RequireFeature.
//
// Devuelve true SOLO si se cumplen todas a la vez: el tenant tiene la feature
// (se pregunta por features.Has con el tenant y la clave del constructor), tiene
// fila de integración con Enabled=true, su EventsAdapter es exactamente
// «webhook», y la integración tiene destino: endpoint y secreto de firma. Si
// falta cualquiera devuelve (false, nil): un gate cerrado no es un error.
//
// Orden: primero la feature. Si no la tiene, o si preguntarla falla, el almacén
// NO se consulta.
//
// Exige el destino (decisión de Jhoan, 2026-10-08; el viejo no lo miraba): una
// integración encendida sin endpoint o sin secreto NO abre el gate, así que no
// se encola una entrega que el worker solo podría fallar hasta `dead`
// (worker_delivery.go exige las mismas dos cosas al entregar). La API ya impide
// encender un puente así; esto cubre la fila dejada a medias por fuera de ella.
// Del secreto solo mira que exista (HasSecret): no lo lee ni lo descifra.
//
// Errores, envueltos con %w y siempre con false:
//
//   - «integrations: evaluar feature <feature> de <tenant>: » — falla el resolver;
//   - «integrations: leer integración de <tenant>: » — falla el almacén.
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
	return ti.EndpointURL != "" && ti.HasSecret, nil
}
