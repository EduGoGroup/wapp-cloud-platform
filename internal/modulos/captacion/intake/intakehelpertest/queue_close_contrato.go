package intakehelpertest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Los casos de la QUINTA operación de la cola, intake.JobStore.CloseWithSourceText: el cierre de
// la ventana y su sobre en una sola sentencia, solo si la ventana no cambió desde que se leyó
// (D-F7-9, D-F8-13). Sus filas están en queueCases (contrato.go), con las del resto de la cola.

// incompleteWindowOnCloseWithText es el texto con el que CloseWithSourceText rechaza una clave de
// ventana incompleta o un id vacío. Observable y LITERAL: el mismo en intake.Postgres y en
// intake.MemoryStore, como los tres de queue_key_contrato.go.
const incompleteWindowOnCloseWithText = "intake: ventana incompleta al cerrar con el literal"

// seenOf devuelve la ventana viva de la clave TAL COMO LA VE EL BARRIDO: el OpenJob que da
// ListAggregating, con su id y sus dos anclas. Es lo que se le pasa a CloseWithSourceText.
func seenOf(t *testing.T, m QueueMontaje, k intake.WindowKey) intake.OpenJob {
	t.Helper()
	live, err := m.Store.ListAggregating(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListAggregating: error inesperado %v", err)
	}
	for _, job := range live {
		if job.Key == k {
			return job
		}
	}
	t.Fatalf("la clave %+v no tiene ventana viva entre las %d listadas", k, len(live))
	return intake.OpenJob{}
}

// halfEnvelopes son los tres recortes de un sobre completo: le falta una de sus tres piezas.
func halfEnvelopes() map[string]intake.SourceText {
	full := envelope("half")
	return map[string]intake.SourceText{
		"sin el cifrado": {DEK: full.DEK, KEKID: full.KEKID},
		"sin la DEK":     {Enc: full.Enc, KEKID: full.KEKID},
		"sin el kek_id":  {Enc: full.Enc, DEK: full.DEK},
	}
}

