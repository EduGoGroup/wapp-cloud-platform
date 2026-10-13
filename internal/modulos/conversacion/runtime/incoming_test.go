package runtime_test

// incoming_test.go trae el montaje común de los tests del camino del entrante (incoming.go) y
// los de su puerta asíncrona, OnIncoming (RT-1, RT-2, RT-3). HandleIncoming va partido por
// tema (E-13): incoming_guards_test.go (lo que pasa antes de tocar la conversación: RT-5,
// RT-6, RT-7), incoming_trigger_test.go (el disparo y el TTL conversacional),
// incoming_advance_test.go (el avance: escape, idempotencia, sueltas, RT-4, RT-10),
// incoming_limiter_test.go (lo que cruza todos los caminos: RT-8, RT-11, RT-19, PII) e
// incoming_aggregation_test.go (AG-7, los tres llamantes de la ventana de captación).
//
// OnIncoming lanza una goroutine con un context.WithTimeout sobre el reloj REAL: sus tests
// corren en una burbuja de testing/synctest, donde ese reloj es falso y solo avanza cuando
// todas las goroutines están paradas. No hay ni una espera de verdad. La excepción es
// TestOnIncoming_SameConversationIsProcessedOneAtATime (incoming_lock_test.go, partido por
// E-13), que va FUERA de la burbuja (la espera de un sync.Mutex no es durable para synctest)
// y observa el candado por su conteo.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Los textos de los guiones del entrante. Llevan un sufijo raro para que una búsqueda en el
// log (PII) no case por accidente con otra cosa.
const (
	incomingFlowID     = "steps-qzx"
	incomingRootPrompt = "Menú raíz-qzx"
	incomingSubPrompt  = "Submenú-qzx"
	incomingLeafText   = "Hoja final-qzx"

	incomingSurveyFlowID = "survey-qzx"
	incomingSurveyFirst  = "Pregunta uno-qzx"
	incomingSurveySecond = "Pregunta dos-qzx"

	incomingKeyword       = "hola"
	incomingEventKeyword  = "carrito"
	incomingSurveyKeyword = "encuesta"
	incomingEscapeKeyword = "salir"
	incomingStopKeyword   = "pausa"

	// Los dos textos fijos de incoming.go, byte a byte (diseno.md §5).
	incomingDefaultEscape     = "Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."
	incomingSinkFailureNotice = "No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."

	incomingOtherPhone = "573004445566"
)

// incomingStepFlow es un flujo de TRES pasos sin contenido durable: menú raíz → submenú →
// mensaje final. Deja avanzar dos veces la misma conversación antes de terminarla.
func incomingStepFlow() model.Flow {
	return model.Flow{
		FlowID:  incomingFlowID,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: incomingRootPrompt, Options: map[string]string{"1": "sub"}},
			"sub":  {Type: model.NodeTypeMenu, Prompt: incomingSubPrompt, Options: map[string]string{"1": "leaf"}},
			"leaf": {Type: model.NodeTypeMessage, Text: incomingLeafText},
		},
	}
}

// incomingSurveyFlow es una encuesta de dos preguntas: contenido DURABLE, así que solo
// arranca dentro de un evento y cada respuesta produce un efecto survey_answer.
func incomingSurveyFlow() model.Flow {
	return model.Flow{
		FlowID:  incomingSurveyFlowID,
		Initial: "q1",
		Nodes: map[string]model.Node{
			"q1":  {Type: model.NodeTypeSurveyQuestion, QuestionID: "q1", Prompt: incomingSurveyFirst, Options: map[string]string{"1": "q2"}},
			"q2":  {Type: model.NodeTypeSurveyQuestion, QuestionID: "q2", Prompt: incomingSurveySecond, Options: map[string]string{"1": "bye"}},
			"bye": {Type: model.NodeTypeMessage, Text: "Gracias-qzx"},
		},
	}
}

// incomingKeywordRule es la palabra clave que arranca el flujo SIN evento (arranque plano).
func incomingKeywordRule(flowID string) trigger.Rule {
	return trigger.Rule{Kind: trigger.KindKeyword, Keyword: incomingKeyword, MatchType: trigger.MatchExact, FlowID: flowID}
}

