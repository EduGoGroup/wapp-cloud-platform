//go:build pendiente

package runtime_test

// incoming_advance_test.go: el AVANCE —un entrante sobre una conversación viva (incoming.go
// §4)—: el escape global (RT-9), las dos sueltas de mitad de conversación, la versión de la
// definición, el orden Save → Send (RT-4) y el turno cortado por el sink durable (RT-10).
// Parte de incoming_test.go (E-13).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// incomingEscapeReasons devuelve el `reason` de cada event_escaped escrito, en orden.
func incomingEscapeReasons(h *harness) []string {
	escaped := h.flowEvents(runtime.EffectEventEscaped)
	reasons := make([]string, 0, len(escaped))
	for _, fe := range escaped {
		reasons = append(reasons, fmt.Sprint(fe.Payload["reason"]))
	}
	return reasons
}

// TestHandleIncoming_Escape_ClosesTheConversationAndSendsTheNotice: RT-9. El aviso es el
// `message` de la regla o, si viene vacío, el texto por defecto, byte a byte. El estado se
// borra y su racha se cierra (RT-11).
func TestHandleIncoming_Escape_ClosesTheConversationAndSendsTheNotice(t *testing.T) {
	cases := []struct {
		name, message, want string
	}{
		{name: "default notice", message: "", want: incomingDefaultEscape},
		{name: "notice of the rule", message: "Hasta luego-qzx", want: "Hasta luego-qzx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedFlow(incomingStepFlow())
			h.seedRule(incomingKeywordRule(incomingFlowID))
			h.seedRule(incomingEscapeRule(tc.message))
			h.say("wa-1", incomingKeyword)

			h.say("wa-2", incomingEscapeKeyword)

			incomingWantNoState(t, h)
			incomingWantTexts(t, h, incomingRootPrompt, tc.want)
			if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{1}) {
				t.Errorf("rachas cerradas = %v, quería [1]: el escape cierra la racha del episodio", streaks)
			}
			if reasons := incomingEscapeReasons(h); len(reasons) != 0 {
				t.Errorf("event_escaped = %v, una conversación sin evento no emite ninguno", reasons)
			}
		})
	}
}

// TestHandleIncoming_Escape_WithAnEventEmitsEventEscaped: RT-9. Con evento conocido, el escape
// emite event_escaped con reason = client_escape.
func TestHandleIncoming_Escape_WithAnEventEmitsEventEscaped(t *testing.T) {
	h, _ := incomingLiveEvent(t)

	h.say("wa-2", incomingEscapeKeyword)

	incomingWantNoState(t, h)
	if reasons := incomingEscapeReasons(h); !slices.Equal(reasons, []string{runtime.EscapeReasonClientEscape}) {
		t.Errorf("reason de event_escaped = %v, quería [%s]", reasons, runtime.EscapeReasonClientEscape)
	}
	incomingWantTexts(t, h, incomingRootPrompt, incomingDefaultEscape)
}

// TestHandleIncoming_Escape_WithoutQuotaStillEscapes: RT-9 y RT-8. El token del aviso se cobra
// DESPUÉS de borrar el estado y emitir el efecto: sin cupo no se avisa, pero el escape ya
// ocurrió. Se cuenta "rate_limit".
func TestHandleIncoming_Escape_WithoutQuotaStillEscapes(t *testing.T) {
	h, _ := incomingLiveEvent(t)
	h.limiter.Limit(1) // el nacimiento del evento ya gastó el único token.

	h.say("wa-2", incomingEscapeKeyword)

	incomingWantNoState(t, h)
	if reasons := incomingEscapeReasons(h); !slices.Equal(reasons, []string{runtime.EscapeReasonClientEscape}) {
		t.Errorf("reason de event_escaped = %v, quería [%s] aunque no haya cupo", reasons, runtime.EscapeReasonClientEscape)
	}
	incomingWantTexts(t, h, incomingRootPrompt)
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"rate_limit"}) {
		t.Errorf("motivos contados = %v, quería [rate_limit]", reasons)
	}
	if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{1}) {
		t.Errorf("rachas cerradas = %v, quería [1]", streaks)
	}
}

// TestHandleIncoming_Escape_OnlyCutsALiveConversation: el escape es del AVANCE. Sin
// conversación viva, la palabra de escape es un texto más que no casa nada.
func TestHandleIncoming_Escape_OnlyCutsALiveConversation(t *testing.T) {
	h := newHarness(t)
	h.seedRule(incomingEscapeRule(""))

	h.say("wa-1", incomingEscapeKeyword)

	incomingWantTexts(t, h)
	incomingWantNoState(t, h)
}

