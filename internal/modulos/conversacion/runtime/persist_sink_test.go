package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// persistCtxKey marca el contexto de la llamada para comprobar que llega tal cual al almacén,
// al hilo y al proyector.
type persistCtxKey struct{}

func persistCtx() context.Context {
	return context.WithValue(context.Background(), persistCtxKey{}, "marked")
}

func persistMarked(ctx context.Context) bool { return ctx.Value(persistCtxKey{}) == "marked" }

// persistTrace apunta, en orden, qué colaborador recibió cada llamada: es lo que deja afirmar
// «outbox, luego hilo, luego proyección».
type persistTrace struct{ steps []string }

func (tr *persistTrace) add(step string) { tr.steps = append(tr.steps, step) }

// persistOutbox es el almacén del outbox: el gemelo en memoria de conversacion/store, con un
// fallo inyectable delante.
type persistOutbox struct {
	repo   *store.MemoryRepository
	trace  *persistTrace
	err    error
	marked []bool
}

func (o *persistOutbox) InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error {
	o.trace.add("outbox")
	o.marked = append(o.marked, persistMarked(ctx))
	if o.err != nil {
		return o.err
	}
	return o.repo.InsertFlowEvent(ctx, ev)
}

// persistProjection es una llamada a Project tal como llegó.
type persistProjection struct {
	marked bool
	meta   modules.EffectMeta
	eff    modules.Effect
	// payload es una copia del Payload en el momento de la llamada.
	payload map[string]any
}

// persistProjector reconoce los nombres de `names`. Si annotate no es nil, se llama con el
// Payload recibido (es como el proyector del carrito anota el intake_id).
type persistProjector struct {
	label    string
	trace    *persistTrace
	names    []string
	err      error
	annotate func(payload map[string]any)
	asked    []string
	calls    []persistProjection
}

func (p *persistProjector) Handles(effectName string) bool {
	p.asked = append(p.asked, effectName)
	return slices.Contains(p.names, effectName)
}

func (p *persistProjector) Project(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	p.trace.add("project:" + p.label)
	p.calls = append(p.calls, persistProjection{
		marked: persistMarked(ctx), meta: meta, eff: eff, payload: maps.Clone(eff.Payload),
	})
	if p.annotate != nil {
		p.annotate(eff.Payload)
	}
	return p.err
}

// persistDecision es una llamada a AppendDecision tal como llegó.
type persistDecision struct {
	marked  bool
	eventID string
	payload string
}

// persistThread es el hilo del evento.
type persistThread struct {
	trace *persistTrace
	err   error
	calls []persistDecision
}

func (th *persistThread) AppendDecision(ctx context.Context, eventID string, payload []byte) error {
	th.trace.add("thread")
	th.calls = append(th.calls, persistDecision{marked: persistMarked(ctx), eventID: eventID, payload: string(payload)})
	return th.err
}

// Aserciones de compilación: el sink es un EventSink, y los dobles son sus puertos.
var (
	_ error             = ErrMaterializationFailed // sus casos: persist_sink_failures_test.go
	_ EventSink         = (*PersistSink)(nil)
	_ DecisionAppender  = (*persistThread)(nil)
	_ modules.Projector = (*persistProjector)(nil)
)

// persistRig es un sink con sus tres colaboradores y la traza común.
type persistRig struct {
	trace  *persistTrace
	outbox *persistOutbox
	thread *persistThread
	first  *persistProjector
	second *persistProjector
	sink   *PersistSink
}

// newPersistRig monta el sink con DOS proyectores (first reconoce `firstNames`; second,
// `secondNames`) y con el hilo cableado.
func newPersistRig(firstNames, secondNames []string) *persistRig {
	trace := &persistTrace{}
	rig := &persistRig{
		trace:  trace,
		outbox: &persistOutbox{repo: store.NewMemoryRepository(), trace: trace},
		thread: &persistThread{trace: trace},
		first:  &persistProjector{label: "first", trace: trace, names: firstNames},
		second: &persistProjector{label: "second", trace: trace, names: secondNames},
	}
	rig.sink = NewPersistSink(rig.outbox, rig.first, rig.second).WithDecisionThread(rig.thread)
	return rig
}

func (r *persistRig) events() []store.FlowEvent { return r.outbox.repo.FlowEvents() }

