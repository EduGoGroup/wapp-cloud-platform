// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Trozo de incoming.go (E-13): el DISPARO —un entrante sin conversación viva (incoming.go
// §3)—: la decisión del resolver, la oferta de entrada, las puertas del nacimiento tardío, el
// arranque plano y su degradación (RT-9b), y la señal que se le pasa al resolver. Solo se
// movieron declaraciones.

// handleTrigger resuelve un entrante SIN conversación viva contra el trigger.Resolver
// (Plan 019 · T3). Con el resolver por defecto (Noop) devuelve Ignore ⇒ return nil,
// idéntico a la decisión C histórica (INV-6). Un error del resolver se LOGUEA y NO
// aborta la recepción (REQ-A7: el entrante simplemente se ignora). Ante Start/Fallback
// arranca el flujo por startLocked (el keyedMutex de la clave YA está tomado por
// HandleIncoming; llamar a Start re-tomaría el mutex y causaría auto-deadlock). Un
// ErrConversationExists (carrera con otro entrante) se trata como benigno (log + nil).
// La señal ya viene construida por el llamante (buildSignal, con el gate de
// entitlements aplicado); handleTrigger solo la resuelve.
//
// ⚠️ `m` (el ENTRANTE EN CRUDO) viaja además de `sig`, y no es redundante (Plan 044 ·
// Ola 1, corrección del 2026-08-22). `sig` lleva el TEXTO y la intención ya gateada;
// la VENTANA DE CAPTACIÓN necesita otras tres cosas que `trigger.Signal` no tiene y no
// debe aprender: el `wa_message_id` (que es literalmente lo que entra en `source_refs`),
// el `ts_unix` del cliente (la base de fechas, D-044.9) y la intención SIN el gate de
// `llm_intent` (el derecho que abre la ventana es `llm_intake`, ver
// observeForAggregation). Se pasa el mensaje entero en vez de tres parámetros sueltos
// porque es exactamente lo que observeForAggregation ya recibe desde advanceLiveStep, y
// tener dos formas de llamar al mismo puente sería la primera grieta por la que los dos
// caminos divergen.
func (rt *Runtime) handleTrigger(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, sig trigger.Signal, m *cloudlinkv1.IncomingMessage) error {
	dec, err := rt.triggers.Resolve(ctx, tenantID, sessionID, sig)
	if err != nil {
		rt.log.Warn("runtime: resolver de disparos falló; se ignora el entrante",
			"error", err, "session_id", sessionID)
		return nil
	}
	switch dec.Action {
	case trigger.StartEvent, trigger.Start:
		return rt.startFromDecision(ctx, tenantID, sessionID, key, contactID, dec, m)
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
//
// # 🔴 AQUÍ ENTRA A LA VENTANA EL MENSAJE QUE ARRANCA EL EVENTO (Plan 044, 2026-08-22)
//
// Y este es EL punto, no una elección de comodidad. La ventana de captación exige un
// `event_id` —`intake_jobs.event_id` es NOT NULL y la fuente del literal (el hilo)
// cuelga del evento—, así que la llamada no puede ir ni un paso antes:
//
//   - en `HandleIncoming`, cuando se decide llamar a handleTrigger, NO HAY evento: o
//     no había `flow_state` (el limbo) o el reloj acaba de soltar la conversación;
//   - en `handleTrigger`, tampoco: ahí solo se ha resuelto la DECISIÓN del resolver, y
//     una `trigger.Decision` trae un `event_kind`, jamás un id — el evento todavía no
//     existe en la base;
//   - el id NACE dentro de `beginEvent` → `birthEvent` (`events.CreateEvent`, el
//     `INSERT`) o se RECUPERA en `switchToEvent` (`ev.ID`, el evento vivo al que se
//     conmuta). Esas dos son las únicas puertas, y las dos desembocan en el mismo
//     `beginEvent`.
//
// Por eso `beginEvent` DEVUELVE ese id (cambio de firma de esta corrección) en vez de
// que el agregador se llame desde dentro: `birthEvent`/`switchToEvent` no tienen —ni
// deben tener— el entrante en la mano, y bajarles `m` por tres firmas para escribir una
// fila sería meter el 044 en el plano de eventos del 043. Se sube el id, que es un
// `string`, en lugar de bajar el mensaje.
//
// EL PRESUPUESTO ES EL MISMO QUE EN advanceLiveStep, y por construcción: se llama al
// MISMO `observeForAggregation`, que es best-effort integral (no devuelve error, jamás
// corta el turno, no-op exacto sin `WithAggregator`) y cuyo coste es 1 escritura y 0
// lecturas (D-044.26). No se añade ni una guarda nueva: las de `Observe` —clave
// completa, `wa_message_id`, gate `llm_intake` fail-closed— gobiernan igual aquí.
//
// EL DEFECTO QUE CIERRA, dicho con el caso: «quiero presupuesto de 20 hamburguesas»
// abre el evento por esta puerta y **no llegaba a `source_refs`**. La ventana la abría
// el mensaje SIGUIENTE (el primero que pasa por `advanceLiveStep`), así que el
// `message_ts` —la BASE DE FECHAS del presupuesto, D-044.9— quedaba anclado al segundo
// mensaje y «para el jueves» se resolvía contra el instante equivocado.
//
// ⚠️ NO PUEDE DOBLE-CONTAR con advanceLiveStep: por un entrante corre este camino O
// aquél, nunca los dos (HandleIncoming enruta a handleTrigger *o* a advanceLive, y las
// dos sueltas que reentran aquí —releaseFinishedState y releaseOrphanMenu— hacen
// `return` sin pasar por el Step). Y si algún día se solaparan, `alreadySeen` descarta
// el mismo `wa_message_id` sobre la misma ventana.
func (rt *Runtime) startFromDecision(ctx context.Context, tenantID, sessionID string, key store.Key, contactID string, dec trigger.Decision, m *cloudlinkv1.IncomingMessage) error {
	if dec.Action != trigger.Fallback {
		// `openedBy(m)` es EL TURNO DE APERTURA (Plan 044 · T1.4, corrección del
		// 2026-08-22): baja hasta startLocked para que el literal con el que el cliente
		// abrió el evento —«quiero presupuesto de 20 hamburguesas»— deje su fila en el
		// hilo, junto a las salidas del arranque y en ese orden.
		//
		// 🔴 ES LA OTRA MITAD DE observeForAggregation, Y VAN JUNTAS A PROPÓSITO. La
		// ventana guarda la REFERENCIA del mensaje (`source_refs`) y ancla en él la base
		// de fechas; el hilo guarda su LITERAL, y es de ahí de donde el compositor de
		// T1.4 saca el `source_text`. Hasta esta corrección solo existía la primera: la
		// referencia apuntaba a un mensaje cuyo texto no estaba escrito en ninguna parte,
		// así que el `source_text` empezaba por el SEGUNDO mensaje de la ráfaga, sin
		// error y sin que nadie lo notara. Si algún día se retira una, hay que retirar la
		// otra — dejar la referencia sin literal es el defecto peor de los dos.
		//
		// Baja el TEXTO y no el mensaje entero: ver openingTurn (start.go).
		eventID, consumed, err := rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, "", openedBy(m))
		if err != nil {
			return err
		}
		if consumed {
			// LA VENTANA DE CAPTACIÓN (Plan 044 · Ola 1). `eventID` es "" en los caminos
			// de beginEvent que consumen el turno SIN dejar evento vivo en la mano (el
			// cupo anti-loop agotado, la carrera benigna del ErrAliveExists) Y —desde el
			// 2026-08-22— en el ARRANQUE CORTADO por el sink durable, donde el evento
			// existe pero su turno no llegó a escribir hilo. En los tres, `Observe` no
			// escribe nada: `intake.WindowKey.Valid()` es false sin `event_id`. Es la
			// MISMA guarda que ya protege a advanceLiveStep, no una excepción escrita
			// para este camino.
			//
			// 🔴 LA INVARIANTE, en una línea, para que el siguiente no la rompa: TODO
			// MENSAJE QUE ENTRA EN `source_refs` TIENE SU LITERAL EN EL HILO — si el
			// turno se cortó y su texto no se escribió, la referencia tampoco existe.
			rt.observeForAggregation(ctx, tenantID, sessionID, contactID, eventID, m)
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
	// openingTurn{}: el arranque PLANO no pare evento (eventID "") y sin evento no hay
	// hilo donde escribir — threadAllowed ya lo cerraría igual. Se pasa el valor cero
	// para decirlo en la llamada y no depender de esa coincidencia (Plan 044 · T1.4).
	// El bool del medio —EL TURNO CORTADO por el sink durable— se DESCARTA aquí y es lo
	// correcto: este camino arranca SIN evento (el "" de la firma), así que ni escribe
	// hilo ni observa la ventana de captación, y no hay pareja que pueda descuadrar. La
	// invariante del Plan 044 solo tiene algo que decir donde hay `event_id`, que es el
	// camino de enterEventFlow.
	if _, _, serr := rt.startLocked(ctx, tenantID, dec.FlowID, sessionID, key, contactID, "", dec.Params, dec.IntentName,
		rt.taglineFor(ctx, tenantID, sessionID, contactID, dec.IntentName), openingTurn{}); serr != nil {
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
// origin sale de dec.Action, lo único que trigger.Decision trae en la mano aquí:
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
	origin := "fallback"
	switch dec.Action {
	case trigger.Start:
		origin = "keyword"
	case trigger.StartEvent:
		origin = "event"
	}
	nodeType := ""
	if def, derr := rt.store.LatestDefinition(ctx, tenantID, dec.FlowID); derr == nil {
		nodeType = rt.engine.DurableNodeType(def)
	}
	rt.log.Warn("runtime: flujo con contenido durable no puede arrancar sin evento; se degrada a la oferta del despachador",
		"flow_id", dec.FlowID, "node_type", nodeType, "origin", origin, "session_id", sessionID)
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
// texto crudo del entrante y el tipo del evento activo.
//
// # 🔴 AQUÍ VIVÍA LA INTENCIÓN LLM, Y SE FUE CON EL PUSH (T1.6-4, D-044.31)
//
// Hasta la Ola 1.6 esta función leía `m.GetIntent()` —la etiqueta que el Edge sellaba
// en `SensitivePayload.intent` y el gateway copiaba a `IncomingMessage.intent`—, la
// filtraba por la feature `llm_intent` (ADR-0022) y la metía en `sig.Intent`. T1.6-1
// retiró `ClassifiedIntent` del contrato: los dos campos quedaron `reserved` y la
// llamada dejó de compilar. No hay de dónde leerla.
//
// **CONSECUENCIA, DICHA CON TODAS LAS LETRAS PORQUE NO ES UN DETALLE DE ESTA FUNCIÓN:
// `sig.Intent` ES HOY SIEMPRE nil, así que las reglas `kind='llm'` (Plan 029 · T7) NO
// PUEDEN DISPARAR.** El resolver conserva su rama —`config_resolver.go`, `if
// sig.Intent != nil`— y sus tests, pero en producción nadie la alcanza. La rama NO se
// retira aquí a propósito: es un frente de producto con dueño propio, no un residuo
// de esta tarea, y el sustituto natural (pedir la clasificación) no cabe en el camino
// del disparo — una decisión de arranque se toma EN EL TURNO y una inferencia tarda
// segundos (p50 medido: 8,1 s), que es exactamente lo que REQ-35 prohíbe esperar.
//
// El único consumidor de una clasificación pedida es hoy el adelanto de la ventana de
// captación (`intakeahead` → `IntakeAggregator.OnClassified`), que puede permitírselo
// porque NO corre en el turno: adelanta un cierre que iba a ocurrir igual.
//
// Los dos primeros parámetros quedan sin usar y NO se retiran: los cuatro llamantes
// pasan ya el tenant y el ctx, la firma es la que documenta el contrato del resolver,
// y quitarlos ahora obligaría a reponerlos el día que la señal vuelva a tener una
// fuente. Ese día se cambia el cuerpo, no las llamadas.
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
func (rt *Runtime) buildSignal(_ context.Context, _ string, m *cloudlinkv1.IncomingMessage, activeEventKind string) trigger.Signal {
	return trigger.Signal{Text: m.GetText(), ActiveEventKind: activeEventKind}
}
