package runtime_test

// thread_test.go: los productores de filas de TEXTO LITERAL del hilo del evento (thread.go,
// TH-1…TH-18; RT-20). thread.go no exporta nada: sus promesas se prueban por HandleIncoming y
// por Start, leyendo el hilo del doble de eventos (rol, grado y texto de cada fila).

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// threadLine es una fila del hilo sin su número: de quién es, de qué grado y qué dice.
type threadLine struct {
	role events.Role
	kind events.EntryKind
	text string
}

// threadClient es una fila del turno con lo que escribió el cliente.
func threadClient(text string) threadLine {
	return threadLine{role: events.RoleClient, kind: events.KindMessage, text: text}
}

// threadBusiness es una fila del turno con lo que contestó el negocio.
func threadBusiness(text string) threadLine {
	return threadLine{role: events.RoleBusiness, kind: events.KindMessage, text: text}
}

// threadOutOfTurn es un saliente fuera de turno: voz del negocio, MARCADO.
func threadOutOfTurn(text string) threadLine {
	return threadLine{role: events.RoleBusiness, kind: events.KindMessageOutOfTurn, text: text}
}

// threadRows lee el hilo entero del evento, con el texto resuelto.
func threadRows(t *testing.T, h *harness, eventID string) []events.ThreadEntry {
	t.Helper()
	rows, err := h.events.ListThread(t.Context(), eventID, 1000)
	if err != nil {
		t.Fatalf("leer el hilo del evento %s: %v", eventID, err)
	}
	return rows
}

// threadWant exige que el hilo del evento sea EXACTAMENTE esas filas, en ese orden.
func threadWant(t *testing.T, h *harness, eventID string, want ...threadLine) {
	t.Helper()
	rows := threadRows(t, h, eventID)
	got := make([]threadLine, 0, len(rows))
	for _, row := range rows {
		got = append(got, threadLine{role: row.Role, kind: row.Kind, text: row.Text})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("hilo del evento = %+v\nquería            %+v\nlog:\n%s", got, want, h.log.dump())
	}
}

// threadWithoutWelcome quita la bienvenida, que cuelga de la misma feature que el hilo y
// metería un saliente más en cada guion.
func threadWithoutWelcome() harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithWelcomeStore(nil)}
	})
}

// threadLiveEvent abre, con `llm_intake` encendida y sin bienvenida, una conversación dentro
// de un evento `cart` sobre el flujo de tres pasos. Devuelve el arnés y el id del evento.
func threadLiveEvent(t *testing.T, opts ...harnessOption) (*harness, string) {
	t.Helper()
	h, eventID := incomingLiveEvent(t, append([]harnessOption{threadWithoutWelcome(), threadWithFeature()}, opts...)...)
	return h, eventID
}

// threadWithFeature enciende `llm_intake` ANTES de que el guion procese su primer entrante.
func threadWithFeature() harnessOption {
	return withOptions(func(h *harness) []runtime.Option {
		h.enableFeature(entitlements.FeatureLLMIntake)
		return nil
	})
}

// threadOpening son las dos filas que deja el turno que abre el evento del guion.
func threadOpening() []threadLine {
	return []threadLine{threadClient(incomingEventKeyword), threadBusiness(incomingRootPrompt)}
}

// TestThread_TurnsAreWrittenInConversationOrder: TH-5, TH-11 y TH-14. El mensaje que ABRE el
// evento deja su literal seguido de las salidas del arranque, y cada avance deja el texto del
// cliente y después la respuesta del negocio. Ningún literal aparece dos veces.
func TestThread_TurnsAreWrittenInConversationOrder(t *testing.T) {
	h, eventID := threadLiveEvent(t)
	opening := h.texts()
	if len(opening) != 1 {
		t.Fatalf("textos del arranque = %q, quería uno", opening)
	}
	// La fila del negocio es lo que SALIÓ, coletilla incluida si la hubiera.
	threadWant(t, h, eventID, threadClient(incomingEventKeyword), threadBusiness(opening[0]))

	h.say("wa-2", "1")

	threadWant(t, h, eventID, append(threadOpening(), threadClient("1"), threadBusiness(incomingSubPrompt))...)
}

