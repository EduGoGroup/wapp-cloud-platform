package runtime

import (
	"context"
	"errors"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva
// para la clave (T3 lo mapea a HTTP 409). Se inspecciona con errors.Is.
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

// ErrDurableFlowNeedsEvent lo devuelve startLocked cuando el flujo exige un evento
// padre —algún nodo resuelve, vía el Registry, a un módulo con
// ProducesDurableContent()==true (cart, survey)— y la puerta por la que se intenta
// arrancar no trae uno (eventID == "": API, keyword, fallback; design.md D-054.5).
// Sin esta guarda el flujo arranca igual y revienta varios turnos después contra el
// NOT NULL de intakes.event_id/survey_results.event_id (migración 0054), silenciado
// por el fan-out best-effort del ADR-0003: el pedido se pierde SIN que nada lo avise
// (hallazgos #001/#003 de docs/runbooks/bitacora-errores-uat.md).
//
// La CONSECUENCIA la decide cada llamante, nunca este paquete (el motor no conoce
// HTTP ni el despachador):
//   - startPlainFlow (incoming.go, Plan 054 · T2.4) lo traduce en DEGRADAR a la
//     oferta del despachador: el cliente de WhatsApp jamás ve un error, solo lo que
//     puede hacer.
//   - writeStartError en internal/flujos/admin y internal/publicapi (T2.5) lo
//     traduce en 409, con un motivo distinto del 409 de ErrConversationExists.
//
// Se inspecciona con errors.Is (mismo patrón que ErrConversationExists).
var ErrDurableFlowNeedsEvent = errors.New("el flujo exige un evento padre para arrancar por esta puerta")

// openingTurn es EL ENTRANTE QUE ABRIÓ EL EVENTO, viajando hasta startLocked —el
// único embudo por el que pasan las cuatro puertas de arranque— para que su literal
// deje fila en el hilo (Plan 044 · T1.4, corrección del 2026-08-22).
//
// # EL DEFECTO QUE CIERRA, DICHO CON EL CASO
//
// «quiero presupuesto de 20 hamburguesas» abre el evento por el camino del
// disparador (handleTrigger → startFromDecision → beginEvent) y NO DEJABA UNA SOLA
// FILA en `conversation_event_messages`: persistTurnMessages tenía UN llamante,
// advanceLiveStep, que es el turno de una conversación YA VIVA. Desde el 2026-08-22
// ese mismo mensaje SÍ entra en `intake_jobs.source_refs` (observeForAggregation en
// startFromDecision), así que la referencia existía y el literal no: el compositor
// de T1.4 leía el hilo, no encontraba el primer mensaje de la ráfaga y componía un
// `source_text` que EMPIEZA POR EL SEGUNDO, sin error y sin que nadie lo notara. Es
// exactamente el accidente que advanceLiveStep documenta al dejar fuera de la
// ventana el turno cortado por el sink durable, visto por el otro lado.
//
// # POR QUÉ BAJA EL TEXTO EN VEZ DE SUBIR LAS SALIDAS
//
// Se sopesaron las dos y esta gana por UNA razón concreta: EL ORDEN. El hilo tiene
// que quedar «entrante primero, salidas después» (es el orden de la conversación, y
// `listThreadSQL` lo devuelve por `seq`), y las salidas del arranque NACEN dentro de
// startLocked. Escribiendo desde aquí, cliente y negocio salen del MISMO sitio y en
// el mismo acto: no hay forma de que se intercalen. Subiendo las salidas hasta
// startFromDecision el resultado sería idéntico pero repartido en dos capas, y el
// día que alguien meta una escritura intermedia el orden se rompe en silencio.
//
// ⚠️ NO ES `*cloudlinkv1.IncomingMessage`, y eso es deliberado: beginEvent /
// birthEvent / enterEventFlow son el plano de EVENTOS del Plan 043 y no deben
// aprender la forma del entrante (es el mismo argumento con el que el `event_id`
// SUBE en vez de bajar el mensaje — ver startFromDecision). Lo que baja es un
// literal y un booleano.
//
// 🔴 HAY UN SEGUNDO LECTOR, Y ES UNA EXCEPCIÓN ACOTADA: la rama de CONMUTA de
// beginEvent (events.go). Cuando el disparador no pare evento sino que salta a uno
// vivo, ese entrante entra igual en la ventana de captación, así que su literal
// TAMBIÉN tiene que estar en el hilo — pero SIN las salidas, porque el evento al que
// se conmuta ya tiene su nodo inicial escrito. Allí se llama directamente a
// persistTurnMessages con `nil` en las salidas, y el porqué está escrito en el sitio.
type openingTurn struct {
	// Text es lo que el cliente escribió. Puede ser "" —un adjunto sin caption abre
	// el evento igual— y entonces NO se escribe fila de cliente, exactamente la misma
	// poda que hace persistTurnMessages con su clientText.
	Text string
	// FromClient distingue «este arranque lo disparó un entrante» de «este arranque
	// no tiene turno detrás». NO se puede deducir de Text=="" : un media sin caption
	// es lo primero con el texto vacío, y confundirlos dejaría las salidas de ese
	// arranque fuera del hilo. El valor CERO de la struct (FromClient=false) es «no
	// hay turno de apertura», que es lo que pasan las puertas sin entrante: Start
	// (API/admin), startPlainFlow (keyword/fallback, que además va sin evento) y
	// switchToEvent (la CONMUTA — ver su llamada en events.go).
	FromClient bool
}

// openedBy construye el turno de apertura a partir del entrante que lo disparó. Se
// escribe una vez para que ningún llamante pueda olvidarse de poner FromClient.
func openedBy(m *cloudlinkv1.IncomingMessage) openingTurn {
	return openingTurn{Text: m.GetText(), FromClient: true}
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
	// Arranque por API (admin / /api/v1/.../start): sin intención LLM ⇒ sin params
	// (el pre-carga del carrito solo aplica al arranque por decisión llm, T8).
	// Sin coletilla: este arranque no viene de una intención inferida (T3.8 punto 2).
	// Sin evento: el arranque por API no pare fila en conversation_events (E-6).
	// Sin turno de apertura (openingTurn{}): aquí NO hay entrante — un humano pulsó un
	// botón del admin. No hay literal de cliente que llevar al hilo, y sin evento
	// tampoco habría hilo donde ponerlo (Plan 044 · T1.4).
	//
	// El segundo valor —EL TURNO CORTADO por el sink durable— se DESCARTA aquí, y es lo
	// correcto: existe para que nadie meta en la ventana de captación un mensaje cuyo
	// literal no se escribió, y este camino no trae entrante NI evento (el "" de la
	// firma), así que no hay ventana ni hilo que puedan descuadrar. La respuesta a la
	// API sigue siendo byte a byte la de siempre: el Ack del aviso, sin error. Quien SÍ
	// lo mira es enterEventFlow (events.go).
	ack, _, err := rt.startLocked(ctx, tenantID, flowID, sessionID, key, contactID, "", nil, "", "", openingTurn{})
	return ack, err
}

// startLocked es el cuerpo de Start SIN tomar el keyedMutex: asume que el llamante
// YA lo tiene tomado sobre `key`, con el contact_id ya resuelto. Lo comparten Start
// (API /admin/flows/start, /api/v1/.../start — toma el mutex y delega) y el enganche
// por palabra clave de HandleIncoming (Plan 019 · T3), que YA tomó el mutex sobre la
// misma clave: re-llamar a Start ahí causaría un auto-deadlock. Reglas de arranque
// (guard 409, reinicio de carrito, orden Save-antes-de-Send) son idénticas.
//
// `tagline` es la coletilla YA RESUELTA que se pega al final de la respuesta (T3.8
// punto 2). Llega resuelta a propósito: startLocked es el camino por el que pasa TODO
// arranque —API, keyword, fallback, evento—, así que si decidiera aquí si toca
// emitirla, esa decisión afectaría a caminos que no tienen nada que ver con la
// intención. Recibiéndola, los demás llamantes pasan "" y siguen byte a byte igual.
//
// `eventID` es el id del evento conversacional al que pertenece este arranque
// (Plan 043 · Ola 4.5 · T4.5.1, D-043.21), o "" en los caminos sin evento (API,
// keyword, fallback). ⚠️ Llega por parámetro y NO puede leerse de st.EventID: en este
// camino st nace fresco —siempre, desde que la Ola 7 retiró el reinicio— y el puntero
// flow_state.event_id lo estampa pointStateAtEvent DESPUÉS de arrancar (events.go,
// enterEventFlow), así que leerlo aquí daría SIEMPRE "". Quien tiene el evento recién
// nacido/conmutado en la mano es el llamante, y es él quien lo pasa para que los
// efectos del pre-carga (p. ej. item_added del Prime del carrito) lleguen al proyector
// declarando a su padre.
//
// 📌 Este párrafo dijo lo contrario entre la Ola 6 y la Ola 7 del Plan 053, y no por
// error: mientras existió el reinicio por Start, st PODÍA nacer con los punteros
// heredados de la fila que pisaba. Al retirarse esa rama, «st nace fresco» vuelve a ser
// cierto sin excepciones.
//
// `opening` es EL ENTRANTE QUE ABRIÓ EL EVENTO (Plan 044 · T1.4, 2026-08-22), o el
// valor cero cuando este arranque no nace de un turno del cliente. Es lo que permite
// que el literal del disparador —el «quiero presupuesto de X»— deje su fila en el
// hilo, que hasta esa fecha NO ocurría por ningún camino. Ver openingTurn y
// persistOpeningTurn, aquí abajo.
//
// # EL SEGUNDO VALOR: «ESTE TURNO SE CORTÓ» (Plan 044, corrección del 2026-08-22)
//
// Vale true —y solo true— cuando el arranque se abortó porque el sink que MATERIALIZA
// contenido durable agotó su reintento acotado (D-054.4): el cliente recibió el aviso,
// el turno se consumió, `err` es nil y NO se guardó estado NI se escribió una sola fila
// de hilo. Es un HECHO del turno, no un fallo del arranque, y por eso viaja como bool y
// no como error: convertirlo en error cambiaría la respuesta de la API (Start) y la del
// disparo plano (startPlainFlow), que hoy son un éxito silencioso a propósito.
//
// 🔴 LA INVARIANTE QUE OBLIGA A DEVOLVERLO, dicha entera: TODO MENSAJE QUE ENTRA EN
// `source_refs` TIENE SU LITERAL EN EL HILO. Hasta esta corrección, un arranque cortado
// aquí volvía sin escribir hilo —el `return` de la rama del corte va ANTES de
// persistOpeningTurn— pero startFromDecision observaba igual: tenía el `event_id` en la
// mano y recibía `consumed=true, err=nil`, así que metía en la ventana un mensaje cuyo
// texto no estaba escrito en ninguna parte. El compositor de T1.4 leería el hilo, no lo
// encontraría, y compondría un `source_text` INCOMPLETO sin error y sin que nadie lo
// notara. Es EXACTAMENTE el mismo corte que ya hace advanceLiveStep (incoming.go:
// «tampoco entra en la ventana de captación»), visto por el lado del arranque: si el
// turno se cortó, no se observa.
//
// El bool SUBE por enterEventFlow hasta birthEvent, que lo traduce en el `event_id` ""
// con el que `Observe` descarta sola la ventana (WindowKey.Valid() exige event_id).
func (rt *Runtime) startLocked(ctx context.Context, tenantID, flowID, sessionID string, key store.Key, contactID, eventID string, intentParams map[string]string, intentName, tagline string, opening openingTurn) (*cloudlinkv1.Ack, bool, error) {
	def, err := rt.store.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		return nil, false, fmt.Errorf("runtime: definición vigente: %w", err)
	}

	// Guarda D-054.5 (Plan 054 · T2.3): ningún flujo con contenido durable arranca
	// sin evento padre. La condición es el PARÁMETRO eventID, NUNCA st.EventID —st
	// ni siquiera existe todavía, y el comentario de la firma de arriba ya avisa de
	// que en este camino (API/keyword/fallback) eventID llega SIEMPRE ""—. Va JUSTO
	// AQUÍ, tras cargar def y ANTES de rt.store.Exists/EnterPrimed/Save: un rechazo
	// no deja rastro — cero flow_state, cero efectos, cero envío (REQ-054.2).
	if eventID == "" && rt.engine.FlowProducesDurableContent(def) {
		return nil, false, ErrDurableFlowNeedsEvent
	}

	exists, err := rt.store.Exists(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("runtime: comprobar existencia: %w", err)
	}
	// 409 INCONDICIONAL (Plan 053 · Ola 7). Aquí hubo, desde el Plan 027 · T8, un
	// «gotcha de reinicio»: si el nodo inicial tenía ResumePolicy y esa política decía
	// que el estado guardado estaba terminal, el Start REINICIABA la conversación en
	// vez de rechazarla. Se retiró entera —con restartableOnStart y ownerFlowMismatch—
	// porque llevaba sin poder ejecutarse desde el Plan 054 · T2.3, y las razones están
	// abajo por si alguien viene a reponerla.
	//
	// POR QUÉ NO PODÍA EJECUTARSE (la cadena, verificada atacándola por el lado
	// contrario y no por el nombre de las funciones):
	//   1. Solo se reinicia si hay ResumePolicy para el nodo INICIAL. La única
	//      registrada en producción es la del carrito (bootstrap.go) ⇒ el nodo inicial
	//      tiene que ser cart.
	//   2. cart se registra INCONDICIONALMENTE y su ProducesDurableContent() devuelve
	//      true a secas (cart.go) ⇒ todo flujo con nodo inicial cart es durable.
	//   3. Un flujo durable sin evento muere ARRIBA, en la guarda D-054.5, que va antes
	//      de rt.store.Exists ⇒ no se llega aquí.
	//   4. La única puerta que trae evento es enterEventFlow, y hace Delete(key) justo
	//      antes ⇒ exists es false y el bloque ni se evaluaba.
	// La condición que hacía falta —un módulo con ResumePolicy y NO durable— no la
	// cumple ninguno.
	//
	// Y LA DECISIÓN DE PRODUCTO YA ESTABA TOMADA, que es lo que convierte esto en
	// limpieza y no en un cambio de conducta: D-054.5 (design.md §4 del Plan 054)
	// eligió que un Start sobre un flujo durable avise con 409 ANTES de tocar la base
	// en vez de dejar la bandeja vacía horas después; el operador abre por la puerta de
	// eventos (event_start), no por /admin/flows/start. Lo fijan
	// TestCartStart_SiempreExigeEvento_ElGotchaDeReinicioQuedaSuperado y
	// TestCartStart_ConPedidoVencido_SigueDando409, que ya se escribieron con la
	// aserción invertida.
	//
	// SI ALGÚN DÍA SE REGISTRA UNA SEGUNDA ResumePolicy sobre un módulo no durable,
	// esta es la línea a revisar — y la pregunta a contestar primero es de producto, no
	// de código: ¿debe un /start por API poder reiniciar una conversación en curso?
	// Para el carrito y la encuesta la respuesta ya es NO.
	//
	// ⚠️ NO CONFUNDIR CON prepareResume (resume.go), que sigue VIVO y es el reinicio
	// por el camino del ENTRANTE: el cliente escribe, su pedido anterior ya terminó, y
	// el flujo arranca de nuevo. Comparten el mapa rt.resumePolicies; lo que sobraba
	// era ESTE consumidor, no el mecanismo.
	if exists {
		return nil, false, ErrConversationExists
	}

	st := model.Conversation{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	// Params iniciales (Plan 029 · T8): al arrancar por decisión llm se siembran los
	// intent_params en Vars ANTES del primer paso, para que un módulo pre-cargue el
	// flujo (p. ej. el carrito con el producto pedido). EnterPrimed consulta la
	// capacidad Primer del nodo inicial; sin params sembrados equivale a Enter.
	seedIntentParams(&st, intentParams, intentName)
	st, outs, effects, err := rt.engine.EnterPrimed(ctx, def, st)
	if err != nil {
		return nil, false, fmt.Errorf("runtime: enter: %w", err)
	}
	// La coletilla se pega ANTES de guardar para que la marca de «ya se ofreció» viaje
	// en el MISMO Save que el estado inicial: si se marcara después, un fallo entre
	// medias dejaría una conversación que ya la vio y no lo recuerda.
	outs, ofrecida := appendTagline(outs, tagline)
	if ofrecida {
		markTaglineOffered(&st)
	}
	// Efectos DECLARADOS por el pre-add del módulo (p. ej. item_added del carrito):
	// mismo fan-out EN PROCESO que HandleIncoming, pero ANTES del Save (Plan 054 ·
	// T3, MD-054.1 opción (a)): si el sink que MATERIALIZA contenido durable agota
	// su reintento acotado (D-054.4), el arranque se corta AQUÍ —nada se ha
	// guardado todavía, así que no hay nada que revertir— y el cliente recibe el
	// aviso en vez del render normal del nodo inicial.
	if len(effects) > 0 {
		// EventID sale del PARÁMETRO, no de st.EventID: aquí st.EventID es SIEMPRE ""
		// a propósito (pointStateAtEvent estampa el puntero después de este camino).
		// Ver el comentario de la firma. Durable sale del ÚNICO nodo que pudo producir
		// estos efectos: el inicial (def.Initial) — el pre-carga de EnterPrimed solo
		// dispara sobre ese nodo (engine.tryPrime).
		ec := EffectContext{
			TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID,
			FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: eventID,
			Durable: rt.engine.NodeProducesDurableContent(def.Nodes[def.Initial].Type),
		}
		if cutErr := rt.dispatch(ctx, ec, effects, sessionID); cutErr != nil {
			rt.log.Error("runtime: arranque cortado: el sink durable no pudo materializar el pre-carga tras el reintento acotado",
				"error", cutErr, "tenant_id", tenantID, "flow_id", flowID)
			// El `true` de las DOS salidas de aquí abajo es EL TURNO CORTADO (ver la
			// cabecera): desde este punto el arranque NO guarda estado y NO escribe hilo,
			// así que el mensaje que lo disparó NO puede entrar en `source_refs`. Se dice
			// también en el camino de error del destino, aunque allí el error ya corte por
			// su cuenta: el hecho es el mismo y mentir sobre él para ahorrar una palabra
			// sería sembrar la próxima referencia colgando.
			to, derr := rt.destino(ctx, tenantID, contactID)
			if derr != nil {
				return nil, true, derr
			}
			ack, serr := rt.send(ctx, sessionID, to, key, []engine.Output{{Text: defaultDurableSinkFailureNotice}})
			return ack, true, serr
		}
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, false, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	// EL HILO LITERAL DEL TURNO DE APERTURA (Plan 044 · T1.4). Va EXACTAMENTE donde
	// advanceLiveStep pone el suyo: después del Save y antes del envío. Las dos razones
	// son las mismas que allí — se persiste lo que el motor PRODUJO (aunque el envío
	// luego falle o el rate-limit lo calle: el hilo cuenta el turno del motor, no la
	// entrega) y el corte por sink durable de arriba ya devolvió, así que un arranque
	// revertido NO deja rastro en el hilo, igual que no entra en la ventana.
	//
	// `outs` lleva YA la coletilla pegada (appendTagline, más arriba): es texto que el
	// cliente lee, así que es texto del hilo.
	rt.persistOpeningTurn(ctx, tenantID, sessionID, eventID, opening, outs)
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return nil, false, err
	}
	// Cuenta la racha por `key`, que es la MISMA clave que usa el llamante (Start la
	// clava y startLocked la recibe): el arranque es la primera auto-respuesta del
	// episodio. ⚠️ Este punto lo recorren las DOS puertas —el arranque por API/admin
	// (Start) y el arranque REACTIVO por keyword/fallback/evento (handleTrigger,
	// enterEventFlow)— y las dos cuentan, a propósito: las dos son el motor hablándole
	// al contacto sin que un humano teclee nada, que es la definición de la métrica.
	//
	// `false`: el turno NO se cortó — se guardó estado y el hilo del turno de apertura
	// ya está escrito unas líneas arriba, así que su referencia puede entrar en la
	// ventana sin quedar colgando.
	ack, err := rt.send(ctx, sessionID, to, key, outs)
	return ack, false, err
}

// appendTagline PEGA la coletilla al final del último texto que se va a enviar (T3.8
// punto 2), en vez de mandarla como mensaje aparte.
//
// Que vaya pegada no es estética: son UN turno y UN token del anti-loop. Enviarla suelta
// serían dos mensajes al cliente y dos tokens, y con el cupo justo el segundo no
// saldría — el aviso desaparecería justo cuando la conversación va apretada.
//
// Sin coletilla, o sin ningún texto al que pegarla, devuelve las salidas TAL CUAL: si la
// respuesta no lleva texto (solo un adjunto, por ejemplo) no se inventa un saliente para
// colocarla, porque eso es exactamente lo que esta función existe para evitar.
//
// El bool dice si REALMENTE se pegó, y no sobra: quien llama usa esa respuesta para
// decidir si marca la conversación. Sin él, una respuesta que termina en adjunto se
// marcaría como «ya se le ofreció» sin que el cliente hubiera leído nada, y esa
// conversación no volvería a recibir la coletilla nunca. La marca tiene que seguir al
// hecho, no a la intención.
func appendTagline(outs []engine.Output, tagline string) ([]engine.Output, bool) {
	if tagline == "" || len(outs) == 0 {
		return outs, false
	}
	last := len(outs) - 1
	if outs[last].Text == "" {
		return outs, false
	}
	outs[last].Text += "\n\n" + tagline
	return outs, true
}

// markTaglineOffered deja constancia en el estado de que esta conversación ya la vio.
// Ver varTaglineOffered para el alcance («por conversación») y su consecuencia.
func markTaglineOffered(st *model.Conversation) {
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	st.Vars[varTaglineOffered] = true
}

// seedIntentParams siembra en el estado recién creado los parámetros de la intención
// que originó el arranque (Plan 029 · T8): Vars["intent_params"] (map de strings del
// clasificador) y Vars["intent_name"]. Sin params (arranque por keyword/fallback/API)
// es un no-op ⇒ no-regresión. El módulo los CONSUME una sola vez (los limpia de Vars
// tras usarlos) en su Prime.
func seedIntentParams(st *model.Conversation, params map[string]string, name string) {
	if len(params) == 0 && name == "" {
		return
	}
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	if len(params) > 0 {
		p := make(map[string]any, len(params))
		for k, v := range params {
			p[k] = v
		}
		st.Vars[modules.VarIntentParams] = p
	}
	if name != "" {
		st.Vars[modules.VarIntentName] = name
	}
}
