package runtime

import (
	"context"
	"errors"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
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
	// EL reloj de esta conversación (Plan 043 · T3.1/T3.2/T3.7). Va ANTES de IsEscape /
	// consecutiveReplay / prepareResume: un estado que ya no vale no debe escapar ni
	// avanzar. Devuelve si el entrante se trata como ARRANQUE NUEVO en vez de avance.
	restart, err := rt.conversationClock(ctx, tenantID, key, st)
	if err != nil {
		return err
	}
	if restart {
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m))
	}
	return rt.advanceLive(ctx, tenantID, sessionID, key, contactID, st, m)
}

// conversationClock aplica el reloj que gobierna esta conversación y dice si el
// entrante debe tratarse como un arranque nuevo (true) o como un avance (false).
//
// Hay DOS relojes y son EXCLUYENTES (REQ-01c, INV-18, D-043.16). Cuál manda lo
// decide una sola cosa: si la conversación tiene evento activo.
//
//   - CON evento activo manda event_inactivity_ttl_seconds y NADIE más (T3.1/T3.2):
//     dentro de la ventana el entrante REFRESCA el reloj; vencida, se suelta la
//     conversación —el evento sigue `open`— y el texto entra como uno nuevo.
//   - SIN evento activo —el LIMBO: un saludo, la cháchara, un menú a medias— manda
//     conversation_ttl_seconds, que es recolección de basura del flow_state y nada
//     más (Plan 029 · T9).
//
// La guarda es la tarea (T3.7): hasta la Ola 3, el TTL conversacional se evaluaba
// SIEMPRE, también con un pedido en curso, y descartaba su estado por un reloj
// pensado para otra cosa. No se toca la migración 0034 ni su default 0: lo que
// cambia es CUÁNDO se pregunta.
func (rt *Runtime) conversationClock(ctx context.Context, tenantID string, key store.Key, st model.Conversation) (bool, error) {
	if st.EventID != "" {
		return rt.eventClock(ctx, tenantID, key, st.EventID)
	}
	// El limbo no toca conversation_events: aquí no se lee ni se escribe ningún
	// last_activity_at (T3.7.2). El reloj del evento empieza a contar cuando el evento
	// NACE, no cuando el contacto saludó.
	if !rt.conversationExpired(ctx, tenantID, st) {
		return false, nil
	}
	if err := rt.store.Delete(ctx, key); err != nil {
		return false, fmt.Errorf("runtime: cerrar conversación vencida (TTL): %w", err)
	}
	return true, nil
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
		return rt.handleEscape(ctx, tenantID, sessionID, key, contactID, escMsg, st)
	}
	// Salto por tipo y desactivación SOBRE conversación viva (Plan 043 · T2.2/T2.4,
	// D-043.2): la EXCEPCIÓN ACOTADA de INV-02. Va justo aquí por dos razones:
	// DESPUÉS del escape global, que conserva su semántica y su prioridad EXACTAS
	// (INV-06), y ANTES de consecutiveReplay y del Step, porque «carrito» dicho a
	// media encuesta es una orden de navegación y no una respuesta para el módulo —si
	// llegara al engine, la encuesta lo guardaría como si fuera la contestación a su
	// pregunta.
	if done, eerr := rt.liveEventSwitch(ctx, tenantID, sessionID, key, st, m); eerr != nil {
		return eerr
	} else if done {
		return nil
	}
	if consecutiveReplay(st, m) {
		// Re-entrega INMEDIATA del mismo mensaje → no avanzar ni reenviar.
		return nil
	}

	// Estado SIN flujo: lo deja el menú del despachador cuando el contacto pide la
	// lista sin tener ninguna conversación abierta (D-043.3: el menú no es una fila de
	// flow_definitions). Si llegamos aquí es que el texto NO era una opción del menú
	// —menuChoice ya lo descartó—, así que se cumple lo que el propio menú promete
	// («si prefieres otra cosa, escríbelo») soltando el estado y tratándolo como un
	// entrante sin conversación viva, en vez de reventar buscando un flujo que no existe.
	if st.FlowID == "" {
		if derr := rt.store.Delete(ctx, key); derr != nil {
			return fmt.Errorf("runtime: soltar el estado sin flujo del menú: %w", derr)
		}
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m))
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
	// CIERRE NATURAL del evento (Plan 043 · T4.1): si el Step dejó el flujo en el
	// centinela y la conversación tenía evento activo, el evento pasa a `closed` y el
	// puntero se apaga EN ESTE MISMO Save — una escritura de flow_state, no dos. Va
	// ANTES del Save (el estado que se persiste ya es el final) y antes del fan-out:
	// los sinks proyectan sobre un evento cuya muerte ya está sellada.
	// El EventID del turno se captura ANTES del cierre natural (T4.5.1): los efectos
	// de este Step pertenecen al evento que estaba vivo MIENTRAS se produjeron, y
	// closeIfFinished apaga st.EventID en el turno que termina el flujo. Sin esta
	// captura, justo los efectos del final (p. ej. cart_closed) llegarían al
	// proyector con EventID "" y el hijo no podría declarar a su padre (D-043.21).
	turnEventID := st.EventID
	rt.closeIfFinished(ctx, &st)
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar estado: %w", err)
	}
	// Fan-out EN PROCESO (ADR-0003, sin broker) de los efectos declarados por el módulo
	// (Plan 015 · T3): el PersistSink escribe flow_events y proyecta survey_results /
	// intakes. Va DESPUÉS del Save (el estado ya está persistido) y respeta el orden
	// Save-antes-de-Send. La idempotencia es HEREDADA de la dedupe por last_wa_message_id
	// (reprocesar el mismo entrante corta antes del Step). Un fallo de un sink se LOGUEA
	// y NO aborta el avance ni corta el resto de sinks/efectos.
	ec := EffectContext{TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: turnEventID}
	rt.dispatch(ctx, ec, effects, sessionID)
	// El hilo LITERAL del turno (T4.5.7b, D-043.23): cliente y negocio, cifrado y
	// SOLO con la feature llm_intake. Usa turnEventID por lo mismo que los efectos:
	// el turno que cierra el flujo pertenece al evento que estaba vivo al hablar.
	// Best-effort — jamás tumba el turno (ver thread.go).
	rt.persistTurnMessages(ctx, tenantID, sessionID, turnEventID, m.GetText(), outs)
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
	case trigger.StartEvent, trigger.Start:
		return rt.startFromDecision(ctx, tenantID, sessionID, key, contactID, dec)
	case trigger.Fallback:
		// La rama PARTIDA (Plan 043 · T3.8.4, REQ-27b/INV-20/D-043.20). Lo que antes
		// compartía sitio con Start ahora vive aparte, porque hace otra cosa: en vez de
		// arrancar el flujo del `fallback` y soltar su frase, se le OFRECE al contacto lo
		// que puede hacer. Start no se entera: sigue byte a byte igual.
		//
		// La condición «esta conversación no tiene evento» NO se comprueba aquí, y esa
		// ausencia es deliberada: la garantiza el SITIO. A handleTrigger solo se llega sin
		// flow_state, o tras haberlo descartado, así que añadir una guarda sería un segundo
		// sitio decidiendo lo mismo. Y por eso se toca aquí y no en el resolver: trigger/
		// no sabe de eventos y no debe aprenderlo (INV-5) — interpretar la señal es suyo,
		// decidir QUIÉN habla es del runtime.
		return rt.openWithOffer(ctx, tenantID, sessionID, key, contactID, dec)
	default: // trigger.Ignore (o cualquier otro): decisión C, no arranca nada.
		return nil
	}
}

