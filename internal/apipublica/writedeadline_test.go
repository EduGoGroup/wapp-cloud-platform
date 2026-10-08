package apipublica

// writedeadline_test.go — cubre writedeadline.go, que no exporta nada: por eso es un test
// INTERNO (package apipublica) y nace con su fichero (05 E-4, P6). El plazo de escritura visto
// desde la ruta que lo usa (G7, con su cadena de verdad) está en quotesuggestion_test.go.
//
// Sin red y sin reloj real: el ResponseWriter es un doble que admite plazos (como la conexión)
// o no (como el de httptest), y el reloj se inyecta.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// writeDeadlineWriter es un ResponseWriter que admite plazo de escritura: apunta los que le
// ponen y devuelve err.
type writeDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error
}

func (w *writeDeadlineWriter) SetWriteDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return w.err
}

// writeDeadlineUnwrapper es un envoltorio que SÍ desenvuelve (como metrics.statusRecorder).
type writeDeadlineUnwrapper struct{ http.ResponseWriter }

func (u writeDeadlineUnwrapper) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// writeDeadlineLine es una línea que recibió writeDeadlineLog.
type writeDeadlineLine struct {
	level, msg string
	fields     map[string]any
}

// writeDeadlineLog es un sharedlogger.Logger que guarda sus líneas. (El arnés de la cara no se
// puede importar desde un test interno: importa este paquete.)
type writeDeadlineLog struct{ lines []writeDeadlineLine }

var _ sharedlogger.Logger = (*writeDeadlineLog)(nil)

func (l *writeDeadlineLog) add(level, msg string, args []any) {
	fields := map[string]any{}
	for i := 0; i+1 < len(args); i += 2 {
		if key, ok := args[i].(string); ok {
			fields[key] = args[i+1]
		}
	}
	l.lines = append(l.lines, writeDeadlineLine{level: level, msg: msg, fields: fields})
}

func (l *writeDeadlineLog) Debug(msg string, args ...any)   { l.add("debug", msg, args) }
func (l *writeDeadlineLog) Info(msg string, args ...any)    { l.add("info", msg, args) }
func (l *writeDeadlineLog) Warn(msg string, args ...any)    { l.add("warn", msg, args) }
func (l *writeDeadlineLog) Error(msg string, args ...any)   { l.add("error", msg, args) }
func (l *writeDeadlineLog) With(...any) sharedlogger.Logger { return l }

const writeDeadlineWarn = "no se pudo extender el plazo de escritura de la sugerencia de cotización: " +
	"la respuesta larga volverá a no caber por el cable"

// writeDeadlineClock es un reloj fijo, en una zona que no es UTC.
func writeDeadlineClock() time.Time {
	return time.Date(2026, 10, 8, 6, 30, 5, 0, time.FixedZone("-03", -3*3600))
}

// writeDeadlineInner es el handler envuelto: cuenta sus llamadas, apunta cuántos plazos había
// puestos cuando empezó y responde 204.
type writeDeadlineInner struct {
	calls   int
	seen    int
	observe func() int
}

func (h *writeDeadlineInner) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls++
	if h.observe != nil {
		h.seen = h.observe()
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDeadlineRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/intakes/abc/quote-suggestion?secreto=1", nil)
}

// TestWriteDeadline_SetsNowPlusDeadlineBeforeServing: el plazo es exactamente now() + deadline,
// se pone UNA vez y ANTES de que el handler empiece; el handler se sirve una vez y no hay Warn.
func TestWriteDeadline_SetsNowPlusDeadlineBeforeServing(t *testing.T) {
	for _, deadline := range []time.Duration{time.Millisecond, 60 * time.Second, 3 * time.Hour} {
		t.Run(deadline.String(), func(t *testing.T) {
			w := &writeDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			log := &writeDeadlineLog{}
			inner := &writeDeadlineInner{observe: func() int { return len(w.deadlines) }}

			writeDeadline(log, writeDeadlineClock, deadline, inner).ServeHTTP(w, writeDeadlineRequest())

			want := writeDeadlineClock().Add(deadline)
			if len(w.deadlines) != 1 || !w.deadlines[0].Equal(want) {
				t.Fatalf("plazos puestos = %v; quiero exactamente uno, %s", w.deadlines, want)
			}
			if inner.calls != 1 || inner.seen != 1 {
				t.Errorf("el handler se sirvió %d veces y vio %d plazos al empezar; quiero 1 y 1 (el plazo va antes)", inner.calls, inner.seen)
			}
			if w.Code != http.StatusNoContent {
				t.Errorf("código %d; quiero el del handler, 204", w.Code)
			}
			if len(log.lines) != 0 {
				t.Errorf("líneas de log inesperadas: %+v", log.lines)
			}
		})
	}
}

