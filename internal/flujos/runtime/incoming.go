package runtime

import (
	"context"
	"errors"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// defaultEscapeMessage es el aviso corto que se envía al cortar una conversación
// viva por escape global (Plan 019 · T4) cuando la regla de escape que casó NO
// define un aviso propio. Si la regla trae message (columna flow_triggers.message,
// Plan 019 · T4b), handleEscape lo usa en su lugar.
const defaultEscapeMessage = "Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."

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
	// TOQUE del recordatorio de la seña (Plan 041 · T4.4): el cliente acaba de
	// hablar. Va en DEFER —y registrado ANTES del candado, así que corre el último—
	// para que ocurra después de haberle contestado y con la clave ya libre: primero
	// se atiende lo que la persona vino a hacer, y solo entonces se le recuerda lo que
	// debe. Sin cablear (deposits nil) esto no existe.
	defer rt.touchDeposit(ctx, tenantID, contactID)
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
		// sessionID) ya está resuelto ⇒ se arranca sin re-resolver el contacto. La
		// Signal lleva el texto y, si el tenant tiene la feature, la intención LLM.
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m))
	}
	// TTL conversacional genérico (Plan 029 · T9): si el tenant configuró un
	// conversation_ttl_seconds > 0 y el estado vivo lleva más tiempo que eso sin
	// tocarse, se DESCARTA silenciosamente y el entrante se trata como un arranque
	// nuevo (camino handleTrigger, donde la señal LLM aplica). Va ANTES de IsEscape /
	// consecutiveReplay / prepareResume: un estado vencido no debe escapar ni avanzar.
	// Es el ÚNICO reloj que queda en este camino: el de la SOLICITUD del carrito
	// (order_ttl_seconds) se derogó en T4.7 (D-041.16) y prepareResume ya no lo
	// evalúa. Vencer aquí descarta el ESTADO CONVERSACIONAL; la solicitud sobrevive
	// con sus líneas y solo la mata una persona (D-041.18). ttl<=0 o error de
	// settings ⇒ no vence (no-regresión).
	if rt.conversationExpired(ctx, tenantID, st) {
		if derr := rt.store.Delete(ctx, key); derr != nil {
			return fmt.Errorf("runtime: cerrar conversación vencida (TTL): %w", derr)
		}
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m))
	}
	return rt.advanceLive(ctx, tenantID, sessionID, key, contactID, st, m)
}

// advanceLive avanza una conversación VIVA (estado ya cargado y no vencido) con un
// entrante: escape global → idempotencia consecutiva → reanudación por módulo →
// engine.Step → persistir → fan-out de efectos → auto-respuesta. Extraído de
// HandleIncoming (Plan 029 · T9) para acotar su complejidad ciclomática; el orden y la
// semántica son idénticos al camino previo (INV-5/INV-6 no-regresión).
func (rt *Runtime) advanceLive(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) error {
	// Escape global (Plan 019 · T4): sobre una conversación viva, ANTES de despachar el
	// entrante al engine, si el texto casa una regla de escape del tenant se corta la
	// conversación y se avisa. Un fallo de IsEscape es best-effort: se LOGUEA y NO
	// bloquea el avance normal (no aborta).
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

	// Reanudación por módulo (Plan 027 · Ola 3 · T8): TTL perezoso DE LA SOLICITUD +
	// auto-reinicio + siembra de Vars, GATEADO por la ResumePolicy registrada para el
	// tipo de nodo (un no-op para menú/encuesta ⇒ comportamiento idéntico). handled=true
	// ⇒ el turno se consumió reiniciando. Es DISTINTO del TTL conversacional (T9), que
	// ya se evaluó antes en HandleIncoming.
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
	// Fan-out EN PROCESO (ADR-0003, sin broker) de los efectos declarados por el módulo
	// (Plan 015 · T3): el PersistSink escribe flow_events y proyecta survey_results /
	// intakes. Va DESPUÉS del Save (el estado ya está persistido) y respeta el orden
	// Save-antes-de-Send. La idempotencia es HEREDADA de la dedupe por last_wa_message_id
	// (reprocesar el mismo entrante corta antes del Step). Un fallo de un sink se LOGUEA
	// y NO aborta el avance ni corta el resto de sinks/efectos.
	ec := EffectContext{TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion}
	rt.dispatch(ctx, ec, effects, sessionID)
	return rt.sendReply(ctx, tenantID, sessionID, contactID, key, outs)
}

