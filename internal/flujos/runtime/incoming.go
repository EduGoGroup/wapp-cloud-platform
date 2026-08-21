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

// defaultDurableSinkFailureNotice es el aviso EXPLÍCITO que recibe el cliente
// cuando el turno se corta por D-054.4 (Plan 054 · T3): el sink que MATERIALIZA
// contenido durable (la proyección a intakes/survey_results) agotó su reintento
// acotado. Es un DEFAULT DE PLATAFORMA, deliberadamente sin superficie de
// configuración por tenant (no la pidió nadie; ampliarla no es parte de este
// plan) — mismo estatus que defaultEscapeMessage, arriba. Dice qué pasó en
// términos del cliente (su pedido, no un error técnico) y qué puede hacer
// (reintentar), nunca un SQLSTATE ni un detalle de Postgres.
const defaultDurableSinkFailureNotice = "No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."

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
		//
		// El descarte se CUENTA además de loguearse: es el único camino del contador
		// que no responde a una política sino a una pérdida —el mensaje debía entrar al
		// motor y se tiró—, y sin contador solo se sabía leyendo logs a mano.
		if rt.incomingSem != nil {
			select {
			case rt.incomingSem <- struct{}{}:
				defer func() { <-rt.incomingSem }()
			case <-ctx.Done():
				rt.countReactiveBlocked(reasonSaturation)
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
	tenantID, profile, err := rt.resolver.ResolveTenant(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("runtime: resolver tenant: %w", err)
	}
	// Guardas de BORDE del motor reactivo (Plan 020 · T1 passive + T2 anti-self-loop):
	// se cortan ANTES de resolver contacto, tomar el keyedMutex o cargar estado; la
	// escucha y los acuses (vía separada del Gateway) no se ven afectados.
	if rt.reactiveBlocked(ctx, tenantID, sessionID, profile, m.GetFromPn()) {
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
		// Signal lleva el texto y, si el tenant tiene la feature, la intención LLM. Sin
		// flow_state no hay puntero de evento ⇒ activeEventKind="" (Plan 043 · T5.3).
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, ""))
	}
	// EL reloj de esta conversación (Plan 043 · T3.1/T3.2/T3.7). Va ANTES de IsEscape /
	// consecutiveReplay / prepareResume: un estado que ya no vale no debe escapar ni
	// avanzar. Devuelve si el entrante se trata como ARRANQUE NUEVO en vez de avance.
	restart, err := rt.conversationClock(ctx, tenantID, key, st)
	if err != nil {
		return err
	}
	if restart {
		// El reloj del evento venció y releaseForNewConversation ya borró el estado
		// (T3.2/E-6): el evento sigue `open` pero YA NO es el activo ⇒
		// activeEventKind="" (Plan 043 · T5.3). Atarlo aquí reintroduciría la atadura
		// que T3.2 quita.
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, ""))
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
	// FIN DE EPISODIO de la racha de auto-respuestas (Plan 049 · Opción A). El estado
	// conversacional acaba de destruirse, así que la racha que venía contándose para
	// esta clave TERMINA aquí: se reporta su longitud al histograma y se olvida. Va
	// DESPUÉS del Delete —solo se cierra lo que de verdad murió— y es idempotente y
	// nil-safe, así que puede colgar de los seis caminos de destrucción sin llevar la
	// cuenta de cuál llegó primero. Qué es una racha, por qué se mide el EPISODIO y
	// por qué un entrante NO lo cierra: ver la cabecera de streak.go.
	rt.autoreplyStreaks.Close(key, rt.now())
	return true, nil
}

