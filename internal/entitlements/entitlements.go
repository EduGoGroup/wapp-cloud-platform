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

// FeatureCartBasic es la feature del carrito y sus SOLICITUDES (Plan 041, ADR-0031):
// gatea la bandeja de pedidos —listado, detalle y cambio de estado— de la API
// pública. Es la primera capacidad que usa el middleware RequireFeature en una ruta
// real (hasta el Plan 041 el gate solo vivía en checks in-code).
const FeatureCartBasic = "cart_basic"

// FeatureIntakesExport es la feature de SACAR las solicitudes del sistema (Plan
// 041 · T1.2/T1.3): el export CSV/XLSX y el summary.json.
//
// Es una feature APARTE de FeatureCartBasic a propósito, no un detalle de
// granularidad: ver la bandeja y poder llevarse los datos son dos capacidades
// comerciales distintas, y la taxonomía del Plan 040 (migración 0039) las siembra
// por separado en cada plan. Componerla sobre la otra —o dar por hecho que quien
// tiene cart_basic exporta— borraría esa distinción en el código aunque la BD la
// mantenga.
const FeatureIntakesExport = "intakes_export"

// FeatureCatalogImport es la feature de CARGAR el catálogo de golpe (Plan 041 ·
// T3.3): el POST /api/v1/catalog/import y todo lo que cuelgue de esa ruta.
//
// La clave lleva sembrada desde la taxonomía del Plan 040 (migración 0039); lo
// que faltaba —y añade esta línea— era la constante Go, sin la cual el gate no se
// podía escribir. Que la clave exista en BD y no en el código NO es un gate a
// medias: es NINGÚN gate, porque la ruta se monta igual.
//
// Cargar el catálogo es una capacidad comercial aparte de tocarlo a mano: quien
// no la tenga sigue pudiendo escribir su contenido por
// PUT /api/v1/tenant-content/{ref}, que es capa técnica y no se gatea (ADR-0035).
// Lo que se vende aquí es el atajo —validar, ver el diff y versionar de una
// pasada—, no el derecho a tener catálogo.
const FeatureCatalogImport = "catalog_import"

// FeatureCRMBridge es la feature del puente CRM (Plan 042, D-042.8): gatea el
// encolado en webhook_outbox del WebhookSink y el CRUD de
// /api/v1/integrations (Ola 5). Como con FeatureCatalogImport, la clave ya
// venía sembrada en plan_features desde el Plan 040 (migración 0039:
// commerce/advisor_ai/advisor_ai_pro/pro la incluyen) — sin la constante Go el
// gate no se podía escribir, aunque la clave ya existiera en BD.
//
// El gate real combina ESTA feature con tenant_integrations (events_adapter=
// 'webhook' AND enabled=true): tener el plan comercial habilita la CAPACIDAD,
// tener la fila configurada habilita el DESTINO. Ninguna de las dos sustituye
// a la otra (mismo principio que "el grant dice puedes operar esto; la
// feature dice tu plan lo incluye").
const FeatureCRMBridge = "crm_bridge"

// FeatureMenu es la feature del tipo de fábrica `menu` (lista numerada → rama
// por elección, Plan 015/016): la clave lleva sembrada en plan_features desde
// la taxonomía del Plan 040 (migración 0039_seed_plan_taxonomy.sql:52,56,62,
// 69,80 — los cinco planes la incluyen) pero sin constante Go, porque hasta
// ahora nada la gateaba desde código. El despachador de nivel superior (Plan
// 043 · Ola 2 · T2.3) necesita filtrar los tipos ofrecibles por feature, y
// `menu` es uno de ellos igual que `survey` y `media`, así que se declara
// aquí de paso.
const FeatureMenu = "menu"

// FeatureSurvey es la feature del tipo de fábrica `survey` (secuencia de
// preguntas, Plan 014): nace en el plan `basic` (migración
// 0053_seed_survey_media_features.sql) porque es solo lógica conversacional,
// sin coste de infraestructura por tenant que la use. La consume el
// despachador de T2.3 para filtrar el menú numérico dinámico.
const FeatureSurvey = "survey"

// FeatureMedia es la feature del tipo de fábrica `media` (entrega de URL
// prefirmada R2, Plan 017): a diferencia de `survey`, nace en el plan
// `commerce`, no en `basic` (migración 0053_seed_survey_media_features.sql),
// porque consume almacenamiento R2 y ancho de banda con coste real por uso.
const FeatureMedia = "media"

// FeatureLLMIntake es la feature de captación asistida por LLM (Plan
// 040/042). La clave ya venía sembrada en plan_features desde la taxonomía
// del Plan 040 (migración 0039_seed_plan_taxonomy.sql:75,86: `advisor_ai_pro`
// y `pro` la incluyen) sin constante Go — es formalmente de la Ola 3 del Plan
// 043 (T3.5), pero se declara aquí de paso porque el fichero ya estaba
// abierto para las dos anteriores.
const FeatureLLMIntake = "llm_intake"

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
