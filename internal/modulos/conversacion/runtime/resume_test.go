//go:build pendiente

package runtime_test

// resume_test.go prueba, por HandleIncoming, la REANUDACIÓN por módulo (RS-1…RS-6 del
// contrato de resume.go) y el centinela ErrTurnCutBySinkFailure. El fan-out (FO-1…FO-7) va
// en resume_fanout_test.go y los dobles, en resume_doubles_test.go.

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// RS-1 · Sin política para el tipo del nodo actual: no se consulta nada, las Vars no se tocan
// y el turno sigue hacia el módulo.
func TestResume_NoPolicyForNodeTypeIsNoop(t *testing.T) {
	policy := &resumePolicy{restart: true, notice: resumeNotice}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy("other-type-zzq", policy))
	resumeSeed(h, func(st *model.Conversation) { st.Vars = map[string]any{"kept-zzq": "yes"} })

	h.say("wa-1", "hola")

	if restarts, seeds := policy.calls(); restarts != 0 || seeds != 0 {
		t.Errorf("la política de OTRO tipo se consultó (Restart=%d, Seed=%d); no debía", restarts, seeds)
	}
	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería solo la respuesta del módulo", got)
	}
	st, _ := h.state()
	if st.Vars["kept-zzq"] != "yes" || len(st.Vars) != 1 {
		t.Errorf("Vars = %v, quería las de antes sin tocar", st.Vars)
	}
}

// RS-1 · Con el nodo actual fuera de la definición la política tampoco se consulta: el turno
// sigue hasta el engine, que es quien lo rechaza.
func TestResume_CurrentNodeOutsideDefinitionSkipsPolicy(t *testing.T) {
	policy := &resumePolicy{restart: true}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, policy))
	resumeSeed(h, func(st *model.Conversation) { st.CurrentNode = "ghost-zzq" })

	err := h.handle(h.incoming("wa-1", "hola"))

	if err == nil || !strings.Contains(err.Error(), "runtime: step: ") {
		t.Errorf("error = %v, quería el del paso del engine («runtime: step: …»)", err)
	}
	if restarts, seeds := policy.calls(); restarts != 0 || seeds != 0 {
		t.Errorf("la política se consultó (Restart=%d, Seed=%d) con el nodo fuera de la definición", restarts, seeds)
	}
}

// RS-2 · «No reiniciar»: la siembra recibe un mapa NO nil y el turno sigue con esas Vars.
func TestResume_NoRestartSeedsVarsAndTheTurnGoesOn(t *testing.T) {
	policy := &resumePolicy{}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, policy))
	resumeSeed(h, nil) // Vars nil

	h.say("wa-1", "hola")

	if restarts, seeds := policy.calls(); restarts != 1 || seeds != 1 {
		t.Errorf("Restart=%d, Seed=%d; quería una consulta de cada una", restarts, seeds)
	}
	if policy.seedGotNil {
		t.Error("Seed recibió un mapa nil; el runtime garantiza uno no nil")
	}
	st, _ := h.state()
	if st.Vars["page_size-zzq"] != "5" {
		t.Errorf("Vars = %v, quería lo que sembró la política", st.Vars)
	}
	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería la respuesta normal del módulo", got)
	}
	if st.LastWaMessageID != "wa-1" {
		t.Errorf("last_wa_message_id = %q, quería wa-1: el turno tenía que seguir", st.LastWaMessageID)
	}
}

// RS-2 · Un error de Restart o de Seed corta el turno con su prefijo y sin responder.
func TestResume_PolicyErrorsCutTheTurn(t *testing.T) {
	cases := []struct {
		name   string
		policy *resumePolicy
		prefix string
	}{
		{"restart error", &resumePolicy{restartErr: errResumeCause}, "runtime: política de reanudación: "},
		{"seed error", &resumePolicy{seedErr: errResumeCause}, "runtime: siembra de reanudación: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, tc.policy))
			resumeSeed(h, nil)

			err := h.handle(h.incoming("wa-1", "hola"))

			if err == nil || !strings.Contains(err.Error(), tc.prefix) || !errors.Is(err, errResumeCause) {
				t.Errorf("error = %v, quería %q envolviendo la causa", err, tc.prefix)
			}
			if got := h.texts(); len(got) != 0 {
				t.Errorf("textos = %q, un turno cortado no responde", got)
			}
			if st, _ := h.state(); st.LastWaMessageID != "wa-previous" {
				t.Errorf("last_wa_message_id = %q, el estado no debía avanzar", st.LastWaMessageID)
			}
		})
	}
}

