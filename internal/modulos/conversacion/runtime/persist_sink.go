// Porta internal/flujos/runtime/persist_sink.go @ e0159171

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// flowEventStore es lo único que el PersistSink necesita del almacén tras extraer la
// proyección a los módulos (Plan 027 · Ola 3 · T8, ISP): el outbox append-only
// flow_events. La proyección tipada la aportan los modules.Projector inyectados. Lo
// satisfacen los dos repositorios de conversacion/store (Postgres y memoria).
type flowEventStore interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// DecisionAppender es el puerto ESTRECHO del sink hacia el hilo del evento
// (Plan 043 · Ola 4.5 · T4.5.7a, D-043.23): una fila `decision` por efecto
// estructurado del cliente. Lo satisface *events.Store (AppendDecision) y el
// EventStore del runtime — se declara aparte por ISP: el sink no necesita crear
// eventos ni tocar su ciclo de vida para escribir en su hilo.
//
// payload es JSON en claro de nivel 1 (nunca una clave privada, ver Handle).
type DecisionAppender interface {
	AppendDecision(ctx context.Context, eventID string, payload []byte) error
}

// Nombres de los efectos que este sink reconoce como DECISIÓN del cliente. Son
// RÉPLICAS de los literales que declaran los módulos (la convención de siempre: el
// PersistSink replica los nombres sin importar el módulo), y forman una LISTA
// CERRADA — la whitelist de D-043.13/23.
const (
	// effectItemAdded (cart.EffectItemAdded): la línea que el cliente AÑADIÓ al
	// pedido, con su sku, cantidad y variante en el payload. Es la decisión
	// arquetípica del carrito.
	effectItemAdded = "item_added"
	// effectNoteAdded (cart.EffectNoteAdded): la personalización que el cliente
	// dictó — la indicación de UNA línea (con su texto y, si partió la línea ×N,
	// el split_from_qty) o la indicación del pedido entero (de la que el payload
	// público solo lleva el LARGO; el literal es PII y no entra en el nivel 1).
	// También es el único efecto que acompaña a un cambio de líneas que no es un
	// alta (el split ×N → ×(N-1)+×1), así que cubre el «quitado/cantidad» de la
	// tabla de D-043.23 tal como el módulo lo emite hoy.
	effectNoteAdded = "note_added"
	// effectSurveyAnswer (survey.EffectSurveyAnswer): la respuesta validada de la
	// encuesta (question_id + answer_code).
	effectSurveyAnswer = "survey_answer"
)

// decisionEffects es la whitelist CERRADA de efectos-decisión (D-043.23): lo que
// el CLIENTE decidió, estructurado, nivel 1 en claro. Lo que queda FUERA, y por
// qué, para que nadie lo «complete» sin pasar por la decisión:
//
//   - cart_started, category_selected, item_viewed: NAVEGACIÓN — mirar no decide.
//   - cart_closed, cart_cancelled, cart_expired y toda apertura: CICLO DE VIDA del
//     evento/solicitud, no una decisión dentro de él (D-043.23 lo dice explícito).
//   - buyer_data_captured: sí es del cliente, pero es DATO PERSONAL con Kind
//     private (nivel 2, D-041.13) — su sitio es la fila cifrada del proyector,
//     jamás una entrada en claro del hilo. La guarda de KindPrivate en Handle lo
//     cortaría igual; no listarlo evita depender solo de esa guarda.
var decisionEffects = map[string]struct{}{
	effectItemAdded:    {},
	effectNoteAdded:    {},
	effectSurveyAnswer: {},
}

// ErrMaterializationFailed marca, DENTRO del error que Handle devuelve, que el
// fallo alcanzó la parte que MATERIALIZA contenido: el INSERT del outbox
// flow_events o la proyección tipada del módulo (intakes/survey_results). NO lo
// lleva un fallo AISLADO del hilo de decisión: ese sigue siendo best-effort puro
// incluso cuando la proyección tuvo éxito — «perder una fila de historial no puede
// costar una fila de negocio» (D-043.23).
//
// Plan 054 · T3 (D-054.4, RT-10): el despacho del runtime lo inspecciona con
// errors.Is para decidir si ESTE fallo concreto es candidato al reintento acotado /
// corte de turno de un módulo durable (EffectContext.Durable). Sin esta distinción,
// un hipo aislado en conversation_events —que Handle YA sube para que el despacho lo
// loguee, por diseño— cortaría el turno de un cliente cuyo pedido SÍ se guardó:
// justo el modo de sobre-corte que la decisión de Jhoan (R3) quería evitar.
//
// El reintento, el corte del turno y el aviso al cliente
// (defaultDurableSinkFailureNotice, nunca un SQLSTATE) NO son de este sink: son del
// despacho del runtime (ola de `incoming`/`resume`). Este sink solo MARCA; no
// reintenta ni mira EffectContext.Durable.
//
// Su texto es observable (sale en el log del despacho) y se copia literal.
var ErrMaterializationFailed = errors.New("runtime: la materialización del efecto falló")

