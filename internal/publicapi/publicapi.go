// Package publicapi expone la cara PÚBLICA de wApp para terceros (Plan 018 · T5):
// las rutas /api/v1 de operación (enviar mensajes, publicar/leer/arrancar flujos)
// sobre el MISMO listener :8103 y el MISMO middleware que la fase IAM (T3) ya
// construyó. No reimplementa lógica de negocio: envuelve el gateway CloudLink
// (SendText), el motor de flujos (InsertDefinition/Start) y el store de
// definiciones con autenticación M2M (api-key/service-token) + autorización por
// scope (glob RBAC) + auditoría de escrituras.
//
// SEGURIDAD (INV-8, R6): TODA operación se acota al tenant de la Identity del token
// (httpapi.IdentityFromContext), NUNCA a un tenant del cuerpo. Un tercero con
// api-key del tenant A no puede tocar recursos del tenant B: las lecturas/escrituras
// filtran por tenant en el store, y el envío por session_id valida que la sesión
// pertenezca al tenant (sessionBelongsToTenant). Zero-knowledge: jamás se loguean
// api-keys/secretos ni se audita PII (AuditMiddleware fija action/resource opacos).
package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// MessageSender empuja un SendText hacia una sesión viva del Edge y espera su Ack.
// Lo satisface *gatewaygrpc.Server (mismo método que reusa /admin/messages/send).
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// SessionLister lista las sesiones (durables) de un tenant. Lo satisface
// *fleet.PostgresRepository (su método List). Se usa para el aislamiento por tenant
// del envío por session_id (INV-8): una sesión que no aparece en la lista del tenant
// del token NO es suya y el envío se rechaza (404).
type SessionLister interface {
	List(ctx context.Context, tenantID string) ([]fleet.Session, error)
}

// FlowStore agrupa las operaciones sobre definiciones de flujo que consume la API
// pública. Todas son tenant-scoped. Lo satisface *store.PostgresRepository (y el
// MemoryRepository en tests). InsertDefinition encaja además con
// flowadmin.DefinitionStore (se reusa DefinitionHandler tal cual).
type FlowStore interface {
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	ListDefinitions(ctx context.Context, tenantID string) ([]store.FlowSummary, error)
}

// AuditReader lista la bitácora de auditoría de un tenant (Plan 018 · T10, R11).
// Lo satisface *iamusecase.AuditService (ListAudit). Se declara aquí para no
// acoplar publicapi al usecase concreto.
type AuditReader interface {
	ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}

// FlowDeps agrupa las dependencias del motor de flujos.
type FlowDeps struct {
	Flows   FlowStore                  // store de definiciones (CRUD lectura/alta)
	Modules flowadmin.ModuleTypeSource // tipos de nodo de módulos (validación del alta)
	Starter flowadmin.Starter          // motor de flujos (arranque de conversación)
}

// SessionDeps agrupa las dependencias de gestión de sesiones del Edge.
type SessionDeps struct {
	Sessions SessionLister // fleet: sesiones del tenant (aislamiento)
	// SessionProfiles administra el PERFIL active|passive de una sesión (Plan 046 ·
	// T1.2). Lo satisface *fleet.PostgresRepository (SetProfile). nil ⇒ no se monta
	// la ruta.
	SessionProfiles flowadmin.SessionProfileStore
	// ProfilePush empuja el cambio de perfil a la sesión viva tras persistirlo
	// (best-effort, ADR-0027). 🔴 En T1.2 se cablea a nil (no-op): quien lo enchufa
	// de verdad es T2.1. nil NO desmonta la ruta, solo apaga el push.
	ProfilePush flowadmin.ProfilePusher
	// SessionStatus administra el estatus offline|loggedout de una sesión, para
	// retirar/limpiar un zombie (Plan 020 · T3). Lo satisface *fleet.PostgresRepository
	// (SetState). nil ⇒ no se monta la ruta.
	SessionStatus flowadmin.SessionStatusStore
}

// DiagDeps agrupa las dependencias de diagnóstico remoto.
type DiagDeps struct {
	// Diagnostics persiste las solicitudes/bundles del diagnóstico remoto y resuelve
	// el consentimiento por tenant (Plan 031 · T5, ADR-0023). Lo satisface
	// *diagnostics.Postgres. nil ⇒ no se montan las rutas de diagnóstico.
	Diagnostics DiagnosticsStore
	// DiagnosticsRequester emite el DiagnosticsRequest por el stream a la sesión. Lo
	// satisface *gatewaygrpc.Server (mismo objeto que Sender/ConfigPush). nil ⇒ no se
	// montan las rutas de diagnóstico.
	DiagnosticsRequester DiagnosticsRequester
	// DiagnosticsBundleTTL es la retención del bundle (requested_at + TTL). Cero-valor
	// ⇒ default 30m. Se cablea desde config.DiagnosticsConfig.
	DiagnosticsBundleTTL time.Duration
}

// MediaDeps agrupa las dependencias de gestión de contenido y presign de media.
type MediaDeps struct {
	Media   PresignUploader    // presign R2 (upload-url, Plan 017/018 · T6)
	Content TenantContentStore // blobs JSONB por-tenant (tenant_content, T6)
	// ContentMaxBytes es el techo del blob de tenant_content. Cero-valor ⇒ default de
	// catalogimport (1 MiB, el que este endpoint tenía hardcodeado). Se cablea desde
	// config.TenantContentConfig: es el MISMO número que gobierna el import de catálogo,
	// porque los dos escriben en la misma tabla y dos techos distintos dejarían un
	// blob importable que el PUT genérico rechaza (Plan 041 · Ola 3).
	ContentMaxBytes int64
	// ContentVersions archiva el catálogo vigente y escribe el nuevo en un solo acto
	// (Plan 041 · T3.3, D-041.8). Lo satisface el MISMO *store.PostgresRepository que
	// Content; se declara aparte porque el PUT genérico no versiona. nil ⇒ no se
	// montan las rutas de import.
	ContentVersions CatalogVersionWriter
	// ImportMaxItems es el tope de artículos de UN documento de import. Cero-valor ⇒
	// default de catalogimport (500). Se cablea desde config.ImportConfig. A
	// diferencia del techo de bytes, este SÍ es del import: el PUT genérico no cuenta
	// artículos porque no sabe qué hay dentro del blob.
	ImportMaxItems int
}

