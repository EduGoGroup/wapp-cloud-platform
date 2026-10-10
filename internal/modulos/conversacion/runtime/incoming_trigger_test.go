package runtime_test

// incoming_trigger_test.go: el DISPARO —un entrante sin conversación viva (incoming.go §3)—,
// los errores de carga de §2 y el TTL conversacional. Parte de incoming_test.go (E-13).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// TestHandleIncoming_Trigger_IgnoreDoesNothing: decisión C. Sin regla que case, el entrante
// no escribe ni envía nada y no gasta cuota del limitador. La señal lleva el texto, ningún
// tipo de evento activo y NUNCA una intención.
func TestHandleIncoming_Trigger_IgnoreDoesNothing(t *testing.T) {
	var spy *incomingSpyResolver
	h := newHarness(t, incomingWithSpyResolver(&spy))

	h.say("wa-1", "texto libre-qzx")

	incomingWantNoState(t, h)
	incomingWantTexts(t, h)
	if calls := h.limiter.Calls(); len(calls) != 0 {
		t.Errorf("Ignore pidió %d tokens al limitador, quería 0", len(calls))
	}
	signals := spy.seen()
	if len(signals) != 1 || signals[0].Text != "texto libre-qzx" || signals[0].Intent != nil || signals[0].ActiveEventKind != "" {
		t.Errorf("señales al resolver = %+v, quería una con el texto, sin intención y sin tipo activo", signals)
	}
}

// TestHandleIncoming_Trigger_ResolverErrorIsIgnored: un fallo del resolver no aborta la
// recepción ni arranca nada: WARN y nil.
func TestHandleIncoming_Trigger_ResolverErrorIsIgnored(t *testing.T) {
	var spy *incomingSpyResolver
	h := newHarness(t, incomingWithSpyResolver(&spy))
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	spy.fail(func(r *incomingSpyResolver) { r.resolveErr = errors.New("resolver-down-qzx") })

	h.say("wa-1", incomingKeyword)

	incomingWantLog(t, h, "warn", "runtime: resolver de disparos falló; se ignora el entrante", 1)
	incomingWantNoState(t, h)
	incomingWantTexts(t, h)
}

// TestHandleIncoming_Trigger_KeywordStartsAPlainFlow: una palabra clave sin event_kind
// arranca el flujo SIN evento: estado en el nodo inicial, la pantalla enviada y ningún evento
// ni event_started.
func TestHandleIncoming_Trigger_KeywordStartsAPlainFlow(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))

	h.say("wa-1", incomingKeyword)

	st := incomingWantNode(t, h, "root")
	if st.FlowID != incomingFlowID || st.EventID != "" || st.OwnerEventID != "" {
		t.Errorf("estado = %+v, quería el flujo %s sin evento", st, incomingFlowID)
	}
	incomingWantTexts(t, h, incomingRootPrompt)
	if evs := h.events.Events(harnessTenant); len(evs) != 0 {
		t.Errorf("eventos = %+v, una keyword sin event_kind no pare evento", evs)
	}
	if got := h.flowEvents(runtime.EffectEventStarted); len(got) != 0 {
		t.Errorf("event_started = %+v, no quería ninguno", got)
	}
}

// TestHandleIncoming_Trigger_EventStartGivesBirthToAnEvent: con plano de eventos y event_kind,
// el disparo pare el evento y arranca su flujo dentro de él.
func TestHandleIncoming_Trigger_EventStartGivesBirthToAnEvent(t *testing.T) {
	h, eventID := incomingLiveEvent(t)

	evs := h.events.Events(harnessTenant)
	if len(evs) != 1 || evs[0].ID != eventID || evs[0].Kind != trigger.EventKindCart || evs[0].FlowID != incomingFlowID {
		t.Fatalf("eventos = %+v, quería un cart vivo con el flujo %s y activo en el estado (%s)", evs, incomingFlowID, eventID)
	}
	if got := h.flowEvents(runtime.EffectEventStarted); len(got) != 1 {
		t.Errorf("event_started = %d filas, quería 1", len(got))
	}
	incomingWantTexts(t, h, incomingRootPrompt)
}

