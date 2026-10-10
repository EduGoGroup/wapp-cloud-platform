// Porta internal/flujos/runtime/event_sink.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// EffectContext lleva la identidad de la conversación que produjo un efecto, sin
// PII (Plan 015 · T2). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID en claro.
//
// Es puro dato: no tiene métodos ni constructor, y su valor cero es válido (sin
// evento, no durable).
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
	// Ola 4.5 · T4.5.1, D-043.21). El "" es un valor con significado («no hay
	// evento», p. ej. el arranque por API), no un descuido. Es lo que permite al
	// proyector de cada módulo escribir la FK invertida (intakes.event_id,
	// survey_results.event_id): el hijo declara a su padre.
	//
	// ⚠️ Quien lo rellena es el runtime, con dos matices que su contrato tiene que
	// llevar (ola siguiente): en el camino de arranque NO sale del estado guardado
	// (ahí todavía es "": el puntero se estampa DESPUÉS de arrancar) sino del evento
	// recién nacido o conmutado que el runtime tiene en la mano; y en el turno que
	// TERMINA el flujo se captura ANTES del cierre, porque los efectos pertenecen al
	// evento que estaba vivo mientras se produjeron. Los efectos de ciclo de vida del
	// evento (event_started, event_closed…) también lo llevan.
	EventID string
	// Durable indica si ESTE lote de efectos lo declaró un módulo con
	// ProducesDurableContent()==true (Plan 054 · T3, D-054.4 — el MISMO predicado
	// de F1/T2.1, ninguna lista nueva). Lo fija el llamante del despacho consultando
	// el ÚNICO nodo/módulo que produjo este lote —un nodo por turno—, nunca el flujo
	// entero (esa es otra pregunta: «¿hace falta evento para ARRANCAR?»).
	//
	// El despacho lo usa para decidir si el fallo del sink que MATERIALIZA contenido
	// exige el reintento acotado y el corte del turno (RT-10), en vez del best-effort
	// puro del ADR-0003. El valor cero (false) es SIEMPRE seguro: es lo que reciben
	// los efectos de ciclo de vida del evento (ningún módulo los declara) y cualquier
	// llamante que no lo fije — ADR-0003 intacto.
	Durable bool
}

// EventSink es el puerto por el que el runtime despacha cada Effect que un módulo
// DECLARA (modules.Effect) al avanzar una conversación (Plan 015 · T2, segunda
// costura del refactor hexagonal). Es el análogo del sink de acuses del gateway: el
// runtime lo invoca en fan-out EN PROCESO (ADR-0003, sin broker) y el default es
// log-only (LogSink).
//
// Contrato de Handle, que obliga a TODA implementación:
//   - NO debe bloquear de forma indefinida (el runtime lo llama en la goroutine del
//     entrante, tras guardar el estado).
//   - NUNCA debe filtrar PII ni credenciales (ContactID es opaco; el Payload es
//     dato de negocio, no PII).
//   - Un error se LOGUEA (lo hace el despacho del runtime) y NO aborta el avance del
//     flujo: el estado ya quedó persistido antes del despacho. La única excepción es
//     la de EffectContext.Durable (RT-10), que es del sink que materializa.
//
// El MISMO modules.Effect llega a todos los sinks del fan-out, y su Payload es un
// map —un tipo REFERENCIA—: lo que un sink anota lo ve el que corre después. Ver
// SinkPhase.
type EventSink interface {
	Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error
}

// SinkPhase declara CUÁNDO corre un EventSink dentro del fan-out de UN efecto
// (Plan 042 · Ola 3.1, endurecimiento post-review).
//
// POR QUÉ EXISTE ESTO. El fan-out entrega el MISMO modules.Effect a todos los sinks,
// y `Effect.Payload` es un `map[string]any` — un tipo REFERENCIA. Eso significa que
// un sink que corre temprano puede ENRIQUECER el payload y que el que corre después
// lo vea, sin volver a consultar la base. El puente CRM depende exactamente de eso:
// el proyector del carrito anota el `intake_id` que acaba de generar el cierre de la
// solicitud y el WebhookSink lo lee para correlacionar (INV-02: cero consultas extra
// en línea con el mensaje).
//
// Hasta aquella ola, que eso funcionara dependía ÚNICAMENTE del orden en que el
// arranque registraba los sinks: invertir dos líneas de cableado rompía la
// correlación EN SILENCIO —el webhook salía con `intake_id: ""`, sin error ni test
// rojo—.
//
// # La promesa del fan-out (RT-18), que cumple el runtime
//
// El orden es una propiedad del RUNTIME, no del cableado. El constructor del Runtime
// ordena sus sinks por fase UNA sola vez, y el despacho los recorre en ese orden:
//
//   - de menor a mayor SinkPhase: todo sink de PhaseProject corre antes que
//     cualquiera de PhaseNotify, AUNQUE el de PhaseNotify se haya registrado antes
//     (PersistSink antes que WebhookSink; el orden de las líneas del arranque no es
//     load-bearing);
//   - la ordenación es ESTABLE: dentro de una misma fase se respeta el orden de
//     registro (dos sinks de proyección cableados en cierto orden lo conservan);
//   - un sink que no implementa PhasedSink corre en PhaseProject;
//   - el coste por efecto es cero: se ordena al construir, no al despachar.
//
// La función que ordena no es exportada (en el viejo, sortSinksByPhase y phaseOf):
// nace en el verde con su test, y la conducta de extremo a extremo —los tres sinks
// reciben el efecto en el orden proyecta-A, proyecta-B, notifica; y el WebhookSink
// registrado PRIMERO encola igualmente el intake_id que generó la proyección— se
// prueba con el Runtime, en la ola siguiente.
//
// Es un entero para que quepan fases intermedias: los valores dejan hueco a propósito.
type SinkPhase int

const (
	// PhaseProject es la fase por DEFECTO, y vale 0 para que lo sea también el valor
	// cero de SinkPhase: materializa el efecto (flow_events, proyecciones tipadas) y
	// puede ENRIQUECER eff.Payload con lo que genere al hacerlo (identificadores de
	// fila, sobre todo). Es donde corre PersistSink.
	PhaseProject SinkPhase = 0

	// PhaseNotify vale 100 y corre DESPUÉS de que toda la proyección terminó: son los
	// sinks que solo LEEN el efecto ya enriquecido para contárselo a alguien de fuera
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
	// Phase declara en qué fase del fan-out debe correr este sink. Tiene que ser
	// constante para un mismo sink: el runtime la lee UNA vez, al construirse.
	Phase() SinkPhase
}
