package trigger_test

import (
	"context"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// NoopResolver cumple el puerto, y lo que devuelve el constructor también.
var (
	_ trigger.Resolver            = trigger.NoopResolver{}
	_ func() trigger.NoopResolver = trigger.NewNoopResolver
)

// TestKinds_AreTheStoredLiterals: los valores de Kind son los que viven en flow_triggers.kind.
func TestKinds_AreTheStoredLiterals(t *testing.T) {
	cases := []struct {
		got  trigger.Kind
		want string
	}{
		{trigger.KindKeyword, "keyword"},
		{trigger.KindFallback, "fallback"},
		{trigger.KindEscape, "escape"},
		{trigger.KindLLM, "llm"},
		{trigger.KindEventStart, "event_start"},
		{trigger.KindEventStop, "event_stop"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("Kind = %q, quería %q", c.got, c.want)
		}
	}
}

// TestMatchTypes_AreTheStoredLiterals: los de flow_triggers.match_type.
func TestMatchTypes_AreTheStoredLiterals(t *testing.T) {
	if trigger.MatchExact != trigger.MatchType("exact") || trigger.MatchContains != trigger.MatchType("contains") {
		t.Errorf("(MatchExact, MatchContains) = (%q, %q), quería (exact, contains)", trigger.MatchExact, trigger.MatchContains)
	}
}

// TestFactoryEventKinds_ClosedVocabularyInStableOrder: los cuatro tipos de fábrica, en su orden,
// y cada llamada entrega su propia lista (quien la reciba no puede estropear la siguiente).
func TestFactoryEventKinds_ClosedVocabularyInStableOrder(t *testing.T) {
	want := []string{"menu", "cart", "survey", "media"}
	got := trigger.FactoryEventKinds()
	if !slices.Equal(got, want) {
		t.Fatalf("FactoryEventKinds() = %v, quería %v", got, want)
	}
	if consts := []string{trigger.EventKindMenu, trigger.EventKindCart, trigger.EventKindSurvey, trigger.EventKindMedia}; !slices.Equal(consts, want) {
		t.Errorf("las constantes EventKind* = %v, quería %v", consts, want)
	}
	got[0] = "tampered"
	if again := trigger.FactoryEventKinds(); !slices.Equal(again, want) {
		t.Errorf("tras modificar la lista devuelta, FactoryEventKinds() = %v, quería %v", again, want)
	}
}

// TestIsFactoryEventKind: solo los cuatro de fábrica, byte a byte (sin normalizar).
func TestIsFactoryEventKind(t *testing.T) {
	cases := []struct {
		name string
		kind string
		want bool
	}{
		{"menu", trigger.EventKindMenu, true},
		{"cart", trigger.EventKindCart, true},
		{"survey", trigger.EventKindSurvey, true},
		{"media", trigger.EventKindMedia, true},
		{"empty", "", false},
		{"typo that used to slip in with a 200", "carrrito", false},
		{"upper case is not folded", "CART", false},
		{"surrounding space is not trimmed", " cart", false},
		{"a trigger kind is not an event kind", string(trigger.KindEventStart), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trigger.IsFactoryEventKind(c.kind); got != c.want {
				t.Errorf("IsFactoryEventKind(%q) = %v, quería %v", c.kind, got, c.want)
			}
		})
	}
}

// TestAction_ZeroValueIsIgnore: el valor cero de Action —y por tanto de Decision— es Ignore, el
// default seguro (INV-6), y las seis acciones son distintas entre sí.
func TestAction_ZeroValueIsIgnore(t *testing.T) {
	var zero trigger.Action
	if zero != trigger.Ignore {
		t.Errorf("el valor cero de Action = %d, quería Ignore", zero)
	}
	if (trigger.Decision{}).Action != trigger.Ignore {
		t.Error("la Decision cero no es Ignore")
	}
	actions := []trigger.Action{
		trigger.Ignore, trigger.Start, trigger.Fallback, trigger.Escape, trigger.StartEvent, trigger.StopEvent,
	}
	seen := make(map[trigger.Action]bool, len(actions))
	for _, a := range actions {
		if seen[a] {
			t.Errorf("la acción %d está repetida entre las seis", a)
		}
		seen[a] = true
	}
}

// TestNoopResolver_NeverStartsNorEscapes es INV-6: con el resolver por defecto el comportamiento
// es el previo al Plan 019. Un entrante sin conversación viva se ignora, nada es escape y una
// conversación viva no salta ni se desactiva, venga lo que venga en la señal —también una
// intención, la rama que en producción no llega (deuda D-5)—.
func TestNoopResolver_NeverStartsNorEscapes(t *testing.T) {
	var r trigger.Resolver = trigger.NewNoopResolver()
	ctx := context.Background()
	signals := []trigger.Signal{
		{},
		{Text: "pedido"},
		{Text: "pedido", ActiveEventKind: trigger.EventKindCart},
		{Text: "quiero 2 pizzas", Intent: &trigger.IntentSignal{
			Name: "pedido", Params: map[string]string{"producto": "pizza"}, Confidence: 0.99, ConfigVersion: "v1",
		}},
	}
	for _, sig := range signals {
		dec, err := r.Resolve(ctx, "tenant-a", "session-1", sig)
		if err != nil {
			t.Fatalf("Resolve(%+v): error inesperado %v", sig, err)
		}
		if dec.Action != trigger.Ignore || dec.FlowID != "" || dec.EventKind != "" || dec.IntentName != "" || dec.Params != nil {
			t.Errorf("Resolve(%+v) = %+v, quería la Decision cero (Ignore)", sig, dec)
		}
	}

	matched, message, err := r.IsEscape(ctx, "tenant-a", "session-1", "salir")
	if err != nil || matched || message != "" {
		t.Errorf("IsEscape = (%v, %q, %v), quería (false, \"\", nil)", matched, message, err)
	}

	dec, err := r.ResolveLive(ctx, "tenant-a", "session-1", "carrito")
	if err != nil {
		t.Fatalf("ResolveLive: error inesperado %v", err)
	}
	if dec.Action != trigger.Ignore || dec.FlowID != "" || dec.EventKind != "" {
		t.Errorf("ResolveLive = %+v, quería la Decision cero (Ignore)", dec)
	}
}

// TestRule_IsComparable: Rule se compara con ==, que es como la suite de contrato del puerto
// afirma que una regla volvió entera. Un campo nuevo que no sea comparable rompe aquí.
func TestRule_IsComparable(t *testing.T) {
	a := trigger.Rule{
		TenantID: "t", TriggerID: "id", Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact,
		FlowID: "f", Priority: 1, Enabled: true, Message: "m", SessionID: "s", EventKind: trigger.EventKindMenu,
	}
	b := a
	if a != b {
		t.Error("dos copias de la misma Rule no son iguales")
	}
	b.EventKind = ""
	if a == b {
		t.Error("dos Rule que difieren en EventKind son iguales")
	}
}
