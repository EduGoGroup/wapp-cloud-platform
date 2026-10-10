package trigger_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// El peldaño de INTENCIÓN de ConfigResolver.Resolve (reglas kind='llm') y el camino de
// CONVERSACIÓN VIVA (ResolveLive). Los ayudantes (stubStore, rule, with…) están en
// config_resolver_test.go.
//
// 🔴 Deuda D-5: en producción la señal llega siempre con Intent nil, así que el peldaño de
// intención no lo alcanza nadie. Estos tests fijan la rama tal como está; no la arreglan.

// llm es una regla kind='llm' habilitada: su keyword es el nombre de la intención.
func llm(id, intentName, flowID string, changes ...func(*trigger.Rule)) trigger.Rule {
	return with(rule(id, trigger.KindLLM, intentName, "", flowID), changes...)
}

// intent es una señal con intención, texto y parámetros.
func intent(name, text string) trigger.Signal {
	return trigger.Signal{Text: text, Intent: &trigger.IntentSignal{
		Name: name, Params: map[string]string{"producto": "pizza", "cantidad": "2"}, Confidence: 0.9, ConfigVersion: "v7",
	}}
}

// TestConfigResolver_Resolve_WithoutIntent_LLMRulesDoNotFire es la conducta de PRODUCCIÓN (D-5):
// sin intención en la señal, una regla llm no dispara aunque el texto sea su keyword, y ni se le
// piden al store.
func TestConfigResolver_Resolve_WithoutIntent_LLMRulesDoNotFire(t *testing.T) {
	r, store := newResolver(llm("1", "pedido", "carrito"))
	requireDecision(t, "texto igual al nombre de la intención", resolve(t, r, "", "pedido"), trigger.Decision{Action: trigger.Ignore})
	if slices.Contains(store.asked, trigger.KindLLM) {
		t.Errorf("sin intención, Resolve pidió las reglas llm: %v", store.asked)
	}
}

// TestConfigResolver_Resolve_IntentStartsTheLLMRule: la intención casa por su NOMBRE, no por el
// texto, y la decisión lleva el nombre y los parámetros que extrajo el clasificador.
func TestConfigResolver_Resolve_IntentStartsTheLLMRule(t *testing.T) {
	r, _ := newResolver(llm("1", "pedido", "carrito"))
	sig := intent("pedido", "quiero 2 pizzas")
	requireDecision(t, "intención con regla llm", resolveSignal(t, r, "", sig),
		trigger.Decision{Action: trigger.Start, FlowID: "carrito", IntentName: "pedido", Params: sig.Intent.Params})
}

// TestConfigResolver_Resolve_IntentNameMatching: el nombre casa EXACTO tras normalizar los dos
// lados, sin mirar el match_type de la regla, y una keyword vacía no casa nunca.
func TestConfigResolver_Resolve_IntentNameMatching(t *testing.T) {
	cases := []struct {
		name       string
		keyword    string
		matchType  trigger.MatchType
		intentName string
		want       trigger.Action
	}{
		{"same name", "pedir_encuesta", trigger.MatchExact, "pedir_encuesta", trigger.Start},
		{"case, accents and spaces are normalized", "Pedir Encuesta", "", "  pedír   ENCUESTA ", trigger.Start},
		{"a contains rule still needs the whole name", "pedido", trigger.MatchContains, "pedido_urgente", trigger.Ignore},
		{"another intent", "pedido", trigger.MatchExact, "saludo", trigger.Ignore},
		{"empty keyword never matches, not even an empty name", "", trigger.MatchExact, "", trigger.Ignore},
		{"keyword of only spaces never matches", "   ", trigger.MatchExact, " ", trigger.Ignore},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(with(llm("1", c.keyword, "flow"), func(r *trigger.Rule) { r.MatchType = c.matchType }))
			got := resolveSignal(t, r, "", trigger.Signal{Intent: &trigger.IntentSignal{Name: c.intentName}})
			if got.Action != c.want {
				t.Errorf("intención %q contra la regla %q: %+v, quería la acción %d", c.intentName, c.keyword, got, c.want)
			}
		})
	}
}

