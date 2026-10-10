//go:build pendiente

package runtime_test

// resume_doubles_test.go son los dobles y ayudantes que comparten los tests de resume,
// start, send, exit_menu y runtime_engine (partidos de resume_test.go por E-13): el módulo
// sonda, el sink que apunta, la política de reanudación espía y las siembras de estado.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

const (
	resumeProbeType   = "probe-zzq"
	resumeProbeFlowID = "probe-flow-zzq"
	resumeProbeScreen = "Pantalla de la sonda-zzq"
	resumeProbeReply  = "Respuesta de la sonda-zzq"
	resumeNotice      = "Tu pedido anterior caducó-zzq"
	// resumeFailureNotice es el aviso de avería de incoming.go (RT-10), byte a byte.
	resumeFailureNotice = "No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."
)

// errResumeCause es la causa TRANSITORIA que los dobles inyectan.
var errResumeCause = errors.New("conexión caída-zzq")

// resumeMaterializationErr marca cause como fallo de materialización, igual que el PersistSink.
func resumeMaterializationErr(cause error) error {
	return fmt.Errorf("%w: %w", runtime.ErrMaterializationFailed, cause)
}

// resumePermanentErr es un fallo de materialización PERMANENTE (violación de integridad).
func resumePermanentErr() error {
	return resumeMaterializationErr(&pgconn.PgError{Code: "23502"})
}

// resumeProbe es un módulo de mentira que espera entrada: contesta siempre resumeProbeReply,
// declara los efectos que se le den y, con finish, termina el flujo. Con silent no contesta nada.
type resumeProbe struct {
	durable bool
	finish  bool
	silent  bool
	effects []modules.Effect
}

func (resumeProbe) Type() string                   { return resumeProbeType }
func (resumeProbe) WaitsForInput() bool            { return true }
func (p resumeProbe) ProducesDurableContent() bool { return p.durable }

// Render enseña la pantalla del nodo; un nodo sin pantalla no dice nada.
func (resumeProbe) Render(node model.Node, _ model.Content) []string {
	if node.Prompt == "" {
		return nil
	}
	return []string{node.Prompt}
}

func (p resumeProbe) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	res := modules.Result{Vars: conv.Vars, Outputs: []string{resumeProbeReply}, Effects: p.effects}
	if p.silent {
		res.Outputs = nil
	}
	if p.finish {
		next := model.NodeTerminal
		res.Next = &next
		res.Outcome = model.OutcomeCompleted
	}
	return res
}

// resumeProbeFlow es un flujo de un solo nodo sonda con esa pantalla.
func resumeProbeFlow(screen string) model.Flow {
	return model.Flow{
		FlowID:  resumeProbeFlowID,
		Initial: "root",
		Nodes:   map[string]model.Node{"root": {Type: resumeProbeType, Prompt: screen}},
	}
}

// resumeEffect es un efecto de persistencia con payload propio (mapa nuevo en cada llamada).
func resumeEffect(name string) modules.Effect {
	return modules.Effect{Kind: "persist", Name: name, Payload: map[string]any{}}
}

// resumeSinkCall es una entrega que vio un sink: quién, qué efecto y con qué contexto.
type resumeSinkCall struct {
	sink   string
	effect string
	ec     runtime.EffectContext
}

// resumeSinkLog apunta, en UN orden global, las entregas de todos los sinks que lo comparten.
type resumeSinkLog struct {
	mu    sync.Mutex
	calls []resumeSinkCall
}

func (l *resumeSinkLog) add(c resumeSinkCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, c)
}

// all devuelve las entregas en orden.
func (l *resumeSinkLog) all() []resumeSinkCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]resumeSinkCall(nil), l.calls...)
}

// order devuelve las entregas como "sink:efecto", en orden.
func (l *resumeSinkLog) order() []string {
	calls := l.all()
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.sink+":"+c.effect)
	}
	return out
}

// resumeSink es un EventSink que apunta cada entrega y contesta lo que diga fail (nil = bien).
// fail recibe el número de entrega de ESTE sink, desde 1, y el efecto.
type resumeSink struct {
	name string
	log  *resumeSinkLog
	fail func(attempt int, eff modules.Effect) error

	mu sync.Mutex
	n  int
}

