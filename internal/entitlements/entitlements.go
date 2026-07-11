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
package entitlements

import "context"

// FeatureLLMIntent es la feature del clasificador de intenciones LLM (ADR-0020),
// primera capacidad gateada por entitlements (ADR-0022).
const FeatureLLMIntent = "llm_intent"

// Resolver responde si un tenant tiene habilitada una feature. Lo satisface la
// implementación Postgres (con caché) y el Fake de tests. Toda consulta va
// acotada al tenant (INV-8).
type Resolver interface {
	// Has devuelve true si el tenant tiene la feature efectiva habilitada. Un
	// error solo se devuelve ante fallo de infraestructura (no ante "no la tiene",
	// que es false, nil): el llamante trata el error como "sin la feature" en los
	// gates, sin abrir la capacidad por un fallo transitorio.
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// Fake es un Resolver en memoria para tests: el conjunto de features habilitadas
// por tenant. Ausencia ⇒ false. Es seguro para lectura concurrente si no se muta
// tras construirlo.
type Fake struct {
	// Enabled mapea tenantID → conjunto de features habilitadas.
	Enabled map[string]map[string]bool
	// Err, si no es nil, se devuelve en cada Has (simula fallo de infraestructura).
	Err error
}

// NewFake construye un Fake vacío listo para poblar.
func NewFake() *Fake {
	return &Fake{Enabled: make(map[string]map[string]bool)}
}

// Enable marca una feature como habilitada para un tenant (helper de tests).
func (f *Fake) Enable(tenantID, feature string) {
	if f.Enabled == nil {
		f.Enabled = make(map[string]map[string]bool)
	}
	set := f.Enabled[tenantID]
	if set == nil {
		set = make(map[string]bool)
		f.Enabled[tenantID] = set
	}
	set[feature] = true
}

// Has implementa Resolver sobre el mapa en memoria.
func (f *Fake) Has(_ context.Context, tenantID, feature string) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	return f.Enabled[tenantID][feature], nil
}
