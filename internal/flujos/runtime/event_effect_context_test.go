package runtime_test

// Tests de T4.5.1 (Plan 043 · Ola 4.5, D-043.21): el EventID viaja por el cauce de
// efectos hasta el proyector. Fijan los DOS matices que no se ven a simple vista:
//
//  1. En el camino de `start` el EventID NO puede salir de st.EventID (ahí es
//     SIEMPRE "" a propósito: pointStateAtEvent estampa el puntero DESPUÉS de
//     arrancar). Sale del evento recién nacido que enterEventFlow tiene en la mano.
//  2. En el turno que TERMINA el flujo, closeIfFinished apaga st.EventID antes del
//     dispatch: el EventID de los efectos de ese turno se captura ANTES del cierre
//     (los efectos pertenecen al evento que estaba vivo mientras se produjeron).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ecSink captura el PAR (EffectContext, Effect) de cada efecto despachado: lo que
// el fakeSink de dispatch_test no guarda y estos tests necesitan afirmar.
type ecSink struct {
	mu       sync.Mutex
	contexts []runtime.EffectContext
	effects  []modules.Effect
}

func (s *ecSink) Handle(_ context.Context, ec runtime.EffectContext, eff modules.Effect) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts = append(s.contexts, ec)
	s.effects = append(s.effects, eff)
	return nil
}

func (s *ecSink) all() []runtime.EffectContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtime.EffectContext(nil), s.contexts...)
}

// effectsAll devuelve los Effect capturados, EN EL MISMO ORDEN que all(): índice a
// índice son el mismo despacho. Lo añade T5.4 (Plan 043 · D2) para que los tests de
// este fichero puedan identificar POR NOMBRE el efecto del Prime/Step entre los
// nuevos efectos de ciclo de vida (event_started/event_closed) que ahora comparten
// el mismo fan-out, en vez de depender de un índice fijo que la Ola 5 desplazó.
func (s *ecSink) effectsAll() []modules.Effect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modules.Effect(nil), s.effects...)
}

// primedModule sustituye al módulo "menu" imitando el contrato del carrito: con
// intent_params sembrados, su Prime CONSUME la señal y DECLARA un efecto (el
// item_added del pre-carga, Plan 029 · T8) — es decir, emite un efecto DURANTE el
// arranque, que es exactamente el camino cuyo EventID fija el test. Su Step declara
// otro efecto y transiciona a la hoja "ventas" (fin del flujo en sampleFlow).
type primedModule struct{}

func (primedModule) Type() string        { return model.NodeTypeMenu }
func (primedModule) WaitsForInput() bool { return true }

func (primedModule) Render(node model.Node, _ model.Content) []string {
	return []string{node.Prompt}
}

func (primedModule) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	next := "ventas"
	return modules.Result{Next: &next, Vars: conv.Vars, Effects: []modules.Effect{
		{Kind: "persist", Name: "cart_closed", Payload: map[string]any{"total": 1}},
	}}
}

func (primedModule) Prime(_ model.Node, _ model.Content, vars map[string]any) (modules.Result, bool) {
	if _, ok := vars[modules.VarIntentParams]; !ok {
		return modules.Result{}, false
	}
	delete(vars, modules.VarIntentParams)
	delete(vars, modules.VarIntentName)
	return modules.Result{
		Outputs: []string{"pre-cargado"},
		Vars:    vars,
		Effects: []modules.Effect{
			{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "torta", "qty": 1}},
		},
	}, true
}

// eventStartTrigger resuelve TODO entrante sin conversación viva a un StartEvent
// con params de intención: la puerta del nacimiento tardío que llega a birthEvent →
// enterEventFlow → startLocked con pre-carga. Embebe el Noop para el resto del
// contrato (IsEscape/ResolveLive: nada).
type eventStartTrigger struct {
	trigger.NoopResolver
	dec trigger.Decision
}

func (r eventStartTrigger) Resolve(context.Context, string, string, trigger.Signal) (trigger.Decision, error) {
	return r.dec, nil
}