// Deps agrupa las dependencias de negocio que la API pública envuelve. Se
// construyen una sola vez en cmd/server (los MISMOS objetos que sirven a
// gRPC/admin): esta capa solo añade transporte + autorización pública.
type Deps struct {
	FlowDeps
	SessionDeps
	DiagDeps
	MediaDeps

	Sender MessageSender // gateway CloudLink (SendText)
	// SendBudget es el PRESUPUESTO DE LA PETICIÓN de POST /api/v1/messages (Plan 050 ·
	// Ola 5 · T5.4): el techo por encima de los relojes secuenciales del envío, para
	// que su suma no pueda pasarse del WriteTimeout del servidor y dejar al cliente
	// con la conexión cerrada y sin cuerpo. NO se cablea a mano: se DERIVA con
	// SendBudgetFrom(writeTimeout) en el mismo sitio que construye el http.Server, de
	// modo que los dos no puedan desincronizarse. <=0 ⇒ sin plazo (comportamiento
	// previo a esta tarea).
	SendBudget time.Duration
	Audit      AuditReader            // bitácora de auditoría (GET /api/v1/audit, T10)
	Triggers   flowadmin.TriggerStore // reglas de disparo (CRUD /api/v1/triggers, Plan 019 T5)
	// TriggersDurableFlow es el puerto ESTRECHO de T2.6/T2.7 (Plan 054 · F3,
	// D-054.6/D-054.8): «¿el flujo de esta regla tiene contenido durable?». Mismo
	// parámetro POSICIONAL que reciben los tres constructores del CRUD de reglas
	// (flowadmin.Create/List/DeleteTriggerHandler) — nil es un valor VÁLIDO
	// (fail-open, ver el docstring de flowadmin.DurableFlowChecker), pero el
	// parámetro en sí no se puede omitir sin dejar de compilar. Se cablea en
	// cmd/server con el MISMO flowStore/flowEngine que ya usa el resto del motor.
	TriggersDurableFlow flowadmin.DurableFlowChecker
	// Intents persiste el blob de config del clasificador de intenciones por tenant
	// (Plan 029 · T5). Lo satisface *intentcfg.PostgresStore. nil ⇒ no se montan las
	// rutas /api/v1/intents.
	Intents IntentConfigStore
	// Entitlements resuelve los derechos comerciales del tenant (ADR-0022): es el
	// gate de verdad del PUT de intents (sin llm_intent ⇒ 403) y la fuente de
	// GET /api/v1/entitlements (Plan 040 · T2.2). Lo satisface
	// *entitlements.Postgres. nil ⇒ no se montan ni /api/v1/intents ni
	// /api/v1/entitlements.
	Entitlements EntitlementsResolver
	// ConfigPush empuja el ConfigUpdate a las sesiones vivas del tenant tras el PUT
	// (ADR-0021, best-effort). Lo satisface *gatewaygrpc.Server (PushConfig). nil ⇒
	// no se empuja (solo se persiste; el push al conectar reconcilia).
	ConfigPush ConfigPusher
	// Intakes es el dominio de SOLICITUDES (pedidos/presupuestos, ADR-0031): la
	// bandeja del dueño y la máquina de estados del Plan 041. Lo satisface
	// *intakes.Service. nil ⇒ no se montan las rutas /api/v1/intakes.
	Intakes IntakeService
	// ConversationEvents lista los EVENTOS conversacionales del tenant (Plan 043 ·
	// T3.9b, REQ-28): la bandeja hermana de la de solicitudes, la que enseña lo que
	// `…/intakes/discard` no alcanza porque no parió solicitud. Lo satisface
	// *events.Store. nil ⇒ no se monta GET /api/v1/conversation-events.
	ConversationEvents ConversationEventLister
	// EventCanceller cancela un evento conversacional por id (Plan 043 · T4.2):
	// la acción de limpieza de la bandeja de arriba. Lo satisface *runtime.Runtime
	// (internal/flujos/runtime) — el canceller es el runtime y no el store porque
	// cancelar orquesta tres efectos (guard del evento, flow_state, intake
	// colgante). nil ⇒ no se monta POST /api/v1/conversation-events/{id}/cancel.
	EventCanceller ConversationEventCanceller
	// TenantVariables son las variables de empresa clave→valor que wApp NO
	// interpreta (Plan 041 · T2.1, D-041.1). Lo satisface *tenantvars.Postgres.
	// nil ⇒ no se montan las rutas /api/v1/tenant-variables.
	TenantVariables TenantVariableStore
	// Health son los umbrales de la derivación de salud (degraded>N, stale>M) que
	// GET /api/v1/sessions aplica al servir (Plan 031 · T4, ADR-0023). Cero-valor ⇒
	// defaults (5m/2m). Se cablea desde config.HealthConfig.
	Health HealthRules
	// Alerter es el punto de extensión del alerting push sobre la salud derivada
	// (ADR-0023). nil ⇒ NoopAlerter (nada se empuja; el estado queda consultable).
	Alerter Alerter
	// Integrations es la CONFIGURACIÓN del puente CRM por tenant
	// (tenant_integrations): el CRUD /api/v1/integrations del Plan 042 · T5.1. Lo
	// satisface *integrations.Postgres — el MISMO objeto que sirve a CRMSecrets y
	// al gate; lo que cambia es qué se le pide. nil ⇒ no se montan las rutas.
	Integrations IntegrationsStore
	// Reanalysis es el caso de uso de POST /api/v1/intakes/{id}/reanalyze (Plan 044
	// · Ola 4 · T4.6): re-interpretar un pedido desde el literal original del evento.
	// Lo satisface *reanalisis.Servicio. nil ⇒ NO se monta la ruta.
	//
	// Es una dependencia APARTE de Intakes —y no un método más de aquel puerto—
	// porque el re-análisis cruza cinco fronteras que la bandeja no cruza:
	// entitlements, tenant_llm, el hilo cifrado del evento, la cola del pipeline y el
	// compositor del literal. El porqué entero está en la cabecera de
	// internal/reanalisis.
	Reanalysis ReanalysisService
	// QuoteSuggestions redacta la cotización con la voz de la dueña y la DEVUELVE
	// (Plan 044 · Ola 5 · T5.1): `POST /api/v1/intakes/{id}/quote-suggestion`. Lo
	// satisface *quotetext.Servicio. nil ⇒ NO se monta la ruta.
	//
	// Es una dependencia APARTE de Intakes —y no un método más de aquel puerto— por lo
	// mismo que Reanalysis: necesita el selector de vía LLM, el historial aprobado del
	// TENANT (no de la solicitud) y el contenido dinámico del tenant, y ninguna de las
	// tres la conoce hoy `intakes.Service`.
	QuoteSuggestions QuoteSuggester
	// TenantLLM es la CONFIGURACIÓN de la vía LLM API por tenant (tenant_llm): el
	// CRUD /api/v1/tenant-llm del Plan 044 · T0.3. Lo satisface
	// *tenantllm.Postgres, pero por un puerto RECORTADO que no puede devolver la
	// credencial (ver TenantLLMStore). nil ⇒ no se montan las rutas.
	TenantLLM TenantLLMStore
	// DegradationNotices lee los avisos al dueño de que el LLM se degradó al
	// Nivel A (owner_degradation_notices, Plan 044 · T1.5-4, REQ-38). Lo satisface
	// *degradation.Postgres, por un puerto de SOLO LECTURA que no puede escribir
	// (ver DegradationNoticeLister). nil ⇒ no se monta
	// GET /api/v1/degradation-notices.
	//
	// ⚠️ En la Ola 1.5 la tabla está VACÍA y eso es lo esperado: el punto de
	// inyección se construye aquí y lo pueblan T1.6-6 y la O2.
	DegradationNotices DegradationNoticeLister
	// CRMSecrets entrega el secreto HMAC con el que se verifica el callback del
	// puente (Plan 042 · T4.2). Lo satisface *integrations.Postgres. nil ⇒ NO se
	// monta POST /api/v1/integrations/callback: mejor un 404 de ruta inexistente
	// que una puerta de autenticación a medio cablear.
	CRMSecrets CRMSecretReader
	// CRMGate combina la feature comercial y la integración encendida (D-042.8). Es
	// el MISMO gate que decide si se encola la ida. nil ⇒ no se monta el callback.
	CRMGate CRMBridgeGate
	// CRMReflect aplica el estado canónico sobre la solicitud (ADR-0031: cuando HAY
	// CRM, el CRM manda). Lo satisface *intakes.Postgres. nil ⇒ no se monta.
	CRMReflect CRMReflector
	// CRMNotify avisa al cliente final del cambio reflejado (T4.4). OPCIONAL: sin
	// él el callback refleja igual y no escribe a nadie.
	CRMNotify CRMStatusNotifier
	// EventTelemetry lee el outbox append-only flow_events filtrado al
	// vocabulario de ciclo de vida (name LIKE 'event\_%'), Plan 043 · Ola 6 ·
	// T6.5, cierra MD-043.17. Lo satisface *PostgresEventTelemetryStore. nil ⇒
	// no se monta GET /api/v1/events/telemetry.
	EventTelemetry EventTelemetryReader
	// DBTimeout es el plazo de cada consulta a BD de estos handlers, cableado desde
	// config.PublicAPIDBTimeout (1,5s por defecto; Plan 050 · Ola 3). <=0 cae a
	// defaultDBTimeout — la promesa la cumple dbCtx, que es por donde pasan TODAS
	// las lecturas acotadas (T3.2/T3.3).
	DBTimeout time.Duration
}

// defaultDiagnosticsTTL es la retención del bundle cuando Deps.DiagnosticsBundleTTL
// llega en cero (mismo criterio defensivo que los umbrales de salud).
const defaultDiagnosticsTTL = 30 * time.Minute

// defaultDBTimeout es el SUELO del plazo de las lecturas a BD de estos handlers: el
// valor que se usa cuando Deps.DBTimeout llega en cero o negativo. Es el mismo
// número que config.PublicAPIDBTimeout trae por defecto, y está aquí a propósito:
// los docstrings de los dos prometen que «<=0 cae al default» y el cargador de
// config NO normaliza, así que la promesa la tiene que cumplir el consumidor. Sin
// esto, unas Deps con el campo sin cablear —un test, un bootstrap futuro que lo
// olvide— darían un context.WithTimeout de 0 y TODA lectura moriría en el acto.
const defaultDBTimeout = 1500 * time.Millisecond

