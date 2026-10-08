package intakes

// Los tests de fichero de notifier.go, con dobles de sus cuatro dependencias: sin Gateway, sin
// base y sin reloj. Los literales esperados (textos que ve el cliente, mensajes de log) están
// calculados con el fichero viejo (internal/intakes/notifier.go @ 64c181a): el candado de
// fronteras impide importarlo desde aquí. Las plantillas, byte a byte, van en
// notifier_templates_test.go; la cotización del dueño, en notifier_quote_test.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

const (
	// ntDestination es el número del contacto de las pruebas. Es PII: ningún log del
	// notificador puede contenerlo.
	ntDestination = "573015550101"
	ntTenant      = "tenant-nt-0001"
	ntIntakeID    = "11111111-1111-1111-1111-111111111111"
	ntContactID   = "contact-opaque-1"
	ntSession     = "sess-business"
)

// Los mensajes de log del notificador, byte a byte.
const (
	ntLogSent         = "notificación de cambio de estado enviada al cliente"
	ntLogSilentStatus = "notificación: el estado no le dice nada al cliente, no se envía"
	ntLogNoTemplate   = "notificación: el tenant no tiene plantilla de seña (tenant_settings.deposit_template); "
	ntLogSettingsFail = "notificación: no se pudo leer la config del tenant"
	ntLogResolveFail  = "notificación: no se pudo resolver el destino del contacto"
	ntLogNotSendable  = "notificación: el contacto no tiene destino direccionable"
	ntLogSendFail     = "notificación: el envío falló; la transición ya está aplicada"
	ntLogEdgeRejected = "notificación: el Edge rechazó el envío; la transición ya está aplicada"
	ntLogPanic        = "notificación: pánico avisando del cambio de estado; la transición YA está aplicada"
	ntLogCRMNoText    = "notificación CRM: estado canónico sin texto, el cliente no se entera"

	ntConsequenceOnDeposit = "la transición se aplicó pero al cliente no se le manda nada"
	ntConsequenceOnApprove = "se le manda la cotización del dueño sola, sin instrucciones de pago"
)

// ntMessage es un envío tal como lo vio el MessageSender.
type ntMessage struct{ sessionID, to, text string }

// ntSender apunta cada SendText y contesta lo que se le configure.
type ntSender struct {
	sent      []ntMessage
	ack       *cloudlinkv1.Ack
	nilAck    bool
	err       error
	panicWith any
}

func (s *ntSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	s.sent = append(s.sent, ntMessage{sessionID: sessionID, to: to, text: text})
	switch {
	case s.panicWith != nil:
		panic(s.panicWith)
	case s.err != nil:
		return nil, s.err
	case s.nilAck:
		return nil, nil //nolint:nilnil // el doble imita un Ack inesperado: ni acuse ni error
	case s.ack != nil:
		return s.ack, nil
	}
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-ok", Ok: true}, nil
}

// ntCarrierError imita el error del Gateway con la sesión offline: lleva el command_id del
// comando que NO se pudo empujar. Es un doble del contrato (`CommandID() string`).
type ntCarrierError struct{ commandID string }

func (e *ntCarrierError) Error() string     { return "sesión offline" }
func (e *ntCarrierError) CommandID() string { return e.commandID }

// ntDestinations imita la vía custodiada de PII y apunta cada consulta ("tenant|contacto").
type ntDestinations struct {
	ref       contact.Ref
	err       error
	panicWith any
	calls     []string
}

func (d *ntDestinations) Destino(_ context.Context, tenantID, contactID string) (contact.Ref, error) {
	d.calls = append(d.calls, tenantID+"|"+contactID)
	if d.panicWith != nil {
		panic(d.panicWith)
	}
	if d.err != nil {
		return contact.Ref{}, d.err
	}
	return d.ref, nil
}

// ntSettings es el lector de config del tenant de mentira; cuenta las lecturas.
type ntSettings struct {
	cfg   NotifySettings
	err   error
	calls int
}

func (s *ntSettings) NotifySettings(context.Context, string) (NotifySettings, error) {
	s.calls++
	return s.cfg, s.err
}

// ntLog retiene TODO lo emitido —nivel, mensaje y pares, los del With incluidos, como el logger
// de verdad— para afirmar tanto que algo se registró como que el destino jamás aparece.
type ntLog struct {
	lines *[]string
	args  []any
}

func newNTLog() *ntLog { return &ntLog{lines: new([]string)} }

func (l *ntLog) record(level, msg string, args ...any) {
	all := append(append([]any(nil), l.args...), args...)
	*l.lines = append(*l.lines, level+" "+msg+" | "+strings.TrimSuffix(fmt.Sprintln(all...), "\n"))
}

