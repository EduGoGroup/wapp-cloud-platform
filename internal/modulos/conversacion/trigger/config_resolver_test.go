package trigger_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// ConfigResolver cumple el puerto Resolver.
var _ trigger.Resolver = (*trigger.ConfigResolver)(nil)

const tenant = "tenant-1"

// stubStore es el trigger.Store sobre el que se prueba el resolver: entrega por ListByKind las
// reglas que el test le da, con el filtro y el orden que promete el puerto (tenant, kind, la
// sesión pedida o global; por trigger_id), apunta qué se le preguntó y puede fallar en un kind.
// A diferencia de MemoryStore, el trigger_id lo pone el test: así el último peldaño del desempate
// es determinista. El resolver solo usa ListByKind; el resto del puerto falla el test.
type stubStore struct {
	trigger.Store
	rules []trigger.Rule
	// failOn: ListByKind de ese kind devuelve err.
	failOn trigger.Kind
	err    error
	// asked son los kinds consultados, en orden.
	asked []trigger.Kind
}

func (s *stubStore) ListByKind(_ context.Context, tenantID, sessionID string, k trigger.Kind) ([]trigger.Rule, error) {
	s.asked = append(s.asked, k)
	if s.err != nil && k == s.failOn {
		return nil, s.err
	}
	out := make([]trigger.Rule, 0)
	for _, r := range s.rules {
		if r.TenantID == tenantID && r.Kind == k && (r.SessionID == sessionID || r.SessionID == "") {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b trigger.Rule) int { return strings.Compare(a.TriggerID, b.TriggerID) })
	return out, nil
}

// rule es una regla habilitada del tenant de los tests; id es su trigger_id.
func rule(id string, k trigger.Kind, keyword string, mt trigger.MatchType, flowID string) trigger.Rule {
	return trigger.Rule{TenantID: tenant, TriggerID: id, Kind: k, Keyword: keyword, MatchType: mt, FlowID: flowID, Enabled: true}
}

// with devuelve la regla tras aplicarle los cambios.
func with(r trigger.Rule, changes ...func(*trigger.Rule)) trigger.Rule {
	for _, change := range changes {
		change(&r)
	}
	return r
}

func priority(p int) func(*trigger.Rule)        { return func(r *trigger.Rule) { r.Priority = p } }
func session(id string) func(*trigger.Rule)     { return func(r *trigger.Rule) { r.SessionID = id } }
func eventKind(k string) func(*trigger.Rule)    { return func(r *trigger.Rule) { r.EventKind = k } }
func message(text string) func(*trigger.Rule)   { return func(r *trigger.Rule) { r.Message = text } }
func otherTenant(id string) func(*trigger.Rule) { return func(r *trigger.Rule) { r.TenantID = id } }
func disabled(r *trigger.Rule)                  { r.Enabled = false }

// newResolver monta el resolver sobre un stubStore con esas reglas.
func newResolver(rules ...trigger.Rule) (*trigger.ConfigResolver, *stubStore) {
	store := &stubStore{rules: rules}
	return trigger.NewConfigResolver(store), store
}

// resolve llama a Resolve con solo texto y falla el test si hay error.
func resolve(t *testing.T, r *trigger.ConfigResolver, sessionID, text string) trigger.Decision {
	t.Helper()
	return resolveSignal(t, r, sessionID, trigger.Signal{Text: text})
}

func resolveSignal(t *testing.T, r *trigger.ConfigResolver, sessionID string, sig trigger.Signal) trigger.Decision {
	t.Helper()
	dec, err := r.Resolve(context.Background(), tenant, sessionID, sig)
	if err != nil {
		t.Fatalf("Resolve(sesión %q, %+v): error inesperado %v", sessionID, sig, err)
	}
	return dec
}

// resolveLive llama a ResolveLive y falla el test si hay error. Sus casos están en
// config_resolver_intent_test.go, con los del peldaño de intención.
func resolveLive(t *testing.T, r *trigger.ConfigResolver, sessionID, text string) trigger.Decision {
	t.Helper()
	dec, err := r.ResolveLive(context.Background(), tenant, sessionID, text)
	if err != nil {
		t.Fatalf("ResolveLive(sesión %q, %q): error inesperado %v", sessionID, text, err)
	}
	return dec
}

