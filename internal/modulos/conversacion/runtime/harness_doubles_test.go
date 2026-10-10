//go:build pendiente

package runtime_test

// harness_doubles_test.go son los dobles PROPIOS del arnés (harness_test.go, de donde se
// partieron por E-13): el reloj movible, el logger que apunta, el abandonador, el
// FlowForKind sobre las reglas y el apuntador de los dos hooks. Los dobles de los puertos del
// runtime viven en runtimehelpertest.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// movableClock es EL reloj del guion: lo comparten el runtime (WithClock), el doble de
// eventos (SetClock, el equivalente de events.WithClock) y el almacén en memoria (que fecha
// UpdatedAt en cada Save). Solo avanza cuando el test lo mueve.
type movableClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now devuelve el instante actual del guion. Es la función que se inyecta.
func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance mueve el reloj d hacia delante.
func (c *movableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set fija el reloj en un instante.
func (c *movableClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// logLine es una línea de log tal como la vio el doble: nivel, mensaje y sus pares
// clave/valor (los heredados de With más los propios).
type logLine struct {
	level  string
	msg    string
	fields map[string]any
}

// logBook guarda lo que escribieron un logRecorder y todos sus hijos de With.
type logBook struct {
	mu    sync.Mutex
	lines []logLine
}

// logRecorder es un logger.Logger que apunta cada llamada en vez de escribirla: los mensajes
// y las claves del runtime son texto observable y se afirman literales.
type logRecorder struct {
	book *logBook
	with []any
}

func newLogRecorder() *logRecorder { return &logRecorder{book: &logBook{}} }

func (l *logRecorder) record(level, msg string, args []any) {
	all := append(append([]any{}, l.with...), args...)
	fields := map[string]any{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			fields[k] = all[i+1]
		}
	}
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	l.book.lines = append(l.book.lines, logLine{level: level, msg: msg, fields: fields})
}

func (l *logRecorder) Debug(msg string, args ...any) { l.record("debug", msg, args) }
func (l *logRecorder) Info(msg string, args ...any)  { l.record("info", msg, args) }
func (l *logRecorder) Warn(msg string, args ...any)  { l.record("warn", msg, args) }
func (l *logRecorder) Error(msg string, args ...any) { l.record("error", msg, args) }
func (l *logRecorder) With(args ...any) logger.Logger {
	return &logRecorder{book: l.book, with: append(append([]any{}, l.with...), args...)}
}

// at devuelve las líneas apuntadas en ese nivel ("debug", "info", "warn" o "error"), en orden.
func (l *logRecorder) at(level string) []logLine {
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	var out []logLine
	for _, line := range l.book.lines {
		if line.level == level {
			out = append(out, line)
		}
	}
	return out
}

// dump vuelca TODO lo apuntado —mensajes, claves y valores— como texto: para buscar un
// literal que no debería estar en el log (PII) y para el mensaje de un fallo.
func (l *logRecorder) dump() string {
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	var b strings.Builder
	for _, line := range l.book.lines {
		fmt.Fprintf(&b, "%s %s %v\n", line.level, line.msg, line.fields)
	}
	return b.String()
}

// abandonRecorder es el doble de runtime.IntakeAbandoner: apunta los eventos cuya solicitud
// se pidió abandonar y, con un error inyectado, lo devuelve.
type abandonRecorder struct {
	mu     sync.Mutex
	events []string
	err    error
}

// AbandonByEvent implementa runtime.IntakeAbandoner.
func (a *abandonRecorder) AbandonByEvent(_ context.Context, _, eventID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, eventID)
	return a.err
}

// calls devuelve los event_id por los que se llamó, en orden.
func (a *abandonRecorder) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...)
}

// fail hace que las llamadas siguientes devuelvan err (nil lo retira).
func (a *abandonRecorder) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// ruleFlows es el runtime.FlowForKind del guion: el flujo de la primera regla event_start
// habilitada de ese tipo, igual que el adaptador del arranque.
type ruleFlows struct{ rules *trigger.MemoryStore }

// FlowForKind implementa runtime.FlowForKind.
func (f ruleFlows) FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	rules, err := f.rules.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return "", err
	}
	for _, r := range rules {
		if r.Enabled && r.EventKind == kind && r.FlowID != "" {
			return r.FlowID, nil
		}
	}
	return "", nil
}

// hookLog apunta lo que el runtime cuenta por sus dos hooks: los motivos de corte y las
// longitudes de las rachas cerradas.
type hookLog struct {
	mu      sync.Mutex
	reasons []string
	streaks []int
}

func (h *hookLog) blocked(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reasons = append(h.reasons, reason)
}

func (h *hookLog) streakClosed(streak int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streaks = append(h.streaks, streak)
}