// exitMenuStep evalúa el menú de salida (Plan 043 · T5.2, D-043.10) y colapsa su
// resultado a un único punto de decisión para el llamante: stop=true ⇒ el turno
// termina aquí (con o sin error; err es nil si el turno se consumió sin fallo).
// Extraído de advanceLive para acotar SU complejidad ciclomática (gocyclo), igual
// que el resto de esta función: el comportamiento es idéntico a inlinear las dos
// ramas de rt.exitMenuChoice.
func (rt *Runtime) exitMenuStep(ctx context.Context, key store.Key, sessionID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	done, err := rt.exitMenuChoice(ctx, key, sessionID, st, m)
	if err != nil {
		return true, err
	}
	return done, nil
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
	// Menú de SALIDA del reprompt acotado (Plan 043 · T5.2, D-043.10). Va DESPUÉS del
	// salto por tipo —«carrito» dicho ante el menú de salida sigue siendo una orden de
	// navegación, y el escape global conserva su prioridad exacta— y ANTES del Step,
	// porque el «2» que el cliente teclea ahí NO es una respuesta para el módulo.
	if stop, xerr := rt.exitMenuStep(ctx, key, sessionID, st, m); stop {
		return xerr
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
		return rt.releaseOrphanMenu(ctx, tenantID, sessionID, key, contactID, st, m)
	}
	// Un flow_state TERMINAL con el puntero YA APAGADO cuenta como «ninguna
	// conversación en curso» — el mismo trato que saveMenuState (events.go) ya le
	// daba con `!ok || st.Finished()` desde la Ola 4, y que este camino vivo no le
	// daba (efecto colateral del hallazgo #24, Plan 043 · Ola 6, decisión de Jhoan
	// 2026-08-11). Confirmar un pedido deja CurrentNode en el centinela:
	// prepareResume no encuentra def.Nodes[NodeTerminal] (reservado, no es un nodo
	// real) y engine.Step sobre un estado ya Finished() ignora la entrada SIN emitir
	// nada (documentado en Step) — el contacto queda mudo para siempre, porque
	// conversationExpired tampoco lo vence con el TTL en su default deshabilitado
	// (<=0). Se enruta exactamente como si no hubiera flow_state: el
	// fallback/oferta/disparadores decide qué contestar, igual que a un contacto de
	// control. ActiveEventKind="" a propósito: el flujo que acaba de terminar no
	// tiene autoridad para acotar la interpretación de lo que venga después.
	//
	// SÍ se borra el estado (medido en revisión, no era el plan original): dejarlo
	// intacto y delegar en handleTrigger parecía lo más simple, pero un disparo que
	// arranca OTRO flujo (p. ej. el fallback del tenant, con su propio FlowID) pasa
	// por startLocked, que ve `Exists(key)==true` sobre esta MISMA fila y activa el
	// gotcha 409 (start.go): restartableOnStart consulta la ResumePolicy del NUEVO
	// flujo contra las Vars del VIEJO —de otro módulo, otra forma— y esa lectura
	// cruzada casi siempre lee «no terminal» (el módulo nuevo no reconoce nada
	// suyo), así que startLocked responde ErrConversationExists y ese error se
	// TRAGA como carrera benigna (incoming.go, startPlainFlow): el turno vuelve a
	// quedar mudo, solo que por una puerta distinta. Medido por ejecución con un
	// flujo `survey` terminal y un fallback que arranca `cart`: sin el Delete, cero
	// salientes. Igual que releaseOrphanMenu (unas líneas arriba) y que
	// saveMenuState (events.go) desde la Ola 4, no hay nada que rescatar aquí
	// (E-1/E-7 — el flujo YA terminó, no hay carrito EN CURSO): borrar es seguro
	// PORQUE la guarda de abajo (pendingClosure) ya certificó que no queda ningún
	// cierre pendiente de reintentar.
	//
	// Esa guarda es «no queda ningún cierre PENDIENTE» y NO «st.EventID == ""»
	// (#28 / H2, Ola 6 · decisión de Jhoan 2026-08-11, medida por sonda). Lo que
	// separa este caso es el reintento de E-8 §4 (event_lifecycle.go,
	// TestCierreNatural_FalloRealNoLimpiaElPuntero): si la transición falló de
	// verdad, el puntero SIGUE puesto a propósito y el siguiente entrante tiene que
	// atravesar advanceLiveStep para que closeIfFinished reintente sobre ESTA MISMA
	// fila — borrarla aquí perdería el reintento para siempre.
	//
	// 🔁 Plan 053 · T2.3: aquí se explicaba EN PRESENTE la otra causa, la guarda de
	// POSESIÓN de H2 («no deja ningún reintento vivo: es DETERMINISTA…»). Esa guarda
	// YA NO EXISTE (T1.6 la retiró), y con ella desapareció la única forma de que
	// pendingClosure contestara «no pendiente» teniendo un evento AJENO vivo. Hoy
	// queda UNA sola causa de «no pendiente» con la fila terminal: que el dueño ya se
	// apagara —porque su cierre se consumó (hueco C)—. El reintento de E-8 §4 sigue
	// siendo lo que separa este caso, y ahora es lo ÚNICO que lo separa.
	//
	// Preguntar por st.EventID metía los dos casos
	// en el mismo saco y dejaba la fila {terminal, con puntero} PARA SIEMPRE: el
	// Step no emite nada sobre un estado ya Finished() y el contacto se quedaba mudo
	// a todo texto normal (solo lo rescataban el menú numerado del despachador, las
	// palabras clave, el escape y la cancelación desde la app). pendingClosure
	// distingue las dos causas con una sola relectura, y es la MISMA que
	// closeIfFinished usa: no hay dos sitios decidiendo lo mismo por separado.
	//
	// LevelCancelled del carrito SÍ alcanza este punto (hallazgo #29, salida (A),
	// decisión de Jhoan 2026-08-11): cart.Module.Step fija Next al centinela para
	// LevelClosed Y LevelCancelled por igual (la misma condición, ver cart.go), así
	// que un carrito cancelado DENTRO de la conversación deja st.Finished()==true
	// exactamente como uno confirmado. cart.ResumePolicy.Restart (isTerminal, ver
	// resume.go) ya NO tiene ocasión de reanudar el flujo desde aquí —CurrentNode
	// llegó al centinela antes de que hubiera un turno siguiente que lo reanudara—:
	// el flow_state terminal se suelta como cualquier otro y solo un disparador
	// real («carrito») abre un pedido nuevo, ya no cualquier texto. Es la otra mitad
	// del #24 que quedó viva a propósito hasta esta ola; ahora está cerrada (ver la
	// advertencia actualizada en cart_fin_de_flujo_test.go).
	if st.Finished() {
		// T2.3: `pendiente` es lo ÚNICO que se consulta aquí. `ev` y `conocido` ya no
		// viajan a releaseFinishedState —hablaban del DUEÑO, y lo que esa función
		// necesita es el ACTIVO (T2.4)—, así que se descartan explícitamente en vez de
		// arrastrarse por la firma.
		if _, _, pendiente := rt.pendingClosure(ctx, st); !pendiente {
			return rt.releaseFinishedState(ctx, tenantID, sessionID, key, contactID, st, m)
		}
	}

	def, err := rt.store.GetDefinition(ctx, tenantID, st.FlowID, st.FlowVersion)
	if err != nil {
		return fmt.Errorf("runtime: definición en curso (v%d): %w", st.FlowVersion, err)
	}

	return rt.advanceLiveStep(ctx, tenantID, sessionID, key, contactID, st, def, m)
}