// incomingEventRule es la regla event_start que pare (o conmuta) un evento de ese tipo.
func incomingEventRule(keyword, kind, flowID string) trigger.Rule {
	return trigger.Rule{Kind: trigger.KindEventStart, Keyword: keyword, MatchType: trigger.MatchExact, FlowID: flowID, EventKind: kind}
}

// incomingEscapeRule es la regla de escape; message vacío deja el aviso por defecto.
func incomingEscapeRule(message string) trigger.Rule {
	return trigger.Rule{Kind: trigger.KindEscape, Keyword: incomingEscapeKeyword, MatchType: trigger.MatchExact, Message: message}
}

// incomingLiveFlat monta una conversación VIVA y sin evento: siembra el flujo de tres pasos y
// su palabra clave, y la arranca con «hola» (wa-open). Queda en el menú raíz.
func incomingLiveFlat(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(incomingEscapeRule(""))
	h.say("wa-open", incomingKeyword)
	incomingWantNode(t, h, "root")
	return h
}

// incomingLiveEvent monta una conversación VIVA dentro de un evento `cart`: siembra el flujo
// de tres pasos y su regla event_start, y la abre con «carrito» (wa-open). Devuelve además el
// id del evento que nació.
func incomingLiveEvent(t *testing.T, opts ...harnessOption) (*harness, string) {
	t.Helper()
	h := newHarness(t, opts...)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))
	h.seedRule(incomingEscapeRule(""))
	h.seedRule(trigger.Rule{Kind: trigger.KindEventStop, Keyword: incomingStopKeyword, MatchType: trigger.MatchExact})
	h.say("wa-open", incomingEventKeyword)
	st := incomingWantNode(t, h, "root")
	if st.EventID == "" {
		t.Fatalf("la regla event_start no dejó evento activo en el estado: %+v\nlog:\n%s", st, h.log.dump())
	}
	return h, st.EventID
}

// incomingWantNode exige que la conversación del guion esté viva en ese nodo y la devuelve.
func incomingWantNode(t *testing.T, h *harness, node string) model.Conversation {
	t.Helper()
	st, found := h.state()
	if !found || st.CurrentNode != node {
		t.Fatalf("estado = (%+v, %v), quería la conversación viva en %q\nlog:\n%s", st, found, node, h.log.dump())
	}
	return st
}

// incomingWantNoState exige que no haya conversación guardada para el cliente del guion.
func incomingWantNoState(t *testing.T, h *harness) {
	t.Helper()
	if st, found := h.state(); found {
		t.Fatalf("quedó estado guardado y no debía: %+v\nlog:\n%s", st, h.log.dump())
	}
}

// incomingWantTexts exige que los textos despachados sean EXACTAMENTE esos, en ese orden.
func incomingWantTexts(t *testing.T, h *harness, want ...string) {
	t.Helper()
	if got := h.texts(); !slices.Equal(got, want) {
		t.Fatalf("textos enviados = %q, quería %q\nlog:\n%s", got, want, h.log.dump())
	}
}

// incomingLogLines devuelve las líneas de ese nivel cuyo mensaje es exactamente msg.
func incomingLogLines(h *harness, level, msg string) []logLine {
	var out []logLine
	for _, line := range h.log.at(level) {
		if line.msg == msg {
			out = append(out, line)
		}
	}
	return out
}

// incomingWantLog exige que el mensaje se haya logueado n veces en ese nivel y devuelve las
// líneas.
func incomingWantLog(t *testing.T, h *harness, level, msg string, n int) []logLine {
	t.Helper()
	lines := incomingLogLines(h, level, msg)
	if len(lines) != n {
		t.Fatalf("líneas a %s con %q = %d, quería %d\nlog:\n%s", level, msg, len(lines), n, h.log.dump())
	}
	return lines
}