// dbCtx deriva del contexto de la petición el contexto ACOTADO con el que se
// consulta a Postgres (Plan 050 · Ola 3, T3.2/T3.3). El llamante DEBE hacer
// `defer cancel()`.
//
// POR QUÉ HACE FALTA UN RELOJ PROPIO y no basta r.Context(): el contexto de un
// handler HTTP no trae plazo. El WriteTimeout del http.Server NO interrumpe al
// handler ni cancela su contexto —solo hace fallar el Write posterior—, de modo que
// una consulta contra una base lenta espera indefinidamente y el cliente se queda
// con la conexión cerrada, sin cuerpo y sin una sola línea de log. El razonamiento
// completo, con el incidente que lo costó, ya está escrito UNA vez y no se copia
// aquí (una copia diverge del original en cuanto uno de los dos cambie):
//
//   - internal/gateway/grpc/send.go:299 (awaitAck) — el incidente del 2026-08-06.
//   - internal/platform/config/config.go (GRPCAckTimeout y PublicAPIDBTimeout) — la
//     invariante contra el WriteTimeout: los relojes del envío son SECUENCIALES, así
//     que contra los 10s cuenta la SUMA. Son TRES, no dos: ese comentario decía dos
//     y la cuenta era falsa hasta que T5.4 la rehizo (ver allí).
//   - SendBudgetFrom, aquí abajo — el techo que hace que esa suma ya no pueda dejar
//     al cliente sin respuesta.
//
// Solo lo usan LECTURAS. Las escrituras y las transacciones quedan fuera a
// propósito: 1,5s está calibrado para una consulta previa al envío, y aplicárselo a
// un import de catálogo o a un ReplaceItems los abortaría a media transacción bajo
// carga — un cambio de comportamiento con riesgo, no una mejora.
func dbCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultDBTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// margenDeEscritura es lo que SendBudgetFrom reserva del WriteTimeout para que el
// handler pueda serializar y escribir su respuesta después de rendirse. Es el único
// número nuevo del arreglo de REQ-050.19, y lo es a propósito: es el parámetro de una
// fórmula, no una copia de una suma. Un presupuesto escrito como constante suelta
// habría sido la cuarta cifra que alguien tiene que rehacer a mano cada vez que se
// mueva un reloj — exactamente el mecanismo por el que la invariante de config.go se
// desincronizó y acabó documentando un margen que no existía.
//
// POR QUÉ 1s Y NO MÁS. Este valor es, exactamente, la franja de envíos que cambian de
// desenlace: los que tardan entre el presupuesto y el WriteTimeout hoy alcanzan a
// responder y a partir de ahora se cortan. Estrecharla es el objetivo, y el trabajo
// que tiene que caber dentro es minúsculo —serializar ~150 bytes de JSON y escribirlos
// en una conexión ya abierta—: el camino feliz completo de este endpoint, medido de
// extremo a extremo en el e2e de T5.4, tarda 0,63 ms. 1s son tres órdenes de magnitud
// de holgura sobre eso, suficientes para absorber un GC o un scheduler cargado, y la
// mitad de los ~2s que el comentario viejo de GRPCAckTimeout daba por supuestos.
//
// 🔴 Lo que NO se puede hacer es bajarlo a cero: sin margen, el handler se rendiría
// justo cuando el deadline de escritura vence y volveríamos a la respuesta vacía.
const margenDeEscritura = 1 * time.Second

// SendBudgetFrom DERIVA el presupuesto de una petición de envío del WriteTimeout del
// servidor HTTP que la sirve (Plan 050 · Ola 5 · T5.4, cierra REQ-050.19). Es la
// respuesta al defecto que documenta config.PublicAPIDBTimeout: los relojes de abajo
// —guarda de tenant, empuje, espera del ack— son SECUENCIALES y su suma (19,5s) se
// pasa del WriteTimeout (10s), de modo que un Edge saturado dejaba al cliente con la
// conexión cerrada y sin cuerpo.
//
// 🔴 SE DERIVA, NO SE INVENTA. Ningún reloj existente se mueve (INV-050.6): lo que se
// añade es QUIÉN ESPERA. El presupuesto es un techo por encima de los tres, y por ser
// el más corto es el que gana. Al derivarlo del WriteTimeout, mover el WriteTimeout lo
// arrastra y la aritmética no puede volver a mentir; los relojes de abajo pueden
// incluso crecer sin que el cliente se quede sin respuesta, porque quien manda es el
// techo y no la suma.
//
// Un writeTimeout que no deje sitio al margen devuelve 0 = SIN presupuesto, que es el
// comportamiento previo a esta tarea: es preferible a un plazo ya vencido al nacer,
// que abortaría TODOS los envíos en el acto.
func SendBudgetFrom(writeTimeout time.Duration) time.Duration {
	if writeTimeout <= margenDeEscritura {
		return 0
	}
	return writeTimeout - margenDeEscritura
}

