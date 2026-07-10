package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// defaultEscapeMessage es el aviso corto que se envía al cortar una conversación
// viva por escape global (Plan 019 · T4) cuando la regla de escape que casó NO
// define un aviso propio. Si la regla trae message (columna flow_triggers.message,
// Plan 019 · T4b), handleEscape lo usa en su lugar.
const defaultEscapeMessage = "Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."

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
// vez garantiza que un Edge mudo libere la clave. Se sobreescribe con
// WithIncomingTimeout (env WAPP_FLOW_INCOMING_TIMEOUT).
const defaultIncomingTimeout = 30 * time.Second

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva
// para la clave (T3 lo mapea a HTTP 409). Se inspecciona con errors.Is.
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

// FlowStore es el subconjunto SEGREGADO de store.Repository que el Runtime
// necesita (ISP, Plan 027 · Ola 2 · T9, cierra H12): estado conversacional +
// lectura de definiciones + lectura de la orden abierta y de los ajustes del carrito
// (para el TTL/reanudación). NO incluye las ESCRITURAS de órdenes/efectos/resultados
// —eso lo consume el PersistSink—, así el runtime declara solo lo que usa. Un
// *store.PostgresRepository / *store.MemoryRepository lo satisface sin cambios.
type FlowStore interface {
	store.ConversationStore
	store.DefinitionReader
	store.OrderReader
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
	// selfNumbers entrega el conjunto de números propios (self_pn) del tenant para
	// la guarda anti-self-loop (Plan 020 · T2): un entrante cuyo from_pn casa un
	// número propio de OTRA sesión del MISMO tenant NO auto-responde. nil (sin
	// WithSelfNumbers) desactiva la guarda: no-regresión total (sin self_pn poblado
	// el comportamiento es idéntico al previo al 020).
	selfNumbers SelfNumberLister
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

// WithSelfNumbers inyecta el lister del conjunto de números propios del tenant que
// alimenta la guarda anti-self-loop (Plan 020 · T2). Sin él, el runtime no aplica
// la guarda (nil ⇒ no-regresión: se procesa igual que antes del Plan 020).
func WithSelfNumbers(l SelfNumberLister) Option {
	return func(rt *Runtime) { rt.selfNumbers = l }
}

// WithIngestDeduper inyecta el deduper persistente de entrantes (Plan 028 · T6,
// ADR-0003). Sin él, el runtime no deduplica los reenvíos del outbox (nil ⇒
// no-regresión: queda solo la idempotencia consecutiva por last_wa_message_id).
func WithIngestDeduper(d IngestDeduper) Option {
	return func(rt *Runtime) { rt.deduper = d }
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

// reactiveBlocked agrupa las guardas de BORDE que impiden entrar al motor reactivo
// (Plan 020). Devuelve true (y NO se procesa el entrante) si:
//   - la sesión es PASSIVE (T1): escucha/transporta pero no dispara triggers, no
//     avanza con auto-envío ni escapa. Una conversación EN CURSO deja de avanzar
//     mientras siga passive (no se borra su estado; vuelve si se re-marca bot). Rol
//     vacío/desconocido ⇒ bot (no-regresión).
//   - el remitente es un número PROPIO del tenant (T2, anti-self-loop): una sesión
//     propia hablando; no se auto-responde (defensa semántica contra el bucle
//     sesión↔sesión del Plan 019).
//
// Sin rol passive y sin self_pn poblado, devuelve false ⇒ no-regresión total.
func (rt *Runtime) reactiveBlocked(ctx context.Context, tenantID, sessionID, role, fromPn string) bool {
	if role == rolePassive {
		rt.log.Debug("runtime: sesión passive; motor reactivo omitido", "session_id", sessionID)
		return true
	}
	return rt.isSelfLoop(ctx, tenantID, sessionID, fromPn)
}

// isSelfLoop decide si un entrante proviene de un número PROPIO del tenant (una
// sesión propia hablando), en cuyo caso NO se debe auto-responder (Plan 020 · T2,
// defensa semántica contra el bucle sesión↔sesión del Plan 019). Normaliza el
// remitente (from_pn) con el MISMO normalizador que el paquete contact y lo compara
// contra el conjunto de self_pn del tenant. Es CONSERVADORA hacia procesar: sin
// lister (nil), sin from_pn, si el número no normaliza o si el lookup falla ⇒
// devuelve false (no bloquea: la ausencia de dato no debe silenciar tráfico
// legítimo). NUNCA loguea el número (PII): solo el hecho y IDs opacos.
func (rt *Runtime) isSelfLoop(ctx context.Context, tenantID, sessionID, fromPn string) bool {
	if rt.selfNumbers == nil || fromPn == "" {
		return false
	}
	norm, err := contact.Normalize(contact.KindPhoneE164, fromPn)
	if err != nil {
		return false // sin número normalizable no se puede afirmar self-loop.
	}
	nums, err := rt.selfNumbers.SelfNumbers(ctx, tenantID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo cargar self_pn del tenant; guarda anti-self-loop omitida",
			"error", err, "session_id", sessionID)
		return false
	}
	for _, n := range nums {
		if n == norm {
			rt.log.Warn("runtime: entrante de un número propio del tenant; auto-respuesta evitada (anti-self-loop)",
				"tenant_id", tenantID, "session_id", sessionID)
			return true
		}
	}
	return false
}

// replyAllowed comprueba el token-bucket de auto-respuestas para la clave (Plan
// 020 · T0). Devuelve true si se puede auto-responder; false (y loguea el hecho SIN
// PII: solo IDs opacos tenant/session/contact, nunca el texto ni el número) si la
// conversación excedió su tope. Con replyLimiter nil siempre permite (no-regresión).
func (rt *Runtime) replyAllowed(key store.Key) bool {
	if rt.replyLimiter == nil || rt.replyLimiter.Allow(key.String()) {
		return true
	}
	rt.log.Warn("runtime: auto-respuesta limitada por rate-limit de conversación",
		"tenant_id", key.TenantID,
		"session_id", key.SessionID,
		"contact_id", key.ContactID,
	)
	return false
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
	// Semáforo de entrantes (Plan 027 · Ola 1 · T5): 0 ⇒ default; <0 ⇒ sin techo
	// (incomingSem queda nil y OnIncoming no acota la concurrencia).
	switch {
	case rt.maxConcurrentIncoming == 0:
		rt.incomingSem = make(chan struct{}, defaultMaxConcurrentIncoming)
	case rt.maxConcurrentIncoming > 0:
		rt.incomingSem = make(chan struct{}, rt.maxConcurrentIncoming)
	}
	return rt
}

// Start abre una conversación por API (design.md §6, decisión C): bajo el
// single-flight de la clave, si ya existe estado → ErrConversationExists; si
// no, fija la versión vigente, renderiza el nodo inicial (el menú), persiste y
// envía. Devuelve el último Ack del envío (el del último texto emitido) o nil
// si no hubo salidas.
func (rt *Runtime) Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	// Resuelve la ref del admin a un contact_id OPACO antes de clavar la key: el
	// motor opera por contact_id, no por el JID/ref crudo (Plan 010, design.md §6).
	contactID, err := rt.contacts.Resolve(ctx, tenantID, []contact.Ref{ref}, "")
	if err != nil {
		return nil, fmt.Errorf("runtime: resolver contacto: %w", err)
	}
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	unlock := rt.locks.lock(key)
	defer unlock()
	return rt.startLocked(ctx, tenantID, flowID, sessionID, key, contactID)
}

