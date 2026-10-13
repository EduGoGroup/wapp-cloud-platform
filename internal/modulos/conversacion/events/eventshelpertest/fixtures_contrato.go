package eventshelpertest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// Los tipos de evento de fábrica que usan los casos, y un flujo cualquiera.
const (
	kindCart   = "cart"
	kindSurvey = "survey"
	kindMenu   = "menu"
	kindMedia  = "media"

	flowID      = "flujo-contrato"
	flowVersion = 3
)

// conversation es la terna que identifica una conversación: la clave del evento sin el tipo.
type conversation struct {
	tenant, session, contact string
}

// newConversation devuelve una conversación NUEVA del tenant: sesión y contacto recién inventados
// (el contacto, un UUID: la columna lo es).
func newConversation(tenant string) conversation {
	return conversation{tenant: tenant, session: "sess-" + uuid.NewString(), contact: uuid.NewString()}
}

// input son los datos de nacimiento de un evento de ese tipo en la conversación.
func (c conversation) input(kind string) events.NewEvent {
	return events.NewEvent{
		TenantID: c.tenant, SessionID: c.session, ContactID: c.contact,
		Kind: kind, FlowID: flowID, FlowVersion: flowVersion,
	}
}

// mustCreate crea un evento vivo de ese tipo en la conversación.
func mustCreate(t *testing.T, m Montaje, c conversation, kind string) events.Event {
	t.Helper()
	ev, err := m.Store.CreateEvent(t.Context(), c.input(kind))
	if err != nil {
		t.Fatalf("CreateEvent(%s) en %+v: %v", kind, c, err)
	}
	return ev
}

// mustTransition lleva el evento a un estado terminal.
func mustTransition(t *testing.T, m Montaje, eventID string, to events.Status) {
	t.Helper()
	if err := m.Store.TransitionEvent(t.Context(), eventID, to); err != nil {
		t.Fatalf("TransitionEvent(%s, %s): %v", eventID, to, err)
	}
}

// mustTouch refresca el reloj del evento.
func mustTouch(t *testing.T, m Montaje, eventID string) {
	t.Helper()
	if err := m.Store.Touch(t.Context(), eventID); err != nil {
		t.Fatalf("Touch(%s): %v", eventID, err)
	}
}

// mustGet lee el evento del tenant.
func mustGet(t *testing.T, m Montaje, tenant, eventID string) events.Event {
	t.Helper()
	ev, err := m.Store.GetEventForTenant(t.Context(), tenant, eventID)
	if err != nil {
		t.Fatalf("GetEventForTenant(%s, %s): %v", tenant, eventID, err)
	}
	return ev
}

// mustMessage añade un mensaje literal al hilo y devuelve su seq.
func mustMessage(t *testing.T, m Montaje, eventID string, role events.Role, body string) int {
	t.Helper()
	seq, err := m.Store.AppendMessage(t.Context(), eventID, role, body)
	if err != nil {
		t.Fatalf("AppendMessage(%s, %s): %v", eventID, role, err)
	}
	return seq
}

// witnesses son los eventos que difieren de (c, kind) en UNA sola componente de la clave: otro
// tipo en la misma conversación, la misma conversación en otra sesión, otro contacto en la misma
// sesión, y la misma sesión, contacto y tipo en el OTRO tenant. Ninguna operación sobre el evento
// (c, kind) puede moverlos, ni ninguna lectura de esa conversación devolver los tres últimos.
type witnesses struct {
	otherKind, otherSession, otherContact, otherTenant events.Event
}

// seedWitnesses crea los cuatro testigos de (c, kind). El de otro tipo es un survey (o un cart, si
// kind ya es survey).
func seedWitnesses(t *testing.T, m Montaje, c conversation, kind string) witnesses {
	t.Helper()
	other := kindSurvey
	if kind == kindSurvey {
		other = kindCart
	}
	otherTenant := m.TenantB
	if c.tenant == m.TenantB {
		otherTenant = m.TenantA
	}
	return witnesses{
		otherKind:    mustCreate(t, m, c, other),
		otherSession: mustCreate(t, m, conversation{c.tenant, "sess-" + uuid.NewString(), c.contact}, kind),
		otherContact: mustCreate(t, m, conversation{c.tenant, c.session, uuid.NewString()}, kind),
		otherTenant:  mustCreate(t, m, conversation{otherTenant, c.session, c.contact}, kind),
	}
}

// requireIs exige que err case con el centinela.
func requireIs(t *testing.T, what string, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("%s: err = %v, quería %v", what, err, sentinel)
	}
}

// requireNoError exige que err sea nil.
func requireNoError(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// ids devuelve los id de los eventos, en su orden.
func ids(evs []events.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.ID)
	}
	return out
}

// rescuableIDs devuelve los id de los rescatables, en su orden.
func rescuableIDs(rs []events.Rescuable) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// requireIDs exige esa lista de ids, en ese orden.
func requireIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, quería %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, quería %v", what, got, want)
		}
	}
}