// PersistSink es el EventSink que MATERIALIZA cada efecto en el outbox append-only
// flow_events y delega la PROYECCIÓN tipada a los modules.Projector registrados (Plan
// 027 · Ola 3 · T8, cierra H10). NO conoce los efectos de ningún módulo: el switch
// central (survey_answer→survey_results, cart_*→intakes/intake_items) vive en cada
// módulo (modules/survey, modules/cart). Añadir un módulo con proyección NO obliga a
// tocar este fichero (OCP): basta registrar su Projector en el arranque.
//
// Además ALIMENTA EL HILO del evento (Ola 4.5 · T4.5.7a): ciertos efectos, cuando el
// turno pertenece a un evento vivo (ec.EventID), dejan su payload público como fila
// `decision` vía el DecisionAppender (ver Handle).
//
// El outbox flow_events es la bitácora completa (base de la telemetría por paso, Δt
// entre efectos); la proyección es la vista consultable (GROUP BY, joins) que un
// JSONB no da con índice. La idempotencia es HEREDADA de la dedupe por
// last_wa_message_id del runtime: este sink no deduplica.
//
// # Fase (RT-18)
//
// PersistSink NO implementa PhasedSink, a propósito: un sink sin fase corre en
// PhaseProject, la fase por defecto, que es donde se materializa y donde un proyector
// puede ENRIQUECER eff.Payload (el del carrito anota intake_id). Por eso corre SIEMPRE
// antes que el WebhookSink (PhaseNotify), que lee esa anotación, se registren en el
// orden que se registren. El orden lo garantiza el runtime, no este tipo.
//
// Lleva tres campos: el almacén del outbox, los proyectores y el hilo de decisiones.
type PersistSink struct {
	repo       flowEventStore
	projectors []modules.Projector
	// decisions es el hilo del evento (T4.5.7a). nil ⇒ no se escribe ninguna fila
	// `decision` (no-regresión total; lo cablea el arranque con WithDecisionThread).
	decisions DecisionAppender
}

// NewPersistSink construye el sink con el almacén del outbox y los proyectores por
// módulo, en el orden dado. Nunca devuelve nil. Sin proyectores, solo escribe
// flow_events (los efectos de negocio no se proyectan a sus tablas tipadas). Nace sin
// hilo de decisiones (ver WithDecisionThread).
//
// No escribe nada al construir. A diferencia del WebhookSink, NO es nil-safe: el
// almacén es obligatorio (un almacén nil es un error de cableado, no un modo de
// funcionamiento).
func NewPersistSink(repo flowEventStore, projectors ...modules.Projector) *PersistSink {
	return &PersistSink{repo: repo, projectors: projectors}
}

// WithDecisionThread cablea el productor de filas `decision` del hilo (T4.5.7a) y
// devuelve EL PROPIO sink (el mismo puntero) para poder encadenarlo en el cableado. Es
// un método y no un parámetro de NewPersistSink para que el constructor no cambie de
// firma.
//
// Con nil —o sin llamarlo— no se escribe ninguna fila `decision` (no-regresión total).
func (s *PersistSink) WithDecisionThread(a DecisionAppender) *PersistSink {
	s.decisions = a
	return s
}

