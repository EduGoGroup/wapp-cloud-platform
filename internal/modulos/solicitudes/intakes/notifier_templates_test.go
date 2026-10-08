package intakes

// El INVENTARIO de lo que ve el cliente por WhatsApp, byte a byte (notifier.go, 🔶): las 7
// plantillas por estado del ciclo de vida, las 4 del CRM y el render de los tres marcadores sobre
// la plantilla de seña del tenant. Cada `want` es el mensaje YA renderizado tal como llegó al
// MessageSender, calculado con internal/intakes/notifier.go @ 64c181a. Las tablas de plantillas
// no exportadas no existen en el rojo: los literales viven aquí. Dobles en notifier_test.go.

import (
	"context"
	"testing"
	"time"
)

// Las siete plantillas por estado destino, ya renderizadas para un total de 18000.
const (
	ntTextPendingApproval = "Recibimos tu pedido y lo estamos revisando. Te avisamos apenas te lo confirmemos."
	ntTextConfirmed       = "✅ Tu pedido quedó confirmado. Total $18000.00. ¡Gracias!"
	ntTextDepositPaid     = "Recibimos tu seña. Tu pedido queda reservado; te avisamos cuando esté listo."
	ntTextSettled         = "Tu pedido está pagado por completo. ¡Gracias por tu compra!"
	ntTextCancelled       = "Tu pedido fue cancelado. Si fue un error, respóndenos por aquí y lo retomamos."
	ntTextRejected        = "No podemos tomar tu pedido en este momento. Si quieres, respóndenos y lo vemos."
	ntTextNeedsInfo       = "Nos falta un dato para avanzar con tu pedido. Te escribimos enseguida por aquí."
)

// requireNTOnlyText afirma que salió UN mensaje y que su texto es want, byte a byte.
func requireNTOnlyText(t *testing.T, r *ntRig, want string) {
	t.Helper()
	if len(r.sender.sent) != 1 {
		t.Fatalf("envíos = %d, quería 1; log:\n%s", len(r.sender.sent), r.log.all())
	}
	if got := r.sender.sent[0].text; got != want {
		t.Errorf("texto al cliente:\n%q\nquería, byte a byte:\n%q", got, want)
	}
}

// TestNotifyStatus_StatusTemplatesByteForByte: las 7 claves con texto, más el alias legado
// `closed`, que recibe el de `confirmed`. Los que NO tienen texto están en
// TestNotifyStatus_SilentStatusesNeverTouchPII.
func TestNotifyStatus_StatusTemplatesByteForByte(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status string
		want   string
	}{
		{StatusPendingApproval, ntTextPendingApproval},
		{StatusConfirmed, ntTextConfirmed},
		{StatusDepositPaid, ntTextDepositPaid},
		{StatusSettled, ntTextSettled},
		{StatusCancelled, ntTextCancelled},
		{StatusRejected, ntTextRejected},
		{StatusNeedsInfo, ntTextNeedsInfo},
		{StatusClosedLegacy, ntTextConfirmed},
	}
	for _, c := range cases {
		r := newNTRig()
		// Un tenant con plantilla de seña y plazo propios no altera los textos del código.
		r.settings.cfg = NotifySettings{DepositTemplate: "NO DEBE SALIR {plazo}", DepositDueDays: 9}
		in := ntIntake(c.status)
		in.DepositDueAt = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
		r.n.NotifyStatus(context.Background(), ntTenant, in, StatusOpen)
		requireNTOnlyText(t, r, c.want)
	}
}