// sendCtx deriva el contexto ACOTADO de una petición de envío. El llamante DEBE hacer
// `defer cancel()`. Un presupuesto <=0 devuelve el contexto tal cual (sin plazo): no
// hay suelo local a propósito, porque un suelo aquí sería justo la constante suelta
// que SendBudgetFrom existe para no crear — el valor viene derivado de quien conoce el
// WriteTimeout, y ese es el bootstrap.
//
// ⚠️ Cubre el handler ENTERO —guarda de tenant incluida—, no solo el envío: la suma
// que se pasaba del WriteTimeout incluye la guarda. Que el plazo de dbCtx (1,5s) sea
// mucho más corto no lo hace redundante: dbCtx acota UNA consulta, esto acota la
// petición.
//
// ⚠️ Cancelar este contexto NO cancela el envío ya en vuelo: la goroutine del Send del
// Registry sobrevive y el comando puede salir hacia el Edge DESPUÉS de que el llamante
// haya recibido su error (session.Push, «Enmienda 1, regla 1»; verificado en el e2e de
// T5.4). Eso es lo que hace seguro este plazo —no se pierde ningún mensaje— y lo que
// obliga a que los textos de error de writeSendError digan «no se sabe si salió» en
// vez de invitar a reintentar.
func sendCtx(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

// dbTimedOut504 traduce el VENCIMIENTO del plazo de dbCtx: si err es el deadline,
// registra el hecho (Warn) con los campos dados, responde 504 con `motivo` y
// devuelve true para que el handler corte. Con cualquier otro error —o con ninguno—
// devuelve false y el handler sigue con el desenlace que ya tenía: esta función NO
// altera el comportamiento de ningún error preexistente.
//
// 504 y no 500 porque el llamante necesita distinguir «no pude» de «no me dio
// tiempo»: lo segundo es transitorio y reintentar sirve. Y a diferencia del 504 del
// Ack (writeSendError / msgStreamCaido), aquí NO hay ambigüedad sobre lo que pasó:
// estas consultas ocurren ANTES de tocar al Edge, así que nada salió hacia WhatsApp
// y reintentar no puede duplicarle un mensaje a nadie. Por eso estos textos SÍ
// pueden decir «reintenta»: es una acción que el llamante puede ejecutar de verdad,
// no un consejo que no le sirve de nada.
//
// Los campos del log son SIEMPRE identificadores opacos (tenant_id, session_id,
// command_id): CERO PII —ni destino ni texto—, como el resto de los logs de esta
// capa. Un logger nil no es un error: se responde igual, solo que mudo.
func dbTimedOut504(w http.ResponseWriter, log sharedlogger.Logger, err error, motivo string, campos ...any) bool {
	if !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if log != nil {
		log.Warn("lectura a BD vencida: se responde 504", campos...)
	}
	writeError(w, http.StatusGatewayTimeout, motivo)
	return true
}

// Register monta las rutas /api/v1 de operación pública en el mux del listener
// público (:8103), reutilizando el middleware de T3 (mw) y el auditor de T4. Cada
// ruta pasa por Authenticate → RequirePermission(scope); las ESCRITURAS añaden
// AuditMiddleware (las lecturas no se auditan: son idempotentes y sin efecto). Los
// patrones método+ruta (Go 1.22+) devuelven 405 automáticamente ante otro método y
// extraen {id} con r.PathValue. No colisiona con las rutas IAM (/api/v1/auth,
// /api/v1/users, …) ya montadas en el mismo mux.
//
// Extensible: T6 añadirá /api/v1/media/upload-url y /api/v1/tenant-content aquí
// mismo (mismo patrón protect/protectRead), sin tocar lo existente.
func Register(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	// Envío de mensajes (escritura auditada). Reusa el gateway; añade el guardia
	// session→tenant que /admin/messages/send (T4) no tenía.
	mux.Handle("POST /api/v1/messages", protect(mw, auditor, log,
		"messages.send", "message", messagesHandler(d.Sender, d.Sessions, d.DBTimeout, d.SendBudget, log)))

	// Publicar definición de flujo (escritura auditada). Reusa TAL CUAL el handler
	// de /admin/flows: ya toma el tenant del token y valida el esquema.
	mux.Handle("POST /api/v1/flows", protect(mw, auditor, log,
		"flows.create", "flow", flowadmin.DefinitionHandler(d.Flows, d.Modules)))

	// Listar / leer definiciones (lecturas, sin auditoría).
	mux.Handle("GET /api/v1/flows", protectRead(mw, log,
		"flows.read", listFlowsHandler(d.Flows)))
	mux.Handle("GET /api/v1/flows/{id}", protectRead(mw, log,
		"flows.read", getFlowHandler(d.Flows)))

	// Arrancar una conversación de un flujo (escritura auditada). flow_id va en la
	// ruta (design.md §8); el resto (session_id, contacto) en el cuerpo. Reusa el
	// motor de flujos (Starter) que también sirve /admin/flows/start.
	mux.Handle("POST /api/v1/flows/{id}/start", protect(mw, auditor, log,
		"flows.start", "flow", startFlowHandler(d.Starter)))

	// Media por API (escritura auditada, R7): presigna una URL PUT de corta vida
	// para subir a R2 un archivo (PDF/imagen) que luego se referencia en un flujo
	// (nodo media / tenant_content). Reusa el PresignClient del Plan 017; el objeto
	// se namespacea por tenant (INV-8). CERO PII en la auditoría (action/resource).
	mux.Handle("POST /api/v1/media/upload-url", protect(mw, auditor, log,
		"media.upload", "media", uploadURLHandler(d.Media)))

	// CRUD de contenido dinámico por-tenant (tenant_content, R7): blobs JSONB que
	// alimentan el adapter content.JSON del Motor (source:json,ref) o una ref de
	// media por-tenant. Escrituras auditadas (content.write); lecturas sin auditoría
	// (content.read). Todo acotado al tenant del token (INV-8). Sin cambios en el Motor.
	mux.Handle("PUT /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", upsertTenantContentHandler(d.Content, d.ContentMaxBytes)))
	mux.Handle("POST /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", upsertTenantContentHandler(d.Content, d.ContentMaxBytes)))
	mux.Handle("DELETE /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", deleteTenantContentHandler(d.Content)))
	mux.Handle("GET /api/v1/tenant-content", protectRead(mw, log,
		"content.read", listTenantContentHandler(d.Content)))
	mux.Handle("GET /api/v1/tenant-content/{ref}", protectRead(mw, log,
		"content.read", getTenantContentHandler(d.Content)))

	// CRUD de reglas de disparo (Plan 019 · T5): keyword/fallback/escape por-tenant
	// que alimentan al ConfigResolver del Motor. Escrituras auditadas
	// (triggers.create/delete); lectura sin auditoría (triggers.read). Todo acotado
	// al tenant del token (INV-8); reusa los MISMOS handlers que /admin/triggers.
	if d.Triggers != nil {
		mux.Handle("POST /api/v1/triggers", protect(mw, auditor, log,
			"triggers.create", "trigger", flowadmin.CreateTriggerHandler(d.Triggers, d.TriggersDurableFlow)))
		mux.Handle("GET /api/v1/triggers", protectRead(mw, log,
			"triggers.read", flowadmin.ListTriggersHandler(d.Triggers, d.TriggersDurableFlow)))
		mux.Handle("DELETE /api/v1/triggers/{id}", protect(mw, auditor, log,
			"triggers.delete", "trigger", flowadmin.DeleteTriggerHandler(d.Triggers, d.TriggersDurableFlow)))
	}

	// Listar las sesiones/teléfonos vinculados del tenant (Plan 021 · T0, R-A1).
	// Lectura sin auditoría (idempotente), acotada al tenant del token (INV-8):
	// fleet.List filtra por tenant, así que una sesión ajena NUNCA aparece. Reusa
	// el MISMO SessionLister que ya alimenta el aislamiento del envío (sin nueva
	// dependencia). Solo expone metadatos de operación (CERO credenciales/PII).
	if d.Sessions != nil {
		alerter := d.Alerter
		if alerter == nil {
			alerter = NoopAlerter{} // ADR-0023: seam del alerting push, no-op por defecto.
		}
		mux.Handle("GET /api/v1/sessions", protectRead(mw, log,
			"sessions.read", listSessionsHandler(d.Sessions, d.Health, alerter, d.DBTimeout, log)))
	}

	// PERFIL de sesión active|passive (Plan 046 · T1.2, ADR-0027): sucede a /role con
	// el vocabulario del dueño. MISMO scope (sessions.write), MISMA auditoría y MISMO
	// aislamiento al tenant del token (INV-8); reusa el MISMO handler que
	// /admin/sessions/{id}/profile. El push al Edge es best-effort y hoy va apagado
	// (d.ProfilePush nil ⇒ no-op; lo enchufa T2.1).
	if d.SessionProfiles != nil {
		mux.Handle("POST /api/v1/sessions/{id}/profile", protect(mw, auditor, log,
			"sessions.write", "session",
			flowadmin.SetSessionProfileHandler(d.SessionProfiles, d.ProfilePush, log)))
	}

	// Diagnóstico remoto bajo demanda (Plan 031 · T5, ADR-0023 capa 3). POST emite un
	// DiagnosticsRequest a la sesión {id} (gate de consentimiento por tenant default
	// ON ⇒ 403 si opt-out; aislamiento session→tenant ⇒ 404); GET descarga el bundle
	// por command_id (202 pendiente / 200 listo / 410 expirado / 404 no encontrado).
	// AMBAS rutas exigen el grant diagnostics.request y se AUDITAN (protect: la descarga
	// se audita a propósito, es una lectura sensible). Solo se montan con el store, el
	// emisor y el listador de sesiones cableados.
	if d.Diagnostics != nil && d.DiagnosticsRequester != nil && d.Sessions != nil {
		ttl := d.DiagnosticsBundleTTL
		if ttl <= 0 {
			ttl = defaultDiagnosticsTTL
		}
		mux.Handle("POST /api/v1/sessions/{id}/diagnostics", protect(mw, auditor, log,
			"diagnostics.request", "session",
			requestDiagnosticsHandler(d.DiagnosticsRequester, d.Diagnostics, d.Sessions, ttl, d.DBTimeout, log)))
		mux.Handle("GET /api/v1/diagnostics/{command_id}", protect(mw, auditor, log,
			"diagnostics.request", "diagnostics",
			getDiagnosticsHandler(d.Diagnostics, d.DBTimeout, log)))
	}

	// Estatus de sesión (Plan 020 · T3): retirar/limpiar un zombie (loggedout) o
	// dejar offline. Escritura auditada (sessions.write), acotada al tenant del token
	// (INV-8); reusa el MISMO handler que /admin/sessions/{id}/status.
	if d.SessionStatus != nil {
		mux.Handle("POST /api/v1/sessions/{id}/status", protect(mw, auditor, log,
			"sessions.write", "session", flowadmin.SetSessionStatusHandler(d.SessionStatus)))
	}

	// Derechos comerciales del tenant (Plan 040 · T2.2, ADR-0022/ADR-0033): plan
	// efectivo + features encendidas + el TTL con el que se cachean. Lectura sin
	// auditoría, acotada al tenant del token (INV-8): NO hay consulta cross-tenant,
	// el tenant no viaja en la URL. Scope entitlements.read — que tenant_admin ('*')
	// y viewer ('*.read') ya cubren por glob, y operator recibe explícito en la
	// migración 0040 (design §D-040.4). Toda UI que pinte capacidades depende de
	// esta ruta.
	if d.Entitlements != nil {
		mux.Handle("GET /api/v1/entitlements", protectRead(mw, log,
			"entitlements.read", listEntitlementsHandler(d.Entitlements)))
	}

	// Config del clasificador de intenciones por-tenant (Plan 029 · T5, ADR-0020/
	// 0021/0022). GET lee el blob vigente (intents.read); PUT valida el contrato
	// (wapp-shared/intents), exige la feature llm_intent (gate de verdad ⇒ 403 sin
	// ella), persiste y empuja el ConfigUpdate a las sesiones vivas del tenant. Todo
	// acotado al tenant del token (INV-8). Escritura auditada (intents.write); lectura
	// sin auditoría (intents.read). Solo se montan si el store y el checker están
	// cableados (fase pre-release).
	if d.Intents != nil && d.Entitlements != nil {
		mux.Handle("GET /api/v1/intents", protectRead(mw, log,
			"intents.read", getIntentsHandler(d.Intents, d.DBTimeout, log)))
		mux.Handle("PUT /api/v1/intents", protect(mw, auditor, log,
			"intents.write", "intents", putIntentsHandler(d.Intents, d.Entitlements, d.ConfigPush, log)))
	}

	// Bandeja de SOLICITUDES (Plan 041, ADR-0031). Va en su propia función: las
	// rutas de la bandeja crecen con el plan (export, summary) y aquí solo se ve
	// que existen.
	registerIntakes(mux, d, mw, auditor, log)

	// Bandeja de EVENTOS conversacionales (Plan 043 · T3.9b/T4.2, REQ-28). Misma
	// razón para vivir aparte: sus `if` de montaje sumarían más a la complejidad
	// de Register, que ya roza el techo del linter (gocyclo 15).
	registerConversationEvents(mux, d, mw, auditor, log)

	// Variables de empresa (Plan 041 · T2.1, D-041.1). También en su propia
	// función: no por tamaño, sino porque el `if` de montaje sumaría uno más a la
	// complejidad de Register, que ya roza el techo del linter (gocyclo 15).
	registerTenantVariables(mux, d, mw, auditor, log)
	registerIntegrations(mux, d, mw, auditor, log)
	registerTenantLLM(mux, d, mw, auditor, log)
	registerDegradationNotices(mux, d, mw, log)
	registerCRMCallback(mux, d, log)

	// Import de catálogo (Plan 041 · T3.3, D-041.6). Misma razón para vivir aparte:
	// su montaje tiene tres condiciones y aquí solo se ve que existe.
	registerCatalogImport(mux, d, mw, auditor, log)

	// Lectura de la bitácora de auditoría (Plan 018 · T10, R11). Paginada, acotada
	// al tenant del token (INV-8); scope audit.read (o *.read del rol viewer).
	// Lectura sin auditoría (no tiene efecto). Los eventos ya son OPACOS (CERO PII).
	if d.Audit != nil {
		mux.Handle("GET /api/v1/audit", protectRead(mw, log,
			"audit.read", listAuditHandler(d.Audit)))
	}

	// Telemetría de ciclo de vida del evento conversacional (Plan 043 · Ola 6 ·
	// T6.5, cierra MD-043.17). Propia función por la misma razón que
	// registerConversationEvents/registerTenantVariables: un `if` más aquí
	// rozaría el techo del linter (gocyclo 15).
	registerEventTelemetry(mux, d, mw, log)
}

