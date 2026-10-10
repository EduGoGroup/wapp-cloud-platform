package runtime_test

// runtime_engine_test.go prueba el contrato de runtime_engine.go: New, las opciones (RT-12:
// una pieza ausente o nil es no-regresión; el orden no cambia el resultado) y los puertos que
// declara. El plazo y el semáforo de los entrantes (RT-3) van en runtime_engine_pool_test.go y
// MaxAutoreplyStreak (RT-11, T-7) en runtime_engine_streak_test.go.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Quién satisface cada puerto de este fichero, sin adaptador.
var (
	_ runtime.FlowStore       = (*store.MemoryRepository)(nil)
	_ runtime.FlowStore       = (*store.PostgresRepository)(nil)
	_ runtime.ReplyLimiter    = (*runtimehelpertest.ReplyLimiter)(nil)
	_ runtime.DepositReminder = (*runtimehelpertest.DepositReminder)(nil)
)

// engineKeywordHarness monta un guion cuyo «hola» arranca el menú de dos opciones.
func engineKeywordHarness(t *testing.T, extra ...runtime.Option) *harness {
	t.Helper()
	h := newHarness(t, withOptions(func(*harness) []runtime.Option { return extra }))
	h.seedFlow(menuFlow(startFlowID))
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: startFlowID})
	return h
}

// engineBare construye OTRO Runtime sobre las cinco dependencias obligatorias del guion, solo
// con esas opciones.
func engineBare(h *harness, opts ...runtime.Option) *runtime.Runtime {
	return runtime.New(h.repo, h.engine, h.sender, h.tenants, h.contacts, h.log, opts...)
}

// engineMenuTexts es lo que contesta el menú de dos opciones a «hola» y después a «1».
func engineMenuTexts() []string {
	flow := menuFlow(startFlowID)
	return []string{flow.Nodes["root"].Prompt, flow.Nodes["first"].Text}
}

// engineNoErrors falla si el runtime escribió alguna línea a ERROR.
func engineNoErrors(t *testing.T, h *harness) {
	t.Helper()
	if lines := h.log.at("error"); len(lines) != 0 {
		t.Errorf("líneas a ERROR = %+v, no quería ninguna", lines)
	}
}

// New sin opciones deja el motor de antes de cada plan (INV-6): resolver de disparos noop (un
// entrante sin estado se ignora), LogSink, sin plano de eventos; Start y el avance funcionan,
// y el contador de rachas existe.
func TestNew_WithoutOptionsIsThePlainEngine(t *testing.T) {
	h := engineKeywordHarness(t)
	h.repo.SetClock(time.Now) // sin WithClock el runtime mide el TTL con time.Now
	rt := engineBare(h)

	if err := rt.HandleIncoming(t.Context(), harnessSession, h.incoming("wa-0", "hola")); err != nil {
		t.Fatalf("HandleIncoming sin estado: %v", err)
	}
	if _, found := h.state(); found || len(h.sender.Attempts()) != 0 {
		t.Fatal("sin WithTriggerResolver un entrante sin conversación viva tenía que ignorarse")
	}
	if _, err := rt.Start(t.Context(), harnessTenant, startFlowID, harnessSession, startRef(t, harnessPhone)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(t.Context(), harnessSession, h.incoming("wa-1", "1")); err != nil {
		t.Fatalf("HandleIncoming sobre la conversación viva: %v", err)
	}

	if got := h.texts(); !resumeEqual(got, engineMenuTexts()) {
		t.Errorf("textos = %q, quería el menú y la primera opción", got)
	}
	if got := rt.MaxAutoreplyStreak(); got != 2 {
		t.Errorf("racha = %d, quería 2: el contador se construye siempre", got)
	}
	if got := h.repo.FlowEvents(); len(got) != 0 {
		t.Errorf("flow_events = %+v, sin WithEventSink el fan-out es solo LogSink", got)
	}
	engineNoErrors(t, h)
}

// RT-12 · El orden en que se pasan las opciones no cambia el resultado; el hook de rachas
// recibe los cierres se pase antes o después que las demás.
func TestNew_OptionOrderDoesNotChangeTheResult(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reversed=%v", reversed), func(t *testing.T) {
			h := engineKeywordHarness(t)
			opts := h.runtimeOptions()
			if reversed {
				for i, j := 0, len(opts)-1; i < j; i, j = i+1, j-1 {
					opts[i], opts[j] = opts[j], opts[i]
				}
			}
			rt := engineBare(h, opts...)

			for i, text := range []string{"hola", "1"} {
				if err := rt.HandleIncoming(t.Context(), harnessSession, h.incoming(fmt.Sprintf("wa-%d", i), text)); err != nil {
					t.Fatalf("HandleIncoming(%q): %v", text, err)
				}
			}
			h.clock.Advance(31 * time.Minute)

			if got := h.texts(); !resumeEqual(got, engineMenuTexts()) {
				t.Errorf("textos = %q, quería el menú y la primera opción", got)
			}
			if got := rt.MaxAutoreplyStreak(); got != 0 {
				t.Errorf("racha viva = %d, quería 0 tras 31 min con el reloj inyectado", got)
			}
			if got := h.closedStreaks(); len(got) != 1 || got[0] != 2 {
				t.Errorf("rachas cerradas = %v, quería [2]: el hook no se congela al construir", got)
			}
		})
	}
}

