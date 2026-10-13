package integrations_test

// Los dobles que comparten los worker_*_test.go: el reloj, el almacén espía, el log de prueba y
// los tres lectores falsos. Llevan la etiqueta `pendiente` mientras solo los usen tests en rojo:
// se la quita el verde de worker.go, a la vez que a worker_rig_test.go y a los worker_*_test.go
// (gate_test.go trae sus propios dobles a propósito, para poder ponerse en verde por separado).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// waitLimit acota cada espera de un test a una goroutine: pasado, el test falla en vez de colgarse.
const waitLimit = 10 * time.Second

// Los métodos del almacén por su nombre, para el espía.
const (
	opRecover   = "RecoverOrphanDeliveries"
	opClaim     = "ClaimWebhookBatch"
	opDelivered = "MarkWebhookDelivered"
	opFailed    = "MarkWebhookFailed"
	opDead      = "MarkWebhookDead"
	opGetTenant = "GetTenantIntegration"
	opGetSecret = "GetTenantSecret"
)

// testClock es un reloj que solo avanza cuando el test lo mueve.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// markCall es una llamada a una de las tres transiciones de cierre, tal como llegó al almacén.
type markCall struct {
	op     string
	claim  integrations.WebhookOutbox
	next   time.Time
	reason string
}

// spyStore envuelve un integrations.Store (el doble Memoria) y apunta lo que el worker le pide.
// Como Postgres —y al revés que Memoria—, respeta el contexto: con ctx cancelado devuelve su
// error sin tocar nada. Además puede hacer fallar un método (failWith) o dejarlo bloqueado hasta
// que el contexto se cancele (blockOn).
type spyStore struct {
	integrations.Store

	mu       sync.Mutex
	changed  *sync.Cond
	sequence []string
	limits   []int
	leases   []time.Duration
	marks    []markCall
	failWith map[string]error
	blockOn  string
	blocked  chan struct{}
	once     sync.Once
}

func newSpyStore(inner integrations.Store) *spyStore {
	s := &spyStore{Store: inner, failWith: map[string]error{}, blocked: make(chan struct{})}
	s.changed = sync.NewCond(&s.mu)
	return s
}

// fail hace que ese método devuelva err en vez de llegar al almacén.
func (s *spyStore) fail(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith[op] = err
}

// enter apunta la llamada y decide si el espía la contesta él: con el contexto cancelado, con el
// fallo guionizado o, si es el método bloqueado, esperando a la cancelación.
func (s *spyStore) enter(ctx context.Context, op string) (handled bool, err error) {
	s.mu.Lock()
	s.sequence = append(s.sequence, op)
	scripted, block := s.failWith[op], s.blockOn == op
	s.changed.Broadcast()
	s.mu.Unlock()

	if block {
		s.once.Do(func() { close(s.blocked) })
		<-ctx.Done()
	}
	if cerr := ctx.Err(); cerr != nil {
		return true, fmt.Errorf("integrations: %s: %w", op, cerr)
	}
	return scripted != nil, scripted
}

func (s *spyStore) RecoverOrphanDeliveries(ctx context.Context, lease time.Duration) (int, error) {
	s.mu.Lock()
	s.leases = append(s.leases, lease)
	s.mu.Unlock()
	if handled, err := s.enter(ctx, opRecover); handled {
		return 0, err
	}
	return s.Store.RecoverOrphanDeliveries(ctx, lease)
}

func (s *spyStore) ClaimWebhookBatch(ctx context.Context, limit int) ([]integrations.WebhookOutbox, error) {
	s.mu.Lock()
	s.limits = append(s.limits, limit)
	s.mu.Unlock()
	if handled, err := s.enter(ctx, opClaim); handled {
		return nil, err
	}
	return s.Store.ClaimWebhookBatch(ctx, limit)
}

func (s *spyStore) mark(call markCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, call)
}

func (s *spyStore) MarkWebhookDelivered(ctx context.Context, claim integrations.WebhookOutbox) error {
	s.mark(markCall{op: opDelivered, claim: claim})
	if handled, err := s.enter(ctx, opDelivered); handled {
		return err
	}
	return s.Store.MarkWebhookDelivered(ctx, claim)
}

