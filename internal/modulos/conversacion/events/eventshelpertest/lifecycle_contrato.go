package eventshelpertest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// El ciclo de vida del evento: nacer, leerlo, transitarlo y refrescar su reloj.

// caseCreateBornOpen: el evento nace vivo, con las seis columnas del llamador tal cual, un id UUID
// nuevo, nacimiento y última actividad en el MISMO instante del reloj inyectado, el HistoryID de
// ese instante y closed_at a cero. Y lo que devuelve es lo que quedó guardado.
func caseCreateBornOpen(t *testing.T, m Montaje) {
	m.Advance(t, 90*time.Minute)
	born := m.Now(t)
	c := newConversation(m.TenantA)
	before := take(t, m)

	ev, err := m.Store.CreateEvent(t.Context(), c.input(kindCart))
	requireNoError(t, "CreateEvent", err)

	if _, perr := uuid.Parse(ev.ID); perr != nil {
		t.Errorf("ID = %q, quería un UUID: %v", ev.ID, perr)
	}
	want := events.Event{
		ID: ev.ID, TenantID: c.tenant, SessionID: c.session, ContactID: c.contact, Kind: kindCart,
		HistoryID: events.HistoryID(kindCart, born), Status: events.StatusOpen,
		FlowID: flowID, FlowVersion: flowVersion, CreatedAt: born, LastActivityAt: born,
	}
	if !sameEvent(ev, want) {
		t.Errorf("CreateEvent = %+v, quería %+v", ev, want)
	}
	if !ev.ClosedAt.IsZero() {
		t.Errorf("ClosedAt = %v, quería el instante cero", ev.ClosedAt)
	}
	after := requireOnlyChanged(t, "CreateEvent", m, before, ev.ID)
	if !sameEvent(after[ev.ID].row, want) {
		t.Errorf("fila guardada = %+v, quería %+v", after[ev.ID].row, want)
	}
	if n := len(after[ev.ID].entries); n != 0 {
		t.Errorf("el evento nace con %d entradas de historial, quería 0", n)
	}
}

// caseCreateSecondAlive: con un vivo de ese tipo en la conversación, el segundo se rechaza con
// ErrAliveExists, devuelve el evento cero y no escribe nada, aunque traiga otro flujo.
func caseCreateSecondAlive(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	mustCreate(t, m, c, kindCart)
	seedWitnesses(t, m, c, kindCart)
	m.Advance(t, time.Minute)
	before := take(t, m)

	in := c.input(kindCart)
	in.FlowID, in.FlowVersion = "otro-flujo", 9
	ev, err := m.Store.CreateEvent(t.Context(), in)
	requireIs(t, "segundo CreateEvent", err, events.ErrAliveExists)
	if ev != (events.Event{}) {
		t.Errorf("el rechazo devolvió %+v, quería el evento cero", ev)
	}
	requireUnchanged(t, "segundo CreateEvent", m, before)
}

// caseCreateSlotIsTheWholeKey: el «uno vivo» es por (tenant, sesión, contacto, tipo): otro tipo,
// otra sesión, otro contacto u otro tenant no chocan con el primero.
func caseCreateSlotIsTheWholeKey(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	first := mustCreate(t, m, c, kindCart)
	before := take(t, m)

	w := seedWitnesses(t, m, c, kindCart)

	requireOnlyChanged(t, "los cuatro vecinos", m, before,
		w.otherKind.ID, w.otherSession.ID, w.otherContact.ID, w.otherTenant.ID)
	if got := mustGet(t, m, c.tenant, first.ID); !sameEvent(got, first) {
		t.Errorf("el primero cambió: %+v, era %+v", got, first)
	}
}

// caseCreateAfterTerminal: cerrar o cancelar libera el tipo: nace uno nuevo, con otro id, y el
// terminal queda como estaba.
func caseCreateAfterTerminal(t *testing.T, m Montaje) {
	for _, to := range []events.Status{events.StatusClosed, events.StatusCancelled} {
		c := newConversation(m.TenantA)
		old := mustCreate(t, m, c, kindCart)
		mustTransition(t, m, old.ID, to)
		before := take(t, m)

		fresh, err := m.Store.CreateEvent(t.Context(), c.input(kindCart))
		requireNoError(t, "CreateEvent tras "+string(to), err)
		if fresh.ID == old.ID || !fresh.Alive() {
			t.Errorf("tras %s nació %+v; quería un evento vivo con otro id", to, fresh)
		}
		requireOnlyChanged(t, "CreateEvent tras "+string(to), m, before, fresh.ID)
	}
}