// persistContext es la conversación del efecto: todos los campos distintos entre sí.
func persistContext() EffectContext {
	return EffectContext{
		TenantID: "tenant-1", ContactID: "contact-opaque", SessionID: "session-9",
		FlowID: "flow-1", FlowVersion: 4, EventID: "event-7", Durable: true,
	}
}

// persistItemAdded es una decisión del carrito, sin claves privadas.
func persistItemAdded() modules.Effect {
	return modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "A1", "qty": 2}}
}

func persistJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("el payload de la decisión no es JSON de objeto: %v (%q)", err, raw)
	}
	return out
}

// TestNewPersistSink_NeverNilAndChainsTheThread: el constructor devuelve un sink, con
// proyectores y sin ellos, y WithDecisionThread devuelve EL MISMO puntero.
func TestNewPersistSink_NeverNilAndChainsTheThread(t *testing.T) {
	trace := &persistTrace{}
	outbox := &persistOutbox{repo: store.NewMemoryRepository(), trace: trace}
	bare := NewPersistSink(outbox)
	if bare == nil {
		t.Fatal("NewPersistSink sin proyectores devolvió nil")
	}
	if got := bare.WithDecisionThread(&persistThread{trace: trace}); got != bare {
		t.Error("WithDecisionThread no devolvió el mismo sink: no se puede encadenar en el cableado")
	}
	if got := bare.WithDecisionThread(nil); got != bare {
		t.Error("WithDecisionThread(nil) no devolvió el mismo sink")
	}
	if len(outbox.repo.FlowEvents()) != 0 || len(trace.steps) != 0 {
		t.Errorf("construir el sink tocó a sus colaboradores: %v", trace.steps)
	}
}

// TestPersistSink_RunsInTheProjectPhase es RT-18: el sink no declara fase, así que corre en la de
// proyección, antes que el WebhookSink (PhaseNotify), que lee lo que el proyector anota.
func TestPersistSink_RunsInTheProjectPhase(t *testing.T) {
	rig := newPersistRig(nil, nil)
	if _, phased := any(rig.sink).(PhasedSink); phased {
		t.Fatal("PersistSink implementa PhasedSink: debe correr en la fase por defecto (PhaseProject)")
	}
	notify := NewWebhookSink(nil, "cart_closed", nil, nil).Phase()
	if PhaseProject >= notify {
		t.Errorf("PhaseProject (%d) no es anterior a la fase del WebhookSink (%d)", PhaseProject, notify)
	}
}

// TestPersistSink_Handle_WritesTheOutboxRow: una fila en flow_events con los cuatro campos de la
// conversación y los tres del efecto; sin proyector que lo reconozca, nada más, y nil.
func TestPersistSink_Handle_WritesTheOutboxRow(t *testing.T) {
	rig := newPersistRig([]string{"survey_answer"}, nil)
	eff := modules.Effect{Kind: "event", Name: "category_selected", Payload: map[string]any{"category": "bebidas"}}

	if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}

	want := []store.FlowEvent{{
		TenantID: "tenant-1", ContactID: "contact-opaque", FlowID: "flow-1", FlowVersion: 4,
		Kind: "event", Name: "category_selected", Payload: map[string]any{"category": "bebidas"},
	}}
	if got := rig.events(); !reflect.DeepEqual(got, want) {
		t.Errorf("flow_events = %+v, quería %+v", got, want)
	}
	if !slices.Equal(rig.outbox.marked, []bool{true}) {
		t.Errorf("el almacén no recibió el ctx de la llamada: %v", rig.outbox.marked)
	}
	if len(rig.first.calls)+len(rig.second.calls) != 0 {
		t.Error("se proyectó un efecto que ningún proyector reconoce")
	}
	if len(rig.thread.calls) != 0 {
		t.Errorf("un efecto de navegación escribió %d decisiones en el hilo", len(rig.thread.calls))
	}
}