// handleTrigger resuelve un entrante SIN conversación viva contra el trigger.Resolver
// (Plan 019 · T3). Con el resolver por defecto (Noop) devuelve Ignore ⇒ return nil,
// idéntico a la decisión C histórica (INV-6). Un error del resolver se LOGUEA y NO
// aborta la recepción (REQ-A7: el entrante simplemente se ignora). Ante Start/Fallback
// arranca el flujo por startLocked (el keyedMutex de la clave YA está tomado por
// HandleIncoming; llamar a Start re-tomaría el mutex y causaría auto-deadlock). Un
// ErrConversationExists (carrera con otro entrante) se trata como benigno (log + nil).
// La señal ya viene construida por el llamante (buildSignal, con el gate de
// entitlements aplicado); handleTrigger solo la resuelve.
func (rt *Runtime) handleTrigger(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, sig trigger.Signal) error {
	dec, err := rt.triggers.Resolve(ctx, tenantID, sessionID, sig)
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
		// dec.Params/IntentName solo vienen poblados si la decisión provino de una regla
		// kind='llm' (T8): startLocked los siembra en Vars para el pre-carga del módulo.
		if _, serr := rt.startLocked(ctx, tenantID, dec.FlowID, sessionID, key, contactID, dec.Params, dec.IntentName); serr != nil {
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

// buildSignal arma la señal de entrada del resolver de disparos (Plan 029 · T7): el
// texto crudo del entrante y, SOLO si el mensaje trae una intención LLM Y el tenant
// tiene la feature llm_intent habilitada (gate de verdad, ADR-0022), la intención
// resuelta. Sin resolver de entitlements cableado (nil) o sin la feature, la señal
// lleva SOLO texto ⇒ una regla kind='llm' nunca dispara sin derecho (camino actual).
// Un fallo del resolver de entitlements es best-effort: se loguea y se descarta la
// intención (se prefiere no abrir la capacidad por un fallo transitorio).
func (rt *Runtime) buildSignal(ctx context.Context, tenantID string, m *cloudlinkv1.IncomingMessage) trigger.Signal {
	sig := trigger.Signal{Text: m.GetText()}
	ci := m.GetIntent()
	if ci == nil || rt.entitlements == nil {
		return sig
	}
	has, err := rt.entitlements.Has(ctx, tenantID, entitlements.FeatureLLMIntent)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver la feature llm_intent; se descarta la intención",
			"error", err, "tenant_id", tenantID)
		return sig
	}
	if !has {
		return sig
	}
	sig.Intent = &trigger.IntentSignal{
		Name:          ci.GetIntent(),
		Params:        ci.GetParams(),
		Confidence:    float64(ci.GetConfidence()),
		ConfigVersion: ci.GetConfigVersion(),
	}
	return sig
}

// conversationExpired decide si un estado vivo venció por el TTL conversacional del
// tenant (Plan 029 · T9). Lee conversation_ttl_seconds de tenant_settings (mismo
// store/camino que page_size/order_ttl); ttl<=0 ⇒ nunca vence (tenants sin configurar
// intactos). Un fallo de settings es best-effort: se loguea y devuelve false (no
// vence — se prefiere no descartar una conversación por un fallo transitorio). La
// comparación usa rt.now() (inyectable en tests) contra st.UpdatedAt (lo estampa el
// store en cada Save). Con UpdatedAt cero (estado sin marca) no vence.
func (rt *Runtime) conversationExpired(ctx context.Context, tenantID string, st model.Conversation) bool {
	settings, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo leer el TTL conversacional; no se vence el estado",
			"error", err, "tenant_id", tenantID)
		return false
	}
	if settings.ConversationTTL <= 0 || st.UpdatedAt.IsZero() {
		return false
	}
	return rt.now().Sub(st.UpdatedAt) > settings.ConversationTTL
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

// touchDeposit evalúa el recordatorio perezoso de la seña para el contacto que
// acaba de escribir (Plan 041 · T4.4, D-041.12). Tres cosas que no son de estilo:
//
//   - Es el ÚNICO reloj admisible aquí: no barre nada ni corre de fondo (ADR-0003).
//     El disparador es este entrante, que ya venía; si nadie escribe y nadie mira,
//     no se recuerda nada, y eso es exactamente lo perezoso.
//   - Pregunta por el CONTACTO, no por la conversación: el cliente puede escribir
//     "hola" sin carrito abierto y seguir debiendo la seña de un pedido de la semana
//     pasada. Por eso la consulta va por (tenant, contacto) y no por la clave del
//     flujo.
//   - Va DESPUÉS de las guardas de borde (passive, anti-self-loop) porque las hereda:
//     una sesión pasiva no auto-responde nada, tampoco esto, y un entrante que es un
//     número propio del tenant no es un cliente al que recordarle nada.
//
// No consume token del rate-limit de auto-respuestas (replyAllowed): ese tope existe
// para cortar BUCLES, y aquí no puede haberlos — cada solicitud se recuerda como
// mucho una vez en su vida, y quien lo garantiza es el compare-and-swap de la marca
// en la BD, no un contador en memoria.
func (rt *Runtime) touchDeposit(ctx context.Context, tenantID, contactID string) {
	if rt.deposits == nil {
		return
	}
	rt.deposits.RemindContact(ctx, tenantID, contactID)
}