// requireDecision compara la decisión entera: los cinco campos.
func requireDecision(t *testing.T, what string, got, want trigger.Decision) {
	t.Helper()
	if got.Action != want.Action || got.FlowID != want.FlowID || got.EventKind != want.EventKind ||
		got.IntentName != want.IntentName || !mapsEqualOrBothNil(got.Params, want.Params) {
		t.Errorf("%s: decisión = %+v, quería %+v", what, got, want)
	}
}

// mapsEqualOrBothNil distingue nil de vacío: una decisión por texto lleva Params nil.
func mapsEqualOrBothNil(a, b map[string]string) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestNewConfigResolver_DoesNotConsultTheStore: construir no lee reglas.
func TestNewConfigResolver_DoesNotConsultTheStore(t *testing.T) {
	r, store := newResolver(rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"))
	if r == nil {
		t.Fatal("NewConfigResolver devolvió nil")
	}
	if len(store.asked) != 0 {
		t.Errorf("construir el resolver consultó el store: %v", store.asked)
	}
}

// TestConfigResolver_Resolve_MatchTypes: exact exige igualdad tras normalizar, contains que el
// texto contenga la keyword, y un match_type desconocido (o vacío) se trata como exact.
func TestConfigResolver_Resolve_MatchTypes(t *testing.T) {
	cases := []struct {
		name      string
		matchType trigger.MatchType
		text      string
		want      trigger.Action
	}{
		{"exact: same text", trigger.MatchExact, "pedido", trigger.Start},
		{"exact: case, accents and spaces are normalized", trigger.MatchExact, "   PEDÍDO  ", trigger.Start},
		{"exact: a substring is not enough", trigger.MatchExact, "quiero un pedido", trigger.Ignore},
		{"contains: keyword inside the text", trigger.MatchContains, "quiero un PEDIDO por favor", trigger.Start},
		{"contains: keyword inside a longer word", trigger.MatchContains, "pedidos", trigger.Start},
		{"contains: text without the keyword", trigger.MatchContains, "quiero pedir", trigger.Ignore},
		{"unknown match type: behaves as exact", trigger.MatchType("regex"), "pedido", trigger.Start},
		{"unknown match type: does not behave as contains", trigger.MatchType("regex"), "un pedido", trigger.Ignore},
		{"empty match type: behaves as exact", trigger.MatchType(""), "un pedido", trigger.Ignore},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(rule("1", trigger.KindKeyword, "pedido", c.matchType, "carrito"))
			want := trigger.Decision{Action: c.want}
			if c.want == trigger.Start {
				want.FlowID = "carrito"
			}
			requireDecision(t, c.text, resolve(t, r, "", c.text), want)
		})
	}
}