// caseGetAliveByKind: devuelve el vivo de ESE tipo en ESA conversación, entero; no haberlo es
// (cero, false, nil). Ni el de otro tipo, ni el de otra sesión, contacto o tenant, ni uno terminal.
func caseGetAliveByKind(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	if ev, found, err := m.Store.GetAliveByKind(t.Context(), c.tenant, c.session, c.contact, kindCart); err != nil || found || ev != (events.Event{}) {
		t.Fatalf("sin eventos: (%+v, %v, %v), quería (cero, false, nil)", ev, found, err)
	}

	cart := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	before := take(t, m)

	got, found, err := m.Store.GetAliveByKind(t.Context(), c.tenant, c.session, c.contact, kindCart)
	if err != nil || !found || !sameEvent(got, cart) {
		t.Errorf("el cart: (%+v, %v, %v), quería (%+v, true, nil)", got, found, err, cart)
	}
	got, found, err = m.Store.GetAliveByKind(t.Context(), c.tenant, c.session, c.contact, kindSurvey)
	if err != nil || !found || !sameEvent(got, w.otherKind) {
		t.Errorf("el survey: (%+v, %v, %v), quería (%+v, true, nil)", got, found, err, w.otherKind)
	}
	if _, found, err = m.Store.GetAliveByKind(t.Context(), c.tenant, c.session, c.contact, kindMedia); err != nil || found {
		t.Errorf("un tipo sin evento: (found=%v, %v), quería (false, nil)", found, err)
	}

	mustTransition(t, m, cart.ID, events.StatusClosed)
	if ev, found, err := m.Store.GetAliveByKind(t.Context(), c.tenant, c.session, c.contact, kindCart); err != nil || found || ev != (events.Event{}) {
		t.Errorf("con el cart cerrado: (%+v, %v, %v), quería (cero, false, nil)", ev, found, err)
	}
	requireOnlyChanged(t, "las lecturas no escriben", m, before, cart.ID)
}

// caseGetForTenant: lee el evento de su tenant en CUALQUIER estado, con sus doce columnas.
func caseGetForTenant(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	open := mustCreate(t, m, c, kindCart)
	closed := mustCreate(t, m, c, kindSurvey)
	cancelled := mustCreate(t, m, c, kindMenu)
	m.Advance(t, time.Minute)
	mustTransition(t, m, closed.ID, events.StatusClosed)
	mustTransition(t, m, cancelled.ID, events.StatusCancelled)
	world := take(t, m)

	for _, ev := range []events.Event{open, closed, cancelled} {
		got, err := m.Store.GetEventForTenant(t.Context(), c.tenant, ev.ID)
		requireNoError(t, "GetEventForTenant", err)
		if !sameEvent(got, world[ev.ID].row) {
			t.Errorf("GetEventForTenant(%s) = %+v, quería %+v", ev.Kind, got, world[ev.ID].row)
		}
	}
	if got := mustGet(t, m, c.tenant, closed.ID); got.Status != events.StatusClosed || !got.ClosedAt.Equal(m.Now(t)) {
		t.Errorf("el cerrado = %+v; quería status closed y closed_at %v", got, m.Now(t))
	}
	requireUnchanged(t, "GetEventForTenant", m, world)
}

