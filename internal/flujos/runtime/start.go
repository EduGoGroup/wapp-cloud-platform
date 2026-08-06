package runtime

import (
	"context"
	"errors"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva
// para la clave (T3 lo mapea a HTTP 409). Se inspecciona con errors.Is.
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

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
	return rt.startLocked(ctx, tenantID, flowID, sessionID, key, contactID, nil, "")
}

// startLocked es el cuerpo de Start SIN tomar el keyedMutex: asume que el llamante
// YA lo tiene tomado sobre `key`, con el contact_id ya resuelto. Lo comparten Start
// (API /admin/flows/start, /api/v1/.../start — toma el mutex y delega) y el enganche
// por palabra clave de HandleIncoming (Plan 019 · T3), que YA tomó el mutex sobre la
// misma clave: re-llamar a Start ahí causaría un auto-deadlock. Reglas de arranque
// (guard 409, reinicio de carrito, orden Save-antes-de-Send) son idénticas.
func (rt *Runtime) startLocked(ctx context.Context, tenantID, flowID, sessionID string, key store.Key, contactID string, intentParams map[string]string, intentName string) (*cloudlinkv1.Ack, error) {
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
		// TERMINÓ (sub-máquina cerrada/cancelada, o con una solicitud "open" vencida por
		// TTL) NO debe bloquear un pedido nuevo. Solo el carrito se reinicia, y solo
		// si está terminado: un carrito EN CURSO (navegando, u solicitud abierta vigente)
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
	// Params iniciales (Plan 029 · T8): al arrancar por decisión llm se siembran los
	// intent_params en Vars ANTES del primer paso, para que un módulo pre-cargue el
	// flujo (p. ej. el carrito con el producto pedido). EnterPrimed consulta la
	// capacidad Primer del nodo inicial; sin params sembrados equivale a Enter.
	seedIntentParams(&st, intentParams, intentName)
	st, outs, effects, err := rt.engine.EnterPrimed(ctx, def, st)
	if err != nil {
		return nil, fmt.Errorf("runtime: enter: %w", err)
	}
	if err := rt.store.Save(ctx, st); err != nil {
		return nil, fmt.Errorf("runtime: guardar estado inicial: %w", err)
	}
	// Efectos DECLARADOS por el pre-add del módulo (p. ej. item_added del carrito):
	// mismo fan-out EN PROCESO que HandleIncoming, DESPUÉS del Save. Un fallo de un
	// sink se loguea y no aborta (el estado ya quedó persistido).
	if len(effects) > 0 {
		ec := EffectContext{TenantID: st.TenantID, ContactID: st.ContactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	return rt.send(ctx, sessionID, to, outs)
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
