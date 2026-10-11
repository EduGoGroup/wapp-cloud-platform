package runtime_test

// events_summary_failure_test.go prueba las dos promesas de «best-effort» de events_summary.go
// cuando el resumen FALLA: abandonar un evento y volver a él ocurren igual, y el fallo se
// registra en vez de callarse. Las fuentes que fallan son un doble: ningún flujo real las provoca.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// errEventsLinesDown es el fallo de la fuente de líneas del pedido.
var errEventsLinesDown = errors.New("fuente de líneas caída-zzq")

// eventsBrokenLines es una fuente de líneas del pedido que siempre falla.
type eventsBrokenLines struct{}

func (eventsBrokenLines) OpenIntakeLines(context.Context, string, string, string) ([]events.SummaryLine, error) {
	return nil, errEventsLinesDown
}

// eventsWithBrokenLines cablea unas fuentes del resumen en las que leer las líneas falla.
func eventsWithBrokenLines() harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithSummarySources(events.SummarySources{Lines: eventsBrokenLines{}, Answers: eventsAnswers{}})}
	})
}

// TestEvents_AbandonGoesOnWhenTheSummaryFails (EV-8): si el resumen del evento que se abandona
// no se puede escribir, el salto ocurre igual, no queda resumen a medias y el fallo se avisa.
func TestEvents_AbandonGoesOnWhenTheSummaryFails(t *testing.T) {
	h := eventsSwitchHarness(t, eventsWithBrokenLines())
	cart := eventsLiveCartWithLevel(t, h)

	h.say("wa-2", eventsSurveyWord)

	surveyEv := eventsAliveOfKind(t, h, trigger.EventKindSurvey)
	if st, _ := h.state(); st.EventID != surveyEv.ID {
		t.Errorf("estado = %+v, el salto debía ocurrir aunque fallara el resumen", st)
	}
	if sums := eventsSummaries(t, h, cart.ID); len(sums) != 0 {
		t.Errorf("resúmenes = %+v, con la fuente caída no se escribe ninguno", sums)
	}
	if row := eventsRow(t, h, cart.ID); row.Status != events.StatusOpen {
		t.Errorf("el carrito quedó %q, un resumen fallido no lo cierra", row.Status)
	}
	if log := h.log.dump(); !strings.Contains(log, "no se pudo escribir el resumen del evento abandonado") {
		t.Errorf("el fallo del resumen no se avisó en el log:\n%s", log)
	}
}

// TestEvents_ComingBackGoesOnWhenTheReminderFails (EV-3, camino 2): si el recordatorio de lo
// decidido no se puede armar, el cliente vuelve a su evento igual: sale la pantalla del flujo,
// sin recordatorio, y el fallo se avisa.
func TestEvents_ComingBackGoesOnWhenTheReminderFails(t *testing.T) {
	h := eventsSwitchHarness(t, eventsWithBrokenLines())
	h.say("wa-1", eventsCartWord)
	h.say("wa-2", eventsSurveyWord)

	h.say("wa-3", eventsCartWord)

	texts := h.texts()
	if len(texts) != 3 {
		t.Fatalf("textos = %q, quería tres pantallas y ningún recordatorio\nlog:\n%s", texts, h.log.dump())
	}
	if texts[2] != texts[0] {
		t.Errorf("al volver salió %q, quería la pantalla del flujo %q", texts[2], texts[0])
	}
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if st, _ := h.state(); st.EventID != cart.ID {
		t.Errorf("estado = %+v, el cliente debía quedar de vuelta en su pedido", st)
	}
	if log := h.log.dump(); !strings.Contains(log, "no se pudo armar el resumen del evento rescatado") {
		t.Errorf("el fallo del recordatorio no se avisó en el log:\n%s", log)
	}
}

// TestEvents_ComingBackWithoutSourcesSendsNoReminder (EV-3, no-regresión): sin fuentes del
// resumen cableadas, volver a un evento solo enseña la pantalla del flujo.
func TestEvents_ComingBackWithoutSourcesSendsNoReminder(t *testing.T) {
	h := eventsSwitchHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithSummarySources(events.SummarySources{})}
	}))
	h.say("wa-1", eventsCartWord)
	h.say("wa-2", eventsSurveyWord)

	h.say("wa-3", eventsCartWord)

	if texts := h.texts(); len(texts) != 3 || texts[2] != texts[0] {
		t.Errorf("textos = %q, quería tres pantallas y la última igual a la primera", texts)
	}
}

// TestEvents_ComingBackGoesOnWhenTheReminderCannotBeSent (EV-3, camino 2): si el recordatorio
// se arma pero su ENVÍO falla, el cliente vuelve a su evento igual: sale la pantalla del
// flujo, el fallo se avisa y el hilo no cuenta un recordatorio que el cliente no tiene delante.
func TestEvents_ComingBackGoesOnWhenTheReminderCannotBeSent(t *testing.T) {
	h := eventsSwitchHarness(t, eventsWithCartLines(), threadWithoutWelcome(), threadWithFeature())
	h.say("wa-1", eventsCartWord)
	h.say("wa-2", eventsSurveyWord)
	// Falla SOLO el primer envío del turno, que es el recordatorio; el siguiente ya pasa.
	h.sender.FailText(errEventsInjected)
	h.sender.OnSend(func(runtimehelpertest.Send) { h.sender.FailText(nil) })

	err := h.handle(h.incoming("wa-3", eventsCartWord))

	if err != nil {
		t.Fatalf("HandleIncoming = %v, un recordatorio que no sale no tumba el rescate\nlog:\n%s", err, h.log.dump())
	}
	attempts := h.sender.Attempts()
	if len(attempts) != 4 || !errors.Is(attempts[2].Err, errEventsInjected) || !strings.Contains(attempts[2].Text, eventsLineLabel) {
		t.Fatalf("intentos = %+v, quería cuatro y el tercero —el recordatorio— fallido", attempts)
	}
	texts := h.texts()
	if len(texts) != 3 || texts[2] != texts[0] {
		t.Errorf("textos = %q, quería tres pantallas y la última la del flujo del pedido", texts)
	}
	for _, text := range texts {
		if strings.Contains(text, eventsLineLabel) {
			t.Errorf("se despachó %q, el recordatorio no debía llegar a salir", text)
		}
	}
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if st, _ := h.state(); st.EventID != cart.ID || st.FlowID != eventsCartFlow {
		t.Errorf("estado = %+v, el cliente debía quedar de vuelta en su pedido", st)
	}
	const notSent = "runtime: no se pudo enviar el resumen del rescate; se retoma igual"
	if !eventsLogged(h, "warn", notSent) {
		t.Errorf("no se avisó a WARN de que el recordatorio no salió\nlog:\n%s", h.log.dump())
	}
	for _, row := range threadRows(t, h, cart.ID) {
		if row.Kind == events.KindMessageOutOfTurn {
			t.Errorf("hilo: fila %d fuera de turno %q; un resumen que no llegó no es antecedente de nada", row.Seq, row.Text)
		}
	}
}
