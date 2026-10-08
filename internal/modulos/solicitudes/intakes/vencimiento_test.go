//go:build pendiente

package intakes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// Las respuestas de Overdue y la traza del sumidero de este fichero son LITERALES
// calculados con el fichero viejo (internal/intakes/vencimiento.go @ 64c181a) para
// este mismo corpus: el candado de fronteras impide importarlo desde aquí.
//
// Ningún test de este fichero toca una base ni afirma que el dueño reciba nada: el
// plazo AVISA Y NO MATA, y el emisor de hoy es una traza (T-8). Los candados de
// invariante del plazo (R-06) no viven aquí todavía: llegan con su propia tarea.

const (
	expiryTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	expiryIntake = "11111111-1111-1111-1111-111111111111"
)

// expiryNow es el instante fijo del toque.
var expiryNow = time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

// --- dobles -----------------------------------------------------------------

// expiryLog retiene todo lo emitido: nivel, mensaje y campos (también los que arrastra
// With), con los campos separados por espacios.
type expiryLog struct {
	mu     *sync.Mutex
	lines  *[]string
	fields []any
}

func newExpiryLog() *expiryLog { return &expiryLog{mu: &sync.Mutex{}, lines: &[]string{}} }

func (l *expiryLog) record(level, msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, fmt.Sprintf("%s|%s|%v", level, msg, slices.Concat(l.fields, args)))
}

func (l *expiryLog) Debug(msg string, args ...any) { l.record("DEBUG", msg, args) }
func (l *expiryLog) Info(msg string, args ...any)  { l.record("INFO", msg, args) }
func (l *expiryLog) Warn(msg string, args ...any)  { l.record("WARN", msg, args) }
func (l *expiryLog) Error(msg string, args ...any) { l.record("ERROR", msg, args) }
func (l *expiryLog) With(args ...any) logger.Logger {
	return &expiryLog{mu: l.mu, lines: l.lines, fields: slices.Concat(l.fields, args)}
}

func (l *expiryLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(*l.lines)
}

func (l *expiryLog) has(level string) bool {
	return slices.ContainsFunc(l.all(), func(line string) bool { return strings.HasPrefix(line, level+"|") })
}

// expiryMark es una petición de marca tal como la vio el store.
type expiryMark struct {
	tenantID, intakeID string
	at                 time.Time
}

// expiryStore es un ExpiryStore de juguete con un compare-and-swap de verdad: la primera
// marca de cada solicitud gana y las demás pierden. Devuelve la fila VIGENTE que se le
// sembró (rows), que puede no ser la que traía el llamante.
type expiryStore struct {
	mu       sync.Mutex
	rows     map[string]Intake
	lose     map[string]bool // solicitudes a las que ya no les toca
	reminded map[string]bool
	markErr  error
	panicky  bool
	marks    []expiryMark
}

var _ ExpiryStore = (*expiryStore)(nil)

func (s *expiryStore) MarkExpiryReminded(_ context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, expiryMark{tenantID: tenantID, intakeID: intakeID, at: at})
	if s.panicky {
		panic("el pool reventó")
	}
	if s.markErr != nil {
		return Intake{}, false, s.markErr
	}
	if s.lose[intakeID] || s.reminded[intakeID] {
		return Intake{}, false, nil
	}
	if s.reminded == nil {
		s.reminded = map[string]bool{}
	}
	s.reminded[intakeID] = true
	row := s.rows[intakeID]
	row.ExpiryRemindedAt = at
	return row, true, nil
}

func (s *expiryStore) markCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marks)
}

// expiryOwnerSpy ocupa el sitio del emisor: apunta a quién se avisó y con qué fila.
type expiryOwnerSpy struct {
	mu      sync.Mutex
	tenants []string
	rows    []Intake
	panicky bool
}

var _ OwnerNotice = (*expiryOwnerSpy)(nil)