// TestNotifyStatus_TotalFormat: {total} es "$" y dos decimales, sin separador de miles: el mismo
// formato que el carrito le enseña al cliente al cerrar.
func TestNotifyStatus_TotalFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		total float64
		want  string
	}{
		{0, "$0.00"},
		{1234.5, "$1234.50"},
		{0.005, "$0.01"},
		{0.004, "$0.00"},
		{2.675, "$2.67"}, // el redondeo es el del binario, no el de la escuela
		{99.999, "$100.00"},
		{1000000, "$1000000.00"},
		{-5, "$-5.00"},
	}
	for _, c := range cases {
		r := newNTRig()
		in := ntIntake(StatusConfirmed)
		in.Total = c.total
		r.n.NotifyStatus(context.Background(), ntTenant, in, StatusPendingApproval)
		requireNTOnlyText(t, r, "✅ Tu pedido quedó confirmado. Total "+c.want+". ¡Gracias!")
	}
}

// TestNotifyStatus_DepositTemplateRendering: la plantilla de seña es del TENANT y la plataforma
// solo rellena sus tres marcadores. Incluye los casos adversarios: plazo no positivo, fecha sin
// fijar, fecha que cambia de día al pasar a UTC, marcadores repetidos, con otra grafía, y una
// plantilla con espacios en los extremos, saltos y emojis, que sale intacta.
func TestNotifyStatus_DepositTemplateRendering(t *testing.T) {
	t.Parallel()
	// 9 de agosto a las 23:30 en UTC-3 es ya 10 de agosto en UTC.
	lateEvening := time.Date(2026, 8, 9, 23, 30, 0, 0, time.FixedZone("x", -3*3600))
	cases := []struct {
		name  string
		tpl   string
		days  int
		total float64
		due   time.Time
		want  string
	}{
		{name: "total and days", tpl: "Abona {total} a la cuenta 001-2 en {plazo} días.", days: 5, total: 18000,
			want: "Abona $18000.00 a la cuenta 001-2 en 5 días."},
		{name: "zero days is the default", tpl: "Tienes {plazo} días.", days: 0,
			want: "Tienes 3 días."},
		{name: "negative days is the default", tpl: "Tienes {plazo} días.", days: -7,
			want: "Tienes 3 días."},
		{name: "one day", tpl: "Tienes {plazo} días.", days: 1,
			want: "Tienes 1 días."},
		{name: "due date in UTC", tpl: "Hasta el {fecha_limite}.", days: 3, due: lateEvening,
			want: "Hasta el 10/08/2026."},
		{name: "unset due date stays visible", tpl: "Hasta el {fecha_limite} ({plazo} días).", days: 4,
			want: "Hasta el {fecha_limite} (4 días)."},
		{name: "repeated markers", tpl: "{total}{total} {plazo}{plazo} {fecha_limite}{fecha_limite}", days: 2, total: 1.5, due: lateEvening,
			want: "$1.50$1.50 22 10/08/202610/08/2026"},
		{name: "other spellings are not markers", tpl: "{TOTAL} { total } {Plazo} {fecha limite} {total", days: 2, total: 7,
			want: "{TOTAL} { total } {Plazo} {fecha limite} {total"},
		{name: "nested braces", tpl: "{{total}} {{plazo}}", days: 2, total: 7,
			want: "{$7.00} {2}"},
		{name: "no markers", tpl: "Transfiere a la cuenta 001-2.", days: 2, total: 7,
			want: "Transfiere a la cuenta 001-2."},
		{name: "whitespace and emoji survive", tpl: "  🏦 Sra. María  José 🎂:\n\ttransfiere {total}  \n", days: 2, total: 1234.5,
			want: "  🏦 Sra. María  José 🎂:\n\ttransfiere $1234.50  \n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newNTRig()
			r.settings.cfg = NotifySettings{DepositTemplate: c.tpl, DepositDueDays: c.days}
			in := ntIntake(StatusDepositRequested)
			in.Total, in.DepositDueAt = c.total, c.due
			r.n.NotifyStatus(context.Background(), ntTenant, in, StatusConfirmed)
			requireNTOnlyText(t, r, c.want)
			if r.settings.calls != 1 {
				t.Errorf("lecturas de config = %d, quería 1", r.settings.calls)
			}
			r.log.line(t, "INFO", ntLogSent)
		})
	}
}

