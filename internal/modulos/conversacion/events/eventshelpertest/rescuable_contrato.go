package eventshelpertest

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// LA consulta de rescatables (D-043.15): lo que se le puede ofrecer retomar a un contacto.

// mustRescuable lee los rescatables de la conversación.
func mustRescuable(t *testing.T, m Montaje, c conversation, limit int) []events.Rescuable {
	t.Helper()
	rs, err := m.Store.ListRescuable(t.Context(), c.tenant, c.session, c.contact, limit)
	requireNoError(t, "ListRescuable", err)
	return rs
}

// seedFourKinds crea en la conversación un evento de cada tipo de fábrica, con un minuto entre
// nacimientos, y los devuelve por orden de nacimiento: cart, survey, menu, media.
func seedFourKinds(t *testing.T, m Montaje, c conversation) [4]events.Event {
	t.Helper()
	var out [4]events.Event
	for i, kind := range []string{kindCart, kindSurvey, kindMenu, kindMedia} {
		out[i] = mustCreate(t, m, c, kind)
		m.Advance(t, time.Minute)
	}
	return out
}

// caseRescuableOrderAndContent: lo último que se tocó va primero (no lo último que nació), cada
// fila trae el evento entero y su contenido DERIVADO de la vista: vacío sin contenido, `alive` y
// su ref con el contenido vivo. Leer no escribe.
func caseRescuableOrderAndContent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	if rs := mustRescuable(t, m, c, 0); len(rs) != 0 {
		t.Fatalf("sin eventos: %d rescatables, quería ninguno", len(rs))
	}
	evs := seedFourKinds(t, m, c)
	cart, survey, menu, media := evs[0], evs[1], evs[2], evs[3]
	ref := m.SetContent(t, cart.ID, ContentAlive)
	if ref == "" {
		t.Fatal("Montaje.SetContent devolvió un ref vacío")
	}
	// El más viejo se toca el último; el segundo, antes.
	mustTouch(t, m, survey.ID)
	m.Advance(t, time.Minute)
	mustTouch(t, m, cart.ID)
	world := take(t, m)

	rs := mustRescuable(t, m, c, 0)
	requireIDs(t, "ListRescuable", rescuableIDs(rs), []string{cart.ID, survey.ID, media.ID, menu.ID})
	for _, r := range rs {
		if !sameEvent(r.Event, world[r.ID].row) {
			t.Errorf("el rescatable trae %+v, quería %+v", r.Event, world[r.ID].row)
		}
		if r.Stale {
			t.Errorf("%s salió vencido sin haber pasado el TTL", r.Kind)
		}
	}
	if rs[0].ContentState != ContentAlive || rs[0].ContentRef != ref {
		t.Errorf("contenido del cart = (%q, %q), quería (%q, %q)", rs[0].ContentState, rs[0].ContentRef, ContentAlive, ref)
	}
	for _, r := range rs[1:] {
		if r.ContentState != "" || r.ContentRef != "" {
			t.Errorf("%s sin contenido trae (%q, %q), quería vacío", r.Kind, r.ContentState, r.ContentRef)
		}
	}
	requireUnchanged(t, "ListRescuable", m, world)
}

// caseRescuableDeadContent (INV-17): un evento cuyo contenido cuajó (`settled`) o murió
// (`discarded`) no se ofrece, aunque siga VIVO: ListAlive lo sigue listando. Y si el contenido
// vuelve a estar vivo, vuelve a ofrecerse: es derivado, no hay nada que sincronizar.
func caseRescuableDeadContent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	evs := seedFourKinds(t, m, c)
	cart, survey, menu, media := evs[0], evs[1], evs[2], evs[3]
	m.SetContent(t, cart.ID, ContentDiscarded)
	m.SetContent(t, survey.ID, ContentSettled)
	m.SetContent(t, menu.ID, ContentAlive)
	world := take(t, m)

	requireIDs(t, "ListRescuable", rescuableIDs(mustRescuable(t, m, c, 0)), []string{media.ID, menu.ID})
	alive, err := m.Store.ListAlive(t.Context(), c.tenant, c.session, c.contact)
	requireNoError(t, "ListAlive", err)
	requireIDs(t, "ListAlive (los cuatro siguen vivos)", ids(alive), []string{cart.ID, survey.ID, menu.ID, media.ID})

	ref := m.SetContent(t, cart.ID, ContentAlive)
	rs := mustRescuable(t, m, c, 0)
	requireIDs(t, "ListRescuable con el contenido del cart vivo otra vez", rescuableIDs(rs), []string{media.ID, menu.ID, cart.ID})
	if rs[2].ContentState != ContentAlive || rs[2].ContentRef != ref {
		t.Errorf("contenido del cart = (%q, %q), quería (%q, %q)", rs[2].ContentState, rs[2].ContentRef, ContentAlive, ref)
	}
	requireUnchanged(t, "ListRescuable", m, world)
}

