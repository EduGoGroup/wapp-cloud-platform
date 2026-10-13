package runtime_test

// events_test.go prueba el PLANO DE EVENTOS del runtime (events.go) por sus puertas
// exportadas: HandleIncoming y StartNewOfKind. Aquí viven los ayudantes comunes (prefijo
// `events`), los puertos y las reglas EV-1, EV-2 y EV-9. El resto va partido por tema (E-13):
//
//   - events_start_new_test.go — StartNewOfKind y E-11 (EV-3, camino 3)
//   - events_switch_test.go    — el salto por tipo y event_stop (EV-3, EV-4, EV-5)
//   - events_clock_test.go     — el reloj del evento y los dos relojes (EV-6, RT-15)
//   - events_menu_test.go      — el menú del despachador y la oferta (EV-7, RT-12)
//   - events_summary_test.go   — el resumen del abandono y la coletilla (EV-8)

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Las palabras, los flujos y la ventana de un guion del plano de eventos.
const (
	eventsCartWord   = "carrito"
	eventsSurveyWord = "encuesta"
	eventsMenuWord   = "menu"
	eventsStopWord   = "dejarlo"
	eventsCartFlow   = "cart-flow-zzq"
	eventsSurveyFlow = "survey-flow-zzq"
	// eventsWindow es el event_inactivity_ttl del guion; eventsLimboTTL, el TTL
	// conversacional, más corto A PROPÓSITO: si mandara con evento activo, se vería.
	eventsWindow   = 30 * time.Minute
	eventsLimboTTL = 5 * time.Minute
)

// errEventsInjected es el fallo que los dobles de este fichero devuelven.
var errEventsInjected = errors.New("fallo inyectado-zzq")

// Los puertos de events.go los satisfacen las piezas de verdad y los dobles, sin adaptador.
var (
	_ runtime.EventStore      = (*eventshelpertest.Store)(nil)
	_ runtime.EventStore      = (*eventsFaultyStore)(nil)
	_ runtime.IntakeAbandoner = (*abandonRecorder)(nil)
	_ runtime.Dispatcher      = (*events.Dispatcher)(nil)
	_ runtime.OpeningBuilder  = (*events.Dispatcher)(nil)
	_ runtime.Dispatcher      = eventsBrokenDispatcher{}
	_ runtime.OpeningBuilder  = (*eventsOpening)(nil)
	_ runtime.FlowForKind     = ruleFlows{}
	_ runtime.FlowForKind     = eventsBrokenFlows{}
)

// eventsFaultyStore es el almacén de eventos del guion con cuatro costuras para hacerlo
// fallar o perder una carrera. Sin gancho puesto delega en el doble de verdad.
type eventsFaultyStore struct {
	*eventshelpertest.Store
	mu         sync.Mutex
	create     func(ctx context.Context, in events.NewEvent) (events.Event, error)
	transition func(ctx context.Context, eventID string, to events.Status) error
	listErr    error
	getErr     error
}

func (f *eventsFaultyStore) onCreate(fn func(ctx context.Context, in events.NewEvent) (events.Event, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.create = fn
}

func (f *eventsFaultyStore) onTransition(fn func(ctx context.Context, eventID string, to events.Status) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transition = fn
}

func (f *eventsFaultyStore) failListAlive(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = err
}

func (f *eventsFaultyStore) failGet(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

func (f *eventsFaultyStore) GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	f.mu.Lock()
	err := f.getErr
	f.mu.Unlock()
	if err != nil {
		return events.Event{}, err
	}
	return f.Store.GetEventForTenant(ctx, tenantID, eventID)
}

func (f *eventsFaultyStore) CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error) {
	f.mu.Lock()
	hook := f.create
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, in)
	}
	return f.Store.CreateEvent(ctx, in)
}

func (f *eventsFaultyStore) TransitionEvent(ctx context.Context, eventID string, to events.Status) error {
	f.mu.Lock()
	hook := f.transition
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, eventID, to)
	}
	return f.Store.TransitionEvent(ctx, eventID, to)
}

func (f *eventsFaultyStore) ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]events.Event, error) {
	f.mu.Lock()
	err := f.listErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.Store.ListAlive(ctx, tenantID, sessionID, contactID)
}

// eventsWithFaults cablea f como EventStore del runtime, sobre el doble del arnés.
func eventsWithFaults(f *eventsFaultyStore) harnessOption {
	return withOptions(func(h *harness) []runtime.Option {
		f.Store = h.events
		return []runtime.Option{runtime.WithEventStore(f)}
	})
}

// loseTransitionRace hace que la PRÓXIMA transición pierda la carrera: otro escritor
// sella antes el evento con `winner` y la llamada del runtime recibe el ErrNotOpen de verdad.
func (f *eventsFaultyStore) loseTransitionRace(winner events.Status) {
	f.onTransition(func(ctx context.Context, eventID string, to events.Status) error {
		if err := f.Store.TransitionEvent(ctx, eventID, winner); err != nil {
			return err
		}
		return f.Store.TransitionEvent(ctx, eventID, to)
	})
}

