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
			return true, rt.sendReply(ctx, tenantID, sessionID, contactID, key, []engine.Output{{Text: defaultDurableSinkFailureNotice}})
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
	if _, err := rt.send(ctx, sessionID, to, texts); err != nil {
		return false, err
	}
	return true, nil
}

// inheritedPointers son los DOS punteros a evento que un reinicio hereda de la fila
// que va a pisar: el ACTIVO (flow_state.event_id) y el DUEÑO
// (flow_state.owner_event_id). Existe para que restartableOnStart pueda devolverlos
// al llamante SIN devolverle el model.Conversation entero, y esa restricción es el
// punto (Plan 053 · Ola 6 · T6.1): de la fila que se reinicia se heredan los
// punteros y NADA MÁS. Las Vars se tiran a propósito —arrancar limpio ES el
// reinicio—, y devolver el estado completo invitaría justo al error contrario.
//
// El cero (los dos "") es el caso normal: no había fila, o no tenía punteros. Quien
// lo reciba debe comportarse como se comportaba antes de que este tipo existiera.
type inheritedPointers struct {
	active string
	owner  string
}

// restartableOnStart decide si un Start sobre una conversación EXISTENTE puede
// reiniciarse en vez de devolver 409 (gotcha, design.md §3.4), consultando la
// ResumePolicy del nodo inicial (Plan 027 · Ola 3 · T8). Sin política (menú/encuesta)
// ⇒ false (409 intacto). Si la política sintetizara efectos se despachan (coherencia
// BD↔conversación) y devuelve true; ninguna lo hace hoy (ver prepareResume).
//
// Desde el Plan 053 · Ola 3 · T3.1 hay una segunda razón para NO reiniciar, anterior
// a la política: que el flujo guardado en esa fila sea de OTRO evento dueño (ver
// ownerFlowMismatch, justo debajo).
//
// El SEGUNDO valor de retorno son los punteros de la fila que se reinicia, y es del
// Plan 053 · Ola 6 · T6.1 (cierra DEUDA-053.1): el llamante los necesita para que el
// estado que persiste no nazca huérfano. Se devuelve poblado solo cuando hubo fila
// que cargar; en cualquier otra salida vale el cero, que es lo mismo que devolvía
// esta función implícitamente antes de la tarea.
func (rt *Runtime) restartableOnStart(ctx context.Context, def model.Flow, key store.Key, tenantID, contactID, sessionID string) (bool, inheritedPointers, error) {
	node, ok := def.Nodes[def.Initial]
	if !ok {
		return false, inheritedPointers{}, nil
	}
	policy, ok := rt.resumePolicies[node.Type]
	if !ok {
		return false, inheritedPointers{}, nil
	}
	var vars map[string]any
	// eventID se retiene JUNTO a las Vars al cargar el estado (T4.5.1): si la
	// conversación que se reinicia apuntaba a un evento, los efectos que la política
	// sintetice deben llegar al proyector declarando ese padre (D-043.21).
	var eventID string
	// heredados viaja al llamante (T6.1). Nótese que `eventID` y `heredados.active`
	// valen lo MISMO y no se fusionan: el primero es un dato de ESTE turno (el padre
	// de los efectos que la política sintetice) y el segundo es un dato de la FILA
	// (lo que hay que volver a escribir). Hoy coinciden; unificarlos ataría dos
	// decisiones que no tienen por qué moverse juntas.
	var heredados inheritedPointers
	if st, found, err := rt.store.Load(ctx, key); err != nil {
		return false, inheritedPointers{}, fmt.Errorf("runtime: cargar estado: %w", err)
	} else if found {
		// Guarda de POSESIÓN (Plan 053 · Ola 3 · T3.1, REQ-053.3). El flujo que hay
		// guardado en esta fila puede pertenecer a OTRO evento —un `cart` a medias—, y
		// reiniciarlo por un Start de otro flujo mezclaría dos conversaciones en una
		// sola fila: las Vars ajenas viajarían a la política del módulo que arranca.
		// LA FRONTERA QUE IMPORTA —y la única OBSERVABLE— es que corta ANTES de
		// invocar policy.Restart: Restart es lo único con efectos por aquí (consulta
		// el estado del módulo y puede sintetizar efectos que luego se despachan), así
		// que es lo que un espía de test puede cazar y lo que la letra de T3.1 pide
		// impedir. Que además esté antes de retener `vars`/`eventID` es higiene de
		// lectura, no una propiedad verificable: al retornar, esas dos locales están
		// muertas, y colocar la guarda debajo daría un programa indistinguible. El
		// llamante contesta el 409 determinista de startLocked.
		if rt.ownerFlowMismatch(ctx, tenantID, sessionID, contactID, st.OwnerEventID, def.FlowID) {
			return false, inheritedPointers{}, nil
		}
		vars = st.Vars
		eventID = st.EventID
		heredados = inheritedPointers{active: st.EventID, owner: st.OwnerEventID}
	}
	restart, _, effects, err := policy.Restart(ctx, tenantID, contactID, vars)
	if err != nil {
		return false, inheritedPointers{}, fmt.Errorf("runtime: política de reanudación: %w", err)
	}
	if !restart {
		return false, inheritedPointers{}, nil
	}
	if len(effects) > 0 {
		ec := EffectContext{
			TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
			FlowID: def.FlowID, FlowVersion: def.Version, EventID: eventID,
			Durable: rt.engine.NodeProducesDurableContent(node.Type),
		}
		if cutErr := rt.dispatch(ctx, ec, effects, sessionID); cutErr != nil {
			// Camino HOY inalcanzable para contenido durable (mismo análisis que el
			// comentario de startLocked al llamar a esta función): la ÚNICA
			// ResumePolicy registrada es la del carrito, y el carrito SIEMPRE es
			// durable — cualquier intento de llegar aquí con eventID=="" ya lo
			// rechazó la guarda de D-054.5 antes de que startLocked invocara
			// restartableOnStart. Se propaga como CUALQUIER otro error de esta
			// función (mismo trato que ya tenía antes de esta tarea): no hay
			// superficie de cliente que avisar aquí, eso lo decide el llamante.
			return false, inheritedPointers{}, fmt.Errorf("runtime: reanudación al arrancar: %w", cutErr)
		}
	}
	return true, heredados, nil
}

