package runtime_test

// runtime_engine_options_test.go prueba RT-12 en las dos opciones que no lo cumplían en el
// viejo: WithEventSink(nil) y WithResumePolicy(tipo, nil) guardaban el nil y el pánico llegaba
// después, en el primer turno que pasara por ahí. Divergencia deliberada del viejo (D-F8-16,
// hallazgo 34e): un nil se ignora, como en el resto de las opciones.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// WithEventSink (RT-12) · Un sink nil no entra en el fan-out: el que sí es de verdad recibe el
// efecto y el turno termina.
func TestWithEventSink_NilIsIgnored(t *testing.T) {
	log := &resumeSinkLog{}
	h := newHarness(t,
		withModules(resumeProbe{effects: []modules.Effect{resumeEffect("e1")}}),
		withSinks(func(*harness) []runtime.EventSink { return nil }),
		withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{
				runtime.WithEventSink(nil),
				runtime.WithEventSink(&resumeSink{name: "a", log: log}),
				runtime.WithEventSink(nil),
			}
		}),
	)
	h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if got := log.order(); !resumeEqual(got, []string{"a:e1"}) {
		t.Errorf("entregas = %v, quería el efecto una vez, en el único sink de verdad", got)
	}
	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería la respuesta del módulo: el turno tenía que terminar", got)
	}
}

// WithEventSink (RT-12) · Solo con sinks nil el motor queda como sin la opción: el fan-out es
// el LogSink por defecto y el turno termina.
func TestWithEventSink_OnlyNilLeavesTheDefaultSink(t *testing.T) {
	h := newHarness(t,
		withModules(resumeProbe{effects: []modules.Effect{resumeEffect("e1")}}),
		withSinks(func(*harness) []runtime.EventSink { return nil }),
		withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithEventSink(nil)}
		}),
	)
	h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería la respuesta del módulo: el turno tenía que terminar", got)
	}
}

// WithResumePolicy (RT-12) · Una política nil no registra nada ni borra la que ya había para
// ese tipo.
func TestWithResumePolicy_NilIsIgnored(t *testing.T) {
	policy := &resumePolicy{}
	h := resumeHarness(t, resumeProbe{}, nil,
		runtime.WithResumePolicy(resumeProbeType, policy),
		runtime.WithResumePolicy(resumeProbeType, nil),
	)
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if restarts, seeds := policy.calls(); restarts != 1 || seeds != 1 {
		t.Errorf("política registrada antes del nil: Restart=%d, Seed=%d; quería una de cada", restarts, seeds)
	}
}

// WithResumePolicy (RT-12) · Solo con una política nil, el tipo queda sin política: el turno
// sigue hacia el módulo, como sin la opción.
func TestWithResumePolicy_OnlyNilMeansNoPolicy(t *testing.T) {
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, nil))
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería solo la respuesta del módulo", got)
	}
}
