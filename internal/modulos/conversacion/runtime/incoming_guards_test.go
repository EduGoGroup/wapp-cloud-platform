//go:build pendiente

package runtime_test

// incoming_guards_test.go: lo que HandleIncoming hace ANTES de tocar la conversación
// (incoming.go §1), en su orden: dedupe persistente (RT-5), tenant y perfil, perfil pasivo
// (RT-6), anti-self-loop (RT-7) y la identidad del contacto. Parte de incoming_test.go (E-13).

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// TestHandleIncoming_Dedupe_SeenIsDroppedBeforeAnythingElse: RT-5. Un entrante ya visto se
// ignora lo primero de todo: no se resuelve el tenant, no se toca el estado, no se responde y
// no cuenta como visita para el recordatorio de la seña.
func TestHandleIncoming_Dedupe_SeenIsDroppedBeforeAnythingElse(t *testing.T) {
	h := incomingLiveFlat(t)
	resolvedBefore := len(h.tenants.Sessions())
	remindedBefore := len(h.deposits.Calls())
	h.deduper.MarkSeen(harnessSession, "wa-dup")

	if err := h.handle(h.incoming("wa-dup", "1")); err != nil {
		t.Fatalf("HandleIncoming de un duplicado = %v, quería nil", err)
	}

	calls := h.deduper.Calls()
	if last := calls[len(calls)-1]; last != (runtimehelpertest.IngestKey{SessionID: harnessSession, WaMessageID: "wa-dup"}) {
		t.Errorf("el dedupe se consultó con %+v, quería (session_id, wa_message_id) del entrante", last)
	}
	if got := len(h.tenants.Sessions()); got != resolvedBefore {
		t.Errorf("el tenant se resolvió %d veces más por un duplicado, quería 0", got-resolvedBefore)
	}
	if got := len(h.deposits.Calls()); got != remindedBefore {
		t.Errorf("el recordatorio de la seña se evaluó %d veces más por un duplicado, quería 0", got-remindedBefore)
	}
	incomingWantNode(t, h, "root")
	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantLog(t, h, "debug", "runtime: entrante duplicado ignorado (dedupe de ingesta)", 1)
}

// TestHandleIncoming_Dedupe_InterleavedReplayHasNoEffect: RT-5. El duplicado INTERCALADO
// (A, B, A) que la idempotencia consecutiva no ve: el reenvío de A llega cuando el último
// procesado ya es B. Sin dedupe persistente, ese «hola» repetido re-preguntaría el submenú.
func TestHandleIncoming_Dedupe_InterleavedReplayHasNoEffect(t *testing.T) {
	h := incomingLiveFlat(t) // A = wa-open, «hola»
	h.say("wa-b", "1")
	incomingWantNode(t, h, "sub")

	h.say("wa-open", incomingKeyword)

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
}

// TestHandleIncoming_Dedupe_ReplayOfATriggerDoesNotStartAgain: RT-5. El reenvío de un entrante
// que DISPARÓ no vuelve a disparar: tras cerrar la conversación con un escape, el «hola»
// original reenviado no arranca otra.
func TestHandleIncoming_Dedupe_ReplayOfATriggerDoesNotStartAgain(t *testing.T) {
	h := incomingLiveFlat(t)
	h.say("wa-esc", incomingEscapeKeyword)
	incomingWantNoState(t, h)
	sent := len(h.texts())

	h.say("wa-open", incomingKeyword)

	incomingWantNoState(t, h)
	if got := len(h.texts()); got != sent {
		t.Errorf("el reenvío del disparo envió %d textos más, quería 0", got-sent)
	}
}

// TestHandleIncoming_Dedupe_FailOpen: RT-5. Si el deduplicador falla, el entrante NO se
// pierde: se avisa a WARN y se procesa.
func TestHandleIncoming_Dedupe_FailOpen(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.deduper.Fail(errors.New("dedupe-down-qzx"))

	h.say("wa-1", incomingKeyword)

	incomingWantLog(t, h, "warn", "runtime: dedupe de ingesta falló; se continúa (fail-open)", 1)
	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantNode(t, h, "root")
}

