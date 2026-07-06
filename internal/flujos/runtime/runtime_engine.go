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

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva
// para la clave (T3 lo mapea a HTTP 409). Se inspecciona con errors.Is.
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

// Constantes del carrito que el runtime inspecciona por su FORMA (tipo de nodo,
// claves/valores serializados de la sub-máquina, nombre de efecto), replicadas
// como literales para NO acoplar el runtime al paquete cart — mismo criterio de
// desacople que los literales de nombre de efecto del PersistSink. El TTL y el
// auto-reinicio del carrito (design.md §4.3/§9.H) se GATEAN por estas señales:
// menú/encuesta nunca las igualan ⇒ su comportamiento queda intacto.
const (
	cartNodeType       = "cart"           // model.Node.Type que maneja el módulo cart
	cartStateVarKey    = "cart"           // Conversation.Vars[cart] = sub-estado del carrito
	cartLevelKey       = "level"          // cartState.Level serializado (json)
	cartLevelClosed    = "closed"         // terminal · pedido confirmado
	cartLevelCancelled = "cancelled"      // terminal · pedido cancelado
	cartPageSizeVarKey = "cart_page_size" // == cart.VarPageSize
	effNameCartExpired = "cart_expired"   // == cart.EffectCartExpired
)

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
	store    store.Repository
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
func New(repo store.Repository, eng *engine.Engine, sender Sender, resolver TenantResolver, contacts contact.Resolver, log logger.Logger, opts ...Option) *Runtime {
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
		restart, rerr := rt.cartRestartable(ctx, def, key, tenantID, contactID, sessionID)
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
	tenantID, role, err := rt.resolver.ResolveTenant(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("runtime: resolver tenant: %w", err)
	}
	// Guarda passive (Plan 020 · T1): una sesión passive escucha/transporta pero NO
	// entra al motor reactivo (ni trigger/arranque, ni escape, ni avance con
	// auto-envío). Es el BORDE del motor: se corta ANTES de resolver contacto, tomar
	// el keyedMutex o cargar estado; la escucha y los acuses (vía separada del
	// Gateway) no se ven afectados. Una conversación ya EN CURSO en una sesión que
	// pasa a passive deja de avanzar mientras siga passive (no se borra su estado):
	// criterio conservador "passive no auto-responde"; vuelve a avanzar si se
	// re-marca bot. rol vacío/desconocido ⇒ bot (no-regresión).
	if role == rolePassive {
		rt.log.Debug("runtime: sesión passive; motor reactivo omitido", "session_id", sessionID)
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
	if esc, escMsg, escErr := rt.triggers.IsEscape(ctx, tenantID, m.GetText()); escErr != nil {
		rt.log.Warn("runtime: IsEscape falló; se ignora el escape", "error", escErr, "session_id", sessionID)
	} else if esc {
		return rt.handleEscape(ctx, tenantID, sessionID, key, contactID, escMsg)
	}
	if st.LastWaMessageID != "" && st.LastWaMessageID == m.GetWaMessageId() {
		// Re-entrega del mismo mensaje → no avanzar ni reenviar (idempotencia).
		return nil
	}

	def, err := rt.store.GetDefinition(ctx, tenantID, st.FlowID, st.FlowVersion)
	if err != nil {
		return fmt.Errorf("runtime: definición en curso (v%d): %w", st.FlowVersion, err)
	}

	// Carrito (Plan 016 · T3): TTL perezoso + auto-reinicio + siembra de page_size,
	// GATEADO por tipo de nodo (prepareCart es un no-op para menú/encuesta ⇒
	// comportamiento idéntico). handled=true ⇒ el turno se consumió reseteando.
	if handled, cerr := rt.prepareCart(ctx, sessionID, &st, def, m, tenantID, contactID); cerr != nil {
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
	dec, err := rt.triggers.Resolve(ctx, tenantID, m.GetText())
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

// prepareCart aplica, SOLO para nodos de carrito, la lógica de reanudación del
// Plan 016 · T3: TTL perezoso + auto-reinicio (resumeCart) y, si el carrito sigue
// vivo en navegación, la siembra del page_size REAL del tenant para que la
// paginación use tenant_settings en vez del default del módulo (design.md §9.E).
// Devuelve handled=true si el turno ya se resolvió (reinicio: se avisó y se mostró
// L1). Para nodos que NO son carrito es un no-op total (handled=false, sin tocar
// Vars) ⇒ menú/encuesta quedan intactos (no-regresión). Extraído de HandleIncoming
// para acotar su complejidad ciclomática.
func (rt *Runtime) prepareCart(ctx context.Context, sessionID string, st *model.Conversation, def model.Flow, m *cloudlinkv1.IncomingMessage, tenantID, contactID string) (bool, error) {
	node, ok := def.Nodes[st.CurrentNode]
	if !ok || node.Type != cartNodeType {
		return false, nil
	}
	handled, err := rt.resumeCart(ctx, sessionID, st, def, m, tenantID, contactID)
	if err != nil || handled {
		return handled, err
	}
	if err := rt.seedCartPageSize(ctx, st, tenantID); err != nil {
		return false, err
	}
	return false, nil
}

// resumeCart aplica, al REANUDAR una conversación de carrito, la evaluación
// PEREZOSA del TTL y el auto-reinicio tras terminar (design.md §4.3/§9.H). Si el
// carrito debe empezar de cero (orden "open" vencida, o sub-máquina en un nivel
// terminal cerrado/cancelado) lo reinicia POR COMPLETO: descarta el estado de la
// sub-máquina, re-entra el flujo (renderiza L1 categorías fresco), persiste y
// avisa; devuelve handled=true (el llamante NO procesa el entrante como avance).
// En navegación normal devuelve handled=false (el flujo sigue por engine.Step).
//
// Coherencia BD↔conversación (design.md §3.4): cuando vence una orden abierta se
// SINTETIZA el efecto cart_expired y se despacha por el MISMO PersistSink, que lo
// registra en flow_events y transiciona la orden a "expired". Así un pedido nuevo
// queda habilitado en la MISMA conversación (sin exigir un /start nuevo) y la
// orden no queda "open" colgada (evita el gotcha 409).
func (rt *Runtime) resumeCart(ctx context.Context, sessionID string, st *model.Conversation, def model.Flow, m *cloudlinkv1.IncomingMessage, tenantID, contactID string) (bool, error) {
	order, found, err := rt.store.GetOpenOrder(ctx, tenantID, contactID)
	if err != nil {
		return false, fmt.Errorf("runtime: orden abierta del carrito: %w", err)
	}
	expired := found && orderExpired(order, time.Now())
	terminal := cartTerminal(st.Vars)
	if !expired && !terminal {
		return false, nil // carrito vivo en navegación normal.
	}

	// Red anti-loop (Plan 020 · T0): el reinicio del carrito auto-responde (aviso +
	// L1), así que consume un token. Agotado ⇒ turno consumido SIN responder ni
	// reiniciar (el carrito queda como está; un entrante posterior reevalúa): corta un
	// bucle carrito↔carrito que no pasa por el Step principal.
	if !rt.replyAllowed(store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}) {
		return true, nil
	}

	var notice string
	if expired {
		// El runtime DETECTA el vencimiento; el PersistSink PERSISTE (flow_events +
		// orders.status=expired). Fan-out best-effort: un fallo se loguea, no aborta.
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion}
		rt.dispatch(ctx, ec, []modules.Effect{{Kind: "event", Name: effNameCartExpired, Payload: map[string]any{}}}, sessionID)
		notice = "⌛ Tu pedido anterior expiró. Empezamos de nuevo."
	}

	// Arranca LIMPIO: descarta las Vars (sub-máquina del carrito) y re-entra el
	// flujo con la MISMA versión con la que corría (def viene de GetDefinition).
	fresh := *st
	fresh.Vars = nil
	fresh, outs, err := rt.engine.Enter(ctx, def, fresh)
	if err != nil {
		return false, fmt.Errorf("runtime: reentrar carrito: %w", err)
	}
	fresh.LastWaMessageID = m.GetWaMessageId()
	if err := rt.store.Save(ctx, fresh); err != nil {
		return false, fmt.Errorf("runtime: guardar carrito reiniciado: %w", err)
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

// seedCartPageSize inyecta en Vars el page_size REAL del tenant
// (tenant_settings.page_size, default 5 si no hay fila) para que el módulo pagine
// con la config del tenant sin hacer I/O (design.md §9.E). Solo lo llama el camino
// del carrito ⇒ nunca añade claves a las Vars de menú/encuesta (no-regresión).
func (rt *Runtime) seedCartPageSize(ctx context.Context, st *model.Conversation, tenantID string) error {
	settings, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("runtime: config de tenant (page_size): %w", err)
	}
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	st.Vars[cartPageSizeVarKey] = settings.PageSize
	return nil
}

// cartRestartable decide si un Start sobre una conversación EXISTENTE puede
// reiniciar el carrito en vez de devolver 409 (gotcha, design.md §3.4). Solo el
// carrito TERMINADO se reinicia:
//   - nodo inicial NO es "cart" (menú/encuesta) ⇒ false (409 intacto);
//   - sub-máquina en nivel terminal (cerrado/cancelado) ⇒ true (reiniciable);
//   - orden "open" VENCIDA por TTL ⇒ la marca "expired" (coherencia BD↔conv) y
//     devuelve true (reiniciable);
//   - en cualquier otro caso (carrito NAVEGANDO, u orden abierta vigente) ⇒ false
//     (409: no se clobbea un pedido en curso).
func (rt *Runtime) cartRestartable(ctx context.Context, def model.Flow, key store.Key, tenantID, contactID, sessionID string) (bool, error) {
	node, ok := def.Nodes[def.Initial]
	if !ok || node.Type != cartNodeType {
		return false, nil
	}
	if st, found, err := rt.store.Load(ctx, key); err != nil {
		return false, fmt.Errorf("runtime: cargar estado del carrito: %w", err)
	} else if found && cartTerminal(st.Vars) {
		return true, nil
	}
	order, found, err := rt.store.GetOpenOrder(ctx, tenantID, contactID)
	if err != nil {
		return false, fmt.Errorf("runtime: orden abierta del carrito: %w", err)
	}
	if found && orderExpired(order, time.Now()) {
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: def.FlowID, FlowVersion: def.Version}
		rt.dispatch(ctx, ec, []modules.Effect{{Kind: "event", Name: effNameCartExpired, Payload: map[string]any{}}}, sessionID)
		return true, nil
	}
	return false, nil
}

// orderExpired indica si una orden tiene TTL fijado (expires_at no-cero) y ya
// venció respecto a now. Sin expires_at (zero) NUNCA expira.
func orderExpired(o store.Order, now time.Time) bool {
	return !o.ExpiresAt.IsZero() && o.ExpiresAt.Before(now)
}

// cartTerminal lee el nivel serializado de la sub-máquina del carrito en Vars y
// dice si quedó en un estado terminal (pedido confirmado o cancelado). Inspecciona
// la FORMA del sub-estado (Vars["cart"]["level"]) sin importar el paquete cart
// (mismo desacople que los literales de efecto del PersistSink). Ausente/otro tipo
// ⇒ false (carrito en navegación).
func cartTerminal(vars map[string]any) bool {
	sub, ok := vars[cartStateVarKey].(map[string]any)
	if !ok {
		return false
	}
	level, ok := sub[cartLevelKey].(string)
	if !ok {
		return false
	}
	return level == cartLevelClosed || level == cartLevelCancelled
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
func (rt *Runtime) OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage) {
	go func() {
		if err := rt.HandleIncoming(context.Background(), sessionID, m); err != nil {
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
