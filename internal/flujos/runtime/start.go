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
	return rt.startLocked(ctx, tenantID, flowID, sessionID, key, contactID, "", nil, "", "")
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
// keyword, fallback). ⚠️ Llega por parámetro y NO puede leerse de st.EventID: en
// este camino st nace fresco y el puntero flow_state.event_id lo estampa
// pointStateAtEvent DESPUÉS de arrancar (events.go, enterEventFlow) — leerlo aquí
// daría SIEMPRE "". Quien tiene el evento recién nacido/conmutado en la mano es el
// llamante (enterEventFlow), y es él quien lo pasa para que los efectos del
// pre-carga (p. ej. item_added del Prime del carrito) lleguen al proyector
// declarando a su padre.
func (rt *Runtime) startLocked(ctx context.Context, tenantID, flowID, sessionID string, key store.Key, contactID, eventID string, intentParams map[string]string, intentName, tagline string) (*cloudlinkv1.Ack, error) {
	def, err := rt.store.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		return nil, fmt.Errorf("runtime: definición vigente: %w", err)
	}

	// Guarda D-054.5 (Plan 054 · T2.3): ningún flujo con contenido durable arranca
	// sin evento padre. La condición es el PARÁMETRO eventID, NUNCA st.EventID —st
	// ni siquiera existe todavía, y el comentario de la firma de arriba ya avisa de
	// que en este camino (API/keyword/fallback) eventID llega SIEMPRE ""—. Va JUSTO
	// AQUÍ, tras cargar def y ANTES de rt.store.Exists/EnterPrimed/Save: un rechazo
	// no deja rastro — cero flow_state, cero efectos, cero envío (REQ-054.2).
	if eventID == "" && rt.engine.FlowProducesDurableContent(def) {
		return nil, ErrDurableFlowNeedsEvent
	}

	exists, err := rt.store.Exists(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("runtime: comprobar existencia: %w", err)
	}
	if exists {
		// Gotcha 409 (design.md §3.4) — ESTADO ACTUAL, no la historia que este
		// comentario contaba antes de la guarda D-054.5 (arriba): hoy este bloque NO
		// tiene camino vivo que lo ejecute con algo que reiniciar.
		//
		// API y keyword SIEMPRE pasan eventID=="" (Start la clava a "", startPlainFlow
		// también); si def es durable —el carrito lo es SIEMPRE, cart.Module.
		// ProducesDurableContent() no mira el flujo, devuelve true a secas— la guarda
		// de arriba ya devolvió ErrDurableFlowNeedsEvent antes de llegar aquí. La
		// única puerta que trae un eventID real es enterEventFlow (events.go), y esa
		// hace rt.store.Delete(key) justo antes de llamar a startLocked, así que
		// exists ya dio false y este `if` ni se evalúa. Y la ÚNICA ResumePolicy
		// registrada en producción (bootstrap.go) es la de cart.NodeTypeCart: para
		// cualquier otro tipo de nodo, restartableOnStart no encuentra política y
		// devuelve (false, nil) sin más, así que tampoco reinicia nada.
		//
		// Neto: restartableOnStart puede seguir LLAMÁNDOSE (un nodo no-cart con
		// exists==true) pero ya no puede REINICIAR nada — «solo el carrito se
		// reinicia» describe un comportamiento que hoy es imposible de alcanzar. NO
		// se borra aquí (esa decisión es de otro frente); se deja como lo que es: un
		// camino sin nadie que lo recorra.
		restart, rerr := rt.restartableOnStart(ctx, def, key, tenantID, contactID, sessionID)
		if rerr != nil {
			return nil, rerr
		}
		if !restart {
			return nil, ErrConversationExists
		}
	}

	st := model.Conversation{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	// Params iniciales (Plan 029 · T8): al arrancar por decisión llm se siembran los
	// intent_params en Vars ANTES del primer paso, para que un módulo pre-cargue el
	// flujo (p. ej. el carrito con el producto pedido). EnterPrimed consulta la
	// capacidad Primer del nodo inicial; sin params sembrados equivale a Enter.
	seedIntentParams(&st, intentParams, intentName)
	st, outs, effects, err := rt.engine.EnterPrimed(ctx, def, st)
	if err != nil {
		return nil, fmt.Errorf("runtime: enter: %w", err)
	}
	// La coletilla se pega ANTES de guardar para que la marca de «ya se ofreció» viaje
	// en el MISMO Save que el estado inicial: si se marcara después, un fallo entre
	// medias dejaría una conversación que ya la vio y no lo recuerda.
	outs, ofrecida := appendTagline(outs, tagline)
	if ofrecida {
		markTaglineOffered(&st)
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	// Efectos DECLARADOS por el pre-add del módulo (p. ej. item_added del carrito):
	// mismo fan-out EN PROCESO que HandleIncoming, DESPUÉS del Save. Un fallo de un
	// sink se loguea y no aborta (el estado ya quedó persistido).
	if len(effects) > 0 {
		// EventID sale del PARÁMETRO, no de st.EventID: aquí st.EventID es SIEMPRE ""
		// a propósito (pointStateAtEvent estampa el puntero después de este camino).
		// Ver el comentario de la firma (T4.5.1).
		ec := EffectContext{TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: eventID}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	return rt.send(ctx, sessionID, to, outs)
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
