// Porta internal/flujos/runtime/runtime_engine.go @ e0159171

package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// FlowStore es el subconjunto SEGREGADO del almacén de flujos que el Runtime necesita (ISP,
// Plan 027 · Ola 2 · T9, cierra H12): el estado conversacional, la LECTURA de definiciones, la
// lectura de la solicitud abierta y la de los ajustes del tenant (los dos TTL, la bienvenida).
//
// NO incluye las ESCRITURAS de solicitudes, efectos ni resultados: esas las consume el
// PersistSink, de modo que el runtime declara solo lo que usa. Lo satisfacen sin adaptador
// *store.PostgresRepository y *store.MemoryRepository.
type FlowStore interface {
	store.ConversationStore
	store.DefinitionReader
	store.IntakeReader
	store.TenantSettingsReader
}

// Runtime orquesta el motor de flujos vivo (design.md §6): el arranque por API (Start), el
// avance por entrante (OnIncoming / HandleIncoming) y la muerte explícita del evento
// (CancelEventForTenant). Es seguro para uso concurrente.
//
// Lo que promete como pieza, y que cada método repite donde le toca:
//
//   - Serializa POR CONVERSACIÓN con un single-flight en memoria indexado por store.Key
//     (keyedmutex.go): dos operaciones sobre la misma terna (tenant, sesión, contacto) nunca
//     se solapan; claves distintas avanzan en paralelo.
//   - RT-4 · Persiste el estado ANTES de enviar (Save antes de Send). Si el envío falla, el
//     paso NO se reenvía: el estado ya avanzó. Se prefiere un texto perdido a un avance
//     duplicado.
//   - Todo su estado vivo es EN MEMORIA y sin broker (ADR-0003): el candado por clave, el
//     semáforo de entrantes, el contador de rachas y el registro de sesiones pasivas ya
//     anunciadas. Un reinicio lo vacía y no pasa nada.
//
// 🔴 Trampa T-1 de la fase: el proceso tiene que tener UN SOLO Runtime. Dos instancias (una
// para el Gateway y otra para la API) no dan ningún error: parten el candado, el limitador,
// las rachas y el semáforo, y dos turnos de la misma conversación dejan de excluirse.
//
// Los campos nacen en el verde (F8-04b); el valor cero NO es utilizable: se construye con New.
type Runtime struct{}

// DepositReminder evalúa si a un contacto hay que recordarle la seña de alguna solicitud suya
// (Plan 041 · T4.4, D-041.12). Lo satisface el recordatorio del dominio de solicitudes, o
// runtimehelpertest.DepositReminder en tests.
//
// Es un puerto, y no el tipo concreto, porque el motor no tiene por qué conocer el dominio de
// solicitudes para avisarle de que alguien habló.
//
// NO devuelve error a propósito: un recordatorio que no sale no puede tumbar el
// procesamiento del mensaje que el cliente acaba de mandar.
//
// RT-19 · 🔴 DEVUELVE LOS TEXTOS QUE MANDÓ (Plan 044 · T1.6, D-044.24), en el orden en que
// salieron. El recordatorio sale por el notificador de solicitudes, no por el envío del
// runtime, y ese dominio no conoce ni el evento conversacional ni el resolver de features: el
// recordatorio sabe QUÉ dijo, el runtime sabe A QUÉ EVENTO pertenece y si el tenant puede
// persistirlo. Por eso devuelve las cadenas y es el runtime quien las escribe en el hilo, como
// saliente FUERA DE TURNO, solo si el tenant tiene `llm_intake` y el turno tiene evento.
//
// Slice vacío o nil = no se mandó nada, que es lo normal.
type DepositReminder interface {
	RemindContact(ctx context.Context, tenantID, contactID string) []string
}

// ReplyLimiter acota la tasa de auto-respuestas por conversación (Plan 020 · T0, red
// anti-loop). Allow consume UN token para la clave y devuelve false si la conversación
// excedió su tope. La clave que el runtime le pasa es store.Key.String() de la conversación.
//
// Lo satisface el token-bucket en memoria de platform/ratelimit (sin broker, ADR-0003), o
// runtimehelpertest.ReplyLimiter en tests.
type ReplyLimiter interface {
	Allow(key string) bool
}

// Option configura el Runtime al construirlo (patrón functional-options).
//
// RT-12 · Regla común de TODAS las opciones que inyectan una pieza: no pasarla, o pasarla
// nil, deja el motor EXACTAMENTE como estaba antes del plan que la introdujo (INV-6,
// no-regresión total). Ninguna pieza opcional ausente produce un error ni un pánico en el
// camino del entrante. El ORDEN en que se pasan las opciones no cambia el resultado.
type Option func(*Runtime)