func (s *expiryOwnerSpy) RemindOwner(_ context.Context, tenantID string, in Intake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panicky {
		panic("el emisor reventó")
	}
	s.tenants = append(s.tenants, tenantID)
	s.rows = append(s.rows, in)
}

func (s *expiryOwnerSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// --- escenario --------------------------------------------------------------

// expiryWaiting es un presupuesto que espera al dueño desde hace `age`.
func expiryWaiting(id string, age time.Duration) Intake {
	return Intake{
		ID: id, ContactID: "contacto-opaco-1", SessionID: "sess-negocio",
		Status: StatusPendingApproval, Total: 18000,
		CreatedAt: expiryNow.Add(-age), UpdatedAt: expiryNow.Add(-age),
	}
}

// expiryScene es el recordatorio cableado entero, con todo lo que se puede mirar.
type expiryScene struct {
	reminder *ExpiryReminder
	store    *expiryStore
	owner    *expiryOwnerSpy
	log      *expiryLog
}

// newExpiryScene arma el recordatorio con el reloj fijo y las solicitudes dadas como filas
// vigentes del store.
func newExpiryScene(rows ...Intake) *expiryScene {
	sc := &expiryScene{
		store: &expiryStore{rows: map[string]Intake{}, lose: map[string]bool{}},
		owner: &expiryOwnerSpy{},
		log:   newExpiryLog(),
	}
	for _, row := range rows {
		sc.store.rows[row.ID] = row
	}
	sc.reminder = NewExpiryReminder(sc.owner, sc.store, sc.log, WithExpiryClock(func() time.Time { return expiryNow }))
	return sc
}

// ExpiryReminder es lo que el Service invoca al tocar una solicitud.
var _ ExpiryTouch = (*ExpiryReminder)(nil)

// --- QuoteDeadline y Overdue ------------------------------------------------

// TestQuoteDeadline_IsTwentyFourHours congela la constante de plataforma (R-06) y la
// CONDUCTA que produce: el instante exacto del plazo ya cuenta, un nanosegundo antes no.
func TestQuoteDeadline_IsTwentyFourHours(t *testing.T) {
	t.Parallel()
	if QuoteDeadline != 24*time.Hour {
		t.Fatalf("QuoteDeadline = %v, quería 24h", QuoteDeadline)
	}
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "one nanosecond short", age: 24*time.Hour - time.Nanosecond, want: false},
		{name: "exactly the deadline", age: 24 * time.Hour, want: true},
		{name: "one nanosecond over", age: 24*time.Hour + time.Nanosecond, want: true},
		{name: "23h59m", age: 23*time.Hour + 59*time.Minute, want: false},
		{name: "just touched", age: 0, want: false},
		{name: "touched in the future", age: -time.Hour, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Overdue(expiryWaiting(expiryIntake, c.age), expiryNow); got != c.want {
				t.Errorf("Overdue tras %v de espera = %v, quería %v", c.age, got, c.want)
			}
		})
	}
}

// TestOverdue_OnlyPendingApprovalWaits: con dos días de espera, solo `pending_approval`
// (normalizado, sin recortar ni plegar mayúsculas) sale marcado. Nada más espera la decisión
// de nadie: ni lo confirmado, ni la seña, ni lo cancelado, ni el `expired` legado.
func TestOverdue_OnlyPendingApprovalWaits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status string
		want   bool
	}{
		{status: "pending_approval", want: true},
		{status: " pending_approval ", want: false},
		{status: "PENDING_APPROVAL", want: false},
		{status: "needs_info", want: false},
		{status: "confirmed", want: false},
		{status: "closed", want: false},
		{status: "deposit_requested", want: false},
		{status: "cancelled", want: false},
		{status: "expired", want: false},
		{status: "open", want: false},
		{status: "abandoned", want: false},
		{status: "rejected", want: false},
		{status: "", want: false},
		{status: "draft", want: false},
	}
	for _, c := range cases {
		t.Run("status "+c.status, func(t *testing.T) {
			t.Parallel()
			in := expiryWaiting(expiryIntake, 48*time.Hour)
			in.Status = c.status
			if got := Overdue(in, expiryNow); got != c.want {
				t.Errorf("Overdue(%q) = %v, quería %v", c.status, got, c.want)
			}
		})
	}
}

