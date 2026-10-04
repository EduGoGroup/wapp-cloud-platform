// Package entitlements resuelve los DERECHOS COMERCIALES de un tenant: qué features (capacidades
// de pago) tiene habilitadas (ADR-0022). Es el "gate de verdad" del servidor: cualquier gate que
// viva solo en el Edge es decorativo (el Edge corre en la máquina del cliente), así que la
// respuesta a "¿este tenant tiene derecho a la capacidad X?" vive AQUÍ.
//
// Resolución (ADR-0022): el override de tenant_features GANA, en los dos sentidos; si no hay
// override, mandan las features del plan (plan NULL ⇒ 'basic'). Se consulta en puntos calientes
// (push de config, API de intents, ingesta), por eso la implementación Postgres lleva una caché
// en memoria con TTL corto para no pagar una query por mensaje.
//
// # Cómo se gatea una feature (design §D-040.5)
//
// Hay DOS formas canónicas, ambas sobre el puerto Resolver de este paquete:
//
//  1. Middleware HTTP: RequireFeature(resolver, "intakes_export") en middleware.go. Se compone en
//     la cadena de una ruta de la API pública, después de autenticar y de autorizar por scope.
//     Sin la feature corta con 403.
//
//  2. Check in-code, para los puntos que NO son HTTP (ingesta de entrantes, push de config al
//     conectar, un job): no hay dónde colgar un middleware, así que se pregunta y se decide en el
//     sitio:
//
//     has, err := resolver.Has(ctx, tenantID, entitlements.FeatureLLMIntent)
//     if err != nil || !has {
//     // degradar en silencio o rechazar, según el punto
//     return
//     }
//
// La regla que comparten las dos formas es FAIL-CLOSED: si el derecho no se puede resolver, no se
// concede. Un fallo transitorio de BD que abriera una capacidad de pago sería peor que una
// denegación temporal.
//
// El doble en memoria (Fake) y la suite de contrato del puerto (ContratoResolver) viven en
// entitlementshelpertest (D-F2-4), no aquí: ningún código de producción los necesita.
//
// Porta internal/entitlements/entitlements.go @ 9a77307.
package entitlements

import (
	"context"
	"time"
)

// Las claves de feature. Su VALOR es contrato hacia fuera: es la clave sembrada en plan_features y
// tenant_features (migraciones 0032, 0039, 0053, 0074) y la que publica GET /api/v1/entitlements,
// así que se copian byte a byte del paquete viejo. Que una clave exista en BD y no aquí NO es un
// gate a medias: es NINGÚN gate, porque la ruta se monta igual.

// FeatureLLMIntent es la feature del clasificador de intenciones LLM (ADR-0020), primera
// capacidad gateada por entitlements (ADR-0022).
const FeatureLLMIntent = "llm_intent"

// FeatureCartBasic es la feature del carrito y sus SOLICITUDES (Plan 041, ADR-0031): gatea la
// bandeja de pedidos —listado, detalle y cambio de estado— de la API pública. Fue la primera
// capacidad que usó RequireFeature en una ruta real.
const FeatureCartBasic = "cart_basic"

// FeatureIntakesExport es la feature de SACAR las solicitudes del sistema (Plan 041 · T1.2/T1.3):
// el export CSV/XLSX y el summary.json.
//
// Es una feature APARTE de FeatureCartBasic a propósito: ver la bandeja y poder llevarse los
// datos son dos capacidades comerciales distintas, y la taxonomía del Plan 040 (migración 0039)
// las siembra por separado en cada plan. Componerla sobre la otra —o dar por hecho que quien
// tiene cart_basic exporta— borraría esa distinción en el código aunque la BD la mantenga.
const FeatureIntakesExport = "intakes_export"