// startLocked es el cuerpo de Start SIN tomar el keyedMutex: asume que el llamante
// YA lo tiene tomado sobre `key`, con el contact_id ya resuelto. Lo comparten Start
// (API /admin/flows/start, /api/v1/.../start — toma el mutex y delega) y el enganche
// por palabra clave de HandleIncoming (Plan 019 · T3), que YA tomó el mutex sobre la
// misma clave: re-llamar a Start ahí causaría un auto-deadlock. Reglas de arranque
// (guard 409, reinicio de carrito, orden Save-antes-de-Send) son idénticas.
func (rt *Runtime) startLocked(ctx context.Context, tenantID, flowID, sessionID string, key store.Key, contactID string) (*cloudlinkv1.Ack, error) {
	def, err := rt.store.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		return nil, fmt.Errorf("runtime: definición vigente: %w", err)
	}

	exists, err := rt.store.Exists(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("runtime: comprobar existencia: %w", err)
	}
	if exists {
		// Gotcha 409 (design.md §3.4): una conversación de CARRITO cuyo pedido ya
		// TERMINÓ (sub-máquina cerrada/cancelada, o con una orden "open" vencida por
		// TTL) NO debe bloquear un pedido nuevo. Solo el carrito se reinicia, y solo
		// si está terminado: un carrito EN CURSO (navegando, u orden abierta vigente)
		// y cualquier conversación de menú/encuesta siguen devolviendo 409. Al
		// reiniciar, el Save de Enter (upsert por la misma clave) SOBRESCRIBE el
		// estado viejo con uno limpio.
		restart, rerr := rt.restartableOnStart(ctx, def, key, tenantID, contactID, sessionID)
		if rerr != nil {
			return nil, rerr
		}
		if !restart {
			return nil, ErrConversationExists
		}
	}

	st := model.Conversation{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	st, outs, err := rt.engine.Enter(ctx, def, st)
	if err != nil {
		return nil, fmt.Errorf("runtime: enter: %w", err)
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	return rt.send(ctx, sessionID, to, outs)
}

// duplicateIngest es la guarda de dedupe PERSISTENTE de entrantes (Plan 028 · T6,
// ADR-0003): el outbox durable del Edge (Plan 027 Ola 3) reenvía frames tras
// reconexión ⇒ semántica at-least-once. La idempotencia consecutiva por
// last_wa_message_id (dentro de HandleIncoming) solo corta la RE-ENTREGA INMEDIATA;
// un duplicado INTERCALADO (A, B, A) o el reenvío de un entrante que dispara/escapa
// un flujo (caminos que NO tocan last_wa_message_id) se colaría. Aquí, ANTES de
// tocar el motor (resolver tenant/contacto, tomar el keyedMutex, cargar estado o
// correr efectos), se registra la clave (session_id, wa_message_id) en una tabla
// idempotente: si ya se vio ⇒ true (el llamante descarta el frame sin re-procesar
// efectos ni auto-responder). La clave única de la tabla resuelve además dos
// duplicados CONCURRENTES (cada entrante corre en su goroutine): exactamente uno
// inserta y procesa. Un wa_message_id vacío (evento sintético, no esperable en
// entrantes reales) NO se deduplica: cae al camino de siempre. Sin deduper cableado
// (nil) tampoco deduplica (no-regresión). Un fallo del deduper es best-effort
// (fail-open): se LOGUEA y devuelve false (se prefiere reprocesar a perder el
// entrante), coherente con las guardas best-effort del motor (p.ej. IsEscape).
func (rt *Runtime) duplicateIngest(ctx context.Context, sessionID string, m *cloudlinkv1.IncomingMessage) bool {
	if rt.deduper == nil || m.GetWaMessageId() == "" {
		return false
	}
	seen, err := rt.deduper.Seen(ctx, sessionID, m.GetWaMessageId())
	if err != nil {
		rt.log.Warn("runtime: dedupe de ingesta falló; se continúa (fail-open)",
			"error", err, "session_id", sessionID, "wa_message_id", m.GetWaMessageId())
		return false
	}
	if seen {
		rt.log.Debug("runtime: entrante duplicado ignorado (dedupe de ingesta)",
			"session_id", sessionID, "wa_message_id", m.GetWaMessageId())
	}
	return seen
}

// consecutiveReplay es la idempotencia CONSECUTIVA (design.md §10.G): corta la
// re-entrega INMEDIATA de un mensaje comparándolo con el último procesado en el
// estado del flujo (last_wa_message_id). Complementa —no reemplaza— el dedupe
// persistente (duplicateIngest), que cubre además los duplicados intercalados y los
// caminos que no tocan last_wa_message_id (disparo/escape).
func consecutiveReplay(st model.Conversation, m *cloudlinkv1.IncomingMessage) bool {
	return st.LastWaMessageID != "" && st.LastWaMessageID == m.GetWaMessageId()
}

// HandleIncoming avanza una conversación EXISTENTE con un entrante (design.md
// §6). Resuelve el tenant, serializa por clave y:
//   - si no hay estado vivo → lo IGNORA (return nil; decisión C: un entrante no
//     inicia flujo);
//   - si el wa_message_id coincide con el último procesado → idempotencia
//     (return nil, no reenvía; design.md §10.G);
//   - en otro caso avanza con engine.Step sobre la versión con la que arrancó
//     (Conversation.FlowVersion), persiste y envía.
//
// Orden: persiste el estado ANTES de enviar (design.md §6). Tradeoff aceptado
// en este corte: si el SendText falla, el paso NO se reenvía porque el estado
// ya avanzó (preferimos no duplicar el avance a costa de un texto perdido).
func (rt *Runtime) HandleIncoming(ctx context.Context, sessionID string, m *cloudlinkv1.IncomingMessage) error {
	// Dedupe PERSISTENTE de ingesta (Plan 028 · T6, ADR-0003): un reenvío del outbox
	// del Edge se corta ANTES de tocar el motor. Ver duplicateIngest.
	if rt.duplicateIngest(ctx, sessionID, m) {
		return nil
	}
	tenantID, role, err := rt.resolver.ResolveTenant(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("runtime: resolver tenant: %w", err)
	}
	// Guardas de BORDE del motor reactivo (Plan 020 · T1 passive + T2 anti-self-loop):
	// se cortan ANTES de resolver contacto, tomar el keyedMutex o cargar estado; la
	// escucha y los acuses (vía separada del Gateway) no se ven afectados.
	if rt.reactiveBlocked(ctx, tenantID, sessionID, role, m.GetFromPn()) {
		return nil
	}
	// Resuelve la identidad enriquecida del entrante (from_pn/from_lid, con
	// fallback al JID crudo) a un contact_id OPACO antes de clavar la key: así el
	// mismo contacto casa el MISMO estado aunque el JID llegue como número o LID
	// (Plan 010, design.md §5, §6).
	refs := contact.RefsFrom(m.GetFromPn(), m.GetFromLid(), m.GetFrom())
	contactID, err := rt.contacts.Resolve(ctx, tenantID, refs, m.GetPushName())
	if err != nil {
		return fmt.Errorf("runtime: resolver contacto: %w", err)
	}
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	unlock := rt.locks.lock(key)
	defer unlock()

	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: cargar estado: %w", err)
	}
	if !ok {
		// Sin conversación viva: consulta el resolver de disparos (Plan 019 · T3).
		// Con NoopResolver (default) devuelve Ignore ⇒ return nil idéntico a la
		// decisión C histórica (INV-6). El contexto (tenantID, contactID, key,
		// sessionID) ya está resuelto ⇒ se arranca sin re-resolver el contacto.
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, m)
	}
	// Escape global (Plan 019 · T4): sobre una conversación viva, ANTES de
	// despachar el entrante al engine, si el texto casa una regla de escape del
	// tenant se corta la conversación y se avisa. Bloque autocontenido: si NO es
	// escape, el camino normal queda idéntico (INV-5 no-regresión). Un fallo de
	// IsEscape es best-effort: se LOGUEA y NO bloquea el avance normal (no aborta).
	if esc, escMsg, escErr := rt.triggers.IsEscape(ctx, tenantID, sessionID, m.GetText()); escErr != nil {
		rt.log.Warn("runtime: IsEscape falló; se ignora el escape", "error", escErr, "session_id", sessionID)
	} else if esc {
		return rt.handleEscape(ctx, tenantID, sessionID, key, contactID, escMsg)
	}
	if consecutiveReplay(st, m) {
		// Re-entrega INMEDIATA del mismo mensaje → no avanzar ni reenviar.
		return nil
	}

	def, err := rt.store.GetDefinition(ctx, tenantID, st.FlowID, st.FlowVersion)
	if err != nil {
		return fmt.Errorf("runtime: definición en curso (v%d): %w", st.FlowVersion, err)
	}

	// Reanudación por módulo (Plan 027 · Ola 3 · T8): TTL perezoso + auto-reinicio +
	// siembra de Vars, GATEADO por la ResumePolicy registrada para el tipo de nodo (un
	// no-op para menú/encuesta ⇒ comportamiento idéntico). handled=true ⇒ el turno se
	// consumió reiniciando.
	if handled, cerr := rt.prepareResume(ctx, sessionID, &st, def, m, tenantID, contactID); cerr != nil {
		return cerr
	} else if handled {
		return nil
	}

	st, outs, effects, err := rt.engine.Step(ctx, def, st, engine.Input{Text: m.GetText()})
	if err != nil {
		return fmt.Errorf("runtime: step: %w", err)
	}
	st.LastWaMessageID = m.GetWaMessageId()
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar estado: %w", err)
	}
	// Fan-out EN PROCESO (ADR-0003, sin broker) de los efectos declarados por el
	// módulo (Plan 015 · T2, segunda costura). Va DESPUÉS del Save (el estado ya
	// está persistido) y respeta el orden Save-antes-de-Send, igual que el flush.
	// Un fallo de un sink se LOGUEA y NO aborta el avance ni corta el resto de
	// sinks/efectos. En T2 nadie emite (effects vacío) ⇒ el bucle no itera ⇒
	// no-regresión total (Menú/Encuesta idénticos); el flush viejo sigue por su
	// vía hasta T3.
	// Fan-out EN PROCESO (ADR-0003, sin broker) de los efectos declarados por el
	// módulo (Plan 015 · T3). Es AHORA la única vía por la que survey_results se
	// materializa: el módulo survey DECLARA un Effect{persist,survey_answer} por
	// cada respuesta válida y el PersistSink inyectado (main) escribe flow_events y
	// proyecta survey_results (la MISMA fila que producía el flush del Plan 014).
	// Va DESPUÉS del Save (el estado ya está persistido) y respeta el orden
	// Save-antes-de-Send. La idempotencia es HEREDADA de la dedupe por
	// last_wa_message_id (reprocesar el mismo entrante corta antes del Step). Un
	// fallo de un sink se LOGUEA y NO aborta el avance ni corta el resto de
	// sinks/efectos.
	ec := EffectContext{TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion}
	rt.dispatch(ctx, ec, effects, sessionID)
	return rt.sendReply(ctx, tenantID, sessionID, contactID, key, outs)
}

