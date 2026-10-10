package runtime_test

// incoming_aggregation_test.go: AG-7, el puente del entrante con la ventana de captación
// (incoming.go §5, «Ventana de captación»). El mensaje del turno se ofrece al agregador en
// tres sitios —el avance normal, el mensaje que ABRE un evento por el disparador y el
// reinicio por reanudación—, con el evento del turno, nunca en un turno cortado y nunca dos
// veces. Solo se ve a través del Runtime con WithAggregator: aquí el agregador es el de
// verdad, sobre el gemelo en memoria de captacion/intake. Parte de incoming_test.go (E-13).

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// incomingCountingFeatures es el resolver de features del guion con las preguntas contadas.
// Cableado SOLO en el agregador, cada pregunta es un entrante que llegó a ofrecerse: Observe
// consulta la feature una vez por entrante admitido, antes de descartar un repetido.
type incomingCountingFeatures struct {
	entitlements.Resolver
	mu   sync.Mutex
	asks int
}

// Has implementa entitlements.Resolver.
func (f *incomingCountingFeatures) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	f.mu.Lock()
	f.asks++
	f.mu.Unlock()
	return f.Resolver.Has(ctx, tenantID, feature)
}

func (f *incomingCountingFeatures) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asks
}

// incomingWindows es lo que un guion con agregador deja ver: el almacén de ventanas y cuántas
// veces se le ofreció un entrante.
type incomingWindows struct {
	jobs   *intake.MemoryStore
	offers *incomingCountingFeatures
}

// incomingWithAggregator cablea un IntakeAggregator de verdad sobre el gemelo en memoria de
// intake_jobs, con el reloj del guion, y quita la bienvenida (que también cuelga de
// `llm_intake` y metería un saliente más en cada guion).
func incomingWithAggregator(w *incomingWindows) harnessOption {
	return withOptions(func(h *harness) []runtime.Option {
		w.jobs = intake.NewMemoryStore(h.clock.Now)
		w.offers = &incomingCountingFeatures{Resolver: h.features}
		aggregator := runtime.NewIntakeAggregator(h.log, w.jobs, h.repo, w.offers, runtime.WithAggregatorClock(h.clock.Now))
		return []runtime.Option{runtime.WithAggregator(aggregator), runtime.WithWelcomeStore(nil)}
	})
}

// incomingWantWindow exige que haya UNA sola ventana, la del evento, con esas referencias en
// ese orden.
func incomingWantWindow(t *testing.T, h *harness, w *incomingWindows, eventID string, refs ...string) {
	t.Helper()
	jobs := w.jobs.Jobs()
	want := intake.WindowKey{TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone), EventID: eventID}
	if len(jobs) != 1 || jobs[0].Key != want || !slices.Equal(jobs[0].SourceRefs, refs) {
		t.Fatalf("ventanas = %+v, quería una del evento %s con las referencias %v\nlog:\n%s", jobs, eventID, refs, h.log.dump())
	}
}

// incomingRestartPolicy es una modules.ResumePolicy de guion: mientras restart esté puesto,
// pide reiniciar la conversación con ese aviso y esos efectos.
type incomingRestartPolicy struct {
	mu      sync.Mutex
	restart bool
	notice  string
	effects []modules.Effect
}

// Restart implementa modules.ResumePolicy.
func (p *incomingRestartPolicy) Restart(context.Context, string, string, map[string]any) (bool, string, []modules.Effect, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restart, p.notice, slices.Clone(p.effects), nil
}

// Seed implementa modules.ResumePolicy: no siembra nada.
func (p *incomingRestartPolicy) Seed(context.Context, string, map[string]any) error { return nil }

func (p *incomingRestartPolicy) arm() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restart = true
}

const incomingRestartNotice = "Empezamos de nuevo-qzx"

// incomingWithRestartPolicy registra la política para los nodos de menú.
func incomingWithRestartPolicy(p *incomingRestartPolicy) harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithResumePolicy(model.NodeTypeMenu, p)}
	})
}

// TestHandleIncoming_Aggregation_TheMessageThatOpensAnEvent: AG-7, llamante 2. El mensaje que
// ABRE un evento por el disparador entra en la ventana de ESE evento.
func TestHandleIncoming_Aggregation_TheMessageThatOpensAnEvent(t *testing.T) {
	w := &incomingWindows{}
	h := newHarness(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))

	h.say("wa-open", incomingEventKeyword)

	st := incomingWantNode(t, h, "root")
	incomingWantWindow(t, h, w, st.EventID, "wa-open")
	if got := w.offers.asked(); got != 1 {
		t.Errorf("el entrante se ofreció al agregador %d veces, quería 1", got)
	}
}

