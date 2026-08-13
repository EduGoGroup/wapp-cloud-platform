package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// Nombres de los efectos de CICLO DE VIDA del evento conversacional (Plan 043 ·
// T5.4, D-043.11). Son de PLATAFORMA, no de módulo: ningún modules.Module los
// declara, los emite el runtime en los puntos en que la vida del evento cambia —seis
// desde T5.4 (birthEvent, switchToEvent, stopEvent, eventClock, closeIfFinished,
// cancelAndAbandon) más los dos que la Ola 6 añade con EffectEventEscaped
// (handleEscape y el quinto camino de suelta, incoming.go).
//
// ⚠️ `event_expired` NO está y NUNCA estuvo: no existe esa transición (E-6 la derogó;
// verificado: 0 coincidencias en todo el árbol). Esta tarea no retira nada, solo añade.
const (
	EffectEventStarted           = "event_started"
	EffectEventSwitched          = "event_switched"
	EffectEventDeactivated       = "event_deactivated"
	EffectEventInactivityExpired = "event_inactivity_expired"
	EffectEventClosed            = "event_closed"
	EffectEventCancelled         = "event_cancelled"
	// EffectEventEscaped es el SÉPTIMO efecto de ciclo de vida (Ola 6 · E5, cierra
	// MD-043.16): el abandono del escape global (handleEscape, incoming.go) y de su
	// quinto camino gemelo (E6, el texto que no casa el despachador con
	// st.FlowID==""). Decisión de Jhoan (2026-08-11): NO reusar event_deactivated.
	// stopEvent (event_deactivated) apaga el puntero y CONSERVA el flow_state; estos
	// dos caminos lo DESTRUYEN (Delete). Son abandonos distintos y fundirlos en un
	// solo nombre haría inútil el embudo: quien lea flow_events no podría distinguir
	// «se puede retomar tal cual» de «el progreso de nodo se perdió».
	EffectEventEscaped = "event_escaped"
)

// effectKindEvent es el valor de la COLUMNA flow_events.kind para estos efectos.
// La columna admite "persist" | "event" (migración 0009, sin CHECK): "persist" es
// «persistir dato de negocio» —lo que declara la encuesta para que su proyector
// escriba survey_results— y "event" es «suceso a despachar». El ciclo de vida del
// evento no proyecta a ninguna tabla tipada: es bitácora. Va "event".
const effectKindEvent = "event"

// emitEventEffect despacha UN efecto de ciclo de vida del evento por el MISMO
// fan-out que los efectos de módulo (dispatch → rt.sinks → PersistSink →
// flow_events). Es BEST-EFFORT por construcción: dispatch loguea el fallo de cada
// sink y no devuelve error, así que ninguno de los seis sitios puede abortar su
// operación por no haber podido escribir telemetría. Es el MISMO trato que reciben
// hoy los efectos de módulo, y es el correcto: el hecho (la fila del evento) ya
// está sellado en BD antes de llegar aquí.
//
// El EffectContext sale ENTERO de la fila del evento y no del turno, y eso es lo
// que hace que el mismo helper sirva a CancelEventForTenant, que entra por HTTP y
// no tiene turno: el evento sabe de qué conversación es.
//
// ⚠️ COLISIÓN DE NOMBRES CONSCIENTE: la clave `kind` del payload es el TIPO DE
// EVENTO (menu|cart|survey|media) y la COLUMNA `kind` de la misma fila es la clase
// del efecto ("event"). El criterio de tasks.md T5.4 §Criterio pide literalmente
// `{history_id, kind}` y el criterio de tasks.md T6.2 §Criterio se escribirá contra él,
// así que se RESPETA. Una consulta que las mezcle da `kind='event'` y
// `payload->>'kind'='cart'`: distintas, no ambiguas. Hoy flow_events no tiene NINGÚN
// lector en producción, así que no hay consumidor que se pueda confundir.
func (rt *Runtime) emitEventEffect(ctx context.Context, ev events.Event, name string) {
	if ev.ID == "" {
		return
	}
	ec := EffectContext{
		TenantID:  ev.TenantID,
		ContactID: ev.ContactID,
		SessionID: ev.SessionID,
		// FlowID/FlowVersion son los del EVENTO, congelados en su fila. Para el tipo
		// `menu` valen "" y 0 y esa es la VERDAD, no una omisión: el menú no es una
		// fila de flow_definitions (D-043.3, ver flowVersionFor en events.go:458).
		// flow_events.flow_id es NOT NULL y '' lo satisface; inventar un flow_id
		// sería mentir en una bitácora append-only.
		FlowID:      ev.FlowID,
		FlowVersion: ev.FlowVersion,
		EventID:     ev.ID,
	}
	eff := modules.Effect{
		Kind: effectKindEvent,
		Name: name,
		Payload: map[string]any{
			"history_id": ev.HistoryID,
			"kind":       ev.Kind,
		},
	}
	// ec.Durable queda en su CERO (false, EffectContext no lo fija arriba): estos
	// efectos son de PLATAFORMA, ningún modules.Module los declara (docstring de
	// arriba), así que dispatch() nunca alcanza el camino de corte de D-054.4 aquí
	// — el error solo puede venir de un ErrTurnCutBySinkFailure hipotético que hoy
	// es inalcanzable. Se comprueba de todos modos (Plan 054 · T3) en vez de
	// descartarlo en silencio: si algún día un efecto de ciclo de vida SÍ marcara
	// Durable, este log es la única señal de que el corte no se está honrando aquí.
	if err := rt.dispatch(ctx, ec, []modules.Effect{eff}, ev.SessionID); err != nil {
		rt.log.Warn("runtime: dispatch de un efecto de ciclo de vida del evento devolvió un corte inesperado (best-effort de plataforma, no debería ocurrir)",
			"error", err, "session_id", ev.SessionID, "name", name)
	}
}