// RT-12 · Pasar nil (o el valor cero) a cualquier opción que inyecta una pieza no produce ni
// error ni pánico en el camino del entrante: el motor contesta lo mismo.
func TestOptions_NilPieceIsNoRegression(t *testing.T) {
	cases := map[string]runtime.Option{
		"WithPresignClient":         runtime.WithPresignClient(nil),
		"WithTriggerResolver":       runtime.WithTriggerResolver(nil),
		"WithReplyLimiter":          runtime.WithReplyLimiter(nil),
		"WithSelfNumbers":           runtime.WithSelfNumbers(nil),
		"WithIngestDeduper":         runtime.WithIngestDeduper(nil),
		"WithEntitlements":          runtime.WithEntitlements(nil),
		"WithClock":                 runtime.WithClock(nil),
		"WithDepositReminder":       runtime.WithDepositReminder(nil),
		"WithEventStore":            runtime.WithEventStore(nil),
		"WithIntakeAbandoner":       runtime.WithIntakeAbandoner(nil),
		"WithDispatcher":            runtime.WithDispatcher(nil),
		"WithSummarySources":        runtime.WithSummarySources(events.SummarySources{}),
		"WithOpeningBuilder":        runtime.WithOpeningBuilder(nil),
		"WithFlowForKind":           runtime.WithFlowForKind(nil),
		"WithIncomingTimeout":       runtime.WithIncomingTimeout(0),
		"WithMaxConcurrentIncoming": runtime.WithMaxConcurrentIncoming(0),
		"WithReactiveBlockedHook":   runtime.WithReactiveBlockedHook(nil),
		"WithAutoreplyStreakHook":   runtime.WithAutoreplyStreakHook(nil),
		"WithAggregator":            runtime.WithAggregator(nil),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			h := engineKeywordHarness(t, opt)
			h.enableFeature(entitlements.FeatureLLMIntake)

			if _, err := startAPI(h, startFlowID); err != nil {
				t.Fatalf("Start: %v", err)
			}
			h.say("wa-1", "1")
			if err := h.handle(h.incomingFrom("573009998877", "wa-2", "algo sin disparo-zzq")); err != nil {
				t.Fatalf("HandleIncoming de un contacto sin conversación: %v", err)
			}

			texts := h.texts()
			if len(texts) < 2 || !resumeEqual(texts[:2], engineMenuTexts()) {
				t.Errorf("textos = %q, quería empezar por el menú y la primera opción", texts)
			}
			if got := h.rt.MaxAutoreplyStreak(); got != 2 {
				t.Errorf("racha = %d, quería 2", got)
			}
			engineNoErrors(t, h)
		})
	}
}