// WithEventSink añade un EventSink al fan-out de efectos (Plan 015 · T2). Se puede pasar
// varias veces: se ACUMULAN. Sin ninguna, New deja un único LogSink.
//
// RT-18 · El orden de despacho no es el de las llamadas a esta opción: New ordena los sinks
// por fase, de forma estable, y PhaseProject corre antes que PhaseNotify.
func WithEventSink(sink EventSink) Option {
	panic(pendiente.Implementar("runtime.WithEventSink"))
}

// WithPresignClient inyecta el Presigner con el que el runtime firma la clave de un adjunto
// antes de despacharlo por Sender.SendMedia (Plan 017 · T4).
//
// RT-4 · Sin él (nil), una salida con Media produce un error CONTROLADO en el envío —nunca
// un pánico— y el estado ya quedó guardado. Las salidas de texto no lo necesitan.
func WithPresignClient(p Presigner) Option {
	panic(pendiente.Implementar("runtime.WithPresignClient"))
}

// WithTriggerResolver inyecta el resolver de disparos que el runtime consulta ante un
// entrante sin conversación viva (Resolve), para el escape global (IsEscape) y para el salto
// entre eventos sobre conversación viva (ResolveLive) (Plan 019 · T3/T4, Plan 043 · T2.2).
//
// Sin él, New usa trigger.NewNoopResolver(): un entrante sin estado se ignora, nada escapa y
// nada salta (INV-6).
func WithTriggerResolver(r trigger.Resolver) Option {
	panic(pendiente.Implementar("runtime.WithTriggerResolver"))
}

// WithReplyLimiter inyecta el tope de auto-respuestas por conversación (RT-8). Sin él (nil)
// no hay tope: se responde siempre y el motivo `rate_limit` no se cuenta nunca.
func WithReplyLimiter(l ReplyLimiter) Option {
	panic(pendiente.Implementar("runtime.WithReplyLimiter"))
}

// WithSelfNumbers inyecta el predicado «¿este número es propio del tenant?» que alimenta la
// guarda anti-self-loop (RT-7). Sin él (nil) la guarda no existe y el motivo `self_loop` no
// se cuenta nunca.
//
// 🔴 Trampa T-3 de la fase: el predicado calcula un índice ciego, y tiene que hacerlo con el
// MISMO KeyProvider con el que la flota escribió ese índice en fleet_sessions. Con un segundo
// KeyProvider ningún número casa y la guarda DEJA DE BLOQUEAR SIN UN SOLO ERROR. El arranque
// construye NewPostgresSelfNumbers con el KeyProvider compartido.
//
// Conserva el nombre aunque el puerto pasó de lista a predicado (Plan 046 · T4.1): el nombre
// dice QUÉ se inyecta, no cómo se consulta.
func WithSelfNumbers(c SelfNumberChecker) Option {
	panic(pendiente.Implementar("runtime.WithSelfNumbers"))
}

// WithIngestDeduper inyecta la dedupe PERSISTENTE de entrantes (RT-5). Sin ella (nil) solo
// queda la idempotencia consecutiva por last_wa_message_id.
func WithIngestDeduper(d IngestDeduper) Option {
	panic(pendiente.Implementar("runtime.WithIngestDeduper"))
}

// WithEntitlements inyecta el resolver de features del tenant (ADR-0022): el gate de VERDAD
// del servidor. Hoy gobierna una sola feature, `llm_intake`, y con ella tres cosas (RT-20):
// el hilo literal del evento, la bienvenida única y —dentro del agregador— la ventana de
// captación.
//
// Sin él (nil) las tres quedan cerradas (fail-closed): cero filas de hilo y ninguna
// bienvenida, aunque WithEventStore y WithWelcomeStore estén cableados.
//
// 🔴 Trampa T-2 de la fase: el arranque pasa EL MISMO resolver al runtime, al agregador, al
// despachador y a la cara HTTP. Un segundo resolver son dos cachés y dos verdades de lo
// contratado.
func WithEntitlements(r entitlements.Resolver) Option {
	panic(pendiente.Implementar("runtime.WithEntitlements"))
}

// WithClock inyecta el reloj del runtime. Un reloj nil se IGNORA (se queda el que hubiera);
// sin esta opción New usa time.Now.
//
// RT-15 · Qué gobierna y qué no. Este reloj decide: el vencimiento del TTL conversacional
// del limbo (contra Conversation.UpdatedAt), el instante de la bienvenida (el `now` de
// TouchContact y de MarkWelcomed) y el de las rachas (Inc, Close y el barrido de
// MaxAutoreplyStreak). NO decide el reloj del EVENTO: si un evento está suspendido, su
// last_activity_at y su closed_at los pone el reloj del almacén de eventos
// (events.WithClock). En un guion de test los dos relojes van sobre la MISMA función movible.
func WithClock(now func() time.Time) Option {
	panic(pendiente.Implementar("runtime.WithClock"))
}