// sendReply auto-responde al avance de un entrante respetando el tope anti-loop
// (Plan 020 · T0): SOLO si hay salidas consume un token de la conversación antes de
// resolver el destino y enviar; agotado ⇒ no envía (corta el bucle; el estado ya
// avanzó y se persistió, así que no se corrompe). Sin salidas es un no-op que NO
// gasta cuota. Extraído de HandleIncoming para acotar su complejidad ciclomática.
func (rt *Runtime) sendReply(ctx context.Context, tenantID, sessionID, contactID string, key store.Key, outs []engine.Output) error {
	if len(outs) == 0 {
		return nil
	}
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, outs)
	return err
}

// handleTrigger resuelve un entrante SIN conversación viva contra el trigger.Resolver
// (Plan 019 · T3). Con el resolver por defecto (Noop) devuelve Ignore ⇒ return nil,
// idéntico a la decisión C histórica (INV-6). Un error del resolver se LOGUEA y NO
// aborta la recepción (REQ-A7: el entrante simplemente se ignora). Ante Start/Fallback
// arranca el flujo por startLocked (el keyedMutex de la clave YA está tomado por
// HandleIncoming; llamar a Start re-tomaría el mutex y causaría auto-deadlock). Un
// ErrConversationExists (carrera con otro entrante) se trata como benigno (log + nil).
func (rt *Runtime) handleTrigger(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, m *cloudlinkv1.IncomingMessage) error {
	dec, err := rt.triggers.Resolve(ctx, tenantID, sessionID, m.GetText())
	if err != nil {
		rt.log.Warn("runtime: resolver de disparos falló; se ignora el entrante",
			"error", err, "session_id", sessionID)
		return nil
	}
	switch dec.Action {
	case trigger.Start, trigger.Fallback:
		// Red anti-loop (Plan 020 · T0): el arranque por disparo SIEMPRE auto-responde
		// (renderiza el nodo inicial), así que consume un token; agotado ⇒ no arranca
		// (corta el bucle de fallback destapado en el e2e del Plan 019). Ignore no llega
		// aquí ⇒ no gasta cuota.
		if !rt.replyAllowed(key) {
			return nil
		}
		if _, serr := rt.startLocked(ctx, tenantID, dec.FlowID, sessionID, key, contactID); serr != nil {
			if errors.Is(serr, ErrConversationExists) {
				rt.log.Info("runtime: disparo abortado por conversación ya viva (carrera benigna)",
					"session_id", sessionID)
				return nil
			}
			return serr
		}
		return nil
	default: // trigger.Ignore (o cualquier otro): decisión C, no arranca nada.
		return nil
	}
}

