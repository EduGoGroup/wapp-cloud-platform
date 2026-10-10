// Porta internal/flujos/runtime/runtime_engine.go @ e0159171

package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
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
// El valor cero NO es utilizable: se construye con New.
type Runtime struct {
	store    FlowStore
	engine   *engine.Engine
	sender   Sender
	resolver TenantResolver
	contacts contact.Resolver
	log      logger.Logger
	locks    *keyedMutex
	// sinks recibe en fan-out EN PROCESO (ADR-0003, sin broker) cada Effect que
	// un módulo declara al avanzar (Plan 015 · T2). New lo deja en LogSink por
	// defecto si no se inyecta otro con WithEventSink.
	sinks []EventSink
	// presigner genera la URL prefirmada de descarga de un adjunto cuando una
	// salida trae Media (nodo media, Plan 017 §4.2). nil si no se cablea
	// WithPresignClient (entornos sin media): un output de media con presigner nil
	// devuelve error CONTROLADO en send (no pánico), coherente con el orden
	// Save-antes-de-Send (el estado ya quedó persistido).
	presigner Presigner
	// triggers decide, ante un entrante SIN conversación viva, si se arranca un
	// flujo por palabra clave/fallback (Resolve) y, sobre una conversación viva,
	// si el texto es una señal de escape que la corta (IsEscape) (Plan 019 · T3/T4).
	// New lo deja en NoopResolver si no se inyecta WithTriggerResolver: sin resolver
	// real el comportamiento es idéntico al previo al Plan 019 (INV-6 no-regresión).
	triggers trigger.Resolver
	// replyLimiter acota las auto-respuestas por conversación (Plan 020 · T0, red
	// anti-loop): un token-bucket EN MEMORIA por store.Key. Antes de CADA auto-envío
	// (arranque por disparo, avance por Step, aviso de escape, reinicio de carrito) el
	// runtime consume un token; agotado ⇒ NO responde (corta cualquier bucle). nil
	// (sin WithReplyLimiter) desactiva el tope: no-regresión total.
	replyLimiter ReplyLimiter
	// selfNumbers decide si el remitente de un entrante es un número propio del
	// tenant, para la guarda anti-self-loop (Plan 020 · T2): un entrante cuyo from_pn
	// es el número de OTRA sesión del MISMO tenant NO auto-responde. Desde el Plan
	// 046 · T4.1 es un PREDICADO resuelto en SQL por índice ciego, no una lista en
	// claro: los teléfonos del tenant ya no se traen a memoria. nil (sin
	// WithSelfNumbers) desactiva la guarda: no-regresión total (sin números propios
	// poblados el comportamiento es idéntico al previo al 020).
	selfNumbers SelfNumberChecker
	// incomingTimeout acota el procesamiento de cada entrante reactivo despachado
	// por OnIncoming (Plan 027 · Ola 0 · T1, cierra H1). New lo deja en
	// defaultIncomingTimeout si no se inyecta WithIncomingTimeout (o si el valor es
	// <=0): el camino caliente NUNCA queda sin deadline.
	incomingTimeout time.Duration
	// incomingSem es el semáforo que acota la concurrencia de HandleIncoming
	// despachado por OnIncoming (Plan 027 · Ola 1 · T5, cierra H5). nil ⇒ sin techo
	// (opt-out explícito con WithMaxConcurrentIncoming(<0)); en New se materializa a
	// defaultMaxConcurrentIncoming si no se configuró.
	incomingSem chan struct{}
	// maxConcurrentIncoming es el tope configurado (lo fija WithMaxConcurrentIncoming
	// antes de que New construya incomingSem). 0 ⇒ default; <0 ⇒ sin techo.
	maxConcurrentIncoming int
	// resumePolicies asocia un tipo de nodo con la política de reanudación de su
	// módulo (Plan 027 · Ola 3 · T8, cierra H9): reinicio por estado terminal /
	// expiración + siembra de Vars. Un nodo sin política (menú/encuesta) NO reanuda
	// nada (no-regresión). Se registra con WithResumePolicy; el carrito la aporta.
	resumePolicies map[string]modules.ResumePolicy
	// deduper deduplica los entrantes ante los reenvíos del outbox durable del Edge
	// (Plan 028 · T6, ADR-0003): antes de tocar el motor, un frame ya visto (misma
	// session_id + wa_message_id) se ignora. nil (sin WithIngestDeduper) desactiva la
	// dedupe persistente: no-regresión total (queda solo la consecutiva por
	// last_wa_message_id).
	deduper IngestDeduper
	// entitlements es el GATE DE VERDAD del servidor (ADR-0022, Plan 029 · T7): una
	// intención LLM del entrante SOLO alimenta la Signal si el tenant tiene la feature
	// llm_intent habilitada. nil (sin WithEntitlements) ⇒ el intent se DESCARTA siempre
	// (camino actual sin clasificador): un gate que solo viviera en el Edge sería
	// decorativo (corre en la máquina del cliente).
	entitlements entitlements.Resolver
	// ⚰️ Aquí vivió, del 2026-08-10 al 2026-08-22, la SEGUNDA condición del productor
	// `message` del hilo: un booleano de DESPLIEGUE, leído de una variable de
	// entorno, por encima del gate por tenant. Lo retiró el Plan 044 · T1.6, que es
	// el LECTOR que estaba esperando. Hoy la condición es UNA: la feature
	// `llm_intake` del tenant (thread.go). Quien eche de menos el interruptor y
	// quiera reponerlo, lea antes thread.go: apagar el hilo para TODO el parque desde
	// una variable de entorno es lo que dejó al 044 sin materia prima durante doce
	// días.
	//
	// ⚠️ LA LÍNEA EN BLANCO DE ABAJO ES OBLIGATORIA Y NO ES ESTILO. Sin ella la lápida
	// y el doc-comment del campo siguiente forman UN SOLO bloque de comentario, así que
	// la documentación de `now` pasa a empezar por «⚰️ Aquí vivió…». Es el mismo defecto
	// que en WithClock (más abajo), donde además lo caza `revive`/`exported`.

	// now entrega la hora actual para el TTL conversacional (Plan 029 · T9). Inyectable
	// (WithClock) para tests deterministas; New lo deja en time.Now.
	now func() time.Time
	// deposits evalúa el recordatorio PEREZOSO de la seña cuando el CLIENTE vuelve a
	// escribir (Plan 041 · T4.4, D-041.12). Es uno de los tres toques del
	// recordatorio y el único que no nace de una lectura del dueño. nil (sin
	// WithDepositReminder) ⇒ el motor no lo evalúa: no-regresión total.
	//
	// NO es un reloj (ADR-0003): no hay barrido ni goroutine de fondo; es este
	// entrante, que ya estaba pasando por aquí, el que hace de disparador.
	deposits DepositReminder
	// events es el almacén del EVENTO conversacional (Plan 043 · Ola 2, D-043.4): la
	// instancia viva de una capacidad —carrito, encuesta, menú— para ESTA
	// conversación. Lo consultan el salto por tipo (event_start) y la desactivación
	// (event_stop). nil (sin WithEventStore) ⇒ no hay plano de eventos: un
	// event_start arranca su flujo como lo haría una keyword y nada pare filas.
	// No-regresión total (INV-6).
	events EventStore
	// intakes abandona la solicitud del evento que el CLIENTE cierra al elegir
	// empezar otro del mismo tipo estando el viejo VENCIDO (ADR-0029 · E-11). Es la
	// otra mitad de un solo hecho: el evento pasa a cancelled y su solicitud a
	// abandoned. nil ⇒ el evento se cierra igual y la solicitud queda huérfana con
	// un WARN; el bootstrap lo cablea siempre.
	intakes IntakeAbandoner
	// dispatcher decide qué ofrecerle al contacto cuando pide el menú (Plan 043 ·
	// T2.3): tipos que el tenant ofrece + eventos suyos que puede retomar. SOLO LEE;
	// crear filas, mover el puntero y hablar es del runtime. nil (sin
	// WithDispatcher) ⇒ la palabra del menú no presenta nada.
	dispatcher Dispatcher
	// flows resuelve qué flujo arranca un tipo de evento, leyendo la regla
	// event_start del tenant. Lo necesita la elección «empezar uno nuevo» del menú,
	// que dice el tipo pero no el flujo. nil ⇒ el evento nace sin flujo.
	flows FlowForKind
	// opening arma lo que se le OFRECE a quien escribe algo que no casó nada (Plan
	// 043 · T3.8, REQ-27): la lista de lo que puede empezar y lo que puede retomar.
	// Sustituye al texto del `fallback` del tenant, y SOLO ahí (INV-20).
	//
	// nil (sin WithOpeningBuilder) ⇒ el fallback se comporta exactamente como antes
	// del Plan 043: arranca su flujo y dice su frase. No-regresión total (INV-6).
	opening OpeningBuilder
	// sources son las fuentes DURABLES del resumen del evento que se abandona (Plan
	// 043 · T3.4/T3.3): las líneas del pedido para `cart` y las respuestas dadas para
	// `survey`. De Vars sale solo lo que no es durable (el nivel de la sub-máquina).
	//
	// Van las DOS o ninguna: LoadSummary devuelve error cuando le falta el lector del
	// tipo que le toca —en vez de un resumen vacío, que borraría del historial lo que
	// el cliente sí decidió—, así que cablear una sola convierte el abandono del otro
	// tipo en un WARN por cada salto. Cero valor ⇒ no se escribe ningún resumen y el
	// abandono ocurre igual (no-regresión).
	sources events.SummarySources
	// onReactiveBlocked cuenta cada entrante que NO entra al motor reactivo, con el
	// motivo (reasonPassive|reasonSelfLoop|reasonRateLimit). Hook NIL-SAFE inyectado
	// con WithReactiveBlockedHook —típicamente metrics.FlowReactiveBlocked— para no
	// acoplar el motor a prometheus, igual que el onRecord del sink de acuses. nil
	// (default) ⇒ no se cuenta nada: los cortes se comportan igual.
	onReactiveBlocked func(reason string)
	// onAutoreplyStreak observa la LONGITUD de cada racha de auto-respuestas que se
	// CIERRA (Plan 049 · Opción A, OBSERVAR). Se invoca una sola vez por episodio, al
	// cerrarse —flujo terminado, escape, TTL del estado o 30 min de inactividad—, NO
	// en cada auto-respuesta: llamarlo en cada una convertiría el histograma en la
	// distribución de los prefijos de cada racha (1, 2, 3… para una racha de 3) y
	// hundiría el p99 hacia 1. Ver streak.go.
	//
	// Hook NIL-SAFE inyectado con WithAutoreplyStreakHook —típicamente
	// metrics.FlowAutoreplyStreak— por la MISMA razón que su hermano
	// onReactiveBlocked: el motor de flujos NUNCA importa prometheus. La observación
	// va por callback; el motor no conoce a quién le está contando nada. nil (default)
	// ⇒ no se observa: el contador sigue contando y el envío se comporta igual.
	//
	// ⚠️ OBSERVA, NUNCA DECIDE: no hay umbral ni corte en la Opción A (cortar es la B,
	// aplazada hasta tener 2-4 semanas de esta distribución).
	onAutoreplyStreak func(streak int)
	// autoreplyStreaks es el contador de rachas por conversación (streak.go). Lo
	// construye New SIEMPRE —no depende de que el hook esté cableado— para que el
	// gauge de la racha viva (MaxAutoreplyStreak) tenga qué contestar aunque nadie
	// haya inyectado el histograma.
	autoreplyStreaks *streakCounter
	// passiveAnnounced recuerda qué sesiones ya anunciaron a INFO su corte por rol
	// passive, para decirlo UNA vez por sesión y las siguientes a Debug. El corte
	// salta en CADA entrante de una sesión passive (~2.000/hora en el e2e del
	// 2026-08-06): a INFO siempre inundaría el log, y solo a Debug es invisible con
	// el nivel por defecto —que fue justo lo que hizo diagnosticar mal un escenario
	// que no corrió—. Crece acotado por el nº de sesiones passive del despliegue
	// (decenas) y se vacía al reiniciar, que es cuando el operador quiere volver a
	// verlas. sync.Map porque OnIncoming procesa entrantes concurrentes.
	passiveAnnounced sync.Map
	// aggregator acumula los entrantes en VENTANAS de captación (Plan 044 · Ola 1 ·
	// T1.1/T1.2, ver aggregator.go). nil (sin WithAggregator) ⇒ el motor se comporta
	// EXACTAMENTE como antes del 044: no se abre ninguna ventana y no se escribe una
	// sola fila en intake_jobs. Es la misma no-regresión por defecto que el resto de
	// piezas opcionales de este struct.
	//
	// Su Observe corre en línea con el mensaje y su presupuesto está acotado y
	// escrito (D-044.26): UNA sentencia, cero lecturas, cero cripto, cero red. NUNCA
	// devuelve error — un fallo suyo se loguea y el turno del cliente sigue (INV-10).
	aggregator *IntakeAggregator
	// welcomes guarda el estado de la BIENVENIDA ÚNICA por conversación (Plan 044 ·
	// T1.8-2, D6, ver welcome.go): «a esta conversación ya le saludé» y «cuándo habló
	// el contacto por última vez». nil (sin WithWelcomeStore) ⇒ el motor no manda
	// ninguna bienvenida y no escribe una sola fila: misma no-regresión por defecto
	// que el resto de piezas opcionales de este struct.
	//
	// Va SIEMPRE con WithEntitlements: el gate por tenant (`llm_intake`) es
	// fail-closed, así que con el resolver a nil esto queda inerte aunque se cablee.
	welcomes WelcomeStore
}

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
//
// Divergencia deliberada del viejo (D-F8-16, hallazgo 34e): un sink nil se IGNORA (RT-12).
// El viejo lo guardaba y el pánico llegaba en el primer turno con un efecto.
func WithEventSink(sink EventSink) Option {
	return func(rt *Runtime) {
		if sink == nil {
			return
		}
		rt.sinks = append(rt.sinks, sink)
	}
}