// TestThread_Gate_NeedsFeatureAndResolver: TH-1, TH-4 y RT-20. Con conversación y evento
// vivos, el hilo solo se escribe si hay resolver de features y el tenant tiene `llm_intake`.
// El turno y el saliente fuera de turno comparten gate: se persisten, o no, en bloque.
func TestThread_Gate_NeedsFeatureAndResolver(t *testing.T) {
	cases := []struct {
		name    string
		options []harnessOption
		written bool
	}{
		{name: "feature and resolver", options: []harnessOption{threadWithFeature()}, written: true},
		{name: "without the feature"},
		{name: "without the resolver", options: []harnessOption{threadWithFeature(), withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithEntitlements(nil)}
		})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, eventID := incomingLiveEvent(t, append([]harnessOption{threadWithoutWelcome()}, tc.options...)...)
			h.deposits.SetTexts("Recuerda tu seña-qzx")

			h.say("wa-2", "1")

			incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
			if !tc.written {
				threadWant(t, h, eventID)
				return
			}
			threadWant(t, h, eventID, append(threadOpening(),
				threadClient("1"), threadBusiness(incomingSubPrompt), threadOutOfTurn("Recuerda tu seña-qzx"))...)
		})
	}
}

// TestThread_Gate_NeedsAnEvent: TH-1 y TH-12. La feature no basta: el literal solo vive
// DENTRO de un evento. Ni una conversación plana (keyword), ni un Start por API, ni un motor
// sin plano de eventos escriben una fila; tampoco en el hilo de un evento vivo del contacto
// que no es el del turno.
func TestThread_Gate_NeedsAnEvent(t *testing.T) {
	t.Run("plain conversation next to an alive event", func(t *testing.T) {
		h := newHarness(t, threadWithoutWelcome(), threadWithFeature())
		version := h.seedFlow(incomingStepFlow())
		h.seedRule(incomingKeywordRule(incomingFlowID))
		bystander := h.seedEvent(trigger.EventKindSurvey, incomingFlowID, version)

		h.say("wa-1", incomingKeyword)
		h.say("wa-2", "1")

		incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
		threadWant(t, h, bystander.ID)
	})
	t.Run("start by API", func(t *testing.T) {
		h := newHarness(t, threadWithoutWelcome(), threadWithFeature())
		version := h.seedFlow(incomingStepFlow())
		bystander := h.seedEvent(trigger.EventKindSurvey, incomingFlowID, version)
		ref := contact.RefsFrom(harnessPhone, "", jid(harnessPhone))[0]

		if _, err := h.rt.Start(t.Context(), harnessTenant, incomingFlowID, harnessSession, ref); err != nil {
			t.Fatalf("Start por API = %v", err)
		}

		incomingWantTexts(t, h, incomingRootPrompt)
		threadWant(t, h, bystander.ID)
	})
	t.Run("without event plane", func(t *testing.T) {
		h := newHarness(t, threadWithoutWelcome(), threadWithFeature(), withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithEventStore(nil)}
		}))
		h.seedFlow(incomingStepFlow())
		h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))

		h.say("wa-1", incomingEventKeyword)
		h.say("wa-2", "1")

		incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
		if evs := h.events.Events(harnessTenant); len(evs) != 0 {
			t.Errorf("eventos = %+v, sin plano de eventos no quería ninguno ni hilo que escribir", evs)
		}
	})
}

// TestThread_Gate_ResolverErrorIsFailClosed: TH-2. Si el resolver falla no se escribe ni una
// fila y se avisa a WARN: un fallo transitorio no abre una capacidad de pago. El turno sigue.
func TestThread_Gate_ResolverErrorIsFailClosed(t *testing.T) {
	h, eventID := threadLiveEvent(t)
	h.features.Err = errors.New("entitlements-down-qzx")

	h.say("wa-2", "1")

	incomingWantTexts(t, h, incomingRootPrompt, incomingSubPrompt)
	threadWant(t, h, eventID, threadOpening()...)
	lines := incomingWantLog(t, h, "warn", "runtime: no se pudo resolver la feature llm_intake; no se escribe en el hilo", 1)
	incomingWantFields(t, lines[0], map[string]any{"tenant_id": harnessTenant, "session_id": harnessSession})
}

