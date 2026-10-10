package runtime_test

// resume_retry_test.go prueba, por HandleIncoming y con el PersistSink DE VERDAD cableado, la
// promesa FO-7 del contrato de resume.go: el reintento del sink durable RETOMA DONDE FALLÓ
// (divergencia deliberada del viejo, D-F8-15, hallazgo 34c). Partido de resume_fanout_test.go
// por E-13; los dobles del arnés son los de resume_doubles_test.go.
//
// Como los de resume_fanout_test.go, esperan los 25 ms REALES del contrato (FO-4): a lo sumo 50 ms.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// retryEffectName es una DECISIÓN del cliente (lista cerrada del PersistSink): la única clase
// de efecto que escribe los tres pasos —outbox, hilo y proyección—.
const retryEffectName = "item_added"

// errRetryThread es el fallo del hilo de decisiones (best-effort, sin marca de materialización).
var errRetryThread = errors.New("hilo de decisiones caído-zzq")

// retryOutbox es el outbox del sink: el almacén en memoria del arnés con un fallo pasajero
// delante. Cuenta solo las inserciones del efecto del test (el arnés no emite otro, pero así
// el conteo no depende de eso).
type retryOutbox struct {
	repo      *store.MemoryRepository
	failTimes int
	calls     int
}

func (o *retryOutbox) InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error {
	if ev.Name != retryEffectName {
		return o.repo.InsertFlowEvent(ctx, ev)
	}
	o.calls++
	if o.calls <= o.failTimes {
		return errResumeCause
	}
	return o.repo.InsertFlowEvent(ctx, ev)
}

// retryProjector proyecta solo el efecto del test y falla las primeras failTimes proyecciones.
type retryProjector struct {
	failTimes int
	calls     int
}

func (*retryProjector) Handles(name string) bool { return name == retryEffectName }

func (p *retryProjector) Project(context.Context, modules.EffectMeta, modules.Effect) error {
	p.calls++
	if p.calls <= p.failTimes {
		return errResumeCause
	}
	return nil
}

// retryThread es el hilo de decisiones del sink: cuenta las filas que se le piden y contesta err.
type retryThread struct {
	err   error
	calls int
}

func (th *retryThread) AppendDecision(context.Context, string, []byte) error {
	th.calls++
	return th.err
}

// retryAlways es un número de fallos que el reintento acotado (3 intentos) nunca agota.
const retryAlways = 1 << 20

// retryScenario es UN turno de un módulo durable, dentro de un evento vivo, cuyo único efecto
// pasa por el PersistSink real con esos tres colaboradores.
type retryScenario struct {
	h         *harness
	outbox    *retryOutbox
	projector *retryProjector
	thread    *retryThread
}

// runRetryScenario monta el guion, procesa el entrante y devuelve lo que quedó. outboxFails y
// projectionFails son cuántas veces falla cada paso antes de ceder; threadErr, lo que contesta
// el hilo.
func runRetryScenario(t *testing.T, outboxFails, projectionFails int, threadErr error) retryScenario {
	t.Helper()
	sc := retryScenario{
		outbox:    &retryOutbox{failTimes: outboxFails},
		projector: &retryProjector{failTimes: projectionFails},
		thread:    &retryThread{err: threadErr},
	}
	sc.h = newHarness(t,
		withModules(resumeProbe{durable: true, effects: []modules.Effect{resumeEffect(retryEffectName)}}),
		withSinks(func(h *harness) []runtime.EventSink {
			sc.outbox.repo = h.repo
			return []runtime.EventSink{runtime.NewPersistSink(sc.outbox, sc.projector).WithDecisionThread(sc.thread)}
		}),
	)
	sc.h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	resumeSeedInEvent(sc.h, nil)

	sc.h.say("wa-1", "confirmar")
	return sc
}

// assertWrites comprueba cuántas veces se llamó a cada paso y cuántas filas quedaron en el outbox.
func (sc retryScenario) assertWrites(t *testing.T, outboxCalls, outboxRows, decisions, projections int) {
	t.Helper()
	if sc.outbox.calls != outboxCalls {
		t.Errorf("llamadas a InsertFlowEvent = %d, quería %d", sc.outbox.calls, outboxCalls)
	}
	if got := len(sc.h.flowEvents(retryEffectName)); got != outboxRows {
		t.Errorf("filas de flow_events = %d, quería %d: el reintento no duplica el outbox", got, outboxRows)
	}
	if sc.thread.calls != decisions {
		t.Errorf("decisiones pedidas al hilo = %d, quería %d: el reintento no repite la decisión", sc.thread.calls, decisions)
	}
	if sc.projector.calls != projections {
		t.Errorf("proyecciones = %d, quería %d", sc.projector.calls, projections)
	}
}