// handleEscape corta una conversación viva por escape global (Plan 019 · T4): libera
// la clave borrando el flow_state (idempotente) y envía un aviso corto por el MISMO
// mecanismo de salida del runtime (send). El aviso es el configurado en la regla de
// escape que casó (message, Plan 019 · T4b); si viene vacío se usa defaultEscapeMessage.
// Tras el borrado, un entrante posterior vuelve a pasar por el resolver (Resolve), no
// por escape. El estado ya se borró (equivalente al orden Save-antes-de-Send): un
// fallo del envío se surface al llamante.
func (rt *Runtime) handleEscape(ctx context.Context, tenantID, sessionID string, key store.Key, contactID, message string) error {
	// Red anti-loop (Plan 020 · T0): el aviso de escape es una auto-respuesta ⇒
	// consume un token. Agotado ⇒ no se corta ni se avisa (la conversación sigue
	// viva); rompe cualquier bucle en el que un aviso de escape realimente al peer.
	if !rt.replyAllowed(key) {
		return nil
	}
	if err := rt.store.Delete(ctx, key); err != nil {
		return fmt.Errorf("runtime: cerrar conversación por escape: %w", err)
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	notice := message
	if notice == "" {
		notice = defaultEscapeMessage
	}
	if _, err := rt.send(ctx, sessionID, to, []engine.Output{{Text: notice}}); err != nil {
		return err
	}
	return nil
}

// dispatch hace el fan-out EN PROCESO (ADR-0003, sin broker) de los efectos por
// cada EventSink registrado. Un fallo de un sink se LOGUEA y NO aborta el avance
// ni corta el resto de sinks/efectos (el estado ya quedó persistido antes del
// dispatch). Lo comparten HandleIncoming (efectos que DECLARA el módulo) y el TTL
// perezoso del carrito (cart_expired SINTETIZADO por el runtime, design.md §4.3).
func (rt *Runtime) dispatch(ctx context.Context, ec EffectContext, effects []modules.Effect, sessionID string) {
	for _, eff := range effects {
		for _, sink := range rt.sinks {
			if err := sink.Handle(ctx, ec, eff); err != nil {
				rt.log.Error("runtime: sink de efecto falló",
					"error", err,
					"kind", eff.Kind,
					"name", eff.Name,
					"session_id", sessionID,
				)
			}
		}
	}
}

// prepareResume aplica, al REANUDAR una conversación en un nodo cuyo módulo declaró
// una ResumePolicy, el reinicio por estado terminal/expiración y la siembra de Vars
// de navegación (Plan 027 · Ola 3 · T8, cierra H9): saca del engine la lógica
// cart-específica. Para nodos SIN política (menú/encuesta) es un no-op total
// (handled=false, sin tocar Vars) ⇒ no-regresión. handled=true ⇒ el turno se consumió
// reiniciando (se avisó y se mostró el nodo inicial fresco).
func (rt *Runtime) prepareResume(ctx context.Context, sessionID string, st *model.Conversation, def model.Flow, m *cloudlinkv1.IncomingMessage, tenantID, contactID string) (bool, error) {
	node, ok := def.Nodes[st.CurrentNode]
	if !ok {
		return false, nil
	}
	policy, ok := rt.resumePolicies[node.Type]
	if !ok {
		return false, nil // nodo sin política de reanudación (menú/encuesta).
	}
	restart, notice, effects, err := policy.Restart(ctx, tenantID, contactID, st.Vars)
	if err != nil {
		return false, fmt.Errorf("runtime: política de reanudación: %w", err)
	}
	if !restart {
		// Navegación normal: el módulo siembra en Vars lo que necesite (page_size). El
		// runtime garantiza el mapa no-nil antes de delegar la siembra.
		if st.Vars == nil {
			st.Vars = map[string]any{}
		}
		if err := policy.Seed(ctx, tenantID, st.Vars); err != nil {
			return false, fmt.Errorf("runtime: siembra de reanudación: %w", err)
		}
		return false, nil
	}
	// Red anti-loop (Plan 020 · T0): el reinicio auto-responde (aviso + nodo inicial),
	// así que consume un token. Agotado ⇒ turno consumido SIN responder ni reiniciar.
	if !rt.replyAllowed(store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}) {
		return true, nil
	}
	// Efectos SINTETIZADOS por la política (p. ej. cart_expired) por el MISMO fan-out:
	// el proyector del módulo los materializa (orders.status=expired). Best-effort: un
	// fallo se loguea, no aborta (coherencia BD↔conversación, design.md §3.4).
	if len(effects) > 0 {
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	// Arranca LIMPIO: descarta las Vars y re-entra el flujo con la MISMA versión con
	// la que corría (def viene de GetDefinition).
	fresh := *st
	fresh.Vars = nil
	fresh, outs, err := rt.engine.Enter(ctx, def, fresh)
	if err != nil {
		return false, fmt.Errorf("runtime: reentrar tras reinicio: %w", err)
	}
	fresh.LastWaMessageID = m.GetWaMessageId()
	if err := rt.store.Save(ctx, fresh); err != nil {
		return false, fmt.Errorf("runtime: guardar conversación reiniciada: %w", err)
	}
	*st = fresh

	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return false, err
	}
	texts := outs
	if notice != "" {
		texts = append([]engine.Output{{Text: notice}}, outs...)
	}
	if _, err := rt.send(ctx, sessionID, to, texts); err != nil {
		return false, err
	}
	return true, nil
}