// eventsScriptedResolver es un trigger.Resolver que contesta SIEMPRE la misma decisión a un
// entrante sin conversación viva: para fabricar las que el ConfigResolver no produce hoy (un
// fallback con event_kind, un arranque con intención).
type eventsScriptedResolver struct{ decision trigger.Decision }

func (r eventsScriptedResolver) Resolve(context.Context, string, string, trigger.Signal) (trigger.Decision, error) {
	return r.decision, nil
}

func (eventsScriptedResolver) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
}

func (eventsScriptedResolver) ResolveLive(context.Context, string, string, string) (trigger.Decision, error) {
	return trigger.Decision{Action: trigger.Ignore}, nil
}

// eventsWithResolver sustituye el resolver de disparos del guion.
func eventsWithResolver(r trigger.Resolver) harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithTriggerResolver(r)}
	})
}

// eventsStartRule es una regla event_start de palabra exacta para ese tipo y flujo.
func eventsStartRule(word, kind, flowID string) trigger.Rule {
	return trigger.Rule{
		Kind: trigger.KindEventStart, Keyword: word, MatchType: trigger.MatchExact,
		EventKind: kind, FlowID: flowID,
	}
}

// eventsCartHarness monta el guion base: el flujo del carrito (el menú de dos opciones del
// arnés), su regla event_start y los dos TTL del guion.
func eventsCartHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	h.seedFlow(menuFlow(eventsCartFlow))
	h.seedRule(eventsStartRule(eventsCartWord, trigger.EventKindCart, eventsCartFlow))
	h.seedSettings(func(s *store.TenantSettings) {
		s.EventInactivityTTL = eventsWindow
		s.ConversationTTL = eventsLimboTTL
	})
	return h
}

// eventsAddSurvey añade al guion un segundo tipo con su propio flujo y su palabra.
func eventsAddSurvey(h *harness) {
	h.seedFlow(menuFlow(eventsSurveyFlow))
	h.seedRule(eventsStartRule(eventsSurveyWord, trigger.EventKindSurvey, eventsSurveyFlow))
}

// eventsAliveOfKind devuelve el evento vivo de ese tipo del cliente del guion, o falla.
func eventsAliveOfKind(t *testing.T, h *harness, kind string) events.Event {
	t.Helper()
	ev, found, err := h.events.GetAliveByKind(t.Context(), harnessTenant, harnessSession, h.contactID(harnessPhone), kind)
	if err != nil || !found {
		t.Fatalf("evento vivo de tipo %q = (%v, %v), quería uno\nlog:\n%s", kind, found, err, h.log.dump())
	}
	return ev
}

// eventsRow relee la fila de un evento, viva o terminal.
func eventsRow(t *testing.T, h *harness, eventID string) events.Event {
	t.Helper()
	ev, err := h.events.GetEventForTenant(t.Context(), harnessTenant, eventID)
	if err != nil {
		t.Fatalf("releer el evento %s: %v", eventID, err)
	}
	return ev
}

// eventsLogged dice si el runtime escribió ESA línea, byte a byte, en ese nivel.
func eventsLogged(h *harness, level, msg string) bool {
	for _, line := range h.log.at(level) {
		if line.msg == msg {
			return true
		}
	}
	return false
}

// eventsRequireLifecycle exige `want` filas de flow_events con ese efecto de ciclo de vida y
// las devuelve.
func eventsRequireLifecycle(t *testing.T, h *harness, name string, want int) []store.FlowEvent {
	t.Helper()
	got := h.flowEvents(name)
	if len(got) != want {
		t.Fatalf("filas %s = %d, quería %d: %+v\nlog:\n%s", name, len(got), want, got, h.log.dump())
	}
	return got
}

// eventsRequireRowOf exige que la fila de bitácora salga ENTERA de la fila del evento: kind
// "event", el contexto del evento y el payload {history_id, kind} más las claves extra.
func eventsRequireRowOf(t *testing.T, fe store.FlowEvent, ev events.Event, extraKeys int) {
	t.Helper()
	if fe.Kind != "event" || fe.TenantID != ev.TenantID || fe.ContactID != ev.ContactID {
		t.Errorf("fila %s = kind %q, tenant %q, contacto %q; quería \"event\" y los del evento", fe.Name, fe.Kind, fe.TenantID, fe.ContactID)
	}
	if fe.FlowID != ev.FlowID || fe.FlowVersion != ev.FlowVersion {
		t.Errorf("fila %s = flujo %q v%d, quería el del evento: %q v%d", fe.Name, fe.FlowID, fe.FlowVersion, ev.FlowID, ev.FlowVersion)
	}
	if fe.Payload["history_id"] != ev.HistoryID || fe.Payload["kind"] != ev.Kind || len(fe.Payload) != 2+extraKeys {
		t.Errorf("payload de %s = %v, quería history_id %q, kind %q y %d claves más", fe.Name, fe.Payload, ev.HistoryID, ev.Kind, extraKeys)
	}
}

