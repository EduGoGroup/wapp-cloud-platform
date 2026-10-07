package degradationhelpertest

// Los casos de Save: el dedupe por (tenant, motivo, vía, inicio de ventana), `creado`, qué toca
// y qué no toca el colapso, la ventana siguiente y la concurrencia (R4.5.c).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// caseSaveFirstFailureCreates: el primer fallo de una ventana hace NACER el aviso: creado=true y
// una fila con un fallo, sin leer, cuyo nacimiento es el instante del fallo —el mismo que su
// último visto—, no el reloj de quien escribe.
func caseSaveFirstFailureCreates(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonOllamaDown, degradation.ViaLocal, baseWindow, baseWindow.Add(3*time.Minute))
	mustCreate(t, m, n)
	row := requireOnlyRow(t, m, tenant, born(n))
	if !row.CreatedAt.Equal(row.LastSeenAt) {
		t.Errorf("el aviso nació en %s y se vio por última vez en %s: los dos son el instante del primer fallo", row.CreatedAt, row.LastSeenAt)
	}
}

// caseSaveIgnoresStoreOwnedFields: de lo que trae el aviso, el id, el contador, la lectura y el
// nacimiento NO se escriben: los decide el store. Ni al crear ni al colapsar.
func caseSaveIgnoresStoreOwnedFields(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, baseWindow.Add(time.Minute))
	loaded := n
	loaded.ID = "no-lo-decide-quien-escribe"
	loaded.Occurrences = 99
	loaded.ReadAt = readInstant
	loaded.CreatedAt = baseWindow.Add(-24 * time.Hour)

	mustCreate(t, m, loaded)
	first := requireOnlyRow(t, m, tenant, born(n))

	loaded.LastSeenAt = baseWindow.Add(2 * time.Minute)
	mustCollapse(t, m, loaded)
	want := born(n)
	want.ID, want.Occurrences, want.LastSeenAt = first.ID, 2, loaded.LastSeenAt
	requireOnlyRow(t, m, tenant, want)
}

// caseSaveSameWindowCollapses (R4.5.c, REQ-38): veinticinco fallos sostenidos dentro de la misma
// ventana son UN aviso. Solo el primero dice `creado`; el aviso cuenta los veinticinco, conserva
// su id y su nacimiento y adelanta su último visto.
func caseSaveSameWindowCollapses(t *testing.T, m Montaje) {
	const failures = 25
	tenant := seedTenant(t, m)
	at := func(i int) time.Time { return baseWindow.Add(time.Duration(i) * 30 * time.Second) } // 25 × 30 s caben en la ventana

	first := failure(tenant, degradation.ReasonOllamaDown, degradation.ViaLocal, baseWindow, at(0))
	mustCreate(t, m, first)
	id := requireOnlyRow(t, m, tenant, born(first)).ID

	for i := 1; i < failures; i++ {
		mustCollapse(t, m, failure(tenant, degradation.ReasonOllamaDown, degradation.ViaLocal, baseWindow, at(i)))
	}
	want := born(first)
	want.ID, want.Occurrences, want.LastSeenAt = id, failures, at(failures-1)
	requireOnlyRow(t, m, tenant, want)
}

// caseSaveCollapseTouchesOnlyTwo (hallazgo 35): el colapso sube el contador y adelanta el último
// visto, y NADA más. El fin de la ventana no se pisa aunque el fallo nuevo traiga otro —cambiar
// el tamaño de la ventana no reescribe los avisos ya nacidos—, el nacimiento tampoco, y un aviso
// ya leído sigue leído.
func caseSaveCollapseTouchesOnlyTwo(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonBreakerOpen, degradation.ViaLocal, baseWindow, baseWindow.Add(time.Minute))
	mustCreate(t, m, n)
	created := requireOnlyRow(t, m, tenant, born(n))
	m.MarkRead(t, tenant, created.ID, readInstant)

	wider := n
	wider.WindowEnd = n.WindowStart.Add(time.Hour) // misma clave, otra política de ventana
	wider.LastSeenAt = baseWindow.Add(9 * time.Minute)
	mustCollapse(t, m, wider)

	want := created
	want.ReadAt = readInstant
	want.Occurrences, want.LastSeenAt = 2, wider.LastSeenAt
	requireOnlyRow(t, m, tenant, want)
}

