package intakehelpertest

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Los textos con los que las tres escrituras de la cola rechazan una clave de ventana incompleta.
// Son observables y LITERALES: los mismos en intake.Postgres y en intake.MemoryStore (hallazgo 7
// de F7), cada escritura con el suyo.
const (
	incompleteKeyOnOpen  = "intake: clave de ventana incompleta (tenant/session/contact/event)"
	incompleteKeyOnClose = "intake: clave de ventana incompleta al cerrar"
	incompleteKeyOnPut   = "intake: clave de ventana incompleta al guardar el literal"
)

// incompleteKey es una clave de ventana a la que le falta algo, con el nombre de lo que le falta.
type incompleteKey struct {
	name string
	key  intake.WindowKey
}

// incompleteKeysOf devuelve k sin cada una de sus cuatro piezas —una por vez— y la clave vacía.
// Salvo la pieza que falta, cada una es la clave de k: es lo que haría que una implementación sin
// la validación encontrara (o creara) una fila vecina de la buena.
func incompleteKeysOf(k intake.WindowKey) []incompleteKey {
	noTenant, noSession, noContact, noEvent := k, k, k, k
	noTenant.TenantID, noSession.SessionID, noContact.ContactID, noEvent.EventID = "", "", "", ""
	return []incompleteKey{
		{"sin tenant", noTenant},
		{"sin sesión", noSession},
		{"sin contacto", noContact},
		{"sin evento", noEvent},
		{"vacía", intake.WindowKey{}},
	}
}

// queueSnapshot es la tabla entera tal como la ve un caso: las filas de los dos tenants y las del
// tenant vacío (donde caería una ventana abierta con una clave sin tenant), por id.
func queueSnapshot(t *testing.T, m QueueMontaje) map[string]Row {
	t.Helper()
	out := map[string]Row{}
	for _, tenant := range []string{m.TenantA, m.TenantB, ""} {
		for _, r := range m.Rows(t, tenant) {
			out[r.ID] = r
		}
	}
	return out
}

// requireSameQueue afirma que la tabla es la de antes: las mismas filas, ninguna de más ni de
// menos, y cada una sin tocar (`updated_at` incluido). Y que las ventanas vivas siguen siendo
// wantLive: una fila que no fuera de ninguno de los tres tenants saldría por ahí.
func requireSameQueue(t *testing.T, m QueueMontaje, what string, before map[string]Row, wantLive int) {
	t.Helper()
	after := queueSnapshot(t, m)
	if len(after) != len(before) {
		t.Errorf("%s: la tabla tiene %d filas, quería las %d de antes", what, len(after), len(before))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			t.Errorf("%s: la fila %s ya no está", what, id)
			continue
		}
		requireSameRow(t, what+": la fila "+id, got, want)
	}
	live, err := m.Store.ListAggregating(context.Background(), 50)
	if err != nil {
		t.Fatalf("%s: ListAggregating: error inesperado %v", what, err)
	}
	if len(live) != wantLive {
		t.Errorf("%s: hay %d ventanas vivas, quería %d: %+v", what, len(live), wantLive, live)
	}
}

// caseOpenIncompleteKey: una clave a la que le falta cualquiera de sus cuatro piezas no abre
// ninguna ventana ni amplía la viva de la clave buena: error, con su texto, y la tabla intacta.
// Las cuatro columnas son NOT NULL: una clave a medias no es «una ventana rara».
func caseOpenIncompleteKey(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	before := queueSnapshot(t, m)
	m.Advance(t)

	for _, c := range incompleteKeysOf(k) {
		err := m.Store.OpenOrAppend(context.Background(), intake.Append{Key: c.key, MessageTS: firstMessageTS, Refs: []string{"wamid.stray"}})
		if err == nil || err.Error() != incompleteKeyOnOpen {
			t.Errorf("OpenOrAppend con la clave %s = %v, quería el error %q", c.name, err, incompleteKeyOnOpen)
		}
		requireSameQueue(t, m, "abrir con la clave "+c.name, before, 2)
	}
}

// caseCloseIncompleteKey: una clave incompleta no cierra nada —tampoco la ventana viva de la
// clave buena, que comparte con ella tres de sus cuatro piezas—: (false, error) con su texto, y
// la tabla intacta. No es el (false, nil) de «no había ventana viva».
func caseCloseIncompleteKey(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	before := queueSnapshot(t, m)
	m.Advance(t)

	for _, c := range incompleteKeysOf(k) {
		ok, err := m.Store.CloseWindow(context.Background(), c.key)
		if ok || err == nil || err.Error() != incompleteKeyOnClose {
			t.Errorf("CloseWindow con la clave %s = (%v, %v), quería (false, %q)", c.name, ok, err, incompleteKeyOnClose)
		}
		requireSameQueue(t, m, "cerrar con la clave "+c.name, before, 2)
	}
}

// casePutIncompleteKey: una clave incompleta no escribe el sobre en ninguna ventana cerrada:
// (false, error) con su texto, y la tabla intacta. LA CLAVE SE MIRA ANTES QUE EL SOBRE: con las
// dos cosas mal, el error es el de la clave.
func casePutIncompleteKey(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	closeLive(t, m, k)
	// El testigo del otro tenant también queda cerrado y sin sobre: es otra fila que rellenar.
	closeLive(t, m, w.key)
	before := queueSnapshot(t, m)
	m.Advance(t)

	for _, c := range incompleteKeysOf(k) {
		for name, env := range map[string]intake.SourceText{"un sobre completo": envelope("stray"), "un sobre incompleto": {}} {
			ok, err := m.Store.PutSourceText(context.Background(), c.key, env)
			if ok || err == nil || err.Error() != incompleteKeyOnPut {
				t.Errorf("PutSourceText con la clave %s y %s = (%v, %v), quería (false, %q)", c.name, name, ok, err, incompleteKeyOnPut)
			}
		}
		requireSameQueue(t, m, "guardar el literal con la clave "+c.name, before, 0)
	}
}