// TestConfigResolver_Resolve_IntentComesBeforeText: con intención que casa, la regla llm gana
// aunque el texto case una keyword o un event_start de más priority.
func TestConfigResolver_Resolve_IntentComesBeforeText(t *testing.T) {
	r, _ := newResolver(
		with(rule("1", trigger.KindKeyword, "pedido", trigger.MatchContains, "by-keyword"), priority(9)),
		with(rule("2", trigger.KindEventStart, "pedido", trigger.MatchContains, "by-event"), eventKind(trigger.EventKindCart), priority(9)),
		llm("3", "pedido", "by-llm"),
	)
	if dec := resolveSignal(t, r, "", intent("pedido", "quiero un pedido")); dec.FlowID != "by-llm" || dec.Action != trigger.Start {
		t.Errorf("decisión = %+v, quería Start del flujo by-llm", dec)
	}
}

// TestConfigResolver_Resolve_IntentWithoutRule_FallsToText: si ninguna regla llm casa —no hay,
// es de otra intención o está deshabilitada—, la señal sigue por el texto y la decisión NO lleva
// rastro de la intención: ni nombre ni parámetros (nil, no un mapa vacío).
func TestConfigResolver_Resolve_IntentWithoutRule_FallsToText(t *testing.T) {
	keyword := rule("1", trigger.KindKeyword, "menu", trigger.MatchExact, "menu-flow")
	cases := []struct {
		name string
		llm  []trigger.Rule
	}{
		{"no llm rule", nil},
		{"llm rule for another intent", []trigger.Rule{llm("2", "pedido", "carrito")}},
		{"llm rule disabled", []trigger.Rule{llm("2", "saludo", "carrito", disabled)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(append([]trigger.Rule{keyword}, c.llm...)...)
			requireDecision(t, "cae a la keyword", resolveSignal(t, r, "", intent("saludo", "menu")),
				trigger.Decision{Action: trigger.Start, FlowID: "menu-flow"})
			requireDecision(t, "y sin keyword que case, a Ignore", resolveSignal(t, r, "", intent("saludo", "otra cosa")),
				trigger.Decision{Action: trigger.Ignore})
		})
	}
}

// TestConfigResolver_Resolve_IntentTieBreak: entre reglas llm que casan vale el desempate de
// siempre: sesión antes que global, luego priority, luego trigger_id.
func TestConfigResolver_Resolve_IntentTieBreak(t *testing.T) {
	cases := []struct {
		name  string
		rules []trigger.Rule
	}{
		{"session-specific beats global", []trigger.Rule{llm("1", "pedido", "loser", priority(9)), llm("2", "pedido", "winner", session("session-x"))}},
		{"higher priority wins", []trigger.Rule{llm("1", "pedido", "loser", priority(1)), llm("2", "pedido", "winner", priority(2))}},
		{"lower trigger_id closes the order", []trigger.Rule{llm("b", "pedido", "loser"), llm("a", "pedido", "winner")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(c.rules...)
			if dec := resolveSignal(t, r, "session-x", intent("pedido", "")); dec.FlowID != "winner" {
				t.Errorf("ganó %+v, quería la regla de FlowID winner", dec)
			}
		})
	}
}

// TestConfigResolver_Resolve_IntentWithEventKind es la segunda puerta del nacimiento (Plan 054 ·
// F2b) y su guardián: la regla llm GANADORA que trae event_kind da StartEvent con ese tipo, y la
// que no lo trae sigue dando Start SIN EventKind, byte a byte como siempre. En las dos viajan el
// nombre y los parámetros.
func TestConfigResolver_Resolve_IntentWithEventKind(t *testing.T) {
	sig := intent("pedido", "quiero 2 pizzas")

	r, _ := newResolver(llm("1", "pedido", "carrito", eventKind(trigger.EventKindCart)))
	requireDecision(t, "regla llm con event_kind", resolveSignal(t, r, "", sig), trigger.Decision{
		Action: trigger.StartEvent, FlowID: "carrito", EventKind: trigger.EventKindCart, IntentName: "pedido", Params: sig.Intent.Params,
	})

	r, _ = newResolver(llm("1", "pedido", "carrito"))
	requireDecision(t, "regla llm sin event_kind", resolveSignal(t, r, "", sig), trigger.Decision{
		Action: trigger.Start, FlowID: "carrito", IntentName: "pedido", Params: sig.Intent.Params,
	})

	// El tipo sale de la regla GANADORA, no de cualquiera que case.
	r, _ = newResolver(llm("1", "pedido", "plain", priority(9)), llm("2", "pedido", "cart", eventKind(trigger.EventKindCart)))
	requireDecision(t, "gana la regla sin event_kind", resolveSignal(t, r, "", sig), trigger.Decision{
		Action: trigger.Start, FlowID: "plain", IntentName: "pedido", Params: sig.Intent.Params,
	})
}