// TestOverdue_BaseDate: la base es UpdatedAt; CreatedAt solo la suple cuando falta, y sin
// ninguna fecha NO se marca (una resta contra el tiempo cero marcaría todo). La marca no
// mira si ya se avisó, y compara instantes: la zona horaria no cambia la respuesta.
func TestOverdue_BaseDate(t *testing.T) {
	t.Parallel()
	old := expiryNow.Add(-48 * time.Hour)
	recent := expiryNow.Add(-time.Hour)
	cases := []struct {
		name string
		in   Intake
		want bool
	}{
		{name: "no dates at all", in: Intake{Status: StatusPendingApproval}, want: false},
		{name: "created at stands in when updated at is missing", in: Intake{Status: StatusPendingApproval, CreatedAt: old}, want: true},
		{name: "a recent update resets an old creation", in: Intake{Status: StatusPendingApproval, CreatedAt: old, UpdatedAt: recent}, want: false},
		{name: "an old update wins over a recent creation", in: Intake{Status: StatusPendingApproval, CreatedAt: expiryNow, UpdatedAt: old}, want: true},
		{name: "already reminded is still overdue", in: Intake{Status: StatusPendingApproval, UpdatedAt: old, ExpiryRemindedAt: expiryNow}, want: true},
		{name: "another time zone, same instant", in: Intake{
			Status:    StatusPendingApproval,
			UpdatedAt: expiryNow.Add(-24 * time.Hour).In(time.FixedZone("x", -5*3600)),
		}, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Overdue(c.in, expiryNow); got != c.want {
				t.Errorf("Overdue = %v, quería %v", got, c.want)
			}
		})
	}
}

// --- RemindOverdue ----------------------------------------------------------

// TestRemindOverdue_MarksThenNotifiesWithTheStoredRow: el camino entero. Un presupuesto
// vencido y sin avisar pide la marca con el instante del reloj y avisa UNA vez, con la fila
// que devolvió el store —la única que se sabe vigente— y no con la que traía el llamante.
func TestRemindOverdue_MarksThenNotifiesWithTheStoredRow(t *testing.T) {
	t.Parallel()
	sc := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	stale := expiryWaiting(expiryIntake, 72*time.Hour)
	stale.Total = 1

	sc.reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{stale})

	wantMark := []expiryMark{{tenantID: expiryTenant, intakeID: expiryIntake, at: expiryNow}}
	if !slices.Equal(sc.store.marks, wantMark) {
		t.Fatalf("marcas pedidas = %+v, quería %+v", sc.store.marks, wantMark)
	}
	if sc.owner.count() != 1 {
		t.Fatalf("avisos al dueño = %d, quería 1", sc.owner.count())
	}
	got := sc.owner.rows[0]
	if sc.owner.tenants[0] != expiryTenant || got.ID != expiryIntake || got.Total != 18000 || !got.ExpiryRemindedAt.Equal(expiryNow) {
		t.Errorf("se avisó con (%q, %+v), quería el tenant y la fila que devolvió el store", sc.owner.tenants[0], got)
	}
	// El plazo avisa y no mata: la fila con la que se avisa sigue esperando al dueño.
	if got.Status != StatusPendingApproval {
		t.Errorf("la fila del aviso está en %q, quería pending_approval", got.Status)
	}
}

