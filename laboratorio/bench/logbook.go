//go:build laboratorio

package bench

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// LogBook es el logger.Logger del banco: apunta CADA línea que el runtime, el carrito y el
// agregador escriben, para que el escalón pueda enseñar las de aviso y error junto al paso que las
// provocó, y las reenvía al logger de fuera si lo hay (-v).
type LogBook struct {
	mu    sync.Mutex
	lines []LogLine
	read  int
	inner logger.Logger
	with  []any
	root  *LogBook
}

// LogLine es una línea apuntada.
type LogLine struct {
	Level  string
	Msg    string
	Fields map[string]any
}

// String la deja en una línea: «WARN mensaje clave=valor …», con las claves en orden.
func (l LogLine) String() string {
	keys := make([]string, 0, len(l.Fields))
	for k := range l.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(strings.ToUpper(l.Level) + " " + l.Msg)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, l.Fields[k])
	}
	return b.String()
}

var _ logger.Logger = (*LogBook)(nil)

// NewLogBook crea el libro; inner puede ser nil.
func NewLogBook(inner logger.Logger) *LogBook {
	b := &LogBook{inner: inner}
	b.root = b
	return b
}

func (l *LogBook) record(level, msg string, args []any) {
	all := append(append([]any{}, l.with...), args...)
	fields := map[string]any{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			fields[k] = all[i+1]
		}
	}
	r := l.root
	r.mu.Lock()
	r.lines = append(r.lines, LogLine{Level: level, Msg: msg, Fields: fields})
	r.mu.Unlock()
}

// Debug implementa logger.Logger.
func (l *LogBook) Debug(msg string, args ...any) {
	l.record("debug", msg, args)
	if l.inner != nil {
		l.inner.Debug(msg, args...)
	}
}

// Info implementa logger.Logger.
func (l *LogBook) Info(msg string, args ...any) {
	l.record("info", msg, args)
	if l.inner != nil {
		l.inner.Info(msg, args...)
	}
}

// Warn implementa logger.Logger.
func (l *LogBook) Warn(msg string, args ...any) {
	l.record("warn", msg, args)
	if l.inner != nil {
		l.inner.Warn(msg, args...)
	}
}

// Error implementa logger.Logger.
func (l *LogBook) Error(msg string, args ...any) {
	l.record("error", msg, args)
	if l.inner != nil {
		l.inner.Error(msg, args...)
	}
}

// With implementa logger.Logger: el hijo escribe en el mismo libro.
func (l *LogBook) With(args ...any) logger.Logger {
	child := &LogBook{root: l.root, with: append(append([]any{}, l.with...), args...)}
	if l.inner != nil {
		child.inner = l.inner.With(args...)
	}
	return child
}

// Take devuelve las líneas de aviso y de error escritas desde la última llamada (con info=true,
// también las de info).
func (l *LogBook) Take(info bool) []LogLine {
	r := l.root
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []LogLine
	for _, line := range r.lines[r.read:] {
		if line.Level == "warn" || line.Level == "error" || (info && line.Level == "info") {
			out = append(out, line)
		}
	}
	r.read = len(r.lines)
	return out
}