// TestThread_Gate_DoesNotRequireTheAPIRoute: TH-3. «Una sola condición» incluye a `api_llm`:
// un tenant con `llm_intake` y la vía API apagada archiva su hilo igual.
func TestThread_Gate_DoesNotRequireTheAPIRoute(t *testing.T) {
	h, eventID := threadLiveEvent(t, withOptions(func(h *harness) []runtime.Option {
		h.features.Disable(harnessTenant, entitlements.FeatureAPILLM)
		return nil
	}))

	h.say("wa-2", "1")

	threadWant(t, h, eventID, append(threadOpening(), threadClient("1"), threadBusiness(incomingSubPrompt))...)
}

// TestThread_Gate_IsResolvedOncePerCall: TH-4. El gate se resuelve UNA vez por llamada, no
// por cadena: un turno con dos filas es una pregunta, y un saliente fuera de turno con dos
// textos es otra.
func TestThread_Gate_IsResolvedOncePerCall(t *testing.T) {
	var counting *incomingCountingFeatures
	h, eventID := threadLiveEvent(t, withOptions(func(h *harness) []runtime.Option {
		counting = &incomingCountingFeatures{Resolver: h.features}
		return []runtime.Option{runtime.WithEntitlements(counting)}
	}))
	h.deposits.SetTexts("Recuerda tu seña-qzx", "Vence mañana-qzx")
	before := counting.asked()

	h.say("wa-2", "1")

	threadWant(t, h, eventID, append(threadOpening(),
		threadClient("1"), threadBusiness(incomingSubPrompt),
		threadOutOfTurn("Recuerda tu seña-qzx"), threadOutOfTurn("Vence mañana-qzx"))...)
	if got := counting.asked() - before; got != 2 {
		t.Errorf("preguntas al resolver en el turno = %d, quería 2: una por el turno y una por el saliente fuera de turno", got)
	}
}

// TestThread_EmptyTextLeavesNoRow: TH-6. Un adjunto sin caption no tiene literal: el turno
// deja la respuesta del negocio y ninguna fila del cliente.
func TestThread_EmptyTextLeavesNoRow(t *testing.T) {
	h, eventID := threadLiveEvent(t)

	h.say("wa-2", "")

	texts := h.texts()
	if len(texts) != 2 {
		t.Fatalf("textos enviados = %q, quería la pantalla y la respuesta al mensaje sin texto", texts)
	}
	threadWant(t, h, eventID, append(threadOpening(), threadBusiness(texts[1]))...)
}

// TestThread_RecordsWhatTheEngineProducedNotTheDelivery: TH-7. El turno se persiste después
// del Save y antes del envío: queda en el hilo aunque el limitador calle la respuesta o el
// envío falle.
func TestThread_RecordsWhatTheEngineProducedNotTheDelivery(t *testing.T) {
	want := append(threadOpening(), threadClient("1"), threadBusiness(incomingSubPrompt))
	t.Run("limiter silences the reply", func(t *testing.T) {
		h, eventID := threadLiveEvent(t)
		h.limiter.Limit(1) // el nacimiento del evento ya gastó el único token.

		h.say("wa-2", "1")

		incomingWantTexts(t, h, incomingRootPrompt)
		threadWant(t, h, eventID, want...)
	})
	t.Run("send fails", func(t *testing.T) {
		h, eventID := threadLiveEvent(t)
		boom := errors.New("edge-down-qzx")
		h.sender.FailText(boom)

		if err := h.handle(h.incoming("wa-2", "1")); !errors.Is(err, boom) {
			t.Fatalf("HandleIncoming = %v, quería el error del envío", err)
		}

		threadWant(t, h, eventID, want...)
	})
}

