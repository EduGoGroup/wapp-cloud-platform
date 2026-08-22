package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
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
	//
	// Y SIN ventana de captación (Plan 044): no se reinició, no se contestó y no se
	// escribió una línea de hilo, así que no hay turno del que la ventana pueda hablar.
	// Ver el bloque del final de esta función, donde sí se observa.
	if !rt.replyAllowed(store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}) {
		return true, nil
	}
	// Efectos SINTETIZADOS por la política, por el MISMO fan-out: el proyector del
	// módulo los materializa. Best-effort para lo NO durable (coherencia
	// BD↔conversación, design.md §3.4); para un módulo con
	// ProducesDurableContent()==true (Plan 054 · T3, D-054.4) un fallo agotado del
	// sink que MATERIALIZA corta el turno — ver dispatch. Camino sin uso desde T4.7
	// —el único que lo recorría era cart_expired, derogado por D-041.16— pero el
	// fan-out es del puerto, no del carrito, y se queda para la próxima política
	// que lo necesite.
	if len(effects) > 0 {
		// st es el estado que se REANUDA: su EventID (si lo hay) es el evento al que
		// pertenecen los efectos sintetizados por la política (T4.5.1, D-043.21).
		ec := EffectContext{
			TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
			FlowID: st.FlowID, FlowVersion: st.FlowVersion, EventID: st.EventID,
			Durable: rt.engine.NodeProducesDurableContent(node.Type),
		}
		if cutErr := rt.dispatch(ctx, ec, effects, sessionID); cutErr != nil {
			// MD-054.1 opción (a): dispatch corre ANTES de re-entrar/Save (más abajo),
			// así que no hay estado avanzado que revertir — basta con NO reiniciar y
			// responder con el aviso en vez del reinicio normal. El turno queda
			// consumido (handled=true) igual que el corte del anti-loop de arriba.
			rt.log.Error("runtime: reanudación cortada: el sink durable no pudo materializar el efecto sintetizado tras el reintento acotado",
				"error", cutErr, "session_id", sessionID, "flow_id", st.FlowID)
			key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
			err := rt.sendReply(ctx, tenantID, sessionID, contactID, key, []engine.Output{{Text: defaultDurableSinkFailureNotice}})
			// Saliente FUERA DE TURNO (Plan 044 · T1.6, D-044.24): un aviso de avería
			// nuestra. No es la respuesta del flujo —el flujo se cortó justo antes—, así
			// que va MARCADO. Se escribe aunque el envío devuelva error: sendReply se
			// calla también cuando el anti-loop agota el cupo, y en ese caso no hay
			// error que distinguir; el rastro de que lo intentamos vale más que la
			// precisión sobre la entrega, que es la misma regla que persistTurnMessages.
			rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, st.EventID, defaultDurableSinkFailureNotice)
			return true, err
		}
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
	// prepareResume no recibe la store.Key —trabaja con tenantID/sessionID/contactID
	// sueltos, igual que el resto de la función—, así que se RECONSTRUYE con las tres
	// piezas que sí están en ámbito: exactamente la misma que arman el cobro del
	// anti-loop (arriba) y el sendReply del corte durable. El aviso del reinicio y el
	// nodo inicial fresco van en UNA llamada a propósito (ver el comentario del turno
	// en send.go): son un turno, no dos auto-respuestas.
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	if _, err := rt.send(ctx, sessionID, to, key, texts); err != nil {
		return false, err
	}
	// 🔴 EL LITERAL DEL CLIENTE, Y LA INVARIANTE QUE LO OBLIGA (Plan 044 · T1.4,
	// corrección del 2026-08-22): TODO MENSAJE QUE ENTRA EN `source_refs` TIENE SU
	// LITERAL EN EL HILO. Este camino observa —unas líneas más abajo, con `st.EventID`—,
	// así que el texto con el que el cliente REABRE un pedido caducado no puede faltar:
	// una referencia sin literal produce un `source_text` INCOMPLETO **sin dar error**,
	// que es el peor modo de fallo de este plan. Hasta esta corrección aquí entraba la
	// referencia y no entraba el texto: el bloque de abajo solo escribía las SALIDAS.
	//
	// NO ES UNA PUERTA NUEVA: es persistTurnMessages —el MISMO productor que usa
	// advanceLiveStep—, y con él el mismo gate (threadAllowed, fail-closed por tenant),
	// el mismo sobre cifrado (AppendMessage, nivel 2 del ADR-0034), los mismos roles y
	// el mismo best-effort. Es el gesto EXACTO que hace la CONMUTA en beginEvent
	// (events.go): literal del cliente sí, salidas no.
	//
	// DOS cosas que no son de estilo:
	//
	//   - VA PRIMERO, antes del bloque de salientes de abajo. El hilo se lee por `seq`
	//     (listThreadSQL) y el orden de la conversación es «entrante, luego salidas».
	//   - VA CON `nil` EN LAS SALIDAS. Las de este turno NO son la respuesta del flujo
	//     —son el aviso del reinicio más la pantalla inicial fresca— y entran MARCADAS
	//     por la puerta de al lado. Pasárselas aquí las duplicaría, y un literal
	//     duplicado en el hilo es una cantidad duplicada en el presupuesto.
	//
	// Y NO PUEDE ESCRIBIRSE DOS VECES: este turno se consume aquí (handled=true), así
	// que jamás alcanza el persistTurnMessages de advanceLiveStep.
	rt.persistTurnMessages(ctx, tenantID, sessionID, st.EventID, m.GetText(), nil)
	// Saliente FUERA DE TURNO (Plan 044 · T1.6, D-044.24). El reinicio NO es la
	// respuesta a lo que el cliente escribió: el entrante disparó una POLÍTICA de
	// reanudación, y lo que sale es el aviso del reinicio más la pantalla inicial
	// fresca. Un LLM que lea esa pantalla como pedido del cliente extraería el menú
	// entero, que es exactamente el bucle que la marca corta.
	//
	// `st` ya es `fresh` en este punto y `fresh` conserva el EventID del estado que se
	// reinicia: el hilo al que pertenecen estos textos es el del evento que se retoma.
	//
	// ⚠️ CON UNA EXCEPCIÓN QUE ESTE PÁRRAFO CALLABA (medida el 2026-08-22 leyendo el
	// código, no el comentario): el closeIfFinished de arriba APAGA st.EventID cuando el
	// flujo reentrado termina en el propio Enter (un `message` sin `next`). En ese caso
	// `st.EventID` vale "" y NO SE ESCRIBE NADA — ni esta fila, ni la del literal del
	// cliente, ni la referencia de la ventana de abajo: threadAllowed y
	// `WindowKey.Valid()` cierran los tres por la misma condición. La invariante se
	// mantiene (no hay referencia sin literal: no hay ninguna de las dos), pero ese
	// turno de reinicio desaparece entero del rastro. No se «arregla» aquí capturando un
	// turnEventID como hace advanceLiveStep: eso es un cambio de conducta con dueño
	// propio, y este encargo era cerrar referencias colgando.
	rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, st.EventID, outputTexts(texts)...)
	// LA VENTANA DE CAPTACIÓN (Plan 044 · Ola 1, cableada el 2026-08-22 al revisar los
	// caminos que se quedaban fuera). Este turno se CONSUME aquí —handled=true— y por
	// tanto NUNCA llega al observeForAggregation de advanceLiveStep: si no se llama
	// aquí, el mensaje con el que el cliente reabre un pedido caducado no entra en
	// ninguna ventana, y la ventana la termina anclando el mensaje SIGUIENTE.
	//
	// SÍ entra, y la razón es la misma que en startFromDecision: el cliente HABLÓ, hay
	// un evento vivo (`st.EventID`, que `fresh` conserva) y este es el primer mensaje
	// de la ráfaga que viene. Ancla bien el `message_ts` (D-044.9). Y su LITERAL acaba
	// de escribirse arriba, con persistTurnMessages: las dos mitades van juntas o no va
	// ninguna, que es lo que la invariante exige.
	//
	// 🔴 VA EN ESTA RAMA Y SOLO EN ESTA, que es la del reinicio CONSUMADO. Las otras
	// dos salidas con handled=true quedan fuera a propósito:
	//   - el cupo anti-loop agotado (arriba, `return true, nil`): no se reinició nada,
	//     no se contestó nada y no se escribió una línea de hilo. Es un turno que se
	//     tira, no un turno que ocurre.
	//   - el corte del sink durable: mismo argumento EXACTO que en advanceLiveStep —el
	//     literal de este turno no llega al hilo, así que su referencia quedaría
	//     colgando y el `source_text` saldría incompleto sin dar error—.
	//
	// Best-effort integral y no-op exacto sin agregador cableado, igual que en el otro
	// punto de llamada: 1 escritura, 0 lecturas (D-044.26), y jamás corta el turno.
	rt.observeForAggregation(ctx, tenantID, sessionID, contactID, st.EventID, m)
	return true, nil
}

