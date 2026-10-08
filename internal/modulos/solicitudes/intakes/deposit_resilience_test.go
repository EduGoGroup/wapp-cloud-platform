package intakes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Lo que el recordatorio de la seña no puede romper: la carrera entre toques, el pánico
// de un colaborador y el cero PII en los logs. Los dobles y el escenario viven en
// deposit_test.go.

// TestRemind_ConcurrentTouchesSendOne: ocho toques que LEYERON LO MISMO y evalúan a la vez.
// Los ocho pasan el pre-filtro —miran la fila que trae el llamante— y solo uno gana la
// marca. Corre con -race en el gate.
func TestRemind_ConcurrentTouchesSendOne(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	touched := []Intake{depositOverdue(depositIntake)}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { sc.reminder.Remind(context.Background(), depositTenant, touched) })
	}
	wg.Wait()

	if got := len(sc.sender.messages()); got != 1 {
		t.Errorf("envíos = %d, quería exactamente 1", got)
	}
	if sc.store.markCount() != 8 {
		t.Errorf("marcas pedidas = %d, quería 8: la garantía es del store, no del pre-filtro", sc.store.markCount())
	}
}

// TestRemind_PanicIsContained: un pánico en el store o en el transporte no sube hasta la
// lectura del dueño, y queda registrado como error.
func TestRemind_PanicIsContained(t *testing.T) {
	t.Parallel()
	inStore := newDepositScene(depositOverdue(depositIntake))
	inStore.store.panicky = true
	inSender := newDepositScene(depositOverdue(depositIntake))
	inSender.sender.panicky = true

	for name, sc := range map[string]*depositScene{"store": inStore, "sender": inSender} {
		sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})
		if !strings.Contains(sc.log.all(), "ERROR") {
			t.Errorf("el pánico del %s no quedó registrado como error:\n%s", name, sc.log.all())
		}
	}
}

// TestDepositReminder_NeverLogsTheDestination: el número del contacto se usa y se suelta.
// Se barren los caminos que emiten log: el envío bueno, el envío fallido y la marca fallida.
func TestDepositReminder_NeverLogsTheDestination(t *testing.T) {
	t.Parallel()
	sent := newDepositScene(depositOverdue(depositIntake))
	offline := newDepositScene(depositOverdue(depositIntake))
	offline.sender.err = errors.New("sesión offline")
	broken := newDepositScene(depositOverdue(depositIntake))
	broken.store.markErr = errors.New("pool agotado")

	for name, sc := range map[string]*depositScene{"sent": sent, "offline": offline, "broken store": broken} {
		sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})
		if sc.log.all() == "" {
			t.Errorf("%s: no se registró nada; el barrido no miraría ningún log", name)
		}
		if strings.Contains(sc.log.all(), depositPhone) {
			t.Errorf("%s: el destino del contacto acabó en el log:\n%s", name, sc.log.all())
		}
	}
}
