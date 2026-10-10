//go:build pendiente

package runtime_test

// events_summary_test.go prueba el RESUMEN del evento que se abandona y la COLETILLA de
// «tienes algo a medias» (events.go, EV-8), y el recordatorio de lo decidido al volver a un
// evento (EV-3, camino 2).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

const (
	// eventsLineLabel es la etiqueta de la única línea de pedido del guion.
	eventsLineLabel = "Café de altura-zzq"
	// eventsCartLevel es el nivel de la sub-máquina que el guion deja en Vars: solo existe
	// mientras el estado vive, así que un resumen que lo lleve se escribió ANTES de borrarlo.
	eventsCartLevel = "choosing_qty-zzq"
	// eventsSurveyTagline es la coletilla del despachador con una encuesta a medias.
	eventsSurveyTagline = "Por cierto, tu encuesta sigue a medias — dime si quieres retomarlo."
	eventsIntentName    = "make_order-zzq"
)

// eventsLines es la fuente DURABLE de las líneas del pedido: siempre la misma línea.
type eventsLines struct{}

func (eventsLines) OpenIntakeLines(context.Context, string, string, string) ([]events.SummaryLine, error) {
	return []events.SummaryLine{{SKU: "CAFE-zzq", Label: eventsLineLabel, Qty: 2, UnitPrice: 2.5}}, nil
}

// eventsAnswers es la fuente de las respuestas de encuesta: ninguna.
type eventsAnswers struct{}

func (eventsAnswers) SurveyAnswers(context.Context, events.Event) ([]events.SummaryAnswer, error) {
	return nil, nil
}

// eventsWithCartLines cablea unas fuentes del resumen en las que todo `cart` tiene una línea.
func eventsWithCartLines() harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithSummarySources(events.SummarySources{Lines: eventsLines{}, Answers: eventsAnswers{}})}
	})
}

// eventsSummaries devuelve los resúmenes escritos en el hilo de un evento, decodificados.
func eventsSummaries(t *testing.T, h *harness, eventID string) []events.Summary {
	t.Helper()
	var out []events.Summary
	for _, entry := range h.thread(eventID) {
		if entry.Kind != events.KindSummary {
			continue
		}
		var sum events.Summary
		if err := json.Unmarshal(entry.Payload, &sum); err != nil {
			t.Fatalf("resumen ilegible en el hilo de %s: %v", eventID, err)
		}
		out = append(out, sum)
	}
	return out
}

// eventsLiveCartWithLevel abre el carrito y deja en sus Vars el nivel de la sub-máquina.
func eventsLiveCartWithLevel(t *testing.T, h *harness) events.Event {
	t.Helper()
	h.say("wa-1", eventsCartWord)
	st, _ := h.state()
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	st.Vars["cart"] = map[string]any{"level": eventsCartLevel}
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("sembrar el nivel del carrito en Vars: %v", err)
	}
	return eventsAliveOfKind(t, h, trigger.EventKindCart)
}

// TestEvents_EveryAbandonIsSummarisedBeforeTheStateDies (EV-8): en los TRES abandonos —salto
// por tipo, event_stop y escape— el resumen se escribe ANTES de destruir o apagar el estado:
// lleva el nivel que solo vivía en sus Vars.
func TestEvents_EveryAbandonIsSummarisedBeforeTheStateDies(t *testing.T) {
	for _, tc := range []struct{ name, word string }{
		{name: "switch by kind", word: eventsSurveyWord},
		{name: "event_stop", word: eventsStopWord},
		{name: "escape", word: "salir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := eventsSwitchHarness(t, eventsWithCartLines())
			h.seedRule(trigger.Rule{Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact})
			cart := eventsLiveCartWithLevel(t, h)

			h.say("wa-2", tc.word)

			sums := eventsSummaries(t, h, cart.ID)
			if len(sums) != 1 {
				t.Fatalf("resúmenes del carrito = %+v, quería uno\nlog:\n%s", sums, h.log.dump())
			}
			if sums[0].Kind != trigger.EventKindCart || sums[0].Level != eventsCartLevel || len(sums[0].Lines) != 1 || sums[0].Lines[0].Label != eventsLineLabel {
				t.Errorf("resumen = %+v, quería las líneas del pedido y el nivel %q que estaba en Vars", sums[0], eventsCartLevel)
			}
			if row := eventsRow(t, h, cart.ID); row.Status != events.StatusOpen {
				t.Errorf("el carrito quedó %q, abandonar no lo cierra", row.Status)
			}
		})
	}
}

// TestEvents_SummaryIsBestEffort (EV-8, RT-12): sin fuentes del resumen cableadas no se
// escribe ninguno y el abandono ocurre igual.
func TestEvents_SummaryIsBestEffort(t *testing.T) {
	h := eventsSwitchHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithSummarySources(events.SummarySources{})}
	}))
	cart := eventsLiveCartWithLevel(t, h)

	h.say("wa-2", eventsSurveyWord)

	if sums := eventsSummaries(t, h, cart.ID); len(sums) != 0 {
		t.Errorf("resúmenes = %+v, sin fuentes no se escribe ninguno", sums)
	}
	surveyEv := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
	if st, _ := h.state(); st.EventID != surveyEv.ID {
		t.Errorf("estado = %+v, el salto debía ocurrir igual", st)
	}
}

