package runtime_test

// start_funnel_test.go prueba, por HandleIncoming, las reglas del embudo de arranque de
// start.go que la puerta de la API no alcanza: la siembra de la intención y el pre-carga
// (ST-C, ST-D), el corte del arranque (ST-E), la coletilla (ST-F) y el hilo del turno de
// apertura (ST-G). El arranque reactivo se provoca con un resolver de disparos de mentira, que
// es el único que hoy puede traer parámetros de intención (camino sin productor).

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

const (
	startPrimedText = "Precarga de la sonda-zzq"
	startTagline    = "Tienes un pedido a medias-zzq"
	startIntentName = "order-zzq"
)

// startDecisionResolver es un trigger.Resolver que contesta SIEMPRE la misma decisión a un
// entrante sin conversación viva; nada escapa y nada salta.
type startDecisionResolver struct{ dec trigger.Decision }

func (r startDecisionResolver) Resolve(context.Context, string, string, trigger.Signal) (trigger.Decision, error) {
	return r.dec, nil
}

func (startDecisionResolver) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
}

func (startDecisionResolver) ResolveLive(context.Context, string, string, string) (trigger.Decision, error) {
	return trigger.Decision{}, nil
}

// startOpening es un runtime.OpeningBuilder que no ofrece nada y solo trae la coletilla.
type startOpening struct{ tagline string }

func (startOpening) BuildOpening(context.Context, events.ConversationRef) (events.Offering, error) {
	return events.Offering{}, nil
}

func (startOpening) BuildRescue(context.Context, events.ConversationRef) (events.Offering, error) {
	return events.Offering{}, nil
}

func (o startOpening) BuildTagline(context.Context, events.ConversationRef) (string, error) {
	return o.tagline, nil
}

// startPrimed apunta lo que el pre-carga vio en las Vars.
type startPrimed struct {
	mu     sync.Mutex
	calls  int
	params any
	name   any
}

// startPrimer es la sonda con la capacidad de pre-carga (modules.Primer).
type startPrimer struct {
	resumeProbe
	seen *startPrimed
}

func (p startPrimer) Prime(_ model.Node, _ model.Content, vars map[string]any) (modules.Result, bool) {
	p.seen.mu.Lock()
	defer p.seen.mu.Unlock()
	p.seen.calls++
	p.seen.params = vars[modules.VarIntentParams]
	p.seen.name = vars[modules.VarIntentName]
	return modules.Result{Vars: vars, Outputs: []string{startPrimedText}, Effects: p.effects}, true
}

// startIntentDecision es la decisión de abrir un evento `cart` sobre flowID por una intención.
func startIntentDecision(flowID string, params map[string]string) trigger.Decision {
	return trigger.Decision{
		Action: trigger.StartEvent, FlowID: flowID, EventKind: trigger.EventKindCart,
		Params: params, IntentName: startIntentName,
	}
}

// startFunnelHarness monta un guion cuyo disparo contesta dec, con el pre-carga durable que
// declara el efecto probe_primed, esos sinks y la coletilla fija.
func startFunnelHarness(t *testing.T, dec trigger.Decision, seen *startPrimed, sinks ...runtime.EventSink) *harness {
	t.Helper()
	probe := startPrimer{
		resumeProbe: resumeProbe{durable: true, effects: []modules.Effect{resumeEffect("probe_primed")}},
		seen:        seen,
	}
	h := resumeHarness(t, probe, sinks,
		runtime.WithTriggerResolver(startDecisionResolver{dec: dec}),
		runtime.WithOpeningBuilder(startOpening{tagline: startTagline}),
	)
	h.enableFeature(entitlements.FeatureLLMIntake)
	return h
}

// startTexts devuelve los textos enviados SIN la bienvenida única: con `llm_intake` el primer
// contacto la recibe antes que nada (welcome.go), y no es asunto de estos tests.
func startTexts(h *harness) []string {
	texts := h.texts()
	if len(texts) > 0 && texts[0] == store.DefaultWelcomeText {
		return texts[1:]
	}
	return texts
}

// startBornEvent devuelve el único evento del tenant; falla si no hay exactamente uno.
func startBornEvent(h *harness) events.Event {
	h.t.Helper()
	evs := h.events.Events(harnessTenant)
	if len(evs) != 1 {
		h.t.Fatalf("eventos = %d, quería exactamente el recién nacido\nlog:\n%s", len(evs), h.log.dump())
	}
	return evs[0]
}

