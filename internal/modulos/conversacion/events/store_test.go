//go:build pendiente

package events

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Aserción de compilación: las firmas del ciclo de vida, que son las que consume el runtime por su
// puerto del almacén de eventos.
var _ interface {
	CreateEvent(ctx context.Context, in NewEvent) (Event, error)
	GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (Event, bool, error)
	GetEventForTenant(ctx context.Context, tenantID, eventID string) (Event, error)
	ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error)
	TransitionEvent(ctx context.Context, eventID string, to Status) error
	Touch(ctx context.Context, eventID string) error
	IsSuspended(e Event, ttl time.Duration) bool
} = (*Store)(nil)

// TestNewStore_DoesNotTouchTheDatabase: construir no manda ni una sentencia, y las opciones son
// funciones sobre el *Store.
func TestNewStore_DoesNotTouchTheDatabase(t *testing.T) {
	h := newFakeStore(t)
	if h.store == nil {
		t.Fatal("NewStore devolvió nil")
	}
	for _, option := range []Option{WithClock(time.Now), WithClock(nil)} {
		option(h.store)
	}
	requireStatements(t, h.fake, 0)
}

// TestWithClock_InjectsTheClock_NilIsIgnored: el reloj inyectado es el que usa el store, y un
// WithClock(nil) posterior no lo sustituye.
func TestWithClock_InjectsTheClock_NilIsIgnored(t *testing.T) {
	last := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ev := Event{Status: StatusOpen, LastActivityAt: last}
	// Con el reloj de pared, un evento tocado en 2026-08-09 con 1 h de TTL estaría vencido; con
	// el inyectado, parado 30 min después, no.
	store := NewStore(nil, nil, WithClock(func() time.Time { return last.Add(30 * time.Minute) }), WithClock(nil))
	if store.IsSuspended(ev, time.Hour) {
		t.Error("IsSuspended = true: el store no usa el reloj inyectado (o WithClock(nil) lo sustituyó)")
	}
}

// TestStore_IsSuspended_PureWithTheInjectedClock: es IsSuspended con el reloj del store: vivo y
// más de ttl sin actividad; ttl <= 0 y un terminal, nunca. No toca la base.
func TestStore_IsSuspended_PureWithTheInjectedClock(t *testing.T) {
	h := newFakeStore(t)
	cases := []struct {
		name   string
		status Status
		last   time.Time
		ttl    time.Duration
		want   bool
	}{
		{"past the ttl", StatusOpen, pgNow.Add(-2*time.Hour - time.Second), 2 * time.Hour, true},
		{"exactly at the ttl", StatusOpen, pgNow.Add(-2 * time.Hour), 2 * time.Hour, false},
		{"within the window", StatusOpen, pgNow.Add(-time.Hour), 2 * time.Hour, false},
		{"zero ttl", StatusOpen, pgNow.Add(-1000 * time.Hour), 0, false},
		{"negative ttl", StatusOpen, pgNow.Add(-1000 * time.Hour), -time.Second, false},
		{"terminal", StatusClosed, pgNow.Add(-1000 * time.Hour), time.Hour, false},
	}
	for _, c := range cases {
		if got := h.store.IsSuspended(Event{Status: c.status, LastActivityAt: c.last}, c.ttl); got != c.want {
			t.Errorf("%s: IsSuspended = %v, quería %v", c.name, got, c.want)
		}
	}
	requireStatements(t, h.fake, 0)
}

