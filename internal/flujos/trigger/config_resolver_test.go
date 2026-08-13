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

// mustResolveLive ejecuta ResolveLive —el camino de CONVERSACIÓN VIVA (Plan 043 ·
// D-043.2)— para la sesión dada y falla si hay error.
func mustResolveLive(t *testing.T, r *trigger.ConfigResolver, tenantID, sessionID, text string) trigger.Decision {
	t.Helper()
	dec, err := r.ResolveLive(context.Background(), tenantID, sessionID, text)
	if err != nil {
		t.Fatalf("resolveLive(%q,%q,%q): %v", tenantID, sessionID, text, err)
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

// TestConfigResolver_IntentWithEventKindStartsEvent (Plan 054 · F2b, D-A): una
// regla kind='llm' que trae event_kind PARE evento — Action=StartEvent con
// EventKind poblado desde la regla, Params/IntentName intactos para el pre-carga
// del módulo (T8). Es la SEGUNDA puerta del nacimiento que el Plan 043 ·
// T2.5/REQ-01b declaró y este frente construye.
func TestConfigResolver_IntentWithEventKindStartsEvent(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", EventKind: "cart", Enabled: true})
	sig := trigger.Signal{Text: "quiero 2 pizzas", Intent: &trigger.IntentSignal{Name: "pedido", Params: map[string]string{"producto": "pizza", "cantidad": "2"}}}
	dec := mustResolveSig(t, r, "t1", "", sig)
	if dec.Action != trigger.StartEvent || dec.EventKind != "cart" || dec.FlowID != "carrito" {
		t.Fatalf("intent con event_kind debe parir el evento cart, got %+v", dec)
	}
	if dec.IntentName != "pedido" || dec.Params["producto"] != "pizza" || dec.Params["cantidad"] != "2" {
		t.Fatalf("la decisión debe seguir llevando IntentName y Params para el Prime, got %+v", dec)
	}
}

// TestConfigResolver_IntentWithoutEventKindStaysStart (Plan 054 · F2b, D-A, punto
// 1): no-regresión DURA — una regla llm SIN event_kind sigue devolviendo
// exactamente lo de siempre (Action=Start, sin evento), byte a byte. Hay tenants
// con reglas llm sin event_kind y este es su contrato.
func TestConfigResolver_IntentWithoutEventKindStaysStart(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", Enabled: true})
	sig := trigger.Signal{Text: "quiero 2 pizzas", Intent: &trigger.IntentSignal{Name: "pedido", Params: map[string]string{"producto": "pizza"}}}
	dec := mustResolveSig(t, r, "t1", "", sig)
	if dec.Action != trigger.Start || dec.EventKind != "" || dec.FlowID != "carrito" {
		t.Fatalf("intent sin event_kind debe seguir siendo Start sin evento, got %+v", dec)
	}
}

// ---------------------------------------------------------------------------
// Plan 043 · T2.1 — event_start / event_stop
// ---------------------------------------------------------------------------

// TestConfigResolver_EventStartMatchesNormalized: «CARRÍTO» (mayúsculas, acento y
// espacios) casa el event_start de keyword `carrito` con la MISMA normalización de
// siempre, y la decisión lleva el tipo de evento que el runtime tiene que parir.
func TestConfigResolver_EventStartMatchesNormalized(t *testing.T) {
	r := seed(t, trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true})
	dec := mustResolve(t, r, "t1", "  CARRÍTO ")
	if dec.Action != trigger.StartEvent || dec.EventKind != "cart" {
		t.Fatalf("esperaba StartEvent/cart, got %+v", dec)
	}
}

// TestConfigResolver_EventStartIsPeerOfKeyword: event_start y keyword compiten en el
// MISMO peldaño (D-043.2), así que el ganador lo decide la priority del dueño y no el
// kind. Se verifica en los dos sentidos para que no pase por casualidad.
func TestConfigResolver_EventStartIsPeerOfKeyword(t *testing.T) {
	eventWins := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "viejo", Priority: 1, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "pedido", MatchType: trigger.MatchExact, EventKind: "cart", Priority: 9, Enabled: true},
	)
	if dec := mustResolve(t, eventWins, "t1", "pedido"); dec.Action != trigger.StartEvent || dec.EventKind != "cart" {
		t.Fatalf("con mayor priority debe ganar el event_start, got %+v", dec)
	}
	keywordWins := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "viejo", Priority: 9, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "pedido", MatchType: trigger.MatchExact, EventKind: "cart", Priority: 1, Enabled: true},
	)
	if dec := mustResolve(t, keywordWins, "t1", "pedido"); dec.Action != trigger.Start || dec.FlowID != "viejo" {
		t.Fatalf("con mayor priority debe ganar la keyword, got %+v", dec)
	}
}

