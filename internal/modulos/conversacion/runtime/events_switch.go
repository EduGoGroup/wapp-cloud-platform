// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Trozo de events.go (E-13): la navegación entre eventos SOBRE una conversación viva
// (liveEventSwitch), la conmuta hacia un evento que ya vivía, la entrada al flujo del evento con
// sus dos punteros (EV-4, RT-16) y event_stop (EV-5). Solo se movieron declaraciones.

// liveEventSwitch atiende un entrante que llega SOBRE una conversación viva y
// pregunta al resolver si ese texto es una orden de navegación entre eventos
// (Plan 043 · T2.2/T2.4). Devuelve true si el turno se consumió.
//
// Solo se consultan event_start y event_stop —eso lo garantiza ResolveLive, no
// esto—: keyword, fallback y llm NO se evalúan con conversación viva, de modo que
// INV-02 sigue intacto para todo lo demás y el texto sigue siendo del módulo.
//
// Un fallo del resolver es BEST-EFFORT, igual que IsEscape: se LOGUEA y la
// conversación avanza con normalidad. Preferimos un salto perdido a una
// conversación bloqueada por un error transitorio.
func (rt *Runtime) liveEventSwitch(ctx context.Context, tenantID, sessionID string, key store.Key, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	if rt.events == nil {
		return false, nil
	}
	// La elección del menú se mira ANTES que los disparos: es la respuesta directa a
	// la pregunta que acabamos de hacerle al cliente, y solo se consulta mientras el
	// evento ACTIVO es el propio menú — así el «2» de un carrito nunca se confunde con
	// el «2» de una lista que ya nadie tiene delante.
	if done, cerr := rt.menuChoice(ctx, key, sessionID, st, m); cerr != nil || done {
		return done, cerr
	}
	dec, err := rt.triggers.ResolveLive(ctx, tenantID, sessionID, m.GetText())
	if err != nil {
		rt.log.Warn("runtime: ResolveLive falló; se ignora el salto de evento",
			"error", err, "session_id", sessionID)
		return false, nil
	}
	switch dec.Action {
	case trigger.StartEvent:
		// El `event_id` se DESCARTA aquí a propósito (Plan 044): un salto por tipo sobre
		// una conversación VIVA es una orden de navegación —«carrito» dicho a media
		// encuesta—, no un pedido. Anclar la ventana en esa palabra pondría el
		// `message_ts` del presupuesto en ella y la dejaría de primera referencia. El
		// mensaje que ABRE el evento sí entra, pero por su camino (startFromDecision).
		// openingTurn{} por el MISMO motivo por el que se descarta el `event_id`: una
		// orden de navegación sobre conversación viva no es un pedido, no abre la ventana
		// y no abre el hilo del evento al que se salta (Plan 044 · T1.4).
		_, done, berr := rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, st.EventID, openingTurn{})
		return done, berr
	case trigger.StopEvent:
		return true, rt.stopEvent(ctx, key, sessionID, st)
	default:
		return false, nil
	}
}

