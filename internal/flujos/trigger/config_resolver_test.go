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

// mustResolve ejecuta Resolve para una sesión GLOBAL (sessionID="") y falla si hay
// error. Los tests que ejercen reglas por-sesión usan mustResolveIn.
func mustResolve(t *testing.T, r *trigger.ConfigResolver, tenantID, text string) trigger.Decision {
	t.Helper()
	return mustResolveIn(t, r, tenantID, "", text)
}

// mustResolveIn ejecuta Resolve para una sesión concreta (Plan 020 · T4) y falla si
// hay error.
func mustResolveIn(t *testing.T, r *trigger.ConfigResolver, tenantID, sessionID, text string) trigger.Decision {
	t.Helper()
	return mustResolveSig(t, r, tenantID, sessionID, trigger.Signal{Text: text})
}

// mustResolveSig ejecuta Resolve con una Signal cualquiera (texto y/o intención) y
// falla si hay error (Plan 029 · T7).
func mustResolveSig(t *testing.T, r *trigger.ConfigResolver, tenantID, sessionID string, sig trigger.Signal) trigger.Decision {
	t.Helper()
	dec, err := r.Resolve(context.Background(), tenantID, sessionID, sig)
	if err != nil {
		t.Fatalf("resolve(%q,%q,%+v): %v", tenantID, sessionID, sig, err)
	}
	return dec
}

// mustEscape ejecuta IsEscape (sesión global) y falla si hay error, devolviendo
// también el message.
func mustEscape(t *testing.T, r *trigger.ConfigResolver, tenantID, text string) (bool, string) {
	t.Helper()
	esc, msg, err := r.IsEscape(context.Background(), tenantID, "", text)
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

// TestConfigResolver_SessionSpecificBeatsGlobal (Plan 020 · T4, caso a): cuando
// una regla acotada a la sesión y una global casan el mismo texto, gana la
// ESPECÍFICA aunque tenga menor priority (la especificidad es el criterio maestro).
func TestConfigResolver_SessionSpecificBeatsGlobal(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "global", Priority: 9, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "sesion", Priority: 0, Enabled: true, SessionID: "sX"},
	)
	dec := mustResolveIn(t, r, "t1", "sX", "hola")
	if dec.Action != trigger.Start || dec.FlowID != "sesion" {
		t.Fatalf("regla específica de sesión debe ganar a la global, got %+v", dec)
	}
}

// TestConfigResolver_GlobalAppliesToAllSessions (Plan 020 · T4, caso b): una regla
// global (SessionID="") sigue aplicando a cualquier sesión (no-regresión del 019).
func TestConfigResolver_GlobalAppliesToAllSessions(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "global", Enabled: true})
	dec := mustResolveIn(t, r, "t1", "sX", "hola")
	if dec.Action != trigger.Start || dec.FlowID != "global" {
		t.Fatalf("regla global debe aplicar a la sesión sX, got %+v", dec)
	}
}

// TestConfigResolver_SessionSpecificDoesNotLeak (Plan 020 · T4, caso c): una regla
// acotada a la sesión sX NO aplica a otra sesión sY (y sin global ⇒ Ignore).
func TestConfigResolver_SessionSpecificDoesNotLeak(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "sesion", Enabled: true, SessionID: "sX"})
	if dec := mustResolveIn(t, r, "t1", "sY", "hola"); dec.Action != trigger.Ignore {
		t.Fatalf("regla de sX no debe aplicar a sY, got %+v", dec)
	}
	// La MISMA regla sí arranca en su propia sesión sX.
	if dec := mustResolveIn(t, r, "t1", "sX", "hola"); dec.Action != trigger.Start || dec.FlowID != "sesion" {
		t.Fatalf("regla de sX debe aplicar a sX, got %+v", dec)
	}
}