// TestEvents_WithoutEventStore_ThereIsNoPlane (EV-1, INV-6): sin WithEventStore, un
// event_start arranca su flujo como una keyword, un event_stop no desactiva nada,
// StartNewOfKind no toca nada y no se escribe ni fila ni hilo ni bitácora.
func TestEvents_WithoutEventStore_ThereIsNoPlane(t *testing.T) {
	h := eventsCartHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithEventStore(nil)}
	}))
	h.seedRule(trigger.Rule{Kind: trigger.KindEventStop, Keyword: eventsStopWord, MatchType: trigger.MatchExact})

	h.say("wa-1", eventsCartWord)
	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Fatalf("textos = %q, quería solo la pantalla inicial del flujo", texts)
	}
	st, found := h.state()
	if !found || st.FlowID != eventsCartFlow || st.EventID != "" || st.OwnerEventID != "" {
		t.Fatalf("estado = (%+v, %v), quería el flujo arrancado sin evento ni dueño", st, found)
	}

	h.say("wa-2", eventsStopWord)
	if st, _ := h.state(); st.FlowID != eventsCartFlow || st.CurrentNode != "root" {
		t.Errorf("tras la palabra de event_stop el estado es %+v, quería la conversación intacta en su nodo", st)
	}

	consumed, err := h.rt.StartNewOfKind(t.Context(), h.key(), harnessSession, trigger.EventKindCart, eventsCartFlow, "")
	if consumed || err != nil {
		t.Errorf("StartNewOfKind sin plano = (%v, %v), quería (false, nil)", consumed, err)
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, sin plano no nace ninguno", rows)
	}
	for _, name := range []string{runtime.EffectEventStarted, runtime.EffectEventDeactivated} {
		eventsRequireLifecycle(t, h, name, 0)
	}
}

// TestEvents_EventStartGivesBirthBeforeSpeaking (EV-2, E-3): la regla event_start pare UNA
// fila viva con el flujo y la VERSIÓN vigentes, antes de hablarle al cliente; el estado
// queda con el evento como activo y dueño (EV-4), se anota event_started y la respuesta no
// lleva el history_id.
func TestEvents_EventStartGivesBirthBeforeSpeaking(t *testing.T) {
	h := eventsCartHarness(t)
	if version := h.seedFlow(menuFlow(eventsCartFlow)); version != 2 {
		t.Fatalf("la segunda publicación del flujo dio la versión %d, quería 2", version)
	}
	rowsAtSend := -1
	h.sender.OnSend(func(runtimehelpertest.Send) { rowsAtSend = len(h.events.Events(harnessTenant)) })

	h.say("wa-1", "  CARRITO ")

	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if ev.FlowID != eventsCartFlow || ev.FlowVersion != 2 {
		t.Errorf("el evento nació con %q v%d, quería el flujo vigente %q v2", ev.FlowID, ev.FlowVersion, eventsCartFlow)
	}
	if want := events.HistoryID(trigger.EventKindCart, harnessStart); ev.HistoryID != want {
		t.Errorf("history_id = %q, quería %q", ev.HistoryID, want)
	}
	if rowsAtSend != 1 {
		t.Errorf("al enviar la primera respuesta había %d filas de evento, quería 1: la fila nace ANTES de hablar", rowsAtSend)
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 1 {
		t.Errorf("eventos = %d, quería uno solo", len(rows))
	}
	st, found := h.state()
	if !found || st.EventID != ev.ID || st.OwnerEventID != ev.ID || st.FlowVersion != 2 {
		t.Errorf("estado = (%+v, %v), quería el evento %s como activo Y dueño, en la v2", st, found, ev.ID)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)[0], ev, 0)
	texts := h.texts()
	if len(texts) != 1 {
		t.Fatalf("textos = %q, quería la pantalla inicial del flujo", texts)
	}
	if strings.Contains(texts[0], ev.HistoryID) || strings.Contains(texts[0], ev.ID) {
		t.Errorf("la respuesta lleva un identificador del evento: %q", texts[0])
	}
}

