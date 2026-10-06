package apipublica

// deadlines_test.go — cubre deadlines.go. Es un test INTERNO (package apipublica) porque, salvo
// SendBudgetFrom, lo que deadlines.go promete vive en auxiliares no exportados, que nacieron con
// el verde (05 E-4, P6). No espera a que venza ningún plazo: mira el Deadline de los contextos.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

func TestSendBudgetFrom(t *testing.T) {
	cases := []struct {
		name         string
		writeTimeout time.Duration
		want         time.Duration
	}{
		{"default_write_timeout_leaves_nine_seconds", 10 * time.Second, 9 * time.Second},
		{"just_above_the_margin", time.Second + time.Millisecond, time.Millisecond},
		{"exactly_the_margin_is_no_budget", time.Second, 0},
		{"below_the_margin_is_no_budget", 500 * time.Millisecond, 0},
		{"zero_is_no_budget", 0, 0},
		{"negative_is_no_budget", -5 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SendBudgetFrom(tc.writeTimeout); got != tc.want {
				t.Errorf("SendBudgetFrom(%v) = %v, quiero %v", tc.writeTimeout, got, tc.want)
			}
		})
	}
}

// TestDeadlineDefaults fija las cifras: 1,5 s es el default de config.PublicAPIDBTimeout y 30 min
// la retención del bundle de diagnóstico; ninguna de las dos se mueve sin moverlo todo.
func TestDeadlineDefaults(t *testing.T) {
	if defaultDBTimeout != 1500*time.Millisecond {
		t.Errorf("defaultDBTimeout = %v, quiero 1,5 s", defaultDBTimeout)
	}
	if defaultDiagnosticsTTL != 30*time.Minute {
		t.Errorf("defaultDiagnosticsTTL = %v, quiero 30 min", defaultDiagnosticsTTL)
	}
	if writeMargin != time.Second {
		t.Errorf("writeMargin = %v, quiero 1 s", writeMargin)
	}
}

// remaining devuelve cuánto le queda al contexto y si trae plazo.
func remaining(ctx context.Context) (time.Duration, bool) {
	dl, ok := ctx.Deadline()
	return time.Until(dl), ok
}

// TestDBCtx: el plazo pedido, o el suelo de 1,5 s si llega <= 0 (nunca un plazo ya vencido).
func TestDBCtx(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"given", 10 * time.Second, 10 * time.Second},
		{"zero_falls_to_default", 0, defaultDBTimeout},
		{"negative_falls_to_default", -time.Second, defaultDBTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := dbCtx(t.Context(), tc.timeout)
			defer cancel()
			left, ok := remaining(ctx)
			if !ok || left > tc.want || left < tc.want-500*time.Millisecond {
				t.Errorf("dbCtx(%v): plazo=%v restante=%v; quiero ~%v", tc.timeout, ok, left, tc.want)
			}
			if ctx.Err() != nil {
				t.Errorf("dbCtx(%v) nació vencido: %v", tc.timeout, ctx.Err())
			}
			cancel()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Errorf("tras cancel, ctx.Err() = %v, quiero Canceled", ctx.Err())
			}
		})
	}
}

// TestDBCtx_KeepsAShorterParentDeadline: dbCtx acota UNA consulta; no alarga el presupuesto de
// la petición que la envuelve.
func TestDBCtx_KeepsAShorterParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancelParent()
	ctx, cancel := dbCtx(parent, 10*time.Second)
	defer cancel()
	if left, ok := remaining(ctx); !ok || left > 200*time.Millisecond {
		t.Errorf("plazo=%v restante=%v; quiero el del padre (<= 200 ms)", ok, left)
	}
}