// releaseFinishedState suelta un flow_state TERMINAL al que ya no le queda ningún
// cierre pendiente (pendingClosure) y enruta el entrante como si no hubiera
// conversación viva. Extraída de advanceLive para acotar SU complejidad ciclomática
// (gocyclo), igual que releaseOrphanMenu; el comportamiento es idéntico a inlinearla.
//
// # DE DÓNDE SALEN EL EFECTO Y EL SCOPING (Plan 053 · T2.4)
//
// Del evento **ACTIVO** (st.EventID), releído aquí — NO de lo que devuelva
// pendingClosure, que desde T1.6 relee al DUEÑO y por tanto habla del evento que
// acaba de MORIR. Son dos preguntas distintas sobre la misma fila y esta función
// necesita la del activo: lo que se está soltando es la FILA, y quien sigue vivo
// —y por tanto tiene un efecto que emitir y un tipo con el que acotar la señal— es
// el evento al que el contacto le está hablando.
//
// 🔴 POR QUÉ ESTO ES UNA REPOSICIÓN Y NO UN AÑADIDO. Hasta T1.6 este trabajo lo hacía
// el parámetro `conocido`, que valía true por la guarda de POSESIÓN de H2 (el puntero
// miraba a un evento ajeno). Al retirarse la guarda, pendingClosure dejó de poder
// devolver `conocido=true, pendiente=false`: la rama quedó MUERTA y con ella se
// perdieron, en silencio y sin un solo test que lo delatara, TRES cosas —el efecto
// event_escaped que cuenta el colector de métricas, el scoping ActiveEventKind de las
// reglas llm, y (la que nadie había censado) el hecho de que con ActiveEventKind ""
// la guarda de config_resolver.go NO DESCARTA NADA, así que el scoping no se perdía:
// se AFLOJABA, y una regla llm anotada podía abrir la segunda puerta del Plan 054 F2b—.
// Se repone leyendo el activo, que es de donde debió salir siempre.
//
// El patrón es EL MISMO que releaseOrphanMenu (justo debajo) y eso es deliberado: los
// dos sueltan una fila conservando un evento vivo, así que los dos leen al vivo.
//
// El Delete no conserva NADA, tampoco last_wa_message_id, y es deliberado: es el
// mismo gesto exacto de releaseOrphanMenu y del saveMenuState de la Ola 4 sobre un
// estado terminal. Lo que saveMenuState sí conserva es el last_wa_message_id de un
// flow_state que SOBREVIVE, y aquí la fila desaparece entera: no queda estado al que
// reprocesar un mensaje, el turno se enruta como el de un contacto sin conversación,
// y el corte de un reenvío del outbox es el dedupe PERSISTENTE de ingesta por
// (session_id, wa_message_id) —duplicateIngest, al principio de HandleIncoming—, que
// no depende de esta fila.
//
// ⚠️ El evento activo NO se cierra ni se desactiva aquí: lo que se suelta es la FILA.
// Su muerte sigue siendo de la cancelación desde la app (D-043.5).
func (rt *Runtime) releaseFinishedState(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) error {
	// Se lee ANTES del Delete por la misma higiene que releaseOrphanMenu documenta: hoy
	// daría igual (activeEvent consulta conversation_events, no flow_state), y se deja
	// así para que siga siendo cierto si algún día la relectura pasara a depender del
	// estado.
	activeKind := ""
	var ev events.Event
	var vivo bool
	if st.EventID != "" {
		activeKind = rt.activeEventKind(ctx, key, sessionID, st.EventID)
		ev, vivo = rt.activeEvent(ctx, key, sessionID, st.EventID)
	}
	if derr := rt.store.Delete(ctx, key); derr != nil {
		return fmt.Errorf("runtime: soltar el flow_state terminal: %w", derr)
	}
	rt.autoreplyStreaks.Close(key, rt.now()) // fin de episodio (Plan 049, ver streak.go).
	if vivo {
		// EffectEventEscaped por el MISMO eje que el quinto camino de suelta (E5/E6): el
		// nombre describe el EFECTO sobre el flow_state —destruido, no conservado como en
		// event_deactivated—, no la intención del cliente. La INTENCIÓN, que el nombre
		// deliberadamente no lleva, viaja desde T4.1 en el `reason`: aquí nadie abandonó
		// nada, el flujo llegó a su nodo terminal y su fila se recoge.
		rt.emitEventEscaped(ctx, ev, EscapeReasonOwnerFlowFinished)
	}
	return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, activeKind))
}