// caseRescuableStale (INV-19): «vencido» es una MARCA que informa, no un filtro. Sin fila de
// configuración el TTL es de 2 h: pasado ese silencio el evento SIGUE en la lista, marcado; el que
// se tocó dentro de la ventana, no. Justo en el TTL aún no está vencido, y la marca sale del reloj
// inyectado.
func caseRescuableStale(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	old := mustCreate(t, m, c, kindCart)
	m.Advance(t, time.Hour)
	recent := mustCreate(t, m, c, kindSurvey)
	m.Advance(t, time.Hour) // old: 2 h justas; recent: 1 h

	rs := mustRescuable(t, m, c, 0)
	requireIDs(t, "a las 2 h justas", rescuableIDs(rs), []string{recent.ID, old.ID})
	if rs[0].Stale || rs[1].Stale {
		t.Errorf("a las 2 h justas: vencidos = (%v, %v), quería ninguno (se vence al SUPERAR el TTL)", rs[0].Stale, rs[1].Stale)
	}

	m.Advance(t, time.Second)
	world := take(t, m)
	rs = mustRescuable(t, m, c, 0)
	requireIDs(t, "pasadas las 2 h", rescuableIDs(rs), []string{recent.ID, old.ID})
	if rs[0].Stale || !rs[1].Stale {
		t.Errorf("pasadas las 2 h: vencidos = (%v, %v), quería (false, true)", rs[0].Stale, rs[1].Stale)
	}
	requireUnchanged(t, "ListRescuable", m, world)

	// Volver a hablar lo saca de vencido: no hay estado que deshacer.
	mustTouch(t, m, old.ID)
	rs = mustRescuable(t, m, c, 0)
	requireIDs(t, "tras tocar el vencido", rescuableIDs(rs), []string{old.ID, recent.ID})
	if rs[0].Stale || rs[1].Stale {
		t.Errorf("tras tocar el vencido: vencidos = (%v, %v), quería ninguno", rs[0].Stale, rs[1].Stale)
	}
}

// caseRescuableTTL: el TTL es el del TENANT del evento. Con 60 s vence al minuto largo; con 0
// («sin vencimiento») no vence nunca; y la fila de un tenant no cambia la marca del otro, que
// sigue con las 2 h por defecto.
func caseRescuableTTL(t *testing.T, m Montaje) {
	a := newConversation(m.TenantA)
	b := conversation{m.TenantB, a.session, a.contact}
	inA := mustCreate(t, m, a, kindCart)
	inB := mustCreate(t, m, b, kindCart)
	m.SetInactivityTTL(t, m.TenantA, 60)
	m.Advance(t, 61*time.Second)

	staleOf := func(c conversation, id string) bool {
		t.Helper()
		rs := mustRescuable(t, m, c, 0)
		requireIDs(t, "ListRescuable", rescuableIDs(rs), []string{id})
		return rs[0].Stale
	}
	if !staleOf(a, inA.ID) {
		t.Error("TTL de 60 s y 61 s de silencio: no salió vencido")
	}
	if staleOf(b, inB.ID) {
		t.Error("el otro tenant (2 h por defecto) salió vencido a los 61 s")
	}

	m.SetInactivityTTL(t, m.TenantA, 0)
	m.Advance(t, 1000*time.Hour)
	if staleOf(a, inA.ID) {
		t.Error("con TTL 0 («sin vencimiento») salió vencido")
	}
	if !staleOf(b, inB.ID) {
		t.Error("el otro tenant, tras 1000 h con el TTL por defecto, no salió vencido")
	}
}

// caseRescuableLimit: limit acota el lote por el principio del orden (lo más reciente); 0 y un
// negativo son «sin tope».
func caseRescuableLimit(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	evs := seedFourKinds(t, m, c)
	newestFirst := []string{evs[3].ID, evs[2].ID, evs[1].ID, evs[0].ID}

	cases := []struct {
		name  string
		limit int
		want  []string
	}{
		{"zero is no cap", 0, newestFirst},
		{"negative is no cap", -1, newestFirst},
		{"one", 1, newestFirst[:1]},
		{"three", 3, newestFirst[:3]},
		{"exactly the size", 4, newestFirst},
		{"above the size", 6, newestFirst},
	}
	for _, tc := range cases {
		requireIDs(t, tc.name, rescuableIDs(mustRescuable(t, m, c, tc.limit)), tc.want)
	}
}

// caseRescuableScope: solo los vivos de ESA conversación: ni los de otra sesión, otro contacto u
// otro tenant, ni los terminales.
func caseRescuableScope(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	cart := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	menu := mustCreate(t, m, c, kindMenu)
	mustTransition(t, m, menu.ID, events.StatusClosed)
	m.Advance(t, time.Minute)
	mustTouch(t, m, cart.ID)

	requireIDs(t, "ListRescuable", rescuableIDs(mustRescuable(t, m, c, 0)), []string{cart.ID, w.otherKind.ID})
}