// switchToEvent conmuta la conversación hacia un evento que YA estaba vivo: apunta
// el puntero, refresca su reloj y re-entra al flujo con el que ese evento nació
// (ev.FlowID, congelado en la fila para que el salto no dependa de qué regla lo
// trajo). El status del evento que se abandona NO se toca (D-043.4).
//
// El progreso de nodo del evento que se deja atrás se pierde: flow_state tiene UNA
// fila por conversación. Es el precio aceptado por el diseño, que restaura desde la
// VERDAD DURABLE del módulo (las líneas del intake, las respuestas de la encuesta),
// no desde el puntero de nodo.
// Antes de re-entrar al flujo se le RECUERDA al cliente lo que llevaba decidido (T3.4,
// E-4). Va aquí y no en resumeEvent porque a un evento se vuelve por DOS caminos —
// diciendo la palabra de su tipo, que entra por beginEvent, y eligiendo su número en la
// lista de rescatables, que entra por resumeEvent— y los dos pasan por aquí. Ponerlo en
// uno solo deja al otro sin recordatorio, que es lo que medimos al escribirlo.
func (rt *Runtime) switchToEvent(ctx context.Context, key store.Key, sessionID string, ev events.Event) error {
	if err := rt.events.Touch(ctx, ev.ID); err != nil {
		return fmt.Errorf("runtime: refrescar el reloj del evento al conmutar: %w", err)
	}
	rt.emitEventEffect(ctx, ev, EffectEventSwitched)
	rt.sendResumeSummary(ctx, key, sessionID, ev)
	// Sin coletilla: volver a un evento que ya existía no es atender una intención, y
	// además el propio evento al que se vuelve sería lo primero que se anunciaría.
	//
	// Y SIN turno de apertura (openingTurn{}): el literal del cliente que trajo hasta
	// aquí ya lo escribió beginEvent antes de llamar —ver la rama `reuse`— y las
	// salidas de esta re-entrada NO van al hilo, porque el evento ya tiene su nodo
	// inicial escrito de cuando nació (Plan 044 · T1.4).
	//
	// ⚠️ EL BOOL DEL CORTE SE DESCARTA AQUÍ, Y NO ES UN OLVIDO. En birthEvent el corte
	// obliga a no devolver el id porque el literal del cliente se escribía DENTRO de
	// startLocked (persistOpeningTurn) y por tanto no llegó a escribirse. En la CONMUTA
	// pasa lo contrario: beginEvent ya escribió ese literal ANTES de llamar aquí, con
	// persistTurnMessages, así que la referencia de la ventana tiene su literal en el
	// hilo aunque la re-entrada se corte. La invariante se cumple y no hay nada que
	// suprimir; lo que este camino sí corta —el render del nodo inicial— nunca iba al
	// hilo de todos modos.
	_, err := rt.enterEventFlow(ctx, key, sessionID, ev.ID, ev.Kind, ev.FlowID, nil, "", "", openingTurn{})
	return err
}