// TestRemindOverdue_PreFilterSkipsWithoutTouchingTheStore: el pre-filtro son DOS preguntas.
// Lo que no está vencido y lo que ya se avisó no llegan al store: una bandeja con veinte
// vencidos ya avisados no ejecuta ni una sentencia.
func TestRemindOverdue_PreFilterSkipsWithoutTouchingTheStore(t *testing.T) {
	t.Parallel()
	reminded := expiryWaiting("reminded", 72*time.Hour)
	reminded.ExpiryRemindedAt = expiryNow.Add(-time.Hour)
	decided := expiryWaiting("decided", 72*time.Hour)
	decided.Status = StatusConfirmed
	touched := []Intake{expiryWaiting("in-time", time.Hour), reminded, decided, {ID: "no-dates", Status: StatusPendingApproval}}
	sc := newExpiryScene(touched...)

	sc.reminder.RemindOverdue(context.Background(), expiryTenant, touched)

	if sc.store.markCount() != 0 || sc.owner.count() != 0 {
		t.Errorf("hubo %d marcas y %d avisos, quería 0 y 0", sc.store.markCount(), sc.owner.count())
	}
}

// TestRemindOverdue_LosingTheMarkNotifiesNobody: lo que decide es el compare-and-swap. Si el
// store dice que no le tocaba (ya se avisó, el dueño ya decidió), no se avisa y no es una
// avería.
func TestRemindOverdue_LosingTheMarkNotifiesNobody(t *testing.T) {
	t.Parallel()
	sc := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	sc.store.lose[expiryIntake] = true

	sc.reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{expiryWaiting(expiryIntake, 72*time.Hour)})

	if sc.store.markCount() != 1 || sc.owner.count() != 0 {
		t.Errorf("hubo %d marcas y %d avisos, quería 1 y 0", sc.store.markCount(), sc.owner.count())
	}
	if sc.log.has("ERROR") {
		t.Errorf("perder la marca se registró como error: %v", sc.log.all())
	}
}

// TestRemindOverdue_StoreFailureNotifiesNobody: si la marca falla no se avisa, queda
// registrado como error y la lectura que tocó la solicitud sigue su curso.
func TestRemindOverdue_StoreFailureNotifiesNobody(t *testing.T) {
	t.Parallel()
	sc := newExpiryScene(expiryWaiting(expiryIntake, 72*time.Hour))
	sc.store.markErr = errors.New("pool agotado")

	sc.reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{expiryWaiting(expiryIntake, 72*time.Hour)})

	if sc.owner.count() != 0 {
		t.Errorf("avisos al dueño = %d, quería 0", sc.owner.count())
	}
	if !sc.log.has("ERROR") {
		t.Errorf("el fallo de la marca no quedó registrado como error: %v", sc.log.all())
	}
}

// TestRemindOverdue_OneNoticePerTouch: una bandeja con cinco presupuestos vencidos avisa de
// UNO. Y una candidata que pierde la marca no cuenta: se sigue con la siguiente.
func TestRemindOverdue_OneNoticePerTouch(t *testing.T) {
	t.Parallel()
	var touched []Intake
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		touched = append(touched, expiryWaiting(id, 72*time.Hour))
	}

	sc := newExpiryScene(touched...)
	sc.reminder.RemindOverdue(context.Background(), expiryTenant, touched)
	if sc.store.markCount() != 1 || sc.owner.count() != 1 {
		t.Errorf("hubo %d marcas y %d avisos, quería 1 y 1", sc.store.markCount(), sc.owner.count())
	}

	sc = newExpiryScene(touched...)
	sc.store.lose["a"] = true
	sc.reminder.RemindOverdue(context.Background(), expiryTenant, touched)
	if sc.store.markCount() != 2 || sc.owner.count() != 1 || sc.owner.rows[0].ID != "b" {
		t.Errorf("con la primera perdida hubo %d marcas y %d avisos, quería 2 y 1 (por b)", sc.store.markCount(), sc.owner.count())
	}
}