// TestPersistSink_Handle_LifecycleEffectKeepsItsKind: el Kind se escribe tal cual; el de los
// efectos de ciclo de vida del evento es "event" y ninguno es una decisión.
func TestPersistSink_Handle_LifecycleEffectKeepsItsKind(t *testing.T) {
	rig := newPersistRig(nil, nil)
	eff := modules.Effect{Kind: "event", Name: EffectEventStarted, Payload: map[string]any{"history_id": "cart-2026-01-02-0304", "kind": "cart"}}

	if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	got := rig.events()
	if len(got) != 1 || got[0].Kind != "event" || got[0].Name != "event_started" {
		t.Fatalf("flow_events = %+v, quería una fila kind=event name=event_started", got)
	}
	if len(rig.thread.calls) != 0 {
		t.Error("un efecto de ciclo de vida entró al hilo como decisión")
	}
}

// TestPersistSink_Handle_NilPayloadDoesNotPanic: el sink no lee el contenido del payload.
func TestPersistSink_Handle_NilPayloadDoesNotPanic(t *testing.T) {
	rig := newPersistRig([]string{"survey_answer"}, nil)
	if err := rig.sink.Handle(persistCtx(), persistContext(), modules.Effect{Kind: "persist", Name: "survey_answer"}); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	if got := rig.events(); len(got) != 1 || got[0].Payload != nil {
		t.Errorf("flow_events = %+v, quería una fila con el payload nil tal cual", got)
	}
	if len(rig.first.calls) != 1 {
		t.Errorf("el proyector recibió %d llamadas, quería 1", len(rig.first.calls))
	}
}

// TestPersistSink_Handle_ProjectsWithTheFullMeta: el proyector que reconoce el nombre recibe el
// ctx, los seis campos del EffectContext y el efecto entero; lo que anota en el Payload lo ve el
// llamante (es el mismo mapa: así llega el intake_id al WebhookSink).
func TestPersistSink_Handle_ProjectsWithTheFullMeta(t *testing.T) {
	rig := newPersistRig([]string{"cart_closed"}, nil)
	rig.first.annotate = func(payload map[string]any) { payload["intake_id"] = "intake-abc" }
	eff := modules.Effect{Kind: "persist", Name: "cart_closed", Payload: map[string]any{"total": 19.8}}

	if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	if len(rig.first.calls) != 1 {
		t.Fatalf("el proyector recibió %d llamadas, quería 1", len(rig.first.calls))
	}
	call := rig.first.calls[0]
	wantMeta := modules.EffectMeta{
		TenantID: "tenant-1", ContactID: "contact-opaque", SessionID: "session-9",
		FlowID: "flow-1", FlowVersion: 4, EventID: "event-7",
	}
	if call.meta != wantMeta {
		t.Errorf("meta = %+v, quería %+v", call.meta, wantMeta)
	}
	if !call.marked {
		t.Error("el proyector no recibió el ctx de la llamada")
	}
	if call.eff.Kind != "persist" || call.eff.Name != "cart_closed" || !reflect.DeepEqual(call.payload, map[string]any{"total": 19.8}) {
		t.Errorf("el proyector recibió %+v con payload %v, quería el efecto entero", call.eff, call.payload)
	}
	if eff.Payload["intake_id"] != "intake-abc" {
		t.Errorf("la anotación del proyector no se ve en el efecto del llamante: %v", eff.Payload)
	}
	if got := rig.events(); len(got) != 1 || got[0].Payload["intake_id"] != nil {
		t.Errorf("flow_events = %+v, quería la fila escrita ANTES de la anotación del proyector", got)
	}
}

// TestPersistSink_Handle_OnlyTheFirstMatchingProjectorRuns: con dos proyectores que reconocen el
// mismo nombre, se llama al primero registrado y al segundo ni se le pregunta. Con uno que no lo
// reconoce delante, se llega al segundo.
func TestPersistSink_Handle_OnlyTheFirstMatchingProjectorRuns(t *testing.T) {
	both := newPersistRig([]string{"cart_closed"}, []string{"cart_closed"})
	if err := both.sink.Handle(persistCtx(), persistContext(), modules.Effect{Kind: "persist", Name: "cart_closed"}); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	if len(both.first.calls) != 1 || len(both.second.calls) != 0 || len(both.second.asked) != 0 {
		t.Errorf("first=%d llamadas, second=%d llamadas y %d preguntas; quería 1, 0 y 0",
			len(both.first.calls), len(both.second.calls), len(both.second.asked))
	}

	later := newPersistRig([]string{"survey_answer"}, []string{"cart_closed"})
	if err := later.sink.Handle(persistCtx(), persistContext(), modules.Effect{Kind: "persist", Name: "cart_closed"}); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	if len(later.first.calls) != 0 || len(later.second.calls) != 1 {
		t.Errorf("first=%d, second=%d llamadas; quería 0 y 1", len(later.first.calls), len(later.second.calls))
	}
	if !slices.Equal(later.first.asked, []string{"cart_closed"}) {
		t.Errorf("al primer proyector se le preguntó por %v, quería el nombre del efecto", later.first.asked)
	}
}

