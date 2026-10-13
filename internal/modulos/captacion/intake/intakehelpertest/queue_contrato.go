package intakehelpertest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// caseOpenNewWindow: el primer entrante de una tupla abre UNA ventana `aggregating` con el
// instante de su mensaje, sus referencias y el sobre vacío.
func caseOpenNewWindow(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one", "media.one")

	got := onlyRow(t, m, k)
	if got.ID == "" {
		t.Error("la ventana recién abierta no tiene id")
	}
	if got.CreatedAt.IsZero() || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at = %v y updated_at = %v: quería las dos puestas e iguales al abrir", got.CreatedAt, got.UpdatedAt)
	}
	if got.CreatedAt.Equal(firstMessageTS) {
		t.Error("created_at es el instante del mensaje: tiene que ser el reloj de la implementación, no el del cliente")
	}
	want := Row{
		ID: got.ID, Key: k, Status: intake.StatusAggregating, MessageTS: firstMessageTS,
		SourceRefs: []string{"wamid.one", "media.one"},
		CreatedAt:  got.CreatedAt, NextAttemptAt: got.NextAttemptAt,
	}
	for _, d := range diffRow(got, want) {
		t.Errorf("la ventana recién abierta: %s", d)
	}
	w.requireUntouched(t, m)
}

// caseAppendToLiveWindow: el segundo entrante de la misma tupla NO abre otra fila. Sus
// referencias se concatenan PLANAS y en orden (ni sustituyen ni se anidan), `message_ts` sigue
// siendo el del PRIMER mensaje, `created_at` no se mueve y `updated_at` sí: es el ancla del
// silencio.
func caseAppendToLiveWindow(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	// Dos referencias en el primero (el mensaje con adjunto): es lo que distingue el array
	// plano del anidado.
	open(t, m, k, firstMessageTS, "wamid.one", "media.one")
	before := onlyRow(t, m, k)
	m.Advance(t)
	open(t, m, k, firstMessageTS.Add(time.Minute), "wamid.three")

	want := before
	want.SourceRefs = []string{"wamid.one", "media.one", "wamid.three"}
	requireWrittenRow(t, "la ventana ampliada", onlyRow(t, m, k), want)
	w.requireUntouched(t, m)
}

// caseOpenWithoutRefs: un entrante sin referencias ni instante de mensaje es legítimo. Abre la
// ventana con el array vacío y `message_ts` sin valor; ampliarla sin referencias no añade nada
// y no le pone un `message_ts` que no tenía.
func caseOpenWithoutRefs(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, time.Time{})
	first := onlyRow(t, m, k)
	if first.Status != intake.StatusAggregating || len(first.SourceRefs) != 0 || !first.MessageTS.IsZero() {
		t.Errorf("ventana abierta sin referencias = (status %q, refs %v, message_ts %v), quería (aggregating, vacío, sin valor)",
			first.Status, first.SourceRefs, first.MessageTS)
	}
	m.Advance(t)
	open(t, m, k, firstMessageTS)
	requireWrittenRow(t, "la ventana ampliada sin referencias", onlyRow(t, m, k), first)
	w.requireUntouched(t, m)
}

// caseReopenAfterClose: cerrar una ventana LIBERA la tupla. El siguiente entrante abre una
// ventana nueva, solo con lo suyo, y la cerrada no se amplía ni se toca.
func caseReopenAfterClose(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	closeLive(t, m, k)
	closed := onlyRow(t, m, k)
	m.Advance(t)
	second := firstMessageTS.Add(time.Hour)
	open(t, m, k, second, "wamid.two")

	if rows := rowsOf(t, m, k); len(rows) != 2 {
		t.Fatalf("tras reabrir, la tupla tiene %d filas, quería 2: %+v", len(rows), rows)
	}
	requireSameRow(t, "la ventana ya cerrada", rowWithRef(t, m, k, "wamid.one"), closed)
	fresh := rowWithRef(t, m, k, "wamid.two")
	if fresh.ID == closed.ID {
		t.Error("la ventana nueva tiene el id de la cerrada")
	}
	if fresh.Status != intake.StatusAggregating || !slices.Equal(fresh.SourceRefs, []string{"wamid.two"}) || !fresh.MessageTS.Equal(second) {
		t.Errorf("ventana nueva = (status %q, refs %v, message_ts %v), quería (aggregating, [wamid.two], %v)",
			fresh.Status, fresh.SourceRefs, fresh.MessageTS, second)
	}
	if !fresh.CreatedAt.After(closed.CreatedAt) {
		t.Errorf("created_at de la ventana nueva = %v, quería uno posterior al de la cerrada (%v)", fresh.CreatedAt, closed.CreatedAt)
	}
	w.requireUntouched(t, m)
}