// TestHandleIncoming_Advance_ResolverFailuresAreBestEffort: un error de IsEscape no escapa ni
// corta, y uno de ResolveLive no salta de evento ni corta: WARN y el avance sigue.
func TestHandleIncoming_Advance_ResolverFailuresAreBestEffort(t *testing.T) {
	cases := []struct {
		name string
		fail func(r *incomingSpyResolver)
		warn string
	}{
		{
			name: "IsEscape", warn: "runtime: IsEscape falló; se ignora el escape",
			fail: func(r *incomingSpyResolver) { r.escapeErr = errors.New("escape-down-qzx") },
		},
		{
			name: "ResolveLive", warn: "runtime: ResolveLive falló; se ignora el salto de evento",
			fail: func(r *incomingSpyResolver) { r.liveErr = errors.New("live-down-qzx") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spy *incomingSpyResolver
			h, eventID := incomingLiveEvent(t, incomingWithSpyResolver(&spy))
			spy.fail(tc.fail)

			h.say("wa-2", "1")

			incomingWantLog(t, h, "warn", tc.warn, 1)
			if st := incomingWantNode(t, h, "sub"); st.EventID != eventID {
				t.Errorf("evento activo = %q, quería el mismo (%s)", st.EventID, eventID)
			}
			incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
		})
	}
}

// TestHandleIncoming_Advance_OrphanMenuIsReleasedAndRetriggered: §4.5. El menú de la oferta
// queda pendiente en un estado SIN flujo; si el texto siguiente no es una opción, ese estado
// se borra, su racha se cierra y el entrante se trata como disparo.
func TestHandleIncoming_Advance_OrphanMenuIsReleasedAndRetriggered(t *testing.T) {
	h := newHarness(t, incomingWithOpening(&incomingOpening{offer: incomingOffer()}))
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: incomingFlowID})
	h.say("wa-1", "no caso con nada-qzx")
	incomingWantTexts(t, h, incomingOfferText)

	h.say("wa-2", incomingKeyword)

	if st := incomingWantNode(t, h, "root"); st.FlowID != incomingFlowID {
		t.Errorf("flujo = %q, quería que la palabra clave arrancara %s", st.FlowID, incomingFlowID)
	}
	incomingWantTexts(t, h, incomingOfferText, incomingRootPrompt)
	if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{1}) {
		t.Errorf("rachas cerradas = %v, quería [1]: soltar el menú huérfano cierra su racha (RT-11)", streaks)
	}
}

// TestHandleIncoming_Advance_ReleasesNameTheActiveEvent: §4.5 y §4.6. Las dos sueltas de mitad
// de conversación, con un evento activo que sigue vivo: emiten event_escaped con SU causa y
// el disparo siguiente lleva el tipo de ese evento en la señal.
func TestHandleIncoming_Advance_ReleasesNameTheActiveEvent(t *testing.T) {
	cases := []struct {
		name   string
		state  func(st *model.Conversation)
		reason string
	}{
		{name: "state without flow", state: func(*model.Conversation) {}, reason: runtime.EscapeReasonOrphanMenu},
		{
			name: "terminal state without pending closure", reason: runtime.EscapeReasonOwnerFlowFinished,
			state: func(st *model.Conversation) {
				st.FlowID, st.FlowVersion, st.CurrentNode = incomingFlowID, 1, model.NodeTerminal
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spy *incomingSpyResolver
			h := newHarness(t, incomingWithSpyResolver(&spy))
			h.seedFlow(incomingStepFlow())
			ev := h.seedEvent(trigger.EventKindCart, incomingFlowID, 1)
			st := model.Conversation{TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone), EventID: ev.ID}
			tc.state(&st)
			if err := h.repo.Save(t.Context(), st); err != nil {
				t.Fatalf("sembrar el estado: %v", err)
			}

			h.say("wa-1", "texto libre-qzx")

			incomingWantNoState(t, h)
			if reasons := incomingEscapeReasons(h); !slices.Equal(reasons, []string{tc.reason}) {
				t.Errorf("reason de event_escaped = %v, quería [%s]", reasons, tc.reason)
			}
			signals := spy.seen()
			if len(signals) != 1 || signals[0].ActiveEventKind != trigger.EventKindCart || signals[0].Intent != nil {
				t.Errorf("señales al resolver = %+v, quería una con el tipo del evento que estaba activo (cart)", signals)
			}
		})
	}
}