// newPrimedEventRuntime arma el runtime con el módulo primado, el plano de eventos
// en memoria y el sink que captura contextos.
func newPrimedEventRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *memEventStore, *ecSink) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(primedModule{})
	sink := &ecSink{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC))
	rt := runtime.New(repo, engine.New(reg), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(eventStartTrigger{dec: trigger.Decision{
			Action: trigger.StartEvent, EventKind: "cart", FlowID: testFlow,
			Params: map[string]string{"producto": "torta"}, IntentName: "comprar",
		}}),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(sink))
	return rt, repo, contacts, evs, sink
}

// TestEffectContext_ElArranqueLlevaElEventoRecienNacido (T4.5.1, el matiz de start):
// un efecto emitido DURANTE el arranque del flujo (el Prime del pre-carga) llega al
// sink con el EventID del evento que acaba de nacer — que en ese instante todavía
// NO está estampado en flow_state (pointStateAtEvent va después). Si startLocked
// leyera st.EventID, este test vería "".
func TestEffectContext_ElArranqueLlevaElEventoRecienNacido(t *testing.T) {
	rt, repo, contacts, evs, sink := newPrimedEventRuntime(t)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "quiero una torta", "wamid.p1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	alive := evs.alive()
	if len(alive) != 1 {
		t.Fatalf("el arranque debe parir UN evento vivo, hay %d", len(alive))
	}
	ecs := sink.all()
	effs := sink.effectsAll()
	// Desde T5.4 (Plan 043 · D2, telemetría de eventos) birthEvent emite
	// event_started ANTES de enterEventFlow, así que el Prime del pre-carga ya no es
	// el único efecto del turno: son DOS, y el que importa a este test (el que
	// startLocked puede haber mal-atribuido) se identifica por NOMBRE, no por índice
	// fijo.
	if len(ecs) != 2 {
		t.Fatalf("deben llegar DOS efectos (event_started del ciclo de vida + item_added del Prime), llegaron %d", len(ecs))
	}
	if effs[0].Name != runtime.EffectEventStarted {
		t.Fatalf("el primero debe ser %q (T5.4), llegó %q", runtime.EffectEventStarted, effs[0].Name)
	}
	if effs[1].Name != "item_added" {
		t.Fatalf("el segundo debe ser el item_added del Prime, llegó %q", effs[1].Name)
	}
	if ecs[1].EventID == "" {
		t.Fatal("el efecto del arranque llegó con EventID vacío: startLocked está leyendo st.EventID (que ahí es \"\" a propósito) en vez del evento recién nacido")
	}
	if ecs[1].EventID != alive[0].ID {
		t.Fatalf("EventID del efecto = %q, quiero el del evento recién nacido %q", ecs[1].EventID, alive[0].ID)
	}
	// Y el puntero que se estampó DESPUÉS apunta al mismo evento: las dos verdades
	// coinciden aunque se escriban en momentos distintos.
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != alive[0].ID {
		t.Fatalf("flow_state.event_id = %q, quiero %q", st.EventID, alive[0].ID)
	}
}

// TestEffectContext_ElTurnoQueCierraConservaElEventID (T4.5.1, el matiz del cierre
// natural): el efecto del turno que TERMINA el flujo (cart_closed) llega con el
// EventID del evento que estaba vivo durante el Step, aunque closeIfFinished lo
// apague de st antes del dispatch. Si advanceLive leyera st.EventID DESPUÉS del
// cierre, este efecto llegaría con "".
func TestEffectContext_ElTurnoQueCierraConservaElEventID(t *testing.T) {
	rt, _, _, evs, sink := newPrimedEventRuntime(t)
	ctx := context.Background()

	// Turno 1: nace el evento y el Prime declara su efecto.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero una torta", "wamid.c1")); err != nil {
		t.Fatalf("turno de arranque: %v", err)
	}
	eventID := evs.alive()[0].ID
	// Turno 2: el Step transiciona a la hoja "ventas" ⇒ el flujo termina y el
	// evento se cierra en el MISMO turno en el que se declara cart_closed.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.c2")); err != nil {
		t.Fatalf("turno de cierre: %v", err)
	}

	if got := len(evs.alive()); got != 0 {
		t.Fatalf("el cierre natural debe dejar 0 eventos vivos, hay %d", got)
	}
	ecs := sink.all()
	effs := sink.effectsAll()
	// Desde T5.4 (Plan 043 · D2) el turno 1 despacha event_started+item_added y el
	// turno 2 despacha event_closed (closeIfFinished, ANTES del Save) seguido del
	// cart_closed que declaró el Step: CUATRO efectos en total. El que importa a
	// este test —el efecto del Step que cierra el flujo— se localiza por NOMBRE.
	if len(ecs) != 4 {
		t.Fatalf("deben haber llegado 4 efectos (event_started+item_added del turno 1, event_closed+cart_closed del turno 2), llegaron %d", len(ecs))
	}
	if effs[2].Name != runtime.EffectEventClosed {
		t.Fatalf("el tercero debe ser %q (T5.4, cierre natural), llegó %q", runtime.EffectEventClosed, effs[2].Name)
	}
	if effs[3].Name != "cart_closed" {
		t.Fatalf("el cuarto debe ser el cart_closed del Step, llegó %q", effs[3].Name)
	}
	if ecs[3].EventID != eventID {
		t.Fatalf("el efecto del turno que cierra llegó con EventID %q, quiero %q (¿se leyó st.EventID después de closeIfFinished?)", ecs[3].EventID, eventID)
	}
}

