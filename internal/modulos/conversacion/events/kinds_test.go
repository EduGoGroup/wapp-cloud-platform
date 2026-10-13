package events

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Aserciones de compilación: el almacén de reglas de verdad satisface el puerto estrecho, y la
// oferta tiene la firma que el despachador espera.
var (
	_ RuleLister                                                                 = (*trigger.MemoryStore)(nil)
	_ func(*TriggerKindOffer, context.Context, string, string) ([]string, error) = (*TriggerKindOffer).OfferedKinds
)

// fixedRules es un RuleLister que contesta siempre lo mismo y apunta lo que le piden.
type fixedRules struct {
	rules []trigger.Rule
	err   error
	asked []string
}

func (f *fixedRules) ListByKind(_ context.Context, tenantID, sessionID string, k trigger.Kind) ([]trigger.Rule, error) {
	f.asked = append(f.asked, tenantID+"|"+sessionID+"|"+string(k))
	return f.rules, f.err
}

func eventStart(eventKind string, enabled bool) trigger.Rule {
	return trigger.Rule{Kind: trigger.KindEventStart, EventKind: eventKind, Enabled: enabled}
}

// TestOfferedKinds_DistinctSortedEnabledWithKind: tipos distintos, en orden alfabético, sin las
// reglas apagadas ni las que no dicen qué tipo paren.
func TestOfferedKinds_DistinctSortedEnabledWithKind(t *testing.T) {
	cases := []struct {
		name  string
		rules []trigger.Rule
		want  []string
	}{
		{"no rules", nil, []string{}},
		{"two words for the same kind are one option", []trigger.Rule{eventStart("cart", true), eventStart("cart", true)}, []string{"cart"}},
		{"alphabetical", []trigger.Rule{eventStart("survey", true), eventStart("cart", true), eventStart("media", true)}, []string{"cart", "media", "survey"}},
		{"a disabled rule offers nothing", []trigger.Rule{eventStart("cart", false)}, []string{}},
		{"a rule without event kind offers nothing", []trigger.Rule{eventStart("", true)}, []string{}},
		{"disabled twin does not hide the enabled one", []trigger.Rule{eventStart("cart", false), eventStart("cart", true)}, []string{"cart"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewTriggerKindOffer(&fixedRules{rules: c.rules}).OfferedKinds(t.Context(), "t-1", "s-1")
			if err != nil {
				t.Fatalf("OfferedKinds: %v", err)
			}
			if got == nil || !slices.Equal(got, c.want) {
				t.Errorf("OfferedKinds = %#v, quería %#v", got, c.want)
			}
		})
	}
}

// TestOfferedKinds_AsksForEventStartOfThatTenantAndSession: UNA lectura, con el tenant, la sesión
// y kind='event_start'.
func TestOfferedKinds_AsksForEventStartOfThatTenantAndSession(t *testing.T) {
	rules := &fixedRules{}
	offer := NewTriggerKindOffer(rules)
	if _, err := offer.OfferedKinds(t.Context(), "t-1", "s-1"); err != nil {
		t.Fatalf("OfferedKinds: %v", err)
	}
	if want := []string{"t-1|s-1|event_start"}; !slices.Equal(rules.asked, want) {
		t.Errorf("lecturas = %v, quería %v", rules.asked, want)
	}
}

// TestOfferedKinds_ReaderFailureIsWrapped: el fallo del lector llega envuelto con su texto y sin
// lista.
func TestOfferedKinds_ReaderFailureIsWrapped(t *testing.T) {
	boom := errors.New("boom")
	got, err := NewTriggerKindOffer(&fixedRules{err: boom}).OfferedKinds(t.Context(), "t-1", "s-1")
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "events: leer las reglas event_start del tenant: ") {
		t.Errorf("err = %v, quería el fallo envuelto", err)
	}
	if got != nil {
		t.Errorf("lista = %v, quería nil", got)
	}
}

// TestOfferedKinds_OverTheRealMemoryStore: contra el almacén de reglas de verdad (en memoria), la
// regla de otro tenant no existe, la de otra sesión no se ofrece aquí, la global sí, y los demás
// kinds no ofrecen tipos.
func TestOfferedKinds_OverTheRealMemoryStore(t *testing.T) {
	store := trigger.NewMemoryStore()
	seed := []trigger.Rule{
		{TenantID: "t-1", Kind: trigger.KindEventStart, EventKind: "cart", Keyword: "pedido", Enabled: true},
		{TenantID: "t-1", Kind: trigger.KindEventStart, EventKind: "survey", Keyword: "encuesta", Enabled: true, SessionID: "s-1"},
		{TenantID: "t-1", Kind: trigger.KindEventStart, EventKind: "media", Keyword: "docs", Enabled: true, SessionID: "s-2"},
		{TenantID: "t-2", Kind: trigger.KindEventStart, EventKind: "menu", Keyword: "menu", Enabled: true},
		{TenantID: "t-1", Kind: trigger.KindKeyword, Keyword: "hola", FlowID: "f", Enabled: true},
		{TenantID: "t-1", Kind: trigger.KindEventStop, Keyword: "salir", Enabled: true},
	}
	for _, r := range seed {
		if _, err := store.Insert(t.Context(), r); err != nil {
			t.Fatalf("sembrar la regla %+v: %v", r, err)
		}
	}
	got, err := NewTriggerKindOffer(store).OfferedKinds(t.Context(), "t-1", "s-1")
	if err != nil {
		t.Fatalf("OfferedKinds: %v", err)
	}
	if want := []string{"cart", "survey"}; !slices.Equal(got, want) {
		t.Errorf("OfferedKinds = %v, quería %v", got, want)
	}
}