// caseSaveLastSeenNeverGoesBack: dos réplicas pueden escribir fuera de orden. Un fallo anterior
// al último visto cuenta —sube el contador— pero el aviso no RETROCEDE en el tiempo; y su
// nacimiento tampoco se mueve hacia atrás.
func caseSaveLastSeenNeverGoesBack(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	late := failure(tenant, degradation.ReasonTimeout, degradation.ViaAPI, baseWindow, baseWindow.Add(10*time.Minute))
	mustCreate(t, m, late)
	created := requireOnlyRow(t, m, tenant, born(late))

	early := late
	early.LastSeenAt = baseWindow.Add(2 * time.Minute)
	mustCollapse(t, m, early)
	want := created
	want.Occurrences = 2
	requireOnlyRow(t, m, tenant, want)

	same := late // el mismo instante otra vez: cuenta, y el último visto se queda donde estaba
	mustCollapse(t, m, same)
	want.Occurrences = 3
	requireOnlyRow(t, m, tenant, want)
}

// caseSaveZeroLastSeenUsesWindowEnd: un aviso sin instante de último visto no se rellena con el
// reloj sino con el FIN DE SU VENTANA: la fila queda coherente con el bucket que la produjo y
// dos ejecuciones dan el mismo valor. Vale al nacer —y entonces ese es también su nacimiento— y
// al colapsar.
func caseSaveZeroLastSeenUsesWindowEnd(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonEdgeOffline, degradation.ViaLocal, baseWindow, time.Time{})
	mustCreate(t, m, n)
	want := born(n)
	want.CreatedAt, want.LastSeenAt = n.WindowEnd, n.WindowEnd
	created := requireOnlyRow(t, m, tenant, want)

	// Un fallo de dentro de la ventana es ANTERIOR a su fin: cuenta, pero no retrocede.
	inside := n
	inside.LastSeenAt = baseWindow.Add(5 * time.Minute)
	mustCollapse(t, m, inside)
	mustCollapse(t, m, n) // y otro sin instante
	want = created
	want.Occurrences = 3
	requireOnlyRow(t, m, tenant, want)

	// En otra ventana, un aviso nacido con instante que colapsa uno sin él: salta al fin.
	next := failure(tenant, degradation.ReasonEdgeOffline, degradation.ViaLocal, windowAt(1), windowAt(1).Add(time.Minute))
	mustCreate(t, m, next)
	blank := next
	blank.LastSeenAt = time.Time{}
	mustCollapse(t, m, blank)
	wantNext := born(next)
	wantNext.Occurrences, wantNext.LastSeenAt = 2, next.WindowEnd
	requireRows(t, m, tenant, want, wantNext)
}

// caseSaveNextWindowOpensNewNotice: lo contrario del dedupe, e igual de importante: el aviso es
// POR VENTANA, no eterno. Sin esto, un tenant con el Ollama caído toda la semana recibiría un
// aviso el lunes y nada más. El borde es de la ventana siguiente.
func caseSaveNextWindowOpensNewNotice(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	first := failure(tenant, degradation.ReasonEdgeOffline, degradation.ViaLocal, windowAt(0), windowAt(0).Add(14*time.Minute+59*time.Second))
	second := failure(tenant, degradation.ReasonEdgeOffline, degradation.ViaLocal, windowAt(1), windowAt(1)) // un segundo después
	later := failure(tenant, degradation.ReasonEdgeOffline, degradation.ViaLocal, windowAt(96), windowAt(96).Add(time.Minute))

	mustCreate(t, m, first)
	mustCreate(t, m, second)
	mustCreate(t, m, later)
	requireRows(t, m, tenant, born(first), born(second), born(later))

	// Cada ventana colapsa sobre la suya.
	again := first
	mustCollapse(t, m, again)
	wantFirst := born(first)
	wantFirst.Occurrences = 2
	requireRows(t, m, tenant, wantFirst, born(second), born(later))
}

// caseSaveKeyHasFourColumns: la clave del dedupe son las CUATRO columnas y no solo la ventana:
// otro motivo, otra vía u otro tenant en el mismo minuto son avisos distintos, porque el dueño
// necesita saber que se le cayeron dos cosas.
func caseSaveKeyHasFourColumns(t *testing.T, m Montaje) {
	tenant, other := seedTenant(t, m), seedTenant(t, m)
	at := baseWindow.Add(3 * time.Minute)
	original := failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, baseWindow, at)
	otherReason := failure(tenant, degradation.ReasonOllamaDown, degradation.ViaLocal, baseWindow, at)
	otherVia := failure(tenant, degradation.ReasonTimeout, degradation.ViaAPI, baseWindow, at)
	otherWindow := failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(-1), at)
	otherTenant := failure(other, degradation.ReasonTimeout, degradation.ViaLocal, baseWindow, at)

	for _, n := range []degradation.Notice{original, otherReason, otherVia, otherWindow, otherTenant} {
		mustCreate(t, m, n)
	}
	requireRows(t, m, tenant, born(original), born(otherReason), born(otherVia), born(otherWindow))
	requireRows(t, m, other, born(otherTenant))

	// Y con las cuatro iguales, colapsa: solo sube el suyo.
	mustCollapse(t, m, original)
	collapsed := born(original)
	collapsed.Occurrences = 2
	requireRows(t, m, tenant, collapsed, born(otherReason), born(otherVia), born(otherWindow))
	requireRows(t, m, other, born(otherTenant))
}

