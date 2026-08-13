package trigger_test

// intent_scope_test.go — Plan 043 · T5.3, D-043.9: SCOPING de una regla kind='llm'
// por el TIPO de evento conversacional activo (Signal.ActiveEventKind vs
// Rule.EventKind), fijado a nivel de ConfigResolver.Resolve.
//
// ⚠️ Alcance deliberado (decisión de Jhoan, 2026-08-10, ver §5.1 del
// CONTRATO-OLA5.md): estos tests fijan la guarda EN LA PIEZA (el resolver), no en el
// camino de producción. Con conversación viva, un entrante va por ResolveLive, que
// NO consulta reglas kind='llm' (INV-02, config_resolver.go:89-98): el clasificador
// nunca llega a interrumpir un módulo. El criterio del plan («con evento cart activo,
// una señal de encuesta cae a desconocido») se demuestra aquí, a nivel de Resolve, no
// reproducido por HandleIncoming con un evento en curso. Ver también
// internal/flujos/runtime/intent_scope_test.go para el tramo que SÍ atraviesa desde
// el turno entrante (y que documenta el mismo límite).
//
// ⚠️ ACTUALIZACIÓN (Plan 054 · F2b, D-A — decisión de Jhoan 2026-08-12, tras
// review): el SCOPING que este fichero fija —qué reglas llm son ELEGIBLES según el
// evento activo— sigue intacto y sin tocar. Lo que SÍ cambió es qué pasa con la
// regla ganadora cuando trae event_kind: antes quedaba fuera de la Decision
// (CONTRATO-OLA5 D1: «la anotación es scoping, no puerta»); ahora SÍ se propaga y
// PARE (o conmuta) ese evento — la segunda puerta del nacimiento tardío que el Plan
// 043 · T2.5 declaró y nunca se cableó. Ver
// TestIntentScope_Decision_PropagaEventKindAlGanar, más abajo, que sustituye a la
// antigua TestIntentScope_Decision_NoPropagaEventKind.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

const (
	t53Tenant       = "tenant-t53"
	t53SurveyFlow   = "encuesta-flow"
	t53CatalogFlow  = "catalogo-flow"
	t53GreetingFlow = "saludo-flow"
)

// t53LlmRule construye una regla kind='llm' anotada (o no) con un event_kind.
func t53LlmRule(intentName, flowID, eventKind string) trigger.Rule {
	return trigger.Rule{
		TenantID: t53Tenant, Kind: trigger.KindLLM, Keyword: intentName, FlowID: flowID,
		EventKind: eventKind, Enabled: true,
	}
}

// TestIntentScope_SinEventoActivo_NoAcotaNada fija la PRIMERA guarda de
// retrocompatibilidad: ActiveEventKind == "" (el caso NORMAL en el camino de
// producción, ver buildSignal) no acota nada, aunque la regla lleve un event_kind
// anotado. Sin evento activo, no hay nada que acotar — la regla CASA igual.
//
// Lo que SÍ cambió (Plan 054 · F2b, D-A — decisión de Jhoan 2026-08-12): antes de
// esta tarea, casar era siempre {Start, FlowID, IntentName}, sin importar si la regla
// llevaba event_kind. Ahora, con la regla anotada, casar PARE el evento de ese tipo
// —{StartEvent, FlowID, EventKind, IntentName}—: el scoping (esta guarda) y la puerta
// de nacimiento leen el MISMO campo en SECUENCIA, no compiten.
func TestIntentScope_SinEventoActivo_NoAcotaNada(t *testing.T) {
	r := seed(t, t53LlmRule("pedir_encuesta", t53SurveyFlow, "survey"))

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Intent:          &trigger.IntentSignal{Name: "pedir_encuesta"},
		ActiveEventKind: "",
	})
	if dec.Action != trigger.StartEvent || dec.EventKind != "survey" || dec.FlowID != t53SurveyFlow || dec.IntentName != "pedir_encuesta" {
		t.Fatalf("sin evento activo, la regla llm anotada debe casar igual y parir su evento: %+v", dec)
	}
}

// TestIntentScope_ReglaSinAnotar_SigueCasandoSiempre fija la SEGUNDA guarda de
// retrocompatibilidad: una regla llm SIN event_kind (el caso del Plan 029, previo a
// esta tarea) casa SIEMPRE, sea cual sea el evento activo.
//
// ⚠️ Este es el criterio LITERAL del plan («config vieja sin event_kind sigue
// funcionando») y es TRIVIALMENTE cierto: una guarda `r.EventKind != "" && ...` que
// nunca se hubiera escrito también lo cumpliría. No demuestra que el scoping
// funcione — solo que no rompió nada. El test que SÍ prueba la guarda es el inverso,
// TestIntentScope_ReglaAnotada_EventoActivoDistinto_CaeAlPeldañoDeTexto, más abajo.
func TestIntentScope_ReglaSinAnotar_SigueCasandoSiempre(t *testing.T) {
	r := seed(t, t53LlmRule("pedir_catalogo", t53CatalogFlow, ""))

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Intent:          &trigger.IntentSignal{Name: "pedir_catalogo"},
		ActiveEventKind: "cart",
	})
	if dec.Action != trigger.Start || dec.FlowID != t53CatalogFlow {
		t.Fatalf("una regla llm sin event_kind debe casar con cualquier evento activo: %+v", dec)
	}
}