// registerIntakes monta la bandeja de SOLICITUDES (Plan 041 · T1.1/T1.4/T4.10,
// ADR-0031): listado con filtros y paginación, detalle con líneas, transición del
// ciclo de vida (D-041.10) y edición manual de las líneas del presupuesto
// (D-041.26). Todo acotado al tenant del token (INV-8): una solicitud ajena
// responde 404, nunca 403 — un 403 confirmaría que el id existe.
//
// DOS guardias por ruta, y ninguno sustituye al otro: el scope
// (intakes.read/intakes.write) dice "puedes operar esto"; la feature dice "tu plan
// lo incluye". RequireFeature se compone SIEMPRE después de Authenticate y
// RequirePermission — antes no habría identidad de la que sacar el tenant y el
// gate cortaría fail-closed a todo el mundo.
//
// DOS features distintas, no una: `cart_basic` abre la bandeja (ver los pedidos);
// `intakes_export` abre el sacarlos del sistema —CSV/XLSX y summary.json—, que es
// una capacidad comercial aparte y se vende aparte (taxonomía del Plan 040). Un
// tenant puede tener la primera y no la segunda; por eso el gate del export NO
// hereda del de la lista.
//
// El scope de las tres LECTURAS es el mismo (intakes.read): el export no enseña
// nada que la bandeja no enseñe ya, solo lo empaqueta.
//
// Sin el servicio o sin el resolver de features las rutas NO se montan: es
// preferible un 404 de ruta inexistente a una bandeja que responde 500 a medio
// camino (o, peor, que se abre sin poder comprobar el plan).
//
// Las rutas literales (…/export, …/summary.json) conviven con …/{id} sin ambigüedad:
// el mux de Go 1.22+ prefiere el patrón MÁS específico, y un segmento literal lo es
// más que un comodín.
func registerIntakes(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.Intakes == nil || d.Entitlements == nil {
		return
	}
	cartBasic := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCartBasic)
	canExport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureIntakesExport)
	llmIntake := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureLLMIntake)

	mux.Handle("GET /api/v1/intakes", protectRead(mw, log,
		"intakes.read", cartBasic(listIntakesHandler(d.Intakes))))
	mux.Handle("GET /api/v1/intakes/{id}", protectRead(mw, log,
		"intakes.read", cartBasic(getIntakeHandler(d.Intakes, d.Entitlements))))
	mux.Handle("POST /api/v1/intakes/{id}/status", protect(mw, auditor, log,
		"intakes.write", "intake", cartBasic(setIntakeStatusHandler(d.Intakes))))

	// Edición MANUAL de las líneas del presupuesto (Plan 041 · T4.10, REQ-36 /
	// D-041.26): el dueño añade, quita o corrige líneas de una solicitud en
	// `pending_approval` SIN LLM de por medio, y cada edición deja su revisión
	// `corrected`. Va con `cart_basic` y NO con la feature del pipeline del 044:
	// re-presupuestar es del OBJETO, no de la máquina que lo redacta sola — un
	// tenant sin LLM que llegara a `pending_approval` sin poder editar se quedaría
	// encerrado en un estado editable que nadie puede editar.
	mux.Handle("PUT /api/v1/intakes/{id}/items", protect(mw, auditor, log,
		"intakes.write", "intake", cartBasic(putIntakeItemsHandler(d.Intakes, d.Entitlements))))

	// APROBAR el presupuesto (Plan 044 · Ola 4 · T4.3, D-044.49): el dueño manda su
	// cotización, el cliente la recibe por WhatsApp y la solicitud queda `confirmed`
	// con su revisión `approved`.
	//
	// Va con `cart_basic` y NO con `llm_intake`, y es la MISMA razón que el 041 dejó
	// escrita tres líneas más arriba para el `PUT …/items` (D-044.49 §3): cobrar
	// aprobar con la feature del pipeline dejaría a un tenant `Basic` —plan REAL en
	// UAT— corrigiendo líneas que después no puede aprobar. Re-presupuestar y aprobar
	// son del OBJETO; la máquina que redacta el borrador sola es lo que se vende
	// aparte, y eso ya lo cubre el gate POR CAMPO de T4.1 (intakes_llm_gate.go).
	//
	// Auditada como escritura (`intakes.write`), como sus hermanas: es la escritura
	// que le habla al cliente, así que tiene que constar quién la disparó.
	mux.Handle("POST /api/v1/intakes/{id}/approve", protect(mw, auditor, log,
		"intakes.write", "intake", cartBasic(approveIntakeHandler(d.Intakes, d.Entitlements))))

	// PEDIR MÁS INFORMACIÓN (Plan 044 · Ola 4 · T4.4, D-044.49 §2): el dueño manda su
	// pregunta —la que el sistema le sugirió, editada por él— y la solicitud queda en
	// `needs_info` esperando la respuesta del cliente.
	//
	// Mismo gate `cart_basic` que `approve` y por el MISMO argumento (D-044.49 §3):
	// preguntarle algo al cliente es del objeto, no de la máquina que redacta el
	// borrador. Cobrarlo con `llm_intake` dejaría a un tenant Basic con un presupuesto
	// que no entiende y sin poder preguntar por qué.
	//
	// La ACCIÓN «Corregir» de esa misma tarea NO tiene ruta aquí, y no falta: es el
	// `PUT …/items` de arriba con `"as_correction": true` (D-044.48 §1). Dos rutas
	// dejando la misma revisión `corrected` era el duplicado que este plan ya pagó.
	mux.Handle("POST /api/v1/intakes/{id}/request-info", protect(mw, auditor, log,
		"intakes.write", "intake", cartBasic(requestInfoIntakeHandler(d.Intakes, d.Entitlements))))

	// RE-ANALIZAR desde el origen (Plan 044 · Ola 4 · T4.6, D-044.15, contrato
	// completo en design §8.1): el dueño pide que la máquina vuelva a leer el pedido
	// a partir del literal cifrado del evento, y el pipeline le cuelga una revisión
	// más. NO responde al cliente y NO transiciona la solicitud.
	//
	// 🔴 ES LA ÚNICA RUTA DE LA BANDEJA SIN GATE EN LA CADENA DE MIDDLEWARES, Y ESO
	// ES DELIBERADO. No falta `cartBasic(...)` ni un `RequireFeature(llm_intake)`:
	// están DENTRO, en `reanalisis.Servicio`. Las dos razones, en orden:
	//
	//  1. **El contrato manda que el 400 vaya PRIMERO.** El §8.1 lo dice con todas
	//     las letras para el `invalid_via`: «Validación de forma, antes de cualquier
	//     gate». Un middleware corre ANTES del handler por definición, así que con el
	//     gate aquí un cuerpo con `{"via":"chatgpt"}` de un tenant sin `llm_intake`
	//     respondería 403 en vez de 400 — y el orden del contrato dejaría de ser
	//     verdad sin que ningún test de esta ruta lo viera.
	//  2. **El segundo gate DEPENDE de la vía efectiva**, que solo se conoce después
	//     de leer `tenant_llm`. `api_llm` gatea LA VÍA y no la capacidad (ADR-0044 ·
	//     D-044.28), así que preguntarlo aquí arriba —donde todavía no se sabe si la
	//     vía es `api`— cerraría la puerta a todo tenant de vía local, que es
	//     exactamente el invariante que vigilan
	//     internal/flujos/runtime/via_local_sin_api_llm_test.go y
	//     internal/publicapi/tenantllm_gate_via_test.go.
	//
	// Lo que NO cambia es el cuerpo: el 403 de dentro es LITERALMENTE
	// `{"error":"feature_not_enabled","feature":"…"}`, el mismo del middleware
	// (design §D-040.5), para que la UI lo trate en un solo sitio. Y lo que sostiene
	// que el gate no se pierda es un test de conducta, no este comentario:
	// TestReanalyze_SinLLMIntake_403 y TestReanalyze_ViaInvalidaGanaAlGate.
	//
	// `cart_basic` NO aplica aquí y tampoco es un olvido (D-044.49, último párrafo):
	// aprobar y corregir son del OBJETO —por eso van con `cart_basic`—, pero
	// re-analizar es literalmente la máquina que se vende aparte. «Ahí el LLM sí es
	// el servicio que se presta.»
	//
	// Auditada como escritura (`intakes.write`, recurso `intake`) como sus hermanas:
	// abre trabajo en la cola del pipeline y acaba en una revisión nueva del pedido,
	// así que tiene que constar quién lo disparó.
	if d.Reanalysis != nil {
		mux.Handle("POST /api/v1/intakes/{id}/reanalyze", protect(mw, auditor, log,
			"intakes.write", "intake", reanalyzeIntakeHandler(d.Reanalysis)))
	}

	// SUGERIR LA COTIZACIÓN con la voz de la dueña (Plan 044 · Ola 5 · T5.1,
	// D-044.11): la máquina redacta el texto y lo DEVUELVE; el dueño lo edita y lo
	// aprueba por `POST …/approve`, que sigue exigiendo su `rendered_text`.
	//
	// 🔴 ES LA ÚNICA RUTA DE LA BANDEJA CON `llm_intake` EN LA CADENA, y no contradice
	// a sus vecinas: aprobar, corregir y preguntar son del OBJETO (por eso van solo con
	// `cart_basic`), y esto es literalmente «la máquina que redacta el borrador sola»,
	// que es lo que D-044.49 §3 dice que se vende aparte. Van las DOS features porque
	// también se opera sobre un pedido: un tenant con `llm_intake` y sin `cart_basic`
	// no tiene bandeja donde poner el texto.
	//
	// Auditada como LECTURA (`intakes.read`) y no como escritura, porque no lo es: no
	// escribe una fila, no transiciona nada y no le manda nada al cliente. Lo que sí
	// hace es gastar una inferencia, y por eso es POST — ver el fichero del handler.
	if d.QuoteSuggestions != nil {
		mux.Handle("POST /api/v1/intakes/{id}/quote-suggestion", protectRead(mw, log,
			"intakes.read", cartBasic(llmIntake(quoteSuggestionHandler(d.QuoteSuggestions)))))
	}

	// DESCARTE MANUAL por lotes del pedido huérfano (Plan 041 · T4.8, REQ-32 /
	// D-041.18). Ruta LITERAL bajo /intakes y no bajo /intakes/{id}: la operación es
	// del LOTE, no de una solicitud, y colgarla de un id obligaría a N llamadas —
	// justo lo que la tarea existe para evitar. El mux de Go 1.22+ prefiere el
	// segmento literal, así que no compite con …/{id}/… (que además usan otros verbos).
	//
	// Mismo scope y misma feature que el resto de la bandeja: descartar es operar
	// SOBRE la bandeja, no una capacidad que se venda aparte. Auditado como escritura
	// (`intakes.write`) — y es la escritura de esta ola que MÁS falta hace en la
	// bitácora, porque no se puede deshacer.
	mux.Handle("POST /api/v1/intakes/discard", protect(mw, auditor, log,
		"intakes.write", "intake", cartBasic(discardIntakesHandler(d.Intakes))))

	// Export y resumen (Plan 041 · T1.2/T1.3, REQ-03/REQ-04).
	mux.Handle("GET /api/v1/intakes/export", protectRead(mw, log,
		"intakes.read", canExport(exportIntakesHandler(d.Intakes))))
	mux.Handle("GET /api/v1/intakes/summary.json", protectRead(mw, log,
		"intakes.read", canExport(intakeSummaryHandler(d.Intakes))))
}

