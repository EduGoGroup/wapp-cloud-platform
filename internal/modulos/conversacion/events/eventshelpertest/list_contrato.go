package eventshelpertest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// El listado por el que el DUEÑO limpia (REQ-28): la misma consulta de rescatables, leída para
// todo el tenant.

// mustList lee una página del listado del tenant.
func mustList(t *testing.T, m Montaje, tenant string, f events.ListFilter) events.EventPage {
	t.Helper()
	page, err := m.Store.ListEvents(t.Context(), tenant, f)
	requireNoError(t, "ListEvents", err)
	return page
}

// requirePage exige esos eventos, en ese orden, y ese total.
func requirePage(t *testing.T, what string, page events.EventPage, total int, want ...string) {
	t.Helper()
	requireIDs(t, what, rescuableIDs(page.Events), want)
	if page.Total != total {
		t.Errorf("%s: Total = %d, quería %d", what, page.Total, total)
	}
}

// caseListDefaults: el filtro cero es «lo abierto, con cualquier contenido ofrecible, primera
// página de 50», por última actividad descendente; la página trae la paginación YA normalizada,
// el evento entero y su contenido derivado. Leer no escribe.
func caseListDefaults(t *testing.T, m Montaje) {
	if page := mustList(t, m, m.TenantA, events.ListFilter{}); len(page.Events) != 0 || page.Total != 0 {
		t.Fatalf("tenant sin eventos: %+v, quería una página vacía con Total 0", page)
	}
	c1, c2 := newConversation(m.TenantA), newConversation(m.TenantA)
	first := mustCreate(t, m, c1, kindCart)
	m.Advance(t, time.Minute)
	second := mustCreate(t, m, c2, kindSurvey)
	m.Advance(t, time.Minute)
	closed := mustCreate(t, m, c2, kindMenu)
	mustTransition(t, m, closed.ID, events.StatusClosed)
	ref := m.SetContent(t, first.ID, ContentAlive)
	m.Advance(t, time.Minute)
	mustTouch(t, m, first.ID)
	world := take(t, m)

	page := mustList(t, m, m.TenantA, events.ListFilter{})
	requirePage(t, "ListEvents sin filtro", page, 2, first.ID, second.ID)
	if page.Page != 1 || page.PageSize != events.DefaultPageSize {
		t.Errorf("paginación = (%d, %d), quería (1, %d)", page.Page, page.PageSize, events.DefaultPageSize)
	}
	for _, r := range page.Events {
		if !sameEvent(r.Event, world[r.ID].row) || r.Stale {
			t.Errorf("la página trae %+v (vencido=%v), quería %+v sin vencer", r.Event, r.Stale, world[r.ID].row)
		}
	}
	if got := page.Events[0]; got.ContentState != ContentAlive || got.ContentRef != ref {
		t.Errorf("contenido del primero = (%q, %q), quería (%q, %q)", got.ContentState, got.ContentRef, ContentAlive, ref)
	}
	if got := page.Events[1]; got.ContentState != "" || got.ContentRef != "" {
		t.Errorf("contenido del segundo = (%q, %q), quería vacío", got.ContentState, got.ContentRef)
	}

	page = mustList(t, m, m.TenantA, events.ListFilter{Page: -4, PageSize: events.MaxPageSize + 50})
	if page.Page != 1 || page.PageSize != events.MaxPageSize {
		t.Errorf("paginación saneada = (%d, %d), quería (1, %d)", page.Page, page.PageSize, events.MaxPageSize)
	}
	requireUnchanged(t, "ListEvents", m, world)
}

