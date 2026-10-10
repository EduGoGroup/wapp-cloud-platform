package eventshelpertest_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
)

// testClock es un reloj que solo avanza cuando el test lo mueve.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
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

// newStore devuelve un doble vacío con un reloj de test inyectado.
func newStore() (*eventshelpertest.Store, *testClock) {
	clock := newTestClock()
	store := eventshelpertest.NewStore()
	store.SetClock(clock.Now)
	return store, clock
}

// TestStore_Contrato corre la suite del almacén del evento contra el doble en memoria, sin BD y
// sin reloj real: cada caso monta un doble nuevo con su reloj. Los observadores y las siembras son
// los métodos del doble.
func TestStore_Contrato(t *testing.T) {
	eventshelpertest.Contrato(t, func(*testing.T) eventshelpertest.Montaje {
		store, clock := newStore()
		return eventshelpertest.Montaje{
			Store:    store,
			NoCipher: store.WithoutCipher(),
			TenantA:  uuid.NewString(),
			TenantB:  uuid.NewString(),
			Events:   func(_ *testing.T, tenantID string) []eventshelpertest.Event { return store.Events(tenantID) },
			Entries:  func(_ *testing.T, eventID string) []eventshelpertest.Entry { return store.Entries(eventID) },
			SetContent: func(_ *testing.T, eventID, state string) string {
				return store.SetContent(eventID, state)
			},
			SetInactivityTTL: func(_ *testing.T, tenantID string, seconds int) {
				store.SetInactivityTTL(tenantID, seconds)
			},
			CorruptEntry: func(t *testing.T, eventID string, seq int) {
				t.Helper()
				if !store.CorruptEntry(eventID, seq) {
					t.Fatalf("no hay entrada sellada %d en el evento %s", seq, eventID)
				}
			},
			Now:     func(*testing.T) time.Time { return clock.Now() },
			Advance: func(_ *testing.T, d time.Duration) { clock.Advance(d) },
		}
	})
}