// TestHandleIncoming_Dedupe_EmptyMessageIDIsNotConsulted: RT-5. Un entrante sin wa_message_id
// no se deduplica: el deduplicador ni se consulta y la conversación avanza.
func TestHandleIncoming_Dedupe_EmptyMessageIDIsNotConsulted(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))

	h.say("", incomingKeyword)
	h.say("", "1")
	h.say("", "1")

	if calls := h.deduper.Calls(); len(calls) != 0 {
		t.Errorf("el dedupe se consultó %d veces sin wa_message_id, quería 0: %+v", len(calls), calls)
	}
	// Sin id tampoco hay idempotencia consecutiva: los dos «1» avanzan.
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt, incomingLeafText)
}

// TestHandleIncoming_NilDeduper_OnlyTheConsecutiveReplayIsDropped: RT-5 y §4.4. Sin
// deduplicador queda SOLO la idempotencia consecutiva por last_wa_message_id: el mismo id que
// el último procesado no avanza ni reenvía; el duplicado intercalado, en cambio, sí pasa.
func TestHandleIncoming_NilDeduper_OnlyTheConsecutiveReplayIsDropped(t *testing.T) {
	h := incomingLiveFlat(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithIngestDeduper(nil)}
	}))
	h.say("wa-b", "1")
	if st := incomingWantNode(t, h, "sub"); st.LastWaMessageID != "wa-b" {
		t.Fatalf("last_wa_message_id = %q, quería el del turno procesado (wa-b)", st.LastWaMessageID)
	}

	h.say("wa-b", "1")
	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)

	// El intercalado (wa-open tras wa-b) no es consecutivo: sin dedupe persistente se procesa
	// como un texto más del submenú y el motor contesta.
	h.say("wa-open", incomingKeyword)
	if got := len(h.texts()); got != 3 {
		t.Errorf("textos enviados = %d, quería 3: sin deduplicador el duplicado intercalado SÍ se procesa", got)
	}
}

// TestHandleIncoming_TenantResolverError: si la sesión no resuelve tenant, HandleIncoming
// devuelve «runtime: resolver tenant: …» con la causa y no ocurre nada más.
func TestHandleIncoming_TenantResolverError(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	boom := errors.New("tenant-down-qzx")
	h.tenants.Fail(boom)

	err := h.handle(h.incoming("wa-1", incomingKeyword))

	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "runtime: resolver tenant: ") {
		t.Fatalf("HandleIncoming = %v, quería «runtime: resolver tenant: …» envolviendo la causa", err)
	}
	incomingWantTexts(t, h)
	incomingWantNoState(t, h)
	if calls := h.deposits.Calls(); len(calls) != 0 {
		t.Errorf("el recordatorio de la seña se evaluó sin tenant: %+v", calls)
	}
}

// TestHandleIncoming_OnlyTheLiteralPassiveCuts: RT-6. Vacío, desconocido o con otra caja es
// ACTIVO: el disparo ocurre. Solo "passive" corta.
func TestHandleIncoming_OnlyTheLiteralPassiveCuts(t *testing.T) {
	cases := []struct {
		name, profile string
		fires         bool
	}{
		{name: "active", profile: runtimehelpertest.ProfileActive, fires: true},
		{name: "empty", profile: "", fires: true},
		{name: "unknown", profile: "bot", fires: true},
		{name: "other case", profile: "Passive", fires: true},
		{name: "passive", profile: runtimehelpertest.ProfilePassive, fires: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withProfile(tc.profile))
			h.seedFlow(incomingStepFlow())
			h.seedRule(incomingKeywordRule(incomingFlowID))

			h.say("wa-1", incomingKeyword)

			if _, found := h.state(); found != tc.fires {
				t.Errorf("conversación arrancada = %v, quería %v con perfil %q", found, tc.fires, tc.profile)
			}
			if sent := len(h.texts()) == 1; sent != tc.fires {
				t.Errorf("respuesta enviada = %v, quería %v con perfil %q", sent, tc.fires, tc.profile)
			}
		})
	}
}