// TestNotifyCRMStatus_TemplatesByteForByte: las 4 claves del CRM. Ninguna lleva marcador, así
// que ni el total ni la fecha de la solicitud las cambian, y no se lee la config del tenant.
func TestNotifyCRMStatus_TemplatesByteForByte(t *testing.T) {
	t.Parallel()
	cases := []struct {
		crmStatus string
		want      string
	}{
		{CRMStatusPaid, "Recibimos tu pago. ¡Gracias! Ya estamos con tu pedido."},
		{CRMStatusPreparing, "Tu pedido ya se está preparando. Te avisamos apenas salga."},
		{CRMStatusDelivered, "Tu pedido fue entregado. ¡Que lo disfrutes! Cualquier cosa, respóndenos por aquí."},
		// El rechazo del CRM y el del ciclo de vida dicen LO MISMO: una sola redacción.
		{CRMStatusRejected, ntTextRejected},
	}
	if len(cases) != len(crmCanonicalStatuses) {
		t.Fatalf("hay %d textos para %d estados canónicos del CRM", len(cases), len(crmCanonicalStatuses))
	}
	for _, c := range cases {
		r := newNTRig()
		in := ntIntake(StatusAbandoned) // el estado del ciclo de vida no decide nada aquí
		in.Total, in.DepositDueAt = 1234.5, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
		r.n.NotifyCRMStatus(context.Background(), ntTenant, in, c.crmStatus)
		requireNTOnlyText(t, r, c.want)
		if got := r.sender.sent[0]; got.sessionID != ntSession || got.to != ntDestination {
			t.Errorf("envío = %+v, quería la sesión de la solicitud y el destino custodiado", got)
		}
		if r.settings.calls != 0 {
			t.Errorf("lecturas de config = %d, quería 0", r.settings.calls)
		}
		requireNTContains(t, r.log.line(t, "INFO", ntLogSent),
			"intake_id "+ntIntakeID, "tenant_id "+ntTenant, "session_id "+ntSession,
			"crm_status "+c.crmStatus, "command_id cmd-ok")
	}
}

// TestNotifyCRMStatus_EveryCanonicalStatusSpeaks: la vigilancia que el propio NotifyCRMStatus
// asume al tratar un estado sin texto como anomalía. Un estado canónico nuevo sin plantilla
// dejaría al cliente sin enterarse con un 200 en el callback.
func TestNotifyCRMStatus_EveryCanonicalStatusSpeaks(t *testing.T) {
	t.Parallel()
	for _, status := range crmCanonicalStatuses {
		r := newNTRig()
		r.n.NotifyCRMStatus(context.Background(), ntTenant, ntIntake(StatusConfirmed), status)
		if len(r.sender.sent) != 1 || r.sender.sent[0].text == "" {
			t.Errorf("el estado canónico %q no le dice nada al cliente: %+v", status, r.sender.sent)
		}
	}
}

// TestNotifyCRMStatus_UnknownStatusIsAnAnomaly: el estado se busca TAL CUAL —sin normalizar— y
// uno sin texto no envía, no toca la vía custodiada y queda en Warn (no en Debug). Los estados
// del ciclo de vida no son estados del CRM, salvo el literal compartido `rejected`.
func TestNotifyCRMStatus_UnknownStatusIsAnAnomaly(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"", "PAID", " paid", "paid ", "shipped", StatusConfirmed, StatusSettled, StatusClosedLegacy} {
		r := newNTRig()
		r.n.NotifyCRMStatus(context.Background(), ntTenant, ntIntake(StatusConfirmed), status)
		if len(r.sender.sent) != 0 || len(r.contacts.calls) != 0 {
			t.Errorf("estado CRM %q: envíos=%d destinos=%d, quería 0/0", status, len(r.sender.sent), len(r.contacts.calls))
		}
		requireNTContains(t, r.log.line(t, "WARN", ntLogCRMNoText+" |"), "crm_status "+status)
	}
}