func (s *spyStore) MarkWebhookFailed(ctx context.Context, claim integrations.WebhookOutbox, next time.Time, reason string) error {
	s.mark(markCall{op: opFailed, claim: claim, next: next, reason: reason})
	if handled, err := s.enter(ctx, opFailed); handled {
		return err
	}
	return s.Store.MarkWebhookFailed(ctx, claim, next, reason)
}

func (s *spyStore) MarkWebhookDead(ctx context.Context, claim integrations.WebhookOutbox, reason string) error {
	s.mark(markCall{op: opDead, claim: claim, reason: reason})
	if handled, err := s.enter(ctx, opDead); handled {
		return err
	}
	return s.Store.MarkWebhookDead(ctx, claim, reason)
}

func (s *spyStore) GetTenantIntegration(ctx context.Context, tenantID string) (integrations.TenantIntegration, bool, error) {
	if handled, err := s.enter(ctx, opGetTenant); handled {
		return integrations.TenantIntegration{}, false, err
	}
	return s.Store.GetTenantIntegration(ctx, tenantID)
}

func (s *spyStore) GetTenantSecret(ctx context.Context, tenantID string) (string, bool, error) {
	if handled, err := s.enter(ctx, opGetSecret); handled {
		return "", false, err
	}
	return s.Store.GetTenantSecret(ctx, tenantID)
}

// count dice cuántas llamadas a ese método han EMPEZADO.
func (s *spyStore) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countLocked(op)
}

func (s *spyStore) countLocked(op string) int {
	n := 0
	for _, seen := range s.sequence {
		if seen == op {
			n++
		}
	}
	return n
}

// waitCalls espera, sin dormir, a que hayan EMPEZADO al menos n llamadas a ese método.
func (s *spyStore) waitCalls(t *testing.T, op string, n int) {
	t.Helper()
	expired := false
	watchdog := time.AfterFunc(waitLimit, func() {
		s.mu.Lock()
		expired = true
		s.changed.Broadcast()
		s.mu.Unlock()
	})
	defer watchdog.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.countLocked(op) < n && !expired {
		s.changed.Wait()
	}
	if got := s.countLocked(op); got < n {
		t.Fatalf("el worker solo hizo %d llamadas a %s en %v, esperaba %d", got, op, waitLimit, n)
	}
}

// waitPolls espera a que n polls hayan TERMINADO. El worker es una sola goroutine y entrega en
// serie: que empiece el reclamo n+1 es la prueba de que el poll n acabó entero.
func (s *spyStore) waitPolls(t *testing.T, n int) {
	t.Helper()
	s.waitCalls(t, opClaim, n+1)
}

// waitFreshPoll espera a que termine entero un poll que EMPEZÓ después de esta llamada: es lo que
// usa un test tras mover el reloj o cambiar una fuente.
func (s *spyStore) waitFreshPoll(t *testing.T) {
	t.Helper()
	s.waitCalls(t, opClaim, s.count(opClaim)+2)
}

// calls devuelve, en orden, los métodos que el worker ha llamado.
func (s *spyStore) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sequence)
}

// marked devuelve las llamadas a las tres transiciones de cierre, en orden.
func (s *spyStore) marked() []markCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.marks)
}

// seenLimits y seenLeases devuelven los argumentos de los reclamos y de los rescates.
func (s *spyStore) seenLimits() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.limits)
}

func (s *spyStore) seenLeases() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.leases)
}