// TestHandleIncoming_PassiveSession_FreezesTheLiveConversation: RT-6. Una conversación en
// curso deja de avanzar y de escapar mientras la sesión sea pasiva, pero su estado NO se
// borra: al reactivarla, sigue donde estaba. Tampoco se evalúa el recordatorio ni se registra
// al contacto para la bienvenida.
func TestHandleIncoming_PassiveSession_FreezesTheLiveConversation(t *testing.T) {
	h := newHarness(t)
	h.enableFeature(entitlements.FeatureLLMIntake) // con la bienvenida activa, cada entrante admitido deja su marca
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(incomingEscapeRule(""))
	h.say("wa-open", incomingKeyword)
	incomingWantNode(t, h, "root")
	incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
	remindedBefore := len(h.deposits.Calls())
	h.clock.Advance(time.Minute)
	h.tenants.SetProfile(runtimehelpertest.ProfilePassive)

	h.say("wa-2", "1")
	h.say("wa-3", incomingEscapeKeyword)

	incomingWantNode(t, h, "root")
	incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"passive", "passive"}) {
		t.Errorf("motivos contados = %v, quería un passive por entrante cortado", reasons)
	}
	if got := len(h.deposits.Calls()); got != remindedBefore {
		t.Errorf("una sesión pasiva evaluó el recordatorio de la seña %d veces, quería 0", got-remindedBefore)
	}
	if got := h.repo.Welcome(h.key()).LastIncomingAt; !got.Equal(harnessStart) {
		t.Errorf("una sesión pasiva registró actividad del contacto en %v, quería que siguiera en %v", got, harnessStart)
	}

	h.tenants.SetProfile(runtimehelpertest.ProfileActive)
	h.say("wa-4", "1")
	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt, incomingSubPrompt)
}

// TestHandleIncoming_PassiveSession_AnnouncedOncePerSession: RT-6. El primer entrante de una
// sesión pasiva se anuncia a INFO; los siguientes de ESA sesión, a DEBUG. Otra sesión pasiva
// se anuncia a su vez.
func TestHandleIncoming_PassiveSession_AnnouncedOncePerSession(t *testing.T) {
	const (
		announce = "runtime: sesión passive; motor reactivo omitido — no auto-responderá mientras siga passive (se anuncia una vez por sesión; el resto en debug)"
		quiet    = "runtime: sesión passive; motor reactivo omitido"
		other    = "session-other-qzx"
	)
	h := newHarness(t, withProfile(runtimehelpertest.ProfilePassive))

	h.say("wa-1", "uno")
	h.say("wa-2", "dos")
	h.say("wa-3", "tres")
	if err := h.rt.HandleIncoming(t.Context(), other, h.incoming("wa-4", "cuatro")); err != nil {
		t.Fatalf("HandleIncoming por la otra sesión pasiva = %v, quería nil", err)
	}

	info := incomingWantLog(t, h, "info", announce, 2)
	incomingWantFields(t, info[0], map[string]any{"session_id": harnessSession})
	incomingWantFields(t, info[1], map[string]any{"session_id": other})
	debug := incomingWantLog(t, h, "debug", quiet, 2)
	incomingWantFields(t, debug[0], map[string]any{"session_id": harnessSession})
	incomingWantNoLeak(t, h, harnessPhone, "cuatro")
}

// TestHandleIncoming_SelfLoop_OwnNumberIsNotAnswered: RT-7. Un entrante cuyo remitente es un
// número propio del tenant no dispara, no crea estado y no se contesta; se cuenta "self_loop"
// y se avisa a WARN con ids opacos, sin el número.
func TestHandleIncoming_SelfLoop_OwnNumberIsNotAnswered(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.selfNumbers.Add(harnessTenant, harnessPhone)

	h.say("wa-1", incomingKeyword)

	incomingWantNoState(t, h)
	incomingWantTexts(t, h)
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"self_loop"}) {
		t.Errorf("motivos contados = %v, quería [self_loop]", reasons)
	}
	lines := incomingWantLog(t, h, "warn", "runtime: entrante de un número propio del tenant; auto-respuesta evitada (anti-self-loop)", 1)
	incomingWantFields(t, lines[0], map[string]any{"tenant_id": harnessTenant, "session_id": harnessSession})
	incomingWantNoLeak(t, h, harnessPhone)
	if calls := h.deposits.Calls(); len(calls) != 0 {
		t.Errorf("un número propio llegó al recordatorio de la seña: %+v", calls)
	}
}

// TestHandleIncoming_SelfLoop_AsksWithTheNormalizedNumber: RT-7. El número se normaliza a
// dígitos puros ANTES de preguntar, y se pregunta por el tenant de la sesión. Escrito con
// «+», espacios y guiones da el mismo veredicto que en su forma canónica. El fallo que
// previene es mudo: sin normalizar, el índice ciego no casaría y la guarda dejaría de cortar.
func TestHandleIncoming_SelfLoop_AsksWithTheNormalizedNumber(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.selfNumbers.Add(harnessTenant, harnessPhone)
	m := h.incoming("wa-1", incomingKeyword)
	m.FromPn = "+57 300-111 2233"

	if err := h.handle(m); err != nil {
		t.Fatalf("HandleIncoming = %v, quería nil", err)
	}

	want := []runtimehelpertest.SelfNumberQuery{{TenantID: harnessTenant, Number: harnessPhone}}
	if got := h.selfNumbers.Queries(); !slices.Equal(got, want) {
		t.Errorf("preguntas al checker = %+v, quería %+v (dígitos puros)", got, want)
	}
	incomingWantNoState(t, h)
	incomingWantTexts(t, h)
}