// caseListStatusKindContact: el estado es exacto (y los terminales se listan si se piden), el
// tipo y el contacto acotan cuando no van vacíos, y los tres se combinan.
func caseListStatusKindContact(t *testing.T, m Montaje) {
	c1, c2 := newConversation(m.TenantA), newConversation(m.TenantA)
	cart1 := mustCreate(t, m, c1, kindCart)
	m.Advance(t, time.Minute)
	survey1 := mustCreate(t, m, c1, kindSurvey)
	m.Advance(t, time.Minute)
	cart2 := mustCreate(t, m, c2, kindCart)
	m.Advance(t, time.Minute)
	closed := mustCreate(t, m, c2, kindMenu)
	cancelled := mustCreate(t, m, c2, kindMedia)
	mustTransition(t, m, closed.ID, events.StatusClosed)
	mustTransition(t, m, cancelled.ID, events.StatusCancelled)

	cases := []struct {
		name string
		f    events.ListFilter
		want []string
	}{
		{"open by default", events.ListFilter{}, []string{cart2.ID, survey1.ID, cart1.ID}},
		{"open explicit", events.ListFilter{Status: events.StatusOpen}, []string{cart2.ID, survey1.ID, cart1.ID}},
		{"closed", events.ListFilter{Status: events.StatusClosed}, []string{closed.ID}},
		{"cancelled", events.ListFilter{Status: events.StatusCancelled}, []string{cancelled.ID}},
		{"kind", events.ListFilter{Kind: kindCart}, []string{cart2.ID, cart1.ID}},
		{"kind without events", events.ListFilter{Kind: "inexistente"}, nil},
		{"contact", events.ListFilter{ContactID: c1.contact}, []string{survey1.ID, cart1.ID}},
		{"unknown contact", events.ListFilter{ContactID: uuid.NewString()}, nil},
		{"kind and contact", events.ListFilter{Kind: kindCart, ContactID: c2.contact}, []string{cart2.ID}},
		{"status kind and contact", events.ListFilter{Status: events.StatusClosed, Kind: kindMenu, ContactID: c2.contact}, []string{closed.ID}},
		{"status and a kind in another status", events.ListFilter{Status: events.StatusClosed, Kind: kindCart}, nil},
	}
	for _, tc := range cases {
		requirePage(t, tc.name, mustList(t, m, m.TenantA, tc.f), len(tc.want), tc.want...)
	}
}

// caseListKinds: Kinds es el conjunto de tipos que el tenant PUEDE ver. nil no filtra; una lista
// VACÍA no deja pasar ninguno (falla cerrado); y se aplica A LA VEZ que Kind.
func caseListKinds(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	evs := seedFourKinds(t, m, c)
	cart, survey, menu, media := evs[0], evs[1], evs[2], evs[3]

	cases := []struct {
		name string
		f    events.ListFilter
		want []string
	}{
		{"nil is no filter", events.ListFilter{Kinds: nil}, []string{media.ID, menu.ID, survey.ID, cart.ID}},
		{"empty lets none through", events.ListFilter{Kinds: []string{}}, nil},
		{"a set", events.ListFilter{Kinds: []string{kindCart, kindMedia}}, []string{media.ID, cart.ID}},
		{"a set with unknown kinds", events.ListFilter{Kinds: []string{"inexistente", kindSurvey}}, []string{survey.ID}},
		{"kind inside the set", events.ListFilter{Kind: kindCart, Kinds: []string{kindCart, kindMedia}}, []string{cart.ID}},
		{"kind outside the set is empty, not an error", events.ListFilter{Kind: kindSurvey, Kinds: []string{kindCart, kindMedia}}, nil},
	}
	for _, tc := range cases {
		requirePage(t, tc.name, mustList(t, m, m.TenantA, tc.f), len(tc.want), tc.want...)
	}
}

// caseListContent: `none` son los eventos sin contenido, `alive` los de contenido vivo y `any` las
// dos mitades. Un evento cuyo contenido cuajó o murió NO sale con ninguno de los tres (INV-17): ha
// desaparecido de la bandeja aunque siga `open`.
func caseListContent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	evs := seedFourKinds(t, m, c)
	withAlive, withSettled, withDiscarded, without := evs[0], evs[1], evs[2], evs[3]
	m.SetContent(t, withAlive.ID, ContentAlive)
	m.SetContent(t, withSettled.ID, ContentSettled)
	m.SetContent(t, withDiscarded.ID, ContentDiscarded)

	cases := []struct {
		name    string
		content events.ContentFilter
		want    []string
	}{
		{"any by default", "", []string{without.ID, withAlive.ID}},
		{"any", events.ContentAny, []string{without.ID, withAlive.ID}},
		{"none", events.ContentNone, []string{without.ID}},
		{"alive", events.ContentAlive, []string{withAlive.ID}},
	}
	for _, tc := range cases {
		requirePage(t, tc.name, mustList(t, m, m.TenantA, events.ListFilter{Content: tc.content}), len(tc.want), tc.want...)
	}
	// La marca de contenido también llega a los terminales que se piden.
	mustTransition(t, m, withAlive.ID, events.StatusCancelled)
	page := mustList(t, m, m.TenantA, events.ListFilter{Status: events.StatusCancelled, Content: events.ContentAlive})
	requirePage(t, "cancelled con contenido vivo", page, 1, withAlive.ID)
}