// TestHandleIncoming_Trigger_WithoutEventPlaneEventStartIsAPlainStart: sin plano de eventos
// (WithEventStore(nil)), una regla event_start arranca su flujo en plano, como una keyword.
func TestHandleIncoming_Trigger_WithoutEventPlaneEventStartIsAPlainStart(t *testing.T) {
	h := newHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithEventStore(nil)}
	}))
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))

	h.say("wa-1", incomingEventKeyword)

	if st := incomingWantNode(t, h, "root"); st.EventID != "" {
		t.Errorf("evento activo = %q, quería ninguno sin plano de eventos", st.EventID)
	}
	incomingWantTexts(t, h, incomingRootPrompt)
	if calls := h.limiter.Calls(); len(calls) != 1 {
		t.Errorf("tokens pedidos = %d, quería 1 (el del arranque plano)", len(calls))
	}
}

// TestHandleIncoming_Trigger_StartWithoutFlowIDDoesNothing: una decisión de arranque sin
// flow_id no tiene nada que arrancar: nil, sin estado y sin envío.
func TestHandleIncoming_Trigger_StartWithoutFlowIDDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.seedRule(incomingKeywordRule(""))

	h.say("wa-1", incomingKeyword)

	incomingWantNoState(t, h)
	incomingWantTexts(t, h)
}

// TestHandleIncoming_Trigger_FallbackWithoutOfferStartsItsFlow: sin oferta que enseñar (un
// tenant sin tipos ni nada que retomar), el fallback arranca su flujo en plano, como
// siempre. Jamás pare evento, aunque la regla traiga event_kind.
func TestHandleIncoming_Trigger_FallbackWithoutOfferStartsItsFlow(t *testing.T) {
	opening := &incomingOpening{}
	h := newHarness(t, incomingWithOpening(opening))
	h.seedFlow(incomingStepFlow())
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: incomingFlowID, EventKind: trigger.EventKindCart})

	h.say("wa-1", "no caso con nada-qzx")

	if st := incomingWantNode(t, h, "root"); st.EventID != "" {
		t.Errorf("evento activo = %q, el fallback jamás pare evento", st.EventID)
	}
	if evs := h.events.Events(harnessTenant); len(evs) != 0 {
		t.Errorf("eventos = %+v, el fallback jamás pare evento", evs)
	}
	incomingWantTexts(t, h, incomingRootPrompt)
	if got := opening.built(); got != 1 {
		t.Errorf("BuildOpening se llamó %d veces, quería 1", got)
	}
}

// TestHandleIncoming_Trigger_FallbackWithOfferSendsItAndKeepsItPending: RT-12. Con una oferta
// NO vacía, el fallback cobra un token, guarda el menú pendiente —sin evento y sin flujo— y
// envía el texto de la oferta en lugar de arrancar su flujo.
func TestHandleIncoming_Trigger_FallbackWithOfferSendsItAndKeepsItPending(t *testing.T) {
	opening := &incomingOpening{offer: incomingOffer()}
	h := newHarness(t, incomingWithOpening(opening))
	h.seedFlow(incomingStepFlow())
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: incomingFlowID})

	h.say("wa-1", "no caso con nada-qzx")

	incomingWantTexts(t, h, incomingOfferText)
	st, found := h.state()
	if !found || st.FlowID != "" || st.EventID != "" || len(st.Vars) == 0 {
		t.Errorf("estado = (%+v, %v), quería el menú de la oferta pendiente, sin flujo y sin evento", st, found)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 || calls[0].Key != h.key().String() {
		t.Errorf("tokens pedidos = %+v, quería uno con la clave de la conversación", calls)
	}
}