// TestHandleIncoming_Aggregation_TheSwitchToAnAliveEvent: AG-7, llamante 2. Si el disparador
// CONMUTA hacia un evento que ya estaba vivo, el mensaje entra en la ventana de ese evento.
func TestHandleIncoming_Aggregation_TheSwitchToAnAliveEvent(t *testing.T) {
	w := &incomingWindows{}
	h := newHarness(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)
	version := h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))
	alive := h.seedEvent(trigger.EventKindCart, incomingFlowID, version)

	h.say("wa-back", incomingEventKeyword)

	incomingWantWindow(t, h, w, alive.ID, "wa-back")
	if got := w.offers.asked(); got != 1 {
		t.Errorf("el entrante se ofreció al agregador %d veces, quería 1", got)
	}
}

// TestHandleIncoming_Aggregation_TheNormalAdvance: AG-7, llamante 1. Cada turno que avanza
// dentro del evento añade su mensaje a la misma ventana, una vez.
func TestHandleIncoming_Aggregation_TheNormalAdvance(t *testing.T) {
	w := &incomingWindows{}
	h, eventID := incomingLiveEvent(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)

	h.say("wa-2", "quiero dos empanadas-qzx")
	h.say("wa-3", "1")

	incomingWantWindow(t, h, w, eventID, "wa-2", "wa-3")
	if got := w.offers.asked(); got != 3 {
		t.Errorf("ofrecimientos al agregador = %d, quería 3: uno por entrante (el que abrió y los dos avances)", got)
	}
}

// TestHandleIncoming_Aggregation_TheRestartByResume: AG-7, llamante 3. El mensaje que provoca
// un reinicio por reanudación también entra en la ventana del evento activo.
func TestHandleIncoming_Aggregation_TheRestartByResume(t *testing.T) {
	w := &incomingWindows{}
	policy := &incomingRestartPolicy{notice: incomingRestartNotice}
	h, eventID := incomingLiveEvent(t, incomingWithAggregator(w), incomingWithRestartPolicy(policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	policy.arm()

	h.say("wa-2", "1")

	// El turno NO avanzó por el engine: la política lo reinició en el nodo inicial.
	incomingWantNode(t, h, "root")
	if texts := h.texts(); len(texts) < 2 || texts[1] != incomingRestartNotice {
		t.Fatalf("textos enviados = %q, quería el aviso del reinicio tras la pantalla de apertura", texts)
	}
	incomingWantWindow(t, h, w, eventID, "wa-2")
	if got := w.offers.asked(); got != 2 {
		t.Errorf("ofrecimientos al agregador = %d, quería 2: el que abrió (sin feature aún) y el reinicio", got)
	}
}

// TestHandleIncoming_Aggregation_NothingWithoutAnEvent: sin evento no hay ventana. Ni el
// entrante que el motor ignora ni una conversación plana abren ninguna.
func TestHandleIncoming_Aggregation_NothingWithoutAnEvent(t *testing.T) {
	w := &incomingWindows{}
	h := newHarness(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))

	h.say("wa-1", "texto que no casa-qzx")
	h.say("wa-2", incomingKeyword)
	h.say("wa-3", "1")

	incomingWantNode(t, h, "sub")
	if jobs := w.jobs.Jobs(); len(jobs) != 0 {
		t.Errorf("ventanas = %+v, sin evento no quería ninguna", jobs)
	}
}

// TestHandleIncoming_Aggregation_ACutTurnIsNotOffered: RT-10. Un turno cortado por el sink
// durable no entra en la ventana de captación; el turno siguiente, ya sano, sí.
func TestHandleIncoming_Aggregation_ACutTurnIsNotOffered(t *testing.T) {
	w := &incomingWindows{}
	h, sink, eventID := incomingLiveSurvey(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)
	sink.set(true)

	h.say("wa-cut", "1")

	if jobs := w.jobs.Jobs(); len(jobs) != 0 {
		t.Fatalf("ventanas tras el turno cortado = %+v, no quería ninguna", jobs)
	}
	if got := w.offers.asked(); got != 1 {
		t.Errorf("ofrecimientos al agregador = %d, quería solo el del mensaje que abrió la encuesta", got)
	}

	sink.set(false)
	h.say("wa-ok", "1")
	incomingWantWindow(t, h, w, eventID, "wa-ok")
}

// TestHandleIncoming_Aggregation_AFailureNeverCutsTheTurn: la ventana de captación no devuelve
// error ni corta el turno: si no se puede escribir, el cliente recibe su respuesta igual.
func TestHandleIncoming_Aggregation_AFailureNeverCutsTheTurn(t *testing.T) {
	w := &incomingWindows{}
	h, _ := incomingLiveEvent(t, incomingWithAggregator(w))
	h.enableFeature(entitlements.FeatureLLMIntake)
	w.jobs.FailOpenWith(errors.New("intake-jobs-down-qzx"))

	h.say("wa-2", "1")

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
}