// caseListStale: Stale nil no filtra y la marca viaja en cada fila; true deja solo los vencidos y
// false solo los que no lo están. El vencido SIGUE en la lista sin filtro (INV-19).
func caseListStale(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	old := mustCreate(t, m, c, kindCart)
	m.Advance(t, 3*time.Hour)
	fresh := mustCreate(t, m, c, kindSurvey)
	yes, no := true, false

	page := mustList(t, m, m.TenantA, events.ListFilter{})
	requirePage(t, "sin filtro de vencido", page, 2, fresh.ID, old.ID)
	if page.Events[0].Stale || !page.Events[1].Stale {
		t.Errorf("marcas = (%v, %v), quería (false, true)", page.Events[0].Stale, page.Events[1].Stale)
	}
	requirePage(t, "solo vencidos", mustList(t, m, m.TenantA, events.ListFilter{Stale: &yes}), 1, old.ID)
	requirePage(t, "solo no vencidos", mustList(t, m, m.TenantA, events.ListFilter{Stale: &no}), 1, fresh.ID)

	// Con el reloj apagado (TTL 0) nada vence: el filtro `true` se queda vacío.
	m.SetInactivityTTL(t, m.TenantA, 0)
	requirePage(t, "solo vencidos con TTL 0", mustList(t, m, m.TenantA, events.ListFilter{Stale: &yes}), 0)
	requirePage(t, "solo no vencidos con TTL 0", mustList(t, m, m.TenantA, events.ListFilter{Stale: &no}), 2, fresh.ID, old.ID)
}

// caseListPagination: las páginas parten la lista ordenada sin solaparse ni dejar huecos, Total es
// el del filtro entero —no el de la página—, y una página pasada el final viene vacía con su Total.
func caseListPagination(t *testing.T, m Montaje) {
	var newestFirst []string
	for range 5 {
		ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
		newestFirst = append([]string{ev.ID}, newestFirst...)
		m.Advance(t, time.Minute)
	}
	// Uno que el filtro deja fuera: no cuenta en el Total.
	mustCreate(t, m, newConversation(m.TenantA), kindSurvey)

	cases := []struct {
		name       string
		page, size int
		want       []string
	}{
		{"first page", 1, 2, newestFirst[0:2]},
		{"second page", 2, 2, newestFirst[2:4]},
		{"last partial page", 3, 2, newestFirst[4:5]},
		{"past the end", 4, 2, nil},
		{"one page holds all", 1, 5, newestFirst},
	}
	for _, tc := range cases {
		page := mustList(t, m, m.TenantA, events.ListFilter{Kind: kindCart, Page: tc.page, PageSize: tc.size})
		requirePage(t, tc.name, page, 5, tc.want...)
		if page.Page != tc.page || page.PageSize != tc.size {
			t.Errorf("%s: paginación = (%d, %d), quería (%d, %d)", tc.name, page.Page, page.PageSize, tc.page, tc.size)
		}
	}
}

// caseListTenantScope: el listado es del tenant que se pide y de ninguno más, también cuando
// comparten sesión, contacto y tipo; y el TTL de cada fila es el de SU tenant.
func caseListTenantScope(t *testing.T, m Montaje) {
	a := newConversation(m.TenantA)
	inA := mustCreate(t, m, a, kindCart)
	w := seedWitnesses(t, m, a, kindCart)
	m.SetInactivityTTL(t, m.TenantB, 60)
	m.Advance(t, 61*time.Second)

	pageB := mustList(t, m, m.TenantB, events.ListFilter{})
	requirePage(t, "tenant B", pageB, 1, w.otherTenant.ID)
	if !pageB.Events[0].Stale {
		t.Error("el evento del tenant B (TTL 60 s) no salió vencido a los 61 s")
	}
	pageA := mustList(t, m, m.TenantA, events.ListFilter{ContactID: a.contact, Kind: kindCart})
	if len(pageA.Events) != 2 || pageA.Total != 2 {
		t.Fatalf("tenant A: %d eventos (Total %d), quería los 2 cart del contacto", len(pageA.Events), pageA.Total)
	}
	for _, r := range pageA.Events {
		if r.TenantID != m.TenantA || (r.ID != inA.ID && r.ID != w.otherSession.ID) || r.Stale {
			t.Errorf("tenant A trae %+v (vencido=%v); quería sus dos cart, sin vencer", r.Event, r.Stale)
		}
	}
}