// ownerFlowMismatch contesta UNA pregunta y solo muerde cuando la respuesta es
// DETERMINISTA: ¿el flujo guardado en este flow_state pertenece a un evento DUEÑO
// distinto del flujo que se va a arrancar? (Plan 053 · Ola 3 · T3.1, REQ-053.3).
// true ⇒ el llamante NO reinicia y el Start acaba en el 409 determinista.
//
// ⚠️ EL COMPARANDO ES `FlowID`, **NO** EL `kind` DEL EVENTO, y es una desviación
// DELIBERADA del enunciado de T3.1: no la "arregles" dentro de seis meses. `model.Flow`
// no tiene campo `Kind`, y el `Type` del nodo no es fiel al tipo de evento
// (`survey_question` ≠ `survey`), así que comparar kinds obligaría a montar una tabla
// nodeType→kind: una pieza nueva, frágil y que habría que acordarse de ampliar con
// cada módulo. El evento, en cambio, CONGELA su `FlowID` al nacer
// (events.Event.FlowID, poblado en runtime/events.go desde dec.FlowID), que es
// exactamente el dato que `def.FlowID` compara sin traducción intermedia.
//
// FAIL-OPEN en todo lo demás — decisión explícita del plan, mismo precedente que
// D-054.8 con su flow_id inexistente: esta guarda solo puede AÑADIR rechazos sobre un
// hecho que se sabe cierto; ante cualquier duda deja pasar y el comportamiento queda
// byte a byte como antes del Plan 053. Los cinco casos que dejan pasar:
//  1. ownerEventID == "" — el caso NORMAL y frecuente: menú puro (D-043.3), flujo que
//     no nació de ningún evento, o fila LEGADA anterior al backfill de T1.3.
//  2. rt.events == nil — el plano de eventos es OPCIONAL (ver EventStore en
//     events.go): sin él nadie estampa `owner_event_id` y no hay a quién preguntar.
//     Mismo guardarraíl que liveEventSwitch/eventClock/summarizeAbandoned (INV-6).
//  3. GetEventForTenant falla, events.ErrEventNotFound incluido. Contra Postgres
//     real lo que cabe aquí es un fallo TRANSITORIO de la BD, un timeout o un `ctx`
//     ya cancelado; NO las causas que uno escribiría de memoria: el id no puede ser
//     basura (la columna es `UUID`, y un no-UUID revienta al ESCRIBIR con 22P02 —
//     nunca llega a leerse) ni apuntar a un evento borrado (la FK de la 0062 va SIN
//     `ON DELETE`, deliberadamente: el DELETE falla antes que dejar el puntero
//     colgando). Donde SÍ se pueden construir esos estados es en el runtime EN
//     MEMORIA y en los tests, y por eso la rama existe. Se LOGUEA y se sigue:
//     negarle el arranque a un cliente porque una lectura NUESTRA falló sería
//     castigarle por un problema nuestro — el mismo criterio que liveEventSwitch
//     aplica a su resolver. El error no se propaga jamás hacia arriba.
//  4. el dueño resuelve SIN error pero es de OTRA conversación del mismo tenant —
//     ver el guardarraíl de SessionID/ContactID en el cuerpo.
//  5. ev.FlowID == "" — un evento que no congeló flujo al nacer. HOY es inalcanzable
//     por construcción: enterEventFlow saca el `menu` por presentMenu antes de tocar
//     flujo alguno (runtime/events.go:543-546) y cualquier otro kind con flowID==""
//     llega a pointStateAtEvent con arrancado=false (:553-567), que no estampa dueño.
//     Se mantiene por la misma razón que el 3: con un store en memoria sí se alcanza.
//
// Nada de esta lista responde a una patología observada: el fail-open es DEFENSA EN
// PROFUNDIDAD sobre una guarda que solo tiene derecho a morder cuando está segura.
func (rt *Runtime) ownerFlowMismatch(ctx context.Context, tenantID, sessionID, contactID, ownerEventID, flowID string) bool {
	if ownerEventID == "" || rt.events == nil {
		return false
	}
	// GetEventForTenant y no la cadena activeEventKind→aliveByID→ListAlive: aquella
	// lleva `AND status='open'` y un dueño ya CERRADO le saldría como «no existe»,
	// que es justo la fila sobre la que esta guarda tiene que poder pronunciarse.
	// Este lookup es por id acotado al tenant (id AND tenant_id) y SIN filtro de
	// estado, así que ve al dueño terminal igual que al vivo.
	ev, err := rt.events.GetEventForTenant(ctx, tenantID, ownerEventID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver el evento dueño; la guarda de posesión deja pasar (fail-open)",
			"error", err, "session_id", sessionID, "owner_event_id", ownerEventID, "flow_id", flowID)
		return false
	}
	// El lookup acota por TENANT, no por conversación: `WHERE id = $1 AND
	// tenant_id = $2` (flujos/events/store.go), y la FK de la 0062 tampoco liga
	// sesión ni contacto. Un `owner_event_id` que apuntase a un evento de OTRA
	// conversación del mismo tenant resolvería sin error, y esta guarda emitiría un
	// veredicto determinista sobre datos ajenos: es el único camino que se escapa de
	// los fail-open de arriba, porque no falla — acierta con el dato equivocado. Y solo
	// puede ENSANCHAR el fail-open —convierte un mordisco en un «deja pasar», nunca al
	// revés—, así que no introduce 409 falsos.
	//
	// SE LOGUEA COMO Error Y NO COMO Warn, a diferencia del fail-open de arriba, y la
	// diferencia es deliberada: aquel cubre un fallo TRANSITORIO de infraestructura
	// (la BD, un timeout) del que nadie tiene la culpa, mientras que llegar aquí
	// significa que alguien escribió un dueño que no es de esta conversación. Ningún
	// camino de CÓDIGO produce ese estado —pointStateAtEvent estampa el evento de la
	// MISMA conversación (runtime/events.go), y la FK de la 0062 no ayuda: solo exige
	// que el id exista en conversation_events, no que sea de esta sesión/contacto—,
	// pero el paso (d) del runbook de backfill SÍ puede: es un UPDATE MANUAL donde el
	// UUID del dueño lo elige una persona a mano («la elección del dueño es un
	// juicio», docs/runbooks/backfill-053-owner-event-id.sql) y su WHERE acota la FILA
	// por tenant/sesión/contacto, no el evento que se le estampa. Un dedo torcido ahí
	// deja una fila juzgada con datos ajenos para siempre, y en silencio: por eso el
	// fail-open que la protege tiene que DELATARLA, no enterrarla bajo un Warn.
	if ev.SessionID != sessionID || ev.ContactID != contactID {
		rt.log.Error("runtime: el evento dueño pertenece a otra conversación; la guarda de posesión deja pasar (fail-open) — revisa quién escribió ese owner_event_id",
			"session_id", sessionID, "owner_event_id", ownerEventID, "owner_session_id", ev.SessionID, "flow_id", flowID)
		return false
	}
	if ev.FlowID == "" {
		return false
	}
	return ev.FlowID != flowID
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