// releaseOrphanMenu es el QUINTO camino de suelta (Plan 043 · T5.3 D-043.9; Ola 6 ·
// E6): un flow_state SIN flujo (el `menu`, D-043.3) cuyo texto no casó ninguna
// opción. Extraído de advanceLive para acotar su complejidad ciclomática (gocyclo);
// el comportamiento es idéntico a inlinearlo.
func (rt *Runtime) releaseOrphanMenu(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) error {
	// El tipo del evento ACTIVO acota la interpretación de la intención (T5.3,
	// D-043.9). Se lee ANTES de soltar el estado por HIGIENE de lectura, no por
	// necesidad: st es una COPIA (el puntero st.EventID sigue en mano tras el
	// Delete) y activeEventKind → aliveByID → ListAlive consulta
	// conversation_events, no flow_state (events/store.go: selectAliveSQL), así
	// que invertir el orden daría hoy el MISMO resultado. Se deja así para que
	// siga siendo cierto si algún día la relectura pasara a depender del estado.
	activeKind := ""
	// E-6 (el QUINTO camino de suelta, cubierto en la misma pasada que E5): esta
	// misma lectura vuelve a hacerse por rt.activeEvent (no por activeEventKind,
	// que devuelve solo el nombre y no la fila completa que emitEventEffect
	// necesita) para poder registrar el abandono más abajo. #15 en un octavo
	// punto: se acepta el mismo patrón que handleEscape.
	var ev events.Event
	var conocido bool
	if st.EventID != "" {
		activeKind = rt.activeEventKind(ctx, key, sessionID, st.EventID)
		ev, conocido = rt.activeEvent(ctx, key, sessionID, st.EventID)
	}
	if derr := rt.store.Delete(ctx, key); derr != nil {
		return fmt.Errorf("runtime: soltar el estado sin flujo del menú: %w", derr)
	}
	rt.autoreplyStreaks.Close(key, rt.now()) // fin de episodio (Plan 049, ver streak.go).
	// El hecho está sellado (el flow_state ya no existe) ⇒ se registra. Nombre
	// ELEGIDO Y NO OBVIO: se reutiliza EffectEventEscaped —no uno nuevo— porque el
	// eje que la Ola 6 fijó para este efecto (E5) es «¿sobrevive el flow_state al
	// abandono?», no «¿lo pidió el cliente con la palabra exacta?»: aquí, igual
	// que en handleEscape, el Delete DESTRUYE el flow_state (event_deactivated,
	// en cambio, lo CONSERVA — stopEvent). Quien lea flow_events para saber si
	// hay progreso de nodo que perder no necesita un tercer nombre para una
	// tercera causa; necesita saber si lo hay, y la respuesta aquí es la MISMA
	// que en el escape global. La objeción obvia —el cliente no dijo ninguna
	// palabra de escape, solo tecleó algo que el menú no reconoció— es cierta y
	// se acepta: el nombre describe el EFECTO sobre el estado, no la intención
	// del cliente, igual que event_closed no distingue POR QUÉ terminó el flujo.
	if conocido {
		// El `reason` (T4.1) es lo que rescata la objeción de arriba: el nombre sigue
		// sin distinguir esta causa del escape deliberado —a propósito—, pero el
		// payload SÍ, así que quien quiera contarlas por separado ya puede.
		rt.emitEventEscaped(ctx, ev, EscapeReasonOrphanMenu)
	}
	return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, activeKind))
}

// advanceLiveStep es la SEGUNDA MITAD de advanceLive (Plan 029 · T9, extendida en la
// Ola 6 para acotar gocyclo tras E6): resuelve la reanudación por módulo y corre
// engine.Step sobre un flow_state QUE SÍ tiene flujo (st.FlowID != ""). Comportamiento
// idéntico a inlinearlo en advanceLive.
func (rt *Runtime) advanceLiveStep(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, st model.Conversation, def model.Flow, m *cloudlinkv1.IncomingMessage) error {
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

	// El nodo/módulo QUE PRODUCE effects es el que estaba vivo ANTES de este Step
	// (st.CurrentNode todavía sin avanzar aquí): Step delega en UN módulo por turno
	// (engine.Step, model.go), así que basta consultarlo una vez, ANTES de que la
	// reasignación de abajo pise st con el estado siguiente (Plan 054 · T3, D-054.4
	// — mismo predicado de F1/T2.1, vía engine.NodeProducesDurableContent).
	durable := rt.engine.NodeProducesDurableContent(def.Nodes[st.CurrentNode].Type)
	st, outs, effects, err := rt.engine.Step(ctx, def, st, engine.Input{Text: m.GetText()})
	if err != nil {
		return fmt.Errorf("runtime: step: %w", err)
	}
	st.LastWaMessageID = m.GetWaMessageId()
	// CIERRE NATURAL del evento (Plan 043 · T4.1): si el Step dejó el flujo en el
	// centinela y la conversación tenía evento activo, el evento pasa a `closed` y el
	// puntero se apaga EN ESTE MISMO Save — una escritura de flow_state, no dos.
	// closeIfFinished es una escritura APARTE del flow_state (events.TransitionEvent),
	// fuera del alcance de MD-054.1 (que es, literalmente, el orden Save/dispatch de
	// FLOW_STATE): se queda en su sitio de siempre, antes del fan-out.
	// El EventID del turno se captura ANTES del cierre natural (T4.5.1): los efectos
	// de este Step pertenecen al evento que estaba vivo MIENTRAS se produjeron, y
	// closeIfFinished apaga st.EventID en el turno que termina el flujo. Sin esta
	// captura, justo los efectos del final (p. ej. cart_closed) llegarían al
	// proyector con EventID "" y el hijo no podría declarar a su padre (D-043.21).
	turnEventID := st.EventID
	rt.closeIfFinished(ctx, &st)
	// Fan-out EN PROCESO (ADR-0003, sin broker) de los efectos declarados por el
	// módulo (Plan 015 · T3): el PersistSink escribe flow_events y proyecta
	// survey_results/intakes. Va ANTES del Save (Plan 054 · T3, MD-054.1 opción (a)):
	// si el sink que MATERIALIZA contenido durable agota su reintento acotado
	// (D-054.4), el turno se corta AQUÍ, antes de que el avance (p. ej. a la
	// despedida del carrito) quede persistido — nada que revertir, y el cliente
	// jamás se despide creyendo que compró. Para todo lo NO durable el orden no
	// cambia nada observable (el estado igual se guarda después, como siempre) y el
	// ADR-0003 sigue best-effort.
	ec := EffectContext{
		TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID,
		FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: turnEventID,
		Durable: durable,
	}
	if cutErr := rt.dispatch(ctx, ec, effects, sessionID); cutErr != nil {
		rt.log.Error("runtime: turno cortado: el sink durable no pudo materializar el efecto tras el reintento acotado",
			"error", cutErr, "session_id", sessionID, "flow_id", st.FlowID, "wa_message_id", m.GetWaMessageId())
		// NO se guarda st (el avance, incluida una posible despedida, se descarta:
		// MD-054.1 opción (a) — nada que revertir porque nunca se escribió) y NO se
		// persisten los mensajes del turno: solo el aviso explícito, por el MISMO
		// camino (anti-loop incluido) que cualquier otra auto-respuesta.
		return rt.sendReply(ctx, tenantID, sessionID, contactID, key, []engine.Output{{Text: defaultDurableSinkFailureNotice}})
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar estado: %w", err)
	}
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
	if offer, ok := rt.buildOpeningOffer(ctx, tenantID, sessionID, contactID); ok {
		return rt.sendOffer(ctx, key, sessionID, offer)
	}
	// Sin oferta (sin opening cableado, BuildOpening falló, o Offering.Empty): cae al
	// fallback del tenant de siempre (INV-20) — comportamiento IDÉNTICO al de antes de
	// que existiera buildOpeningOffer; ver su docstring para las tres causas.
	//
	// offerAlreadyEmpty=true viaja hasta degradeDurableStart (D5, review de código):
	// buildOpeningOffer YA se preguntó aquí arriba y ya sabemos que da ok=false —
	// dentro del mismo entrante, bajo el keyedMutex de la clave, nada cambia esa
	// respuesta entre esta línea y la siguiente—, así que si startPlainFlow rebota
	// por ErrDurableFlowNeedsEvent no hace falta que degradeDurableStart la
	// reconstruya: eso era construir la misma oferta DOS veces (y, si BuildOpening
	// fallaba, duplicar el WARN) para una sola decisión.
	return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec, true)
}