// TestHandleIncoming_Trigger_OfferErrorFallsBackToTheFlow: si la oferta no se puede construir,
// se avisa a WARN y se cae al fallback de siempre.
func TestHandleIncoming_Trigger_OfferErrorFallsBackToTheFlow(t *testing.T) {
	opening := &incomingOpening{offer: incomingOffer(), err: errors.New("opening-down-qzx")}
	h := newHarness(t, incomingWithOpening(opening))
	h.seedFlow(incomingStepFlow())
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: incomingFlowID})

	h.say("wa-1", "no caso con nada-qzx")

	incomingWantLog(t, h, "warn", "runtime: no se pudo construir la entrada que ofrece", 1)
	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantNode(t, h, "root")
}

// TestHandleIncoming_Trigger_DurableFlowDegradesToTheOffer: RT-9b. Un flujo con contenido
// durable no puede arrancar en plano: el cliente no ve un error, se avisa a WARN con el
// origen y se le envía la oferta SIN cobrar un segundo token. BuildOpening se llama una vez.
func TestHandleIncoming_Trigger_DurableFlowDegradesToTheOffer(t *testing.T) {
	const degraded = "runtime: flujo con contenido durable no puede arrancar sin evento; se degrada a la oferta del despachador"
	cases := []struct {
		name    string
		rule    trigger.Rule
		text    string
		options []harnessOption
		origin  string
		// offered dice si el contacto acaba recibiendo la oferta: el fallback ya la pidió y
		// venía vacía, así que no hay a qué degradar.
		offer   events.Offering
		offered bool
	}{
		{name: "keyword", rule: incomingKeywordRule(incomingSurveyFlowID), text: incomingKeyword, origin: "keyword", offer: incomingOffer(), offered: true},
		{
			name: "event start without event plane", rule: incomingEventRule(incomingSurveyKeyword, trigger.EventKindSurvey, incomingSurveyFlowID),
			text: incomingSurveyKeyword, origin: "event", offer: incomingOffer(), offered: true,
			options: []harnessOption{withOptions(func(*harness) []runtime.Option { return []runtime.Option{runtime.WithEventStore(nil)} })},
		},
		{name: "fallback whose offer was already empty", rule: trigger.Rule{Kind: trigger.KindFallback, FlowID: incomingSurveyFlowID}, text: "nada-qzx", origin: "fallback"},
		{name: "keyword with nothing to offer", rule: incomingKeywordRule(incomingSurveyFlowID), text: incomingKeyword, origin: "keyword"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opening := &incomingOpening{offer: tc.offer}
			h := newHarness(t, append([]harnessOption{incomingWithOpening(opening)}, tc.options...)...)
			h.seedFlow(incomingSurveyFlow())
			h.seedRule(tc.rule)

			h.say("wa-1", tc.text)

			lines := incomingWantLog(t, h, "warn", degraded, 1)
			incomingWantFields(t, lines[0], map[string]any{
				"flow_id": incomingSurveyFlowID, "node_type": model.NodeTypeSurveyQuestion, "origin": tc.origin, "session_id": harnessSession,
			})
			if got := opening.built(); got != 1 {
				t.Errorf("BuildOpening se llamó %d veces, quería 1 por entrante", got)
			}
			if calls := h.limiter.Calls(); len(calls) != 1 {
				t.Errorf("tokens pedidos = %d, quería 1: la degradación no cobra un segundo", len(calls))
			}
			assertIncomingDegradedOutcome(t, h, tc.offered)
		})
	}
}

// assertIncomingDegradedOutcome: con oferta, el cliente recibe SOLO la oferta y su menú queda
// pendiente; sin ella se queda sin respuesta, con un segundo WARN, y no queda estado.
func assertIncomingDegradedOutcome(t *testing.T, h *harness, offered bool) {
	t.Helper()
	if offered {
		incomingWantTexts(t, h, incomingOfferText)
		if st, found := h.state(); !found || st.FlowID != "" {
			t.Errorf("estado = (%+v, %v), quería el menú de la oferta pendiente", st, found)
		}
		return
	}
	incomingWantTexts(t, h)
	incomingWantNoState(t, h)
	if warns := h.log.at("warn"); len(warns) < 2 {
		t.Errorf("líneas a warn = %d, quería la de la degradación y la de «sin oferta a la que degradar»", len(warns))
	}
}