// incomingWantFields exige que la línea lleve esas claves con esos valores.
func incomingWantFields(t *testing.T, line logLine, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got, ok := line.fields[key]; !ok || got != value {
			t.Errorf("la línea %q lleva %s = %v (presente: %v), quería %v", line.msg, key, got, ok, value)
		}
	}
}

// incomingWantNoLeak exige que NADA de lo logueado —mensajes, claves o valores— contenga
// ninguno de esos literales (el texto del cliente, su número).
func incomingWantNoLeak(t *testing.T, h *harness, secrets ...string) {
	t.Helper()
	dump := h.log.dump()
	for _, secret := range secrets {
		if strings.Contains(dump, secret) {
			t.Errorf("el log contiene %q y no puede (PII):\n%s", secret, dump)
		}
	}
}

// incomingBlockSends hace que todo envío se quede PARADO dentro del Sender —como un Edge que
// no devuelve el Ack— hasta que se cierre el canal devuelto.
func incomingBlockSends(h *harness) chan struct{} {
	release := make(chan struct{})
	h.sender.OnSend(func(runtimehelpertest.Send) { <-release })
	return release
}

// incomingHook es el gancho de entrantes tal como lo ve el Gateway: la firma, sin error, que
// el arranque le asigna y que él invoca desde el bucle Recv del stream de cada Edge.
type incomingHook interface {
	OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage)
}

// incomingDeliver entrega el entrante por la puerta asíncrona, como el Gateway: a través del
// gancho y por la sesión del guion.
func incomingDeliver(h *harness, m *cloudlinkv1.IncomingMessage) {
	var hook incomingHook = h.rt
	hook.OnIncoming(harnessSession, m)
}

// incomingDeadlineProbe es un IngestDeduper que no deduplica nada: apunta cuánto plazo le
// queda al contexto con que HandleIncoming fue llamado (lo primero que hace es consultarlo).
type incomingDeadlineProbe struct {
	mu        sync.Mutex
	remaining []time.Duration
	unbounded int
}

// Seen implementa runtime.IngestDeduper.
func (p *incomingDeadlineProbe) Seen(ctx context.Context, _, _ string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		p.unbounded++
		return false, nil
	}
	p.remaining = append(p.remaining, time.Until(deadline))
	return false, nil
}

func (p *incomingDeadlineProbe) seen() ([]time.Duration, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.remaining), p.unbounded
}

// TestOnIncoming_ReturnsWhileTheTurnWaitsForTheEdge: RT-1. OnIncoming vuelve aunque el
// procesamiento esté parado esperando el Ack del Edge; si procesara en línea, este test se
// quedaría bloqueado (la burbuja lo delata como deadlock). Al soltar el envío, el turno acaba.
func TestOnIncoming_ReturnsWhileTheTurnWaitsForTheEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedFlow(incomingStepFlow())
		h.seedRule(incomingKeywordRule(incomingFlowID))
		release := incomingBlockSends(h)

		incomingDeliver(h, h.incoming("wa-1", incomingKeyword))
		synctest.Wait()

		if attempts := h.sender.Attempts(); len(attempts) != 1 {
			t.Fatalf("intentos de envío con el Edge mudo = %d, quería 1 (el turno parado en su envío)", len(attempts))
		}
		// RT-4: el estado ya estaba guardado cuando el envío se quedó esperando.
		incomingWantNode(t, h, "root")

		close(release)
		synctest.Wait()
		incomingWantTexts(t, h, incomingRootPrompt)
		if lines := h.log.at("error"); len(lines) != 0 {
			t.Errorf("líneas a error = %+v, no quería ninguna", lines)
		}
	})
}