// assertNotCut: el turno NO se cortó: sale la respuesta del módulo y el avance quedó guardado.
func (sc retryScenario) assertNotCut(t *testing.T) {
	t.Helper()
	if got := sc.h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería la respuesta normal del módulo\nlog:\n%s", got, sc.h.log.dump())
	}
	if st, _ := sc.h.state(); st.LastWaMessageID != "wa-1" {
		t.Errorf("estado = %+v, quería el avance guardado", st)
	}
	if _, cut := resumeLogLine(sc.h, "error", fanOutCutLog); cut {
		t.Errorf("el turno se cortó y el reintento había cedido\nlog:\n%s", sc.h.log.dump())
	}
}

// FO-7 · Falla la PROYECCIÓN y cede: se reintenta SOLO la proyección. Una fila en el outbox y
// una decisión en el hilo, no dos (D-F8-15: el viejo repetía el Handle entero y las duplicaba).
func TestFanOut_RetryAfterProjectionFailureOnlyProjectsAgain(t *testing.T) {
	sc := runRetryScenario(t, 0, 1, nil)

	sc.assertWrites(t, 1, 1, 1, 2)
	sc.assertNotCut(t)
}

// FO-7 · Falla el OUTBOX y cede: no se había escrito nada, así que se reintenta el Handle entero.
func TestFanOut_RetryAfterOutboxFailureRepeatsTheWholeHandle(t *testing.T) {
	sc := runRetryScenario(t, 1, 0, nil)

	sc.assertWrites(t, 2, 1, 1, 1)
	sc.assertNotCut(t)
}

// FO-7 · El paso se decide EN CADA intento según el ÚLTIMO error: falla el outbox, el Handle
// entero del reintento pasa el outbox y falla en la proyección, y el siguiente intento ya es
// solo proyección.
func TestFanOut_RetryStepFollowsTheLastError(t *testing.T) {
	sc := runRetryScenario(t, 1, 1, nil)

	sc.assertWrites(t, 2, 1, 1, 2)
	sc.assertNotCut(t)
}

// FO-7 / FO-6 · La proyección falla SIEMPRE: tres intentos de proyección, el turno se corta como
// siempre y el outbox y el hilo siguen con UNA escritura.
func TestFanOut_ProjectionThatNeverYieldsCutsWithASingleOutboxRow(t *testing.T) {
	sc := runRetryScenario(t, 0, retryAlways, nil)

	sc.assertWrites(t, 1, 1, 1, 3)
	assertFanOutCut(t, sc.h, errResumeCause)
	var attempts []any
	for _, line := range sc.h.log.at("error") {
		if line.msg == fanOutRetryLog {
			attempts = append(attempts, line.fields["intento"])
		}
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Errorf("reintentos logueados = %v, quería dos líneas con intento 1 y 2", attempts)
	}
}

// FO-7 · El hilo es best-effort también aquí: si falló en el primer intento JUNTO a la
// proyección (los dos errores viajan unidos), el reintento sigue siendo solo proyección y la
// decisión NO se vuelve a pedir.
func TestFanOut_RetryAfterProjectionFailureDoesNotRetryTheThread(t *testing.T) {
	sc := runRetryScenario(t, 0, 1, errRetryThread)

	sc.assertWrites(t, 1, 1, 1, 2)
	sc.assertNotCut(t)
}

// retryHandleOnlySink es un sink que NO sabe reintentar solo la proyección (no tiene
// RetryProjection): el PersistSink real, visto solo por su Handle.
type retryHandleOnlySink struct{ inner *runtime.PersistSink }

func (s retryHandleOnlySink) Handle(ctx context.Context, ec runtime.EffectContext, eff modules.Effect) error {
	return s.inner.Handle(ctx, ec, eff)
}

// FO-7 · Un sink que no ofrece RetryProjection recibe el Handle ENTERO en cada reintento, como
// siempre, aunque su error sea el de una proyección: ahí la fila del outbox sí se repite.
func TestFanOut_SinkWithoutRetryProjectionGetsTheWholeHandle(t *testing.T) {
	projector := &retryProjector{failTimes: 1}
	h := newHarness(t,
		withModules(resumeProbe{durable: true, finish: true, effects: []modules.Effect{resumeEffect(retryEffectName)}}),
		withSinks(func(h *harness) []runtime.EventSink {
			return []runtime.EventSink{retryHandleOnlySink{runtime.NewPersistSink(h.repo, projector)}}
		}),
	)
	h.seedFlow(resumeProbeFlow(resumeProbeScreen))
	resumeSeed(h, nil)

	h.say("wa-1", "confirmar")

	if projector.calls != 2 {
		t.Errorf("proyecciones = %d, quería 2 (el fallo y el reintento que cede)", projector.calls)
	}
	if got := len(h.flowEvents(retryEffectName)); got != 2 {
		t.Errorf("filas de flow_events = %d, quería 2: sin RetryProjection se repite el Handle entero", got)
	}
	assertFanOutCompleted(t, h)
}