// ST-C / ST-D · Con parámetros de intención se siembran las Vars antes del primer paso, y los
// efectos del pre-carga llevan el evento RECIÉN NACIDO (que llega por parámetro: el puntero
// se estampa después) y Durable según el nodo inicial.
func TestStartFunnel_IntentParamsPrimeTheInitialNode(t *testing.T) {
	seen := &startPrimed{}
	sinks := &resumeSinkLog{}
	dec := startIntentDecision(resumeProbeFlowID, map[string]string{"product-zzq": "empanadas"})
	h := startFunnelHarness(t, dec, seen, &resumeSink{name: "a", log: sinks})

	h.say("wa-1", "quiero empanadas")

	params, isMap := seen.params.(map[string]any)
	if seen.calls != 1 || !isMap || params["product-zzq"] != "empanadas" || seen.name != startIntentName {
		t.Fatalf("pre-carga: llamadas=%d params=%v name=%v; quería los parámetros y el nombre de la intención sembrados", seen.calls, seen.params, seen.name)
	}
	ev := startBornEvent(h)
	assertStartPrimedEffect(t, sinks, ev.ID)
	if st, found := h.state(); !found || st.EventID != ev.ID || st.OwnerEventID != ev.ID {
		t.Errorf("estado = (%+v, %v), quería el puntero estampado al evento tras arrancar", st, found)
	}
}

// assertStartPrimedEffect: el efecto del pre-carga llegó UNA vez, con el evento recién nacido
// y Durable según el nodo inicial (ST-D).
func assertStartPrimedEffect(t *testing.T, sinks *resumeSinkLog, eventID string) {
	t.Helper()
	var primed []resumeSinkCall
	for _, c := range sinks.all() {
		if c.effect == "probe_primed" {
			primed = append(primed, c)
		}
	}
	if len(primed) != 1 {
		t.Fatalf("entregas del efecto del pre-carga = %+v, quería una", primed)
	}
	want := runtime.EffectContext{
		TenantID: harnessTenant, ContactID: primed[0].ec.ContactID, SessionID: harnessSession,
		FlowID: resumeProbeFlowID, FlowVersion: 1, EventID: eventID, Durable: true,
	}
	if primed[0].ec != want || want.ContactID == "" {
		t.Errorf("EffectContext = %+v, quería %+v", primed[0].ec, want)
	}
}

// ST-C · Sin parámetros de intención no se siembra nada: el pre-carga no corre y el estado
// nace sin Vars de intención.
func TestStartFunnel_WithoutIntentNothingIsSeeded(t *testing.T) {
	seen := &startPrimed{}
	dec := trigger.Decision{Action: trigger.StartEvent, FlowID: resumeProbeFlowID, EventKind: trigger.EventKindCart}
	h := startFunnelHarness(t, dec, seen)

	h.say("wa-1", "carrito")

	if seen.calls != 0 {
		t.Errorf("el pre-carga corrió %d veces sin parámetros de intención", seen.calls)
	}
	st, _ := h.state()
	if _, has := st.Vars[modules.VarIntentParams]; has {
		t.Errorf("Vars = %v, sin intención no se siembra intent_params", st.Vars)
	}
	if _, has := st.Vars[modules.VarIntentName]; has {
		t.Errorf("Vars = %v, sin intención no se siembra intent_name", st.Vars)
	}
	if got := startTexts(h); !resumeEqual(got, []string{resumeProbeScreen}) {
		t.Errorf("textos = %q, quería la pantalla inicial sin coletilla (no hay intención)", got)
	}
}

// ST-E · Si el sink durable no materializa el pre-carga, el arranque se corta ANTES del Save:
// sin estado, sin hilo, el cliente recibe el aviso de avería y el resultado es nil.
func TestStartFunnel_CutBeforeSave(t *testing.T) {
	seen := &startPrimed{}
	permanent := resumePermanentErr()
	sink := &resumeSink{name: "a", log: &resumeSinkLog{}, fail: func(_ int, eff modules.Effect) error {
		if eff.Name == "probe_primed" {
			return permanent
		}
		return nil
	}}
	dec := startIntentDecision(resumeProbeFlowID, map[string]string{"product-zzq": "empanadas"})
	h := startFunnelHarness(t, dec, seen, sink)

	if err := h.handle(h.incoming("wa-1", "quiero empanadas")); err != nil {
		t.Fatalf("HandleIncoming = %v, un arranque cortado no es un error", err)
	}

	if got := startTexts(h); !resumeEqual(got, []string{resumeFailureNotice}) {
		t.Errorf("textos = %q, quería SOLO el aviso de avería", got)
	}
	if st, found := h.state(); found {
		t.Errorf("estado = %+v, un arranque cortado no guarda estado", st)
	}
	for _, ev := range h.events.Events(harnessTenant) {
		if got := resumeThread(h, ev.ID); len(got) != 0 {
			t.Errorf("hilo = %v, un arranque cortado no escribe hilo", got)
		}
	}
	if _, ok := resumeLogLine(h, "error", "runtime: arranque cortado: el sink durable no pudo materializar el pre-carga tras el reintento acotado"); !ok {
		t.Errorf("falta la línea a ERROR del arranque cortado\nlog:\n%s", h.log.dump())
	}
}