// TestStore_AcceptsIDsThatAreNotUUID: lo que el doble NO imita de la base. Tenant, sesión y
// contacto son cadenas cualesquiera (los tests de sus consumidores usan "t-1"); el id del evento
// lo pone el doble y sí es un UUID, que es lo que GetEventForTenant exige.
func TestStore_AcceptsIDsThatAreNotUUID(t *testing.T) {
	store, _ := newStore()
	ev, err := store.CreateEvent(t.Context(), events.NewEvent{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart"})
	if err != nil {
		t.Fatalf("CreateEvent con ids que no son UUID: %v", err)
	}
	if _, err := uuid.Parse(ev.ID); err != nil {
		t.Errorf("el id del evento %q no es un UUID: %v", ev.ID, err)
	}
	got, err := store.GetEventForTenant(t.Context(), "t-1", ev.ID)
	if err != nil || got != ev {
		t.Errorf("GetEventForTenant = (%+v, %v), quería (%+v, nil)", got, err, ev)
	}
	page, err := store.ListEvents(t.Context(), "t-1", events.ListFilter{ContactID: "c-1"})
	if err != nil || page.Total != 1 {
		t.Errorf("ListEvents por un contacto que no es UUID = (%+v, %v), quería un evento", page, err)
	}
}

// TestStore_SetClock_NilIsIgnoredAndDefaultIsWallClock: sin SetClock el doble fecha con time.Now, y
// un reloj nil no sustituye al que hubiera.
func TestStore_SetClock_NilIsIgnoredAndDefaultIsWallClock(t *testing.T) {
	store := eventshelpertest.NewStore()
	before := time.Now().UTC()
	ev, err := store.CreateEvent(t.Context(), events.NewEvent{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if ev.CreatedAt.Before(before) || ev.CreatedAt.After(time.Now().UTC()) || ev.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v, quería un instante UTC del reloj de pared", ev.CreatedAt)
	}

	fixed := time.Date(2026, 8, 9, 18, 30, 0, 0, time.FixedZone("-04", -4*3600))
	store.SetClock(func() time.Time { return fixed })
	store.SetClock(nil)
	if err := store.Touch(t.Context(), ev.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got := store.Events("t-1")[0]
	if !got.LastActivityAt.Equal(fixed) || got.LastActivityAt.Location() != time.UTC {
		t.Errorf("LastActivityAt = %v, quería %v en UTC", got.LastActivityAt, fixed.UTC())
	}
}

// TestStore_SetContent_EmptyStateRemovesIt: con el estado vacío el evento se queda sin contenido;
// volver a ponérselo le da un ref NUEVO.
func TestStore_SetContent_EmptyStateRemovesIt(t *testing.T) {
	store, _ := newStore()
	ev, err := store.CreateEvent(t.Context(), events.NewEvent{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	first := store.SetContent(ev.ID, eventshelpertest.ContentSettled)
	if same := store.SetContent(ev.ID, eventshelpertest.ContentDiscarded); same != first {
		t.Errorf("cambiar el estado cambió el ref: %q, era %q", same, first)
	}
	if ref := store.SetContent(ev.ID, ""); ref != "" {
		t.Errorf("quitar el contenido devolvió el ref %q", ref)
	}
	rs, err := store.ListRescuable(t.Context(), "t-1", "s-1", "c-1", 0)
	if err != nil || len(rs) != 1 || rs[0].ContentState != "" || rs[0].ContentRef != "" {
		t.Errorf("ListRescuable = (%+v, %v), quería el evento sin contenido", rs, err)
	}
	if again := store.SetContent(ev.ID, eventshelpertest.ContentAlive); again == "" || again == first {
		t.Errorf("el contenido nuevo lleva el ref %q; quería uno nuevo, distinto de %q", again, first)
	}
}

// TestStore_CorruptEntry_OnlySealedEntries: solo se puede estropear una entrada sellada que exista.
func TestStore_CorruptEntry_OnlySealedEntries(t *testing.T) {
	store, _ := newStore()
	ev, err := store.CreateEvent(t.Context(), events.NewEvent{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := store.AppendDecision(t.Context(), ev.ID, []byte(`[1]`)); err != nil {
		t.Fatalf("AppendDecision: %v", err)
	}
	if _, err := store.AppendMessage(t.Context(), ev.ID, events.RoleClient, "hola"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if store.CorruptEntry(ev.ID, 1) {
		t.Error("CorruptEntry sobre una entrada de nivel 1 devolvió true")
	}
	if store.CorruptEntry(ev.ID, 3) || store.CorruptEntry(uuid.NewString(), 1) {
		t.Error("CorruptEntry sobre una entrada que no existe devolvió true")
	}
	if !store.CorruptEntry(ev.ID, 2) {
		t.Error("CorruptEntry sobre la entrada sellada devolvió false")
	}
}

// TestStore_Entries_ReturnsCopies: lo que devuelve Entries no comparte memoria con el doble, ni el
// doble con el payload que le dieron.
func TestStore_Entries_ReturnsCopies(t *testing.T) {
	store, _ := newStore()
	ev, err := store.CreateEvent(t.Context(), events.NewEvent{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	payload := []byte(`{"a":1}`)
	if err := store.AppendDecision(t.Context(), ev.ID, payload); err != nil {
		t.Fatalf("AppendDecision: %v", err)
	}
	payload[2] = 'z'
	seen := store.Entries(ev.ID)
	seen[0].Payload[2] = 'y'
	if got := string(store.Entries(ev.ID)[0].Payload); got != `{"a":1}` {
		t.Errorf("el payload guardado es %s, quería {\"a\":1}", got)
	}
}

// TestStore_NilStore_ThreadReadsReturnNothing: como el adaptador, las dos lecturas del hilo sobre
// un *Store nil devuelven nil sin error.
func TestStore_NilStore_ThreadReadsReturnNothing(t *testing.T) {
	var store *eventshelpertest.Store
	if thread, err := store.ListThread(t.Context(), uuid.NewString(), 10); thread != nil || err != nil {
		t.Errorf("ListThread sobre nil = (%v, %v), quería (nil, nil)", thread, err)
	}
	if pasted, err := store.ListPastedByOwner(t.Context(), uuid.NewString()); pasted != nil || err != nil {
		t.Errorf("ListPastedByOwner sobre nil = (%v, %v), quería (nil, nil)", pasted, err)
	}
}