// TestConfigResolver_EventStartBeatsFallback: el event_start está en el peldaño de
// keyword, o sea ANTES del fallback.
func TestConfigResolver_EventStartBeatsFallback(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "bienvenida", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true},
	)
	if dec := mustResolve(t, r, "t1", "carrito"); dec.Action != trigger.StartEvent {
		t.Fatalf("event_start debe ganar al fallback, got %+v", dec)
	}
	if dec := mustResolve(t, r, "t1", "buenas"); dec.Action != trigger.Fallback || dec.FlowID != "bienvenida" {
		t.Fatalf("sin casar nada debe seguir mandando el fallback, got %+v", dec)
	}
}

// TestConfigResolver_EventStopNotResolvedWithoutLiveConversation: sin conversación
// viva no hay evento activo que desactivar, así que event_stop NO participa en la
// escalera reactiva y el turno sigue su curso normal (aquí, el fallback).
func TestConfigResolver_EventStopNotResolvedWithoutLiveConversation(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "bienvenida", Enabled: true},
	)
	if dec := mustResolve(t, r, "t1", "parar"); dec.Action != trigger.Fallback {
		t.Fatalf("event_stop no debe resolver sin conversación viva, got %+v", dec)
	}
}

// TestConfigResolver_ResolveLiveStartsAndStops: con conversación viva, event_start
// conmuta por tipo y event_stop desactiva — la excepción ACOTADA de INV-02.
func TestConfigResolver_ResolveLiveStartsAndStops(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "encuesta", MatchType: trigger.MatchExact, EventKind: "survey", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true},
	)
	if dec := mustResolveLive(t, r, "t1", "", "ENCUESTA"); dec.Action != trigger.StartEvent || dec.EventKind != "survey" {
		t.Fatalf("event_start debe resolver con conversación viva, got %+v", dec)
	}
	dec := mustResolveLive(t, r, "t1", "", "parar")
	if dec.Action != trigger.StopEvent {
		t.Fatalf("event_stop debe resolver con conversación viva, got %+v", dec)
	}
	if dec.EventKind != "" {
		t.Fatalf("event_stop corta el activo sea del tipo que sea: no debe llevar EventKind, got %+v", dec)
	}
}

// TestConfigResolver_ResolveLiveLeavesINV02Intact: lo que NO es un disparo de evento
// no se toca con conversación viva — ni keyword, ni fallback, ni llm. El texto sigue
// siendo del módulo, que es justo lo que INV-02 protege.
func TestConfigResolver_ResolveLiveLeavesINV02Intact(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindFallback, FlowID: "bienvenida", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindLLM, Keyword: "pedido", FlowID: "carrito", Enabled: true},
	)
	for _, text := range []string{"pedido", "cualquier otra cosa"} {
		if dec := mustResolveLive(t, r, "t1", "", text); dec.Action != trigger.Ignore {
			t.Fatalf("con conversación viva %q no debe resolver nada (INV-02), got %+v", text, dec)
		}
	}
}

// TestConfigResolver_ResolveLiveRespetaEnabledSesionYTenant: el camino de conversación
// viva hereda las MISMAS reglas de visibilidad que el reactivo (enabled, sesión, INV-8).
func TestConfigResolver_ResolveLiveRespetaEnabledSesionYTenant(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "apagado", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: false},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true, SessionID: "sX"},
		trigger.Rule{TenantID: "t2", Kind: trigger.KindEventStart, Keyword: "ajeno", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true},
	)
	if dec := mustResolveLive(t, r, "t1", "", "apagado"); dec.Action != trigger.Ignore {
		t.Fatalf("una regla deshabilitada no debe casar, got %+v", dec)
	}
	if dec := mustResolveLive(t, r, "t1", "sY", "carrito"); dec.Action != trigger.Ignore {
		t.Fatalf("una regla de la sesión sX no debe filtrarse a sY, got %+v", dec)
	}
	if dec := mustResolveLive(t, r, "t1", "sX", "carrito"); dec.Action != trigger.StartEvent {
		t.Fatalf("la regla de sX debe aplicar en sX, got %+v", dec)
	}
	if dec := mustResolveLive(t, r, "t1", "", "ajeno"); dec.Action != trigger.Ignore {
		t.Fatalf("una regla de t2 NUNCA es visible para t1 (INV-8), got %+v", dec)
	}
}