// recordingLog es un logger.Logger de prueba: retiene TODO lo emitido —nivel, mensaje y pares
// clave=valor, los del With incluidos— para afirmar tanto lo que se registró como lo que no.
type recordingLog struct {
	shared *logLines
	args   []any
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func newRecordingLog() *recordingLog { return &recordingLog{shared: &logLines{}} }

var _ logger.Logger = (*recordingLog)(nil)

func (l *recordingLog) record(level, msg string, args ...any) {
	var b strings.Builder
	b.WriteString(level + " " + msg + " |")
	all := append(slices.Clone(l.args), args...)
	for i := 0; i+1 < len(all); i += 2 {
		fmt.Fprintf(&b, " %v=%v", all[i], all[i+1])
	}
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	l.shared.lines = append(l.shared.lines, b.String())
}

func (l *recordingLog) Debug(msg string, args ...any) { l.record("DEBUG", msg, args...) }
func (l *recordingLog) Info(msg string, args ...any)  { l.record("INFO", msg, args...) }
func (l *recordingLog) Warn(msg string, args ...any)  { l.record("WARN", msg, args...) }
func (l *recordingLog) Error(msg string, args ...any) { l.record("ERROR", msg, args...) }
func (l *recordingLog) With(args ...any) logger.Logger {
	return &recordingLog{shared: l.shared, args: append(slices.Clone(l.args), args...)}
}

// at devuelve las líneas de ese nivel, en orden.
func (l *recordingLog) at(level string) []string {
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	var out []string
	for _, line := range l.shared.lines {
		if strings.HasPrefix(line, level+" ") {
			out = append(out, line)
		}
	}
	return out
}

// line devuelve la ÚNICA línea de ese nivel con exactamente ese mensaje, o falla el test.
func (l *recordingLog) line(t *testing.T, level, msg string) string {
	t.Helper()
	var found []string
	for _, line := range l.at(level) {
		if strings.HasPrefix(line, level+" "+msg+" |") {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("líneas %s %q = %d, quería 1; líneas de ese nivel:\n%s", level, msg, len(found), strings.Join(l.at(level), "\n"))
	}
	return found[0]
}

// requireNoErrors afirma que no hay NINGUNA línea a ERROR.
func (l *recordingLog) requireNoErrors(t *testing.T) {
	t.Helper()
	if lines := l.at("ERROR"); len(lines) != 0 {
		t.Errorf("el log tiene %d líneas a ERROR, quería ninguna:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// requireKeys afirma que la línea lleva todas esas claves (como clave=valor).
func requireKeys(t *testing.T, line string, pairs ...string) {
	t.Helper()
	for _, pair := range pairs {
		if !strings.Contains(line, " "+pair) {
			t.Errorf("la línea %q no lleva %q", line, pair)
		}
	}
}

// fakeBuyerData satisface BuyerDataReader con un mapa por intake_id, o con un error fijo.
type fakeBuyerData struct {
	mu    sync.Mutex
	data  map[string]intakes.BuyerData
	err   error
	asked []string
}

func (f *fakeBuyerData) GetBuyerData(_ context.Context, intakeID string) (intakes.BuyerData, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, intakeID)
	if f.err != nil {
		return nil, false, f.err
	}
	bd, ok := f.data[intakeID]
	return bd, ok, nil
}

func (f *fakeBuyerData) set(intakeID string, bd intakes.BuyerData) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = map[string]intakes.BuyerData{}
	}
	f.data[intakeID] = bd
}

func (f *fakeBuyerData) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// fakeCustomerNotes satisface CustomerNoteReader con un mapa por (tenant, intake). La clave es
// COMPUESTA a propósito: el almacén real filtra por tenant además de por id, y un doble que solo
// mirara el intake_id no distinguiría «la nota de otra empresa» de «no hay nota».
type fakeCustomerNotes struct {
	mu    sync.Mutex
	notes map[[2]string]string
	err   error
	asked [][2]string
}

func (f *fakeCustomerNotes) GetCustomerNote(_ context.Context, tenantID, intakeID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, [2]string{tenantID, intakeID})
	if f.err != nil {
		return "", false, f.err
	}
	note, ok := f.notes[[2]string{tenantID, intakeID}]
	return note, ok, nil
}

func (f *fakeCustomerNotes) set(tenantID, intakeID, note string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notes == nil {
		f.notes = map[[2]string]string{}
	}
	f.notes[[2]string{tenantID, intakeID}] = note
}

func (f *fakeCustomerNotes) calls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// fakeTenantVars satisface TenantVariablesReader con una lista por tenant, o con un error fijo.
type fakeTenantVars struct {
	mu    sync.Mutex
	vars  map[string][]tenantvars.Variable
	err   error
	asked []string
}

func (f *fakeTenantVars) List(_ context.Context, tenantID string) ([]tenantvars.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, tenantID)
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.vars[tenantID]), nil
}

func (f *fakeTenantVars) set(tenantID string, vars ...tenantvars.Variable) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vars == nil {
		f.vars = map[string][]tenantvars.Variable{}
	}
	f.vars[tenantID] = vars
}

func (f *fakeTenantVars) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}