// TestHandleIncoming_LoadStateError: si el estado no se puede cargar, el error sube como
// «runtime: cargar estado: …» y no se dispara nada.
func TestHandleIncoming_LoadStateError(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	rt, flaky := incomingFlakyRuntime(h, nil)
	boom := errors.New("load-down-qzx")
	flaky.fail(func(s *incomingFlakyStore) { s.loadErr = boom })

	err := incomingHandleOn(h, rt, h.incoming("wa-1", incomingKeyword))

	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "runtime: cargar estado: ") {
		t.Fatalf("HandleIncoming = %v, quería «runtime: cargar estado: …» envolviendo la causa", err)
	}
	incomingWantTexts(t, h)
}

// incomingBrokenContacts es el resolver de contactos del guion con Resolve roto.
type incomingBrokenContacts struct {
	contact.Resolver
	err error
}

// Resolve implementa contact.Resolver: falla siempre.
func (c incomingBrokenContacts) Resolve(context.Context, string, []contact.Ref, string) (string, error) {
	return "", c.err
}

// TestHandleIncoming_ContactResolverError: si el contacto no se puede resolver, el error sube
// como «runtime: resolver contacto: …» y el recordatorio de la seña no llega a evaluarse.
func TestHandleIncoming_ContactResolverError(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("contacts-down-qzx")
	rt := runtime.New(h.repo, h.engine, h.sender, h.tenants, incomingBrokenContacts{Resolver: h.contacts, err: boom}, h.log, h.runtimeOptions()...)

	err := incomingHandleOn(h, rt, h.incoming("wa-1", incomingKeyword))

	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "runtime: resolver contacto: ") {
		t.Fatalf("HandleIncoming = %v, quería «runtime: resolver contacto: …» envolviendo la causa", err)
	}
	if calls := h.deposits.Calls(); len(calls) != 0 {
		t.Errorf("el recordatorio de la seña se evaluó sin contacto: %+v", calls)
	}
}

// TestHandleIncoming_ConversationTTL: sin evento activo manda conversation_ttl_seconds, medido
// con el reloj del runtime contra el UpdatedAt del estado. Vence ESTRICTAMENTE por encima del
// TTL; un TTL de 0 no vence nunca.
func TestHandleIncoming_ConversationTTL(t *testing.T) {
	const ttl = 10 * time.Minute
	cases := []struct {
		name    string
		ttl     time.Duration
		silence time.Duration
		expires bool
	}{
		{name: "inside the ttl", ttl: ttl, silence: ttl - time.Second},
		{name: "exactly the ttl does not expire", ttl: ttl, silence: ttl},
		{name: "one second past the ttl", ttl: ttl, silence: ttl + time.Second, expires: true},
		{name: "zero ttl never expires", ttl: 0, silence: 1000 * time.Hour},
		{name: "negative ttl never expires", ttl: -time.Minute, silence: 1000 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := incomingLiveFlat(t)
			h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = tc.ttl })
			h.clock.Advance(tc.silence)

			h.say("wa-2", "1")

			if tc.expires {
				// El «1» ya no es la opción de un menú: es un entrante NUEVO que no casa nada.
				incomingWantNoState(t, h)
				incomingWantTexts(t, h, incomingRootPrompt)
				if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{1}) {
					t.Errorf("rachas cerradas = %v, quería [1]: el estado vencido cierra su racha (RT-11)", streaks)
				}
				return
			}
			incomingWantNode(t, h, "sub")
			incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
		})
	}
}

// TestHandleIncoming_ConversationTTL_ExpiredStateRestartsFromTheTrigger: un estado vencido se
// suelta y el entrante se trata como arranque NUEVO: la palabra clave vuelve a abrir el flujo
// desde su nodo inicial.
func TestHandleIncoming_ConversationTTL_ExpiredStateRestartsFromTheTrigger(t *testing.T) {
	h := incomingLiveFlat(t)
	h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = time.Minute })
	h.say("wa-2", "1")
	incomingWantNode(t, h, "sub")
	h.clock.Advance(2 * time.Minute)

	h.say("wa-3", incomingKeyword)

	incomingWantNode(t, h, "root")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt, incomingRootPrompt)
}

