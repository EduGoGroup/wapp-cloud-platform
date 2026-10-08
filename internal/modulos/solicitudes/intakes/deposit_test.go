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

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// El texto del recordatorio de este fichero es un LITERAL calculado con el fichero
// viejo (internal/intakes/deposit.go @ 64c181a) para esta misma solicitud, esta
// plantilla y este reloj: el candado de fronteras impide importarlo desde aquí.

const (
	depositTenant  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	depositIntake  = "11111111-1111-1111-1111-111111111111"
	depositContact = "contacto-opaco-1"
	// depositPhone es PII: ningún log del recordatorio puede contenerlo.
	depositPhone    = "573015550101"
	depositTemplate = "Abona {total} a la cuenta 001-2 antes del {fecha_limite} ({plazo} días)."
	// depositReminderText es lo que recibe el cliente: el preámbulo de la plataforma,
	// una línea en blanco y la plantilla del tenant ya renderizada.
	depositReminderText = "⏰ Te recordamos que tu pedido sigue esperando la seña para quedar reservado. " +
		"Si ya la pagaste, avísanos por aquí y lo confirmamos." +
		"\n\nAbona $18000.00 a la cuenta 001-2 antes del 06/08/2026 (3 días)."
)

// depositNow es el instante fijo del toque: la seña vence ANTES y el toque ocurre AQUÍ.
var depositNow = time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

// --- dobles -----------------------------------------------------------------

// depositLog retiene todo lo emitido (nivel, mensaje y campos, también los que arrastra
// With) para poder afirmar que algo se registró y que el destino jamás aparece.
type depositLog struct {
	mu     *sync.Mutex
	lines  *[]string
	fields []any
}

func newDepositLog() *depositLog { return &depositLog{mu: &sync.Mutex{}, lines: &[]string{}} }

func (l *depositLog) record(level, msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, fmt.Sprint(level, " ", msg, " ", slices.Concat(l.fields, args)))
}

func (l *depositLog) Debug(msg string, args ...any) { l.record("DEBUG", msg, args) }
func (l *depositLog) Info(msg string, args ...any)  { l.record("INFO", msg, args) }
func (l *depositLog) Warn(msg string, args ...any)  { l.record("WARN", msg, args) }
func (l *depositLog) Error(msg string, args ...any) { l.record("ERROR", msg, args) }
func (l *depositLog) With(args ...any) logger.Logger {
	return &depositLog{mu: l.mu, lines: l.lines, fields: slices.Concat(l.fields, args)}
}

func (l *depositLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(*l.lines, "\n")
}

// depositMessage es un envío tal como lo vio el transporte.
type depositMessage struct{ sessionID, to, text string }

// depositSender registra los envíos. Con err no registra nada: imita al Gateway, que ante
// una sesión offline ni siquiera llega a empujar el comando.
type depositSender struct {
	mu      sync.Mutex
	sent    []depositMessage
	err     error
	panicky bool
}

func (s *depositSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panicky {
		panic("el transporte reventó")
	}
	if s.err != nil {
		return nil, s.err
	}
	s.sent = append(s.sent, depositMessage{sessionID: sessionID, to: to, text: text})
	return &cloudlinkv1.Ack{AckedCommandId: "cmd-ok", Ok: true}, nil
}

func (s *depositSender) messages() []depositMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// depositDestinations imita la vía custodiada de PII.
type depositDestinations struct{}

func (depositDestinations) Destino(context.Context, string, string) (contact.Ref, error) {
	return contact.Ref{Kind: contact.KindPhoneE164, Value: depositPhone}, nil
}

// depositSettings es la config del tenant, y cuenta cuántas veces se leyó.
type depositSettings struct {
	mu       sync.Mutex
	template string
	reads    int
}

func (s *depositSettings) NotifySettings(context.Context, string) (NotifySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return NotifySettings{DepositTemplate: s.template, DepositDueDays: 3}, nil
}

func (s *depositSettings) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// depositMark es una petición de marca tal como la vio el store.
type depositMark struct {
	tenantID, intakeID string
	at                 time.Time
}

// depositStore es un DepositStore de juguete con un compare-and-swap de verdad: la primera
// marca de cada solicitud gana y las demás pierden. Devuelve la fila VIGENTE que se le
// sembró (rows), que puede no ser la que traía el llamante.
type depositStore struct {
	mu       sync.Mutex
	rows     map[string]Intake
	lose     map[string]bool // solicitudes a las que ya no les toca
	reminded map[string]bool
	markErr  error
	panicky  bool
	marks    []depositMark

	pending      []Intake
	pendingErr   error
	pendingCalls []string // "tenant|contact|at|limit"
}