// restartableOnStart decide si un Start sobre una conversación EXISTENTE puede
// reiniciarse en vez de devolver 409 (gotcha, design.md §3.4), consultando la
// ResumePolicy del nodo inicial (Plan 027 · Ola 3 · T8). Sin política (menú/encuesta)
// ⇒ false (409 intacto). Si la política sintetiza efectos (p. ej. cart_expired al
// vencer la orden), se despachan (coherencia BD↔conversación) y devuelve true.
func (rt *Runtime) restartableOnStart(ctx context.Context, def model.Flow, key store.Key, tenantID, contactID, sessionID string) (bool, error) {
	node, ok := def.Nodes[def.Initial]
	if !ok {
		return false, nil
	}
	policy, ok := rt.resumePolicies[node.Type]
	if !ok {
		return false, nil
	}
	var vars map[string]any
	if st, found, err := rt.store.Load(ctx, key); err != nil {
		return false, fmt.Errorf("runtime: cargar estado: %w", err)
	} else if found {
		vars = st.Vars
	}
	restart, _, effects, err := policy.Restart(ctx, tenantID, contactID, vars)
	if err != nil {
		return false, fmt.Errorf("runtime: política de reanudación: %w", err)
	}
	if !restart {
		return false, nil
	}
	if len(effects) > 0 {
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: def.FlowID, FlowVersion: def.Version}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	return true, nil
}

