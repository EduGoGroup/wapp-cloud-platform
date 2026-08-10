package runtime

import (
	"cmp"
	"context"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// EffectContext lleva la identidad de la conversación que produjo un efecto, sin
// PII (Plan 015 · T2). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID en claro.
type EffectContext struct {
	TenantID  string
	ContactID string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	// SessionID identifica la sesión de WhatsApp que produjo el efecto; el
	// PersistSink lo persiste como intakes.session_id (metadato de trazabilidad,
	// Plan 016 · design.md §3.4). No es PII.
	SessionID   string
	FlowID      string
	FlowVersion int
	// EventID es el id del evento conversacional VIVO (conversation_events.id) al
	// que pertenece el efecto; "" si la conversación no tiene evento (Plan 043 ·
	// Ola 4.5 · T4.5.1, D-043.21). Es lo que permite al proyector de cada módulo
	// escribir la FK invertida (intakes.event_id, survey_results.event_id): el
	// hijo declara a su padre. ⚠️ En el camino de `start` NO sale de st.EventID
	// (ahí todavía es "" a propósito: pointStateAtEvent estampa el puntero DESPUÉS
	// de arrancar) sino del evento recién nacido/conmutado que el runtime tiene en
	// la mano — ver startLocked.
	EventID string
}

// EventSink es el puerto por el que el runtime despacha cada Effect que un módulo
// DECLARA (modules.Effect) al avanzar una conversación (Plan 015 · T2, segunda
// costura del refactor hexagonal). Es el análogo de gatewaygrpc.ReceiptSink: el
// runtime lo invoca en fan-out EN PROCESO (ADR-0003, sin broker) y el default es
// log-only (LogSink).
//
// Contrato de Handle:
//   - NO debe bloquear de forma indefinida (el runtime lo llama en su goroutine
//     de HandleIncoming, tras el Save del estado).
//   - NUNCA debe filtrar PII ni credenciales (ContactID es opaco; el Payload es
//     dato de negocio, no PII).
//   - Un error se LOGUEA (lo hace el dispatcher del runtime) y NO aborta el
//     avance del flujo: el estado ya quedó persistido antes del dispatch.
type EventSink interface {
	Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error
}

// SinkPhase declara CUÁNDO corre un EventSink dentro del fan-out de UN efecto
// (Plan 042 · Ola 3.1, endurecimiento post-review).
//
// POR QUÉ EXISTE ESTO. El fan-out de dispatch() entrega el MISMO modules.Effect a
// todos los sinks, y `Effect.Payload` es un `map[string]any` — un tipo REFERENCIA.
// Eso significa que un sink que corre temprano puede ENRIQUECER el payload y que
// el que corre después lo vea, sin volver a consultar la base. El puente CRM
// depende exactamente de eso: `cart.Projector.closeIntake` anota el `intake_id`
// que acaba de generar `store.CloseIntake` y el `WebhookSink` lo lee para
// correlacionar (INV-02: cero consultas extra en línea con el mensaje).
//
// Hasta esta ola, que eso funcionara dependía ÚNICAMENTE del orden en que
// bootstrap.go llamaba a WithEventSink: `rt.sinks` es un slice y las Option hacen
// append, así que invertir dos líneas de wiring rompía la correlación EN SILENCIO
// —el webhook salía con `intake_id: ""`, sin error ni test rojo—. Una dependencia
// de orden real que no estaba expresada en ningún tipo es justo la clase de hueco
// que el review de las Olas 1-3 pedía cerrar.
//
// Ahora el orden es una propiedad del RUNTIME, no del wiring: Runtime.New ordena
// los sinks por fase (estable) una sola vez al construir, y dispatch() no cambia.
// El coste por efecto es cero.
type SinkPhase int

const (
	// PhaseProject es la fase por DEFECTO: materializa el efecto (flow_events,
	// proyecciones tipadas) y puede ENRIQUECER eff.Payload con lo que genere al
	// hacerlo (identificadores de fila, sobre todo). Es donde corre PersistSink.
	PhaseProject SinkPhase = 0

	// PhaseNotify corre DESPUÉS de que toda la proyección terminó: son los sinks
	// que solo LEEN el efecto ya enriquecido para contárselo a alguien de fuera
	// (el WebhookSink del puente CRM). Un sink de esta fase NO debe escribir en
	// eff.Payload: nadie detrás suyo lo leería.
	PhaseNotify SinkPhase = 100
)

// PhasedSink lo implementa el EventSink que necesita correr en una fase distinta
// de la de proyección. Es OPCIONAL y retrocompatible: un sink que no lo implementa
// corre en PhaseProject, que es exactamente donde corría antes de que esta
// interfaz existiera (no-regresión total para LogSink y PersistSink).
type PhasedSink interface {
	EventSink
	// Phase declara en qué fase del fan-out debe correr este sink.
	Phase() SinkPhase
}

// phaseOf devuelve la fase declarada por un sink, o PhaseProject si no declara
// ninguna (el default que preserva el comportamiento previo).
func phaseOf(s EventSink) SinkPhase {
	if p, ok := s.(PhasedSink); ok {
		return p.Phase()
	}
	return PhaseProject
}

// sortSinksByPhase ordena los sinks por fase de forma ESTABLE: dentro de una misma
// fase se respeta el orden de registro (dos PersistSink siguen corriendo en el
// orden en que se cablearon). Se llama UNA vez, en Runtime.New, no por efecto.
func sortSinksByPhase(sinks []EventSink) {
	slices.SortStableFunc(sinks, func(a, b EventSink) int {
		return cmp.Compare(phaseOf(a), phaseOf(b))
	})
}
