package intakes_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// El número del contacto de las pruebas. Es PII: ningún log del notificador puede
// contenerlo, y hay un test que lo comprueba a lo bruto sobre TODO lo emitido.
const destinoDePrueba = "573015550101"

// --- dobles -----------------------------------------------------------------

// sentMessage es un envío tal como lo vio el Sender.
type sentMessage struct{ sessionID, to, text string }

// stubSender registra los envíos y devuelve lo que se le configure. Con err != nil
// no registra nada: imita al Gateway, que ante una sesión offline ni siquiera llega
// a empujar el comando.
type stubSender struct {
	mu   sync.Mutex
	sent []sentMessage
	ack  *cloudlinkv1.Ack
	err  error
}

func (s *stubSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.sent = append(s.sent, sentMessage{sessionID: sessionID, to: to, text: text})
	if s.ack != nil {
		return s.ack, nil
	}
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-ok", Ok: true}, nil
}

func (s *stubSender) messages() []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentMessage(nil), s.sent...)
}

// offlineError imita el error del Gateway cuando la sesión está offline: lleva el
// command_id del comando que NO se pudo empujar. Es un doble del contrato
// (`interface{ CommandID() string }`), no del tipo concreto; que
// *gatewaygrpc.SendError lo cumpla de verdad lo prueba el test de su propio
// paquete (TestSendTextSesiónOfflineDevuelveSendErrorConCommandID).
type offlineError struct{ cmdID string }

func (e *offlineError) Error() string     { return "sesión offline" }
func (e *offlineError) CommandID() string { return e.cmdID }

// stubDestinations imita la vía custodiada de PII: devuelve el destino descifrado
// o el error del resolver.
type stubDestinations struct {
	ref contact.Ref
	err error
}

func (d stubDestinations) Destino(_ context.Context, _, _ string) (contact.Ref, error) {
	if d.err != nil {
		return contact.Ref{}, d.err
	}
	return d.ref, nil
}

// logSpy retiene TODO lo emitido (nivel, mensaje y args) para poder afirmar tanto
// que algo se registró como que algo —el destino— jamás aparece.
type logSpy struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSpy) record(level, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+msg+" "+fmt.Sprint(args...))
}

func (l *logSpy) Debug(msg string, args ...any) { l.record("DEBUG", msg, args...) }
func (l *logSpy) Info(msg string, args ...any)  { l.record("INFO", msg, args...) }
func (l *logSpy) Warn(msg string, args ...any)  { l.record("WARN", msg, args...) }
func (l *logSpy) Error(msg string, args ...any) { l.record("ERROR", msg, args...) }

// With arrastra los args del contexto a cada línea, igual que el logger real: sin
// esto, el test de "cero PII en logs" no miraría lo que de verdad se emite.
func (l *logSpy) With(args ...any) logger.Logger { return &childSpy{parent: l, args: args} }

func (l *logSpy) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type childSpy struct {
	parent *logSpy
	args   []any
}

func (c *childSpy) Debug(msg string, args ...any) { c.parent.record("DEBUG", msg, c.join(args)...) }
func (c *childSpy) Info(msg string, args ...any)  { c.parent.record("INFO", msg, c.join(args)...) }
func (c *childSpy) Warn(msg string, args ...any)  { c.parent.record("WARN", msg, c.join(args)...) }
func (c *childSpy) Error(msg string, args ...any) { c.parent.record("ERROR", msg, c.join(args)...) }
func (c *childSpy) With(args ...any) logger.Logger {
	return &childSpy{parent: c.parent, args: c.join(args)}
}
func (c *childSpy) join(args []any) []any { return append(append([]any(nil), c.args...), args...) }

var (
	_ logger.Logger = (*logSpy)(nil)
	_ logger.Logger = (*childSpy)(nil)
)

// --- helpers ----------------------------------------------------------------

