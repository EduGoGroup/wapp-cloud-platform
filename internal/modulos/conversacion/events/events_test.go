package events

import (
	"errors"
	"testing"
	"time"
)

// TestStatusRoleOrigin_Vocabulary: los literales son los de los CHECK de la base (0051) y no se
// tocan: tres estados, tres roles y dos orígenes.
func TestStatusRoleOrigin_Vocabulary(t *testing.T) {
	statuses := map[Status]string{StatusOpen: "open", StatusClosed: "closed", StatusCancelled: "cancelled"}
	roles := map[Role]string{RoleClient: "client", RoleBusiness: "business", RoleSystem: "system"}
	origins := map[Origin]string{OriginWhatsApp: "whatsapp", OriginOwnerPasted: "owner_pasted"}
	if len(statuses) != 3 || len(roles) != 3 || len(origins) != 2 {
		t.Fatalf("hay valores repetidos: %v, %v, %v", statuses, roles, origins)
	}
	for got, want := range statuses {
		if string(got) != want {
			t.Errorf("estado = %q, quería %q", got, want)
		}
	}
	for got, want := range roles {
		if string(got) != want {
			t.Errorf("rol = %q, quería %q", got, want)
		}
	}
	for got, want := range origins {
		if string(got) != want {
			t.Errorf("origen = %q, quería %q", got, want)
		}
	}
}

// TestSentinels_TextsAndIdentity: los ocho centinelas llevan su texto literal y son distintos
// entre sí (errors.Is no confunde uno con otro).
func TestSentinels_TextsAndIdentity(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrNotOpen, "events: el evento no está open (transición rechazada por el guard)"},
		{ErrEventMissing, "events: el evento no existe"},
		{ErrEventNotFound, "events: el evento no existe para ese tenant"},
		{ErrSummaryNotJSON, "events: el resumen debe ser JSON válido (el nivel 1 es estructura, no prosa)"},
		{ErrNoCipher, "events: sin FieldCipher no se persiste texto literal (no hay camino en claro)"},
		{ErrAliveExists, "events: ya existe un evento vivo de ese tipo en la conversación"},
		{ErrNotTerminal, "events: el destino de una transición debe ser closed o cancelled"},
		{ErrInvalidRole, "events: rol desconocido"},
	}
	for i, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("centinela %d = %q, quería %q", i, got, c.want)
		}
		for j, other := range cases {
			if i != j && errors.Is(c.err, other.err) {
				t.Errorf("el centinela %q casa con %q", c.want, other.want)
			}
		}
	}
}

// TestEvent_Alive: vivo es status open y nada más.
func TestEvent_Alive(t *testing.T) {
	cases := []struct {
		status Status
		want   bool
	}{
		{StatusOpen, true},
		{StatusClosed, false},
		{StatusCancelled, false},
		{"", false},
	}
	for _, c := range cases {
		if got := (Event{Status: c.status}).Alive(); got != c.want {
			t.Errorf("Event{Status: %q}.Alive() = %v, quería %v", c.status, got, c.want)
		}
	}
}

// TestRescuable_EmbedsEvent: un Rescuable es el evento más la marca y el contenido derivado; sus
// campos y sus métodos se ven a través.
func TestRescuable_EmbedsEvent(t *testing.T) {
	r := Rescuable{Event: Event{ID: "ev-1", Status: StatusOpen}, Stale: true, ContentState: "alive", ContentRef: "ref-1"}
	if r.ID != "ev-1" || !r.Alive() || !r.Stale || r.ContentState != "alive" || r.ContentRef != "ref-1" {
		t.Errorf("Rescuable = %+v", r)
	}
}

// TestNewEvent_CarriesOnlyWhatTheCallerDecides: los seis campos que dicta el llamador.
func TestNewEvent_CarriesOnlyWhatTheCallerDecides(t *testing.T) {
	in := NewEvent{TenantID: "t", SessionID: "s", ContactID: "c", Kind: "cart", FlowID: "f", FlowVersion: 3}
	if in.TenantID != "t" || in.SessionID != "s" || in.ContactID != "c" || in.Kind != "cart" || in.FlowID != "f" || in.FlowVersion != 3 {
		t.Errorf("NewEvent = %+v", in)
	}
}

// TestHistoryID_FormatAndUTC: «<tipo>-YYYY-MM-DD-HHMM», siempre en UTC y sin segundos.
func TestHistoryID_FormatAndUTC(t *testing.T) {
	santiago := time.FixedZone("-04", -4*60*60)
	cases := []struct {
		name string
		kind string
		at   time.Time
		want string
	}{
		{"utc", "cart", time.Date(2026, 8, 9, 18, 30, 59, 0, time.UTC), "cart-2026-08-09-1830"},
		{"another zone is converted to utc", "survey", time.Date(2026, 8, 9, 22, 5, 0, 0, santiago), "survey-2026-08-10-0205"},
		{"seconds are dropped", "menu", time.Date(2026, 1, 2, 3, 4, 59, 999, time.UTC), "menu-2026-01-02-0304"},
		{"empty kind", "", time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), "-2026-01-02-0304"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HistoryID(c.kind, c.at); got != c.want {
				t.Errorf("HistoryID(%q, %v) = %q, quería %q", c.kind, c.at, got, c.want)
			}
		})
	}
}

// TestIsSuspended_DerivedFromTheClock: vivo y sin interacción durante MÁS de ttl; ttl <= 0 es «sin
// vencimiento» y un evento terminal no está suspendido.
func TestIsSuspended_DerivedFromTheClock(t *testing.T) {
	last := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Hour
	cases := []struct {
		name   string
		status Status
		ttl    time.Duration
		now    time.Time
		want   bool
	}{
		{"within the window", StatusOpen, ttl, last.Add(time.Hour), false},
		{"exactly at the ttl is not yet suspended", StatusOpen, ttl, last.Add(ttl), false},
		{"one nanosecond past the ttl", StatusOpen, ttl, last.Add(ttl + 1), true},
		{"zero ttl never expires", StatusOpen, 0, last.Add(1000 * time.Hour), false},
		{"negative ttl never expires", StatusOpen, -time.Second, last.Add(1000 * time.Hour), false},
		{"closed is not suspended", StatusClosed, ttl, last.Add(10 * ttl), false},
		{"cancelled is not suspended", StatusCancelled, ttl, last.Add(10 * ttl), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Event{Status: c.status, LastActivityAt: last}
			if got := IsSuspended(ev, c.ttl, c.now); got != c.want {
				t.Errorf("IsSuspended = %v, quería %v", got, c.want)
			}
		})
	}
}

// TestIsSuspended_DoesNotMutateTheEvent: es pura; el evento sale como entró.
func TestIsSuspended_DoesNotMutateTheEvent(t *testing.T) {
	last := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ev := Event{ID: "ev-1", Status: StatusOpen, LastActivityAt: last}
	before := ev
	if !IsSuspended(ev, time.Minute, last.Add(time.Hour)) {
		t.Fatal("IsSuspended = false, quería true")
	}
	if ev != before {
		t.Errorf("el evento cambió: %+v, era %+v", ev, before)
	}
}
