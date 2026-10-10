package runtime_test

// welcome_delivery_test.go: la entrega y el sello de la bienvenida (WL-11…WL-13) y lo que la
// bienvenida NO hace (WL-14…WL-16). Parte de welcome_test.go (E-13).

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// TestWelcome_FailedSendIsNotMarkedAndTheNextMessageRetries: WL-11 y WL-13. Si el envío
// falla, la bienvenida NO se marca: el siguiente mensaje del contacto la reintenta. El fallo
// no devuelve error.
func TestWelcome_FailedSendIsNotMarkedAndTheNextMessageRetries(t *testing.T) {
	h, spy := welcomeHarness(t)
	h.sender.FailText(errors.New("edge-down-qzx"))

	h.say("wa-1", "hola-qzx")

	incomingWantLog(t, h, "warn", "runtime: el envío de la bienvenida falló; NO se marca y el próximo mensaje reintenta", 1)
	if _, marks := spy.calls(); len(marks) != 0 {
		t.Fatalf("sellos tras un envío fallido = %d, quería 0", len(marks))
	}

	h.sender.FailText(nil)
	h.say("wa-2", "sigo aquí-qzx")

	incomingWantTexts(t, h, store.DefaultWelcomeText)
	if mark := h.repo.Welcome(h.key()); mark.WelcomedAt.IsZero() {
		t.Errorf("marca = %+v, quería sellada tras el reintento", mark)
	}
}

// welcomeRejectingSender es un Sender cuyo Edge RECHAZA los textos: devuelve el Ack sin error
// pero con ok = false. Apunta lo que se le pidió enviar.
type welcomeRejectingSender struct {
	*runtimehelpertest.Sender
	mu       sync.Mutex
	rejected []string
}

// SendText implementa runtime.Sender.
func (s *welcomeRejectingSender) SendText(_ context.Context, _, _, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = append(s.rejected, text)
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-rejected", Ok: false, Error: "edge-rejected-qzx"}, nil
}

func (s *welcomeRejectingSender) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rejected)
}

// TestWelcome_RejectedByTheEdgeIsNotMarked: WL-11. Se marca SOLO si el Ack vuelve con
// ok = true: un Edge que rechaza la bienvenida no la sella, y el siguiente mensaje reintenta.
func TestWelcome_RejectedByTheEdgeIsNotMarked(t *testing.T) {
	h, spy := welcomeHarness(t)
	rejecting := &welcomeRejectingSender{Sender: h.sender}
	rt, _ := incomingFlakyRuntime(h, rejecting)

	for _, id := range []string{"wa-1", "wa-2"} {
		if err := incomingHandleOn(h, rt, h.incoming(id, "hola-qzx")); err != nil {
			t.Fatalf("HandleIncoming(%s) = %v, quería nil", id, err)
		}
	}

	if got := rejecting.texts(); !slices.Equal(got, []string{store.DefaultWelcomeText, store.DefaultWelcomeText}) {
		t.Errorf("textos pedidos al Edge = %q, quería la bienvenida en los dos mensajes (reintento)", got)
	}
	incomingWantLog(t, h, "warn", "runtime: el Edge rechazó la bienvenida; NO se marca y el próximo mensaje reintenta", 2)
	if _, marks := spy.calls(); len(marks) != 0 {
		t.Errorf("sellos con el Edge rechazando = %d, quería 0", len(marks))
	}
}