// TestConfigResolver_Resolve_OnlyEnabledRulesCount: una regla deshabilitada no casa en ningún
// peldaño —keyword, event_start, fallback— y el resultado es Ignore.
func TestConfigResolver_Resolve_OnlyEnabledRulesCount(t *testing.T) {
	r, _ := newResolver(
		with(rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"), disabled),
		with(rule("2", trigger.KindEventStart, "pedido", trigger.MatchExact, ""), eventKind(trigger.EventKindCart), disabled),
		with(rule("3", trigger.KindFallback, "", trigger.MatchExact, "menu"), disabled),
	)
	requireDecision(t, "todo deshabilitado", resolve(t, r, "", "pedido"), trigger.Decision{Action: trigger.Ignore})
}

// TestConfigResolver_Resolve_TieBreak recorre el desempate peldaño a peldaño. En cada caso casan
// las dos reglas y gana la de FlowID "winner"; el criterio anterior está empatado y el siguiente
// juega EN CONTRA, para que no acierte por casualidad.
func TestConfigResolver_Resolve_TieBreak(t *testing.T) {
	kw := func(id, keyword string, mt trigger.MatchType, flowID string, changes ...func(*trigger.Rule)) trigger.Rule {
		return with(rule(id, trigger.KindKeyword, keyword, mt, flowID), changes...)
	}
	cases := []struct {
		name  string
		rules []trigger.Rule
	}{
		{"session-specific beats global, even with lower priority", []trigger.Rule{
			kw("1", "hola", trigger.MatchExact, "loser", priority(9)),
			kw("2", "hola", trigger.MatchContains, "winner", session("session-x")),
		}},
		{"higher priority beats exact-before-contains", []trigger.Rule{
			kw("1", "hola", trigger.MatchExact, "loser", priority(1)),
			kw("2", "hola", trigger.MatchContains, "winner", priority(9)),
		}},
		{"exact beats contains, against keyword order", []trigger.Rule{
			kw("1", "ho", trigger.MatchContains, "loser"),
			kw("2", "hola", trigger.MatchExact, "winner"),
		}},
		{"lower keyword wins, against trigger_id order", []trigger.Rule{
			kw("1", "ola", trigger.MatchContains, "loser"),
			kw("2", "hol", trigger.MatchContains, "winner"),
		}},
		{"lower trigger_id closes the order", []trigger.Rule{
			kw("b", "hola", trigger.MatchExact, "loser"),
			kw("a", "hola", trigger.MatchExact, "winner"),
		}},
		{"the kind does not break ties: an event_start with lower trigger_id wins", []trigger.Rule{
			kw("b", "hola", trigger.MatchExact, "loser"),
			rule("a", trigger.KindEventStart, "hola", trigger.MatchExact, "winner"),
		}},
		{"the kind does not break ties: a keyword with lower trigger_id wins", []trigger.Rule{
			rule("b", trigger.KindEventStart, "hola", trigger.MatchExact, "loser"),
			kw("a", "hola", trigger.MatchExact, "winner"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, rules := range [][]trigger.Rule{c.rules, {c.rules[1], c.rules[0]}} {
				r, _ := newResolver(rules...)
				if dec := resolve(t, r, "session-x", "hola"); dec.FlowID != "winner" {
					t.Errorf("ganó %+v, quería la regla de FlowID winner", dec)
				}
			}
		})
	}
}

// TestConfigResolver_Resolve_EventStartIsPeerOfKeyword: event_start compite en el peldaño de
// keyword y manda la priority, en los dos sentidos. La decisión de un event_start lleva el tipo
// de evento y el FlowID de la regla; la de una keyword, solo el FlowID.
func TestConfigResolver_Resolve_EventStartIsPeerOfKeyword(t *testing.T) {
	keyword := rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "old-flow")
	start := with(rule("2", trigger.KindEventStart, "pedido", trigger.MatchExact, "cart-flow"), eventKind(trigger.EventKindCart))

	r, _ := newResolver(with(keyword, priority(1)), with(start, priority(9)))
	requireDecision(t, "event_start con más priority", resolve(t, r, "", "pedido"),
		trigger.Decision{Action: trigger.StartEvent, FlowID: "cart-flow", EventKind: trigger.EventKindCart})

	r, _ = newResolver(with(keyword, priority(9)), with(start, priority(1)))
	requireDecision(t, "keyword con más priority", resolve(t, r, "", "pedido"),
		trigger.Decision{Action: trigger.Start, FlowID: "old-flow"})

	// Un event_start sin flujo (el menú no arranca ninguno) da StartEvent con FlowID vacío.
	r, _ = newResolver(with(rule("1", trigger.KindEventStart, "menu", trigger.MatchExact, ""), eventKind(trigger.EventKindMenu)))
	requireDecision(t, "event_start sin flujo", resolve(t, r, "", "  MENÚ "),
		trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindMenu})
}