// buildOpeningOffer arma la oferta del despachador para (tenant, sesión, contacto):
// es la ÚNICA construcción que usan openWithOffer (T3.8.4, la rama Fallback de
// siempre) y la degradación de contenido durable (Plan 054 · T2.4), para que las dos
// ramas no puedan divergir en qué es "la oferta".
//
// ok=false cubre TRES causas a propósito indistinguibles para quien llama —sin
// opening cableado, BuildOpening falló, o la oferta salió vacía
// (events.Offering.Empty, REQ-27b/INV-20/INV-054.1: el tenant no tiene nada que
// ofrecer)—: en los tres, D-054.3(b) deja que cada llamante decida su propia
// consecuencia (openWithOffer cae al fallback del tenant; la degradación de T2.4 NO
// puede hacer lo mismo sin reentrar, ver su docstring). Esta función SOLO construye
// —nunca arranca un flujo ni llama a startPlainFlow/startLocked—, así que no puede
// ser el origen de ningún bucle.
func (rt *Runtime) buildOpeningOffer(ctx context.Context, tenantID, sessionID, contactID string) (events.Offering, bool) {
	if rt.opening == nil {
		return events.Offering{}, false
	}
	offer, err := rt.opening.BuildOpening(ctx, events.ConversationRef{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
	})
	if err != nil {
		rt.log.Warn("runtime: no se pudo construir la entrada que ofrece",
			"error", err, "session_id", sessionID)
		return events.Offering{}, false
	}
	if offer.Empty() {
		return events.Offering{}, false
	}
	return offer, true
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
// Las DOS puertas que pasan por aquí y consumen turno son event_start y, desde el
// Plan 054 · F2b (D-A), la intención LLM mapeada a un event_kind: decisionFor
// (trigger/config_resolver.go:171-180) puebla dec.EventKind para KindEventStart, y
// la rama sig.Intent != nil de Resolve (config_resolver.go, rama del match llm) lo
// puebla igual cuando la regla GANADORA trae event_kind —en ambos casos beginEvent
// (events.go:245) no mira dec.Action, solo dec.EventKind, así que le da igual por
// cuál de las dos puertas llegó—. beginEvent (events.go:246) trata cualquier
// Decision con EventKind=="" como la keyword de siempre —no consume, cae a
// startPlainFlow—. Por eso una keyword pura, o una llm sin event_kind (Action=Start,
// EventKind=="") SÍ entra a este `if` (dec.Action != Fallback lo deja pasar) pero
// beginEvent la descarta enseguida.
//
// La tercera puerta —elegir en el despachador— entra por su propio camino
// (StartNewOfKind, events.go).
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
	// offerAlreadyEmpty=false: este camino (event_start/keyword) nunca pasó por
	// openWithOffer, así que si startPlainFlow rebota por un flujo durable sin
	// evento, degradeDurableStart SÍ tiene que construir la oferta —nadie la probó
	// todavía—.
	return rt.startPlainFlow(ctx, tenantID, sessionID, key, contactID, dec, false)
}