// WithPresignClient inyecta el Presigner con el que el runtime firma la clave de un adjunto
// antes de despacharlo por Sender.SendMedia (Plan 017 · T4).
//
// RT-4 · Sin él (nil), una salida con Media produce un error CONTROLADO en el envío —nunca
// un pánico— y el estado ya quedó guardado. Las salidas de texto no lo necesitan.
func WithPresignClient(p Presigner) Option {
	return func(rt *Runtime) { rt.presigner = p }
}

// WithTriggerResolver inyecta el resolver de disparos que el runtime consulta ante un
// entrante sin conversación viva (Resolve), para el escape global (IsEscape) y para el salto
// entre eventos sobre conversación viva (ResolveLive) (Plan 019 · T3/T4, Plan 043 · T2.2).
//
// Sin él, New usa trigger.NewNoopResolver(): un entrante sin estado se ignora, nada escapa y
// nada salta (INV-6).
func WithTriggerResolver(r trigger.Resolver) Option {
	return func(rt *Runtime) { rt.triggers = r }
}

// WithReplyLimiter inyecta el tope de auto-respuestas por conversación (RT-8). Sin él (nil)
// no hay tope: se responde siempre y el motivo `rate_limit` no se cuenta nunca.
func WithReplyLimiter(l ReplyLimiter) Option {
	return func(rt *Runtime) { rt.replyLimiter = l }
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
	return func(rt *Runtime) { rt.selfNumbers = c }
}

