//go:build pendiente

package runtime_test

// incoming_limiter_test.go: lo que cruza todos los caminos del entrante (incoming.go §5): el
// token del limitador antes de cada auto-envío (RT-8), el candado de rachas visto desde fuera
// (RT-11), el recordatorio de la seña (RT-19) y la PII de los logs. Parte de
// incoming_test.go (E-13).

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// TestHandleIncoming_Limiter_OneTokenBeforeEachAutoReply: RT-8. El arranque plano, el avance
// y el aviso de escape piden cada uno UN token, con la clave de la conversación, y lo piden
// ANTES de enviar.
func TestHandleIncoming_Limiter_OneTokenBeforeEachAutoReply(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(incomingEscapeRule(""))
	var tokensAtSend []int
	h.sender.OnSend(func(runtimehelpertest.Send) { tokensAtSend = append(tokensAtSend, len(h.limiter.Calls())) })

	h.say("wa-1", incomingKeyword)
	h.say("wa-2", "1")
	h.say("wa-3", incomingEscapeKeyword)

	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt, incomingDefaultEscape)
	if !slices.Equal(tokensAtSend, []int{1, 2, 3}) {
		t.Errorf("tokens pedidos en el instante de cada envío = %v, quería [1 2 3]: uno ANTES de cada auto-envío", tokensAtSend)
	}
	want := runtimehelpertest.LimiterCall{Key: h.key().String(), Allowed: true}
	if calls := h.limiter.Calls(); !slices.Equal(calls, []runtimehelpertest.LimiterCall{want, want, want}) {
		t.Errorf("peticiones al limitador = %+v, quería tres con la clave de la conversación", calls)
	}
}

// TestHandleIncoming_Limiter_ExhaustedOnTheAdvance: RT-8. Sin cupo el avance NO se contesta,
// pero el turno ocurrió: el estado avanzó. Se cuenta "rate_limit" y se avisa a WARN solo con
// ids opacos.
func TestHandleIncoming_Limiter_ExhaustedOnTheAdvance(t *testing.T) {
	h := incomingLiveFlat(t)
	h.limiter.Limit(1) // el arranque ya gastó el único token.

	h.say("wa-2", "1")

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt)
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"rate_limit"}) {
		t.Errorf("motivos contados = %v, quería [rate_limit]", reasons)
	}
	key := h.key()
	found := false
	for _, line := range h.log.at("warn") {
		if line.fields["contact_id"] == key.ContactID && line.fields["tenant_id"] == key.TenantID && line.fields["session_id"] == key.SessionID {
			found = true
		}
	}
	if !found {
		t.Errorf("no hay línea a warn con los tres ids opacos de la conversación limitada\nlog:\n%s", h.log.dump())
	}
	incomingWantNoLeak(t, h, harnessPhone)
}

// TestHandleIncoming_Limiter_ExhaustedOnTheStart: RT-8. Sin cupo no se arranca ni se pare
// nada: ni estado, ni evento, ni respuesta; solo el motivo contado.
func TestHandleIncoming_Limiter_ExhaustedOnTheStart(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		{name: "plain start", text: incomingKeyword},
		{name: "event birth", text: incomingEventKeyword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedFlow(incomingStepFlow())
			h.seedRule(incomingKeywordRule(incomingFlowID))
			h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))
			h.limiter.Limit(0)

			h.say("wa-1", tc.text)

			incomingWantNoState(t, h)
			incomingWantTexts(t, h)
			if evs := h.events.Events(harnessTenant); len(evs) != 0 {
				t.Errorf("eventos = %+v, sin cupo no nace ninguno", evs)
			}
			if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"rate_limit"}) {
				t.Errorf("motivos contados = %v, quería [rate_limit]", reasons)
			}
		})
	}
}

// TestHandleIncoming_Limiter_NilMeansNoCap: RT-8 y RT-12. Sin limitador no hay tope: todo se
// contesta y no se cuenta ningún corte.
func TestHandleIncoming_Limiter_NilMeansNoCap(t *testing.T) {
	h := incomingLiveFlat(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithReplyLimiter(nil)}
	}))
	h.limiter.Limit(0) // el doble ya no está cableado: su tope no puede importar.

	h.say("wa-2", "1")
	h.say("wa-3", "1")

	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt, incomingLeafText)
	if reasons := h.blockedReasons(); len(reasons) != 0 {
		t.Errorf("motivos contados = %v, no quería ninguno", reasons)
	}
}

// TestHandleIncoming_Streak_AnIncomingDoesNotRestartIt: RT-11. La racha la suman las
// EMISIONES; un entrante entre dos auto-respuestas no la reinicia. Al cerrar la conversación,
// el hook recibe la longitud entera del episodio, una sola vez.
func TestHandleIncoming_Streak_AnIncomingDoesNotRestartIt(t *testing.T) {
	h := incomingLiveFlat(t)
	h.say("wa-2", "1")
	if streaks := h.closedStreaks(); len(streaks) != 0 {
		t.Fatalf("rachas cerradas = %v con la conversación viva, no quería ninguna", streaks)
	}

	h.say("wa-3", incomingEscapeKeyword)

	if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{2}) {
		t.Errorf("rachas cerradas = %v, quería [2]: dos auto-respuestas con un entrante en medio", streaks)
	}
}