// startPlainFlow arranca el flujo del tenant SIN parir evento: es el camino del Plan
// 019 byte a byte, extraído para que la rama de eventos no lo duplique.
//
// dec.Params/IntentName solo vienen poblados si la decisión provino de una regla
// kind='llm' (T8): startLocked los siembra en Vars para el pre-carga del módulo.
//
// offerAlreadyEmpty viaja SOLO para degradeDurableStart (D5, review de código): dice
// si el llamante (openWithOffer) YA le preguntó a buildOpeningOffer y salió false,
// para que la degradación no la vuelva a pedir. startFromDecision, que nunca pasó
// por openWithOffer, siempre pasa false aquí.
func (rt *Runtime) startPlainFlow(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision, offerAlreadyEmpty bool) error {
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
	//
	// Este token cubre TODA la consecuencia de este entrante, incluida su posible
	// degradación (D1, review de código): si startLocked rebota más abajo con
	// ErrDurableFlowNeedsEvent, degradeDurableStart manda la oferta con
	// sendOfferNow —SIN volver a cobrar— precisamente porque el cobro ya ocurrió
	// aquí. Cobrar también allá sería pagar DOS veces por el mismo saliente y, con
	// el cupo justo (burst 1), dejaría la degradación muda pese al WARN que la
	// anuncia.
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
		if errors.Is(serr, ErrDurableFlowNeedsEvent) {
			return rt.degradeDurableStart(ctx, tenantID, sessionID, key, contactID, dec, offerAlreadyEmpty)
		}
		return serr
	}
	return nil
}

// degradeDurableStart atiende D-054.3(b) (Plan 054 · T2.4): startPlainFlow acaba de
// recibir ErrDurableFlowNeedsEvent —dec.FlowID lleva contenido durable y este camino
// no trae evento (keyword, o fallback sin oferta que ya cayó aquí)—. El interlocutor
// aquí es un contacto de WhatsApp sin ninguna capacidad de leer un código de error
// (a diferencia de la API, T2.5), así que el rechazo del motor NUNCA se le muestra:
// se degrada a la MISMA oferta que openWithOffer construye (buildOpeningOffer), para
// que entre por la 3.ª puerta de T2.5/Plan 043 (elección en el despachador) — esa sí
// pare evento.
//
// origen sale de dec.Action, lo único que trigger.Decision trae en la mano aquí:
// Start es una keyword pura, StartEvent es un event_start/LLM cuyo beginEvent no
// consumió (sin plano de eventos cableado, el mismo trato que una keyword), y
// Fallback es el `fallback` del tenant sin nada que ofrecer. No hay un cuarto valor
// que distinguir; inventar una etiqueta más fina que esta sería mentir en el log.
//
// EL TOKEN anti-loop de este saliente lo cobró YA startPlainFlow (D1, review de
// código): esta función usa sendOfferNow —el cuerpo de sendOffer sin su propio
// cobro— y NUNCA rt.sendOffer, precisamente para no cobrar dos por el mismo mensaje.
// Cobrar aquí también dejaba la degradación MUDA con el cupo justo (burst 1) pese al
// WARN que la anuncia: el mismo error que el docstring de openWithOffer ya advertía
// para su propio camino, reintroducido por este y corregido en el mismo sitio
// (sendOfferNow).
//
// offerAlreadyEmpty (D5, review de código) evita reconstruir la oferta cuando
// openWithOffer YA la construyó y dio vacía: solo se vuelve a preguntar cuando este
// camino NUNCA pasó por buildOpeningOffer (keyword/event puros, que llegan aquí vía
// startFromDecision con offerAlreadyEmpty=false).
//
// NUNCA vuelve a llamar a startPlainFlow ni a openWithOffer (la trampa de recursión
// de esta tarea): si buildOpeningOffer no tiene nada que dar —sin opening cableado,
// BuildOpening falló, o la oferta salió vacía—, reintentar por cualquiera de esos dos
// caminos repetiría la MISMA guarda sobre el MISMO flujo durable y entraría en
// bucle. Ese corner es MD-054.2: un tenant con un fallback/keyword durable y sin
// ningún event_start vivo se queda SIN RESPUESTA (silencio elegido, no un panic ni
// un bucle). D-054.8/T2.7 —otro frente de este plan, NO implementado aquí— lo cierra
// en tiempo de CONFIGURACIÓN, rechazando el alta de esa combinación.
func (rt *Runtime) degradeDurableStart(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision, offerAlreadyEmpty bool) error {
	origen := "fallback"
	switch dec.Action {
	case trigger.Start:
		origen = "keyword"
	case trigger.StartEvent:
		origen = "event"
	}
	nodeType := ""
	if def, derr := rt.store.LatestDefinition(ctx, tenantID, dec.FlowID); derr == nil {
		nodeType = rt.engine.DurableNodeType(def)
	}
	rt.log.Warn("runtime: flujo con contenido durable no puede arrancar sin evento; se degrada a la oferta del despachador",
		"flow_id", dec.FlowID, "node_type", nodeType, "origin", origen, "session_id", sessionID)
	if !offerAlreadyEmpty {
		if offer, ok := rt.buildOpeningOffer(ctx, tenantID, sessionID, contactID); ok {
			return rt.sendOfferNow(ctx, key, sessionID, offer)
		}
	}
	// MD-054.2 (ver el docstring de arriba): sin oferta a la que degradar, el
	// contacto se queda sin respuesta en vez de reentrar en un bucle sobre el mismo
	// flujo durable.
	rt.log.Warn("runtime: sin oferta a la que degradar (MD-054.2, D-054.8/T2.7 lo cierra en configuración); el contacto se queda sin respuesta",
		"flow_id", dec.FlowID, "session_id", sessionID)
	return nil
}

