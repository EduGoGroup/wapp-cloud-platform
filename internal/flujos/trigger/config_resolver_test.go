package trigger_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// seed inserta reglas y devuelve el resolver listo.
func seed(t *testing.T, rules ...trigger.Rule) *trigger.ConfigResolver {
	t.Helper()
	s := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := s.Insert(context.Background(), r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return trigger.NewConfigResolver(s)
}

// mustResolve ejecuta Resolve y falla si hay error.
func mustResolve(t *testing.T, r *trigger.ConfigResolver, tenantID, text string) trigger.Decision {
	t.Helper()
	dec, err := r.Resolve(context.Background(), tenantID, text)
	if err != nil {
		t.Fatalf("resolve(%q,%q): %v", tenantID, text, err)
	}
	return dec
}

// mustEscape ejecuta IsEscape y falla si hay error, devolviendo también el message.
func mustEscape(t *testing.T, r *trigger.ConfigResolver, tenantID, text string) (bool, string) {
	t.Helper()
	esc, msg, err := r.IsEscape(context.Background(), tenantID, text)
	if err != nil {
		t.Fatalf("isEscape(%q,%q): %v", tenantID, text, err)
	}
	return esc, msg
}

func TestConfigResolver_ExactMatchStarts(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true})
	dec := mustResolve(t, r, "t1", "Pedido")
	if dec.Action != trigger.Start || dec.FlowID != "carrito" {
		t.Fatalf("esperaba Start/carrito, got %+v", dec)
	}
}

func TestConfigResolver_ExactRequiresFullEquality(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true})
	if dec := mustResolve(t, r, "t1", "quiero un pedido"); dec.Action != trigger.Ignore {
		t.Fatalf("exact no debe casar substring, got %+v", dec)
	}
}

func TestConfigResolver_ContainsMatch(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchContains, FlowID: "carrito", Enabled: true})
	dec := mustResolve(t, r, "t1", "quiero un PEDIDO por favor")
	if dec.Action != trigger.Start || dec.FlowID != "carrito" {
		t.Fatalf("esperaba Start/carrito por contains, got %+v", dec)
	}
}

func TestConfigResolver_NormalizeAccentsCaseSpaces(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "menú", MatchType: trigger.MatchExact, FlowID: "menu", Enabled: true})
	// mayúsculas + sin acento + espacios extra
	dec := mustResolve(t, r, "t1", "   MENU  ")
	if dec.Action != trigger.Start || dec.FlowID != "menu" {
		t.Fatalf("normalización acentos/mayúsculas/espacios falló, got %+v", dec)
	}
}

func TestConfigResolver_PriorityWins(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "low", Priority: 1, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "high", Priority: 9, Enabled: true},
	)
	if dec := mustResolve(t, r, "t1", "hola"); dec.FlowID != "high" {
		t.Fatalf("debe ganar mayor priority, got %+v", dec)
	}
}

func TestConfigResolver_ExactBeatsContainsOnTie(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchContains, FlowID: "byContains", Priority: 0, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "byExact", Priority: 0, Enabled: true},
	)
	if dec := mustResolve(t, r, "t1", "hola"); dec.FlowID != "byExact" {
		t.Fatalf("empate priority debe ganar exact, got %+v", dec)
	}
}

func TestConfigResolver_DisabledRuleIgnored(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: false})
	if dec := mustResolve(t, r, "t1", "pedido"); dec.Action != trigger.Ignore {
		t.Fatalf("regla deshabilitada no debe casar, got %+v", dec)
	}
}

func TestConfigResolver_FallbackWhenNoMatch(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "menu", Priority: 3, Enabled: true},
	)
	dec := mustResolve(t, r, "t1", "cualquier cosa")
	if dec.Action != trigger.Fallback || dec.FlowID != "menu" {
		t.Fatalf("esperaba Fallback/menu, got %+v", dec)
	}
}

func TestConfigResolver_FallbackPicksHighestPriority(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "low", Priority: 1, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "high", Priority: 5, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "off", Priority: 9, Enabled: false},
	)
	if dec := mustResolve(t, r, "t1", "nada"); dec.FlowID != "high" {
		t.Fatalf("fallback debe elegir mayor priority habilitado, got %+v", dec)
	}
}

func TestConfigResolver_NoMatchNoFallbackIgnores(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true})
	if dec := mustResolve(t, r, "t1", "otra cosa"); dec.Action != trigger.Ignore {
		t.Fatalf("sin match ni fallback debe Ignore, got %+v", dec)
	}
}

func TestConfigResolver_TenantIsolation(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true})
	if dec := mustResolve(t, r, "t2", "pedido"); dec.Action != trigger.Ignore {
		t.Fatalf("regla de t1 no debe verse desde t2, got %+v", dec)
	}
}

func TestConfigResolver_IsEscape(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true})
	if esc, _ := mustEscape(t, r, "t1", "SALIR"); !esc {
		t.Fatal("SALIR debe ser escape (normalizado)")
	}
	if esc, _ := mustEscape(t, r, "t1", "hola"); esc {
		t.Fatal("hola no debe ser escape")
	}
	if esc, _ := mustEscape(t, r, "t2", "salir"); esc {
		t.Fatal("escape de t1 no debe verse desde t2")
	}
}

// TestConfigResolver_IsEscapeReturnsMessage: una regla escape con message devuelve
// ese aviso al casar; una regla sin message devuelve "" (⇒ default del runtime).
func TestConfigResolver_IsEscapeReturnsMessage(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true, Message: "Hasta pronto 👋"})
	esc, msg := mustEscape(t, r, "t1", "SALIR")
	if !esc || msg != "Hasta pronto 👋" {
		t.Fatalf("esperaba escape con message configurado, got esc=%v msg=%q", esc, msg)
	}

	r2 := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true})
	if esc, msg := mustEscape(t, r2, "t1", "salir"); !esc || msg != "" {
		t.Fatalf("regla sin message debe devolver \"\", got esc=%v msg=%q", esc, msg)
	}
}

func TestConfigResolver_IsEscapeDisabledIgnored(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: false})
	if esc, _ := mustEscape(t, r, "t1", "salir"); esc {
		t.Fatal("escape deshabilitado no debe casar")
	}
}

func TestNoopResolver_NoRegression(t *testing.T) {
	var r trigger.Resolver = trigger.NewNoopResolver()
	dec, err := r.Resolve(context.Background(), "t1", "pedido")
	if err != nil || dec.Action != trigger.Ignore {
		t.Fatalf("Noop debe Ignore sin error, got %+v err=%v", dec, err)
	}
	esc, msg, err := r.IsEscape(context.Background(), "t1", "salir")
	if err != nil || esc || msg != "" {
		t.Fatalf("Noop IsEscape debe ser (false,\"\") sin error, got esc=%v msg=%q err=%v", esc, msg, err)
	}
}