// TestConfigResolver_Resolve_IntentScopedByActiveEvent es el scoping de D-043.9: una regla llm
// anotada con un event_kind DISTINTO del evento activo no casa y la señal cae al texto. Las dos
// guardas de retrocompatibilidad: sin evento activo no se acota nada, y una regla sin anotar
// casa con cualquier evento activo.
func TestConfigResolver_Resolve_IntentScopedByActiveEvent(t *testing.T) {
	greeting := rule("9", trigger.KindKeyword, "hola", trigger.MatchExact, "greeting")
	annotated := llm("1", "pedir_encuesta", "survey-flow", eventKind(trigger.EventKindSurvey))
	plain := llm("1", "pedir_encuesta", "survey-flow")
	fromSurvey := trigger.Decision{Action: trigger.StartEvent, FlowID: "survey-flow", EventKind: trigger.EventKindSurvey, IntentName: "pedir_encuesta"}
	cases := []struct {
		name   string
		rule   trigger.Rule
		active string
		want   trigger.Decision
	}{
		{"annotated rule, no active event: matches", annotated, "", fromSurvey},
		{"annotated rule, same active event: matches", annotated, trigger.EventKindSurvey, fromSurvey},
		{"annotated rule, another active event: falls to text", annotated, trigger.EventKindCart, trigger.Decision{Action: trigger.Start, FlowID: "greeting"}},
		{"plain rule, any active event: matches", plain, trigger.EventKindCart, trigger.Decision{Action: trigger.Start, FlowID: "survey-flow", IntentName: "pedir_encuesta"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(c.rule, greeting)
			sig := trigger.Signal{Text: "hola", Intent: &trigger.IntentSignal{Name: "pedir_encuesta"}, ActiveEventKind: c.active}
			requireDecision(t, c.name, resolveSignal(t, r, "", sig), c.want)
		})
	}

	// El scoping descarta la regla ANTES del desempate: gana la siguiente que sí es elegible.
	r, _ := newResolver(
		llm("1", "pedir_encuesta", "survey-flow", eventKind(trigger.EventKindSurvey), priority(9)),
		llm("2", "pedir_encuesta", "cart-flow", eventKind(trigger.EventKindCart)),
	)
	sig := trigger.Signal{Intent: &trigger.IntentSignal{Name: "pedir_encuesta"}, ActiveEventKind: trigger.EventKindCart}
	requireDecision(t, "la regla del evento activo gana a la ajena de más priority", resolveSignal(t, r, "", sig),
		trigger.Decision{Action: trigger.StartEvent, FlowID: "cart-flow", EventKind: trigger.EventKindCart, IntentName: "pedir_encuesta"})
}

// TestConfigResolver_ResolveLive_StartsAndStops: con conversación viva, event_start salta por
// tipo (con el EventKind y el FlowID de la regla) y event_stop desactiva SIN EventKind ni FlowID.
func TestConfigResolver_ResolveLive_StartsAndStops(t *testing.T) {
	r, _ := newResolver(
		with(rule("1", trigger.KindEventStart, "encuesta", trigger.MatchExact, "survey-flow"), eventKind(trigger.EventKindSurvey)),
		with(rule("2", trigger.KindEventStop, "parar", trigger.MatchExact, "ignored-flow"), eventKind("ignored")),
	)
	requireDecision(t, "event_start", resolveLive(t, r, "", "  ENCUESTA "),
		trigger.Decision{Action: trigger.StartEvent, FlowID: "survey-flow", EventKind: trigger.EventKindSurvey})
	requireDecision(t, "event_stop", resolveLive(t, r, "", "Parar"), trigger.Decision{Action: trigger.StopEvent})
	requireDecision(t, "nada casa", resolveLive(t, r, "", "otra cosa"), trigger.Decision{Action: trigger.Ignore})
}

// TestConfigResolver_ResolveLive_LeavesINV02Intact: con conversación viva, keyword, fallback,
// llm y escape no deciden nada —el texto es del módulo— y al store solo se le piden event_stop
// y event_start, en ese orden.
func TestConfigResolver_ResolveLive_LeavesINV02Intact(t *testing.T) {
	r, store := newResolver(
		rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"),
		rule("2", trigger.KindFallback, "", trigger.MatchExact, "welcome"),
		llm("3", "pedido", "carrito"),
		rule("4", trigger.KindEscape, "pedido", trigger.MatchExact, ""),
	)
	for _, text := range []string{"pedido", "cualquier otra cosa"} {
		requireDecision(t, text, resolveLive(t, r, "", text), trigger.Decision{Action: trigger.Ignore})
	}
	want := []trigger.Kind{trigger.KindEventStop, trigger.KindEventStart, trigger.KindEventStop, trigger.KindEventStart}
	if !slices.Equal(store.asked, want) {
		t.Errorf("kinds pedidos al store = %v, quería %v", store.asked, want)
	}
}