// consecutiveReplay es la idempotencia CONSECUTIVA (design.md §10.G): corta la
// re-entrega INMEDIATA de un mensaje comparándolo con el último procesado en el
// estado del flujo (last_wa_message_id). Complementa —no reemplaza— el dedupe
// persistente (duplicateIngest), que cubre además los duplicados intercalados y los
// caminos que no tocan last_wa_message_id (disparo/escape).
func consecutiveReplay(st model.Conversation, m *cloudlinkv1.IncomingMessage) bool {
	return st.LastWaMessageID != "" && st.LastWaMessageID == m.GetWaMessageId()
}

// reactiveBlocked agrupa las guardas de BORDE que impiden entrar al motor reactivo
// (Plan 020). Devuelve true (y NO se procesa el entrante) si:
//   - la sesión es PASSIVE (T1): escucha/transporta pero no dispara triggers, no
//     avanza con auto-envío ni escapa. Una conversación EN CURSO deja de avanzar
//     mientras siga passive (no se borra su estado; vuelve si se re-marca bot). Rol
//     vacío/desconocido ⇒ bot (no-regresión).
//   - el remitente es un número PROPIO del tenant (T2, anti-self-loop): una sesión
//     propia hablando; no se auto-responde (defensa semántica contra el bucle
//     sesión↔sesión del Plan 019). Consciente del rol: solo cuentan como "propios"
//     los números de sesiones NO passive — un passive nunca auto-responde, así que
//     una sesión bot SÍ puede responder a mensajes que llegan desde el número
//     personal (passive) del mismo tenant sin riesgo de loop.
//
// Sin rol passive y sin self_pn poblado, devuelve false ⇒ no-regresión total.
func (rt *Runtime) reactiveBlocked(ctx context.Context, tenantID, sessionID, role, fromPn string) bool {
	if role == rolePassive {
		rt.countReactiveBlocked(reasonPassive)
		rt.logPassiveSkip(sessionID)
		return true
	}
	return rt.isSelfLoop(ctx, tenantID, sessionID, fromPn)
}

// countReactiveBlocked registra el corte en el contador inyectado (nil-safe: sin
// WithReactiveBlockedHook no cuenta nada). Observa; NUNCA decide.
func (rt *Runtime) countReactiveBlocked(reason string) {
	if rt.onReactiveBlocked != nil {
		rt.onReactiveBlocked(reason)
	}
}

// logPassiveSkip anuncia el corte por rol passive UNA vez por sesión a INFO y las
// siguientes a Debug. El corte es un ESTADO configurado, no un evento: repetirlo a
// INFO en cada entrante inunda el log, pero dejarlo solo en Debug lo hace invisible
// con el nivel por defecto y el operador acaba diagnosticando otra cosa. La primera
// línea lleva el session_id —que el contador no puede etiquetar sin disparar la
// cardinalidad— y es justo lo que hace falta para saber QUÉ sesión marcar como bot.
func (rt *Runtime) logPassiveSkip(sessionID string) {
	if _, anunciada := rt.passiveAnnounced.LoadOrStore(sessionID, struct{}{}); anunciada {
		rt.log.Debug("runtime: sesión passive; motor reactivo omitido", "session_id", sessionID)
		return
	}
	rt.log.Info("runtime: sesión passive; motor reactivo omitido — no auto-responderá mientras siga passive (se anuncia una vez por sesión; el resto en debug)",
		"session_id", sessionID)
}

// isSelfLoop decide si un entrante proviene de un número PROPIO del tenant (una
// sesión propia hablando), en cuyo caso NO se debe auto-responder (Plan 020 · T2,
// defensa semántica contra el bucle sesión↔sesión del Plan 019). Normaliza el
// remitente (from_pn) con el MISMO normalizador que el paquete contact y lo compara
// contra el conjunto de self_pn del tenant. El conjunto es CONSCIENTE DEL ROL: el
// lister excluye los números de sesiones passive — un passive nunca auto-responde
// (reactiveBlocked lo corta), así que un mensaje desde ese número no puede cerrar
// un bucle; bloquear ahí solo impediría atender al número personal del tenant. El
// rate-limit por conversación (T0) sigue como red. Es CONSERVADORA hacia procesar: sin
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
			rt.countReactiveBlocked(reasonSelfLoop)
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
	rt.countReactiveBlocked(reasonRateLimit)
	rt.log.Warn("runtime: auto-respuesta limitada por rate-limit de conversación",
		"tenant_id", key.TenantID,
		"session_id", key.SessionID,
		"contact_id", key.ContactID,
	)
	return false
}