func (s *resumeSink) Handle(_ context.Context, ec runtime.EffectContext, eff modules.Effect) error {
	s.log.add(resumeSinkCall{sink: s.name, effect: eff.Name, ec: ec})
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()
	if s.fail == nil {
		return nil
	}
	return s.fail(n, eff)
}

// resumeNotifySink es un resumeSink que declara PhaseNotify.
type resumeNotifySink struct{ *resumeSink }

func (resumeNotifySink) Phase() runtime.SinkPhase { return runtime.PhaseNotify }

// resumeFailFirst falla con err las primeras n entregas y después deja pasar.
func resumeFailFirst(n int, err error) func(int, modules.Effect) error {
	return func(attempt int, _ modules.Effect) error {
		if attempt <= n {
			return err
		}
		return nil
	}
}

// resumeFailAlways falla siempre con err.
func resumeFailAlways(err error) func(int, modules.Effect) error {
	return func(int, modules.Effect) error { return err }
}

// resumePolicy es una modules.ResumePolicy espía.
type resumePolicy struct {
	mu         sync.Mutex
	restart    bool
	notice     string
	effects    []modules.Effect
	restartErr error
	seedErr    error
	restarts   int
	seeds      int
	seedGotNil bool
}

func (p *resumePolicy) Restart(context.Context, string, string, map[string]any) (bool, string, []modules.Effect, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restarts++
	if p.restartErr != nil {
		return false, "", nil, p.restartErr
	}
	return p.restart, p.notice, p.effects, nil
}

func (p *resumePolicy) Seed(_ context.Context, _ string, vars map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seeds++
	if vars == nil {
		p.seedGotNil = true
		return p.seedErr
	}
	vars["page_size-zzq"] = "5"
	return p.seedErr
}

// calls devuelve cuántas veces se consultó Restart y Seed.
func (p *resumePolicy) calls() (restarts, seeds int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restarts, p.seeds
}

// resumeHarness monta un guion con la sonda registrada, esos sinks y esas opciones, y publica
// el flujo sonda (versión 1).
func resumeHarness(t *testing.T, probe modules.Module, sinks []runtime.EventSink, extra ...runtime.Option) *harness {
	t.Helper()
	h := newHarness(t,
		withModules(probe),
		withSinks(func(*harness) []runtime.EventSink { return sinks }),
		withOptions(func(*harness) []runtime.Option { return extra }),
	)
	h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	return h
}

// resumeSeed guarda una conversación VIVA del cliente del guion en la raíz del flujo sonda v1
// (mutate la ajusta antes de guardar) y devuelve lo guardado.
func resumeSeed(h *harness, mutate func(st *model.Conversation)) model.Conversation {
	h.t.Helper()
	st := model.Conversation{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		FlowID: resumeProbeFlowID, FlowVersion: 1, CurrentNode: "root", LastWaMessageID: "wa-previous",
	}
	if mutate != nil {
		mutate(&st)
	}
	if err := h.repo.Save(h.t.Context(), st); err != nil {
		h.t.Fatalf("sembrar la conversación viva: %v", err)
	}
	return st
}

// resumeSeedInEvent es resumeSeed con un evento `cart` vivo, activo y dueño.
func resumeSeedInEvent(h *harness, mutate func(st *model.Conversation)) (model.Conversation, events.Event) {
	h.t.Helper()
	ev := h.seedEvent(trigger.EventKindCart, resumeProbeFlowID, 1)
	st := resumeSeed(h, func(st *model.Conversation) {
		st.EventID, st.OwnerEventID = ev.ID, ev.ID
		if mutate != nil {
			mutate(st)
		}
	})
	return st, ev
}

// resumeLogLine busca la primera línea de ese nivel con ese mensaje exacto.
func resumeLogLine(h *harness, level, msg string) (logLine, bool) {
	for _, line := range h.log.at(level) {
		if line.msg == msg {
			return line, true
		}
	}
	return logLine{}, false
}

// resumeThread devuelve el hilo del evento como "rol:clase", en orden.
func resumeThread(h *harness, eventID string) []string {
	entries := h.thread(eventID)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, string(e.Role)+":"+string(e.Kind))
	}
	return out
}

// resumeEqual compara dos listas de cadenas (una vacía y una nil son iguales).
func resumeEqual(got, want []string) bool {
	return slices.Equal(got, want)
}