// TestCreateEvent_InsertsOpenWithTheClockAndMapsTheRow: UNA sentencia con los ocho argumentos —el
// HistoryID y el instante salen del reloj inyectado, en UTC— y la fila devuelta sale mapeada, con
// closed_at NULL a cero.
func TestCreateEvent_InsertsOpenWithTheClockAndMapsTheRow(t *testing.T) {
	h := newFakeStore(t, pgOne(pgEventRow(pgEvent, "cart", StatusOpen, nil)...))
	in := NewEvent{TenantID: pgTenant, SessionID: pgSession, ContactID: pgContact, Kind: "cart", FlowID: "flujo", FlowVersion: 4}

	ev, err := h.store.CreateEvent(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	requireSameEvent(t, "CreateEvent", ev, pgEventOf(pgEvent, "cart", StatusOpen, time.Time{}))
	if !ev.ClosedAt.IsZero() {
		t.Errorf("ClosedAt = %v, quería cero", ev.ClosedAt)
	}
	stmt := requireStatements(t, h.fake, 1)[0]
	requireArgs(t, "CreateEvent", stmt, pgTenant, pgSession, pgContact, "cart", "cart-2026-08-09-1830", "flujo", 4, pgNow)
	if !strings.Contains(stmt.query, "INSERT INTO public.conversation_events") || !strings.Contains(stmt.query, "'open'") {
		t.Errorf("la sentencia no inserta un evento open:\n%s", stmt.query)
	}
}

// TestCreateEvent_UniqueViolation_ErrAliveExists: la violación del único parcial sale como
// ErrAliveExists con la conversación y el tipo en el texto, y con la causa también envuelta.
func TestCreateEvent_UniqueViolation_ErrAliveExists(t *testing.T) {
	h := newFakeStore(t, pgReply{err: errPgUnique})
	ev, err := h.store.CreateEvent(t.Context(), NewEvent{TenantID: pgTenant, SessionID: pgSession, ContactID: pgContact, Kind: "cart"})
	var pg *pgconn.PgError
	if !errors.Is(err, ErrAliveExists) || !errors.As(err, &pg) {
		t.Fatalf("err = %v, quería ErrAliveExists envolviendo la violación de unicidad", err)
	}
	want := "events: ya existe un evento vivo de ese tipo en la conversación (tenant=" + pgTenant + " sesión=" + pgSession + " tipo=cart): "
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("texto = %q, quería el prefijo %q", err.Error(), want)
	}
	if ev != (Event{}) {
		t.Errorf("evento = %+v, quería el cero", ev)
	}
}

// TestCreateEvent_OtherFailure_Wrapped: cualquier otro fallo va envuelto con su texto y NO es
// ErrAliveExists.
func TestCreateEvent_OtherFailure_Wrapped(t *testing.T) {
	h := newFakeStore(t, pgFails())
	ev, err := h.store.CreateEvent(t.Context(), NewEvent{TenantID: pgTenant, Kind: "cart"})
	requireWrapped(t, "CreateEvent", err, "events: insertar evento: ", errPgBoom)
	if errors.Is(err, ErrAliveExists) || ev != (Event{}) {
		t.Errorf("(%+v, %v): quería el evento cero y un error que no sea ErrAliveExists", ev, err)
	}
}

// TestGetAliveByKind_RowNoRowAndFailure: con fila, el evento y true; sin fila, (cero, false, nil);
// el fallo, envuelto con el tipo citado. Acota por los cuatro y por status open.
func TestGetAliveByKind_RowNoRowAndFailure(t *testing.T) {
	h := newFakeStore(t, pgOne(pgEventRow(pgEvent, "cart", StatusOpen, nil)...), pgReply{}, pgFails())

	ev, found, err := h.store.GetAliveByKind(t.Context(), pgTenant, pgSession, pgContact, "cart")
	if err != nil || !found {
		t.Fatalf("con fila: (found=%v, %v)", found, err)
	}
	requireSameEvent(t, "GetAliveByKind", ev, pgEventOf(pgEvent, "cart", StatusOpen, time.Time{}))

	ev, found, err = h.store.GetAliveByKind(t.Context(), pgTenant, pgSession, pgContact, "cart")
	if err != nil || found || ev != (Event{}) {
		t.Errorf("sin fila: (%+v, %v, %v), quería (cero, false, nil)", ev, found, err)
	}

	ev, found, err = h.store.GetAliveByKind(t.Context(), pgTenant, pgSession, pgContact, "cart")
	requireWrapped(t, "GetAliveByKind", err, `events: leer evento vivo de tipo "cart": `, errPgBoom)
	if found || ev != (Event{}) {
		t.Errorf("con fallo: (%+v, %v), quería (cero, false)", ev, found)
	}

	stmts := requireStatements(t, h.fake, 3)
	requireArgs(t, "GetAliveByKind", stmts[0], pgTenant, pgSession, pgContact, "cart")
	if !strings.Contains(stmts[0].query, "status = 'open'") {
		t.Errorf("la lectura del vivo no acota por status open:\n%s", stmts[0].query)
	}
}

// TestGetEventForTenant_NotAUUID_NeverReachesTheDatabase: un id que no es UUID es ErrEventNotFound
// sin consultar.
func TestGetEventForTenant_NotAUUID_NeverReachesTheDatabase(t *testing.T) {
	h := newFakeStore(t)
	for _, id := range []string{"", "cart-2026-08-09-1830", "1 OR 1=1"} {
		ev, err := h.store.GetEventForTenant(t.Context(), pgTenant, id)
		if !errors.Is(err, ErrEventNotFound) || ev != (Event{}) {
			t.Errorf("id %q: (%+v, %v), quería (cero, ErrEventNotFound)", id, ev, err)
		}
	}
	_, err := h.store.GetEventForTenant(t.Context(), pgTenant, "x")
	if want := `events: el evento no existe para ese tenant (id="x" no es un UUID)`; err == nil || err.Error() != want {
		t.Errorf("texto = %v, quería %q", err, want)
	}
	requireStatements(t, h.fake, 0)
}