// caseGetForTenantNotFound: un id inexistente, el id de un evento de OTRO tenant y un id que ni es
// un UUID son la MISMA ausencia: ErrEventNotFound y el evento cero.
func caseGetForTenantNotFound(t *testing.T, m Montaje) {
	mine := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	before := take(t, m)

	cases := []struct{ name, tenant, id string }{
		{"unknown id", m.TenantA, uuid.NewString()},
		{"id of another tenant", m.TenantB, mine.ID},
		{"not a uuid", m.TenantA, "cart-2026-08-09-1830"},
		{"empty id", m.TenantA, ""},
	}
	for _, c := range cases {
		ev, err := m.Store.GetEventForTenant(t.Context(), c.tenant, c.id)
		requireIs(t, c.name, err, events.ErrEventNotFound)
		if ev != (events.Event{}) {
			t.Errorf("%s: devolvió %+v, quería el evento cero", c.name, ev)
		}
	}
	requireUnchanged(t, "GetEventForTenant", m, before)
}

// caseListAlive: los vivos de la conversación en orden de NACIMIENTO (no de actividad), enteros;
// sin los terminales ni los de otra sesión, contacto o tenant.
func caseListAlive(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	if evs, err := m.Store.ListAlive(t.Context(), c.tenant, c.session, c.contact); err != nil || len(evs) != 0 {
		t.Fatalf("sin eventos: (%v, %v), quería ninguno", evs, err)
	}

	cart := mustCreate(t, m, c, kindCart)
	m.Advance(t, time.Minute)
	w := seedWitnesses(t, m, c, kindCart) // el survey de la conversación nace aquí
	m.Advance(t, time.Minute)
	menu := mustCreate(t, m, c, kindMenu)
	m.Advance(t, time.Minute)
	media := mustCreate(t, m, c, kindMedia)
	// Tocar el más viejo no lo mueve: el orden es el de nacimiento.
	m.Advance(t, time.Minute)
	mustTouch(t, m, cart.ID)
	mustTransition(t, m, menu.ID, events.StatusCancelled)
	world := take(t, m)

	evs, err := m.Store.ListAlive(t.Context(), c.tenant, c.session, c.contact)
	requireNoError(t, "ListAlive", err)
	requireIDs(t, "ListAlive", ids(evs), []string{cart.ID, w.otherKind.ID, media.ID})
	for _, ev := range evs {
		if !sameEvent(ev, world[ev.ID].row) {
			t.Errorf("ListAlive devolvió %+v, quería %+v", ev, world[ev.ID].row)
		}
	}
	requireUnchanged(t, "ListAlive", m, world)
}

// caseTransition: closed y cancelled sellan status y closed_at —con el reloj inyectado— y NADA
// más: last_activity_at y el resto de la fila quedan como estaban.
func caseTransition(t *testing.T, m Montaje) {
	for _, to := range []events.Status{events.StatusClosed, events.StatusCancelled} {
		c := newConversation(m.TenantA)
		ev := mustCreate(t, m, c, kindCart)
		seedWitnesses(t, m, c, kindCart)
		m.Advance(t, 45*time.Minute)
		before := take(t, m)

		requireNoError(t, "TransitionEvent a "+string(to), m.Store.TransitionEvent(t.Context(), ev.ID, to))

		after := requireOnlyChanged(t, "TransitionEvent a "+string(to), m, before, ev.ID)
		want := ev
		want.Status, want.ClosedAt = to, m.Now(t)
		if got := after[ev.ID].row; !sameEvent(got, want) {
			t.Errorf("tras transitar a %s la fila es %+v, quería %+v", to, got, want)
		}
	}
}

// caseTransitionNotTerminal: open (y cualquier cosa que no sea closed o cancelled) no es un
// destino: ErrNotTerminal, sin escribir.
func caseTransitionNotTerminal(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	m.Advance(t, time.Minute)
	before := take(t, m)

	for _, to := range []events.Status{events.StatusOpen, "", "paused", "expired"} {
		requireIs(t, "TransitionEvent a "+string(to), m.Store.TransitionEvent(t.Context(), ev.ID, to), events.ErrNotTerminal)
	}
	requireUnchanged(t, "TransitionEvent a un destino no terminal", m, before)
}

