package intakeshelpertest

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// discardableKeys son las claves ALMACENADAS desde las que los casos dejan descartar. Qué es
// descartable lo decide el dominio y se lo pasa al puerto: aquí es solo una lista.
var discardableKeys = []string{intakes.StatusOpen, intakes.StatusClosedLegacy}

// discard descarta o falla el test.
func discard(t *testing.T, m Montaje, r ref) intakes.DiscardOutcome {
	t.Helper()
	out, err := m.Store.Discard(bg(), r.tenant, r.id, discardableKeys)
	if err != nil {
		t.Fatalf("Discard(%s): error inesperado %v", r.id, err)
	}
	return out
}

// caseDiscard: el descarte deja la solicitud en `abandoned` con UpdatedAt refrescado y UNA
// revisión `discarded` de `owner` que guarda de dónde venía (la clave ALMACENADA) y su total. Las
// líneas y el total no se tocan, y un evento ya terminal no se pisa. El outcome dice el estado del
// que VENÍA, normalizado.
func caseDiscard(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	for _, c := range []struct {
		stored, normalized, event string
	}{
		{intakes.StatusOpen, intakes.StatusOpen, eventCancelled},
		{intakes.StatusClosedLegacy, intakes.StatusConfirmed, eventClosed},
	} {
		what := "descartar desde " + c.stored
		r := seed(t, m, m.TenantA, c.stored, 1, c.event, append(customerLines(), ownerShipping())...)
		insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
		want := take(t, m, r).clone()
		m.Advance(t)

		out := discard(t, m, r)
		if want := (intakes.DiscardOutcome{Discarded: true, Status: c.normalized}); out != want {
			t.Errorf("%s: outcome = %+v, quería %+v", what, out, want)
		}
		got := take(t, m, r)
		want.stored, want.detail.Status = intakes.StatusAbandoned, intakes.StatusAbandoned
		adoptRefreshedUpdatedAt(t, what, got, &want)
		rev := adoptNewRevision(t, what, got, &want, intakes.RevisionKindDiscarded, intakes.RevisionByOwner, "")
		requireSame(t, what, got, want)
		wantPayload := fmt.Sprintf(`{"version":1,"from_status":%q,"total":4508}`, c.stored)
		if !sameJSON(rev.Payload, json.RawMessage(wantPayload)) {
			t.Errorf("%s: payload de la revisión = %s, quería %s", what, rev.Payload, wantPayload)
		}
	}
	w.requireUntouched(t, m)
}

// caseDiscardLiveEvent: si el evento que ESTA solicitud declara sigue `open`, el descarte dice
// LiveEvent y no escribe NADA, ni en la solicitud ni en el evento. Que el tenant tenga OTRO evento
// `open` —el carrito nuevo de esa conversación— no frena el descarte de una cuyo evento ya murió.
func caseDiscardLiveEvent(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	live := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventOpen, customerLines()...)
	before := take(t, m, live)
	m.Advance(t)

	out := discard(t, m, live)
	if want := (intakes.DiscardOutcome{Status: intakes.StatusOpen, LiveEvent: true}); out != want {
		t.Errorf("con el evento vivo: outcome = %+v, quería %+v", out, want)
	}
	requireSame(t, "con el evento vivo", take(t, m, live), before)

	orphan := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	if out := discard(t, m, orphan); !out.Discarded || out.LiveEvent {
		t.Errorf("con el evento propio muerto y otro vivo en el tenant: outcome = %+v, quería descartada", out)
	}
	requireSame(t, "la del evento vivo, tras descartar a su vecina", take(t, m, live), before)
	w.requireUntouched(t, m)
}

// caseDiscardNotDiscardable: desde un estado que no está en la lista se rechaza SIN escribir, y el
// outcome dice dónde está la solicitud. El estado se mira ANTES que el evento: una no descartable
// con el evento vivo no dice LiveEvent.
func caseDiscardNotDiscardable(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	for _, c := range []struct {
		stored, event string
	}{
		{intakes.StatusSettled, eventCancelled},
		{intakes.StatusPendingApproval, eventOpen},
		{intakes.StatusConfirmed, eventOpen}, // `closed` está en la lista; su forma canónica, no
	} {
		r := seed(t, m, m.TenantA, c.stored, 1, c.event, customerLines()...)
		before := take(t, m, r)
		m.Advance(t)
		out := discard(t, m, r)
		if want := (intakes.DiscardOutcome{Status: c.stored}); out != want {
			t.Errorf("desde %s con el evento %s: outcome = %+v, quería %+v", c.stored, c.event, out, want)
		}
		requireSame(t, "desde "+c.stored, take(t, m, r), before)
	}

	empty := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled)
	before := take(t, m, empty)
	out, err := m.Store.Discard(bg(), empty.tenant, empty.id, nil)
	if err != nil || out != (intakes.DiscardOutcome{Status: intakes.StatusOpen}) {
		t.Errorf("con la lista de descartables vacía: (%+v, %v), quería solo el estado y ningún error", out, err)
	}
	requireSame(t, "con la lista de descartables vacía", take(t, m, empty), before)
	w.requireUntouched(t, m)
}