// TestEffectContext_LosEfectosDeCicloDeVidaTambienLlevanElEventID (hallazgo #16,
// Ola 6 · E7): TestEffectContext_ElTurnoQueCierraConservaElEventID ya monta el
// escenario de los CUATRO efectos (event_started, item_added, event_closed,
// cart_closed) pero solo comprobaba ecs[1] y ecs[3] —los del MÓDULO—; nadie
// comprobaba ecs[0] ni ecs[2] —los del CICLO DE VIDA—. Verificado por mutación:
// cambiar `EventID: ev.ID` por otro campo en emitEventEffect (event_effects.go) NO lo
// mataba NINGÚN test de NINGÚN fichero, ni contra Postgres real, porque hoy ese
// campo está MUERTO río abajo (flow_events no tiene columna event_id, appendDecision
// filtra por una whitelist de tres que no incluye los event_*, y ningún proyector los
// reconoce). Por eso este test se vende por lo que es: un test de CONTRATO que
// protege el día que aparezca el lector (T6.5, en construcción EN PARALELO a esta
// tarea), no una regresión de HOY — no hay nada hoy que lo consuma.
func TestEffectContext_LosEfectosDeCicloDeVidaTambienLlevanElEventID(t *testing.T) {
	rt, _, _, evs, sink := newPrimedEventRuntime(t)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero una torta", "wamid.e7-1")); err != nil {
		t.Fatalf("turno de arranque: %v", err)
	}
	eventID := evs.alive()[0].ID
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.e7-2")); err != nil {
		t.Fatalf("turno de cierre: %v", err)
	}

	ecs := sink.all()
	effs := sink.effectsAll()
	if len(ecs) != 4 {
		t.Fatalf("precondición: se esperan 4 efectos (ver TestEffectContext_ElTurnoQueCierraConservaElEventID), llegaron %d", len(ecs))
	}
	if effs[0].Name != runtime.EffectEventStarted || ecs[0].EventID != eventID {
		t.Fatalf("#16: ecs[0] (%q) debe llevar el EventID del evento recién nacido (%q), llegó %q",
			effs[0].Name, eventID, ecs[0].EventID)
	}
	if effs[2].Name != runtime.EffectEventClosed || ecs[2].EventID != eventID {
		t.Fatalf("#16: ecs[2] (%q) debe llevar el EventID del evento que cierra (%q), llegó %q",
			effs[2].Name, eventID, ecs[2].EventID)
	}
}

// TestEffectContext_ArranquePlanoSinEventoLlevaVacio (T4.5.1, el contraste): el
// arranque por API (rt.Start) no pare evento y sus turnos despachan con EventID ""
// — el "" es un valor con significado («no hay evento»), no un descuido.
func TestEffectContext_ArranquePlanoSinEventoLlevaVacio(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(primedModule{})
	sink := &ecSink{}
	rt := runtime.New(repo, engine.New(reg), &fakeSender{}, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(), runtime.WithEventSink(sink))
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	ecs := sink.all()
	if len(ecs) != 1 {
		t.Fatalf("el Step debe declarar UN efecto, llegaron %d", len(ecs))
	}
	if ecs[0].EventID != "" {
		t.Fatalf("sin plano de eventos el EventID debe ser \"\", llegó %q", ecs[0].EventID)
	}
}