// ST-F / ST-G · La coletilla se pega al ÚLTIMO texto con "\n\n" y se marca en el mismo Save;
// el hilo del turno de apertura lleva el literal del cliente y las salidas con la coletilla.
func TestStartFunnel_TaglineIsAppendedMarkedAndThreaded(t *testing.T) {
	flow := menuFlow(startFlowID)
	h := startFunnelHarness(t, startIntentDecision(startFlowID, nil), &startPrimed{})
	h.seedFlow(flow)
	markedAtSend := false
	h.sender.OnSend(func(runtimehelpertest.Send) {
		st, _ := h.state()
		markedAtSend = st.Vars["tagline_offered"] == true
	})

	h.say("wa-1", "quiero pedir-zzq")

	want := flow.Nodes["root"].Prompt + "\n\n" + startTagline
	if got := startTexts(h); !resumeEqual(got, []string{want}) {
		t.Errorf("textos = %q, quería UN texto con la coletilla pegada: %q", got, want)
	}
	if !markedAtSend {
		t.Error("al enviar, tagline_offered no estaba en el estado guardado (va en el MISMO Save)")
	}
	ev := startBornEvent(h)
	thread, err := h.events.ListThread(t.Context(), ev.ID, 10)
	if err != nil {
		t.Fatalf("leer el hilo: %v", err)
	}
	if len(thread) != 2 || thread[0].Role != events.RoleClient || thread[0].Text != "quiero pedir-zzq" ||
		thread[1].Role != events.RoleBusiness || thread[1].Text != want || thread[1].Kind != events.KindMessage {
		t.Errorf("hilo = %+v, quería el literal del cliente y la salida con la coletilla, como turno", thread)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 1 {
		t.Errorf("racha = %d, quería 1: el arranque reactivo también cuenta (ST-H)", got)
	}
}

// ST-F · Con la respuesta terminada en adjunto la coletilla ni se pega ni se marca.
func TestStartFunnel_TaglineIsNotAppendedAfterAnAttachment(t *testing.T) {
	doc := "doc"
	flow := model.Flow{
		FlowID:  "brochure-zzq",
		Initial: "intro",
		Nodes: map[string]model.Node{
			"intro": {Type: model.NodeTypeMessage, Text: "Va la lista-zzq", Next: &doc},
			"doc": {Type: media.NodeTypeMedia, Content: &model.ContentRef{
				Source: "static", Key: "wapp/media/list-zzq.pdf", Filename: "Lista-zzq.pdf",
				Mime: "application/pdf", Kind: media.KindDocument,
			}},
		},
	}
	h := startFunnelHarness(t, startIntentDecision(flow.FlowID, nil), &startPrimed{})
	h.seedFlow(flow)

	h.say("wa-1", "quiero la lista-zzq")

	if got := startTexts(h); !resumeEqual(got, []string{"Va la lista-zzq"}) {
		t.Errorf("textos = %q, quería el texto SIN coletilla: la respuesta termina en adjunto", got)
	}
	if got := h.sender.Media(); len(got) != 1 {
		t.Fatalf("adjuntos = %+v, quería uno", got)
	}
	if st, _ := h.state(); st.Vars["tagline_offered"] != nil {
		t.Errorf("Vars = %v, sin coletilla pegada no se marca tagline_offered", st.Vars)
	}
}

// ST-G · El arranque PLANO (una keyword sin event_kind) no pare evento ni escribe hilo.
func TestStartFunnel_PlainStartWritesNoThread(t *testing.T) {
	h := newHarness(t)
	h.enableFeature(entitlements.FeatureLLMIntake)
	flow := menuFlow(startFlowID)
	h.seedFlow(flow)
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: startFlowID})

	h.say("wa-1", "hola")

	if got := h.events.Events(harnessTenant); len(got) != 0 {
		t.Errorf("eventos = %+v, un arranque plano no pare evento", got)
	}
	if st, found := h.state(); !found || st.EventID != "" || st.CurrentNode != "root" {
		t.Errorf("estado = (%+v, %v), quería la conversación arrancada sin evento", st, found)
	}
}
