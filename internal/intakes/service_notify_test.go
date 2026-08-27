package intakes_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

const intakeDePrueba = "11111111-1111-1111-1111-111111111111"

// avisoSpy cuenta los avisos y con qué transición se pidieron. Es el instrumento de
// «como mucho un mensaje por transición»: lo que se cuenta aquí es lo que llega a un
// teléfono.
type avisoSpy struct {
	mu     sync.Mutex
	avisos []string // "from→to"
}

func (a *avisoSpy) NotifyStatus(_ context.Context, _ string, in intakes.Intake, from string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.avisos = append(a.avisos, from+"→"+in.Status)
}

func (a *avisoSpy) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.avisos)
}

// conflictStore es un Store que aplica el compare-and-swap PERDIDO: el estado se
// leyó bien pero, al ir a escribir, otro operador ya se había adelantado. Imita lo
// que devuelve el store Postgres en esa carrera.
type conflictStore struct{ *intakes.MemoryStore }

func (conflictStore) UpdateStatus(context.Context, string, string, string, []string) (intakes.Intake, error) {
	return intakes.Intake{}, intakes.ErrConflict
}

// TestService_NotificaUnaVezPorTransición: una transición aplicada ⇒ exactamente un
// aviso, con los dos extremos correctos.
func TestService_NotificaUnaVezPorTransición(t *testing.T) {
	spy := &avisoSpy{}
	svc := intakes.NewService(seedStore(t, intakes.StatusConfirmed), intakes.WithNotifier(spy))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	if spy.count() != 1 {
		t.Fatalf("avisos = %d, quiero exactamente 1", spy.count())
	}
	if got := spy.avisos[0]; got != "confirmed→deposit_requested" {
		t.Fatalf("aviso = %q, quiero confirmed→deposit_requested", got)
	}
}

// TestService_PedirDosVecesLoMismoNoMandaDosMensajes: el segundo POST idéntico ya no
// es una transición (la solicitud YA está en el destino) y CanTransition lo rechaza
// antes de llegar al store. Al cliente le llegó UN WhatsApp, no dos.
func TestService_PedirDosVecesLoMismoNoMandaDosMensajes(t *testing.T) {
	spy := &avisoSpy{}
	svc := intakes.NewService(seedStore(t, intakes.StatusConfirmed), intakes.WithNotifier(spy))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient); err != nil {
		t.Fatalf("primera transición: %v", err)
	}
	_, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient)

	var invalid *intakes.TransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("la repetición debe dar *TransitionError, dio %v", err)
	}
	if spy.count() != 1 {
		t.Fatalf("avisos = %d tras pedir dos veces lo mismo, quiero 1", spy.count())
	}
}

// TestService_EstadoQueNoCambiaNoNotifica: `confirmed → confirmed` no es una
// transición (status.go: «una transición a sí mismo no es una transición»), así que
// no hay nada que contarle a nadie.
func TestService_EstadoQueNoCambiaNoNotifica(t *testing.T) {
	spy := &avisoSpy{}
	svc := intakes.NewService(seedStore(t, intakes.StatusConfirmed), intakes.WithNotifier(spy))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusConfirmed, intakes.NoticeToClient); err == nil {
		t.Fatal("confirmed → confirmed debería rechazarse")
	}
	if spy.count() != 0 {
		t.Fatalf("avisos = %d, quiero 0", spy.count())
	}
}

// TestService_ElQuePierdeElCASNoNotifica: de dos operadores que piden la MISMA
// transición a la vez, solo uno escribe. El otro se lleva ErrConflict — y sobre
// todo, no le manda un segundo mensaje al cliente por un cambio que no hizo.
func TestService_ElQuePierdeElCASNoNotifica(t *testing.T) {
	spy := &avisoSpy{}
	svc := intakes.NewService(conflictStore{seedStore(t, intakes.StatusConfirmed)}, intakes.WithNotifier(spy))

	_, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient)
	if err == nil {
		t.Fatal("quiero ErrConflict")
	}
	if spy.count() != 0 {
		t.Fatalf("avisos = %d, quiero 0: quien no escribió no notifica", spy.count())
	}
}