// TestIntentScope_ReglaAnotada_EventoActivoDistinto_CaeAlPeldañoDeTexto es el test
// que VALE (el inverso del trivial de arriba): con un event_kind puesto en la regla
// Y un evento activo DISTINTO, la regla llm NO casa —el intent cae a "desconocido"
// de facto— y la señal sigue su camino por TEXTO: aquí hay también una regla
// keyword que casa el mismo texto, y es ESA la que gana la Decision (FlowID de
// saludo, sin IntentName ni Params poblados). Si la guarda se borrara o se
// invirtiera, este test se pondría en rojo (ver la tabla de mutación en el
// handoff).
func TestIntentScope_ReglaAnotada_EventoActivoDistinto_CaeAlPeldañoDeTexto(t *testing.T) {
	r := seed(t,
		t53LlmRule("pedir_encuesta", t53SurveyFlow, "survey"),
		trigger.Rule{TenantID: t53Tenant, Kind: trigger.KindKeyword, Keyword: "hola", FlowID: t53GreetingFlow, MatchType: trigger.MatchExact, Enabled: true},
	)

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Text:            "hola",
		Intent:          &trigger.IntentSignal{Name: "pedir_encuesta"},
		ActiveEventKind: "cart",
	})
	if dec.Action != trigger.Start || dec.FlowID != t53GreetingFlow {
		t.Fatalf("con event_kind ajeno al activo, debe ganar el peldaño de texto (saludo), got %+v", dec)
	}
	if dec.IntentName != "" || dec.Params != nil {
		t.Fatalf("la Decision de texto NO debe llevar rastro de la intención descartada: %+v", dec)
	}
}

// TestIntentScope_ReglaAnotada_EventoActivoIgual_Casa es el control positivo: el
// mismo event_kind en la regla y en la señal SÍ casa (la guarda es `!=`, no un
// bloqueo total de reglas anotadas) — y, ganadora, PARE (o conmuta) ese mismo tipo
// de evento (Plan 054 · F2b, D-A: ver el comentario de
// TestIntentScope_Decision_PropagaEventKindAlGanar, más abajo).
func TestIntentScope_ReglaAnotada_EventoActivoIgual_Casa(t *testing.T) {
	r := seed(t, t53LlmRule("pedir_encuesta", t53SurveyFlow, "survey"))

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Intent:          &trigger.IntentSignal{Name: "pedir_encuesta"},
		ActiveEventKind: "survey",
	})
	if dec.Action != trigger.StartEvent || dec.EventKind != "survey" || dec.FlowID != t53SurveyFlow || dec.IntentName != "pedir_encuesta" {
		t.Fatalf("con el mismo event_kind que el activo, la regla debe casar y parir/conmutar su evento: %+v", dec)
	}
}

// TestIntentScope_Decision_PropagaEventKindAlGanar reemplaza a la antigua
// TestIntentScope_Decision_NoPropagaEventKind (Plan 054 · F2b, D-A — decisión de
// Jhoan 2026-08-12, tras review, que SUSTITUYE a CONTRATO-OLA5 D1 del 2026-08-10).
//
// El contrato viejo decía: «la Decision de la rama llm JAMÁS lleva EventKind […]
// propagarlo reabriría el defecto de la coletilla […] la anotación es scoping, no
// puerta». Eso dejó de ser cierto a propósito: una regla llm cuyo event_kind gana el
// scoping AHORA sí lo propaga a la Decision —{StartEvent, EventKind}— para que la
// intención pueda parir (o conmutar) su evento, con el mismo tratamiento que
// event_start.
//
// El riesgo que motivó el contrato viejo (la coletilla anunciándose a sí misma)
// sigue vigilado, pero por OTRO mecanismo, más fuerte que «no combinar los campos»:
// birthEvent (runtime/events.go) resuelve la coletilla ANTES de crear la fila del
// evento, así que un evento recién nacido no puede aparecer nunca como «a medias»
// sobre sí mismo, combine o no combine la Decision IntentName con EventKind. Lo fija
// runtime/tagline_test.go — TestTagline_ElEventoQueAcabaDeNacerNoSeAnunciaASiMismo
// (genérico, con un resolver de prueba) y
// TestTagline_LLMRuleConEventKind_NoSeAnunciaASiMismo (de extremo a extremo, con el
// ConfigResolver real).
func TestIntentScope_Decision_PropagaEventKindAlGanar(t *testing.T) {
	r := seed(t, t53LlmRule("pedir_encuesta", t53SurveyFlow, "survey"))

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Intent:          &trigger.IntentSignal{Name: "pedir_encuesta"},
		ActiveEventKind: "survey",
	})
	if dec.EventKind != "survey" {
		t.Fatalf("la Decision de una regla llm GANADORA con event_kind debe propagarlo, got %q", dec.EventKind)
	}
	if dec.Action != trigger.StartEvent {
		t.Fatalf("y la Action debe ser StartEvent, no Start, got %v", dec.Action)
	}
}

// TestIntentScope_ReglaSinEventKind_DecisionNuncaLoLleva es el guardián que SÍ
// sigue vigente del contrato viejo: una regla llm SIN event_kind (el caso de
// siempre, Plan 029) jamás lleva EventKind en su Decision — no hay campo que
// propagar. Sin esto, TestIntentScope_Decision_PropagaEventKindAlGanar (arriba)
// podría pasar por accidente si alguien poblara EventKind incondicionalmente.
func TestIntentScope_ReglaSinEventKind_DecisionNuncaLoLleva(t *testing.T) {
	r := seed(t, t53LlmRule("pedir_catalogo", t53CatalogFlow, ""))

	dec := mustResolveSig(t, r, t53Tenant, "", trigger.Signal{
		Intent:          &trigger.IntentSignal{Name: "pedir_catalogo"},
		ActiveEventKind: "",
	})
	if dec.Action != trigger.Start || dec.EventKind != "" {
		t.Fatalf("una regla llm sin event_kind debe seguir devolviendo Start sin EventKind, got %+v", dec)
	}
}
