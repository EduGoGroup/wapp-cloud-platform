// Package entitlements resuelve los DERECHOS COMERCIALES de un tenant: qué
// features (capacidades de pago) tiene habilitadas (ADR-0022). Es el "gate de
// verdad" del servidor — cualquier gate que viva solo en el Edge es decorativo
// (el Edge corre en la máquina del cliente), así que la respuesta a "¿este tenant
// tiene derecho a la capacidad X?" vive AQUÍ.
//
// Resolución (ADR-0022): el override de tenant_features GANA; si no hay override,
// mandan las features del plan (plan NULL ⇒ 'basic'). Se consulta en puntos
// calientes (push de config, API de intents, ingesta), por eso la implementación
// Postgres lleva una caché en memoria por (tenant, feature) con TTL corto para no
// pagar una query por mensaje.
//
// # Cómo se gatea una feature (design §D-040.5)
//
// Hay DOS formas canónicas, ambas sobre el puerto Resolver de este paquete:
//
//  1. Middleware HTTP — RequireFeature(resolver, "intakes_export") en middleware.go:
//     se compone en la cadena de una ruta de la API pública, después de autenticar
//     y de autorizar por scope. Sin la feature corta con 403.
//
//  2. Check in-code — para los puntos que NO son HTTP (ingesta de entrantes, push
//     de config al conectar, un job): no hay dónde colgar un middleware, así que se
//     pregunta y se decide en el sitio. Es el patrón de los tres gates vivos de
//     llm_intent (bootstrap/auth.go, flujos/runtime/incoming.go, publicapi/intents.go):
//
//     has, err := resolver.Has(ctx, tenantID, entitlements.FeatureLLMIntent)
//     if err != nil || !has {
//     // degradar en silencio o rechazar, según el punto
//     return
//     }
//
// La regla que comparten las dos formas es FAIL-CLOSED: si el derecho no se puede
// resolver, no se concede. Un fallo transitorio de BD que abriera una capacidad de
// pago sería peor que una denegación temporal.
package entitlements

import (
	"context"
	"slices"
	"time"
)

// FeatureLLMIntent es la feature del clasificador de intenciones LLM (ADR-0020),
// primera capacidad gateada por entitlements (ADR-0022).
const FeatureLLMIntent = "llm_intent"

// Resolver responde si un tenant tiene habilitada una feature y sabe listar sus
// derechos efectivos. Lo satisface la implementación Postgres (con caché) y el
// Fake de tests. Toda consulta va acotada al tenant (INV-8).
type Resolver interface {
	// Has devuelve true si el tenant tiene la feature efectiva habilitada. Un
	// error solo se devuelve ante fallo de infraestructura (no ante "no la tiene",
	// que es false, nil): el llamante trata el error como "sin la feature" en los
	// gates, sin abrir la capacidad por un fallo transitorio.
	Has(ctx context.Context, tenantID, feature string) (bool, error)

	// ListEffective devuelve el plan efectivo del tenant y las features que tiene
	// ENCENDIDAS (plan ∪ overrides que activan, ∖ overrides que desactivan), en
	// orden alfabético. Es la capacidad que alimenta GET /api/v1/entitlements
	// (Plan 040 · T2.2): la UI decide por `contains`, no por un mapa completo de
	// claves conocidas (design §D-040.3). Un tenant sin derechos devuelve una
	// lista vacía, no un error.
	ListEffective(ctx context.Context, tenantID string) (plan string, features []string, err error)

	// CacheTTL expone el TTL con el que el Resolver cachea sus respuestas, para
	// que quien las publique (el endpoint) pueda decirle al cliente cuánto tarda
	// como mucho en propagarse un cambio de plan/override. No es config: es el
	// TTL REAL del objeto (design §3, corrección 2 de la Ola 0).
	CacheTTL() time.Duration
}

// Fake es un Resolver en memoria para tests: el conjunto de features habilitadas
// por tenant. Ausencia ⇒ false. Es seguro para lectura concurrente si no se muta
// tras construirlo.
//
// El mapa modela el resultado YA RESUELTO (plan ∪/∖ overrides), no las dos tablas:
// Enable deja la feature encendida y Disable la deja apagada explícitamente, que
// es lo que un override `enabled=false` produce en la BD. Por eso una feature con
// valor false NO aparece en ListEffective.
type Fake struct {
	// Enabled mapea tenantID → feature → encendida.
	Enabled map[string]map[string]bool
	// Plans mapea tenantID → plan efectivo que devuelve ListEffective. Ausencia ⇒
	// "basic" (mismo criterio que la resolución real ante plan_id NULL).
	Plans map[string]string
	// TTL es el valor que devuelve CacheTTL. Cero ⇒ defaultCacheTTL (60 s), el
	// mismo que sirve la implementación Postgres.
	TTL time.Duration
	// Err, si no es nil, se devuelve en cada Has/ListEffective (simula fallo de
	// infraestructura).
	Err error
}

// NewFake construye un Fake vacío listo para poblar.
func NewFake() *Fake {
	return &Fake{Enabled: make(map[string]map[string]bool)}
}

// Enable marca una feature como habilitada para un tenant (helper de tests).
func (f *Fake) Enable(tenantID, feature string) {
	f.set(tenantID, feature, true)
}

// Disable marca una feature como APAGADA para un tenant: modela el override
// `enabled=false`, que gana sobre el plan (ADR-0022). Distinto de no declararla:
// aquí queda registrada y explícitamente en false.
func (f *Fake) Disable(tenantID, feature string) {
	f.set(tenantID, feature, false)
}

// SetPlan fija el plan efectivo que ListEffective reporta para un tenant.
func (f *Fake) SetPlan(tenantID, plan string) {
	if f.Plans == nil {
		f.Plans = make(map[string]string)
	}
	f.Plans[tenantID] = plan
}

func (f *Fake) set(tenantID, feature string, enabled bool) {
	if f.Enabled == nil {
		f.Enabled = make(map[string]map[string]bool)
	}
	set := f.Enabled[tenantID]
	if set == nil {
		set = make(map[string]bool)
		f.Enabled[tenantID] = set
	}
	set[feature] = enabled
}

// Has implementa Resolver sobre el mapa en memoria.
func (f *Fake) Has(_ context.Context, tenantID, feature string) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	return f.Enabled[tenantID][feature], nil
}

// ListEffective implementa Resolver sobre el mapa en memoria: devuelve el plan
// del tenant (o "basic") y sus features ENCENDIDAS en orden alfabético.
func (f *Fake) ListEffective(_ context.Context, tenantID string) (string, []string, error) {
	if f.Err != nil {
		return "", nil, f.Err
	}
	plan, ok := f.Plans[tenantID]
	if !ok {
		plan = "basic"
	}
	features := make([]string, 0, len(f.Enabled[tenantID]))
	for feature, enabled := range f.Enabled[tenantID] {
		if enabled {
			features = append(features, feature)
		}
	}
	slices.Sort(features)
	return plan, features, nil
}

// CacheTTL implementa Resolver: el TTL configurado o el default del paquete.
func (f *Fake) CacheTTL() time.Duration {
	if f.TTL > 0 {
		return f.TTL
	}
	return defaultCacheTTL
}