// TestEvents_FallbackNeverGivesBirth (EV-2): el fallback queda fuera del nacimiento tardío
// AUNQUE su decisión traiga event_kind: un saludo no pare fila ni arranca reloj.
func TestEvents_FallbackNeverGivesBirth(t *testing.T) {
	resolver := eventsScriptedResolver{decision: trigger.Decision{
		Action: trigger.Fallback, FlowID: eventsCartFlow, EventKind: trigger.EventKindCart,
	}}
	h := eventsCartHarness(t, eventsWithResolver(resolver), withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithOpeningBuilder(nil)}
	}))

	h.say("wa-1", "hola-zzq")

	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, el fallback no pare ninguno", rows)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 0)
	st, found := h.state()
	if !found || st.FlowID != eventsCartFlow || st.EventID != "" || st.OwnerEventID != "" {
		t.Errorf("estado = (%+v, %v), quería el flujo del fallback arrancado sin evento", st, found)
	}
}

// TestEvents_KindWithoutFlowIsBornWithVersionZero (EV-2, D-043.3): el tipo `menu` no tiene
// flujo; su fila y su event_started llevan flow_id "" y versión 0, y esa es la verdad.
func TestEvents_KindWithoutFlowIsBornWithVersionZero(t *testing.T) {
	h := eventsCartHarness(t)
	h.seedRule(eventsStartRule(eventsMenuWord, trigger.EventKindMenu, ""))

	h.say("wa-1", eventsMenuWord)

	ev := eventsAliveOfKind(t, h, trigger.EventKindMenu)
	if ev.FlowID != "" || ev.FlowVersion != 0 {
		t.Errorf("el menú nació con %q v%d, quería sin flujo y versión 0", ev.FlowID, ev.FlowVersion)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)[0], ev, 0)
}

// TestEvents_UnreadableVersionAbortsTheBirth (EV-2): si la versión vigente del flujo no se
// puede leer, el evento NO nace y el error sube.
func TestEvents_UnreadableVersionAbortsTheBirth(t *testing.T) {
	h := eventsCartHarness(t)
	h.seedRule(eventsStartRule("fantasma", trigger.EventKindSurvey, "ghost-flow-zzq"))

	err := h.handle(h.incoming("wa-1", "fantasma"))

	if err == nil {
		t.Fatal("HandleIncoming = nil, quería el error de no poder leer la versión del flujo")
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, no debía nacer ninguno", rows)
	}
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, no debía enviarse nada", texts)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 0)
}

// eventsSurveyDefinition es un flujo con UNA pregunta de encuesta: contenido durable de
// verdad, que solo puede arrancar con evento.
func eventsSurveyDefinition() model.Flow {
	return model.Flow{
		FlowID:  eventsSurveyFlow,
		Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {
				Type: model.NodeTypeSurveyQuestion, QuestionID: "satisfaction-zzq",
				Prompt: "¿Cómo te atendimos?-zzq", Options: map[string]string{"1": "thanks", "2": "thanks"},
			},
			"thanks": {Type: model.NodeTypeMessage, Text: "Gracias por responder-zzq"},
		},
	}
}

// TestEvents_TheWholeChainNeedsNoHandSeededBinding (EV-9, RT-17, W45): regla de disparo →
// evento → engine → sink → proyector, solo con lo que el runtime escribe. Nadie siembra el
// puntero del estado ni la ligadura del contenido con su evento.
func TestEvents_TheWholeChainNeedsNoHandSeededBinding(t *testing.T) {
	h := newHarness(t, withSinks(func(h *harness) []runtime.EventSink {
		return []runtime.EventSink{runtime.NewPersistSink(h.repo, survey.NewProjector(h.repo))}
	}))
	version := h.seedFlow(eventsSurveyDefinition())
	h.seedRule(eventsStartRule(eventsSurveyWord, trigger.EventKindSurvey, eventsSurveyFlow))

	h.say("wa-1", eventsSurveyWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
	h.say("wa-2", "2")

	results := h.repo.SurveyResults()
	if len(results) != 1 {
		t.Fatalf("respuestas proyectadas = %+v, quería una\nlog:\n%s", results, h.log.dump())
	}
	got := results[0]
	if got.EventID != ev.ID || got.QuestionID != "satisfaction-zzq" || got.AnswerCode != "2" {
		t.Errorf("respuesta = %+v, quería la pregunta contestada con «2» y ligada al evento %s", got, ev.ID)
	}
	if got.FlowID != eventsSurveyFlow || got.FlowVersion != version || got.ContactID != h.contactID(harnessPhone) {
		t.Errorf("respuesta = %+v, quería el flujo %s v%d del contacto del guion", got, eventsSurveyFlow, version)
	}
	if rows := h.flowEvents(survey.EffectSurveyAnswer); len(rows) != 1 {
		t.Errorf("filas %s = %d, quería una en la bitácora", survey.EffectSurveyAnswer, len(rows))
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusClosed {
		t.Errorf("el evento quedó %q, quería closed: su flujo terminó", row.Status)
	}
	if texts := h.texts(); len(texts) != 2 || texts[1] != "Gracias por responder-zzq" {
		t.Errorf("textos = %q, quería la pregunta y el cierre", texts)
	}
}