// WithIngestDeduper inyecta la dedupe PERSISTENTE de entrantes (RT-5). Sin ella (nil) solo
// queda la idempotencia consecutiva por last_wa_message_id.
func WithIngestDeduper(d IngestDeduper) Option {
	return func(rt *Runtime) { rt.deduper = d }
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
	return func(rt *Runtime) { rt.entitlements = r }
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
	return func(rt *Runtime) {
		if now != nil {
			rt.now = now
		}
	}
}

// WithDepositReminder cablea el recordatorio perezoso de la seña al camino del entrante
// (RT-19). Sin él (nil) el motor no lo evalúa.
func WithDepositReminder(d DepositReminder) Option {
	return func(rt *Runtime) { rt.deposits = d }
}

// WithEventStore cablea el almacén del evento conversacional (Plan 043 · Ola 2). Sin él (nil)
// NO HAY PLANO DE EVENTOS: un event_start arranca su flujo como una keyword y no pare fila,
// un event_stop no desactiva nada, no hay hilo, y GetEventForTenant / CancelEventForTenant
// devuelven ErrNoEventPlane.
func WithEventStore(s EventStore) Option {
	return func(rt *Runtime) { rt.events = s }
}

// WithIntakeAbandoner cablea el abandono de la solicitud que acompaña a la cancelación de un
// evento. Va con WithEventStore: sin él (nil) el evento se cancela igual, su solicitud queda
// sin abandonar y se avisa con un WARN.
func WithIntakeAbandoner(a IntakeAbandoner) Option {
	return func(rt *Runtime) { rt.intakes = a }
}

