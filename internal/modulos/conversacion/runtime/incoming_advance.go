// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de incoming.go (E-13): el AVANCE —un entrante sobre una conversación viva (incoming.go
// §4)—: el orden de sus pasos, las dos sueltas de mitad de conversación, el paso del engine con
// su fan-out y su turno cortado (RT-10), el escape global (RT-9) y la idempotencia consecutiva.
// Solo se movieron declaraciones.

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
		// T2.3: `pending` es lo ÚNICO que se consulta aquí. `ev` y `known` ya no
		// viajan a releaseFinishedState —hablaban del DUEÑO, y lo que esa función
		// necesita es el ACTIVO (T2.4)—, así que se descartan explícitamente en vez de
		// arrastrarse por la firma.
		if _, _, pending := rt.pendingClosure(ctx, st); !pending {
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
// el parámetro `known`, que valía true por la guarda de POSESIÓN de H2 (el puntero
// miraba a un evento ajeno). Al retirarse la guarda, pendingClosure dejó de poder
// devolver `known=true, pending=false`: la rama quedó MUERTA y con ella se
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
	var alive bool
	if st.EventID != "" {
		activeKind = rt.activeEventKind(ctx, key, sessionID, st.EventID)
		ev, alive = rt.activeEvent(ctx, key, sessionID, st.EventID)
	}
	if derr := rt.store.Delete(ctx, key); derr != nil {
		return fmt.Errorf("runtime: soltar el flow_state terminal: %w", derr)
	}
	rt.autoreplyStreaks.Close(key, rt.now()) // fin de episodio (Plan 049, ver streak.go).
	if alive {
		// EffectEventEscaped por el MISMO eje que el quinto camino de suelta (E5/E6): el
		// nombre describe el EFECTO sobre el flow_state —destruido, no conservado como en
		// event_deactivated—, no la intención del cliente. La INTENCIÓN, que el nombre
		// deliberadamente no lleva, viaja desde T4.1 en el `reason`: aquí nadie abandonó
		// nada, el flujo llegó a su nodo terminal y su fila se recoge.
		rt.emitEventEscaped(ctx, ev, EscapeReasonOwnerFlowFinished)
	}
	return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, activeKind), m)
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
	var known bool
	if st.EventID != "" {
		activeKind = rt.activeEventKind(ctx, key, sessionID, st.EventID)
		ev, known = rt.activeEvent(ctx, key, sessionID, st.EventID)
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
	if known {
		// El `reason` (T4.1) es lo que rescata la objeción de arriba: el nombre sigue
		// sin distinguir esta causa del escape deliberado —a propósito—, pero el
		// payload SÍ, así que quien quiera contarlas por separado ya puede.
		rt.emitEventEscaped(ctx, ev, EscapeReasonOrphanMenu)
	}
	return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, activeKind), m)
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
		//
		// 🔴 Y TAMPOCO ENTRA EN LA VENTANA DE CAPTACIÓN, Y ESO ES UNA DECISIÓN
		// (Plan 044, revisada el 2026-08-22 — antes era un silencio). El motivo NO es
		// el presupuesto de I/O, que aquí sobraría: es que meterlo sería INCOHERENTE
		// con lo que la ventana promete. `source_refs` es la lista de mensajes cuyo
		// literal el pipeline va a leer del hilo, y este turno acaba de decidir, dos
		// líneas más arriba, que su literal NO SE ESCRIBE en el hilo. La referencia
		// quedaría colgando: el compositor de T1.4 leería el hilo, no encontraría este
		// mensaje, y compondría un `source_text` al que le falta justo lo que el
		// cliente pidió — SIN ERROR y sin que nadie lo note, que es la forma peor.
		// Ancla, además, sobre un turno REVERTIDO: si es el primero, su `message_ts`
		// pasaría a ser la base de fechas (D-044.9) de un pedido que no ocurrió.
		// Y no se pierde nada: al cliente se le dice explícitamente que reintente, y
		// el mensaje con el que reintente sí abrirá (o ampliará) su ventana.
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
	// LA VENTANA DE CAPTACIÓN (Plan 044 · Ola 1 · T1.1/T1.2, ver aggregator.go). Va
	// justo detrás del hilo y usa el MISMO turnEventID por el mismo motivo: el
	// mensaje pertenece al evento que estaba vivo mientras se produjo. Es el
	// LECTOR de lo que la línea de arriba acaba de escribir.
	//
	// Presupuesto acotado y escrito (D-044.26): UNA sentencia, cero lecturas, cero
	// cripto, cero red. Best-effort integral — no devuelve error y jamás corta el
	// turno (INV-10). Sin WithAggregator es un no-op exacto.
	rt.observeForAggregation(ctx, tenantID, sessionID, contactID, turnEventID, m)
	return rt.sendReply(ctx, tenantID, sessionID, contactID, key, outs)
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
	var known bool
	if st.EventID != "" {
		ev, known = rt.activeEvent(ctx, key, sessionID, st.EventID)
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
	if known {
		rt.emitEventEscaped(ctx, ev, EscapeReasonClientEscape)
	}
	// Red anti-loop (Plan 020 · T0): el AVISO de escape es una auto-respuesta ⇒
	// consume un token. Agotado ⇒ no se avisa, pero el escape YA OCURRIÓ (E-4): el
	// Delete y el event_escaped de arriba no dependen de esto.
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destination(ctx, tenantID, contactID)
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
	// Saliente FUERA DE TURNO (Plan 044 · T1.6, D-044.24). El aviso de escape es
	// nuestro y no contesta a nada: el cliente dijo la palabra de salida, no hizo un
	// pedido. Con `known` en false, `ev` es el valor cero y ev.ID == "" ⇒
	// persistOutOfTurnMessage no escribe nada, que es lo correcto: sin evento no hay
	// hilo. La misma guarda que ya gobierna el emitEventEscaped de arriba.
	rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, ev.ID, notice)
	return nil
}

// consecutiveReplay es la idempotencia CONSECUTIVA (design.md §10.G): corta la
// re-entrega INMEDIATA de un mensaje comparándolo con el último procesado en el
// estado del flujo (last_wa_message_id). Complementa —no reemplaza— el dedupe
// persistente (duplicateIngest), que cubre además los duplicados intercalados y los
// caminos que no tocan last_wa_message_id (disparo/escape).
func consecutiveReplay(st model.Conversation, m *cloudlinkv1.IncomingMessage) bool {
	return st.LastWaMessageID != "" && st.LastWaMessageID == m.GetWaMessageId()
}