// TestWithExpiryClock_DefaultsToTheRealClock: sin la opción, o con un reloj nil, manda
// time.Now. Un presupuesto recién tocado no es candidato; uno de hace dos días sí, y la
// marca se pide con un instante de ahora mismo.
func TestWithExpiryClock_DefaultsToTheRealClock(t *testing.T) {
	t.Parallel()
	options := map[string][]ExpiryOption{
		"no option": nil,
		"nil clock": {WithExpiryClock(nil)},
	}
	for name, opts := range options {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fresh := Intake{ID: "fresh", Status: StatusPendingApproval, UpdatedAt: time.Now().Add(time.Hour)}
			old := Intake{ID: expiryIntake, Status: StatusPendingApproval, UpdatedAt: time.Now().Add(-48 * time.Hour)}
			store := &expiryStore{rows: map[string]Intake{"fresh": fresh, expiryIntake: old}}
			owner := &expiryOwnerSpy{}
			reminder := NewExpiryReminder(owner, store, newExpiryLog(), opts...)

			reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{fresh})
			if store.markCount() != 0 {
				t.Fatalf("un presupuesto recién tocado pidió la marca")
			}
			reminder.RemindOverdue(context.Background(), expiryTenant, []Intake{old})
			if store.markCount() != 1 || owner.count() != 1 {
				t.Fatalf("hubo %d marcas y %d avisos, quería 1 y 1", store.markCount(), owner.count())
			}
			if age := time.Since(store.marks[0].at); age < 0 || age > time.Minute {
				t.Errorf("la marca se pidió con un instante a %v de ahora; quería el reloj real", age)
			}
		})
	}
}

// --- el sumidero de HOY -----------------------------------------------------

// TestLogOwnerNotice_LeavesOneTraceWithoutPII congela lo decidido (T-8): el emisor real no
// existe y el de hoy escribe UNA línea. Se miran las dos cosas: que la traza sale con sus
// campos, y que no lleva ni el contacto ni el total.
func TestLogOwnerNotice_LeavesOneTraceWithoutPII(t *testing.T) {
	t.Parallel()
	log := newExpiryLog()
	in := expiryWaiting(expiryIntake, 72*time.Hour)

	NewLogOwnerNotice(log).RemindOwner(context.Background(), expiryTenant, in)

	want := "INFO|recordatorio de plazo: el presupuesto lleva más del plazo esperando al dueño|" +
		"[intake_id 11111111-1111-1111-1111-111111111111 tenant_id aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa " +
		"plazo_horas 24 emisor traza pendiente el canal real es el push del Plan 045; hoy nadie recibe esto]"
	if got := log.all(); !slices.Equal(got, []string{want}) {
		t.Fatalf("traza = %q, quería exactamente %q", got, want)
	}
	for _, pii := range []string{in.ContactID, "18000"} {
		if strings.Contains(log.all()[0], pii) {
			t.Errorf("la traza lleva %q, que no tiene por qué acabar en un log", pii)
		}
	}
}

// TestLogOwnerNotice_IsATraceNotTheRealChannel deja escrito lo que nadie puede afirmar: que
// el dueño recibe el recordatorio. NO lo recibe. El sumidero implementa el puerto y no manda
// nada a ninguna parte; sin log, ni siquiera deja traza, y no revienta. El día que exista el
// emisor real este test se cambia por uno que compruebe el envío.
func TestLogOwnerNotice_IsATraceNotTheRealChannel(t *testing.T) {
	t.Parallel()
	var notice OwnerNotice = NewLogOwnerNotice(newExpiryLog())
	if _, isTrace := notice.(*LogOwnerNotice); !isTrace {
		t.Fatalf("el emisor de hoy es %T, quería el sumidero de traza", notice)
	}

	var missing *LogOwnerNotice
	missing.RemindOwner(context.Background(), expiryTenant, expiryWaiting(expiryIntake, 72*time.Hour))
	NewLogOwnerNotice(nil).RemindOwner(context.Background(), expiryTenant, expiryWaiting(expiryIntake, 72*time.Hour))
}