// TestPersistSink_Handle_PrivateKindSkipsTheOutbox: un efecto KindPrivate no se escribe en
// flow_events ni en el hilo —aunque su nombre fuera una decisión— y va entero al proyector.
func TestPersistSink_Handle_PrivateKindSkipsTheOutbox(t *testing.T) {
	for _, name := range []string{"buyer_data_captured", "note_added"} {
		t.Run(name, func(t *testing.T) {
			rig := newPersistRig([]string{name}, nil)
			eff := modules.Effect{Kind: modules.KindPrivate, Name: name, Payload: map[string]any{"name": "Ana Zzq"}}

			if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
				t.Fatalf("Handle devolvió %v, quería nil", err)
			}
			if got := rig.events(); len(got) != 0 {
				t.Errorf("un efecto privado dejó %d filas en flow_events: %+v", len(got), got)
			}
			if len(rig.thread.calls) != 0 {
				t.Errorf("un efecto privado dejó %d decisiones en claro en el hilo", len(rig.thread.calls))
			}
			if len(rig.first.calls) != 1 || rig.first.calls[0].payload["name"] != "Ana Zzq" {
				t.Errorf("el proyector no recibió el efecto privado entero: %+v", rig.first.calls)
			}
			if !slices.Equal(rig.trace.steps, []string{"project:first"}) {
				t.Errorf("traza = %v, quería solo la proyección", rig.trace.steps)
			}
		})
	}
}

// TestPersistSink_Handle_PrunesPrivateKeys: las claves privadas no llegan al outbox ni al hilo, el
// proyector sí las recibe y el Payload original no se muta.
func TestPersistSink_Handle_PrunesPrivateKeys(t *testing.T) {
	rig := newPersistRig([]string{"note_added"}, nil)
	eff := modules.Effect{
		Kind: "persist", Name: "note_added",
		Payload:     map[string]any{"scope": "order", "length": 12, "customer_note": "sin cebolla zzq"},
		PrivateKeys: []string{"customer_note"},
	}

	if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}
	public := map[string]any{"scope": "order", "length": 12}
	if got := rig.events(); len(got) != 1 || !reflect.DeepEqual(got[0].Payload, public) {
		t.Errorf("flow_events = %+v, quería el payload sin la clave privada", got)
	}
	if len(rig.thread.calls) != 1 {
		t.Fatalf("el hilo recibió %d decisiones, quería 1", len(rig.thread.calls))
	}
	if got := persistJSON(t, rig.thread.calls[0].payload); !reflect.DeepEqual(got, map[string]any{"scope": "order", "length": float64(12)}) {
		t.Errorf("decisión = %v, quería el payload público", got)
	}
	if len(rig.first.calls) != 1 || rig.first.calls[0].payload["customer_note"] != "sin cebolla zzq" {
		t.Errorf("el proyector no recibió la clave privada: %+v", rig.first.calls)
	}
	if eff.Payload["customer_note"] != "sin cebolla zzq" {
		t.Error("Handle mutó el Payload del efecto: la clave privada desapareció para el resto del fan-out")
	}
}

