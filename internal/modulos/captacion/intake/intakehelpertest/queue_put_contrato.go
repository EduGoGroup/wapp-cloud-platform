package intakehelpertest

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// casePutOnce: sobre la ventana recién cerrada, el sobre se escribe ENTERO —las tres columnas—
// y la llamada devuelve true. Un segundo sobre para la misma ventana devuelve (false, nil) y no
// pisa el primero ni toca la fila.
func casePutOnce(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	closeLive(t, m, k)
	before := onlyRow(t, m, k)
	m.Advance(t)

	ctx := context.Background()
	if ok, err := m.Store.PutSourceText(ctx, k, envelope("first")); err != nil || !ok {
		t.Fatalf("PutSourceText = (%v, %v), quería (true, nil)", ok, err)
	}
	want := before
	want.SourceText = envelope("first")
	written := onlyRow(t, m, k)
	requireWrittenRow(t, "la ventana con su sobre", written, want)

	m.Advance(t)
	if ok, err := m.Store.PutSourceText(ctx, k, envelope("second")); err != nil || ok {
		t.Errorf("segundo PutSourceText = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana tras el segundo sobre", onlyRow(t, m, k), written)
	w.requireUntouched(t, m)
}

// casePutPicksTheLatestPending: una tupla puede tener VARIAS ventanas cerradas (el índice único
// solo cubre las vivas). El sobre va a la ÚLTIMA cerrada y solo a esa; y una vez escrito, un
// sobre más NO cae en la ventana vieja que se quedó sin componer: rellenarla con el texto de
// otra ventana es el accidente que la guarda existe para impedir.
func casePutPicksTheLatestPending(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	for _, ref := range []string{"wamid.old", "wamid.recent"} {
		open(t, m, k, firstMessageTS, ref)
		m.Advance(t)
		closeLive(t, m, k)
		m.Advance(t)
	}
	old, recent := rowWithRef(t, m, k, "wamid.old"), rowWithRef(t, m, k, "wamid.recent")

	ctx := context.Background()
	if ok, err := m.Store.PutSourceText(ctx, k, envelope("now")); err != nil || !ok {
		t.Fatalf("PutSourceText = (%v, %v), quería (true, nil)", ok, err)
	}
	want := recent
	want.SourceText = envelope("now")
	written := rowWithRef(t, m, k, "wamid.recent")
	requireWrittenRow(t, "la última ventana cerrada", written, want)
	requireSameRow(t, "la ventana cerrada antes", rowWithRef(t, m, k, "wamid.old"), old)

	m.Advance(t)
	if ok, err := m.Store.PutSourceText(ctx, k, envelope("later")); err != nil || ok {
		t.Errorf("PutSourceText con la última ventana ya compuesta = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la última ventana cerrada, tras el intento", rowWithRef(t, m, k, "wamid.recent"), written)
	requireSameRow(t, "la ventana vieja sin componer", rowWithRef(t, m, k, "wamid.old"), old)
	w.requireUntouched(t, m)
}

// casePutWithoutPending: sin ventana cerrada no hay dónde escribir. Una ventana todavía VIVA no
// recibe el sobre, ni una tupla que no existe: (false, nil) y nada tocado.
func casePutWithoutPending(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	live := onlyRow(t, m, k)
	// El testigo del otro tenant SÍ está cerrado y sin sobre: es la fila que una sentencia sin
	// el tenant en su filtro rellenaría.
	closeLive(t, m, w.key)
	w.row = onlyRow(t, m, w.key)
	m.Advance(t)

	ctx := context.Background()
	if ok, err := m.Store.PutSourceText(ctx, k, envelope("early")); err != nil || ok {
		t.Errorf("PutSourceText sobre una ventana viva = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana viva", onlyRow(t, m, k), live)

	unknown := newKey(m.TenantA)
	if ok, err := m.Store.PutSourceText(ctx, unknown, envelope("nobody")); err != nil || ok {
		t.Errorf("PutSourceText sobre una tupla sin ventanas = (%v, %v), quería (false, nil)", ok, err)
	}
	if rows := rowsOf(t, m, unknown); len(rows) != 0 {
		t.Errorf("PutSourceText creó %d filas para una tupla sin ventanas", len(rows))
	}
	w.requireUntouched(t, m)
}

// casePutIncompleteEnvelope: LAS TRES O NINGUNA. Un sobre al que le falta cualquiera de sus
// tres piezas se rechaza con error ANTES de escribir: media escritura deja una fila
// indescifrable, que es peor que una fila vacía.
func casePutIncompleteEnvelope(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	closeLive(t, m, k)
	before := onlyRow(t, m, k)
	m.Advance(t)

	full := envelope("full")
	incomplete := map[string]intake.SourceText{
		"sin nada":         {},
		"sin el cifrado":   {DEK: full.DEK, KEKID: full.KEKID},
		"sin la DEK":       {Enc: full.Enc, KEKID: full.KEKID},
		"sin el kek_id":    {Enc: full.Enc, DEK: full.DEK},
		"cifrado vacío":    {Enc: []byte{}, DEK: full.DEK, KEKID: full.KEKID},
		"solo con kek_id":  {KEKID: full.KEKID},
		"solo con cifrado": {Enc: full.Enc},
	}
	for name, env := range incomplete {
		ok, err := m.Store.PutSourceText(context.Background(), k, env)
		if err == nil || ok {
			t.Errorf("PutSourceText con un sobre %s = (%v, %v), quería (false, error)", name, ok, err)
		}
		requireSameRow(t, "la ventana tras un sobre "+name, onlyRow(t, m, k), before)
	}
	w.requireUntouched(t, m)
}