// caseKeyIsTheFourColumns: la ventana es la tupla ENTERA. Cambiar una sola de las cuatro piezas
// —tenant, sesión, contacto o evento— es otra ventana, y un entrante de una no amplía otra.
func caseKeyIsTheFourColumns(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	otherTenant, otherSession, otherContact, otherEvent := k, k, k, k
	otherTenant.TenantID = m.TenantB
	otherSession.SessionID = sessionB
	otherContact.ContactID = contactB
	otherEvent.EventID = newKey(m.TenantA).EventID
	keys := []intake.WindowKey{k, otherTenant, otherSession, otherContact, otherEvent}
	for i, key := range keys {
		open(t, m, key, firstMessageTS, "wamid.first")
		if i == 0 {
			continue
		}
		if rows := rowsOf(t, m, key); len(rows) != 1 {
			t.Fatalf("la clave %+v tiene %d filas, quería su propia ventana", key, len(rows))
		}
	}
	before := make([]Row, len(keys))
	for i, key := range keys {
		before[i] = onlyRow(t, m, key)
	}
	m.Advance(t)
	open(t, m, k, firstMessageTS, "wamid.second")
	for i, key := range keys[1:] {
		requireSameRow(t, "la ventana de otra tupla", onlyRow(t, m, key), before[i+1])
	}
	if got := onlyRow(t, m, k); !slices.Equal(got.SourceRefs, []string{"wamid.first", "wamid.second"}) {
		t.Errorf("source_refs de la ventana ampliada = %v, quería [wamid.first wamid.second]", got.SourceRefs)
	}
}

// caseCloseOnce: cerrar la ventana viva la pasa a `pending` y devuelve true UNA sola vez. El
// segundo cierre devuelve (false, nil) y NO TOCA LA FILA: de ese guard cuelga que el flush por
// intent y el flush por ventana no produzcan dos jobs del mismo pedido.
func caseCloseOnce(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	open(t, m, k, firstMessageTS, "wamid.one")
	before := onlyRow(t, m, k)
	m.Advance(t)
	closeLive(t, m, k)

	want := before
	want.Status = intake.StatusPending
	closed := onlyRow(t, m, k)
	requireWrittenRow(t, "la ventana recién cerrada", closed, want)

	m.Advance(t)
	if ok, err := m.Store.CloseWindow(context.Background(), k); err != nil || ok {
		t.Errorf("segundo CloseWindow = (%v, %v), quería (false, nil)", ok, err)
	}
	requireSameRow(t, "la ventana tras el segundo cierre", onlyRow(t, m, k), closed)
	w.requireUntouched(t, m)
}

// caseCloseWithoutLiveWindow: cerrar una tupla que nunca tuvo ventana es (false, nil) y no crea
// ni toca nada; las ventanas vivas de las tuplas vecinas siguen abiertas.
func caseCloseWithoutLiveWindow(t *testing.T, m QueueMontaje) {
	k := newKey(m.TenantA)
	w := seedQueueWitness(t, m, k)
	neighbour := k
	neighbour.ContactID = contactB
	open(t, m, neighbour, firstMessageTS, "wamid.neighbour")
	before := onlyRow(t, m, neighbour)
	m.Advance(t)

	if ok, err := m.Store.CloseWindow(context.Background(), k); err != nil || ok {
		t.Errorf("CloseWindow de una tupla sin ventana = (%v, %v), quería (false, nil)", ok, err)
	}
	if rows := rowsOf(t, m, k); len(rows) != 0 {
		t.Errorf("cerrar una tupla sin ventana dejó %d filas suyas: %+v", len(rows), rows)
	}
	requireSameRow(t, "la ventana viva de la tupla vecina", onlyRow(t, m, neighbour), before)
	w.requireUntouched(t, m)
}

// caseListEmpty: sin ventanas vivas, la lista viene vacía y sin error.
func caseListEmpty(t *testing.T, m QueueMontaje) {
	got, err := m.Store.ListAggregating(context.Background(), 10)
	if err != nil || len(got) != 0 {
		t.Errorf("ListAggregating sobre una tabla vacía = (%+v, %v), quería (vacío, nil)", got, err)
	}
}