func (l *ntLog) Debug(msg string, args ...any) { l.record("DEBUG", msg, args...) }
func (l *ntLog) Info(msg string, args ...any)  { l.record("INFO", msg, args...) }
func (l *ntLog) Warn(msg string, args ...any)  { l.record("WARN", msg, args...) }
func (l *ntLog) Error(msg string, args ...any) { l.record("ERROR", msg, args...) }
func (l *ntLog) With(args ...any) logger.Logger {
	return &ntLog{lines: l.lines, args: append(append([]any(nil), l.args...), args...)}
}

func (l *ntLog) all() string { return strings.Join(*l.lines, "\n") }

// line devuelve la ÚNICA línea de ese nivel cuyo mensaje empieza por msg.
func (l *ntLog) line(t *testing.T, level, msg string) string {
	t.Helper()
	var found []string
	for _, ln := range *l.lines {
		if strings.HasPrefix(ln, level+" "+msg) {
			found = append(found, ln)
		}
	}
	if len(found) != 1 {
		t.Fatalf("líneas %s %q = %d, quería 1; log:\n%s", level, msg, len(found), l.all())
	}
	return found[0]
}

// Los dobles satisfacen los puertos del contrato.
var (
	_ MessageSender  = (*ntSender)(nil)
	_ Destinations   = (*ntDestinations)(nil)
	_ SettingsReader = (*ntSettings)(nil)
	_ logger.Logger  = (*ntLog)(nil)
)

// ntRig es un notificador montado sobre sus cuatro dobles.
type ntRig struct {
	sender   *ntSender
	contacts *ntDestinations
	settings *ntSettings
	log      *ntLog
	n        *Notifier
}

func newNTRig() *ntRig {
	r := &ntRig{
		sender:   &ntSender{},
		contacts: &ntDestinations{ref: contact.Ref{Kind: contact.KindPhoneE164, Value: ntDestination}},
		settings: &ntSettings{},
		log:      newNTLog(),
	}
	r.n = NewNotifier(r.sender, r.contacts, r.settings, r.log)
	return r
}

// ntIntake devuelve la cabecera de una solicitud en el estado dado.
func ntIntake(status string) Intake {
	return Intake{ID: ntIntakeID, ContactID: ntContactID, SessionID: ntSession, Status: status, Total: 18000}
}

func requireNTContains(t *testing.T, line string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(line, want) {
			t.Errorf("la línea de log no lleva %q:\n%s", want, line)
		}
	}
}

// TestStatusNotice_ZeroValueSpeaks: el CERO del tipo es NoticeToClient —el valor por descuido
// habla, nunca calla— y NoticeByCaller es el otro valor, 1.
func TestStatusNotice_ZeroValueSpeaks(t *testing.T) {
	t.Parallel()
	var zero StatusNotice
	if zero != NoticeToClient {
		t.Errorf("el cero de StatusNotice = %d, quería NoticeToClient (%d)", zero, NoticeToClient)
	}
	if NoticeToClient != 0 || NoticeByCaller != 1 {
		t.Errorf("NoticeToClient = %d, NoticeByCaller = %d; quería 0 y 1", NoticeToClient, NoticeByCaller)
	}
}

// TestDefaultDepositDueDays_MirrorsTheColumnDefault: espeja el DEFAULT de la migración 0045.
func TestDefaultDepositDueDays_MirrorsTheColumnDefault(t *testing.T) {
	t.Parallel()
	if DefaultDepositDueDays != 3 {
		t.Errorf("DefaultDepositDueDays = %d, quería 3", DefaultDepositDueDays)
	}
}

// TestNotifyStatus_DeliversThroughTheIntakeSession: UN SendText, por la sesión de la solicitud, al
// destino que devolvió la vía custodiada para (tenant, contact_id), y el éxito queda en Info con
// el command_id del Ack. Un estado con plantilla del código no lee la config del tenant.
func TestNotifyStatus_DeliversThroughTheIntakeSession(t *testing.T) {
	t.Parallel()
	r := newNTRig()

	r.n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusSettled), StatusClosedLegacy)

	want := ntMessage{sessionID: ntSession, to: ntDestination, text: "Tu pedido está pagado por completo. ¡Gracias por tu compra!"}
	if len(r.sender.sent) != 1 || r.sender.sent[0] != want {
		t.Fatalf("envíos = %+v, quería exactamente %+v", r.sender.sent, want)
	}
	if len(r.contacts.calls) != 1 || r.contacts.calls[0] != ntTenant+"|"+ntContactID {
		t.Errorf("consultas a la vía custodiada = %v, quería una por (tenant, contact_id)", r.contacts.calls)
	}
	if r.settings.calls != 0 {
		t.Errorf("lecturas de config = %d, quería 0: la plantilla es del código", r.settings.calls)
	}
	// El origen también se normaliza en el log: `closed` es `confirmed`.
	requireNTContains(t, r.log.line(t, "INFO", ntLogSent),
		"intake_id "+ntIntakeID, "tenant_id "+ntTenant, "session_id "+ntSession,
		"status_from confirmed", "status_to settled", "command_id cmd-ok")
}

