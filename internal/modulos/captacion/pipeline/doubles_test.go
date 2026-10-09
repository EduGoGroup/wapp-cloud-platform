//go:build pendiente

package pipeline_test

// doubles_test.go — LOS DOBLES DE INFRAESTRUCTURA de los tests del worker (sin fichero de
// producción gemelo): el reloj, el log que se deja interrogar, el ticker de mentira y el
// store que falla donde se le pide. Las etapas falsas y el banco están en
// doubles_stages_test.go.
//
// 🔴 AQUÍ NO SE DUERME. Lo que avanza es un reloj falso; lo que sincroniza son canales
// (un tic del ticker falso no vuelve hasta que Run lo recibe) y `eventually`, que cede el
// procesador hasta que una condición POSITIVA se cumple y solo usa el reloj real como
// límite de paciencia para fallar con diagnóstico en vez de colgarse.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

const (
	tenantID  = "tenant-1"
	sessionID = "sess-1"
	contactID = "contacto-1"
	eventID   = "11111111-1111-1111-1111-111111111111"

	// draftIntakeID es el id que devuelve el draft falso: un UUID fijo, para comparar
	// contra ESTE valor y no solo contra «no vacío».
	draftIntakeID = "9f3c1d52-4b8a-4a6e-9c11-7d2e6f0a5b34"
	// clearLiteral es lo que «descifra» el descifrador falso.
	clearLiteral = "quiero una torta de chocolate para el viernes"
)

// fakeClock es el reloj inyectado: solo avanza cuando un test lo empuja.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// logLine es una línea de log capturada, con sus campos ya emparejados.
type logLine struct {
	level  string
	msg    string
	fields map[string]any
}

// requireKeys afirma que la línea lleva todas esas claves.
func (l logLine) requireKeys(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := l.fields[k]; !ok {
			t.Errorf("a la línea %q le falta el campo %q; trae %v", l.msg, k, l.fields)
		}
	}
}

// recordingLog implementa logger.Logger guardando lo que se emite.
type recordingLog struct {
	mu    sync.Mutex
	lines []logLine
}

var _ logger.Logger = (*recordingLog)(nil)

func (r *recordingLog) emit(level, msg string, args []any) {
	fields := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		fields[fmt.Sprint(args[i])] = args[i+1]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, logLine{level: level, msg: msg, fields: fields})
}

func (r *recordingLog) Debug(msg string, args ...any) { r.emit("DEBUG", msg, args) }
func (r *recordingLog) Info(msg string, args ...any)  { r.emit("INFO", msg, args) }
func (r *recordingLog) Warn(msg string, args ...any)  { r.emit("WARN", msg, args) }
func (r *recordingLog) Error(msg string, args ...any) { r.emit("ERROR", msg, args) }

// With devuelve el mismo log: el worker no deriva loggers.
func (r *recordingLog) With(...any) logger.Logger { return r }

// find devuelve las líneas cuyo mensaje contiene el fragmento, en orden.
func (r *recordingLog) find(fragment string) []logLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logLine
	for _, l := range r.lines {
		if strings.Contains(l.msg, fragment) {
			out = append(out, l)
		}
	}
	return out
}

// one exige UNA sola línea con ese fragmento, a ese nivel, y la devuelve.
func (r *recordingLog) one(t *testing.T, level, fragment string) logLine {
	t.Helper()
	found := r.find(fragment)
	if len(found) != 1 {
		t.Fatalf("se esperaba UNA línea con %q, hay %d:\n%s", fragment, len(found), r.dump())
	}
	if found[0].level != level {
		t.Fatalf("la línea %q salió a %s, se esperaba %s", found[0].msg, found[0].level, level)
	}
	return found[0]
}

// at devuelve las líneas de un nivel.
func (r *recordingLog) at(level string) []logLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logLine
	for _, l := range r.lines {
		if l.level == level {
			out = append(out, l)
		}
	}
	return out
}

// requireNoErrors afirma que no hay ni una línea a ERROR.
func (r *recordingLog) requireNoErrors(t *testing.T) {
	t.Helper()
	if lines := r.at("ERROR"); len(lines) != 0 {
		t.Errorf("el log tiene %d línea(s) a ERROR, se esperaba ninguna:\n%s", len(lines), r.dump())
	}
}

// dump es el log entero, para el mensaje de un fallo.
func (r *recordingLog) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, l := range r.lines {
		fmt.Fprintf(&b, "  [%s] %s %v\n", l.level, l.msg, l.fields)
	}
	return b.String()
}

// fakeTicker es el ticker inyectado por WithTicker. Su canal NO tiene buffer: `tick` no
// vuelve hasta que Run recibe el tic, así que un test sabe que Run estaba en su select.
type fakeTicker struct {
	c chan time.Time

	mu       sync.Mutex
	cadences []time.Duration
	stops    int
}

func newFakeTicker() *fakeTicker { return &fakeTicker{c: make(chan time.Time)} }

// start es la fábrica que se le pasa a WithTicker.
func (f *fakeTicker) start(cadence time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cadences = append(f.cadences, cadence)
	return f.c, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stops++
	}
}

// tick entrega un tic y espera a que Run lo recoja.
func (f *fakeTicker) tick(t *testing.T) {
	t.Helper()
	select {
	case f.c <- time.Time{}:
	case <-time.After(waitLimit):
		t.Fatalf("Run no recogió el tic en %v: no está en su select", waitLimit)
	}
}