// TestWelcome_DeliveredButNotMarked: WL-12 y WL-13. Entregada y la marca falla: ERROR, porque
// el cliente recibirá un duplicado. Entregada y otro turno marcó primero: WARN. En los dos
// casos el turno sigue y no hay error.
func TestWelcome_DeliveredButNotMarked(t *testing.T) {
	cases := []struct {
		name       string
		arrange    func(s *welcomeSpy)
		level, msg string
	}{
		{
			name: "mark fails", level: "error",
			msg:     "runtime: la bienvenida se entregó pero no se pudo marcar; el cliente recibirá un duplicado",
			arrange: func(s *welcomeSpy) { s.markErr = errors.New("welcomes-down-qzx") },
		},
		{
			name: "another turn marked first", level: "warn",
			msg:     "runtime: otro turno marcó la bienvenida primero; este envío fue un duplicado",
			arrange: func(s *welcomeSpy) { s.markLost = true },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, spy := welcomeHarness(t)
			h.seedFlow(incomingStepFlow())
			h.seedRule(incomingKeywordRule(incomingFlowID))
			spy.set(tc.arrange)

			h.say("wa-1", incomingKeyword)

			incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
			incomingWantLog(t, h, tc.level, tc.msg, 1)
			if lines := incomingLogLines(h, "info", "runtime: bienvenida entregada al contacto"); len(lines) != 0 {
				t.Errorf("se anunció como entregada y marcada una bienvenida que no quedó marcada")
			}
		})
	}
}

// TestWelcome_StaysOutOfTheThreadAndTheWindow: WL-14. La bienvenida no entra en el análisis:
// ni en el hilo del evento que nace en ese mismo turno (tampoco marcada como fuera de turno)
// ni en la ventana de captación.
func TestWelcome_StaysOutOfTheThreadAndTheWindow(t *testing.T) {
	w := &incomingWindows{}
	spy := &welcomeSpy{}
	// El espía va DESPUÉS del agregador: incomingWithAggregator quita la bienvenida y aquí se
	// vuelve a cablear.
	h := newHarness(t, incomingWithAggregator(w), welcomeWithSpy(spy))
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID))

	h.say("wa-open", incomingEventKeyword)

	incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
	st := incomingWantNode(t, h, "root")
	rows := threadRows(t, h, st.EventID)
	if len(rows) == 0 {
		t.Fatalf("el hilo del evento quedó vacío: el test no ejercitó la escritura del hilo\nlog:\n%s", h.log.dump())
	}
	for _, row := range rows {
		if row.Text == store.DefaultWelcomeText {
			t.Errorf("la bienvenida entró en el hilo del evento: %+v", row)
		}
	}
	incomingWantWindow(t, h, w, st.EventID, "wa-open")
}

// TestWelcome_DoesNotCountAsAnAutoReply: WL-15. La bienvenida no cobra token del limitador
// —sale aunque no quede cupo— y no suma a la racha de auto-respuestas.
func TestWelcome_DoesNotCountAsAnAutoReply(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedRule(incomingEscapeRule(""))

	h.say("wa-1", incomingKeyword)
	if calls := h.limiter.Calls(); len(calls) != 1 {
		t.Errorf("tokens pedidos = %d, quería 1: el del arranque, ninguno por la bienvenida", len(calls))
	}
	h.say("wa-2", incomingEscapeKeyword)
	if streaks := h.closedStreaks(); !slices.Equal(streaks, []int{1}) {
		t.Errorf("rachas cerradas = %v, quería [1]: la bienvenida no cuenta como auto-respuesta", streaks)
	}

	h.limiter.Limit(0)
	h.seedSettings(func(s *store.TenantSettings) { s.WelcomeSilence = 0 })
	h.say("wa-3", "sin cupo-qzx")
	welcomeWantCount(t, h, 2)
}

// TestWelcome_LogsCarryOnlyOpaqueIDs: WL-16. Los logs de la bienvenida —entregada, fallida—
// no llevan ni el número del contacto ni el texto.
func TestWelcome_LogsCarryOnlyOpaqueIDs(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedSettings(func(s *store.TenantSettings) {
		s.WelcomeText = "Texto privado de bienvenida-qzx"
		s.WelcomeSilence = 0
	})

	h.say("wa-1", "hola-qzx")
	h.sender.FailText(errors.New("edge-down"))
	h.say("wa-2", "otra vez-qzx")

	incomingWantLog(t, h, "info", "runtime: bienvenida entregada al contacto", 1)
	incomingWantLog(t, h, "warn", "runtime: el envío de la bienvenida falló; NO se marca y el próximo mensaje reintenta", 1)
	incomingWantNoLeak(t, h, harnessPhone, "Texto privado de bienvenida-qzx", "hola-qzx", "otra vez-qzx")
}
