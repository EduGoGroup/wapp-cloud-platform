//go:build pendiente

package intakes

// La cotización del DUEÑO (Plan 044 · T4.3/T4.4, D-044.49): QuoteText compone, SendQuote y
// SendQuestion entregan tal cual. Vectores calculados con internal/intakes/notifier.go @ 64c181a.
// Dobles en notifier_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestQuoteText_ComposesOwnerTextAndDepositTemplate: el texto del dueño BYTE A BYTE, un renglón
// en blanco y la plantilla de seña renderizada. No envía nada ni toca la vía custodiada.
func TestQuoteText_ComposesOwnerTextAndDepositTemplate(t *testing.T) {
	t.Parallel()
	due := time.Date(2026, 8, 9, 23, 30, 0, 0, time.FixedZone("x", -3*3600))
	cases := []struct {
		name  string
		owner string
		tpl   string
		days  int
		due   time.Time
		want  string
	}{
		{name: "owner text then template", owner: "Hola Marta, son 2 tortas.", tpl: "Abona {total} en {plazo} días.", days: 5,
			want: "Hola Marta, son 2 tortas.\n\nAbona $18000.00 en 5 días."},
		{name: "owner text is not trimmed", owner: "  Hola 🎂 \n", tpl: "Abona {total}.", days: 5,
			want: "  Hola 🎂 \n\n\nAbona $18000.00."},
		{name: "owner braces are not markers", owner: "Total {total}, plazo {plazo}", tpl: "Abona {total} en {plazo} días.", days: 0,
			want: "Total {total}, plazo {plazo}\n\nAbona $18000.00 en 3 días."},
		{name: "empty owner text", owner: "", tpl: "Abona {total}.", days: 5,
			want: "\n\nAbona $18000.00."},
		{name: "due date unset on approval stays visible", owner: "Listo.", tpl: "Hasta el {fecha_limite}.", days: 5,
			want: "Listo.\n\nHasta el {fecha_limite}."},
		{name: "due date already set", owner: "Listo.", tpl: "Hasta el {fecha_limite}.", days: 5, due: due,
			want: "Listo.\n\nHasta el 10/08/2026."},
		{name: "template is not trimmed", owner: "Listo.", tpl: " \n Abona {total} \n", days: 5,
			want: "Listo.\n\n \n Abona $18000.00 \n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newNTRig()
			r.settings.cfg = NotifySettings{DepositTemplate: c.tpl, DepositDueDays: c.days}
			in := ntIntake(StatusPendingApproval)
			in.DepositDueAt = c.due
			if got := r.n.QuoteText(context.Background(), ntTenant, in, c.owner); got != c.want {
				t.Errorf("QuoteText:\n%q\nquería, byte a byte:\n%q", got, c.want)
			}
			if len(r.sender.sent) != 0 || len(r.contacts.calls) != 0 {
				t.Errorf("envíos=%d destinos=%d: QuoteText solo compone", len(r.sender.sent), len(r.contacts.calls))
			}
		})
	}
}

// TestQuoteText_WithoutTemplateIsTheOwnerTextAlone: sin plantilla (Warn) o con la config ilegible
// (Error) la cotización sale sola, intacta: es una respuesta completa. La consecuencia que cuenta
// el log es la de APROBAR, no la de pedir la seña.
func TestQuoteText_WithoutTemplateIsTheOwnerTextAlone(t *testing.T) {
	t.Parallel()
	const owner = "  Hola Marta {total} \n"
	for _, tpl := range []string{"", " \n\t ", "\u00a0"} {
		r := newNTRig()
		r.settings.cfg = NotifySettings{DepositTemplate: tpl, DepositDueDays: 5}
		if got := r.n.QuoteText(context.Background(), ntTenant, ntIntake(StatusPendingApproval), owner); got != owner {
			t.Errorf("plantilla %q: QuoteText = %q, quería el texto del dueño solo %q", tpl, got, owner)
		}
		requireNTContains(t, r.log.line(t, "WARN", ntLogNoTemplate+ntConsequenceOnApprove+" |"),
			"intake_id "+ntIntakeID, "tenant_id "+ntTenant, "accion approve")
	}

	r := newNTRig()
	r.settings.cfg = NotifySettings{DepositTemplate: "Abona {total}", DepositDueDays: 5}
	r.settings.err = errors.New("db caída")
	if got := r.n.QuoteText(context.Background(), ntTenant, ntIntake(StatusPendingApproval), owner); got != owner {
		t.Errorf("config ilegible: QuoteText = %q, quería %q", got, owner)
	}
	requireNTContains(t, r.log.line(t, "ERROR", ntLogSettingsFail),
		"error db caída", "consecuencia "+ntConsequenceOnApprove, "accion approve")
}

// TestSendQuoteAndSendQuestion_DeliverTheTextVerbatim: las dos entregan el texto TAL CUAL —no
// componen, no renderizan, no recortan, y no leen la config— por la sesión de la solicitud; solo
// las distingue el motivo que queda en el log.
func TestSendQuoteAndSendQuestion_DeliverTheTextVerbatim(t *testing.T) {
	t.Parallel()
	texts := []string{"Son {total} en {plazo} días, hasta el {fecha_limite}.", "  ¿De qué color? 🎂 \n", ""}
	exits := []struct {
		action string
		send   func(n *Notifier, in Intake, text string)
	}{
		{"approve", func(n *Notifier, in Intake, text string) { n.SendQuote(context.Background(), ntTenant, in, text) }},
		{"request_info", func(n *Notifier, in Intake, text string) {
			n.SendQuestion(context.Background(), ntTenant, in, text)
		}},
	}
	for _, e := range exits {
		for _, text := range texts {
			r := newNTRig()
			r.settings.cfg = NotifySettings{DepositTemplate: "NO DEBE SALIR", DepositDueDays: 5}
			in := ntIntake(StatusClosedLegacy)
			in.DepositDueAt = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

			e.send(r.n, in, text)

			want := ntMessage{sessionID: ntSession, to: ntDestination, text: text}
			if len(r.sender.sent) != 1 || r.sender.sent[0] != want {
				t.Fatalf("%s: envíos = %+v, quería exactamente %+v", e.action, r.sender.sent, want)
			}
			if r.settings.calls != 0 {
				t.Errorf("%s: lecturas de config = %d, quería 0", e.action, r.settings.calls)
			}
			// El estado del log va normalizado: `closed` es `confirmed`.
			requireNTContains(t, r.log.line(t, "INFO", ntLogSent),
				"intake_id "+ntIntakeID, "tenant_id "+ntTenant, "session_id "+ntSession,
				"status_to confirmed", "accion "+e.action, "command_id cmd-ok")
		}
	}
}