// outputTexts extrae el literal de cada salida, saltándose las que no lo tienen (un
// adjunto sin caption). Es la misma poda que hace persistTurnMessages sobre sus
// engine.Output, escrita una vez para los emisores fuera de turno que mandan varias
// salidas en una sola llamada.
func outputTexts(outs []engine.Output) []string {
	texts := make([]string, 0, len(outs))
	for _, out := range outs {
		if out.Text == "" {
			continue
		}
		texts = append(texts, out.Text)
	}
	return texts
}

// durableRetryAttempts es el reintento ACOTADO de D-054.4 (Plan 054 · T3, R3 de
// Jhoan): 2 reintentos → 3 intentos en total sobre el MISMO (efecto, sink) antes
// de cortar el turno. Solo se gasta cuando las TRES condiciones se cumplen a la
// vez: ec.Durable (el módulo que declaró el efecto tiene
// ProducesDurableContent()==true, F1), el sink que falló marca su error con
// ErrMaterializationFailed (persist_sink.go: el sink que MATERIALIZA, no
// cualquiera — el hilo de decisión y cualquier PhaseNotify como el webhook nunca
// lo marcan), y el error NO es PERMANENTE (postgres.IsPermanentFailure): un
// 23502/23505/… no se cura reintentando la MISMA escritura, así que gastarlo ahí
// solo alarga el turno del cliente sin ganar nada (REQ-054.4).
const durableRetryAttempts = 2