func TestNoopResolver_NoRegression(t *testing.T) {
	var r trigger.Resolver = trigger.NewNoopResolver()
	dec, err := r.Resolve(context.Background(), "t1", "", trigger.Signal{Text: "pedido"})
	if err != nil || dec.Action != trigger.Ignore {
		t.Fatalf("Noop debe Ignore sin error, got %+v err=%v", dec, err)
	}
	esc, msg, err := r.IsEscape(context.Background(), "t1", "", "salir")
	if err != nil || esc || msg != "" {
		t.Fatalf("Noop IsEscape debe ser (false,\"\") sin error, got esc=%v msg=%q err=%v", esc, msg, err)
	}
}

// TestConfigResolver_IntentStartsLLMRule (Plan 029 · T7): una señal con intención
// casa una regla kind='llm' por el NOMBRE del intent (no por texto) y devuelve los
// Params en la decisión (para el pre-carga del módulo, T8).
func TestConfigResolver_IntentStartsLLMRule(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", Enabled: true})
	sig := trigger.Signal{Text: "quiero 2 pizzas", Intent: &trigger.IntentSignal{Name: "pedido", Params: map[string]string{"producto": "pizza", "cantidad": "2"}}}
	dec := mustResolveSig(t, r, "t1", "", sig)
	if dec.Action != trigger.Start || dec.FlowID != "carrito" {
		t.Fatalf("intent pedido debe arrancar carrito, got %+v", dec)
	}
	if dec.IntentName != "pedido" || dec.Params["producto"] != "pizza" || dec.Params["cantidad"] != "2" {
		t.Fatalf("la decisión llm debe llevar IntentName y Params, got %+v", dec)
	}
}

// TestConfigResolver_LLMBeatsKeyword (Plan 029 · T7): con intención presente, la
// regla llm gana aunque el texto también casaría una keyword (orden llm > keyword).
func TestConfigResolver_LLMBeatsKeyword(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchContains, FlowID: "byKeyword", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "byLLM", Enabled: true},
	)
	sig := trigger.Signal{Text: "quiero un pedido", Intent: &trigger.IntentSignal{Name: "pedido"}}
	if dec := mustResolveSig(t, r, "t1", "", sig); dec.FlowID != "byLLM" {
		t.Fatalf("la regla llm debe ganar a la keyword cuando hay intención, got %+v", dec)
	}
}

// TestConfigResolver_IntentFallsBackToKeyword (Plan 029 · T7): si la intención no
// casa ninguna regla llm, la resolución continúa por keyword sobre el texto.
func TestConfigResolver_IntentFallsBackToKeyword(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "menu", MatchType: trigger.MatchExact, FlowID: "menu", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", Enabled: true},
	)
	sig := trigger.Signal{Text: "menu", Intent: &trigger.IntentSignal{Name: "saludo"}}
	dec := mustResolveSig(t, r, "t1", "", sig)
	if dec.Action != trigger.Start || dec.FlowID != "menu" {
		t.Fatalf("intent sin regla llm debe caer a keyword por texto, got %+v", dec)
	}
	if dec.Params != nil {
		t.Fatalf("una decisión por keyword no debe llevar Params, got %+v", dec.Params)
	}
}

// TestConfigResolver_LLMSessionSpecificBeatsGlobal (Plan 029 · T7 + Plan 020 · T4):
// una regla llm acotada a la sesión gana a la global, igual que keyword.
func TestConfigResolver_LLMSessionSpecificBeatsGlobal(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "global", Priority: 9, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "sesion", Priority: 0, Enabled: true, SessionID: "sX"},
	)
	sig := trigger.Signal{Intent: &trigger.IntentSignal{Name: "pedido"}}
	if dec := mustResolveSig(t, r, "t1", "sX", sig); dec.FlowID != "sesion" {
		t.Fatalf("regla llm específica de sesión debe ganar a la global, got %+v", dec)
	}
}

// TestConfigResolver_DisabledLLMIgnored (Plan 029 · T7): una regla llm deshabilitada
// no casa; sin otra regla ⇒ Ignore.
func TestConfigResolver_DisabledLLMIgnored(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", Enabled: false})
	sig := trigger.Signal{Intent: &trigger.IntentSignal{Name: "pedido"}}
	if dec := mustResolveSig(t, r, "t1", "", sig); dec.Action != trigger.Ignore {
		t.Fatalf("regla llm deshabilitada no debe casar, got %+v", dec)
	}
}