// destino resuelve el contact_id a una cadena de destino DIRECCIONABLE por el
// Edge (design.md §10.E): desacopla el envío del JID entrante (doble rol, R4).
func (rt *Runtime) destino(ctx context.Context, tenantID, contactID string) (string, error) {
	dst, err := rt.contacts.Destino(ctx, tenantID, contactID)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver destino: %w", err)
	}
	to, err := dst.Sendable()
	if err != nil {
		return "", fmt.Errorf("runtime: destino no direccionable: %w", err)
	}
	return to, nil
}

// OnIncoming es el wrapper que T5 asigna a (*gatewaygrpc.Server).OnIncoming
// (func(sessionID string, m *cloudlinkv1.IncomingMessage), sin error).
//
// Despacha HandleIncoming en una goroutine y NO bloquea al llamante: el Gateway
// invoca este hook de forma SÍNCRONA dentro del loop Recv del stream del Edge
// (internal/gateway/grpc/server.go, route), y HandleIncoming hace un SendText
// que espera el Ack —Ack que ese MISMO loop Recv debe entregar (deliverAck).
// Procesar inline bloquearía el loop y causaría un deadlock por sesión. La
// serialización por conversación la sigue garantizando el keyedMutex dentro de
// HandleIncoming (cada clave se procesa de a una). Los errores se LOGUEAN sin
// propagarse ni panickear.
//
// El contexto es context.Background() (desacoplado del stream Recv, que ya
// retornó) pero ACOTADO por rt.incomingTimeout (Plan 027 · Ola 0 · T1, cierra
// H1): sin deadline, el SendText interno esperaría el Ack contra un ctx.Done()
// que nunca dispara ⇒ goroutine fugada reteniendo el keyedMutex y cuñando la
// conversación. El timeout garantiza que un Edge mudo libere la clave.
func (rt *Runtime) OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), rt.incomingTimeout)
		defer cancel()
		// Semáforo de concurrencia (Plan 027 · Ola 1 · T5, cierra H5): se adquiere el
		// cupo DENTRO de la goroutine para no bloquear el loop Recv del stream. Si no
		// hay cupo dentro del incomingTimeout, se descarta el entrante con log (sin
		// PII): bajo saturación sostenida es preferible soltar uno a acumular
		// goroutines colgadas sin techo. Sin semáforo (incomingSem nil) no acota.
		if rt.incomingSem != nil {
			select {
			case rt.incomingSem <- struct{}{}:
				defer func() { <-rt.incomingSem }()
			case <-ctx.Done():
				rt.log.Warn("runtime: entrante descartado por saturación (sin cupo en el pool a tiempo)",
					"session_id", sessionID, "wa_message_id", m.GetWaMessageId())
				return
			}
		}
		if err := rt.HandleIncoming(ctx, sessionID, m); err != nil {
			rt.log.Error("runtime: procesar entrante",
				"error", err,
				"session_id", sessionID,
				"wa_message_id", m.GetWaMessageId(),
			)
		}
	}()
}

