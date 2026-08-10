package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// prepareResume aplica, al REANUDAR una conversación en un nodo cuyo módulo declaró
// una ResumePolicy, el reinicio por estado terminal y la siembra de Vars de
// navegación (Plan 027 · Ola 3 · T8, cierra H9): saca del engine la lógica
// cart-específica. Para nodos SIN política (menú/encuesta) es un no-op total
// (handled=false, sin tocar Vars) ⇒ no-regresión. handled=true ⇒ el turno se consumió
// reiniciando (se avisó y se mostró el nodo inicial fresco).
func (rt *Runtime) prepareResume(ctx context.Context, sessionID string, st *model.Conversation, def model.Flow, m *cloudlinkv1.IncomingMessage, tenantID, contactID string) (bool, error) {
	node, ok := def.Nodes[st.CurrentNode]
	if !ok {
		return false, nil
	}
	policy, ok := rt.resumePolicies[node.Type]
	if !ok {
		return false, nil // nodo sin política de reanudación (menú/encuesta).
	}
	restart, notice, effects, err := policy.Restart(ctx, tenantID, contactID, st.Vars)
	if err != nil {
		return false, fmt.Errorf("runtime: política de reanudación: %w", err)
	}
	if !restart {
		// Navegación normal: el módulo siembra en Vars lo que necesite (page_size). El
		// runtime garantiza el mapa no-nil antes de delegar la siembra.
		if st.Vars == nil {
			st.Vars = map[string]any{}
		}
		if err := policy.Seed(ctx, tenantID, st.Vars); err != nil {
			return false, fmt.Errorf("runtime: siembra de reanudación: %w", err)
		}
		return false, nil
	}
	// Red anti-loop (Plan 020 · T0): el reinicio auto-responde (aviso + nodo inicial),
	// así que consume un token. Agotado ⇒ turno consumido SIN responder ni reiniciar.
	if !rt.replyAllowed(store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}) {
		return true, nil
	}
	// Efectos SINTETIZADOS por la política, por el MISMO fan-out: el proyector del
	// módulo los materializa. Best-effort: un fallo se loguea, no aborta (coherencia
	// BD↔conversación, design.md §3.4). Camino sin uso desde T4.7 —el único que lo
	// recorría era cart_expired, derogado por D-041.16— pero el fan-out es del
	// puerto, no del carrito, y se queda para la próxima política que lo necesite.
	if len(effects) > 0 {
		// st es el estado que se REANUDA: su EventID (si lo hay) es el evento al que
		// pertenecen los efectos sintetizados por la política (T4.5.1, D-043.21).
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: st.EventID}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	// Arranca LIMPIO: descarta las Vars y re-entra el flujo con la MISMA versión con
	// la que corría (def viene de GetDefinition).
	fresh := *st
	fresh.Vars = nil
	fresh, outs, err := rt.engine.Enter(ctx, def, fresh)
	if err != nil {
		return false, fmt.Errorf("runtime: reentrar tras reinicio: %w", err)
	}
	fresh.LastWaMessageID = m.GetWaMessageId()
	// CIERRE NATURAL (Plan 043 · T4.1): la copia conserva el EventID del estado que
	// se reinicia, y un Enter sobre un flujo sin nodo interactivo (message sin next)
	// deja `fresh` YA en el centinela. Mismo trato que advanceLive: cerrar y apagar
	// el puntero en el MISMO Save, no en una segunda escritura.
	rt.closeIfFinished(ctx, &fresh)
	if err := rt.store.Save(ctx, fresh); err != nil {
		return false, fmt.Errorf("runtime: guardar conversación reiniciada: %w", err)
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

// restartableOnStart decide si un Start sobre una conversación EXISTENTE puede
// reiniciarse en vez de devolver 409 (gotcha, design.md §3.4), consultando la
// ResumePolicy del nodo inicial (Plan 027 · Ola 3 · T8). Sin política (menú/encuesta)
// ⇒ false (409 intacto). Si la política sintetizara efectos se despachan (coherencia
// BD↔conversación) y devuelve true; ninguna lo hace hoy (ver prepareResume).
func (rt *Runtime) restartableOnStart(ctx context.Context, def model.Flow, key store.Key, tenantID, contactID, sessionID string) (bool, error) {
	node, ok := def.Nodes[def.Initial]
	if !ok {
		return false, nil
	}
	policy, ok := rt.resumePolicies[node.Type]
	if !ok {
		return false, nil
	}
	var vars map[string]any
	// eventID se retiene JUNTO a las Vars al cargar el estado (T4.5.1): si la
	// conversación que se reinicia apuntaba a un evento, los efectos que la política
	// sintetice deben llegar al proyector declarando ese padre (D-043.21).
	var eventID string
	if st, found, err := rt.store.Load(ctx, key); err != nil {
		return false, fmt.Errorf("runtime: cargar estado: %w", err)
	} else if found {
		vars = st.Vars
		eventID = st.EventID
	}
	restart, _, effects, err := policy.Restart(ctx, tenantID, contactID, vars)
	if err != nil {
		return false, fmt.Errorf("runtime: política de reanudación: %w", err)
	}
	if !restart {
		return false, nil
	}
	if len(effects) > 0 {
		ec := EffectContext{TenantID: tenantID, ContactID: contactID, SessionID: sessionID, FlowID: def.FlowID, FlowVersion: def.Version, EventID: eventID}
		rt.dispatch(ctx, ec, effects, sessionID)
	}
	return true, nil
}

// dispatch hace el fan-out EN PROCESO (ADR-0003, sin broker) de los efectos por
// cada EventSink registrado. Un fallo de un sink se LOGUEA y NO aborta el avance
// ni corta el resto de sinks/efectos (el estado ya quedó persistido antes del
// dispatch). Hoy lo usa HandleIncoming (efectos que DECLARA el módulo); el otro
// llamante era el TTL perezoso del carrito, derogado en T4.7 (D-041.16).
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
