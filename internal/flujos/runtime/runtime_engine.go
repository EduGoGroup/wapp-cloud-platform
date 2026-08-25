package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// defaultMaxConcurrentIncoming acota cuántos entrantes reactivos procesa el runtime
// A LA VEZ (Plan 027 · Ola 1 · T5, cierra H5). Antes OnIncoming lanzaba una
// goroutine por entrante SIN techo: bajo una inundación de historial esto arranca
// cientos de HandleIncoming en paralelo (cada uno con su transacción de contactos y
// su SendText), saturando la BD y el gRPC. Un semáforo acotado limita la
// concurrencia REAL; el resto de goroutines esperan cupo baratas. 64 es holgado
// para el piloto. Se sobreescribe con WithMaxConcurrentIncoming (env
// WAPP_FLOW_MAX_CONCURRENT_INCOMING); un valor negativo lo desactiva (sin techo).
const defaultMaxConcurrentIncoming = 64

// defaultIncomingTimeout acota el procesamiento de CADA entrante reactivo (Plan
// 027 · Ola 0 · T1, cierra H1). OnIncoming despacha HandleIncoming en una
// goroutine con context.Background() (desacoplado del stream); sin deadline, el
// SendText interno espera el Ack contra un ctx.Done() que nunca dispara ⇒ la
// goroutine se fuga para siempre reteniendo el keyedMutex y cuñando la
// conversación. 30s es holgado para un round-trip Cloud→Edge→WhatsApp→Ack y a la
// vez guarantees que un Edge mudo libere la clave. Se sobreescribe con
// WithIncomingTimeout (env WAPP_FLOW_INCOMING_TIMEOUT).
const defaultIncomingTimeout = 30 * time.Second

// FlowStore es el subconjunto SEGREGADO de store.Repository que el Runtime
// necesita (ISP, Plan 027 · Ola 2 · T9, cierra H12): estado conversacional +
// lectura de definiciones + lectura de la solicitud abierta y de los ajustes del carrito
// (para el TTL/reanudación). NO incluye las ESCRITURAS de solicitudes/efectos/resultados
// —eso lo consume el PersistSink—, así el runtime declara solo lo que usa. Un
// *store.PostgresRepository / *store.MemoryRepository lo satisface sin cambios.
type FlowStore interface {
	store.ConversationStore
	store.DefinitionReader
	store.IntakeReader
	store.TenantSettingsReader
}

// Runtime orquesta el motor de flujos vivo (design.md §6): inicio por API
// (Start) y avance por entrante (HandleIncoming/OnIncoming). Serializa por
// conversación con un single-flight en memoria (keyedMutex) y persiste el
// estado ANTES de enviar (orden Save-antes-de-SendText, design.md §6).
//
// Las dependencias se inyectan: el store (durabilidad), el engine (máquina de
// estados pura), el Sender (salida hacia el Gateway), el TenantResolver
// (session_id → tenant_id, design.md §10.A) y el contact.Resolver (identidad
// del contacto: JID/ref → contact_id opaco y contact_id → destino enviable,
// Plan 010, design.md §4-§6). Es seguro para uso concurrente.
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
	onAutoreplyStreak func(racha int)
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

// DepositReminder evalúa si a un contacto hay que recordarle la seña de alguna
// solicitud suya (Plan 041 · T4.4). Lo satisface *intakes.DepositReminder.
//
// Se declara aquí como puerto —y no se importa el tipo concreto— por lo de siempre:
// el motor de flujos no tiene por qué conocer el dominio de solicitudes para
// avisarle de que alguien habló. NO devuelve error a propósito: un recordatorio que
// no sale no puede tumbar el procesamiento del mensaje que el cliente acaba de
// mandar (misma regla que el notificador de estados).
//
// 🔴 DEVUELVE LOS TEXTOS QUE MANDÓ, y eso lo estrenó el Plan 044 · T1.6 (D-044.24).
// El recordatorio de la seña es la coletilla ARQUETÍPICA fuera de turno: sale por
// el Notifier de `intakes`, no por rt.send, y hasta hoy no dejaba rastro en el hilo
// del evento. Pero el hilo no lo puede escribir `intakes`: ese paquete no conoce el
// evento conversacional (intakes.Intake no tiene EventID) ni el resolver de
// entitlements. El runtime sí tiene las dos cosas. Por eso el reparto es éste — el
// recordatorio sabe QUÉ dijo, el runtime sabe A QUÉ EVENTO pertenece y si el tenant
// puede persistirlo— y por eso la firma devuelve las cadenas en vez de escribirlas.
//
// Slice vacío o nil = no se mandó nada (lo normal, y el 99,9 % de los toques). El
// llamante NO puede fiarse del orden para nada más que el orden del hilo: son los
// textos en el orden en que salieron.
type DepositReminder interface {
	RemindContact(ctx context.Context, tenantID, contactID string) []string
}