// TestGetEventForTenant_RowNoRowAndFailure: la fila sale mapeada en cualquier estado (closed_at
// incluido); sin fila es ErrEventNotFound con el id; el fallo, envuelto. La consulta lleva el id Y
// el tenant.
func TestGetEventForTenant_RowNoRowAndFailure(t *testing.T) {
	closedAt := time.Date(2026, 8, 9, 19, 0, 0, 0, time.UTC)
	h := newFakeStore(t, pgOne(pgEventRow(pgEvent, "cart", StatusCancelled, closedAt)...), pgReply{}, pgFails())

	ev, err := h.store.GetEventForTenant(t.Context(), pgTenant, pgEvent)
	if err != nil {
		t.Fatalf("con fila: %v", err)
	}
	requireSameEvent(t, "GetEventForTenant", ev, pgEventOf(pgEvent, "cart", StatusCancelled, closedAt))

	_, err = h.store.GetEventForTenant(t.Context(), pgTenant, pgEvent)
	if want := "events: el evento no existe para ese tenant (id=" + pgEvent + ")"; !errors.Is(err, ErrEventNotFound) || err.Error() != want {
		t.Errorf("sin fila: err = %v, quería %q", err, want)
	}

	_, err = h.store.GetEventForTenant(t.Context(), pgTenant, pgEvent)
	requireWrapped(t, "GetEventForTenant", err, "events: leer el evento del tenant: ", errPgBoom)
	if errors.Is(err, ErrEventNotFound) {
		t.Error("un fallo de la base no es ErrEventNotFound")
	}

	stmts := requireStatements(t, h.fake, 3)
	requireArgs(t, "GetEventForTenant", stmts[0], pgEvent, pgTenant)
	if !strings.Contains(stmts[0].query, "tenant_id = $2") {
		t.Errorf("la lectura no acota por tenant en el SQL:\n%s", stmts[0].query)
	}
}

// TestListAlive_MapsTheRowsInOrder: las filas salen en el orden de la base, mapeadas; sin filas,
// ninguna y sin error. Acota por la conversación y por status open, y ordena por nacimiento.
func TestListAlive_MapsTheRowsInOrder(t *testing.T) {
	other := "44444444-4444-4444-8444-444444444444"
	h := newFakeStore(t, pgReply{rows: [][]driver.Value{
		pgEventRow(pgEvent, "cart", StatusOpen, nil),
		pgEventRow(other, "survey", StatusOpen, nil),
	}}, pgReply{})

	evs, err := h.store.ListAlive(t.Context(), pgTenant, pgSession, pgContact)
	if err != nil || len(evs) != 2 {
		t.Fatalf("ListAlive = (%+v, %v), quería dos eventos", evs, err)
	}
	requireSameEvent(t, "primero", evs[0], pgEventOf(pgEvent, "cart", StatusOpen, time.Time{}))
	requireSameEvent(t, "segundo", evs[1], pgEventOf(other, "survey", StatusOpen, time.Time{}))

	evs, err = h.store.ListAlive(t.Context(), pgTenant, pgSession, pgContact)
	if err != nil || len(evs) != 0 {
		t.Errorf("sin filas: (%+v, %v), quería ninguno", evs, err)
	}

	stmt := requireStatements(t, h.fake, 2)[0]
	requireArgs(t, "ListAlive", stmt, pgTenant, pgSession, pgContact)
	if !strings.Contains(stmt.query, "status = 'open'") || !strings.Contains(stmt.query, "ORDER BY created_at, id") {
		t.Errorf("la lista de vivos no acota por open o no ordena por nacimiento:\n%s", stmt.query)
	}
}

// TestListAlive_Failures_Wrapped: los cuatro fallos del recorrido, cada uno con su texto (D-17:
// el del cierre solo si no había otro).
func TestListAlive_Failures_Wrapped(t *testing.T) {
	row := pgEventRow(pgEvent, "cart", StatusOpen, nil)
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query", pgFails(), "events: listar eventos vivos: ", errPgBoom},
		{"scan", pgOne("solo", "dos"), "events: leer fila de evento: ", nil},
		{"iteration", pgReply{rows: [][]driver.Value{row}, endErr: errPgBoom}, "events: recorrer eventos vivos: ", errPgBoom},
		{"close", pgReply{rows: [][]driver.Value{row}, closeErr: errPgBoom}, "events: cerrar filas de eventos: ", errPgBoom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeStore(t, c.reply)
			evs, err := h.store.ListAlive(t.Context(), pgTenant, pgSession, pgContact)
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || (c.cause != nil && !errors.Is(err, c.cause)) {
				t.Errorf("err = %v, quería el prefijo %q", err, c.prefix)
			}
			if evs != nil {
				t.Errorf("lista = %+v, quería nil", evs)
			}
		})
	}
}

