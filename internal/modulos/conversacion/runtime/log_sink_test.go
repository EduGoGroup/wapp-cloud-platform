//go:build pendiente

package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// sinkLogLine es una línea de log tal como la vio el doble: nivel, mensaje y sus pares
// clave/valor (los heredados de With más los propios).
type sinkLogLine struct {
	level  string
	msg    string
	fields map[string]any
}

// sinkLogBook guarda lo que escribieron un sinkLogRecorder y todos sus hijos de With.
type sinkLogBook struct {
	mu    sync.Mutex
	lines []sinkLogLine
}

// sinkLogRecorder es un logger.Logger que apunta cada llamada en vez de escribirla: los
// mensajes y las claves de los sinks son texto observable y se afirman literales. Lo usan
// los tests de los dos sinks de este paquete (log_sink_test.go y webhook_sink_test.go).
type sinkLogRecorder struct {
	book *sinkLogBook
	with []any
}

func newSinkLogRecorder() *sinkLogRecorder { return &sinkLogRecorder{book: &sinkLogBook{}} }

func (l *sinkLogRecorder) record(level, msg string, args []any) {
	all := append(append([]any{}, l.with...), args...)
	fields := map[string]any{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			fields[k] = all[i+1]
		}
	}
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	l.book.lines = append(l.book.lines, sinkLogLine{level: level, msg: msg, fields: fields})
}

func (l *sinkLogRecorder) Debug(msg string, args ...any) { l.record("debug", msg, args) }
func (l *sinkLogRecorder) Info(msg string, args ...any)  { l.record("info", msg, args) }
func (l *sinkLogRecorder) Warn(msg string, args ...any)  { l.record("warn", msg, args) }
func (l *sinkLogRecorder) Error(msg string, args ...any) { l.record("error", msg, args) }
func (l *sinkLogRecorder) With(args ...any) logger.Logger {
	return &sinkLogRecorder{book: l.book, with: append(append([]any{}, l.with...), args...)}
}

// all devuelve una copia de todas las líneas apuntadas, en orden.
func (l *sinkLogRecorder) all() []sinkLogLine {
	l.book.mu.Lock()
	defer l.book.mu.Unlock()
	return append([]sinkLogLine(nil), l.book.lines...)
}

// at devuelve las líneas apuntadas en ese nivel.
func (l *sinkLogRecorder) at(level string) []sinkLogLine {
	var out []sinkLogLine
	for _, line := range l.all() {
		if line.level == level {
			out = append(out, line)
		}
	}
	return out
}

// dump vuelca TODO lo apuntado —mensajes, claves y valores— como texto, para buscar un
// literal que no debería estar en ningún sitio del log.
func (l *sinkLogRecorder) dump() string {
	var b strings.Builder
	for _, line := range l.all() {
		fmt.Fprintf(&b, "%s %s %v\n", line.level, line.msg, line.fields)
	}
	return b.String()
}

// Aserciones de compilación: el sink por defecto es un EventSink.
var _ EventSink = (*LogSink)(nil)

// logSinkContext y logSinkEffect son el par con el que se llama a Handle: todos los
// campos distintos entre sí, para que un cruce de claves se vea.
func logSinkContext() EffectContext {
	return EffectContext{
		TenantID: "tenant-1", ContactID: "contact-opaque", SessionID: "session-zzq",
		FlowID: "flow-1", FlowVersion: 7, EventID: "event-zzq",
	}
}

func logSinkEffect() modules.Effect {
	return modules.Effect{
		Kind: "persist", Name: "survey_answer",
		Payload: map[string]any{"question_key_zzq": "q1", "answer_code": "answer-value-zzq"},
	}
}

// TestNewLogSink_NeverNil: el constructor devuelve un sink utilizable, con logger y sin él.
func TestNewLogSink_NeverNil(t *testing.T) {
	if NewLogSink(newSinkLogRecorder()) == nil {
		t.Error("NewLogSink(logger) devolvió nil")
	}
	if NewLogSink(nil) == nil {
		t.Error("NewLogSink(nil) devolvió nil; tiene que devolver un sink mudo")
	}
}