// WithReplyLimiter · Sin limitador no hay tope y rate_limit no se cuenta nunca.
func TestWithReplyLimiter_NilMeansNoCap(t *testing.T) {
	h := engineKeywordHarness(t, runtime.WithReplyLimiter(nil))
	h.limiter.Limit(0)

	h.say("wa-0", "hola")
	h.say("wa-1", "1")

	if got := h.texts(); !resumeEqual(got, engineMenuTexts()) {
		t.Errorf("textos = %q, sin limitador se responde siempre", got)
	}
	if len(h.limiter.Calls()) != 0 || len(h.blockedReasons()) != 0 {
		t.Errorf("limitador = %+v, cortes = %v; quería ninguno", h.limiter.Calls(), h.blockedReasons())
	}
}

// WithEntitlements · Sin resolver, el hilo y la bienvenida quedan CERRADOS (fail-closed)
// aunque el plano de eventos y el almacén de la bienvenida estén cableados (RT-20).
func TestWithEntitlements_NilClosesThreadAndWelcome(t *testing.T) {
	h := newHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithEntitlements(nil)}
	}))
	h.enableFeature(entitlements.FeatureLLMIntake)
	flow := menuFlow(startFlowID)
	h.seedFlow(flow)
	h.seedRule(trigger.Rule{
		Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindCart, FlowID: startFlowID,
	})

	h.say("wa-0", "carrito")

	if got := h.texts(); !resumeEqual(got, []string{flow.Nodes["root"].Prompt}) {
		t.Errorf("textos = %q, quería solo el menú: sin resolver no hay bienvenida", got)
	}
	ev := startBornEvent(h)
	if got := resumeThread(h, ev.ID); len(got) != 0 {
		t.Errorf("hilo = %v, sin resolver no se escribe ni una fila", got)
	}
}

// WithClock · Un reloj nil se ignora: se queda el que hubiera.
func TestWithClock_NilKeepsThePreviousClock(t *testing.T) {
	h := engineKeywordHarness(t, runtime.WithClock(nil))
	h.say("wa-0", "hola")

	h.clock.Advance(31 * time.Minute)

	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("racha viva = %d, quería 0: el reloj del guion seguía mandando tras WithClock(nil)", got)
	}
}

// WithClock (RT-15) · El reloj del runtime decide el vencimiento del TTL conversacional del
// limbo: a exactamente el TTL sigue viva; pasado, se suelta (cerrando su racha) y el entrante
// se trata como nuevo.
//
// El TTL del guion (10 min) es MÁS CORTO que la ventana de inactividad de la racha
// (streakIdleTTL, 30 min; streak.go, igual que el viejo), y es a propósito: con un TTL de una
// hora, el entrante «a exactamente el TTL» ya encuentra la racha vencida por inactividad y la
// cierra él solo ([1]) antes de que nadie suelte el estado, y el test mediría la racha y no el
// TTL. El TTL no tiene mínimo ni saneo en el runtime (conversationExpired solo aparta el <= 0).
func TestWithClock_GovernsTheConversationTTL(t *testing.T) {
	const engineShortTTL = 10 * time.Minute
	h := engineKeywordHarness(t)
	h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = engineShortTTL })
	prompt := menuFlow(startFlowID).Nodes["root"].Prompt
	h.say("wa-0", "hola")

	h.clock.Advance(engineShortTTL)
	h.say("wa-1", "zzz")
	if st, found := h.state(); !found || st.LastWaMessageID != "wa-1" || len(h.closedStreaks()) != 0 {
		t.Fatalf("a exactamente el TTL la conversación tenía que seguir viva: estado = (%+v, %v), cerradas = %v", st, found, h.closedStreaks())
	}

	h.clock.Advance(engineShortTTL + time.Second)
	h.say("wa-2", "hola")

	texts := h.texts()
	if len(texts) != 3 || texts[2] != prompt {
		t.Errorf("textos = %q, quería terminar con el menú de un arranque nuevo", texts)
	}
	if got := h.closedStreaks(); len(got) != 1 || got[0] != 2 {
		t.Errorf("rachas cerradas = %v, quería [2]: soltar el estado cierra su episodio (RT-11)", got)
	}
	if st, _ := h.state(); st.CurrentNode != "root" || st.LastWaMessageID == "wa-1" {
		t.Errorf("estado = %+v, quería una conversación recién arrancada", st)
	}
}