// Handle materializa UN efecto. Hace hasta tres cosas, SIEMPRE en este orden, con el
// mismo ctx que recibió:
//
// # 1 · El outbox flow_events
//
// Inserta una fila con TenantID, ContactID, FlowID y FlowVersion del EffectContext, y
// Kind, Name y payload del efecto. El Kind se escribe TAL CUAL (el de los efectos de
// ciclo de vida del evento es "event", ver event_effects.go). Ni SessionID ni EventID
// viajan al outbox.
//
// Hay DOS excepciones a «siempre en flow_events», y las dos son de la plataforma, no
// del módulo — si dependieran de que cada módulo se acuerde de no emitir PII, bastaría
// uno nuevo distraído:
//
//   - modules.KindPrivate (Plan 041 · T4.5): el efecto ENTERO lleva datos personales.
//     Se SALTA el INSERT y va DIRECTO al proyector, que lo cifra.
//   - Effect.PrivateKeys (defecto A2 del cierre del Plan 041): el efecto es público
//     salvo por unas claves. Se escribe SIN ellas (Effect.PublicPayload) y el proyector
//     sigue recibiéndolo entero. El eff.Payload original NO se muta.
//
// Las dos existen por lo mismo: public.flow_events es un outbox append-only, en claro
// y sin poda, así que lo que se escriba ahí sobrevive a la solicitud que lo motivó y a
// cualquier borrado posterior.
//
// Si el INSERT falla, Handle CORTA ahí: no escribe la decisión ni llama a ningún
// proyector, y devuelve un error que cumple errors.Is con ErrMaterializationFailed y
// con el error del almacén, de texto
// "runtime: la materialización del efecto falló: outbox: <error del almacén>". Sin la
// fila del outbox la proyección nunca llega a intentarse, así que para un efecto
// durable esto TAMBIÉN es «el contenido no quedó materializado», no solo un fallo de
// bitácora.
//
// # 2 · La fila `decision` del hilo del evento (T4.5.7a, D-043.23)
//
// Se escribe UNA fila, con AppendDecision(ctx, ec.EventID, payload), solo si se cumplen
// las cuatro condiciones:
//
//   - hay hilo cableado (WithDecisionThread con un valor no nil);
//   - ec.EventID no es "": «siempre que haya evento vivo» es literal en D-043.23; una
//     decisión sin evento no tiene hilo en el que vivir, y no se inventa uno;
//   - el efecto NO es modules.KindPrivate: el hilo en claro no ve NUNCA un dato
//     personal (misma regla de plataforma que el outbox);
//   - el NOMBRE del efecto está en la lista CERRADA de decisiones del cliente: exacta
//     y literalmente "item_added", "note_added" y "survey_answer" (réplicas de los
//     nombres que declaran los módulos del carrito y de la encuesta: este sink no los
//     importa).
//
// Lo que queda FUERA de la lista, y por qué, para que nadie la «complete» sin pasar
// por la decisión: cart_started, category_selected, item_viewed son NAVEGACIÓN (mirar
// no decide); cart_closed, cart_cancelled, cart_expired y los efectos de ciclo de vida
// del evento son CICLO DE VIDA, no una decisión dentro de él; buyer_data_captured sí
// es del cliente, pero es dato personal (nivel 2, D-041.13) y su sitio es la fila
// cifrada del proyector.
//
// El payload es el Effect.PublicPayload serializado a JSON: la misma poda que el
// outbox (la indicación del pedido entra como su LARGO, no como su texto).
//
// Va ANTES de la proyección a propósito: la decisión es lo que el MÓDULO declaró, y
// algún proyector ENRIQUECE eff.Payload al materializar (ver SinkPhase) — escribir
// después colaría en el hilo datos que el cliente no decidió.
//
// Es BEST-EFFORT: un fallo del hilo NO corta la proyección. El error sale igualmente
// (para que el despacho lo loguee), cumple errors.Is con el error del hilo y NO con
// ErrMaterializationFailed, con el texto
// `runtime: escribir la decisión "<nombre>" en el hilo del evento: <error del hilo>`
// (o `runtime: serializar la decisión "<nombre>" para el hilo: <error>` si el payload
// no se puede serializar a JSON, en cuyo caso no se llama al hilo).
//
// # 3 · La proyección tipada
//
// Recorre los proyectores en su orden de registro y delega en EL PRIMERO cuyo
// Handles(eff.Name) sea true; los siguientes NO se consultan ni se llaman, aunque
// también lo reconozcan (así se comporta el viejo, persist_sink.go:182-192: un nombre
// de efecto, un proyector). Project recibe el efecto ENTERO —el mismo mapa Payload,
// con sus claves privadas, de modo que lo que el proyector anote lo ven los sinks de
// fases posteriores— y un modules.EffectMeta con los seis campos del EffectContext:
// TenantID, ContactID, SessionID, FlowID, FlowVersion y EventID.
//
// Un efecto que ningún proyector reconoce (navegación, telemetría, ciclo de vida) solo
// queda en flow_events.
//
// Si Project falla, Handle devuelve un error que cumple errors.Is con
// ErrMaterializationFailed y con el error del proyector, de texto
// "runtime: la materialización del efecto falló: proyección: <error del proyector>".
// Si ADEMÁS falló el hilo, los dos errores salen JUNTOS (errors.Join, el del hilo
// primero): errors.Is reconoce los dos orígenes y la marca, que la lleva solo la
// proyección.
//
// # Qué devuelve, en resumen
//
// nil si todo fue bien o si lo único que había que hacer era el outbox; el error del
// hilo a secas (sin marca) si solo falló el hilo; un error marcado con
// ErrMaterializationFailed si falló el outbox o la proyección. Nunca hace panic por un
// payload nil o sin las claves que un proyector esperaría: no lee el contenido del
// payload.
func (s *PersistSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if eff.Kind != modules.KindPrivate {
		fe := store.FlowEvent{
			TenantID:    ec.TenantID,
			ContactID:   ec.ContactID,
			FlowID:      ec.FlowID,
			FlowVersion: ec.FlowVersion,
			Kind:        eff.Kind,
			Name:        eff.Name,
			Payload:     eff.PublicPayload(),
		}
		if err := s.repo.InsertFlowEvent(ctx, fe); err != nil {
			// Marcado con ErrMaterializationFailed (Plan 054 · T3): sin la fila del
			// outbox, la proyección NUNCA llega a intentarse (return corta aquí), así
			// que para un efecto durable esto TAMBIÉN es "el contenido no quedó
			// materializado", no solo un fallo de bitácora.
			return fmt.Errorf("%w: outbox: %w", ErrMaterializationFailed, err)
		}
	}

	// El hilo va ANTES de la proyección a propósito: la decisión es lo que el
	// MÓDULO declaró, y algún proyector ENRIQUECE eff.Payload al materializar
	// (cart anota intake_id en cart_closed, ver SinkPhase) — escribir después
	// colaría en el hilo datos que el cliente no decidió.
	threadErr := s.appendDecision(ctx, ec, eff)

	meta := modules.EffectMeta{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		SessionID:   ec.SessionID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
		EventID:     ec.EventID,
	}
	for _, p := range s.projectors {
		if p.Handles(eff.Name) {
			if perr := p.Project(ctx, meta, eff); perr != nil {
				// Igual que arriba: SOLO la proyección (o el outbox) lleva la marca.
				// threadErr, si lo hay, viaja JUNTO (errors.Join no lo descarta, así que
				// el despacho lo sigue viendo en el log) pero no la lleva él mismo.
				return errors.Join(threadErr, fmt.Errorf("%w: proyección: %w", ErrMaterializationFailed, perr))
			}
			return threadErr
		}
	}
	return threadErr
}