// TestTransitionEvent_NotTerminal_NeverReachesTheDatabase: open (o cualquier otra cosa) no es un
// destino.
func TestTransitionEvent_NotTerminal_NeverReachesTheDatabase(t *testing.T) {
	h := newFakeStore(t)
	for _, to := range []Status{StatusOpen, "", "paused"} {
		if err := h.store.TransitionEvent(t.Context(), pgEvent, to); !errors.Is(err, ErrNotTerminal) {
			t.Errorf("destino %q: err = %v, quería ErrNotTerminal", to, err)
		}
	}
	err := h.store.TransitionEvent(t.Context(), pgEvent, StatusOpen)
	if want := `events: el destino de una transición debe ser closed o cancelled (recibido "open")`; err == nil || err.Error() != want {
		t.Errorf("texto = %v, quería %q", err, want)
	}
	requireStatements(t, h.fake, 0)
}

// TestTransitionEvent_CompareAndSwap: UNA sentencia con el id, el destino y el instante del reloj;
// el guard `status = 'open'` va en el SQL. Cero filas afectadas es ErrNotOpen; el fallo, envuelto.
func TestTransitionEvent_CompareAndSwap(t *testing.T) {
	h := newFakeStore(t, pgReply{}, pgReply{}, pgReply{noneAffected: true}, pgFails())

	if err := h.store.TransitionEvent(t.Context(), pgEvent, StatusClosed); err != nil {
		t.Fatalf("a closed: %v", err)
	}
	if err := h.store.TransitionEvent(t.Context(), pgEvent, StatusCancelled); err != nil {
		t.Fatalf("a cancelled: %v", err)
	}
	err := h.store.TransitionEvent(t.Context(), pgEvent, StatusClosed)
	if want := "events: el evento no está open (transición rechazada por el guard) (id=" + pgEvent + " destino=closed)"; !errors.Is(err, ErrNotOpen) || err.Error() != want {
		t.Errorf("sin filas afectadas: err = %v, quería %q", err, want)
	}
	err = h.store.TransitionEvent(t.Context(), pgEvent, StatusClosed)
	requireWrapped(t, "TransitionEvent", err, `events: transitar evento a "closed": `, errPgBoom)

	stmts := requireStatements(t, h.fake, 4)
	requireArgs(t, "a closed", stmts[0], pgEvent, "closed", pgNow)
	requireArgs(t, "a cancelled", stmts[1], pgEvent, "cancelled", pgNow)
	query := stmts[0].query
	if stmts[0].kind != pgExec || !strings.Contains(query, "status = 'open'") || strings.Contains(query, "last_activity_at") {
		t.Errorf("la transición no es un UPDATE con guard que solo toque status y closed_at:\n%s", query)
	}
}

// TestTouch_RefreshesOnlyTheClock: UNA sentencia con el id y el instante del reloj, que no nombra
// status. Cero filas afectadas es ErrEventMissing; el fallo, envuelto.
func TestTouch_RefreshesOnlyTheClock(t *testing.T) {
	h := newFakeStore(t, pgReply{}, pgReply{noneAffected: true}, pgFails())

	if err := h.store.Touch(t.Context(), pgEvent); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	err := h.store.Touch(t.Context(), pgEvent)
	if want := "events: el evento no existe (id=" + pgEvent + ")"; !errors.Is(err, ErrEventMissing) || err.Error() != want {
		t.Errorf("sin filas afectadas: err = %v, quería %q", err, want)
	}
	err = h.store.Touch(t.Context(), pgEvent)
	requireWrapped(t, "Touch", err, "events: refrescar el reloj del evento: ", errPgBoom)

	stmt := requireStatements(t, h.fake, 3)[0]
	requireArgs(t, "Touch", stmt, pgEvent, pgNow)
	if stmt.kind != pgExec || !strings.Contains(stmt.query, "last_activity_at = $2") || strings.Contains(stmt.query, "status") {
		t.Errorf("Touch no es un UPDATE que solo toque last_activity_at:\n%s", stmt.query)
	}
}