// intakeEn devuelve la cabecera de una solicitud en el estado dado.
func intakeEn(status string) intakes.Intake {
	return intakes.Intake{
		ID:        "11111111-1111-1111-1111-111111111111",
		ContactID: "contacto-opaco-1",
		SessionID: "sess-negocio",
		Status:    status,
		Total:     18000,
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
}

// newNotifier arma el notificador con el store en memoria como fuente de config.
func newNotifier(sender intakes.MessageSender, st *intakes.MemoryStore, log logger.Logger) *intakes.Notifier {
	dst := stubDestinations{ref: contact.Ref{Kind: contact.KindPhoneE164, Value: destinoDePrueba}}
	return intakes.NewNotifier(sender, dst, st, log)
}

// --- el camino feliz --------------------------------------------------------

// TestNotifier_EnvíaPorLaSesiónDeLaSolicitud: el aviso sale por la MISMA sesión de
// WhatsApp con la que el cliente armó el pedido, al destino que devolvió la vía
// custodiada, con el texto del estado destino.
func TestNotifier_EnvíaPorLaSesiónDeLaSolicitud(t *testing.T) {
	sender := &stubSender{}
	n := newNotifier(sender, intakes.NewMemoryStore(), &logSpy{})

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("envíos = %d, quiero exactamente 1", len(msgs))
	}
	if msgs[0].sessionID != "sess-negocio" {
		t.Fatalf("session_id = %q, quiero la de la solicitud", msgs[0].sessionID)
	}
	if msgs[0].to != destinoDePrueba {
		t.Fatalf("destino = %q, quiero el resuelto por la vía custodiada", msgs[0].to)
	}
	if !strings.Contains(msgs[0].text, "confirmado") {
		t.Fatalf("texto = %q, no habla de la confirmación", msgs[0].text)
	}
	// El total se renderiza con el MISMO formato que el carrito le enseña al
	// cliente al cerrar (cart/screens.go, `money`): "$" y dos decimales.
	if !strings.Contains(msgs[0].text, "$18000.00") {
		t.Fatalf("texto = %q, no trae el total con el formato del carrito", msgs[0].text)
	}
}

// TestNotifier_LaSeñaUsaLaPlantillaDelTenant: el texto de deposit_requested NO sale
// del código —son los datos bancarios del tenant— y sus marcadores se rellenan.
func TestNotifier_LaSeñaUsaLaPlantillaDelTenant(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.SetDepositTemplate(tenantA, "Abona {total} a la cuenta 001-2 en {plazo} días.", 5)
	sender := &stubSender{}
	n := newNotifier(sender, st, &logSpy{})

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusDepositRequested), intakes.StatusConfirmed)

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("envíos = %d, quiero 1", len(msgs))
	}
	if got, want := msgs[0].text, "Abona $18000.00 a la cuenta 001-2 en 5 días."; got != want {
		t.Fatalf("texto = %q, quiero %q", got, want)
	}
}

// TestNotifier_LaSeñaSinPlantillaNoManda: DECISIÓN DE PRODUCTO — un «te pedimos una
// seña» sin decir dónde pagarla es peor que el silencio, y la columna ya lo dice
// («vacía ⇒ el tenant no puede pedir seña»). El silencio queda registrado en Warn.
func TestNotifier_LaSeñaSinPlantillaNoManda(t *testing.T) {
	sender := &stubSender{}
	spy := &logSpy{}
	n := newNotifier(sender, intakes.NewMemoryStore(), spy) // tenant sin sembrar

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusDepositRequested), intakes.StatusConfirmed)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d, quiero 0: sin plantilla no se manda nada", got)
	}
	if !strings.Contains(spy.all(), "WARN") || !strings.Contains(spy.all(), "deposit_template") {
		t.Fatalf("el silencio no quedó registrado con su causa; log:\n%s", spy.all())
	}
}

// TestNotifier_AbandonedNoNotifica: descartar es higiene interna del dueño y NO se
// le cuenta al cliente (design.md §D-041.18: «no borra y no notifica»).
func TestNotifier_AbandonedNoNotifica(t *testing.T) {
	sender := &stubSender{}
	n := newNotifier(sender, intakes.NewMemoryStore(), &logSpy{})

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusAbandoned), intakes.StatusOpen)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d, quiero 0: el descarte no notifica", got)
	}
}

// --- lo que pasa cuando el envío no sale ------------------------------------

// TestNotifier_SesiónOfflineLogueaConCommandID: el fallo NO se traga ni escala; se
// registra en Error CON el command_id que el Gateway ya había asignado, que es el
// único hilo para buscar después en el outbox del Edge y en los acuses.
func TestNotifier_SesiónOfflineLogueaConCommandID(t *testing.T) {
	sender := &stubSender{err: &offlineError{cmdID: "cmd-perdido-42"}}
	spy := &logSpy{}
	n := newNotifier(sender, intakes.NewMemoryStore(), spy)

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)

	log := spy.all()
	if !strings.Contains(log, "ERROR") {
		t.Fatalf("el fallo de envío no se registró como error; log:\n%s", log)
	}
	if !strings.Contains(log, "cmd-perdido-42") {
		t.Fatalf("el error no lleva el command_id; log:\n%s", log)
	}
}