// FeatureCatalogImport es la feature de CARGAR el catálogo de golpe (Plan 041 · T3.3): el
// POST /api/v1/catalog/import y todo lo que cuelgue de esa ruta.
//
// Cargar el catálogo es una capacidad aparte de tocarlo a mano: quien no la tenga sigue pudiendo
// escribir su contenido por PUT /api/v1/tenant-content/{ref}, que es capa técnica y no se gatea
// (ADR-0035). Lo que se vende es el atajo —validar, ver el diff y versionar de una pasada—, no el
// derecho a tener catálogo.
const FeatureCatalogImport = "catalog_import"

// FeatureCRMBridge es la feature del puente CRM (Plan 042, D-042.8): gatea el encolado en
// webhook_outbox del WebhookSink y el CRUD de /api/v1/integrations.
//
// El gate real combina ESTA feature con tenant_integrations (events_adapter='webhook' AND
// enabled=true): el plan habilita la CAPACIDAD, la fila configurada habilita el DESTINO. Ninguna
// sustituye a la otra («el grant dice puedes operar esto; la feature dice tu plan lo incluye»).
const FeatureCRMBridge = "crm_bridge"

// FeatureMenu es la feature del tipo de fábrica `menu` (lista numerada → rama por elección, Plan
// 015/016). Sembrada en todos los planes desde la taxonomía del Plan 040 (migración 0039; y en
// `advisor_ai_local`, migración 0074). La consume el despachador de nivel superior (Plan 043 ·
// T2.3) para filtrar los tipos ofrecibles, igual que `survey` y `media`.
const FeatureMenu = "menu"

// FeatureSurvey es la feature del tipo de fábrica `survey` (secuencia de preguntas, Plan 014).
// Nace en el plan `basic` (migración 0053) porque es solo lógica conversacional, sin coste de
// infraestructura por tenant.
const FeatureSurvey = "survey"

// FeatureMedia es la feature del tipo de fábrica `media` (entrega de URL prefirmada R2, Plan
// 017). A diferencia de `survey`, nace en el plan `commerce`, no en `basic` (migración 0053),
// porque consume almacenamiento R2 y ancho de banda con coste real por uso.
const FeatureMedia = "media"

// FeatureLLMIntake es la feature de captación asistida por LLM (Plan 040/042; sembrada en
// `advisor_ai_pro` y `pro`, migración 0039).
//
// 🔴 ES EL NIVEL, Y SE BASTA SOLA (ADR-0044, D-044.28). Es el ÚNICO derecho que gatea el carril
// de captación entero —ventana de agregación, hilo literal, compositor y nacimiento del job—, y
// ninguno de esos puntos puede exigir además `api_llm`: ver el 🔴 de FeatureAPILLM.
const FeatureLLMIntake = "llm_intake"

// FeatureAPILLM es la feature de la VÍA API del LLM (ADR-0030, ADR-0044, Plan 044): el derecho a
// configurar credenciales de un proveedor externo y a que el pipeline llame por ahí (sembrada en
// `advisor_ai_pro` y `pro`, migración 0039).
//
// 🔴 GATEA LA VÍA, NO LA CAPACIDAD, Y ESA FRASE ES UN INVARIANTE (ADR-0044, D-044.28). Lo que el
// tenant paga es el NIVEL —`llm_intake`—; la API es una CONFIGURACIÓN dentro del nivel. Por eso
// esta constante solo puede aparecer donde se CONFIGURA o se USA la vía API: el CRUD
// /api/v1/tenant-llm y, cuando exista, la elección de vía `api`. Un tenant con `llm_intake` y SIN
// `api_llm` es un tenant VÁLIDO en vía local, no uno «a medias»: su ventana abre, su hilo se
// archiva y su job nace `pending`.
//
// ⚠️ ESTO INVIERTE D-044.6, que exigía `api_llm` para tener `llm_intake`. Esta constante en un
// gate del agregador, del productor del hilo, del compositor o de un endpoint de captación es un
// DEFECTO. Lo vigilan, en el código viejo, internal/flujos/runtime/via_local_sin_api_llm_test.go
// (por esta clave no se pregunta ni una vez en el carril) e
// internal/publicapi/tenantllm_gate_via_test.go (tener la capacidad NO abre la vía); sus gemelos
// nuevos nacen con los módulos que los consumen.
const FeatureAPILLM = "api_llm"