// TestWriteDeadline_ReachesTheConnectionThroughUnwrap: un envoltorio que desenvuelve no corta
// el camino hasta la conexión.
func TestWriteDeadline_ReachesTheConnectionThroughUnwrap(t *testing.T) {
	w := &writeDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	inner := &writeDeadlineInner{}
	writeDeadline(nil, writeDeadlineClock, time.Minute, inner).ServeHTTP(writeDeadlineUnwrapper{w}, writeDeadlineRequest())
	if len(w.deadlines) != 1 || inner.calls != 1 {
		t.Errorf("plazos = %v, llamadas = %d; quiero un plazo puesto a través de Unwrap y el handler servido", w.deadlines, inner.calls)
	}
}

// TestWriteDeadline_FailureIsLoggedAndTheRequestIsServed: si el plazo no se puede poner —el
// ResponseWriter no admite plazos, o la conexión lo rechaza— la petición se sirve IGUAL y queda
// una línea Warn con el camino (sin la query), el plazo y el error.
func TestWriteDeadline_FailureIsLoggedAndTheRequestIsServed(t *testing.T) {
	rejected := errors.New("conexión cerrada")
	cases := map[string]struct {
		writer  http.ResponseWriter
		wantErr error
	}{
		"writer_without_deadlines": {httptest.NewRecorder(), http.ErrNotSupported},
		"connection_rejects_it":    {&writeDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), err: rejected}, rejected},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := &writeDeadlineLog{}
			inner := &writeDeadlineInner{}
			writeDeadline(log, writeDeadlineClock, 37*time.Second, inner).ServeHTTP(tc.writer, writeDeadlineRequest())

			if inner.calls != 1 {
				t.Errorf("el handler se sirvió %d veces; el fallo del plazo NO aborta la petición, quiero 1", inner.calls)
			}
			if len(log.lines) != 1 || log.lines[0].level != "warn" || log.lines[0].msg != writeDeadlineWarn {
				t.Fatalf("líneas = %+v; quiero exactamente una Warn, %q", log.lines, writeDeadlineWarn)
			}
			fields := log.lines[0].fields
			if fields["path"] != "/api/v1/intakes/abc/quote-suggestion" || fields["plazo"] != "37s" || len(fields) != 3 {
				t.Errorf("campos = %v; quiero path sin la query, plazo \"37s\" y error, y nada más", fields)
			}
			if err, ok := fields["error"].(error); !ok || !errors.Is(err, tc.wantErr) {
				t.Errorf("campo error = %v; quiero %v", fields["error"], tc.wantErr)
			}
		})
	}
}

// TestWriteDeadline_NilLogIsSilent: sin logger el fallo no tiene dónde decirse; se sirve igual.
func TestWriteDeadline_NilLogIsSilent(t *testing.T) {
	inner := &writeDeadlineInner{}
	rec := httptest.NewRecorder()
	writeDeadline(nil, writeDeadlineClock, time.Second, inner).ServeHTTP(rec, writeDeadlineRequest())
	if inner.calls != 1 || rec.Code != http.StatusNoContent {
		t.Errorf("llamadas = %d, código = %d; quiero el handler servido una vez (204)", inner.calls, rec.Code)
	}
}

// TestWriteDeadline_InsideTheAccessLogItCannotReachTheConnection es la trampa T-6, a la vista:
// el ResponseWriter del access-log (observedResponse) ni admite plazos ni desenvuelve, así que
// el MISMO envoltorio colgado por DENTRO de accessLog no alcanza una conexión que sí los admite
// —y lo dice a Warn—, mientras que por FUERA sí. Por eso MountIntakeReports lo pone por fuera.
func TestWriteDeadline_InsideTheAccessLogItCannotReachTheConnection(t *testing.T) {
	inner := &writeDeadlineInner{}

	inside := &writeDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	insideLog := &writeDeadlineLog{}
	accessLog(insideLog, writeDeadline(insideLog, writeDeadlineClock, time.Minute, inner)).ServeHTTP(inside, writeDeadlineRequest())
	warns := 0
	for _, line := range insideLog.lines {
		if line.level == "warn" && line.msg == writeDeadlineWarn {
			warns++
		}
	}
	if len(inside.deadlines) != 0 || warns != 1 {
		t.Errorf("por dentro del access-log: plazos = %v, Warn = %d; quiero ninguno y 1 (si esto cambia, "+
			"observedResponse ya desenvuelve y el comentario de writeDeadline está caducado)", inside.deadlines, warns)
	}

	outside := &writeDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	outsideLog := &writeDeadlineLog{}
	writeDeadline(outsideLog, writeDeadlineClock, time.Minute, accessLog(outsideLog, inner)).ServeHTTP(outside, writeDeadlineRequest())
	if len(outside.deadlines) != 1 {
		t.Errorf("por fuera del access-log: plazos = %v; quiero 1", outside.deadlines)
	}
	if inner.calls != 2 {
		t.Errorf("el handler se sirvió %d veces; quiero 2 (una por montaje)", inner.calls)
	}
}