// TestOnIncoming_BoundsEachIncomingWithItsTimeout: RT-1 y RT-3. HandleIncoming recibe un
// contexto ACOTADO por el plazo del entrante: 30 s por defecto, o el de WithIncomingTimeout.
func TestOnIncoming_BoundsEachIncomingWithItsTimeout(t *testing.T) {
	cases := []struct {
		name    string
		options []runtime.Option
		want    time.Duration
	}{
		{name: "default is thirty seconds", want: 30 * time.Second},
		{name: "configured timeout", options: []runtime.Option{runtime.WithIncomingTimeout(7 * time.Second)}, want: 7 * time.Second},
		{name: "non positive falls back to the default", options: []runtime.Option{runtime.WithIncomingTimeout(-time.Second)}, want: 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probe := &incomingDeadlineProbe{}
				h := newHarness(t, withOptions(func(*harness) []runtime.Option {
					return append([]runtime.Option{runtime.WithIngestDeduper(probe)}, tc.options...)
				}))

				incomingDeliver(h, h.incoming("wa-1", "texto libre"))
				synctest.Wait()

				remaining, unbounded := probe.seen()
				if unbounded != 0 || len(remaining) != 1 || remaining[0] != tc.want {
					t.Fatalf("plazo del contexto = %v (sin plazo: %d), quería una llamada con %v", remaining, unbounded, tc.want)
				}
			})
		})
	}
}

// TestOnIncoming_DropsTheIncomingWhenThePoolIsFull: RT-2. Con el único cupo ocupado por un
// turno parado, el segundo entrante espera su plazo y se DESCARTA: no llega a HandleIncoming,
// se cuenta "saturation" y se avisa a WARN sin PII.
func TestOnIncoming_DropsTheIncomingWhenThePoolIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 5 * time.Second
		saturated := make(chan struct{})
		h := newHarness(t, withOptions(func(h *harness) []runtime.Option {
			return []runtime.Option{
				runtime.WithMaxConcurrentIncoming(1),
				runtime.WithIncomingTimeout(timeout),
				runtime.WithReactiveBlockedHook(func(reason string) {
					h.hooks.blocked(reason)
					if reason == "saturation" {
						close(saturated)
					}
				}),
			}
		}))
		h.seedFlow(incomingStepFlow())
		h.seedRule(incomingKeywordRule(incomingFlowID))
		release := incomingBlockSends(h)

		incomingDeliver(h, h.incoming("wa-holder", incomingKeyword))
		synctest.Wait()
		started := time.Now()
		incomingDeliver(h, h.incomingFrom(incomingOtherPhone, "wa-dropped", "pedido secreto-qzx"))
		<-saturated
		synctest.Wait()

		if waited := time.Since(started); waited != timeout {
			t.Errorf("el descarte llegó a los %v, quería que esperara el plazo del entrante (%v)", waited, timeout)
		}
		assertIncomingWasDropped(t, h, "wa-dropped")

		close(release)
		synctest.Wait()
		incomingWantTexts(t, h, incomingRootPrompt)
	})
}

// assertIncomingWasDropped comprueba las tres caras del descarte por saturación: el motivo
// contado una vez, la línea a WARN con sus dos ids y sin PII, y que el entrante no llegó a
// HandleIncoming (el dedupe, que es lo primero que consulta, no lo vio).
func assertIncomingWasDropped(t *testing.T, h *harness, waMessageID string) {
	t.Helper()
	if reasons := h.blockedReasons(); !slices.Equal(reasons, []string{"saturation"}) {
		t.Errorf("motivos contados = %v, quería [saturation]", reasons)
	}
	lines := incomingWantLog(t, h, "warn", "runtime: entrante descartado por saturación (sin cupo en el pool a tiempo)", 1)
	incomingWantFields(t, lines[0], map[string]any{"session_id": harnessSession, "wa_message_id": waMessageID})
	incomingWantNoLeak(t, h, "pedido secreto-qzx", incomingOtherPhone)
	for _, call := range h.deduper.Calls() {
		if call.WaMessageID == waMessageID {
			t.Errorf("el entrante descartado llegó a HandleIncoming (el dedupe lo vio): %+v", call)
		}
	}
	if attempts := h.sender.Attempts(); len(attempts) != 1 {
		t.Errorf("intentos de envío = %d, quería solo el del turno que ocupaba el cupo", len(attempts))
	}
}

