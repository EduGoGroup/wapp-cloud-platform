package intakehelpertest

import (
	"context"
	"testing"
	"time"

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

// casePutCrossedMarks: «la última cerrada» es la de `updated_at` más reciente, NO la creada más
// tarde; y entre dos de la misma marca, la creada más tarde. Son las dos claves de la subconsulta
// (`ORDER BY updated_at DESC, created_at DESC`), en ese orden.
//
// 🔴 Este caso fija la LETRA de la sentencia heredada, no una ruta viva de la cola: por
// intake.JobStore las marcas no se cruzan (una ventana solo se abre con la anterior ya cerrada, y
// a una `pending` que no es la última nada le mueve la marca), y por eso se SIEMBRA. Donde sí se
// cruzan es en la tabla, por la máquina: Release y Retry devuelven a `pending` un job viejo con
// `updated_at = now()`. Si esa elección es la que se quiere ahí no lo decide este caso.
func casePutCrossedMarks(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	// Todo en el pasado del reloj de la implementación, en segundos enteros: así el
	// `updated_at` de la escritura es estrictamente posterior al sembrado.
	base := w.row.CreatedAt.Truncate(time.Second)
	seed := func(key intake.WindowKey, ref string, created, updated time.Duration) {
		t.Helper()
		id := m.Seed(t, Row{
			Key: key, Status: intake.StatusPending, MessageTS: firstMessageTS, SourceRefs: []string{ref},
			CreatedAt: base.Add(created), UpdatedAt: base.Add(updated), NextAttemptAt: base.Add(created),
		})
		if id == "" {
			t.Fatal("QueueMontaje.Seed devolvió un id vacío")
		}
	}
	// Marcas cruzadas: la creada ANTES es la tocada DESPUÉS.
	seed(k, "wamid.created-first", -30*time.Minute, -10*time.Minute)
	seed(k, "wamid.created-last", -20*time.Minute, -15*time.Minute)
	// Empate en `updated_at` (otra tupla): desempata la creación.
	tie := newKey(m.TenantA)
	seed(tie, "wamid.tie-older", -30*time.Minute, -10*time.Minute)
	seed(tie, "wamid.tie-newer", -20*time.Minute, -10*time.Minute)

	for _, c := range []struct {
		what          string
		key           intake.WindowKey
		winner, loser string
	}{
		{"marcas cruzadas", k, "wamid.created-first", "wamid.created-last"},
		{"empate de updated_at", tie, "wamid.tie-newer", "wamid.tie-older"},
	} {
		want, loser := rowWithRef(t, m, c.key, c.winner), rowWithRef(t, m, c.key, c.loser)
		if ok, err := m.Store.PutSourceText(context.Background(), c.key, envelope("crossed")); err != nil || !ok {
			t.Fatalf("%s: PutSourceText = (%v, %v), quería (true, nil)", c.what, ok, err)
		}
		want.SourceText = envelope("crossed")
		requireWrittenRow(t, c.what+": la ventana "+c.winner, rowWithRef(t, m, c.key, c.winner), want)
		requireSameRow(t, c.what+": la ventana "+c.loser, rowWithRef(t, m, c.key, c.loser), loser)
	}
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