// WithDepositReminder cablea el recordatorio perezoso de la seña al camino del entrante
// (RT-19). Sin él (nil) el motor no lo evalúa.
func WithDepositReminder(d DepositReminder) Option {
	panic(pendiente.Implementar("runtime.WithDepositReminder"))
}

// WithEventStore cablea el almacén del evento conversacional (Plan 043 · Ola 2). Sin él (nil)
// NO HAY PLANO DE EVENTOS: un event_start arranca su flujo como una keyword y no pare fila,
// un event_stop no desactiva nada, no hay hilo, y GetEventForTenant / CancelEventForTenant
// devuelven ErrNoEventPlane.
func WithEventStore(s EventStore) Option {
	panic(pendiente.Implementar("runtime.WithEventStore"))
}

// WithIntakeAbandoner cablea el abandono de la solicitud que acompaña a la cancelación de un
// evento. Va con WithEventStore: sin él (nil) el evento se cancela igual, su solicitud queda
// sin abandonar y se avisa con un WARN.
func WithIntakeAbandoner(a IntakeAbandoner) Option {
	panic(pendiente.Implementar("runtime.WithIntakeAbandoner"))
}

// WithDispatcher cablea el despachador de nivel superior (Plan 043 · T2.3). Sin él (nil), un
// event_start de tipo `menu` crea su evento pero no presenta ninguna lista.
func WithDispatcher(d Dispatcher) Option {
	panic(pendiente.Implementar("runtime.WithDispatcher"))
}

// WithSummarySources cablea las fuentes DURABLES del resumen del evento que se abandona
// (Plan 043 · T3.4): las líneas del pedido y las respuestas de la encuesta. Van JUNTAS en un
// struct (NewSummarySources las arma sobre un mismo almacén).
//
// Con el valor cero no se escribe ningún resumen y el abandono ocurre igual. Cablear solo una
// de las dos es peor que ninguna: el abandono del otro tipo produce un WARN en cada salto.
func WithSummarySources(src events.SummarySources) Option {
	panic(pendiente.Implementar("runtime.WithSummarySources"))
}

// WithOpeningBuilder cablea el constructor de la entrada que OFRECE (Plan 043 · T3.8).
//
// RT-12 · Sustituye SOLO al texto del `fallback` del tenant (INV-20): cuando un entrante sin
// conversación viva resuelve a Fallback y la oferta trae al menos una opción, se envía la
// oferta en vez de arrancar el flujo del fallback. Una oferta vacía, un fallo al construirla o
// la opción sin cablear dejan el fallback byte a byte como siempre. Start, StartEvent e Ignore
// no se enteran.
//
// En producción la satisface el mismo despachador que WithDispatcher, pero es una opción
// aparte: son dos preguntas distintas y puede estar cableada una sin la otra.
func WithOpeningBuilder(b OpeningBuilder) Option {
	panic(pendiente.Implementar("runtime.WithOpeningBuilder"))
}

// WithFlowForKind cablea la resolución «tipo de evento → flujo del tenant» que necesita la
// opción «empezar uno nuevo» del menú. Sin ella (nil), elegir un tipo crea su evento sin
// arrancar ningún flujo.
func WithFlowForKind(f FlowForKind) Option {
	panic(pendiente.Implementar("runtime.WithFlowForKind"))
}

// WithIncomingTimeout fija el plazo con que OnIncoming acota CADA entrante (Plan 027 · Ola 0
// · T1, cierra H1; variable WAPP_FLOW_INCOMING_TIMEOUT).
//
// RT-3 · Un valor <= 0 equivale a no pasarla: New deja 30 s. El camino caliente nunca queda
// sin plazo.
func WithIncomingTimeout(d time.Duration) Option {
	panic(pendiente.Implementar("runtime.WithIncomingTimeout"))
}

// WithMaxConcurrentIncoming fija cuántos entrantes procesa OnIncoming A LA VEZ (Plan 027 ·
// Ola 1 · T5, cierra H5; variable WAPP_FLOW_MAX_CONCURRENT_INCOMING).
//
// RT-3 · n == 0 equivale a no pasarla: 64. n < 0 quita el techo (sin semáforo). n > 0 es el
// cupo.
func WithMaxConcurrentIncoming(n int) Option {
	panic(pendiente.Implementar("runtime.WithMaxConcurrentIncoming"))
}

// WithResumePolicy registra la política de reanudación de un módulo bajo su tipo de nodo
// (Plan 027 · Ola 3 · T8, cierra H9; ver resume.go). Pasarla dos veces para el mismo tipo deja
// la última. Un nodo cuyo tipo no tiene política no reanuda nada.
func WithResumePolicy(nodeType string, p modules.ResumePolicy) Option {
	panic(pendiente.Implementar("runtime.WithResumePolicy"))
}