// buildSignal arma la señal de entrada del resolver de disparos (Plan 029 · T7): el
// texto crudo del entrante y, SOLO si el mensaje trae una intención LLM Y el tenant
// tiene la feature llm_intent habilitada (gate de verdad, ADR-0022), la intención
// resuelta. Sin resolver de entitlements cableado (nil) o sin la feature, la señal
// lleva SOLO texto ⇒ una regla kind='llm' nunca dispara sin derecho (camino actual).
// Un fallo del resolver de entitlements es best-effort: se loguea y se descarta la
// intención (se prefiere no abrir la capacidad por un fallo transitorio).
//
// activeEventKind es el TIPO del evento ACTIVO en el instante de interpretar la señal
// (Plan 043 · T5.3, D-043.9; enmendado Ola 6, decisión de Jhoan 2026-08-11). ⚠️
// ⚠️ 🔁 REESCRITO EN EL PLAN 053 · T2.3/T2.4 — lo que decía aquí era VERDAD MEDIDA en
// la Ola 6 y dejó de serlo dos veces seguidas, así que se dice qué cambió y cuándo:
//
//   - Decía que de los CUATRO llamantes los CUATRO pasaban "" por construcción, y que
//     por tanto el scoping «NO SE DISPARA NUNCA» desde aquí. Para releaseFinishedState
//     eso se apoyaba en que closeIfFinished apaga el puntero en el mismo turno.
//   - Desde T1.6 el cierre apaga el ACTIVO **solo si era el mismo evento** que el dueño
//     (hueco D): en el caso divergente —`menu` activo sobre un flujo cuyo dueño es el
//     `cart`— el activo SOBREVIVE al cierre a propósito.
//   - Y desde T2.4 releaseFinishedState **lee ese activo y lo pasa**. O sea: hoy ese
//     llamante SÍ puede traer un kind real, y el scoping SÍ se ejerce por esa vía. Es
//     una reposición deliberada, no una fuga: es exactamente lo que la retirada de la
//     guarda de posesión había dejado huérfano.
//
// 🔴 Y el matiz que hace que esto importe más de lo que parece: con ActiveEventKind ""
// la guarda del resolver (`sig.ActiveEventKind != "" && …`, trigger/config_resolver.go)
// **no descarta NADA**. Pasar "" no es «no acotar»: es **no filtrar**, que deja casar
// reglas llm anotadas que no pertenecen al evento vivo. El lado seguro es pasar el kind
// real cuando lo hay, no omitirlo. El scoping
// que este parámetro habilita se ejerce hoy en UN (1) solo sitio de producción: el
// menú PENDIENTE (advanceLive, rama st.FlowID == ""), y ahí con la config canónica
// (reglas event_start de cart/survey CON flow_id, admin/triggers.go:63) el único
// valor posible es el tipo del MENÚ. SIN EMBARGO, un event_start sin flow_id sobre
// un menú pendiente deja el puntero en un evento de otro tipo con FlowID vacío, así
// que entonces el valor alcanzable es el de ESE evento.
//
// Ver §5.1 del CONTRATO-OLA5: con una conversación viva y un evento en curso el
// entrante va por ResolveLive, que NUNCA consulta reglas llm (INV-02), así que el
// caso del criterio del plan («con cart activo, una señal de encuesta cae a
// desconocido») NO es alcanzable en producción y se fija solo a nivel de resolver.
// Así se deja (D-043.9 vía Jhoan, 2026-08-10): no se toca ResolveLive ni
// liveEventSwitch en esta tarea.
func (rt *Runtime) buildSignal(ctx context.Context, tenantID string, m *cloudlinkv1.IncomingMessage, activeEventKind string) trigger.Signal {
	sig := trigger.Signal{Text: m.GetText(), ActiveEventKind: activeEventKind}
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
//
// ⚠️ Orden E-4 (decisión de Jhoan, 2026-08-11): resumen → Delete → efecto (E-5) → y
// SOLO ENTONCES replyAllowed gobernando el AVISO. Antes, replyAllowed cortaba de
// primero: con el cupo anti-loop agotado el escape NO OCURRÍA — ni Delete, ni resumen,
// ni telemetría — y el cliente que dijo la palabra de emergencia se quedaba atrapado
// en silencio absoluto. Es el MISMO orden que stopEvent (events.go) ya usa, y el
// mismo precedente textual se aplica aquí: «Va ANTES del replyAllowed a propósito:
// que la red anti-loop impida CONTARLO al cliente no significa que no haya pasado.»
// Contrapartida asumida: un peer automático que repite la palabra de escape produce N
// Delete idempotentes (y N event_escaped, el mismo trato que ya reciben
// event_cancelled/event_closed ante reintentos — no es nuevo en este camino).
func (rt *Runtime) handleEscape(ctx context.Context, tenantID, sessionID string, key store.Key, contactID, message string, st model.Conversation) error {
	// #15 en su séptimo punto (E-5): handleEscape recibe la CONVERSACIÓN, no la fila
	// del evento, así que emitir event_escaped exige releerla — el mismo patrón que
	// activeEvent ya paga en closeIfFinished/stopEvent. Se lee ANTES del Delete (el
	// evento no depende del flow_state, pero es el mismo momento en que stopEvent lo
	// hace) para no acoplar el efecto al orden del borrado.
	var ev events.Event
	var conocido bool
	if st.EventID != "" {
		ev, conocido = rt.activeEvent(ctx, key, sessionID, st.EventID)
	}
	// El escape es el tercer abandono real (T3.4), y el más brusco: se resume ANTES del
	// Delete, porque ese Delete se lleva las Vars con el nivel de la sub-máquina.
	rt.summarizeAbandoned(ctx, key, sessionID, st, "")
	if err := rt.store.Delete(ctx, key); err != nil {
		return fmt.Errorf("runtime: cerrar conversación por escape: %w", err)
	}
	// Fin de episodio (Plan 049, ver streak.go). Va aquí y NO tras el envío del aviso:
	// el episodio muere con el estado, no con el mensaje — y el aviso de más abajo
	// puede no salir (cupo anti-loop agotado) sin que eso cambie que la racha terminó.
	// El propio aviso, si sale, abre una racha nueva de 1 sobre la misma clave, que es
	// lo correcto: es la primera —y única— auto-respuesta del episodio siguiente.
	rt.autoreplyStreaks.Close(key, rt.now())
	// El hecho está sellado (el flow_state ya no existe) ⇒ se registra (E-5,
	// EffectEventEscaped: NO event_deactivated — stopEvent conserva el flow_state,
	// este Delete lo destruye, y son abandonos distintos). El único de los tres
	// emisores donde el contacto PIDIÓ el abandono con la palabra exacta, y por eso
	// el único cuyo `reason` mide intención y no consecuencia (T4.1).
	if conocido {
		rt.emitEventEscaped(ctx, ev, EscapeReasonClientEscape)
	}
	// Red anti-loop (Plan 020 · T0): el AVISO de escape es una auto-respuesta ⇒
	// consume un token. Agotado ⇒ no se avisa, pero el escape YA OCURRIÓ (E-4): el
	// Delete y el event_escaped de arriba no dependen de esto.
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	notice := message
	if notice == "" {
		notice = defaultEscapeMessage
	}
	if _, err := rt.send(ctx, sessionID, to, key, []engine.Output{{Text: notice}}); err != nil {
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
//   - la sesión es PASIVA (T1): escucha/transporta pero no dispara triggers, no
//     avanza con auto-envío ni escapa. Una conversación EN CURSO deja de avanzar
//     mientras siga pasiva (no se borra su estado; vuelve si se re-activa). Valor
//     vacío/desconocido ⇒ activa (no-regresión de esta guarda).
//   - el remitente es un número PROPIO del tenant (T2, anti-self-loop): una sesión
//     propia hablando; no se auto-responde (defensa semántica contra el bucle
//     sesión↔sesión del Plan 019). Consciente del perfil: solo cuentan como
//     "propios" los números de sesiones NO pasivas — una pasiva nunca auto-responde,
//     así que una sesión activa SÍ puede responder a mensajes que llegan desde el
//     número personal (pasivo) del mismo tenant sin riesgo de loop.
//
// El `profile` que entra por parámetro sale de fleet_sessions.profile (Plan 046 ·
// T1.1), agregado por el resolver. 🔴 Y su DEFAULT es PASIVO (0063, D-07): una sesión
// NUEVA sin configurar NO entra al motor reactivo hasta que su dueño la active.
//
// Sin perfil pasivo y sin números propios poblados, devuelve false ⇒ no-regresión total.
func (rt *Runtime) reactiveBlocked(ctx context.Context, tenantID, sessionID, profile, fromPn string) bool {
	if profile == profilePassive {
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
// defensa semántica contra el bucle sesión↔sesión del Plan 019).
//
// 🔑 LA NORMALIZACIÓN OCURRE AQUÍ Y SOLO AQUÍ (Plan 046 · T4.1). El remitente
// (from_pn) llega como lo mande WhatsApp —con '+', espacios, guiones o paréntesis—
// y se pasa por contact.Normalize(KindPhoneE164, …), que lo deja en dígitos puros.
// El checker calcula el ÍNDICE CIEGO sobre exactamente esos bytes, y el escritor del
// índice normalizó con la MISMA función: por eso "+57 300-111-0000" y "573001110000"
// dan el mismo veredicto. Si esta línea desapareciera, el HMAC saldría distinto y el
// número dejaría de casar consigo mismo — el fallo sería MUDO (la guarda no
// bloquearía nada y nadie vería un error), que es lo peor que puede pasarle a una
// defensa anti-bucle. crypto.KeyProvider.BlindIndex NO normaliza a propósito: es un
// HMAC sobre bytes, no conoce el dominio de los teléfonos.
//
// El veredicto es CONSCIENTE DEL PERFIL: el checker excluye los números de sesiones
// pasivas — una pasiva nunca auto-responde (reactiveBlocked lo corta), así que un
// mensaje desde ese número no puede cerrar un bucle; bloquear ahí solo impediría
// atender al número personal del tenant. El rate-limit por conversación (T0) sigue
// como red.
//
// Es CONSERVADORA hacia PROCESAR: sin checker (nil), sin from_pn, si el número no
// normaliza o si la consulta falla ⇒ devuelve false (no bloquea: la ausencia de dato
// no debe silenciar tráfico legítimo). NUNCA loguea el número ni su índice ciego
// (PII): solo el hecho y IDs opacos.
func (rt *Runtime) isSelfLoop(ctx context.Context, tenantID, sessionID, fromPn string) bool {
	if rt.selfNumbers == nil || fromPn == "" {
		return false
	}
	norm, err := contact.Normalize(contact.KindPhoneE164, fromPn)
	if err != nil {
		return false // sin número normalizable no se puede afirmar self-loop.
	}
	propio, err := rt.selfNumbers.IsSelfNumber(ctx, tenantID, norm)
	if err != nil {
		// El error se ANUNCIA (no se traga) pero no bloquea: una BD lenta o un
		// KeyProvider ausente no pueden convertirse en un tenant que deja de atender.
		rt.log.Warn("runtime: no se pudo comprobar si el remitente es un número propio del tenant; guarda anti-self-loop omitida",
			"error", err, "session_id", sessionID)
		return false
	}
	if !propio {
		return false
	}
	rt.countReactiveBlocked(reasonSelfLoop)
	rt.log.Warn("runtime: entrante de un número propio del tenant; auto-respuesta evitada (anti-self-loop)",
		"tenant_id", tenantID, "session_id", sessionID)
	return true
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