// TestThread_TheTurnThatEndsTheFlowBelongsToItsEvent: TH-8. El evento del turno se captura
// ANTES del cierre natural: el turno que termina el flujo queda en el hilo del evento que
// acaba de cerrarse.
func TestThread_TheTurnThatEndsTheFlowBelongsToItsEvent(t *testing.T) {
	h := newHarness(t, threadWithoutWelcome(), threadWithFeature())
	flow := menuFlow("closing-zzq")
	h.seedFlow(flow)
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, flow.FlowID))
	h.say("wa-1", incomingEventKeyword)
	st := incomingWantNode(t, h, flow.Initial)
	eventID := st.EventID

	h.say("wa-2", "1")

	evs := h.events.Events(harnessTenant)
	if len(evs) != 1 || evs[0].Status == events.StatusOpen {
		t.Fatalf("eventos = %+v, quería el evento del flujo ya cerrado por su fin natural", evs)
	}
	threadWant(t, h, eventID,
		threadClient(incomingEventKeyword), threadBusiness("Elige una opción-zzq"),
		threadClient("1"), threadBusiness("Elegiste la primera-zzq"))
}

// TestThread_ACutTurnLeavesNoRow: TH-9. Un turno cortado por el sink durable no deja ni una
// fila: ni el literal del cliente ni el aviso de avería.
func TestThread_ACutTurnLeavesNoRow(t *testing.T) {
	h, sink, eventID := incomingLiveSurvey(t, threadWithoutWelcome(), threadWithFeature())
	opening := []threadLine{threadClient(incomingSurveyKeyword), threadBusiness(incomingSurveyFirst)}
	threadWant(t, h, eventID, opening...)
	sink.set(true)

	h.say("wa-2", "1")

	incomingWantTexts(t, h, incomingSurveyFirst, incomingSinkFailureNotice)
	threadWant(t, h, eventID, opening...)
}

// TestThread_WriteFailuresNeverBreakTheTurn: TH-10 y TH-18. El hilo es best-effort: un almacén
// que no puede escribir texto deja un WARN por fila —el fallo de una no impide intentar la
// siguiente— y el turno contesta igual. Lo mismo el saliente fuera de turno.
func TestThread_WriteFailuresNeverBreakTheTurn(t *testing.T) {
	const (
		clientFailed    = "runtime: no se pudo escribir el mensaje del cliente en el hilo; el turno sigue"
		businessFailed  = "runtime: no se pudo escribir la respuesta del negocio en el hilo; el turno sigue"
		outOfTurnFailed = "runtime: no se pudo escribir el saliente fuera de turno en el hilo; el envío ya salió"
	)
	h, eventID := threadLiveEvent(t, withOptions(func(h *harness) []runtime.Option {
		return []runtime.Option{runtime.WithEventStore(h.events.WithoutCipher())}
	}))
	incomingWantLog(t, h, "warn", clientFailed, 1)
	incomingWantLog(t, h, "warn", businessFailed, 1)

	h.say("wa-2", "1")
	h.say("wa-3", incomingStopKeyword)

	incomingWantLog(t, h, "warn", clientFailed, 2)
	incomingWantLog(t, h, "warn", businessFailed, 2)
	incomingWantLog(t, h, "warn", outOfTurnFailed, 1)
	if texts := h.texts(); len(texts) != 3 || texts[1] != incomingSubPrompt {
		t.Errorf("textos enviados = %q, quería las tres respuestas aunque el hilo no se pudiera escribir", texts)
	}
	threadWant(t, h, eventID)
}

// TestThread_SwitchWritesTheLiteralButNotTheOutputs: TH-13. La CONMUTA hacia un evento que ya
// estaba vivo, traída por el disparador, escribe el literal del cliente y NO las salidas: el
// hilo de ese evento ya tiene su pantalla inicial.
func TestThread_SwitchWritesTheLiteralButNotTheOutputs(t *testing.T) {
	h := newHarness(t, threadWithoutWelcome(), threadWithFeature())
	version := h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))
	alive := h.seedEvent(trigger.EventKindCart, incomingFlowID, version)

	h.say("wa-1", incomingEventKeyword)

	if st := incomingWantNode(t, h, "root"); st.EventID != alive.ID {
		t.Fatalf("evento activo = %q, quería la conmuta hacia el que ya vivía (%s)", st.EventID, alive.ID)
	}
	if texts := h.texts(); len(texts) == 0 {
		t.Fatal("la conmuta no contestó nada: el test no ejercitó las salidas que no deben entrar al hilo")
	}
	threadWant(t, h, alive.ID, threadClient(incomingEventKeyword))
}

