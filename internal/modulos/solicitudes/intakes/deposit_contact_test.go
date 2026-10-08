//go:build pendiente

package intakes

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// El toque del CLIENTE (RemindContact). Los dobles y el escenario viven en
// deposit_test.go; el texto esperado es el literal calculado con el fichero viejo.

// TestRemindContact_ReturnsTheTextsItSent: el toque del cliente. Le pregunta al store por
// las señas vencidas de ESE contacto con el instante del reloj y un límite de 1, y devuelve
// exactamente el texto que entregó al envío.
func TestRemindContact_ReturnsTheTextsItSent(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.pending = []Intake{depositOverdue(depositIntake)}

	got := sc.reminder.RemindContact(context.Background(), depositTenant, depositContact)

	if !slices.Equal(got, []string{depositReminderText}) {
		t.Fatalf("textos devueltos = %q, quería el recordatorio", got)
	}
	msgs := sc.sender.messages()
	if len(msgs) != 1 || msgs[0].text != got[0] {
		t.Errorf("envíos = %+v, quería uno con el mismo texto que se devolvió", msgs)
	}
	wantCall := depositTenant + "|" + depositContact + "|2026-08-06T15:00:00Z|1"
	if !slices.Equal(sc.store.pendingCalls, []string{wantCall}) {
		t.Errorf("consultas de pendientes = %v, quería [%s]", sc.store.pendingCalls, wantCall)
	}
	wantMark := []depositMark{{tenantID: depositTenant, intakeID: depositIntake, at: depositNow}}
	if !slices.Equal(sc.store.marks, wantMark) {
		t.Errorf("marcas pedidas = %+v, quería %+v", sc.store.marks, wantMark)
	}
}

// TestRemindContact_NothingSentIsNil: sin pendientes, sin plantilla o perdiendo la marca no
// se mandó nada, y eso se dice con nil.
func TestRemindContact_NothingSentIsNil(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		arrange func(sc *depositScene)
	}{
		{name: "nothing pending", arrange: func(sc *depositScene) { sc.store.pending = nil }},
		{name: "tenant without a template", arrange: func(sc *depositScene) { sc.settings.template = "" }},
		{name: "the mark was lost", arrange: func(sc *depositScene) { sc.store.lose[depositIntake] = true }},
		{name: "the mark failed", arrange: func(sc *depositScene) { sc.store.markErr = errors.New("pool agotado") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sc := newDepositScene(depositOverdue(depositIntake))
			sc.store.pending = []Intake{depositOverdue(depositIntake)}
			c.arrange(sc)

			if got := sc.reminder.RemindContact(context.Background(), depositTenant, depositContact); got != nil {
				t.Errorf("RemindContact = %q, quería nil", got)
			}
			if len(sc.sender.messages()) != 0 {
				t.Errorf("envíos = %d, quería 0", len(sc.sender.messages()))
			}
		})
	}
}

// TestRemindContact_EmptyIdentifiersDoNotAsk: sin tenant o sin contacto no hay a quién
// buscarle señas, y el store ni se toca.
func TestRemindContact_EmptyIdentifiersDoNotAsk(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.pending = []Intake{depositOverdue(depositIntake)}

	if got := sc.reminder.RemindContact(context.Background(), "", depositContact); got != nil {
		t.Errorf("sin tenant RemindContact = %q, quería nil", got)
	}
	if got := sc.reminder.RemindContact(context.Background(), depositTenant, ""); got != nil {
		t.Errorf("sin contacto RemindContact = %q, quería nil", got)
	}
	if len(sc.store.pendingCalls) != 0 || sc.store.markCount() != 0 {
		t.Errorf("el store recibió %d consultas y %d marcas, quería 0 y 0", len(sc.store.pendingCalls), sc.store.markCount())
	}
}

// TestRemindContact_QueryFailureIsNilAndLogged: si la consulta falla no se manda nada, queda
// registrado como error y el mensaje entrante del cliente sigue su curso.
func TestRemindContact_QueryFailureIsNilAndLogged(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.pendingErr = errors.New("pool agotado")

	if got := sc.reminder.RemindContact(context.Background(), depositTenant, depositContact); got != nil {
		t.Errorf("RemindContact = %q, quería nil", got)
	}
	if !strings.Contains(sc.log.all(), "ERROR") {
		t.Errorf("el fallo de la consulta no quedó registrado como error:\n%s", sc.log.all())
	}
	if strings.Contains(sc.log.all(), depositPhone) {
		t.Errorf("el destino del contacto acabó en el log:\n%s", sc.log.all())
	}
}

// TestRemindContact_FailedDeliveryStillReturnsTheText: la marca se ganó y la entrega se
// intentó; aunque el envío falle, ese recordatorio ocurrió y su texto vuelve.
func TestRemindContact_FailedDeliveryStillReturnsTheText(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.pending = []Intake{depositOverdue(depositIntake)}
	sc.sender.err = errors.New("sesión offline")

	got := sc.reminder.RemindContact(context.Background(), depositTenant, depositContact)

	if !slices.Equal(got, []string{depositReminderText}) {
		t.Errorf("textos devueltos = %q, quería el recordatorio aunque el envío fallara", got)
	}
}

// TestRemindContact_PanicReturnsNil: ante un pánico contenido vuelve nil y NO lo acumulado.
// Una lista parcial de recordatorios de los que no se sabe si salieron sería inventar rastro
// en el hilo.
func TestRemindContact_PanicReturnsNil(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.pending = []Intake{depositOverdue(depositIntake)}
	sc.sender.panicky = true

	if got := sc.reminder.RemindContact(context.Background(), depositTenant, depositContact); got != nil {
		t.Errorf("RemindContact = %q, quería nil tras el pánico", got)
	}
	if !strings.Contains(sc.log.all(), "ERROR") {
		t.Errorf("el pánico no quedó registrado como error:\n%s", sc.log.all())
	}
}