// caseListLiveWindows: la lista trae SOLO las ventanas `aggregating`, las más antiguas primero,
// y de cada una las DOS anclas del reloj de la implementación: LastActivity es su `updated_at`
// (la mueve cada entrante) y CreatedAt su `created_at` (no la mueve nadie). Ninguna de las dos
// es `message_ts`. Listar no escribe.
func caseListLiveWindows(t *testing.T, m QueueMontaje) {
	oldest, closed, newest := newKey(m.TenantA), newKey(m.TenantA), newKey(m.TenantB)
	for _, k := range []intake.WindowKey{oldest, closed, newest} {
		open(t, m, k, firstMessageTS, "wamid.first")
		m.Advance(t)
	}
	closeLive(t, m, closed)
	m.Advance(t)
	// Un segundo entrante sobre la más antigua: mueve su LastActivity y no su sitio en la lista.
	open(t, m, oldest, firstMessageTS.Add(time.Minute), "wamid.second")
	rows := map[intake.WindowKey]Row{oldest: onlyRow(t, m, oldest), newest: onlyRow(t, m, newest)}
	closedRow := onlyRow(t, m, closed)
	m.Advance(t)

	got, err := m.Store.ListAggregating(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAggregating: error inesperado %v", err)
	}
	if len(got) != 2 || got[0].Key != oldest || got[1].Key != newest {
		t.Fatalf("ListAggregating = %+v, quería las dos ventanas vivas, la más antigua primero", got)
	}
	for _, job := range got {
		row := rows[job.Key]
		if job.ID != row.ID {
			t.Errorf("OpenJob.ID = %q, quería %q", job.ID, row.ID)
		}
		if !job.LastActivity.Equal(row.UpdatedAt) || !job.CreatedAt.Equal(row.CreatedAt) {
			t.Errorf("anclas de %s = (LastActivity %v, CreatedAt %v), quería (updated_at %v, created_at %v)",
				job.ID, job.LastActivity, job.CreatedAt, row.UpdatedAt, row.CreatedAt)
		}
		if job.LastActivity.Equal(row.MessageTS) || job.CreatedAt.Equal(row.MessageTS) {
			t.Errorf("un ancla de %s es message_ts (%v): los plazos se medirían contra el reloj del cliente", job.ID, row.MessageTS)
		}
	}
	if !got[0].LastActivity.After(got[0].CreatedAt) {
		t.Errorf("la ventana ampliada trae LastActivity %v, quería una posterior a su CreatedAt %v", got[0].LastActivity, got[0].CreatedAt)
	}
	requireSameRow(t, "listar: la ventana viva más antigua", onlyRow(t, m, oldest), rows[oldest])
	requireSameRow(t, "listar: la ventana viva más nueva", onlyRow(t, m, newest), rows[newest])
	requireSameRow(t, "listar: la ventana cerrada", onlyRow(t, m, closed), closedRow)
}

// caseListLimit: el límite recorta por la cola —las que llevan más rato esperando salen igual—
// y un límite cero o negativo es una lista vacía, sin error.
func caseListLimit(t *testing.T, m QueueMontaje) {
	keys := []intake.WindowKey{newKey(m.TenantA), newKey(m.TenantB), newKey(m.TenantA)}
	for _, k := range keys {
		open(t, m, k, firstMessageTS, "wamid.first")
		m.Advance(t)
	}
	ctx := context.Background()
	for limit, want := range map[int][]intake.WindowKey{1: keys[:1], 2: keys[:2], 3: keys, 50: keys} {
		got, err := m.Store.ListAggregating(ctx, limit)
		if err != nil {
			t.Fatalf("ListAggregating(%d): error inesperado %v", limit, err)
		}
		gotKeys := make([]intake.WindowKey, 0, len(got))
		for _, job := range got {
			gotKeys = append(gotKeys, job.Key)
		}
		if !slices.Equal(gotKeys, want) {
			t.Errorf("ListAggregating(%d) = %v, quería %v (las más antiguas primero)", limit, gotKeys, want)
		}
	}
	for _, limit := range []int{0, -1} {
		if got, err := m.Store.ListAggregating(ctx, limit); err != nil || len(got) != 0 {
			t.Errorf("ListAggregating(%d) = (%+v, %v), quería (vacío, nil)", limit, got, err)
		}
	}
}