// send empuja cada salida por el Sender en orden y devuelve el último Ack. Una
// salida con Media (nodo media, Plan 017 §4.2) se PRESIGNA y despacha por
// SendMedia; el resto por SendText. Ante el primer error corta y lo devuelve
// (con el último Ack logrado). El estado ya se persistió antes de llamar a send
// (orden Save-antes-de-Send), así que un fallo aquí NO corrompe el estado: se
// devuelve para que el llamante lo LOGUEE (OnIncoming) o lo surface (Start).
func (rt *Runtime) send(ctx context.Context, sessionID, to string, outs []engine.Output) (*cloudlinkv1.Ack, error) {
	var last *cloudlinkv1.Ack
	for _, out := range outs {
		if out.Media != nil {
			ack, err := rt.sendMedia(ctx, sessionID, to, out.Media)
			if err != nil {
				return last, err
			}
			last = ack
			continue
		}
		ack, err := rt.sender.SendText(ctx, sessionID, to, out.Text)
		if err != nil {
			return last, fmt.Errorf("runtime: enviar texto: %w", err)
		}
		last = ack
	}
	return last, nil
}

// sendMedia presigna la key del adjunto y lo despacha por Sender.SendMedia (Plan
// 017 §4.2/§9.C): el runtime presigna, el módulo no. Exige un Presigner cableado
// (WithPresignClient); su ausencia es un error de configuración explícito (un nodo
// media sin almacén), no un pánico. La URL prefirmada es un capability token de
// corta vida; el binario nunca viaja por la nube ni por gRPC (zero-knowledge).
func (rt *Runtime) sendMedia(ctx context.Context, sessionID, to string, ref *model.MediaRef) (*cloudlinkv1.Ack, error) {
	if rt.presigner == nil {
		return nil, fmt.Errorf("runtime: nodo media sin PresignClient configurado (usa WithPresignClient)")
	}
	url, _, err := rt.presigner.GenerateDownloadURL(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("runtime: presignar media %q: %w", ref.Key, err)
	}
	ack, err := rt.sender.SendMedia(ctx, sessionID, to, url, ref.Filename, ref.Mime, ref.Caption, ref.Kind)
	if err != nil {
		return nil, fmt.Errorf("runtime: enviar media %q: %w", ref.Key, err)
	}
	return ack, nil
}
