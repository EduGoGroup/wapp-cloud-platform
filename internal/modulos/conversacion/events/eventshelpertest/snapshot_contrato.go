package eventshelpertest

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// world es la MARCA DE ESTADO de la suite: todo lo que una operación del almacén puede tocar, de
// una vez (hallazgo 35 de F1). Una marca que vigilara una sola columna —el status, por ejemplo—
// dejaría pasar la transición que acierta en la suya y estropea last_activity_at.
//
// Son las dos tablas del almacén, de los DOS tenants del Montaje: cada evento con sus doce
// columnas y, colgando de él, su historial entero.
type world map[string]eventMark

// eventMark es un evento con todo lo suyo.
type eventMark struct {
	row     events.Event
	entries []Entry
}

// take saca la marca de estado de los dos tenants del Montaje.
func take(t *testing.T, m Montaje) world {
	t.Helper()
	w := make(world)
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		for _, ev := range m.Events(t, tenant) {
			if ev.TenantID != tenant {
				t.Fatalf("Montaje.Events(%s) devolvió un evento del tenant %s", tenant, ev.TenantID)
			}
			w[ev.ID] = eventMark{row: ev, entries: m.Entries(t, ev.ID)}
		}
	}
	return w
}

// sameEvent compara las doce columnas de dos eventos; los instantes, con Equal.
func sameEvent(a, b events.Event) bool {
	return a.ID == b.ID && a.TenantID == b.TenantID && a.SessionID == b.SessionID &&
		a.ContactID == b.ContactID && a.Kind == b.Kind && a.HistoryID == b.HistoryID &&
		a.Status == b.Status && a.FlowID == b.FlowID && a.FlowVersion == b.FlowVersion &&
		a.CreatedAt.Equal(b.CreatedAt) && a.LastActivityAt.Equal(b.LastActivityAt) &&
		a.ClosedAt.Equal(b.ClosedAt)
}

// samePayload compara dos payloads por contenido: los dos ausentes, o el mismo JSON (JSONB
// reordena las claves y normaliza los números).
func samePayload(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// sameEntry compara las cinco marcas de dos entradas.
func sameEntry(a, b Entry) bool {
	return a.Seq == b.Seq && a.Role == b.Role && a.Kind == b.Kind && a.Origin == b.Origin &&
		a.Sealed == b.Sealed && samePayload(a.Payload, b.Payload)
}

// sameMark compara un evento y su historial.
func sameMark(a, b eventMark) bool {
	return sameEvent(a.row, b.row) && slices.EqualFunc(a.entries, b.entries, sameEntry)
}

// changed devuelve, ordenados, los id de los eventos que difieren entre las dos marcas: los que
// aparecen, los que desaparecen y aquellos cuya fila o cuyo historial cambió.
func changed(before, after world) []string {
	var out []string
	for id, b := range before {
		if a, ok := after[id]; !ok || !sameMark(a, b) {
			out = append(out, id)
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// requireUnchanged exige que NADA observable haya cambiado desde before.
func requireUnchanged(t *testing.T, what string, m Montaje, before world) {
	t.Helper()
	if diff := changed(before, take(t, m)); len(diff) != 0 {
		t.Fatalf("%s: no tenía que cambiar nada y cambiaron los eventos %v", what, diff)
	}
}

// requireOnlyChanged exige que desde before hayan cambiado EXACTAMENTE esos eventos (su fila o su
// historial, o que sean nuevos) y ninguno más. Devuelve la marca nueva.
func requireOnlyChanged(t *testing.T, what string, m Montaje, before world, eventIDs ...string) world {
	t.Helper()
	after := take(t, m)
	want := slices.Clone(eventIDs)
	slices.Sort(want)
	if got := changed(before, after); !slices.Equal(got, want) {
		t.Fatalf("%s: cambiaron los eventos %v, quería exactamente %v", what, got, want)
	}
	return after
}

// requireEntry exige las cinco marcas de una entrada observada.
func requireEntry(t *testing.T, what string, got, want Entry) {
	t.Helper()
	if !sameEntry(got, want) {
		t.Errorf("%s = %+v (payload %s), quería %+v (payload %s)", what, got, got.Payload, want, want.Payload)
	}
}