// enterEventFlow deja la conversación dentro del evento eventID: arranca (o
// re-arranca) flowID y estampa el puntero flow_state.event_id.
//
// Se borra el flow_state previo a propósito: startLocked rechaza con
// ErrConversationExists si ya hay uno, y un salto por tipo es precisamente cambiar
// de conversación. Lo que se borra es el PUNTERO DE NODO del motor, nunca datos de
// negocio: el evento que se abandona sigue `open` con su intake intacto.
//
// flowID vacío ⇒ no hay flujo que arrancar y solo se estampa el puntero. Es el caso
// del tipo `menu`, cuyo contenido lo renderiza el despachador y no una fila de
// flow_definitions (D-043.3).
//
// `tagline` llega YA RESUELTA y no se calcula aquí: cuando este camino viene de un
// evento que acaba de nacer, mirar los rescatables desde dentro devolvería ese mismo
// evento (ver birthEvent). Quien sabe cuándo era seguro preguntarlo es el llamante.
//
// `opening` es el ÚLTIMO tramo del correo del turno de apertura (Plan 044 · T1.4):
// llega de birthEvent poblado y de switchToEvent en cero, y esta función solo lo pasa
// a startLocked. El camino del `menu` (kind == EventKindMenu) sale ANTES de usarlo y
// eso es correcto: el menú no arranca flujo, así que no hay turno de arranque que
// escribir — su contenido lo pinta presentMenu, que ya está fuera del censo de
// emisores del hilo (ver thread.go).
//
// # EL BOOL DEVUELTO: «EL ARRANQUE SE CORTÓ» (Plan 044, corrección del 2026-08-22)
//
// Sube TAL CUAL desde startLocked (ver su cabecera) y esta función no lo interpreta:
// solo lo transporta hasta birthEvent, que es quien decide su consecuencia. Vale true
// cuando el sink durable agotó su reintento acotado (D-054.4) y el arranque volvió sin
// guardar estado y SIN ESCRIBIR HILO. Sostiene la invariante del plan —todo mensaje que
// entra en `source_refs` tiene su literal en el hilo—, y por eso `started` va atado a
// su negación: con el turno cortado no hay flow_state nuevo del que este evento sea
// dueño, porque no hay flow_state en absoluto.
//
// Los caminos que no arrancan flujo —el `menu` y el `flowID == ""`— devuelven false: no
// hay turno que cortar donde no hay turno.
func (rt *Runtime) enterEventFlow(ctx context.Context, key store.Key, sessionID, eventID, kind, flowID string, params map[string]string, intentName, tagline string, opening openingTurn) (bool, error) {
	// El SALTO POR TIPO es un abandono: antes de tocar nada, se resume el evento que se
	// deja atrás (T3.4). Va aquí arriba y no dentro del `if flowID != ""` porque el
	// salto al menú también abandona, y porque lo que sigue BORRA el flow_state —y con
	// él las Vars de las que sale el nivel de la sub-máquina.
	if previous, ok, err := rt.store.Load(ctx, key); err != nil {
		rt.log.Warn("runtime: no se pudo leer el estado previo para resumir el abandono",
			"error", err, "session_id", sessionID)
	} else if ok {
		rt.summarizeAbandoned(ctx, key, sessionID, previous, eventID)
	}
	if kind == trigger.EventKindMenu {
		// El menú NO es una fila de flow_definitions (D-043.3): su contenido es dinámico
		// por contacto, así que lo renderiza el despachador y no el motor. Sin turno que
		// arrancar no hay turno que cortar ⇒ false.
		return false, rt.presentMenu(ctx, key, sessionID, eventID)
	}
	// `started` es EL DATO DEL CAMINO que pointStateAtEvent necesita para saber si
	// puede estampar el dueño: vale true SOLO si aquí abajo se borró el flow_state y
	// se arrancó uno nuevo para eventID. No es una inferencia sobre flowID leída más
	// tarde —es el hecho, capturado por quien lo hizo—. Ver el docstring de
	// pointStateAtEvent y el camino de la tercera salida (flowID=="" y kind!="menu").
	started := false
	cutOff := false
	if flowID != "" {
		if err := rt.store.Delete(ctx, key); err != nil {
			return false, fmt.Errorf("runtime: liberar el estado previo al entrar al evento: %w", err)
		}
		// Fin de episodio (Plan 049, ver streak.go): el salto por tipo destruye el
		// flow_state y arranca otro flujo, así que la racha del flujo que se deja atrás
		// se cierra aquí y el startLocked de abajo abre una nueva en 1.
		rt.autoreplyStreaks.Close(key, rt.now())
		// eventID viaja al arranque (T4.5.1): startLocked no puede leerlo de
		// st.EventID (el puntero se estampa DESPUÉS, en pointStateAtEvent) y es
		// AQUÍ donde el evento recién nacido/conmutado está en la mano.
		_, cut, err := rt.startLocked(ctx, key.TenantID, flowID, sessionID, key, key.ContactID, eventID, params, intentName,
			tagline, opening)
		if err != nil {
			return cut, fmt.Errorf("runtime: arrancar el flujo del evento: %w", err)
		}
		// `started` es la NEGACIÓN del corte y no un `true` a secas (Plan 044): con el
		// turno cortado, startLocked volvió ANTES de su Save, así que no existe la fila
		// de la que este evento sería dueño. pointStateAtEvent no encontraría nada que
		// estampar de todos modos —y lo loguearía en Debug—, pero el dato del camino
		// tiene que decir la verdad: es él, y no una relectura tardía, lo que
		// pointStateAtEvent usa para no escribir un dueño falso.
		cutOff = cut
		started = !cut
	}
	return cutOff, rt.pointStateAtEvent(ctx, key, eventID, started)
}