// openWithOffer atiende el `fallback`: en vez de la frase de «no te entendí», le
// enseña al contacto lo que puede empezar y lo que puede retomar (T3.8.4).
//
// El caso vacío es la mitad importante: un tenant sin nada habilitado y un contacto
// sin nada a medias producen una oferta SIN una sola opción, y entonces la rama cae al
// `fallback` de siempre (INV-20). Es lo que impide que un tenant recién creado se
// quede mudo, y la razón de que el startLocked del fallback siga existiendo.
//
// El TOKEN anti-loop (Plan 020 · T0) se cobra UNA vez por saliente y por eso no se pide
// arriba del todo: la oferta se construye primero —leer no habla—, y solo si hay algo
// que decir se consume el token. Si la oferta viene vacía, el token lo pide
// startPlainFlow como toda la vida; pedirlo también aquí cobraría dos por un solo
// mensaje y, con el cupo justo, dejaría mudo al fallback por una lista que ni se envió.
func (rt *Runtime) openWithOffer(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision) error {
	if rt.opening == nil {
		return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec)
	}
	offer, err := rt.opening.BuildOpening(ctx, events.ConversationRef{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
	})
	if err != nil {
		// Un fallo construyendo la oferta NO deja al contacto sin respuesta: se cae al
		// fallback del tenant, que es exactamente lo que había antes de esta tarea.
		rt.log.Warn("runtime: no se pudo construir la entrada que ofrece; se usa el fallback del tenant",
			"error", err, "session_id", sessionID)
		return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec)
	}
	if offer.Empty() {
		return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec)
	}
	return rt.sendOffer(ctx, key, sessionID, offer)
}

