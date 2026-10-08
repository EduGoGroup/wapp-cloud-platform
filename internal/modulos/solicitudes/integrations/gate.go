// Porta internal/integrations/gate.go @ 36d5a04

package integrations

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type EntitlementsGate struct{}

// NewEntitlementsGate construye el gate. feature es la clave a comprobar
// (entitlements.FeatureCRMBridge en producción; un valor de prueba en tests
// sin acoplar este paquete a entitlements por el símbolo de la constante). No
// consulta nada al construirse.
func NewEntitlementsGate(features FeatureResolver, store Store, feature string) *EntitlementsGate {
	panic(pendiente.Implementar("integrations.NewEntitlementsGate"))
}

// Enabled implementa runtime.WebhookGate: fail-closed en las tres formas de
// no-resolución (sin feature, sin integración, o cualquier error de
// infraestructura) — mismo criterio que entitlements.RequireFeature.
//
// Devuelve true SOLO si se cumplen las tres a la vez: el tenant tiene la feature
// (se pregunta por features.Has con el tenant y la clave del constructor), tiene
// fila de integración con Enabled=true, y su EventsAdapter es exactamente
// «webhook». Si falta cualquiera devuelve (false, nil): un gate cerrado no es un
// error.
//
// Orden: primero la feature. Si no la tiene, o si preguntarla falla, el almacén
// NO se consulta.
//
// No mira el endpoint ni el secreto: una integración encendida sin URL o sin
// secreto abre el gate, y es el worker quien falla la entrega por falta de
// destino (worker.go).
//
// Errores, envueltos con %w y siempre con false:
//
//   - «integrations: evaluar feature <feature> de <tenant>: » — falla el resolver;
//   - «integrations: leer integración de <tenant>: » — falla el almacén.
func (g *EntitlementsGate) Enabled(ctx context.Context, tenantID string) (bool, error) {
	panic(pendiente.Implementar("integrations.EntitlementsGate.Enabled"))
}
