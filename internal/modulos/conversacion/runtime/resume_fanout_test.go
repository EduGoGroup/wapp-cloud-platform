package runtime_test

// resume_fanout_test.go prueba, por HandleIncoming, el FAN-OUT de efectos del contrato de
// resume.go (FO-1…FO-6; FO-7, el reintento que retoma donde falló, está en
// resume_retry_test.go) y el orden por fase que fija New (RT-18, la promesa de event_sink.go
// que quedó anotada para esta ola). Los dobles son los de resume_test.go.
//
// Los tests del reintento esperan los 25 ms REALES del contrato (FO-4): a lo sumo 50 ms.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

const (
	fanOutCutLog   = "runtime: turno cortado: el sink durable no pudo materializar el efecto tras el reintento acotado"
	fanOutRetryLog = "runtime: reintento del sink durable falló"
)

// fanOutEffects son los dos efectos que declara la sonda en estos tests.
func fanOutEffects() []modules.Effect {
	return []modules.Effect{resumeEffect("e1"), resumeEffect("e2")}
}

// fanOutScript monta la sonda (durable o no, que termina el flujo) con esos sinks, procesa UN
// entrante sobre una conversación viva y devuelve el guion. Va SIN evento a propósito: con
// uno, el cierre natural emitiría su efecto de ciclo de vida por los mismos sinks y ya no se
// vería solo el fan-out del módulo.
func fanOutScript(t *testing.T, durable bool, sinks ...runtime.EventSink) *harness {
	t.Helper()
	h := resumeHarness(t, resumeProbe{durable: durable, finish: true, effects: fanOutEffects()}, sinks)
	resumeSeed(h, nil)
	h.say("wa-1", "confirmar")
	return h
}

// assertFanOutCompleted: el turno terminó con normalidad (respuesta del módulo, flujo acabado).
func assertFanOutCompleted(t *testing.T, h *harness) {
	t.Helper()
	if got := h.texts(); !resumeEqual(got, []string{resumeProbeReply}) {
		t.Errorf("textos = %q, quería la respuesta normal del módulo\nlog:\n%s", got, h.log.dump())
	}
	st, _ := h.state()
	if !st.Finished() || st.Outcome() != model.OutcomeCompleted || st.LastWaMessageID != "wa-1" {
		t.Errorf("estado = %+v, quería el flujo terminado y guardado", st)
	}
}

// assertFanOutCut: el turno se cortó (FO-6 y «qué hace quien recibe el corte»): solo el aviso
// de avería, estado sin avanzar, y el error del log es el centinela con su causa.
func assertFanOutCut(t *testing.T, h *harness, cause error) {
	t.Helper()
	if got := h.texts(); !resumeEqual(got, []string{resumeFailureNotice}) {
		t.Errorf("textos = %q, quería SOLO el aviso de avería", got)
	}
	st, _ := h.state()
	if st.CurrentNode != "root" || st.Outcome() == model.OutcomeCompleted || st.LastWaMessageID != "wa-previous" {
		t.Errorf("estado = %+v, un turno cortado no guarda el avance ni alcanza la despedida", st)
	}
	line, ok := resumeLogLine(h, "error", fanOutCutLog)
	if !ok {
		t.Fatalf("falta la línea a ERROR del turno cortado\nlog:\n%s", h.log.dump())
	}
	cut, isErr := line.fields["error"].(error)
	if !isErr || !errors.Is(cut, runtime.ErrTurnCutBySinkFailure) || !errors.Is(cut, cause) {
		t.Errorf("error del corte = %v, quería ErrTurnCutBySinkFailure envolviendo %v", line.fields["error"], cause)
	}
}

// «Qué hace quien recibe el corte» · Un turno cortado no se escribe en el hilo; el mismo turno
// sin corte sí (el literal del cliente y la respuesta).
func TestFanOut_CutTurnLeavesNoThread(t *testing.T) {
	cases := []struct {
		name string
		fail func(int, modules.Effect) error
		want int
	}{
		{"cut", resumeFailAlways(resumePermanentErr()), 0},
		{"not cut", nil, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &resumeSink{name: "a", log: &resumeSinkLog{}, fail: tc.fail}
			h := resumeHarness(t, resumeProbe{durable: true, effects: fanOutEffects()}, []runtime.EventSink{sink})
			h.enableFeature(entitlements.FeatureLLMIntake)
			_, ev := resumeSeedInEvent(h, nil)

			h.say("wa-1", "confirmar")

			if got := resumeThread(h, ev.ID); len(got) != tc.want {
				t.Errorf("hilo = %v, quería %d entradas", got, tc.want)
			}
		})
	}
}