// ReplyLimiter acota la tasa de auto-respuestas por conversación (Plan 020 · T0).
// Allow consume un token para la clave dada y devuelve false si la conversación
// excedió su tope. Lo satisface *ratelimit.Limiter (token-bucket EN MEMORIA, sin
// broker, ADR-0003); se declara como interfaz para no acoplar el runtime al
// paquete concreto y facilitar los tests deterministas.
type ReplyLimiter interface {
	Allow(key string) bool
}

// Option configura el Runtime al construirlo (patrón funcional-options, igual que
// gatewaygrpc.Server).
type Option func(*Runtime)

// WithEventSink añade un EventSink al fan-out de efectos del runtime (Plan 015 ·
// T2). Se puede pasar varias veces (se acumulan).
func WithEventSink(sink EventSink) Option {
	return func(rt *Runtime) { rt.sinks = append(rt.sinks, sink) }
}

// WithPresignClient inyecta el Presigner que el runtime usa para firmar la key de
// un adjunto antes de despacharlo por Sender.SendMedia (Plan 017 · T4). Sin él, un
// nodo media produce un error controlado en el envío (no un pánico).
func WithPresignClient(p Presigner) Option {
	return func(rt *Runtime) { rt.presigner = p }
}

// WithTriggerResolver inyecta el trigger.Resolver que el runtime consulta ante un
// entrante sin conversación viva (arranque por palabra clave/fallback) y para el
// escape global (Plan 019 · T3/T4). Sin él, New usa trigger.NewNoopResolver()
// (no-regresión total, INV-6: un entrante sin estado se ignora igual que antes).
func WithTriggerResolver(r trigger.Resolver) Option {
	return func(rt *Runtime) { rt.triggers = r }
}

// WithReplyLimiter inyecta el token-bucket que acota las auto-respuestas por
// conversación (Plan 020 · T0, red anti-loop). Sin él, el runtime no aplica tope
// (nil ⇒ no-regresión: se responde igual que antes del Plan 020).
func WithReplyLimiter(l ReplyLimiter) Option {
	return func(rt *Runtime) { rt.replyLimiter = l }
}

// WithSelfNumbers inyecta el checker que decide si un número es propio del tenant y
// alimenta la guarda anti-self-loop (Plan 020 · T2). Sin él, el runtime no aplica
// la guarda (nil ⇒ no-regresión: se procesa igual que antes del Plan 020).
//
// Conserva el nombre WithSelfNumbers pese a que el puerto pasó de lista a predicado
// en el Plan 046 · T4.1: renombrar la Option obligaría a tocar todos los llamantes
// sin aportar nada, y el nombre sigue describiendo QUÉ se inyecta (la fuente de
// verdad de los números propios), no CÓMO se consulta.
func WithSelfNumbers(c SelfNumberChecker) Option {
	return func(rt *Runtime) { rt.selfNumbers = c }
}

// WithIngestDeduper inyecta el deduper persistente de entrantes (Plan 028 · T6,
// ADR-0003). Sin él, el runtime no deduplica los reenvíos del outbox (nil ⇒
// no-regresión: queda solo la idempotencia consecutiva por last_wa_message_id).
func WithIngestDeduper(d IngestDeduper) Option {
	return func(rt *Runtime) { rt.deduper = d }
}

// WithEntitlements inyecta el resolver de features del tenant (ADR-0022, Plan 029 ·
// T7): el gate de VERDAD que decide si una intención LLM del entrante alimenta la
// Signal del resolver de disparos. Sin él (nil), el runtime DESCARTA el intent
// siempre (no-regresión: comportamiento idéntico al previo al clasificador).
func WithEntitlements(r entitlements.Resolver) Option {
	return func(rt *Runtime) { rt.entitlements = r }
}

// ⚰️ Aquí vivió, del 2026-08-10 al 2026-08-22, la Option que encendía el productor
// `message` del hilo. La retiró el Plan 044 · T1.6 junto con su campo y su variable
// de entorno: era el interruptor de DESPLIEGUE, y el propio thread.go le puso fecha
// de caducidad el día que nació. El gate por tenant (`llm_intake`,
// WithEntitlements) NO se toca y sigue siendo fail-closed.
//
// ⚠️ LA LÍNEA EN BLANCO DE ABAJO ES OBLIGATORIA Y NO ES ESTILO. Sin ella esta lápida
// y el doc-comment de WithClock —que es EXPORTADA— forman UN SOLO bloque, y revive con
// la regla `exported` (.golangci.yml) falla con «comment on exported function WithClock
// should be of the form...». Una línea `//` vacía NO separa: para gofmt y para go/doc
// sigue siendo el mismo comentario. El separador tiene que ser una línea VACÍA de verdad.