// caseTransitionNotOpen: un evento terminal no transita —ni al otro terminal ni al suyo— y un id
// que no existe tampoco: ErrNotOpen, sin tocar el estado ni el closed_at ya sellado.
func caseTransitionNotOpen(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	closed := mustCreate(t, m, c, kindCart)
	cancelled := mustCreate(t, m, c, kindSurvey)
	mustTransition(t, m, closed.ID, events.StatusClosed)
	mustTransition(t, m, cancelled.ID, events.StatusCancelled)
	m.Advance(t, time.Hour)
	before := take(t, m)

	attempts := []struct {
		name string
		id   string
		to   events.Status
	}{
		{"closed to cancelled", closed.ID, events.StatusCancelled},
		{"closed to closed", closed.ID, events.StatusClosed},
		{"cancelled to closed", cancelled.ID, events.StatusClosed},
		{"cancelled to cancelled", cancelled.ID, events.StatusCancelled},
		{"unknown id", uuid.NewString(), events.StatusClosed},
	}
	for _, a := range attempts {
		requireIs(t, a.name, m.Store.TransitionEvent(t.Context(), a.id, a.to), events.ErrNotOpen)
	}
	requireUnchanged(t, "TransitionEvent sobre un evento que no está vivo", m, before)
}

// caseTouch: Touch estampa last_activity_at con el reloj inyectado y NADA más —ni status, ni
// closed_at, ni el nacimiento—, también sobre un evento terminal.
func caseTouch(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	alive := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	mustTransition(t, m, w.otherKind.ID, events.StatusClosed)
	m.Advance(t, 3*time.Hour)
	before := take(t, m)

	requireNoError(t, "Touch del vivo", m.Store.Touch(t.Context(), alive.ID))
	after := requireOnlyChanged(t, "Touch del vivo", m, before, alive.ID)
	want := before[alive.ID].row
	want.LastActivityAt = m.Now(t)
	if got := after[alive.ID].row; !sameEvent(got, want) {
		t.Errorf("tras Touch la fila es %+v, quería %+v", got, want)
	}

	requireNoError(t, "Touch del terminal", m.Store.Touch(t.Context(), w.otherKind.ID))
	final := requireOnlyChanged(t, "Touch del terminal", m, after, w.otherKind.ID)
	want = after[w.otherKind.ID].row
	want.LastActivityAt = m.Now(t)
	if got := final[w.otherKind.ID].row; !sameEvent(got, want) || got.Status != events.StatusClosed {
		t.Errorf("tras Touch el terminal es %+v, quería %+v", got, want)
	}
}

// caseTouchMissing: un id que no existe da ErrEventMissing y no escribe nada.
func caseTouchMissing(t *testing.T, m Montaje) {
	mustCreate(t, m, newConversation(m.TenantA), kindCart)
	m.Advance(t, time.Minute)
	before := take(t, m)

	requireIs(t, "Touch de un id desconocido", m.Store.Touch(t.Context(), uuid.NewString()), events.ErrEventMissing)
	requireUnchanged(t, "Touch de un id desconocido", m, before)
}

// caseIsSuspended: es events.IsSuspended con el reloj INYECTADO del almacén —no el de pared—:
// vivo y sin interacción durante MÁS de ttl. ttl <= 0 nunca suspende, un terminal tampoco, y
// preguntar no escribe nada.
func caseIsSuspended(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	ttl := 2 * time.Hour

	if m.Store.IsSuspended(ev, ttl) {
		t.Error("recién nacido: IsSuspended = true")
	}
	m.Advance(t, ttl)
	if m.Store.IsSuspended(ev, ttl) {
		t.Error("justo en el ttl: IsSuspended = true; se suspende al SUPERARLO")
	}
	m.Advance(t, time.Second)
	before := take(t, m)
	if !m.Store.IsSuspended(ev, ttl) {
		t.Error("un segundo pasado el ttl: IsSuspended = false")
	}
	if m.Store.IsSuspended(ev, 0) || m.Store.IsSuspended(ev, -time.Second) {
		t.Error("con ttl <= 0 («sin vencimiento»): IsSuspended = true")
	}
	terminal := ev
	terminal.Status = events.StatusCancelled
	if m.Store.IsSuspended(terminal, ttl) {
		t.Error("un evento terminal: IsSuspended = true")
	}
	if m.NoCipher.IsSuspended(ev, ttl) != m.Store.IsSuspended(ev, ttl) {
		t.Error("Store y NoCipher no comparten reloj")
	}
	requireUnchanged(t, "IsSuspended", m, before)
}