// pointStateAtEvent estampa flow_state.event_id (D-043.4). Va DESPUÉS de arrancar el
// flujo porque startLocked escribe el estado desde cero y borraría el puntero si se
// estampara antes; el single-flight de la clave garantiza que nadie observa el hueco
// dentro del proceso.
//
// Sin flow_state (flujo vacío, o un arranque que no persistió) no hay dónde estampar
// y se LOGUEA: el evento existe y es rescatable por su tipo, que es lo que importa.
//
// El closeIfFinished de antes del Save es el cierre natural de un flujo DE UN SOLO
// PASO (T4.1): un evento cuyo flujo termina en el propio Enter (message sin next)
// llega aquí con el estado YA en el centinela — startLocked nunca ve el puntero
// (trabaja con un estado fresco), así que este es el primer sitio donde el evento y
// su final coinciden. Se estampa y se cierra en el MISMO Save: la fila queda closed
// y el puntero no sobrevive ni un turno apuntando a un muerto.
//
// Desde el Plan 053 · T1.5 se estampan LOS DOS punteros (REQ-053.1): el ACTIVO
// (event_id) y el DUEÑO (owner_event_id). El activo se estampa SIEMPRE; el dueño
// SOLO cuando `ownsFlow` lo autoriza.
//
// # POR QUÉ EL DUEÑO NO ES INCONDICIONAL (el camino de la tercera salida)
//
// enterEventFlow (events.go:543-567, justo arriba) tiene TRES salidas y solo DOS de
// ellas dejan un flujo recién nacido en la fila:
//
//   - kind == "menu" ⇒ se va por presentMenu y no llega aquí.
//   - flowID != "" ⇒ Delete + startLocked: el flujo de FlowID/FlowVersion/
//     CurrentNode/Vars acaba de nacer para ESTE evento y de nadie más. Aquí activo y
//     dueño valen lo mismo, y no es casualidad: es la definición del camino.
//   - flowID == "" y kind != "menu" ⇒ se cae hasta aquí SIN Delete y SIN startLocked,
//     así que el flow_state es HEREDADO de otro evento. Escribir el dueño ahí sería
//     mentir sobre de quién es ese flujo — el mismo daño exacto que saveMenuState
//     evita NO tocando el campo. Y el daño es real: cuando ese flujo heredado llegue
//     a su nodo terminal, closeIfFinished cerraría el evento EQUIVOCADO (el que
//     acabamos de estampar) y el dueño de verdad se quedaría `open` para siempre.
//
// Ese tercer camino NO es teórico: admin/triggers.go acepta una regla
// {kind: event_start, event_kind: "…", flow_id: ""} —no exige needsFlowID— y
// flowForKind devuelve "" sin error cuando no hay regla para el tipo. Lo alcanzan
// birthEvent y switchToEvent.
//
// El booleano viene del LLAMANTE y no se infiere de FlowID aquí: el único que sabe
// si la fila nació para este evento es quien la arrancó. Esta es, además, la pieza
// que SUSTITUYE a la guarda de posesión que T1.6 retiró de pendingClosure —pero por
// el lado correcto: en vez de adivinar la posesión comparando ev.FlowID con
// st.FlowID cuando ya es tarde, se deja de escribir un dueño falso desde el
// principio. No se reintroduce ninguna inferencia sobre FlowID.
func (rt *Runtime) pointStateAtEvent(ctx context.Context, key store.Key, eventID string, ownsFlow bool) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: releer el estado para apuntar al evento: %w", err)
	}
	if !ok {
		rt.log.Debug("runtime: sin flow_state donde apuntar el evento activo",
			"session_id", key.SessionID)
		return nil
	}
	// El ACTIVO es incondicional: la conversación le habla a eventID venga por donde
	// venga este camino. Eso nunca estuvo en duda.
	st.EventID = eventID
	// El DUEÑO solo si este camino ACABA de arrancar el flujo para este evento
	// (Delete + startLocked en enterEventFlow). Con flowID=="" y kind!="menu" se llega
	// aquí sobre un flow_state HEREDADO —la tercera salida, events.go:543-567— y
	// escribir el dueño ahí sería mentir sobre de quién es ese flujo: exactamente el
	// mismo daño que saveMenuState evita no tocando el campo.
	//
	// AQUÍ CON PERMISO Y EN saveMenuState NUNCA — y el contraste es la tarea entera
	// (Plan 053 · T1.5). saveMenuState (events.go, más abajo) es el ÚNICO camino que
	// estampa el puntero SIN pasar por aquí, y lo hace precisamente porque NO borra el
	// estado previo: cuando el `menu` se monta sobre un flujo VIVO, hereda
	// FlowID/CurrentNode/Vars de otro evento — escribir allí el dueño con el id del
	// `menu` sería mentir sobre de quién es ese flujo, y es exactamente el daño que
	// este plan viene a impedir (cerrar el menú se llevaba por delante el carrito,
	// hallazgo H2). Y en su otra rama —la que resetea la Conversation porque no había
	// estado o estaba terminal— el dueño nace "" solo, que también es lo correcto: un
	// menú puro no tiene flujo (D-043.3), luego no tiene dueño. Por eso saveMenuState
	// no se toca: el campo que no menciona es justo el que no le corresponde.
	//
	// Esta condición es lo que SUSTITUYE a la guarda de posesión retirada por T1.6
	// (`known && ev.FlowID != st.FlowID` en pendingClosure). Se sustituye por el
	// lado bueno —no escribiendo el dato falso, en vez de descontarlo después— y sin
	// reintroducir ninguna inferencia sobre FlowID: aquí no se lee FlowID para nada.
	if ownsFlow {
		st.OwnerEventID = eventID
	}
	// El orden NO es cosmético: el dueño queda escrito ANTES de closeIfFinished
	// porque desde T1.6 el cierre natural decide MIRANDO al dueño, no al activo.
	rt.closeIfFinished(ctx, &st)
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: apuntar el evento activo: %w", err)
	}
	return nil
}