// TestEvents_ExpiryIsNotAnAbandon (EV-8, EV-6): callarse no es abandonar. Al vencer la
// ventana el evento sigue open y rescatable, y no se escribe resumen.
func TestEvents_ExpiryIsNotAnAbandon(t *testing.T) {
	h := eventsSwitchHarness(t, eventsWithCartLines())
	cart := eventsLiveCartWithLevel(t, h)
	h.clock.Advance(eventsWindow + time.Minute)

	h.say("wa-2", "vuelvo tarde-zzq")

	eventsRequireLifecycle(t, h, runtime.EffectEventInactivityExpired, 1)
	if sums := eventsSummaries(t, h, cart.ID); len(sums) != 0 {
		t.Errorf("resúmenes = %+v, el vencimiento no escribe ninguno", sums)
	}
}

// TestEvents_ComingBackRemindsWhatWasDecided (EV-3, camino 2): al conmutar hacia un evento
// que ya existía se le recuerda al cliente lo que llevaba, ANTES de la pantalla del flujo y
// con el mismo token.
func TestEvents_ComingBackRemindsWhatWasDecided(t *testing.T) {
	h := eventsSwitchHarness(t, eventsWithCartLines())
	h.say("wa-1", eventsCartWord)
	h.say("wa-2", eventsSurveyWord)

	h.say("wa-3", eventsCartWord)

	texts := h.texts()
	if len(texts) != 4 {
		t.Fatalf("textos = %q, quería tres pantallas y un resumen\nlog:\n%s", texts, h.log.dump())
	}
	if !strings.HasPrefix(texts[2], "Esto es lo que ya habías decidido en tu pedido:") || !strings.Contains(texts[2], eventsLineLabel) {
		t.Errorf("recordatorio = %q, quería el resumen de lo decidido en el pedido", texts[2])
	}
	if texts[3] != texts[0] {
		t.Errorf("tras el recordatorio salió %q, quería la pantalla del flujo %q", texts[3], texts[0])
	}
	if calls := h.limiter.Calls(); len(calls) != 3 {
		t.Errorf("tokens pedidos = %d, quería 3: el recordatorio no cobra el suyo", len(calls))
	}
}

// eventsIntentResolver fabrica el arranque que combina intención Y nacimiento de un `cart`.
func eventsIntentResolver() harnessOption {
	return eventsWithResolver(eventsScriptedResolver{decision: trigger.Decision{
		Action: trigger.StartEvent, FlowID: eventsCartFlow, EventKind: trigger.EventKindCart, IntentName: eventsIntentName,
	}})
}

// TestEvents_TaglineIsPastedToTheLastTextAndMarked (EV-8, OpeningBuilder.BuildTagline): con
// una intención atendida y algo a medias, la coletilla se pega al ÚLTIMO texto —no es un
// saliente aparte— y se marca en el mismo guardado.
func TestEvents_TaglineIsPastedToTheLastTextAndMarked(t *testing.T) {
	h := eventsCartHarness(t, eventsIntentResolver())
	h.seedEvent(trigger.EventKindSurvey, "", 0)

	h.say("wa-1", "quiero pedir algo-zzq")

	want := menuFlow(eventsCartFlow).Nodes["root"].Prompt + "\n\n" + eventsSurveyTagline
	if texts := h.texts(); len(texts) != 1 || texts[0] != want {
		t.Fatalf("textos = %q, quería UN texto con la coletilla pegada:\n%q", texts, want)
	}
	st, _ := h.state()
	if marked, ok := st.Vars["tagline_offered"].(bool); !ok || !marked {
		t.Errorf("Vars = %v, quería tagline_offered = true", st.Vars)
	}
	if cart := eventsAliveOfKind(t, h, trigger.EventKindCart); st.EventID != cart.ID {
		t.Errorf("estado = %+v, la marca debía sobrevivir al estampado del evento %s", st, cart.ID)
	}
}

// TestEvents_TheNewbornEventDoesNotAnnounceItself (EV-8): la coletilla se resuelve ANTES de
// crear el evento nuevo. Sin nada a medias de antes no se dice nada ni se marca nada.
func TestEvents_TheNewbornEventDoesNotAnnounceItself(t *testing.T) {
	h := eventsCartHarness(t, eventsIntentResolver())

	h.say("wa-1", "quiero pedir algo-zzq")

	eventsAliveOfKind(t, h, trigger.EventKindCart)
	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Fatalf("textos = %q, el evento recién nacido no se anuncia «a medias» a sí mismo", texts)
	}
	if st, _ := h.state(); st.Vars["tagline_offered"] != nil {
		t.Errorf("Vars = %v, sin coletilla no se marca la conversación", st.Vars)
	}
}

// TestEvents_StartWithoutIntentCarriesNoTagline (EV-8): la coletilla es del camino de la
// intención. Un event_start de palabra no la lleva aunque haya algo a medias.
func TestEvents_StartWithoutIntentCarriesNoTagline(t *testing.T) {
	h := eventsCartHarness(t)
	h.seedEvent(trigger.EventKindSurvey, "", 0)

	h.say("wa-1", eventsCartWord)

	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, un arranque por palabra no lleva coletilla", texts)
	}
}

// TestEvents_TaglineFailureIsAWarningNotASilence (OpeningBuilder.BuildTagline): si la
// coletilla no se puede armar se avisa a WARN y se responde sin ella.
func TestEvents_TaglineFailureIsAWarningNotASilence(t *testing.T) {
	h := eventsCartHarness(t, eventsIntentResolver(), eventsWithOpening(&eventsOpening{taglineErr: errEventsInjected}))

	h.say("wa-1", "quiero pedir algo-zzq")

	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería la respuesta del flujo sin coletilla", texts)
	}
	const warning = "runtime: no se pudo armar la coletilla; se responde sin ella"
	if !eventsLogged(h, "warn", warning) {
		t.Errorf("no se avisó a WARN de la coletilla que no se pudo armar\nlog:\n%s", h.log.dump())
	}
}