// TestNotifier_AckConOkFalseEsFallo: el Edge acusó el comando pero avisó de que el
// envío falló. Para el cliente la consecuencia es la misma —no le llegó nada—, así
// que se registra como error, no como éxito.
func TestNotifier_AckConOkFalseEsFallo(t *testing.T) {
	sender := &stubSender{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-rechazado", Ok: false, Error: "destino inválido"}}
	spy := &logSpy{}
	n := newNotifier(sender, intakes.NewMemoryStore(), spy)

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusSettled), intakes.StatusDepositPaid)

	log := spy.all()
	if !strings.Contains(log, "ERROR") || !strings.Contains(log, "cmd-rechazado") {
		t.Fatalf("un Ack con Ok=false debe registrarse como fallo con su command_id; log:\n%s", log)
	}
	if strings.Contains(log, "INFO") {
		t.Fatalf("un Ack con Ok=false no puede loguearse como envío exitoso; log:\n%s", log)
	}
}

// TestNotifier_ContactoSinDestinoNoRompe: si la vía custodiada no puede resolver el
// contacto, se registra y se acaba ahí. La transición ya está aplicada.
func TestNotifier_ContactoSinDestinoNoRompe(t *testing.T) {
	sender := &stubSender{}
	spy := &logSpy{}
	n := intakes.NewNotifier(sender,
		stubDestinations{err: errors.New("contact: contacto no encontrado")},
		intakes.NewMemoryStore(), spy)

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d, quiero 0: sin destino no hay a quién mandar", got)
	}
	if !strings.Contains(spy.all(), "ERROR") {
		t.Fatalf("el fallo al resolver el destino no se registró; log:\n%s", spy.all())
	}
}

// TestNotifier_ElKindNoDirecionableNoEnvía: wa_username todavía no es direccionable
// (contact.Ref.Sendable lo rechaza) y eso no puede acabar en un envío a "".
func TestNotifier_ElKindNoDirecionableNoEnvía(t *testing.T) {
	sender := &stubSender{}
	spy := &logSpy{}
	n := intakes.NewNotifier(sender,
		stubDestinations{ref: contact.Ref{Kind: contact.KindWAUsername, Value: "marta"}},
		intakes.NewMemoryStore(), spy)

	n.NotifyStatus(context.Background(), tenantA, intakeEn(intakes.StatusConfirmed), intakes.StatusPendingApproval)

	if got := len(sender.messages()); got != 0 {
		t.Fatalf("envíos = %d, quiero 0: el kind no es direccionable", got)
	}
	if !strings.Contains(spy.all(), "ERROR") {
		t.Fatalf("el destino no direccionable no se registró; log:\n%s", spy.all())
	}
}

// --- zero-knowledge ---------------------------------------------------------

// TestNotifier_NuncaLogueaElDestino barre TODOS los caminos que emiten log y exige
// que el número del contacto no aparezca en ninguno (ADR-0007 / INV-04). Es el test
// que muerde si alguien añade el destino a un log "para depurar": ahí se acaba
// filtrando la PII que el resto del sistema cifra.
func TestNotifier_NuncaLogueaElDestino(t *testing.T) {
	casos := []struct {
		nombre string
		sender *stubSender
		estado string
	}{
		{"envío ok", &stubSender{}, intakes.StatusConfirmed},
		{"envío fallido", &stubSender{err: &offlineError{cmdID: "cmd-x"}}, intakes.StatusConfirmed},
		{"ack rechazado", &stubSender{ack: &cloudlinkv1.Ack{AckedCommandId: "c", Ok: false, Error: "no"}}, intakes.StatusCancelled},
		{"sin plantilla de seña", &stubSender{}, intakes.StatusDepositRequested},
		{"estado que no notifica", &stubSender{}, intakes.StatusAbandoned},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			spy := &logSpy{}
			n := newNotifier(c.sender, intakes.NewMemoryStore(), spy)

			n.NotifyStatus(context.Background(), tenantA, intakeEn(c.estado), intakes.StatusOpen)

			if strings.Contains(spy.all(), destinoDePrueba) {
				t.Fatalf("el destino (PII) se filtró al log:\n%s", spy.all())
			}
		})
	}
}