// durableRetryBackoff es el backoff CORTO entre reintentos del sink durable
// (D-054.4): síncrono, dentro de la MISMA goroutine de HandleIncoming/
// startLocked (§5 design.md — sin broker, ADR-0003, ninguna cola ni reintento
// diferido), así que se mantiene deliberadamente breve para no alargar el turno
// del cliente más de lo necesario.
const durableRetryBackoff = 25 * time.Millisecond

// ErrTurnCutBySinkFailure lo devuelve dispatch() cuando el sink que MATERIALIZA
// contenido durable agotó el reintento acotado de D-054.4 (o falló con un error
// PERMANENTE que no lo gasta). El llamante (startLocked/advanceLiveStep/
// prepareResume/restartableOnStart) debe cortar el turno: NO guardar el estado
// avanzado (dispatch corre ANTES del Save en los cuatro sitios — MD-054.1 opción
// (a): nada que revertir) y responder con el aviso explícito
// (defaultDurableSinkFailureNotice) en vez de sus salidas normales — el flujo
// NUNCA debe alcanzar su nodo de despedida (flow_outcome no debe quedar
// "completed").
var ErrTurnCutBySinkFailure = errors.New("runtime: el turno se corta: un sink durable no pudo materializar el efecto")

// dispatch hace el fan-out EN PROCESO (ADR-0003, sin broker) de los efectos por
// cada EventSink registrado. Best-effort por defecto: un fallo se LOGUEA y NO
// aborta el avance ni corta el resto de sinks/efectos. Hoy lo usa HandleIncoming
// (efectos que DECLARA el módulo); el otro llamante era el TTL perezoso del
// carrito, derogado en T4.7 (D-041.16).
//
// Excepción ACOTADA (Plan 054 · T3, D-054.4): cuando ec.Durable es true —el
// llamante ya consultó engine.NodeProducesDurableContent sobre el único
// nodo/módulo que produjo este lote— y el sink que falla marca su error con
// ErrMaterializationFailed, dispatch reintenta hasta durableRetryAttempts veces
// con backoff corto, saltándose el reintento si postgres.IsPermanentFailure(err)
// (corta de inmediato, sin gastarlo). Agotado el cupo —o ante el fallo
// permanente— dispatch CORTA de inmediato: deja de despachar el resto de
// efectos/sinks de este turno y devuelve ErrTurnCutBySinkFailure.
//
// Para CUALQUIER otro caso —ec.Durable==false (telemetría, menú, media) o un
// sink que no marca ErrMaterializationFailed (el hilo de decisión, cualquier
// PhaseNotify como el webhook: D-054.4 no los toca)— el ADR-0003 sigue intacto:
// log y sigue, sin reintento y sin corte. Esa es también la salida de un
// reintento que SÍ materializó pero dejó un fallo best-effort colgando (p. ej. el
// hilo): no cuenta contra el cupo ni corta el turno (ver retryDurableSink).
func (rt *Runtime) dispatch(ctx context.Context, ec EffectContext, effects []modules.Effect, sessionID string) error {
	for _, eff := range effects {
		for _, sink := range rt.sinks {
			err := sink.Handle(ctx, ec, eff)
			if err == nil {
				continue
			}
			rt.log.Error("runtime: sink de efecto falló",
				"error", err,
				"kind", eff.Kind,
				"name", eff.Name,
				"session_id", sessionID,
			)
			if !ec.Durable || !errors.Is(err, ErrMaterializationFailed) {
				continue // ADR-0003 intacto: best-effort, sigue con el resto.
			}
			if lastErr := rt.retryDurableSink(ctx, sink, ec, eff, sessionID, err); lastErr != nil {
				return fmt.Errorf("%w: %w", ErrTurnCutBySinkFailure, lastErr)
			}
		}
	}
	return nil
}