// caseSaveSameInstantOtherZoneCollapses: la clave es el INSTANTE del inicio de la ventana, no
// la zona con la que llega escrito: dos procesos con TZ distinta no parten la ventana en dos.
func caseSaveSameInstantOtherZoneCollapses(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonLeaseInvalid, degradation.ViaLocal, baseWindow, baseWindow.Add(time.Minute))
	mustCreate(t, m, n)

	for i, zone := range []*time.Location{time.FixedZone("UTC-5", -5*3600), time.FixedZone("UTC+9", 9*3600)} {
		shifted := n
		shifted.WindowStart, shifted.WindowEnd = n.WindowStart.In(zone), n.WindowEnd.In(zone)
		shifted.LastSeenAt = baseWindow.Add(time.Duration(i+2) * time.Minute).In(zone)
		mustCollapse(t, m, shifted)
	}
	want := born(n)
	want.Occurrences, want.LastSeenAt = 3, baseWindow.Add(3*time.Minute)
	requireOnlyRow(t, m, tenant, want)
}

// caseSaveAcceptsWholeVocabulary: los ocho motivos entran por las dos vías —no hay regla que ate
// motivo y vía: `timeout` es plausible en las dos— y en una misma ventana son dieciséis avisos.
func caseSaveAcceptsWholeVocabulary(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	want := make([]degradation.Notice, 0, 16)
	for i, reason := range allReasons() {
		for j, via := range []string{degradation.ViaLocal, degradation.ViaAPI} {
			n := failure(tenant, reason, via, baseWindow, baseWindow.Add(time.Duration(2*i+j)*time.Second))
			mustCreate(t, m, n)
			want = append(want, born(n))
		}
	}
	if len(want) != 16 {
		t.Fatalf("la suite armó %d avisos, quería 16 (ocho motivos por dos vías)", len(want))
	}
	requireRows(t, m, tenant, want...)
	if got := list(t, m, tenant, degradation.ListFilter{}); len(got) != len(want) {
		t.Errorf("List devolvió %d avisos de la ventana, quería %d", len(got), len(want))
	}
}

// caseConcurrentSaves: veinte caminos escribiendo a la vez el mismo fallo —el equivalente en un
// proceso de dos réplicas del servidor— dejan UNA fila que cuenta los veinte, y exactamente uno
// dice haberla creado. Mientras, las lecturas nunca ven más de un aviso.
func caseConcurrentSaves(t *testing.T, m Montaje) {
	const writers = 20
	tenant := seedTenant(t, m)
	ctx := context.Background()
	at := func(w int) time.Time { return baseWindow.Add(time.Duration(w) * time.Second) }

	created := make([]bool, writers)
	failures := make(chan error, 2*writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var err error
			created[w], err = m.Store.Save(ctx, failure(tenant, degradation.ReasonBreakerOpen, degradation.ViaLocal, baseWindow, at(w)))
			if err != nil {
				failures <- fmt.Errorf("Save del escritor %d: %w", w, err)
			}
		}()
		go func() {
			defer wg.Done()
			got, err := m.Store.List(ctx, tenant, degradation.ListFilter{})
			if err != nil {
				failures <- fmt.Errorf("List en carrera: %w", err)
				return
			}
			if len(got) > 1 || (len(got) == 1 && (got[0].Occurrences < 1 || got[0].Occurrences > writers)) {
				failures <- fmt.Errorf("List en carrera vio %d avisos:%s", len(got), describe(got))
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	first := -1
	for w, c := range created {
		if c && first >= 0 {
			t.Fatalf("los escritores %d y %d dijeron haber creado el aviso: quería exactamente uno", first, w)
		}
		if c {
			first = w
		}
	}
	if first < 0 {
		t.Fatal("ningún escritor dijo haber creado el aviso: quería exactamente uno")
	}
	// Nació con el fallo del escritor que llegó primero, y el último visto es el más tardío.
	want := born(failure(tenant, degradation.ReasonBreakerOpen, degradation.ViaLocal, baseWindow, at(first)))
	want.Occurrences, want.LastSeenAt = writers, at(writers-1)
	requireOnlyRow(t, m, tenant, want)
}