// WithDispatcher cablea el despachador de nivel superior (Plan 043 · T2.3). Sin él (nil), un
// event_start de tipo `menu` crea su evento pero no presenta ninguna lista.
func WithDispatcher(d Dispatcher) Option {
	return func(rt *Runtime) { rt.dispatcher = d }
}

// WithSummarySources cablea las fuentes DURABLES del resumen del evento que se abandona
// (Plan 043 · T3.4): las líneas del pedido y las respuestas de la encuesta. Van JUNTAS en un
// struct (NewSummarySources las arma sobre un mismo almacén).
//
// Con el valor cero no se escribe ningún resumen y el abandono ocurre igual. Cablear solo una
// de las dos es peor que ninguna: el abandono del otro tipo produce un WARN en cada salto.
func WithSummarySources(src events.SummarySources) Option {
	return func(rt *Runtime) { rt.sources = src }
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
	return func(rt *Runtime) { rt.opening = b }
}

// WithFlowForKind cablea la resolución «tipo de evento → flujo del tenant» que necesita la
// opción «empezar uno nuevo» del menú. Sin ella (nil), elegir un tipo crea su evento sin
// arrancar ningún flujo.
func WithFlowForKind(f FlowForKind) Option {
	return func(rt *Runtime) { rt.flows = f }
}