// registerConversationEvents monta la bandeja de EVENTOS conversacionales (Plan
// 043 · T3.9b/T4.2, REQ-28): el listado por el que el dueño limpia lo que la de
// solicitudes no alcanza, y la cancelación por id que ejecuta esa limpieza. Todo
// acotado al tenant del token (INV-8); la lectura sin auditoría, como el resto de
// las lecturas; la cancelación auditada, como el resto de las escrituras.
//
// DOS decisiones que conviene no deshacer sin leer esto:
//
// Los SCOPES son los de la bandeja de solicitudes —`intakes.read` para leer,
// `intakes.write` para cancelar—, y no un `events.read`/`events.write` nuevo. Un
// scope nuevo no lo tiene nadie hasta que una migración se lo conceda al rol
// `operator` (así nacieron sessions.read en la 0030, entitlements.read en la 0040
// e intakes.read en la 0042), así que estrenarlo aquí dejaría la ruta montada y
// devolviendo 403 a la única persona que la necesita. `intakes.write` ya está
// concedido a operator (0042) y su precedente exacto es POST /api/v1/intakes/
// discard: cancelar un evento ES operar la limpieza de la bandeja, la misma faena
// del turno. Lo que sí es nuevo es el RECURSO de auditoría (`conversation_event`):
// la bitácora distingue qué se canceló, aunque el permiso sea el mismo. Además las
// dos bandejas son la misma tarea partida en dos: esta enseña exactamente lo que a
// la otra se le escapa.
//
// La FEATURE no es una: son las CUATRO de los tipos de fábrica, y basta tener UNA
// (decisión de Jhoan, 2026-08-09). El plan pedía «el mismo gate que …/cancel»
// (D-043.8), pero ese endpoint todavía NO EXISTE —es T4.2, de la Ola 4—, así que
// aquí no se copiaba un gate: se elegía el primero. Se descartó `cart_basic` —el de
// la bandeja de solicitudes— por lo que dejaba fuera: esta lista abarca menu, cart,
// survey y media, y gatearla con la del carrito habría cegado a un tenant de solo
// encuestas sobre sus PROPIAS encuestas.
//
// El gate abre la puerta; lo que se ve dentro lo acota events.AllowedKinds en el
// handler, con el MISMO mapa tipo→feature del despachador. Las dos mitades son
// necesarias: sin la primera un tenant sin nada entraría; sin la segunda, entrar
// por `survey` enseñaría también los carritos.
//
// POST …/conversation-events/{id}/cancel (T4.2) usa EXACTAMENTE este gate (y el
// filtro por tipos sobre el id que cancela, dentro del handler). Un segundo
// criterio dejaría al dueño viendo eventos que no puede cerrar, o cerrando los
// que no ve — por eso el evento de un tipo sin feature responde el MISMO 404 que
// el inexistente: un tipo que no ves en el listado no existe para ti.
//
// Sin el resolver de features NADA se monta, y cada ruta exige además su propia
// dependencia (lister/canceller): mejor un 404 de ruta inexistente que una
// bandeja que se abre sin poder comprobar el plan. Los `if` son independientes a
// propósito — el cableado puede traer el listado sin el canceller (así vivió
// entre la Ola 3 y la 4) o al revés, y la mitad presente debe funcionar.
func registerConversationEvents(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.Entitlements == nil || (d.ConversationEvents == nil && d.EventCanceller == nil) {
		return
	}
	algúnTipo := entitlements.RequireAnyFeature(d.Entitlements, events.KindFeatures()...)
	if d.ConversationEvents != nil {
		mux.Handle("GET /api/v1/conversation-events", protectRead(mw, log,
			"intakes.read", algúnTipo(listConversationEventsHandler(d.ConversationEvents, d.Entitlements))))
	}
	if d.EventCanceller != nil {
		mux.Handle("POST /api/v1/conversation-events/{id}/cancel", protect(mw, auditor, log,
			"intakes.write", "conversation_event",
			algúnTipo(cancelConversationEventHandler(d.EventCanceller, d.Entitlements))))
	}
}

// registerTenantVariables monta las variables de empresa por-tenant (Plan 041 ·
// T2.1, D-041.1): pares clave→valor de texto que el tenant define y que wApp NO
// interpreta. GET lee el conjunto; PUT lo REEMPLAZA entero (ver
// putTenantVariablesHandler). Todo acotado al tenant del token (INV-8).
//
// MISMO scope que /api/v1/tenant-content (content.read / content.write), y no por
// parecido de nombre: es literalmente el mismo público. tenant_content guarda el
// contenido dinámico que el tenant redacta —catálogos, prompts— y estas variables
// son el resto de esa configuración de presentación (`moneda=Bs`). Quien puede
// reescribir el catálogo del tenant puede reescribir su símbolo de moneda; separar
// los scopes inventaría un permiso que nadie tiene sembrado y dejaría la pantalla
// inaccesible hasta una migración de grants. Reparto que hereda: tenant_admin ('*')
// escribe, viewer ('*.read') solo lee, operator NO escribe — correcto: cambiar la
// configuración de la empresa no es la faena del turno.
//
// SIN gate de feature, a diferencia de la bandeja: esto es CAPA TÉCNICA (ADR-0035),
// no una capacidad que se venda. Ponerle RequireFeature dejaría a un tenant del
// plan más simple sin poder decir cómo se llama su moneda.
//
// Escritura auditada (content.write) sin PII: se audita la acción, jamás claves ni
// valores. Sin store cableado las rutas NO se montan (404 de ruta inexistente,
// mejor que un 500 a medio camino).
func registerTenantVariables(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.TenantVariables == nil {
		return
	}
	mux.Handle("GET /api/v1/tenant-variables", protectRead(mw, log,
		"content.read", getTenantVariablesHandler(d.TenantVariables, d.DBTimeout, log)))
	mux.Handle("PUT /api/v1/tenant-variables", protect(mw, auditor, log,
		"content.write", "tenant_variables", putTenantVariablesHandler(d.TenantVariables)))
}