// TestThread_OutOfTurnMessagesAreMarked: TH-15 y TH-16. Lo que la plataforma dice sin que
// nazca de un turno entra MARCADO en el hilo del evento: la confirmación de un event_stop, el
// aviso de escape y el reinicio por reanudación (aviso y pantalla inicial).
func TestThread_OutOfTurnMessagesAreMarked(t *testing.T) {
	t.Run("event_stop confirmation", func(t *testing.T) {
		h, eventID := threadLiveEvent(t)

		h.say("wa-2", incomingStopKeyword)

		texts := h.texts()
		if len(texts) != 2 {
			t.Fatalf("textos enviados = %q, quería la pantalla y la confirmación del event_stop", texts)
		}
		threadWant(t, h, eventID, append(threadOpening(), threadOutOfTurn(texts[1]))...)
	})
	t.Run("escape notice", func(t *testing.T) {
		h, eventID := threadLiveEvent(t)

		h.say("wa-2", incomingEscapeKeyword)

		threadWant(t, h, eventID, append(threadOpening(), threadOutOfTurn(incomingDefaultEscape))...)
	})
	t.Run("restart by resume", func(t *testing.T) {
		policy := &incomingRestartPolicy{notice: incomingRestartNotice}
		h, eventID := threadLiveEvent(t, incomingWithRestartPolicy(policy))
		policy.arm()

		h.say("wa-2", "1")

		incomingWantTexts(t, h, incomingRootPrompt, incomingRestartNotice, incomingRootPrompt)
		threadWant(t, h, eventID, append(threadOpening(),
			threadClient("1"), threadOutOfTurn(incomingRestartNotice), threadOutOfTurn(incomingRootPrompt))...)
	})
}

// TestThread_DepositReminder_IsWrittenAfterTheTurnRow: TH-15, TH-17 y RT-19. Los textos del
// recordatorio de la seña entran MARCADOS, detrás de las filas del turno, y las cadenas
// vacías se saltan.
func TestThread_DepositReminder_IsWrittenAfterTheTurnRow(t *testing.T) {
	h, eventID := threadLiveEvent(t)
	h.deposits.SetTexts("", "Recuerda tu seña-qzx")

	h.say("wa-2", "1")

	threadWant(t, h, eventID, append(threadOpening(),
		threadClient("1"), threadBusiness(incomingSubPrompt), threadOutOfTurn("Recuerda tu seña-qzx"))...)
}

// TestThread_DepositReminder_NeedsATurnOverAnActiveEvent: TH-17. El recordatorio solo se
// escribe si el turno AVANZÓ sobre un evento que seguía activo: ni en el mensaje que abre la
// conversación (aún no había evento) ni cuando el reloj la soltó en ese mismo turno.
func TestThread_DepositReminder_NeedsATurnOverAnActiveEvent(t *testing.T) {
	h := newHarness(t, threadWithoutWelcome(), threadWithFeature())
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))
	h.seedSettings(func(s *store.TenantSettings) { s.EventInactivityTTL = time.Hour })
	h.deposits.SetTexts("Recuerda tu seña-qzx")

	h.say("wa-1", incomingEventKeyword)
	eventID := incomingWantNode(t, h, "root").EventID
	threadWant(t, h, eventID, threadOpening()...)

	h.clock.Advance(3 * time.Hour)
	h.say("wa-2", "texto tras el silencio-qzx")

	incomingWantNoState(t, h)
	threadWant(t, h, eventID, threadOpening()...)
	if calls := h.deposits.Calls(); len(calls) != 2 {
		t.Errorf("toques del recordatorio = %d, quería 2: se evalúa igual, lo que no hay es fila", len(calls))
	}
}