// TestLogSink_Handle_LogsOneInfoLineWithMetadata: una línea Info, el mensaje literal y
// exactamente las seis claves de metadatos, cada una con su valor.
func TestLogSink_Handle_LogsOneInfoLineWithMetadata(t *testing.T) {
	log := newSinkLogRecorder()

	if err := NewLogSink(log).Handle(context.Background(), logSinkContext(), logSinkEffect()); err != nil {
		t.Fatalf("Handle devolvió %v; el sink por defecto no falla nunca", err)
	}

	lines := log.all()
	if len(lines) != 1 {
		t.Fatalf("se escribieron %d líneas, quería 1: %+v", len(lines), lines)
	}
	line := lines[0]
	if line.level != "info" {
		t.Errorf("nivel = %q, quería info", line.level)
	}
	if want := "runtime: efecto despachado (log-only)"; line.msg != want {
		t.Errorf("mensaje = %q, quería %q", line.msg, want)
	}
	want := map[string]any{
		"kind": "persist", "name": "survey_answer", "tenant": "tenant-1",
		"contact_id": "contact-opaque", "flow_id": "flow-1", "version": 7,
	}
	if len(line.fields) != len(want) {
		t.Errorf("la línea lleva %d claves, quería exactamente %d: %v", len(line.fields), len(want), line.fields)
	}
	for key, value := range want {
		if got, ok := line.fields[key]; !ok || got != value {
			t.Errorf("clave %q = %#v (presente=%v), quería %#v", key, got, ok, value)
		}
	}
}

// TestLogSink_Handle_NeverLogsPayloadSessionOrEvent: ni las claves ni los valores del
// payload salen por el log (higiene §10.G), y tampoco la sesión ni el evento.
func TestLogSink_Handle_NeverLogsPayloadSessionOrEvent(t *testing.T) {
	log := newSinkLogRecorder()

	if err := NewLogSink(log).Handle(context.Background(), logSinkContext(), logSinkEffect()); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}

	logged := log.dump()
	if logged == "" {
		t.Fatal("no se escribió nada: sin línea que inspeccionar el barrido no prueba nada")
	}
	for _, forbidden := range []string{"question_key_zzq", "answer-value-zzq", "session-zzq", "event-zzq"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("el log contiene %q, que no puede salir:\n%s", forbidden, logged)
		}
	}
}

// TestLogSink_Handle_DoesNotTouchTheEffect: el payload lo comparten los demás sinks del
// fan-out; el sink por defecto no escribe en él.
func TestLogSink_Handle_DoesNotTouchTheEffect(t *testing.T) {
	sink := NewLogSink(newSinkLogRecorder())
	eff := logSinkEffect()

	if err := sink.Handle(context.Background(), logSinkContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}

	if len(eff.Payload) != 2 || eff.Payload["question_key_zzq"] != "q1" || eff.Payload["answer_code"] != "answer-value-zzq" {
		t.Errorf("el sink tocó el payload compartido: %v", eff.Payload)
	}
}

// TestLogSink_Handle_SilentAndSafeWithoutLogger: sobre un receptor nil, sobre el valor
// cero y sobre un sink construido con logger nil, Handle devuelve nil sin pánico.
func TestLogSink_Handle_SilentAndSafeWithoutLogger(t *testing.T) {
	var nilSink *LogSink
	cases := []struct {
		name string
		sink *LogSink
	}{
		{"nil receiver", nilSink},
		{"zero value", &LogSink{}},
		{"built with nil logger", NewLogSink(nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.sink.Handle(context.Background(), logSinkContext(), logSinkEffect()); err != nil {
				t.Errorf("Handle devolvió %v, quería nil", err)
			}
		})
	}
}

// TestLogSink_NotPhased: el sink por defecto no declara fase, así que corre en la de
// proyección.
func TestLogSink_NotPhased(t *testing.T) {
	var sink EventSink = NewLogSink(newSinkLogRecorder())
	if _, ok := sink.(PhasedSink); ok {
		t.Error("LogSink implementa PhasedSink; no debe declarar fase (corre en PhaseProject)")
	}
}