// WithEventStore · Sin almacén de eventos no hay plano: un event_start arranca su flujo como
// una keyword y no pare fila.
func TestWithEventStore_NilMeansNoEventPlane(t *testing.T) {
	h := newHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithEventStore(nil)}
	}))
	flow := menuFlow(startFlowID)
	h.seedFlow(flow)
	h.seedRule(trigger.Rule{
		Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindCart, FlowID: startFlowID,
	})

	h.say("wa-0", "carrito")

	if got := h.texts(); !resumeEqual(got, []string{flow.Nodes["root"].Prompt}) {
		t.Errorf("textos = %q, quería el menú del flujo", got)
	}
	if got := h.events.Events(harnessTenant); len(got) != 0 {
		t.Errorf("eventos = %+v, sin plano no nace ninguna fila", got)
	}
	if st, _ := h.state(); st.EventID != "" {
		t.Errorf("event_id = %q, sin plano no se estampa evento", st.EventID)
	}
}

// WithReactiveBlockedHook · Recibe el motivo una vez por corte; con nil el corte es el mismo.
func TestWithReactiveBlockedHook_ObservesWithoutDeciding(t *testing.T) {
	for _, hooked := range []bool{true, false} {
		t.Run(fmt.Sprintf("hooked=%v", hooked), func(t *testing.T) {
			var extra []runtime.Option
			if !hooked {
				extra = append(extra, runtime.WithReactiveBlockedHook(nil))
			}
			h := engineKeywordHarness(t, extra...)
			h.tenants.SetProfile(runtimehelpertest.ProfilePassive)

			h.say("wa-0", "hola")
			h.say("wa-1", "hola")

			if _, found := h.state(); found || len(h.sender.Attempts()) != 0 {
				t.Error("una sesión pasiva no dispara ni responde, haya hook o no")
			}
			want := []string{}
			if hooked {
				want = []string{"passive", "passive"}
			}
			if got := h.blockedReasons(); !resumeEqual(got, want) {
				t.Errorf("motivos contados = %v, quería %v", got, want)
			}
		})
	}
}

// DepositReminder / WithDepositReminder (RT-19) · Cada entrante que pasa las guardas termina
// tocando el recordatorio con (tenant, contacto), DESPUÉS de contestar; también sin
// conversación. El duplicado y la sesión pasiva no lo tocan.
func TestWithDepositReminder_IsTouchedLastOnEveryIncoming(t *testing.T) {
	h := engineKeywordHarness(t)
	touchedAtSend := -1
	h.sender.OnSend(func(runtimehelpertest.Send) { touchedAtSend = len(h.deposits.Calls()) })

	h.say("wa-0", "hola")

	want := runtimehelpertest.ReminderCall{TenantID: harnessTenant, ContactID: h.key().ContactID}
	if calls := h.deposits.Calls(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("toques = %+v, quería uno con el tenant y el contacto", calls)
	}
	if touchedAtSend != 0 {
		t.Errorf("al enviar la respuesta ya había %d toques; el recordatorio va lo último", touchedAtSend)
	}

	h.say("wa-0", "hola") // duplicado: la dedupe de ingesta lo corta antes
	if got := len(h.deposits.Calls()); got != 1 {
		t.Errorf("toques tras el duplicado = %d, quería 1", got)
	}
	if err := h.handle(h.incomingFrom("573009998877", "wa-9", "algo sin disparo-zzq")); err != nil {
		t.Fatalf("HandleIncoming de un contacto sin conversación: %v", err)
	}
	if got := len(h.deposits.Calls()); got != 2 {
		t.Errorf("toques tras un entrante sin conversación = %d, quería 2: pregunta por el contacto", got)
	}
	h.tenants.SetProfile(runtimehelpertest.ProfilePassive)
	h.say("wa-3", "hola")
	if got := len(h.deposits.Calls()); got != 2 {
		t.Errorf("toques tras un entrante de sesión pasiva = %d, quería 2", got)
	}
}