// RS-3 · «Reiniciar» sin token: el turno se consume sin reiniciar, sin responder y sin
// escribir nada; se cuenta rate_limit.
func TestResume_RestartWithoutTokenConsumesTheTurn(t *testing.T) {
	sinks := &resumeSinkLog{}
	policy := &resumePolicy{restart: true, notice: resumeNotice, effects: []modules.Effect{resumeEffect("probe_expired")}}
	h := resumeHarness(t, resumeProbe{}, []runtime.EventSink{&resumeSink{name: "a", log: sinks}},
		runtime.WithResumePolicy(resumeProbeType, policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	_, ev := resumeSeedInEvent(h, func(st *model.Conversation) { st.Vars = map[string]any{"junk-zzq": "old"} })
	h.limiter.Limit(0)

	h.say("wa-1", "hola")

	if got := h.texts(); len(got) != 0 {
		t.Errorf("textos = %q, sin token no se responde", got)
	}
	if got := sinks.order(); len(got) != 0 {
		t.Errorf("entregas a sinks = %v, sin token no se despacha nada", got)
	}
	st, _ := h.state()
	if st.Vars["junk-zzq"] != "old" || st.LastWaMessageID != "wa-previous" {
		t.Errorf("estado = %+v, quería el de antes: no se reinicia ni se guarda", st)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 || calls[0].Key != h.key().String() {
		t.Errorf("peticiones al limitador = %+v, quería UNA con la clave de la conversación", calls)
	}
	if got := h.blockedReasons(); !resumeEqual(got, []string{"rate_limit"}) {
		t.Errorf("motivos de corte = %v, quería [rate_limit]", got)
	}
	if got := resumeThread(h, ev.ID); len(got) != 0 {
		t.Errorf("hilo = %v, un turno que se tira no deja rastro (RS-5)", got)
	}
}

// RS-4 · «Reiniciar» con token: efectos al fan-out con el evento del estado, Vars descartadas,
// re-entrada con la MISMA versión, last_wa_message_id, Save y UNA emisión con aviso + pantalla.
func TestResume_RestartReentersTheSameVersion(t *testing.T) {
	sinks := &resumeSinkLog{}
	policy := &resumePolicy{restart: true, notice: resumeNotice, effects: []modules.Effect{resumeEffect("probe_expired")}}
	h := resumeHarness(t, resumeProbe{durable: true}, []runtime.EventSink{&resumeSink{name: "a", log: sinks}},
		runtime.WithResumePolicy(resumeProbeType, policy))
	if v := h.seedFlow(resumeProbeFlow("Pantalla de la versión 2-zzq")); v != 2 {
		t.Fatalf("la segunda definición salió con versión %d, quería 2", v)
	}
	_, ev := resumeSeedInEvent(h, func(st *model.Conversation) { st.Vars = map[string]any{"junk-zzq": "old"} })

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeNotice, resumeProbeScreen}) {
		t.Errorf("textos = %q, quería el aviso y la pantalla inicial de la versión 1", got)
	}
	st, _ := h.state()
	if _, kept := st.Vars["junk-zzq"]; kept {
		t.Errorf("Vars = %v, el reinicio descarta las de antes", st.Vars)
	}
	if st.FlowVersion != 1 || st.CurrentNode != "root" || st.LastWaMessageID != "wa-1" {
		t.Errorf("estado = %+v, quería v1 en el nodo inicial con last_wa_message_id = wa-1", st)
	}
	calls := sinks.all()
	if len(calls) != 1 || calls[0].effect != "probe_expired" {
		t.Fatalf("entregas = %+v, quería el efecto sintetizado una vez", calls)
	}
	want := runtime.EffectContext{
		TenantID: harnessTenant, ContactID: st.ContactID, SessionID: harnessSession,
		FlowID: resumeProbeFlowID, FlowVersion: 1, EventID: ev.ID, Durable: true,
	}
	if calls[0].ec != want {
		t.Errorf("EffectContext = %+v, quería %+v", calls[0].ec, want)
	}
	if got := len(h.limiter.Calls()); got != 1 {
		t.Errorf("tokens cobrados = %d, quería UNO: aviso y pantalla son una emisión", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 1 {
		t.Errorf("racha = %d, quería 1: una sola emisión", got)
	}
}

// RS-4 · Sin aviso de la política solo sale la pantalla inicial.
func TestResume_RestartWithoutNoticeSendsOnlyTheScreen(t *testing.T) {
	policy := &resumePolicy{restart: true}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, policy))
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeProbeScreen}) {
		t.Errorf("textos = %q, quería solo la pantalla inicial", got)
	}
}

// RS-5 · Tras un reinicio consumado con evento activo: el literal del cliente entra como
// turno y las salidas, MARCADAS como fuera de turno.
func TestResume_RestartWritesTheThread(t *testing.T) {
	policy := &resumePolicy{restart: true, notice: resumeNotice}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	_, ev := resumeSeedInEvent(h, nil)

	h.say("wa-1", "quiero retomar-zzq")

	want := []string{
		string(events.RoleClient) + ":" + string(events.KindMessage),
		string(events.RoleBusiness) + ":" + string(events.KindMessageOutOfTurn),
		string(events.RoleBusiness) + ":" + string(events.KindMessageOutOfTurn),
	}
	if got := resumeThread(h, ev.ID); !resumeEqual(got, want) {
		t.Errorf("hilo = %v, quería %v", got, want)
	}
}