// TestConfigResolver_Resolve_Fallback: sin keyword ni event_start que casen, arranca el fallback;
// si algo casa, el fallback no entra; y sin fallback, Ignore.
func TestConfigResolver_Resolve_Fallback(t *testing.T) {
	r, _ := newResolver(
		rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"),
		with(rule("2", trigger.KindEventStart, "encuesta", trigger.MatchExact, ""), eventKind(trigger.EventKindSurvey)),
		with(rule("3", trigger.KindFallback, "", trigger.MatchExact, "welcome"), priority(3)),
	)
	requireDecision(t, "nada casa", resolve(t, r, "", "buenas"), trigger.Decision{Action: trigger.Fallback, FlowID: "welcome"})
	requireDecision(t, "casa la keyword", resolve(t, r, "", "pedido"), trigger.Decision{Action: trigger.Start, FlowID: "carrito"})
	requireDecision(t, "casa el event_start", resolve(t, r, "", "encuesta"),
		trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindSurvey})

	r, _ = newResolver(rule("1", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"))
	requireDecision(t, "sin fallback", resolve(t, r, "", "buenas"), trigger.Decision{Action: trigger.Ignore})
}

// TestConfigResolver_Resolve_FallbackPicksTheBest: entre fallbacks habilitados gana el de sesión,
// luego la mayor priority, luego la keyword menor y por último el trigger_id menor. El match_type
// no cuenta aquí.
func TestConfigResolver_Resolve_FallbackPicksTheBest(t *testing.T) {
	fb := func(id, flowID string, changes ...func(*trigger.Rule)) trigger.Rule {
		return with(rule(id, trigger.KindFallback, "", trigger.MatchExact, flowID), changes...)
	}
	cases := []struct {
		name  string
		rules []trigger.Rule
	}{
		{"session-specific beats global with higher priority", []trigger.Rule{
			fb("1", "loser", priority(9)), fb("2", "winner", session("session-x")),
		}},
		{"higher priority wins; a disabled one with more priority does not", []trigger.Rule{
			fb("1", "loser", priority(1)), fb("2", "winner", priority(5)), fb("0", "off", priority(9), disabled),
		}},
		{"lower keyword wins, against trigger_id order", []trigger.Rule{
			with(fb("1", "loser"), func(r *trigger.Rule) { r.Keyword = "b" }),
			with(fb("2", "winner"), func(r *trigger.Rule) { r.Keyword = "a" }),
		}},
		{"lower trigger_id closes the order, whatever the match type", []trigger.Rule{
			fb("b", "loser"), with(fb("a", "winner"), func(r *trigger.Rule) { r.MatchType = trigger.MatchContains }),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(c.rules...)
			requireDecision(t, "fallback", resolve(t, r, "session-x", "buenas"), trigger.Decision{Action: trigger.Fallback, FlowID: "winner"})
		})
	}
}

// TestConfigResolver_Resolve_AsksTheStoreForTenantAndSession: lo que ve el resolver es lo que el
// store entrega para ESE tenant y ESA sesión (INV-8): la regla de otro tenant y la de otra sesión
// no existen, y la global vale para cualquier sesión.
func TestConfigResolver_Resolve_AsksTheStoreForTenantAndSession(t *testing.T) {
	r, _ := newResolver(
		with(rule("1", trigger.KindKeyword, "ajeno", trigger.MatchExact, "foreign"), otherTenant("tenant-2")),
		with(rule("2", trigger.KindKeyword, "hola", trigger.MatchExact, "of-x"), session("session-x")),
		rule("3", trigger.KindKeyword, "global", trigger.MatchExact, "global"),
	)
	requireDecision(t, "regla de otro tenant", resolve(t, r, "", "ajeno"), trigger.Decision{Action: trigger.Ignore})
	requireDecision(t, "regla de session-x vista desde session-y", resolve(t, r, "session-y", "hola"), trigger.Decision{Action: trigger.Ignore})
	requireDecision(t, "regla de session-x vista sin sesión", resolve(t, r, "", "hola"), trigger.Decision{Action: trigger.Ignore})
	requireDecision(t, "regla de session-x en session-x", resolve(t, r, "session-x", "hola"), trigger.Decision{Action: trigger.Start, FlowID: "of-x"})
	requireDecision(t, "regla global en session-y", resolve(t, r, "session-y", "global"), trigger.Decision{Action: trigger.Start, FlowID: "global"})
}

// TestConfigResolver_Resolve_NeverEvaluatesEventStop: sin conversación viva no hay evento que
// desactivar. Un event_stop que casaría no decide nada y ni siquiera se le pide al store.
func TestConfigResolver_Resolve_NeverEvaluatesEventStop(t *testing.T) {
	r, store := newResolver(
		rule("1", trigger.KindEventStop, "parar", trigger.MatchExact, ""),
		rule("2", trigger.KindFallback, "", trigger.MatchExact, "welcome"),
	)
	requireDecision(t, "event_stop sin conversación viva", resolve(t, r, "", "parar"), trigger.Decision{Action: trigger.Fallback, FlowID: "welcome"})
	if slices.Contains(store.asked, trigger.KindEventStop) {
		t.Errorf("Resolve pidió al store las reglas event_stop: %v", store.asked)
	}
}

// TestConfigResolver_Resolve_StoreErrors: el fallo del store en cualquier peldaño sale tal cual,
// con la decisión cero. El de llm solo se alcanza con intención.
func TestConfigResolver_Resolve_StoreErrors(t *testing.T) {
	boom := errors.New("base caída")
	for _, k := range []trigger.Kind{trigger.KindLLM, trigger.KindEventStart, trigger.KindKeyword, trigger.KindFallback} {
		t.Run(string(k), func(t *testing.T) {
			store := &stubStore{failOn: k, err: boom}
			sig := trigger.Signal{Text: "hola", Intent: &trigger.IntentSignal{Name: "pedido"}}
			dec, err := trigger.NewConfigResolver(store).Resolve(context.Background(), tenant, "", sig)
			if err != boom { //nolint:errorlint // se afirma que sale SIN envolver
				t.Errorf("err = %v, quería el del store tal cual", err)
			}
			requireDecision(t, "con error", dec, trigger.Decision{})
		})
	}
}

// TestConfigResolver_IsEscape: casa con la misma normalización y devuelve el aviso de la regla
// (vacío si no define uno); sin coincidencia, deshabilitada, de otro tenant o de otra sesión, no
// es escape. Solo consulta las reglas escape.
func TestConfigResolver_IsEscape(t *testing.T) {
	r, store := newResolver(
		with(rule("1", trigger.KindEscape, "salir", trigger.MatchExact, ""), message("Hasta pronto 👋")),
		rule("2", trigger.KindEscape, "chao", trigger.MatchContains, ""),
		with(rule("3", trigger.KindEscape, "apagado", trigger.MatchExact, ""), message("no"), disabled),
		with(rule("4", trigger.KindEscape, "ajeno", trigger.MatchExact, ""), otherTenant("tenant-2")),
		with(rule("5", trigger.KindEscape, "privado", trigger.MatchExact, ""), session("session-x")),
		rule("6", trigger.KindKeyword, "pedido", trigger.MatchExact, "carrito"),
	)
	cases := []struct {
		name, session, text string
		matched             bool
		message             string
	}{
		{"normalized exact match returns its message", "", "  SALÍR ", true, "Hasta pronto 👋"},
		{"contains match without message returns empty", "", "bueno, chao!", true, ""},
		{"no rule matches", "", "hola", false, ""},
		{"disabled rule", "", "apagado", false, ""},
		{"rule of another tenant", "", "ajeno", false, ""},
		{"rule of another session", "session-y", "privado", false, ""},
		{"rule of its own session", "session-x", "privado", true, ""},
		{"a keyword rule is not an escape", "", "pedido", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			matched, msg, err := r.IsEscape(context.Background(), tenant, c.session, c.text)
			if err != nil {
				t.Fatalf("IsEscape: error inesperado %v", err)
			}
			if matched != c.matched || msg != c.message {
				t.Errorf("IsEscape(%q) = (%v, %q), quería (%v, %q)", c.text, matched, msg, c.matched, c.message)
			}
		})
	}
	if slices.ContainsFunc(store.asked, func(k trigger.Kind) bool { return k != trigger.KindEscape }) {
		t.Errorf("IsEscape consultó kinds que no son escape: %v", store.asked)
	}
}

