package intakes

// Lo que hace ESTRUCTURAL la regla 1 de notifier.go —notificar no puede tumbar la transición—:
// la contención del pánico y el notificador a medias. Los dobles viven en notifier_test.go.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// TestNotifier_ContainsPanics: un pánico de la vía custodiada o del Gateway no sale de ninguna de
// las salidas que entregan, y queda en Error con el pánico entero.
func TestNotifier_ContainsPanics(t *testing.T) {
	t.Parallel()
	sources := map[string]func(r *ntRig){
		"sender":       func(r *ntRig) { r.sender.panicWith = "boom-xkq" },
		"destinations": func(r *ntRig) { r.contacts.panicWith = "boom-xkq" },
	}
	for source, arrange := range sources {
		for _, c := range ntCalls {
			if c.name == "QuoteText" {
				continue // no entrega: no toca ni el Gateway ni la vía custodiada
			}
			t.Run(source+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := newNTRig()
				r.settings.cfg = NotifySettings{DepositTemplate: "Abona {total}", DepositDueDays: 2}
				arrange(r)
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("el pánico salió del notificador: %v", rec)
					}
				}()
				c.call(r.n)
				requireNTContains(t, r.log.line(t, "ERROR", ntLogPanic), "intake_id "+ntIntakeID, "panic boom-xkq")
				if strings.Contains(r.log.all(), "INFO ") {
					t.Errorf("un pánico no puede registrarse como envío hecho; log:\n%s", r.log.all())
				}
			})
		}
	}
}

// TestNotifier_HalfBuiltStaysSilent: un notificador nil, o al que le falta una dependencia que la
// salida necesita, ni avisa ni rompe. Qué necesita cada salida es contrato:
//
//   - NotifyStatus y NotifyCRMStatus: las cuatro (la segunda, aunque no lea la config);
//   - SendQuote y SendQuestion: log, MessageSender y Destinations — sin lector de config ENVÍAN;
//   - QuoteText: log y lector de config — sin MessageSender ni Destinations COMPONE.
func TestNotifier_HalfBuiltStaysSilent(t *testing.T) {
	t.Parallel()
	const composed = "cotización\n\nAbona $18000.00"
	builds := []struct {
		name string
		// missing: la dependencia que falta ("receiver" = el *Notifier es nil).
		missing string
		// sends: las salidas que AUN ASÍ envían. quote: lo que devuelve QuoteText.
		sends map[string]bool
		quote string
	}{
		{name: "nil notifier", missing: "receiver", quote: "cotización"},
		{name: "no sender", missing: "sender", quote: composed},
		{name: "no destinations", missing: "contacts", quote: composed},
		{name: "no settings", missing: "settings", quote: "cotización",
			sends: map[string]bool{"SendQuote": true, "SendQuestion": true}},
		{name: "no logger", missing: "log", quote: "cotización"},
	}
	for _, b := range builds {
		for _, c := range ntCalls {
			t.Run(b.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := newNTRig()
				r.settings.cfg = NotifySettings{DepositTemplate: "Abona {total}", DepositDueDays: 2}
				var (
					sender   MessageSender  = r.sender
					contacts Destinations   = r.contacts
					settings SettingsReader = r.settings
					log      logger.Logger  = r.log
				)
				switch b.missing {
				case "sender":
					sender = nil
				case "contacts":
					contacts = nil
				case "settings":
					settings = nil
				case "log":
					log = nil
				}
				n := NewNotifier(sender, contacts, settings, log)
				if b.missing == "receiver" {
					n = nil
				}

				got := c.call(n) // un pánico aquí tumba el test: es justo lo que no puede pasar

				wantSends := 0
				if b.sends[c.name] {
					wantSends = 1
				}
				if len(r.sender.sent) != wantSends {
					t.Errorf("envíos = %d, quería %d", len(r.sender.sent), wantSends)
				}
				if c.name == "QuoteText" && got != b.quote {
					t.Errorf("QuoteText = %q, quería %q", got, b.quote)
				}
			})
		}
	}
}