// TestHandleIncoming_Advance_FinishedFlowDoesNotLeaveTheContactMute: §4.6. Un contacto cuyo
// flujo ya terminó no se queda mudo: el estado terminal se suelta, su racha se cierra y la
// palabra clave vuelve a arrancar.
func TestHandleIncoming_Advance_FinishedFlowDoesNotLeaveTheContactMute(t *testing.T) {
	h := incomingLiveFlat(t)
	h.say("wa-2", "1")
	h.say("wa-3", "1")
	if st, found := h.state(); !found || !st.Finished() {
		t.Fatalf("estado = (%+v, %v), quería el flujo terminado", st, found)
	}

	h.say("wa-4", incomingKeyword)

	incomingWantNode(t, h, "root")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt, incomingLeafText, incomingRootPrompt)
	if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{3}) {
		t.Errorf("rachas cerradas = %v, quería [3]: tres auto-respuestas en el episodio que se suelta", streaks)
	}
}

// TestHandleIncoming_Advance_UsesTheVersionTheConversationStartedWith: §4.7. Publicar una
// versión nueva del flujo no cambia la conversación en curso: avanza con la suya.
func TestHandleIncoming_Advance_UsesTheVersionTheConversationStartedWith(t *testing.T) {
	h := incomingLiveFlat(t)
	newer := incomingStepFlow()
	sub := newer.Nodes["sub"]
	sub.Prompt = "Submenú de la versión 2-qzx"
	newer.Nodes["sub"] = sub
	if version := h.seedFlow(newer); version != 2 {
		t.Fatalf("la segunda publicación quedó en la versión %d, quería 2", version)
	}

	h.say("wa-2", "1")

	if st := incomingWantNode(t, h, "sub"); st.FlowVersion != 1 {
		t.Errorf("versión del estado = %d, quería que siguiera en la 1", st.FlowVersion)
	}
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
}

// TestHandleIncoming_Advance_MissingDefinitionError: si la definición de la versión en curso
// no se puede leer, el error sube como «runtime: definición en curso (vN): …».
func TestHandleIncoming_Advance_MissingDefinitionError(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	st := model.Conversation{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		FlowID: incomingFlowID, FlowVersion: 7, CurrentNode: "root",
	}
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("sembrar el estado: %v", err)
	}

	err := h.handle(h.incoming("wa-1", "1"))

	if err == nil || !strings.HasPrefix(err.Error(), "runtime: definición en curso (v7): ") {
		t.Fatalf("HandleIncoming = %v, quería «runtime: definición en curso (v7): …»", err)
	}
	incomingWantTexts(t, h)
}

// TestHandleIncoming_Advance_SavesBeforeSending: RT-4. En el instante del envío el estado ya
// está guardado en el nodo nuevo; y si el envío falla, el error sube pero el estado avanzó y
// el paso no se reenvía.
func TestHandleIncoming_Advance_SavesBeforeSending(t *testing.T) {
	h := incomingLiveFlat(t)
	var nodeAtSend string
	h.sender.OnSend(func(runtimehelpertest.Send) {
		if st, found := h.state(); found {
			nodeAtSend = st.CurrentNode
		}
	})
	boom := errors.New("edge-down-qzx")
	h.sender.FailText(boom)

	err := h.handle(h.incoming("wa-2", "1"))

	if !errors.Is(err, boom) {
		t.Fatalf("HandleIncoming = %v, quería el error del envío", err)
	}
	if nodeAtSend != "sub" {
		t.Errorf("nodo guardado en el instante del envío = %q, quería sub (Save antes de Send)", nodeAtSend)
	}
	if st := incomingWantNode(t, h, "sub"); st.LastWaMessageID != "wa-2" {
		t.Errorf("last_wa_message_id = %q, quería wa-2", st.LastWaMessageID)
	}
}

// TestHandleIncoming_Advance_SaveErrorSendsNothing: RT-4. Si el estado no se puede guardar,
// el error sube como «runtime: guardar estado: …» y la respuesta no sale.
func TestHandleIncoming_Advance_SaveErrorSendsNothing(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	rt, flaky := incomingFlakyRuntime(h, nil)
	if err := incomingHandleOn(h, rt, h.incoming("wa-1", incomingKeyword)); err != nil {
		t.Fatalf("HandleIncoming del arranque = %v", err)
	}
	boom := errors.New("save-down-qzx")
	flaky.fail(func(s *incomingFlakyStore) { s.saveErr = boom })

	err := incomingHandleOn(h, rt, h.incoming("wa-2", "1"))

	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "runtime: guardar estado: ") {
		t.Fatalf("HandleIncoming = %v, quería «runtime: guardar estado: …» envolviendo la causa", err)
	}
	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantNode(t, h, "root")
}

// incomingBrokenSink es el sink del guion —un PersistSink sobre el almacén— que, mientras
// está roto, falla la materialización de survey_answer con un error que huele a base de
// datos. Lo demás lo deja pasar.
type incomingBrokenSink struct {
	inner  runtime.EventSink
	mu     sync.Mutex
	broken bool
	tries  int
}