// retryDurableSink reintenta UN sink que ya falló materializando un efecto
// durable (D-054.4, primer intento hecho por dispatch: firstErr es su error, ya
// confirmado ErrMaterializationFailed). Corta de inmediato —sin gastar ni un
// reintento— ante un error PERMANENTE (postgres.IsPermanentFailure): reintentar
// la MISMA escritura contra un 23502/23505/… vuelve a chocar siempre (REQ-054.4).
// Si no es permanente, reintenta hasta durableRetryAttempts veces con backoff
// corto. Tras CADA intento vuelve a comprobar ErrMaterializationFailed: si el
// reintento SÍ materializó y lo único que falló es algo best-effort (p. ej. el
// hilo de decisión), se trata como ÉXITO a efectos de D-054.4 —el error queda
// logueado, pero no cuenta contra el cupo ni corta el turno—. Devuelve nil en
// cuanto un intento cuenta como éxito, o el último error si el cupo se agota.
//
// ⚠️ COMPROMISO ASUMIDO, dicho entero: se reintenta el `Handle` COMPLETO del sink,
// no solo la proyección que falló. Si el INSERT del outbox (`flow_events`) ya había
// tenido éxito y lo que falló fue el proyector, el reintento vuelve a insertar esa
// fila: `flow_events` NO tiene restricción de unicidad, así que queda duplicada. Se
// acepta a propósito, y por tres razones: (1) solo ocurre en el camino estrecho de
// un fallo TRANSITORIO que luego cede —un permanente corta sin reintentar—; (2) el
// outbox es append-only y su consumidor de negocio (el webhook) no lee de aquí, así
// que el daño se limita a doble conteo en las métricas de ciclo de vida, no a una
// entrega duplicada al comercio; y (3) la alternativa —cirugía dentro de PersistSink
// para reintentar solo la llamada al proyector— parte una operación que hoy es
// legible de arriba abajo, a cambio de una métrica. Si algún día el outbox gana un
// consumidor de negocio, esta decisión hay que rehacerla: el cálculo cambia.
func (rt *Runtime) retryDurableSink(ctx context.Context, sink EventSink, ec EffectContext, eff modules.Effect, sessionID string, firstErr error) error {
	lastErr := firstErr
	for attempt := 1; attempt <= durableRetryAttempts; attempt++ {
		if postgres.IsPermanentFailure(lastErr) {
			return lastErr
		}
		if werr := durableRetrySleep(ctx, durableRetryBackoff); werr != nil {
			return werr
		}
		lastErr = sink.Handle(ctx, ec, eff)
		if lastErr == nil || !errors.Is(lastErr, ErrMaterializationFailed) {
			if lastErr != nil {
				rt.log.Error("runtime: sink de efecto falló tras reintentar (best-effort, no bloquea el turno)",
					"error", lastErr, "kind", eff.Kind, "name", eff.Name, "session_id", sessionID, "intento", attempt)
			}
			return nil
		}
		rt.log.Error("runtime: reintento del sink durable falló",
			"error", lastErr, "kind", eff.Kind, "name", eff.Name,
			"session_id", sessionID, "intento", attempt,
		)
	}
	return lastErr
}

// durableRetrySleep espera el backoff CORTO entre reintentos del sink durable
// (D-054.4), respetando la cancelación del ctx. No reusa
// postgres.backoffBeforeRetry (privado a su paquete, y con jitter/8 intentos
// pensados para una transacción completa, no para una espera de milisegundos
// entre dos llamadas a un EventSink) para no acoplar runtime a un detalle
// interno de otro paquete por algo tan pequeño.
func durableRetrySleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