// TestNotifyStatus_SilentStatusesNeverTouchPII: un estado sin plantilla no envía y —el orden del
// contrato— no llega a pedir el destino ni a leer la config. El estado no se recorta ni se pasa a
// minúsculas: otra grafía es un estado sin plantilla. Es el camino normal de `abandoned` (Debug).
func TestNotifyStatus_SilentStatusesNeverTouchPII(t *testing.T) {
	t.Parallel()
	statuses := []string{
		StatusAbandoned, StatusOpen, StatusExpired, "", "unknown",
		"Confirmed", "CONFIRMED", " confirmed", "confirmed ", "confirmed\n", "ｃonfirmed",
	}
	for _, status := range statuses {
		r := newNTRig()
		r.n.NotifyStatus(context.Background(), ntTenant, ntIntake(status), StatusOpen)
		if len(r.sender.sent) != 0 || len(r.contacts.calls) != 0 || r.settings.calls != 0 {
			t.Errorf("estado %q: envíos=%d destinos=%d config=%d, quería 0/0/0",
				status, len(r.sender.sent), len(r.contacts.calls), r.settings.calls)
		}
		r.log.line(t, "DEBUG", ntLogSilentStatus)
	}
}

// TestNotifyStatus_DepositWithoutTemplateSendsNothing: decisión de producto — sin plantilla de
// seña (vacía o solo blancos, Unicode incluido) no sale nada, no se descifra un contacto para
// nada, y el silencio queda en Warn con su causa y su consecuencia.
func TestNotifyStatus_DepositWithoutTemplateSendsNothing(t *testing.T) {
	t.Parallel()
	for _, tpl := range []string{"", "   ", " \n\t ", "\u00a0\u3000"} {
		r := newNTRig()
		r.settings.cfg = NotifySettings{DepositTemplate: tpl, DepositDueDays: 5}
		r.n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusDepositRequested), StatusConfirmed)
		if len(r.sender.sent) != 0 || len(r.contacts.calls) != 0 {
			t.Errorf("plantilla %q: envíos=%d destinos=%d, quería 0/0", tpl, len(r.sender.sent), len(r.contacts.calls))
		}
		r.log.line(t, "WARN", ntLogNoTemplate+ntConsequenceOnDeposit+" |")
	}
}

// TestNotifyStatus_SettingsFailureSendsNothing: no poder leer la config es una avería (Error) y
// acaba en silencio, aunque el lector devuelva además una plantilla.
func TestNotifyStatus_SettingsFailureSendsNothing(t *testing.T) {
	t.Parallel()
	r := newNTRig()
	r.settings.cfg = NotifySettings{DepositTemplate: "Abona {total}", DepositDueDays: 5}
	r.settings.err = errors.New("db caída")

	r.n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusDepositRequested), StatusConfirmed)

	if len(r.sender.sent) != 0 || len(r.contacts.calls) != 0 {
		t.Errorf("envíos=%d destinos=%d, quería 0/0", len(r.sender.sent), len(r.contacts.calls))
	}
	requireNTContains(t, r.log.line(t, "ERROR", ntLogSettingsFail),
		"error db caída", "consecuencia "+ntConsequenceOnDeposit)
}