// WithClock inyecta el reloj que el TTL conversacional usa para decidir si un estado
// vivo venció (Plan 029 · T9). Sin él, New usa time.Now. Existe para tests
// deterministas del TTL.
func WithClock(now func() time.Time) Option {
	return func(rt *Runtime) {
		if now != nil {
			rt.now = now
		}
	}
}

// WithDepositReminder cablea el recordatorio perezoso de la seña al camino del
// entrante (Plan 041 · T4.4): cuando un contacto vuelve a escribir, se evalúa si
// alguna solicitud suya tiene la seña vencida y sin recordar. Sin él (nil), el motor
// no evalúa nada — no-regresión total.
func WithDepositReminder(d DepositReminder) Option {
	return func(rt *Runtime) { rt.deposits = d }
}

// WithEventStore cablea el almacén del evento conversacional (Plan 043 · Ola 2).
// Sin él, el motor se comporta EXACTAMENTE como antes del Plan 043: un event_start
// arranca su flujo sin parir evento y un event_stop no desactiva nada (INV-6).
func WithEventStore(s EventStore) Option {
	return func(rt *Runtime) { rt.events = s }
}

// WithIntakeAbandoner cablea el cierre de la solicitud que acompaña al cierre de un
// evento por E-11. Va junto a WithEventStore: sin él, el evento se cierra y su
// solicitud queda huérfana (se avisa por WARN), que es media verdad y no la que
// queremos en producción.
func WithIntakeAbandoner(a IntakeAbandoner) Option {
	return func(rt *Runtime) { rt.intakes = a }
}

// WithDispatcher cablea el despachador de nivel superior (Plan 043 · T2.3). Sin él,
// un event_start de tipo menu crea el evento pero no presenta ninguna lista.
func WithDispatcher(d Dispatcher) Option {
	return func(rt *Runtime) { rt.dispatcher = d }
}

// WithSummarySources cablea las fuentes durables del resumen del evento abandonado
// (Plan 043 · T3.4): líneas del pedido y respuestas de la encuesta. Van JUNTAS —un
// struct y no dos Options— para que el día que un tipo nuevo traiga su fuente se añada
// un campo y ninguna firma se mueva.
//
// Cablear solo una es peor que no cablear ninguna: LoadSummary falla cuando le falta el
// lector del tipo que le toca, así que el abandono de ese tipo produciría un WARN en
// cada salto en vez de una fila.
func WithSummarySources(src events.SummarySources) Option {
	return func(rt *Runtime) { rt.sources = src }
}

// WithOpeningBuilder cablea el constructor de la entrada que OFRECE (Plan 043 · T3.8).
// Va con WithDispatcher —en producción lo satisface el MISMO *events.Dispatcher— pero
// es una Option aparte a propósito: son dos preguntas distintas y un despliegue puede
// tener cableada una sin la otra. Sin ella, la rama `fallback` de handleTrigger es
// byte a byte la de siempre (INV-6).
func WithOpeningBuilder(b OpeningBuilder) Option {
	return func(rt *Runtime) { rt.opening = b }
}

// WithFlowForKind cablea la resolución «tipo de evento → flujo del tenant», que
// necesita la opción «empezar uno nuevo» del menú. Va con WithDispatcher: sin ella,
// elegir un tipo en el menú crearía su evento sin arrancar nada.
func WithFlowForKind(f FlowForKind) Option {
	return func(rt *Runtime) { rt.flows = f }
}

// WithIncomingTimeout fija el deadline con que OnIncoming acota cada entrante
// reactivo (Plan 027 · Ola 0 · T1, cierra H1). Un valor <=0 se ignora y New cae a
// defaultIncomingTimeout (el camino caliente nunca queda sin deadline).
func WithIncomingTimeout(d time.Duration) Option {
	return func(rt *Runtime) { rt.incomingTimeout = d }
}

// WithMaxConcurrentIncoming fija el tope de entrantes procesados a la vez (Plan 027
// · Ola 1 · T5, cierra H5). n==0 cae al default; n<0 desactiva el techo (sin
// semáforo, comportamiento previo). New construye el semáforo a partir de este valor.
func WithMaxConcurrentIncoming(n int) Option {
	return func(rt *Runtime) { rt.maxConcurrentIncoming = n }
}

// WithResumePolicy registra la ResumePolicy de un módulo bajo su tipo de nodo (Plan
// 027 · Ola 3 · T8, cierra H9): el runtime la consulta al reanudar/reabrir una
// conversación en un nodo de ese tipo. Sin registrar ninguna, ningún nodo reanuda
// (no-regresión: menú/encuesta nunca la necesitan).
func WithResumePolicy(nodeType string, p modules.ResumePolicy) Option {
	return func(rt *Runtime) {
		if rt.resumePolicies == nil {
			rt.resumePolicies = make(map[string]modules.ResumePolicy)
		}
		rt.resumePolicies[nodeType] = p
	}
}