// TestService_SinNotificadorNoRevienta: el servicio funciona igual sin la opción
// cableada (es como lo construyen los tests de dominio y como quedaría un despliegue
// que no quisiera avisar).
func TestService_SinNotificadorNoRevienta(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusConfirmed))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusDepositRequested, intakes.NoticeToClient); err != nil {
		t.Fatalf("SetStatus sin notificador: %v", err)
	}
}

// TestService_SesiónOfflineNoTumbaLaTransición es EL test de la regla 1, y va con el
// notificador REAL (no con el espía): el sender falla como falla el Gateway con la
// sesión caída, y aun así
//
//   - SetStatus devuelve sin error,
//   - el estado nuevo está persistido cuando se relee,
//   - y el fallo quedó registrado con su command_id.
//
// Es exactamente el escenario del criterio de T4.2: al dueño le funciona la consola
// aunque el Edge esté apagado.
func TestService_SesiónOfflineNoTumbaLaTransición(t *testing.T) {
	store := seedStore(t, intakes.StatusConfirmed)
	sender := &stubSender{err: &offlineError{cmdID: "cmd-offline-7"}}
	spy := &logSpy{}
	svc := intakes.NewService(store, intakes.WithNotifier(newNotifier(sender, store, spy)))

	updated, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusSettled, intakes.NoticeToClient)
	if err != nil {
		t.Fatalf("la transición no puede fallar porque el envío falle: %v", err)
	}
	if updated.Status != intakes.StatusSettled {
		t.Fatalf("status devuelto = %q, quiero settled", updated.Status)
	}

	after, err := svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get tras la transición: %v", err)
	}
	if after.Status != intakes.StatusSettled {
		t.Fatalf("la transición no se persistió: status=%q", after.Status)
	}
	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos entregados = %d, quiero 0 (la sesión estaba offline)", got)
	}
	if !strings.Contains(spy.all(), "cmd-offline-7") {
		t.Fatalf("el fallo no quedó registrado con su command_id; log:\n%s", spy.all())
	}
}

// senderQuePanica imita el peor fallo posible del transporte: no un error, un
// pánico. Puede venir de cualquiera de las piezas que el notificador consume y que
// esta tarea no controla.
type senderQuePanica struct{}

func (senderQuePanica) SendText(context.Context, string, string, string) (*cloudlinkv1.Ack, error) {
	panic("el transporte reventó")
}

// TestService_UnPánicoAlAvisarNoTumbaLaTransición: la otra mitad de la regla 1. Que
// NotifyStatus no devuelva error no basta si un pánico puede subir por la pila hasta
// el handler: la transición ya está escrita y el dueño se llevaría un 500 por un
// cambio que SÍ se aplicó. Se contiene y se registra.
func TestService_UnPánicoAlAvisarNoTumbaLaTransición(t *testing.T) {
	store := seedStore(t, intakes.StatusConfirmed)
	spy := &logSpy{}
	svc := intakes.NewService(store, intakes.WithNotifier(newNotifier(senderQuePanica{}, store, spy)))

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusSettled, intakes.NoticeToClient); err != nil {
		t.Fatalf("un pánico del aviso no puede tumbar la transición: %v", err)
	}

	after, err := svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get tras la transición: %v", err)
	}
	if after.Status != intakes.StatusSettled {
		t.Fatalf("la transición no se persistió: status=%q", after.Status)
	}
	if !strings.Contains(spy.all(), "el transporte reventó") {
		t.Fatalf("el pánico no quedó registrado; log:\n%s", spy.all())
	}
}

// TestNotifier_ConDependenciasIncompletasNoRevienta: un notificador mal cableado no
// avisa, pero tampoco se lleva por delante la transición.
func TestNotifier_ConDependenciasIncompletasNoRevienta(t *testing.T) {
	var nilo *intakes.Notifier
	nilo.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)

	incompleto := intakes.NewNotifier(nil, nil, nil, nil)
	incompleto.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)
}