// TestOnIncoming_WithoutSemaphoreNothingIsBoundedNorDropped: RT-2 y RT-3. Con cupo negativo
// no hay semáforo: 65 entrantes —uno más que el cupo por defecto— corren a la vez y, pasado
// el plazo, no se ha descartado ninguno.
func TestOnIncoming_WithoutSemaphoreNothingIsBoundedNorDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			timeout  = 5 * time.Second
			incoming = 65
		)
		h := newHarness(t, withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithMaxConcurrentIncoming(-1), runtime.WithIncomingTimeout(timeout)}
		}))
		h.seedFlow(incomingStepFlow())
		h.seedRule(incomingKeywordRule(incomingFlowID))
		release := incomingBlockSends(h)

		for i := range incoming {
			phone := fmt.Sprintf("57300777%04d", i)
			incomingDeliver(h, h.incomingFrom(phone, fmt.Sprintf("wa-%d", i), incomingKeyword))
		}
		synctest.Wait()
		if attempts := h.sender.Attempts(); len(attempts) != incoming {
			t.Fatalf("turnos corriendo a la vez = %d, quería los %d (sin semáforo no hay techo)", len(attempts), incoming)
		}

		// El reloj falso de la burbuja pasa del plazo: nadie esperaba un cupo, nadie se pierde.
		<-time.After(timeout + time.Second)
		synctest.Wait()
		if reasons := h.blockedReasons(); len(reasons) != 0 {
			t.Errorf("motivos contados = %v, no quería ningún descarte", reasons)
		}

		close(release)
		synctest.Wait()
		if texts := h.texts(); len(texts) != incoming {
			t.Errorf("respuestas despachadas = %d, quería %d", len(texts), incoming)
		}
	})
}

// TestOnIncoming_DefaultPoolHoldsSixtyFour: RT-3. Sin la opción el cupo es 64: de 65
// entrantes parados en su envío, uno se queda esperando sitio.
func TestOnIncoming_DefaultPoolHoldsSixtyFour(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedFlow(incomingStepFlow())
		h.seedRule(incomingKeywordRule(incomingFlowID))
		release := incomingBlockSends(h)

		for i := range 65 {
			phone := fmt.Sprintf("57300888%04d", i)
			incomingDeliver(h, h.incomingFrom(phone, fmt.Sprintf("wa-%d", i), incomingKeyword))
		}
		synctest.Wait()
		if attempts := h.sender.Attempts(); len(attempts) != 64 {
			t.Errorf("turnos corriendo a la vez = %d, quería 64 (el cupo por defecto)", len(attempts))
		}

		close(release)
		synctest.Wait()
		if texts := h.texts(); len(texts) != 65 {
			t.Errorf("respuestas despachadas = %d, quería 65: el que esperaba entra al liberarse un cupo", len(texts))
		}
	})
}

// TestOnIncoming_LogsTheErrorAndNeverPropagatesIt: un error de HandleIncoming no sale de la
// goroutine ni provoca pánico: queda a ERROR con el error y los dos ids.
func TestOnIncoming_LogsTheErrorAndNeverPropagatesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.tenants.Fail(errors.New("tenant-down-qzx"))

		incomingDeliver(h, h.incoming("wa-1", incomingKeyword))
		synctest.Wait()

		lines := incomingWantLog(t, h, "error", "runtime: procesar entrante", 1)
		incomingWantFields(t, lines[0], map[string]any{"session_id": harnessSession, "wa_message_id": "wa-1"})
		if got := fmt.Sprint(lines[0].fields["error"]); !strings.Contains(got, "runtime: resolver tenant") || !strings.Contains(got, "tenant-down-qzx") {
			t.Errorf("clave error = %q, quería el error de HandleIncoming con su causa", got)
		}
		incomingWantTexts(t, h)
	})
}

// incomingHandleOn procesa un entrante por la sesión del guion en el Runtime que se le pase: el
// del arnés o el segundo que monta incomingFlakyRuntime (incoming_doubles_test.go).
func incomingHandleOn(h *harness, rt *runtime.Runtime, m *cloudlinkv1.IncomingMessage) error {
	return rt.HandleIncoming(h.t.Context(), harnessSession, m)
}