// WithReactiveBlockedHook inyecta el contador de entrantes que NO entran al motor
// reactivo (motivo passive|self_loop|rate_limit). Se cablea con
// metrics.FlowReactiveBlocked; sin él no se cuenta nada y los cortes se comportan
// igual (el hook NO decide: solo observa).
func WithReactiveBlockedHook(fn func(reason string)) Option {
	return func(rt *Runtime) { rt.onReactiveBlocked = fn }
}

// WithAutoreplyStreakHook inyecta el observador de rachas de auto-respuestas (Plan
// 049 · Opción A): recibe la LONGITUD de cada racha al CERRARSE el episodio, una vez
// por episodio. Se cablea con metrics.FlowAutoreplyStreak; sin él el contador sigue
// contando (el gauge de la racha viva funciona igual) y simplemente nadie recoge el
// histograma. El hook NO decide: solo observa — igual que WithReactiveBlockedHook.
func WithAutoreplyStreakHook(fn func(racha int)) Option {
	return func(rt *Runtime) { rt.onAutoreplyStreak = fn }
}

// WithAggregator cablea el AGREGADOR DE VENTANAS de captación (Plan 044 · Ola 1 ·
// T1.1/T1.2; ver aggregator.go). Sin él —el default— el motor no abre ninguna
// ventana ni escribe una sola fila en intake_jobs: no-regresión total.
//
// ⚠️ Cablearlo NO basta para que las ventanas se cierren: el cierre lo ejecuta el
// BARRIDO (IntakeAggregator.Run), que se arranca aparte en bootstrap. Con el sink
// cableado y el barrido sin arrancar, las ventanas se abrirían y se quedarían en
// `aggregating` para siempre — y el fallo sería MUDO. Van juntos.
func WithAggregator(a *IntakeAggregator) Option {
	return func(rt *Runtime) { rt.aggregator = a }
}

// New construye el Runtime con sus dependencias. Las opcionales (sinks de
// efectos) se pasan como Option; sin ninguna, el fan-out queda en LogSink
// (log-only) por defecto.
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
	// para que reordenar dos WithEventSink en bootstrap.go deje de poder romper la
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
	rt.autoreplyStreaks = newStreakCounter(0, 0, func(racha int) {
		if rt.onAutoreplyStreak != nil {
			rt.onAutoreplyStreak(racha)
		}
	})
	return rt
}

// MaxAutoreplyStreak devuelve la racha de auto-respuestas VIVA más larga en este
// instante (Plan 049 · Opción A). Es la FUENTE DEL GAUGE
// wapp_flow_autoreply_streak_max, y se cablea en el arranque con
// metrics.SetFlowAutoreplyStreakMaxSource — la dependencia va al revés que el hook
// del histograma porque un gauge se TIRA en el scrape en vez de empujarse.
//
// 🔴 PENSADA PARA EL SCRAPE (cada 15-60 s), JAMÁS PARA EL CAMINO CALIENTE: recorre
// el mapa entero de conversaciones vivas BAJO EL CANDADO del contador, así que es
// O(conversaciones vivas) y además serializa contra todos los Inc/Close que estén
// ocurriendo. Llamarla por cada entrante metería ese recorrido dentro del p99 del
// handler, que ya está en 71 ms contra un objetivo de < 50 ms.
//
// 🔴 Y NO ES UN GETTER PURO: streakCounter.Max BARRE de paso las rachas vencidas por
// inactividad y las reporta al histograma. Es DELIBERADO —el scrape es lo único que
// pasa por aquí con regularidad sin violar el ADR-0003— y está explicado en Max. La
// consecuencia para quien la llame desde otro sitio: llamarla es OBSERVAR, no solo
// leer. No la uses como sonda barata en un test sin saber que puede cerrar episodios.
//
// La firma NO lleva `now` a propósito: es `func() int` porque ese es el contrato que
// bootstrap inyecta como fuente del GaugeFunc de Prometheus
// (SetFlowAutoreplyStreakMaxSource). El instante lo pone el reloj INYECTABLE del
// runtime (rt.now, WithClock), que es lo que hace testeable el vencimiento desde el
// motor con un reloj falso.
//
// Nil-safe de arriba abajo: sobre un contador nil devuelve 0 (streakCounter.Max), y
// sobre un Runtime que no salió de New —sin reloj— devuelve 0 en vez de entrar en
// pánico al desreferenciar rt.now.
func (rt *Runtime) MaxAutoreplyStreak() int {
	if rt == nil || rt.now == nil {
		return 0
	}
	return rt.autoreplyStreaks.Max(rt.now())
}
