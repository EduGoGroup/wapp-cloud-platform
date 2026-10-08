//go:build pendiente

package crmpush

// push_doubles_test.go — los dobles de los tests de este paquete: reloj fijo, log que
// apunta, gate y cola de mentira. Partido de push_test.go por tamaño (E-13).

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// fixedClock congela el `timestamp` del contrato para poder comparar el documento
// entero contra un literal.
func fixedClock() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }

// logEntry es una línea de log tal como la vio el doble: nivel, mensaje y sus pares
// clave/valor (los heredados de With más los propios).
type logEntry struct {
	level  string
	msg    string
	fields map[string]any
}

// logBook guarda lo que escribieron un recordingLogger y todos sus hijos de With.
type logBook struct {
	mu      sync.Mutex
	entries []logEntry
}

// recordingLogger es un logger.Logger que apunta cada llamada en vez de escribirla:
// los mensajes y las claves de este paquete son texto observable y se afirman
// literales.
type recordingLogger struct {
	book *logBook
	with []any
}

func newRecordingLogger() *recordingLogger { return &recordingLogger{book: &logBook{}} }

func (l *recordingLogger) record(level, msg string, args []any) {
	all := append(append([]any{}, l.with...), args...)
	fields := map[string]any{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			fields[k] = all[i+1]
		}
	}
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	l.book.entries = append(l.book.entries, logEntry{level: level, msg: msg, fields: fields})
}

func (l *recordingLogger) Debug(msg string, args ...any) { l.record("debug", msg, args) }
func (l *recordingLogger) Info(msg string, args ...any)  { l.record("info", msg, args) }
func (l *recordingLogger) Warn(msg string, args ...any)  { l.record("warn", msg, args) }
func (l *recordingLogger) Error(msg string, args ...any) { l.record("error", msg, args) }
func (l *recordingLogger) With(args ...any) logger.Logger {
	return &recordingLogger{book: l.book, with: append(append([]any{}, l.with...), args...)}
}

// at devuelve las líneas apuntadas en ese nivel.
func (l *recordingLogger) at(level string) []logEntry {
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	var out []logEntry
	for _, e := range l.book.entries {
		if e.level == level {
			out = append(out, e)
		}
	}
	return out
}

// fakeGate deja pasar/bloquea por tenant; sin entrada trata como cerrado. Apunta
// cada consulta para poder contar evaluaciones.
type fakeGate struct {
	mu    sync.Mutex
	open  map[string]bool
	err   error
	calls []string
	ctxs  []context.Context
}

func (g *fakeGate) Enabled(ctx context.Context, tenantID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, tenantID)
	g.ctxs = append(g.ctxs, ctx)
	if g.err != nil {
		return false, g.err
	}
	return g.open[tenantID], nil
}

// queueCall es un INSERT tal como llegó al doble.
type queueCall struct {
	ctx      context.Context
	tenantID string
	kind     string
	payload  json.RawMessage
}

// fakeQueue graba cada INSERT para que el test inspeccione tenant/kind/cuerpo sin
// tocar Postgres. Los ids salen de firstID en adelante, para que un OutboxID
// inventado no acierte por casualidad.
type fakeQueue struct {
	mu      sync.Mutex
	calls   []queueCall
	err     error
	firstID int64
}

func (q *fakeQueue) EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return 0, q.err
	}
	q.calls = append(q.calls, queueCall{ctx: ctx, tenantID: tenantID, kind: kind, payload: payload})
	return q.firstID + int64(len(q.calls)) - 1, nil
}

func (q *fakeQueue) recorded() []queueCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]queueCall(nil), q.calls...)
}