// TestHandleIncoming_SelfLoop_IsConservativeTowardsProcessing: RT-7. La guarda solo corta
// cuando SABE que el número es propio. Sin checker, sin from_pn, con un número que no
// normaliza, con el número declarado en OTRO tenant o con un error del checker, el entrante
// sigue su camino.
func TestHandleIncoming_SelfLoop_IsConservativeTowardsProcessing(t *testing.T) {
	const checkerFailed = "runtime: no se pudo comprobar si el remitente es un número propio del tenant; guarda anti-self-loop omitida"
	cases := []struct {
		name      string
		options   []harnessOption
		prepare   func(h *harness)
		fromPn    string
		asked     int
		wantsWarn bool
	}{
		{
			name:    "no checker wired",
			options: []harnessOption{withOptions(func(*harness) []runtime.Option { return []runtime.Option{runtime.WithSelfNumbers(nil)} })},
			prepare: func(h *harness) { h.selfNumbers.Add(harnessTenant, harnessPhone) },
			fromPn:  harnessPhone,
		},
		{name: "no from_pn", prepare: func(h *harness) { h.selfNumbers.Add(harnessTenant, harnessPhone) }, fromPn: ""},
		{name: "number does not normalize", prepare: func(h *harness) { h.selfNumbers.Add(harnessTenant, harnessPhone) }, fromPn: "sin-digitos"},
		{name: "own number of another tenant", prepare: func(h *harness) { h.selfNumbers.Add("another-tenant-qzx", harnessPhone) }, fromPn: harnessPhone, asked: 1},
		{name: "checker error", prepare: func(h *harness) {
			h.selfNumbers.Add(harnessTenant, harnessPhone)
			h.selfNumbers.Fail(errors.New("checker-down-qzx"))
		}, fromPn: harnessPhone, asked: 1, wantsWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.options...)
			h.seedFlow(incomingStepFlow())
			h.seedRule(incomingKeywordRule(incomingFlowID))
			tc.prepare(h)
			m := h.incoming("wa-1", incomingKeyword)
			m.FromPn = tc.fromPn

			if err := h.handle(m); err != nil {
				t.Fatalf("HandleIncoming = %v, quería nil\nlog:\n%s", err, h.log.dump())
			}

			if got := len(h.selfNumbers.Queries()); got != tc.asked {
				t.Errorf("preguntas al checker = %d, quería %d", got, tc.asked)
			}
			if reasons := h.blockedReasons(); len(reasons) != 0 {
				t.Errorf("motivos contados = %v, no quería ningún corte", reasons)
			}
			warned := len(incomingLogLines(h, "warn", checkerFailed)) == 1
			if warned != tc.wantsWarn {
				t.Errorf("aviso del fallo del checker = %v, quería %v\nlog:\n%s", warned, tc.wantsWarn, h.log.dump())
			}
			incomingWantTexts(t, h, incomingRootPrompt)
			incomingWantNoLeak(t, h, harnessPhone)
		})
	}
}

// TestHandleIncoming_SameContactMatchesTheSameStateAsNumberOrLID: el contacto se resuelve a un
// id opaco con las referencias del entrante, así que la MISMA persona casa el mismo estado
// llegue como número o como LID. El primer mensaje trae las dos referencias; el segundo, solo
// el LID, y avanza la conversación que abrió el primero.
func TestHandleIncoming_SameContactMatchesTheSameStateAsNumberOrLID(t *testing.T) {
	const lid = "99887766554433@lid"
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	first := h.incoming("wa-1", incomingKeyword)
	first.FromLid = lid
	if err := h.handle(first); err != nil {
		t.Fatalf("HandleIncoming del primer mensaje = %v", err)
	}

	second := h.incoming("wa-2", "1")
	second.From, second.FromPn, second.FromLid = lid, "", lid
	if err := h.handle(second); err != nil {
		t.Fatalf("HandleIncoming del mensaje que llega solo como LID = %v\nlog:\n%s", err, h.log.dump())
	}

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
}