// registerIntegrations monta la CONFIGURACIÓN del puente CRM (Plan 042 · T5.1,
// design §5): GET / PUT / DELETE de /api/v1/integrations, más el
// GET /api/v1/integrations/outbox con el estado de la cola de entregas (T5.2, el
// panel del outbox que la pantalla del BFF no podía pintar porque nadie lo
// publicaba). Todo acotado al tenant del token (INV-8).
//
// DOS GUARDIAS, como en el resto de rutas de pago: el scope dice «puedes operar
// esto» y la feature `crm_bridge` dice «tu plan lo incluye» (D-042.8). Sin la
// feature son 403 los tres verbos, también el GET: la configuración del puente no
// es información que un tenant sin puente deba poder consultar. RequireFeature va
// SIEMPRE después de Authenticate y RequirePermission — antes no habría identidad
// de la que sacar el tenant y el gate cortaría fail-closed a todo el mundo.
//
// SCOPES PROPIOS (integrations.read / integrations.write), y aquí SÍ se justifican
// en vez de reusar content.* como hicieron las variables de empresa y el import.
// La diferencia no es de nombre: esta fila guarda el SECRETO de firma del puente y
// la URL a la que se entregan todos los pedidos del tenant. Quien pueda
// escribirla puede repuntar el destino a un host propio y quedarse con el flujo
// entero. Ese poder no puede venir incluido en «puede editar el catálogo».
//
// El reparto que resulta NO necesita migración de grants, y es el correcto:
// tenant_admin ('*') hace las tres; viewer ('*.read') solo lee; operator no
// alcanza ninguna —configurar un puente externo no es la faena del turno—.
//
// Escrituras auditadas (integrations.write / recurso `integration`) sin PII y sin
// el secreto: se audita la ACCIÓN, jamás el cuerpo.
//
// Sin store o sin resolver de features las rutas NO se montan: mejor un 404 de
// ruta inexistente que una configuración que responde 500 a medio camino o, peor,
// que se guarda sin poder comprobar el plan.
func registerIntegrations(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.Integrations == nil || d.Entitlements == nil {
		return
	}
	crmBridge := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCRMBridge)

	mux.Handle("GET /api/v1/integrations", protectRead(mw, log,
		"integrations.read", crmBridge(getIntegrationHandler(d.Integrations))))
	// El estado de la cola cuelga de la MISMA ruta y va con las mismas dos
	// guardias: es la otra mitad de la pantalla del puente (la configuración dice
	// a dónde se entrega; esto, si está llegando). Lectura pura, así que
	// protectRead sin auditar — mirar un contador no cambia nada.
	mux.Handle("GET /api/v1/integrations/outbox", protectRead(mw, log,
		"integrations.read", crmBridge(getOutboxHandler(d.Integrations))))
	mux.Handle("PUT /api/v1/integrations", protect(mw, auditor, log,
		"integrations.write", "integration", crmBridge(putIntegrationHandler(d.Integrations))))
	mux.Handle("DELETE /api/v1/integrations", protect(mw, auditor, log,
		"integrations.write", "integration", crmBridge(deleteIntegrationHandler(d.Integrations))))
}

// registerTenantLLM monta la CONFIGURACIÓN de la vía LLM API por tenant
// (Plan 044 · Ola 0 · T0.3, design §8): GET / PUT / DELETE de /api/v1/tenant-llm.
//
// Gate `api_llm` en las TRES rutas, incluida la lectura. Que el GET no devuelva
// la clave no lo convierte en inocuo: dice si el tenant tiene vía API y con qué
// proveedor, y eso es exactamente la información del add-on que el gate acota.
// Es el mismo criterio que aplica registerIntegrations a su GET.
//
// ⚠️ El cableado del gate es formalmente de T0.4 («403 sin api_llm»), y se hace
// aquí a propósito: montar un CRUD de CREDENCIALES sin gate, aunque fuera por una
// tarea, dejaría abierta una puerta que después habría que acordarse de cerrar.
// T0.4 lo hereda hecho por este lado y le queda su otra mitad (el gate
// `llm_intake` del pipeline).
//
// 🔴 Y ÉSTE ES EL ÚNICO SITIO DEL REPO DONDE `api_llm` DECIDE ALGO (ADR-0044,
// D-044.28, T1.5-1). No porque haya quedado suelto, sino porque es lo que la
// feature significa: gatea la VÍA —configurar y usar credenciales de un proveedor
// externo—, no la CAPACIDAD. El carril de captación (agregador, compositor, hilo,
// nacimiento del job) mira `llm_intake` y solo `llm_intake`, y un tenant que tenga
// la capacidad SIN la vía sigue recibiendo aquí los tres 403 de siempre: tener
// nivel no es tener cuenta de pago. Lo afirma tenantllm_gate_via_test.go.
//
// SCOPES PROPIOS (llm.read / llm.write) por el MISMO argumento que
// registerIntegrations, y no por simetría de nombres: esta fila guarda una
// credencial de pago de un proveedor externo, y quien pueda escribirla puede
// apuntar el gasto del tenant a una cuenta ajena. Ese poder no viene incluido en
// «puede editar el catálogo».
//
// Y como allí, el reparto que resulta NO necesita migración de grants:
// tenant_admin (`*`) y viewer (`*.read`) están sembrados con glob
// (0015_iam_roles.sql:65,73) y cubren las dos claves sin tocar nada; operator
// lleva lista EXPLÍCITA (0015:66, ampliada por la 0042) y no alcanza ninguna de
// las dos. Que operator quede fuera no es un descuido de los que avisa la 0057
// —donde un scope estrenado sin grant dejó la ruta devolviendo 403 a quien la
// necesitaba—: aquí el 403 es el reparto que se busca, porque configurar la
// credencial de pago del tenant no es la faena del turno.
//
// Escrituras auditadas (llm.write / recurso `tenant_llm`) sin la credencial y sin
// PII: se audita la ACCIÓN, jamás el cuerpo.
//
// Sin store o sin resolver de features las rutas NO se montan: mejor un 404 de
// ruta inexistente que un endpoint de credenciales que responde 500 a medio
// camino o, peor, que guarda una clave sin poder comprobar el plan.
func registerTenantLLM(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.TenantLLM == nil || d.Entitlements == nil {
		return
	}
	apiLLM := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureAPILLM)

	mux.Handle("GET /api/v1/tenant-llm", protectRead(mw, log,
		"llm.read", apiLLM(getTenantLLMHandler(d.TenantLLM))))
	mux.Handle("PUT /api/v1/tenant-llm", protect(mw, auditor, log,
		"llm.write", "tenant_llm", apiLLM(putTenantLLMHandler(d.TenantLLM))))
	mux.Handle("DELETE /api/v1/tenant-llm", protect(mw, auditor, log,
		"llm.write", "tenant_llm", apiLLM(deleteTenantLLMHandler(d.TenantLLM))))
}

// registerDegradationNotices monta la LECTURA de los avisos de degradación
// (Plan 044 · Ola 1.5 · T1.5-4, D-044.32, REQ-38, contrato en design.md §8.2):
// GET /api/v1/degradation-notices.
//
// 🔴 EL GATE ES `llm_intake`, NO `api_llm`, Y ESO ES LA DECISIÓN DE ESTA FUNCIÓN
// (ADR-0044, D-044.28, la misma doctrina que T1.5-1). Lo que se lee aquí es «tu
// captación asistida se degradó al Nivel A», y eso le pasa —y le importa— a
// CUALQUIER tenant con el nivel, use la vía que use. Gatear con `api_llm` dejaría
// sin sus propios avisos exactamente a los tenants de la vía LOCAL, que son los
// dueños de SEIS de los ocho motivos del vocabulario (`ollama_down`,
// `breaker_open`, `edge_offline`, `timeout` y, desde T1.6-6,
// `lease_invalid` y `edge_sin_capacidad`): tendrían el Ollama caído y una
// bandeja que responde 403. `api_llm` gatea la VÍA, no la capacidad, y este
// endpoint no es de la vía.
//
// SCOPE `llm.read`, el MISMO que ya usa el GET de /api/v1/tenant-llm, y a
// propósito: no se estrena clave nueva. Un scope recién inventado sin grant
// sembrado deja la ruta devolviendo 403 a quien la necesita (es lo que pagó la
// 0057), y aquí no hace falta correr ese riesgo — «leer la configuración LLM del
// tenant» y «leer los avisos de esa misma configuración» son la misma faena, y el
// reparto ya está resuelto: tenant_admin (`*`) y viewer (`*.read`) por glob
// (0015_iam_roles.sql:65,73), operator fuera por su lista explícita. Cero
// migración de grants.
//
// NO SE AUDITA (protectRead): es una lectura sin efecto, mismo criterio que las
// otras 21 rutas de lectura de la API pública. Sí queda en el access-log.
//
// Sin store o sin resolver de features la ruta NO se monta: un 404 de ruta
// inexistente es más honesto que una bandeja de avisos que responde 500 —o, peor,
// que responde 200 con la lista de otro porque el gate no se pudo resolver—.
func registerDegradationNotices(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, log sharedlogger.Logger) {
	if d.DegradationNotices == nil || d.Entitlements == nil {
		return
	}
	llmIntake := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureLLMIntake)

	mux.Handle("GET /api/v1/degradation-notices", protectRead(mw, log,
		"llm.read", llmIntake(listDegradationNoticesHandler(d.DegradationNotices, d.DBTimeout))))
}

