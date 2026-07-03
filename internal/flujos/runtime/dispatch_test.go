package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// --- dobles para el dispatch de efectos ---

// fakeSink registra cada efecto recibido y puede devolver un error configurable.
type fakeSink struct {
	mu       sync.Mutex
	received []modules.Effect
	err      error
}

func (f *fakeSink) Handle(_ context.Context, _ runtime.EffectContext, eff modules.Effect) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, eff)
	return f.err
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// effectModule sustituye al módulo "menu" en un engine de test: al recibir input
// transiciona a "ventas" (hoja message de sampleFlow) y DECLARA los efectos dados.
// Registrado bajo Type()=="menu", reutiliza sampleFlow() sin tocar el módulo real.
type effectModule struct{ effects []modules.Effect }

func (effectModule) Type() string        { return model.NodeTypeMenu }
func (effectModule) WaitsForInput() bool { return true }

func (effectModule) Render(node model.Node, _ model.Content) []string {
	return []string{node.Prompt}
}

func (m effectModule) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	next := "ventas"
	return modules.Result{Next: &next, Vars: conv.Vars, Effects: m.effects}
}

func newEffectEngine(effects []modules.Effect) *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(effectModule{effects: effects})
	return engine.New(reg)
}

func sampleEffect() modules.Effect {
	return modules.Effect{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer_code": "a"}}
}

// startAndStep siembra la conversación (Start) y la avanza con un entrante,
// devolviendo el error de HandleIncoming.
func startAndStep(t *testing.T, rt *runtime.Runtime) error {
	t.Helper()
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.1"))
}

// TestDispatch_DosSinks_AmbosReciben: con dos EventSink inyectados, ambos reciben
// el efecto declarado por el módulo al avanzar.
func TestDispatch_DosSinks_AmbosReciben(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	s1, s2 := &fakeSink{}, &fakeSink{}
	rt := runtime.New(repo, newEffectEngine([]modules.Effect{sampleEffect()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithEventSink(s1), runtime.WithEventSink(s2))

	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if s1.count() != 1 || s2.count() != 1 {
		t.Fatalf("ambos sinks deberían recibir 1 efecto: s1=%d s2=%d", s1.count(), s2.count())
	}
}

// TestDispatch_SinkConError_NoAborta: si un sink devuelve error, se LOGUEA y NO
// aborta: los demás sinks reciben igual el efecto y el flujo continúa (err nil).
func TestDispatch_SinkConError_NoAborta(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	boom := &fakeSink{err: errors.New("boom")}
	ok := &fakeSink{}
	rt := runtime.New(repo, newEffectEngine([]modules.Effect{sampleEffect()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithEventSink(boom), runtime.WithEventSink(ok))

	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("un sink con error NO debe abortar el avance: %v", err)
	}
	if boom.count() != 1 || ok.count() != 1 {
		t.Fatalf("todos los sinks deben recibir el efecto pese al error: boom=%d ok=%d", boom.count(), ok.count())
	}
}

// TestDispatch_DefaultLogSink_EffectsVacio_NoPersiste (NO-REGRESIÓN): con el sink
// default (LogSink) y un flujo real (menú, sin emisores de efectos), el dispatch
// no itera y no se persiste nada en flow_events/survey_results.
func TestDispatch_DefaultLogSink_EffectsVacio_NoPersiste(t *testing.T) {
	rt, repo, _, _ := newRuntime(t) // engine real (menu), default LogSink
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 0 {
		t.Fatalf("no-regresión: no debería haber flow_events, hay %d", len(evs))
	}
	if res := repo.SurveyResults(); len(res) != 0 {
		t.Fatalf("no-regresión: no debería haber survey_results (menú puro), hay %d", len(res))
	}
}