// stopEvent DESACTIVA el evento activo sin matarlo (Plan 043 · T2.4, D-043.2): apaga
// flow_state.event_id y deja la fila en `open`, rescatable diciendo su tipo.
//
// Lo que NO hace, y es la tarea entera: no llama a TransitionEvent, no toca `status`
// y no borra el flow_state. `event_stop` no es el escape global —ese sigue con su
// semántica EXACTA en handleEscape, borrando el estado— sino dejar de atender un
// evento. El resumen que acompaña a la confirmación lo conecta la Ola 3.
//
// La confirmación nombra el TIPO, nunca el history_id (E-3).
//
// ⚠️ «Deja la fila en `open`» es cierto de ESTE gesto, no para siempre: el puntero
// DUEÑO (owner_event_id, Plan 053) sobrevive al stop a propósito, así que si el
// contacto termina el flujo que queda cargado, closeIfFinished cerrará ese evento. El
// porqué está entero en el bloque de ratificación junto al `st.EventID = ""`.
func (rt *Runtime) stopEvent(ctx context.Context, key store.Key, sessionID string, st model.Conversation) error {
	if st.EventID == "" {
		// Nada que desactivar: el turno se consumió igual (el cliente dijo la palabra),
		// pero no hay estado que escribir ni nada que confirmar.
		return nil
	}
	ev, known := rt.activeEvent(ctx, key, sessionID, st.EventID)
	kind := ""
	if known {
		kind = ev.Kind
	}
	// `event_stop` es un abandono declarado por el cliente: se resume ANTES de apagar el
	// puntero, mientras las Vars todavía dicen dónde se había quedado (T3.4).
	rt.summarizeAbandoned(ctx, key, sessionID, st, "")
	// 🔴 SE APAGA EL ACTIVO Y **NO** SE TOCA EL DUEÑO (st.OwnerEventID). No es un olvido
	// de simetría del Plan 053 · Ola 1: es la conducta ratificada por Jhoan el
	// 2026-08-19, y quien la "arregle" por parecerle incoherente rompe el test que la
	// fija (abajo).
	//
	// POR QUÉ EL DUEÑO SE CONSERVA. stopEvent DESACTIVA el evento; NO destruye el flujo.
	// FlowID, FlowVersion, CurrentNode y Vars sobreviven intactos en esta misma fila —es
	// la diferencia exacta con handleEscape, que hace Delete, y por eso el efecto de aquí
	// se llama event_deactivated y el de allí event_escaped (event_effects.go)—. Ese
	// flujo que sigue cargado SIGUE SIENDO del evento que acabamos de desactivar: borrar
	// el dueño aquí declararía huérfano un flujo con padre conocido, que es justo la
	// mentira que T1.5 se cuida de no escribir en pointStateAtEvent.
	//
	// LA CONSECUENCIA, DICHA ENTERA. La fila queda {flow=F, event="", owner=C}. El
	// contacto puede seguir avanzando F (advanceLiveStep no exige evento activo), y si F
	// alcanza el centinela, closeIfFinished transiciona AL DUEÑO ⇒ **C se cierra** con su
	// event_closed, aunque el cliente dijera «déjalo» un turno antes.
	//
	// ANTES DEL PLAN 053 no ocurría, y lo que había era PEOR: pendingClosure entraba por
	// `st.EventID == ""`, así que con el activo apagado no cerraba NADA y C se quedaba
	// `open` PARA SIEMPRE —un evento huérfano con su intake colgando, que ya nadie iba a
	// matar por esta vía—. Cerrarlo es coherente con D-043.5 («closed» es el fin NATURAL
	// del flujo, y el flujo de C terminó de verdad): se prefiere un evento cerrado a un
	// huérfano inmortal. Es una elección, no un daño colateral.
	//
	// ⚠️ COSTURA CONOCIDA Y ACEPTADA, NO REPARADA AQUÍ: el copy de stopNotice (justo
	// debajo, «Sigue abierto: puedes retomarlo cuando quieras») puede quedar DESALINEADO
	// con ese cierre —es verdad en el momento en que se dice, y deja de serlo si el
	// contacto termina F—. El texto NO se toca en esta ola. Si algún día se ajusta el
	// copy, ESTE comentario es el sitio que explica por qué.
	//
	// Lo fija TestCloseIfFinished_TrasEventStop_ElDuenoSobreviveYCierraElEvento
	// (event_lifecycle_owner_test.go).
	st.EventID = ""
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: desactivar el evento activo: %w", err)
	}
	// El hecho está sellado (el puntero está apagado en BD) ⇒ se registra. Va ANTES
	// del replyAllowed a propósito: que la red anti-loop impida CONTARLO al cliente no
	// significa que no haya pasado.
	if known {
		rt.emitEventEffect(ctx, ev, EffectEventDeactivated)
	}
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destination(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	notice := stopNotice(kind)
	if _, err = rt.send(ctx, sessionID, to, key, []engine.Output{{Text: notice}}); err != nil {
		return err
	}
	// Saliente FUERA DE TURNO (Plan 044 · T1.6, D-044.24): la confirmación del
	// `event_stop` la escribimos nosotros y no responde a un pedido. `ev` es el
	// evento que se acaba de desactivar y sigue siendo el dueño del hilo —el puntero
	// se apagó, el evento no se cerró—, así que el aviso cuelga de él.
	rt.persistOutOfTurnMessage(ctx, key.TenantID, sessionID, ev.ID, notice)
	return nil
}

// stopNotice arma la confirmación de event_stop. Nombra el TIPO del evento que se
// deja de atender y JAMÁS su history_id (E-3): el identificador legible es para la
// bandeja del negocio, no para la conversación.
//
// El nombre sale de events.KindName —«carrito», «encuesta», «documentos»—, el MISMO
// que usa el despachador para rotular sus opciones: si el menú ofrece «Retomar el
// pedido» y la confirmación hablara de un «cart», serían dos vocabularios para el
// mismo objeto en la misma conversación. Sin tipo conocido cae a una frase genérica
// antes que mentir con un nombre.
func stopNotice(kind string) string {
	if kind == "" {
		return "Listo, lo dejamos aquí. Sigue abierto por si quieres retomarlo."
	}
	// No se le promete NINGUNA palabra concreta para volver: la que sirve es la que el
	// tenant configuró en su regla event_start, que puede no coincidir con el nombre
	// de cara al cliente (hoy el tipo `cart` se llama «pedido» y la palabra puede ser
	// «carrito»). Prometer una palabra que quizá no dispare nada sería peor que no
	// prometer ninguna.
	return fmt.Sprintf("Listo, dejamos el %s por ahora. Sigue abierto: puedes retomarlo cuando quieras.", events.KindName(kind))
}