// TestHandleIncoming_ConversationTTL_DoesNotApplyWithAnActiveEvent: INV-18. Los dos relojes
// son excluyentes: con evento activo manda el del evento, así que un TTL conversacional ya
// vencido no suelta la conversación.
func TestHandleIncoming_ConversationTTL_DoesNotApplyWithAnActiveEvent(t *testing.T) {
	h, eventID := incomingLiveEvent(t)
	h.seedSettings(func(s *store.TenantSettings) {
		s.ConversationTTL = time.Minute
		s.EventInactivityTTL = 2 * time.Hour
	})
	h.clock.Advance(30 * time.Minute)

	h.say("wa-2", "1")

	if st := incomingWantNode(t, h, "sub"); st.EventID != eventID {
		t.Errorf("evento activo = %q, quería que siguiera el mismo (%s)", st.EventID, eventID)
	}
	if streaks := h.closedStreaks(); len(streaks) != 0 {
		t.Errorf("rachas cerradas = %v, no quería ninguna: el TTL conversacional no aplica con evento activo", streaks)
	}
}

// TestHandleIncoming_ConversationTTL_UnreadableSettingsDoNotExpire: un fallo al leer los
// ajustes NO vence el estado: WARN y la conversación avanza.
func TestHandleIncoming_ConversationTTL_UnreadableSettingsDoNotExpire(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = time.Minute })
	rt, flaky := incomingFlakyRuntime(h, nil)
	if err := incomingHandleOn(h, rt, h.incoming("wa-1", incomingKeyword)); err != nil {
		t.Fatalf("HandleIncoming del arranque = %v", err)
	}
	h.clock.Advance(time.Hour)
	flaky.fail(func(s *incomingFlakyStore) { s.settingsErr = errors.New("settings-down-qzx") })

	if err := incomingHandleOn(h, rt, h.incoming("wa-2", "1")); err != nil {
		t.Fatalf("HandleIncoming con los ajustes ilegibles = %v, quería nil", err)
	}

	lines := incomingWantLog(t, h, "warn", "runtime: no se pudo leer el TTL conversacional; no se vence el estado", 1)
	incomingWantFields(t, lines[0], map[string]any{"tenant_id": harnessTenant})
	incomingWantNode(t, h, "sub")
}

// incomingUnstampedStore es el almacén del guion devolviendo el estado SIN marca de tiempo
// (UpdatedAt cero), como una fila que nunca la tuvo.
type incomingUnstampedStore struct{ *store.MemoryRepository }

// Load implementa store.ConversationStore.
func (s incomingUnstampedStore) Load(ctx context.Context, key store.Key) (model.Conversation, bool, error) {
	st, found, err := s.MemoryRepository.Load(ctx, key)
	st.UpdatedAt = time.Time{}
	return st, found, err
}

// TestHandleIncoming_ConversationTTL_AStateWithoutStampDoesNotExpire: «con UpdatedAt cero
// (estado sin marca) no vence». Sin fecha contra la que medir el silencio, el TTL
// conversacional no suelta la conversación por muy corto que sea: el entrante la avanza.
func TestHandleIncoming_ConversationTTL_AStateWithoutStampDoesNotExpire(t *testing.T) {
	h := incomingLiveFlat(t)
	h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = time.Minute })
	rt := runtime.New(incomingUnstampedStore{MemoryRepository: h.repo}, h.engine, h.sender, h.tenants, h.contacts, h.log, h.runtimeOptions()...)
	h.clock.Advance(time.Second)

	if err := incomingHandleOn(h, rt, h.incoming("wa-2", "1")); err != nil {
		t.Fatalf("HandleIncoming = %v", err)
	}

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
	if streaks := h.closedStreaks(); len(streaks) != 0 {
		t.Errorf("rachas cerradas = %v, no quería ninguna: un estado sin marca no vence", streaks)
	}
}