// TestNotifyStatus_DeliveryFailuresAreLoggedNotRaised: cada tropiezo de la entrega se registra en
// Error con su mensaje y muere ahí; nunca hay un Info de éxito ni un segundo intento.
func TestNotifyStatus_DeliveryFailuresAreLoggedNotRaised(t *testing.T) {
	t.Parallel()
	carrier := &ntCarrierError{commandID: "cmd-lost-42"}
	cases := []struct {
		name      string
		arrange   func(r *ntRig)
		wantSends int
		wantMsg   string
		wantInLog []string
	}{
		{name: "resolver fails", wantSends: 0, wantMsg: ntLogResolveFail,
			arrange:   func(r *ntRig) { r.contacts.err = errors.New("contact: contacto no encontrado") },
			wantInLog: []string{"error contact: contacto no encontrado"}},
		{name: "kind not addressable", wantSends: 0, wantMsg: ntLogNotSendable,
			arrange: func(r *ntRig) { r.contacts.ref = contact.Ref{Kind: contact.KindWAUsername, Value: "marta"} }},
		{name: "send error carries command id", wantSends: 1, wantMsg: ntLogSendFail,
			arrange:   func(r *ntRig) { r.sender.err = carrier },
			wantInLog: []string{"command_id cmd-lost-42 error sesión offline"}},
		{name: "wrapped send error carries command id", wantSends: 1, wantMsg: ntLogSendFail,
			arrange:   func(r *ntRig) { r.sender.err = fmt.Errorf("gateway: %w", carrier) },
			wantInLog: []string{"command_id cmd-lost-42 error gateway: sesión offline"}},
		{name: "send error without command id", wantSends: 1, wantMsg: ntLogSendFail,
			arrange:   func(r *ntRig) { r.sender.err = errors.New("sin stream") },
			wantInLog: []string{"command_id  error sin stream"}},
		{name: "ack not ok", wantSends: 1, wantMsg: ntLogEdgeRejected,
			arrange: func(r *ntRig) {
				r.sender.ack = &cloudlinkv1.Ack{AckedCommandId: "cmd-rejected", Ok: false, Error: "destino inválido"}
			},
			wantInLog: []string{"command_id cmd-rejected edge_error destino inválido"}},
		{name: "nil ack", wantSends: 1, wantMsg: ntLogEdgeRejected,
			arrange:   func(r *ntRig) { r.sender.nilAck = true },
			wantInLog: []string{"command_id  edge_error"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newNTRig()
			c.arrange(r)
			r.n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusConfirmed), StatusPendingApproval)
			if len(r.sender.sent) != c.wantSends {
				t.Errorf("llamadas a SendText = %d, quería %d", len(r.sender.sent), c.wantSends)
			}
			requireNTContains(t, r.log.line(t, "ERROR", c.wantMsg), c.wantInLog...)
			if strings.Contains(r.log.all(), "INFO ") {
				t.Errorf("un envío fallido no puede registrarse como éxito; log:\n%s", r.log.all())
			}
		})
	}
}

// ntCalls son las cinco salidas del notificador, cada una con lo que devuelve (solo QuoteText).
var ntCalls = []struct {
	name string
	call func(n *Notifier) string
}{
	{"NotifyStatus", func(n *Notifier) string {
		n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusConfirmed), StatusPendingApproval)
		return ""
	}},
	{"NotifyStatus deposit", func(n *Notifier) string {
		n.NotifyStatus(context.Background(), ntTenant, ntIntake(StatusDepositRequested), StatusConfirmed)
		return ""
	}},
	{"NotifyCRMStatus", func(n *Notifier) string {
		n.NotifyCRMStatus(context.Background(), ntTenant, ntIntake(StatusConfirmed), CRMStatusPaid)
		return ""
	}},
	{"SendQuote", func(n *Notifier) string {
		n.SendQuote(context.Background(), ntTenant, ntIntake(StatusConfirmed), "cotización")
		return ""
	}},
	{"SendQuestion", func(n *Notifier) string {
		n.SendQuestion(context.Background(), ntTenant, ntIntake(StatusNeedsInfo), "¿color?")
		return ""
	}},
	{"QuoteText", func(n *Notifier) string {
		return n.QuoteText(context.Background(), ntTenant, ntIntake(StatusPendingApproval), "cotización")
	}},
}

// TestNotifier_NeverLogsTheDestination barre los caminos que emiten log, por las cinco salidas, y
// exige que el número del contacto no aparezca en ninguno (ADR-0007 / INV-04).
func TestNotifier_NeverLogsTheDestination(t *testing.T) {
	t.Parallel()
	arrangements := map[string]func(r *ntRig){
		"ok":           func(*ntRig) {},
		"send error":   func(r *ntRig) { r.sender.err = &ntCarrierError{commandID: "cmd-x"} },
		"ack rejected": func(r *ntRig) { r.sender.ack = &cloudlinkv1.Ack{AckedCommandId: "c", Error: "no"} },
		"sender panic": func(r *ntRig) { r.sender.panicWith = "boom" },
		"no template":  func(r *ntRig) { r.settings.cfg = NotifySettings{} },
	}
	for name, arrange := range arrangements {
		for _, c := range ntCalls {
			r := newNTRig()
			r.settings.cfg = NotifySettings{DepositTemplate: "Abona {total}", DepositDueDays: 2}
			arrange(r)
			c.call(r.n)
			if strings.Contains(r.log.all(), ntDestination) {
				t.Errorf("%s / %s: el destino (PII) se filtró al log:\n%s", name, c.name, r.log.all())
			}
		}
	}
}