// caseCloseWithTextOnce: sobre la ventana viva que nadie tocó desde que se leyó, la llamada la
// pasa a `pending` Y le escribe el sobre ENTERO, y devuelve true UNA sola vez. Un segundo intento
// con el mismo `seen` devuelve (false, nil) y no pisa el sobre ni toca la fila.
func caseCloseWithTextOnce(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one", "media.one")
	before := onlyRow(t, m, k)
	seen := seenOf(t, m, k)
	if seen.ID != before.ID {
		t.Fatalf("ListAggregating dio el id %q, quería el de la fila (%q)", seen.ID, before.ID)
	}
	m.Advance(t)

	ctx := context.Background()
	if ok, err := m.Store.CloseWithSourceText(ctx, seen, envelope("first")); err != nil || !ok {
		t.Fatalf("CloseWithSourceText = (%v, %v), quería (true, nil)", ok, err)
	}
	want := before
	want.Status = intake.StatusPending
	want.SourceText = envelope("first")
	closed := onlyRow(t, m, k)
	requireWrittenRow(t, "la ventana cerrada con su sobre", closed, want)

	m.Advance(t)
	if ok, err := m.Store.CloseWithSourceText(ctx, seen, envelope("second")); err != nil || ok {
		t.Errorf("segundo CloseWithSourceText = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana tras el segundo intento", onlyRow(t, m, k), closed)
	w.requireUntouched(t, m)
}

// caseCloseWithTextStaleRead es LA CARRERA que la operación existe para cerrar: entre que el
// barrido leyó la ventana y fue a cerrarla entró otro mensaje. El sobre que trae se compuso sin
// él, así que NO cierra: (false, nil) y la fila idéntica —viva, sin sobre y con sus dos
// referencias—. Con la ventana vuelta a leer sí cierra, y lo que cierra trae los dos mensajes.
func caseCloseWithTextStaleRead(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	stale := seenOf(t, m, k)
	m.Advance(t)
	open(t, m, k, firstMessageTS.Add(time.Minute), "wamid.two")
	before := onlyRow(t, m, k)
	m.Advance(t)

	ctx := context.Background()
	if ok, err := m.Store.CloseWithSourceText(ctx, stale, envelope("stale")); err != nil || ok {
		t.Errorf("CloseWithSourceText con una lectura anterior al último mensaje = (%v, %v), quería (false, nil)", ok, err)
	}
	after := onlyRow(t, m, k)
	requireSameRow(t, "la ventana que cambió después de leerla", after, before)
	if after.Status != intake.StatusAggregating || !after.SourceText.Empty() {
		t.Errorf("la ventana que cambió quedó (status %q, sobre %+v), quería aggregating y sin sobre", after.Status, after.SourceText)
	}

	fresh := seenOf(t, m, k)
	if fresh.ID != stale.ID || !fresh.LastActivity.After(stale.LastActivity) {
		t.Fatalf("la ventana releída = (id %q, LastActivity %v), quería la misma (%q) con una marca posterior a %v",
			fresh.ID, fresh.LastActivity, stale.ID, stale.LastActivity)
	}
	m.Advance(t)
	if ok, err := m.Store.CloseWithSourceText(ctx, fresh, envelope("fresh")); err != nil || !ok {
		t.Fatalf("CloseWithSourceText con la ventana releída = (%v, %v), quería (true, nil)", ok, err)
	}
	want := before
	want.Status = intake.StatusPending
	want.SourceText = envelope("fresh")
	closed := onlyRow(t, m, k)
	requireWrittenRow(t, "la ventana cerrada tras releerla", closed, want)
	if !slices.Equal(closed.SourceRefs, []string{"wamid.one", "wamid.two"}) {
		t.Errorf("source_refs de la ventana cerrada = %v, quería los dos mensajes [wamid.one wamid.two]", closed.SourceRefs)
	}
	w.requireUntouched(t, m)
}

// caseCloseWithTextEmptyEnvelope: el sobre VACÍO ENTERO es el hilo sin mensajes, y es legítimo:
// la ventana cierra igual —true, `pending`— y sus tres columnas quedan vacías. No es un sobre a
// medias.
func caseCloseWithTextEmptyEnvelope(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	before := onlyRow(t, m, k)
	seen := seenOf(t, m, k)
	m.Advance(t)

	if ok, err := m.Store.CloseWithSourceText(context.Background(), seen, intake.SourceText{}); err != nil || !ok {
		t.Fatalf("CloseWithSourceText con el sobre vacío = (%v, %v), quería (true, nil)", ok, err)
	}
	want := before
	want.Status = intake.StatusPending
	closed := onlyRow(t, m, k)
	requireWrittenRow(t, "la ventana cerrada sin sobre", closed, want)
	if !closed.SourceText.Empty() {
		t.Errorf("el sobre de la ventana cerrada = %+v, quería las tres columnas vacías", closed.SourceText)
	}
	w.requireUntouched(t, m)
}

// caseCloseWithTextHalfEnvelope: COMPLETO O VACÍO ENTERO. Un sobre al que le falta una de sus
// tres piezas se rechaza con error ANTES de escribir: ni cierra la ventana ni deja media fila. La
// ventana sigue viva, como estaba.
func caseCloseWithTextHalfEnvelope(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	seen := seenOf(t, m, k)
	before := queueSnapshot(t, m)
	m.Advance(t)

	for name, env := range halfEnvelopes() {
		ok, err := m.Store.CloseWithSourceText(context.Background(), seen, env)
		if err == nil || ok {
			t.Errorf("CloseWithSourceText con un sobre %s = (%v, %v), quería (false, error)", name, ok, err)
		}
		requireSameQueue(t, m, "cerrar con un sobre "+name, before, 2)
	}
}

// caseCloseWithTextIncompleteWindow: con la clave a medias, o sin id, no se cierra nada —tampoco
// la ventana viva de la clave buena—: (false, error) con su texto, y la tabla intacta. LA
// VENTANA SE MIRA ANTES QUE EL SOBRE: con las dos cosas mal, el error es el de la ventana.
func caseCloseWithTextIncompleteWindow(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	seen := seenOf(t, m, k)
	before := queueSnapshot(t, m)
	m.Advance(t)

	// Cada una conserva el id y la marca de la ventana viva: es lo que haría que una
	// implementación sin la validación la cerrara igualmente.
	broken := map[string]intake.OpenJob{}
	for _, c := range incompleteKeysOf(k) {
		job := seen
		job.Key = c.key
		broken["la clave "+c.name] = job
	}
	noID := seen
	noID.ID = ""
	broken["el id vacío"] = noID

	envelopes := map[string]intake.SourceText{"un sobre completo": envelope("stray"), "un sobre vacío": {}}
	for name, env := range halfEnvelopes() {
		envelopes["un sobre "+name] = env
	}
	for what, job := range broken {
		for name, env := range envelopes {
			ok, err := m.Store.CloseWithSourceText(context.Background(), job, env)
			if ok || err == nil || err.Error() != incompleteWindowOnCloseWithText {
				t.Errorf("CloseWithSourceText con %s y %s = (%v, %v), quería (false, %q)", what, name, ok, err, incompleteWindowOnCloseWithText)
			}
		}
		requireSameQueue(t, m, "cerrar con el literal y "+what, before, 2)
	}
}

// caseCloseWithTextNotLive: lo que ya no es una ventana viva no se cierra. Una ventana que cerró
// otro (CloseWindow), una fila en cualquier estado que no sea `aggregating` —aunque su marca sea
// EXACTAMENTE la que se trae— y un id que no existe: (false, nil) y nada tocado. Idempotente,
// como CloseWindow.
func caseCloseWithTextNotLive(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	ctx := context.Background()

	// 1 · La cerró otro entre la lectura y la llamada.
	open(t, m, k, firstMessageTS, "wamid.one")
	seen := seenOf(t, m, k)
	m.Advance(t)
	closeLive(t, m, k)
	closed := onlyRow(t, m, k)
	m.Advance(t)
	if ok, err := m.Store.CloseWithSourceText(ctx, seen, envelope("late")); err != nil || ok {
		t.Errorf("CloseWithSourceText sobre una ventana ya cerrada = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana que ya estaba cerrada", onlyRow(t, m, k), closed)

	// 2 · La marca coincide, el estado no: lo único que la frena es la guarda de estado. Se
	// siembra, en el pasado y en segundos enteros, porque por el puerto no se llega a una fila
	// cerrada con la marca que se leyó.
	past := w.row.CreatedAt.Truncate(time.Second).Add(-10 * time.Minute)
	for _, status := range []string{intake.StatusPending, intake.StatusProcessing, intake.StatusDone, intake.StatusFailed} {
		key := newKey(m.TenantA)
		id := m.Seed(t, Row{
			Key: key, Status: status, MessageTS: firstMessageTS, SourceRefs: []string{"wamid.seeded"},
			CreatedAt: past.Add(-time.Minute), UpdatedAt: past, NextAttemptAt: past,
		})
		if id == "" {
			t.Fatal("QueueMontaje.Seed devolvió un id vacío")
		}
		before := onlyRow(t, m, key)
		job := intake.OpenJob{ID: id, Key: key, LastActivity: before.UpdatedAt, CreatedAt: before.CreatedAt}
		if ok, err := m.Store.CloseWithSourceText(ctx, job, envelope("wrong-status")); err != nil || ok {
			t.Errorf("CloseWithSourceText sobre una fila %s con la marca exacta = (%v, %v), quería (false, nil)", status, ok, err)
		}
		requireSameRow(t, "la fila "+status+" con la marca exacta", onlyRow(t, m, key), before)
	}

	// 3 · Un id que no existe, con la clave y la marca de una ventana que SÍ está viva: se
	// identifica por id, no por la tupla.
	neighbour := newKey(m.TenantA)
	open(t, m, neighbour, firstMessageTS, "wamid.neighbour")
	live := onlyRow(t, m, neighbour)
	unknown := seenOf(t, m, neighbour)
	unknown.ID = uuid.NewString()
	m.Advance(t)
	if ok, err := m.Store.CloseWithSourceText(ctx, unknown, envelope("nobody")); err != nil || ok {
		t.Errorf("CloseWithSourceText con un id que no existe = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana viva cuya clave y marca traía el id desconocido", onlyRow(t, m, neighbour), live)
	w.requireUntouched(t, m)
}

// caseCloseWithTextLeavesOlderPending: una tupla puede tener ventanas cerradas de antes (el
// índice único solo cubre las vivas), y una de ellas puede haberse quedado SIN sobre. Cerrar la
// viva con su literal escribe en la viva y solo en ella: la `pending` vieja de la MISMA tupla no
// recibe el texto de otra ventana, ni la ventana viva de la tupla vecina se cierra.
func caseCloseWithTextLeavesOlderPending(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	past := w.row.CreatedAt.Truncate(time.Second).Add(-10 * time.Minute)
	if id := m.Seed(t, Row{
		Key: k, Status: intake.StatusPending, MessageTS: firstMessageTS, SourceRefs: []string{"wamid.old"},
		CreatedAt: past.Add(-time.Minute), UpdatedAt: past, NextAttemptAt: past,
	}); id == "" {
		t.Fatal("QueueMontaje.Seed devolvió un id vacío")
	}
	old := rowWithRef(t, m, k, "wamid.old")
	neighbour := k
	neighbour.ContactID = contactB
	open(t, m, neighbour, firstMessageTS, "wamid.neighbour")
	neighbourRow := onlyRow(t, m, neighbour)
	open(t, m, k, firstMessageTS.Add(time.Hour), "wamid.now")
	before := rowWithRef(t, m, k, "wamid.now")
	seen := seenOf(t, m, k)
	m.Advance(t)

	if ok, err := m.Store.CloseWithSourceText(context.Background(), seen, envelope("now")); err != nil || !ok {
		t.Fatalf("CloseWithSourceText = (%v, %v), quería (true, nil)", ok, err)
	}
	want := before
	want.Status = intake.StatusPending
	want.SourceText = envelope("now")
	requireWrittenRow(t, "la ventana viva, cerrada con su sobre", rowWithRef(t, m, k, "wamid.now"), want)
	requireSameRow(t, "la ventana vieja de la misma tupla, cerrada y sin sobre", rowWithRef(t, m, k, "wamid.old"), old)
	requireSameRow(t, "la ventana viva de la tupla vecina", onlyRow(t, m, neighbour), neighbourRow)
	w.requireUntouched(t, m)
}