var _ DepositStore = (*depositStore)(nil)

func (s *depositStore) MarkDepositReminded(_ context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, depositMark{tenantID: tenantID, intakeID: intakeID, at: at})
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
	row.DepositRemindedAt = at
	return row, true, nil
}

func (s *depositStore) PendingDepositReminders(_ context.Context, tenantID, contactID string, at time.Time, limit int) ([]Intake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingCalls = append(s.pendingCalls, fmt.Sprintf("%s|%s|%s|%d", tenantID, contactID, at.Format(time.RFC3339), limit))
	return s.pending, s.pendingErr
}

func (s *depositStore) markCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marks)
}

// --- escenario --------------------------------------------------------------

// depositOverdue es una solicitud con la seña pedida y vencida hace una hora.
func depositOverdue(id string) Intake {
	return Intake{
		ID: id, ContactID: depositContact, SessionID: "sess-negocio",
		Status: StatusDepositRequested, Total: 18000,
		CreatedAt: depositNow.AddDate(0, 0, -10), UpdatedAt: depositNow.AddDate(0, 0, -10),
		DepositDueAt: depositNow.Add(-time.Hour),
	}
}

// depositScene es el recordatorio cableado entero, con todo lo que se puede mirar.
type depositScene struct {
	reminder *DepositReminder
	store    *depositStore
	sender   *depositSender
	settings *depositSettings
	log      *depositLog
}

// newDepositScene arma el recordatorio con el reloj fijo y las solicitudes dadas como filas
// vigentes del store.
func newDepositScene(rows ...Intake) *depositScene {
	sc := &depositScene{
		store:    &depositStore{rows: map[string]Intake{}, lose: map[string]bool{}},
		sender:   &depositSender{},
		settings: &depositSettings{template: depositTemplate},
		log:      newDepositLog(),
	}
	for _, row := range rows {
		sc.store.rows[row.ID] = row
	}
	sc.reminder = NewDepositReminder(NewNotifier(sc.sender, depositDestinations{}, sc.settings, sc.log), sc.store,
		WithReminderClock(func() time.Time { return depositNow }))
	return sc
}

// DepositReminder es lo que el Service invoca al tocar una solicitud.
var _ DepositTouch = (*DepositReminder)(nil)

// --- Remind -----------------------------------------------------------------

// TestRemind_OverdueDepositSendsTheReminder: el camino entero. Una seña vencida y sin
// recordar manda UN mensaje, por la sesión de la solicitud, al destino custodiado, con el
// preámbulo y la plantilla del tenant; y la marca se pide con el instante del reloj.
func TestRemind_OverdueDepositSendsTheReminder(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))

	sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})

	want := []depositMessage{{sessionID: "sess-negocio", to: depositPhone, text: depositReminderText}}
	if got := sc.sender.messages(); !slices.Equal(got, want) {
		t.Fatalf("envíos = %+v, quería %+v", got, want)
	}
	wantMark := []depositMark{{tenantID: depositTenant, intakeID: depositIntake, at: depositNow}}
	if !slices.Equal(sc.store.marks, wantMark) {
		t.Errorf("marcas pedidas = %+v, quería %+v", sc.store.marks, wantMark)
	}
}

// TestRemind_RendersWithTheRowTheStoreReturned: el texto sale de la fila que devolvió el
// compare-and-swap —la única que se sabe vigente—, no de la que traía el llamante.
func TestRemind_RendersWithTheRowTheStoreReturned(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	stale := depositOverdue(depositIntake)
	stale.Total = 1
	stale.DepositDueAt = depositNow.AddDate(0, -1, 0)

	sc.reminder.Remind(context.Background(), depositTenant, []Intake{stale})

	msgs := sc.sender.messages()
	if len(msgs) != 1 || msgs[0].text != depositReminderText {
		t.Fatalf("envíos = %+v, quería el texto con el total y la fecha de la fila vigente", msgs)
	}
}