// RS-5 · Si el cierre natural apagó el evento en ese mismo turno (el flujo reentrado termina
// en su propia entrada), no se escribe nada en el hilo.
func TestResume_RestartThatFinishesTheFlowLeavesNoThread(t *testing.T) {
	policy := &resumePolicy{restart: true, notice: resumeNotice}
	h := resumeHarness(t, resumeProbe{}, nil, runtime.WithResumePolicy(resumeProbeType, policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	flow := model.Flow{
		FlowID:  "probe-bye-zzq",
		Initial: "bye",
		Nodes: map[string]model.Node{
			"bye": {Type: model.NodeTypeMessage, Text: "Hasta luego-zzq"},
			"ask": {Type: resumeProbeType, Prompt: resumeProbeScreen},
		},
	}
	h.seedFlow(flow)
	_, ev := resumeSeedInEvent(h, func(st *model.Conversation) { st.FlowID, st.CurrentNode = flow.FlowID, "ask" })

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeNotice, "Hasta luego-zzq"}) {
		t.Errorf("textos = %q, quería el aviso y la despedida del flujo reentrado", got)
	}
	if st, _ := h.state(); st.EventID != "" {
		t.Fatalf("event_id = %q, el cierre natural tenía que apagar el evento activo", st.EventID)
	}
	if got := resumeThread(h, ev.ID); len(got) != 0 {
		t.Errorf("hilo = %v, con el evento apagado en el mismo turno no se escribe nada", got)
	}
}

// RS-6 · El fan-out del reinicio corta (RT-10): no se reinicia ni se guarda, sale el aviso de
// avería con su token y entra al hilo como fuera de turno. El error del corte es
// ErrTurnCutBySinkFailure envolviendo la causa.
func TestResume_RestartCutBySinkFailure(t *testing.T) {
	permanent := resumePermanentErr()
	sinks := &resumeSinkLog{}
	policy := &resumePolicy{restart: true, notice: resumeNotice, effects: []modules.Effect{resumeEffect("probe_expired")}}
	h := resumeHarness(t, resumeProbe{durable: true},
		[]runtime.EventSink{&resumeSink{name: "a", log: sinks, fail: resumeFailAlways(permanent)}},
		runtime.WithResumePolicy(resumeProbeType, policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	_, ev := resumeSeedInEvent(h, func(st *model.Conversation) { st.Vars = map[string]any{"junk-zzq": "old"} })

	h.say("wa-1", "hola")

	if got := h.texts(); !resumeEqual(got, []string{resumeFailureNotice}) {
		t.Errorf("textos = %q, quería SOLO el aviso de avería", got)
	}
	st, _ := h.state()
	if st.Vars["junk-zzq"] != "old" || st.LastWaMessageID != "wa-previous" {
		t.Errorf("estado = %+v, un reinicio cortado no reinicia ni guarda", st)
	}
	if got := len(h.limiter.Calls()); got != 2 {
		t.Errorf("tokens cobrados = %d, quería 2: el del reinicio (RS-3) y el del aviso", got)
	}
	want := []string{string(events.RoleBusiness) + ":" + string(events.KindMessageOutOfTurn)}
	if got := resumeThread(h, ev.ID); !resumeEqual(got, want) {
		t.Errorf("hilo = %v, quería solo el aviso marcado como fuera de turno", got)
	}
	line, ok := resumeLogLine(h, "error", "runtime: reanudación cortada: el sink durable no pudo materializar el efecto sintetizado tras el reintento acotado")
	if !ok {
		t.Fatalf("falta la línea a ERROR de la reanudación cortada\nlog:\n%s", h.log.dump())
	}
	cut, isErr := line.fields["error"].(error)
	if !isErr || !errors.Is(cut, runtime.ErrTurnCutBySinkFailure) || !errors.Is(cut, permanent) {
		t.Errorf("error del corte = %v, quería ErrTurnCutBySinkFailure envolviendo la causa del sink", line.fields["error"])
	}
}

// RS-6 · El aviso de avería entra al hilo «salga o no»: con el cupo agotado para el aviso no
// se envía, pero la fila fuera de turno se escribe igual.
func TestResume_CutNoticeIsThreadedEvenWhenRateLimited(t *testing.T) {
	policy := &resumePolicy{restart: true, effects: []modules.Effect{resumeEffect("probe_expired")}}
	h := resumeHarness(t, resumeProbe{durable: true},
		[]runtime.EventSink{&resumeSink{name: "a", log: &resumeSinkLog{}, fail: resumeFailAlways(resumePermanentErr())}},
		runtime.WithResumePolicy(resumeProbeType, policy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	_, ev := resumeSeedInEvent(h, nil)
	h.limiter.Limit(1) // el token del reinicio sí; el del aviso, no.

	h.say("wa-1", "hola")

	if got := h.texts(); len(got) != 0 {
		t.Errorf("textos = %q, sin token el aviso no sale", got)
	}
	want := []string{string(events.RoleBusiness) + ":" + string(events.KindMessageOutOfTurn)}
	if got := resumeThread(h, ev.ID); !resumeEqual(got, want) {
		t.Errorf("hilo = %v, el aviso se escribe salga o no", got)
	}
}