// FO-1 / RT-18 · Cada efecto pasa por cada sink; los de proyección corren antes que el de
// notificación aunque este se registre primero, y dentro de la fase se respeta el registro.
func TestFanOut_EachEffectGoesThroughEachSinkInPhaseOrder(t *testing.T) {
	log := &resumeSinkLog{}
	h := fanOutScript(t, false,
		resumeNotifySink{&resumeSink{name: "notify", log: log}},
		&resumeSink{name: "a", log: log},
		&resumeSink{name: "b", log: log},
	)

	want := []string{"a:e1", "b:e1", "notify:e1", "a:e2", "b:e2", "notify:e2"}
	if got := log.order(); !resumeEqual(got, want) {
		t.Errorf("orden del fan-out = %v, quería %v", got, want)
	}
	assertFanOutCompleted(t, h)
}

// RT-18 · El sink de notificación registrado PRIMERO lee igualmente lo que la proyección
// anotó en el payload (la correlación del intake_id no depende del orden del arranque).
func TestFanOut_NotifySinkRegisteredFirstReadsTheEnrichedPayload(t *testing.T) {
	log := &resumeSinkLog{}
	var seen []string
	notify := resumeNotifySink{&resumeSink{name: "notify", log: log, fail: func(_ int, eff modules.Effect) error {
		id, ok := eff.Payload["intake_id"].(string)
		if !ok {
			id = "(sin intake_id)"
		}
		seen = append(seen, id)
		return nil
	}}}
	project := &resumeSink{name: "project", log: log, fail: func(_ int, eff modules.Effect) error {
		eff.Payload["intake_id"] = "intake-zzq"
		return nil
	}}

	h := fanOutScript(t, false, notify, project)

	assertFanOutCompleted(t, h)

	if !resumeEqual(seen, []string{"intake-zzq", "intake-zzq"}) {
		t.Errorf("el sink de notificación leyó intake_id = %q, quería el que anotó la proyección en los dos efectos", seen)
	}
}

// FO-2 · Best-effort: un sink que falla se loguea y el fan-out sigue con los demás sinks y
// efectos, sin reintento, si falta CUALQUIERA de las dos condiciones de FO-3.
func TestFanOut_FailingSinkIsBestEffort(t *testing.T) {
	cases := []struct {
		name    string
		durable bool
		err     error
	}{
		{"not durable even with materialization error", false, resumeMaterializationErr(errResumeCause)},
		{"durable but the error is not a materialization failure", true, errResumeCause},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &resumeSinkLog{}
			h := fanOutScript(t, tc.durable,
				&resumeSink{name: "a", log: log, fail: resumeFailAlways(tc.err)},
				&resumeSink{name: "b", log: log},
			)

			want := []string{"a:e1", "b:e1", "a:e2", "b:e2"}
			if got := log.order(); !resumeEqual(got, want) {
				t.Errorf("orden del fan-out = %v, quería %v (sin reintentos y sin cortar)", got, want)
			}
			assertFanOutCompleted(t, h)
			line, ok := resumeLogLine(h, "error", "runtime: sink de efecto falló")
			if !ok {
				t.Fatalf("falta la línea a ERROR del sink que falló\nlog:\n%s", h.log.dump())
			}
			if line.fields["kind"] != "persist" || line.fields["name"] != "e1" || line.fields["session_id"] != harnessSession {
				t.Errorf("campos = %v, quería kind, name y session_id del efecto que falló", line.fields)
			}
		})
	}
}

// FO-3 / FO-5 / FO-6 · Durable + materialización transitoria: 3 intentos en total sobre el
// mismo (efecto, sink), cada reintento fallido se loguea con su «intento», y agotado el cupo
// se corta sin despachar lo que quedaba.
func TestFanOut_DurableTransientFailureRetriesThenCuts(t *testing.T) {
	log := &resumeSinkLog{}
	cause := resumeMaterializationErr(errResumeCause)
	h := fanOutScript(t, true,
		&resumeSink{name: "a", log: log, fail: resumeFailAlways(cause)},
		&resumeSink{name: "b", log: log},
	)

	if got := log.order(); !resumeEqual(got, []string{"a:e1", "a:e1", "a:e1"}) {
		t.Errorf("entregas = %v, quería 3 intentos sobre (e1, a) y nada más", got)
	}
	assertFanOutCut(t, h, errResumeCause)
	var attempts []any
	for _, line := range h.log.at("error") {
		if line.msg == fanOutRetryLog {
			attempts = append(attempts, line.fields["intento"])
		}
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Errorf("reintentos logueados = %v, quería dos líneas con intento 1 y 2", attempts)
	}
}