// TestRemind_PreFilterSkipsWithoutTouchingAnything: lo que no puede ser candidato no llega
// ni al store ni a la config. El instante exacto del vencimiento ya es candidato.
func TestRemind_PreFilterSkipsWithoutTouchingAnything(t *testing.T) {
	t.Parallel()
	with := func(edit func(in *Intake)) Intake {
		in := depositOverdue(depositIntake)
		edit(&in)
		return in
	}
	cases := []struct {
		name      string
		in        Intake
		candidate bool
	}{
		{name: "overdue and unreminded", in: depositOverdue(depositIntake), candidate: true},
		{name: "due exactly now", in: with(func(in *Intake) { in.DepositDueAt = depositNow }), candidate: true},
		{name: "due one nanosecond from now", in: with(func(in *Intake) { in.DepositDueAt = depositNow.Add(time.Nanosecond) })},
		{name: "no due date", in: with(func(in *Intake) { in.DepositDueAt = time.Time{} })},
		{name: "already reminded", in: with(func(in *Intake) { in.DepositRemindedAt = depositNow.Add(-time.Minute) })},
		{name: "deposit already paid", in: with(func(in *Intake) { in.Status = StatusDepositPaid })},
		{name: "cancelled", in: with(func(in *Intake) { in.Status = StatusCancelled })},
		{name: "confirmed without a deposit", in: with(func(in *Intake) { in.Status = StatusConfirmed })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sc := newDepositScene(depositOverdue(depositIntake))

			sc.reminder.Remind(context.Background(), depositTenant, []Intake{c.in})

			if c.candidate {
				if len(sc.sender.messages()) != 1 {
					t.Fatalf("envíos = %d, quería 1", len(sc.sender.messages()))
				}
				return
			}
			if sc.store.markCount() != 0 || sc.settings.readCount() != 0 || len(sc.sender.messages()) != 0 {
				t.Errorf("hubo %d marcas, %d lecturas de config y %d envíos; quería 0, 0 y 0",
					sc.store.markCount(), sc.settings.readCount(), len(sc.sender.messages()))
			}
		})
	}
}

// TestRemind_NoTemplateNeitherSendsNorSpendsTheMark: sin plantilla de seña no hay nada útil
// que decir, y la marca NO se gasta: la config se lee antes del compare-and-swap. Si se
// gastara, el tenant que configure su plantilla mañana descubriría que sus clientes de hoy
// perdieron su único recordatorio.
func TestRemind_NoTemplateNeitherSendsNorSpendsTheMark(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.settings.template = ""

	sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})

	if sc.store.markCount() != 0 {
		t.Errorf("marcas pedidas = %d, quería 0: sin plantilla la marca queda libre", sc.store.markCount())
	}
	if len(sc.sender.messages()) != 0 {
		t.Errorf("envíos = %d, quería 0", len(sc.sender.messages()))
	}
}

// TestRemind_LosingTheMarkSendsNothing: lo que decide es el compare-and-swap. Si el store
// dice que no le tocaba (ya pagó, ya se recordó), no sale nada, y no es una avería.
func TestRemind_LosingTheMarkSendsNothing(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.lose[depositIntake] = true

	sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})

	if sc.store.markCount() != 1 || len(sc.sender.messages()) != 0 {
		t.Errorf("hubo %d marcas y %d envíos, quería 1 y 0", sc.store.markCount(), len(sc.sender.messages()))
	}
	if strings.Contains(sc.log.all(), "ERROR") {
		t.Errorf("perder la marca se registró como error:\n%s", sc.log.all())
	}
}

// TestRemind_StoreFailureSendsNothing: si la marca falla no se envía, queda registrado como
// error y la lectura que tocó la solicitud sigue su curso.
func TestRemind_StoreFailureSendsNothing(t *testing.T) {
	t.Parallel()
	sc := newDepositScene(depositOverdue(depositIntake))
	sc.store.markErr = errors.New("pool agotado")

	sc.reminder.Remind(context.Background(), depositTenant, []Intake{depositOverdue(depositIntake)})

	if len(sc.sender.messages()) != 0 {
		t.Errorf("envíos = %d, quería 0", len(sc.sender.messages()))
	}
	if !strings.Contains(sc.log.all(), "ERROR") {
		t.Errorf("el fallo de la marca no quedó registrado como error:\n%s", sc.log.all())
	}
}

// TestRemind_OneReminderPerTouch: una bandeja con tres señas vencidas manda UNO. Y una
// candidata que pierde la marca no cuenta: se sigue con la siguiente.
func TestRemind_OneReminderPerTouch(t *testing.T) {
	t.Parallel()
	touched := []Intake{depositOverdue("a"), depositOverdue("b"), depositOverdue("c")}

	sc := newDepositScene(touched...)
	sc.reminder.Remind(context.Background(), depositTenant, touched)
	if sc.store.markCount() != 1 || len(sc.sender.messages()) != 1 {
		t.Errorf("hubo %d marcas y %d envíos, quería 1 y 1", sc.store.markCount(), len(sc.sender.messages()))
	}

	sc = newDepositScene(touched...)
	sc.store.lose["a"] = true
	sc.reminder.Remind(context.Background(), depositTenant, touched)
	if sc.store.markCount() != 2 || len(sc.sender.messages()) != 1 {
		t.Errorf("con la primera perdida hubo %d marcas y %d envíos, quería 2 y 1", sc.store.markCount(), len(sc.sender.messages()))
	}
}