// TestConfigResolver_ResolveLive_StopBeatsStart: la colisión de la configuración natural del
// dueño. «salir del carrito» casa el event_start «carrito» y el event_stop «salir del carrito»,
// los dos por contains, y DESACTIVA: el stop gana aunque el start tenga más priority o sea el
// específico de la sesión. El texto que solo pide entrar, entra.
func TestConfigResolver_ResolveLive_StopBeatsStart(t *testing.T) {
	stop := rule("2", trigger.KindEventStop, "salir del carrito", trigger.MatchContains, "")
	start := with(rule("1", trigger.KindEventStart, "carrito", trigger.MatchContains, ""), eventKind(trigger.EventKindCart))
	for _, c := range []struct {
		name  string
		start trigger.Rule
	}{
		{"default priority", start},
		{"start with higher priority", with(start, priority(9))},
		{"start specific to the session", with(start, session("session-x"))},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(c.start, stop)
			requireDecision(t, "salir del carrito", resolveLive(t, r, "session-x", "quiero salir del carrito"), trigger.Decision{Action: trigger.StopEvent})
			requireDecision(t, "quiero el carrito", resolveLive(t, r, "session-x", "quiero el carrito"),
				trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindCart})
		})
	}
}

// TestConfigResolver_ResolveLive_SameVisibilityAndTieBreak: el camino vivo hereda las reglas de
// visibilidad (habilitada, sesión, tenant) y, dentro del kind, el desempate de siempre.
func TestConfigResolver_ResolveLive_SameVisibilityAndTieBreak(t *testing.T) {
	start := func(id, keyword, kind string, changes ...func(*trigger.Rule)) trigger.Rule {
		return with(rule(id, trigger.KindEventStart, keyword, trigger.MatchExact, ""), append(changes, eventKind(kind))...)
	}
	r, _ := newResolver(
		start("1", "apagado", trigger.EventKindCart, disabled),
		start("2", "privado", trigger.EventKindCart, session("session-x")),
		start("3", "ajeno", trigger.EventKindCart, otherTenant("tenant-2")),
		start("4", "ver", trigger.EventKindMenu, priority(9)),
		start("5", "ver", trigger.EventKindSurvey, priority(1)),
		start("6", "ver", trigger.EventKindMedia, session("session-x")),
	)
	ignore := trigger.Decision{Action: trigger.Ignore}
	requireDecision(t, "regla deshabilitada", resolveLive(t, r, "", "apagado"), ignore)
	requireDecision(t, "regla de session-x desde session-y", resolveLive(t, r, "session-y", "privado"), ignore)
	requireDecision(t, "regla de session-x en session-x", resolveLive(t, r, "session-x", "privado"),
		trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindCart})
	requireDecision(t, "regla de otro tenant", resolveLive(t, r, "", "ajeno"), ignore)
	requireDecision(t, "entre globales, la de más priority", resolveLive(t, r, "session-y", "ver"),
		trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindMenu})
	requireDecision(t, "la de la sesión gana a la global de más priority", resolveLive(t, r, "session-x", "ver"),
		trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindMedia})
}

// TestConfigResolver_ResolveLive_StoreErrors: el fallo del store en cualquiera de las dos
// consultas sale tal cual, con la decisión cero.
func TestConfigResolver_ResolveLive_StoreErrors(t *testing.T) {
	boom := errors.New("base caída")
	for _, k := range []trigger.Kind{trigger.KindEventStop, trigger.KindEventStart} {
		t.Run(string(k), func(t *testing.T) {
			store := &stubStore{failOn: k, err: boom}
			dec, err := trigger.NewConfigResolver(store).ResolveLive(context.Background(), tenant, "", "carrito")
			if err != boom { //nolint:errorlint // se afirma que sale SIN envolver
				t.Errorf("err = %v, quería el del store tal cual", err)
			}
			requireDecision(t, "con error", dec, trigger.Decision{})
		})
	}
}