// WithEventSink · Se acumulan: los dos sinks reciben el efecto.
func TestWithEventSink_Accumulates(t *testing.T) {
	log := &resumeSinkLog{}
	h := newHarness(t,
		withModules(resumeProbe{effects: []modules.Effect{resumeEffect("e1")}}),
		withSinks(func(*harness) []runtime.EventSink { return nil }),
		withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{
				runtime.WithEventSink(&resumeSink{name: "a", log: log}),
				runtime.WithEventSink(&resumeSink{name: "b", log: log}),
			}
		}),
	)
	h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if got := log.order(); !resumeEqual(got, []string{"a:e1", "b:e1"}) {
		t.Errorf("entregas = %v, quería el efecto en los dos sinks, en orden de registro", got)
	}
}

// WithResumePolicy · Pasarla dos veces para el mismo tipo deja la última.
func TestWithResumePolicy_LastOneWins(t *testing.T) {
	first := &resumePolicy{restart: true, notice: "aviso de la primera-zzq"}
	last := &resumePolicy{}
	h := resumeHarness(t, resumeProbe{}, nil,
		runtime.WithResumePolicy(resumeProbeType, first),
		runtime.WithResumePolicy(resumeProbeType, last),
	)
	resumeSeed(h, nil)

	h.say("wa-1", "hola")

	if restarts, _ := first.calls(); restarts != 0 {
		t.Errorf("la primera política se consultó %d veces; manda la última", restarts)
	}
	if restarts, seeds := last.calls(); restarts != 1 || seeds != 1 {
		t.Errorf("última política: Restart=%d, Seed=%d; quería una de cada", restarts, seeds)
	}
}

// Runtime · Serializa por conversación: N entrantes simultáneos de la MISMA clave se procesan
// de uno en uno y el estado queda íntegro.
func TestRuntime_SameConversationIsSerialized(t *testing.T) {
	h := engineKeywordHarness(t)
	h.say("wa-0", "hola")
	before := len(h.sender.Attempts())

	const n = 30
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := h.handle(h.incoming(fmt.Sprintf("wa-parallel-%d", i), "zzz")); err != nil {
				t.Errorf("HandleIncoming concurrente: %v", err)
			}
		})
	}
	wg.Wait()

	if got := len(h.sender.Attempts()) - before; got != n {
		t.Errorf("respuestas = %d, quería %d: un turno perdido o duplicado delata que no se serializó", got, n)
	}
	if st, _ := h.state(); st.CurrentNode != "root" {
		t.Errorf("estado = %+v, quería la conversación íntegra en su menú", st)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != n+1 {
		t.Errorf("racha = %d, quería %d", got, n+1)
	}
}

// Runtime · Claves distintas avanzan en paralelo y cada una queda con su estado.
func TestRuntime_DistinctConversationsAdvanceInParallel(t *testing.T) {
	h := engineKeywordHarness(t)

	const n = 30
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			phone := fmt.Sprintf("5730000000%02d", i)
			if _, err := h.rt.Start(t.Context(), harnessTenant, startFlowID, harnessSession, startRef(t, phone)); err != nil {
				t.Errorf("Start concurrente de %s: %v", phone, err)
			}
		})
	}
	wg.Wait()

	if got := len(h.sender.Sends()); got != n {
		t.Errorf("menús enviados = %d, quería %d", got, n)
	}
	for i := range n {
		key := store.Key{TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(fmt.Sprintf("5730000000%02d", i))}
		st, found, err := h.repo.Load(t.Context(), key)
		if err != nil || !found || st.CurrentNode != "root" {
			t.Errorf("conversación %d = (%+v, %v, %v), quería creada en su menú", i, st, found, err)
		}
	}
}