// TestNoopResolver_ResolveLive: con el resolver por defecto, una conversación viva
// nunca salta ni se desactiva por texto (no-regresión total, INV-6).
func TestNoopResolver_ResolveLive(t *testing.T) {
	var r trigger.Resolver = trigger.NewNoopResolver()
	dec, err := r.ResolveLive(context.Background(), "t1", "", "carrito")
	if err != nil || dec.Action != trigger.Ignore {
		t.Fatalf("Noop ResolveLive debe Ignore sin error, got %+v err=%v", dec, err)
	}
}

// TestConfigResolver_ResolveLiveStopGanaAlStart: la colisión REAL de la
// configuración natural del dueño. «carrito» por contains para entrar y «salir del
// carrito» por contains para salir hacen que el texto «salir del carrito» case las
// DOS reglas; con la priority por defecto, el desempate por keyword las ordena al
// revés de lo pedido («carrito» < «salir del carrito»). Antes de evaluar event_stop
// primero, quien pedía salir acababa DENTRO del carrito.
func TestConfigResolver_ResolveLiveStopGanaAlStart(t *testing.T) {
	r := seed(t,
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchContains, EventKind: "cart", Enabled: true},
		trigger.Rule{TenantID: "t1", Kind: trigger.KindEventStop, Keyword: "salir del carrito", MatchType: trigger.MatchContains, Enabled: true},
	)
	dec := mustResolveLive(t, r, "t1", "", "salir del carrito")
	if dec.Action != trigger.StopEvent {
		t.Fatalf("«salir del carrito» debe DESACTIVAR, no entrar; got %+v", dec)
	}
	// Y el arranque sigue funcionando para el texto que sí lo pide.
	if dec := mustResolveLive(t, r, "t1", "", "quiero el carrito"); dec.Action != trigger.StartEvent || dec.EventKind != "cart" {
		t.Fatalf("«quiero el carrito» debe seguir entrando al carrito; got %+v", dec)
	}
}

// TestConfigResolver_DesempateTotalPorTriggerID protege el desempate final por
// trigger_id, que es lo ÚNICO que hace la resolución independiente del orden en que
// se consultaron los kinds: event_start y keyword compiten en el mismo peldaño, así
// que la lista viene de DOS consultas y un empate exacto quedaría decidido por cuál
// se preguntó antes.
//
// Se repite con stores nuevos porque el trigger_id es un UUID aleatorio: en cada
// vuelta cambia cuál de las dos reglas lo tiene menor, y sin el desempate ganaría
// siempre la del primer kind consultado. Una sola vuelta acertaría por casualidad la
// mitad de las veces; treinta convierten el fallo en certeza práctica.
func TestConfigResolver_DesempateTotalPorTriggerID(t *testing.T) {
	for i := 0; i < 30; i++ {
		s := trigger.NewMemoryStore()
		// Empate EXACTO en todo lo que desempata antes: sesión, priority, match_type y
		// keyword. Solo queda el trigger_id.
		ev, err := s.Insert(context.Background(), trigger.Rule{
			TenantID: "t1", Kind: trigger.KindEventStart, Keyword: "pedido",
			MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true,
		})
		if err != nil {
			t.Fatalf("insert event_start: %v", err)
		}
		kw, err := s.Insert(context.Background(), trigger.Rule{
			TenantID: "t1", Kind: trigger.KindKeyword, Keyword: "pedido",
			MatchType: trigger.MatchExact, FlowID: "viejo", Enabled: true,
		})
		if err != nil {
			t.Fatalf("insert keyword: %v", err)
		}

		dec, err := trigger.NewConfigResolver(s).Resolve(context.Background(), "t1", "", trigger.Signal{Text: "pedido"})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		// Gana la de trigger_id menor, sea del kind que sea.
		wantEvent := ev.TriggerID < kw.TriggerID
		gotEvent := dec.Action == trigger.StartEvent
		if gotEvent != wantEvent {
			t.Fatalf("vuelta %d: con event_start=%s y keyword=%s debe ganar el trigger_id menor; got %+v",
				i, ev.TriggerID, kw.TriggerID, dec)
		}
	}
}