// FO-3 / FO-6 · Un error PERMANENTE de la base corta de inmediato, sin gastar reintentos.
func TestFanOut_PermanentFailureCutsWithoutRetrying(t *testing.T) {
	log := &resumeSinkLog{}
	permanent := resumePermanentErr()
	h := fanOutScript(t, true,
		&resumeSink{name: "a", log: log, fail: resumeFailAlways(permanent)},
		&resumeSink{name: "b", log: log},
	)

	if got := log.order(); !resumeEqual(got, []string{"a:e1"}) {
		t.Errorf("entregas = %v, quería UN intento y nada más", got)
	}
	assertFanOutCut(t, h, permanent)
	if _, retried := resumeLogLine(h, "error", fanOutRetryLog); retried {
		t.Error("se logueó un reintento ante un error permanente")
	}
}

// FO-3 · Un fallo transitorio que cede al reintentar deja el turno completo y el fan-out sigue.
func TestFanOut_TransientFailureThatYieldsCompletesTheTurn(t *testing.T) {
	log := &resumeSinkLog{}
	h := fanOutScript(t, true,
		&resumeSink{name: "a", log: log, fail: resumeFailFirst(1, resumeMaterializationErr(errResumeCause))},
		&resumeSink{name: "b", log: log},
	)

	want := []string{"a:e1", "a:e1", "b:e1", "a:e2", "b:e2"}
	if got := log.order(); !resumeEqual(got, want) {
		t.Errorf("entregas = %v, quería %v", got, want)
	}
	assertFanOutCompleted(t, h)
}

// FO-5 · Un reintento que vuelve SIN ErrMaterializationFailed cuenta como éxito aunque traiga
// otro error: se loguea y el turno no se corta.
func TestFanOut_RetryWithAnotherErrorCountsAsSuccess(t *testing.T) {
	log := &resumeSinkLog{}
	fail := func(attempt int, _ modules.Effect) error {
		switch attempt {
		case 1:
			return resumeMaterializationErr(errResumeCause)
		case 2:
			return errors.New("hilo de decisión caído-zzq")
		default:
			return nil
		}
	}
	h := fanOutScript(t, true,
		&resumeSink{name: "a", log: log, fail: fail},
		&resumeSink{name: "b", log: log},
	)

	want := []string{"a:e1", "a:e1", "b:e1", "a:e2", "b:e2"}
	if got := log.order(); !resumeEqual(got, want) {
		t.Errorf("entregas = %v, quería %v", got, want)
	}
	assertFanOutCompleted(t, h)
	if _, ok := resumeLogLine(h, "error", "runtime: sink de efecto falló tras reintentar (best-effort, no bloquea el turno)"); !ok {
		t.Errorf("falta la línea a ERROR del reintento que dejó un fallo best-effort\nlog:\n%s", h.log.dump())
	}
}

// FO-4 · La espera del reintento respeta el contexto: cancelado, el reintento se abandona con
// el error del contexto y el turno se corta.
func TestFanOut_CancelledContextAbandonsTheRetry(t *testing.T) {
	log := &resumeSinkLog{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sink := &resumeSink{name: "a", log: log, fail: func(int, modules.Effect) error {
		cancel()
		return resumeMaterializationErr(errResumeCause)
	}}
	h := resumeHarness(t, resumeProbe{durable: true, finish: true, effects: fanOutEffects()}, []runtime.EventSink{sink})
	resumeSeed(h, nil)

	if err := h.rt.HandleIncoming(ctx, harnessSession, h.incoming("wa-1", "confirmar")); err != nil {
		t.Fatalf("HandleIncoming = %v, quería el resultado (nil) de enviar el aviso", err)
	}

	if got := log.order(); !resumeEqual(got, []string{"a:e1"}) {
		t.Errorf("entregas = %v, con el contexto cancelado no se reintenta", got)
	}
	assertFanOutCut(t, h, context.Canceled)
}