// TestConfigResolver_IsEscape_WhichMessageWins: si casan varias, manda la específica de sesión
// (llegue antes o después que la global); a igual especificidad, la de trigger_id menor, y la
// priority NO cuenta.
func TestConfigResolver_IsEscape_WhichMessageWins(t *testing.T) {
	esc := func(id, msg string, changes ...func(*trigger.Rule)) trigger.Rule {
		return with(rule(id, trigger.KindEscape, "salir", trigger.MatchExact, ""), append(changes, message(msg))...)
	}
	cases := []struct {
		name  string
		rules []trigger.Rule
	}{
		{"session-specific after the global", []trigger.Rule{esc("1", "loser", priority(9)), esc("2", "winner", session("session-x"))}},
		{"session-specific before the global", []trigger.Rule{esc("1", "winner", session("session-x")), esc("2", "loser", priority(9))}},
		{"same specificity: first by trigger_id, not by priority", []trigger.Rule{esc("a", "winner"), esc("b", "loser", priority(9))}},
		{"two of the session: first by trigger_id", []trigger.Rule{esc("b", "loser", session("session-x")), esc("a", "winner", session("session-x"))}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(c.rules...)
			matched, msg, err := r.IsEscape(context.Background(), tenant, "session-x", "salir")
			if err != nil || !matched || msg != "winner" {
				t.Errorf("IsEscape = (%v, %q, %v), quería (true, winner, nil)", matched, msg, err)
			}
		})
	}
}

// TestConfigResolver_IsEscape_StoreError: el fallo del store sale tal cual, sin escape ni aviso.
func TestConfigResolver_IsEscape_StoreError(t *testing.T) {
	boom := errors.New("base caída")
	store := &stubStore{failOn: trigger.KindEscape, err: boom}
	matched, msg, err := trigger.NewConfigResolver(store).IsEscape(context.Background(), tenant, "", "salir")
	if err != boom || matched || msg != "" { //nolint:errorlint // se afirma que sale SIN envolver
		t.Errorf("IsEscape = (%v, %q, %v), quería (false, \"\", el error del store)", matched, msg, err)
	}
}