// Handle implementa runtime.EventSink.
func (s *incomingBrokenSink) Handle(ctx context.Context, ec runtime.EffectContext, eff modules.Effect) error {
	s.mu.Lock()
	fail := s.broken && eff.Name == "survey_answer"
	if fail {
		s.tries++
	}
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("%w: SQLSTATE 08006 connection_failure-qzx", runtime.ErrMaterializationFailed)
	}
	return s.inner.Handle(ctx, ec, eff)
}

func (s *incomingBrokenSink) set(broken bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken = broken
}

func (s *incomingBrokenSink) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tries
}

// incomingLiveSurvey abre una encuesta (contenido durable) dentro de su evento, con el sink
// rompible cableado y todavía sano. Devuelve el arnés, el sink y el id del evento.
func incomingLiveSurvey(t *testing.T, opts ...harnessOption) (*harness, *incomingBrokenSink, string) {
	t.Helper()
	sink := &incomingBrokenSink{}
	all := append([]harnessOption{withSinks(func(h *harness) []runtime.EventSink {
		sink.inner = runtime.NewPersistSink(h.repo)
		return []runtime.EventSink{sink}
	})}, opts...)
	h := newHarness(t, all...)
	h.seedFlow(incomingSurveyFlow())
	h.seedRule(incomingEventRule(incomingSurveyKeyword, trigger.EventKindSurvey, incomingSurveyFlowID))
	h.say("wa-open", incomingSurveyKeyword)
	st := incomingWantNode(t, h, "q1")
	if st.EventID == "" {
		t.Fatalf("la encuesta no quedó dentro de un evento: %+v\nlog:\n%s", st, h.log.dump())
	}
	return h, sink, st.EventID
}

// TestHandleIncoming_DurableSinkFailure_CutsTheTurn: RT-10. Cuando el sink durable no puede
// materializar el efecto tras su reintento, el turno se CORTA: el estado avanzado no se
// guarda y el cliente recibe SOLO el aviso de avería —por el camino normal, con su token—,
// nunca un SQLSTATE. Cuando el sink sana, la misma respuesta avanza.
func TestHandleIncoming_DurableSinkFailure_CutsTheTurn(t *testing.T) {
	h, sink, _ := incomingLiveSurvey(t)
	before, _ := h.state()
	tokens := len(h.limiter.Calls())
	sink.set(true)

	if err := h.handle(h.incoming("wa-2", "1")); err != nil {
		t.Fatalf("HandleIncoming del turno cortado = %v, quería nil (el resultado de enviar el aviso)", err)
	}

	incomingWantTexts(t, h, incomingSurveyFirst, incomingSinkFailureNotice)
	if after := incomingWantNode(t, h, "q1"); after.LastWaMessageID != before.LastWaMessageID {
		t.Errorf("last_wa_message_id = %q, quería el de antes del turno cortado (%q): no se guarda nada", after.LastWaMessageID, before.LastWaMessageID)
	}
	if sink.attempts() == 0 {
		t.Fatal("el sink durable no llegó a recibir el efecto: el test no ejercitó el corte")
	}
	if got := len(h.limiter.Calls()) - tokens; got != 1 {
		t.Errorf("tokens pedidos por el turno cortado = %d, quería 1 (el del aviso)", got)
	}
	incomingWantLog(t, h, "error", "runtime: turno cortado: el sink durable no pudo materializar el efecto tras el reintento acotado", 1)
	for _, text := range h.texts() {
		if strings.Contains(text, "SQLSTATE") || strings.Contains(text, "connection_failure") {
			t.Errorf("el cliente recibió un detalle de la base: %q", text)
		}
	}

	sink.set(false)
	h.say("wa-3", "1")
	incomingWantNode(t, h, "q2")
	incomingWantTexts(t, h, incomingSurveyFirst, incomingSinkFailureNotice, incomingSurveySecond)
}

// TestHandleIncoming_DurableSinkFailure_WithoutQuotaTheNoticeIsNotSent: RT-10 y RT-8. El aviso
// de avería sale por el camino normal: sin cupo no se envía, el turno sigue cortado y el
// resultado es nil.
func TestHandleIncoming_DurableSinkFailure_WithoutQuotaTheNoticeIsNotSent(t *testing.T) {
	h, sink, _ := incomingLiveSurvey(t)
	h.limiter.Limit(1) // el nacimiento del evento ya gastó el único token.
	sink.set(true)

	if err := h.handle(h.incoming("wa-2", "1")); err != nil {
		t.Fatalf("HandleIncoming = %v, quería nil", err)
	}

	incomingWantTexts(t, h, incomingSurveyFirst)
	incomingWantNode(t, h, "q1")
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"rate_limit"}) {
		t.Errorf("motivos contados = %v, quería [rate_limit]", reasons)
	}
}