// asked devuelve las cadencias con las que se pidió un ticker, y cuántas veces se paró.
func (f *fakeTicker) asked() (cadences []time.Duration, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.cadences...), f.stops
}

// Las operaciones del store que un test puede romper o espiar.
const (
	opClaim   = "claim"
	opAwake   = "awake"
	opRelease = "release"
	opRetry   = "retry"
	opFinish  = "finish"
	opFail    = "fail"
)

// closeCall es lo que una escritura de desenlace recibió: con qué ctx y con qué datos.
type closeCall struct {
	op          string
	jobID       string
	alive       bool          // el ctx NO estaba cancelado
	hasDeadline bool          // el ctx traía plazo
	budget      time.Duration // cuánto le quedaba
	mark        time.Time     // Retry: la marca
	text        string        // Fail: el motivo · Finish: el intake_id
}

// faultyStore es la máquina en memoria (MachineMemory) con tres cosas más: apunta cada
// escritura de desenlace, puede fallarla o «no aplicarla», y puede dejar un reclamo
// colgado hasta que el ctx muera (la parada a mitad de reclamo).
type faultyStore struct {
	*intakehelpertest.MachineMemory

	mu      sync.Mutex
	fail    map[string]error
	lost    map[string]bool
	blockOn string
	calls   []closeCall
	tenants []string

	once    sync.Once
	blocked chan struct{}
}

var _ intake.PipelineStore = (*faultyStore)(nil)

func newFaultyStore(mem *intakehelpertest.MachineMemory) *faultyStore {
	return &faultyStore{
		MachineMemory: mem,
		fail:          map[string]error{},
		lost:          map[string]bool{},
		blocked:       make(chan struct{}),
	}
}

// breakOp hace que esa operación devuelva `err`.
func (s *faultyStore) breakOp(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[op] = err
}

// loseOp hace que esa escritura devuelva (false, nil) sin tocar la fila.
func (s *faultyStore) loseOp(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost[op] = true
}

// hangOn deja colgado ese reclamo (opClaim u opAwake) hasta que su ctx muera.
func (s *faultyStore) hangOn(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockOn = op
}

// closes devuelve las escrituras de desenlace vistas, en orden.
func (s *faultyStore) closes() []closeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]closeCall(nil), s.calls...)
}

// closesOf devuelve las de una operación.
func (s *faultyStore) closesOf(op string) []closeCall {
	var out []closeCall
	for _, c := range s.closes() {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

// awakeTenants son los tenants con los que se pidió el reclamo por evento.
func (s *faultyStore) awakeTenants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...)
}

// claim es lo común a los dos reclamos: el fallo inyectado y el cuelgue.
func (s *faultyStore) claim(ctx context.Context, op string) error {
	s.mu.Lock()
	hang, failure := s.blockOn == op, s.fail[op]
	s.mu.Unlock()
	if hang {
		s.once.Do(func() { close(s.blocked) })
		<-ctx.Done()
		return ctx.Err()
	}
	return failure
}

func (s *faultyStore) ClaimNext(ctx context.Context) (intake.ClaimedJob, bool, error) {
	if err := s.claim(ctx, opClaim); err != nil {
		return intake.ClaimedJob{}, false, err
	}
	return s.MachineMemory.ClaimNext(ctx)
}

func (s *faultyStore) ClaimNextIgnoringBackoff(ctx context.Context, tenant string) (intake.ClaimedJob, bool, error) {
	s.mu.Lock()
	s.tenants = append(s.tenants, tenant)
	s.mu.Unlock()
	if err := s.claim(ctx, opAwake); err != nil {
		return intake.ClaimedJob{}, false, err
	}
	return s.MachineMemory.ClaimNextIgnoringBackoff(ctx, tenant)
}

// note apunta una escritura de desenlace y dice si hay que romperla o perderla.
func (s *faultyStore) note(ctx context.Context, c closeCall) (lost bool, err error) {
	c.alive = ctx.Err() == nil
	if dl, ok := ctx.Deadline(); ok {
		c.hasDeadline, c.budget = true, time.Until(dl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	return s.lost[c.op], s.fail[c.op]
}

func (s *faultyStore) Release(ctx context.Context, jobID string) (bool, error) {
	if lost, err := s.note(ctx, closeCall{op: opRelease, jobID: jobID}); err != nil || lost {
		return false, err
	}
	return s.MachineMemory.Release(ctx, jobID)
}

func (s *faultyStore) Retry(ctx context.Context, jobID string, next time.Time) (bool, error) {
	if lost, err := s.note(ctx, closeCall{op: opRetry, jobID: jobID, mark: next}); err != nil || lost {
		return false, err
	}
	return s.MachineMemory.Retry(ctx, jobID, next)
}

func (s *faultyStore) Finish(ctx context.Context, jobID, intakeID string) (bool, error) {
	if lost, err := s.note(ctx, closeCall{op: opFinish, jobID: jobID, text: intakeID}); err != nil || lost {
		return false, err
	}
	return s.MachineMemory.Finish(ctx, jobID, intakeID)
}

func (s *faultyStore) Fail(ctx context.Context, jobID, reason string) (bool, error) {
	if lost, err := s.note(ctx, closeCall{op: opFail, jobID: jobID, text: reason}); err != nil || lost {
		return false, err
	}
	return s.MachineMemory.Fail(ctx, jobID, reason)
}