// TestSendCtx: con presupuesto, ese plazo; sin él (<= 0), el contexto TAL CUAL, sin suelo.
func TestSendCtx(t *testing.T) {
	ctx, cancel := sendCtx(t.Context(), 9*time.Second)
	defer cancel()
	if left, ok := remaining(ctx); !ok || left > 9*time.Second || left < 8*time.Second {
		t.Errorf("sendCtx(9s): plazo=%v restante=%v; quiero ~9 s", ok, left)
	}
	for _, budget := range []time.Duration{0, -time.Second} {
		parent := t.Context()
		got, cancelNone := sendCtx(parent, budget)
		if got != parent {
			t.Errorf("sendCtx(%v) devolvió otro contexto; quiero el mismo, sin plazo", budget)
		}
		if _, ok := got.Deadline(); ok {
			t.Errorf("sendCtx(%v) puso un plazo", budget)
		}
		cancelNone() // no-op: no puede cancelar el contexto del llamante
		if got.Err() != nil {
			t.Errorf("el cancel de sendCtx(%v) canceló el contexto del llamante", budget)
		}
	}
}

// warnSpy es un sharedlogger.Logger que guarda las líneas Warn (el arnés del paquete
// apipublicahelpertest importa apipublica: un test interno no puede usarlo).
type warnSpy struct {
	msgs   []string
	fields [][]any
}

var _ sharedlogger.Logger = (*warnSpy)(nil)

func (*warnSpy) Debug(string, ...any) {}
func (*warnSpy) Info(string, ...any)  {}
func (*warnSpy) Error(string, ...any) {}
func (s *warnSpy) Warn(msg string, args ...any) {
	s.msgs = append(s.msgs, msg)
	s.fields = append(s.fields, args)
}
func (s *warnSpy) With(...any) sharedlogger.Logger { return s }

func TestDBTimedOut504_Deadline(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"bare", context.DeadlineExceeded},
		{"wrapped", fmt.Errorf("fleet: listando: %w", context.DeadlineExceeded)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, log := httptest.NewRecorder(), &warnSpy{}
			if !dbTimedOut504(rec, log, tc.err, "no dio tiempo, reintenta", "op", "x.y", "tenant_id", "t-1") {
				t.Fatal("con el plazo vencido devolvió false: el handler seguiría de largo")
			}
			if rec.Code != http.StatusGatewayTimeout {
				t.Errorf("código %d, quiero 504", rec.Code)
			}
			wantBody(t, "dbTimedOut504", rec.Body.String(), `{"error":"no dio tiempo, reintenta"}`)
			if len(log.msgs) != 1 || log.msgs[0] != "lectura a BD vencida: se responde 504" {
				t.Fatalf("líneas Warn = %q; quiero una con el texto de siempre", log.msgs)
			}
			if got := fmt.Sprint(log.fields[0]); got != "[op x.y tenant_id t-1]" {
				t.Errorf("campos = %s; quiero los recibidos, tal cual", got)
			}
		})
	}
}

// TestDBTimedOut504_OtherErrorsPassThrough: solo el vencimiento es suyo. Ni una cancelación ni
// un fallo cualquiera ni la ausencia de error escriben o registran nada.
func TestDBTimedOut504_OtherErrorsPassThrough(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, errors.New("bd caída")} {
		rec, log := httptest.NewRecorder(), &warnSpy{}
		if dbTimedOut504(rec, log, err, "motivo") {
			t.Errorf("dbTimedOut504(%v) = true; quiero false", err)
		}
		if rec.Body.Len() != 0 || len(rec.Header()) != 0 || len(log.msgs) != 0 {
			t.Errorf("dbTimedOut504(%v) escribió (%q) o registró (%q)", err, rec.Body.String(), log.msgs)
		}
	}
}

// TestDBTimedOut504_NilLogStillAnswers: un logger nil no es un error: se responde igual, mudo.
func TestDBTimedOut504_NilLogStillAnswers(t *testing.T) {
	rec := httptest.NewRecorder()
	if !dbTimedOut504(rec, nil, context.DeadlineExceeded, "motivo") || rec.Code != http.StatusGatewayTimeout {
		t.Errorf("sin logger: código %d; quiero true y 504", rec.Code)
	}
}