// TestRemind_FailedDeliveryStillCountsAsSent: el envío falla DESPUÉS de marcar. Es el error
// elegido: la marca ya se gastó, ese recordatorio «ocurrió» y el toque no sigue con la
// siguiente solicitud. Y la lectura no revienta.
func TestRemind_FailedDeliveryStillCountsAsSent(t *testing.T) {
	t.Parallel()
	touched := []Intake{depositOverdue("a"), depositOverdue("b")}
	sc := newDepositScene(touched...)
	sc.sender.err = errors.New("sesión offline")

	sc.reminder.Remind(context.Background(), depositTenant, touched)

	if sc.store.markCount() != 1 {
		t.Errorf("marcas pedidas = %d, quería 1: un envío fallido cuenta como recordatorio hecho", sc.store.markCount())
	}
}

// TestRemind_HalfWiredStaysSilent: un recordatorio a medias no recuerda, pero tampoco se
// lleva por delante la lectura que lo invocó.
func TestRemind_HalfWiredStaysSilent(t *testing.T) {
	t.Parallel()
	touched := []Intake{depositOverdue(depositIntake)}
	store := &depositStore{rows: map[string]Intake{depositIntake: depositOverdue(depositIntake)}}
	notifier := NewNotifier(&depositSender{}, depositDestinations{}, &depositSettings{template: depositTemplate}, newDepositLog())
	senderless := NewNotifier(nil, depositDestinations{}, &depositSettings{template: depositTemplate}, newDepositLog())

	cases := []struct {
		name     string
		reminder *DepositReminder
	}{
		{name: "nil receiver", reminder: nil},
		{name: "no notifier", reminder: NewDepositReminder(nil, store)},
		{name: "no store", reminder: NewDepositReminder(notifier, nil)},
		{name: "notifier without a sender", reminder: NewDepositReminder(senderless, store)},
	}
	for _, c := range cases {
		c.reminder.Remind(context.Background(), depositTenant, touched)
		if got := c.reminder.RemindContact(context.Background(), depositTenant, depositContact); got != nil {
			t.Errorf("%s: RemindContact = %v, quería nil", c.name, got)
		}
	}
	if store.markCount() != 0 || len(store.pendingCalls) != 0 {
		t.Errorf("un recordatorio a medias tocó el store: %d marcas, %d consultas", store.markCount(), len(store.pendingCalls))
	}
}

// --- el reloj ---------------------------------------------------------------

// TestWithReminderClock_DefaultsToTheRealClock: sin la opción, o con un reloj nil, manda
// time.Now. Una seña que vence dentro de un siglo no es candidata; una que venció hace una
// hora sí, y la marca se pide con un instante de ahora mismo.
func TestWithReminderClock_DefaultsToTheRealClock(t *testing.T) {
	t.Parallel()
	options := map[string][]ReminderOption{
		"no option": nil,
		"nil clock": {WithReminderClock(nil)},
	}
	for name, opts := range options {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			overdue := depositOverdue(depositIntake)
			overdue.DepositDueAt = time.Now().Add(-time.Hour)
			future := depositOverdue("future")
			future.DepositDueAt = time.Now().AddDate(100, 0, 0)

			store := &depositStore{rows: map[string]Intake{depositIntake: overdue, "future": future}}
			sender := &depositSender{}
			notifier := NewNotifier(sender, depositDestinations{}, &depositSettings{template: depositTemplate}, newDepositLog())
			reminder := NewDepositReminder(notifier, store, opts...)

			reminder.Remind(context.Background(), depositTenant, []Intake{future})
			if store.markCount() != 0 {
				t.Fatalf("una seña que vence dentro de un siglo pidió la marca")
			}
			reminder.Remind(context.Background(), depositTenant, []Intake{overdue})
			if store.markCount() != 1 || len(sender.messages()) != 1 {
				t.Fatalf("hubo %d marcas y %d envíos, quería 1 y 1", store.markCount(), len(sender.messages()))
			}
			if age := time.Since(store.marks[0].at); age < 0 || age > time.Minute {
				t.Errorf("la marca se pidió con un instante a %v de ahora; quería el reloj real", age)
			}
		})
	}
}