// WithResumePolicy registra la política de reanudación de un módulo bajo su tipo de nodo
// (Plan 027 · Ola 3 · T8, cierra H9; ver resume.go). Pasarla dos veces para el mismo tipo deja
// la última. Un nodo cuyo tipo no tiene política no reanuda nada.
//
// Divergencia deliberada del viejo (D-F8-16, hallazgo 34e): una política nil se IGNORA
// (RT-12): no registra nada y no borra la que ya hubiera para ese tipo. El viejo la guardaba
// y el pánico llegaba en el primer turno sobre un nodo de ese tipo.
func WithResumePolicy(nodeType string, p modules.ResumePolicy) Option {
	return func(rt *Runtime) {
		if p == nil {
			return
		}
		if rt.resumePolicies == nil {
			rt.resumePolicies = make(map[string]modules.ResumePolicy)
		}
		rt.resumePolicies[nodeType] = p
	}
}

// WithReactiveBlockedHook inyecta el contador de entrantes que NO llegan al motor reactivo.
// Recibe el motivo, de cardinalidad FIJA: "passive", "self_loop", "rate_limit" o
// "saturation". Se llama una vez por corte. El hook OBSERVA y no decide: con nil los cortes
// se comportan igual. Existe para que el módulo no importe prometheus.
func WithReactiveBlockedHook(fn func(reason string)) Option {
	return func(rt *Runtime) { rt.onReactiveBlocked = fn }
}