// WithReactiveBlockedHook inyecta el contador de entrantes que NO llegan al motor reactivo.
// Recibe el motivo, de cardinalidad FIJA: "passive", "self_loop", "rate_limit" o
// "saturation". Se llama una vez por corte. El hook OBSERVA y no decide: con nil los cortes
// se comportan igual. Existe para que el módulo no importe prometheus.
func WithReactiveBlockedHook(fn func(reason string)) Option {
	panic(pendiente.Implementar("runtime.WithReactiveBlockedHook"))
}

// WithAutoreplyStreakHook inyecta el observador de rachas de auto-respuestas (Plan 049 ·
// Opción A). Recibe la LONGITUD de cada racha al CERRARSE su episodio, UNA vez por episodio:
// nunca en cada auto-respuesta, nunca una longitud <= 0.
//
// Sin él (nil) el contador sigue contando y MaxAutoreplyStreak sigue contestando. El hook
// llega a recibir las rachas se pase antes o después que cualquier otra opción: New no
// congela su valor al construir el contador.
func WithAutoreplyStreakHook(fn func(streak int)) Option {
	panic(pendiente.Implementar("runtime.WithAutoreplyStreakHook"))
}

// WithAggregator cablea el agregador de ventanas de captación (Plan 044 · Ola 1). Sin él
// (nil) el motor no abre ninguna ventana ni escribe una fila en intake_jobs.
//
// ⚠️ Trampa T-4 de la fase: cablearlo NO basta. El cierre de las ventanas lo ejecuta el
// barrido de fondo del agregador, que el arranque lanza aparte; sin él las ventanas se abren
// y no cierran nunca, en silencio.
func WithAggregator(a *IntakeAggregator) Option {
	panic(pendiente.Implementar("runtime.WithAggregator"))
}

// New construye el Runtime. Las cinco dependencias obligatorias son el almacén de flujos, el
// engine (la máquina de estados pura), la salida hacia el Gateway, el resolver de sesión →
// tenant y perfil, y el resolver de identidad de contactos; el logger recibe todo lo que el
// runtime anuncia. No valida ni desreferencia ninguna al construir.
//
// Lo que deja resuelto, además de aplicar las opciones:
//
//   - Sinks: sin ningún WithEventSink, el fan-out es un único LogSink. Con uno o más, se
//     ordenan por fase UNA vez, de forma estable (RT-18).
//   - Disparos: sin WithTriggerResolver, el resolver noop.
//   - RT-3 · Plazo del entrante: <= 0 (o sin opción) → 30 s. Semáforo: 0 (o sin opción) → 64
//     cupos; > 0 → ese cupo; < 0 → sin semáforo.
//   - Reloj: sin WithClock (o con nil), time.Now.
//   - Rachas: el contador se construye SIEMPRE, con o sin WithAutoreplyStreakHook, con los
//     valores por defecto de streak.go (30 min, 10.000 conversaciones).
//
// 🔴 Trampa T-6 de la fase: el resolutor de consultas del turno acotado NO es una opción del
// runtime. Viaja dentro del engine que se pasa aquí, y se cablea al construirlo con
// engine.WithQueryResolver (en el viejo, engine.WithConsultaResolver). Olvidarlo no da error:
// el engine contesta «sin_resolutor» y el carrito repromptea como siempre.
func New(repo FlowStore, eng *engine.Engine, sender Sender, resolver TenantResolver, contacts contact.Resolver, log logger.Logger, opts ...Option) *Runtime {
	panic(pendiente.Implementar("runtime.New"))
}

// MaxAutoreplyStreak devuelve la racha de auto-respuestas VIVA más larga en este instante
// (Plan 049 · Opción A), o 0 si no hay ninguna. Es la fuente del gauge
// wapp_flow_autoreply_streak_max: su firma, func() int, es la que el arranque inyecta.
//
// RT-11 · 🔴 NO es un getter puro (trampa T-7: `/metrics` no es inocuo). En el mismo
// recorrido BARRE las rachas vencidas por inactividad (30 min sin auto-respuesta): las borra
// y las reporta al hook de WithAutoreplyStreakHook. Es el único sitio donde se cierra el
// episodio de la conversación que se abandona y no vuelve. Una vencida no cuenta para el
// máximo, y dos llamadas seguidas no reportan dos veces la misma.
//
// El instante lo pone el reloj del runtime (WithClock), no un parámetro.
//
// Pensada para el scrape, no para el camino caliente: recorre todas las conversaciones vivas
// bajo el candado del contador.
//
// Sobre un *Runtime nil, o sobre uno que no salió de New, devuelve 0 sin entrar en pánico.
func (rt *Runtime) MaxAutoreplyStreak() int {
	panic(pendiente.Implementar("runtime.Runtime.MaxAutoreplyStreak"))
}