// startFromDecision ejecuta una decisión de arranque separando las PUERTAS DEL
// NACIMIENTO TARDÍO del evento (Plan 043 · T2.5, E-6) del arranque de siempre.
//
// Aquí se parte la rama que antes compartían Start y Fallback, y el corte es la
// tarea entera: el `fallback` queda FUERA de las puertas a propósito. Un saludo, la
// cháchara o un texto que no casó nada NO crean fila en conversation_events ni
// arrancan reloj — ese es el TIEMPO MUERTO. La rama ignora dec.EventKind aunque una
// regla mal configurada lo trajera poblado: quién puede parir un evento lo decide el
// SITIO, no el dato que llega.
//
// Las dos puertas que sí pasan por aquí son event_start (kind del tenant) y una
// intención LLM mapeada a un event_kind (dec.EventKind poblado sobre Action=Start).
// La tercera —elegir en el despachador— entra por su propio camino.
func (rt *Runtime) startFromDecision(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision) error {
	if dec.Action != trigger.Fallback {
		consumed, err := rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, "")
		if err != nil {
			return err
		}
		if consumed {
			return nil
		}
	}
	return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec)
}

// startPlainFlow arranca el flujo del tenant SIN parir evento: es el camino del Plan
// 019 byte a byte, extraído para que la rama de eventos no lo duplique.
//
// dec.Params/IntentName solo vienen poblados si la decisión provino de una regla
// kind='llm' (T8): startLocked los siembra en Vars para el pre-carga del módulo.
func (rt *Runtime) startPlainFlow(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision) error {
	if dec.FlowID == "" {
		// Una regla de evento sin flujo (el caso del tipo `menu`, D-043.3) y sin plano
		// de eventos cableado no tiene nada que arrancar. No es un error: es un motor
		// sin la Ola 2 puesta.
		rt.log.Debug("runtime: decisión de arranque sin flow_id; no hay flujo que arrancar",
			"session_id", sessionID)
		return nil
	}
	// Red anti-loop (Plan 020 · T0): el arranque por disparo SIEMPRE auto-responde
	// (renderiza el nodo inicial), así que consume un token; agotado ⇒ no arranca
	// (corta el bucle de fallback destapado en el e2e del Plan 019). Ignore no llega
	// aquí ⇒ no gasta cuota.
	if !rt.replyAllowed(key) {
		return nil
	}
	// Sin evento (eventID ""): este es el arranque plano del Plan 019 — el camino
	// que NO pare fila en conversation_events (E-6).
	if _, serr := rt.startLocked(ctx, tenantID, dec.FlowID, sessionID, key, contactID, "", dec.Params, dec.IntentName,
		rt.taglineFor(ctx, tenantID, sessionID, contactID, dec.IntentName)); serr != nil {
		if errors.Is(serr, ErrConversationExists) {
			rt.log.Info("runtime: disparo abortado por conversación ya viva (carrera benigna)",
				"session_id", sessionID)
			return nil
		}
		return serr
	}
	return nil
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
//
// Desde el Plan 043 · T3.7 esto SOLO se pregunta cuando la conversación no tiene
// evento activo: lo garantiza conversationClock, su único llamante. No es un detalle
// de orden — este TTL es recolección de basura del limbo, y evaluarlo sobre un pedido
// en curso descartaba el estado de un evento vivo por un reloj que no es el suyo
// (REQ-01c, INV-18). Si vuelves a llamarlo sin mirar st.EventID, reabres eso.
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
func (rt *Runtime) handleEscape(ctx context.Context, tenantID, sessionID string, key store.Key, contactID, message string, st model.Conversation) error {
	// Red anti-loop (Plan 020 · T0): el aviso de escape es una auto-respuesta ⇒
	// consume un token. Agotado ⇒ no se corta ni se avisa (la conversación sigue
	// viva); rompe cualquier bucle en el que un aviso de escape realimente al peer.
	if !rt.replyAllowed(key) {
		return nil
	}
	// El escape es el tercer abandono real (T3.4), y el más brusco: se resume ANTES del
	// Delete, porque ese Delete se lleva las Vars con el nivel de la sub-máquina.
	rt.summarizeAbandoned(ctx, key, sessionID, st, "")
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