// caseDiscardTwice: descartar dos veces la misma fila deja el mismo estado y UNA sola revisión. Es
// lo que hace seguro reintentar un lote que se cortó.
func caseDiscardTwice(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	if out := discard(t, m, r); !out.Discarded {
		t.Fatalf("el primer descarte no descartó: %+v", out)
	}
	after := take(t, m, r)
	if n := len(after.detail.Revisions); n != 1 {
		t.Fatalf("tras el primer descarte hay %d revisiones, quería 1", n)
	}
	m.Advance(t)

	out := discard(t, m, r)
	if want := (intakes.DiscardOutcome{Status: intakes.StatusAbandoned}); out != want {
		t.Errorf("segundo descarte: outcome = %+v, quería %+v", out, want)
	}
	requireSame(t, "tras el segundo descarte", take(t, m, r), after)
	w.requireUntouched(t, m)
}

func caseDiscardNotFound(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	all := []string{intakes.StatusPendingApproval, intakes.StatusOpen} // el estado de los testigos entra
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: w.foreign().id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
	} {
		out, err := m.Store.Discard(bg(), target.tenant, target.id, all)
		if !errors.Is(err, intakes.ErrNotFound) {
			t.Errorf("Discard de una solicitud %s: err = %v, quería ErrNotFound", name, err)
		}
		if out != (intakes.DiscardOutcome{}) {
			t.Errorf("Discard de una solicitud %s devolvió un outcome: %+v", name, out)
		}
	}
	w.requireUntouched(t, m)
}

// abandonByEvent abandona por evento o falla el test.
func abandonByEvent(t *testing.T, m Montaje, tenant, eventID string) {
	t.Helper()
	if err := m.Store.AbandonByEvent(bg(), tenant, eventID); err != nil {
		t.Fatalf("AbandonByEvent(%s): error inesperado %v", eventID, err)
	}
}

// caseAbandonByEvent: el evento abandona su solicitud `open` por el event_id que ELLA declara. Se
// refresca UpdatedAt, NO se escribe revisión y no se tocan las líneas ni el evento. El segundo
// intento es éxito idempotente y no vuelve a tocar nada.
func caseAbandonByEvent(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	other := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled, customerLines()...)
	otherBefore := take(t, m, other)
	want := take(t, m, r).clone()
	m.Advance(t)

	abandonByEvent(t, m, r.tenant, r.eventID)
	got := take(t, m, r)
	want.stored, want.detail.Status = intakes.StatusAbandoned, intakes.StatusAbandoned
	adoptRefreshedUpdatedAt(t, "AbandonByEvent", got, &want)
	requireSame(t, "tras AbandonByEvent", got, want)
	requireSame(t, "la otra `open` del tenant, que declara otro evento", take(t, m, other), otherBefore)

	m.Advance(t)
	abandonByEvent(t, m, r.tenant, r.eventID)
	requireSame(t, "tras el segundo AbandonByEvent", take(t, m, r), want)
	w.requireUntouched(t, m)
}

// caseAbandonByEventNotOpen: la guarda del estado va en la propia escritura. Una solicitud ya
// resuelta por un humano no se abandona porque su evento muera, y la clave se compara ALMACENADA:
// solo `open`.
func caseAbandonByEventNotOpen(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	for _, stored := range []string{
		intakes.StatusSettled, intakes.StatusPendingApproval, intakes.StatusClosedLegacy, intakes.StatusNeedsInfo,
	} {
		r := seed(t, m, m.TenantA, stored, 1, eventCancelled, customerLines()...)
		before := take(t, m, r)
		m.Advance(t)
		abandonByEvent(t, m, r.tenant, r.eventID)
		requireSame(t, "una solicitud "+stored, take(t, m, r), before)
	}
	w.requireUntouched(t, m)
}

// caseAbandonByEventForeign: el tenant viaja en la condición —el evento de otro tenant no alcanza
// una solicitud ajena (INV-8)—, y un evento desconocido, vacío o que no es un UUID es éxito sin
// tocar nada.
func caseAbandonByEventForeign(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	mine := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	theirs := seed(t, m, m.TenantB, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	mineBefore, theirsBefore := take(t, m, mine), take(t, m, theirs)
	m.Advance(t)

	abandonByEvent(t, m, m.TenantA, theirs.eventID)
	abandonByEvent(t, m, m.TenantB, mine.eventID)
	abandonByEvent(t, m, uuid.NewString(), mine.eventID)
	for _, eventID := range []string{uuid.NewString(), "", "no-soy-un-uuid"} {
		abandonByEvent(t, m, m.TenantA, eventID)
		abandonByEvent(t, m, m.TenantB, eventID)
	}
	requireSame(t, "la solicitud `open` de A", take(t, m, mine), mineBefore)
	requireSame(t, "la solicitud `open` de B", take(t, m, theirs), theirsBefore)
	w.requireUntouched(t, m)
}
