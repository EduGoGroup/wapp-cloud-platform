//go:build pendiente

package llmvia

// La ESCRITURA del aviso (notify.go): con qué contexto y qué instante se llama al
// notificador, qué pasa si falla y qué queda en el log. Es la parte de los tests de
// notify.go que no cabe en notify_test.go (E-13); el rig y sus dobles están allí.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// ctxSpy es un Notifier que mira CON QUÉ se le llama: el estado del contexto, su plazo y el
// instante. Contesta lo sembrado.
type ctxSpy struct {
	calls     int
	ctxErr    error
	budget    time.Duration
	hasBudget bool
	at        time.Time
	created   bool
	err       error
}

func (c *ctxSpy) Record(ctx context.Context, _ string, _ degradation.Reason, _ string, at time.Time) (bool, error) {
	c.calls++
	c.ctxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		c.hasBudget, c.budget = true, time.Until(deadline)
	}
	c.at = at
	return c.created, c.err
}

// TestNotice_SurvivesTheDeadContext es el caso que se pierde solo (T-7). Cuando el llamante
// se rinde —la ventana se cerró, el proceso se apaga— el ctx llega YA CANCELADO. Sin
// desacoplarlo, la escritura del aviso fallaría exactamente cuando hay algo que contar. Y el
// desacople lleva su propio techo de 3 s, para no convertirse en una espera sin fin.
func TestNotice_SurvivesTheDeadContext(t *testing.T) {
	t.Parallel()
	dead, cancel := context.WithCancel(context.Background())
	cancel() // el llamante ya se rindió

	for name, fail := range map[string]func(s *Selector) error{
		"pipeline door": func(s *Selector) error {
			// For va con un ctx VIVO a propósito: el que llega muerto es el de la llamada al
			// provider, que es la que se quiere ver fallar y avisar.
			p, err := s.For(context.Background(), rigTenant, "") //nolint:contextcheck // ver arriba: el ctx muerto es el de la P1
			if err != nil {
				return nil //nolint:nilerr // sin provider no hay fallo del pipeline que devolver: el test falla en «quería el error del transporte»
			}
			return classify(dead, p)
		},
		"turn door": func(s *Selector) error {
			_, err := s.Turno(dead, rigTenant, "s-1", rigTurn())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(&reasonError{edgegrpc.ReasonTimeout})
			spy := &ctxSpy{created: true}
			if err := fail(r.selector(t, WithNotifier(spy))); err == nil {
				t.Fatal("quería el error del transporte")
			}
			if spy.calls != 1 {
				t.Fatalf("avisos = %d, quería 1", spy.calls)
			}
			if spy.ctxErr != nil {
				t.Errorf("el aviso se escribió con el contexto del llamante ya cancelado (%v): se perdería justo cuando hace falta", spy.ctxErr)
			}
			if !spy.hasBudget || spy.budget <= 0 || spy.budget > 3*time.Second {
				t.Errorf("plazo del aviso = (%v, %v), quería uno propio de 3 s como mucho", spy.budget, spy.hasBudget)
			}
			if !spy.at.Equal(rigClock) {
				t.Errorf("instante del aviso = %s, quería el del reloj del selector (%s)", spy.at, rigClock)
			}
		})
	}
}

// TestNotice_RecordFailureOnlyGoesToTheLog: el aviso NO PUEDE tumbar nada. El fallo de la
// inferencia ya ocurrió y ya se va a propagar; que además no se pueda anotar es un segundo
// problema, no un motivo para cambiar lo que el llamante recibe.
func TestNotice_RecordFailureOnlyGoesToTheLog(t *testing.T) {
	t.Parallel()
	failure := &reasonError{edgegrpc.ReasonOllamaDown}
	broken := errors.New("la base de avisos no está")
	r := newRig(failure)
	spy := &ctxSpy{err: broken}

	_, err := r.selector(t, WithNotifier(spy)).Turno(context.Background(), rigTenant, "s-1", rigTurn())
	if err != error(failure) { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
		t.Fatalf("Turno = %v, quería el error del transporte y no el del aviso", err)
	}
	if len(r.falls.falls) != 1 {
		t.Errorf("caídas = %v, quería 1: se cuenta antes de escribir", r.falls.falls)
	}
	entries := r.logMessages(t, logNoticeFailed)
	if len(entries) != 1 {
		t.Fatalf("líneas %q en el log = %d, quería 1; log: %s", logNoticeFailed, len(entries), r.logs)
	}
	want := map[string]any{"tenant_id": rigTenant, "reason": edgegrpc.ReasonOllamaDown, "via": tenantllm.ViaLocal}
	for key, value := range want {
		if entries[0][key] != value {
			t.Errorf("clave %s del log = %v, quería %v", key, entries[0][key], value)
		}
	}
	if _, ok := entries[0]["error"]; !ok {
		t.Error("la línea del log no lleva la clave error con el fallo del aviso")
	}
	// El error ORIGINAL no se repite en el log: puede llevar reflejado texto del proveedor.
	if bytes.Contains(r.logs.Bytes(), []byte(failure.Error())) {
		t.Errorf("el log repite el error original de la inferencia: %s", r.logs)
	}
	if born := r.logMessages(t, logNoticeBorn); len(born) != 0 {
		t.Errorf("se logueó un aviso nacido que no se pudo escribir: %v", born)
	}
}

// TestNotice_LogsOnlyWhenBorn: un aviso que se colapsa sobre otro de la misma ventana es el
// dedupe funcionando, y loguearlo cada vez reintroduciría por el log el ruido que la tabla
// evita.
func TestNotice_LogsOnlyWhenBorn(t *testing.T) {
	t.Parallel()
	r := newRig(&reasonError{edgegrpc.ReasonBreakerOpen})
	s := r.selector(t, r.notifier())
	for range 3 {
		if _, err := s.Turno(context.Background(), rigTenant, "s-1", rigTurn()); err == nil {
			t.Fatal("quería el error del transporte")
		}
	}
	entries := r.logMessages(t, logNoticeBorn)
	if len(entries) != 1 {
		t.Fatalf("líneas %q en el log = %d, quería 1 (solo cuando NACE); log: %s", logNoticeBorn, len(entries), r.logs)
	}
	want := map[string]any{"level": "WARN", "tenant_id": rigTenant, "reason": edgegrpc.ReasonBreakerOpen, "via": tenantllm.ViaLocal}
	for key, value := range want {
		if entries[0][key] != value {
			t.Errorf("clave %s del log = %v, quería %v", key, entries[0][key], value)
		}
	}
}