// registerCRMCallback monta la VUELTA del puente CRM (Plan 042 · T4.2):
// POST /api/v1/integrations/callback.
//
// NO PASA POR `protect` NI POR `protectRead`, y esa es la diferencia con todo lo
// demás de este fichero: el callback no lleva JWT. Quien llama es el puente del
// cliente, un proceso que no tiene usuario ni sesión, y su credencial es la firma
// HMAC sobre el cuerpo crudo con el secreto del tenant (D-042.5). Meterlo bajo el
// middleware de sesión lo dejaría inalcanzable; darle un token lo convertiría en un
// usuario más, que es justo lo que el contrato evita.
//
// Las cuatro dependencias son obligatorias salvo el notificador: sin secreto no se
// puede autenticar, sin gate no se puede decidir y sin reflector no hay dónde
// escribir, así que faltando cualquiera la ruta NO se monta y responde 404 de ruta
// inexistente — mejor que un 500 a mitad de una puerta de autenticación.
func registerCRMCallback(mux *http.ServeMux, d Deps, log sharedlogger.Logger) {
	if d.CRMSecrets == nil || d.CRMGate == nil || d.CRMReflect == nil {
		return
	}
	// accessLog explícito: es la ÚNICA ruta pública que no pasa por protect/protectRead
	// (se autentica por firma HMAC del CRM, no por Context Token), así que sin esto
	// sería el agujero que deja el «toda petición deja rastro» a medias.
	mux.Handle("POST /api/v1/integrations/callback",
		accessLog(log, crmCallbackHandler(d.CRMSecrets, d.CRMGate, d.CRMReflect, d.CRMNotify, log)))
}

// registerCatalogImport monta el import de catálogo (Plan 041 · T3.2/T3.3,
// D-041.6): POST /api/v1/catalog/import?mode=validate|apply&ref=… con el documento
// como JSON crudo en el cuerpo, más las dos rutas que lo hacen usable sin leerse el
// contrato —GET .../import/template (la plantilla de ejemplo) y GET
// .../import/prompt (el prompt-plantilla para el LLM del dueño)—. La escritura va
// acotada al tenant del token (INV-8); la plantilla y el prompt son idénticos para
// todos y no tocan la BD.
//
// TRES GUARDIAS, Y NINGUNO SUSTITUYE A OTRO. El scope (content.write) dice
// "puedes tocar el contenido de este tenant"; la feature (catalog_import) dice "tu
// plan incluye cargarlo de golpe"; y la auditoría deja constancia. RequireFeature
// se compone SIEMPRE después de Authenticate y RequirePermission: antes no habría
// identidad de la que sacar el tenant y el gate cortaría fail-closed a todo el
// mundo.
//
// MISMO SCOPE QUE tenant-content, y por lo mismo que las variables de empresa: el
// import escribe exactamente donde escribe el PUT genérico (public.tenant_content),
// así que inventarle un scope propio no protegería nada nuevo y dejaría la ruta
// inaccesible hasta una migración de grants.
//
// content.write TAMBIÉN PARA mode=validate, que no escribe. Elegir el permiso
// según un parámetro de la URL haría que la autorización dependiera de una entrada
// del usuario, y bastaría cambiar una letra del query para pasar de la puerta
// laxa a la operación de escritura. Una ruta, un permiso: el más fuerte de los
// que puede ejercer.
//
// A DIFERENCIA de tenant-variables, esto SÍ lleva gate de feature: cargar el
// catálogo entero validado, con diff y versión, es una capacidad que se vende
// (taxonomía del Plan 040). Quien no la tenga sigue pudiendo escribir su blob a
// mano por PUT /api/v1/tenant-content/{ref}.
//
// Sin store de contenido, sin versionador o sin resolver de features las rutas NO
// se montan: mejor un 404 de ruta inexistente que un import que responde 500 a
// medio camino o, peor, que se aplica sin poder comprobar el plan.
func registerCatalogImport(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	if d.Content == nil || d.ContentVersions == nil || d.Entitlements == nil {
		return
	}
	canImport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCatalogImport)
	limits := catalogimport.Limits{
		MaxJSONBytes: tenantContentBytes(d.ContentMaxBytes),
		MaxItems:     d.ImportMaxItems,
	}
	mux.Handle("POST /api/v1/catalog/import", protect(mw, auditor, log,
		"content.write", "catalog_import",
		canImport(catalogImportHandler(d.Content, d.ContentVersions, limits))))

	// La planilla (T3.4, D-041.9) es la MISMA operación por otra puerta: sube el
	// CSV/XLSX que el dueño llenó en su hoja en vez del JSON del contrato. Va con
	// los mismos tres guardias y con el MISMO recurso de auditoría —lo que se hace
	// es lo mismo, y por dónde entró queda anotado en `source` de la versión, que es
	// donde de verdad significa algo.
	mux.Handle("POST /api/v1/catalog/import/tabular", protect(mw, auditor, log,
		"content.write", "catalog_import",
		canImport(catalogImportTabularHandler(d.Content, d.ContentVersions, limits))))

	// La plantilla y el prompt son LECTURA (content.read) y no tocan la BD: son el
	// contrato dicho de dos maneras. Cuelgan de este mismo registro —y por tanto
	// aparecen y desaparecen con el POST— porque son la primera mitad del mismo
	// acto: repartir la plantilla de un import que no está montado mandaría al
	// operador a llenarla para encontrarse un 404 al subirla (T3.2).
	mux.Handle("GET /api/v1/catalog/import/template", protectRead(mw, log,
		"content.read", canImport(catalogTemplateHandler())))
	mux.Handle("GET /api/v1/catalog/import/prompt", protectRead(mw, log,
		"content.read", canImport(catalogPromptHandler())))
}

// protect compone la cadena de una ESCRITURA pública: Authenticate → identidad del
// token; RequirePermission(perm) → scope glob; AuditMiddleware → bitácora sin PII
// (action=perm, resource). Espeja adminHandler de cmd/server (T4) para /api/v1.
func protect(mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger, perm, resource string, h http.Handler) http.Handler {
	h = httpapi.AuditMiddleware(auditor, perm, resource, log)(h)
	h = mw.RequirePermission(perm)(h)
	h = anotarTenant(h)
	return accessLog(log, mw.Authenticate(h))
}

// protectRead compone la cadena de una LECTURA pública: Authenticate →
// RequirePermission(perm). No audita (lectura sin efecto).
//
// Recibe el logger desde el Plan 050 · Ola 5 · T5.4 y no por simetría cosmética: sin
// él, las 21 rutas de lectura de la API pública quedarían fuera del access-log y el
// «toda petición deja rastro» de REQ-050.19 sería verdad solo para las escrituras.
func protectRead(mw *httpapi.Middleware, log sharedlogger.Logger, perm string, h http.Handler) http.Handler {
	return accessLog(log, mw.Authenticate(mw.RequirePermission(perm)(anotarTenant(h))))
}

// writeJSON serializa v como JSON con el código dado (mismo patrón que
// httpapi/flujos-admin). Ante fallo de codificación responde 500. El fallo de
// ESCRITURA se descarta: quien necesite enterarse usa writeJSONErr.
func writeJSON(w http.ResponseWriter, code int, v any) {
	//nolint:errcheck // el descarte es el contrato de writeJSON: quien necesite
	// enterarse del fallo de escritura llama a writeJSONErr, que lo devuelve.
	_ = writeJSONErr(w, code, v)
}

// writeJSONErr es writeJSON pero DEVUELVE el fallo de escritura en vez de
// tragárselo. Existe porque ese error silencioso fue lo que dejó el incidente del
// 2026-08-06 sin una sola línea de log: el handler creía haber respondido 200 y el
// cliente veía la conexión cerrada sin cuerpo. Un Write que falla es justo el
// evento que hay que registrar, no el que hay que ignorar.
func writeJSONErr(w http.ResponseWriter, code int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "codificando respuesta", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, werr := w.Write(body)
	return werr
}