// FeatureMultiCompany es el derecho a que UNA MISMA PERSONA pertenezca a MÁS DE UNA empresa (Plan
// 047 · T5.2, D-047.14; sembrada solo en el plan `pro`, migración 0039). En el paquete viejo se
// llamaba FeatureMultiEmpresa (`05` E-11: el identificador pasa al inglés; el VALOR, que es la
// clave en BD y en el cable, no cambia).
//
// 🔴 GOBIERNA EL DESENLACE DE UN ALTA, NUNCA EL ACCESO A UNA RUTA (D-047.10). El plano de roles y
// membresías es CAPACIDAD BASE: cualquier empresa administra sus miembros. Lo que esta clave decide
// es si un alta CONCRETA —la de alguien que YA es miembro de otra empresa— escribe o se va con
// 409, y ese veredicto lo da el alta de membresía (iampostgres.GrantTenantAccess), no un
// middleware. Un RequireFeature(…, FeatureMultiCompany) colgando de /api/v1/members es un
// DEFECTO: dejaría sin administración de miembros a todo tenant que no pague la multi-empresa.
//
// ⚠️ La pregunta se hace contra el tenant que RECIBE al miembro —el del alta—, no contra el que ya
// lo tenía: es esa empresa la que compra la capacidad de incorporar a alguien de otra parte.
const FeatureMultiCompany = "multi_empresa"

// Resolver responde si un tenant tiene habilitada una feature y sabe listar sus derechos
// efectivos. Lo satisfacen la implementación Postgres (con caché) y entitlementshelpertest.Fake;
// la suite entitlementshelpertest.ContratoResolver fija lo que las dos prometen. Toda consulta va
// acotada al tenant (INV-8): lo que se siembra para un tenant no se ve desde otro.
//
// La regla de resolución (ADR-0022), la misma para Has y para ListEffective: un override de
// tenant_features gana en los dos sentidos (enabled=true activa aunque el plan no la traiga;
// enabled=false apaga aunque el plan la traiga); sin override mandan las features del plan, y un
// plan NULL se resuelve como 'basic'.
type Resolver interface {
	// Has devuelve true si el tenant tiene la feature efectiva habilitada. «No la tiene» es
	// (false, nil), también para un tenant que no existe: no es un error. Un error SOLO se
	// devuelve ante fallo de infraestructura, y entonces con false: el llamante lo trata como «sin
	// la feature», sin abrir la capacidad por un fallo transitorio (fail-closed).
	Has(ctx context.Context, tenantID, feature string) (bool, error)

	// ListEffective devuelve el plan efectivo del tenant y las features que tiene ENCENDIDAS
	// (plan ∪ overrides que activan, ∖ overrides que desactivan), en orden alfabético y sin
	// repetidas. Una feature apagada por override NO aparece, aunque el plan la traiga. Alimenta
	// GET /api/v1/entitlements (Plan 040 · T2.2): la UI decide por `contains`, no por un mapa de
	// claves conocidas (design §D-040.3).
	//
	// Un tenant sin derechos devuelve una lista vacía, no un error. Un tenant que no existe no
	// tiene plan resoluble: devuelve plan "" y ninguna feature, sin error (la misma respuesta que
	// da Has). Ante un fallo de infraestructura devuelve ("", nil, err).
	ListEffective(ctx context.Context, tenantID string) (plan string, features []string, err error)

	// CacheTTL es el TTL con el que el Resolver cachea sus respuestas, para que quien las publique
	// (el endpoint) le diga al cliente cuánto tarda como mucho en propagarse un cambio de plan u
	// override. No es configuración: es el TTL REAL del objeto (design §3, corrección 2 de la
	// Ola 0), siempre positivo.
	CacheTTL() time.Duration
}