// appendDecision escribe la fila `decision` del hilo para un efecto de la
// whitelist (T4.5.7a, D-043.23). Las cuatro guardas, en orden de baratura:
//
//   - decisions nil: el hilo no está cableado (no-regresión).
//   - ec.EventID vacío: el turno NO pertenece a un evento vivo — «siempre que haya
//     evento vivo» es literal en D-043.23; una decisión sin evento no tiene hilo
//     en el que vivir, y no se inventa uno.
//   - KindPrivate: el payload es dato personal; el hilo en claro no lo ve NUNCA
//     (misma regla de plataforma que el outbox).
//   - fuera de la whitelist: navegación y ciclo de vida no son decisiones.
//
// El payload es el PublicPayload serializado: estructura en claro de nivel 1, sin
// las claves privadas (la indicación del pedido entra como su LARGO, igual que en
// flow_events — defecto A2 del Plan 041, misma poda, mismo motivo).
func (s *PersistSink) appendDecision(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if s.decisions == nil || ec.EventID == "" || eff.Kind == modules.KindPrivate {
		return nil
	}
	if _, ok := decisionEffects[eff.Name]; !ok {
		return nil
	}
	payload, err := json.Marshal(eff.PublicPayload())
	if err != nil {
		return fmt.Errorf("runtime: serializar la decisión %q para el hilo: %w", eff.Name, err)
	}
	if err := s.decisions.AppendDecision(ctx, ec.EventID, payload); err != nil {
		// Best-effort (patrón PersistSummary): el hilo JAMÁS tumba el turno. El
		// error sube envuelto para que el despacho lo loguee y siga.
		return fmt.Errorf("runtime: escribir la decisión %q en el hilo del evento: %w", eff.Name, err)
	}
	return nil
}