// TestPersistSink_Handle_WritesTheDecisionBeforeProjecting: los tres nombres de la lista cerrada
// dejan su payload público en el hilo del evento, después del outbox y ANTES de la proyección: lo
// que el proyector anota no entra en la decisión.
func TestPersistSink_Handle_WritesTheDecisionBeforeProjecting(t *testing.T) {
	for _, name := range []string{"item_added", "note_added", "survey_answer"} {
		t.Run(name, func(t *testing.T) {
			rig := newPersistRig([]string{name}, nil)
			rig.first.annotate = func(payload map[string]any) { payload["row_id"] = "generated" }
			eff := modules.Effect{Kind: "persist", Name: name, Payload: map[string]any{"code": "x1"}}

			if err := rig.sink.Handle(persistCtx(), persistContext(), eff); err != nil {
				t.Fatalf("Handle devolvió %v, quería nil", err)
			}
			if !slices.Equal(rig.trace.steps, []string{"outbox", "thread", "project:first"}) {
				t.Errorf("orden = %v, quería outbox, thread, project", rig.trace.steps)
			}
			if len(rig.thread.calls) != 1 {
				t.Fatalf("el hilo recibió %d decisiones, quería 1", len(rig.thread.calls))
			}
			call := rig.thread.calls[0]
			if call.eventID != "event-7" || !call.marked {
				t.Errorf("decisión en el evento %q (ctx propio=%v), quería event-7 con el ctx de la llamada", call.eventID, call.marked)
			}
			if got := persistJSON(t, call.payload); !reflect.DeepEqual(got, map[string]any{"code": "x1"}) {
				t.Errorf("decisión = %v, quería solo lo que el módulo declaró", got)
			}
		})
	}
}

// TestPersistSink_Handle_NoDecisionOutsideTheClosedList: navegación, ciclo de vida y dato personal
// no son decisiones, aunque haya evento vivo e hilo cableado.
func TestPersistSink_Handle_NoDecisionOutsideTheClosedList(t *testing.T) {
	names := []string{
		"cart_started", "category_selected", "item_viewed",
		"cart_closed", "cart_cancelled", "cart_expired",
		"buyer_data_captured", "Item_Added", "",
		EffectEventStarted, EffectEventClosed, EffectEventCancelled, EffectEventEscaped,
	}
	for _, name := range names {
		rig := newPersistRig(nil, nil)
		if err := rig.sink.Handle(persistCtx(), persistContext(), modules.Effect{Kind: "event", Name: name, Payload: map[string]any{"k": "v"}}); err != nil {
			t.Fatalf("%q: Handle devolvió %v, quería nil", name, err)
		}
		if len(rig.thread.calls) != 0 {
			t.Errorf("%q escribió una decisión en el hilo: no está en la lista cerrada", name)
		}
		if len(rig.events()) != 1 {
			t.Errorf("%q: flow_events tiene %d filas, quería 1", name, len(rig.events()))
		}
	}
}

// TestPersistSink_Handle_NoDecisionWithoutEventOrThread: sin evento vivo no hay hilo en el que
// escribir, y sin hilo cableado (o cableado con nil) no se escribe nada; el resto sigue igual.
func TestPersistSink_Handle_NoDecisionWithoutEventOrThread(t *testing.T) {
	t.Run("no live event", func(t *testing.T) {
		rig := newPersistRig([]string{"item_added"}, nil)
		ec := persistContext()
		ec.EventID = ""
		if err := rig.sink.Handle(persistCtx(), ec, persistItemAdded()); err != nil {
			t.Fatalf("Handle devolvió %v, quería nil", err)
		}
		if len(rig.thread.calls) != 0 {
			t.Error("se escribió una decisión sin evento vivo")
		}
		if len(rig.events()) != 1 || len(rig.first.calls) != 1 || rig.first.calls[0].meta.EventID != "" {
			t.Errorf("sin evento, el outbox y la proyección tienen que seguir: filas=%d, proyecciones=%+v", len(rig.events()), rig.first.calls)
		}
	})
	t.Run("thread not wired", func(t *testing.T) {
		trace := &persistTrace{}
		outbox := &persistOutbox{repo: store.NewMemoryRepository(), trace: trace}
		sink := NewPersistSink(outbox)
		if err := sink.Handle(persistCtx(), persistContext(), persistItemAdded()); err != nil {
			t.Fatalf("Handle sin hilo devolvió %v, quería nil", err)
		}
		if err := sink.WithDecisionThread(nil).Handle(persistCtx(), persistContext(), persistItemAdded()); err != nil {
			t.Fatalf("Handle con el hilo a nil devolvió %v, quería nil", err)
		}
		if got := len(outbox.repo.FlowEvents()); got != 2 {
			t.Errorf("flow_events tiene %d filas, quería 2", got)
		}
	})
}