// TestHandleIncoming_Streak_WithoutHookNothingBreaks: RT-11 y RT-12. Sin el hook de rachas el
// camino del entrante es el mismo, también al cerrar una conversación.
func TestHandleIncoming_Streak_WithoutHookNothingBreaks(t *testing.T) {
	h := incomingLiveFlat(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithAutoreplyStreakHook(nil), runtime.WithReactiveBlockedHook(nil)}
	}))
	h.limiter.Limit(2)

	h.say("wa-2", "1")
	h.say("wa-3", incomingEscapeKeyword) // sin cupo: el corte tampoco tiene a quién contarse.

	incomingWantNoState(t, h)
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
	if streaks, reasons := h.closedStreaks(), h.blockedReasons(); len(streaks) != 0 || len(reasons) != 0 {
		t.Errorf("hooks retirados recibieron rachas %v y motivos %v", streaks, reasons)
	}
}

// incomingReminderProbe es un runtime.DepositReminder que apunta, en cada toque, cuántos
// envíos había despachado ya el Sender: dice si el recordatorio se evalúa después de contestar.
type incomingReminderProbe struct {
	sender *runtimehelpertest.Sender
	mu     sync.Mutex
	sentAt []int
}

// RemindContact implementa runtime.DepositReminder.
func (p *incomingReminderProbe) RemindContact(context.Context, string, string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sentAt = append(p.sentAt, len(p.sender.Sends()))
	return nil
}

func (p *incomingReminderProbe) calls() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sentAt)
}

// TestHandleIncoming_DepositReminder_EveryAdmittedIncomingEndsWithIt: RT-19. Cada entrante que
// pasa las guardas termina evaluando el recordatorio, por tenant y CONTACTO —también un
// «hola» sin conversación que el motor ignora—, y sin gastar cuota del limitador.
func TestHandleIncoming_DepositReminder_EveryAdmittedIncomingEndsWithIt(t *testing.T) {
	h := newHarness(t)
	h.deposits.SetTexts("Recuerda tu seña-qzx")

	h.say("wa-1", "hola sin conversación-qzx")

	want := []runtimehelpertest.ReminderCall{{TenantID: harnessTenant, ContactID: h.contactID(harnessPhone)}}
	if calls := h.deposits.Calls(); !slices.Equal(calls, want) {
		t.Errorf("toques del recordatorio = %+v, quería %+v", calls, want)
	}
	if calls := h.limiter.Calls(); len(calls) != 0 {
		t.Errorf("el recordatorio pidió %d tokens al limitador, quería 0", len(calls))
	}
	incomingWantNoState(t, h)
}

// TestHandleIncoming_DepositReminder_RunsAfterTheReply: RT-19. El recordatorio es lo ÚLTIMO
// del turno: cuando se evalúa, la respuesta de ese turno ya salió. Vale para el disparo, el
// avance y el escape.
func TestHandleIncoming_DepositReminder_RunsAfterTheReply(t *testing.T) {
	var probe *incomingReminderProbe
	h := newHarness(t, withOptions(func(h *harness) []runtime.Option {
		probe = &incomingReminderProbe{sender: h.sender}
		return []runtime.Option{runtime.WithDepositReminder(probe)}
	}))
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(incomingEscapeRule(""))

	h.say("wa-1", incomingKeyword)
	h.say("wa-2", "1")
	h.say("wa-3", incomingEscapeKeyword)

	if got := probe.calls(); !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("envíos ya despachados en cada toque = %v, quería [1 2 3]: el recordatorio va después de contestar", got)
	}
}

// TestHandleIncoming_DepositReminder_NilChangesNothing: RT-19 y RT-12. Sin la opción el
// camino del entrante es el de siempre.
func TestHandleIncoming_DepositReminder_NilChangesNothing(t *testing.T) {
	h := incomingLiveFlat(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithDepositReminder(nil)}
	}))
	h.deposits.SetTexts("Recuerda tu seña-qzx")

	h.say("wa-2", "1")

	incomingWantNode(t, h, "sub")
	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
	if calls := h.deposits.Calls(); len(calls) != 0 {
		t.Errorf("el recordatorio se tocó sin estar cableado: %+v", calls)
	}
}

// TestHandleIncoming_NoPIIInLogsNorEventIDsToTheClient: PII. Tras un guion que recorre el
// nacimiento de un evento, un texto libre, un avance sin cupo y un escape, ningún log lleva
// el texto del cliente ni su número, y ningún texto enviado lleva el id del evento.
func TestHandleIncoming_NoPIIInLogsNorEventIDsToTheClient(t *testing.T) {
	const clientText = "mi dirección es Calle Falsa 123-qzx"
	h, eventID := incomingLiveEvent(t)

	h.say("wa-2", clientText)
	h.limiter.Limit(2)
	h.say("wa-3", "1")
	h.limiter.Limit(-1)
	h.say("wa-4", incomingEscapeKeyword)
	h.say("wa-5", clientText)

	incomingWantNoLeak(t, h, clientText, "Calle Falsa", harnessPhone)
	for _, text := range h.texts() {
		if strings.Contains(text, eventID) {
			t.Errorf("un texto enviado al cliente lleva el id del evento: %q", text)
		}
	}
	if len(h.log.at("warn")) == 0 {
		t.Errorf("el guion no produjo ni un aviso (esperaba el del limitador): no ejercitó los logs que vigila")
	}
}