// WithAggregator cablea el agregador de ventanas de captación (Plan 044 · Ola 1). Sin él
// (nil) el motor no abre ninguna ventana ni escribe una fila en intake_jobs.
//
// ⚠️ Trampa T-4 de la fase: cablearlo NO basta. El cierre de las ventanas lo ejecuta el
// barrido de fondo del agregador, que el arranque lanza aparte; sin él las ventanas se abren
// y no cierran nunca, en silencio.
func WithAggregator(a *IntakeAggregator) Option {
	return func(rt *Runtime) { rt.aggregator = a }
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
	rt := &Runtime{
		store:    repo,
		engine:   eng,
		sender:   sender,
		resolver: resolver,
		contacts: contacts,
		log:      log,
		locks:    newKeyedMutex(),
	}
	for _, opt := range opts {
		opt(rt)
	}
	// El fan-out de efectos nunca es nil: log-only por defecto (NO PersistSink,
	// para no duplicar survey_results con el flush viejo hasta T3).
	if len(rt.sinks) == 0 {
		rt.sinks = []EventSink{NewLogSink(log)}
	}
	// El ORDEN del fan-out es una propiedad del runtime, no del wiring (Plan 042 ·
	// Ola 3.1): los sinks que proyectan —y de paso ENRIQUECEN eff.Payload— corren
	// antes que los que solo leen ese payload para notificar afuera. Se ordena UNA
	// vez aquí, de forma estable, para que dispatch() no pague nada por efecto y
	// para que reordenar dos WithEventSink en el arranque deje de poder romper la
	// correlación del intake_id en silencio (ver SinkPhase en event_sink.go).
	sortSinksByPhase(rt.sinks)
	// El resolver de disparos nunca es nil: NoopResolver por defecto (INV-6
	// no-regresión: sin WithTriggerResolver el comportamiento es idéntico al previo
	// al Plan 019 — un entrante sin conversación viva se ignora, decisión C).
	if rt.triggers == nil {
		rt.triggers = trigger.NewNoopResolver()
	}
	// El deadline del entrante reactivo nunca es <=0: sin WithIncomingTimeout (o con
	// un valor no positivo) cae a defaultIncomingTimeout (Plan 027 · Ola 0 · T1).
	if rt.incomingTimeout <= 0 {
		rt.incomingTimeout = defaultIncomingTimeout
	}
	// El reloj del TTL conversacional nunca es nil: time.Now por defecto (Plan 029 · T9).
	if rt.now == nil {
		rt.now = time.Now
	}
	// Semáforo de entrantes (Plan 027 · Ola 1 · T5): 0 ⇒ default; <0 ⇒ sin techo
	// (incomingSem queda nil y OnIncoming no acota la concurrencia).
	switch {
	case rt.maxConcurrentIncoming == 0:
		rt.incomingSem = make(chan struct{}, defaultMaxConcurrentIncoming)
	case rt.maxConcurrentIncoming > 0:
		rt.incomingSem = make(chan struct{}, rt.maxConcurrentIncoming)
	}
	// Contador de rachas de auto-respuestas (Plan 049 · Opción A). Se construye AQUÍ,
	// DESPUÉS del bucle de Options, por lo mismo que el semáforo y el reloj: es un
	// campo DERIVADO, y New materializa los derivados cuando ya se sabe qué se
	// configuró. Siempre, sin condición: el contador es del runtime, no del hook — sin
	// hook cableado sigue contando y MaxAutoreplyStreak sigue sabiendo contestar.
	//
	// El onClose es una INDIRECCIÓN EN TIEMPO DE LLAMADA (mira rt.onAutoreplyStreak
	// cada vez, no captura su valor de ahora) y esa es la decisión que importa: pasar
	// rt.onAutoreplyStreak directo congelaría el valor que tuviera en este instante, y
	// bastaría con que alguien reordenara las Options —o añadiera una que lo fijara
	// más tarde, o un test que lo cambiara— para que el hook dejara de recibir nada EN
	// SILENCIO (no rompe: simplemente no se observa, que es el fallo más difícil de
	// notar). Con la indirección, el orden de aplicación de las Options deja de ser
	// load-bearing. La guarda nil vive dentro del closure porque streakCounter.report
	// solo sabe que su onClose no es nil, no que el hook del runtime sí lo sea.
	//
	// Los ceros van a propósito: newStreakCounter normaliza idleTTL<=0 a streakIdleTTL
	// (30 min) y maxEntries<=0 a streakMaxEntries (10.000). Hoy no hay Option que los
	// exponga —el plan no pide configurarlos— y las constantes viven en streak.go.
	rt.autoreplyStreaks = newStreakCounter(0, 0, func(streak int) {
		if rt.onAutoreplyStreak != nil {
			rt.onAutoreplyStreak(streak)
		}
	})
	return rt
}
